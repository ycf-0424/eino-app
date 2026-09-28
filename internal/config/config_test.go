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

func TestDocumentToolsRequireLocalFileRoots(t *testing.T) {
	cfg := Config{
		OpenAI:        OpenAI{APIKey: "local", Model: "qwen", BaseURL: "http://localhost:11434/v1"},
		DocumentTools: DocumentTools{Enabled: true},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "document_tools.enabled requires local_files.enabled") {
		t.Fatalf("expected document/local-file dependency error, got %v", err)
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

func TestAttachmentsRequireMySQLAndMalwareScanner(t *testing.T) {
	base := Config{OpenAI: OpenAI{APIKey: "local", Model: "qwen", BaseURL: "http://localhost:11434/v1"}, Attachments: AttachmentsConfig{Enabled: true}}
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "session.store=mysql") {
		t.Fatalf("attachments should require MySQL metadata: %v", err)
	}
	base.Session.Store = "mysql"
	base.MySQL.User = "app"
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "virus_scanner is required") {
		t.Fatalf("attachments should fail closed without malware scanner: %v", err)
	}
	base.Attachments.VirusScanner = "clamscan"
	if err := base.Validate(); err != nil {
		t.Fatalf("valid disabled-provider attachment configuration failed: %v", err)
	}
	if base.Attachments.MaxMediaDurationSeconds != 600 || base.Attachments.MaxVideoFrames != 12 || base.Attachments.MaxPages != 10 {
		t.Fatalf("media processing defaults are not bounded: %+v", base.Attachments)
	}
}

func TestContextWindowConfigurationAndProfileResolution(t *testing.T) {
	t.Setenv("SESSION_STORE", "")
	cfg := Config{
		Models: []ModelProfile{
			{ID: "small", Model: "small-model", BaseURL: "http://localhost:11434/v1", MaxCompletionTokens: 512, ContextWindowTokens: 16384},
			{ID: "large", Model: "large-model", BaseURL: "http://localhost:11434/v1", MaxCompletionTokens: 2048, ContextWindowTokens: 65536},
		},
		ActiveModel: "small",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Session.MaxSummaryChars != 4000 || cfg.Session.ContextSafetyTokens != 8192 || cfg.Session.HardMaxMessages != 200 || cfg.Session.HardMaxChars != 300000 {
		t.Fatalf("session context defaults = %+v", cfg.Session)
	}
	profile, err := cfg.ResolveModelProfile("large")
	if err != nil {
		t.Fatal(err)
	}
	if profile.ContextWindowTokens != 65536 || profile.MaxCompletionTokens != 2048 {
		t.Fatalf("resolved context profile = %+v", profile)
	}
	active, err := cfg.ResolveModelProfile("")
	if err != nil || active.ID != "small" {
		t.Fatalf("active profile = %+v, %v", active, err)
	}
}

func TestValidateContextWindowRequiresOutputAndSafetyRoom(t *testing.T) {
	t.Setenv("SESSION_STORE", "")
	tests := []struct {
		name    string
		profile ModelProfile
		wantErr string
	}{
		{
			name:    "missing output reserve",
			profile: ModelProfile{ID: "small", Model: "qwen", BaseURL: "http://localhost/v1", ContextWindowTokens: 8192},
			wantErr: "max_completion_tokens must be positive",
		},
		{
			name:    "window too small",
			profile: ModelProfile{ID: "small", Model: "qwen", BaseURL: "http://localhost/v1", MaxCompletionTokens: 4096, ContextWindowTokens: 6000},
			wantErr: "must exceed max_completion_tokens plus session.context_safety_tokens",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{Models: []ModelProfile{test.profile}}
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}
