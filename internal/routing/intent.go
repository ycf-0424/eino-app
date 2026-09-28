package routing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
)

// IntentKind is the fixed server-side contract for choosing an evidence source.
type IntentKind string

const (
	IntentGeneral           IntentKind = "general"
	IntentProjectFact       IntentKind = "project_fact"
	IntentRealtimeData      IntentKind = "realtime_data"
	IntentFileUnderstanding IntentKind = "file_understanding"
	IntentExecution         IntentKind = "execution"
)

// IntentPlan contains only values the server can safely derive. In particular,
// a classifier can never populate operation IDs, owners, URLs, SQL, or paths.
type IntentPlan struct {
	Kind               IntentKind `json:"kind"`
	Confidence         float64    `json:"confidence"`
	ReasonCode         string     `json:"reason_code"`
	RequiredSources    []string   `json:"required_sources,omitempty"`
	AttachmentIDs      []string   `json:"attachment_ids,omitempty"`
	Operation          string     `json:"operation,omitempty"`
	RequiresApproval   bool       `json:"requires_approval,omitempty"`
	FreshnessRequired  bool       `json:"freshness_required,omitempty"`
	NeedsClarification bool       `json:"needs_clarification,omitempty"`
	NeedsClassifier    bool       `json:"-"`
}

// IntentClassification is the deliberately narrow model fallback schema.
// Unknown fields are rejected so model output cannot smuggle executable data.
type IntentClassification struct {
	Kind       IntentKind `json:"kind"`
	Confidence float64    `json:"confidence"`
}

// ParseIntentClassification parses exactly one JSON object and rejects extra
// keys, trailing data, unsupported enum values, and invalid confidence scores.
func ParseIntentClassification(raw string) (IntentClassification, error) {
	var result IntentClassification
	decoder := json.NewDecoder(bytes.NewBufferString(strings.TrimSpace(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return IntentClassification{}, fmt.Errorf("invalid intent classifier response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return IntentClassification{}, fmt.Errorf("intent classifier response must contain one JSON object")
		}
		return IntentClassification{}, fmt.Errorf("invalid trailing classifier data: %w", err)
	}
	if !validIntent(result.Kind) {
		return IntentClassification{}, fmt.Errorf("unknown intent kind %q", result.Kind)
	}
	if math.IsNaN(result.Confidence) || math.IsInf(result.Confidence, 0) || result.Confidence < 0 || result.Confidence > 1 {
		return IntentClassification{}, fmt.Errorf("intent confidence must be between 0 and 1")
	}
	return result, nil
}

// WithClassification applies a validated low-confidence fallback. It only
// accepts the enum and confidence; source selection and safety flags are
// derived again by the server, never copied from model output.
func WithClassification(classification IntentClassification, threshold float64, attachmentIDs []string) IntentPlan {
	plan := planFor(classification.Kind, attachmentIDs)
	plan.Confidence = classification.Confidence
	plan.ReasonCode = "classifier_" + string(classification.Kind)
	if classification.Confidence < threshold {
		return IntentPlan{
			Kind: IntentGeneral, Confidence: classification.Confidence,
			ReasonCode: "classifier_low_confidence", NeedsClarification: true,
			AttachmentIDs: cleanIDs(attachmentIDs),
		}
	}
	if plan.Kind == IntentFileUnderstanding && len(plan.AttachmentIDs) == 0 {
		plan.NeedsClarification = true
		plan.ReasonCode = "classifier_file_missing_attachment"
	}
	return plan
}

// ClassifyIntent applies the deterministic priority from the execution plan:
// execution, explicit files, live data, project facts, then general chat.
func ClassifyIntent(query string, attachmentIDs []string) IntentPlan {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return planFor(IntentGeneral, nil)
	}
	ids := cleanIDs(attachmentIDs)
	if isExecutionIntent(q) {
		plan := planFor(IntentExecution, ids)
		plan.RequiresApproval = true
		plan.ReasonCode = "explicit_execution_action"
		if !hasAny(q, "工单", "流程", "审批", "任务", "项目资产", "内部系统", "订单", "库存", "账号", "记录", "状态") {
			plan.NeedsClarification = true
			plan.ReasonCode = "execution_target_missing"
		}
		return plan
	}
	if len(ids) > 0 || isFileRequest(q) {
		plan := planFor(IntentFileUnderstanding, ids)
		plan.ReasonCode = "explicit_attachment_request"
		if len(ids) == 0 {
			plan.NeedsClarification = true
			plan.ReasonCode = "attachment_required"
		}
		return plan
	}
	if isRealtimeRequest(q) {
		plan := planFor(IntentRealtimeData, nil)
		plan.FreshnessRequired = true
		plan.ReasonCode = "live_data_signal"
		if !hasAny(q, "订单", "库存", "余额", "状态", "进度", "工单", "任务", "资产", "流程", "设备", "服务", "数据", "记录", "指标") {
			plan.NeedsClarification = true
			plan.ReasonCode = "realtime_target_missing"
		}
		return plan
	}
	if isProjectFactRequest(q) {
		plan := planFor(IntentProjectFact, nil)
		plan.ReasonCode = "project_fact_signal"
		return plan
	}
	plan := planFor(IntentGeneral, nil)
	if hasAny(q, "查一下这个", "查询一下这个", "帮我确认一下", "看一下这个事情", "帮我查一下") {
		plan.NeedsClassifier = true
		plan.NeedsClarification = true
		plan.ReasonCode = "ambiguous_intent"
	}
	return plan
}

func planFor(kind IntentKind, attachmentIDs []string) IntentPlan {
	plan := IntentPlan{Kind: kind, Confidence: 1, AttachmentIDs: cleanIDs(attachmentIDs)}
	switch kind {
	case IntentGeneral:
		plan.ReasonCode = "general_fallback"
	case IntentProjectFact:
		plan.RequiredSources = []string{"project_documents", "project_memory"}
	case IntentRealtimeData:
		plan.RequiredSources = []string{"registered_read_only_source"}
		plan.FreshnessRequired = true
	case IntentFileUnderstanding:
		plan.RequiredSources = []string{"attachment_artifacts"}
	case IntentExecution:
		plan.RequiredSources = []string{"allowlisted_operation"}
		plan.RequiresApproval = true
	default:
		plan.Kind = IntentGeneral
		plan.Confidence = 0
		plan.ReasonCode = "invalid_intent_fallback"
		plan.NeedsClarification = true
	}
	return plan
}

// WithLocalFilePath marks a file-understanding plan as backed by a concrete
// path that the service has matched to its configured local-file roots. The
// reader still performs the authoritative resolved-path, symlink and file
// policy checks; this plan only selects the server-owned evidence source.
func WithLocalFilePath(plan IntentPlan) IntentPlan {
	if plan.Kind != IntentFileUnderstanding {
		return plan
	}
	if len(plan.AttachmentIDs) == 0 {
		plan.RequiredSources = []string{"local_file_read"}
		plan.NeedsClarification = false
		plan.ReasonCode = "explicit_local_file_path"
		return plan
	}
	for _, source := range plan.RequiredSources {
		if source == "local_file_read" {
			plan.NeedsClarification = false
			plan.ReasonCode = "explicit_local_file_path"
			return plan
		}
	}
	plan.RequiredSources = append(plan.RequiredSources, "local_file_read")
	plan.NeedsClarification = false
	plan.ReasonCode = "explicit_local_file_path"
	return plan
}

func cleanIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	cleaned := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		cleaned = append(cleaned, id)
	}
	return cleaned
}

func isExecutionIntent(q string) bool {
	if hasAny(q, "怎么修改", "如何修改", "修改哪里", "改哪里", "需要修改哪里", "需要改哪里", "应该修改哪里", "应该改哪里", "在哪里修改") &&
		!hasAny(q, "现在帮我", "请直接", "直接修改", "直接改", "立即", "马上", "执行") {
		return false
	}
	if hasAny(q, "是什么", "有哪些", "在哪里", "指什么", "有什么作用", "怎么", "如何", "设计方案", "说明一下", "解释一下", "应该怎样", "能不能", "介绍") &&
		!hasAny(q, "现在帮我", "请直接", "立即", "马上", "执行", "提交", "修改", "删除", "创建", "更新", "关闭", "批准") {
		return false
	}
	return hasAny(q, "帮我提交", "提交", "创建", "新增", "更新", "修改", "删除", "关闭", "批准", "审批通过", "执行", "部署", "写入", "变更") ||
		(strings.Contains(q, "运行") && strings.Contains(q, "任务"))
}

func isFileRequest(q string) bool {
	if hasAny(q, "不要读取", "不要打开", "不用上传", "不需要文件", "怎么解析", "如何解析", "如何读取", "设计解析方案") {
		return false
	}
	if strings.Contains(q, "://") || strings.Contains(q, "www.") {
		return false
	}
	pathLike := strings.ContainsAny(q, "/\\") && (hasAny(q, "读取", "读一下", "读出来", "打开", "阅读") || hasEnglishWord(q, "read"))
	pathLike = pathLike || hasAny(q, ".txt", ".md", ".csv", ".json", ".yaml", ".yml", ".go", ".log")
	object := pathLike || hasAny(q, "附件", "上传的文件", "这个文件", "这份文件", "这张图片", "这份图片", "扫描件", "扫描 pdf", "扫描pdf", "pdf", ".pdf", "图片", "照片", "xlsx", "excel", "工作簿", "音频", "录音", "视频", "docx", "word 文件")
	action := hasAny(q, "读取", "分析", "总结", "提取", "识别", "转写", "解析", "里面写了什么", "内容是什么", "看一下", "问答") || hasEnglishWord(q, "read") || hasEnglishWord(q, "summarize")
	return object && action
}

func hasEnglishWord(q, word string) bool {
	for offset := strings.Index(q, word); offset >= 0; {
		end := offset + len(word)
		leftBoundary := offset == 0 || !isIntentWord(q[offset-1])
		rightBoundary := end == len(q) || !isIntentWord(q[end])
		if leftBoundary && rightBoundary {
			return true
		}
		next := strings.Index(q[end:], word)
		if next < 0 {
			return false
		}
		offset = end + next
	}
	return false
}

func isIntentWord(value byte) bool {
	return value == '_' || value == '-' || value == '.' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func isRealtimeRequest(q string) bool {
	if hasAny(q, "几点", "当前时间", "现在时间", "今天几号") {
		return false
	}
	return hasAny(q, "现在", "当前", "最新", "最近", "实时", "目前", "刚刚", "此刻", "今天") &&
		hasAny(q, "订单", "库存", "余额", "状态", "情况", "进度", "工单", "任务", "资产", "流程", "设备", "服务", "数据", "记录", "指标")
}

func isProjectFactRequest(q string) bool {
	if hasAny(q, "通用模板", "通用项目计划", "通用项目管理", "项目计划模板", "项目管理方法") {
		return false
	}
	return hasAny(q,
		"项目", "本项目", "我们系统", "星河系统", "内部", "知识库", "项目资料", "项目文档", "生产部署", "配置", "代码", "接口", "技术栈", "项目版本", "历史决策",
	)
}

func hasAny(q string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(q, term) {
			return true
		}
	}
	return false
}

func validIntent(kind IntentKind) bool {
	switch kind {
	case IntentGeneral, IntentProjectFact, IntentRealtimeData, IntentFileUnderstanding, IntentExecution:
		return true
	default:
		return false
	}
}
