package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// stateCookieName 是 CSRF state 的短时 Cookie 名。
const stateCookieName = "eino_oauth_state"

// Middleware 校验 Cookie 登录态并把 owner 注入 request context。
type Middleware struct {
	sessions *Sessions
}

// NewMiddleware 创建认证中间件。
func NewMiddleware(sessions *Sessions) *Middleware {
	return &Middleware{sessions: sessions}
}

// Authenticate 要求请求已登录：
// 浏览器导航（Accept 含 text/html）302 到登录页，其余一律 401 JSON。
// WebSocket 升级请求走 401 —— 浏览器无法对 302 自动跟随升级，前端应转登录页。
func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := m.sessions.Get(tokenFromCookie(r))
		if !ok {
			if wantsHTML(r) {
				http.Redirect(w, r, "/auth/login", http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
			return
		}
		next.ServeHTTP(w, r.WithContext(WithOwner(r.Context(), sess.Owner)))
	})
}

// wantsHTML 判断是否浏览器导航请求。fetch/XHR 的 Accept 不含 text/html。
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// tokenFromCookie 取登录 token；无 Cookie 时返回空串。
func tokenFromCookie(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// SetSessionCookie 写登录 Cookie。Secure 由配置决定（内网 http 部署必须 false，
// 否则浏览器不保存）；SameSite=Lax 允许从外部链接进入时携带。
func SetSessionCookie(w http.ResponseWriter, token string, ttl time.Duration, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// ClearSessionCookie 清除登录 Cookie（登出与会话过期后）。
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// NewState 生成随机 state 并写入短时 Cookie（HttpOnly + SameSite=Lax，5 分钟）。
func NewState(w http.ResponseWriter) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败意味着系统熵源不可用，此时任何凭据都不可信，直接置空
		// 让后续校验必然失败。
		return ""
	}
	state := hex.EncodeToString(buf)
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    state,
		Path:     "/auth",
		MaxAge:   300,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return state
}

// VerifyState 恒定时间比对回调携带的 state 与 Cookie 中的值。
// 任意一侧缺失即失败 —— 不校验 state 等于开放登录 CSRF。
func VerifyState(r *http.Request, got string) bool {
	c, err := r.Cookie(stateCookieName)
	if err != nil || c.Value == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(got)) == 1
}

// ClearStateCookie 校验完成后立即清除 state Cookie（一次性）。
func ClearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/auth",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
