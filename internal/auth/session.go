package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// CookieName 是携带登录 token 的 Cookie 名。
const CookieName = "eino_session"

// Session 是一个已登录身份。
type Session struct {
	Owner     string // "feishu:<open_id>" 或 "local:<uuid>"
	Name      string
	AvatarURL string
	Provider  string // "feishu" | "local"，供前端显示与 /auth/me 返回
	// IsAdmin controls server-side management actions such as knowledge-base
	// ingestion. It is carried by the signed-in session rather than inferred
	// from a client-supplied field.
	IsAdmin   bool
	ExpiresAt time.Time
}

// Sessions 是登录态的进程内存储（D11）。
//
// key 是 token 的 SHA-256 十六进制（与 auth_sessions.token_hash 同形，长度 64），
// 进程重启即全部失效 —— 安全上更保守，也避免登录态对 DB 的额外依赖；
// auth_sessions 表已建好备用，将来切换持久化无需再迁移。
type Sessions struct {
	mu     sync.RWMutex
	byHash map[string]Session
	ttl    time.Duration
}

// NewSessions 创建登录态存储；ttl 非正时按 12h。
func NewSessions(ttl time.Duration) *Sessions {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &Sessions{byHash: make(map[string]Session), ttl: ttl}
}

// TTL 返回登录态有效期，供写 Cookie MaxAge 时对齐。
func (s *Sessions) TTL() time.Duration { return s.ttl }

// Create 签发新登录态，返回明文 token（只此一次可见，之后只存哈希）。
func (s *Sessions) Create(owner, name, avatarURL, provider string) (string, Session, error) {
	return s.CreateWithAdmin(owner, name, avatarURL, provider, false)
}

// CreateWithAdmin signs in a session with an explicit server-side admin flag.
// The legacy Create method remains non-admin for callers that do not have an
// authoritative role source.
func (s *Sessions) CreateWithAdmin(owner, name, avatarURL, provider string, isAdmin bool) (string, Session, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", Session{}, err
	}
	token := hex.EncodeToString(buf)
	sess := Session{
		Owner:     owner,
		Name:      name,
		AvatarURL: avatarURL,
		Provider:  provider,
		IsAdmin:   isAdmin,
		ExpiresAt: time.Now().Add(s.ttl),
	}
	s.mu.Lock()
	s.byHash[hashToken(token)] = sess
	s.mu.Unlock()
	return token, sess, nil
}

// Get 校验 token 并返回会话；不存在或已过期都视为未登录。
func (s *Sessions) Get(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	s.mu.RLock()
	sess, ok := s.byHash[hashToken(token)]
	s.mu.RUnlock()
	if !ok || time.Now().After(sess.ExpiresAt) {
		return Session{}, false
	}
	return sess, true
}

// Delete 注销登录态；token 为空时静默忽略（登出对未登录者幂等）。
func (s *Sessions) Delete(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	delete(s.byHash, hashToken(token))
	s.mu.Unlock()
}

// cleanup 删除已过期会话并返回清理数量。
func (s *Sessions) cleanup() int {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for hash, sess := range s.byHash {
		if now.After(sess.ExpiresAt) {
			delete(s.byHash, hash)
			removed++
		}
	}
	return removed
}

// StartJanitor 周期清理过期会话，直到 ctx 取消。
// 过期会话在 Get 时本来就会被拒，这里只为回收内存。
func (s *Sessions) StartJanitor(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.cleanup()
			}
		}
	}()
}

// hashToken 把明文 token 折叠为存储 key。
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
