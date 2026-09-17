package memory

import (
	"testing"

	"my-eino-app/internal/config"
)

// TestOwnerScope 锁死「请求身份 → 记忆归属」的换算规则。
//
// 两种模式的行为必须被固定下来，因为写错一边就会串号，写错另一边就会
// 让同一个会话的第二轮被判越权（2026-09-17 实测的那条 500）。
func TestOwnerScope(t *testing.T) {
	single := &Engine{Repo: &Repository{Config: config.Memory{
		IdentityMode: "local_single_user",
		OwnerID:      "local-owner",
		ProjectID:    "my-eino-app",
	}}}
	multi := &Engine{Repo: &Repository{Config: config.Memory{
		IdentityMode: "multi_user",
		OwnerID:      "local-owner", // 多用户模式下这一项被忽略
		ProjectID:    "my-eino-app",
	}}}

	cases := []struct {
		name    string
		engine  *Engine
		request string
		want    string
	}{
		// 单用户模式没有登录态，请求侧 owner 恒为空串：必须映射回配置身份，
		// 否则 binding 记在 "" 下、而读写走配置 owner，第二轮必然判归属不符。
		{"single_user/empty", single, "", "local-owner"},
		{"single_user/nonempty", single, "ignored", "local-owner"},
		// 多用户模式身份来自登录态，配置 owner 只作展示，绝不能被当成身份使用。
		{"multi_user/logged_in", multi, "feishu:ou_1", "feishu:ou_1"},
		// 空串原样返回：上游漏注入身份时应该让归属校验去拒绝，
		// 而不是静默回退到配置身份（那等于所有人共用一份记忆）。
		{"multi_user/empty", multi, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.engine.OwnerScope(c.request); got != c.want {
				t.Fatalf("OwnerScope(%q) = %q, want %q", c.request, got, c.want)
			}
		})
	}
}
