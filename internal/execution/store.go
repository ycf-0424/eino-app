package execution

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Run 是一条执行记录的头信息。
type Run struct {
	RunID             string     `json:"run_id"`
	SessionID         string     `json:"session_id"`
	Status            string     `json:"status"`
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	UserSequence      *int64     `json:"user_sequence,omitempty"`
	AssistantSequence *int64     `json:"assistant_sequence,omitempty"`
}

// 运行状态取值。
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	// StatusAwaitingApproval 表示 run 因审批暂停，尚未结束。
	StatusAwaitingApproval = "awaiting_approval"
)

// Store 是执行记录与事件的持久化接口。MySQL 与文件两种后端共用它。
type Store interface {
	// StartRun 创建一条执行记录。实现必须先保证会话行存在，再插入外键记录。
	StartRun(ctx context.Context, run Run) error
	// LinkRunMessage 把 run 关联到 user/assistant 两条消息的序号。
	LinkRunMessage(ctx context.Context, runID string, userSequence, assistantSequence int64) error
	// FinishRun 写入终态与结束时间。
	FinishRun(ctx context.Context, runID, status string) error
	// AppendEvents 批量写入事件，重复 (run_id, sequence) 必须幂等。
	AppendEvents(ctx context.Context, runID string, events []Event) error
	// ListRuns 按会话返回执行记录，最近的在前。
	ListRuns(ctx context.Context, sessionID string, limit int) ([]Run, error)
	// ListEvents 按会话返回事件。runID 非空时只返回该 run 的事件，
	// 配合 afterSequence 可精确补取；runID 为空时返回全会话事件用于回放。
	ListEvents(ctx context.Context, sessionID, runID string, afterSequence int64, limit int) ([]Event, error)
	// MaxSequence 返回某个 run 当前已持久化的最大序号；不存在时返回 0。
	MaxSequence(ctx context.Context, runID string) (int64, error)
	// DeleteBySession 清理某个会话的执行记录与事件。
	DeleteBySession(ctx context.Context, sessionID string) error
	// RecoverStale 把超时仍处于 running 的 run 置为 failed{interrupted}。
	RecoverStale(ctx context.Context, olderThan time.Duration) (int, error)
	// CleanupOlderThan 删除超过保留期的事件与记录，返回清理的 run 数量。
	CleanupOlderThan(ctx context.Context, age time.Duration) (int, error)
}

// MemoryStore 是线程安全的内存实现，供单测和「仅实时进度」的降级场景使用。
type MemoryStore struct {
	mu     sync.Mutex
	runs   map[string]*Run
	order  []*Run
	events map[string][]Event
}

// NewMemoryStore 创建内存执行记录存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{runs: map[string]*Run{}, events: map[string][]Event{}}
}

func (m *MemoryStore) StartRun(_ context.Context, run Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := run
	m.runs[run.RunID] = &copied
	m.order = append(m.order, &copied)
	return nil
}

func (m *MemoryStore) LinkRunMessage(_ context.Context, runID string, userSequence, assistantSequence int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run, ok := m.runs[runID]; ok {
		run.UserSequence = &userSequence
		run.AssistantSequence = &assistantSequence
	}
	return nil
}

func (m *MemoryStore) FinishRun(_ context.Context, runID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run, ok := m.runs[runID]; ok {
		now := time.Now().UTC()
		run.Status = status
		run.FinishedAt = &now
	}
	return nil
}

func (m *MemoryStore) AppendEvents(_ context.Context, runID string, events []Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ev := range events {
		dup := false
		for _, existing := range m.events[runID] {
			if existing.Sequence == ev.Sequence {
				dup = true
				break
			}
		}
		if !dup {
			m.events[runID] = append(m.events[runID], ev)
		}
	}
	sort.Slice(m.events[runID], func(i, j int) bool { return m.events[runID][i].Sequence < m.events[runID][j].Sequence })
	return nil
}

func (m *MemoryStore) ListRuns(_ context.Context, sessionID string, limit int) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []Run
	for i := len(m.order) - 1; i >= 0; i-- {
		if m.order[i].SessionID != sessionID {
			continue
		}
		result = append(result, *m.order[i])
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (m *MemoryStore) ListEvents(_ context.Context, sessionID, runID string, afterSequence int64, limit int) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []Event
	for id, run := range m.runs {
		if run.SessionID != sessionID {
			continue
		}
		if runID != "" && id != runID {
			continue
		}
		for _, ev := range m.events[id] {
			if ev.Sequence > afterSequence {
				result = append(result, ev)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].RunID == result[j].RunID {
			return result[i].Sequence < result[j].Sequence
		}
		return result[i].OccurredAt.Before(result[j].OccurredAt)
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (m *MemoryStore) MaxSequence(_ context.Context, runID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var max int64
	for _, ev := range m.events[runID] {
		if ev.Sequence > max {
			max = ev.Sequence
		}
	}
	return max, nil
}

func (m *MemoryStore) DeleteBySession(_ context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for runID, run := range m.runs {
		if run.SessionID == sessionID {
			delete(m.runs, runID)
			delete(m.events, runID)
		}
	}
	filtered := m.order[:0]
	for _, run := range m.order {
		if run.SessionID != sessionID {
			filtered = append(filtered, run)
		}
	}
	m.order = filtered
	return nil
}

func (m *MemoryStore) RecoverStale(_ context.Context, olderThan time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-olderThan)
	count := 0
	for _, run := range m.order {
		if run.Status != StatusRunning || run.StartedAt.After(cutoff) {
			continue
		}
		now := time.Now().UTC()
		run.Status = StatusFailed
		run.FinishedAt = &now
		count++
	}
	return count, nil
}

func (m *MemoryStore) CleanupOlderThan(_ context.Context, age time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-age)
	count := 0
	kept := m.order[:0]
	for _, run := range m.order {
		if run.StartedAt.Before(cutoff) {
			delete(m.runs, run.RunID)
			delete(m.events, run.RunID)
			count++
			continue
		}
		kept = append(kept, run)
	}
	m.order = kept
	return count, nil
}
