package evaluation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	routingrules "my-eino-app/internal/routing"
)

// fakeRouter 按题目文本返回预置的路由结果，并记录被调用的题目。
type fakeRouter struct {
	results map[string]Routing
	errs    map[string]error
	calls   []string
}

func (f *fakeRouter) Route(_ context.Context, question string) (Routing, error) {
	f.calls = append(f.calls, question)
	result := f.results[question]
	if err, ok := f.errs[question]; ok {
		return result, err
	}
	return result, nil
}

func TestIsRefusalRecognizesKnowledgeAbsence(t *testing.T) {
	for _, answer := range []string{
		"知识库没有记录星河系统在火星的办公室地址。",
		"现有资料未提及这个地址。",
		"无法确认该信息。",
	} {
		if !isRefusal(answer) {
			t.Errorf("isRefusal(%q) = false, want true", answer)
		}
	}
	if isRefusal("星河系统在上海部署，使用 Go、Eino 和 Milvus。") {
		t.Fatal("ordinary factual answers must not count as refusals")
	}
}

// 没有声明期望的题目既不执行也不计分：跑一次 Agent 很贵，且「零期望」是另一种断言。
func TestRunRoutingSkipsCasesWithoutExpectations(t *testing.T) {
	router := &fakeRouter{results: map[string]Routing{
		"q1": {Loaded: []string{"documents"}},
	}}
	cases := []Case{
		{Question: "q0"}, // 无 expect_skills
		{Question: "q1", ExpectSkills: []string{"documents"}},
	}

	results, accuracy := RunRouting(context.Background(), router, cases)
	if len(results) != 1 || results[0].Question != "q1" {
		t.Fatalf("只应产出 1 条路由结果: %+v", results)
	}
	if len(router.calls) != 1 || router.calls[0] != "q1" {
		t.Fatalf("无期望的题目不该被执行: %v", router.calls)
	}
	if accuracy != 1 {
		t.Fatalf("accuracy 应为 1，实际 %v", accuracy)
	}
}

// 判定是覆盖而不是相等：漏加载算错，多加载不算错但记入 Extra。
func TestRunRoutingUsesCoverageNotEquality(t *testing.T) {
	router := &fakeRouter{results: map[string]Routing{
		"partial": {Loaded: []string{"pdf"}},
		"full":    {Loaded: []string{"documents", "pdf"}},
		"extra":   {Loaded: []string{"documents", "pdf", "visualize"}},
	}}
	want := []string{"documents", "pdf"}
	cases := []Case{
		{Question: "partial", ExpectSkills: want},
		{Question: "full", ExpectSkills: want},
		{Question: "extra", ExpectSkills: want},
	}

	results, accuracy := RunRouting(context.Background(), router, cases)
	if len(results) != 3 {
		t.Fatalf("应产出 3 条: %+v", results)
	}
	if results[0].Matched || len(results[0].Missing) != 1 || results[0].Missing[0] != "documents" {
		t.Fatalf("漏加载应判为未命中并给出 Missing: %+v", results[0])
	}
	if !results[1].Matched {
		t.Fatalf("全部命中应判为命中: %+v", results[1])
	}
	if !results[2].Matched || len(results[2].Extra) != 1 || results[2].Extra[0] != "visualize" {
		t.Fatalf("多加载不算错但要记 Extra: %+v", results[2])
	}
	if diff := accuracy - 2.0/3.0; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("accuracy 应为 2/3，实际 %v", accuracy)
	}
}

// 服务端确定性预加载是生产路由证据，应与模型自主加载一起计入命中。
func TestRunRoutingAcceptsPreloaded(t *testing.T) {
	router := &fakeRouter{results: map[string]Routing{
		"q": {Preloaded: []string{"documents"}},
	}}
	results, accuracy := RunRouting(context.Background(), router, []Case{
		{Question: "q", ExpectSkills: []string{"documents"}},
	})

	if !results[0].Matched || accuracy != 1 {
		t.Fatalf("预加载应算命中: %+v accuracy=%v", results[0], accuracy)
	}
	if len(results[0].Preloaded) != 1 {
		t.Fatalf("预加载仍应被记录: %+v", results[0])
	}
}

// 执行失败要如实记下错误，并且仍然计入分母 —— 跳过会虚高准确率。
func TestRunRoutingCountsFailuresInDenominator(t *testing.T) {
	router := &fakeRouter{
		results: map[string]Routing{"ok": {Loaded: []string{"documents"}}},
		errs:    map[string]error{"bad": errors.New("model unavailable")},
	}
	cases := []Case{
		{Question: "ok", ExpectSkills: []string{"documents"}},
		{Question: "bad", ExpectSkills: []string{"documents"}},
	}

	results, accuracy := RunRouting(context.Background(), router, cases)
	if results[1].Error == "" || results[1].Matched {
		t.Fatalf("失败题应记录错误且未命中: %+v", results[1])
	}
	if accuracy != 0.5 {
		t.Fatalf("失败题应计入分母: accuracy=%v", accuracy)
	}
}

func TestRunRoutingPreservesEvidenceReturnedWithError(t *testing.T) {
	router := &fakeRouter{
		results: map[string]Routing{
			"partial": {
				Preloaded:      []string{"documents"},
				DecisionSource: "server_rule",
				ToolStarted:    []string{"local_file_read"},
				ToolFailed:     []string{"local_file_read"},
			},
		},
		errs: map[string]error{"partial": errors.New("model failed after tool call")},
	}
	cases := []Case{{
		Question: "partial", ExpectSkills: []string{"documents"}, RouteMode: "server",
		ExpectTools: []string{"local_file_read"}, ExpectToolSuccess: []string{"local_file_read"},
	}}
	results, accuracy := RunRouting(context.Background(), router, cases)
	if results[0].Error == "" || !results[0].Matched || accuracy != 1 {
		t.Fatalf("partial skill evidence should remain measurable after a later model error: %+v accuracy=%v", results[0], accuracy)
	}
	if !results[0].ToolAttemptMatched || results[0].ToolSuccessMatched || len(results[0].FailedTools) != 1 {
		t.Fatalf("tool attempt/failure evidence should survive the model error: %+v", results[0])
	}
}

// 没有可计分的题时准确率为 0，且不产生除零。
func TestRunRoutingWithoutScorableCases(t *testing.T) {
	results, accuracy := RunRouting(context.Background(), &fakeRouter{}, []Case{{Question: "q"}})
	if len(results) != 0 || accuracy != 0 {
		t.Fatalf("无可计分题时 results=%v accuracy=%v", results, accuracy)
	}
}

func TestRunRoutingSeparatesSkillToolAndSourceEvidence(t *testing.T) {
	cases := []Case{
		{Question: "server skill", ExpectSkills: []string{"documents"}, RouteMode: "server"},
		{Question: "failed tool", ExpectNoSkills: true, ExpectTools: []string{"local_file_read"}, ExpectToolSuccess: []string{"local_file_read"}},
		{Question: "knowledge tool", ExpectNoSkills: true, ExpectTools: []string{"knowledge_search"}, ExpectToolSuccess: []string{"knowledge_search"}, ExpectToolSources: map[string]string{"knowledge_search": "server_preflight"}, RouteMode: "server"},
		{Question: "false positive", ExpectNoSkills: true, ForbidTools: []string{"local_file_read"}},
	}
	router := &fakeRouter{results: map[string]Routing{
		"server skill":   {Preloaded: []string{"documents"}, DecisionSource: "server_rule"},
		"failed tool":    {ToolStarted: []string{"local_file_read"}, ToolFailed: []string{"local_file_read"}},
		"knowledge tool": {ToolStarted: []string{"knowledge_search"}, ToolCompleted: []string{"knowledge_search"}, ToolSources: map[string][]string{"knowledge_search": {"server_preflight"}}, DecisionSource: "server_rule"},
		"false positive": {Preloaded: []string{"pdf"}, ToolStarted: []string{"local_file_read"}},
	}}
	results, _ := RunRouting(context.Background(), router, cases)
	var report Report
	SummarizeRouting(&report, cases, results)

	if report.RoutingTotal != 4 || report.RoutingAccuracy != 0.75 {
		t.Fatalf("skill/no-skill denominator mismatch: %+v", report)
	}
	if report.SkillEvidenceTotal != 1 || report.SkillEvidenceMatched != 1 || report.SkillEvidenceRate != 1 {
		t.Fatalf("skill evidence mismatch: %+v", report)
	}
	if report.NoSkillTotal != 3 || report.NoSkillFalsePositives != 1 || report.NoSkillFalsePositiveRate != 1.0/3.0 {
		t.Fatalf("no-skill false positives mismatch: %+v", report)
	}
	if report.ServerRouteTotal != 1 || report.ServerRouteMatched != 1 || report.ServerRouteRecall != 1 {
		t.Fatalf("server route recall mismatch: %+v", report)
	}
	if report.ToolAttemptTotal != 2 || report.ToolAttemptMatched != 2 || report.ToolAttemptRate != 1 {
		t.Fatalf("tool attempt metric must use tool_started: %+v", report)
	}
	if report.ToolSuccessTotal != 2 || report.ToolSuccessMatched != 1 || report.ToolSuccessRate != 0.5 {
		t.Fatalf("failed tool must not count as success: %+v", report)
	}
	if report.ToolSourceTotal != 1 || report.ToolSourceMatched != 1 || report.ToolSourceRate != 1 {
		t.Fatalf("tool source mismatch: %+v", report)
	}
	if report.ForbiddenToolCaseTotal != 1 || report.ForbiddenToolViolations != 1 {
		t.Fatalf("forbidden tool call mismatch: %+v", report)
	}
	if results[0].ToolAttemptMatched != true || results[0].ToolAttemptAsserted {
		t.Fatalf("preloaded skill must not be treated as a tool attempt: %+v", results[0])
	}
	if results[1].ToolSuccessMatched || len(results[1].MissingToolSuccess) != 1 {
		t.Fatalf("tool_failed must not count as successful completion: %+v", results[1])
	}
	if !results[2].ToolSourceMatched || len(results[2].MissingToolSources) != 0 {
		t.Fatalf("server preflight source should be verified: %+v", results[2])
	}
}

func TestRunRoutingChecksRouteModeSeparately(t *testing.T) {
	cases := []Case{
		{Question: "server", ExpectSkills: []string{"documents"}, RouteMode: "server"},
		{Question: "model", ExpectSkills: []string{"pdf"}, RouteMode: "model"},
	}
	router := &fakeRouter{results: map[string]Routing{
		"server": {Preloaded: []string{"documents"}, DecisionSource: "server_rule"},
		"model":  {Loaded: []string{"pdf"}, DecisionSource: "model_autonomous"},
	}}
	results, accuracy := RunRouting(context.Background(), router, cases)
	var report Report
	SummarizeRouting(&report, cases, results)
	if accuracy != 1 || report.RouteModeAccuracy != 1 || report.AutonomousRouteAccuracy != 1 || report.AutonomousRouteTotal != 1 {
		t.Fatalf("expected routes and source modes should match: results=%+v report=%+v", results, report)
	}
}

func TestEvalCasesRouteModesAgreeWithSharedDeterministicRouter(t *testing.T) {
	cases, err := LoadCases(filepath.Join("..", "integration", "eval_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	resultsByQuestion := make(map[string]Routing, len(cases))
	for _, item := range cases {
		decision := routingrules.Decide(item.Question)
		resultsByQuestion[item.Question] = Routing{DecisionSource: decision.Source, Preloaded: decision.PreloadSkills}
	}
	routes, _ := RunRouting(context.Background(), &fakeRouter{results: resultsByQuestion}, cases)
	for _, route := range routes {
		if route.RouteModeAsserted && !route.RouteModeMatched {
			t.Errorf("case %d declares route_mode=%q but shared routing chose %q (%s): %s", route.CaseIndex, route.RouteMode, route.DecisionSource, route.DecisionReason, route.Question)
		}
	}
	var report Report
	SummarizeRouting(&report, cases, routes)
	if report.RouteModeTotal == 0 || report.RouteModeAccuracy != 1 {
		t.Fatalf("all route mode cases should agree with the shared router: %+v", report)
	}
}

func TestNotInKeepsOrderAndDedupes(t *testing.T) {
	got := notIn([]string{"b", "a", "b", "c"}, []string{"c"})
	if strings.Join(got, ",") != "b,a" {
		t.Fatalf("应保持顺序并去重: %v", got)
	}
	if notIn([]string{"a"}, []string{"a"}) != nil {
		t.Fatal("完全覆盖时应返回 nil")
	}
}
