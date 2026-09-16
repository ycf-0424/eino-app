package prompt

import (
	"context"
	"testing"
)

func TestFormatRAG(t *testing.T) {
	messages, err := FormatRAG(context.Background(), "幸运数字是 7319", "doc.md", "幸运数字是什么？")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Content == "" {
		t.Fatalf("unexpected rendered messages: %+v", messages)
	}
	if !contains(messages[1].Content, "7319") || !contains(messages[1].Content, "doc.md") {
		t.Fatalf("template variables were not rendered: %s", messages[1].Content)
	}
}

func contains(text, part string) bool {
	for i := 0; i+len(part) <= len(text); i++ {
		if text[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
