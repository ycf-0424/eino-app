package memory

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestMaintainCleanup exercises the real MySQL cleanup statements. It is
// opt-in because the repository's integration database is shared by local
// development and must never be modified by the default test command.
func TestMaintainCleanup(t *testing.T) {
	if os.Getenv("MEMORY_INTEGRATION") != "1" {
		t.Skip("set MEMORY_INTEGRATION=1 for isolated MySQL tests")
	}
	r, s := integrationRepo(t)
	ctx := context.Background()
	old := time.Now().Add(-31 * 24 * time.Hour)
	turnID := uuid.NewString()
	jobID := uuid.NewString()
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO memory_turns(turn_id,session_id,owner_id,project_id,source_message_id,input,state,suppressed,created_at,updated_at) VALUES(?,?,?,?,?,JSON_OBJECT(), 'processed',FALSE,?,?)`, turnID, uuid.NewString(), r.Config.OwnerID, r.Config.ProjectID, uuid.NewString(), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO memory_decisions(turn_id,ordinal,action,reason_code,memory_id,prompt_version,model,created_at) VALUES(?,?,?,?,?,?,?,?)`, turnID, 0, "ignore", "test", "", PromptVersion, "test", old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO memory_jobs(id,owner_id,project_id,kind,target,version,generation,dedupe_key,status,attempts,available_at,updated_at) VALUES(?,?,?,?,?,?,?,?, 'succeeded', 5, ?, ?)`, jobID, r.Config.OwnerID, r.Config.ProjectID, "extract", turnID, 0, "", "cleanup-test-"+jobID, old, old); err != nil {
		t.Fatal(err)
	}
	factID := uuid.NewString()
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO memory_facts(id,owner_id,scope_kind,scope_id,type,fact_key,value,content_hash,version,state,index_state,generation,expires_at,last_turn_seq,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, factID, r.Config.OwnerID, "project", r.Config.ProjectID, "project_fact", "cleanup_test", "current", Hash("current"), 5, "active", "not_required", r.Generation, nil, 1, old, old); err != nil {
		t.Fatal(err)
	}
	failedIndexID := uuid.NewString()
	if _, err := s.DB().ExecContext(ctx, `UPDATE memory_facts SET index_state='pending' WHERE id=?`, factID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO memory_jobs(id,owner_id,project_id,kind,target,version,generation,dedupe_key,status,attempts,available_at,updated_at) VALUES(?,?,?,?,?,?,?,?, 'failed', 5, ?, ?)`, failedIndexID, r.Config.OwnerID, r.Config.ProjectID, "index", factID, 5, r.Generation, "cleanup-test-index-"+failedIndexID, old, old); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 5; version++ {
		if _, err := s.DB().ExecContext(ctx, `INSERT INTO memory_versions(memory_id,version,value,state,reason,created_at) VALUES(?,?,?,?,?,?)`, factID, version, "v", "active", "test", old); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{Repo: r, Index: &fakeIndex{facts: map[string]Fact{}}}
	if err := e.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_turns WHERE turn_id=?", turnID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("old turn count=%d err=%v", count, err)
	}
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_jobs WHERE id=?", jobID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("old job count=%d err=%v", count, err)
	}
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_versions WHERE memory_id=?", factID).Scan(&count); err != nil || count != 3 {
		t.Fatalf("version count=%d err=%v", count, err)
	}
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_facts WHERE id=? AND state='active'", factID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("fact count=%d err=%v", count, err)
	}
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_jobs WHERE id=?", failedIndexID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed current index job count=%d err=%v", count, err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE memory_facts SET index_state='indexed' WHERE id=?`, factID); err != nil {
		t.Fatal(err)
	}
	if err := e.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_jobs WHERE id=?", failedIndexID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("indexed terminal job count=%d err=%v", count, err)
	}
}
