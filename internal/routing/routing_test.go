package routing

import (
	"reflect"
	"strings"
	"testing"
)

func TestDecideDocumentVerbsAndRootPrefixedPath(t *testing.T) {
	for _, query := range []string{
		"把 workspace-files 里的 README.txt 读出来做个摘要。",
		"请读取 workspace-files 目录下的文档并总结要点。",
		"这份 DOCX 工单里写了什么？请读取正文并总结。",
	} {
		decision := Decide(query)
		if !reflect.DeepEqual(decision.PreloadSkills, []string{"documents"}) {
			t.Fatalf("query=%q decision=%+v", query, decision)
		}
		if decision.Source != SourceServer || decision.Reason != ReasonDocumentRead {
			t.Fatalf("query=%q route diagnostics=%+v", query, decision)
		}
	}
}

func TestDecideKnowledgeAndReportRoutes(t *testing.T) {
	knowledge := Decide("星河系统使用什么技术栈？")
	if !knowledge.PreloadKnowledge || knowledge.Source != SourceServer || knowledge.Reason != ReasonKnowledgePreflight {
		t.Fatalf("unexpected knowledge route: %+v", knowledge)
	}

	for _, query := range []string{
		"根据以下事实写一份结构化的中文报告：本月完成 3 个工单。",
		"把刚才检索到的结论整理成一份中文报告。",
		"项目版本已确认，请写一段项目周报中的版本说明。",
		"我要写一份项目周报，事实我会提供，请按报告结构组织。",
	} {
		decision := Decide(query)
		if !reflect.DeepEqual(decision.PreloadSkills, []string{"report_writer"}) {
			t.Fatalf("query=%q decision=%+v", query, decision)
		}
		if decision.Source != SourceServer || decision.Reason != ReasonReportWrite {
			t.Fatalf("query=%q route diagnostics=%+v", query, decision)
		}
	}
}

func TestDecideCompoundSupportedRequest(t *testing.T) {
	decision := Decide("先读取 workspace-files 里的工单，然后据此汇总成一份中文报告。")
	want := []string{"report_writer", "documents"}
	if !reflect.DeepEqual(decision.PreloadSkills, want) {
		t.Fatalf("got skills=%v want=%v", decision.PreloadSkills, want)
	}
	if !strings.Contains(decision.Reason, ReasonDocumentRead) || !strings.Contains(decision.Reason, ReasonReportWrite) {
		t.Fatalf("compound route reason=%q", decision.Reason)
	}
}

func TestDecideUnsupportedFileAndTextOnlyRequests(t *testing.T) {
	tests := []struct {
		name           string
		query          string
		wantReason     string
		wantNoNotice   bool
		wantNoPreloads bool
	}{
		{name: "pdf extraction", query: "这份 PDF 里的文字要怎么提取出来？", wantReason: ReasonUnsupportedPDF, wantNoPreloads: true},
		{name: "workbook operation", query: "请打开这个 xlsx 文件并分析里面的数据。", wantReason: ReasonUnsupportedWorkbook, wantNoPreloads: true},
		{name: "presentation file generation", query: "帮我生成一份季度汇报的 PPT 文件。", wantReason: ReasonUnsupportedSlides, wantNoPreloads: true},
		{name: "render visualization", query: "请生成一个 HTML 可视化页面。", wantReason: ReasonUnsupportedVisualize, wantNoPreloads: true},
		{name: "text table analysis", query: "下面是一段表格数据，帮我分析它的结构并设计公式。", wantNoNotice: true, wantNoPreloads: true},
		{name: "chart design", query: "解释一下这个流程，并给出对比两个方案的图表结构设计。", wantNoNotice: true, wantNoPreloads: true},
		{name: "presentation outline", query: "帮我组织一份季度汇报的演示大纲和叙事结构。", wantNoNotice: true, wantNoPreloads: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := Decide(tt.query)
			if tt.wantNoNotice {
				if decision.CapabilityNotice != "" {
					t.Fatalf("text-only request should remain answerable, got %+v", decision)
				}
			} else if decision.CapabilityNotice == "" || decision.Reason != tt.wantReason {
				t.Fatalf("expected capability notice reason %q, got %+v", tt.wantReason, decision)
			}
			if tt.wantNoPreloads && len(decision.PreloadSkills) != 0 {
				t.Fatalf("unsupported or text-only request should not preload empty skills: %+v", decision)
			}
		})
	}
}

func TestDecideDoesNotForceSkillForAdviceOrNegatedActions(t *testing.T) {
	for _, query := range []string{
		"报告写作技能应该怎么设计？",
		"报告写作技能通常适合哪些工作场景？",
		"不要读取 workspace-files 里的工单，只告诉我报告流程怎么设计。",
	} {
		decision := Decide(query)
		if len(decision.PreloadSkills) != 0 || decision.Source != SourceModel {
			t.Fatalf("query=%q should remain model-routed, got %+v", query, decision)
		}
	}
}

func TestDecideNoMatchHasStableDiagnostic(t *testing.T) {
	decision := Decide("你好，解释一下什么是向量数据库。")
	if decision.Source != SourceModel || decision.Reason != ReasonNoMatch {
		t.Fatalf("unexpected fallback decision: %+v", decision)
	}
}
