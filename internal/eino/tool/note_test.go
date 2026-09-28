package tool

import "testing"

func TestWriteNoteRequestedIsConservative(t *testing.T) {
	for _, query := range []string{
		"请把这段内容写入笔记",
		"创建一个项目决策文档",
		"save this as a note",
		"export the result to a file",
	} {
		if !WriteNoteRequested(query) {
			t.Fatalf("expected explicit note intent for %q", query)
		}
	}
	for _, query := range []string{
		"你好",
		"请记住我们使用 Milvus",
		"我们已经决定使用 Milvus",
		"以后回答简洁一些",
		"请先读取授权目录中的 workspace-files/README.txt 正文，再根据正文写一份中文报告。",
		"读取 /workspace-files/report.txt 后，写一份报告。",
	} {
		if WriteNoteRequested(query) {
			t.Fatalf("unexpected note intent for %q", query)
		}
	}
}
