// auth.go 提供 HTTP 层的认证路由。步骤 2.4。
package server

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

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
	// POST /auth/local 仅在启用自有账号时注册；未启用则 404，
	// 前端登录页据此不渲染账号入口（步骤 2.14）。
	if s.localLoginEnabled() {
		mux.HandleFunc("POST /auth/local", s.handleAuthLocal)
	}
	// 不做注册路由（D12：仅管理员建号），也不做改密路由（D15：改密走 cmd/user-admin）。
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

// localLoginEnabled 判断自有账号登录是否可用（配置开启且存储已装配）。
func (s *Service) localLoginEnabled() bool {
	return s.localAccounts != nil && s.cfg != nil && s.cfg.Auth.Local.Enabled
}

// localLoginRequest 是 POST /auth/local 的请求体。
type localLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleAuthLocal 处理自有账号登录：校验成功后复用与飞书完全相同的一套
// 登录态签发与 Cookie 写入（步骤 2.3），因此下游的 owner 注入路径只有一条。
//
// 顺序是刻意的：先限流再校验口令。反过来就等于「先花 21 万轮 PBKDF2，
// 再决定要不要拒绝」，爆破者照样能把 CPU 打满。
func (s *Service) handleAuthLocal(w http.ResponseWriter, r *http.Request) {
	if !s.localLoginEnabled() {
		writeJSON(w, http.StatusNotFound, response{Error: "local login is not enabled"})
		return
	}
	var req localLoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, response{Error: err.Error()})
		return
	}

	keys := s.loginRateKeys(r, req.Username)
	for _, key := range keys {
		if ok, retryAfter := s.loginLimiter.Allow(key); !ok {
			// 429 + Retry-After：调用方能算出该等多久，浏览器与脚本都能按规矩退避。
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds()+0.999)))
			writeJSON(w, http.StatusTooManyRequests, response{Error: "登录尝试过于频繁，请稍后再试"})
			return
		}
	}

	sess, err := s.localAccounts.Login(r.Context(), req.Username, req.Password, s.cfg.Auth.Local.MinPasswordLength)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			// 与「账号不存在」「口令错误」「已停用」共用同一响应，不泄露账号状态。
			writeJSON(w, http.StatusUnauthorized, response{Error: "invalid username or password"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, response{Error: err.Error()})
		return
	}
	// 成功即归还令牌：桶里的计数只反映失败尝试，正常登录不该被自己的历史失败拖累。
	for _, key := range keys {
		s.loginLimiter.Refund(key)
	}

	token, _, err := s.authSessions.Create(sess.Owner, sess.Name, sess.AvatarURL, sess.Provider)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, response{Error: err.Error()})
		return
	}
	auth.SetSessionCookie(w, token, s.authSessions.TTL(), s.cfg.Auth.CookieSecure)
	writeJSON(w, http.StatusOK, response{Data: map[string]any{
		"owner":    sess.Owner,
		"name":     sess.Name,
		"provider": sess.Provider,
	}})
}

// loginRateKeys 返回本次登录要计数的桶：IP 与用户名各一个。
//
// 只按 IP 会漏掉内网多机分布式尝试（对方换台机器就是一个新桶）；
// 只按用户名会漏掉批量撞库（每个用户名只试一次）。两者都要有。
// 用户名归一成小写：账号本身大小写敏感，但爆破者会拿大小写变体刷桶。
func (s *Service) loginRateKeys(r *http.Request, username string) []string {
	return []string{"ip:" + clientIP(r), "user:" + strings.ToLower(username)}
}

// clientIP 取限流用的客户端地址。
//
// 只用 RemoteAddr，不采信 X-Forwarded-For：XFF 是客户端可以随便写的头，
// 采信它等于把 IP 维度的限流直接让给攻击者（每次换个假 IP 就绕开了）。
// 将来若部署在反向代理之后，正确做法是在代理层保证 RemoteAddr 可信，
// 而不是在这里无条件读 XFF。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
