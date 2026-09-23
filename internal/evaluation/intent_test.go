package evaluation

import (
	"path/filepath"
	"testing"
)

func TestFixedIntentEvaluationMeetsPhase6Gates(t *testing.T) {
	cases, err := LoadIntentCases(filepath.Join("..", "integration", "intent_eval_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 50 {
		t.Fatalf("intent evaluation has %d fixed cases; want at least 50", len(cases))
	}
	report := EvaluateIntentCases(cases)
	if report.Accuracy < 0.95 || report.SourceAccuracy < 0.95 {
		for _, item := range report.Cases {
			if !item.Matched {
				t.Errorf("intent mismatch: %+v", item)
			}
		}
		t.Fatalf("intent gates failed: %+v", report)
	}
}
