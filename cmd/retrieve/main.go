// Command retrieve 只执行向量检索并打印原始分数，用于校准 score_threshold。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"my-eino-app/internal/config"
	"my-eino-app/internal/health"
	"my-eino-app/internal/rag"
)

func main() {
	flag.Parse()
	query := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if query == "" {
		fmt.Fprintln(os.Stderr, "query is required")
		os.Exit(2)
	}
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	if err := health.CheckOllama(ctx, cfg.OpenAI.BaseURL, cfg.OpenAI.Model, cfg.RAG.Embedding.Model, cfg.RAG.Dimension, time.Duration(cfg.Ollama.Timeout)); err != nil {
		fail(err)
	}
	store, err := rag.NewFromConfig(ctx, cfg.RAG)
	if err != nil {
		fail(err)
	}
	docs, err := store.Search(ctx, query, cfg.RAG.TopK)
	if err != nil {
		fail(err)
	}
	for _, doc := range docs {
		fmt.Printf("score=%.6f id=%s source=%v\n", doc.Score(), doc.ID, doc.MetaData["source"])
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
