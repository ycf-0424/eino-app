package rag

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitText(t *testing.T) {
	got := SplitText("一二三四五六", 4, 1)
	if len(got) != 2 || got[0] != "一二三四" || got[1] != "四五六" {
		t.Fatalf("SplitText() = %#v", got)
	}
}

func TestToFloat32(t *testing.T) {
	got := toFloat32([]float64{1.5, -2.25})
	if len(got) != 2 || got[0] != 1.5 || got[1] != -2.25 {
		t.Fatalf("toFloat32() = %#v", got)
	}
}

func TestLoadDocumentsSupportedFormats(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "guide.md"), "# 安装\n安装内容\n## 配置\n配置内容")
	writeTestFile(t, filepath.Join(dir, "page.html"), "<html><style>隐藏</style><body><h1>标题</h1><p>正文</p></body></html>")
	writeTestFile(t, filepath.Join(dir, "data.json"), `{"name":"星河系统","number":7319}`)
	writeDOCX(t, filepath.Join(dir, "manual.docx"), "第一段", "第二段")
	writeTestFile(t, filepath.Join(dir, "ignored.exe"), "不应加载")

	docs, err := LoadDocuments(dir, 800, 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 5 {
		t.Fatalf("got %d documents, want 5: %+v", len(docs), docs)
	}
	joined := ""
	for _, doc := range docs {
		joined += doc.Content
		if strings.Contains(doc.MetaData["source"].(string), dir) {
			t.Fatalf("source must be relative: %v", doc.MetaData)
		}
	}
	for _, want := range []string{"安装内容", "配置内容", "正文", "7319", "第一段"} {
		if !strings.Contains(joined, want) {
			t.Errorf("parsed content missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "隐藏") || strings.Contains(joined, "不应加载") {
		t.Fatalf("unexpected content: %s", joined)
	}
}

func TestLoadDocumentsInvalidChunkConfig(t *testing.T) {
	if _, err := LoadDocuments(t.TempDir(), 100, 100); err == nil {
		t.Fatal("expected invalid chunk error")
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeDOCX(t *testing.T, path string, paragraphs ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, err := z.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	var xml strings.Builder
	xml.WriteString(`<?xml version="1.0"?><w:document xmlns:w="x"><w:body>`)
	for _, p := range paragraphs {
		xml.WriteString("<w:p><w:r><w:t>" + p + "</w:t></w:r></w:p>")
	}
	xml.WriteString("</w:body></w:document>")
	if _, err = w.Write([]byte(xml.String())); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}
