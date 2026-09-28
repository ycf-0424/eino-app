package tool

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMinimalDocx(t *testing.T, path string, body string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	part, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte(`<w:document><w:body><w:p><w:r><w:t>` + body + `</w:t></w:r></w:p></w:body></w:document>`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRewriteDocumentXMLReplacesAndAppends(t *testing.T) {
	data := []byte(`<w:document><w:body><w:p><w:r><w:t>Hello &amp; team</w:t></w:r></w:p></w:body></w:document>`)
	got, operations, err := rewriteDocumentXML(data, []DocumentReplacement{{Find: "Hello & team", Replace: "Hi & team"}}, []string{"追加内容"})
	if err != nil {
		t.Fatal(err)
	}
	if operations != 2 || !strings.Contains(string(got), "Hi &amp; team") || !strings.Contains(string(got), "追加内容") {
		t.Fatalf("operations=%d xml=%s", operations, got)
	}
	if _, _, err := rewriteDocumentXML(data, []DocumentReplacement{{Find: "不存在", Replace: "x"}}, nil); err == nil {
		t.Fatal("expected missing replacement error")
	}
}

func TestDocumentPathAndOutputSafety(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.docx")
	writeMinimalDocx(t, source, "original")
	originalBefore, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDocumentPath(filepath.Join(root, "..", filepath.Base(root), "source.docx"), []string{root}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDocumentPath(filepath.Join(root, "..", "outside.docx"), []string{root}); err == nil {
		t.Fatal("expected path outside root rejection")
	}
	if _, err := safeDocxOutputName("../bad.docx", source); err == nil {
		t.Fatal("expected unsafe output rejection")
	}
	if _, err := safeDocxOutputName("source.docx", source); err == nil {
		t.Fatal("expected source overwrite rejection")
	}
	if _, err := safeDocxOutputName("", source); err == nil {
		t.Fatal("expected output name requirement")
	}
	archive, _, err := editDocx(source, 1<<20, nil, []string{"new paragraph"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(root, "out-*.docx")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDocxArchive(file, archive); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(original, originalBefore) {
		t.Fatalf("source should remain intact: %v", err)
	}
}
