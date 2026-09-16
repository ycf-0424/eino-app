package output

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestParseModelAnswerJSON(t *testing.T) {
	got := ParseModelAnswer(&schema.Message{Content: "```json\n{\"answer\":\"7319\",\"confidence\":0.9}\n```"}, []string{"doc.md", "doc.md"}, 0.8)
	if got.Answer != "7319" || got.Confidence != 0.9 || len(got.Sources) != 1 {
		t.Fatalf("unexpected answer: %+v", got)
	}
}

func TestParseModelAnswerPlainTextFallback(t *testing.T) {
	got := ParseModelAnswer(&schema.Message{Content: "无法确定"}, nil, 0)
	if got.Answer != "无法确定" || got.Error == "" {
		t.Fatalf("expected plain-text fallback: %+v", got)
	}
}
