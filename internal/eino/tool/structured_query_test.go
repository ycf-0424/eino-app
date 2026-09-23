package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"my-eino-app/internal/auth"
	"my-eino-app/internal/dataquery"
	"my-eino-app/internal/execution"
)

func TestStructuredQueryUsesServerOwnerAndAuditsRedactedEvents(t *testing.T) {
	registry := dataquery.NewRegistry()
	var seenOwner string
	err := registry.Register(dataquery.Operation{
		Source: "assets", ID: "by_id", Parameters: map[string]dataquery.Parameter{"id": {Kind: dataquery.StringValue, Required: true, MaxBytes: 32}},
		AllowedFields: []string{"id", "status"}, OwnerRequired: true, ReadOnly: true,
		MaxRows: 5, MaxBytes: 1024, Timeout: time.Second, FreshnessTTL: time.Minute,
		Handler: func(_ context.Context, request dataquery.QueryRequest) (dataquery.QueryResult, error) {
			seenOwner = request.OwnerID
			return dataquery.QueryResult{AsOf: time.Now().UTC(), Rows: []map[string]any{{"id": "A-1", "status": "ready", "api_key": "never-expose"}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewStructuredQueryTool(registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder := execution.NewRecorder(20)
	emitter := execution.NewSession("run-test", "session-test", nil, recorder, execution.SessionOptions{})
	defer emitter.Close()
	ctx := execution.WithEmitter(auth.WithOwner(context.Background(), "local:owner-a"), emitter)
	result, err := tool.InvokableRun(ctx, `{"source":"assets","operation":"by_id","arguments_json":"{\"id\":\"A-1\"}"}`)
	if err != nil {
		t.Fatal(err)
	}
	if seenOwner != "local:owner-a" {
		t.Fatalf("query owner=%q; owner must come from auth context", seenOwner)
	}
	if !strings.Contains(result, `"status":"ready"`) || strings.Contains(result, "never-expose") {
		t.Fatalf("unsafe result: %s", result)
	}
	events := recorder.Events()
	if len(events) != 2 || events[0].Type != execution.DataQueryStarted || events[1].Type != execution.DataQueryCompleted {
		t.Fatalf("query lifecycle events=%+v", events)
	}
	payload, _ := json.Marshal(events)
	if strings.Contains(string(payload), "A-1") || strings.Contains(string(payload), "api_key") || strings.Contains(string(payload), "never-expose") {
		t.Fatalf("query events contain raw arguments/results: %s", payload)
	}
}

func TestStructuredQueryRejectsDisabledAndRawArguments(t *testing.T) {
	if _, err := NewStructuredQueryTool(dataquery.NewRegistry()); err == nil {
		t.Fatal("empty registry was exposed")
	}
	registry := dataquery.NewRegistry()
	if err := registry.Register(dataquery.Operation{
		Source: "assets", ID: "by_id", Parameters: map[string]dataquery.Parameter{"id": {Kind: dataquery.StringValue, Required: true, MaxBytes: 32}},
		AllowedFields: []string{"id"}, ReadOnly: true, MaxRows: 1, MaxBytes: 128, Timeout: time.Second, FreshnessTTL: time.Minute,
		Handler: func(context.Context, dataquery.QueryRequest) (dataquery.QueryResult, error) {
			return dataquery.QueryResult{AsOf: time.Now()}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	tool, err := NewStructuredQueryTool(registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.InvokableRun(context.Background(), `{"source":"assets","operation":"by_id","arguments_json":"{\"id\":\"A\",\"sql\":\"DROP TABLE x\"}"}`); err == nil {
		t.Fatal("raw SQL was accepted")
	}
}
