package prompt

import (
	"context"
	"testing"
)

func TestFormatRAG(t *testing.T) {
	messages, err := FormatRAG(context.Background(), "幸运数字是 7319", "doc.md", "幸运数字是什么？")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Content == "" {
		t.Fatalf("unexpected rendered messages: %+v", messages)
	}
	if !contains(messages[1].Content, "7319") || !contains(messages[1].Content, "doc.md") {
		t.Fatalf("template variables were not rendered: %s", messages[1].Content)
	}
}

func TestSystemInstructionsRequireRealFileReadEvidence(t *testing.T) {
	for _, rule := range []string{
		"<untrusted_local_file_evidence>",
		"若没有该证据且 local_file_read 工具已注册，必须实际调用该工具",
		"工具会强制执行管理员配置的授权目录和文件类型限制",
		"本轮服务端预检索证据或 local_file_read 成功返回的正文",
		"不得仅凭技能说明、目录摘要或模型常识声称已读取",
		"不等于要保存文件或笔记",
		"闲聊、一般解释、讨论技能适用场景或列举助手能做什么时，不得试探性调用工具",
	} {
		if !contains(SystemInstruction+WriterAgentInstruction, rule) {
			t.Errorf("system and writer instructions should include evidence rule %q", rule)
		}
	}
}

func contains(text, part string) bool {
	for i := 0; i+len(part) <= len(text); i++ {
		if text[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
