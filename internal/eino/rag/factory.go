package rag

import (
	"context"
	"fmt"

	openaiembedding "github.com/cloudwego/eino-ext/components/embedding/openai"

	"my-eino-app/internal/config"
)

// TargetFingerprint includes routing and index generation, never credentials.
func TargetFingerprint(c config.RAG) string {
	if c.Store == "redis" {
		return fmt.Sprintf("v2|%s|db=%d|%s|prefix=%s|embedding=%s", c.Redis.Addr, c.Redis.DB, c.Redis.Index, c.Redis.KeyPrefix, c.Embedding.BaseURL)
	}
	return fmt.Sprintf("v2|%s|database=default|%s|embedding=%s", c.Milvus.Address, c.Milvus.Collection, c.Embedding.BaseURL)
}

// NewFromConfig 根据 store 字段创建 Redis 或 Milvus 向量存储。
func NewFromConfig(ctx context.Context, cfg config.RAG) (Store, error) {
	// Redis 和 Milvus 共用同一个 Embedding 实例，确保入库和检索向量一致。
	embedder, err := openaiembedding.NewEmbedder(ctx, &openaiembedding.EmbeddingConfig{
		APIKey: cfg.Embedding.APIKey, Model: cfg.Embedding.Model, BaseURL: cfg.Embedding.BaseURL,
	})
	if err != nil {
		return nil, fmt.Errorf("new embedder: %w", err)
	}
	switch cfg.Store {
	case "redis":
		// 上层只拿到 Store 接口，不需要了解 Redis 的实现细节。
		return NewRedisStore(ctx, RedisConfig{
			Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB,
			Index: cfg.Redis.Index, KeyPrefix: cfg.Redis.KeyPrefix,
			Dimension: cfg.Dimension, TopK: cfg.TopK,
		}, embedder)
	case "milvus":
		return NewMilvusStore(ctx, MilvusConfig{
			Address: cfg.Milvus.Address, Username: cfg.Milvus.Username,
			Password: cfg.Milvus.Password, Collection: cfg.Milvus.Collection,
			Dimension: cfg.Dimension, TopK: cfg.TopK,
		}, embedder)
	default:
		return nil, fmt.Errorf("unsupported rag store %q", cfg.Store)
	}
}
