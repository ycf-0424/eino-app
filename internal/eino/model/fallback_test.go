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
	stream  *schema.StreamReader[*schema.Message]
	called  bool
}

func (m *fallbackTestModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	m.called = true
	return m.message, m.err
}

func (m *fallbackTestModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	m.called = true
	if m.stream != nil {
		return m.stream, nil
	}
	if m.err != nil {
		return nil, m.err
	}
	return schema.StreamReaderFromArray([]*schema.Message{m.message}), nil
}

func errorStream(err error, messages ...*schema.Message) *schema.StreamReader[*schema.Message] {
	reader, writer := schema.Pipe[*schema.Message](len(messages) + 1)
	go func() {
		for _, message := range messages {
			writer.Send(message, nil)
		}
		writer.Send(nil, err)
		writer.Close()
	}()
	return reader
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

func TestFallbackChatModelUsesSecondaryWhenPrimaryStreamFailsOnFirstRead(t *testing.T) {
	primary := &fallbackTestModel{stream: errorStream(errors.New("upstream 502"))}
	fallback := &fallbackTestModel{message: &schema.Message{Content: "备用模型回答。"}}
	model := NewFallbackChatModel(primary, fallback)

	stream, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("请回答")})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	got, err := stream.Recv()
	if err != nil || got == nil || got.Content != fallback.message.Content {
		t.Fatalf("first fallback chunk = %#v, err=%v", got, err)
	}
	if !primary.called || !fallback.called {
		t.Fatalf("primary called=%v, fallback called=%v; want both called", primary.called, fallback.called)
	}
}

func TestFallbackChatModelUsesSecondaryWhenPrimaryStreamIsEmpty(t *testing.T) {
	primary := &fallbackTestModel{stream: schema.StreamReaderFromArray([]*schema.Message{})}
	fallback := &fallbackTestModel{message: &schema.Message{Content: "空响应后的备用回答。"}}
	model := NewFallbackChatModel(primary, fallback)

	stream, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("请回答")})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	got, err := stream.Recv()
	if err != nil || got == nil || got.Content != fallback.message.Content {
		t.Fatalf("first fallback chunk = %#v, err=%v", got, err)
	}
	if !fallback.called {
		t.Fatal("empty primary stream must call fallback")
	}
}

func TestFallbackChatModelDoesNotRetryAfterPrimaryOutput(t *testing.T) {
	primaryErr := errors.New("stream broke after output")
	primary := &fallbackTestModel{stream: errorStream(primaryErr, &schema.Message{Content: "已输出的内容"})}
	fallback := &fallbackTestModel{message: &schema.Message{Content: "不应重复输出"}}
	model := NewFallbackChatModel(primary, fallback)

	stream, err := model.Stream(context.Background(), []*schema.Message{schema.UserMessage("请回答")})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	defer stream.Close()
	got, err := stream.Recv()
	if err != nil || got == nil || got.Content != "已输出的内容" {
		t.Fatalf("primary chunk = %#v, err=%v", got, err)
	}
	_, err = stream.Recv()
	if !errors.Is(err, primaryErr) {
		t.Fatalf("second Recv() error = %v, want %v", err, primaryErr)
	}
	if fallback.called {
		t.Fatal("fallback must not replay a request after primary output started")
	}
}
