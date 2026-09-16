package execution

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 文件后端使用 append-only JSONL，每行是一条记录。
// 该格式便于人工排查，也避免进程中断时出现半个事件。
const (
	recordKindRun    = "run"
	recordKindEvent  = "event"
	recordKindLink   = "link"
	recordKindFinish = "finish"
)

var validSessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type fileRecord struct {
	Kind              string `json:"kind"`
	RunID             string `json:"run_id,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	Status            string `json:"status,omitempty"`
	StartedAt         string `json:"started_at,omitempty"`
	FinishedAt        string `json:"finished_at,omitempty"`
	UserSequence      *int64 `json:"user_sequence,omitempty"`
	AssistantSequence *int64 `json:"assistant_sequence,omitempty"`
	Event             *Event `json:"event,omitempty"`
}

// FileStore 把执行记录写入 data/executions/<session>.jsonl。
// 本地默认 session.store=file，没有它 P8 无法在本地验收。
type FileStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileStore 创建文件后端并确保目录存在。
func NewFileStore(dir string) (*FileStore, error) {
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join("data", "executions")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create execution dir: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

// Dir 返回存储目录，便于测试与文档说明。
func (s *FileStore) Dir() string { return s.dir }

func (s *FileStore) path(sessionID string) (string, error) {
	if !validSessionID.MatchString(sessionID) {
		return "", fmt.Errorf("invalid session id %q", sessionID)
	}
	return filepath.Join(s.dir, sessionID+".jsonl"), nil
}

func (s *FileStore) append(sessionID string, records ...fileRecord) error {
	path, err := s.path(sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open execution log: %w", err)
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	encoder := json.NewEncoder(writer)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			return fmt.Errorf("encode execution record: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush execution log: %w", err)
	}
	return file.Sync()
}

func (s *FileStore) read(sessionID string) ([]fileRecord, error) {
	path, err := s.path(sessionID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read execution log: %w", err)
	}
	defer file.Close()
	var records []fileRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record fileRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			// 单行损坏不应导致整段历史不可读。
			continue
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}

// StartRun 写入一条运行头记录。
func (s *FileStore) StartRun(_ context.Context, run Run) error {
	return s.append(run.SessionID, fileRecord{
		Kind: recordKindRun, RunID: run.RunID, SessionID: run.SessionID,
		Status: run.Status, StartedAt: run.StartedAt.UTC().Format(time.RFC3339Nano),
	})
}

// LinkRunMessage 写入 run 与消息序号的关联。
func (s *FileStore) LinkRunMessage(_ context.Context, runID string, userSequence, assistantSequence int64) error {
	// 文件后端把关联写在事件流末尾，读取时回放到对应 run 上。
	return s.appendRunScoped(runID, fileRecord{
		Kind: recordKindLink, RunID: runID,
		UserSequence: &userSequence, AssistantSequence: &assistantSequence,
	})
}

// FinishRun 写入终态记录。
func (s *FileStore) FinishRun(_ context.Context, runID, status string) error {
	return s.appendRunScoped(runID, fileRecord{
		Kind: recordKindFinish, RunID: runID, Status: status,
		FinishedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// AppendEvents 逐条写入事件，读取时按 (run_id, sequence) 去重。
func (s *FileStore) AppendEvents(_ context.Context, runID string, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	// 事件自带 session_id，因此不需要额外查询即可定位文件。
	sessionID := events[0].SessionID
	records := make([]fileRecord, 0, len(events))
	for i := range events {
		ev := events[i]
		if sessionID == "" {
			sessionID = ev.SessionID
		}
		records = append(records, fileRecord{Kind: recordKindEvent, RunID: runID, Event: &ev})
	}
	if sessionID == "" {
		return fmt.Errorf("execution event has no session id")
	}
	return s.append(sessionID, records...)
}

// appendRunScoped 在不知道会话 ID 时按 run 定位文件。
func (s *FileStore) appendRunScoped(runID string, record fileRecord) error {
	sessionID, err := s.sessionOfRun(runID)
	if err != nil {
		return err
	}
	return s.append(sessionID, record)
}

func (s *FileStore) sessionOfRun(runID string) (string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return "", fmt.Errorf("scan execution dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
		records, err := s.read(sessionID)
		if err != nil {
			continue
		}
		for _, record := range records {
			if record.RunID == runID && record.Kind == recordKindRun {
				return sessionID, nil
			}
		}
	}
	return "", fmt.Errorf("execution run %q not found", runID)
}

// ListRuns 返回会话的执行记录，最近的在前。
func (s *FileStore) ListRuns(_ context.Context, sessionID string, limit int) ([]Run, error) {
	records, err := s.read(sessionID)
	if err != nil {
		return nil, err
	}
	runs, order := replayRuns(records)
	result := make([]Run, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		result = append(result, *runs[order[i]])
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// ListEvents 读取并排序事件，按 run 与 sequence 去重。
func (s *FileStore) ListEvents(_ context.Context, sessionID, runID string, afterSequence int64, limit int) ([]Event, error) {
	records, err := s.read(sessionID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var result []Event
	for _, record := range records {
		if record.Kind != recordKindEvent || record.Event == nil {
			continue
		}
		ev := *record.Event
		if runID != "" && ev.RunID != runID {
			continue
		}
		if ev.Sequence <= afterSequence {
			continue
		}
		key := fmt.Sprintf("%s#%d", ev.RunID, ev.Sequence)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, ev)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].Sequence < result[j].Sequence
		}
		return result[i].OccurredAt.Before(result[j].OccurredAt)
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *FileStore) MaxSequence(_ context.Context, runID string) (int64, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, err
	}
	var max int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		records, err := s.read(strings.TrimSuffix(entry.Name(), ".jsonl"))
		if err != nil {
			continue
		}
		for _, record := range records {
			if record.Kind == recordKindEvent && record.Event != nil && record.Event.RunID == runID && record.Event.Sequence > max {
				max = record.Event.Sequence
			}
		}
	}
	return max, nil
}

// DeleteBySession 删除整个会话的执行日志。
func (s *FileStore) DeleteBySession(_ context.Context, sessionID string) error {
	path, err := s.path(sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RecoverStale 把异常退出后残留的 running 记录补成 failed，避免前端永久显示运行中。
func (s *FileStore) RecoverStale(ctx context.Context, olderThan time.Duration) (int, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-olderThan)
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
		records, err := s.read(sessionID)
		if err != nil {
			continue
		}
		runs, order := replayRuns(records)
		for _, runID := range order {
			run := runs[runID]
			if run.Status != StatusRunning || run.StartedAt.After(cutoff) {
				continue
			}
			finished := time.Now().UTC()
			sequence, _ := s.MaxSequence(ctx, runID)
			ev := Event{
				Version: Version, EventID: newEventID(), RunID: runID, SessionID: sessionID,
				Sequence: sequence + 1, OccurredAt: finished, Type: RunFailed,
				Summary: "运行中断", Payload: map[string]any{"error_code": "interrupted"},
			}
			if err := s.append(sessionID,
				fileRecord{Kind: recordKindEvent, RunID: runID, Event: &ev},
				fileRecord{Kind: recordKindFinish, RunID: runID, Status: StatusFailed, FinishedAt: finished.Format(time.RFC3339Nano)},
			); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

// CleanupOlderThan 删除全部 run 都已超过保留期的会话日志。
func (s *FileStore) CleanupOlderThan(_ context.Context, age time.Duration) (int, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-age)
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
		records, err := s.read(sessionID)
		if err != nil {
			continue
		}
		runs, order := replayRuns(records)
		if len(order) == 0 {
			continue
		}
		allOld := true
		for _, runID := range order {
			if !runs[runID].StartedAt.Before(cutoff) {
				allOld = false
				break
			}
		}
		if !allOld {
			continue
		}
		if err := s.DeleteBySession(context.Background(), sessionID); err == nil {
			removed += len(order)
		}
	}
	return removed, nil
}

// replayRuns 把 JSONL 记录回放成 Run 列表，并保留出现顺序。
func replayRuns(records []fileRecord) (map[string]*Run, []string) {
	runs := map[string]*Run{}
	var order []string
	for _, record := range records {
		switch record.Kind {
		case recordKindRun:
			if record.RunID == "" {
				continue
			}
			started, _ := time.Parse(time.RFC3339Nano, record.StartedAt)
			runs[record.RunID] = &Run{RunID: record.RunID, SessionID: record.SessionID, Status: record.Status, StartedAt: started}
			order = append(order, record.RunID)
		case recordKindLink:
			if run, ok := runs[record.RunID]; ok {
				run.UserSequence, run.AssistantSequence = record.UserSequence, record.AssistantSequence
			}
		case recordKindFinish:
			if run, ok := runs[record.RunID]; ok {
				run.Status = record.Status
				if finished, err := time.Parse(time.RFC3339Nano, record.FinishedAt); err == nil {
					run.FinishedAt = &finished
				}
			}
		}
	}
	return runs, order
}
