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

func TestConflictDetectionIgnoresRetrievalMetadataAndUnlabelledReleaseIDs(t *testing.T) {
	evidence := `[1] source=tech-stack.md block_type=paragraph score=0.91
星河系统主要使用 Go、Eino 和 Milvus。

[2] source=milvus-validation.md block_type=mixed score=0.84
系统幸运数字：7319，文档编号 XH-2026-09。`
	if conflicts := DetectEvidenceConflicts(evidence); len(conflicts) != 0 {
		t.Fatalf("retrieval metadata or unrelated release id became a conflict: %+v", conflicts)
	}

	versionEvidence := "source=a.md\n内部版本号：XH-2026-09\nsource=b.md\n内部版本号：XH-2026-10"
	conflicts := DetectEvidenceConflicts(versionEvidence)
	if len(conflicts) != 1 || conflicts[0].Key != "version" {
		t.Fatalf("expected explicit version conflict, got %+v", conflicts)
	}
}
