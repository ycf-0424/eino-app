package skill

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	ErrSkillExists        = errors.New("skill already exists")
	ErrManagementDisabled = errors.New("skill management is disabled")
	ErrNetworkDisabled    = errors.New("skill installation network access is disabled")
)

// ManagerConfig describes the writable boundaries for skill and template
// management. SourceRoots are read-only input roots used by template_write.
type ManagerConfig struct {
	Loader          *Loader
	WritableDir     string
	TemplatesDir    string
	SourceRoots     []string
	MaxPackageBytes int64
	MaxFileCount    int
	AllowNetwork    bool
	AllowedHosts    []string
}

// Manager contains the only filesystem and network mutation paths used by the
// skill_write, template_write, and skill_install tools.
type Manager struct {
	loader          *Loader
	writableDir     string
	templatesDir    string
	sourceRoots     []string
	maxPackageBytes int64
	maxFileCount    int
	allowNetwork    bool
	allowedHosts    map[string]struct{}
}

func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Loader == nil {
		return nil, fmt.Errorf("skill manager loader is required")
	}
	if strings.TrimSpace(cfg.WritableDir) == "" {
		cfg.WritableDir = cfg.Loader.WritableDir()
	}
	if strings.TrimSpace(cfg.TemplatesDir) == "" {
		cfg.TemplatesDir = cfg.WritableDir
	}
	if cfg.MaxPackageBytes <= 0 {
		cfg.MaxPackageBytes = 50 << 20
	}
	if cfg.MaxFileCount <= 0 {
		cfg.MaxFileCount = 256
	}
	hosts := make(map[string]struct{}, len(cfg.AllowedHosts))
	for _, host := range cfg.AllowedHosts {
		host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
		if host != "" {
			hosts[host] = struct{}{}
		}
	}
	return &Manager{
		loader:          cfg.Loader,
		writableDir:     cfg.WritableDir,
		templatesDir:    cfg.TemplatesDir,
		sourceRoots:     append([]string(nil), cfg.SourceRoots...),
		maxPackageBytes: cfg.MaxPackageBytes,
		maxFileCount:    cfg.MaxFileCount,
		allowNetwork:    cfg.AllowNetwork,
		allowedHosts:    hosts,
	}, nil
}

type skillFrontmatter struct {
	Description   string   `yaml:"description"`
	Scenarios     []string `yaml:"scenarios,omitempty"`
	NotFor        []string `yaml:"not_for,omitempty"`
	RequiredTools []string `yaml:"required_tools,omitempty"`
}

// WriteSkill writes a validated SKILL.md package. It never writes outside the
// configured writable directory and refuses to replace an existing package by
// default.
func (m *Manager) WriteSkill(name, description, instruction string, scenarios, notFor, requiredTools []string, overwrite bool) (string, error) {
	if m == nil || m.loader == nil {
		return "", ErrManagementDisabled
	}
	name = strings.TrimSpace(name)
	if !validName.MatchString(name) {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	description = strings.TrimSpace(description)
	instruction = strings.TrimSpace(instruction)
	if description == "" {
		return "", fmt.Errorf("skill description cannot be empty")
	}
	if instruction == "" {
		return "", fmt.Errorf("skill instruction cannot be empty")
	}
	meta := skillFrontmatter{
		Description:   description,
		Scenarios:     cleanList(scenarios),
		NotFor:        cleanList(notFor),
		RequiredTools: cleanList(requiredTools),
	}
	metaBytes, err := yaml.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("encode skill metadata: %w", err)
	}
	content := "---\n" + string(metaBytes) + "---\n\n" + instruction + "\n"
	if _, err := parseSkill(name, content); err != nil {
		return "", err
	}
	target, err := m.writablePackagePath(name)
	if err != nil {
		return "", err
	}
	if err := m.installPackageDir(target, map[string][]byte{"SKILL.md": []byte(content)}, overwrite); err != nil {
		return "", err
	}
	return target, nil
}

// WriteTemplate creates a project-local, reference-backed template skill. The
// reference is copied from an approved read-only root and never modified.
func (m *Manager) WriteTemplate(name, displayName, description, kind, referencePath, previewPath string, overwrite bool) (string, error) {
	if m == nil || m.loader == nil {
		return "", ErrManagementDisabled
	}
	name = strings.TrimSpace(name)
	if !validName.MatchString(name) {
		return "", fmt.Errorf("invalid template name %q", name)
	}
	displayName = strings.TrimSpace(displayName)
	description = strings.TrimSpace(description)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if displayName == "" || description == "" {
		return "", fmt.Errorf("template display_name and description are required")
	}
	if !validTemplateKind(kind) {
		return "", fmt.Errorf("unsupported template kind %q", kind)
	}
	reference, err := resolveWithinRoots(referencePath, m.sourceRoots)
	if err != nil {
		return "", err
	}
	refInfo, err := os.Stat(reference)
	if err != nil {
		return "", fmt.Errorf("inspect template reference: %w", err)
	}
	if !refInfo.Mode().IsRegular() {
		return "", fmt.Errorf("template reference is not a regular file")
	}
	if refInfo.Size() > m.maxPackageBytes {
		return "", fmt.Errorf("template reference exceeds %d bytes", m.maxPackageBytes)
	}
	if err := validateTemplateExtension(kind, filepath.Ext(reference)); err != nil {
		return "", err
	}

	files := map[string][]byte{}
	refData, err := os.ReadFile(reference)
	if err != nil {
		return "", fmt.Errorf("read template reference: %w", err)
	}
	refName := "assets/reference" + strings.ToLower(filepath.Ext(reference))
	files[refName] = refData
	previewName := ""
	if strings.TrimSpace(previewPath) != "" {
		preview, previewErr := resolveWithinRoots(previewPath, m.sourceRoots)
		if previewErr != nil {
			return "", previewErr
		}
		previewData, previewErr := os.ReadFile(preview)
		if previewErr != nil {
			return "", fmt.Errorf("read template preview: %w", previewErr)
		}
		if int64(len(previewData)) > m.maxPackageBytes {
			return "", fmt.Errorf("template preview exceeds %d bytes", m.maxPackageBytes)
		}
		previewName = "assets/preview" + strings.ToLower(filepath.Ext(preview))
		files[previewName] = previewData
	}

	skillName := "artifact-template-" + name
	if !validName.MatchString(skillName) {
		return "", fmt.Errorf("template name is too long or invalid")
	}
	frontmatter := skillFrontmatter{
		Description: description,
		Scenarios:   []string{"使用可复用的" + displayName + "模板"},
		NotFor:      []string{"一次性内容不需要该模板"},
	}
	metaBytes, err := yaml.Marshal(frontmatter)
	if err != nil {
		return "", fmt.Errorf("encode template metadata: %w", err)
	}
	instruction := fmt.Sprintf("# %s\n\n使用项目内保存的参考文件生成同类内容。\n\n模板类型：%s\n参考文件：%s\n", displayName, kind, refName)
	if previewName != "" {
		instruction += "预览文件：" + previewName + "\n"
	}
	files["SKILL.md"] = []byte("---\n" + string(metaBytes) + "---\n\n" + instruction)
	manifest := map[string]any{
		"version":         1,
		"skill_name":      skillName,
		"display_name":    displayName,
		"description":     description,
		"kind":            kind,
		"reference":       refName,
		"preview":         previewName,
		"source_basename": filepath.Base(reference),
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode template manifest: %w", err)
	}
	files["artifact-template.json"] = append(manifestBytes, '\n')

	target := filepath.Join(m.templatesDir, skillName)
	if err := m.installPackageDir(target, files, overwrite); err != nil {
		return "", err
	}
	return target, nil
}

// InstallGitHubSkill installs one public GitHub skill directory at a pinned
// ref. It downloads only from an allowlisted GitHub host and never executes
// files from the package.
func (m *Manager) InstallGitHubSkill(ctx context.Context, repo, skillPath, ref, name string, overwrite bool) (string, error) {
	if m == nil || m.loader == nil {
		return "", ErrManagementDisabled
	}
	if !m.allowNetwork {
		return "", ErrNetworkDisabled
	}
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || !validRepoPart(parts[0]) || !validRepoPart(parts[1]) {
		return "", fmt.Errorf("repo must be owner/repository")
	}
	skillPath = strings.Trim(strings.TrimSpace(skillPath), "/")
	if skillPath == "" || strings.Contains(skillPath, "..") || strings.ContainsAny(skillPath, "\\:") {
		return "", fmt.Errorf("invalid skill path")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "main"
	}
	if len(ref) > 128 || strings.ContainsAny(ref, "\\\r\n") {
		return "", fmt.Errorf("invalid git ref")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(skillPath)
	}
	if !validName.MatchString(name) {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	downloadURL := url.URL{Scheme: "https", Host: "codeload.github.com", Path: "/" + repo + "/zip/" + ref}
	if !m.hostAllowed(downloadURL.Hostname()) {
		return "", fmt.Errorf("download host %q is not allowed", downloadURL.Hostname())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create skill download request: %w", err)
	}
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download skill package: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download skill package: HTTP %s", resp.Status)
	}
	archiveData, err := io.ReadAll(io.LimitReader(resp.Body, m.maxPackageBytes+1))
	if err != nil {
		return "", fmt.Errorf("read skill package: %w", err)
	}
	if int64(len(archiveData)) > m.maxPackageBytes {
		return "", fmt.Errorf("skill package exceeds %d bytes", m.maxPackageBytes)
	}
	archive, err := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
	if err != nil {
		return "", fmt.Errorf("read skill zip: %w", err)
	}
	rootPrefix := ""
	if len(archive.File) > 0 {
		first := strings.Trim(archive.File[0].Name, "/")
		if slash := strings.IndexByte(first, '/'); slash > 0 {
			rootPrefix = first[:slash]
		}
	}
	selectedPrefix := rootPrefix + "/" + skillPath + "/"
	files := make(map[string][]byte)
	var total uint64
	for _, file := range archive.File {
		entry := strings.TrimPrefix(strings.TrimPrefix(file.Name, "/"), selectedPrefix)
		if entry == file.Name || entry == "" || strings.HasSuffix(file.Name, "/") {
			continue
		}
		if len(files) >= m.maxFileCount {
			return "", fmt.Errorf("skill package contains more than %d files", m.maxFileCount)
		}
		if filepath.IsAbs(entry) || strings.Contains(entry, "..") || strings.ContainsAny(entry, "\\\x00") {
			return "", fmt.Errorf("unsafe path in skill package: %q", entry)
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink in skill package is not allowed: %q", entry)
		}
		if file.UncompressedSize64 > uint64(m.maxPackageBytes) || file.UncompressedSize64 > uint64(m.maxPackageBytes)-total {
			return "", fmt.Errorf("uncompressed skill package exceeds %d bytes", m.maxPackageBytes)
		}
		reader, openErr := file.Open()
		if openErr != nil {
			return "", fmt.Errorf("open skill file %q: %w", entry, openErr)
		}
		remaining := uint64(m.maxPackageBytes) - total
		data, readErr := io.ReadAll(io.LimitReader(reader, int64(remaining)+1))
		reader.Close()
		if readErr != nil {
			return "", fmt.Errorf("read skill file %q: %w", entry, readErr)
		}
		if uint64(len(data)) > uint64(m.maxPackageBytes)-total {
			return "", fmt.Errorf("uncompressed skill package exceeds %d bytes", m.maxPackageBytes)
		}
		total += uint64(len(data))
		files[filepath.ToSlash(entry)] = data
	}
	if _, ok := files["SKILL.md"]; !ok {
		return "", fmt.Errorf("skill package does not contain %s/SKILL.md", skillPath)
	}
	if _, err := parseSkill(name, string(files["SKILL.md"])); err != nil {
		return "", fmt.Errorf("installed skill metadata: %w", err)
	}
	target, err := m.writablePackagePath(name)
	if err != nil {
		return "", err
	}
	if err := m.installPackageDir(target, files, overwrite); err != nil {
		return "", err
	}
	digest := sha256.Sum256(archiveData)
	return fmt.Sprintf("%s (sha256=%s)", target, hex.EncodeToString(digest[:])), nil
}

func (m *Manager) writablePackagePath(name string) (string, error) {
	if !validName.MatchString(name) {
		return "", fmt.Errorf("invalid skill name %q", name)
	}
	if strings.TrimSpace(m.writableDir) == "" {
		return "", ErrManagementDisabled
	}
	return filepath.Join(m.writableDir, name), nil
}

func (m *Manager) installPackageDir(target string, files map[string][]byte, overwrite bool) error {
	if len(files) == 0 {
		return fmt.Errorf("skill package is empty")
	}
	root := filepath.Dir(target)
	if err := os.MkdirAll(root, 0o750); err != nil {
		return fmt.Errorf("create package directory: %w", err)
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target package is a symlink")
		}
		if !overwrite {
			return ErrSkillExists
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect target package: %w", err)
	}
	stage, err := os.MkdirTemp(root, ".skill-stage-")
	if err != nil {
		return fmt.Errorf("create package staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	keys := make([]string, 0, len(files))
	for name := range files {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if filepath.IsAbs(name) || strings.Contains(name, "..") || strings.ContainsAny(name, "\\\x00") {
			return fmt.Errorf("unsafe package file path %q", name)
		}
		path := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("create package subdirectory: %w", err)
		}
		if err := os.WriteFile(path, files[name], 0o640); err != nil {
			return fmt.Errorf("write package file %q: %w", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stage, "SKILL.md")); err != nil {
		return fmt.Errorf("skill package must contain SKILL.md")
	}
	return atomicInstallDir(stage, target, overwrite)
}

func atomicInstallDir(stage, target string, overwrite bool) error {
	backup := ""
	if _, err := os.Lstat(target); err == nil {
		if !overwrite {
			return ErrSkillExists
		}
		backup = target + ".backup-" + fmt.Sprint(time.Now().UnixNano())
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("stage existing package for replacement: %w", err)
		}
	}
	if err := os.Rename(stage, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return fmt.Errorf("activate package: %w", err)
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}

func resolveWithinRoots(input string, roots []string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("path is required")
	}
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rootResolved, err := filepath.EvalSymlinks(rootAbs)
		if err != nil {
			continue
		}
		candidate := input
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(rootResolved, candidate)
		}
		candidateAbs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(candidateAbs)
		if err != nil {
			continue
		}
		if pathWithin(rootResolved, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path is outside approved source roots or does not exist")
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func cleanList(values []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func validTemplateKind(kind string) bool {
	switch kind {
	case "document", "spreadsheet", "presentation", "image", "email", "slack":
		return true
	default:
		return false
	}
}

func validateTemplateExtension(kind, ext string) error {
	ext = strings.ToLower(ext)
	allowed := map[string][]string{
		"document":     {".docx"},
		"spreadsheet":  {".xlsx", ".xls", ".csv"},
		"presentation": {".pptx", ".ppt"},
		"image":        {".png", ".jpg", ".jpeg"},
		"email":        {".txt"},
		"slack":        {".txt"},
	}
	for _, candidate := range allowed[kind] {
		if ext == candidate {
			return nil
		}
	}
	return fmt.Errorf("reference extension %q is not valid for template kind %q", ext, kind)
}

var repoPartPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

func validRepoPart(value string) bool { return repoPartPattern.MatchString(value) }

func (m *Manager) hostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for allowed := range m.allowedHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}
