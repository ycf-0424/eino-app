package chain

import "testing"

func TestRewriteQuery(t *testing.T) {
	if got := RewriteQuery("  星河系统   幸运数字是什么？ "); got != "星河系统 幸运数字是什么" {
		t.Fatalf("unexpected rewritten query: %q", got)
	}
}
