// auth.go 提供 HTTP 层的认证路由。步骤 2.4。
package server

import (
	"net/http"

	"my-eino-app/internal/auth"
)

// registerAuthRoutes 注册认证相关路由。auth.enabled 关闭时全部不注册，
// 路由不存在（404），与改造前行为一致。
//
// 免认证白名单：/auth/login、/auth/callback、/health（已在 http.go）、
// /health/ready（步骤 5.4 新增时一并放行）。登录入口自身必须在白名单内，
// 否则未登录时根本访问不到。
func (s *Service) registerAuthRoutes(mux *http.ServeMux) {
	if !s.authEnabled() {
		return
	}
	mux.HandleFunc("GET /auth/login", s.handleAuthLogin)
	mux.HandleFunc("GET /auth/callback", s.handleAuthCallback)
	mux.HandleFunc("GET /auth/logout", s.handleAuthLogout)
	// /auth/me 需要登录：未登录时它就是 401，这正是前端判断登录态的信号。
	mux.Handle("GET /auth/me", s.protected(http.HandlerFunc(s.handleAuthMe)))
	// POST /auth/local 在步骤 2.12 落地后按 auth.local.enabled 条件注册。
}

// handleAuthLogin 302 到飞书授权页，并种下 CSRF state Cookie。
// 未配置飞书凭据时返回 400：该场景下 ValidateAuth 已保证自有账号开启，
// 由前端登录页（步骤 2.14）承接。
func (s *Service) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !s.feishuReady() {
		writeJSON(w, http.StatusBadRequest, response{Error: "feishu login is not configured"})
		return
	}
	state := auth.NewState(w)
	http.Redirect(w, r, s.authFeishu.AuthorizeURL(state), http.StatusFound)
}

// handleAuthCallback 授权码换用户信息并签发登录态。
// ⚠️ 授权码 5 分钟有效且只能用一次：先校验 state，再立即兑换，不做其他 I/O。
func (s *Service) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if !auth.VerifyState(r, r.URL.Query().Get("state")) {
		auth.ClearStateCookie(w)
		writeJSON(w, http.StatusBadRequest, response{Error: "state mismatch"})
		return
	}
	auth.ClearStateCookie(w)
	code := r.URL.Query().Get("code")
	if code == "" {
		writeJSON(w, http.StatusBadRequest, response{Error: "missing code"})
		return
	}
	accessToken, err := s.authFeishu.Exchange(r.Context(), code)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, response{Error: err.Error()})
		return
	}
	user, err := s.authFeishu.FetchUser(r.Context(), accessToken)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, response{Error: err.Error()})
		return
	}
	// 用户档案落库失败不阻断登录：登录态在进程内，auth_users 只是档案记录。
	_ = s.authUsers.UpsertFeishu(r.Context(), user)
	token, _, err := s.authSessions.Create(auth.FeishuOwner(user.OpenID), user.Name, user.AvatarURL, "feishu")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, response{Error: err.Error()})
		return
	}
	auth.SetSessionCookie(w, token, s.authSessions.TTL(), s.cfg.Auth.CookieSecure)
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleAuthLogout 注销登录态并清 Cookie，回登录入口。
func (s *Service) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	s.authSessions.Delete(auth.TokenFromCookie(r))
	auth.ClearSessionCookie(w)
	http.Redirect(w, r, "/auth/login", http.StatusFound)
}

// handleAuthMe 返回当前登录身份，供前端显示「名字（来源）」。
func (s *Service) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.authSessions.Get(auth.TokenFromCookie(r))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, response{Error: "not authenticated"})
		return
	}
	writeJSON(w, http.StatusOK, response{Data: map[string]any{
		"owner":      sess.Owner,
		"name":       sess.Name,
		"avatar_url": sess.AvatarURL,
		"provider":   sess.Provider,
	}})
}

// feishuReady 判断飞书凭据是否齐备（三者缺一不可）。
func (s *Service) feishuReady() bool {
	if s.cfg == nil {
		return false
	}
	a := s.cfg.Auth
	return a.AppID != "" && a.AppSecret != "" && a.RedirectURL != ""
}

// authEnabled 判断认证是否启用（NewService 里按 cfg.Auth.Enabled 装配）。
func (s *Service) authEnabled() bool { return s.authMW != nil }

// protected 按需给 handler 包上认证中间件；auth 关闭时原样返回。
//
// 中间件必须包在 mux 内部逐路由，而不是包在最外层 HandlerFunc 之外：
// http.go 的超时包装是最外层且对 /ws 豁免，包在外面会让 /ws 丢掉超时豁免。
// /ws 的认证只能靠 Cookie —— 浏览器 WebSocket API 无法自定义 Header；
// ws.go 的 CheckOrigin（同源校验）继续保留，两者互补。
func (s *Service) protected(h http.Handler) http.Handler {
	if !s.authEnabled() {
		return h
	}
	return s.authMW.Authenticate(h)
}
