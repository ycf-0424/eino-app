package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoaderLoadListAndRejectTraversal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("业务规则"), 0o600); err != nil {
		t.Fatal(err)
	}
	loader := NewLoader(dir)
	got, err := loader.Load("demo")
	if err != nil || got.Instruction != "业务规则" {
		t.Fatalf("Load=%+v,%v", got, err)
	}
	names, err := loader.List()
	if err != nil || len(names) != 1 || names[0] != "demo" {
		t.Fatalf("List=%v,%v", names, err)
	}
	if _, err := loader.Load("../escape"); err == nil {
		t.Fatal("expected traversal rejection")
	}
}
