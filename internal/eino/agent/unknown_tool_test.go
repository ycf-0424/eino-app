package agent

import (
	"context"
	"strings"
	"testing"

	"my-eino-app/internal/eino"

	toolset "my-eino-app/internal/eino/tool"
)

// 这组测试锁定「模型调错工具名时不能整轮失败」这一行为契约。
//
// 背景（2026-09-17 评测实测）：技能目录里的 `- report_writer: 一句话描述` 与工具定义
// 排版太像，模型会把技能名当工具调用。框架默认返回
// `NodeRunError: tool report_writer not found in toolsNode indexes`，整轮以
// answer_chars=0 结束 —— 模型连改正的机会都没有。
// unknownToolHint 把它降级为一段交回模型的提示，因此提示里必须同时出现
// 「真实工具名」与「改用 load_skills」两条信息，模型才可能在同一轮里自我纠正。
func TestUnknownToolHintListsRealToolsAndSkillGuidance(t *testing.T) {
	tools := []eino.BaseTool{toolset.NewTimeTool(), toolset.NewWriteNoteTool()}

	handler := unknownToolHint(tools)
	got, err := handler(context.Background(), "report_writer", `{"names":["report_writer"]}`)
	if err != nil {
		t.Fatalf("兜底提示不应返回错误，否则又变回硬失败: %v", err)
	}

	// 1) 必须点出被调用的名字，模型才知道自己在哪一步错了。
	if !strings.Contains(got, "report_writer") {
		t.Errorf("提示里应包含被误调用的名字，实际为: %s", got)
	}
	// 2) 必须给出真实可用的工具名，否则模型只能再猜一次。
	for _, name := range []string{"current_time", "write_note"} {
		if !strings.Contains(got, name) {
			t.Errorf("提示里应列出真实工具 %q，实际为: %s", name, got)
		}
	}
	// 3) 必须明确指向 load_skills —— 这是技能名的正确用法。
	if !strings.Contains(got, "load_skills") {
		t.Errorf("提示里应指引改用 load_skills，实际为: %s", got)
	}
	if !strings.Contains(got, "技能名不是工具名") {
		t.Errorf("提示里应显式区分技能名与工具名，实际为: %s", got)
	}
}

// 无工具注册时不能拼出一个空清单（模型会以为「没有工具可用」而放弃）。
func TestUnknownToolHintWithoutTools(t *testing.T) {
	handler := unknownToolHint(nil)
	got, err := handler(context.Background(), "spreadsheets", "{}")
	if err != nil {
		t.Fatalf("无工具时也不应报错: %v", err)
	}
	if !strings.Contains(got, "没有注册任何工具") {
		t.Errorf("提示里应说明本轮没有工具，实际为: %s", got)
	}
	if !strings.Contains(got, "spreadsheets") {
		t.Errorf("提示里应包含被误调用的名字，实际为: %s", got)
	}
}
