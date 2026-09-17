// auth.go 提供 HTTP 层的认证路由。步骤 2.4。
package server

import (
	"errors"
	"html"
	"net"
	"net/http"
	"strconv"
	"strings"

	"my-eino-app/internal/auth"
	"my-eino-app/internal/eino/observability"
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

// handleAuthLogin 是登录入口，承担两件事：
//
//   - 显式要求飞书（?provider=feishu），或根本没有可用的自有账号 →
//     302 到飞书授权页（与改造前完全一致的行为）；
//   - 否则渲染登录页，页面上并列「飞书登录」与「账号登录」两个入口，
//     各自按启用状态显隐。
//
// 用 query 参数而不是新增 /auth/feishu 路由：登录页上的飞书按钮需要一个能
// 「明确指向飞书」的地址，而 /auth/login 在启用自有账号时已经变成了页面本身，
// 再给它加一条路由只会让两处逻辑分叉。
func (s *Service) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("provider") == "feishu" || !s.localLoginEnabled() {
		if !s.feishuReady() {
			writeJSON(w, http.StatusBadRequest, response{Error: "feishu login is not configured"})
			return
		}
		state := auth.NewState(w)
		http.Redirect(w, r, s.authFeishu.AuthorizeURL(state), http.StatusFound)
		return
	}
	s.renderLoginPage(w)
}

// renderLoginPage 输出登录页，并按其启用状态裁剪掉不可用的入口。
//
// 页面由这里直接返回而不是走静态目录：静态资源挂在受保护的根路径下，
// 未登录时会被中间件重定向，登录页反而加载不出来。
//
// 未启用的登录方式整块移除（而不是让前端隐藏）：入口连标记都不下发，
// 就不存在「被 JS 意外显示」或「点进去 404」的可能，页面源码本身就说明了
// 有哪些登录方式。因此这里不需要模板引擎，只需要按注释标记切块。
func (s *Service) renderLoginPage(w http.ResponseWriter) {
	page, err := webFiles.ReadFile("web/login.html")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, response{Error: "login page is unavailable"})
		return
	}
	body := string(page)
	body = stripMarkedBlock(body, "provider:feishu", s.feishuReady())
	body = stripMarkedBlock(body, "provider:local", s.localLoginEnabled())
	// 「或」这条分隔线只在两个入口同时存在时才有意义。
	body = stripMarkedBlock(body, "both:divider", s.feishuReady() && s.localLoginEnabled())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 登录页涉及凭据输入：禁止缓存，也禁止浏览器内容嗅探。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write([]byte(body)); err != nil {
		return
	}
}

// stripMarkedBlock 删除 <!--name--> 与 <!--/name--> 之间的内容；keep 为 true 时原样保留。
func stripMarkedBlock(page, name string, keep bool) string {
	if keep {
		return page
	}
	open, closing := "<!--"+name+"-->", "<!--/"+name+"-->"
	start := strings.Index(page, open)
	if start < 0 {
		return page
	}
	end := strings.Index(page[start:], closing)
	if end < 0 {
		return page
	}
	return page[:start] + page[start+end+len(closing):]
}

// handleAuthCallback 授权码换用户信息并签发登录态。
// ⚠️ 授权码 5 分钟有效且只能用一次：先校验 state，再立即兑换，不做其他 I/O。
//
// 这是唯一一条「访问者一定是浏览器」的认证路由（飞书 302 过来）。失败时不回 JSON
// 而回一张 HTML 错误页，理由见 renderCallbackError。
func (s *Service) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if !auth.VerifyState(r, r.URL.Query().Get("state")) {
		auth.ClearStateCookie(w)
		s.renderCallbackError(w, http.StatusBadRequest, "登录校验失败", "state mismatch")
		return
	}
	auth.ClearStateCookie(w)
	code := r.URL.Query().Get("code")
	if code == "" {
		s.renderCallbackError(w, http.StatusBadRequest, "登录校验失败", "missing code")
		return
	}
	accessToken, err := s.authFeishu.Exchange(r.Context(), code)
	if err != nil {
		s.renderCallbackError(w, http.StatusBadGateway, "飞书授权码兑换失败", err.Error())
		return
	}
	user, err := s.authFeishu.FetchUser(r.Context(), accessToken)
	if err != nil {
		s.renderCallbackError(w, http.StatusBadGateway, "获取飞书用户信息失败", err.Error())
		return
	}
	// 用户档案落库失败不阻断登录：登录态在进程内，auth_users 只是档案记录。
	_ = s.authUsers.UpsertFeishu(r.Context(), user)
	token, _, err := s.authSessions.Create(auth.FeishuOwner(user.OpenID), user.Name, user.AvatarURL, "feishu")
	if err != nil {
		s.renderCallbackError(w, http.StatusInternalServerError, "签发登录态失败", err.Error())
		return
	}
	auth.SetSessionCookie(w, token, s.authSessions.TTL(), s.cfg.Auth.CookieSecure)
	http.Redirect(w, r, "/", http.StatusFound)
}

// callbackErrorHints 把错误原文映射成一句可执行的处置建议。
// 逐条 Contains，首个命中即用 —— 各 match 串互不包含，无需排序。
var callbackErrorHints = []struct {
	match string
	hint  string
}{
	{"invalid_client", "飞书不认这对 App ID / App Secret。回「凭证与基础信息」重新复制当前 App Secret，用 scripts/feishu-set-secret.ps1 先验证再写入 .env，然后重建容器（up -d --force-recreate app）。"},
	{"20071", "redirect_uri 与飞书后台「安全设置 → 重定向 URL」的登记值不一致。两处必须逐字符相同（含协议、端口、路径），末尾不要带斜杠。"},
	{"20027", "授权范围未开通或未生效。在「权限管理」开通 contact:user.base:readonly，并让应用发布（或切到测试版），配置才会生效。"},
	{"20010", "当前账号不在应用的「可用范围」内。这一条后端日志里看不到，只能在飞书后台把账号加进可用范围。"},
	{"20004", "授权码已过期（寿命只有几分钟）。请重新发起一次授权。"},
	{"20003", "授权码不存在或已被使用过。请重新发起一次授权。"},
	{"state mismatch", "state 校验失败，最常见两种成因：① 浏览器用的主机名与 FEISHU_REDIRECT_URL 不一致（localhost 与 127.0.0.1 在 Cookie 维度并不等价）；② 这一页是刷新出来的 —— state Cookie 在换 token 之前就已被清除。"},
	{"missing code", "回调里没有 code。多半是直接打开了 /auth/callback，而不是从飞书授权页跳回来。"},
}

// renderCallbackError 把 /auth/callback 的失败渲染成一张给人看的 HTML 页。
//
// 这条路由的访问者一定是浏览器：飞书 302 到这里，失败时浏览器会把响应体原样显示。
// 早先这里直接 writeJSON 吐裸 JSON —— 用户看到一屏 {"error":"..."}，既看不出断在
// 哪一环，也没有「重新登录」的出口，唯一能做的动作是刷新，而刷新必然得到
// state mismatch（state Cookie 在兑换之前就已清掉）。2026-09-17 实测为此连跑三趟
// 浏览器才定位到根因：一屏 JSON 把「哪一环失败」这个信息整个丢掉了。
//
// HTTP 状态码保持不变（4xx/5xx 的语义仍然准确），只把响应体换成人可读的页面。
func (s *Service) renderCallbackError(w http.ResponseWriter, status int, title, detail string) {
	// detail 会嵌入飞书返回的原始响应体，属外部可控文本：先脱敏、再 HTML 转义。
	detail = observability.Redact(detail)
	hint := ""
	for _, h := range callbackErrorHints {
		if strings.Contains(detail, h.match) {
			hint = h.hint
			break
		}
	}

	var b strings.Builder
	b.WriteString("<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	b.WriteString("<meta name=\"color-scheme\" content=\"light\"><title>登录失败 · 星河系统</title>")
	// 与登录页同一套变量与版式：静态资源在受保护路径下，未登录时加载不到，故内联。
	b.WriteString(`<style>
    :root { color-scheme: light; --page: #ffffff; --line: #e6e6e8; --text: #202123; --muted: #6f7075; --accent: #10a37f; --accent-hover: #0d8f70; --warn-line: #e8b339; --warn-bg: #fff9e8;
      font-family: Inter, ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", "Microsoft YaHei", sans-serif; }
    * { box-sizing: border-box; }
    html, body { height: 100%; margin: 0; }
    body { display: grid; place-items: center; padding: 24px; background: #f7f7f8; color: var(--text); }
    .card { width: min(560px, 100%); padding: 28px; border: 1px solid var(--line); border-radius: 14px; background: var(--page); box-shadow: 0 12px 32px rgba(0, 0, 0, 0.06); }
    .brand { display: flex; align-items: center; gap: 10px; margin-bottom: 20px; font-size: 15px; font-weight: 650; }
    .brand-mark { display: grid; place-items: center; width: 30px; height: 30px; border-radius: 7px; background: var(--accent); color: #fff; font-weight: 750; }
    h1 { margin: 0 0 8px; font-size: 18px; }
    p.lead { margin: 0 0 4px; color: var(--muted); font-size: 14px; }
    .tip { padding: 12px 14px; margin: 14px 0 0; border-left: 3px solid var(--warn-line); border-radius: 0 8px 8px 0; background: var(--warn-bg); font-size: 13.5px; line-height: 1.7; }
    .label { margin: 20px 0 6px; color: var(--muted); font-size: 12px; }
    pre { margin: 0; padding: 12px; border-radius: 8px; background: #f4f4f5; color: #3f4147; font-size: 12.5px; line-height: 1.6; white-space: pre-wrap; word-break: break-all; }
    .button { display: block; height: 44px; margin-top: 22px; border-radius: 8px; background: var(--accent); color: #fff; font-weight: 600; line-height: 44px; text-align: center; text-decoration: none; }
    .button:hover { background: var(--accent-hover); }
  </style></head><body><main class="card">`)
	b.WriteString(`<div class="brand"><span class="brand-mark">E</span><span>星河系统</span></div>`)
	b.WriteString("<h1>" + html.EscapeString(title) + "</h1>")
	b.WriteString(`<p class="lead">飞书已完成授权，但本系统没能把它换成登录态，所以没有进入主页面。</p>`)
	if hint != "" {
		b.WriteString(`<div class="tip">` + html.EscapeString(hint) + `</div>`)
	}
	b.WriteString(`<div class="tip">这个回调链接已经作废：授权码一次性、几分钟内过期，state Cookie 也已清除。<b>刷新本页不会好</b>，请点下面的按钮重新发起登录。</div>`)
	b.WriteString(`<div class="label">错误详情（排查用）</div>`)
	b.WriteString("<pre>" + html.EscapeString(detail) + "</pre>")
	b.WriteString(`<a class="button" href="/auth/login">重新登录</a>`)
	b.WriteString(`</main></body></html>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 含外部来源文本：禁止缓存与内容嗅探，与登录页一致。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err := w.Write([]byte(b.String())); err != nil {
		return
	}
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
