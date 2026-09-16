package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"my-eino-app/internal/config"
	"my-eino-app/internal/skill"
)

func TestSkillCatalogQueryScope(t *testing.T) {
	for _, query := range []string{"目前都有什么技能", "你有哪些技能？", "请列出所有技能", "当前可用的技能列表", "list skills"} {
		if !isSkillCatalogQuery(query) {
			t.Errorf("missed %q", query)
		}
	}
	for _, query := range []string{"用visualize技能生成页面", "有哪些技能可以生成报表？请生成一个", "读取文件并查看技能", "解释技能列表接口代码"} {
		if isSkillCatalogQuery(query) {
			t.Errorf("intercepted task %q", query)
		}
	}
}

func TestSkillCatalogBypassesSelectedSkillAndModel(t *testing.T) {
	s := newTestService(t, nil)
	s.locks = make(map[string]chan struct{})
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "visualize"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "visualize", "SKILL.md"), []byte("---\ndescription: 创建可视化\n---\n输出 <div>UNRELATED_HTML</div>"), 0600); err != nil {
		t.Fatal(err)
	}
	s.skills = skill.NewLoader(dir)
	// 目录短路与技能名暴露只在 debug 模式生效，这里显式打开。
	s.cfg = &config.Config{Debug: true}
	s.skills.ExposeNames = true
	// model/cfg 均为 nil；误入模型路径会使测试失败。
	var out bytes.Buffer
	result, err := s.ChatWithSink(context.Background(), "catalog-test", "目前都有什么技能", "visualize", &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "visualize") || !strings.Contains(out.String(), "创建可视化") || strings.Contains(out.String(), "UNRELATED_HTML") {
		t.Fatal(out.String())
	}
	history, err := s.sessions.Load("catalog-test")
	if err != nil || len(history) != 2 {
		t.Fatalf("history=%v err=%v", history, err)
	}
	if history[1].Content != out.String() || result.Answer != out.String() {
		t.Fatal("response and history differ")
	}
}

// 技能目录与按名指定技能都属于内部实现，只有调试模式才对外暴露。
func TestSkillExposureIsDebugOnly(t *testing.T) {
	s := newTestService(t, nil)
	plain := httptest.NewServer(s.Handler())
	defer plain.Close()
	resp, err := plain.Client().Get(plain.URL + "/skills")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("非调试模式 /skills 应为 404，实际 %d", resp.StatusCode)
	}
	if got := s.requestedSkill("visualize"); got != "" {
		t.Fatalf("非调试模式应忽略按名指定技能，实际 %q", got)
	}

	s.cfg = &config.Config{Debug: true}
	debug := httptest.NewServer(s.Handler())
	defer debug.Close()
	resp, err = debug.Client().Get(debug.URL + "/skills")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("调试模式 /skills 应为 200，实际 %d", resp.StatusCode)
	}
	if got := s.requestedSkill("visualize"); got != "visualize" {
		t.Fatalf("调试模式应保留按名指定技能，实际 %q", got)
	}
}
