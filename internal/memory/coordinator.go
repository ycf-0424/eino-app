package memory

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"

	"my-eino-app/internal/config"
	"my-eino-app/internal/eino"
	"my-eino-app/internal/eino/embedding"
	"my-eino-app/internal/session"
)

func Open(ctx context.Context, cfg *config.Config, sessions *session.Store, model eino.BaseChatModel) (*Engine, error) {
	if !cfg.Memory.Enabled {
		return nil, nil
	}
	if sessions.DB() == nil {
		return nil, fmt.Errorf("memory requires mysql")
	}
	r := &Repository{DB: sessions.DB(), Config: cfg.Memory, Model: cfg.OpenAI.Model}
	if cfg.Memory.ExtractorModel != "inherit_chat" && cfg.Memory.ExtractorModel != "" {
		r.Model = cfg.Memory.ExtractorModel
	}
	r.Generation = Hash(cfg.RAG.Embedding.BaseURL + "|" + cfg.RAG.Embedding.Model + "|" + fmt.Sprint(cfg.RAG.Dimension) + "|" + cfg.Memory.Collection)[:16]
	check, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, table := range []string{"memory_turns", "memory_facts", "memory_jobs", "memory_bindings", "memory_controls", "memory_versions", "memory_sources", "memory_decisions", "memory_locks"} {
		if _, err := r.DB.ExecContext(check, "SELECT 1 FROM "+table+" LIMIT 0"); err != nil {
			return nil, fmt.Errorf("memory tables missing: run cmd/session-migrate: %w", err)
		}
	}
	emb, err := embedding.NewFromConfig(check, cfg.RAG.Embedding)
	if err != nil {
		return nil, err
	}
	index, err := NewMilvusIndex(check, cfg.RAG, cfg.Memory.Collection, emb)
	if err != nil {
		return nil, err
	}
	if _, err = index.vector(check, "memory dimension check"); err != nil {
		index.Close()
		return nil, err
	}
	e := &Engine{Repo: r, Extractor: &ModelExtractor{model, cfg.Memory.MaxCandidates}, Index: index}
	// hook 必须按「本次写入的记忆归属」派生 Repository：它在启动时只安装一次，
	// 闭包里的 r 是配置身份的副本。多用户模式下若直接调用 r.Capture，
	// memory_bindings/memory_turns 会被写成配置里的 owner_id（例如 local-owner），
	// 于是同一会话的第二轮 CheckBinding 立刻判定归属不符并拒绝服务
	// （现象是「每个会话只能问一次」，2026-09-17 实测）。
	if err = sessions.SetTransactionHook(func(ctx context.Context, tx *sql.Tx, id, owner string, msgs []*schema.Message) error {
		return e.Repo.For(e.OwnerScope(owner)).Capture(ctx, tx, id, msgs)
	}); err != nil {
		index.Close()
		return nil, err
	}
	return e, nil
}

// OwnerScope 把「请求身份」归一化成「记忆归属」，是记忆侧唯一允许的换算点。
//
// 两种身份模式必须统一走它，否则同一份数据会被写进两个命名空间：
//
//   - local_single_user：身份来源是配置里的 owner_id，请求侧根本没有登录态
//     （auth.enabled=false 时 OwnerFromContext 返回空串）。若直接拿空串去派生
//     Repository，binding 会记在 "" 下、而校验/读取走配置 owner，第二轮照样被判归属不符。
//   - multi_user：身份来源是登录态，配置 owner_id 被忽略（启动日志会提示）。
//     请求 owner 为空说明上游漏了身份注入，这里原样返回，让归属校验去拒绝，
//     不要静默回退到配置身份 —— 那等于把所有人的记忆合到一个命名空间里。
func (e *Engine) OwnerScope(requestOwner string) string {
	if e.Repo.Config.IdentityMode == "local_single_user" {
		return e.Repo.Config.OwnerID
	}
	return requestOwner
}
