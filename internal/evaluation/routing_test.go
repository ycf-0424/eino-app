package evaluation

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRouter 按题目文本返回预置的路由结果，并记录被调用的题目。
type fakeRouter struct {
	results map[string]Routing
	errs    map[string]error
	calls   []string
}

func (f *fakeRouter) Route(_ context.Context, question string) (Routing, error) {
	f.calls = append(f.calls, question)
	if err, ok := f.errs[question]; ok {
		return Routing{}, err
	}
	return f.results[question], nil
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

// 没有可计分的题时准确率为 0，且不产生除零。
func TestRunRoutingWithoutScorableCases(t *testing.T) {
	results, accuracy := RunRouting(context.Background(), &fakeRouter{}, []Case{{Question: "q"}})
	if len(results) != 0 || accuracy != 0 {
		t.Fatalf("无可计分题时 results=%v accuracy=%v", results, accuracy)
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
