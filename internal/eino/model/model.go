// Package model 负责构造 ChatModel 组件。
// 业务代码只依赖 eino 的 model.ToolCallingChatModel 接口，
// 将来切换底层实现（openai / ark / ollama 原生）只需改这里。
package model

import (
	"context"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"

	"my-eino-app/internal/config"
)

// NewChatModel 根据 config.yaml 构造一个 OpenAI 兼容的 ChatModel。
func NewChatModel(ctx context.Context, cfg *config.Config) (einomodel.ToolCallingChatModel, error) {
	selected, err := cfg.ResolveModelProfile("")
	if err != nil {
		return nil, err
	}
	modelConfig := &openai.ChatModelConfig{
		APIKey:  selected.APIKey,
		Model:   selected.Model,
		BaseURL: selected.BaseURL,
		Timeout: time.Duration(cfg.Runtime.RequestTimeout),
	}
	// 限制回答长度，防止本地模型长时间占用 CPU/GPU；0 表示沿用模型默认值。
	if selected.MaxCompletionTokens > 0 {
		modelConfig.MaxCompletionTokens = &selected.MaxCompletionTokens
	}
	// qwen3.5 等思考型模型可设为 none。Ollama 的 OpenAI 兼容接口会把该字段
	// 转换为关闭思考模式，从而避免简单 RAG 回答把时间耗在隐藏推理上。
	if selected.ReasoningEffort != "" {
		modelConfig.ReasoningEffort = openai.ReasoningEffortLevel(selected.ReasoningEffort)
	}
	inner, err := openai.NewChatModel(ctx, modelConfig)
	if err != nil {
		return nil, err
	}
	return NewRetryChatModel(inner, RetryConfig{
		MaxAttempts: cfg.Retry.MaxAttempts,
		BaseDelay:   time.Duration(cfg.Retry.BaseDelay),
		MaxDelay:    time.Duration(cfg.Retry.MaxDelay),
	}), nil
}

// NewChatModelByID 根据指定的模型 ID 构造 ChatModel。
// 如果 modelID 为空，使用 cfg.ActiveModel；如果找不到对应 ID，使用默认配置。
func NewChatModelByID(ctx context.Context, cfg *config.Config, modelID string) (einomodel.ToolCallingChatModel, error) {
	selected, err := cfg.ResolveModelProfile(modelID)
	if err != nil {
		return nil, err
	}
	modelConfig := &openai.ChatModelConfig{
		APIKey:  selected.APIKey,
		Model:   selected.Model,
		BaseURL: selected.BaseURL,
		Timeout: time.Duration(cfg.Runtime.RequestTimeout),
	}
	if selected.MaxCompletionTokens > 0 {
		modelConfig.MaxCompletionTokens = &selected.MaxCompletionTokens
	}
	if selected.ReasoningEffort != "" {
		modelConfig.ReasoningEffort = openai.ReasoningEffortLevel(selected.ReasoningEffort)
	}
	inner, err := openai.NewChatModel(ctx, modelConfig)
	if err != nil {
		return nil, err
	}
	return NewRetryChatModel(inner, RetryConfig{
		MaxAttempts: cfg.Retry.MaxAttempts,
		BaseDelay:   time.Duration(cfg.Retry.BaseDelay),
		MaxDelay:    time.Duration(cfg.Retry.MaxDelay),
	}), nil
}
