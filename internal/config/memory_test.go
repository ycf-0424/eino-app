package config

import (
	"strings"
	"testing"
)

// memoryModeConfig 返回一份「只差身份模式」的合法记忆配置。
// 其余字段由 ValidateMemory 补默认值，因此这里只需要满足前置条件。
func memoryModeConfig(mode, owner string) Config {
	return Config{
		Session: Session{Store: "mysql"},
		RAG:     RAG{Enabled: true, Embedding: OpenAI{Model: "text-embedding"}, Dimension: 1024},
		Memory:  Memory{Enabled: true, IdentityMode: mode, OwnerID: owner, ProjectID: "proj"},
	}
}

// 单用户模式以 owner_id 为身份来源；多用户模式的身份来自登录态，owner_id 被忽略。
// 这两种读法直接决定数据落在哪个 owner 名下，任何回退都会造成串号或越权。
func TestValidateMemoryIdentityMode(t *testing.T) {
	t.Setenv("MEMORY_ENABLED", "")

	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "single user accepts owner id",
			cfg:  memoryModeConfig("local_single_user", "local:single"),
		},
		{
			name:    "single user requires owner id",
			cfg:     memoryModeConfig("local_single_user", ""),
			wantErr: "owner_id must be 1..64",
		},
		{
			name: "multi user accepts missing owner id",
			cfg: func() Config {
				c := memoryModeConfig("multi_user", "")
				c.Auth.Enabled = true
				return c
			}(),
		},
		{
			name: "multi user tolerates stale owner id",
			cfg: func() Config {
				c := memoryModeConfig("multi_user", "local:legacy")
				c.Auth.Enabled = true
				return c
			}(),
		},
		{
			name:    "multi user requires auth",
			cfg:     memoryModeConfig("multi_user", ""),
			wantErr: "requires auth.enabled=true",
		},
		{
			name:    "unknown mode rejected",
			cfg:     memoryModeConfig("shared", "local:single"),
			wantErr: "must be local_single_user or multi_user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.ValidateMemory()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateMemory() unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateMemory() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// project_id 在两种模式下都必须存在：它是所有用户共享的项目命名空间，
// 缺失会让项目域读写全部落到空 scope。
func TestValidateMemoryRequiresProjectID(t *testing.T) {
	t.Setenv("MEMORY_ENABLED", "")
	c := memoryModeConfig("local_single_user", "local:single")
	c.Memory.ProjectID = ""
	if err := c.ValidateMemory(); err == nil || !strings.Contains(err.Error(), "project_id must be 1..64") {
		t.Fatalf("ValidateMemory() error = %v, want project_id error", err)
	}
}
