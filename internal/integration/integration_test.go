//go:build integration

package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"my-eino-app/internal/config"
	"my-eino-app/internal/eino/rag"
	"my-eino-app/internal/health"
)

// TestOllamaAndMilvusRAG 是显式端到端测试，普通 go test ./... 不连接外部服务。
func TestOllamaAndMilvusRAG(t *testing.T) {
	if os.Getenv("RUN_E2E") != "1" {
		t.Skip("set RUN_E2E=1 to run")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := health.CheckOllama(ctx, cfg.OpenAI.BaseURL, cfg.OpenAI.Model, cfg.RAG.Embedding.Model, cfg.RAG.Dimension, time.Minute); err != nil {
		t.Fatal(err)
	}
	store, err := rag.NewFromConfig(ctx, cfg.RAG)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := rag.LoadDocuments(cfg.RAG.DocumentDir, cfg.RAG.ChunkSize, cfg.RAG.ChunkOverlap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Index(ctx, docs); err != nil {
		t.Fatal(err)
	}
	results, err := store.Search(ctx, "星河系统的幸运数字是什么", cfg.RAG.TopK)
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range results {
		if doc != nil && strings.Contains(doc.Content, "7319") {
			return
		}
	}
	t.Fatal("expected fact 7319 in retrieval results")
}
