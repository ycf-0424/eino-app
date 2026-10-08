package model

import (
	"context"
	"errors"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type fallbackTestModel struct {
	message *schema.Message
	err     error
	called  bool
}

func (m *fallbackTestModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	m.called = true
	return m.message, m.err
}

func (m *fallbackTestModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	m.called = true
	if m.err != nil {
		return nil, m.err
	}
	return schema.StreamReaderFromArray([]*schema.Message{m.message}), nil
}

func (m *fallbackTestModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return m, nil
}

func TestFallbackChatModelUsesSecondaryAfterPrimaryError(t *testing.T) {
	primary := &fallbackTestModel{err: errors.New("model service is not activated")}
	fallback := &fallbackTestModel{message: &schema.Message{Content: "现在是下午三点。"}}
	model := NewFallbackChatModel(primary, fallback)

	got, err := model.Generate(context.Background(), []*schema.Message{schema.UserMessage("现在几点？")})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !primary.called || !fallback.called {
		t.Fatalf("primary called=%v, fallback called=%v; want both called", primary.called, fallback.called)
	}
	if got == nil || got.Content != fallback.message.Content {
		t.Fatalf("Generate() = %#v, want fallback response %q", got, fallback.message.Content)
	}
}

func TestFallbackChatModelUsesSecondaryWhenPrimaryStreamCannotStart(t *testing.T) {
	primary := &fallbackTestModel{err: errors.New("model service is not activated")}
	fallback := &fallbackTestModel{message: &schema.Message{Content: "现在是下午三点。"}}
	model := NewFallbackChatModel(primary, fallback)

	stream, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("现在几点？")})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	got, err := stream.Recv()
	if err != nil {
		t.Fatalf("stream Recv() error = %v", err)
	}
	if !primary.called || !fallback.called {
		t.Fatalf("primary called=%v, fallback called=%v; want both called", primary.called, fallback.called)
	}
	if got == nil || got.Content != fallback.message.Content {
		t.Fatalf("Stream() message = %#v, want fallback response %q", got, fallback.message.Content)
	}
}
