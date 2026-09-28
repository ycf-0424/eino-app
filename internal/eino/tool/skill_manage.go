package tool

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

type SkillWriteInput struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Instruction   string   `json:"instruction"`
	Scenarios     []string `json:"scenarios,omitempty"`
	NotFor        []string `json:"not_for,omitempty"`
	RequiredTools []string `json:"required_tools,omitempty"`
	Overwrite     bool     `json:"overwrite,omitempty"`
}

type TemplateWriteInput struct {
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description"`
	Kind          string `json:"kind"`
	ReferencePath string `json:"reference_path"`
	PreviewPath   string `json:"preview_path,omitempty"`
	Overwrite     bool   `json:"overwrite,omitempty"`
}

type SkillInstallInput struct {
	Repo      string `json:"repo"`
	Path      string `json:"path"`
	Ref       string `json:"ref,omitempty"`
	Name      string `json:"name,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

type SkillWriteFunc func(context.Context, SkillWriteInput) (string, error)
type TemplateWriteFunc func(context.Context, TemplateWriteInput) (string, error)
type SkillInstallFunc func(context.Context, SkillInstallInput) (string, error)

func NewSkillWriteTool(fn SkillWriteFunc) einotool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "skill_write",
		Desc: "Create or update a project skill package under the managed skill directory. This changes local skill files and requires approval. Do not use it for ordinary answers.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name":           {Type: schema.String, Required: true, Desc: "Lowercase or portable skill directory name"},
			"description":    {Type: schema.String, Required: true},
			"instruction":    {Type: schema.String, Required: true},
			"scenarios":      {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}},
			"not_for":        {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}},
			"required_tools": {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}},
			"overwrite":      {Type: schema.Boolean, Desc: "Only set true when the user explicitly asks to replace an existing skill"},
		}),
	}
	return ApprovableTool{InvokableTool: utils.NewTool(info, func(ctx context.Context, input SkillWriteInput) (string, error) {
		if fn == nil {
			return "", fmt.Errorf("skill_write is not configured")
		}
		return fn(ctx, input)
	})}
}

func NewTemplateWriteTool(fn TemplateWriteFunc) einotool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "template_write",
		Desc: "Create or update a reusable project template package from one approved local reference file. This changes local files and requires approval.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name":           {Type: schema.String, Required: true, Desc: "Portable template slug, without path separators"},
			"display_name":   {Type: schema.String, Required: true},
			"description":    {Type: schema.String, Required: true},
			"kind":           {Type: schema.String, Required: true, Enum: []string{"document", "spreadsheet", "presentation", "image", "email", "slack"}},
			"reference_path": {Type: schema.String, Required: true, Desc: "Reference file inside an approved local directory"},
			"preview_path":   {Type: schema.String, Desc: "Optional preview image or representative file inside an approved local directory"},
			"overwrite":      {Type: schema.Boolean, Desc: "Only set true when the user explicitly asks to replace an existing template"},
		}),
	}
	return ApprovableTool{InvokableTool: utils.NewTool(info, func(ctx context.Context, input TemplateWriteInput) (string, error) {
		if fn == nil {
			return "", fmt.Errorf("template_write is not configured")
		}
		return fn(ctx, input)
	})}
}

func NewSkillInstallTool(fn SkillInstallFunc) einotool.InvokableTool {
	info := &schema.ToolInfo{
		Name: "skill_install",
		Desc: "Download and install one public GitHub skill directory at a pinned ref into the managed skill directory. Network access is allowlisted, package files are validated, and installation requires approval.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"repo":      {Type: schema.String, Required: true, Desc: "Public GitHub repository in owner/repository form"},
			"path":      {Type: schema.String, Required: true, Desc: "Skill directory path in the repository, for example skills/example"},
			"ref":       {Type: schema.String, Desc: "Pinned branch, tag, or commit; defaults to main"},
			"name":      {Type: schema.String, Desc: "Optional managed skill name; defaults to the source directory name"},
			"overwrite": {Type: schema.Boolean, Desc: "Only set true when the user explicitly asks to replace an existing skill"},
		}),
	}
	return ApprovableTool{InvokableTool: utils.NewTool(info, func(ctx context.Context, input SkillInstallInput) (string, error) {
		if fn == nil {
			return "", fmt.Errorf("skill_install is not configured")
		}
		return fn(ctx, input)
	})}
}

var (
	documentMutationIntent = regexp.MustCompile(`(?i)(?:修改|编辑|替换|添加|写入|保存|另存|更新|批注).{0,24}(?:word|docx|文档)|(?:word|docx|文档).{0,24}(?:修改|编辑|替换|添加|写入|保存|另存|更新|批注)`)
	templateMutationIntent = regexp.MustCompile(`(?i)(?:创建|制作|生成|保存|更新|修改|设计).{0,20}模板|模板.{0,20}(?:创建|制作|生成|保存|更新|修改|落盘)`)
	skillWriteIntent       = regexp.MustCompile(`(?i)(?:创建|新建|编写|修改|更新|写入|生成).{0,20}(?:技能|skill|SKILL\.md)|(?:技能|skill|SKILL\.md).{0,20}(?:创建|新建|编写|修改|更新|写入|生成)`)
	skillInstallIntent     = regexp.MustCompile(`(?i)(?:安装|下载|导入|install).{0,20}(?:技能|skill)|(?:技能|skill).{0,20}(?:安装|下载|导入|install)`)
	directMutationIntent   = regexp.MustCompile(`(?i)^(?:请|帮我|请帮我|替我|直接|please|help me)\s*(?:修改|编辑|替换|添加|写入|保存|另存|更新|批注|创建|新建|编写|生成|制作|设计|安装|下载|导入|create|write|update|edit|save|install|download)|^(?:根据|从)\s*.{0,40}(?:修改|编辑|替换|添加|写入|保存|另存|更新|批注|创建|新建|编写|生成|制作|设计|安装|下载|导入|create|write|update|edit|save|install|download)`)
)

// MutationToolRequested is a conservative gate used to keep side-effect
// tools out of ordinary chats. Approval remains mandatory even when a query
// passes this intent gate.
func MutationToolRequested(query, toolName string) bool {
	query = strings.TrimSpace(query)
	if query == "" || mutationHowToOnly(query) && !directMutationIntent.MatchString(query) {
		return false
	}
	switch toolName {
	case "document_write":
		return documentMutationIntent.MatchString(query)
	case "template_write":
		return templateMutationIntent.MatchString(query)
	case "skill_write":
		return skillWriteIntent.MatchString(query)
	case "skill_install":
		return skillInstallIntent.MatchString(query)
	default:
		return false
	}
}

func mutationHowToOnly(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	for _, marker := range []string{"怎么", "如何", "方案", "流程", "原理", "介绍", "说明", "能不能", "可以吗", "how to", "how do i", "what is"} {
		if strings.Contains(q, marker) {
			return true
		}
	}
	return false
}
