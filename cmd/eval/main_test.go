package main

import (
	"strings"
	"testing"

	"my-eino-app/internal/evaluation"
	"my-eino-app/internal/execution"
)

func TestRoutingFromEventsKeepsSkillAndToolEvidenceSeparate(t *testing.T) {
	events := []execution.Event{
		{Type: execution.SkillPreloaded, Payload: map[string]any{"skill_names": []string{"documents"}}},
		{Type: execution.ToolStarted, Payload: map[string]any{"tool_name": "load_skills"}},
		{Type: execution.SkillLoaded, Payload: map[string]any{"skill_names": []string{"report_writer"}, "requested_names": []string{"report_writer"}}},
	}
	got := routingFromEvents(events)
	if len(got.Preloaded) != 1 || got.Preloaded[0] != "documents" || len(got.Loaded) != 1 || got.Loaded[0] != "report_writer" {
		t.Fatalf("skill events should remain visible: %+v", got)
	}
	if len(got.ToolStarted)+len(got.ToolCompleted)+len(got.ToolFailed) != 0 {
		t.Fatalf("preloading/loading skills and load_skills must not count as real tool execution: %+v", got)
	}
}

func TestRoutingFromEventsCountsAttemptSuccessAndFailureSeparately(t *testing.T) {
	events := []execution.Event{
		{Type: execution.ToolStarted, Payload: map[string]any{"tool_name": "local_file_read"}},
		{Type: execution.ToolFailed, Payload: map[string]any{"tool_name": "local_file_read", "error_code": "not_found"}},
		{Type: execution.ToolStarted, Payload: map[string]any{"tool_name": "knowledge_search", "source": "server_preflight"}},
		{Type: execution.ToolCompleted, Payload: map[string]any{"tool_name": "knowledge_search", "source": "server_preflight"}},
		{Type: execution.ToolStarted, Payload: map[string]any{"tool_name": "local_file_read"}},
		{Type: execution.FileReadDone, Payload: map[string]any{"tool_name": "local_file_read"}},
	}
	got := routingFromEvents(events)
	if strings.Join(got.ToolStarted, ",") != "knowledge_search,local_file_read" {
		t.Fatalf("started tools should be unique and sorted: %+v", got)
	}
	if strings.Join(got.ToolCompleted, ",") != "knowledge_search,local_file_read" {
		t.Fatalf("successful terminal events should include FileReadDone: %+v", got)
	}
	if len(got.ToolFailed) != 1 || got.ToolFailed[0] != "local_file_read" {
		t.Fatalf("failed tool should remain distinguishable: %+v", got)
	}
	if len(got.ToolSources["knowledge_search"]) != 1 || got.ToolSources["knowledge_search"][0] != "server_preflight" {
		t.Fatalf("source field should be captured: %+v", got.ToolSources)
	}
}

func TestApplyDebugOverride(t *testing.T) {
	for _, test := range []struct {
		configured bool
		override   string
		want       bool
		wantErr    bool
	}{
		{configured: true, override: "", want: true},
		{configured: true, override: "false", want: false},
		{configured: false, override: "true", want: true},
		{configured: true, override: "no", want: true, wantErr: true},
	} {
		got, err := applyDebugOverride(test.configured, test.override)
		if got != test.want || (err != nil) != test.wantErr {
			t.Errorf("applyDebugOverride(%v, %q) = (%v, %v), want (%v, error=%v)", test.configured, test.override, got, err, test.want, test.wantErr)
		}
	}
}

func TestThresholdsFailWhenExpectedToolEvidenceIsMissing(t *testing.T) {
	report := evaluation.Report{ToolSuccessTotal: 1, ToolSuccessRate: 0, ForbiddenToolCaseTotal: 1, Intent: evaluation.IntentReport{Total: 50, Accuracy: 1, SourceAccuracy: 1}}
	err := checkEvaluationThresholds(report, evaluationThresholds{
		minCorrect: 0, minCitation: 0, minRefusal: 0, minRouting: -1, minServerRoute: -1,
		maxNoSkillFalsePositive: -1, minToolAttempt: -1, minToolSuccess: 1, minToolSource: -1,
	})
	if err == nil || !strings.Contains(err.Error(), "tool success rate") {
		t.Fatalf("missing expected tool completion must fail the gate, got %v", err)
	}
}

func TestThresholdsFailWhenForbiddenToolWasCalled(t *testing.T) {
	report := evaluation.Report{ForbiddenToolCaseTotal: 3, ForbiddenToolViolations: 1, Intent: evaluation.IntentReport{Total: 50, Accuracy: 1, SourceAccuracy: 1}}
	err := checkEvaluationThresholds(report, evaluationThresholds{
		minCorrect: 0, minCitation: 0, minRefusal: 0, minRouting: -1, minServerRoute: -1,
		maxNoSkillFalsePositive: -1, maxForbiddenToolViolations: 0,
		minToolAttempt: -1, minToolSuccess: -1, minToolSource: -1,
	})
	if err == nil || !strings.Contains(err.Error(), "forbidden-tool violations") {
		t.Fatalf("forbidden tool call must fail the gate, got %v", err)
	}
}
