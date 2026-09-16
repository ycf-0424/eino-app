package memory

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var secretPattern = regexp.MustCompile(`(?i)(sk-[a-z0-9_-]{8,}|-----BEGIN .*PRIVATE KEY|bearer\s+[a-z0-9._-]+|(?:password|api[_ -]?key|token|密码|验证码|密钥)\s*[:：=是为]\s*\S+)`)

func Sensitive(s string) bool { return secretPattern.MatchString(s) }
func Suppressed(s string) bool {
	s = strings.ToLower(s)
	return containsAny(s, "不要记住", "不要保存", "别记住", "别保存", "不许记录", "don't remember", "do not remember", "do not save")
}
func containsAny(s string, list ...string) bool {
	for _, v := range list {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}

// ExplicitCorrection reports a user-authored replacement signal. It is used
// as a deterministic backstop when a model emits relation=assert for text that
// clearly corrects an existing fact. A plain conflicting assertion remains
// unresolved and is never last-write-wins.
func ExplicitCorrection(s string) bool {
	return containsAny(strings.ToLower(s), "改用", "改成", "更改为", "换成", "纠正", "说错", "现在用", "现在改", "不再使用", "instead", "change to", "actually use")
}
func Normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(s)), ""))
}

// ValidateCandidate is a deterministic gate. Evidence containment alone is not
// proof; the extractor must still resolve modality, negation and attribution.
func ValidateCandidate(c Candidate, in ExtractInput, maxChars int) string {
	if Sensitive(in.Message.Text) || Sensitive(c.Value) || Sensitive(c.Evidence) {
		return "sensitive"
	}
	if Suppressed(in.Message.Text) {
		return "suppressed"
	}
	if c.SourceID != in.Message.ID || c.Evidence == "" || !strings.Contains(in.Message.Text, c.Evidence) {
		return "invalid_source"
	}
	if len(c.Key) == 0 || len(c.Key) > 128 || utf8.RuneCountInString(c.Value) > maxChars || len(c.Evidence) > 4000 {
		return "invalid_length"
	}
	switch c.Type {
	case "preference", "profile":
		if c.ScopeKind != "user" {
			return "invalid_scope"
		}
	case "project_fact", "decision", "milestone", "todo":
		if c.ScopeKind != "project" {
			return "invalid_scope"
		}
	default:
		return "invalid_type"
	}
	if c.Relation != "assert" && c.Relation != "correct" && c.Relation != "retract" {
		return "invalid_relation"
	}
	if c.Relation != "retract" && strings.TrimSpace(c.Value) == "" {
		return "empty_value"
	}
	if c.Relation == "correct" && !containsAny(in.Message.Text, "改", "换", "纠正", "说错", "现在", "以后", "不再", "instead", "change", "actually") {
		return "unconfirmed_correction"
	}
	if c.Relation == "retract" && !containsAny(in.Message.Text, "忘", "删除", "清除", "不再", "撤销", "作废", "forget", "delete", "no longer") {
		return "unconfirmed_retraction"
	}
	if containsAny(c.Evidence, "假设", "比如", "例如", "考虑", "是否", "如果", "建议", "可以使用", "可能", "也许", "他决定", "她决定") {
		return "not_asserted"
	}
	if strings.Contains(c.Evidence, "?") || strings.Contains(c.Evidence, "？") {
		return "question"
	}
	if c.ExpiresAt != nil && !containsAny(c.Evidence, "截止", "到期", "之前", "有效期", "deadline", "expires") {
		return "unsupported_expiry"
	}
	return ""
}
