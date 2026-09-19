package server

import "testing"

func TestDecideRoutePrivateFacts(t *testing.T) {
	decision := decideRoute("星河系统使用什么技术栈？")
	if !decision.preloadKnowledge {
		t.Fatal("private fact questions must force knowledge preflight")
	}
}

func TestDecideRouteCompoundDocumentReport(t *testing.T) {
	decision := decideRoute("先读取 workspace-files 里的工单，然后据此写一份中文报告")
	if len(decision.preloadSkills) != 2 || decision.preloadSkills[0] != "report_writer" || decision.preloadSkills[1] != "documents" {
		t.Fatalf("unexpected route: %+v", decision)
	}
}

func TestDecideRouteOrdinaryQuestionIsModelRouted(t *testing.T) {
	decision := decideRoute("请介绍一下今天的天气")
	if decision.preloadKnowledge || len(decision.preloadSkills) != 0 {
		t.Fatalf("ordinary question should not be forced: %+v", decision)
	}
}
