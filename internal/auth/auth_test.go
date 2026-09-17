package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"my-eino-app/internal/config"
)

func TestSessionsCreateGetDelete(t *testing.T) {
	s := NewSessions(time.Hour)
	token, _, err := s.Create("feishu:ou_123", "张三", "https://avatar", "feishu")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if token == "" {
		t.Fatal("token should not be empty")
	}
	got, ok := s.Get(token)
	if !ok {
		t.Fatal("valid token should be found")
	}
	if got.Owner != "feishu:ou_123" || got.Name != "张三" || got.Provider != "feishu" {
		t.Fatalf("session mismatch: %+v", got)
	}
	if _, ok := s.Get("wrong-token"); ok {
		t.Fatal("wrong token must not be found")
	}
	s.Delete(token)
	if _, ok := s.Get(token); ok {
		t.Fatal("deleted token must not be found")
	}
	// 登出对未登录者幂等：空 token 不应 panic。
	s.Delete("")
}

func TestSessionsExpired(t *testing.T) {
	s := NewSessions(10 * time.Millisecond)
	token, _, err := s.Create("local:u1", "u1", "", "local")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, ok := s.Get(token); ok {
		t.Fatal("expired token must be rejected")
	}
	if removed := s.cleanup(); removed != 1 {
		t.Fatalf("cleanup should remove 1, got %d", removed)
	}
}

func TestSessionsTwoTokensIndependent(t *testing.T) {
	s := NewSessions(time.Hour)
	t1, _, _ := s.Create("feishu:a", "a", "", "feishu")
	t2, _, _ := s.Create("feishu:b", "b", "", "feishu")
	s.Delete(t1)
	if _, ok := s.Get(t1); ok {
		t.Fatal("t1 should be gone")
	}
	if _, ok := s.Get(t2); !ok {
		t.Fatal("t2 must stay valid")
	}
}

func TestAuthorizeURL(t *testing.T) {
	cfg := config.Auth{
		AppID:       "cli_test",
		AppSecret:   "secret",
		RedirectURL: "http://localhost:18180/auth/callback",
	}
	f := NewFeishuClient(cfg)
	u := f.AuthorizeURL("st4te")
	if !strings.HasPrefix(u, "https://accounts.feishu.cn/open-apis/authen/v1/authorize?") {
		t.Fatalf("authorize base wrong: %s", u)
	}
	for _, want := range []string{
		"client_id=cli_test",
		"redirect_uri=http%3A%2F%2Flocalhost%3A18180%2Fauth%2Fcallback",
		"response_type=code",
		"scope=" + FeishuScope,
		"state=st4te",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("authorize URL missing %q in %s", want, u)
		}
	}
	// 不带 PKCE：官方 v3 token 端点不支持 code_challenge。
	if strings.Contains(u, "code_challenge") {
		t.Fatal("authorize URL must not contain PKCE parameters")
	}
}

func TestExchangeSuccess(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("content type: %s", ct)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"access_token":"u-at-1","expires_in":7200,"token_type":"Bearer"}`))
	}))
	defer srv.Close()
	cfg := config.Auth{AppID: "cli", AppSecret: "sec", RedirectURL: "http://cb"}
	f := NewFeishuClient(cfg)
	f.tokenURL = srv.URL
	token, err := f.Exchange(context.Background(), "code-1")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if token != "u-at-1" {
		t.Fatalf("token: %s", token)
	}
	if gotBody["grant_type"] != "authorization_code" || gotBody["code"] != "code-1" || gotBody["redirect_uri"] != "http://cb" {
		t.Fatalf("exchange body mismatch: %+v", gotBody)
	}
}

func TestExchangeEmptyCode(t *testing.T) {
	f := NewFeishuClient(config.Auth{})
	if _, err := f.Exchange(context.Background(), ""); err == nil {
		t.Fatal("empty code must fail")
	}
}

func TestFetchUserSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer u-at-1" {
			t.Errorf("authorization header: %s", got)
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"open_id":"ou_9","union_id":"on_9","name":"李四","avatar_url":""}}`))
	}))
	defer srv.Close()
	f := NewFeishuClient(config.Auth{})
	f.userInfoURL = srv.URL
	u, err := f.FetchUser(context.Background(), "u-at-1")
	if err != nil {
		t.Fatalf("fetch user: %v", err)
	}
	if u.OpenID != "ou_9" || u.Name != "李四" {
		t.Fatalf("user mismatch: %+v", u)
	}
	if owner := FeishuOwner(u.OpenID); owner != "feishu:ou_9" {
		t.Fatalf("owner: %s", owner)
	}
}

func TestFetchUserBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":99991668,"msg":"token invalid"}`))
	}))
	defer srv.Close()
	f := NewFeishuClient(config.Auth{})
	f.userInfoURL = srv.URL
	if _, err := f.FetchUser(context.Background(), "bad"); err == nil {
		t.Fatal("business error must fail")
	}
}

func TestFetchUserEmptyOpenID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"open_id":""}}`))
	}))
	defer srv.Close()
	f := NewFeishuClient(config.Auth{})
	f.userInfoURL = srv.URL
	if _, err := f.FetchUser(context.Background(), "at"); err == nil {
		t.Fatal("empty open_id must fail")
	}
}

func TestOwnerHelpers(t *testing.T) {
	if FeishuOwner("") != "" {
		t.Fatal("empty open id must give empty owner")
	}
	if LocalOwner("uuid-1") != "local:uuid-1" {
		t.Fatalf("local owner: %s", LocalOwner("uuid-1"))
	}
	if LocalOwner("") != "" {
		t.Fatal("empty user id must give empty owner")
	}
}

func TestMiddlewareUnauthenticated(t *testing.T) {
	m := NewMiddleware(NewSessions(time.Hour))
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("next must not run without a session")
	})
	h := m.Authenticate(next)

	// API 请求：401 JSON。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/chat", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("api status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "error") {
		t.Fatalf("body should be JSON error: %s", rec.Body.String())
	}

	// 浏览器导航：302 到登录页。
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("html status: %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/auth/login" {
		t.Fatalf("location: %s", loc)
	}
}

func TestMiddlewareAuthenticatedInjectsOwner(t *testing.T) {
	s := NewSessions(time.Hour)
	token, _, _ := s.Create("feishu:ou_1", "n", "", "feishu")
	m := NewMiddleware(s)
	var ownerInCtx string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ownerInCtx = OwnerFromContext(r.Context())
	})
	h := m.Authenticate(next)
	req := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	h.ServeHTTP(httptest.NewRecorder(), req)
	if ownerInCtx != "feishu:ou_1" {
		t.Fatalf("owner in context: %q", ownerInCtx)
	}
}

func TestStateCookieRoundTrip(t *testing.T) {
	w := httptest.NewRecorder()
	state := NewState(w)
	if state == "" {
		t.Fatal("state should not be empty")
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == stateCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("state cookie should be set")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("state cookie flags wrong: %+v", cookie)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state="+state, nil)
	req.AddCookie(cookie)
	if !VerifyState(req, state) {
		t.Fatal("matching state should verify")
	}
	if VerifyState(req, "tampered") {
		t.Fatal("mismatched state must be rejected")
	}
	req2 := httptest.NewRequest(http.MethodGet, "/auth/callback?state="+state, nil)
	if VerifyState(req2, state) {
		t.Fatal("missing cookie must be rejected")
	}
}
