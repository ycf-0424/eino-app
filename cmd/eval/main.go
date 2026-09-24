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
	"strconv"
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
	routingrules "my-eino-app/internal/routing"
	appserver "my-eino-app/internal/server"
)

func main() {
	file := flag.String("file", "", "评测集 JSON，默认 internal/integration/eval_cases.json")
	intentFile := flag.String("intent-file", "", "意图来源固定评测集，默认 internal/integration/intent_eval_cases.json")
	threshold := flag.Float64("threshold", -1, "覆盖相似度阈值；-1 使用 config.yaml")
	minCorrect := flag.Float64("min-correct", 0.8, "最低正确率，未达到时返回非零退出码")
	minCitation := flag.Float64("min-citation", 1, "最低引用率，未达到时返回非零退出码")
	minRefusal := flag.Float64("min-refusal", 0, "最低拒答率；只有评测集声明 should_refuse 时才建议设置为正数")
	minRouting := flag.Float64("min-routing", -1, "最低路由准确率，未达到时返回非零退出码；-1 表示不检查")
	minServerRoute := flag.Float64("min-server-route", -1, "最低关键服务端技能路由召回率；-1 表示不检查")
	maxNoSkillFalsePositive := flag.Float64("max-no-skill-false-positive", -1, "无技能反例允许的最高误预加载率；-1 表示不检查")
	minToolAttempt := flag.Float64("min-tool-attempt", -1, "最低期望工具调用开始证据率；-1 表示不检查")
	minToolSuccess := flag.Float64("min-tool-success", -1, "最低期望工具成功终态证据率；-1 表示不检查")
	minToolSource := flag.Float64("min-tool-source", -1, "最低期望工具来源字段匹配率；-1 表示不检查")
	minIntentAccuracy := flag.Float64("min-intent-accuracy", 0.95, "五类意图准确率最低门槛，至少 50 个固定样本")
	minIntentSource := flag.Float64("min-intent-source-accuracy", 0.95, "意图证据来源准确率最低门槛，含普通问题不误触发检索的断言")
	maxOrdinaryFalsePositive := flag.Float64("max-ordinary-false-positive", 0.01, "普通问题误触发非通用证据来源的最高比例")
	intentOnly := flag.Bool("intent-only", false, "只运行离线五类意图/普通问题误触发门禁，不启动模型或向量库")
	maxForbiddenToolViolations := flag.Int("max-forbidden-tool-violations", -1, "评测集明确禁止的工具调用允许的最多违规用例数；-1 表示不检查")
	debugOverride := flag.String("debug", "", "覆盖 config.yaml 的 debug（true/false）；留空时使用配置文件")
	modelOverride := flag.String("model", "", "评测使用的模型 profile ID；默认固定 config.yaml active_model，使用生产自动路由请显式传 auto")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	cfg.Debug, err = applyDebugOverride(cfg.Debug, *debugOverride)
	if err != nil {
		fail(err)
	}
	evaluationModelID := strings.TrimSpace(*modelOverride)
	if evaluationModelID == "" {
		evaluationModelID = cfg.ActiveModel
	}
	if *file == "" {
		*file = filepath.Join(cfg.ProjectDir, "internal", "integration", "eval_cases.json")
	}
	if *intentFile == "" {
		*intentFile = filepath.Join(cfg.ProjectDir, "internal", "integration", "intent_eval_cases.json")
	}
	if *intentOnly {
		intentCases, err := evaluation.LoadIntentCases(*intentFile)
		if err != nil {
			fail(err)
		}
		intentReport := evaluation.EvaluateIntentCases(intentCases)
		thresholdErr := checkIntentThresholds(intentReport, *minIntentAccuracy, *minIntentSource, *maxOrdinaryFalsePositive)
		if thresholdErr == nil {
			intentReport.Cases = nil
		}
		data, err := json.MarshalIndent(intentReport, "", "  ")
		if err != nil {
			fail(err)
		}
		fmt.Println(string(data))
		if thresholdErr != nil {
			fail(thresholdErr)
		}
		return
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
	runner, err := newProductionRouter(ctx, cfg, effectiveThreshold, evaluationModelID)
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
	intentCases, err := evaluation.LoadIntentCases(*intentFile)
	if err != nil {
		fail(err)
	}
	report.Intent = evaluation.EvaluateIntentCases(intentCases)
	// 两个视角读取同一次请求的事件缓存，不重复调用模型；技能与工具分别计数。
	routes, _ := evaluation.RunRouting(ctx, runner, cases)
	evaluation.SummarizeRouting(&report, cases, routes)
	debugSetting := cfg.Debug
	report.ModelID = evaluationModelID
	report.Model = modelName(cfg, evaluationModelID)
	report.Debug = &debugSetting
	report.ScoreThreshold = effectiveThreshold
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fail(err)
	}
	fmt.Println(string(data))
	// 评测命令既输出明细，也可直接作为本地或 CI 回归门禁。
	if err := checkEvaluationThresholds(report, evaluationThresholds{
		minCorrect: *minCorrect, minCitation: *minCitation, minRefusal: *minRefusal,
		minRouting: *minRouting, minServerRoute: *minServerRoute,
		maxNoSkillFalsePositive:    *maxNoSkillFalsePositive,
		maxForbiddenToolViolations: *maxForbiddenToolViolations,
		minToolAttempt:             *minToolAttempt, minToolSuccess: *minToolSuccess, minToolSource: *minToolSource,
		minIntentAccuracy: *minIntentAccuracy, minIntentSourceAccuracy: *minIntentSource,
		maxOrdinaryFalsePositive: *maxOrdinaryFalsePositive,
	}); err != nil {
		fail(err)
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

type evaluationThresholds struct {
	minCorrect, minCitation, minRefusal, minRouting, minServerRoute        float64
	maxNoSkillFalsePositive, minToolAttempt, minToolSuccess, minToolSource float64
	minIntentAccuracy, minIntentSourceAccuracy                             float64
	maxOrdinaryFalsePositive                                               float64
	maxForbiddenToolViolations                                             int
}

func checkEvaluationThresholds(report evaluation.Report, limits evaluationThresholds) error {
	if err := checkIntentThresholds(report.Intent, limits.minIntentAccuracy, limits.minIntentSourceAccuracy, limits.maxOrdinaryFalsePositive); err != nil {
		return err
	}
	if report.CorrectRate < limits.minCorrect || report.CitationRate < limits.minCitation || report.RefusalRate < limits.minRefusal {
		return fmt.Errorf("evaluation threshold not met: correct %.2f/%.2f, citation %.2f/%.2f, refusal %.2f/%.2f", report.CorrectRate, limits.minCorrect, report.CitationRate, limits.minCitation, report.RefusalRate, limits.minRefusal)
	}
	if limits.minRouting >= 0 && report.RoutingAccuracy < limits.minRouting {
		return fmt.Errorf("routing accuracy %.2f below %.2f (scored %d cases)", report.RoutingAccuracy, limits.minRouting, report.RoutingTotal)
	}
	if limits.minServerRoute >= 0 && report.ServerRouteRecall < limits.minServerRoute {
		return fmt.Errorf("server route recall %.2f below %.2f (scored %d cases)", report.ServerRouteRecall, limits.minServerRoute, report.ServerRouteTotal)
	}
	if limits.maxNoSkillFalsePositive >= 0 && (report.NoSkillTotal == 0 || report.NoSkillFalsePositiveRate > limits.maxNoSkillFalsePositive) {
		return fmt.Errorf("no-skill false-positive rate %.2f above %.2f (scored %d cases)", report.NoSkillFalsePositiveRate, limits.maxNoSkillFalsePositive, report.NoSkillTotal)
	}
	if limits.maxForbiddenToolViolations >= 0 && (report.ForbiddenToolCaseTotal == 0 || report.ForbiddenToolViolations > limits.maxForbiddenToolViolations) {
		return fmt.Errorf("forbidden-tool violations %d exceed %d (scored %d cases)", report.ForbiddenToolViolations, limits.maxForbiddenToolViolations, report.ForbiddenToolCaseTotal)
	}
	if limits.minToolAttempt >= 0 && (report.ToolAttemptTotal == 0 || report.ToolAttemptRate < limits.minToolAttempt) {
		return fmt.Errorf("tool attempt rate %.2f below %.2f (scored %d cases)", report.ToolAttemptRate, limits.minToolAttempt, report.ToolAttemptTotal)
	}
	if limits.minToolSuccess >= 0 && (report.ToolSuccessTotal == 0 || report.ToolSuccessRate < limits.minToolSuccess) {
		return fmt.Errorf("tool success rate %.2f below %.2f (scored %d cases)", report.ToolSuccessRate, limits.minToolSuccess, report.ToolSuccessTotal)
	}
	if limits.minToolSource >= 0 && (report.ToolSourceTotal == 0 || report.ToolSourceRate < limits.minToolSource) {
		return fmt.Errorf("tool source rate %.2f below %.2f (scored %d sources)", report.ToolSourceRate, limits.minToolSource, report.ToolSourceTotal)
	}
	return nil
}

func checkIntentThresholds(report evaluation.IntentReport, minAccuracy, minSourceAccuracy, maxOrdinaryFalsePositive float64) error {
	if report.Total < 50 {
		return fmt.Errorf("intent evaluation requires at least 50 fixed samples; got %d", report.Total)
	}
	if report.Accuracy < minAccuracy || report.SourceAccuracy < minSourceAccuracy {
		return fmt.Errorf("intent evaluation threshold not met: accuracy %.2f/%0.2f, source accuracy %.2f/%0.2f (%d samples)", report.Accuracy, minAccuracy, report.SourceAccuracy, minSourceAccuracy, report.Total)
	}
	if maxOrdinaryFalsePositive >= 0 && (report.OrdinaryTotal == 0 || report.OrdinaryFalsePositiveRate > maxOrdinaryFalsePositive) {
		return fmt.Errorf("ordinary-request false-positive rate %.2f above %.2f (%d ordinary samples)", report.OrdinaryFalsePositiveRate, maxOrdinaryFalsePositive, report.OrdinaryTotal)
	}
	return nil
}

func applyDebugOverride(configDebug bool, override string) (bool, error) {
	if strings.TrimSpace(override) == "" {
		return configDebug, nil
	}
	value, err := strconv.ParseBool(strings.TrimSpace(override))
	if err != nil {
		return configDebug, fmt.Errorf("invalid -debug value %q: expected true or false", override)
	}
	return value, nil
}

// productionRouter drives the same Service.ChatWithSink path used by HTTP.
// Its cache lets answer and route metrics inspect one execution per question.
type productionRouter struct {
	service  *appserver.Service
	tempDir  string
	owner    string
	modelID  string
	mu       sync.Mutex
	answers  map[string]output.Answer
	routing  map[string]evaluation.Routing
	errCache map[string]error
}

func newProductionRouter(ctx context.Context, source *config.Config, scoreThreshold float64, modelID string) (*productionRouter, error) {
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
		modelID:  modelID,
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
	_, err := r.service.ChatWithSink(requestCtx, uuid.NewString(), question, "", r.modelID, &answerText, recorder)
	events := recorder.Events()
	answer := output.Answer{Answer: strings.TrimSpace(answerText.String()), Sources: sourcesFromEvents(events)}
	routing := routingFromEvents(events)
	decision := routingrules.Decide(question)
	routing.DecisionSource = decision.Source
	routing.DecisionReason = decision.Reason
	routing.Answer = clip(answer.Answer, 500)
	r.mu.Lock()
	r.answers[question] = answer
	r.routing[question] = routing
	r.errCache[question] = err
	r.mu.Unlock()
	return answer, routing, err
}

func modelName(cfg *config.Config, modelID string) string {
	if modelID == "auto" {
		return "automatic model routing"
	}
	for _, profile := range cfg.Models {
		if profile.ID == modelID {
			return profile.Model
		}
	}
	if modelID == cfg.ActiveModel {
		return cfg.OpenAI.Model
	}
	return modelID
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
	routing.ToolSources = map[string][]string{}
	for _, ev := range events {
		switch ev.Type {
		case execution.SkillLoaded:
			routing.Loaded = append(routing.Loaded, stringsOf(ev.Payload["skill_names"])...)
			routing.Requested = append(routing.Requested, stringsOf(ev.Payload["requested_names"])...)
		case execution.SkillPreloaded:
			routing.Preloaded = append(routing.Preloaded, stringsOf(ev.Payload["skill_names"])...)
		case execution.ToolStarted:
			appendToolEvent(&routing, ev, "started")
		case execution.ToolCompleted, execution.FileReadDone:
			appendToolEvent(&routing, ev, "completed")
		case execution.ToolFailed:
			appendToolEvent(&routing, ev, "failed")
		}
	}
	routing.Loaded = uniqueSorted(routing.Loaded)
	routing.Requested = uniqueSorted(routing.Requested)
	routing.Preloaded = uniqueSorted(routing.Preloaded)
	routing.ToolStarted = uniqueSorted(routing.ToolStarted)
	routing.ToolCompleted = uniqueSorted(routing.ToolCompleted)
	routing.ToolFailed = uniqueSorted(routing.ToolFailed)
	for name, sources := range routing.ToolSources {
		routing.ToolSources[name] = uniqueSorted(sources)
	}
	return routing
}

func appendToolEvent(routing *evaluation.Routing, event execution.Event, outcome string) {
	name, _ := event.Payload["tool_name"].(string)
	name = strings.TrimSpace(name)
	// load_skills is represented by the dedicated skill_loaded event. It must
	// never inflate real tool-execution metrics.
	if name == "" || name == "load_skills" {
		return
	}
	switch outcome {
	case "started":
		routing.ToolStarted = append(routing.ToolStarted, name)
	case "completed":
		routing.ToolCompleted = append(routing.ToolCompleted, name)
	case "failed":
		routing.ToolFailed = append(routing.ToolFailed, name)
	}
	if source, ok := event.Payload["source"].(string); ok && strings.TrimSpace(source) != "" {
		routing.ToolSources[name] = append(routing.ToolSources[name], strings.TrimSpace(source))
	}
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
