// Package skill 加载业务技能说明，并将其作为额外系统规则注入 Agent。
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Skill 类型。
type Skill struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Instruction   string   `json:"instruction"`
	Scenarios     []string `json:"scenarios,omitempty" yaml:"scenarios"`
	NotFor        []string `json:"not_for,omitempty" yaml:"not_for"`
	RequiredTools []string `json:"required_tools,omitempty" yaml:"required_tools"`
}

// Loader 类型。
type Loader struct {
	dir         string
	writableDir string
	templateDir string
	// ExposeNames 决定技能名是否允许出现在面向用户的回答里。
	// 技能名属于内部实现：默认 false，提示词会禁止模型提及技能名称；
	// 只有 debug 模式才置为 true，允许模型按目录介绍和推荐技能。
	// 该字段只在构造阶段写入，运行期只读，因此可以安全地被并发请求共享。
	ExposeNames bool
}

// NewLoader 函数。
func NewLoader(dir string) *Loader {
	return NewLoaderWithDirs(dir, dir, "")
}

// NewLoaderWithWriteDir keeps the bundled skill directory read-only while
// allowing project-created skills to live in a persistent data directory.
func NewLoaderWithWriteDir(dir, writableDir string) *Loader {
	return NewLoaderWithDirs(dir, writableDir, "")
}

// NewLoaderWithDirs reads writable, template, and bundled skill packages in
// that precedence order. The first matching package wins, so a managed skill
// can intentionally override a bundled default without modifying the image.
func NewLoaderWithDirs(dir, writableDir, templateDir string) *Loader {
	if strings.TrimSpace(dir) == "" {
		dir = "skills"
	}
	if strings.TrimSpace(writableDir) == "" {
		writableDir = dir
	}
	return &Loader{dir: dir, writableDir: writableDir, templateDir: templateDir}
}

// WritableDir is the only directory used by skill_write and skill_install.
func (l *Loader) WritableDir() string { return l.writableDir }

func (l *Loader) readDirs() []string {
	seen := map[string]bool{}
	var dirs []string
	for _, dir := range []string{l.writableDir, l.templateDir, l.dir} {
		dir = strings.TrimSpace(dir)
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	return dirs
}

// Load 只允许简单名称并固定读取 SKILL.md，防止通过 Skill 名称进行路径穿越。
func (l *Loader) Load(name string) (Skill, error) {
	if !validName.MatchString(name) {
		return Skill{}, fmt.Errorf("invalid skill name %q", name)
	}
	var data []byte
	var readErr error
	for _, dir := range l.readDirs() {
		data, readErr = os.ReadFile(filepath.Join(dir, name, "SKILL.md"))
		if readErr == nil {
			break
		}
		if !os.IsNotExist(readErr) {
			return Skill{}, fmt.Errorf("load skill %q: %w", name, readErr)
		}
	}
	if readErr != nil {
		return Skill{}, fmt.Errorf("load skill %q: %w", name, readErr)
	}
	return parseSkill(name, string(data))
}

func parseSkill(name, raw string) (Skill, error) {
	data := []byte(raw)
	text := strings.TrimSpace(string(data))
	if text == "" {
		return Skill{}, fmt.Errorf("skill %q is empty", name)
	}
	description := ""
	var routing struct {
		Description   string   `yaml:"description"`
		Scenarios     []string `yaml:"scenarios"`
		NotFor        []string `yaml:"not_for"`
		RequiredTools []string `yaml:"required_tools"`
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.HasPrefix(text, "---\n") {
		end := strings.Index("\n"+text[4:], "\n---\n")
		if end < 0 {
			return Skill{}, fmt.Errorf("skill %q: unclosed frontmatter", name)
		}
		if err := yaml.Unmarshal([]byte(text[4:4+end]), &routing); err != nil {
			return Skill{}, fmt.Errorf("skill %q metadata: %w", name, err)
		}
		description = strings.TrimSpace(routing.Description)
		text = strings.TrimSpace(text[4+end+4:])
	}
	if text == "" {
		return Skill{}, fmt.Errorf("skill %q has no instructions", name)
	}
	if description == "" {
		// 兼容没有 frontmatter 的旧技能，以正文开头作为简短摘要。
		runes := []rune(strings.Join(strings.Fields(text), " "))
		if len(runes) > 240 {
			runes = runes[:240]
		}
		description = string(runes)
	}
	return Skill{Name: name, Description: description, Instruction: text, Scenarios: routing.Scenarios, NotFor: routing.NotFor, RequiredTools: routing.RequiredTools}, nil
}

func (l *Loader) List() ([]string, error) {
	seen := map[string]bool{}
	var names []string
	for _, dir := range l.readDirs() {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() && validName.MatchString(entry.Name()) && !seen[entry.Name()] {
				if _, err := os.Stat(filepath.Join(dir, entry.Name(), "SKILL.md")); err == nil {
					names = append(names, entry.Name())
					seen[entry.Name()] = true
				}
			}
		}
	}
	sort.Strings(names)
	return names, nil
}
