package model

import (
	"context"
	"errors"
	"io"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"my-eino-app/internal/execution"
)

// FallbackChatModel retries a failed primary model request with a secondary
// model. A stream can be created successfully while the provider reports an
// error on its first Recv, so early stream errors and empty streams are also
// retried. Once useful output has started, replaying the request could
// duplicate the answer and is therefore left to the caller.
type FallbackChatModel struct {
	primary    einomodel.ToolCallingChatModel
	fallback   einomodel.ToolCallingChatModel
	primaryID  string
	fallbackID string
}

// NewFallbackChatModel creates a model that uses fallback after a primary
// Generate/Stream setup or early-read error. Parent-context cancellation is
// never retried.
func NewFallbackChatModel(primary, fallback einomodel.ToolCallingChatModel) einomodel.ToolCallingChatModel {
	if primary == nil {
		return fallback
	}
	if fallback == nil {
		return primary
	}
	return &FallbackChatModel{primary: primary, fallback: fallback}
}

// NewFallbackChatModelWithIDs is the observable variant used by the server's
// automatic routing path. IDs are included in the execution event so the UI
// can show that the secondary provider actually took over.
func NewFallbackChatModelWithIDs(primary, fallback einomodel.ToolCallingChatModel, primaryID, fallbackID string) einomodel.ToolCallingChatModel {
	if primary == nil {
		return fallback
	}
	if fallback == nil {
		return primary
	}
	return &FallbackChatModel{primary: primary, fallback: fallback, primaryID: primaryID, fallbackID: fallbackID}
}

func (m *FallbackChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	message, err := m.primary.Generate(ctx, input, opts...)
	if err == nil && hasMessageOutput(message) {
		return message, nil
	}
	if !canRetryFallback(ctx) {
		return message, err
	}
	m.emitFallback(ctx, fallbackReason(err == nil))
	fallbackMessage, fallbackErr := m.fallback.Generate(ctx, input, opts...)
	if fallbackErr == nil && !hasMessageOutput(fallbackMessage) {
		fallbackErr = errors.New("fallback model returned an empty response")
	}
	return fallbackMessage, fallbackErr
}

func (m *FallbackChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	stream, err := m.primary.Stream(ctx, input, opts...)
	if err != nil {
		if !canRetryFallback(ctx) {
			return nil, err
		}
		m.emitFallback(ctx, "stream_setup_error")
		return m.fallback.Stream(ctx, input, opts...)
	}
	if stream == nil {
		err = errors.New("primary model returned a nil stream")
		if !canRetryFallback(ctx) {
			return nil, err
		}
		m.emitFallback(ctx, "empty_stream")
		return m.fallback.Stream(ctx, input, opts...)
	}

	// Eino's model interface returns a StreamReader, so transport and HTTP
	// errors may only become visible while the caller reads it. Proxy the
	// primary stream through a pipe and switch before the first useful chunk.
	out, writer := schema.Pipe[*schema.Message](8)
	go func() {
		defer writer.Close()
		defer stream.Close()
		started := false
		for {
			chunk, recvErr := stream.Recv()
			if recvErr == nil {
				if hasMessageOutput(chunk) {
					started = true
				}
				if writer.Send(chunk, nil) {
					return
				}
				continue
			}
			if !started && canRetryFallback(ctx) {
				if errors.Is(recvErr, io.EOF) {
					m.emitFallback(ctx, "empty_stream")
				} else {
					m.emitFallback(ctx, "stream_read_error")
				}
				fallbackStream, fallbackErr := m.fallback.Stream(ctx, input, opts...)
				if fallbackErr != nil {
					var zero *schema.Message
					writer.Send(zero, fallbackErr)
					return
				}
				if fallbackStream == nil {
					var zero *schema.Message
					writer.Send(zero, errors.New("fallback model returned a nil stream"))
					return
				}
				defer fallbackStream.Close()
				fallbackStarted := false
				for {
					fallbackChunk, fallbackRecvErr := fallbackStream.Recv()
					if fallbackRecvErr != nil {
						var zero *schema.Message
						if errors.Is(fallbackRecvErr, io.EOF) && !fallbackStarted {
							writer.Send(zero, errors.New("fallback model returned an empty stream"))
						} else if !errors.Is(fallbackRecvErr, io.EOF) {
							writer.Send(zero, fallbackRecvErr)
						}
						return
					}
					if hasMessageOutput(fallbackChunk) {
						fallbackStarted = true
					}
					if writer.Send(fallbackChunk, nil) {
						return
					}
				}
			}
			var zero *schema.Message
			writer.Send(zero, recvErr)
			return
		}
	}()
	return out, nil
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
	return &FallbackChatModel{primary: primary, fallback: fallback, primaryID: m.primaryID, fallbackID: m.fallbackID}, nil
}

func (m *FallbackChatModel) emitFallback(ctx context.Context, reason string) {
	execution.FromContext(ctx).Emit(execution.Event{Type: execution.ModelFallback, Payload: map[string]any{
		"from_model_id": m.primaryID,
		"to_model_id":   m.fallbackID,
		"reason":        reason,
	}})
}

func fallbackReason(empty bool) string {
	if empty {
		return "empty_response"
	}
	return "request_error"
}

func canRetryFallback(ctx context.Context) bool {
	return !errors.Is(ctx.Err(), context.Canceled) && !errors.Is(ctx.Err(), context.DeadlineExceeded)
}

func hasMessageOutput(message *schema.Message) bool {
	if message == nil {
		return false
	}
	return message.Content != "" || message.ReasoningContent != "" || len(message.ToolCalls) > 0 || len(message.MultiContent) > 0 || len(message.AssistantGenMultiContent) > 0
}
