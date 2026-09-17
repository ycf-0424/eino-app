package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"my-eino-app/internal/auth"
	"my-eino-app/internal/config"
	"my-eino-app/internal/session"
	"my-eino-app/internal/skill"
)

// newAuthTestService 构造一个 auth.enabled 的最小 Service（不经 NewService，
// 不需要模型与外部服务），覆盖路由层的认证行为。
func newAuthTestService(t *testing.T) *Service {
	t.Helper()
	sessions, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Auth = config.Auth{Enabled: true, AppID: "cli_test", AppSecret: "secret", RedirectURL: "http://localhost:18180/auth/callback"}
	cfg.Session.Store = "file"
	s := &Service{sessions: sessions, skills: skill.NewLoader(t.TempDir()), cfg: cfg}
	s.authSessions = auth.NewSessions(time.Hour)
	s.authFeishu = auth.NewFeishuClient(cfg.Auth)
	s.authUsers = auth.NewUserStore(nil)
	s.authMW = auth.NewMiddleware(s.authSessions)
	return s
}

func TestAuthUnauthenticatedAccess(t *testing.T) {
	service := newAuthTestService(t)
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

	// 浏览器导航：302 到登录入口。
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/auth/login" {
		t.Fatalf("GET / status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// API 调用：401 JSON，不是重定向。
	resp, err = client.Post(server.URL+"/chat", "application/json", strings.NewReader(`{"query":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /chat status=%d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "error") {
		t.Fatalf("POST /chat body=%s", body)
	}

	// /health 在免认证白名单内。
	resp, err = client.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /health status=%d", resp.StatusCode)
	}
}

func TestAuthMeAndLogout(t *testing.T) {
	service := newAuthTestService(t)
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

	token, _, err := service.authSessions.Create("feishu:ou_test", "张三", "", "feishu")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/auth/me status=%d body=%s", resp.StatusCode, body)
	}
	for _, want := range []string{`"owner":"feishu:ou_test"`, `"name":"张三"`, `"provider":"feishu"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("/auth/me body missing %s: %s", want, body)
		}
	}

	// 登出后同一 token 失效。
	req, _ = http.NewRequest(http.MethodGet, server.URL+"/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/auth/login" {
		t.Fatalf("logout status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	req, _ = http.NewRequest(http.MethodGet, server.URL+"/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/auth/me after logout status=%d", resp.StatusCode)
	}
}

func TestAuthLoginRedirectsWithState(t *testing.T) {
	service := newAuthTestService(t)
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := client.Get(server.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("/auth/login status=%d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "https://accounts.feishu.cn/open-apis/authen/v1/authorize?") {
		t.Fatalf("location=%s", loc)
	}
	if !strings.Contains(loc, "client_id=cli_test") || !strings.Contains(loc, "scope=") || !strings.Contains(loc, "state=") {
		t.Fatalf("location missing params: %s", loc)
	}
	var hasStateCookie bool
	for _, c := range resp.Cookies() {
		if c.Name == "eino_oauth_state" && c.Value != "" {
			hasStateCookie = true
		}
	}
	if !hasStateCookie {
		t.Fatal("state cookie should be set")
	}
}

func TestAuthCallbackRejectsStateMismatch(t *testing.T) {
	service := newAuthTestService(t)
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	client := server.Client()

	// 不带 state Cookie 的回调必须被拒绝。
	resp, err := client.Get(server.URL + "/auth/callback?code=x&state=whatever")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback without state cookie status=%d", resp.StatusCode)
	}

	// state 与 Cookie 不一致同样拒绝。
	w := httptest.NewRecorder()
	auth.NewState(w)
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/auth/callback?code=x&state=tampered", nil)
	for _, c := range w.Result().Cookies() {
		req.AddCookie(c)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback with mismatched state status=%d", resp.StatusCode)
	}
}

func TestAuthDisabledKeepsOldBehavior(t *testing.T) {
	// auth 关闭时：/ 不重定向、/auth/* 全部 404、POST /chat 不需要登录。
	sessions, _ := session.New(t.TempDir())
	service := &Service{sessions: sessions, skills: skill.NewLoader(t.TempDir())}
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	client := server.Client()

	resp, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET / status=%d", resp.StatusCode)
	}
	for _, path := range []string{"/auth/login", "/auth/me", "/auth/logout"} {
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s status=%d, want 404", path, resp.StatusCode)
		}
	}
}

// TestCallbackErrorRendersHTMLPage 锁定失败页的形态：必须是给人看的 HTML，
// 带处置建议与「重新登录」出口，而不是早先那屏把信息全丢掉的裸 JSON。
func TestCallbackErrorRendersHTMLPage(t *testing.T) {
	service := newAuthTestService(t)

	rec := httptest.NewRecorder()
	service.renderCallbackError(rec, http.StatusBadGateway, "飞书授权码兑换失败",
		`feishu exchange: http 400: {"error":"invalid_client","error_description":"The client secret is invalid.","code":20002}`)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502（状态码语义保持不变）", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type=%q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"重新登录", `href="/auth/login"`,
		"feishu-set-secret.ps1", // invalid_client 对应的处置建议
		"刷新本页不会好",               // 这一页最容易诱发的无效动作
		"invalid_client",        // 原始错误串要保留，便于排查
	} {
		if !strings.Contains(body, want) {
			t.Errorf("失败页缺少 %q", want)
		}
	}
}

// TestCallbackErrorEscapesAndRedacts 覆盖失败页对外部文本的两道处理：
// HTML 转义（防注入）与密钥脱敏（不把凭据渲染到页面上）。
// detail 里嵌的是飞书返回的原始响应体，是本项目唯一一处外部可控的渲染输入。
func TestCallbackErrorEscapesAndRedacts(t *testing.T) {
	service := newAuthTestService(t)

	rec := httptest.NewRecorder()
	service.renderCallbackError(rec, http.StatusBadGateway, "兑换失败",
		`{"msg":"<script>alert(1)</script>","client_secret":"SUPERSECRETVALUE"}`)
	body := rec.Body.String()

	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("外部文本未转义，原样落进了页面")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("期望看到转义后的 &lt;script&gt;")
	}
	if strings.Contains(body, "SUPERSECRETVALUE") {
		t.Error("client_secret 未被脱敏")
	}
}

// TestAuthCallbackFailureIsHTML 走真实路由确认浏览器拿到的是页面而非 JSON。
func TestAuthCallbackFailureIsHTML(t *testing.T) {
	service := newAuthTestService(t)
	server := httptest.NewServer(service.Handler())
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/auth/callback?code=x&state=whatever")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type=%q, want text/html（浏览器会原样显示响应体）", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "state mismatch") {
		t.Error("页面应保留原始错误串，便于排查")
	}
}
