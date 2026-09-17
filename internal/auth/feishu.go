package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"my-eino-app/internal/config"
)

// 飞书 OAuth 端点（2026-09 对照官方文档核对）。
// 坑 B4：网上大量教程仍写 open.feishu.cn/open-apis/authen/v2/oauth/token，
// 该端点已被官方弃用，照抄会直接失败。
const (
	feishuAuthorizeURL = "https://accounts.feishu.cn/open-apis/authen/v1/authorize"
	feishuTokenURL     = "https://accounts.feishu.cn/oauth/v3/token"
	feishuUserInfoURL  = "https://open.feishu.cn/open-apis/authen/v1/user_info"
)

// FeishuScope 是授权页申请的权限范围：只取基本信息。
// 该权限需先在飞书后台「权限管理」开通，未开通时授权页报 20027。
const FeishuScope = "contact:user.base:readonly"

// FeishuClient 封装飞书 OAuth 的三个调用。
//
// 不引入 PKCE：官方「获取授权码」文档写明 code_challenge 只能搭配 v2 token
// 端点，v3 尚未支持；state 防 CSRF 已由 Cookie 比对承担。
type FeishuClient struct {
	cfg  config.Auth
	http *http.Client
	// 端点做成字段只是为了单元测试能注入 httptest 服务器；
	// 生产路径由 NewFeishuClient 用常量初始化，不会出现第三种取值。
	tokenURL    string
	userInfoURL string
}

// NewFeishuClient 创建客户端；超时 10s 覆盖三个调用。
func NewFeishuClient(cfg config.Auth) *FeishuClient {
	return &FeishuClient{
		cfg:         cfg,
		http:        &http.Client{Timeout: 10 * time.Second},
		tokenURL:    feishuTokenURL,
		userInfoURL: feishuUserInfoURL,
	}
}

// AuthorizeURL 拼接授权页地址。
// redirect_uri 必须整体 URL 编码（不编码回调报 redirect_uri 不匹配 20071）。
// scope 是固定常量，冒号在 query 中合法，保持与官方文档一致的字面形式
// （url.Values.Encode 会把冒号编码成 %3A，标准上等价，但不做无谓的偏离）。
func (f *FeishuClient) AuthorizeURL(state string) string {
	return feishuAuthorizeURL +
		"?client_id=" + url.QueryEscape(f.cfg.AppID) +
		"&redirect_uri=" + url.QueryEscape(f.cfg.RedirectURL) +
		"&response_type=code" +
		"&scope=" + FeishuScope +
		"&state=" + url.QueryEscape(state)
}

// feishuTokenResponse 是 v3 换 token 的响应。
// user_access_token 的有效期由 expires_in 返回（非固定值）；本方案登录时只用
// 一次换用户信息，不做 refresh（D11 的登录态是自签 Cookie，与它解耦）。
type feishuTokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	RefreshToken     string `json:"refresh_token"`
	Code             int    `json:"code"`
	Msg              string `json:"msg"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Exchange 用授权码换用户 access token。
// ⚠️ 授权码 5 分钟有效且只能用一次，调用方拿到 code 后必须立即执行本方法。
func (f *FeishuClient) Exchange(ctx context.Context, code string) (string, error) {
	if code == "" {
		return "", fmt.Errorf("feishu exchange: empty code")
	}
	body, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     f.cfg.AppID,
		"client_secret": f.cfg.AppSecret,
		"code":          code,
		"redirect_uri":  f.cfg.RedirectURL,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.tokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := f.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", fmt.Errorf("feishu exchange: http %d: %s", resp.StatusCode, string(detail))
	}
	var tr feishuTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", fmt.Errorf("feishu exchange: decode: %w", err)
	}
	if tr.AccessToken == "" {
		if tr.Error != "" || tr.ErrorDescription != "" {
			return "", fmt.Errorf("feishu exchange: %s: %s", tr.Error, tr.ErrorDescription)
		}
		if tr.Code != 0 {
			return "", fmt.Errorf("feishu exchange: code %d: %s", tr.Code, tr.Msg)
		}
		return "", fmt.Errorf("feishu exchange: empty access_token")
	}
	return tr.AccessToken, nil
}

// FeishuUser 是 authen/v1/user_info 返回的 data 部分。
type FeishuUser struct {
	OpenID    string `json:"open_id"`
	UnionID   string `json:"union_id"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

type feishuUserResponse struct {
	Code int        `json:"code"`
	Msg  string     `json:"msg"`
	Data FeishuUser `json:"data"`
}

// FetchUser 用用户 access token 取基本信息。
func (f *FeishuClient) FetchUser(ctx context.Context, accessToken string) (FeishuUser, error) {
	if accessToken == "" {
		return FeishuUser{}, fmt.Errorf("feishu user: empty access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.userInfoURL, nil)
	if err != nil {
		return FeishuUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := f.http.Do(req)
	if err != nil {
		return FeishuUser{}, fmt.Errorf("feishu user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return FeishuUser{}, fmt.Errorf("feishu user: http %d: %s", resp.StatusCode, string(detail))
	}
	var ur feishuUserResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ur); err != nil {
		return FeishuUser{}, fmt.Errorf("feishu user: decode: %w", err)
	}
	if ur.Code != 0 {
		return FeishuUser{}, fmt.Errorf("feishu user: code %d: %s", ur.Code, ur.Msg)
	}
	if ur.Data.OpenID == "" {
		return FeishuUser{}, fmt.Errorf("feishu user: empty open_id")
	}
	return ur.Data, nil
}

// UserStore 把登录用户信息落库（auth_users 表）。
// db 为 nil 时所有方法为 no-op，供文件后端与单元测试使用。
type UserStore struct{ db *sql.DB }

// NewUserStore 创建用户存储；db 可为 nil。
func NewUserStore(db *sql.DB) *UserStore { return &UserStore{db: db} }

// UpsertFeishu 记录/更新飞书用户信息，并刷新 last_seen_at。
func (s *UserStore) UpsertFeishu(ctx context.Context, u FeishuUser) error {
	if s.db == nil || u.OpenID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO auth_users(open_id, union_id, name, avatar_url) VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  union_id = VALUES(union_id),
  name = VALUES(name),
  avatar_url = VALUES(avatar_url),
  last_seen_at = CURRENT_TIMESTAMP(6)`,
		u.OpenID, u.UnionID, u.Name, u.AvatarURL)
	return err
}
