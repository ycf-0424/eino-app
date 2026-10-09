package server

import (
	"context"
	"testing"
	"time"

	"my-eino-app/internal/config"
)

func TestModelsForRequestUsesSelectedProfileContext(t *testing.T) {
	cfg := &config.Config{
		Models: []config.ModelProfile{
			{ID: "small", APIKey: "ollama", Model: "small-model", BaseURL: "http://localhost:11434/v1", MaxCompletionTokens: 1024, ContextWindowTokens: 16384},
			{ID: "large", APIKey: "ollama", Model: "large-model", BaseURL: "http://localhost:11434/v1", MaxCompletionTokens: 4096, ContextWindowTokens: 65536},
		},
		Runtime: config.Runtime{RequestTimeout: config.Duration(30 * time.Second)},
	}
	service := &Service{cfg: cfg}
	models, err := service.modelsForRequest(context.Background(), "large", "question")
	if err != nil {
		t.Fatal(err)
	}
	if models.ContextWindowTokens != 65536 || models.MaxCompletionTokens != 4096 {
		t.Fatalf("selected context limits = (%d, %d), want (65536, 4096)", models.ContextWindowTokens, models.MaxCompletionTokens)
	}
}

func TestContextLimitsForAutomaticFallback(t *testing.T) {
	primary := config.ModelProfile{ContextWindowTokens: 65536, MaxCompletionTokens: 4096}
	fallback := config.ModelProfile{ContextWindowTokens: 32768, MaxCompletionTokens: 8192}
	window, output := contextLimitsForRoute(primary, &fallback)
	if window != 32768 || output != 8192 {
		t.Fatalf("combined limits = (%d, %d), want (32768, 8192)", window, output)
	}
	fallback.ContextWindowTokens = 0
	window, _ = contextLimitsForRoute(primary, &fallback)
	if window != 0 {
		t.Fatalf("unknown fallback window should disable dynamic budgeting, got %d", window)
	}
}

func TestAutomaticFallbackModelIDSupportsBothRoutes(t *testing.T) {
	auto := config.AutoModelRouting{FastModel: "fast", StrongModel: "strong"}
	if got := automaticFallbackModelID("fast", auto); got != "strong" {
		t.Fatalf("fast route fallback = %q, want strong", got)
	}
	if got := automaticFallbackModelID("strong", auto); got != "fast" {
		t.Fatalf("strong route fallback = %q, want fast", got)
	}
	if got := automaticFallbackModelID("other", auto); got != "" {
		t.Fatalf("unknown route fallback = %q, want empty", got)
	}
	if got := automaticFallbackModelID("fast", config.AutoModelRouting{FastModel: "fast", StrongModel: "fast"}); got != "" {
		t.Fatalf("same-model route fallback = %q, want empty", got)
	}
}

func TestShouldPreferStrongForKnowledgeMiss(t *testing.T) {
	cfg := &config.Config{Agent: config.Agent{AutoRouting: config.AutoModelRouting{Enabled: true}}}
	if !shouldPreferStrongForKnowledgeMiss(cfg, "auto", true, 0) {
		t.Fatal("automatic project-fact miss should escalate to strong model")
	}
	if shouldPreferStrongForKnowledgeMiss(cfg, "ark-doubao-lite", true, 0) {
		t.Fatal("manual model selection must not be overridden")
	}
	if shouldPreferStrongForKnowledgeMiss(cfg, "auto", true, 1) {
		t.Fatal("knowledge hit must keep the routed model")
	}
	if shouldPreferStrongForKnowledgeMiss(cfg, "auto", false, 0) {
		t.Fatal("ordinary questions must not escalate on a knowledge miss")
	}
}

func TestParseAutoRoute(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantRoute string
		wantScore float64
		wantErr   bool
	}{
		{name: "strict", content: `{"route":"fast","confidence":0.92}`, wantRoute: "fast", wantScore: 0.92},
		{name: "code fence", content: "```json\n{\"route\":\"strong\",\"confidence\":0.81}\n```", wantRoute: "strong", wantScore: 0.81},
		{name: "invalid route", content: `{"route":"medium","confidence":0.9}`, wantErr: true},
		{name: "invalid score", content: `{"route":"fast","confidence":1.2}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route, score, err := parseAutoRoute(tt.content)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected parse error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if route != tt.wantRoute || score != tt.wantScore {
				t.Fatalf("got route=%q score=%v", route, score)
			}
		})
	}
}
