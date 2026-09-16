//go:build mysql_integration

package session

import (
	"encoding/json"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"my-eino-app/internal/config"
	"testing"
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
	defer store.Delete(id)
	user := schema.UserMessage("中文消息 😀")
	reply := schema.AssistantMessage("", nil)
	reply.Extra = map[string]any{"storage_status": "generating", "storage_turn": "one"}
	history := []*schema.Message{user, reply}
	if err = store.Save(id, history); err != nil {
		t.Fatal(err)
	}
	// 模拟另一进程竞争同一会话，必须拒绝替换正在运行的请求。
	other := schema.AssistantMessage("另一个请求", nil)
	other.Extra = map[string]any{"storage_status": "completed", "storage_turn": "two"}
	if store.Save(id, []*schema.Message{user, other}) == nil {
		t.Fatal("concurrent overwrite accepted")
	}
	reply.Content = "部分回答"
	reply.Extra["storage_status"] = "cancelled"
	if err = store.Save(id, history); err != nil {
		t.Fatal(err)
	}
	if err = store.Save(id, history); err != nil {
		t.Fatal("identical import must be idempotent", err)
	}
	got, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(history)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		t.Fatal("message payload changed")
	}
	if store.Save(id, history[:1]) == nil {
		t.Fatal("truncation accepted")
	}
	got, err = store.Load(id)
	if err != nil || len(got) != 2 {
		t.Fatal("failed transaction changed history", err)
	}
	if err = store.Delete(id); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load(id)
	if err != nil || len(got) != 0 {
		t.Fatal("cascade deletion failed", err)
	}
}
