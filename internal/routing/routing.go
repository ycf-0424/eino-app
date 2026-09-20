// Package routing contains the deterministic intent decisions shared by the
// HTTP service and the production evaluation command.
package routing

import "strings"

// Decision describes the server-side work that must happen before the model
// is allowed to answer a request.
type Decision struct {
	PreloadKnowledge bool
	PreloadSkills    []string
	CapabilityNotice string
}

// Decide applies the same conservative intent rules used in production. It
// only forces private-knowledge retrieval, connected document/report skills,
// or an honest response for unsupported file capabilities. Other questions
// remain model-routed.
func Decide(query string) Decision {
	q := strings.ToLower(strings.TrimSpace(query))
	decision := Decision{}
	if strings.Contains(q, "知识库") || strings.Contains(q, "项目资料") || strings.Contains(q, "内部资料") || strings.Contains(q, "私有资料") || strings.Contains(q, "知识问答") || strings.Contains(q, "星河系统") || strings.Contains(q, "默认管理员") || strings.Contains(q, "生产部署") || strings.Contains(q, "内部版本") || strings.Contains(q, "技术栈") || strings.Contains(q, "幸运数字") {
		decision.PreloadKnowledge = true
	}
	if strings.Contains(q, "报告") || strings.Contains(q, "汇报") || strings.Contains(q, "report") {
		decision.PreloadSkills = append(decision.PreloadSkills, "report_writer")
	}
	documentObject := strings.Contains(q, "docx") || strings.Contains(q, "word") || strings.Contains(q, "工单") || strings.Contains(q, "workspace-files") || strings.Contains(q, "文档") || strings.Contains(q, "文件") || strings.Contains(q, "授权目录")
	documentAction := strings.Contains(q, "读取") || strings.Contains(q, "读出来") || strings.Contains(q, "阅读") || strings.Contains(q, "总结") || strings.Contains(q, "摘要") || strings.Contains(q, "正文") || strings.Contains(q, "写了什么")
	if documentObject && documentAction {
		decision.PreloadSkills = append(decision.PreloadSkills, "documents")
	}
	if (strings.Contains(q, "pdf") || strings.Contains(q, "pdf文件") || strings.Contains(q, "表格") || strings.Contains(q, "excel") || strings.Contains(q, "xlsx") || strings.Contains(q, "ppt") || strings.Contains(q, "演示文稿")) && (strings.Contains(q, "读取") || strings.Contains(q, "解析") || strings.Contains(q, "生成") || strings.Contains(q, "编辑") || strings.Contains(q, "提取") || strings.Contains(q, "导出") || strings.Contains(q, "分析") || strings.Contains(q, "整理") || strings.Contains(q, "组织") || strings.Contains(q, "操作")) {
		decision.CapabilityNotice = "当前生产环境已接入文本/DOCX读取、知识库检索和文本报告生成；PDF、表格与演示文稿文件工具尚未接入，不能承诺读取、编辑或生成这类文件。可以先提供纯文本内容，或给出接入工具后的实施方案。"
	}
	if strings.Contains(q, "图表") || strings.Contains(q, "可视化") {
		decision.CapabilityNotice = "可以根据流程或方案给出图表结构设计；当前未接入可视化渲染工具，不能直接生成图表文件或 HTML 页面。"
	}
	return decision
}
