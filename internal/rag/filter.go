package rag

import (
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
		if doc == nil || (options.ScoreThreshold > 0 && doc.Score() < options.ScoreThreshold) || !metadataMatches(doc, options.Metadata) {
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
