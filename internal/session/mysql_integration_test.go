//go:build mysql_integration

package session

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"my-eino-app/internal/config"
)

// 两个不同身份，用于验证跨 owner 的读写一律被拒绝。
const (
	ownerA = "feishu:ou_test_owner_a"
	ownerB = "feishu:ou_test_owner_b"
)

// 使用独立测试会话并在结束时删除；不修改已有会话、不依赖 Ollama。
func TestMySQLRoundTripAndConflict(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Session.Store = "mysql"
	store, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := "test-" + uuid.NewString()
	defer store.Delete(ownerA, id)
	user := schema.UserMessage("中文消息 😀")
	reply := schema.AssistantMessage("", nil)
	reply.Extra = map[string]any{"storage_status": "generating", "storage_turn": "one"}
	history := []*schema.Message{user, reply}
	if err = store.Save(ownerA, id, history); err != nil {
		t.Fatal(err)
	}
	// 模拟另一进程竞争同一会话，必须拒绝替换正在运行的请求。
	other := schema.AssistantMessage("另一个请求", nil)
	other.Extra = map[string]any{"storage_status": "completed", "storage_turn": "two"}
	if store.Save(ownerA, id, []*schema.Message{user, other}) == nil {
		t.Fatal("concurrent overwrite accepted")
	}
	reply.Content = "部分回答"
	reply.Extra["storage_status"] = "cancelled"
	if err = store.Save(ownerA, id, history); err != nil {
		t.Fatal(err)
	}
	if err = store.Save(ownerA, id, history); err != nil {
		t.Fatal("identical import must be idempotent", err)
	}
	got, err := store.Load(ownerA, id)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(history)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Fatal("message payload changed")
	}
	if store.Save(ownerA, id, history[:1]) == nil {
		t.Fatal("truncation accepted")
	}
	got, err = store.Load(ownerA, id)
	if err != nil || len(got) != 2 {
		t.Fatal("failed transaction changed history", err)
	}
	if err = store.Delete(ownerA, id); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load(ownerA, id)
	if err != nil || len(got) != 0 {
		t.Fatal("cascade deletion failed", err)
	}
}

// TestMySQLOwnerIsolation 覆盖隔离的三条路径：读、写、删。
// 只做查询过滤（WHERE owner_id=?）不足以拦住写路径——知道 id 就能追加消息，
// 所以三个方法都必须显式比对归属并返回 ErrForeignSession。
func TestMySQLOwnerIsolation(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Session.Store = "mysql"
	store, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := "test-iso-" + uuid.NewString()
	defer store.Delete(ownerA, id)

	history := []*schema.Message{schema.UserMessage("A 的私有内容")}
	if err = store.Save(ownerA, id, history); err != nil {
		t.Fatal(err)
	}

	// 读：B 读 A 的会话必须报归属错误，而不是返回空历史（空历史会被误认为新会话）。
	if _, err = store.Load(ownerB, id); !errors.Is(err, ErrForeignSession) {
		t.Fatalf("Load(other owner) = %v; want ErrForeignSession", err)
	}
	// 写：B 往 A 的会话追加消息必须被拒绝（原 INSERT IGNORE 会静默放过）。
	if err = store.Save(ownerB, id, append(history, schema.UserMessage("B 注入"))); !errors.Is(err, ErrForeignSession) {
		t.Fatalf("Save(other owner) = %v; want ErrForeignSession", err)
	}
	// 删：B 删 A 的会话必须被拒绝，且 A 的数据仍在。
	if err = store.Delete(ownerB, id); !errors.Is(err, ErrForeignSession) {
		t.Fatalf("Delete(other owner) = %v; want ErrForeignSession", err)
	}
	if got, err := store.Load(ownerA, id); err != nil || len(got) != 1 {
		t.Fatalf("owner A history was damaged: len=%d err=%v", len(got), err)
	}

	// 列表按 owner 过滤：A 能看到，B 看不到。
	items, err := store.List(ownerA)
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(items, id) {
		t.Fatal("owner A cannot see its own session")
	}
	items, err = store.List(ownerB)
	if err != nil {
		t.Fatal(err)
	}
	if containsID(items, id) {
		t.Fatal("owner B can see owner A's session")
	}

	// 不存在的 id 删除仍视为成功（接口幂等）。
	if err = store.Delete(ownerA, "test-missing-"+uuid.NewString()); err != nil {
		t.Fatalf("Delete(missing) = %v; want nil", err)
	}
}

func containsID(items []Info, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
