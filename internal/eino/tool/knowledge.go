package tool

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"

	"my-eino-app/internal/eino/rag"
	"my-eino-app/internal/execution"
	"my-eino-app/internal/output"
)

// KnowledgeInput 类型。
type KnowledgeInput struct {
	// Query 是用户问题或模型整理出的语义检索词。
	Query string `json:"query"`
}

// NewKnowledgeTool 把向量检索包装成 Agent 工具。
func NewKnowledgeTool(store rag.Store, topK int) einotool.InvokableTool {
	return NewKnowledgeToolWithOptions(store, rag.SearchOptions{TopK: topK})
}

// NewKnowledgeToolWithOptions 创建带阈值、去重和上下文限制的知识库工具。
func NewKnowledgeToolWithOptions(store rag.Store, options rag.SearchOptions) einotool.InvokableTool {
	info := &schema.ToolInfo{Name: "knowledge_search", Desc: "Search the private knowledge base. Use it for questions about project documents.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Desc: "Semantic search query", Type: schema.String, Required: true},
		})}
	return utils.NewTool(info, func(ctx context.Context, input KnowledgeInput) (string, error) {
		docs, err := rag.Retrieve(ctx, store, input.Query, options)
		if err != nil {
			return "", err
		}
		if len(docs) == 0 {
			// 零命中也是可断言的事实：明确标注 hit_count=0。
			execution.Annotate(ctx, map[string]any{"hit_count": 0})
			// 明确告知模型没有检索结果，避免模型把空结果误认为有依据。
			return "No relevant knowledge was found.", nil
		}
		conflicts := output.DetectEvidenceConflicts(func() string {
			var text strings.Builder
			for _, doc := range docs {
				fmt.Fprintf(&text, "source=%v\n%s\n", doc.MetaData["source"], doc.Content)
			}
			return text.String()
		}())
		topScore := docs[0].Score()
		sources := make([]string, 0, len(docs))
		for _, doc := range docs {
			if source := filepath.Base(fmt.Sprint(doc.MetaData["source"])); source != "." && source != "" {
				sources = append(sources, source)
			}
		}
		execution.Annotate(ctx, map[string]any{
			"hit_count": len(docs),
			"top_score": topScore,
			"sources":   sources,
		})
		if len(conflicts) > 0 {
			execution.Annotate(ctx, map[string]any{"conflict_count": len(conflicts), "conflicts": conflicts})
		}
		var out strings.Builder
		for i, doc := range docs {
			fmt.Fprintf(&out, "[%d] source=%v", i+1, doc.MetaData["source"])
			if title := fmt.Sprint(doc.MetaData["title"]); title != "<nil>" && title != "" {
				fmt.Fprintf(&out, " title=%s", title)
			}
			if page := fmt.Sprint(doc.MetaData["page"]); page != "<nil>" && page != "" {
				fmt.Fprintf(&out, " page=%s", page)
			}
			if blockType := fmt.Sprint(doc.MetaData["block_type"]); blockType != "<nil>" && blockType != "" {
				fmt.Fprintf(&out, " block_type=%s", blockType)
			}
			fmt.Fprintf(&out, " score=%.4f\n%s\n\n", doc.Score(), doc.Content)
		}
		return out.String(), nil
	})
}
