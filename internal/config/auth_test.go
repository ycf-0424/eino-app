package config

import (
	"strings"
	"testing"
)

// auth.enabled 为 true 时必须至少有一种可用的登录方式，但三种组合都合法：
// 只用飞书、只用自有账号、两者并存。校验过严会把「只想用自有账号」的部署直接堵死。
func TestValidateAuthLoginMethods(t *testing.T) {
	feishu := Auth{
		Enabled:     true,
		AppID:       "cli_test",
		AppSecret:   "secret",
		RedirectURL: "http://localhost:18180/auth/callback",
	}
	local := LocalAuth{Enabled: true, MinPasswordLength: 8}

	both := feishu
	both.Local = local

	tests := []struct {
		name    string
		auth    Auth
		wantErr string
	}{
		{name: "认证关闭时不校验其余字段", auth: Auth{}},
		{name: "只用飞书", auth: feishu},
		{name: "只用自有账号", auth: Auth{Enabled: true, Local: local}},
		{name: "两者并存", auth: both},
		{name: "两种都没启用", auth: Auth{Enabled: true}, wantErr: "至少启用一种登录方式"},
		{name: "飞书凭据只填一半", auth: Auth{Enabled: true, AppID: "cli_test"}, wantErr: "至少启用一种登录方式"},
		{
			name:    "口令下限越界",
			auth:    Auth{Enabled: true, Local: LocalAuth{Enabled: true, MinPasswordLength: 4}},
			wantErr: "min_password_length",
		},
		{
			name:    "provider 非法",
			auth:    Auth{Enabled: true, Provider: "google", Local: local},
			wantErr: "仅支持 feishu",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Auth: tt.auth, Session: Session{Store: "mysql"}}
			err := cfg.ValidateAuth()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateAuth() 意外报错: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateAuth() error = %v, want 含 %q", err, tt.wantErr)
			}
		})
	}
}

// 缺省值必须补齐：调用方（cmd/user-admin、登录逻辑）直接读 MinPasswordLength，
// 不能依赖配置文件里一定写了这一项。
func TestValidateAuthFillsLocalDefaults(t *testing.T) {
	cfg := Config{Auth: Auth{Enabled: true, Local: LocalAuth{Enabled: true}}, Session: Session{Store: "mysql"}}
	if err := cfg.ValidateAuth(); err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Local.MinPasswordLength != 8 {
		t.Fatalf("min_password_length 缺省值 = %d, want 8", cfg.Auth.Local.MinPasswordLength)
	}
	if cfg.Auth.SessionTTL <= 0 {
		t.Fatal("session_ttl 缺省值未补齐")
	}
}

// 认证启用时文件后端没有 owner 维度，必须明确失败而不是静默串号。
func TestValidateAuthRequiresMySQLSessionStore(t *testing.T) {
	cfg := Config{
		Auth:    Auth{Enabled: true, Local: LocalAuth{Enabled: true}},
		Session: Session{Store: "file"},
	}
	if err := cfg.ValidateAuth(); err == nil || !strings.Contains(err.Error(), "session.store=mysql") {
		t.Fatalf("ValidateAuth() error = %v, want 要求 mysql", err)
	}
}
