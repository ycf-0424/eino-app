package tool

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"

	"my-eino-app/internal/execution"
)

// DocumentReplacement is a conservative text replacement inside Word text
// runs. Word often splits a sentence across runs when formatting changes, so a
// replacement must be present within one run; the tool never rewrites the
// whole document or silently changes formatting.
type DocumentReplacement struct {
	Find    string `json:"find"`
	Replace string `json:"replace"`
	All     bool   `json:"all,omitempty"`
}

type DocumentWriteInput struct {
	SourcePath       string                `json:"source_path"`
	OutputName       string                `json:"output_name"`
	Replacements     []DocumentReplacement `json:"replacements,omitempty"`
	AppendParagraphs []string              `json:"append_paragraphs,omitempty"`
}

type documentWriter struct {
	roots    []string
	output   string
	maxBytes int64
}

// NewDocumentWriteTool creates an approval-gated DOCX editor. It preserves the
// original and writes a new file below outputDir.
func NewDocumentWriteTool(roots []string, outputDir string, maxBytes int64) (einotool.InvokableTool, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("document max bytes must be greater than zero")
	}
	if strings.TrimSpace(outputDir) == "" {
		return nil, fmt.Errorf("document output directory is required")
	}
	writer := &documentWriter{roots: append([]string(nil), roots...), output: outputDir, maxBytes: maxBytes}
	info := &schema.ToolInfo{
		Name: "document_write",
		Desc: "Edit an approved .docx file without overwriting the original. Use explicit text replacements or append paragraphs, then save a new DOCX under the configured document output directory. This changes a local file and requires approval.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"source_path": {Type: schema.String, Required: true, Desc: "A DOCX path inside an administrator-approved local directory"},
			"output_name": {Type: schema.String, Required: true, Desc: "A new .docx filename only; never a path and never the source filename"},
			"replacements": {
				Type: schema.Array,
				Desc: "Text replacements. Each find string must occur within one Word text run.",
				ElemInfo: &schema.ParameterInfo{Type: schema.Object, SubParams: map[string]*schema.ParameterInfo{
					"find":    {Type: schema.String, Required: true},
					"replace": {Type: schema.String, Required: true},
					"all":     {Type: schema.Boolean},
				}},
			},
			"append_paragraphs": {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}, Desc: "Paragraphs appended to the end of the document body"},
		}),
	}
	base := utils.NewTool(info, writer.write)
	return ApprovableTool{InvokableTool: base}, nil
}

func (w *documentWriter) write(ctx context.Context, input DocumentWriteInput) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	source, err := resolveDocumentPath(input.SourcePath, w.roots)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(filepath.Ext(source), ".docx") {
		return "", fmt.Errorf("document_write only supports .docx files")
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("inspect source document: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", ErrNotRegular
	}
	if info.Size() > w.maxBytes {
		return "", fmt.Errorf("%w: %d bytes exceeds limit %d", ErrTooLarge, info.Size(), w.maxBytes)
	}
	outputName, err := safeDocxOutputName(input.OutputName, source)
	if err != nil {
		return "", err
	}
	target := filepath.Join(w.output, outputName)
	if err := ensureOutputPath(w.output, target); err != nil {
		return "", err
	}
	if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("output file already exists: %s", outputName)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect output file: %w", err)
	}
	modified, operations, err := editDocx(source, w.maxBytes, input.Replacements, input.AppendParagraphs)
	if err != nil {
		return "", err
	}
	if operations == 0 {
		return "", fmt.Errorf("document_write requested no effective changes")
	}
	if err := os.MkdirAll(w.output, 0o750); err != nil {
		return "", fmt.Errorf("create document output directory: %w", err)
	}
	tmp, err := os.CreateTemp(w.output, ".document-write-*.docx")
	if err != nil {
		return "", fmt.Errorf("create document staging file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := writeDocxArchive(tmp, modified); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("protect document output: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close document output: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return "", fmt.Errorf("activate document output: %w", err)
	}
	execution.Annotate(ctx, map[string]any{
		"source_file": filepath.Base(source),
		"output_file": filepath.Base(target),
		"operations":  operations,
	})
	return fmt.Sprintf("document written to %s (%d change(s)); original preserved", target, operations), nil
}

type docxArchive struct {
	entries []*zip.File
	data    map[string][]byte
}

func editDocx(path string, maxBytes int64, replacements []DocumentReplacement, appendParagraphs []string) (*docxArchive, int, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open docx archive: %w", err)
	}
	defer reader.Close()
	archive := &docxArchive{entries: reader.File, data: make(map[string][]byte, len(reader.File))}
	maxUncompressed := uint64(maxBytes) * 10
	var totalUncompressed uint64
	var documentName string
	for _, entry := range reader.File {
		if entry.Name == "word/vbaProject.bin" {
			return nil, 0, fmt.Errorf("macro-enabled Word content is not supported")
		}
		if entry.Name == "word/document.xml" {
			documentName = entry.Name
		}
		if entry.UncompressedSize64 > uint64(maxBytes)*5 || entry.UncompressedSize64 > maxUncompressed-totalUncompressed {
			return nil, 0, fmt.Errorf("docx entry %q exceeds safety limit", entry.Name)
		}
		totalUncompressed += entry.UncompressedSize64
		file, openErr := entry.Open()
		if openErr != nil {
			return nil, 0, fmt.Errorf("open docx entry %q: %w", entry.Name, openErr)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxBytes*5+1))
		file.Close()
		if readErr != nil {
			return nil, 0, fmt.Errorf("read docx entry %q: %w", entry.Name, readErr)
		}
		if int64(len(data)) > maxBytes*5 {
			return nil, 0, fmt.Errorf("docx entry %q is too large", entry.Name)
		}
		archive.data[entry.Name] = data
	}
	if documentName == "" {
		return nil, 0, fmt.Errorf("docx document.xml is missing")
	}
	document, operations, err := rewriteDocumentXML(archive.data[documentName], replacements, appendParagraphs)
	if err != nil {
		return nil, 0, err
	}
	archive.data[documentName] = document
	return archive, operations, nil
}

var wordTextPattern = regexp.MustCompile(`(?s)(<w:t(?:\s[^>]*)?>)(.*?)(</w:t>)`)

func rewriteDocumentXML(data []byte, replacements []DocumentReplacement, appendParagraphs []string) ([]byte, int, error) {
	for _, replacement := range replacements {
		if strings.TrimSpace(replacement.Find) == "" {
			return nil, 0, fmt.Errorf("document replacement find cannot be empty")
		}
	}
	operations := 0
	found := make([]bool, len(replacements))
	result := wordTextPattern.ReplaceAllStringFunc(string(data), func(match string) string {
		parts := wordTextPattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		text := html.UnescapeString(parts[2])
		changed := false
		for index, replacement := range replacements {
			if !strings.Contains(text, replacement.Find) {
				continue
			}
			found[index] = true
			before := text
			if replacement.All {
				text = strings.ReplaceAll(text, replacement.Find, replacement.Replace)
			} else {
				text = strings.Replace(text, replacement.Find, replacement.Replace, 1)
			}
			if before != text {
				changed = true
				operations++
			}
		}
		if !changed {
			return match
		}
		var escaped bytes.Buffer
		_ = xml.EscapeText(&escaped, []byte(text))
		return parts[1] + escaped.String() + parts[3]
	})
	for index, replacement := range replacements {
		if !found[index] {
			return nil, 0, fmt.Errorf("text to replace was not found in one Word text run: %q", replacement.Find)
		}
	}
	if len(appendParagraphs) > 0 {
		var paragraphs strings.Builder
		for _, paragraph := range appendParagraphs {
			paragraph = strings.TrimSpace(paragraph)
			if paragraph == "" {
				continue
			}
			var escaped bytes.Buffer
			_ = xml.EscapeText(&escaped, []byte(paragraph))
			paragraphs.WriteString(`<w:p><w:r><w:t xml:space="preserve">`)
			paragraphs.WriteString(escaped.String())
			paragraphs.WriteString(`</w:t></w:r></w:p>`)
			operations++
		}
		if paragraphs.Len() > 0 {
			closing := strings.LastIndex(result, "</w:body>")
			if closing < 0 {
				return nil, 0, fmt.Errorf("docx body element is missing")
			}
			result = result[:closing] + paragraphs.String() + result[closing:]
		}
	}
	return []byte(result), operations, nil
}

func writeDocxArchive(output *os.File, archive *docxArchive) error {
	writer := zip.NewWriter(output)
	for _, entry := range archive.entries {
		header := entry.FileHeader
		part, err := writer.CreateHeader(&header)
		if err != nil {
			_ = writer.Close()
			return fmt.Errorf("create docx entry %q: %w", entry.Name, err)
		}
		if _, err := part.Write(archive.data[entry.Name]); err != nil {
			_ = writer.Close()
			return fmt.Errorf("write docx entry %q: %w", entry.Name, err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close docx archive: %w", err)
	}
	return nil
}

func resolveDocumentPath(input string, roots []string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("source_path is required")
	}
	for _, root := range roots {
		rootAbs, err := filepath.Abs(strings.TrimSpace(root))
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
		relative, err := filepath.Rel(rootResolved, resolved)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return resolved, nil
		}
	}
	return "", ErrOutsideRoots
}

var safeOutputName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,127}\.docx$`)

func safeDocxOutputName(name, source string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("output_name is required")
	}
	if !safeOutputName.MatchString(name) || filepath.Base(name) != name || strings.Contains(name, "..") {
		return "", fmt.Errorf("output_name must be a simple .docx filename")
	}
	if strings.EqualFold(name, filepath.Base(source)) {
		return "", fmt.Errorf("output_name must differ from the source filename")
	}
	return name, nil
}

func ensureOutputPath(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("output path is outside document output directory")
	}
	return nil
}
