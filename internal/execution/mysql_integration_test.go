//go:build mysql_integration

package execution

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"my-eino-app/internal/config"
	"my-eino-app/internal/session"
)

// TestMySQLExecutionStoreRoundTrip 在真实 MySQL 上验证执行记录后端的完整读写路径。
// 需要先由管理员执行 001/002 迁移；测试只做 DML，不建表。
func TestMySQLExecutionStoreRoundTrip(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MySQL.User == "" {
		t.Skip("MYSQL_USER 未配置，跳过 MySQL 集成测试")
	}
	cfg.Session.Store = "mysql"
	sessions, err := session.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	db := sessions.DB()
	if db == nil {
		t.Skip("未连接 MySQL，跳过")
	}
	// Execution tables are optional and intentionally absent in deployments
	// where execution_events is disabled. This tagged test only runs after the
	// DBA has applied 002_execution.sql.
	if _, err = db.ExecContext(context.Background(), "SELECT 1 FROM execution_runs LIMIT 0"); err != nil {
		t.Skipf("execution schema is not installed: %v", err)
	}
	store := NewMySQLStore(db)
	ctx := context.Background()

	id := "exec-test-" + uuid.NewString()
	defer sessions.Delete(id)

	runID := uuid.NewString()
	started := time.Now().UTC()
	if err = store.StartRun(ctx, Run{RunID: runID, SessionID: id, Status: StatusRunning, StartedAt: started}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// 再次 StartRun 必须幂等（run_id 主键 + INSERT IGNORE）。
	if err = store.StartRun(ctx, Run{RunID: runID, SessionID: id, Status: StatusRunning, StartedAt: started}); err != nil {
		t.Fatalf("StartRun 幂等失败: %v", err)
	}

	events := []Event{
		{Version: Version, EventID: uuid.NewString(), RunID: runID, SessionID: id, Sequence: 1, OccurredAt: started, Type: RunStarted, Payload: map[string]any{"query_chars": 6}},
		{Version: Version, EventID: uuid.NewString(), RunID: runID, SessionID: id, Sequence: 2, OccurredAt: started.Add(time.Millisecond), Type: ToolCompleted, Payload: map[string]any{"tool_name": "local_file_read", "duration_ms": 12}},
	}
	if err = store.AppendEvents(ctx, runID, events); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	// 重复写入同一批事件必须不产生重复行。
	if err = store.AppendEvents(ctx, runID, events); err != nil {
		t.Fatalf("AppendEvents 幂等失败: %v", err)
	}
	if max, err := store.MaxSequence(ctx, runID); err != nil || max != 2 {
		t.Fatalf("MaxSequence=%d err=%v, 期望 2", max, err)
	}

	if err = store.LinkRunMessage(ctx, runID, 0, 1); err != nil {
		t.Fatalf("LinkRunMessage: %v", err)
	}
	if err = store.FinishRun(ctx, runID, StatusCompleted); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	runs, err := store.ListRuns(ctx, id, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns=%v err=%v, 期望 1 条", runs, err)
	}
	if runs[0].Status != StatusCompleted || runs[0].FinishedAt == nil {
		t.Fatalf("run 终态不正确: %+v", runs[0])
	}
	if runs[0].AssistantSequence == nil || *runs[0].AssistantSequence != 1 {
		t.Fatalf("assistant_sequence 未回填: %+v", runs[0])
	}

	all, err := store.ListEvents(ctx, id, "", 0, 100)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListEvents=%v err=%v, 期望 2 条", all, err)
	}
	afterOne, err := store.ListEvents(ctx, id, runID, 1, 100)
	if err != nil || len(afterOne) != 1 || afterOne[0].Sequence != 2 {
		t.Fatalf("after_sequence 补取失败: %+v err=%v", afterOne, err)
	}
	if afterOne[0].Payload["tool_name"] != "local_file_read" {
		t.Fatalf("payload 未正确往返: %+v", afterOne[0].Payload)
	}

	// RecoverStale：造一条超期 running，应被置为 failed 并补 run_failed 事件。
	staleID := uuid.NewString()
	if err = store.StartRun(ctx, Run{RunID: staleID, SessionID: id, Status: StatusRunning, StartedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("StartRun(stale): %v", err)
	}
	recovered, err := store.RecoverStale(ctx, 5*time.Minute)
	if err != nil || recovered != 1 {
		t.Fatalf("RecoverStale=%d err=%v, 期望 1", recovered, err)
	}

	// DeleteBySession：清理后该会话不应再有 run 与事件。
	if err = store.DeleteBySession(ctx, id); err != nil {
		t.Fatalf("DeleteBySession: %v", err)
	}
	if runs, err = store.ListRuns(ctx, id, 10); err != nil || len(runs) != 0 {
		t.Fatalf("删除后仍有 run: %+v err=%v", runs, err)
	}
	if events, err = store.ListEvents(ctx, id, "", 0, 100); err != nil || len(events) != 0 {
		t.Fatalf("删除后仍有事件: %+v err=%v", events, err)
	}
}
