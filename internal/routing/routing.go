// Package routing contains the deterministic intent decisions shared by the
// HTTP service and the production evaluation command.
package routing

import "strings"

const (
	SourceServer = "server_rule"
	SourceModel  = "model_autonomous"

	ReasonNoMatch              = "no_deterministic_match"
	ReasonKnowledgePreflight   = "knowledge_preflight"
	ReasonDocumentRead         = "document_read_request"
	ReasonReportWrite          = "report_write_request"
	ReasonUnsupportedPDF       = "unsupported_pdf_operation"
	ReasonUnsupportedWorkbook  = "unsupported_workbook_operation"
	ReasonUnsupportedSlides    = "unsupported_presentation_operation"
	ReasonUnsupportedVisualize = "unsupported_visualization_output"
)

// Decision describes the server-side work that must happen before the model
// is allowed to answer a request. Source and Reason are stable diagnostic
// labels; they are not shown to users.
type Decision struct {
	PreloadKnowledge bool
	PreloadSkills    []string
	CapabilityNotice string
	Source           string
	Reason           string
}

// Decide applies conservative rules to requests that need a predictable
// server-side route. It only forces private-knowledge retrieval, connected
// document/report skills, or an honest response for unsupported file output.
// Everything else remains available to the model's normal skill selection.
func Decide(query string) Decision {
	q := strings.ToLower(strings.TrimSpace(query))
	decision := Decision{Source: SourceModel, Reason: ReasonNoMatch}
	if q == "" {
		return decision
	}

	if isKnowledgeRequest(q) {
		decision.PreloadKnowledge = true
		decision.Source = SourceServer
		decision.Reason = ReasonKnowledgePreflight
	}

	if notice, reason := unsupportedCapability(q); notice != "" {
		return Decision{
			CapabilityNotice: notice,
			Source:           SourceServer,
			Reason:           reason,
		}
	}

	if isReportWriteRequest(q) {
		decision.PreloadSkills = append(decision.PreloadSkills, "report_writer")
		decision.Source = SourceServer
		decision.Reason = appendReason(decision.Reason, ReasonReportWrite)
	}
	if isDocumentReadRequest(q) {
		decision.PreloadSkills = append(decision.PreloadSkills, "documents")
		decision.Source = SourceServer
		decision.Reason = appendReason(decision.Reason, ReasonDocumentRead)
	}
	if isDocumentWriteRequest(q) {
		decision.PreloadSkills = append(decision.PreloadSkills, "documents")
		decision.Source = SourceServer
		decision.Reason = appendReason(decision.Reason, "document_write_request")
	}
	if isTemplateWriteRequest(q) {
		decision.PreloadSkills = append(decision.PreloadSkills, "template-creator")
		decision.Source = SourceServer
		decision.Reason = appendReason(decision.Reason, "template_write_request")
	}
	if isSkillWriteRequest(q) {
		decision.PreloadSkills = append(decision.PreloadSkills, "skill-creator")
		decision.Source = SourceServer
		decision.Reason = appendReason(decision.Reason, "skill_write_request")
	}
	if isSkillInstallRequest(q) {
		decision.PreloadSkills = append(decision.PreloadSkills, "skill-installer")
		decision.Source = SourceServer
		decision.Reason = appendReason(decision.Reason, "skill_install_request")
	}

	decision.PreloadSkills = uniqueStrings(decision.PreloadSkills)
	return decision
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func appendReason(current, next string) string {
	if current == "" || current == ReasonNoMatch {
		return next
	}
	return current + "+" + next
}

func isKnowledgeRequest(q string) bool {
	return containsAny(q,
		"知识库", "项目资料", "内部资料", "私有资料", "知识问答",
		"星河系统", "默认管理员", "生产部署", "内部版本", "技术栈", "幸运数字",
	)
}

func isDocumentReadRequest(q string) bool {
	if hasNegatedAction(q, "读取", "读出来", "阅读", "总结", "摘要", "提取", "解析") {
		return false
	}
	if hasHowToOnlyLanguage(q) {
		return false
	}
	object := containsAny(q,
		".docx", ".txt", ".md", "docx", "word", "工单", "文档", "文件", "授权目录", "workspace-files",
	)
	action := containsAny(q,
		"读取", "读出来", "阅读", "总结", "摘要", "提取", "解析", "写了什么", "说了什么", "内容是什么", "read", "summarize",
	)
	return object && action
}

func isDocumentWriteRequest(q string) bool {
	if hasMutationAdviceLanguage(q) || !containsAny(q, "word", "docx", "文档") {
		return false
	}
	return containsAny(q, "修改", "编辑", "替换", "添加", "写入", "保存", "另存", "更新", "批注", "edit", "save")
}

func isTemplateWriteRequest(q string) bool {
	if hasMutationAdviceLanguage(q) || !containsAny(q, "模板", "template") {
		return false
	}
	if !containsAny(q, "创建", "制作", "生成", "保存", "更新", "修改", "设计", "create", "generate", "save") {
		return false
	}
	// A template write needs a concrete reference or a clear persistence
	// request. A conceptual request such as “create a template and explain its
	// structure” should remain an ordinary answer until a source is supplied.
	return containsAny(q, ".docx", ".xlsx", ".xls", ".pptx", ".ppt", ".csv", ".png", ".jpg", ".txt", "docx", "xlsx", "pptx", "参考", "源文件", "文件", "保存", "落盘", "template file")
}

func isSkillWriteRequest(q string) bool {
	if hasMutationAdviceLanguage(q) || !containsAny(q, "技能", "skill", "skill.md") {
		return false
	}
	return containsAny(q, "创建", "新建", "编写", "修改", "更新", "写入", "生成", "create", "write", "update")
}

func isSkillInstallRequest(q string) bool {
	if hasMutationAdviceLanguage(q) || !containsAny(q, "技能", "skill") {
		return false
	}
	return containsAny(q, "安装", "下载", "导入", "install", "download")
}

func isReportWriteRequest(q string) bool {
	if containsAny(q, "演示大纲", "汇报大纲", "幻灯片大纲") && !strings.Contains(q, "报告") {
		return false
	}
	if hasNegatedAction(q, "写一份", "写份", "写一段", "写报告", "撰写", "起草", "生成", "编写", "整理成", "汇总成", "组织一份", "做一份") {
		return false
	}
	if hasHowToOnlyLanguage(q) && !containsAny(q, "帮我写", "请写", "请撰写", "替我写", "生成一份", "整理成一份", "起草一份") {
		return false
	}
	if containsAny(q, "技能适用", "技能适合", "适合哪些工作场景", "通常适合哪些场景") &&
		!containsAny(q, "请写", "帮我写", "替我写", "请撰写", "起草", "生成一份") {
		return false
	}
	if !containsAny(q, "报告", "周报", "月报", "汇报", "report") {
		return false
	}
	if containsAny(q,
		"写一份", "写份", "写一段", "写报告", "写周报", "写月报", "写出", "写一篇",
		"撰写", "起草", "生成", "编写", "整理成", "汇总成", "组织一份", "做一份", "draft", "write",
	) {
		return true
	}
	return containsAny(q, "我要一份", "我需要一份", "请给我一份", "帮我出一份")
}

func unsupportedCapability(q string) (notice, reason string) {
	action := containsAny(q,
		"读取", "打开", "解析", "提取", "生成", "编辑", "修改", "导出", "写入", "保存", "转换", "操作",
		"extract", "generate", "edit", "export",
	)
	if !action {
		return "", ""
	}

	if containsAny(q, "pdf", ".pdf") {
		return "当前生产环境已接入文本/DOCX读取、知识库检索和文本报告生成；PDF 文件工具尚未接入，不能承诺读取、编辑或生成 PDF。可以先提供纯文本内容，或给出接入工具后的实施方案。", ReasonUnsupportedPDF
	}
	if containsAny(q, "excel", "xlsx", ".xls", "工作簿", "表格文件", "excel表格") {
		return "当前生产环境尚未接入工作簿工具，不能承诺读取、编辑或生成 Excel 文件。可以先提供纯文本表格数据，我可以基于这些内容做分析或给出公式建议。", ReasonUnsupportedWorkbook
	}
	if containsAny(q, "ppt", "演示文稿文件", "幻灯片文件", "导出演示文稿") {
		return "当前生产环境尚未接入演示文稿文件工具，不能承诺读取、编辑或生成 PPT 文件。可以先提供文字内容，我可以帮助组织大纲和叙事结构。", ReasonUnsupportedSlides
	}
	if containsAny(q, "生成图表", "渲染图表", "输出图表文件", "生成可视化页面", "html可视化", "可视化页面", "图表文件") &&
		containsAny(q, "生成", "渲染", "输出", "导出", "创建") {
		return "当前可以讨论图表设计与数据表达，但未接入可视化渲染工具，不能直接生成图表文件或 HTML 页面。", ReasonUnsupportedVisualize
	}
	return "", ""
}

func hasHowToOnlyLanguage(q string) bool {
	return containsAny(q, "怎么", "如何", "方案", "设计思路", "实现思路", "原理", "技能怎么", "能力介绍")
}

func hasMutationAdviceLanguage(q string) bool {
	if !containsAny(q, "怎么", "如何", "方案", "流程", "介绍", "说明", "区别", "什么", "哪些", "通常", "适合", "是什么", "能不能", "可以吗", "设计思路", "实现思路", "原理") {
		return false
	}
	return !hasDirectMutationPrefix(q)
}

func hasDirectMutationPrefix(q string) bool {
	for _, prefix := range []string{"请", "帮我", "请帮我", "替我", "直接", "please", "help me"} {
		if !strings.HasPrefix(q, prefix) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(q, prefix))
		runes := []rune(rest)
		if len(runes) > 8 {
			runes = runes[:8]
		}
		if len(runes) > 0 && containsAny(string(runes), "修改", "编辑", "替换", "添加", "写入", "保存", "创建", "新建", "编写", "生成", "制作", "设计", "安装", "下载", "导入", "create", "write", "edit", "save", "install", "download") {
			return true
		}
	}
	return (strings.HasPrefix(q, "根据") || strings.HasPrefix(q, "从")) && containsAny(q, "修改", "编辑", "创建", "生成", "制作", "安装", "下载", "导入")
}

func hasNegatedAction(q string, actions ...string) bool {
	for _, action := range actions {
		for _, prefix := range []string{"不要", "别", "不必", "无需", "不用", "不需要", "不想"} {
			if strings.Contains(q, prefix+action) {
				return true
			}
		}
	}
	return false
}

func containsAny(value string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}
