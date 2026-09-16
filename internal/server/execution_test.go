package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"my-eino-app/internal/checkpoint"
	"my-eino-app/internal/execution"
	"my-eino-app/internal/session"
	"my-eino-app/internal/skill"
)

func newTestService(t *testing.T, store execution.Store) *Service {
	t.Helper()
	sessions, err := session.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cp, err := checkpoint.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Service{sessions: sessions, skills: skill.NewLoader(t.TempDir()), checkpoints: cp, executions: store}
}

func getSnapshot(t *testing.T, server *httptest.Server, path string) executionSnapshot {
	t.Helper()
	resp, err := server.Client().Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status=%d", path, resp.StatusCode)
	}
	var payload struct {
		Data executionSnapshot `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return payload.Data
}

// 关闭执行事件时接口必须返回空结构而不是错误，前端据此隐藏面板。
func TestExecutionEndpointWithoutStoreReturnsEmpty(t *testing.T) {
	server := httptest.NewServer(newTestService(t, nil).Handler())
	defer server.Close()
	snapshot := getSnapshot(t, server, "/sessions/unknown/execution")
	if len(snapshot.Runs) != 0 || len(snapshot.Events) != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestExecutionEndpointFiltersByRunAndSequence(t *testing.T) {
	store := execution.NewMemoryStore()
	ctx := context.Background()
	if err := store.StartRun(ctx, execution.Run{RunID: "run-1", SessionID: "session-1", Status: execution.StatusRunning, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkRunMessage(ctx, "run-1", 0, 1); err != nil {
		t.Fatal(err)
	}
	events := []execution.Event{
		{Version: execution.Version, EventID: "e1", RunID: "run-1", SessionID: "session-1", Sequence: 1, OccurredAt: time.Now(), Type: execution.RunStarted},
		{Version: execution.Version, EventID: "e2", RunID: "run-1", SessionID: "session-1", Sequence: 2, OccurredAt: time.Now(), Type: execution.Chunk, Content: "你"},
		{Version: execution.Version, EventID: "e3", RunID: "run-1", SessionID: "session-1", Sequence: 3, OccurredAt: time.Now(), Type: execution.RunCompleted},
	}
	if err := store.AppendEvents(ctx, "run-1", events); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "run-1", execution.StatusCompleted); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(newTestService(t, store).Handler())
	defer server.Close()

	snapshot := getSnapshot(t, server, "/sessions/session-1/execution")
	if len(snapshot.Runs) != 1 || snapshot.Runs[0].RunID != "run-1" {
		t.Fatalf("runs=%+v", snapshot.Runs)
	}
	if len(snapshot.Events) != 3 {
		t.Fatalf("events=%d, want 3 (replay keeps chunks)", len(snapshot.Events))
	}

	// after_sequence 用于断线补取：只返回尚未收到的事件。
	after := getSnapshot(t, server, "/sessions/session-1/execution?run_id=run-1&after_sequence=2")
	if len(after.Events) != 1 || after.Events[0].Sequence != 3 {
		t.Fatalf("after_sequence=%+v", after.Events)
	}
}

func TestDeleteSessionClearsExecutionRecords(t *testing.T) {
	store := execution.NewMemoryStore()
	if err := store.StartRun(context.Background(), execution.Run{RunID: "run-1", SessionID: "session-1", Status: execution.StatusRunning, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newTestService(t, store).Handler())
	defer server.Close()

	request, err := http.NewRequest(http.MethodDelete, server.URL+"/sessions/session-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d", resp.StatusCode)
	}
	snapshot := getSnapshot(t, server, "/sessions/session-1/execution")
	if len(snapshot.Runs) != 0 || len(snapshot.Events) != 0 {
		t.Fatalf("execution records must be removed with the session: %+v", snapshot)
	}
}
