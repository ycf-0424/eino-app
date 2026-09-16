//go:build mysql_integration

package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"my-eino-app/internal/config"
	"my-eino-app/internal/session"
)

// 真实 HTTP 模型适配层 + Agent + MySQL，模型响应使用 SSE fixture，避免依赖 GPU。
func TestChatPersistsBeforeInferenceAndKeepsHistory(t *testing.T) {
	var service *Service
	id := "test-" + uuid.NewString()
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		messages, err := service.sessions.Load(id)
		if err != nil || len(messages) < 2 || session.MessageStatus(messages[len(messages)-1]) != "generating" {
			t.Errorf("request not persisted before model call: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"验证成功\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer mock.Close()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Session.Store = "mysql"
	cfg.Session.MaxMessages = 1
	cfg.RAG.Enabled = false
	cfg.Memory.Enabled = false
	cfg.LocalFiles.Enabled = false
	cfg.Agent.MultiAgent = false
	cfg.Debug = false
	cfg.Agent.CheckpointDir = t.TempDir()
	cfg.Skills.Dir = t.TempDir()
	cfg.Skills.Default = ""
	cfg.OpenAI.BaseURL = mock.URL + "/v1"
	cfg.OpenAI.APIKey = "test"
	cfg.Retry.MaxAttempts = 1
	service, err = NewService(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.sessions.Close()
	defer service.sessions.Delete(id)
	for i := 0; i < 2; i++ {
		if _, err = service.Chat(context.Background(), id, "测试问题", "", io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := service.sessions.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[3].Content != "验证成功" || session.MessageStatus(messages[3]) != "completed" {
		t.Fatalf("history was compressed or reply not saved: %+v", messages)
	}
}
