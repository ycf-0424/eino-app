package checkpoint

import (
	"context"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Set(ctx, "run-1", []byte("state")); err != nil {
		t.Fatal(err)
	}
	b, ok, err := s.Get(ctx, "run-1")
	if err != nil || !ok || string(b) != "state" {
		t.Fatalf("Get = %q, %v, %v", b, ok, err)
	}
	if err := s.Delete(ctx, "run-1"); err != nil {
		t.Fatal(err)
	}
	_, ok, err = s.Get(ctx, "run-1")
	if err != nil || ok {
		t.Fatalf("deleted checkpoint still exists: %v, %v", ok, err)
	}
}

func TestPendingApprovalRoundTrip(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := PendingApproval{TargetID: "tool:1", ToolName: "write_note", Arguments: `{}`}
	if err := s.SaveApproval("run-1", want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadApproval("run-1")
	if err != nil || got == nil || got.TargetID != want.TargetID {
		t.Fatalf("LoadApproval = %#v, %v", got, err)
	}
	if err := s.ClearApproval("run-1"); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadApproval("run-1")
	if err != nil || got != nil {
		t.Fatalf("approval was not cleared: %#v, %v", got, err)
	}
}
