// Package chain 提供固定流程的 RAG Chain，和 Agent 自主调用工具模式并存。
package chain

import (
	"context"
	"fmt"
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"my-eino-app/internal/eino/prompt"
	"my-eino-app/internal/eino/rag"
	"my-eino-app/internal/output"
)

// Input 是固定 RAG 流程的输入。
type Input struct {
	Query           string
	TopK            int
	ScoreThreshold  float64
	MaxContextChars int
}

// Context 是检索后传给 ChatTemplate 的中间数据。
type Context struct {
	Query   string
	Content string
	Sources string
	Docs    []*schema.Document
}

// MemoryFallback 查询项目范围长期记忆。hit=false 时表示第二级也没有
// 可用事实，RAGChain 会继续让通用模型回答，而不是固定拒答。
type MemoryFallback func(context.Context, string) (contextText string, hit bool, err error)

// RAGChain 承载“检索 → Prompt 渲染 → Ollama 生成”的可复用 Chain。
type RAGChain struct {
	store          rag.Store
	model          einomodel.ToolCallingChatModel
	run            compose.Runnable[Context, *schema.Message]
	fallbackRun    compose.Runnable[Context, *schema.Message]
	memoryFallback MemoryFallback
}

// New 创建 RAG Chain。Chain 内部使用 Eino compose 节点，而不是在控制台手写步骤。
func New(ctx context.Context, store rag.Store, model einomodel.ToolCallingChatModel) (*RAGChain, error) {
	return NewWithMemoryFallback(ctx, store, model, nil)
}

// NewWithMemoryFallback 创建带长期记忆二级检索的固定 RAG Chain。
func NewWithMemoryFallback(ctx context.Context, store rag.Store, model einomodel.ToolCallingChatModel, fallback MemoryFallback) (*RAGChain, error) {
	if store == nil || model == nil {
		return nil, fmt.Errorf("rag chain requires store and model")
	}
	c := compose.NewChain[Context, *schema.Message]()
	c.AppendLambda(compose.InvokableLambda(func(_ context.Context, in Context) (map[string]any, error) {
		return map[string]any{"context": in.Content, "sources": in.Sources, "query": in.Query}, nil
	}))
	c.AppendChatTemplate(prompt.NewRAGTemplate())
	c.AppendChatModel(model)
	run, err := c.Compile(ctx)
	if err != nil {
		return nil, fmt.Errorf("compile rag chain: %w", err)
	}
	fallbackChain := compose.NewChain[Context, *schema.Message]()
	fallbackChain.AppendLambda(compose.InvokableLambda(func(_ context.Context, in Context) (map[string]any, error) {
		return map[string]any{"context": in.Content, "sources": in.Sources, "query": in.Query}, nil
	}))
	fallbackChain.AppendChatTemplate(prompt.NewFallbackTemplate())
	fallbackChain.AppendChatModel(model)
	fallbackRun, err := fallbackChain.Compile(ctx)
	if err != nil {
		return nil, fmt.Errorf("compile rag fallback chain: %w", err)
	}
	return &RAGChain{store: store, model: model, run: run, fallbackRun: fallbackRun, memoryFallback: fallback}, nil
}

// SetMemoryFallback 在构造会话存储后注入长期记忆查询，供控制台入口使用。
// 应在并发调用 Run 之前设置。
func (c *RAGChain) SetMemoryFallback(fallback MemoryFallback) {
	if c != nil {
		c.memoryFallback = fallback
	}
}

// Run 执行固定 RAG 流程，并将检索来源包装成结构化 Answer。
func (c *RAGChain) Run(ctx context.Context, in Input) (output.Answer, error) {
	// 先改写查询，再把改写后的文本交给向量库；原问题仍用于最终回答。
	rewrittenQuery := RewriteQuery(in.Query)
	docs, err := c.store.Search(ctx, rewrittenQuery, in.TopK)
	if err != nil {
		return output.Answer{}, fmt.Errorf("retrieve: %w", err)
	}
	docs = rag.FilterDocuments(docs, rag.SearchOptions{TopK: in.TopK, ScoreThreshold: in.ScoreThreshold, MaxContextChars: in.MaxContextChars})
	var content strings.Builder
	sources := make([]string, 0, len(docs))
	maxScore := 0.0
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		part := fmt.Sprintf("[%s]\n%s\n\n", doc.ID, doc.Content)
		content.WriteString(part)
		if source, ok := doc.MetaData["source"].(string); ok {
			sources = append(sources, source)
		}
		if doc.Score() > maxScore {
			maxScore = doc.Score()
		}
	}
	if content.Len() == 0 {
		memoryText := ""
		memoryHit := false
		if c.memoryFallback != nil {
			memoryText, memoryHit, err = c.memoryFallback(ctx, in.Query)
			if err != nil {
				return output.Answer{}, fmt.Errorf("retrieve project memory: %w", err)
			}
		}
		memorySources := sources
		if memoryHit {
			memorySources = []string{"project_long_term_memory"}
		}
		message, invokeErr := c.fallbackRun.Invoke(ctx, Context{
			Query: in.Query, Content: memoryText, Sources: prompt.FormatSources(memorySources),
		})
		if invokeErr != nil {
			return output.Answer{}, fmt.Errorf("generate fallback answer: %w", invokeErr)
		}
		return output.ParseModelAnswer(message, memorySources, 0), nil
	}
	message, err := c.run.Invoke(ctx, Context{Query: in.Query, Content: content.String(), Sources: prompt.FormatSources(sources), Docs: docs})
	if err != nil {
		return output.Answer{}, fmt.Errorf("generate rag answer: %w", err)
	}
	return output.ParseModelAnswer(message, sources, maxScore), nil
}
