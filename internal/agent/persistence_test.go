package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/schema"
	"my-eino-app/internal/session"
	"testing"
)

func TestPersistenceKeepsFullHistoryAndCancelledReply(t *testing.T) {
	a := &ChatAgent{history: []*schema.Message{schema.UserMessage("旧问题"), schema.AssistantMessage("旧回答", nil), schema.UserMessage("新问题")}}
	var saved []*schema.Message
	a.SetPersistence(func(messages []*schema.Message) error {
		raw, _ := json.Marshal(messages)
		return json.Unmarshal(raw, &saved)
	}, 1, 100)
	if err := a.beginReply(); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 4 || session.MessageStatus(saved[3]) != "generating" {
		t.Fatal("user and placeholder must be saved before inference")
	}
	err := a.finishReply("已生成部分", nil, context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(saved) != 4 || saved[0].Content != "旧问题" || saved[3].Content != "已生成部分" || session.MessageStatus(saved[3]) != "cancelled" {
		t.Fatal("history or partial reply lost")
	}
}

func TestPersistenceFailureIsReturned(t *testing.T) {
	sentinel := errors.New("database offline")
	a := &ChatAgent{}
	a.SetPersistence(func([]*schema.Message) error { return sentinel }, 0, 0)
	if !errors.Is(a.beginReply(), sentinel) {
		t.Fatal("save failure was hidden")
	}
}
