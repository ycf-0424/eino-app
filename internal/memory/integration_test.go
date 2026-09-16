package memory

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"my-eino-app/internal/config"
	"my-eino-app/internal/session"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixedExtractor struct{ candidates []Candidate }

func (f *fixedExtractor) Extract(_ context.Context, in ExtractInput) ([]Candidate, error) {
	cs := append([]Candidate{}, f.candidates...)
	for i := range cs {
		cs[i].SourceID = in.Message.ID
		cs[i].Evidence = in.Message.Text
	}
	return cs, nil
}

type fakeIndex struct {
	mu     sync.Mutex
	facts  map[string]Fact
	fail   bool
	during func()
}

func (f *fakeIndex) UpsertVersion(ctx context.Context, v Fact) error {
	f.mu.Lock()
	f.facts[VectorID(v)] = v
	hook := f.during
	f.during = nil
	fail := f.fail
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if fail {
		return fmt.Errorf("index unavailable")
	}
	return nil
}
func (f *fakeIndex) DeleteVersions(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		delete(f.facts, id)
	}
	return nil
}
func (f *fakeIndex) Search(_ context.Context, s Scope, _ string, _ int) ([]Hit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Hit{}
	for id, v := range f.facts {
		if v.Scope == s {
			out = append(out, Hit{id, 1})
		}
	}
	return out, nil
}

func integrationRepo(t *testing.T) (*Repository, *session.Store) {
	t.Helper()
	if os.Getenv("MEMORY_INTEGRATION") != "1" {
		t.Skip("set MEMORY_INTEGRATION=1 for isolated MySQL tests")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Session.Store = "mysql"
	s, err := session.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err = s.DB().Exec("SELECT 1 FROM memory_turns LIMIT 0"); err != nil {
		t.Fatalf("run explicit memory migration first: %v", err)
	}
	owner := "test-" + uuid.NewString()
	cfg.Memory = config.Memory{Enabled: true, IdentityMode: "local_single_user", OwnerID: owner, ProjectID: owner, AutoExtract: true}
	if err = cfg.ValidateMemory(); err != nil {
		t.Fatal(err)
	}
	r := &Repository{DB: s.DB(), Config: cfg.Memory, Generation: "test-generation", Model: "stub"}
	if err = s.SetTransactionHook(r.Capture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		db := s.DB()
		db.ExecContext(ctx, "DELETE d FROM memory_decisions d JOIN memory_turns t ON d.turn_id=t.turn_id WHERE t.owner_id=?", owner)
		db.ExecContext(ctx, "DELETE v FROM memory_versions v JOIN memory_facts f ON v.memory_id=f.id WHERE f.owner_id=?", owner)
		db.ExecContext(ctx, "DELETE v FROM memory_sources v JOIN memory_facts f ON v.memory_id=f.id WHERE f.owner_id=?", owner)
		db.ExecContext(ctx, "DELETE c FROM conversations c JOIN memory_bindings b ON b.session_id=c.id WHERE b.owner_id=?", owner)
		for _, table := range []string{"memory_controls", "memory_jobs", "memory_facts", "memory_turns", "memory_bindings", "memory_locks"} {
			db.ExecContext(ctx, "DELETE FROM "+table+" WHERE owner_id=?", owner)
		}
	})
	return r, s
}
func saveTurn(t *testing.T, s *session.Store, text string) string {
	t.Helper()
	id := uuid.NewString()
	u := schema.UserMessage(text)
	u.Extra = map[string]any{"memory_turn": id}
	a := schema.AssistantMessage("收到", nil)
	a.Extra = map[string]any{"storage_status": "completed", "storage_turn": id}
	if err := s.Save(uuid.NewString(), []*schema.Message{u, a}); err != nil {
		t.Fatal(err)
	}
	return id
}
func drain(t *testing.T, e *Engine) {
	t.Helper()
	for i := 0; i < 30; i++ {
		worked, err := e.ProcessOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			return
		}
	}
	t.Fatal("queue did not drain")
}
func TestMySQLMemoryLifecycle(t *testing.T) {
	r, s := integrationRepo(t)
	ctx := context.Background()
	x := &fixedExtractor{}
	idx := &fakeIndex{facts: map[string]Fact{}}
	e := &Engine{Repo: r, Extractor: x, Index: idx}
	saveTurn(t, s, "你好")
	drain(t, e)
	fs, err := r.List(ctx)
	if err != nil || len(fs) != 0 {
		t.Fatalf("smalltalk: %v %v", fs, err)
	}
	x.candidates = []Candidate{{Type: "decision", ScopeKind: "project", Key: "vector_database", Value: "Milvus", Relation: "assert"}}
	first := saveTurn(t, s, "我们决定使用 Milvus")
	drain(t, e)
	fs, err = r.List(ctx)
	if err != nil || len(fs) != 1 || fs[0].IndexState != "indexed" {
		t.Fatalf("insert: %+v %v", fs, err)
	}
	f := fs[0]
	saveTurn(t, s, "我们决定使用 Milvus")
	drain(t, e)
	fs, _ = r.List(ctx)
	if fs[0].Version != 1 {
		t.Fatal("duplicate created revision")
	}
	x.candidates[0].Value = "PostgreSQL"
	saveTurn(t, s, "我们使用 PostgreSQL")
	drain(t, e)
	fs, _ = r.List(ctx)
	if fs[0].Value != "Milvus" {
		t.Fatal("unconfirmed conflict overwrote fact")
	}
	x.candidates[0].Relation = "correct"
	saveTurn(t, s, "之前说错了，现在改用 PostgreSQL")
	drain(t, e)
	fs, _ = r.List(ctx)
	if fs[0].Version != 2 || fs[0].Value != "PostgreSQL" {
		t.Fatalf("update %+v", fs)
	}
	recalled, err := e.Recall(ctx, "数据库")
	if err != nil || len(recalled) != 1 || recalled[0].Value != "PostgreSQL" {
		t.Fatalf("recall %+v %v", recalled, err)
	}
	other := *r
	other.Config.ProjectID = "another-project"
	otherEngine := &Engine{Repo: &other, Index: idx}
	recalled, err = otherEngine.Recall(ctx, "数据库")
	if err != nil || len(recalled) != 0 {
		t.Fatal("cross project leak")
	}
	if err = r.Revoke(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	var revokedVersion int64
	if err = r.DB.QueryRowContext(ctx, "SELECT version FROM memory_versions WHERE memory_id=? AND state='revoked' ORDER BY version DESC LIMIT 1", f.ID).Scan(&revokedVersion); err != nil {
		t.Fatal(err)
	}
	if revokedVersion != 3 {
		t.Fatalf("missing revoke tombstone version: %d", revokedVersion)
	}
	if err = r.Revoke(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	var versionCount int
	if err = r.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_versions WHERE memory_id=?", f.ID).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 3 {
		t.Fatalf("repeated revoke created a revision: %d", versionCount)
	}
	recalled, err = e.Recall(ctx, "数据库")
	if err != nil || len(recalled) != 0 {
		t.Fatal("revoked vector visible before cleanup")
	}
	drain(t, e)
	if len(idx.facts) != 0 {
		t.Fatalf("vectors remain: %d", len(idx.facts))
	}
	// A later explicit assertion may recreate a fact after it was revoked.
	x.candidates[0].Relation = "assert"
	x.candidates[0].Value = "Milvus"
	reassert := saveTurn(t, s, "我们重新决定使用 Milvus")
	drain(t, e)
	fs, err = r.List(ctx)
	if err != nil || len(fs) != 1 || fs[0].State != "active" || fs[0].Value != "Milvus" {
		t.Fatalf("reassert after revoke: %+v %v", fs, err)
	}
	status, err := r.Status(ctx, reassert)
	if err != nil || status["state"] != "processed" {
		t.Fatalf("reassert status %v %v", status, err)
	}
	status, err = r.Status(ctx, first)
	if err != nil || status["state"] != "processed" {
		t.Fatalf("status %v %v", status, err)
	}
}
func TestMySQLAtomicCapture(t *testing.T) {
	r, s := integrationRepo(t)
	ctx := context.Background()
	s.SetTransactionHook(func(ctx context.Context, tx *sql.Tx, id string, ms []*schema.Message) error {
		if err := r.Capture(ctx, tx, id, ms); err != nil {
			return err
		}
		return fmt.Errorf("injected failure")
	})
	id := uuid.NewString()
	u := schema.UserMessage("我是工程师")
	u.Extra = map[string]any{"memory_turn": id}
	a := schema.AssistantMessage("ok", nil)
	a.Extra = map[string]any{"storage_status": "completed"}
	if s.Save(id, []*schema.Message{u, a}) == nil {
		t.Fatal("expected rollback")
	}
	var count int
	r.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory_turns WHERE turn_id=?", id).Scan(&count)
	if count != 0 {
		t.Fatal("turn escaped rollback")
	}
	ms, err := s.Load(id)
	if err != nil || len(ms) != 0 {
		t.Fatal("history escaped rollback")
	}
}
func TestMySQLDeletionDuringIndex(t *testing.T) {
	r, s := integrationRepo(t)
	ctx := context.Background()
	x := &fixedExtractor{[]Candidate{{Type: "decision", ScopeKind: "project", Key: "vector_database", Value: "Milvus", Relation: "assert"}}}
	idx := &fakeIndex{facts: map[string]Fact{}}
	e := &Engine{Repo: r, Extractor: x, Index: idx}
	saveTurn(t, s, "我们决定使用 Milvus")
	if _, err := e.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	fs, _ := r.List(ctx)
	idx.during = func() {
		if err := r.Revoke(ctx, fs[0].ID); err != nil {
			t.Error(err)
		}
	}
	drain(t, e)
	if len(idx.facts) != 0 {
		t.Fatal("stale task resurrected deleted vector")
	}
}
func TestMySQLLeaseRecovery(t *testing.T) {
	r, s := integrationRepo(t)
	ctx := context.Background()
	saveTurn(t, s, "你好")
	j, err := r.Claim(ctx)
	if err != nil || j == nil {
		t.Fatal(err)
	}
	r.DB.ExecContext(ctx, "UPDATE memory_jobs SET lease_until=? WHERE id=?", time.Now().Add(-time.Minute), j.ID)
	replacement, err := r.Claim(ctx)
	if err != nil || replacement == nil || replacement.Token == j.Token {
		t.Fatal("lease not recovered")
	}
	if err = r.Finish(ctx, j, nil); err != nil {
		t.Fatal(err)
	}
	var token string
	r.DB.QueryRowContext(ctx, "SELECT lease_token FROM memory_jobs WHERE id=?", j.ID).Scan(&token)
	if token != replacement.Token {
		t.Fatal("old worker finished reclaimed task")
	}
}
func TestMySQLSensitiveAndSuppressed(t *testing.T) {
	r, s := integrationRepo(t)
	x := &fixedExtractor{[]Candidate{{Type: "profile", ScopeKind: "user", Key: "secret", Value: "bad", Relation: "assert"}}}
	e := &Engine{Repo: r, Extractor: x, Index: &fakeIndex{facts: map[string]Fact{}}}
	for _, msg := range []string{"密码是123456", "不要记住：我是开发者"} {
		id := saveTurn(t, s, msg)
		drain(t, e)
		turn, err := r.LoadTurn(context.Background(), id)
		if err != nil || !turn.Suppressed || strings.Contains(turn.Input.Message.Text, msg) {
			t.Fatalf("suppression %+v %v", turn, err)
		}
	}
	fs, _ := r.List(context.Background())
	if len(fs) != 0 {
		t.Fatal("saved blocked information")
	}
}

func TestMySQLSuppressionBlocksQueuedTurn(t *testing.T) {
	r, s := integrationRepo(t)
	x := &fixedExtractor{candidates: []Candidate{{Type: "decision", ScopeKind: "project", Key: "vector_database", Value: "Milvus", Relation: "assert"}}}
	e := &Engine{Repo: r, Extractor: x, Index: &fakeIndex{facts: map[string]Fact{}}}
	oldTurn := saveTurn(t, s, "我们决定使用 Milvus")
	_ = saveTurn(t, s, "不要记住上面这件事")
	drain(t, e)
	fs, err := r.List(context.Background())
	if err != nil || len(fs) != 0 {
		t.Fatalf("suppressed queued turn created memory: %+v %v", fs, err)
	}
	status, err := r.Status(context.Background(), oldTurn)
	if err != nil || status["state"] != "processed" {
		t.Fatalf("old turn status=%v err=%v", status, err)
	}
	decisions, ok := status["decisions"].([]map[string]string)
	if !ok || len(decisions) == 0 || decisions[0]["reason"] != "suppressed" {
		t.Fatalf("old turn was not marked suppressed: %v", status)
	}
}
