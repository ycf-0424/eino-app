package rag

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"

	"github.com/cloudwego/eino/schema"
)

// SearchOptions 统一控制 Agent Tool 和 RAG Chain 的检索质量。
type SearchOptions struct {
	TopK            int
	ScoreThreshold  float64
	MaxContextChars int
	// Metadata 只保留元数据完全匹配的文档，例如 {"format":"pdf"}。
	Metadata map[string]string
	// Hybrid enables a deterministic lexical/semantic score blend. It is
	// deliberately opt-in so existing deployments keep their vector-only
	// ranking until the new path is evaluated.
	Hybrid bool
	// Reranker performs a second-pass ordering over the expanded candidate set.
	Reranker Reranker
	// Lexical is an optional independent lexical index used by Hybrid mode.
	Lexical LexicalSearcher
}

// Reranker reorders an already-retrieved candidate set. Implementations must
// preserve document contents and metadata; only ordering and scores may change.
type Reranker interface {
	Rerank(context.Context, string, []*schema.Document) ([]*schema.Document, error)
}

// LexicalReranker is a small dependency-free baseline. It is useful for local
// deployments and tests; a provider-backed reranker can implement the same
// interface later without changing callers.
type LexicalReranker struct{}

func (LexicalReranker) Rerank(_ context.Context, query string, docs []*schema.Document) ([]*schema.Document, error) {
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		lexical := lexicalScore(query, doc.Content)
		// Preserve semantic relevance while rewarding exact identifiers and
		// terms. Scores remain in a comparable 0..1 range.
		doc.WithScore(0.65*doc.Score() + 0.35*lexical)
	}
	sort.SliceStable(docs, func(i, j int) bool { return docs[i] != nil && docs[j] != nil && docs[i].Score() > docs[j].Score() })
	return docs, nil
}

// Retrieve is the shared retrieval path for Agent tools and RAG chains.
// Hybrid mode fetches a wider candidate set, blends lexical overlap with the
// vector score, then optionally applies a second-pass reranker.
func Retrieve(ctx context.Context, store Store, query string, options SearchOptions) ([]*schema.Document, error) {
	topK := options.TopK
	if topK <= 0 {
		topK = 5
	}
	fetchK := topK
	if options.Hybrid || options.Reranker != nil {
		fetchK = topK * 3
		if fetchK < topK {
			fetchK = topK
		}
	}
	docs, err := store.Search(ctx, query, fetchK)
	if err != nil {
		return nil, err
	}
	if options.Hybrid {
		docs, err = fuseLexical(ctx, query, docs, options.Lexical, fetchK)
		if err != nil {
			return nil, err
		}
	}
	if options.Reranker != nil {
		docs, err = options.Reranker.Rerank(ctx, query, docs)
		if err != nil {
			return nil, err
		}
	}
	return FilterDocuments(docs, options), nil
}

func fuseLexical(ctx context.Context, query string, vectorDocs []*schema.Document, lexical LexicalSearcher, topK int) ([]*schema.Document, error) {
	if lexical == nil {
		// Without an independent index, retain the compatibility candidate blend.
		for _, doc := range vectorDocs {
			if doc != nil {
				doc.WithScore(0.7*doc.Score() + 0.3*lexicalScore(query, doc.Content))
			}
		}
		return vectorDocs, nil
	}
	lexicalDocs, err := lexical.SearchLexical(ctx, query, topK)
	if err != nil {
		return nil, err
	}
	type ranked struct {
		doc             *schema.Document
		vector, lexical float64
	}
	byID := map[string]*ranked{}
	for rank, doc := range vectorDocs {
		if doc == nil {
			continue
		}
		key := doc.ID
		if key == "" {
			key = fmt.Sprintf("vector-%d", rank)
		}
		byID[key] = &ranked{doc: doc, vector: doc.Score()}
	}
	for rank, doc := range lexicalDocs {
		if doc == nil {
			continue
		}
		key := doc.ID
		if key == "" {
			key = fmt.Sprintf("lexical-%d", rank)
		}
		item := byID[key]
		if item == nil {
			item = &ranked{doc: doc}
			byID[key] = item
		}
		item.lexical = doc.Score()
	}
	out := make([]*schema.Document, 0, len(byID))
	for _, item := range byID {
		score := item.lexical
		if item.vector > 0 && item.lexical > 0 {
			score = 0.65*item.vector + 0.35*item.lexical
		} else if item.vector > 0 {
			score = item.vector
		}
		item.doc.WithScore(score)
		out = append(out, item.doc)
	}
	return out, nil
}

// FilterDocuments 按分数排序、元数据过滤、内容去重并限制上下文长度。
// 将这段逻辑放在 rag 包中，确保 Tool 模式和 Chain 模式行为一致。
func FilterDocuments(docs []*schema.Document, options SearchOptions) []*schema.Document {
	sorted := append([]*schema.Document(nil), docs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score() > sorted[j].Score() })
	seen := make(map[string]struct{}, len(sorted))
	result := make([]*schema.Document, 0, len(sorted))
	totalChars := 0
	for _, doc := range sorted {
		lexicalCandidate := doc != nil && fmt.Sprint(doc.MetaData["retrieval_mode"]) == "lexical"
		if doc == nil || (options.ScoreThreshold > 0 && doc.Score() < options.ScoreThreshold && !lexicalCandidate) || !metadataMatches(doc, options.Metadata) {
			continue
		}
		hash := sha256.Sum256([]byte(doc.Content))
		key := fmt.Sprintf("%x", hash)
		if _, exists := seen[key]; exists {
			continue
		}
		contentChars := len([]rune(doc.Content))
		if options.MaxContextChars > 0 && totalChars+contentChars > options.MaxContextChars {
			break
		}
		seen[key] = struct{}{}
		totalChars += contentChars
		result = append(result, doc)
		if options.TopK > 0 && len(result) >= options.TopK {
			break
		}
	}
	return result
}

func metadataMatches(doc *schema.Document, filters map[string]string) bool {
	for key, want := range filters {
		if fmt.Sprint(doc.MetaData[key]) != want {
			return false
		}
	}
	return true
}
