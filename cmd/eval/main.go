// Command eval 对本地 Ollama + Milvus RAG 执行固定问答评测。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"my-eino-app/internal/config"
	"my-eino-app/internal/eino/chain"
	"my-eino-app/internal/eino/model"
	"my-eino-app/internal/eino/rag"
	"my-eino-app/internal/evaluation"
	"my-eino-app/internal/health"
)

func main() {
	file := flag.String("file", "", "评测集 JSON，默认 internal/integration/eval_cases.json")
	threshold := flag.Float64("threshold", -1, "覆盖相似度阈值；-1 使用 config.yaml")
	minCorrect := flag.Float64("min-correct", 0.8, "最低正确率，未达到时返回非零退出码")
	minCitation := flag.Float64("min-citation", 1, "最低引用率，未达到时返回非零退出码")
	minRefusal := flag.Float64("min-refusal", 1, "最低拒答率，未达到时返回非零退出码")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	if *file == "" {
		*file = filepath.Join(cfg.ProjectDir, "internal", "integration", "eval_cases.json")
	}
	if err := health.CheckOllama(ctx, cfg.OpenAI.BaseURL, cfg.OpenAI.Model, cfg.RAG.Embedding.Model, cfg.RAG.Dimension, time.Duration(cfg.Ollama.Timeout)); err != nil {
		fail(err)
	}
	store, err := rag.NewFromConfig(ctx, cfg.RAG)
	if err != nil {
		fail(err)
	}
	cm, err := model.NewChatModel(ctx, cfg)
	if err != nil {
		fail(err)
	}
	runner, err := chain.New(ctx, store, cm)
	if err != nil {
		fail(err)
	}
	cases, err := evaluation.LoadCases(*file)
	if err != nil {
		fail(err)
	}
	effectiveThreshold := cfg.RAG.ScoreThreshold
	if *threshold >= 0 {
		effectiveThreshold = *threshold
	}
	report := evaluation.Run(ctx, runner, cases, func(query string) chain.Input {
		return chain.Input{Query: query, TopK: cfg.RAG.TopK, ScoreThreshold: effectiveThreshold, MaxContextChars: cfg.RAG.MaxContextChars}
	})
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fail(err)
	}
	fmt.Println(string(data))
	// 评测命令既输出明细，也可直接作为本地或 CI 回归门禁。
	if report.CorrectRate < *minCorrect || report.CitationRate < *minCitation || report.RefusalRate < *minRefusal {
		fail(fmt.Errorf("evaluation threshold not met: correct %.2f/%.2f, citation %.2f/%.2f, refusal %.2f/%.2f", report.CorrectRate, *minCorrect, report.CitationRate, *minCitation, report.RefusalRate, *minRefusal))
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
