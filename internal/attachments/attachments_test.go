package attachments

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryRepository struct {
	mu        sync.Mutex
	items     map[string]Attachment
	artifacts map[string][]Artifact
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{items: map[string]Attachment{}, artifacts: map[string][]Artifact{}}
}
func (r *memoryRepository) Create(_ context.Context, item Attachment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[item.ID] = item
	return nil
}
func (r *memoryRepository) BeginProcessing(_ context.Context, owner, id string, expected Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok || item.OwnerID != owner {
		return ErrNotFound
	}
	if item.Status != expected {
		return ErrInvalidState
	}
	item.Status = StatusProcessing
	r.items[id] = item
	return nil
}
func (r *memoryRepository) Get(_ context.Context, owner, id string) (Attachment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok || item.OwnerID != owner {
		return Attachment{}, ErrNotFound
	}
	return item, nil
}
func (r *memoryRepository) List(_ context.Context, owner string, limit int) ([]Attachment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Attachment{}
	for _, item := range r.items {
		if item.OwnerID == owner {
			out = append(out, item)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func (r *memoryRepository) SetStatus(_ context.Context, owner, id string, status Status, reason, version string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok || item.OwnerID != owner {
		return ErrNotFound
	}
	item.Status, item.ErrorReason, item.ParserVersion, item.UpdatedAt = status, reason, version, time.Now().UTC()
	r.items[id] = item
	return nil
}
func (r *memoryRepository) SaveArtifacts(_ context.Context, owner, id string, items []Artifact) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok || item.OwnerID != owner {
		return ErrNotFound
	}
	r.artifacts[id] = append([]Artifact(nil), items...)
	return nil
}
func (r *memoryRepository) Artifacts(_ context.Context, owner, id string) ([]Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok || item.OwnerID != owner {
		return nil, ErrNotFound
	}
	return append([]Artifact(nil), r.artifacts[id]...), nil
}
func (r *memoryRepository) Delete(_ context.Context, owner, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok || item.OwnerID != owner {
		return ErrNotFound
	}
	delete(r.items, id)
	delete(r.artifacts, id)
	return nil
}
func (r *memoryRepository) Expired(_ context.Context, before time.Time, limit int) ([]Attachment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Attachment{}
	for _, item := range r.items {
		if !item.RetainedUntil.After(before) {
			out = append(out, item)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

type processorFunc func(context.Context, string, Attachment) ([]Artifact, string, error)

func (f processorFunc) Process(ctx context.Context, path string, item Attachment) ([]Artifact, string, error) {
	return f(ctx, path, item)
}

type scannerFunc func(context.Context, string) error

func (f scannerFunc) Scan(ctx context.Context, path string) error { return f(ctx, path) }

func newTestManager(t *testing.T, repo Repository, processor Processor, scanner Scanner) *Manager {
	t.Helper()
	manager, err := NewManager(Policy{Root: filepath.Join(t.TempDir(), "uploads"), MaxFileBytes: 1024, MaxPerRequest: 2, Retention: 24 * time.Hour,
		AllowedMIMETypes: []string{"text/csv", "text/plain"}}, repo, processor, scanner)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestUploadIsOwnerScopedAndDeleteRemovesStoredFile(t *testing.T) {
	repo := newMemoryRepository()
	manager := newTestManager(t, repo, processorFunc(func(_ context.Context, path string, item Attachment) ([]Artifact, string, error) {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("processor did not receive generated storage path: %v", err)
		}
		return []Artifact{{SourceRef: "CSV A1:B2", Text: "A1=alpha"}}, "csv-test-v1", nil
	}), scannerFunc(func(_ context.Context, path string) error {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
		return nil
	}))

	item, err := manager.Upload(context.Background(), "alice", "../../unsafe.csv", "text/csv", strings.NewReader("name,value\na,1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != StatusReady || item.OriginalName != "unsafe.csv" || filepath.Base(item.StorageKey) != item.StorageKey || strings.Contains(item.StorageKey, "unsafe") {
		t.Fatalf("unexpected stored attachment: %+v", item)
	}
	if _, err := manager.Get(context.Background(), "bob", item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign owner got item: %v", err)
	}
	contextText, err := manager.ContextFor(context.Background(), "alice", []string{item.ID, item.ID})
	if err != nil || !strings.Contains(contextText, "CSV A1:B2") {
		t.Fatalf("missing source evidence: %q, %v", contextText, err)
	}
	if _, err := manager.ContextFor(context.Background(), "bob", []string{item.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign owner read evidence: %v", err)
	}
	storedPath := filepath.Join(manager.root, item.StorageKey)
	if err := manager.Delete(context.Background(), "alice", item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stored file still exists: %v", err)
	}
}

func TestUploadRejectsOversizeTypeMismatchAndMalware(t *testing.T) {
	for _, test := range []struct {
		name, filename, mime, content string
		max                           int64
		scannerErr                    error
	}{
		{name: "oversize", filename: "a.csv", mime: "text/csv", content: strings.Repeat("a", 2048)},
		{name: "extension mismatch", filename: "a.pdf", mime: "application/pdf", content: "not a pdf"},
		{name: "declared mismatch", filename: "a.csv", mime: "application/pdf", content: "a,b\n1,2\n"},
		{name: "malware", filename: "a.csv", mime: "text/csv", content: "a,b\n1,2\n", scannerErr: ErrMalwareDetected},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newMemoryRepository()
			manager := newTestManager(t, repo, processorFunc(func(context.Context, string, Attachment) ([]Artifact, string, error) { return nil, "", nil }), scannerFunc(func(context.Context, string) error { return test.scannerErr }))
			manager.maxBytes = 1024
			if test.max > 0 {
				manager.maxBytes = test.max
			}
			_, err := manager.Upload(context.Background(), "alice", test.filename, test.mime, strings.NewReader(test.content))
			if err == nil {
				t.Fatal("upload unexpectedly succeeded")
			}
			items, _ := repo.List(context.Background(), "alice", 100)
			if len(items) != 0 {
				t.Fatalf("rejected upload wrote metadata: %+v", items)
			}
		})
	}
}

func TestFailedAttachmentCanRetryAndExpiredUploadIsCleaned(t *testing.T) {
	repo := newMemoryRepository()
	fail := true
	manager := newTestManager(t, repo, processorFunc(func(context.Context, string, Attachment) ([]Artifact, string, error) {
		if fail {
			return nil, "csv-test-v1", errors.New("provider unavailable")
		}
		return []Artifact{{SourceRef: "row 1", Text: "ready"}}, "csv-test-v2", nil
	}), scannerFunc(func(context.Context, string) error { return nil }))
	item, err := manager.Upload(context.Background(), "alice", "evidence.csv", "text/csv", strings.NewReader("a,b\n1,2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != StatusFailed {
		t.Fatalf("failed processor status=%q", item.Status)
	}
	if _, err := manager.ContextFor(context.Background(), "alice", []string{item.ID}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("failed artifact was exposed: %v", err)
	}
	fail = false
	item, err = manager.Retry(context.Background(), "alice", item.ID)
	if err != nil || item.Status != StatusReady {
		t.Fatalf("retry did not recover: %+v, %v", item, err)
	}
	storedPath := filepath.Join(manager.root, item.StorageKey)
	repo.mu.Lock()
	expired := repo.items[item.ID]
	expired.RetainedUntil = time.Now().Add(-time.Minute)
	repo.items[item.ID] = expired
	repo.mu.Unlock()
	removed, err := manager.CleanupExpired(context.Background(), time.Now(), 10)
	if err != nil || removed != 1 {
		t.Fatalf("cleanup removed=%d, err=%v", removed, err)
	}
	if _, err := os.Stat(storedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired file remains: %v", err)
	}
	if _, err := manager.Get(context.Background(), "alice", item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired metadata remains: %v", err)
	}
}

var _ Repository = (*memoryRepository)(nil)
var _ = io.EOF
