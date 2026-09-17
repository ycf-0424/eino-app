// Command eval 对本地 Ollama + Milvus RAG 执行固定问答评测。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"my-eino-app/internal/checkpoint"
	"my-eino-app/internal/config"
	"my-eino-app/internal/eino"
	"my-eino-app/internal/eino/agent"
	"my-eino-app/internal/eino/chain"
	"my-eino-app/internal/eino/model"
	"my-eino-app/internal/eino/rag"
	toolset "my-eino-app/internal/eino/tool"
	"my-eino-app/internal/evaluation"
	"my-eino-app/internal/execution"
	"my-eino-app/internal/health"
	"my-eino-app/internal/skill"
)

func main() {
	file := flag.String("file", "", "评测集 JSON，默认 internal/integration/eval_cases.json")
	threshold := flag.Float64("threshold", -1, "覆盖相似度阈值；-1 使用 config.yaml")
	minCorrect := flag.Float64("min-correct", 0.8, "最低正确率，未达到时返回非零退出码")
	minCitation := flag.Float64("min-citation", 1, "最低引用率，未达到时返回非零退出码")
	minRefusal := flag.Float64("min-refusal", 1, "最低拒答率，未达到时返回非零退出码")
	minRouting := flag.Float64("min-routing", -1, "最低路由准确率，未达到时返回非零退出码；-1 表示不检查")
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
	// 路由维度只在评测集声明了 expect_skills 时执行：它要额外装配一次 Agent，
	// 且每题都会真实调用模型。没有这类题目时不该付这份成本。
	if hasRoutingCases(cases) {
		router, err := newAgentRouter(ctx, cfg, store, cm)
		if err != nil {
			fail(err)
		}
		routing, accuracy := evaluation.RunRouting(ctx, router, cases)
		report.Routing = routing
		report.RoutingTotal = len(routing)
		report.RoutingAccuracy = accuracy
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

// agentRouter 用 Agent 全链路跑一道题，并从执行事件里提取路由结果。
type agentRouter struct {
	chat *agent.ChatAgent
}

// Route 执行一轮问答。每题都清空历史并换新 session id：路由判断不该受上一题
// 的上下文影响，否则同一道题在不同次序下会得到不同结论。
func (r *agentRouter) Route(ctx context.Context, question string) (evaluation.Routing, error) {
	recorder := execution.NewRecorder(0)
	r.chat.SetHistory(nil)
	sessionID := uuid.NewString()
	r.chat.SetSessionID(sessionID)
	// 与 internal/server 同步接口同一条路径：NewSession 才是 Emitter，
	// Recorder 只是它的 sink。评测不落库，store 传 nil。
	em := execution.NewSession(uuid.NewString(), sessionID, nil, recorder, execution.SessionOptions{})
	defer em.Close()
	ctx = execution.WithEmitter(ctx, em)
	var answer strings.Builder
	if err := r.chat.AskTo(ctx, question, &answer); err != nil {
		var approval *agent.ApprovalRequest
		if errors.As(err, &approval) {
			return evaluation.Routing{}, fmt.Errorf("question triggered approval for tool %s; evaluation has no interactive approver", approval.ToolName)
		}
		return evaluation.Routing{}, err
	}
	routing := routingFromEvents(recorder.Events())
	routing.Answer = clip(answer.String(), 500)
	return routing, nil
}

// newAgentRouter 按与 internal/server 相同的方式装配 Agent。
// 评测不落库：只给 checkpoint 目录，不接 session 持久化。
func newAgentRouter(ctx context.Context, cfg *config.Config, knowledge rag.Store, cm eino.ChatModel) (*agentRouter, error) {
	checkpoints, err := checkpoint.New(cfg.Agent.CheckpointDir)
	if err != nil {
		return nil, err
	}
	tools := make([]eino.BaseTool, 0, 3)
	if cfg.LocalFiles.Enabled {
		fileTool, err := toolset.NewLocalFileReadTool(cfg.LocalFiles.Roots, cfg.LocalFiles.MaxBytes)
		if err != nil {
			return nil, err
		}
		tools = append(tools, fileTool)
	}
	if knowledge != nil {
		tools = append(tools, toolset.NewKnowledgeToolWithOptions(knowledge, rag.SearchOptions{
			TopK: cfg.RAG.TopK, ScoreThreshold: cfg.RAG.ScoreThreshold, MaxContextChars: cfg.RAG.MaxContextChars,
		}))
	}
	// 必须把「本次真的注册了哪些工具」告诉技能目录：目录会据此为每个技能标注
	// 能力状态，缺了这个参数所有技能都会被标成「缺少工具…不能承诺执行」，
	// 相当于在提示词里劝模型不要加载技能 —— 路由准确率会失真。
	availableTools := append([]string{"current_time", "write_note", "load_skills"}, toolNames(ctx, tools)...)
	// preferred 留空，让模型完全自主选择。
	instruction, skillTool, _, err := skill.NewLoader(cfg.Skills.Dir).Runtime(cfg.Skills.Default, availableTools...)
	if err != nil {
		return nil, err
	}
	tools = append(tools, skillTool)
	chat, err := agent.NewWithInstruction(ctx, cm, cfg.Debug, cfg.Agent.MultiAgent, checkpoints, instruction, tools...)
	if err != nil {
		return nil, err
	}
	return &agentRouter{chat: chat}, nil
}

// toolNames 取工具注册名。取不到名字的工具不会出现在「当前接入工具」里，
// 只会让它的能力状态偏保守，不会误报成已接入。
func toolNames(ctx context.Context, tools []eino.BaseTool) []string {
	names := make([]string, 0, len(tools))
	for _, item := range tools {
		info, err := item.Info(ctx)
		if err != nil {
			continue
		}
		names = append(names, info.Name)
	}
	return names
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
