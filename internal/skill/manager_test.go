package skill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestManager(t *testing.T) (*Manager, string, string, string) {
	t.Helper()
	root := t.TempDir()
	bundled := filepath.Join(root, "bundled")
	writable := filepath.Join(root, "writable")
	templates := filepath.Join(root, "templates")
	sources := filepath.Join(root, "sources")
	for _, dir := range []string{bundled, writable, templates, sources} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	loader := NewLoaderWithDirs(bundled, writable, templates)
	manager, err := NewManager(ManagerConfig{
		Loader: loader, WritableDir: writable, TemplatesDir: templates,
		SourceRoots: []string{sources}, MaxPackageBytes: 1 << 20, MaxFileCount: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager, writable, templates, sources
}

func TestWriteSkillAndProtectExisting(t *testing.T) {
	manager, writable, _, _ := newTestManager(t)
	path, err := manager.WriteSkill("weekly-report", "weekly report rules", "Only use supplied facts.", []string{"weekly report"}, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(writable, "weekly-report") {
		t.Fatalf("path=%q", path)
	}
	data, err := os.ReadFile(filepath.Join(path, "SKILL.md"))
	if err != nil || !strings.Contains(string(data), "description: weekly report rules") {
		t.Fatalf("skill data=%q err=%v", data, err)
	}
	if _, err := manager.WriteSkill("weekly-report", "changed", "changed", nil, nil, nil, false); !errors.Is(err, ErrSkillExists) {
		t.Fatalf("expected ErrSkillExists, got %v", err)
	}
	if _, err := manager.WriteSkill("../escape", "x", "x", nil, nil, nil, true); err == nil {
		t.Fatal("expected invalid skill name")
	}
}

func TestWriteTemplateCopiesReferenceAndManifest(t *testing.T) {
	manager, _, templates, sources := newTestManager(t)
	reference := filepath.Join(sources, "reference.txt")
	if err := os.WriteFile(reference, []byte("hello template"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := manager.WriteTemplate("email", "Email", "A reusable email", "email", reference, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(templates, "artifact-template-email") {
		t.Fatalf("path=%q", path)
	}
	for _, name := range []string{"SKILL.md", "artifact-template.json", "assets/reference.txt"} {
		if _, err := os.Stat(filepath.Join(path, filepath.FromSlash(name))); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if _, err := manager.WriteTemplate("escape", "Escape", "bad", "email", filepath.Join(sources, "..", "reference.txt"), "", false); err == nil {
		t.Fatal("expected source root rejection")
	}
}

func TestLoaderWritableSkillOverridesBundled(t *testing.T) {
	root := t.TempDir()
	bundled := filepath.Join(root, "bundled")
	writable := filepath.Join(root, "writable")
	if err := os.MkdirAll(filepath.Join(bundled, "demo"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(writable, "demo"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundled, "demo", "SKILL.md"), []byte("bundled"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(writable, "demo", "SKILL.md"), []byte("managed"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewLoaderWithDirs(bundled, writable, "").Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Instruction != "managed" {
		t.Fatalf("instruction=%q", loaded.Instruction)
	}
}

func TestInstallGitHubSkillRequiresNetworkAndPinnedShape(t *testing.T) {
	manager, _, _, _ := newTestManager(t)
	if _, err := manager.InstallGitHubSkill(t.Context(), "openai/skills", "skills/example", "main", "", false); !errors.Is(err, ErrNetworkDisabled) {
		t.Fatalf("expected network-disabled error, got %v", err)
	}
	manager.allowNetwork = true
	manager.allowedHosts = map[string]struct{}{"github.com": {}}
	if _, err := manager.InstallGitHubSkill(t.Context(), "not-a-repo", "skills/example", "main", "", false); err == nil {
		t.Fatal("expected invalid repository error")
	}
	if _, err := manager.InstallGitHubSkill(t.Context(), "openai/skills", "../example", "main", "", false); err == nil {
		t.Fatal("expected invalid skill path error")
	}
}
