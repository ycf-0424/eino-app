package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// For 按 owner 分层存放：即便 session id 泄露，另一身份的请求也读不到
// 对应的 checkpoint 与审批内容。这是步骤 2.7 的兜底层。
func TestForIsolatesOwners(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ownerA := s.For("feishu:ou_aaa")
	ownerB := s.For("feishu:ou_bbb")

	if err := ownerA.Set(ctx, "session-1", []byte("A 的状态")); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ownerB.Get(ctx, "session-1"); err != nil || ok {
		t.Fatalf("owner B must not read owner A's checkpoint: ok=%v err=%v", ok, err)
	}
	if b, ok, err := ownerA.Get(ctx, "session-1"); err != nil || !ok || string(b) != "A 的状态" {
		t.Fatalf("owner A checkpoint = %q %v %v", b, ok, err)
	}

	// 审批元数据同样按 owner 隔离。
	approval := PendingApproval{TargetID: "tool:1", ToolName: "write_note", Arguments: "{}"}
	if err := ownerA.SaveApproval("session-1", approval); err != nil {
		t.Fatal(err)
	}
	if got, err := ownerB.LoadApproval("session-1"); err != nil || got != nil {
		t.Fatalf("owner B must not read owner A's approval: %#v %v", got, err)
	}
	if got, err := ownerA.LoadApproval("session-1"); err != nil || got == nil || got.TargetID != "tool:1" {
		t.Fatalf("owner A approval = %#v %v", got, err)
	}
	// B 清空自己的目录不能影响 A。
	if err := ownerB.ClearApproval("session-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := ownerA.LoadApproval("session-1"); got == nil {
		t.Fatal("owner A approval was removed by owner B")
	}
}

// 空 owner 必须退回原视图：CLI（cmd/console）走这条路，行为要与改造前完全一致。
func TestForEmptyOwnerKeepsRootView(t *testing.T) {
	root := t.TempDir()
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if s.For("") != s {
		t.Fatal("empty owner must return the receiver, not a sub-directory view")
	}
	if err := s.Set(context.Background(), "cli-1", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "cli-1.checkpoint")); err != nil {
		t.Fatalf("CLI checkpoint must stay in the root directory: %v", err)
	}
}

// ownerSegment 是隔离边界，必须一一对应：只要存在两个不同 owner 映射到同一目录，
// 就等于两个身份共用同一份 checkpoint。冒号在 Windows 上是非法文件名字符，
// 因此不能直接用原值。
func TestOwnerSegmentIsInjectiveAndSafe(t *testing.T) {
	owners := []string{
		"feishu:ou_aaa", "feishu:ou_bbb", "local:11111111-2222-3333-4444-555555555555",
		// 故意构造字符替换后相同的一对，验证哈希后缀把两者分开。
		"feishu:ou_a", "feishu_ou:a",
	}
	seen := map[string]string{}
	for _, owner := range owners {
		segment := ownerSegment(owner)
		if prev, ok := seen[segment]; ok {
			t.Fatalf("collision: %q and %q both map to %q", prev, owner, segment)
		}
		seen[segment] = owner
		for _, r := range segment {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			default:
				t.Fatalf("segment %q contains unsafe rune %q", segment, r)
			}
		}
		if len(filepath.Join(t.TempDir(), segment)) == 0 {
			t.Fatal("segment must be a usable path element")
		}
	}
	// 同一 owner 必须稳定映射到同一段，否则重启后读不到旧 checkpoint。
	if ownerSegment("feishu:ou_aaa") != ownerSegment("feishu:ou_aaa") {
		t.Fatal("ownerSegment must be deterministic")
	}
}

// 派生视图共享同一把锁：两个视图同时写同一个 id 时不能交错写坏 <id>.checkpoint.tmp。
func TestForSharesMutex(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	view := s.For("feishu:ou_aaa")
	if view.mu != s.mu {
		t.Fatal("derived view must share the root mutex")
	}
}
