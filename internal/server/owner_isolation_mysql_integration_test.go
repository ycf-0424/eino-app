//go:build mysql_integration

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"my-eino-app/internal/auth"
	"my-eino-app/internal/checkpoint"
	"my-eino-app/internal/config"
	"my-eino-app/internal/execution"
	"my-eino-app/internal/session"
	"my-eino-app/internal/skill"
)

const (
	ownerA = "feishu:ou_iso_owner_a"
	ownerB = "feishu:ou_iso_owner_b"
)

// newMySQLOwnerService 构造 auth.enabled + MySQL 会话存储的 Service。
//
// 文件后端没有 owner 维度（OwnerOf 恒返回空 owner），跨身份的越权用例只能在
// MySQL 上验证 —— 这也正是生产环境启用认证时的唯一合法组合。
func newMySQLOwnerService(t *testing.T) *Service {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Session.Store = "mysql"
	cfg.Auth = config.Auth{Enabled: true, AppID: "cli_test", AppSecret: "secret", RedirectURL: "http://localhost:18180/auth/callback"}
	sessions, err := session.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sessions.Close() })
	cp, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{
		sessions:    sessions,
		skills:      skill.NewLoader(t.TempDir()),
		checkpoints: cp,
		cfg:         cfg,
		// 手工程造 Service 时必须自己补齐 NewService 负责的并发原语：
		// 审批路径经 acquireSession 取会话级锁，locks 为 nil 会 panic。
		locks: map[string]chan struct{}{},
	}
	s.authSessions = auth.NewSessions(time.Hour)
	s.authFeishu = auth.NewFeishuClient(cfg.Auth)
	s.authUsers = auth.NewUserStore(nil)
	s.authMW = auth.NewMiddleware(s.authSessions)
	return s
}

func issueSession(t *testing.T, service *Service, token string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sessions", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	rec := httptest.NewRecorder()
	service.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /sessions status=%d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.ID == "" {
		t.Fatal("POST /sessions returned an empty id")
	}
	return envelope.Data.ID
}

func doAs(t *testing.T, service *Service, token, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	}
	rec := httptest.NewRecorder()
	service.Handler().ServeHTTP(rec, req)
	return rec
}

// 步骤 2.7 的验收：B 拿着 A 的 session id 也读不到、删不掉。
func TestCrossOwnerSessionAccessIsForbidden(t *testing.T) {
	service := newMySQLOwnerService(t)
	tokenA, _, err := service.authSessions.Create(ownerA, "甲", "", "feishu")
	if err != nil {
		t.Fatal(err)
	}
	tokenB, _, err := service.authSessions.Create(ownerB, "乙", "", "feishu")
	if err != nil {
		t.Fatal(err)
	}

	idA := issueSession(t, service, tokenA)
	defer service.sessions.Delete(ownerA, idA)

	// 归属已登记在 A 名下。
	owner, exists, err := service.sessions.OwnerOf(idA)
	if err != nil || !exists || owner != ownerA {
		t.Fatalf("OwnerOf = %q %v %v", owner, exists, err)
	}

	// 读：A 200，B 403。
	if rec := doAs(t, service, tokenA, http.MethodGet, "/sessions/"+idA); rec.Code != http.StatusOK {
		t.Fatalf("owner A GET status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doAs(t, service, tokenB, http.MethodGet, "/sessions/"+idA); rec.Code != http.StatusForbidden {
		t.Fatalf("owner B GET status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}

	// 删：B 403，且 A 的会话仍在；随后 A 自己删掉是 200。
	if rec := doAs(t, service, tokenB, http.MethodDelete, "/sessions/"+idA); rec.Code != http.StatusForbidden {
		t.Fatalf("owner B DELETE status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	if _, exists, err := service.sessions.OwnerOf(idA); err != nil || !exists {
		t.Fatalf("owner A session was removed by owner B: exists=%v err=%v", exists, err)
	}
	if rec := doAs(t, service, tokenA, http.MethodDelete, "/sessions/"+idA); rec.Code != http.StatusOK {
		t.Fatalf("owner A DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// 会话列表按登录身份过滤：B 的列表里不能出现 A 的会话。
func TestSessionListIsScopedToOwner(t *testing.T) {
	service := newMySQLOwnerService(t)
	tokenA, _, err := service.authSessions.Create(ownerA, "甲", "", "feishu")
	if err != nil {
		t.Fatal(err)
	}
	tokenB, _, err := service.authSessions.Create(ownerB, "乙", "", "feishu")
	if err != nil {
		t.Fatal(err)
	}
	idA := issueSession(t, service, tokenA)
	defer service.sessions.Delete(ownerA, idA)

	rec := doAs(t, service, tokenA, http.MethodGet, "/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner A list status=%d", rec.Code)
	}
	if !listContains(t, rec.Body.Bytes(), idA) {
		t.Fatal("owner A cannot see its own session")
	}
	rec = doAs(t, service, tokenB, http.MethodGet, "/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner B list status=%d", rec.Code)
	}
	if listContains(t, rec.Body.Bytes(), idA) {
		t.Fatal("owner B can see owner A's session")
	}
}

func listContains(t *testing.T, body []byte, id string) bool {
	t.Helper()
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	for _, item := range envelope.Data {
		if item.ID == id {
			return true
		}
	}
	return false
}

// resolveSessionID 是「沿用 / 拒绝」的唯一判定点，直接覆盖越权分支。
func TestResolveSessionIDRejectsForeignOwner(t *testing.T) {
	service := newMySQLOwnerService(t)
	idA := issueSession(t, service, mustToken(t, service, ownerA))
	defer service.sessions.Delete(ownerA, idA)

	ctxA := auth.WithOwner(context.Background(), ownerA)
	ctxB := auth.WithOwner(context.Background(), ownerB)

	if got, err := service.resolveSessionID(ctxA, idA); err != nil || got != idA {
		t.Fatalf("owner A resolve = %q, %v", got, err)
	}
	if _, err := service.resolveSessionID(ctxB, idA); !errors.Is(err, session.ErrForeignSession) {
		t.Fatalf("owner B resolve err = %v, want ErrForeignSession", err)
	}
	// 未登记的新 id 沿用（客户端为新会话指定 id 的既有用法不受影响）。
	if got, err := service.resolveSessionID(ctxB, idA+"-unclaimed"); err != nil || got != idA+"-unclaimed" {
		t.Fatalf("unclaimed resolve = %q, %v", got, err)
	}
	// 空 id 由服务端生成。
	if got, err := service.resolveSessionID(ctxB, ""); err != nil || got == "" || got == idA {
		t.Fatalf("empty resolve = %q, %v", got, err)
	}
}

// 步骤 2.8 验收：execution 与 approval 都只按 session id 取数，没有 owner 过滤条件，
// 所以「B 拿 A 的 id」必须在这里被显式拒绝（403），而不是回 200 空快照或
// 「该会话没有待审批」这类业务错误 —— 后者会让越权看起来像参数问题。
func TestCrossOwnerExecutionAndApprovalAreForbidden(t *testing.T) {
	service := newMySQLOwnerService(t)
	db := service.sessions.DB()
	if db == nil {
		t.Skip("mysql session store unavailable")
	}
	if _, err := db.ExecContext(context.Background(), "SELECT 1 FROM execution_runs LIMIT 0"); err != nil {
		t.Skipf("execution schema is not installed: %v", err)
	}
	service.executions = execution.NewMySQLStore(db)

	tokenA := mustToken(t, service, ownerA)
	tokenB := mustToken(t, service, ownerB)
	idA := issueSession(t, service, tokenA)
	defer service.sessions.Delete(ownerA, idA)
	defer service.DeleteExecutions(context.Background(), idA)

	execPath := "/sessions/" + idA + "/execution"
	if rec := doAs(t, service, tokenA, http.MethodGet, execPath); rec.Code != http.StatusOK {
		t.Fatalf("owner A execution status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doAs(t, service, tokenB, http.MethodGet, execPath); rec.Code != http.StatusForbidden {
		t.Fatalf("owner B execution status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}

	approvalPath := "/sessions/" + idA + "/approval"
	if rec := doJSON(t, service, tokenB, http.MethodPost, approvalPath, `{"approved":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("owner B approval status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	// 同一个路由对本人是可达的：返回的是业务错误（没有待审批的中断），不是 403。
	// 这条断言把「越权被拒」和「路由本身不可用」区分开。
	if rec := doJSON(t, service, tokenA, http.MethodPost, approvalPath, `{"approved":true}`); rec.Code == http.StatusForbidden {
		t.Fatalf("owner A approval was rejected as foreign: body=%s", rec.Body.String())
	}
}

func doJSON(t *testing.T, service *Service, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	}
	rec := httptest.NewRecorder()
	service.Handler().ServeHTTP(rec, req)
	return rec
}

func mustToken(t *testing.T, service *Service, owner string) string {
	t.Helper()
	token, _, err := service.authSessions.Create(owner, owner, "", "feishu")
	if err != nil {
		t.Fatal(err)
	}
	return token
}
