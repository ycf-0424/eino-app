// Package session 提供文件或 MySQL 会话持久化，供 Agent 重启后恢复历史。
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// ErrForeignSession 表示会话存在但不属于当前 owner。
//
// 必须与「会话不存在」区分开：后者在 Load 里返回空历史（新会话的正常起点），
// 在 Delete 里视为成功（接口幂等）；而归属他人是越权访问，HTTP 层要映射成 403。
// 只做查询过滤不够——知道 id 就能操作，所以每个按 id 的方法都要显式比对 owner。
var ErrForeignSession = errors.New("session belongs to another user")

// Store 为 HTTP、控制台和迁移工具提供统一入口；Open 根据配置选择后端。
type Store struct {
	dir   string
	mysql *mysqlStore
}

// Info 是会话摘要；Size 为序列化消息字节数，MySQL 模式不代表磁盘占用。
type Info struct {
	ID         string    `json:"id"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}

const summaryPrefix = "[较早对话摘要]\n"

// TrimHistory 保留系统消息和最近若干条消息，防止上下文无限增长。
func TrimHistory(messages []*schema.Message, maxMessages int, maxChars int) []*schema.Message {
	if maxMessages <= 0 && maxChars <= 0 {
		return messages
	}
	var system *schema.Message
	first := 0
	if len(messages) > 0 && messages[0].Role == schema.System {
		system = messages[0]
		first = 1
	}
	start := first
	if maxMessages > 0 && len(messages)-start > maxMessages {
		start = len(messages) - maxMessages
	}
	out := append([]*schema.Message{}, messages[start:]...)
	if maxChars > 0 {
		total, keep := 0, 0
		for i := len(out) - 1; i >= 0; i-- {
			total += len(out[i].Content)
			if total > maxChars {
				keep = i + 1
				break
			}
		}
		out = out[keep:]
	}
	if system != nil {
		out = append([]*schema.Message{system}, out...)
	}
	return out
}

// CompactHistory 将被裁掉的旧消息压缩为确定性摘要，并保留最近消息。
// 这里不额外调用模型，避免每轮会话都增加 Ollama 推理开销。
func CompactHistory(messages []*schema.Message, maxMessages, maxChars int) []*schema.Message {
	trimmed := TrimHistory(messages, maxMessages, maxChars)
	if len(trimmed) == len(messages) {
		return trimmed
	}
	kept := make(map[*schema.Message]struct{}, len(trimmed))
	for _, message := range trimmed {
		kept[message] = struct{}{}
	}
	var lines []string
	for _, message := range messages {
		if message == nil {
			continue
		}
		if _, ok := kept[message]; ok || strings.HasPrefix(message.Content, summaryPrefix) {
			continue
		}
		role := "用户"
		if message.Role == schema.Assistant {
			role = "助手"
		}
		text := strings.Join(strings.Fields(message.Content), " ")
		if len([]rune(text)) > 160 {
			text = string([]rune(text)[:160]) + "..."
		}
		lines = append(lines, role+": "+text)
	}
	if len(lines) == 0 {
		return trimmed
	}
	summary := schema.SystemMessage(summaryPrefix + strings.Join(lines, "\n"))
	// 摘要作为系统上下文放在最近消息之前，不会被误认为新的用户问题。
	return append([]*schema.Message{summary}, trimmed...)
}

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// New 创建会话存储，并自动创建目录。
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func validateID(id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid session id %q: use 1-64 letters, digits, '.', '_' or '-'", id)
	}
	return nil
}

// Load 读取会话；不存在时返回空历史。
//
// owner 是会话归属（"<provider>:<subject>"，单用户/CLI 场景为空串）。
// MySQL 模式下会话存在但归属他人时返回 ErrForeignSession；
// 文件模式没有 owner 维度，owner 参数被忽略（auth 启用时 ValidateAuth
// 已强制 session.store=mysql，因此这一忽略不会成为隔离缺口）。
func (s *Store) Load(owner, id string) ([]*schema.Message, error) {
	if s.mysql != nil {
		return s.mysql.load(owner, id)
	}
	if err := validateID(id); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.path(id))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	var messages []*schema.Message
	if err := json.Unmarshal(b, &messages); err != nil {
		return nil, fmt.Errorf("parse session: %w", err)
	}
	return messages, nil
}

// Save 原子写入会话，避免程序中断时留下半个 JSON 文件。
// owner 语义同 Load：MySQL 模式下归属他人时报 ErrForeignSession。
func (s *Store) Save(owner, id string, messages []*schema.Message) error {
	if s.mysql != nil {
		return s.mysql.save(owner, id, messages)
	}
	if err := validateID(id); err != nil {
		return err
	}
	b, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	tmp := s.path(id) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	if err := os.Rename(tmp, s.path(id)); err != nil {
		return fmt.Errorf("commit session: %w", err)
	}
	return nil
}

// List 返回该 owner 的所有 Session，按最近修改时间倒序排列。
// owner 为空串时匹配未归属的历史数据（单用户模式）。
func (s *Store) List(owner string) ([]Info, error) {
	if s.mysql != nil {
		return s.mysql.list(owner)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var result []Info
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		result = append(result, Info{ID: strings.TrimSuffix(entry.Name(), ".json"), Size: info.Size(), ModifiedAt: info.ModTime()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ModifiedAt.After(result[j].ModifiedAt) })
	return result, nil
}

// Create 登记一条空会话并绑定 owner，供服务端签发 session id 时使用（步骤 2.7）。
//
// 单独一个方法而不是让调用方写 Save(owner, id, nil)：这里表达的是「签发新会话」，
// 与「追加消息」是两种意图，将来要加限流或审计也有明确的落脚点。
func (s *Store) Create(owner, id string) error {
	return s.Save(owner, id, nil)
}

// OwnerOf 返回会话归属；会话不存在时 exists 为 false。
//
// 服务端据此决定是否沿用调用方带来的 session id：只有不存在（新会话）或
// 归属就是当前用户时才接受，否则调用方必须收到明确的越权错误，
// 而不是被悄悄换一个 id —— 后者会掩盖越权尝试。
// 文件后端没有 owner 维度，存在即归属空 owner（auth 启用时 ValidateAuth 已强制 mysql）。
func (s *Store) OwnerOf(id string) (owner string, exists bool, err error) {
	if err := validateID(id); err != nil {
		return "", false, err
	}
	if s.mysql != nil {
		return s.mysql.ownerOf(id)
	}
	if _, statErr := os.Stat(s.path(id)); statErr == nil {
		return "", true, nil
	} else if !os.IsNotExist(statErr) {
		return "", false, statErr
	}
	return "", false, nil
}

// Delete 删除一个 Session；不存在视为成功，方便接口幂等调用。
// 归属他人时返回 ErrForeignSession（MySQL 模式）。
func (s *Store) Delete(owner, id string) error {
	if s.mysql != nil {
		return s.mysql.delete(owner, id)
	}
	if err := validateID(id); err != nil {
		return err
	}
	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// CleanupOlderThan 删除超过指定期限的 Session，并返回删除数量。
func (s *Store) CleanupOlderThan(age time.Duration) (int, error) {
	return s.CleanupOlderThanBatch(age, 500)
}

// CleanupOlderThanBatch is the configurable variant used by the service
// maintenance loop. The default CleanupOlderThan API remains compatible with
// existing callers.
func (s *Store) CleanupOlderThanBatch(age time.Duration, batchSize int) (int, error) {
	if s.mysql != nil {
		return s.mysql.cleanupOlderThan(age, batchSize)
	}
	if age <= 0 {
		return 0, fmt.Errorf("cleanup age must be greater than zero")
	}
	// 全局运维清理，跨 owner，不按归属过滤。
	items, err := s.List("")
	if err != nil {
		return 0, err
	}
	removed := 0
	cutoff := time.Now().Add(-age)
	for _, item := range items {
		if item.ModifiedAt.Before(cutoff) {
			if err := s.Delete("", item.ID); err != nil {
				return removed, err
			}
			removed++
		}
	}
	return removed, nil
}
