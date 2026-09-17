package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGo(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// internal/eino/ 之内用编排 API 是合法的 —— 这正是收敛后的归属地。
func TestScanAllowsGuardedImportsInsideEino(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/eino/agent/agent.go",
		"package agent\n\nimport (\n\t\"github.com/cloudwego/eino/compose\"\n\t\"github.com/cloudwego/eino/adk\"\n)\n")

	violations, scanned, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("internal/eino 内的编排依赖不应违规: %v", violations)
	}
	if scanned != 0 {
		t.Fatalf("internal/eino 下的文件不应计入扫描数，实际 %d", scanned)
	}
}

// 业务包直接 import compose 必须被拦下，并指出确切位置。
func TestScanFlagsGuardedImportInBusinessPackage(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/server/service.go",
		"package server\n\nimport \"github.com/cloudwego/eino/compose\"\n\nvar _ = compose.NewChain[any, any]\n")
	writeGo(t, root, "internal/memory/mysql.go", "package memory\n")

	violations, scanned, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("应恰好拦下 1 处，实际 %d: %v", len(violations), violations)
	}
	if !strings.HasPrefix(violations[0], "internal/server/service.go:3: ") {
		t.Fatalf("违规位置不正确: %q", violations[0])
	}
	if !strings.Contains(violations[0], "compose") {
		t.Fatalf("违规信息应包含依赖名: %q", violations[0])
	}
	if scanned != 2 {
		t.Fatalf("扫描数应为 2，实际 %d", scanned)
	}
}

// 子包前缀也要命中（adk/prebuilt/... 属于同一编排面）。
func TestScanFlagsSubpackageAndCmdDir(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "cmd/console/main.go",
		"package main\n\nimport \"github.com/cloudwego/eino/adk/prebuilt/supervisor\"\n\nvar _ = supervisor.New\n")
	writeGo(t, root, "internal/eino/chain/rag_chain.go",
		"package chain\n\nimport \"github.com/cloudwego/eino/callbacks\"\n")

	violations, _, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || !strings.HasPrefix(violations[0], "cmd/console/main.go:") {
		t.Fatalf("cmd/ 下的 adk 子包应被拦下: %v", violations)
	}
}

// 注释与字符串里提到这些包不算违规 —— 只看 import 声明。
func TestScanIgnoresMentionsOutsideImports(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/server/doc.go",
		"package server\n\n// 这里提到 github.com/cloudwego/eino/compose 与 adk，但并没有真的 import。\nimport \"context\"\n\nvar _ = context.Background\n")

	violations, scanned, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("注释中的提及不应算违规: %v", violations)
	}
	if scanned != 1 {
		t.Fatalf("扫描数应为 1，实际 %d", scanned)
	}
}

// 非 Go 文件与目录不参与扫描。
func TestScanSkipsNonGoFiles(t *testing.T) {
	root := t.TempDir()
	writeGo(t, root, "internal/server/notes.txt", "github.com/cloudwego/eino/compose")

	violations, scanned, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 || scanned != 0 {
		t.Fatalf("非 Go 文件不应被扫描: violations=%v scanned=%d", violations, scanned)
	}
}
