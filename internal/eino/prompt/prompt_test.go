package prompt

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		"技术栈、架构、版本、部署方式、配置",
		"不得把技能目录或工具清单当作项目技术栈",
	} {
		if !contains(SystemInstruction+WriterAgentInstruction, rule) {
			t.Errorf("system and writer instructions should include evidence rule %q", rule)
		}
	}
}

func TestRenderRoleV2IsDeterministicAndScopedToTools(t *testing.T) {
	first, version, hash := RenderRole(RoleSupervisor, ProfileV2, "技能规则", []string{"knowledge_search", "write_note"}, false)
	second, version2, hash2 := RenderRole(RoleSupervisor, ProfileV2, "技能规则", []string{"write_note", "knowledge_search"}, false)
	if first != second || version != version2 || hash != hash2 {
		t.Fatalf("prompt rendering is not deterministic: %q/%q %q/%q", version, version2, hash, hash2)
	}
	for _, want := range []string{"<tool_guidance>", "knowledge_search", "write_note", "<skill_context>", "<role_policy>"} {
		if !contains(first, want) {
			t.Errorf("v2 supervisor prompt missing %q: %s", want, first)
		}
	}
	if version != PromptVersionV2 || len(hash) != 64 {
		t.Fatalf("unexpected prompt metadata: version=%q hash=%q", version, hash)
	}
}

func TestRuntimeContextBoundaries(t *testing.T) {
	if got := WrapRuntimeContext("<knowledge_results>facts</knowledge_results>"); !contains(got, "<runtime_context>") || !contains(got, "<knowledge_results>") {
		t.Fatalf("runtime context was not wrapped: %s", got)
	}
	if got := WrapMemoryContext("project fact"); !contains(got, "<memory_context>") {
		t.Fatalf("memory context was not wrapped: %s", got)
	}
}

func TestRenderContextBlocksPreservesOrderAndHashes(t *testing.T) {
	snapshot := RenderContextBlocks(
		ContextBlock{Name: "memory_context", Order: 200, Text: WrapMemoryContext("memory")},
		ContextBlock{Name: "runtime_context", Order: 100, Text: WrapRuntimeContext("runtime")},
	)
	if !strings.HasPrefix(snapshot.Text, "<runtime_context>") || !contains(snapshot.Text, "<memory_context>") {
		t.Fatalf("context order or boundaries wrong: %s", snapshot.Text)
	}
	if len(snapshot.Hash) != 64 || snapshot.Hash != HashText(snapshot.Text) {
		t.Fatalf("context hash mismatch: %q", snapshot.Hash)
	}
}

func TestToolGuidanceIsScopedAndMCPCanRegister(t *testing.T) {
	RegisterToolGuidance(ToolGuidance{Name: "mcp__demo__lookup", WhenUse: "仅查询演示 MCP 资料。", Evidence: "保留 MCP 来源。"})
	text, _, _ := RenderRole(RoleSupervisor, ProfileV2, "", []string{"mcp__demo__lookup"}, false)
	for _, want := range []string{"mcp__demo__lookup", "保留 MCP 来源"} {
		if !contains(text, want) {
			t.Fatalf("registered guidance missing %q: %s", want, text)
		}
	}
}

func TestV2PromptGoldenSnapshots(t *testing.T) {
	roles := []Role{RoleSupervisor, RoleKnowledge, RoleWriter}
	for _, role := range roles {
		got, version, hash := RenderRole(role, ProfileV2, "示例技能规则", []string{"knowledge_search", "local_file_read", "current_time", "write_note"}, false)
		path := filepath.Join("testdata", string(role)+".golden.md")
		wantBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		want := string(wantBytes)
		want = strings.ReplaceAll(want, "\r\n", "\n")
		if !contains(want, "version="+version) || !contains(want, "hash="+hash) {
			t.Fatalf("snapshot metadata mismatch for %s", role)
		}
		lines := strings.Split(want, "\n")
		if len(lines) < 3 {
			t.Fatalf("invalid snapshot for %s", role)
		}
		body := strings.Join(lines[2:], "\n")
		body = strings.TrimSuffix(body, "\n")
		if body != got+"\n" {
			t.Fatalf("snapshot mismatch for %s: want chars=%d got chars=%d", role, len(body), len(got))
		}
	}
}

func TestRAGGoldenSnapshot(t *testing.T) {
	messages, err := FormatRAG(context.Background(), "幸运数字是 7319", "milvus-validation.md", "幸运数字是什么？")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "rag.golden.md"))
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimPrefix(want, []byte{0xef, 0xbb, 0xbf})
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	var rendered strings.Builder
	for i, message := range messages {
		rendered.WriteString("--- message ")
		rendered.WriteString(strconv.Itoa(i))
		rendered.WriteString(" role=")
		rendered.WriteString(string(message.Role))
		rendered.WriteString(" ---\n")
		rendered.WriteString(message.Content)
		rendered.WriteString("\n")
	}
	if rendered.String() != string(want) {
		t.Fatalf("RAG snapshot mismatch")
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
