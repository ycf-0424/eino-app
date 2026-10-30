package output

import (
	"strings"
	"unicode"
)

// CitationReport is a conservative evidence check. It does not claim that a
// model is factually perfect; it verifies that a non-empty answer has overlap
// with the evidence supplied to it and that at least one source is available.
type CitationReport struct {
	Valid          bool     `json:"valid"`
	EvidenceTokens int      `json:"evidence_tokens"`
	MatchedTokens  int      `json:"matched_tokens"`
	Coverage       float64  `json:"coverage"`
	Sources        []string `json:"sources,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

func CheckCitation(answer, evidence string, sources []string) CitationReport {
	return CheckCitationWithMinCoverage(answer, evidence, sources, 0.08)
}

// CheckCitationWithMinCoverage applies the same conservative token check with
// a caller-selected minimum coverage. A zero or negative threshold uses the
// default 0.08 value. Keeping this in output makes HTTP, Chain and offline
// evaluation share one citation contract.
func CheckCitationWithMinCoverage(answer, evidence string, sources []string, minCoverage float64) CitationReport {
	if minCoverage <= 0 {
		minCoverage = 0.08
	}
	report := CitationReport{Sources: unique(sources)}
	if strings.TrimSpace(answer) == "" {
		report.Reason = "empty_answer"
		return report
	}
	if len(report.Sources) == 0 || strings.TrimSpace(evidence) == "" {
		report.Reason = "missing_evidence"
		return report
	}
	tokens := citationTokens(evidence)
	answerTokens := citationTokens(answer)
	if len(tokens) == 0 || len(answerTokens) == 0 {
		report.Reason = "no_comparable_tokens"
		return report
	}
	matched := 0
	for token := range answerTokens {
		if _, ok := tokens[token]; ok {
			matched++
		}
	}
	report.EvidenceTokens = len(tokens)
	report.MatchedTokens = matched
	report.Coverage = float64(matched) / float64(len(answerTokens))
	// Short answers need only one grounded token; longer answers use a modest
	// threshold to avoid rejecting Chinese answers with little tokenization.
	report.Valid = matched >= 1 && (len(answerTokens) <= 8 || report.Coverage >= minCoverage)
	if !report.Valid {
		report.Reason = "insufficient_evidence_overlap"
	}
	return report
}

func citationTokens(text string) map[string]struct{} {
	text = strings.ToLower(text)
	parts := strings.FieldsFunc(text, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r) || r == '_' || r == '-')
	})
	out := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		if len([]rune(part)) >= 2 {
			out[part] = struct{}{}
		}
	}
	return out
}
