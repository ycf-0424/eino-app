package session

import (
	"errors"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// 文件后端没有 owner 维度，测试统一用空 owner（单用户模式的实际取值）。
const testOwner = ""

func TestSaveAndLoad(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := []*schema.Message{
		schema.UserMessage("你好"),
		schema.AssistantMessage("你好，有什么可以帮你？", nil),
	}
	if err := store.Save(testOwner, "demo-1", want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(testOwner, "demo-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != want[0].Content || got[1].Content != want[1].Content {
		t.Fatalf("loaded history = %#v", got)
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(testOwner, "missing")
	if err != nil || got != nil {
		t.Fatalf("Load() = %#v, %v; want nil, nil", got, err)
	}
}

func TestRejectsUnsafeID(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(testOwner, "../escape", nil); err == nil {
		t.Fatal("expected unsafe session id to be rejected")
	}
}

func TestTrimHistoryKeepsSystemAndRecentMessages(t *testing.T) {
	messages := []*schema.Message{
		schema.SystemMessage("规则"), schema.UserMessage("旧问题"), schema.AssistantMessage("旧回答", nil),
		schema.UserMessage("新问题"), schema.AssistantMessage("新回答", nil),
	}
	got := TrimHistory(messages, 2, 100)
	if len(got) != 3 || got[0].Role != schema.System || got[1].Content != "新问题" || got[2].Content != "新回答" {
		t.Fatalf("unexpected trimmed history: %+v", got)
	}
}

func TestCompactHistoryCreatesSummary(t *testing.T) {
	messages := []*schema.Message{schema.UserMessage("旧问题"), schema.AssistantMessage("旧回答", nil), schema.UserMessage("新问题"), schema.AssistantMessage("新回答", nil)}
	got := CompactHistory(messages, 2, 1000)
	if len(got) != 3 || got[0].Role != schema.System || got[1].Content != "新问题" {
		t.Fatalf("unexpected compact history: %+v", got)
	}
}

func TestListDeleteAndCleanup(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(testOwner, "one", []*schema.Message{schema.UserMessage("1")}); err != nil {
		t.Fatal(err)
	}
	items, err := store.List(testOwner)
	if err != nil || len(items) != 1 || items[0].ID != "one" {
		t.Fatalf("List=%+v,%v", items, err)
	}
	if err := store.Delete(testOwner, "one"); err != nil {
		t.Fatal(err)
	}
	items, err = store.List(testOwner)
	if err != nil || len(items) != 0 {
		t.Fatalf("after delete=%+v,%v", items, err)
	}
}

// TestDeleteMissingIsIdempotent 固定「删除不存在的会话返回 nil」这一契约。
// MySQL 后端要额外区分「不存在」与「归属他人」，两者都不能退化成 500。
func TestDeleteMissingIsIdempotent(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(testOwner, "never-existed"); err != nil {
		t.Fatalf("Delete(missing) = %v; want nil", err)
	}
}

// TestErrForeignSessionIsDistinguishable 防止 ErrForeignSession 的语义被改坏：
// HTTP 层靠 errors.Is 把它映射成 403，一旦不再是可识别的哨兵错误就会退回 400。
func TestErrForeignSessionIsDistinguishable(t *testing.T) {
	wrapped := fmt.Errorf("load: %w", ErrForeignSession)
	if !errors.Is(wrapped, ErrForeignSession) {
		t.Fatal("ErrForeignSession must survive wrapping")
	}
}
