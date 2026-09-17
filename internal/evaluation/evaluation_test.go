package evaluation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cases.json")
	if err := os.WriteFile(path, []byte(`[{"question":"q","expected":"a"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases, err := LoadCases(path)
	if err != nil || len(cases) != 1 || cases[0].Question != "q" {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
}

func TestNormalizeIgnoresFormatting(t *testing.T) {
	if normalize("Go、Eino 和 Milvus") != normalize("Go，Eino、Milvus") {
		t.Fatal("format normalization mismatch")
	}
}

// 判定用的是 strings.Contains(answer, expected)，而 Contains(x, "") **恒为真**。
// 执行类题目（写报告、读文件、设计模板）只声明 expect_skills、不声明 expected，
// 于是它们的 Correct 永远是 true —— 哪怕模型只回一句「请提供文件路径」。
// 这类题必须排除出正确率的分母，否则分母永远不可能失败。
func TestCaseAssertedRequiresAJudgementCriterion(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    Case
		want bool
	}{
		{"只有 expect_skills 不算断言", Case{Question: "写份报告", ExpectSkills: []string{"report_writer"}}, false},
		{"空 expected 不算断言", Case{Question: "q"}, false},
		{"全空白 expected 不算断言", Case{Question: "q", Expected: "   "}, false},
		{"有 expected 算断言", Case{Question: "q", Expected: "7319"}, true},
		{"should_refuse 算断言", Case{Question: "q", ShouldRefuse: true}, true},
	} {
		if got := tc.c.Asserted(); got != tc.want {
			t.Errorf("%s: Asserted()=%v，期望 %v", tc.name, got, tc.want)
		}
	}
}
