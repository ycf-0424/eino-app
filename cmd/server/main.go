// Command server 启动 HTTP/WebSocket Agent 服务。
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"my-eino-app/internal/config"
	"my-eino-app/internal/health"
	"my-eino-app/internal/observability"
	appserver "my-eino-app/internal/server"
)

func main() {
	addr := flag.String("addr", ":18180", "HTTP 监听地址")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	if cfg.Debug {
		observability.EnableDebug()
	}
	if cfg.Ollama.HealthCheck && cfg.RAG.Enabled {
		if err := health.CheckOllama(ctx, cfg.OpenAI.BaseURL, cfg.OpenAI.Model, cfg.RAG.Embedding.Model, cfg.RAG.Dimension, time.Duration(cfg.Ollama.Timeout)); err != nil {
			fail(err)
		}
	}
	service, err := appserver.NewService(ctx, cfg)
	if err != nil {
		fail(err)
	}
	server := &http.Server{Addr: *addr, Handler: service.Handler(), ReadHeaderTimeout: 10 * time.Second}
	defer service.Close()
	// -addr 既可能是 ":8080"（绑定全部网卡），也可能是 "127.0.0.1:18181"。
	// 只有前者需要在展示时补上主机名，否则会拼出 localhost127.0.0.1:18181。
	displayAddr := *addr
	if len(displayAddr) > 0 && displayAddr[0] == ':' {
		displayAddr = "localhost" + displayAddr
	}
	fmt.Printf("server listening on http://%s\n", displayAddr)
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			fail(err)
		}
		if err := <-errCh; err != nil && err != http.ErrServerClosed {
			fail(err)
		}
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			fail(err)
		}
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
