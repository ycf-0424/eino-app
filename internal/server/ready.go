// ready.go 实现 GET /health/ready（步骤 5.4）。
//
// 与 /health 的区别见 internal/health/ready.go 的包注释：这里是 readiness，
// 外部依赖不可用时返回 503，让编排把流量摘走，而不是把进程重启一遍。
//
// 放在 server 包而不是 health 包：探测什么依赖取决于本次启动实际装配了什么
// （session.store=file 时没有 MySQL 可探，rag.enabled=false 时没有 Milvus 与
// Ollama 可探）。探测逻辑在 health 包里保持纯粹，这里只负责挑依赖与出响应。
package server

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"my-eino-app/internal/health"
)

// handleReady 逐项探测外部依赖并返回结果。
//
// 免认证：编排探活时没有 Cookie。它只暴露「哪个依赖不通」这类运维信息，
// 不返回任何业务数据或凭据，与 /health 同级别。
func (s *Service) handleReady(w http.ResponseWriter, r *http.Request) {
	// 未装配任何外部依赖（纯本地文件模式）时，ready 就等于 liveness：
	// 返回空列表而不是硬编码一个假的 ok 项，避免调用方误以为探过什么。
	checks := health.Run(r.Context(), health.DefaultReadyTimeout, s.readinessChecks()...)
	status := http.StatusOK
	body := map[string]any{"status": "ok", "checks": checks}
	if !health.AllOK(checks) {
		status = http.StatusServiceUnavailable
		body["status"] = "unavailable"
	}
	writeJSON(w, status, response{Data: body})
}

// readinessChecks 按本次实际装配情况构造探测项。
func (s *Service) readinessChecks() []health.Checker {
	if s.cfg == nil {
		return nil
	}
	cfg := s.cfg
	checkers := make([]health.Checker, 0, 3)

	// MySQL：只有 session.store=mysql 才真的连着它。
	if cfg.Session.Store == "mysql" && s.sessions != nil {
		if db := s.sessions.DB(); db != nil {
			checkers = append(checkers, health.Checker{Name: "mysql", Check: mysqlPing(db, health.DefaultReadyTimeout)})
		}
	}
	// Milvus：RAG 使用 milvus，或自动记忆启用时都必须探测。记忆索引始终由
	// NewMilvusIndex 创建，即使 RAG 文档存储将来切成 redis 也仍依赖这里。
	milvusRequired := (cfg.RAG.Enabled && cfg.RAG.Store == "milvus") || cfg.Memory.Enabled
	if milvusRequired && cfg.RAG.Milvus.Address != "" {
		checkers = append(checkers, health.Checker{Name: "milvus", Check: health.DialChecker(cfg.RAG.Milvus.Address)})
	}
	// Ollama：只有 Embedding 实际指向本地 Ollama 时才探测。生产环境的
	// Embedding 可以是任意 OpenAI 兼容云端服务，不应被拼接成 /api/tags。
	if (cfg.RAG.Enabled || cfg.Memory.Enabled) && health.IsOllamaBaseURL(cfg.RAG.Embedding.BaseURL) {
		if url := health.OllamaTagsURL(cfg.RAG.Embedding.BaseURL); url != "" {
			checkers = append(checkers, health.Checker{Name: "ollama", Check: health.HTTPChecker(url)})
		}
	}
	return checkers
}

// mysqlPing 把 PingContext 包成探测函数：连接池拿到不可用连接时也会报错，
// 因此探测结果反映的是「现在能不能真的执行一次查询」，而不是「启动时连上过」。
func mysqlPing(db *sql.DB, timeout time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		pingCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return db.PingContext(pingCtx)
	}
}
