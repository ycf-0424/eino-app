package memory

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"my-eino-app/internal/config"
	"my-eino-app/internal/session"
)

// TestHookBindsRequestOwner 覆盖 2026-09-17 实测的「一个会话只能问一次」。
//
// 复现条件：auth.enabled=true + memory.identity_mode=multi_user。第一轮对话照常
// 落库，但 Save 的事务钩子用的是配置里的 owner_id（local-owner）去写
// memory_bindings；第二轮带着同一个 session id 进来时，CheckBinding 拿登录 owner
// 比对，直接判归属不符 —— HTTP 500 "memory session scope mismatch"，耗时 13ms，
// 模型还没被调用就失败了。前端表现为「每轮提问都开一个新会话」。
//
// 这里断言三件事：
//  1. 绑定写的是请求 owner，不是配置 owner；
//  2. 同一会话第二轮仍然通过 CheckBinding（修复前必挂）；
//  3. 别的 owner 依旧被拒 —— 修好的隔离不能靠放弃隔离换取。
func TestHookBindsRequestOwner(t *testing.T) {
	if os.Getenv("MEMORY_INTEGRATION") != "1" {
		t.Skip("set MEMORY_INTEGRATION=1 for isolated MySQL tests")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Session.Store = "mysql"
	// multi_user 身份模式要求认证开启（否则没有 owner 来源，隔离会退化成共用一份）。
	// 这里只是让配置校验通过：真正要用的是登录态的 owner 形态。
	cfg.Auth.Enabled = true
	s, err := session.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err = s.DB().Exec("SELECT 1 FROM memory_turns LIMIT 0"); err != nil {
		t.Fatalf("run explicit memory migration first: %v", err)
	}

	configuredOwner := "configured-" + uuid.NewString() // 配置里的 owner_id：多用户模式下必须被忽略
	project := "project-" + uuid.NewString()
	requestOwner := "feishu:ou_" + uuid.NewString() // 登录态身份
	otherOwner := "feishu:ou_" + uuid.NewString()

	cfg.Memory = config.Memory{
		Enabled:      true,
		IdentityMode: "multi_user",
		OwnerID:      configuredOwner,
		ProjectID:    project,
		AutoExtract:  true,
	}
	if err = cfg.ValidateMemory(); err != nil {
		t.Fatal(err)
	}
	r := &Repository{DB: s.DB(), Config: cfg.Memory, Generation: "test-generation", Model: "stub"}
	// 与生产安装方式一致（coordinator.Open）：钩子按本次写入的记忆归属派生 Repository。
	engine := &Engine{Repo: r}
	if err = s.SetTransactionHook(func(ctx context.Context, tx *sql.Tx, id, owner string, msgs []*schema.Message) error {
		return engine.Repo.For(engine.OwnerScope(owner)).Capture(ctx, tx, id, msgs)
	}); err != nil {
		t.Fatal(err)
	}

	id := uuid.NewString()
	t.Cleanup(func() {
		ctx := context.Background()
		db := s.DB()
		db.ExecContext(ctx, "DELETE d FROM memory_decisions d JOIN memory_turns t ON t.turn_id=d.turn_id WHERE t.session_id=?", id)
		db.ExecContext(ctx, "DELETE FROM memory_jobs WHERE owner_id=?", configuredOwner)
		db.ExecContext(ctx, "DELETE FROM memory_turns WHERE session_id=?", id)
		db.ExecContext(ctx, "DELETE FROM memory_bindings WHERE session_id=?", id)
		db.ExecContext(ctx, "DELETE FROM memory_locks WHERE owner_id=?", configuredOwner)
		db.ExecContext(ctx, "DELETE FROM conversations WHERE id=?", id)
	})

	// 逐轮累积历史：实际服务端每轮 Save 的也是「完整历史」，靠前缀逐条比对来
	// 判断这次写入是不是在已有记录之上追加。
	var history []*schema.Message
	round := func(text string) {
		t.Helper()
		turnID := uuid.NewString()
		u := schema.UserMessage(text)
		u.Extra = map[string]any{"memory_turn": turnID}
		a := schema.AssistantMessage("收到", nil)
		a.Extra = map[string]any{"storage_status": "completed", "storage_turn": turnID}
		history = append(history, u, a)
		if err := s.Save(requestOwner, id, history); err != nil {
			t.Fatal(err)
		}
	}

	round("第一轮")
	bound := engine.Repo.For(engine.OwnerScope(requestOwner))
	if err := bound.CheckBinding(context.Background(), id); err != nil {
		t.Fatalf("第一轮后 CheckBinding 应当通过: %v", err)
	}
	var got string
	if err := s.DB().QueryRow("SELECT owner_id FROM memory_bindings WHERE session_id=?", id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != requestOwner {
		t.Fatalf("会话绑定归属应当是请求 owner %q，实际写成 %q（配置 owner 是 %q）", requestOwner, got, configuredOwner)
	}

	// 第二轮：修复前就是在这一步被 500 掉的。
	round("第二轮")
	if err := bound.CheckBinding(context.Background(), id); err != nil {
		t.Fatalf("同一会话第二轮被误判为越权，复现了「只能问一次」: %v", err)
	}
	if err := engine.Repo.For(otherOwner).CheckBinding(context.Background(), id); err == nil {
		t.Fatal("别的 owner 不该通过归属校验")
	}
}
