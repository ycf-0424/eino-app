package tool

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"

	"my-eino-app/internal/execution"
)

// LocalFileInput 类型。
type LocalFileInput struct {
	// Path 可以是绝对路径，也可以是相对于任一允许目录的路径。
	Path string `json:"path"`
}

type localFileReader struct {
	roots    []string
	maxBytes int64
}

// NewLocalFileReadTool 创建只读文件工具。roots 是唯一允许访问的目录边界；
// 即使模型传入 ..、绝对路径或符号链接，也不能跳出这些目录。
func NewLocalFileReadTool(roots []string, maxBytes int64) (einotool.InvokableTool, error) {
	reader := &localFileReader{maxBytes: maxBytes}
	if reader.maxBytes <= 0 {
		return nil, fmt.Errorf("local file max bytes must be greater than zero")
	}
	for _, root := range roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve local file root %q: %w", root, err)
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return nil, fmt.Errorf("resolve local file root %q: %w", root, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("inspect local file root %q: %w", root, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("local file root %q is not a directory", root)
		}
		reader.roots = append(reader.roots, filepath.Clean(resolved))
	}
	if len(reader.roots) == 0 {
		return nil, fmt.Errorf("at least one local file root is required")
	}

	info := &schema.ToolInfo{
		Name: "local_file_read",
		Desc: "Read a UTF-8 text file or extract DOCX text from an administrator-approved local directory. Use only when the user explicitly asks to read a local file.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path": {Desc: "Absolute path or a path relative to an approved directory", Type: schema.String, Required: true},
		}),
	}
	return utils.NewTool(info, reader.read), nil
}

func (r *localFileReader) read(ctx context.Context, input LocalFileInput) (string, error) {
	path := strings.TrimSpace(input.Path)
	if path == "" {
		return "", fmt.Errorf("file path is required")
	}
	resolved, err := r.resolve(path)
	if err != nil {
		return "", err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", fmt.Errorf("open local file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect local file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", ErrNotRegular
	}
	if info.Size() > r.maxBytes {
		return "", fmt.Errorf("%w: %d bytes exceeds limit %d", ErrTooLarge, info.Size(), r.maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, r.maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read local file: %w", err)
	}
	if int64(len(data)) > r.maxBytes {
		return "", fmt.Errorf("%w: exceeds limit %d bytes", ErrTooLarge, r.maxBytes)
	}
	if strings.EqualFold(filepath.Ext(resolved), ".docx") {
		text, err := extractDocxText(resolved, r.maxBytes)
		if err != nil {
			return "", err
		}
		if int64(len(text)) > r.maxBytes {
			return "", fmt.Errorf("%w: extracted document text exceeds limit %d bytes", ErrTooLarge, r.maxBytes)
		}
		annotateRead(ctx, resolved, "docx", len(text))
		return fmt.Sprintf("path=%s\n%s", resolved, text), nil
	}
	// 当前工具面向大模型文本上下文，拒绝二进制和非 UTF-8 内容。
	if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return "", ErrNotText
	}
	annotateRead(ctx, resolved, "text", len(data))
	return fmt.Sprintf("path=%s\n%s", resolved, data), nil
}

// annotateRead 记录结构化读取结果；只暴露文件名与格式，不暴露绝对路径。
func annotateRead(ctx context.Context, resolved, format string, size int) {
	execution.Annotate(ctx, map[string]any{
		"file_name":   filepath.Base(resolved),
		"file_format": format,
		"bytes":       size,
	})
}

// extractDocxText 只解析 DOCX 的 word/document.xml 正文文本；不会执行宏、嵌入对象或外部链接。
func extractDocxText(path string, maxBytes int64) (string, error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("open docx archive: %w", err)
	}
	defer archive.Close()
	var document *zip.File
	for _, file := range archive.File {
		if file.Name == "word/document.xml" {
			document = file
			break
		}
	}
	if document == nil {
		return "", fmt.Errorf("docx document.xml is missing")
	}
	reader, err := document.Open()
	if err != nil {
		return "", fmt.Errorf("read docx document: %w", err)
	}
	defer reader.Close()
	var out bytes.Buffer
	// XML 本身包含大量标签，因此允许其达到正文上限的五倍，同时仍限制解压炸弹。
	decoder := xml.NewDecoder(io.LimitReader(reader, maxBytes*5+1))
	inText := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse docx document: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "t":
				inText = true
			case "tab":
				out.WriteByte('\t')
			case "br", "cr":
				out.WriteByte('\n')
			}
		case xml.CharData:
			// Word 文本节点名为 w:t；过滤其他 XML 文本可避免把属性内容混入结果。
			if inText {
				out.Write(value)
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				inText = false
			case "p":
				out.WriteByte('\n')
			}
		}
		if int64(out.Len()) > maxBytes {
			return "", fmt.Errorf("%w: extracted document text exceeds limit %d bytes", ErrTooLarge, maxBytes)
		}
	}
	return strings.TrimSpace(out.String()), nil
}

func (r *localFileReader) resolve(input string) (string, error) {
	candidates := make([]string, 0, len(r.roots))
	if filepath.IsAbs(input) {
		candidates = append(candidates, input)
	} else {
		for _, root := range r.roots {
			candidates = append(candidates, filepath.Join(root, input))
		}
	}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			continue
		}
		for _, root := range r.roots {
			if pathWithinRoot(root, resolved) {
				return resolved, nil
			}
		}
	}
	return "", ErrOutsideRoots
}

func pathWithinRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		relative = strings.ToLower(relative)
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
