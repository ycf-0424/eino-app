package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"my-eino-app/internal/eino/rag"
)

// RetrievalABCase describes one retrieval question. MustCite is optional; a
// case without it still contributes latency and candidate-count measurements.
type RetrievalABCase struct {
	Query    string   `json:"query"`
	MustCite []string `json:"must_cite,omitempty"`
}

func LoadRetrievalABCases(path string) ([]RetrievalABCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []RetrievalABCase
	return cases, json.Unmarshal(data, &cases)
}

type RetrievalABReport struct {
	Total               int     `json:"total"`
	VectorHitRate       float64 `json:"vector_hit_rate"`
	HybridHitRate       float64 `json:"hybrid_hit_rate"`
	VectorAvgDurationMS float64 `json:"vector_avg_duration_ms"`
	HybridAvgDurationMS float64 `json:"hybrid_avg_duration_ms"`
	VectorAvgCandidates float64 `json:"vector_avg_candidates"`
	HybridAvgCandidates float64 `json:"hybrid_avg_candidates"`
	Improvement         float64 `json:"hybrid_hit_rate_improvement"`
}

// EvaluateRetrievalAB runs both paths against the same store and query set.
// It does not invoke a language model, so it is safe to run in CI and to use
// before selecting a provider-backed reranker.
func EvaluateRetrievalAB(ctx context.Context, store rag.Store, lexical rag.LexicalSearcher, cases []RetrievalABCase, options rag.SearchOptions) RetrievalABReport {
	report := RetrievalABReport{Total: len(cases)}
	if len(cases) == 0 || store == nil {
		return report
	}
	var vectorMS, hybridMS int64
	var vectorCandidates, hybridCandidates, vectorHits, hybridHits int
	for _, item := range cases {
		vectorOptions := options
		vectorOptions.Hybrid, vectorOptions.Lexical, vectorOptions.Reranker = false, nil, nil
		started := time.Now()
		vectorDocs, vectorErr := rag.Retrieve(ctx, store, item.Query, vectorOptions)
		vectorMS += time.Since(started).Milliseconds()
		if vectorErr == nil {
			vectorCandidates += len(vectorDocs)
			if len(item.MustCite) > 0 && retrievalCites(vectorDocs, item.MustCite) {
				vectorHits++
			}
		}
		hybridOptions := options
		hybridOptions.Hybrid, hybridOptions.Lexical, hybridOptions.Reranker = true, lexical, rag.LexicalReranker{}
		started = time.Now()
		hybridDocs, hybridErr := rag.Retrieve(ctx, store, item.Query, hybridOptions)
		hybridMS += time.Since(started).Milliseconds()
		if hybridErr == nil {
			hybridCandidates += len(hybridDocs)
			if len(item.MustCite) > 0 && retrievalCites(hybridDocs, item.MustCite) {
				hybridHits++
			}
		}
	}
	n := float64(len(cases))
	report.VectorAvgDurationMS = float64(vectorMS) / n
	report.HybridAvgDurationMS = float64(hybridMS) / n
	report.VectorAvgCandidates = float64(vectorCandidates) / n
	report.HybridAvgCandidates = float64(hybridCandidates) / n
	if hasCitations(cases) {
		citationCases := 0
		for _, item := range cases {
			if len(item.MustCite) > 0 {
				citationCases++
			}
		}
		report.VectorHitRate = float64(vectorHits) / float64(citationCases)
		report.HybridHitRate = float64(hybridHits) / float64(citationCases)
		report.Improvement = report.HybridHitRate - report.VectorHitRate
	}
	return report
}

func retrievalCites(docs []*schema.Document, required []string) bool {
	if len(required) == 0 {
		return true
	}
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		source := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(fmt.Sprint(doc.MetaData["source"])), "./"), "/")))
		for _, want := range required {
			if strings.Contains(source, strings.ToLower(strings.TrimSpace(want))) {
				return true
			}
		}
	}
	return false
}

func hasCitations(cases []RetrievalABCase) bool {
	for _, item := range cases {
		if len(item.MustCite) > 0 {
			return true
		}
	}
	return false
}
