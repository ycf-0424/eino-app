// Package embedding 统一嵌入模型的构造。
//
// 为什么集中（EXECUTION-PLAN 步骤 3.3.1）：rag 与 memory 原本各自 new 一次 Embedder，
// 两处参数一旦漂移，就会出现「入库用 A 模型、检索用 B 模型」的静默降级 —— 向量空间
// 不一致时检索不会报错，只会变差。集中构造让两者永远拿到同一份参数，并且让
// internal/memory 不再需要 import cloudwego/eino-ext 的 embedding 组件。
package embedding

import (
	"context"
	"fmt"

	openaiembedding "github.com/cloudwego/eino-ext/components/embedding/openai"

	"my-eino-app/internal/config"
	"my-eino-app/internal/eino"
)

// NewFromConfig 按配置构造 OpenAI 兼容的 Embedder。
// 本项目的 embedding.base_url 指向本地 Ollama（bge-m3）或火山方舟，两者都是
// OpenAI 兼容协议，因此共用这一个构造函数。
//
// 返回接口而非 *openaiembedding.Embedder：调用方只需要 embedding.Embedder 接口，
// 将来换实现不必改签名（openaiembedding.Embedder 是实现结构体，不是接口）。
func NewFromConfig(ctx context.Context, cfg config.OpenAI) (eino.Embedder, error) {
	embedder, err := openaiembedding.NewEmbedder(ctx, &openaiembedding.EmbeddingConfig{
		APIKey:  cfg.APIKey,
		Model:   cfg.Model,
		BaseURL: cfg.BaseURL,
	})
	if err != nil {
		return nil, fmt.Errorf("new embedder: %w", err)
	}
	return embedder, nil
}
