package output

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// EvidenceConflict is a compact, user-safe description of contradictory
// retrieved evidence. It intentionally stores values and source labels only;
// callers should not persist the full private document text in events.
type EvidenceConflict struct {
	Key      string   `json:"key"`
	Values   []string `json:"values"`
	Sources  []string `json:"sources,omitempty"`
	Category string   `json:"category"`
}

var evidenceAssignment = regexp.MustCompile(`(?i)([a-z][a-z0-9_.-]{1,})\s*[:=：]\s*([^\s,;，；]+)`)
var evidenceVersionLabel = regexp.MustCompile(`(?i)(?:版本(?:号|信息)?|version)\s*[:=：]?\s*(v?\d+(?:\.\d+){1,2}|[A-Z]{1,8}-\d{4}-\d{2})`)
var evidenceSource = regexp.MustCompile(`(?i)\bsource=([^\s]+)`)

type evidenceCandidate struct{ value, source string }

// DetectEvidenceConflicts performs a conservative, deterministic pass over
// the formatted retrieval result. It flags only repeated configuration-like
// keys and repeated version/model identifiers, so ordinary prose is unlikely
// to become a false conflict. The result is suitable for an execution event.
func DetectEvidenceConflicts(evidence string) []EvidenceConflict {
	groups := map[string][]evidenceCandidate{}
	category := map[string]string{}
	var source string
	for _, line := range strings.Split(evidence, "\n") {
		if match := evidenceSource.FindStringSubmatch(line); len(match) == 2 {
			source = match[1]
		}
		versionAssigned := false
		for _, match := range evidenceAssignment.FindAllStringSubmatch(line, -1) {
			key, value := strings.ToLower(strings.TrimSpace(match[1])), strings.TrimSpace(match[2])
			if !conflictKey(key) || isEvidenceMetadataKey(key) || value == "" {
				continue
			}
			groups["key:"+key] = append(groups["key:"+key], evidenceCandidate{value, source})
			category["key:"+key] = "configuration"
			if key == "version" || strings.HasSuffix(key, "_version") || strings.HasSuffix(key, ".version") {
				versionAssigned = true
			}
		}
		// A version-looking token in ordinary prose is not evidence of a
		// version claim. Only inspect it when the line explicitly labels it as
		// a version; this avoids treating unrelated release IDs in retrieved
		// chunks as a conflict for an otherwise unrelated question.
		for _, match := range evidenceVersionLabel.FindAllStringSubmatch(line, -1) {
			if len(match) < 2 {
				continue
			}
			value := match[1]
			if versionAssigned {
				continue
			}
			key := "version"
			groups[key] = append(groups[key], evidenceCandidate{value, source})
			category[key] = "version"
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	conflicts := make([]EvidenceConflict, 0)
	seenConflict := map[string]struct{}{}
	for _, key := range keys {
		values, sources := uniqueCandidates(groups[key])
		if len(values) < 2 {
			continue
		}
		label := strings.TrimPrefix(key, "key:")
		categoryName := category[key]
		signature := categoryName + "|" + label + "|" + strings.Join(values, "|")
		if _, exists := seenConflict[signature]; exists {
			continue
		}
		seenConflict[signature] = struct{}{}
		conflicts = append(conflicts, EvidenceConflict{Key: label, Values: values, Sources: sources, Category: categoryName})
	}
	return conflicts
}

func conflictKey(key string) bool {
	for _, marker := range []string{"version", "model", "store", "database", "region", "port", "url", "host", "enabled", "provider", "dimension", "timeout", "mode"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return strings.Contains(key, "_") || strings.Contains(key, ".")
}

// isEvidenceMetadataKey filters fields emitted by the retrieval formatter.
// They describe how a chunk was returned, rather than a project fact. In
// particular, block_type commonly differs between adjacent chunks and must
// never be presented as a factual contradiction to the user.
func isEvidenceMetadataKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "source", "title", "title_level", "page", "block_type", "block_types", "score", "retrieval_mode", "distance", "rank":
		return true
	default:
		return false
	}
}

func uniqueCandidates(items []evidenceCandidate) ([]string, []string) {
	seenValues, seenSources := map[string]struct{}{}, map[string]struct{}{}
	values, sources := []string{}, []string{}
	for _, item := range items {
		if _, ok := seenValues[item.value]; !ok {
			seenValues[item.value] = struct{}{}
			values = append(values, item.value)
		}
		if item.source != "" {
			if _, ok := seenSources[item.source]; !ok {
				seenSources[item.source] = struct{}{}
				sources = append(sources, item.source)
			}
		}
	}
	return values, sources
}

// ConflictSummary formats a short prompt-safe note without including private
// evidence bodies.
func ConflictSummary(conflicts []EvidenceConflict) string {
	if len(conflicts) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintln(&b, "检索到的项目资料存在冲突，不能自行选择其中一个结论：")
	for _, conflict := range conflicts {
		fmt.Fprintf(&b, "- %s: %s", conflict.Key, strings.Join(conflict.Values, " / "))
		if len(conflict.Sources) > 0 {
			fmt.Fprintf(&b, "（来源：%s）", strings.Join(conflict.Sources, "、"))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}
