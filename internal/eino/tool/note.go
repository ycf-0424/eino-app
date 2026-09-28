package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

// noteName 限制文件名字符，避免通过工具参数写入任意路径。
var noteName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// writeNoteIntent matches an explicit request to create or persist a local
// note/document. A bare "remember this" request is deliberately excluded:
// long-term memory is handled by the automatic memory pipeline and must not
// be routed through the approval-gated file-writing tool.
var writeNoteIntent = regexp.MustCompile(`(?i)(?:write[_ ]+note|(?:写|写入|创建|生成|保存|导出)[^。！？?]{0,12}(?:笔记|便签|备忘录|文档|文件|markdown|txt)|(?:笔记|便签|备忘录|文档|文件|markdown|txt)[^。！？?]{0,12}(?:写入|创建|生成|保存|导出)|(?:write|save|create|export)[^.!?]{0,20}(?:note|document|file))`)

// WriteNoteRequested reports whether the user's current request explicitly
// asks for a local note/document write. It is a conservative intent gate used
// by the Agent middleware before the model can invoke write_note.
func WriteNoteRequested(query string) bool {
	return writeNoteIntent.MatchString(strings.TrimSpace(query))
}

// ValidateNoteArguments 在审批前校验模型生成的 JSON，避免把空文件名带入
// Checkpoint；用户可以直接看到具体参数错误并重新提问。
func ValidateNoteArguments(arguments string) error {
	var input NoteInput
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return fmt.Errorf("invalid note arguments: %w", err)
	}
	if !noteName.MatchString(input.Name) {
		return fmt.Errorf("invalid note name %q: use 1-64 letters, digits, '_' or '-'", input.Name)
	}
	if input.Content == "" {
		return fmt.Errorf("note content cannot be empty")
	}
	return nil
}

// NoteInput 类型。
type NoteInput struct {
	// Name 仅作为 data/notes 下的文件名，不允许目录分隔符。
	Name string `json:"name"`
	// Content 是要保存的文本内容。
	Content string `json:"content"`
}

// NewWriteNoteTool 创建需要人工审批的写入工具，文件只能写入 data/notes。
func NewWriteNoteTool() einotool.InvokableTool {
	base := utils.NewTool(&schema.ToolInfo{Name: "write_note", Desc: "Write a text note. This changes local files and requires approval.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name": {Type: schema.String, Required: true}, "content": {Type: schema.String, Required: true},
		})}, func(_ context.Context, input NoteInput) (string, error) {
		if err := ValidateNoteArguments(mustMarshalNote(input)); err != nil {
			return "", err
		}
		dir := filepath.Join("data", "notes")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		path := filepath.Join(dir, input.Name+".txt")
		if err := os.WriteFile(path, []byte(input.Content), 0o600); err != nil {
			return "", err
		}
		return "note written to " + path, nil
	})
	return ApprovableTool{InvokableTool: base}
}

func mustMarshalNote(input NoteInput) string {
	b, _ := json.Marshal(input)
	return string(b)
}
