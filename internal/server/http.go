package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"my-eino-app/internal/auth"
	"my-eino-app/internal/checkpoint"
	"my-eino-app/internal/execution"
	"my-eino-app/internal/session"
)

type chatRequest struct {
	SessionID string `json:"session_id"`
	Query     string `json:"query"`
	Skill     string `json:"skill"`
}
type approvalRequest struct {
	Approved bool `json:"approved"`
}
type response struct {
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

var errUnsignedSession = errors.New("session id was not issued by server")

// executionSnapshot 是执行记录查询接口的返回体。
type executionSnapshot struct {
	Runs   []execution.Run   `json:"runs"`
	Events []execution.Event `json:"events"`
}

// writeSessionError 统一映射会话访问错误。
//
// 归属他人是越权，必须与「参数错误」区分：403 而不是 400。这里不回 404，
// 是为了让调用方能明确知道自己拿到的是别人的会话 id（内部系统，便于排查），
// 同时请求本身已经被拒绝。ErrForeignSession 是唯一需要特殊处理的一类。
func writeSessionError(w http.ResponseWriter, err error) {
	if errors.Is(err, session.ErrForeignSession) {
		writeJSON(w, http.StatusForbidden, response{Error: "session belongs to another user"})
		return
	}
	writeJSON(w, 400, response{Error: err.Error()})
}

// ownerOf 返回当前请求的身份；认证关闭时为空串（单用户模式）。
func ownerOf(r *http.Request) string { return auth.OwnerFromContext(r.Context()) }

// checkpointStoreFor 返回当前请求身份对应的 checkpoint 视图。
//
// checkpoint 与审批元数据按 owner 分目录存放：即便某个 session id 泄露，
// 另一身份的请求也读不到对应的中断内容与审批参数（纵深防御）。
func (s *Service) checkpointStoreFor(ctx context.Context) *checkpoint.FileStore {
	return s.checkpoints.For(auth.OwnerFromContext(ctx))
}

// checkSessionOwner 校验 session 归属，越权返回 session.ErrForeignSession。
//
// 「不存在」与「归属他人」必须分开：前者是新会话的正常起点（沿用 id 继续），
// 后者是越权，必须 403。execution 与 approval 这两条只按 id 取数据的路由
// 依赖本方法 —— 它们的查询条件里没有 owner，不显式比对就等于「知道 id 即可读」。
func (s *Service) checkSessionOwner(ctx context.Context, id string) error {
	owner, exists, err := s.sessions.OwnerOf(id)
	if err != nil {
		return err
	}
	if !exists || owner == auth.OwnerFromContext(ctx) {
		return nil
	}
	return session.ErrForeignSession
}

// resolveSessionID 决定本次请求使用的 session id。
//
// 规则（步骤 2.7）：
//   - 调用方没带 id → 服务端生成；
//   - 带的 id 必须已经由 POST /sessions 签发且属于当前用户；
//   - 未登记 id 拒绝，防止客户端继续自行选择 checkpoint / execution 的键；
//   - 属于别人 → ErrForeignSession（403）。
func (s *Service) resolveSessionID(ctx context.Context, requested string) (string, error) {
	if strings.TrimSpace(requested) == "" {
		return uuid.NewString(), nil
	}
	owner, exists, err := s.sessions.OwnerOf(requested)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", errUnsignedSession
	}
	if owner != auth.OwnerFromContext(ctx) {
		return "", session.ErrForeignSession
	}
	return requested, nil
}

// handleCreateSession 由服务端签发一个新的 session id 并登记归属。
//
// 客户端不再自己造 id —— 「知道别人的 id 就能读他的审批内容、恢复他的执行」
// 正是步骤 2.7 要堵的越权根源。
func (s *Service) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	id := uuid.NewString()
	if err := s.sessions.Create(ownerOf(r), id); err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, response{Data: map[string]string{"id": id}})
}

// throttle 对单次请求做 per-user 限流（步骤 5.2）。
//
// 必须包在 protected 内部：owner 由认证中间件注入 context，包在外面
// 就只能按 IP 限流，同一出口 IP 后面的所有人会互相挤掉配额。
//
// ⚠️ 这是单副本限流，计数在进程内。多副本部署时每个副本各算一份配额，
// 需要换成 Redis 计数器 —— 当前不部署多副本，登记为后续项。
func (s *Service) throttle(h http.Handler) http.Handler {
	if s.chatLimiter == nil {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := "user:" + auth.OwnerFromContext(r.Context())
		if key == "user:" {
			// 认证关闭（或白名单路径）时没有身份，退回 IP 维度。
			// 不采信 X-Forwarded-For：它由客户端任意伪造，采信等于把配额让给攻击者。
			key = "ip:" + clientIP(r)
		}
		if ok, retryAfter := s.chatLimiter.Allow(key); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds()+0.999)))
			writeJSON(w, http.StatusTooManyRequests, response{Error: "请求过于频繁，请稍后再试"})
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	// 认证路由（auth.enabled 关闭时为 no-op）：/auth/login、/auth/callback、
	// /auth/logout 免认证；/auth/me 需要登录。
	s.registerAuthRoutes(mux)
	// 前端与 API 同源提供，浏览器无需额外配置 CORS 或单独启动前端服务。
	// 需要登录：未登录的浏览器导航会被 302 到 /auth/login。
	mux.Handle("GET /", s.protected(webHandler()))
	// /health 回传 debug 状态，前端据此决定是否显示技能入口；同时回传可用的
	// 登录方式，登录页据此只渲染已启用的入口。免认证白名单。
	// 两项都必须是「当前真的可用」：feishu 还要求 auth.enabled，
	// 否则会出现「页面显示飞书入口、点进去 404」。
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, response{Data: map[string]any{
			"status": "ok",
			"debug":  s.debugEnabled(),
			"login": map[string]bool{
				"feishu": s.authEnabled() && s.feishuReady(),
				"local":  s.localLoginEnabled(),
			},
		}})
	})
	// /health/ready 是 readiness：逐项探测 MySQL / Milvus / Ollama，任一不通返回 503，
	// 供 compose 的 app 服务健康检查使用（Dockerfile 的 HEALTHCHECK 仍打 /health）。
	// 与 /health 一样免认证。
	mux.HandleFunc("GET /health/ready", s.handleReady)
	mux.Handle("GET /metrics", s.protected(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, response{Data: s.Stats()}) })))
	// 技能目录属于内部实现，非调试模式不注册该路由，请求直接 404。
	if s.debugEnabled() {
		mux.Handle("GET /skills", s.protected(http.HandlerFunc(s.handleSkills)))
	}
	// 只有这两条路由会真正调用模型（也就是真正花钱、真正占并发），
	// 其余路由是只读或轻量，不挂限流。throttle 在 protected 内部。
	mux.Handle("POST /chat", s.protected(s.throttle(http.HandlerFunc(s.handleChat))))
	mux.Handle("GET /sessions", s.protected(http.HandlerFunc(s.handleSessions)))
	// 会话 id 由服务端签发：前端不再自己造 id（app.js 的 makeSessionId 已移除）。
	mux.Handle("POST /sessions", s.protected(http.HandlerFunc(s.handleCreateSession)))
	mux.Handle("GET /sessions/{id}", s.protected(http.HandlerFunc(s.handleGetSession)))
	mux.Handle("GET /sessions/{id}/execution", s.protected(http.HandlerFunc(s.handleExecution)))
	mux.Handle("DELETE /sessions/{id}", s.protected(http.HandlerFunc(s.handleDeleteSession)))
	mux.Handle("POST /sessions/{id}/approval", s.protected(http.HandlerFunc(s.handleApproval)))
	// WebSocket 认证只能靠 Cookie（浏览器无法自定义 Header）；
	// 中间件包在 mux 内部，最外层的超时豁免不受影响。
	mux.Handle("GET /ws", s.protected(s.throttle(http.HandlerFunc(s.handleWebSocket))))
	s.registerMemoryRoutes(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// WebSocket 是长连接；其他 HTTP 请求统一受 runtime.request_timeout 控制。
		if r.URL.Path == "/ws" {
			mux.ServeHTTP(w, r)
			return
		}
		timeout := 2 * time.Minute
		if s.cfg != nil && s.cfg.Runtime.RequestTimeout > 0 {
			timeout = time.Duration(s.cfg.Runtime.RequestTimeout)
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Service) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, response{Error: err.Error()})
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		writeJSON(w, 400, response{Error: "query is required"})
		return
	}
	id, resolveErr := s.resolveSessionID(r.Context(), req.SessionID)
	if resolveErr != nil {
		writeSessionError(w, resolveErr)
		return
	}
	req.SessionID = id
	var output bytes.Buffer
	// 同步接口把事件收进内存后一次性返回：实时推送只由 WebSocket 提供。
	recorder := execution.NewRecorder(s.execCfg.MaxEventsPerRun)
	result, err := s.ChatWithSink(r.Context(), req.SessionID, req.Query, s.requestedSkill(req.Skill), &output, recorder)
	if err != nil {
		if errors.Is(err, session.ErrForeignSession) {
			writeSessionError(w, err)
			return
		}
		writeJSON(w, 500, response{Error: err.Error()})
		return
	}
	result.Answer = strings.TrimSpace(output.String())
	result.Events = execution.WithoutChunks(recorder.Events())
	if len(result.Events) == 0 {
		result.Events = nil
	}
	writeJSON(w, 200, response{Data: result})
}

func (s *Service) handleApproval(w http.ResponseWriter, r *http.Request) {
	var req approvalRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, response{Error: err.Error()})
		return
	}
	id := r.PathValue("id")
	// 审批票据本身已按 owner 分目录（拿别人的 id 也读不到），这里再补一层会话归属
	// 校验，是为了让越权请求得到明确的 403，而不是被误读成「该会话没有待审批操作」。
	if err := s.checkSessionOwner(r.Context(), id); err != nil {
		writeSessionError(w, err)
		return
	}
	var output bytes.Buffer
	recorder := execution.NewRecorder(s.execCfg.MaxEventsPerRun)
	result, err := s.ApproveWithSink(r.Context(), id, req.Approved, &output, recorder)
	if err != nil {
		writeJSON(w, 400, response{Error: err.Error()})
		return
	}
	result.Answer = strings.TrimSpace(output.String())
	result.Events = execution.WithoutChunks(recorder.Events())
	if len(result.Events) == 0 {
		result.Events = nil
	}
	writeJSON(w, 200, response{Data: result})
}

// handleSessions 只返回当前登录用户的会话；owner 来自认证中间件注入的 context。
func (s *Service) handleSessions(w http.ResponseWriter, r *http.Request) {
	items, err := s.sessions.List(ownerOf(r))
	if err != nil {
		writeJSON(w, 500, response{Error: err.Error()})
		return
	}
	writeJSON(w, 200, response{Data: items})
}

func (s *Service) handleGetSession(w http.ResponseWriter, r *http.Request) {
	messages, err := s.sessions.Load(ownerOf(r), r.PathValue("id"))
	if err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, 200, response{Data: messages})
}

// handleExecution 返回会话的执行记录与事件，供刷新回放与断线补取。
// 未开启执行事件时返回空结构而不是错误，前端据此隐藏面板。
//
// 步骤 2.8：execution_runs 只靠 conversation_id 关联会话、没有 owner 冗余列，
// 所以归属校验必须在这里做 —— 查出记录之前先确认这个会话就是调用方的。
func (s *Service) handleExecution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.checkSessionOwner(r.Context(), id); err != nil {
		writeSessionError(w, err)
		return
	}
	snapshot := executionSnapshot{Runs: []execution.Run{}, Events: []execution.Event{}}
	if s.executions != nil {
		runID := r.URL.Query().Get("run_id")
		after, _ := strconv.ParseInt(r.URL.Query().Get("after_sequence"), 10, 64)
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil || limit <= 0 || limit > 2000 {
			limit = 500
		}
		runs, err := s.executions.ListRuns(r.Context(), id, 50)
		if err != nil {
			writeJSON(w, 400, response{Error: err.Error()})
			return
		}
		events, err := s.executions.ListEvents(r.Context(), id, runID, after, limit)
		if err != nil {
			writeJSON(w, 400, response{Error: err.Error()})
			return
		}
		if runs != nil {
			snapshot.Runs = runs
		}
		if events != nil {
			snapshot.Events = events
		}
	}
	writeJSON(w, 200, response{Data: snapshot})
}

func (s *Service) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.sessions.Delete(ownerOf(r), id); err != nil {
		writeSessionError(w, err)
		return
	}
	// checkpoint 与审批元数据按 owner 分目录，删除必须落在同一身份下，
	// 否则删掉的是别人的中断现场。
	checks := s.checkpointStoreFor(r.Context())
	_ = checks.Delete(r.Context(), id)
	_ = checks.ClearApproval(id)
	// 执行记录必须跟随会话一起清理，否则会留下孤儿 run/event。
	_ = s.DeleteExecutions(r.Context(), id)
	writeJSON(w, 200, response{Data: map[string]bool{"deleted": true}})
}

func (s *Service) handleSkills(w http.ResponseWriter, _ *http.Request) {
	names, err := s.skills.List()
	if err != nil {
		writeJSON(w, 500, response{Error: err.Error()})
		return
	}
	writeJSON(w, 200, response{Data: names})
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
