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

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	// 认证路由（auth.enabled 关闭时为 no-op）：/auth/login、/auth/callback、
	// /auth/logout 免认证；/auth/me 需要登录。
	s.registerAuthRoutes(mux)
	// 前端与 API 同源提供，浏览器无需额外配置 CORS 或单独启动前端服务。
	// 需要登录：未登录的浏览器导航会被 302 到 /auth/login。
	mux.Handle("GET /", s.protected(webHandler()))
	// /health 回传 debug 状态，前端据此决定是否显示技能入口。免认证白名单。
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, response{Data: map[string]any{"status": "ok", "debug": s.debugEnabled()}})
	})
	mux.Handle("GET /metrics", s.protected(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, response{Data: s.Stats()}) })))
	// 技能目录属于内部实现，非调试模式不注册该路由，请求直接 404。
	if s.debugEnabled() {
		mux.Handle("GET /skills", s.protected(http.HandlerFunc(s.handleSkills)))
	}
	mux.Handle("POST /chat", s.protected(http.HandlerFunc(s.handleChat)))
	mux.Handle("GET /sessions", s.protected(http.HandlerFunc(s.handleSessions)))
	mux.Handle("GET /sessions/{id}", s.protected(http.HandlerFunc(s.handleGetSession)))
	mux.Handle("GET /sessions/{id}/execution", s.protected(http.HandlerFunc(s.handleExecution)))
	mux.Handle("DELETE /sessions/{id}", s.protected(http.HandlerFunc(s.handleDeleteSession)))
	mux.Handle("POST /sessions/{id}/approval", s.protected(http.HandlerFunc(s.handleApproval)))
	// WebSocket 认证只能靠 Cookie（浏览器无法自定义 Header）；
	// 中间件包在 mux 内部，最外层的超时豁免不受影响。
	mux.Handle("GET /ws", s.protected(http.HandlerFunc(s.handleWebSocket)))
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
	if req.SessionID == "" {
		req.SessionID = uuid.NewString()
	}
	var output bytes.Buffer
	// 同步接口把事件收进内存后一次性返回：实时推送只由 WebSocket 提供。
	recorder := execution.NewRecorder(s.execCfg.MaxEventsPerRun)
	result, err := s.ChatWithSink(r.Context(), req.SessionID, req.Query, s.requestedSkill(req.Skill), &output, recorder)
	if err != nil {
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
	var output bytes.Buffer
	recorder := execution.NewRecorder(s.execCfg.MaxEventsPerRun)
	result, err := s.ApproveWithSink(r.Context(), r.PathValue("id"), req.Approved, &output, recorder)
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
func (s *Service) handleExecution(w http.ResponseWriter, r *http.Request) {
	snapshot := executionSnapshot{Runs: []execution.Run{}, Events: []execution.Event{}}
	if s.executions != nil {
		id := r.PathValue("id")
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
	_ = s.checkpoints.Delete(r.Context(), id)
	_ = s.checkpoints.ClearApproval(id)
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
