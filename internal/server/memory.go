package server

import (
	"net/http"

	"my-eino-app/internal/auth"
	"my-eino-app/internal/memory"
)

// memoryRepo 返回绑定到当前请求身份的记忆 Repository。
//
// 这些路由都是「读/改我自己的记忆」，因此一律按请求身份派生：直接使用
// s.memories.Repo 拿到的是配置里那个 owner（local_single_user 的身份），
// 多用户模式下等于让 A 读、删 B 的记忆（2026-09-17 实测确认的同类缺陷）。
// OwnerScope 负责两种身份模式的换算，单用户模式下请求侧没有登录态，
// 必须映射回配置 owner，否则读不到自己的记忆。
func (s *Service) memoryRepo(r *http.Request) *memory.Repository {
	owner := auth.OwnerFromContext(r.Context())
	return s.memories.Repo.For(s.memories.OwnerScope(owner))
}

func (s *Service) registerMemoryRoutes(mux *http.ServeMux) {
	mux.Handle("GET /memory/facts", s.protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.memoryAvailable(w) {
			return
		}
		facts, err := s.memoryRepo(r).List(r.Context())
		if err != nil {
			writeJSON(w, 500, response{Error: "memory query failed"})
			return
		}
		writeJSON(w, 200, response{Data: facts})
	})))
	mux.Handle("GET /memory/turns/{turn_id}", s.protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.memoryAvailable(w) {
			return
		}
		status, err := s.memoryRepo(r).Status(r.Context(), r.PathValue("turn_id"))
		if err != nil {
			writeJSON(w, 404, response{Error: "memory turn not found"})
			return
		}
		writeJSON(w, 200, response{Data: status})
	})))
	mux.Handle("DELETE /memory/facts/{id}", s.protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.memoryAvailable(w) {
			return
		}
		if err := s.memoryRepo(r).Revoke(r.Context(), r.PathValue("id")); err != nil {
			writeJSON(w, 404, response{Error: "memory not found or deletion failed"})
			return
		}
		writeJSON(w, 200, response{Data: map[string]any{"revoked": true, "vector_cleanup": "queued"}})
	})))
	mux.Handle("POST /memory/jobs/{id}/retry", s.protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.memoryAvailable(w) {
			return
		}
		if err := s.memoryRepo(r).Retry(r.Context(), r.PathValue("id")); err != nil {
			writeJSON(w, 404, response{Error: "failed job not found"})
			return
		}
		writeJSON(w, 200, response{Data: map[string]bool{"queued": true}})
	})))
}
func (s *Service) memoryAvailable(w http.ResponseWriter) bool {
	if s.memories == nil {
		writeJSON(w, 503, response{Error: "automatic memory is disabled"})
		return false
	}
	return true
}
