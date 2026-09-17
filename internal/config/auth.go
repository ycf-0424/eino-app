package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Auth 是登录认证配置；关闭时服务以单用户模式运行，与改造前行为一致。
type Auth struct {
	Enabled      bool      `yaml:"enabled"`
	Provider     string    `yaml:"provider"`      // 仅支持 "feishu"
	AppID        string    `yaml:"app_id"`        // 飞书自建应用 App ID
	AppSecret    string    `yaml:"app_secret"`    // 飞书自建应用 App Secret
	RedirectURL  string    `yaml:"redirect_url"`  // 必须与飞书后台登记值完全一致
	SessionTTL   Duration  `yaml:"session_ttl"`   // 登录态有效期，缺省 12h
	CookieSecure bool      `yaml:"cookie_secure"` // 内网 http 部署必须为 false
	AdminOpenID  string    `yaml:"admin_open_id"` // 飞书侧管理员：历史数据归属与管理标记
	Local        LocalAuth `yaml:"local"`         // 项目自有账号
}

// LocalAuth 是项目自有账号的配置。
//
// 定位：内部场景下它是「例外通道」而非主通道 —— 主通道是飞书（员工已有飞书账号，
// 一键登录）。它服务两类人：没有飞书账号的（外包、合作方、临时人员），以及飞书
// 故障时的兜底。使用者少且可预期，因此注册方式为「仅管理员创建」，建号走命令行
// 工具（cmd/user-admin），不经 HTTP。
type LocalAuth struct {
	Enabled bool `yaml:"enabled"`
	// MinPasswordLength 是口令长度下限，缺省 8；上限固定 128。
	MinPasswordLength int `yaml:"min_password_length"`
}

// PasswordMaxLength 是口令长度上限，用于防止超长输入把 PBKDF2 拖成 DoS
// （每次登录要算 21 万轮）。固定值，不开放配置。
const PasswordMaxLength = 128

// ValidateAuth 校验认证配置，并补齐缺省值。
//
// 环境变量 FEISHU_APP_ID / FEISHU_APP_SECRET / FEISHU_REDIRECT_URL 会覆盖配置值，
// 避免把凭据写进仓库。
func (c *Config) ValidateAuth() error {
	a := &c.Auth
	for key, dest := range map[string]*string{
		"FEISHU_APP_ID":       &a.AppID,
		"FEISHU_APP_SECRET":   &a.AppSecret,
		"FEISHU_REDIRECT_URL": &a.RedirectURL,
	} {
		if value, ok := os.LookupEnv(key); ok {
			*dest = value
		}
	}

	if a.SessionTTL <= 0 {
		a.SessionTTL = Duration(12 * time.Hour)
	}
	if a.Local.MinPasswordLength == 0 {
		a.Local.MinPasswordLength = 8
	}
	if !a.Enabled {
		// 未启用认证时不校验其余字段，保持旧配置可直接启动。
		return nil
	}

	if strings.TrimSpace(a.Provider) == "" {
		a.Provider = "feishu"
	}
	if a.Provider != "feishu" {
		return fmt.Errorf("auth.provider 仅支持 feishu，当前为 %q", a.Provider)
	}

	// 认证启用时必须至少有一种可用的登录方式，否则没人能登进来，系统等于不可达。
	// 三种组合都合法：只用飞书、只用自有账号、两者并存。
	feishuReady := a.AppID != "" && a.AppSecret != "" && a.RedirectURL != ""
	if !feishuReady && !a.Local.Enabled {
		return fmt.Errorf("auth.enabled 为 true 时必须至少启用一种登录方式：" +
			"飞书凭据（app_id / app_secret / redirect_url）或 auth.local.enabled")
	}
	if a.Local.Enabled {
		if a.Local.MinPasswordLength < 8 || a.Local.MinPasswordLength > 64 {
			return fmt.Errorf("auth.local.min_password_length 必须在 8..64 之间")
		}
	}
	// file 模式没有 owner 维度，无法做数据隔离；缺失清晰失败优于静默串号。
	if c.Session.Store != "mysql" {
		return fmt.Errorf("auth.enabled 要求 session.store=mysql，当前为 %q", c.Session.Store)
	}
	return nil
}
