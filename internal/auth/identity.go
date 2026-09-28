// Package auth 实现登录认证与请求身份注入。
//
// 身份模型：登录成功后签发 256 位随机 token（Cookie 携带），进程内 map 维护
// token 哈希 → Session 的映射（D11）；owner 一律取 <provider>:<subject> 形式
// —— 飞书写 feishu:<open_id>，本地账号写 local:<uuid>。下游只做等值比较，
// 前缀让排查归属时能一眼判断身份来源，也不排斥将来引入第三种身份。
package auth

import "context"

// ownerKey 是 context 中携带 owner 的键类型；私有结构体避免与其他包冲突。
type ownerKey struct{}

// WithOwner 把已认证的 owner 注入 context。
func WithOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerKey{}, owner)
}

// OwnerFromContext 返回当前登录用户；未认证时返回空串。
func OwnerFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ownerKey{}).(string); ok {
		return v
	}
	return ""
}

type adminKey struct{}

// WithAdmin records the server-verified administrator role in request context.
func WithAdmin(ctx context.Context, isAdmin bool) context.Context {
	return context.WithValue(ctx, adminKey{}, isAdmin)
}

// AdminFromContext returns the server-verified administrator role.
func AdminFromContext(ctx context.Context) bool {
	value, _ := ctx.Value(adminKey{}).(bool)
	return value
}

// FeishuOwner 返回飞书身份的 owner 形式。open_id 为空时返回空串，
// 调用方必须在此之前完成「拿不到用户信息即失败」的判定。
func FeishuOwner(openID string) string {
	if openID == "" {
		return ""
	}
	return "feishu:" + openID
}

// LocalOwner 返回本地账号的 owner 形式。
func LocalOwner(userID string) string {
	if userID == "" {
		return ""
	}
	return "local:" + userID
}
