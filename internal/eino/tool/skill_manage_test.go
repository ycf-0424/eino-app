package tool

import "testing"

func TestMutationToolRequested(t *testing.T) {
	tests := []struct {
		query string
		name  string
		want  bool
	}{
		{"请修改这个 Word 文档并另存为新文件", "document_write", true},
		{"请告诉我 Word 文档应该怎么修改", "document_write", false},
		{"根据这个 docx 制作一个可复用模板", "template_write", true},
		{"模板创建方案怎么设计", "template_write", false},
		{"创建一个处理周报的新技能", "skill_write", true},
		{"请创建一个新技能并说明规则", "skill_write", true},
		{"技能怎么创建", "skill_write", false},
		{"从 openai/skills 安装 skills/example 技能", "skill_install", true},
		{"介绍一下技能安装流程", "skill_install", false},
	}
	for _, test := range tests {
		t.Run(test.name+"/"+test.query, func(t *testing.T) {
			if got := MutationToolRequested(test.query, test.name); got != test.want {
				t.Fatalf("MutationToolRequested(%q, %q)=%v, want %v", test.query, test.name, got, test.want)
			}
		})
	}
}
