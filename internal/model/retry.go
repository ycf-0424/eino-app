package model

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// RetryConfig 是模型层实际使用的重试参数。
type RetryConfig struct {
	// MaxAttempts 包含首次请求。
	MaxAttempts int
	// BaseDelay 控制指数退避的起点。
	BaseDelay time.Duration
	// MaxDelay 防止等待时间无限增长。
	MaxDelay time.Duration
}

// RetryChatModel 为 Eino 模型增加统一的请求重试能力。
// Stream 只在创建流失败时重试；流已经开始消费后若中途失败，
// 由上层结束本次请求，避免重新请求导致回答重复输出。
type RetryChatModel struct {
	inner  einomodel.ToolCallingChatModel
	config RetryConfig
}

// NewRetryChatModel 创建带重试能力的 ChatModel 包装器。
func NewRetryChatModel(inner einomodel.ToolCallingChatModel, config RetryConfig) einomodel.ToolCallingChatModel {
	return &RetryChatModel{inner: inner, config: config}
}

// Generate 以非流式方式调用底层模型，并在遇到可重试错误时自动重试。
//
// 方法会等待模型生成完整消息后再返回，适合不需要逐字输出的调用场景。
// 真正的请求仍由 inner.Generate 执行，RetryChatModel 只负责增加重试能力。
func (m *RetryChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	var result *schema.Message
	err := retry(ctx, m.config, func() error {
		var err error
		result, err = m.inner.Generate(ctx, input, opts...)
		return err
	})
	return result, err
}

// Stream 以流式方式调用底层模型，并在创建流失败时自动重试。
//
// 成功后返回 StreamReader，调用方需要持续调用 Recv 读取内容并在使用完毕后
// 关闭 reader。流一旦创建成功，后续 Recv 阶段的错误不会在这里重新发起请求，
// 以避免已经输出的回答被重复打印。
func (m *RetryChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	var stream *schema.StreamReader[*schema.Message]
	err := retry(ctx, m.config, func() error {
		var err error
		stream, err = m.inner.Stream(ctx, input, opts...)
		return err
	})
	return stream, err
}

// WithTools 为底层模型绑定工具，并返回一个新的带重试能力的模型包装器。
//
// 不修改当前 RetryChatModel，调用方可以为不同请求创建不同的工具集合；
// 新包装器会继续沿用原包装器的最大重试次数配置。
func (m *RetryChatModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	inner, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &RetryChatModel{inner: inner, config: m.config}, nil
}

// IsRetryableError 判断错误是否适合自动重试。
//
// 网络抖动、连接被重置、服务端暂时不可用和限流通常是临时问题，
// 重试有机会恢复；用户主动取消、请求超时以及参数错误则不应该盲目重试。
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// 上下文被取消通常代表用户主动终止，不应该继续发起请求。
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// 网络层错误通常是临时故障。
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// 不同 OpenAI 兼容服务返回的错误类型可能不同，这里补充检查常见文本。
	msg := strings.ToLower(err.Error())
	for _, keyword := range []string{
		"timeout",
		"connection reset",
		"connection refused",
		"server error",
		"rate limit",
		"too many requests",
		"502",
		"503",
		"504",
	} {
		if strings.Contains(msg, keyword) {
			return true
		}
	}

	return false
}

// Retry 执行一个可能失败的操作，最多尝试 maxAttempts 次。
// 重试等待时间采用指数退避：1 秒、2 秒、4 秒……，并且支持通过 ctx 中断等待。
func Retry(ctx context.Context, maxAttempts int, fn func() error) error {
	return retry(ctx, RetryConfig{MaxAttempts: maxAttempts, BaseDelay: time.Second, MaxDelay: 10 * time.Second}, fn)
}

func retry(ctx context.Context, config RetryConfig, fn func() error) error {
	maxAttempts := config.MaxAttempts
	if maxAttempts < 1 {
		return fmt.Errorf("maxAttempts must be greater than zero")
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// fn 是一次真实的模型请求；成功后立即结束重试循环。
		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		// 不可重试错误，或已经达到最大次数时立即返回。
		if !IsRetryableError(lastErr) || attempt == maxAttempts {
			return lastErr
		}

		delay := config.BaseDelay * time.Duration(1<<(attempt-1))
		// 指数结果不能超过配置的最大退避时间。
		if delay > config.MaxDelay {
			delay = config.MaxDelay
		}
		// 随机抖动避免多个并发请求在同一时刻同时重试。
		jitter := time.Duration(rand.Float64() * 0.25 * float64(delay))
		delay += jitter
		// 输出简洁的重试提示，便于命令行用户判断程序并未卡死。
		fmt.Fprintf(os.Stderr, "model request failed; retrying in %s (%d/%d): %v\n", delay, attempt, maxAttempts, lastErr)
		select {
		case <-time.After(delay):
			// 等待结束后进入下一轮循环。
		case <-ctx.Done():
			// 用户取消或上层超时时立即停止等待。
			return ctx.Err()
		}
	}

	return lastErr
}
