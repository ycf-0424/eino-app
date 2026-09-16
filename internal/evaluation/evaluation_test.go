package evaluation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.json")
	if err := os.WriteFile(path, []byte(`[{"question":"q","expected":"a"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases, err := LoadCases(path)
	if err != nil || len(cases) != 1 || cases[0].Question != "q" {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
}

func TestNormalizeIgnoresFormatting(t *testing.T) {
	if normalize("Go、Eino 和 Milvus") != normalize("Go，Eino、Milvus") {
		t.Fatal("format normalization mismatch")
	}
}
