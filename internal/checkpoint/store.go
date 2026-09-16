// Package checkpoint 提供 Eino Runner 执行检查点的本地持久化。
package checkpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// FileStore 实现 Eino CheckPointStore 和 CheckPointDeleter。
type FileStore struct {
	dir string
	mu  sync.RWMutex
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
	return &FileStore{dir: dir}, nil
}

func (s *FileStore) path(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", fmt.Errorf("invalid checkpoint id %q", id)
	}
	return filepath.Join(s.dir, id+".checkpoint"), nil
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
