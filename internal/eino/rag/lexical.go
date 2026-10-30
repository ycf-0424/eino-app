package rag

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/cloudwego/eino/schema"
)

// LexicalSearcher retrieves candidates independently from the vector store.
type LexicalSearcher interface {
	SearchLexical(context.Context, string, int) ([]*schema.Document, error)
}

// BM25Index is a bounded in-memory lexical index built from the same parsed
// chunks as the vector index. Refresh it after knowledge ingestion changes.
type BM25Index struct {
	mu      sync.RWMutex
	docs    []*schema.Document
	df      map[string]int
	terms   []map[string]int
	lengths []int
	avgLen  float64
}

func NewBM25Index() *BM25Index { return &BM25Index{df: map[string]int{}} }

// Ready reports whether the in-memory lexical index has been built at least
// once. It is used by the server to avoid silently running hybrid retrieval
// with an empty BM25 side when a deployment starts with a stale/missing path.
func (b *BM25Index) Ready() bool {
	if b == nil {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.docs) > 0
}

func (b *BM25Index) Refresh(dir string, chunkSize, overlap int) error {
	docs, err := LoadDocuments(dir, chunkSize, overlap)
	if err != nil {
		return err
	}
	termCounts := make([]map[string]int, len(docs))
	lengths := make([]int, len(docs))
	df := make(map[string]int)
	totalLength := 0
	for i, doc := range docs {
		counts := map[string]int{}
		for _, token := range lexicalTokens(doc.Content) {
			counts[token]++
		}
		termCounts[i], lengths[i] = counts, sumTermCounts(counts)
		totalLength += lengths[i]
		for token := range counts {
			df[token]++
		}
	}
	avg := 0.0
	if len(docs) > 0 {
		avg = float64(totalLength) / float64(len(docs))
	}
	b.mu.Lock()
	b.docs, b.df, b.terms, b.lengths, b.avgLen = docs, df, termCounts, lengths, avg
	b.mu.Unlock()
	return nil
}

func (b *BM25Index) SearchLexical(ctx context.Context, query string, topK int) ([]*schema.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if topK <= 0 || len(b.docs) == 0 {
		return nil, nil
	}
	queryTokens := lexicalTokens(query)
	if len(queryTokens) == 0 {
		return nil, nil
	}
	type scored struct {
		index int
		score float64
	}
	results := make([]scored, 0)
	const k1, bParam = 1.5, 0.75
	n := float64(len(b.docs))
	for i, terms := range b.terms {
		if i%128 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		score := 0.0
		for _, token := range queryTokens {
			tf := float64(terms[token])
			if tf == 0 {
				continue
			}
			df := float64(b.df[token])
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			norm := tf + k1*(1-bParam+bParam*float64(b.lengths[i])/maxFloat(b.avgLen, 1))
			score += idf * tf * (k1 + 1) / norm
		}
		if score > 0 {
			results = append(results, scored{i, score})
		}
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].score > results[j].score })
	if len(results) > topK {
		results = results[:topK]
	}
	out := make([]*schema.Document, 0, len(results))
	for _, result := range results {
		copyDoc := *b.docs[result.index]
		copyDoc.MetaData = cloneMetadata(b.docs[result.index].MetaData)
		if copyDoc.MetaData == nil {
			copyDoc.MetaData = map[string]any{}
		}
		copyDoc.MetaData["retrieval_mode"] = "lexical"
		// Map positive BM25 scores to a bounded confidence-like range for the
		// common ranking/filter interface. Original vector scores are added
		// separately by Retrieve using reciprocal-rank fusion.
		copyDoc.WithScore(result.score / (result.score + 1))
		out = append(out, &copyDoc)
	}
	return out, nil
}

func lexicalTokens(value string) []string {
	value = strings.ToLower(value)
	runes := []rune(value)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(runes))
	add := func(token string) {
		if len([]rune(token)) < 2 {
			return
		}
		if _, ok := seen[token]; ok {
			return
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	for i := 0; i < len(runes); {
		if unicode.Is(unicode.Han, runes[i]) {
			start := i
			for i < len(runes) && unicode.Is(unicode.Han, runes[i]) {
				i++
			}
			for j := start; j+1 < i; j++ {
				add(string(runes[j : j+2]))
			}
			continue
		}
		if unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_' || runes[i] == '-' || runes[i] == '.' {
			start := i
			for i < len(runes) && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_' || runes[i] == '-' || runes[i] == '.') {
				i++
			}
			add(string(runes[start:i]))
			continue
		}
		i++
	}
	return out
}

func lexicalScore(query, content string) float64 {
	q := lexicalTokens(query)
	if len(q) == 0 {
		return 0
	}
	candidate := strings.ToLower(content)
	hit := 0
	for _, token := range q {
		if strings.Contains(candidate, token) {
			hit++
		}
	}
	return float64(hit) / float64(len(q))
}

func sumTermCounts(counts map[string]int) int {
	n := 0
	for _, count := range counts {
		n += count
	}
	return n
}
func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
func cloneMetadata(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
