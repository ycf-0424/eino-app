//go:build mysql_integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"my-eino-app/internal/auth"
	"my-eino-app/internal/config"
)

// newLocalLoginService 构造「认证开启 + 自有账号启用」的 Service，
// 复用 owner 隔离用例的真实 MySQL 后端。
func newLocalLoginService(t *testing.T) (*Service, *auth.LocalStore) {
	t.Helper()
	service := newMySQLOwnerService(t)
	service.cfg.Auth.Local = config.LocalAuth{Enabled: true, MinPasswordLength: 8}
	accounts := auth.NewLocalStore(service.sessions.DB())
	service.localAccounts = accounts
	service.loginLimiter = newRateLimiter(loginRatePerMinute, loginBurst)
	return service, accounts
}

// registerLocalAccount 建号并在用例结束时清理（验收不留下垃圾账号）。
func registerLocalAccount(t *testing.T, service *Service, accounts *auth.LocalStore, username, password string, disabled bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := accounts.Create(ctx, username, password, "登录用例", false, 8); err != nil {
		t.Fatalf("建号失败: %v", err)
	}
	if disabled {
		if err := accounts.SetDisabled(ctx, username, true); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := service.sessions.DB().ExecContext(context.Background(), "DELETE FROM auth_local_users WHERE username=?", username); err != nil {
			t.Logf("清理账号 %s 失败: %v", username, err)
		}
	})
}

func postLocalLogin(t *testing.T, service *Service, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/local", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	service.Handler().ServeHTTP(rec, req)
	return rec
}

// 步骤 2.12 验收：正确凭据签发登录态，且该登录态与飞书登录走的是同一条身份注入路径。
func TestLocalLoginIssuesSession(t *testing.T) {
	service, accounts := newLocalLoginService(t)
	username := "it_login_" + time.Now().Format("150405000")
	password := "Str0ng-Passw0rd!"
	registerLocalAccount(t, service, accounts, username, password, false)

	rec := postLocalLogin(t, service, username, password)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录状态 = %d, body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data struct {
			Owner    string `json:"owner"`
			Name     string `json:"name"`
			Provider string `json:"provider"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Provider != "local" {
		t.Fatalf("provider = %q, want local", envelope.Data.Provider)
	}
	// owner 必须是 local:<uuid> 形式：下游只做等值比较，前缀是排查归属的依据。
	if !strings.HasPrefix(envelope.Data.Owner, "local:") || len(envelope.Data.Owner) <= len("local:") {
		t.Fatalf("owner = %q, want local:<uuid>", envelope.Data.Owner)
	}
	if envelope.Data.Name != "登录用例" {
		t.Fatalf("name = %q", envelope.Data.Name)
	}

	cookie := sessionCookieFrom(t, rec)
	if cookie == nil {
		t.Fatal("登录响应缺少 Cookie")
	}
	if !cookie.HttpOnly {
		t.Fatal("登录 Cookie 必须是 HttpOnly")
	}

	// 用这张 Cookie 访问 /auth/me：证明登录态真的生效，且身份可被读取。
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.AddCookie(cookie)
	me := httptest.NewRecorder()
	service.Handler().ServeHTTP(me, req)
	if me.Code != http.StatusOK {
		t.Fatalf("/auth/me 状态 = %d, body=%s", me.Code, me.Body.String())
	}
	var meEnvelope struct {
		Data struct {
			Owner    string `json:"owner"`
			Provider string `json:"provider"`
		} `json:"data"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &meEnvelope); err != nil {
		t.Fatal(err)
	}
	if meEnvelope.Data.Provider != "local" || meEnvelope.Data.Owner != envelope.Data.Owner {
		t.Fatalf("/auth/me 返回不正确: %+v", meEnvelope.Data)
	}

	// 登出后同一 Cookie 立即失效。
	logout := httptest.NewRequest(http.MethodGet, "/auth/logout", nil)
	logout.AddCookie(cookie)
	service.Handler().ServeHTTP(httptest.NewRecorder(), logout)
	after := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req2.AddCookie(cookie)
	service.Handler().ServeHTTP(after, req2)
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("登出后 /auth/me 状态 = %d, want 401", after.Code)
	}
}

// 步骤 2.12 的三条「不泄露」要求：账号不存在、口令错误、账号停用、
// 以及会污染 owner 命名空间的用户名，对外必须是同一个响应。
func TestLocalLoginDoesNotLeakAccountState(t *testing.T) {
	service, accounts := newLocalLoginService(t)
	username := "it_leak_" + time.Now().Format("150405000")
	disabled := "it_dis_" + time.Now().Format("150405000")
	registerLocalAccount(t, service, accounts, username, "Str0ng-Passw0rd!", false)
	registerLocalAccount(t, service, accounts, disabled, "Str0ng-Passw0rd!", true)

	cases := []struct {
		name     string
		username string
		password string
	}{
		{name: "口令错误", username: username, password: "Wr0ng-Passw0rd!"},
		{name: "账号不存在", username: username + "_missing", password: "Str0ng-Passw0rd!"},
		{name: "账号已停用", username: disabled, password: "Str0ng-Passw0rd!"},
		{name: "用户名含冒号", username: "feishu:ou_injected", password: "Str0ng-Passw0rd!"},
	}
	var body string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postLocalLogin(t, service, tc.username, tc.password)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("状态 = %d, body=%s", rec.Code, rec.Body.String())
			}
			// 停用账号不能提示「已停用」，否则等于确认了账号存在。
			if strings.Contains(rec.Body.String(), "disabled") || strings.Contains(rec.Body.String(), "停用") {
				t.Fatalf("响应泄露了账号状态: %s", rec.Body.String())
			}
			if body == "" {
				body = rec.Body.String()
				return
			}
			if rec.Body.String() != body {
				t.Fatalf("不同失败原因的响应体不一致：\n前: %s\n后: %s", body, rec.Body.String())
			}
		})
	}
}

// 步骤 2.13 验收：连续失败触发 429，且在限流窗口内即使口令正确也被拒
// （这是预期行为，不是 bug —— 换成正确口令就能绕过就等于没限流）。
func TestLocalLoginRateLimit(t *testing.T) {
	service, accounts := newLocalLoginService(t)
	username := "it_rate_" + time.Now().Format("150405000")
	password := "Str0ng-Passw0rd!"
	registerLocalAccount(t, service, accounts, username, password, false)

	for i := 0; i < loginBurst; i++ {
		if rec := postLocalLogin(t, service, username, "Wr0ng-Passw0rd!"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败尝试状态 = %d, want 401", i+1, rec.Code)
		}
	}
	blocked := postLocalLogin(t, service, username, password)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("超限后状态 = %d, want 429（body=%s）", blocked.Code, blocked.Body.String())
	}
	if blocked.Header().Get("Retry-After") == "" {
		t.Fatal("429 响应必须带 Retry-After")
	}

	// 窗口过后恢复正常：把限流器的时间推后 2 分钟（补足令牌）。
	base := time.Now()
	service.loginLimiter.now = func() time.Time { return base.Add(2 * time.Minute) }
	if rec := postLocalLogin(t, service, username, password); rec.Code != http.StatusOK {
		t.Fatalf("限流窗口过后状态 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
}

// 登录成功要归还令牌：正常用户不该被自己的历史失败（或同 IP 其他人的失败）拖住。
func TestLocalLoginRefundsOnSuccess(t *testing.T) {
	service, accounts := newLocalLoginService(t)
	username := "it_refund_" + time.Now().Format("150405000")
	password := "Str0ng-Passw0rd!"
	registerLocalAccount(t, service, accounts, username, password, false)

	// 先把桶打到只剩 1 个令牌。
	for i := 0; i < loginBurst-1; i++ {
		postLocalLogin(t, service, username, "Wr0ng-Passw0rd!")
	}
	if rec := postLocalLogin(t, service, username, password); rec.Code != http.StatusOK {
		t.Fatalf("最后一次应放行, 状态 = %d", rec.Code)
	}
	// 成功归还了令牌，所以还能再登录。
	if rec := postLocalLogin(t, service, username, password); rec.Code != http.StatusOK {
		t.Fatalf("成功登录后应仍可用, 状态 = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == auth.CookieName {
			return cookie
		}
	}
	return nil
}
