package rag

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"rsc.io/pdf"
)

// documentSection 是解析后的逻辑段，标题和页码会保留到向量元数据中。
type documentSection struct {
	Content  string
	MetaData map[string]any
}

func parseDocument(path, ext string) ([]documentSection, error) {
	switch ext {
	case ".md":
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return parseMarkdown(string(data)), nil
	case ".txt":
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return []documentSection{{Content: cleanText(string(data))}}, nil
	case ".html", ".htm":
		return parseHTML(path)
	case ".json":
		return parseJSON(path)
	case ".docx":
		return parseDOCX(path)
	case ".pdf":
		return parsePDF(path)
	default:
		return nil, fmt.Errorf("unsupported document format %q", ext)
	}
}

// parseMarkdown 按标题切成逻辑段，使检索结果保留章节语义。
func parseMarkdown(text string) []documentSection {
	var result []documentSection
	var title string
	var body strings.Builder
	flush := func() {
		content := cleanText(body.String())
		if content != "" {
			result = append(result, documentSection{Content: content, MetaData: map[string]any{"title": title}})
		}
		body.Reset()
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			if heading != "" {
				flush()
				title = heading
				body.WriteString(heading + "\n")
				continue
			}
		}
		body.WriteString(line + "\n")
	}
	flush()
	return result
}

func parseHTML(path string) ([]documentSection, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	root, e := html.Parse(f)
	if e != nil {
		return nil, e
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && (n.Data == "p" || n.Data == "div" || strings.HasPrefix(n.Data, "h")) {
			b.WriteByte('\n')
		}
	}
	walk(root)
	return []documentSection{{Content: cleanText(b.String())}}, nil
}
func parseJSON(path string) ([]documentSection, error) {
	data, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var v any
	if e = json.Unmarshal(data, &v); e != nil {
		return nil, e
	}
	pretty, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return nil, e
	}
	return []documentSection{{Content: string(pretty)}}, nil
}

func parseDOCX(path string) ([]documentSection, error) {
	r, e := zip.OpenReader(path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name != "word/document.xml" {
			continue
		}
		stream, e := f.Open()
		if e != nil {
			return nil, e
		}
		defer stream.Close()
		d := xml.NewDecoder(stream)
		var b strings.Builder
		for {
			token, e := d.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, e
			}
			switch v := token.(type) {
			case xml.CharData:
				b.Write([]byte(v))
			case xml.EndElement:
				if v.Name.Local == "p" {
					b.WriteByte('\n')
				} else if v.Name.Local == "t" {
					b.WriteByte(' ')
				}
			}
		}
		return []documentSection{{Content: cleanText(b.String())}}, nil
	}
	return nil, fmt.Errorf("word/document.xml not found")
}

func parsePDF(path string) ([]documentSection, error) {
	r, e := pdf.Open(path)
	if e != nil {
		return nil, e
	}
	sections := make([]documentSection, 0, r.NumPage())
	for page := 1; page <= r.NumPage(); page++ {
		items := r.Page(page).Content().Text
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Y == items[j].Y {
				return items[i].X < items[j].X
			}
			return items[i].Y > items[j].Y
		})
		var b strings.Builder
		lastY := 0.0
		for i, item := range items {
			if i > 0 && lastY-item.Y > item.FontSize*0.6 {
				b.WriteByte('\n')
			}
			b.WriteString(item.S)
			b.WriteByte(' ')
			lastY = item.Y
		}
		if content := cleanText(b.String()); content != "" {
			sections = append(sections, documentSection{Content: content, MetaData: map[string]any{"page": page}})
		}
	}
	return sections, nil
}

func cleanText(text string) string {
	text = strings.ReplaceAll(text, "\x00", "")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.FieldsFunc(line, unicode.IsSpace), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
