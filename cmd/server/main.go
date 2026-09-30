// Command server 启动 HTTP/WebSocket Agent 服务。
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"my-eino-app/internal/config"
	"my-eino-app/internal/eino/observability"
	"my-eino-app/internal/health"
	appserver "my-eino-app/internal/server"
)

func main() {
	addr := flag.String("addr", ":18180", "HTTP 监听地址")
	flag.Parse()
	// 必须同时捕获 SIGTERM：docker stop 默认发的是 SIGTERM，只监听 os.Interrupt
	// 会导致容器停机时走不到下面的 server.Shutdown，10 秒优雅期与 defer service.Close()
	// 全部失效，正在跑的请求被硬切、执行事件可能丢终态。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	if cfg.Debug {
		observability.EnableDebug()
	}
	if cfg.Ollama.HealthCheck && cfg.RAG.Enabled && health.ShouldCheckOllama(cfg.OpenAI.BaseURL, cfg.RAG.Embedding.BaseURL) {
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
