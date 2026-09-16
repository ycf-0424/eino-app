package rag

import (
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestManifestIncrementalRoundTrip(t *testing.T) {
	docs := []*schema.Document{{ID: "a", Content: "one"}, {ID: "b", Content: "two"}}
	changed, next := ChangedDocs(docs, Manifest{Files: map[string]string{}})
	if len(changed) != 2 {
		t.Fatalf("first index changed=%d", len(changed))
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := SaveManifest(path, next); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	changed, _ = ChangedDocs(docs, loaded)
	if len(changed) != 0 {
		t.Fatalf("unchanged docs indexed again: %d", len(changed))
	}
	docs[1].Content = "updated"
	changed, _ = ChangedDocs(docs, loaded)
	if len(changed) != 1 || changed[0].ID != "b" {
		t.Fatalf("wrong changed docs: %+v", changed)
	}
}
