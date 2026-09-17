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

// TestCreateRegistersOwnership 固定「服务端签发会话」的契约：
// POST /sessions 之后 OwnerOf 必须能立刻回答「这个 id 存在且属于谁」，
// 否则后续判断「沿用还是拒绝调用方带来的 id」就没有依据。
func TestCreateRegistersOwnership(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// 未登记时 exists=false。
	if _, exists, err := store.OwnerOf("fresh-id"); err != nil || exists {
		t.Fatalf("OwnerOf(before create) exists=%v err=%v", exists, err)
	}
	if err := store.Create("feishu:ou_signed", "fresh-id"); err != nil {
		t.Fatal(err)
	}
	owner, exists, err := store.OwnerOf("fresh-id")
	if err != nil || !exists {
		t.Fatalf("OwnerOf(after create) exists=%v err=%v", exists, err)
	}
	// 文件后端没有 owner 维度，归属恒为空串。这不是缺口：auth.enabled 时
	// ValidateAuth 强制 session.store=mysql，文件模式只用于单用户/CLI。
	if owner != "" {
		t.Fatalf("file backend must not invent an owner, got %q", owner)
	}
	// 空会话可以被正常加载（前端拿到 id 后会立刻拉取会话）。
	msgs, err := store.Load(testOwner, "fresh-id")
	if err != nil || len(msgs) != 0 {
		t.Fatalf("Load() = %+v, %v; want empty history", msgs, err)
	}
	// 非法 id 必须被拒绝，避免调用方把路径片段当 id 塞进来。
	if _, _, err := store.OwnerOf("../escape"); err == nil {
		t.Fatal("OwnerOf must reject unsafe ids")
	}
}
