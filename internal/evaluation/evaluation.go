// Package evaluation 计算固定 RAG 问答集的正确率、引用率、拒答率和平均耗时。
package evaluation

import (
	"context"
	"encoding/json"
	"os"
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
	ExpectSkills []string `json:"expect_skills,omitempty"`
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
	// RoutingTotal / RoutingAccuracy 只统计声明了 expect_skills 的题目：
	// 路由维度与答案维度用不同的分母，混在一起算会互相污染。
	RoutingTotal      int           `json:"routing_total"`
	RoutingAccuracy   float64       `json:"routing_accuracy"`
	Routing           []RouteResult `json:"routing,omitempty"`
	ToolEvidenceTotal int           `json:"tool_evidence_total"`
	ToolEvidenceRate  float64       `json:"tool_evidence_rate"`
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
	for _, marker := range []string{"无法确认", "不知道", "未知", "无足够依据", "没有找到足够", "没有足够资料", "未找到足够", "未记载", "未提及", "无法读取", "不能承诺"} {
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
	Answer       string
	Loaded       []string // 模型通过 load_skills 实际读取的技能
	Requested    []string // 模型在调用里请求过的技能（可能包含不存在的名字）
	Preloaded    []string // 本轮直接注入提示词的技能
	ToolEvidence bool     // 本轮有可审计的工具或服务端预加载事件
}

// RouteResult 是一道题的路由判定结果。
type RouteResult struct {
	Question  string   `json:"question"`
	Expected  []string `json:"expected_skills,omitempty"`
	Loaded    []string `json:"loaded_skills,omitempty"`
	Requested []string `json:"requested_skills,omitempty"`
	Preloaded []string `json:"preloaded_skills,omitempty"`
	// Missing 非空即未命中：它给出「期望但没加载」的清单，是排查路由问题的
	// 第一手材料 —— 只给一个布尔值等于让人猜。
	Missing []string `json:"missing_skills,omitempty"`
	// Extra 是加载了但不在期望里的技能。不计入错误，仅用于观察是否过度加载。
	Extra        []string `json:"extra_skills,omitempty"`
	Matched      bool     `json:"matched"`
	Answer       string   `json:"answer,omitempty"`
	Error        string   `json:"error,omitempty"`
	ToolEvidence bool     `json:"tool_evidence"`
}

// RunRouting 对声明了 ExpectSkills 的题目逐题执行 Agent 全链路并判定路由命中，
// 返回逐题结果与命中率。没有声明期望的题目不计入分母。
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
	for _, item := range cases {
		if len(item.ExpectSkills) == 0 {
			continue
		}
		total++
		result := RouteResult{Question: item.Question, Expected: item.ExpectSkills}
		routing, err := router.Route(ctx, item.Question)
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Answer = routing.Answer
			result.Loaded = routing.Loaded
			result.Requested = routing.Requested
			result.Preloaded = routing.Preloaded
			result.ToolEvidence = routing.ToolEvidence
			observed := append(append([]string{}, routing.Loaded...), routing.Preloaded...)
			result.Missing = notIn(item.ExpectSkills, observed)
			result.Extra = notIn(observed, item.ExpectSkills)
			result.Matched = len(result.Missing) == 0
		}
		if result.Matched {
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
