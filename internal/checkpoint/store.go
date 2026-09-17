// Package checkpoint 提供 Eino Runner 执行检查点的本地持久化。
package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// FileStore 实现 Eino CheckPointStore 和 CheckPointDeleter。
//
// 目录按 owner 分层（dir/<owner>/<id>.checkpoint）。这里不做「方法签名加 owner」：
// Get/Set/Delete 受 eino 的 CheckPointStore 接口约束无法加参数，调用方改用
// For(owner) 取得绑定到当前身份的同款视图。
type FileStore struct {
	dir string
	// mu 在所有派生视图之间共享：视图是按请求创建的，若各自持有锁，
	// 同一会话的并发写会同时落到同一个 <id>.checkpoint.tmp 上并互相覆盖。
	mu *sync.RWMutex
}

// PendingApproval 是跨进程恢复审批所需的最小元数据；执行状态本身保存在 checkpoint 中。
// RunID 用于审批恢复时续用同一次执行记录；旧文件缺少该字段时视为空，会另起一个新 run。
type PendingApproval struct {
	TargetID  string `json:"target_id"`
	ToolName  string `json:"tool_name"`
	Arguments string `json:"arguments"`
	RunID     string `json:"run_id,omitempty"`
}

// New 函数。
func New(dir string) (*FileStore, error) {
	// 启动时确保目录存在，后续 checkpoint 和审批元数据都写在这里。
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create checkpoint dir: %w", err)
	}
	return &FileStore{dir: dir, mu: &sync.RWMutex{}}, nil
}

// For 返回绑定到指定 owner 的视图：checkpoint 与审批元数据落在 dir/<owner>/ 下。
//
// owner 为空串时返回原视图（根目录），保持 CLI 单用户路径与改造前完全一致。
// 这是纵深防御：session id 由服务端签发（步骤 2.7），分层目录保证即便 id 泄露，
// 另一个身份的请求也读不到对应的 checkpoint 与审批内容。
func (s *FileStore) For(owner string) *FileStore {
	if owner == "" || s == nil {
		return s
	}
	mu := s.mu
	if mu == nil {
		mu = &sync.RWMutex{}
	}
	return &FileStore{dir: filepath.Join(s.dir, ownerSegment(owner)), mu: mu}
}

// ownerSegment 把 owner 转成安全的单层目录名。
//
// owner 形如 feishu:<open_id> / local:<uuid>，冒号在 Windows 上是非法文件名字符。
// 只做字符替换会产生碰撞（a:b_c 与 a_b:c 结果相同），而这里一旦碰撞就是两个
// 身份共用同一目录 —— 属于隔离边界，因此额外拼上原值的短哈希保证一一对应。
func ownerSegment(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, owner)
	if len(safe) > 48 {
		safe = safe[:48]
	}
	return safe + "-" + hex.EncodeToString(sum[:6])
}

func (s *FileStore) path(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", fmt.Errorf("invalid checkpoint id %q", id)
	}
	return filepath.Join(s.dir, id+".checkpoint"), nil
}

// ensureDir 在写入前补建目录：For(owner) 是按请求创建的轻量视图，不在构造时
// 做 I/O，避免为一次只读请求也建目录。
func ensureDir(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create checkpoint dir: %w", err)
	}
	return nil
}

func (s *FileStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (s *FileStore) Set(_ context.Context, id string, value []byte) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := ensureDir(path); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp := path + ".tmp"
	// 先写临时文件再原子改名，避免进程中断留下半截 checkpoint。
	if err := os.WriteFile(tmp, value, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *FileStore) Delete(_ context.Context, id string) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *FileStore) approvalPath(id string) (string, error) {
	path, err := s.path(id)
	if err != nil {
		return "", err
	}
	return path + ".approval.json", nil
}

// SaveApproval 保存用户可见的审批信息，供程序重启后重新提示。
func (s *FileStore) SaveApproval(id string, approval PendingApproval) error {
	path, err := s.approvalPath(id)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(approval, "", "  ")
	if err != nil {
		return err
	}
	if err := ensureDir(path); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.WriteFile(path, b, 0o600)
}

func (s *FileStore) LoadApproval(id string) (*PendingApproval, error) {
	path, err := s.approvalPath(id)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var approval PendingApproval
	if err := json.Unmarshal(b, &approval); err != nil {
		return nil, err
	}
	return &approval, nil
}

func (s *FileStore) ClearApproval(id string) error {
	path, err := s.approvalPath(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
