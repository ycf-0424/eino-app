package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"my-eino-app/internal/config"
)

// 文档知识库与长期记忆都没有命中时，服务端不能再用固定拒答短路；
// 请求必须真正到达模型，由模型按通用知识兜底回答。
func TestKnowledgeMissFallsBackToModel(t *testing.T) {
	var calls atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"fallback\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"这是通用模型回答。\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"fallback\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer mock.Close()

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Session.Store = "file"
	cfg.SessionDir = t.TempDir()
	cfg.Agent.CheckpointDir = t.TempDir()
	cfg.Skills.Dir = t.TempDir()
	cfg.OpenAI.BaseURL = mock.URL + "/v1"
	cfg.OpenAI.APIKey = "test"
	cfg.RAG.Enabled = false    // 文档知识库无命中
	cfg.Memory.Enabled = false // 长期记忆无命中
	cfg.LocalFiles.Enabled = false
	cfg.Agent.MultiAgent = false
	cfg.Auth.Enabled = false
	cfg.ExecutionEvents.Enabled = false
	cfg.Retry.MaxAttempts = 1

	service, err := NewService(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	var answer strings.Builder
	if _, err = service.Chat(context.Background(), "fallback-session", "星河系统首席财务官是谁？", "", &answer); err != nil {
		t.Fatal(err)
	}
	if calls.Load() == 0 {
		t.Fatal("knowledge miss must reach the model")
	}
	if got := strings.TrimSpace(answer.String()); got != "这是通用模型回答。" {
		t.Fatalf("answer=%q, want model fallback", got)
	}
}
