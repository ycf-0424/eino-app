package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "valid local ollama",
			cfg:  Config{OpenAI: OpenAI{Model: "qwen", BaseURL: "http://localhost:11434/v1"}},
		},
		{
			name:    "missing model",
			cfg:     Config{OpenAI: OpenAI{BaseURL: "http://localhost:11434/v1"}},
			wantErr: "model is required",
		},
		{
			name:    "missing base url",
			cfg:     Config{OpenAI: OpenAI{Model: "qwen"}},
			wantErr: "base_url is required",
		},
		{
			name:    "remote requires api key",
			cfg:     Config{OpenAI: OpenAI{Model: "qwen", BaseURL: "https://api.example.com/v1"}},
			wantErr: "api_key is required for remote provider",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() unexpected error: %v", err)
				}
				return
			}

			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadFromCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	content := []byte("openai:\n  api_key: ollama\n  model: qwen\n  base_url: http://localhost:11434/v1\n")
	if err := os.WriteFile(configFile, content, 0o600); err != nil {
		t.Fatal(err)
	}

	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.OpenAI.Model != "qwen" || cfg.OpenAI.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("Load() returned unexpected config: %+v", cfg)
	}
}

func TestValidateExpandsEnvironment(t *testing.T) {
	t.Setenv("TEST_MODEL_NAME", "qwen-local")
	cfg := Config{OpenAI: OpenAI{APIKey: "ollama", Model: "${TEST_MODEL_NAME}", BaseURL: "http://localhost:11434/v1"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.OpenAI.Model != "qwen-local" {
		t.Fatalf("model=%q", cfg.OpenAI.Model)
	}
	cfg.OpenAI.Model = "${MISSING_TEST_MODEL}"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing environment variable error")
	}
}
