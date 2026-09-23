package model

import (
	"context"
	"errors"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// FallbackChatModel retries a failed primary model request with a secondary
// model. Stream fallback only applies when the primary stream cannot be
// created; once content has started, replaying a request could duplicate the
// user's answer and is therefore left to the caller.
type FallbackChatModel struct {
	primary  einomodel.ToolCallingChatModel
	fallback einomodel.ToolCallingChatModel
}

// NewFallbackChatModel creates a model that uses fallback after a primary
// Generate/Stream setup error. Parent-context cancellation is never retried.
func NewFallbackChatModel(primary, fallback einomodel.ToolCallingChatModel) einomodel.ToolCallingChatModel {
	if primary == nil {
		return fallback
	}
	if fallback == nil {
		return primary
	}
	return &FallbackChatModel{primary: primary, fallback: fallback}
}

func (m *FallbackChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	message, err := m.primary.Generate(ctx, input, opts...)
	if err == nil || errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return message, err
	}
	return m.fallback.Generate(ctx, input, opts...)
}

func (m *FallbackChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	stream, err := m.primary.Stream(ctx, input, opts...)
	if err == nil || errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return stream, err
	}
	return m.fallback.Stream(ctx, input, opts...)
}

func (m *FallbackChatModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	primary, err := m.primary.WithTools(tools)
	if err != nil {
		return nil, err
	}
	fallback, err := m.fallback.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &FallbackChatModel{primary: primary, fallback: fallback}, nil
}
