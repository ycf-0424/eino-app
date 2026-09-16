package memory

import "testing"

func TestCandidateGate(t *testing.T) {
	base := Candidate{Type: "decision", ScopeKind: "project", Key: "vector_database", Value: "Milvus", SourceID: "m1", Evidence: "我们决定使用 Milvus", Relation: "assert"}
	tests := []struct {
		name, text string
		change     func(*Candidate)
		want       string
	}{
		{"confirmed", "我们决定使用 Milvus", nil, ""},
		{"forged source", "我们决定使用 Milvus", func(c *Candidate) { c.SourceID = "other" }, "invalid_source"},
		{"scope", "我们决定使用 Milvus", func(c *Candidate) { c.ScopeKind = "user" }, "invalid_scope"},
		{"secret", "密码是123456。我们决定使用 Milvus", nil, "sensitive"},
		{"opt out", "不要记住这件事：我们决定使用 Milvus", nil, "suppressed"},
		{"hypothesis", "假设我们决定使用 Milvus", func(c *Candidate) { c.Evidence = "假设我们决定使用 Milvus" }, "not_asserted"},
		{"suggestion", "我建议使用 Milvus", func(c *Candidate) { c.Evidence = "我建议使用 Milvus" }, "not_asserted"},
		{"unconfirmed delete", "我们决定使用 Milvus", func(c *Candidate) { c.Relation = "retract" }, "unconfirmed_retraction"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := base
			if tt.change != nil {
				tt.change(&c)
			}
			got := ValidateCandidate(c, ExtractInput{Message: Message{ID: "m1", Role: "user", Text: tt.text}}, 500)
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestExplicitCorrection(t *testing.T) {
	for _, text := range []string{"之前说错了，现在改用 PostgreSQL", "把方案换成 Milvus", "actually use PostgreSQL"} {
		if !ExplicitCorrection(text) {
			t.Fatalf("expected correction signal in %q", text)
		}
	}
	if ExplicitCorrection("我们使用 PostgreSQL") {
		t.Fatal("plain assertion must not be treated as correction")
	}
}
func TestStrictOutput(t *testing.T) {
	for _, raw := range []string{`{}`, `{"candidates":null}`, `{"candidates":[],"sql":"delete"}`, `{"candidates":[]} {}`, "```json\n{}\n```"} {
		if _, e := ParseCandidates(raw, 5); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, e := ParseCandidates(`{"candidates":[]}`, 5); e != nil {
		t.Fatal(e)
	}
}
