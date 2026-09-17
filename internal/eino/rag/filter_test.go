package rag

import (
	"github.com/cloudwego/eino/schema"
	"testing"
)

func TestFilterDocuments(t *testing.T) {
	docs := []*schema.Document{
		newScoredDoc("low", "低分", 0.2, "md"), newScoredDoc("high", "有效资料", 0.9, "md"),
		newScoredDoc("duplicate", "有效资料", 0.8, "md"), newScoredDoc("pdf", "PDF资料", 0.95, "pdf"),
	}
	got := FilterDocuments(docs, SearchOptions{TopK: 2, ScoreThreshold: 0.5, Metadata: map[string]string{"format": "md"}})
	if len(got) != 1 || got[0].ID != "high" {
		t.Fatalf("unexpected filtered docs: %+v", got)
	}
}
func newScoredDoc(id, content string, score float64, format string) *schema.Document {
	d := &schema.Document{ID: id, Content: content, MetaData: map[string]any{"format": format}}
	d.WithScore(score)
	return d
}
