package server

import (
	"net/http"
)

func (s *Service) registerMemoryRoutes(mux *http.ServeMux) {
	mux.Handle("GET /memory/facts", s.protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.memoryAvailable(w) {
			return
		}
		facts, err := s.memories.Repo.List(r.Context())
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
		status, err := s.memories.Repo.Status(r.Context(), r.PathValue("turn_id"))
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
		if err := s.memories.Repo.Revoke(r.Context(), r.PathValue("id")); err != nil {
			writeJSON(w, 404, response{Error: "memory not found or deletion failed"})
			return
		}
		writeJSON(w, 200, response{Data: map[string]any{"revoked": true, "vector_cleanup": "queued"}})
	})))
	mux.Handle("POST /memory/jobs/{id}/retry", s.protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.memoryAvailable(w) {
			return
		}
		if err := s.memories.Repo.Retry(r.Context(), r.PathValue("id")); err != nil {
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
