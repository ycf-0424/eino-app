package dataquery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func validOperation(handler Handler) Operation {
	return Operation{
		Source: "project_assets", ID: "by_id",
		Parameters:    map[string]Parameter{"id": {Kind: StringValue, Required: true, MaxBytes: 32}},
		AllowedFields: []string{"id", "status"}, RedactedFields: []string{"internal_note"},
		OwnerRequired: true, ReadOnly: true, MaxRows: 10, MaxBytes: 4096,
		Timeout: time.Second, FreshnessTTL: time.Minute, Handler: handler,
	}
}

func TestRegistryValidatesAndFiltersResult(t *testing.T) {
	registry := NewRegistry()
	var gotOwner string
	if err := registry.Register(validOperation(func(_ context.Context, req QueryRequest) (QueryResult, error) {
		gotOwner = req.OwnerID
		return QueryResult{
			Rows: []map[string]any{{"id": "A-1", "status": "active", "internal_note": "private", "secret_token": "hidden", "extra": "drop"}},
			AsOf: time.Now().UTC(),
		}, nil
	})); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(context.Background(), QueryRequest{
		Source: "project_assets", Operation: "by_id", Arguments: map[string]any{"id": "A-1"}, OwnerID: "local:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotOwner != "local:abc" {
		t.Fatalf("handler owner=%q", gotOwner)
	}
	if result.Source != "project_assets" || result.Operation != "by_id" || result.AsOf.IsZero() || result.FreshUntil.IsZero() {
		t.Fatalf("missing server-owned result metadata: %+v", result)
	}
	if len(result.Rows) != 1 || len(result.Rows[0]) != 2 || result.Rows[0]["id"] != "A-1" || result.Rows[0]["status"] != "active" {
		t.Fatalf("result fields were not allowlisted: %+v", result.Rows)
	}
}

func TestRegistryRejectsUnregisteredAndUnsafeRequests(t *testing.T) {
	registry := NewRegistry()
	if !errors.Is(mustExecute(registry, QueryRequest{}), ErrDisabled) {
		t.Fatal("empty registry should be disabled")
	}
	if err := registry.Register(validOperation(func(context.Context, QueryRequest) (QueryResult, error) {
		return QueryResult{AsOf: time.Now()}, nil
	})); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		req  QueryRequest
		want error
	}{
		{name: "unknown source", req: QueryRequest{Source: "other", Operation: "by_id", OwnerID: "o", Arguments: map[string]any{"id": "A"}}, want: ErrUnknownSource},
		{name: "raw sql operation", req: QueryRequest{Source: "project_assets", Operation: "SELECT 1", OwnerID: "o", Arguments: map[string]any{"id": "A"}}, want: ErrUnknownOp},
		{name: "missing owner", req: QueryRequest{Source: "project_assets", Operation: "by_id", Arguments: map[string]any{"id": "A"}}, want: ErrForeignOwner},
		{name: "unknown argument", req: QueryRequest{Source: "project_assets", Operation: "by_id", OwnerID: "o", Arguments: map[string]any{"id": "A", "url": "http://127.0.0.1"}}, want: ErrInvalidRequest},
		{name: "missing argument", req: QueryRequest{Source: "project_assets", Operation: "by_id", OwnerID: "o", Arguments: map[string]any{}}, want: ErrInvalidRequest},
		{name: "oversized argument", req: QueryRequest{Source: "project_assets", Operation: "by_id", OwnerID: "o", Arguments: map[string]any{"id": "012345678901234567890123456789012"}}, want: ErrInvalidRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := registry.Execute(context.Background(), test.req)
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
		})
	}
}

func TestRegistryRequiresReadOnlyBoundedOperations(t *testing.T) {
	registry := NewRegistry()
	op := validOperation(func(context.Context, QueryRequest) (QueryResult, error) { return QueryResult{}, nil })
	op.ReadOnly = false
	if err := registry.Register(op); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("write operation register error=%v", err)
	}
	op = validOperation(func(context.Context, QueryRequest) (QueryResult, error) { return QueryResult{}, nil })
	op.AllowedFields = []string{"api_key"}
	if err := registry.Register(op); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("sensitive allowlist field register error=%v", err)
	}
	if err := registry.Register(validOperation(func(context.Context, QueryRequest) (QueryResult, error) { return QueryResult{}, nil })); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(validOperation(func(context.Context, QueryRequest) (QueryResult, error) { return QueryResult{}, nil })); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("duplicate operation register error=%v", err)
	}
}

func TestRegistryRejectsStaleOverlargeAndSlowResults(t *testing.T) {
	tests := []struct {
		name    string
		handler Handler
		want    error
	}{
		{
			name: "stale",
			handler: func(context.Context, QueryRequest) (QueryResult, error) {
				return QueryResult{AsOf: time.Now().Add(-time.Hour)}, nil
			},
			want: ErrStaleResult,
		},
		{
			name: "too many rows",
			handler: func(context.Context, QueryRequest) (QueryResult, error) {
				return QueryResult{AsOf: time.Now(), Rows: make([]map[string]any, 11)}, nil
			},
			want: ErrInvalidRequest,
		},
		{
			name: "too much output",
			handler: func(context.Context, QueryRequest) (QueryResult, error) {
				return QueryResult{AsOf: time.Now(), Rows: []map[string]any{{"id": string(make([]byte, 5000))}}}, nil
			},
			want: ErrInvalidRequest,
		},
		{
			name: "handler timeout",
			handler: func(ctx context.Context, _ QueryRequest) (QueryResult, error) {
				<-ctx.Done()
				return QueryResult{}, ctx.Err()
			},
			want: context.DeadlineExceeded,
		},
		{
			name: "handler timeout even when callback ignores context",
			handler: func(context.Context, QueryRequest) (QueryResult, error) {
				time.Sleep(20 * time.Millisecond)
				return QueryResult{AsOf: time.Now()}, nil
			},
			want: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			op := validOperation(test.handler)
			if strings.HasPrefix(test.name, "handler timeout") {
				op.Timeout = time.Millisecond
			}
			if err := registry.Register(op); err != nil {
				t.Fatal(err)
			}
			_, err := registry.Execute(context.Background(), QueryRequest{Source: op.Source, Operation: op.ID, OwnerID: "owner", Arguments: map[string]any{"id": "A"}})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
		})
	}
}

func mustExecute(registry *Registry, request QueryRequest) error {
	_, err := registry.Execute(context.Background(), request)
	return err
}
