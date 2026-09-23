// Package evaluation 计算固定 RAG 问答集的正确率、引用率、拒答率和平均耗时。
package evaluation

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"my-eino-app/internal/eino/chain"
	"my-eino-app/internal/output"
)

// Runner is the smallest interface needed by the answer evaluation. Keeping
// it wider than *chain.RAGChain lets production evaluation exercise the same
// deterministic routing path as the HTTP service.
type Runner interface {
	Run(context.Context, chain.Input) (output.Answer, error)
}

// Case 类型。
type Case struct {
	Question     string `json:"question"`
	Expected     string `json:"expected"`
	MustCite     string `json:"must_cite"`
	ShouldRefuse bool   `json:"should_refuse"`
	// ExpectSkills 是本题期望被加载的技能名。留空表示该题不参与路由评测 ——
	// 「一个技能都不该加载」是另一种断言，混进来会让分母失真。
	ExpectSkills      []string          `json:"expect_skills,omitempty"`
	ExpectNoSkills    bool              `json:"expect_no_skills,omitempty"`
	ExpectTools       []string          `json:"expect_tools,omitempty"`
	ExpectToolSuccess []string          `json:"expect_tool_success,omitempty"`
	ExpectToolSources map[string]string `json:"expect_tool_sources,omitempty"`
	ForbidTools       []string          `json:"forbid_tools,omitempty"`
	// RouteMode separates deterministic server routing from model-autonomous
	// skill selection. Supported values are "server" and "model".
	RouteMode string `json:"route_mode,omitempty"`
}

// CaseResult 类型。
type CaseResult struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	// Asserted 表示这道题是否被真正判定过。expected 与 should_refuse 都为空时
	// 这道题没有任何可判定的断言，Correct 恒为 true —— 它不能计入正确率。
	Asserted   bool   `json:"asserted"`
	Correct    bool   `json:"correct"`
	Cited      bool   `json:"cited"`
	Refused    bool   `json:"refused"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// Asserted 表示这道题有没有可判定的答案断言。
//
// 为什么需要它：判据是 strings.Contains(answer, expected)，而
// strings.Contains(任意字符串, "") **恒为真**。执行类题目（写报告、读文件、
// 设计模板）只声明 expect_skills、不声明 expected，于是它们的 Correct 永远是
// true —— 哪怕模型只回一句「请提供文件路径」。把这些题算进正确率的分母，
// 等于让一个永远不可能失败的分母去稀释指标。
func (c Case) Asserted() bool {
	return c.ShouldRefuse || strings.TrimSpace(c.Expected) != ""
}

// Report 类型。
type Report struct {
	Total int `json:"total"`
	// AssertedTotal 是**可判定**的题目数（见 Case.Asserted）。
	// CorrectRate 的分母是它，不是 Total —— 否则执行类题目会稀释指标。
	AssertedTotal     int          `json:"asserted_total"`
	CorrectRate       float64      `json:"correct_rate"`
	CitationRate      float64      `json:"citation_rate"`
	RefusalRate       float64      `json:"refusal_rate"`
	AverageDurationMS float64      `json:"average_duration_ms"`
	Cases             []CaseResult `json:"cases"`
	// RoutingTotal / RoutingAccuracy 只统计技能加载/不加载断言，不与答案质量或工具执行率混算。
	RoutingTotal             int           `json:"routing_total"`
	RoutingAccuracy          float64       `json:"routing_accuracy"`
	Routing                  []RouteResult `json:"routing,omitempty"`
	ServerRouteTotal         int           `json:"server_route_total"`
	ServerRouteMatched       int           `json:"server_route_matched"`
	ServerRouteRecall        float64       `json:"server_route_recall"`
	AutonomousRouteTotal     int           `json:"autonomous_route_total"`
	AutonomousRouteMatched   int           `json:"autonomous_route_matched"`
	AutonomousRouteAccuracy  float64       `json:"autonomous_route_accuracy"`
	NoSkillTotal             int           `json:"no_skill_total"`
	NoSkillFalsePositives    int           `json:"no_skill_false_positives"`
	NoSkillFalsePositiveRate float64       `json:"no_skill_false_positive_rate"`
	SkillEvidenceTotal       int           `json:"skill_evidence_total"`
	SkillEvidenceMatched     int           `json:"skill_evidence_matched"`
	SkillEvidenceRate        float64       `json:"skill_evidence_rate"`
	ToolAttemptTotal         int           `json:"tool_attempt_total"`
	ToolAttemptMatched       int           `json:"tool_attempt_matched"`
	ToolAttemptRate          float64       `json:"tool_attempt_rate"`
	ToolSuccessTotal         int           `json:"tool_success_total"`
	ToolSuccessMatched       int           `json:"tool_success_matched"`
	ToolSuccessRate          float64       `json:"tool_success_rate"`
	ForbiddenToolCaseTotal   int           `json:"forbidden_tool_case_total"`
	ForbiddenToolViolations  int           `json:"forbidden_tool_violations"`
	ToolSourceTotal          int           `json:"tool_source_total"`
	ToolSourceMatched        int           `json:"tool_source_matched"`
	ToolSourceRate           float64       `json:"tool_source_rate"`
	ToolStartEvents          int           `json:"tool_start_events"`
	ToolCompletionEvents     int           `json:"tool_completion_events"`
	ToolFailureEvents        int           `json:"tool_failure_events"`
	RouteModeTotal           int           `json:"route_mode_total"`
	RouteModeMatched         int           `json:"route_mode_matched"`
	RouteModeAccuracy        float64       `json:"route_mode_accuracy"`
	Model                    string        `json:"model,omitempty"`
	ModelID                  string        `json:"model_id,omitempty"`
	Debug                    *bool         `json:"debug,omitempty"`
	ScoreThreshold           float64       `json:"score_threshold,omitempty"`
}

// LoadCases 函数。
func LoadCases(path string) ([]Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []Case
	err = json.Unmarshal(data, &cases)
	return cases, err
}

// Run 逐题执行真实 RAG Chain。指标使用固定规则计算，结果可重复比较。
func Run(ctx context.Context, runner Runner, cases []Case, input func(string) chain.Input) Report {
	report := Report{Total: len(cases)}
	correct, asserted, citationRequired, cited, refusalRequired, refused, totalMS := 0, 0, 0, 0, 0, 0, int64(0)
	for _, item := range cases {
		// 指标分母由评测集定义，不能因为一次请求报错就跳过，否则会虚高。
		if item.MustCite != "" {
			citationRequired++
		}
		if item.ShouldRefuse {
			refusalRequired++
		}
		// 没有断言的题目不计入正确率（见 Case.Asserted）。
		assertedCase := item.Asserted()
		started := time.Now()
		answer, err := runner.Run(ctx, input(item.Question))
		result := CaseResult{Question: item.Question, Answer: answer.Answer, Asserted: assertedCase, DurationMS: time.Since(started).Milliseconds()}
		totalMS += result.DurationMS
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Refused = isRefusal(answer.Answer)
			if item.ShouldRefuse {
				result.Correct = result.Refused
			} else if assertedCase {
				result.Correct = strings.Contains(normalize(answer.Answer), normalize(item.Expected))
			}
			if item.MustCite != "" {
				for _, source := range answer.Sources {
					if strings.Contains(source, item.MustCite) {
						result.Cited = true
						break
					}
				}
			}
			if assertedCase {
				asserted++
				if result.Correct {
					correct++
				}
			}
			if result.Cited {
				cited++
			}
			if item.ShouldRefuse {
				if result.Refused {
					refused++
				}
			}
		}
		report.Cases = append(report.Cases, result)
	}
	report.AssertedTotal = asserted
	if report.Total > 0 {
		report.AverageDurationMS = float64(totalMS) / float64(report.Total)
	}
	if asserted > 0 {
		report.CorrectRate = float64(correct) / float64(asserted)
	}
	if citationRequired > 0 {
		report.CitationRate = float64(cited) / float64(citationRequired)
	}
	if refusalRequired > 0 {
		report.RefusalRate = float64(refused) / float64(refusalRequired)
	}
	return report
}

// isRefusal recognizes the stable refusal language used by both the fixed RAG
// chain and the server-side knowledge preflight. The two paths intentionally
// use slightly different natural-language variants, so the metric must treat
// them as one product behavior rather than under-counting valid refusals.
func isRefusal(answer string) bool {
	for _, marker := range []string{
		"无法确认", "不知道", "未知", "无足够依据", "没有找到足够", "没有足够资料", "未找到足够",
		"未记载", "没有记载", "未记录", "没有记录", "未提及", "没有提及", "无法读取", "不能承诺",
	} {
		if strings.Contains(answer, marker) {
			return true
		}
	}
	return false
}

// Router 跑一道题并返回本轮实际加载的技能名。
//
// 评测包只依赖这个接口，Agent 与技能目录的装配留给调用方：这样路由判定逻辑
// 可以用假实现做单元测试（不需要真实模型），评测包也不会反向依赖 server。
type Router interface {
	Route(ctx context.Context, question string) (Routing, error)
}

// Routing 是一次 Agent 全链路执行的观测结果。
type Routing struct {
	Answer         string
	Loaded         []string // 模型通过 load_skills 实际读取的技能
	Requested      []string // 模型在调用里请求过的技能（可能包含不存在的名字）
	Preloaded      []string // 本轮直接注入提示词的技能
	DecisionSource string   // server_rule 或 model_autonomous
	DecisionReason string
	ToolStarted    []string // 工具调用开始事件
	ToolCompleted  []string // 工具成功终态事件
	ToolFailed     []string // 工具失败终态事件
	ToolSources    map[string][]string
}

// RouteResult 是一道题的路由判定结果。
type RouteResult struct {
	CaseIndex      int      `json:"case_index"`
	Question       string   `json:"question"`
	Expected       []string `json:"expected_skills,omitempty"`
	ExpectNoSkills bool     `json:"expect_no_skills,omitempty"`
	RouteMode      string   `json:"route_mode,omitempty"`
	DecisionSource string   `json:"decision_source,omitempty"`
	DecisionReason string   `json:"decision_reason,omitempty"`
	Loaded         []string `json:"loaded_skills,omitempty"`
	Requested      []string `json:"requested_skills,omitempty"`
	Preloaded      []string `json:"preloaded_skills,omitempty"`
	// Missing 非空即未命中：它给出「期望但没加载」的清单，是排查路由问题的
	// 第一手材料 —— 只给一个布尔值等于让人猜。
	Missing []string `json:"missing_skills,omitempty"`
	// Extra 是加载了但不在期望里的技能。不计入错误，仅用于观察是否过度加载。
	Extra               []string `json:"extra_skills,omitempty"`
	SkillAsserted       bool     `json:"skill_asserted"`
	SkillEvidence       bool     `json:"skill_evidence"`
	RouteModeAsserted   bool     `json:"route_mode_asserted"`
	RouteModeMatched    bool     `json:"route_mode_matched"`
	Matched             bool     `json:"matched"`
	StartedTools        []string `json:"started_tools,omitempty"`
	CompletedTools      []string `json:"completed_tools,omitempty"`
	FailedTools         []string `json:"failed_tools,omitempty"`
	MissingTools        []string `json:"missing_tools,omitempty"`
	MissingToolSuccess  []string `json:"missing_tool_success,omitempty"`
	ForbiddenToolCalls  []string `json:"forbidden_tool_calls,omitempty"`
	ToolAttemptAsserted bool     `json:"tool_attempt_asserted"`
	ToolAttemptMatched  bool     `json:"tool_attempt_matched"`
	ToolSuccessAsserted bool     `json:"tool_success_asserted"`
	ToolSuccessMatched  bool     `json:"tool_success_matched"`
	ToolSourceAsserted  bool     `json:"tool_source_asserted"`
	ToolSourceMatched   bool     `json:"tool_source_matched"`
	MissingToolSources  []string `json:"missing_tool_sources,omitempty"`
	ForbidToolsAsserted bool     `json:"forbid_tools_asserted"`
	ForbidToolsMatched  bool     `json:"forbid_tools_matched"`
	Answer              string   `json:"answer,omitempty"`
	Error               string   `json:"error,omitempty"`
}

// RunRouting 对声明了技能、工具或路由模式断言的题目逐题执行 Agent 全链路。
// 返回的命中率只衡量技能加载/不加载和路由来源，不把工具执行率混入其中。
//
// 判定是「覆盖」而非「相等」：期望的技能必须全部被加载（Loaded ⊇ Expected）。
// 多加载记入 Extra 但不算错 —— 技能规则本身鼓励组合多个技能，把多加载判为错误
// 会让指标变成噪音；而「该用的没用」才是路由质量要抓的问题。
//
// Loaded 与 Preloaded 都是可审计的路由结果：生产服务端确定性路由使用
// Preloaded，模型自主选择使用 Loaded。
func RunRouting(ctx context.Context, router Router, cases []Case) ([]RouteResult, float64) {
	var results []RouteResult
	matched, total := 0, 0
	for caseIndex, item := range cases {
		if !item.hasRoutingAssertions() {
			continue
		}
		skillAsserted := len(item.ExpectSkills) > 0 || item.ExpectNoSkills
		if skillAsserted {
			total++
		}
		result := RouteResult{
			CaseIndex:           caseIndex,
			Question:            item.Question,
			Expected:            item.ExpectSkills,
			ExpectNoSkills:      item.ExpectNoSkills,
			RouteMode:           item.RouteMode,
			SkillAsserted:       skillAsserted,
			RouteModeAsserted:   item.RouteMode != "",
			ToolAttemptAsserted: len(item.ExpectTools) > 0,
			ToolSuccessAsserted: len(item.ExpectToolSuccess) > 0,
			ToolSourceAsserted:  len(item.ExpectToolSources) > 0,
			ForbidToolsAsserted: len(item.ForbidTools) > 0,
		}
		routing, err := router.Route(ctx, item.Question)
		if err != nil {
			result.Error = err.Error()
		}
		// Keep partial evidence even when the model fails after routing or a tool
		// call. Answer quality, route selection, and execution are separate axes.
		result.Answer = routing.Answer
		result.Loaded = routing.Loaded
		result.Requested = routing.Requested
		result.Preloaded = routing.Preloaded
		result.DecisionSource = routing.DecisionSource
		result.DecisionReason = routing.DecisionReason
		result.StartedTools = routing.ToolStarted
		result.CompletedTools = routing.ToolCompleted
		result.FailedTools = routing.ToolFailed
		observed := append(append([]string{}, routing.Loaded...), routing.Preloaded...)
		result.SkillEvidence = len(observed) > 0
		result.Missing = notIn(item.ExpectSkills, observed)
		result.Extra = notIn(observed, item.ExpectSkills)
		skillMatched := len(result.Missing) == 0
		if item.ExpectNoSkills {
			skillMatched = len(observed) == 0
		}
		result.RouteModeMatched = !result.RouteModeAsserted || routingModeMatches(item.RouteMode, routing.DecisionSource)
		result.Matched = !skillAsserted || skillMatched
		result.MissingTools = notIn(item.ExpectTools, routing.ToolStarted)
		result.ToolAttemptMatched = len(result.MissingTools) == 0
		result.MissingToolSuccess = notIn(item.ExpectToolSuccess, routing.ToolCompleted)
		result.ToolSuccessMatched = len(result.MissingToolSuccess) == 0
		result.MissingToolSources = missingToolSources(item.ExpectToolSources, routing.ToolSources)
		result.ToolSourceMatched = len(result.MissingToolSources) == 0
		observedTools := append(append(append([]string{}, routing.ToolStarted...), routing.ToolCompleted...), routing.ToolFailed...)
		result.ForbiddenToolCalls = intersect(item.ForbidTools, observedTools)
		result.ForbidToolsMatched = len(result.ForbiddenToolCalls) == 0
		if skillAsserted && result.Matched {
			matched++
		}
		results = append(results, result)
	}
	accuracy := 0.0
	if total > 0 {
		accuracy = float64(matched) / float64(total)
	}
	return results, accuracy
}

func (c Case) hasRoutingAssertions() bool {
	return len(c.ExpectSkills) > 0 || c.ExpectNoSkills || len(c.ExpectTools) > 0 || len(c.ExpectToolSuccess) > 0 || len(c.ExpectToolSources) > 0 || len(c.ForbidTools) > 0 || c.RouteMode != ""
}

// SummarizeRouting fills the independently-denominated routing and tool metrics.
// A failed route remains in every applicable denominator and therefore lowers
// the corresponding rate instead of making the report look better.
func SummarizeRouting(report *Report, cases []Case, results []RouteResult) {
	if report == nil {
		return
	}
	report.Routing = results
	var routeMatched, routeTotal, serverMatched, serverTotal, modelMatched, modelTotal int
	var noSkillFalsePositives, noSkillTotal, skillEvidenceMatched, skillEvidenceTotal int
	var toolAttemptMatched, toolAttemptTotal, toolSuccessMatched, toolSuccessTotal int
	var forbiddenViolations, forbiddenTotal, sourceMatched, sourceTotal int
	var routeModeMatched, routeModeTotal int
	var toolStartEvents, toolCompletionEvents, toolFailureEvents int
	for _, result := range results {
		if result.CaseIndex < 0 || result.CaseIndex >= len(cases) {
			continue
		}
		item := cases[result.CaseIndex]
		toolStartEvents += len(result.StartedTools)
		toolCompletionEvents += len(result.CompletedTools)
		toolFailureEvents += len(result.FailedTools)
		if len(item.ExpectSkills) > 0 || item.ExpectNoSkills {
			routeTotal++
			if result.Matched {
				routeMatched++
			}
		}
		if len(item.ExpectSkills) > 0 {
			skillEvidenceTotal++
			if len(result.Missing) == 0 {
				skillEvidenceMatched++
			}
			switch item.RouteMode {
			case "server":
				serverTotal++
				if len(result.Missing) == 0 && result.RouteModeMatched {
					serverMatched++
				}
			case "model":
				modelTotal++
				if len(result.Missing) == 0 && result.RouteModeMatched {
					modelMatched++
				}
			}
		}
		if item.ExpectNoSkills {
			noSkillTotal++
			if result.SkillEvidence {
				noSkillFalsePositives++
			}
		}
		if result.RouteModeAsserted {
			routeModeTotal++
			if result.RouteModeMatched {
				routeModeMatched++
			}
		}
		if len(item.ExpectTools) > 0 {
			toolAttemptTotal++
			if result.ToolAttemptMatched {
				toolAttemptMatched++
			}
		}
		if len(item.ExpectToolSuccess) > 0 {
			toolSuccessTotal++
			if result.ToolSuccessMatched {
				toolSuccessMatched++
			}
		}
		if len(item.ForbidTools) > 0 {
			forbiddenTotal++
			if len(result.ForbiddenToolCalls) > 0 {
				forbiddenViolations++
			}
		}
		if len(item.ExpectToolSources) > 0 {
			sourceTotal += len(item.ExpectToolSources)
			sourceMatched += len(item.ExpectToolSources) - len(result.MissingToolSources)
		}
	}
	report.RoutingTotal, report.ServerRouteTotal, report.ServerRouteMatched = routeTotal, serverTotal, serverMatched
	report.SkillEvidenceTotal, report.SkillEvidenceMatched = skillEvidenceTotal, skillEvidenceMatched
	report.NoSkillTotal, report.NoSkillFalsePositives = noSkillTotal, noSkillFalsePositives
	report.AutonomousRouteTotal, report.AutonomousRouteMatched = modelTotal, modelMatched
	report.ToolAttemptTotal, report.ToolAttemptMatched = toolAttemptTotal, toolAttemptMatched
	report.ToolSuccessTotal, report.ToolSuccessMatched = toolSuccessTotal, toolSuccessMatched
	report.ForbiddenToolCaseTotal, report.ForbiddenToolViolations = forbiddenTotal, forbiddenViolations
	report.ToolSourceTotal, report.ToolSourceMatched = sourceTotal, sourceMatched
	report.ToolStartEvents, report.ToolCompletionEvents, report.ToolFailureEvents = toolStartEvents, toolCompletionEvents, toolFailureEvents
	report.RouteModeTotal, report.RouteModeMatched = routeModeTotal, routeModeMatched
	report.RoutingAccuracy = divide(routeMatched, routeTotal)
	report.ServerRouteRecall = divide(serverMatched, serverTotal)
	report.AutonomousRouteAccuracy = divide(modelMatched, modelTotal)
	report.SkillEvidenceRate = divide(skillEvidenceMatched, skillEvidenceTotal)
	report.NoSkillFalsePositiveRate = divide(noSkillFalsePositives, noSkillTotal)
	report.ToolAttemptRate = divide(toolAttemptMatched, toolAttemptTotal)
	report.ToolSuccessRate = divide(toolSuccessMatched, toolSuccessTotal)
	report.ToolSourceRate = divide(sourceMatched, sourceTotal)
	report.RouteModeAccuracy = divide(routeModeMatched, routeModeTotal)
}

func divide(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func missingToolSources(expected map[string]string, observed map[string][]string) []string {
	var missing []string
	for tool, source := range expected {
		if !contains(observed[tool], source) {
			missing = append(missing, tool+"="+source)
		}
	}
	sort.Strings(missing)
	return missing
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func routingModeMatches(expected, actual string) bool {
	switch expected {
	case "server":
		return actual == "server_rule"
	case "model":
		return actual == "model_autonomous"
	default:
		return false
	}
}

func intersect(want, got []string) []string {
	have := make(map[string]bool, len(got))
	for _, value := range got {
		have[value] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, value := range want {
		if have[value] && !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	return out
}

// notIn 返回 want 中不属于 got 的元素，保持 want 的顺序并去重。
func notIn(want, got []string) []string {
	have := make(map[string]bool, len(got))
	for _, name := range got {
		have[name] = true
	}
	seen := make(map[string]bool, len(want))
	var out []string
	for _, name := range want {
		if have[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func normalize(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("，。、；：,.!?！？-—和与", r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}
