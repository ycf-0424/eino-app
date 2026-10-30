package output

import "testing"

func TestCheckCitationRequiresEvidenceOverlap(t *testing.T) {
	valid := CheckCitation("项目使用 Milvus 向量库", "项目的向量库使用 Milvus，来源为 config.yaml", []string{"config.yaml"})
	if !valid.Valid || valid.MatchedTokens == 0 {
		t.Fatalf("expected grounded answer: %+v", valid)
	}
	invalid := CheckCitation("项目使用 PostgreSQL", "项目的向量库使用 Milvus", []string{"architecture.md"})
	if invalid.Valid || invalid.Reason == "" {
		t.Fatalf("expected unsupported answer: %+v", invalid)
	}
}

func TestCitationFailureAndConflictDetection(t *testing.T) {
	report := CheckCitationWithMinCoverage("项目使用 PostgreSQL 和 Redis，同时部署在上海区域，默认管理员为张三，部署版本为 XH-2026-10，服务端口为 8080", "项目使用 Milvus 向量库", []string{"a.md"}, 0.5)
	if report.Valid || report.Reason == "" {
		t.Fatalf("expected invalid citation report: %+v", report)
	}
	conflicts := DetectEvidenceConflicts("source=a.md\nactive_model=qwen2.5\nversion=XH-2026-09\nsource=b.md\nactive_model=qwen3.5\nversion=XH-2026-10")
	if len(conflicts) != 2 {
		t.Fatalf("expected model and version conflicts, got %+v", conflicts)
	}
	if ConflictSummary(conflicts) == "" {
		t.Fatal("expected conflict summary")
	}
}
