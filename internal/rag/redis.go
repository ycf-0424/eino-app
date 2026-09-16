package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	redisindexer "github.com/cloudwego/eino-ext/components/indexer/redis"
	redisretriever "github.com/cloudwego/eino-ext/components/retriever/redis"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/schema"
	rediscli "github.com/redis/go-redis/v9"
)

const contentField, metadataField, vectorField, distanceField = "content", "metadata", "content_vector", "distance"

// RedisStore 类型。
type RedisStore struct {
	// indexer 负责把 Eino Document 转换为 Redis Hash 和向量。
	indexer *redisindexer.Indexer
	// retriever 负责把查询文本向量化，并执行 Redis 向量检索。
	retriever *redisretriever.Retriever
	client    *rediscli.Client
	keyPrefix string
}

// Exists 返回 Redis 中实际存在的知识块 key。
func (s *RedisStore) Exists(ctx context.Context, ids []string) (map[string]bool, error) {
	result := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, s.keyPrefix+id)
	}
	pipe := s.client.Pipeline()
	checks := make([]*rediscli.IntCmd, len(keys))
	for i, key := range keys {
		checks[i] = pipe.Exists(ctx, key)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("check redis document ids: %w", err)
	}
	for i, check := range checks {
		if check.Val() > 0 {
			result[ids[i]] = true
		}
	}
	return result, nil
}

// ListIDs 列出当前 Redis key 前缀下的知识块 ID，用于清理孤儿记录。
func (s *RedisStore) ListIDs(ctx context.Context) ([]string, error) {
	var cursor uint64
	var ids []string
	for {
		keys, next, err := s.client.Scan(ctx, cursor, s.keyPrefix+"*", 500).Result()
		if err != nil {
			return nil, fmt.Errorf("list redis document ids: %w", err)
		}
		for _, key := range keys {
			ids = append(ids, strings.TrimPrefix(key, s.keyPrefix))
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return ids, nil
}

// RedisConfig 是 Redis 向量索引所需的连接、索引和向量参数。
type RedisConfig struct {
	Addr, Password, Index, KeyPrefix string
	DB, Dimension, TopK              int
}

// NewRedisStore 初始化 Redis 向量索引，并创建 Eino Indexer/Retriever。
func NewRedisStore(ctx context.Context, cfg RedisConfig, embedder embedding.Embedder) (*RedisStore, error) {
	client := rediscli.NewClient(&rediscli.Options{Addr: cfg.Addr, Password: cfg.Password, DB: cfg.DB, Protocol: 2})
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("connect Redis Stack: %w", err)
	}
	if err := ensureRedisIndex(ctx, client, cfg); err != nil {
		return nil, err
	}

	idx, err := redisindexer.NewIndexer(ctx, &redisindexer.IndexerConfig{
		Client: client, KeyPrefix: cfg.KeyPrefix, BatchSize: 10, Embedding: embedder,
		DocumentToHashes: func(_ context.Context, doc *schema.Document) (*redisindexer.Hashes, error) {
			meta, err := json.Marshal(doc.MetaData)
			if err != nil {
				return nil, err
			}
			return &redisindexer.Hashes{Key: doc.ID, Field2Value: map[string]redisindexer.FieldValue{
				contentField: {Value: doc.Content, EmbedKey: vectorField}, metadataField: {Value: meta},
			}}, nil
		},
	})
	if err != nil {
		return nil, err
	}
	rtr, err := redisretriever.NewRetriever(ctx, &redisretriever.RetrieverConfig{
		Client: client, Index: cfg.Index, Dialect: 2, TopK: cfg.TopK, VectorField: vectorField, Embedding: embedder,
		ReturnFields: []string{contentField, metadataField, distanceField},
		DocumentConverter: func(_ context.Context, doc rediscli.Document) (*schema.Document, error) {
			out := &schema.Document{ID: doc.ID, MetaData: map[string]any{}}
			out.Content = doc.Fields[contentField]
			_ = json.Unmarshal([]byte(doc.Fields[metadataField]), &out.MetaData)
			if distance, err := strconv.ParseFloat(doc.Fields[distanceField], 64); err == nil {
				out.WithScore(1 - distance)
			}
			return out, nil
		},
	})
	if err != nil {
		return nil, err
	}
	return &RedisStore{indexer: idx, retriever: rtr, client: client, keyPrefix: cfg.KeyPrefix}, nil
}

// DeleteIDs 删除 Redis Hash，向量索引会自动同步。
func (s *RedisStore) DeleteIDs(ctx context.Context, ids []string) error {
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, s.keyPrefix+id)
	}
	if len(keys) == 0 {
		return nil
	}
	return s.client.Del(ctx, keys...).Err()
}

func (s *RedisStore) Index(ctx context.Context, docs []*schema.Document) ([]string, error) {
	// Eino Indexer 会调用同一个 Embedding 模型生成文档向量后写入 Redis。
	return s.indexer.Store(ctx, docs)
}

// Search 使用 Redis 的向量索引查找与问题最相似的文档。
func (s *RedisStore) Search(ctx context.Context, query string, _ int) ([]*schema.Document, error) {
	return s.retriever.Retrieve(ctx, query)
}

func ensureRedisIndex(ctx context.Context, client *rediscli.Client, cfg RedisConfig) error {
	if _, err := client.Do(ctx, "FT.INFO", cfg.Index).Result(); err == nil {
		return nil
	} else if !strings.Contains(err.Error(), "Unknown index") {
		return err
	}
	return client.Do(ctx, "FT.CREATE", cfg.Index, "ON", "HASH", "PREFIX", "1", cfg.KeyPrefix, "SCHEMA",
		contentField, "TEXT", metadataField, "TEXT", vectorField, "VECTOR", "FLAT", "6", "TYPE", "FLOAT32",
		"DIM", cfg.Dimension, "DISTANCE_METRIC", "COSINE").Err()
}
