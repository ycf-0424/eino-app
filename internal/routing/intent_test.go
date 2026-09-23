package routing

import (
	"fmt"
	"testing"
)

func TestClassifyIntentAtLeast50FixedSamples(t *testing.T) {
	tests := []struct {
		query string
		kind  IntentKind
	}{
		{"你好", IntentGeneral},
		{"解释一下什么是向量数据库", IntentGeneral},
		{"帮我润色这段文字：感谢你的帮助", IntentGeneral},
		{"总结一下通用的时间管理方法", IntentGeneral},
		{"比较 Go 和 Rust 的优缺点", IntentGeneral},
		{"写一段关于春天的短文", IntentGeneral},
		{"给我一个递归算法的示例", IntentGeneral},
		{"今天学习英语有什么建议", IntentGeneral},
		{"现在几点了", IntentGeneral},
		{"请介绍下 REST API 的概念", IntentGeneral},
		{"帮我把这句话翻译成英文", IntentGeneral},
		{"给一份通用项目计划模板", IntentGeneral},
		{"怎么设计一个订单系统", IntentGeneral},
		{"如何解析 PDF 文件", IntentGeneral},
		{"这个项目使用什么语言", IntentProjectFact},
		{"星河系统的生产部署流程是什么", IntentProjectFact},
		{"我们系统的 API 接口在哪里", IntentProjectFact},
		{"项目配置中的 session.store 有什么作用", IntentProjectFact},
		{"请查一下项目文档里的技术栈", IntentProjectFact},
		{"内部资料如何规定登录方式", IntentProjectFact},
		{"本项目的历史决策是什么", IntentProjectFact},
		{"项目版本号在哪里设置", IntentProjectFact},
		{"知识库中关于记忆隔离的说明", IntentProjectFact},
		{"我们系统里模型路由怎么实现", IntentProjectFact},
		{"订单现在是什么状态", IntentRealtimeData},
		{"查一下当前库存数量", IntentRealtimeData},
		{"目前工单处理进度如何", IntentRealtimeData},
		{"查询今天的余额", IntentRealtimeData},
		{"最新的设备运行状态", IntentRealtimeData},
		{"实时服务指标是多少", IntentRealtimeData},
		{"这个流程目前走到哪一步了", IntentRealtimeData},
		{"当前任务状态是否已完成", IntentRealtimeData},
		{"最近的订单记录有哪些", IntentRealtimeData},
		{"现在项目资产有多少", IntentRealtimeData},
		{"总结这份 PDF 文件", IntentFileUnderstanding},
		{"分析上传的 Excel 工作簿", IntentFileUnderstanding},
		{"识别这张图片上的文字", IntentFileUnderstanding},
		{"把附件里的录音转写出来", IntentFileUnderstanding},
		{"请总结这个视频的内容", IntentFileUnderstanding},
		{"读取这份 Word 文件", IntentFileUnderstanding},
		{"扫描件里的内容是什么", IntentFileUnderstanding},
		{"提取附件中的表格数据", IntentFileUnderstanding},
		{"创建一个新工单", IntentExecution},
		{"请更新订单状态为已发货", IntentExecution},
		{"删除这条内部任务记录", IntentExecution},
		{"批准这个流程申请", IntentExecution},
		{"帮我提交这张审批单", IntentExecution},
		{"关闭项目工单", IntentExecution},
		{"运行内部系统的资产同步任务", IntentExecution},
		{"修改设备服务状态", IntentExecution},
	}
	if len(tests) < 50 {
		t.Fatalf("fixed intent set has %d samples; want at least 50", len(tests))
	}
	for i, test := range tests {
		t.Run(fmt.Sprintf("sample_%02d", i+1), func(t *testing.T) {
			plan := ClassifyIntent(test.query, nil)
			if plan.Kind != test.kind {
				t.Fatalf("query=%q kind=%q reason=%q; want %q", test.query, plan.Kind, plan.ReasonCode, test.kind)
			}
			if plan.Operation != "" {
				t.Fatalf("classifier must not invent an operation: %+v", plan)
			}
		})
	}
}

func TestClassifyIntentSafetyAndEvidenceContract(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		attachments []string
		kind        IntentKind
		clarify     bool
		approval    bool
		freshness   bool
		wantSource  string
	}{
		{name: "execution target missing", query: "帮我更新一下", kind: IntentExecution, clarify: true, approval: true},
		{name: "realtime target missing", query: "当前情况怎么样", kind: IntentRealtimeData, clarify: true, freshness: true},
		{name: "file requires explicit attachment", query: "总结这个 PDF", kind: IntentFileUnderstanding, clarify: true},
		{name: "attachment ids are deduplicated", query: "请分析", attachments: []string{" a1 ", "a1", "a2"}, kind: IntentFileUnderstanding, wantSource: "attachment_artifacts"},
		{name: "realtime freshness", query: "查询当前库存", kind: IntentRealtimeData, freshness: true, wantSource: "registered_read_only_source"},
		{name: "project fact evidence order", query: "项目文档的认证设计", kind: IntentProjectFact, wantSource: "project_documents"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := ClassifyIntent(test.query, test.attachments)
			if plan.Kind != test.kind || plan.NeedsClarification != test.clarify || plan.RequiresApproval != test.approval || plan.FreshnessRequired != test.freshness {
				t.Fatalf("unexpected plan: %+v", plan)
			}
			if test.wantSource != "" && (len(plan.RequiredSources) == 0 || plan.RequiredSources[0] != test.wantSource) {
				t.Fatalf("required_sources=%v, want first %q", plan.RequiredSources, test.wantSource)
			}
			if len(plan.AttachmentIDs) != 2 && test.name == "attachment ids are deduplicated" {
				t.Fatalf("attachment IDs were not normalized: %v", plan.AttachmentIDs)
			}
		})
	}
}

func TestParseIntentClassificationStrictJSON(t *testing.T) {
	valid := `{"kind":"realtime_data","confidence":0.91}`
	got, err := ParseIntentClassification(valid)
	if err != nil || got.Kind != IntentRealtimeData || got.Confidence != 0.91 {
		t.Fatalf("parsed=%+v err=%v", got, err)
	}
	for _, raw := range []string{
		`{"kind":"unknown","confidence":0.9}`,
		`{"kind":"execution","confidence":0.9,"operation":"delete_all"}`,
		`{"kind":"project_fact","confidence":0.9,"owner_id":"other"}`,
		`{"kind":"general","confidence":1.1}`,
		`{"kind":"general","confidence":0.8} {"kind":"execution","confidence":1}`,
		"```json\n" + valid + "\n```",
	} {
		if _, err := ParseIntentClassification(raw); err == nil {
			t.Fatalf("ParseIntentClassification(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestWithClassificationIsFailClosedAndServerOwned(t *testing.T) {
	low := WithClassification(IntentClassification{Kind: IntentExecution, Confidence: 0.4}, 0.7, nil)
	if low.Kind != IntentGeneral || !low.NeedsClarification || low.RequiresApproval || len(low.RequiredSources) != 0 {
		t.Fatalf("low-confidence result was not fail-closed: %+v", low)
	}
	high := WithClassification(IntentClassification{Kind: IntentExecution, Confidence: 0.95}, 0.7, nil)
	if high.Kind != IntentExecution || !high.RequiresApproval || high.Operation != "" || len(high.RequiredSources) != 1 || high.RequiredSources[0] != "allowlisted_operation" {
		t.Fatalf("classifier unexpectedly supplied execution data: %+v", high)
	}
}
