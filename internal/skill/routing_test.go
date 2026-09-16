package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutingMetadataAndActualToolAvailability(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "demo"), 0755); err != nil {
		t.Fatal(err)
	}
	data := "---\ndescription: 问答\nscenarios: [查询资料]\nnot_for: [无依据推测]\nrequired_tools: [knowledge_search]\n---\nPRIVATE_BODY_SENTINEL"
	if err := os.WriteFile(filepath.Join(dir, "demo", "SKILL.md"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	l := NewLoader(dir)
	s, err := l.Load("demo")
	if err != nil || len(s.Scenarios) != 1 || len(s.RequiredTools) != 1 {
		t.Fatalf("%+v %v", s, err)
	}
	for _, tc := range []struct {
		tools []string
		want  string
	}{
		{[]string{"local_file_read"}, "缺少工具 knowledge_search"},
		{[]string{"knowledge_search"}, "声明的工具已接入"},
	} {
		prompt, _, preloaded, err := l.Runtime("", tc.tools...)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prompt, tc.want) || !strings.Contains(prompt, "查询资料") || !strings.Contains(prompt, "无依据推测") {
			t.Fatal(prompt)
		}
		if strings.Contains(prompt, "PRIVATE_BODY_SENTINEL") || len(preloaded) != 0 {
			t.Fatal("catalog leaked skill body")
		}
	}
}

// 技能名属于内部实现：默认提示词必须禁止向用户暴露技能名称，
// 但技能目录本身仍要进入提示词，否则模型无法选择技能。
func TestRuntimeHidesSkillNamesUnlessExposed(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "demo"), 0755); err != nil {
		t.Fatal(err)
	}
	data := "---\ndescription: 问答\nscenarios: [查询资料]\n---\nPRIVATE_BODY_SENTINEL"
	if err := os.WriteFile(filepath.Join(dir, "demo", "SKILL.md"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	l := NewLoader(dir)

	hidden, _, _, err := l.Runtime("", "knowledge_search")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hidden, "不要在回答中列出、复述或提及任何技能名称") {
		t.Fatalf("默认应禁止暴露技能名：%s", hidden)
	}
	if strings.Contains(hidden, "技能推荐：根据用户的任务目标") {
		t.Fatalf("默认不应保留推荐技能名的话术：%s", hidden)
	}
	if !strings.Contains(hidden, "demo") || !strings.Contains(hidden, "查询资料") {
		t.Fatalf("技能目录仍需进入提示词：%s", hidden)
	}

	l.ExposeNames = true
	shown, _, _, err := l.Runtime("", "knowledge_search")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shown, "技能推荐：根据用户的任务目标") || strings.Contains(shown, "不要在回答中列出、复述或提及任何技能名称") {
		t.Fatalf("debug 模式应保留介绍技能的行为：%s", shown)
	}
}
