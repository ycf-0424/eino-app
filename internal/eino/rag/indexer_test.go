package rag

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/schema"
	"os"
	"path/filepath"
	"testing"
)

type indexTestStore struct {
	fail    bool
	deleted []string
	rows    map[string]bool
}

func (s *indexTestStore) Index(_ context.Context, ds []*schema.Document) ([]string, error) {
	if s.fail {
		return nil, errors.New("injected")
	}
	out := []string{}
	for _, d := range ds {
		s.rows[d.ID] = true
		out = append(out, d.ID)
	}
	return out, nil
}
func (s *indexTestStore) Search(context.Context, string, int) ([]*schema.Document, error) {
	return nil, nil
}
func (s *indexTestStore) DeleteIDs(_ context.Context, ids []string) error {
	for _, id := range ids {
		delete(s.rows, id)
		s.deleted = append(s.deleted, id)
	}
	return nil
}
func (s *indexTestStore) Exists(_ context.Context, ids []string) (map[string]bool, error) {
	return s.rows, nil
}
func TestIndexerFailureKeepsOldAndUnknown(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("old document"), 0600)
	store := &indexTestStore{rows: map[string]bool{"foreign-memory": true}}
	i, _ := NewIndexer(store, IndexOptions{DocumentDir: dir, ChunkSize: 800, ChunkOverlap: 10, Store: "test", Target: dir})
	if _, e := i.Run(context.Background()); e != nil {
		t.Fatal(e)
	}
	os.Remove(p)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new document"), 0600)
	store.fail = true
	if _, e := i.Run(context.Background()); e == nil {
		t.Fatal("expected index error")
	}
	if len(store.deleted) > 0 || !store.rows["foreign-memory"] {
		t.Fatal("deleted before successful write")
	}
	store.fail = false
	report, e := i.Run(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if report.RemovedChunks != 1 || len(store.deleted) != 1 || !store.rows["foreign-memory"] {
		t.Fatalf("unexpected cleanup %+v %+v", report, store)
	}
}
