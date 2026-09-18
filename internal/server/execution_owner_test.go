package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"my-eino-app/internal/auth"
	"my-eino-app/internal/session"
)

// 步骤 2.8：execution_runs 没有 owner 冗余列，只按 conversation_id 取数，
// 所以「这条会话是不是调用方的」只能在应用层判定。判定点就是 checkSessionOwner，
// 它的三种结果（本人放行 / 不存在放行 / 他人拒绝）必须互相可区分。
func TestCheckSessionOwner(t *testing.T) {
	service := newTestService(t, nil)
	ctx := context.Background()
	if err := service.sessions.Create("", "mine"); err != nil {
		t.Fatal(err)
	}

	// 单用户模式（无 auth）：登记者与请求身份都是空 owner → 放行。
	if err := service.checkSessionOwner(ctx, "mine"); err != nil {
		t.Fatalf("single-user owner check = %v", err)
	}
	// 该辅助函数也用于 execution/approval：未登记资源由具体端点按空结果处理。
	// 聊天入口另由 resolveSessionID 拒绝未签发 id。
	if err := service.checkSessionOwner(ctx, "unclaimed"); err != nil {
		t.Fatalf("unclaimed owner check = %v", err)
	}
	// 已登记但请求身份不同 → 越权哨兵错误，HTTP 层据此回 403。
	if err := service.checkSessionOwner(auth.WithOwner(ctx, "feishu:ou_other"), "mine"); !errors.Is(err, session.ErrForeignSession) {
		t.Fatalf("foreign owner check = %v, want ErrForeignSession", err)
	}
	// 非法 id 直接报错，不能落到文件路径拼接里。
	if err := service.checkSessionOwner(ctx, "../escape"); err == nil {
		t.Fatal("owner check must reject unsafe ids")
	}
}

// 单用户路径必须原样可用：文件后端没有 owner 维度，正常会话不能被归属校验挡住。
func TestHandleExecutionKeepsSingleUserPath(t *testing.T) {
	service := newTestService(t, nil)
	if err := service.sessions.Create("", "solo"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/sessions/solo/execution", nil)
	req.SetPathValue("id", "solo")
	rec := httptest.NewRecorder()
	service.handleExecution(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleExecution status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// 越权请求在读到任何执行记录之前就被拒绝，且回的是 403 而不是 400。
func TestHandleExecutionRejectsForeignOwner(t *testing.T) {
	service := newTestService(t, nil)
	if err := service.sessions.Create("", "mine"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/sessions/mine/execution", nil)
	req.SetPathValue("id", "mine")
	req = req.WithContext(auth.WithOwner(req.Context(), "feishu:ou_other"))
	rec := httptest.NewRecorder()
	service.handleExecution(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("handleExecution status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
}
