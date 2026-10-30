package rag

import (
	"context"
	"github.com/cloudwego/eino/schema"
	"os"
	"path/filepath"
	"testing"
)

type retrieveTestStore struct {
	docs []*schema.Document
	seen int
}

func (s *retrieveTestStore) Index(context.Context, []*schema.Document) ([]string, error) {
	return nil, nil
}
func (s *retrieveTestStore) Search(_ context.Context, _ string, topK int) ([]*schema.Document, error) {
	s.seen = topK
	return append([]*schema.Document(nil), s.docs...), nil
}

func TestRetrieveHybridExpandsAndReranksCandidates(t *testing.T) {
	store := &retrieveTestStore{docs: []*schema.Document{
		newScoredDoc("semantic", "项目架构说明", 0.95, "md"),
		newScoredDoc("exact", "qwen3.5:9b active_model config.yaml", 0.70, "md"),
	}}
	got, err := Retrieve(context.Background(), store, "qwen3.5:9b config.yaml", SearchOptions{TopK: 1, Hybrid: true, Reranker: LexicalReranker{}})
	if err != nil {
		t.Fatal(err)
	}
	if store.seen <= 1 || len(got) != 1 || got[0].ID != "exact" {
		t.Fatalf("hybrid retrieval got=%+v fetched=%d", got, store.seen)
	}
}

type lexicalTestIndex struct{ docs []*schema.Document }

func (l lexicalTestIndex) SearchLexical(context.Context, string, int) ([]*schema.Document, error) {
	return l.docs, nil
}

func TestHybridExactLexicalHitSurvivesVectorThreshold(t *testing.T) {
	vector := newScoredDoc("exact", "星河系统使用 Go、Eino 和 Milvus", 0.20, "md")
	lexical := newScoredDoc("exact", vector.Content, 0.95, "md")
	got, err := Retrieve(context.Background(), &retrieveTestStore{docs: []*schema.Document{vector}}, "星河系统 技术栈", SearchOptions{
		TopK: 1, ScoreThreshold: 0.50, Hybrid: true, Lexical: lexicalTestIndex{docs: []*schema.Document{lexical}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "exact" {
		t.Fatalf("exact lexical hit was filtered: %+v", got)
	}
}

func TestBM25IndexSearchesIndependentLexicalCandidates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "guide.md"), []byte("# Milvus 配置\n\nqwen3.5:9b 使用 Milvus 向量库。"), 0o600); err != nil {
		t.Fatal(err)
	}
	index := NewBM25Index()
	if err := index.Refresh(dir, 200, 0); err != nil {
		t.Fatal(err)
	}
	docs, err := index.SearchLexical(context.Background(), "Milvus", 3)
	if err != nil || len(docs) == 0 || docs[0].Score() <= 0 {
		t.Fatalf("lexical docs=%+v err=%v", docs, err)
	}
}

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
