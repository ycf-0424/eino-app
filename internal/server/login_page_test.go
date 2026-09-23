package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"my-eino-app/internal/auth"
	"my-eino-app/internal/config"
)

// newLoginService 构造「认证开启」的 Service：飞书凭据按需配齐、自有账号按需装配。
// 登录页与 /health 都不碰数据库，因此这里不需要 MySQL。
func newLoginService(t *testing.T, localEnabled, feishuConfigured bool) *Service {
	t.Helper()
	service := newTestService(t, nil)
	cfg := &config.Config{}
	cfg.Auth.Enabled = true
	if feishuConfigured {
		cfg.Auth.AppID = "cli_test"
		cfg.Auth.AppSecret = "secret"
		cfg.Auth.RedirectURL = "http://localhost:18180/auth/callback"
	}
	cfg.Auth.Local = config.LocalAuth{Enabled: localEnabled, MinPasswordLength: 8}
	service.cfg = cfg
	service.authSessions = auth.NewSessions(time.Hour)
	service.authFeishu = auth.NewFeishuClient(cfg.Auth)
	service.authMW = auth.NewMiddleware(service.authSessions)
	if localEnabled {
		service.localAccounts = auth.NewLocalStore(nil)
		service.loginLimiter = newRateLimiter(loginRatePerMinute, loginBurst)
	}
	return service
}

func getPath(t *testing.T, service *Service, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	// 登录测试使用配置中的规范主机，避免被 canonical-host 保护提前重定向。
	req.Host = "localhost:18180"
	service.Handler().ServeHTTP(rec, req)
	return rec
}

func healthLogin(t *testing.T, service *Service) map[string]bool {
	t.Helper()
	rec := getPath(t, service, "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("/health 状态 = %d", rec.Code)
	}
	var envelope struct {
		Data struct {
			Login map[string]bool `json:"login"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data.Login
}

// 两种登录方式都启用时，登录页同时给出两个入口。
func TestLoginPageRendersEnabledProviders(t *testing.T) {
	service := newLoginService(t, true, true)
	rec := getPath(t, service, "/auth/login")
	if rec.Code != http.StatusOK {
		t.Fatalf("登录页状态 = %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("登录页不得被缓存")
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"/auth/local"`) {
		t.Error("登录页缺少账号登录表单")
	}
	if !strings.Contains(body, "provider=feishu") {
		t.Error("登录页缺少飞书入口")
	}
	if !strings.Contains(body, `id="loginForm"`) && !strings.Contains(body, `id="localForm"`) {
		t.Error("登录页缺少表单元素")
	}
	// D12：仅管理员建号，页面不放任何注册入口或提示。
	if strings.Contains(body, "注册") {
		t.Error("登录页出现了注册相关字样")
	}
}

// 没配飞书凭据时，飞书入口必须消失（否则点进去是 400）。
func TestLoginPageHidesFeishuWhenUnavailable(t *testing.T) {
	service := newLoginService(t, true, false)
	body := getPath(t, service, "/auth/login").Body.String()
	if strings.Contains(body, "provider=feishu") {
		t.Error("飞书未配置时仍渲染了飞书入口")
	}
	if !strings.Contains(body, `"/auth/local"`) {
		t.Error("账号登录入口应保留")
	}
}

// 只有飞书可用时保持改造前的行为：/auth/login 直接 302 到授权页。
func TestAuthLoginRedirectsToFeishuWhenLocalDisabled(t *testing.T) {
	service := newLoginService(t, false, true)
	rec := getPath(t, service, "/auth/login")
	if rec.Code != http.StatusFound {
		t.Fatalf("/auth/login 状态 = %d, want 302", rec.Code)
	}
	if location := rec.Header().Get("Location"); !strings.Contains(location, "accounts.feishu.cn") {
		t.Fatalf("Location = %q", location)
	}
}

// 两种都启用时，登录页上的飞书按钮通过 ?provider=feishu 明确指向授权页。
func TestAuthLoginExplicitFeishuProviderRedirects(t *testing.T) {
	service := newLoginService(t, true, true)
	rec := getPath(t, service, "/auth/login?provider=feishu")
	if rec.Code != http.StatusFound {
		t.Fatalf("状态 = %d, want 302", rec.Code)
	}
	if location := rec.Header().Get("Location"); !strings.Contains(location, "accounts.feishu.cn") {
		t.Fatalf("Location = %q", location)
	}
}

// 未配置飞书且没启用自有账号时，飞书入口返回 400（沿用改造前的响应）。
func TestAuthLoginWithoutAnyProvider(t *testing.T) {
	service := newLoginService(t, false, false)
	if rec := getPath(t, service, "/auth/login"); rec.Code != http.StatusBadRequest {
		t.Fatalf("状态 = %d, want 400", rec.Code)
	}
}

// /health 的 login 字段必须反映「真的可用」，登录页据此渲染。
func TestHealthReportsLoginMethods(t *testing.T) {
	if login := healthLogin(t, newLoginService(t, true, true)); !login["feishu"] || !login["local"] {
		t.Fatalf("两种都启用时 login = %+v", login)
	}
	if login := healthLogin(t, newLoginService(t, true, false)); login["feishu"] || !login["local"] {
		t.Fatalf("只有自有账号时 login = %+v", login)
	}
	if login := healthLogin(t, newLoginService(t, false, true)); !login["feishu"] || login["local"] {
		t.Fatalf("只有飞书时 login = %+v", login)
	}
	// 认证关闭：两个入口都不该出现（该场景下 /auth/login 本身是 404）。
	if login := healthLogin(t, newTestService(t, nil)); login["feishu"] || login["local"] {
		t.Fatalf("认证关闭时 login = %+v，应全为 false", login)
	}
}

// 认证关闭时登录相关路由整体不存在，与改造前一致。
func TestAuthRoutesAbsentWhenDisabled(t *testing.T) {
	service := newTestService(t, nil)
	for _, path := range []string{"/auth/login", "/auth/me"} {
		if rec := getPath(t, service, path); rec.Code != http.StatusNotFound {
			t.Fatalf("%s 状态 = %d, want 404", path, rec.Code)
		}
	}
}
