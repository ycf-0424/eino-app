package routing

import "testing"

func TestDecideDocumentVerbsAndRootPrefixedPath(t *testing.T) {
	for _, query := range []string{
		"把 workspace-files 里的 README.txt 读出来做个摘要。",
		"请读取 workspace-files 目录下的文档并总结要点。",
	} {
		decision := Decide(query)
		if len(decision.PreloadSkills) != 1 || decision.PreloadSkills[0] != "documents" {
			t.Fatalf("query=%q decision=%+v", query, decision)
		}
	}
}

func TestDecideUnsupportedFileAndTextPresentation(t *testing.T) {
	if got := Decide("这份 PDF 里的文字要怎么提取出来？").CapabilityNotice; got == "" {
		t.Fatal("pdf capability boundary should be explicit")
	}
	if got := Decide("解释一下这个流程，并给出对比两个方案的图表结构设计。").CapabilityNotice; got == "" {
		t.Fatal("visualization boundary should be explicit")
	}
	if got := Decide("帮我组织一份季度汇报的演示大纲和叙事结构。").CapabilityNotice; got != "" {
		t.Fatalf("text outline should remain supported, got %q", got)
	}
}

func TestReportFactsDoNotImplyDocumentRead(t *testing.T) {
	decision := Decide("根据以下事实写一份结构化的中文报告：本月完成 3 个工单，遗留 1 个性能问题。")
	if len(decision.PreloadSkills) != 1 || decision.PreloadSkills[0] != "report_writer" {
		t.Fatalf("unexpected report route: %+v", decision)
	}
}
