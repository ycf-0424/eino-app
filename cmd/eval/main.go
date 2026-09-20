// Command eval 对本地 Ollama + Milvus RAG 执行固定问答评测。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"my-eino-app/internal/auth"
	"my-eino-app/internal/config"
	"my-eino-app/internal/eino/chain"
	"my-eino-app/internal/evaluation"
	"my-eino-app/internal/execution"
	"my-eino-app/internal/health"
	"my-eino-app/internal/output"
	appserver "my-eino-app/internal/server"
)

func main() {
	file := flag.String("file", "", "评测集 JSON，默认 internal/integration/eval_cases.json")
	threshold := flag.Float64("threshold", -1, "覆盖相似度阈值；-1 使用 config.yaml")
	minCorrect := flag.Float64("min-correct", 0.8, "最低正确率，未达到时返回非零退出码")
	minCitation := flag.Float64("min-citation", 1, "最低引用率，未达到时返回非零退出码")
	minRefusal := flag.Float64("min-refusal", 0, "最低拒答率；只有评测集声明 should_refuse 时才建议设置为正数")
	minRouting := flag.Float64("min-routing", -1, "最低路由准确率，未达到时返回非零退出码；-1 表示不检查")
	minEvidence := flag.Float64("min-tool-evidence", -1, "最低工具/预加载证据率；-1 表示不检查")
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
	// 向量库不可达时必须立刻失败。检索调用在连接断掉时不会返回错误，而是一直阻塞：
	// 实测 Milvus 停掉后评测进程空转 18 分钟、零输出、CPU 占用接近 0。
	// 一个「看起来在跑」的评测进程比一个直接报错的更费时间。
	if err := probeVectorStore(ctx, cfg.RAG); err != nil {
		fail(err)
	}
	effectiveThreshold := cfg.RAG.ScoreThreshold
	if *threshold >= 0 {
		effectiveThreshold = *threshold
	}
	runner, err := newProductionRouter(ctx, cfg, effectiveThreshold)
	if err != nil {
		fail(err)
	}
	defer runner.Close()
	cases, err := evaluation.LoadCases(*file)
	if err != nil {
		fail(err)
	}
	report := evaluation.Run(ctx, runner, cases, func(query string) chain.Input {
		return chain.Input{Query: query, TopK: cfg.RAG.TopK, ScoreThreshold: effectiveThreshold, MaxContextChars: cfg.RAG.MaxContextChars}
	})
	// 路由维度只在评测集声明了 expect_skills 时执行：它要额外装配一次 Agent，
	// 结果从同一个 production runner 的事件缓存读取，避免答案指标和路由指标
	// 走两套装配逻辑或对同一题重复调用模型。
	if hasRoutingCases(cases) {
		routing, accuracy := evaluation.RunRouting(ctx, runner, cases)
		report.Routing = routing
		report.RoutingTotal = len(routing)
		report.RoutingAccuracy = accuracy
		for _, item := range routing {
			if item.ToolEvidence {
				report.ToolEvidenceTotal++
			}
		}
		if len(routing) > 0 {
			report.ToolEvidenceRate = float64(report.ToolEvidenceTotal) / float64(len(routing))
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fail(err)
	}
	fmt.Println(string(data))
	// 评测命令既输出明细，也可直接作为本地或 CI 回归门禁。
	if report.CorrectRate < *minCorrect || report.CitationRate < *minCitation || report.RefusalRate < *minRefusal {
		fail(fmt.Errorf("evaluation threshold not met: correct %.2f/%.2f, citation %.2f/%.2f, refusal %.2f/%.2f", report.CorrectRate, *minCorrect, report.CitationRate, *minCitation, report.RefusalRate, *minRefusal))
	}
	// 路由准确率默认只输出不门禁：它取决于模型当轮的判断，波动比答案指标大，
	// 需要卡门禁时用 -min-routing 显式开启。
	if *minRouting >= 0 && report.RoutingAccuracy < *minRouting {
		fail(fmt.Errorf("routing accuracy %.2f below %.2f (scored %d cases)", report.RoutingAccuracy, *minRouting, report.RoutingTotal))
	}
	if *minEvidence >= 0 && report.ToolEvidenceRate < *minEvidence {
		fail(fmt.Errorf("tool evidence rate %.2f below %.2f (scored %d cases)", report.ToolEvidenceRate, *minEvidence, report.RoutingTotal))
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }

// probeVectorStore 在开跑前确认向量库端口可达。
//
// 只做 TCP 建连，不校验 collection 是否存在：这里要拦的是「服务没起来」这类
// 会让评测永久挂住的故障，而不是「索引忘了建」——后者会以检索空结果的形式
// 暴露在评测报告里，本来就该被看到。
func probeVectorStore(ctx context.Context, cfg config.RAG) error {
	var address string
	switch cfg.Store {
	case "milvus":
		address = cfg.Milvus.Address
	case "redis":
		address = cfg.Redis.Addr
	default:
		return nil
	}
	if strings.TrimSpace(address) == "" {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := health.DialChecker(address)(probeCtx); err != nil {
		return fmt.Errorf("vector store %s at %s is unreachable; start it before running the evaluation: %w", cfg.Store, address, err)
	}
	return nil
}

// hasRoutingCases 报告评测集里是否有声明了 expect_skills 的题目。
func hasRoutingCases(cases []evaluation.Case) bool {
	for _, item := range cases {
		if len(item.ExpectSkills) > 0 {
			return true
		}
	}
	return false
}

// productionRouter drives the same Service.ChatWithSink path used by HTTP.
// Its cache lets answer and route metrics inspect one execution per question.
type productionRouter struct {
	service  *appserver.Service
	tempDir  string
	owner    string
	mu       sync.Mutex
	answers  map[string]output.Answer
	routing  map[string]evaluation.Routing
	errCache map[string]error
}

func newProductionRouter(ctx context.Context, source *config.Config, scoreThreshold float64) (*productionRouter, error) {
	tempDir, err := os.MkdirTemp("", "my-eino-eval-")
	if err != nil {
		return nil, fmt.Errorf("create eval workspace: %w", err)
	}
	cfg := *source
	cfg.Auth.Enabled = false
	cfg.Memory.Enabled = false
	cfg.Session.Store = "file"
	cfg.SessionDir = filepath.Join(tempDir, "sessions")
	cfg.Agent.CheckpointDir = filepath.Join(tempDir, "checkpoints")
	cfg.ExecutionEvents.Enabled = true
	cfg.ExecutionEvents.Dir = filepath.Join(tempDir, "executions")
	cfg.RAG.ScoreThreshold = scoreThreshold
	service, err := appserver.NewService(ctx, &cfg)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("create production evaluation service: %w", err)
	}
	return &productionRouter{
		service:  service,
		tempDir:  tempDir,
		owner:    "eval:local",
		answers:  make(map[string]output.Answer),
		routing:  make(map[string]evaluation.Routing),
		errCache: make(map[string]error),
	}, nil
}

func (r *productionRouter) Close() error {
	if r == nil {
		return nil
	}
	err := r.service.Close()
	if removeErr := os.RemoveAll(r.tempDir); err == nil {
		err = removeErr
	}
	return err
}

func (r *productionRouter) Run(ctx context.Context, in chain.Input) (output.Answer, error) {
	answer, _, err := r.run(ctx, in.Query)
	return answer, err
}

func (r *productionRouter) Route(ctx context.Context, question string) (evaluation.Routing, error) {
	_, routing, err := r.run(ctx, question)
	return routing, err
}

func (r *productionRouter) run(ctx context.Context, question string) (output.Answer, evaluation.Routing, error) {
	r.mu.Lock()
	if answer, ok := r.answers[question]; ok {
		routing := r.routing[question]
		err := r.errCache[question]
		r.mu.Unlock()
		return answer, routing, err
	}
	r.mu.Unlock()

	recorder := execution.NewRecorder(0)
	var answerText bytes.Buffer
	requestCtx := auth.WithOwner(ctx, r.owner)
	_, err := r.service.ChatWithSink(requestCtx, uuid.NewString(), question, "", &answerText, recorder)
	answer := output.Answer{Answer: strings.TrimSpace(answerText.String()), Sources: sourcesFromEvents(recorder.Events())}
	routing := routingFromEvents(recorder.Events())
	routing.Answer = clip(answer.Answer, 500)
	r.mu.Lock()
	r.answers[question] = answer
	r.routing[question] = routing
	r.errCache[question] = err
	r.mu.Unlock()
	return answer, routing, err
}

func sourcesFromEvents(events []execution.Event) []string {
	return uniqueSorted(stringsOfMany(events, "sources"))
}

func stringsOfMany(events []execution.Event, key string) []string {
	var values []string
	for _, ev := range events {
		if ev.Type != execution.ToolCompleted {
			continue
		}
		values = append(values, stringsOf(ev.Payload[key])...)
	}
	return values
}

// routingFromEvents 从执行事件里还原本轮加载的技能。
// 断言只认 Payload：Summary 是给人看的，可能被裁剪。
func routingFromEvents(events []execution.Event) evaluation.Routing {
	var routing evaluation.Routing
	for _, ev := range events {
		switch ev.Type {
		case execution.SkillLoaded:
			routing.Loaded = append(routing.Loaded, stringsOf(ev.Payload["skill_names"])...)
			routing.Requested = append(routing.Requested, stringsOf(ev.Payload["requested_names"])...)
		case execution.SkillPreloaded:
			routing.Preloaded = append(routing.Preloaded, stringsOf(ev.Payload["skill_names"])...)
		}
		if ev.Type == execution.ToolStarted || ev.Type == execution.ToolCompleted || ev.Type == execution.ToolFailed || ev.Type == execution.SkillPreloaded {
			routing.ToolEvidence = true
		}
	}
	routing.Loaded = uniqueSorted(routing.Loaded)
	routing.Requested = uniqueSorted(routing.Requested)
	routing.Preloaded = uniqueSorted(routing.Preloaded)
	return routing
}

// stringsOf 兼容两种 payload 形态：进程内事件里是 []string，JSON 往返后是 []any。
func stringsOf(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}

// uniqueSorted 去重并排序，让同一道题的输出在多次运行之间可比。
func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// clip 按字符截断，避免报告被单题的长答案撑爆。
func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
