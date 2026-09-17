// Command indexer 将 docs/knowledge 中的文档切分、向量化并写入向量数据库。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"my-eino-app/internal/config"
	"my-eino-app/internal/eino/rag"
	"my-eino-app/internal/health"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// indexer 与控制台共用同一份配置，保证 Embedding、维度和向量库一致。
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !cfg.RAG.Enabled {
		fmt.Fprintln(os.Stderr, "rag is disabled")
		os.Exit(1)
	}
	if cfg.Ollama.HealthCheck {
		if err := health.CheckOllama(ctx, cfg.OpenAI.BaseURL, cfg.OpenAI.Model, cfg.RAG.Embedding.Model, cfg.RAG.Dimension, time.Duration(cfg.Ollama.Timeout)); err != nil {
			fmt.Fprintln(os.Stderr, "ollama:", err)
			os.Exit(1)
		}
	}
	// Milvus/Redis 不可用时应快速失败，避免 indexer 使用 Background context 永久等待。
	storeContext, cancelStore := context.WithTimeout(ctx, 30*time.Second)
	store, err := rag.NewFromConfig(storeContext, cfg.RAG)
	cancelStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	target := rag.TargetFingerprint(cfg.RAG)
	indexer, err := rag.NewIndexer(store, rag.IndexOptions{
		DocumentDir: cfg.RAG.DocumentDir, EmbeddingModel: cfg.RAG.Embedding.Model, Dimension: cfg.RAG.Dimension,
		ChunkSize: cfg.RAG.ChunkSize, ChunkOverlap: cfg.RAG.ChunkOverlap, Store: cfg.RAG.Store, Target: target,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	report, err := indexer.Run(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("indexed %d changed chunks (%d missing, %d removed), total=%d\n", report.IndexedChunks, report.MissingChunks, report.RemovedChunks, report.TotalChunks)
}
