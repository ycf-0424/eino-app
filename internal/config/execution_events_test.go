package config

import "testing"

func TestValidateFillsExecutionEventDefaultsOnlyWhenEnabled(t *testing.T) {
	enabled := Config{
		OpenAI:          OpenAI{Model: "qwen", BaseURL: "http://localhost:11434/v1"},
		ExecutionEvents: ExecutionEvents{Enabled: true},
	}
	if err := enabled.Validate(); err != nil {
		t.Fatal(err)
	}
	if enabled.ExecutionEvents.QueueSize != 256 {
		t.Fatalf("queue_size=%d, want 256", enabled.ExecutionEvents.QueueSize)
	}
	if enabled.ExecutionEvents.RetentionDays != 7 {
		t.Fatalf("retention_days=%d, want 7", enabled.ExecutionEvents.RetentionDays)
	}
	if enabled.ExecutionEvents.MaxEventsPerRun != 5000 {
		t.Fatalf("max_events_per_run=%d, want 5000", enabled.ExecutionEvents.MaxEventsPerRun)
	}
	if enabled.ExecutionEvents.Dir != "data/executions" {
		t.Fatalf("dir=%q, want data/executions", enabled.ExecutionEvents.Dir)
	}

	// 关闭时必须保持零值，代码据此判断「未启用」，避免误判为已开启。
	disabled := Config{OpenAI: OpenAI{Model: "qwen", BaseURL: "http://localhost:11434/v1"}}
	if err := disabled.Validate(); err != nil {
		t.Fatal(err)
	}
	if disabled.ExecutionEvents.Enabled || disabled.ExecutionEvents.QueueSize != 0 || disabled.ExecutionEvents.Dir != "" {
		t.Fatalf("disabled execution events must stay zero: %+v", disabled.ExecutionEvents)
	}
}

func TestValidateKeepsExplicitExecutionEventValues(t *testing.T) {
	cfg := Config{
		OpenAI: OpenAI{Model: "qwen", BaseURL: "http://localhost:11434/v1"},
		ExecutionEvents: ExecutionEvents{
			Enabled: true, QueueSize: 32, RetentionDays: 3, MaxEventsPerRun: 100, Dir: "data/custom-exec",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.ExecutionEvents.QueueSize != 32 || cfg.ExecutionEvents.RetentionDays != 3 ||
		cfg.ExecutionEvents.MaxEventsPerRun != 100 || cfg.ExecutionEvents.Dir != "data/custom-exec" {
		t.Fatalf("explicit values must be preserved: %+v", cfg.ExecutionEvents)
	}
}
