package execution

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// MySQLStore 复用会话存储的 *sql.DB，把执行记录写入 execution_runs / execution_events。
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore 创建 MySQL 执行记录存储。db 为 nil 时返回 nil，调用方按「未启用」处理。
func NewMySQLStore(db *sql.DB) *MySQLStore {
	if db == nil {
		return nil
	}
	return &MySQLStore{db: db}
}

// StartRun 先确保会话行存在，再插入执行记录。
// conversations 行原本在首次 Save 时才创建，而 run_started 早于 Save，
// 因此这里必须自己 INSERT IGNORE，否则首轮会外键失败。
func (s *MySQLStore) StartRun(ctx context.Context, run Run) error {
	if !validSessionID.MatchString(run.SessionID) {
		return fmt.Errorf("invalid session id %q", run.SessionID)
	}
	started := run.StartedAt
	if started.IsZero() {
		started = time.Now().UTC()
	}
	status := run.Status
	if status == "" {
		status = StatusRunning
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT IGNORE INTO conversations(id) VALUES (?)", run.SessionID); err != nil {
		return fmt.Errorf("ensure conversation: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		"INSERT IGNORE INTO execution_runs(run_id,conversation_id,status,started_at) VALUES(?,?,?,?)",
		run.RunID, run.SessionID, status, started.UTC()); err != nil {
		return fmt.Errorf("insert execution run: %w", err)
	}
	return tx.Commit()
}

func (s *MySQLStore) LinkRunMessage(ctx context.Context, runID string, userSequence, assistantSequence int64) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE execution_runs SET user_sequence=?, assistant_sequence=? WHERE run_id=?",
		userSequence, assistantSequence, runID)
	return err
}

func (s *MySQLStore) FinishRun(ctx context.Context, runID, status string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE execution_runs SET status=?, finished_at=CURRENT_TIMESTAMP(6) WHERE run_id=?", status, runID)
	return err
}

// AppendEvents 批量写入事件；(event_id) 与 (run_id, sequence) 双重去重保证幂等。
func (s *MySQLStore) AppendEvents(ctx context.Context, runID string, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx,
		"INSERT IGNORE INTO execution_events(event_id,run_id,sequence,type,occurred_at,payload) VALUES(?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, ev := range events {
		if ev.Sequence <= 0 {
			continue
		}
		payload, err := json.Marshal(ev.Payload)
		if err != nil {
			payload = []byte("{}")
		}
		occurred := ev.OccurredAt
		if occurred.IsZero() {
			occurred = time.Now().UTC()
		}
		if _, err = statement.ExecContext(ctx, ev.EventID, runID, ev.Sequence, string(ev.Type), occurred.UTC(), string(payload)); err != nil {
			return fmt.Errorf("insert execution event: %w", err)
		}
	}
	return tx.Commit()
}

func (s *MySQLStore) ListRuns(ctx context.Context, sessionID string, limit int) ([]Run, error) {
	if !validSessionID.MatchString(sessionID) {
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT run_id,conversation_id,status,started_at,finished_at,user_sequence,assistant_sequence FROM execution_runs WHERE conversation_id=? ORDER BY started_at DESC LIMIT ?",
		sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Run
	for rows.Next() {
		var run Run
		var finished sql.NullTime
		var userSeq, assistantSeq sql.NullInt64
		if err = rows.Scan(&run.RunID, &run.SessionID, &run.Status, &run.StartedAt, &finished, &userSeq, &assistantSeq); err != nil {
			return nil, err
		}
		if finished.Valid {
			value := finished.Time
			run.FinishedAt = &value
		}
		if userSeq.Valid {
			value := userSeq.Int64
			run.UserSequence = &value
		}
		if assistantSeq.Valid {
			value := assistantSeq.Int64
			run.AssistantSequence = &value
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

func (s *MySQLStore) ListEvents(ctx context.Context, sessionID, runID string, afterSequence int64, limit int) ([]Event, error) {
	if !validSessionID.MatchString(sessionID) {
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	query := "SELECT e.event_id,e.run_id,e.sequence,e.type,e.occurred_at,e.payload FROM execution_events e JOIN execution_runs r ON r.run_id=e.run_id WHERE r.conversation_id=? AND e.sequence>?"
	args := []any{sessionID, afterSequence}
	if runID != "" {
		query += " AND e.run_id=?"
		args = append(args, runID)
	}
	query += " ORDER BY e.occurred_at, e.sequence LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Event
	for rows.Next() {
		var ev Event
		var kind, payload string
		if err = rows.Scan(&ev.EventID, &ev.RunID, &ev.Sequence, &kind, &ev.OccurredAt, &payload); err != nil {
			return nil, err
		}
		ev.Version = Version
		ev.Type = Type(kind)
		ev.SessionID = sessionID
		if payload != "" {
			_ = json.Unmarshal([]byte(payload), &ev.Payload)
		}
		result = append(result, ev)
	}
	return result, rows.Err()
}

func (s *MySQLStore) MaxSequence(ctx context.Context, runID string) (int64, error) {
	var max sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT MAX(sequence) FROM execution_events WHERE run_id=?", runID).Scan(&max)
	if err != nil {
		return 0, err
	}
	return max.Int64, nil
}

// DeleteBySession 只清理执行记录；messages 由会话删除语句级联处理。
func (s *MySQLStore) DeleteBySession(ctx context.Context, sessionID string) error {
	if !validSessionID.MatchString(sessionID) {
		return fmt.Errorf("invalid session id %q", sessionID)
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM execution_runs WHERE conversation_id=?", sessionID)
	return err
}

// RecoverStale 把超时仍为 running 的 run 置为 failed，并补一条 run_failed 事件。
func (s *MySQLStore) RecoverStale(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan).UTC()
	rows, err := s.db.QueryContext(ctx,
		"SELECT run_id,conversation_id FROM execution_runs WHERE status=? AND started_at<?", StatusRunning, cutoff)
	if err != nil {
		return 0, err
	}
	type stale struct{ runID, sessionID string }
	var items []stale
	for rows.Next() {
		var item stale
		if err = rows.Scan(&item.runID, &item.sessionID); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, item := range items {
		max, err := s.MaxSequence(ctx, item.runID)
		if err != nil {
			return recovered, err
		}
		ev := Event{
			Version: Version, EventID: newEventID(), RunID: item.runID, SessionID: item.sessionID,
			Sequence: max + 1, OccurredAt: time.Now().UTC(), Type: RunFailed,
			Summary: "运行中断", Payload: map[string]any{"error_code": "interrupted"},
		}
		if err = s.AppendEvents(ctx, item.runID, []Event{ev}); err != nil {
			return recovered, err
		}
		if err = s.FinishRun(ctx, item.runID, StatusFailed); err != nil {
			return recovered, err
		}
		recovered++
	}
	return recovered, nil
}

// CleanupOlderThan 删除超过保留期的执行记录，事件由外键级联删除。
func (s *MySQLStore) CleanupOlderThan(ctx context.Context, age time.Duration) (int, error) {
	cutoff := time.Now().Add(-age).UTC()
	result, err := s.db.ExecContext(ctx, "DELETE FROM execution_runs WHERE started_at<?", cutoff)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}
