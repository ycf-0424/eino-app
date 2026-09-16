package tool

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalFileReadTool(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("星河系统"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool, err := NewLocalFileReadTool([]string{root}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.InvokableRun(context.Background(), `{"path":"hello.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "星河系统") {
		t.Fatalf("result = %q", result)
	}
}

func TestLocalFileReadToolRejectsOutsideAndUnsafeContent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	tool, err := NewLocalFileReadTool([]string{root}, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outsideFile, "large.txt", "binary.bin"} {
		args := fmt.Sprintf(`{"path":%q}`, path)
		if _, err := tool.InvokableRun(context.Background(), args); err == nil {
			t.Fatalf("expected %q to be rejected", path)
		}
	}
}

func TestLocalFileReadToolExtractsDocx(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "work-order.docx")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	document, err := archive.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = document.Write([]byte(`<w:document xmlns:w="urn:test"><w:body><w:p><w:r><w:t>第一项</w:t></w:r></w:p><w:p><w:r><w:t>第二项</w:t></w:r></w:p></w:body></w:document>`))
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	tool, err := NewLocalFileReadTool([]string{root}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.InvokableRun(context.Background(), `{"path":"work-order.docx"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "第一项\n第二项") {
		t.Fatalf("result = %q", result)
	}
}
