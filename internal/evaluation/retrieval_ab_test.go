package evaluation

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
	"my-eino-app/internal/eino/rag"
)

type abStore struct{ docs []*schema.Document }

func (s abStore) Index(context.Context, []*schema.Document) ([]string, error)     { return nil, nil }
func (s abStore) Search(context.Context, string, int) ([]*schema.Document, error) { return s.docs, nil }

func TestEvaluateRetrievalAB(t *testing.T) {
	doc := &schema.Document{ID: "1", Content: "配置 active_model=qwen3.5", MetaData: map[string]any{"source": "config.yaml"}}
	doc.WithScore(0.8)
	report := EvaluateRetrievalAB(context.Background(), abStore{docs: []*schema.Document{doc}}, nil, []RetrievalABCase{{Query: "active model", MustCite: []string{"config.yaml"}}}, rag.SearchOptions{TopK: 1})
	if report.Total != 1 || report.VectorHitRate != 1 || report.HybridHitRate != 1 {
		t.Fatalf("unexpected AB report: %+v", report)
	}
}
