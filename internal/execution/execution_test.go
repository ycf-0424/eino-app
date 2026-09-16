package execution

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEmitterAssignsMonotonicSequenceForChunksAndEvents(t *testing.T) {
	recorder := NewRecorder(0)
	em := NewSession("run-1", "session-1", nil, recorder, SessionOptions{})
	writer := NewChunkWriter(em, nil)

	if _, err := writer.Write([]byte("你好")); err != nil {
		t.Fatal(err)
	}
	em.Emit(Event{Type: ToolStarted, Payload: map[string]any{"tool_name": "current_time"}})
	if _, err := writer.Write([]byte("世界")); err != nil {
		t.Fatal(err)
	}
	em.Emit(Event{Type: RunCompleted})

	events := recorder.Events()
	if len(events) != 4 {
		t.Fatalf("events=%d, want 4", len(events))
	}
	for i, ev := range events {
		if ev.Sequence != int64(i+1) {
			t.Fatalf("event %d sequence=%d, want %d", i, ev.Sequence, i+1)
		}
		if ev.RunID != "run-1" || ev.SessionID != "session-1" {
			t.Fatalf("event %d run/session=%q/%q", i, ev.RunID, ev.SessionID)
		}
		if ev.EventID == "" || ev.Version != Version || ev.OccurredAt.IsZero() {
			t.Fatalf("event %d missing envelope fields: %+v", i, ev)
		}
	}
	if events[0].Type != Chunk || events[0].Content != "你好" {
		t.Fatalf("first event=%+v", events[0])
	}
	if events[3].Type != RunCompleted || !events[3].Type.Terminal() {
		t.Fatalf("last event=%+v", events[3])
	}
}

func TestEmitterIgnoresUnknownType(t *testing.T) {
	recorder := NewRecorder(0)
	em := NewSession("run-1", "session-1", nil, recorder, SessionOptions{})
	em.Emit(Event{Type: Type("future_event")})
	em.Emit(Event{Type: RunStarted})
	if got := len(recorder.Events()); got != 1 {
		t.Fatalf("events=%d, want 1", got)
	}
	if !recorder.Events()[0].Type.Known() {
		t.Fatal("unknown event should not be delivered")
	}
}

func TestEventSupportedRejectsHigherVersion(t *testing.T) {
	if (Event{Version: Version + 1, Type: RunStarted}).Supported() {
		t.Fatal("higher version must be ignored")
	}
	if !(Event{Version: Version, Type: RunStarted}).Supported() {
		t.Fatal("current version must be supported")
	}
}

func TestDigestArgsDoesNotLeakRawArguments(t *testing.T) {
	digest := DigestArgs(`{"password":"secret-value"}`)
	if digest == "" || strings.Contains(digest, "secret") {
		t.Fatalf("digest=%q", digest)
	}
	if DigestArgs("") != "" {
		t.Fatal("empty arguments must produce empty digest")
	}
}

func TestAnnotateIsolatedPerBag(t *testing.T) {
	first := NewBag()
	second := NewBag()
	first.Set("file_name", "a.txt")
	second.Set("hit_count", 3)

	payload := map[string]any{}
	MergeAnnotations(WithBag(context.Background(), first), payload)
	if payload["file_name"] != "a.txt" {
		t.Fatalf("payload=%v", payload)
	}
	if _, ok := payload["hit_count"]; ok {
		t.Fatal("bags must not leak into each other")
	}
	// 没有容器时静默忽略，不 panic。
	Annotate(context.Background(), map[string]any{"x": 1})
	MergeAnnotations(context.Background(), payload)
}

func TestQueueDropsMiddleKeepsTerminal(t *testing.T) {
	queue := NewQueue(4)
	var mu sync.Mutex
	var written []Event
	var droppedSeen bool
	done := make(chan struct{})
	go func() {
		queue.Range(func(ev Event, dropped bool) error {
			mu.Lock()
			written = append(written, ev)
			if dropped {
				droppedSeen = true
			}
			mu.Unlock()
			return nil
		})
		close(done)
	}()

	// 只入队不消费：先占满队列，再塞中间事件与终态事件。
	for i := 0; i < 10; i++ {
		queue.Push(Event{Type: ModelWaiting, Sequence: int64(i + 1)})
	}
	queue.Push(Event{Type: RunCompleted, Sequence: 99})
	// 同步点保证终态已写出。
	queue.Sync()
	queue.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	var terminal int
	for _, ev := range written {
		if ev.Type.Terminal() {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal events=%d, want 1 (written=%d)", terminal, len(written))
	}
	if !droppedSeen {
		t.Fatal("dropped marker should be set when middle events are discarded")
	}
}

func TestQueueSyncWaitsForPendingEvents(t *testing.T) {
	queue := NewQueue(8)
	var count int
	var mu sync.Mutex
	go queue.Range(func(Event, bool) error {
		mu.Lock()
		count++
		mu.Unlock()
		return nil
	})
	for i := 0; i < 5; i++ {
		queue.Push(Event{Type: ToolCompleted, Sequence: int64(i + 1)})
	}
	queue.Sync()
	mu.Lock()
	got := count
	mu.Unlock()
	if got != 5 {
		t.Fatalf("consumed=%d, want 5", got)
	}
	queue.Close()
}

func TestFileStoreRoundTrip(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run := Run{RunID: "run-a", SessionID: "session-a", Status: StatusRunning, StartedAt: time.Now()}
	if err = store.StartRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{Version: Version, EventID: "e1", RunID: "run-a", SessionID: "session-a", Sequence: 1, OccurredAt: time.Now(), Type: RunStarted},
		{Version: Version, EventID: "e2", RunID: "run-a", SessionID: "session-a", Sequence: 2, OccurredAt: time.Now(), Type: ToolCompleted, Payload: map[string]any{"tool_name": "current_time"}},
	}
	if err = store.AppendEvents(ctx, "run-a", events); err != nil {
		t.Fatal(err)
	}
	// 重复写入必须幂等。
	if err = store.AppendEvents(ctx, "run-a", events); err != nil {
		t.Fatal(err)
	}
	if err = store.LinkRunMessage(ctx, "run-a", 0, 1); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishRun(ctx, "run-a", StatusCompleted); err != nil {
		t.Fatal(err)
	}

	runs, err := store.ListRuns(ctx, "session-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != StatusCompleted || runs[0].FinishedAt == nil {
		t.Fatalf("runs=%+v", runs)
	}
	if runs[0].AssistantSequence == nil || *runs[0].AssistantSequence != 1 {
		t.Fatalf("assistant sequence not linked: %+v", runs[0])
	}

	listed, err := store.ListEvents(ctx, "session-a", "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("events=%d, want 2 (dedup)", len(listed))
	}
	after, err := store.ListEvents(ctx, "session-a", "run-a", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].Sequence != 2 {
		t.Fatalf("after_sequence filter=%+v", after)
	}

	max, err := store.MaxSequence(ctx, "run-a")
	if err != nil || max != 2 {
		t.Fatalf("max=%d err=%v", max, err)
	}

	if err = store.DeleteBySession(ctx, "session-a"); err != nil {
		t.Fatal(err)
	}
	listed, _ = store.ListEvents(ctx, "session-a", "", 0, 100)
	if len(listed) != 0 {
		t.Fatalf("events after delete=%d", len(listed))
	}
}

func TestFileStoreRecoverStale(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = store.StartRun(ctx, Run{RunID: "run-stale", SessionID: "session-b", Status: StatusRunning, StartedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.RecoverStale(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered=%d, want 1", recovered)
	}
	runs, _ := store.ListRuns(ctx, "session-b", 10)
	if len(runs) != 1 || runs[0].Status != StatusFailed {
		t.Fatalf("runs=%+v", runs)
	}
	events, _ := store.ListEvents(ctx, "session-b", "run-stale", 0, 10)
	if len(events) != 1 || events[0].Type != RunFailed {
		t.Fatalf("events=%+v", events)
	}
}

func TestWithoutChunksAndCount(t *testing.T) {
	events := []Event{{Type: Chunk}, {Type: RunStarted}, {Type: RunCompleted}}
	if got := len(WithoutChunks(events)); got != 2 {
		t.Fatalf("without chunks=%d", got)
	}
	if got := CountExecution(events); got != 2 {
		t.Fatalf("execution count=%d", got)
	}
}
