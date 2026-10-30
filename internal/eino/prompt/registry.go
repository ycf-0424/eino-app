package prompt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	einotool "github.com/cloudwego/eino/components/tool"
)

// Prompt profiles are intentionally small and deployment friendly. legacy
// preserves the pre-registry assembly, while v2 enables ordered sections,
// tool guidance, and explicit context boundaries.
const (
	ProfileLegacy       = "legacy"
	ProfileV2           = "v2"
	ProfileV2Shadow     = "v2-shadow"
	PromptVersionLegacy = "agent-prompt-v1"
	PromptVersionV2     = "agent-prompt-v2"
)

type Role string

const (
	RoleSupervisor Role = "supervisor"
	RoleKnowledge  Role = "knowledge-agent"
	RoleWriter     Role = "writer-agent"
)

// Section is one ordered, auditable prompt contribution.
type Section struct {
	Name  string
	Order int
	Text  string
}

// Document is the model-facing prompt before rendering.
type Document struct {
	Role     Role
	Profile  string
	Version  string
	Sections []Section
	Contexts []ContextBlock
}

// ContextBlock is a dynamic, model-visible context contribution. Its content
// is kept separate from fixed policy sections and can be hashed independently.
type ContextBlock struct {
	Name     string
	Order    int
	Required bool
	Text     string
}

type ContextSnapshot struct {
	Blocks []ContextBlock
	Text   string
	Hash   string
}

// RenderContextBlocks deterministically renders dynamic blocks and returns a
// hash suitable for execution audit. Empty optional blocks are omitted.
func RenderContextBlocks(blocks ...ContextBlock) ContextSnapshot {
	ordered := append([]ContextBlock(nil), blocks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Order != ordered[j].Order {
			return ordered[i].Order < ordered[j].Order
		}
		return ordered[i].Name < ordered[j].Name
	})
	parts := make([]string, 0, len(ordered))
	kept := make([]ContextBlock, 0, len(ordered))
	for _, block := range ordered {
		block.Text = strings.TrimSpace(block.Text)
		if block.Text == "" && !block.Required {
			continue
		}
		kept = append(kept, block)
		parts = append(parts, block.Text)
	}
	text := strings.Join(parts, "\n\n")
	return ContextSnapshot{Blocks: kept, Text: text, Hash: HashText(text)}
}

// ToolGuidance describes when a model may use a tool. Enforcement remains in
// Go middleware; this text only makes the policy explicit to the model.
type ToolGuidance struct {
	Name       string
	WhenUse    string
	NeverUse   string
	Evidence   string
	Permission string
}

var (
	toolGuidanceMu sync.RWMutex
	toolGuidance   = map[string]ToolGuidance{
		"knowledge_search": {
			Name: "knowledge_search", WhenUse: "涉及项目、内部文档、配置、版本、架构或历史决策时先调用。",
			NeverUse: "没有私有事实问题或服务端已经完成本轮预检索时不要重复调用。",
			Evidence: "保留返回的来源、片段和分数；低于阈值的结果不能写成确定事实。",
		},
		"local_file_read": {
			Name: "local_file_read", WhenUse: "用户明确指定文件或路径并要求读取、总结或分析时调用。",
			NeverUse: "不得扫描目录、猜测路径或读取用户未指定的其他文件。",
			Evidence: "只有工具成功返回正文后才能声称已经读取。",
		},
		"current_time": {
			Name: "current_time", WhenUse: "用户询问当前时间、日期或时区时调用。",
			NeverUse: "不要使用模型记忆猜测当前时间。",
		},
		"write_note": {
			Name: "write_note", WhenUse: "只有用户明确要求保存为文件或笔记时调用。",
			NeverUse:   "回答问题、生成草稿或自动记录长期记忆时不要调用。",
			Permission: "调用前必须经过现有审批流程；审批失败或拒绝时如实说明。",
		},
	}
)

// RegisterToolGuidance lets optional capabilities such as MCP contribute
// model-facing usage rules without changing the core prompt assembly. The
// actual tool permission and execution policy remains outside the prompt.
func RegisterToolGuidance(guidance ToolGuidance) {
	name := strings.TrimSpace(guidance.Name)
	if name == "" {
		return
	}
	guidance.Name = name
	toolGuidanceMu.Lock()
	toolGuidance[name] = guidance
	toolGuidanceMu.Unlock()
}

// ResolveProfile normalizes configuration and keeps empty values compatible.
func ResolveProfile(profile string) string {
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == ProfileLegacy {
		return ProfileLegacy
	}
	if profile == ProfileV2Shadow {
		return ProfileV2Shadow
	}
	return ProfileV2
}

func VersionForProfile(profile string) string {
	if ResolveProfile(profile) == ProfileLegacy {
		return PromptVersionLegacy
	}
	return PromptVersionV2
}

// Build creates a deterministic prompt document for one role and tool set.
func Build(role Role, profile, skill string, toolNames []string, automatic bool) Document {
	profile = ResolveProfile(profile)
	version := VersionForProfile(profile)
	roleText := map[Role]string{
		RoleSupervisor: SystemInstruction,
		RoleKnowledge:  KnowledgeAgentInstruction,
		RoleWriter:     WriterAgentInstruction,
	}[role]
	sections := []Section{{Name: "base", Order: 100, Text: roleText}}
	if profile == ProfileLegacy {
		if strings.TrimSpace(skill) != "" {
			sections = append(sections, Section{Name: "skill", Order: 400, Text: wrapSkill(skill)})
		} else {
			sections = append(sections, Section{Name: "skill", Order: 400, Text: wrapSkill("")})
		}
		if automatic && role == RoleSupervisor {
			sections = append(sections, Section{Name: "model-routing", Order: 500, Text: automaticRoutingText})
		}
		return Document{Role: role, Profile: profile, Version: version, Sections: sections}
	}

	if guidance := renderToolGuidance(toolNames); guidance != "" {
		sections = append(sections, Section{Name: "tool-guidance", Order: 200, Text: guidance})
	}
	sections = append(sections, Section{Name: "role", Order: 300, Text: renderRoleBoundary(role)})
	sections = append(sections, Section{Name: "skill", Order: 400, Text: wrapSkill(skill)})
	if automatic && role == RoleSupervisor {
		sections = append(sections, Section{Name: "model-routing", Order: 500, Text: automaticRoutingText})
	}
	return Document{Role: role, Profile: profile, Version: version, Sections: sections}
}

const automaticRoutingText = `<model_routing>
服务端已经完成本轮难度分类并绑定最终模型。不要再次选择模型，也不要为了形式而重复委派。
简单问答直接回答；涉及私有资料先委派 knowledge-agent；复杂分析、长文组织、方案比较或取得检索结果后需要成稿时，才委派 writer-agent 输出最终答案。
</model_routing>`

func renderRoleBoundary(role Role) string {
	switch role {
	case RoleKnowledge:
		return `<role_policy>
你只负责检索和整理证据，不负责编造最终答案，也不负责写入文件。
输出应包含查询意图、事实、来源、证据强度、冲突和缺失信息。
</role_policy>`
	case RoleWriter:
		return `<role_policy>
你只负责根据已确认事实组织中文答案。先给结论，再给依据、步骤、限制和来源；未经证实的内容必须标记为无法确认。
</role_policy>`
	default:
		return `<role_policy>
你负责判断任务类型、选择必要工具或子 Agent，并确保最终回答有证据边界。
</role_policy>`
	}
}

func renderToolGuidance(names []string) string {
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[strings.TrimSpace(name)] = true
	}
	toolGuidanceMu.RLock()
	var selected []ToolGuidance
	for name, guidance := range toolGuidance {
		if known[name] {
			selected = append(selected, guidance)
		}
	}
	toolGuidanceMu.RUnlock()
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	if len(selected) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<tool_guidance>\n")
	for _, item := range selected {
		b.WriteString("<tool name=\"")
		b.WriteString(item.Name)
		b.WriteString("\">\n")
		if item.WhenUse != "" {
			b.WriteString("使用：" + item.WhenUse + "\n")
		}
		if item.NeverUse != "" {
			b.WriteString("禁止：" + item.NeverUse + "\n")
		}
		if item.Evidence != "" {
			b.WriteString("证据：" + item.Evidence + "\n")
		}
		if item.Permission != "" {
			b.WriteString("权限：" + item.Permission + "\n")
		}
		b.WriteString("</tool>\n")
	}
	b.WriteString("</tool_guidance>")
	return b.String()
}

func wrapSkill(skill string) string {
	skill = strings.TrimSpace(skill)
	if skill == "" {
		skill = "无额外 Skill 规则。"
	}
	return "<skill_context>\n" + skill + "\n</skill_context>"
}

// Render sorts sections and returns the exact model text and a stable hash.
func Render(doc Document) (string, string) {
	sections := append([]Section(nil), doc.Sections...)
	sort.SliceStable(sections, func(i, j int) bool {
		if sections[i].Order != sections[j].Order {
			return sections[i].Order < sections[j].Order
		}
		return sections[i].Name < sections[j].Name
	})
	parts := make([]string, 0, len(sections))
	for _, section := range sections {
		if text := strings.TrimSpace(section.Text); text != "" {
			parts = append(parts, text)
		}
	}
	text := strings.Join(parts, "\n\n")
	return text, HashText(text)
}

// HashText returns a stable SHA-256 identity for model-visible text.
func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// RenderRole is the main integration point used by the Agent constructor.
func RenderRole(role Role, profile, skill string, toolNames []string, automatic bool) (text, version, hash string) {
	doc := Build(role, profile, skill, toolNames, automatic)
	text, hash = Render(doc)
	return text, doc.Version, hash
}

// ToolSetHash fingerprints the exact tool schemas visible to one agent.
func ToolSetHash(ctx context.Context, tools []einotool.BaseTool) string {
	var parts []string
	for _, tool := range tools {
		if info, err := tool.Info(ctx); err == nil && info != nil {
			encoded, err := json.Marshal(info)
			if err == nil {
				parts = append(parts, string(encoded))
			}
		}
	}
	sort.Strings(parts)
	return HashText(strings.Join(parts, "\n"))
}

// WrapRuntimeContext keeps dynamic facts separate from fixed instructions.
func WrapRuntimeContext(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "<runtime_context>\n" + text + "\n</runtime_context>"
}

func WrapMemoryContext(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "<memory_context>\n" + text + "\n</memory_context>"
}

func WrapKnowledgeContext(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "<knowledge_context>\n" + text + "\n</knowledge_context>"
}

func WrapUserQuery(text string) string {
	return "<user_query>\n" + strings.TrimSpace(text) + "\n</user_query>"
}
