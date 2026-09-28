package evaluation

import (
	"encoding/json"
	"os"
	"slices"
	"strings"

	"my-eino-app/internal/routing"
)

type IntentCase struct {
	Question                   string             `json:"question"`
	AttachmentIDs              []string           `json:"attachment_ids,omitempty"`
	ExplicitLocalFilePath      bool               `json:"explicit_local_file_path,omitempty"`
	ExpectedKind               routing.IntentKind `json:"expected_kind"`
	ExpectedSources            []string           `json:"expected_sources"`
	ExpectedNeedsClarification bool               `json:"expected_needs_clarification"`
}

type IntentCaseResult struct {
	Question                   string             `json:"question"`
	ExpectedKind               routing.IntentKind `json:"expected_kind"`
	ActualKind                 routing.IntentKind `json:"actual_kind"`
	ExpectedSources            []string           `json:"expected_sources"`
	ActualSources              []string           `json:"actual_sources"`
	ExpectedNeedsClarification bool               `json:"expected_needs_clarification"`
	ActualNeedsClarification   bool               `json:"actual_needs_clarification"`
	Matched                    bool               `json:"matched"`
}

type IntentReport struct {
	Total                     int                `json:"total"`
	Matched                   int                `json:"matched"`
	Accuracy                  float64            `json:"accuracy"`
	SourceMatched             int                `json:"source_matched"`
	SourceAccuracy            float64            `json:"source_accuracy"`
	OrdinaryTotal             int                `json:"ordinary_total"`
	OrdinaryFalsePositives    int                `json:"ordinary_false_positives"`
	OrdinaryFalsePositiveRate float64            `json:"ordinary_false_positive_rate"`
	Cases                     []IntentCaseResult `json:"cases"`
}

func LoadIntentCases(path string) ([]IntentCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []IntentCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, err
	}
	return cases, nil
}

func EvaluateIntentCases(cases []IntentCase) IntentReport {
	report := IntentReport{Total: len(cases), Cases: make([]IntentCaseResult, 0, len(cases))}
	for _, item := range cases {
		plan := routing.ClassifyIntent(item.Question, item.AttachmentIDs)
		if item.ExplicitLocalFilePath {
			plan = routing.WithLocalFilePath(plan)
		}
		expectedSources := append([]string(nil), item.ExpectedSources...)
		actualSources := append([]string(nil), plan.RequiredSources...)
		sourcesMatch := equalStrings(expectedSources, actualSources)
		matched := plan.Kind == item.ExpectedKind && sourcesMatch && plan.NeedsClarification == item.ExpectedNeedsClarification
		if matched {
			report.Matched++
		}
		if sourcesMatch {
			report.SourceMatched++
		}
		if item.ExpectedKind == routing.IntentGeneral {
			report.OrdinaryTotal++
			if plan.Kind != routing.IntentGeneral || len(plan.RequiredSources) > 0 {
				report.OrdinaryFalsePositives++
			}
		}
		report.Cases = append(report.Cases, IntentCaseResult{
			Question: item.Question, ExpectedKind: item.ExpectedKind, ActualKind: plan.Kind,
			ExpectedSources: expectedSources, ActualSources: actualSources,
			ExpectedNeedsClarification: item.ExpectedNeedsClarification, ActualNeedsClarification: plan.NeedsClarification, Matched: matched,
		})
	}
	if report.Total > 0 {
		report.Accuracy = float64(report.Matched) / float64(report.Total)
	}
	if report.Total > 0 {
		report.SourceAccuracy = float64(report.SourceMatched) / float64(report.Total)
	}
	if report.OrdinaryTotal > 0 {
		report.OrdinaryFalsePositiveRate = float64(report.OrdinaryFalsePositives) / float64(report.OrdinaryTotal)
	}
	return report
}

func equalStrings(left, right []string) bool {
	left = trimStrings(left)
	right = trimStrings(right)
	return slices.Equal(left, right)
}

func trimStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
