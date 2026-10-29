package rag

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"rsc.io/pdf"
)

// Block is a source-preserving structured part of a document.  Text blocks
// are used for embedding while table rows are retained for exact display and
// downstream extraction.
type Block struct {
	Type        string         `json:"type"` // title, paragraph, table
	Level       int            `json:"level,omitempty"`
	Text        string         `json:"text,omitempty"`
	Rows        [][]string     `json:"rows,omitempty"`
	HeadingPath []string       `json:"heading_path,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// documentSection 是解析后的逻辑段，标题和页码会保留到向量元数据中。
type documentSection struct {
	Content  string
	MetaData map[string]any
}

// Section is one source-preserving part of a document. It is shared by the
// knowledge indexer and explicit attachment parsing so page references use the
// same text extraction behavior.
type Section struct {
	Content  string
	Metadata map[string]any
}

// ParseFile extracts a supported document without chunking or indexing it.
func ParseFile(path string) ([]Section, error) {
	blocks, err := ParseBlocks(path)
	if err != nil {
		return nil, err
	}
	sections := blocksToSections(blocks)
	result := make([]Section, 0, len(sections))
	for _, section := range sections {
		result = append(result, Section{Content: section.Content, Metadata: section.MetaData})
	}
	return result, nil
}

// ParseBlocks extracts headings, paragraphs and basic tables while preserving
// source metadata. It is intentionally conservative: ambiguous layout is
// returned as text instead of being presented as a false table.
func ParseBlocks(path string) ([]Block, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".markdown":
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return parseMarkdownBlocks(string(data)), nil
	case ".html", ".htm":
		return parseHTMLBlocks(path)
	case ".docx":
		return parseDOCXBlocks(path)
	case ".pdf":
		return parsePDFBlocks(path)
	case ".txt":
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if text := cleanText(string(data)); text != "" {
			return []Block{{Type: "paragraph", Text: text}}, nil
		}
		return nil, nil
	case ".json", ".pptx":
		sections, err := parseDocument(path, ext)
		if err != nil {
			return nil, err
		}
		blocks := make([]Block, 0, len(sections))
		for _, section := range sections {
			if text := cleanText(section.Content); text != "" {
				blocks = append(blocks, Block{Type: "paragraph", Text: text, Metadata: section.MetaData})
			}
		}
		return blocks, nil
	default:
		return nil, fmt.Errorf("unsupported document format %q", ext)
	}
}

// PDFPageCount returns the declared page count before rendering work begins.
func PDFPageCount(path string) (pages int, err error) {
	defer recoverPDFPanic(&err)
	doc, err := pdf.Open(path)
	if err != nil {
		return 0, err
	}
	return doc.NumPage(), nil
}

func parseDocument(path, ext string) ([]documentSection, error) {
	switch ext {
	case ".md", ".markdown":
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
	case ".pptx":
		return parsePPTX(path)
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

func parseMarkdownBlocks(text string) []Block {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var blocks []Block
	var headingPath []string
	var paragraph []string
	var table [][]string
	inCode := false
	flushParagraph := func() {
		if value := cleanText(strings.Join(paragraph, "\n")); value != "" {
			blocks = append(blocks, Block{Type: "paragraph", Text: value, HeadingPath: append([]string(nil), headingPath...)})
		}
		paragraph = nil
	}
	flushTable := func() {
		if len(table) > 0 {
			rows := make([][]string, 0, len(table))
			for _, row := range table {
				copyRow := make([]string, len(row))
				copy(copyRow, row)
				rows = append(rows, copyRow)
			}
			blocks = append(blocks, Block{Type: "table", Rows: rows, HeadingPath: append([]string(nil), headingPath...)})
		}
		table = nil
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			flushTable()
			if inCode {
				inCode = false
			} else {
				flushParagraph()
				inCode = true
			}
			if !inCode {
				paragraph = append(paragraph, "")
			}
			continue
		}
		if !inCode && strings.HasPrefix(trimmed, "#") {
			flushTable()
			flushParagraph()
			level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			title := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			if title == "" {
				continue
			}
			if level <= len(headingPath) {
				headingPath = headingPath[:level-1]
			}
			headingPath = append(headingPath, title)
			blocks = append(blocks, Block{Type: "title", Level: level, Text: title, HeadingPath: append([]string(nil), headingPath...)})
			continue
		}
		if !inCode && strings.Count(trimmed, "|") >= 1 {
			cells := splitTableRow(trimmed)
			if isMarkdownSeparator(cells) {
				continue
			}
			flushParagraph()
			table = append(table, cells)
			continue
		}
		if !inCode && len(table) > 0 && strings.Contains(trimmed, "|") {
			table = append(table, splitTableRow(trimmed))
			continue
		}
		if trimmed == "" && !inCode {
			flushTable()
			flushParagraph()
			continue
		}
		paragraph = append(paragraph, line)
	}
	flushTable()
	flushParagraph()
	return blocks
}

func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	result := make([]string, len(parts))
	for i, part := range parts {
		result[i] = strings.TrimSpace(part)
	}
	return result
}

func isMarkdownSeparator(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		if strings.Trim(cell, "-:") != "" || !strings.Contains(cell, "-") {
			return false
		}
	}
	return true
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

func parseHTMLBlocks(path string) ([]Block, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	root, err := html.Parse(f)
	if err != nil {
		return nil, err
	}
	var blocks []Block
	var headingPath []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "nav", "footer":
				return
			}
		}
		if n.Type == html.ElementNode && strings.HasPrefix(n.Data, "h") && len(n.Data) == 2 && n.Data[1] >= '1' && n.Data[1] <= '6' {
			text := cleanText(htmlNodeText(n))
			if text != "" {
				level := int(n.Data[1] - '0')
				if level <= len(headingPath) {
					headingPath = headingPath[:level-1]
				}
				headingPath = append(headingPath, text)
				blocks = append(blocks, Block{Type: "title", Level: level, Text: text, HeadingPath: append([]string(nil), headingPath...)})
			}
			return
		}
		if n.Type == html.ElementNode && n.Data == "title" {
			text := cleanText(htmlNodeText(n))
			if text != "" {
				blocks = append(blocks, Block{Type: "title", Level: 1, Text: text, Metadata: map[string]any{"source": "html_title"}})
			}
			return
		}
		if n.Type == html.ElementNode && n.Data == "table" {
			rows := htmlTableRows(n)
			if len(rows) > 0 {
				blocks = append(blocks, Block{Type: "table", Rows: rows, HeadingPath: append([]string(nil), headingPath...)})
			}
			return
		}
		if n.Type == html.ElementNode && (n.Data == "p" || n.Data == "li" || n.Data == "blockquote" || n.Data == "pre") {
			text := cleanText(htmlNodeText(n))
			if text != "" {
				blocks = append(blocks, Block{Type: "paragraph", Text: text, HeadingPath: append([]string(nil), headingPath...)})
			}
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return blocks, nil
}

func htmlNodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
			b.WriteByte(' ')
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return b.String()
}

func htmlTableRows(table *html.Node) [][]string {
	var rows [][]string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			var cells []string
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == html.ElementNode && (child.Data == "th" || child.Data == "td") {
					cells = append(cells, cleanText(htmlNodeText(child)))
				}
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(table)
	return rows
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

func parseDOCXBlocks(path string) ([]Block, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	for _, file := range r.File {
		if file.Name != "word/document.xml" {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer stream.Close()
		decoder := xml.NewDecoder(stream)
		var blocks []Block
		var paragraph strings.Builder
		paragraphStyle := ""
		var headingPath []string
		var table [][]string
		var cell strings.Builder
		inParagraph, inCell, inTable := false, false, false
		flushParagraph := func() {
			text := cleanText(paragraph.String())
			if text == "" {
				paragraph.Reset()
				paragraphStyle = ""
				return
			}
			if level := docxHeadingLevel(paragraphStyle); level > 0 {
				if level <= len(headingPath) {
					headingPath = headingPath[:level-1]
				}
				headingPath = append(headingPath, text)
				blocks = append(blocks, Block{Type: "title", Level: level, Text: text, HeadingPath: append([]string(nil), headingPath...)})
			} else {
				blocks = append(blocks, Block{Type: "paragraph", Text: text, HeadingPath: append([]string(nil), headingPath...)})
			}
			paragraph.Reset()
			paragraphStyle = ""
		}
		flushTable := func() {
			if len(table) > 0 {
				rows := make([][]string, len(table))
				for i := range table {
					rows[i] = append([]string(nil), table[i]...)
				}
				blocks = append(blocks, Block{Type: "table", Rows: rows, HeadingPath: append([]string(nil), headingPath...)})
			}
			table = nil
		}
		for {
			token, tokenErr := decoder.Token()
			if tokenErr == io.EOF {
				break
			}
			if tokenErr != nil {
				return nil, tokenErr
			}
			switch value := token.(type) {
			case xml.StartElement:
				switch value.Name.Local {
				case "tbl":
					flushParagraph()
					inTable = true
				case "tr":
					if inTable {
						table = append(table, nil)
					}
				case "tc":
					if inTable {
						inCell = true
						cell.Reset()
					}
				case "p":
					if !inTable {
						inParagraph = true
						paragraph.Reset()
						paragraphStyle = ""
					}
				case "pStyle":
					if inParagraph && !inTable {
						for _, attr := range value.Attr {
							if attr.Name.Local == "val" {
								paragraphStyle = attr.Value
							}
						}
					}
				case "t":
					// Character data is appended below; this marker is not needed.
				case "tab":
					if inCell {
						cell.WriteByte('\t')
					} else if inParagraph {
						paragraph.WriteByte('\t')
					}
				case "br", "cr":
					if inCell {
						cell.WriteByte('\n')
					} else if inParagraph {
						paragraph.WriteByte('\n')
					}
				}
			case xml.CharData:
				if inCell {
					cell.Write([]byte(value))
				} else if inParagraph {
					paragraph.Write([]byte(value))
				}
			case xml.EndElement:
				switch value.Name.Local {
				case "tc":
					if inTable && len(table) > 0 {
						table[len(table)-1] = append(table[len(table)-1], cleanText(cell.String()))
					}
					cell.Reset()
					inCell = false
				case "tr":
				case "tbl":
					flushTable()
					inTable = false
				case "p":
					if inParagraph && !inTable {
						flushParagraph()
						inParagraph = false
					}
				}
			}
		}
		flushParagraph()
		flushTable()
		return blocks, nil
	}
	return nil, fmt.Errorf("word/document.xml not found")
}

func docxHeadingLevel(style string) int {
	style = strings.ToLower(strings.TrimSpace(style))
	for level := 1; level <= 6; level++ {
		if style == fmt.Sprintf("heading%d", level) || style == fmt.Sprintf("标题%d", level) || strings.Contains(style, fmt.Sprintf("heading %d", level)) {
			return level
		}
	}
	return 0
}

func parsePPTX(path string) ([]documentSection, error) {
	r, e := zip.OpenReader(path)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	type slideFile struct {
		name string
		file *zip.File
	}
	var slides []slideFile
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			slides = append(slides, slideFile{name: f.Name, file: f})
		}
	}
	sort.Slice(slides, func(i, j int) bool {
		number := func(name string) int {
			base := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(name), "slide"), ".xml")
			value, err := strconv.Atoi(base)
			if err != nil {
				return int(^uint(0) >> 1)
			}
			return value
		}
		left, right := number(slides[i].name), number(slides[j].name)
		if left == right {
			return slides[i].name < slides[j].name
		}
		return left < right
	})
	sections := make([]documentSection, 0, len(slides))
	for i, slide := range slides {
		stream, err := slide.file.Open()
		if err != nil {
			return nil, err
		}
		decoder := xml.NewDecoder(stream)
		var b strings.Builder
		for {
			token, tokenErr := decoder.Token()
			if tokenErr == io.EOF {
				break
			}
			if tokenErr != nil {
				_ = stream.Close()
				return nil, tokenErr
			}
			if text, ok := token.(xml.CharData); ok {
				b.Write([]byte(text))
				b.WriteByte(' ')
			}
		}
		_ = stream.Close()
		if content := cleanText(b.String()); content != "" {
			sections = append(sections, documentSection{Content: content, MetaData: map[string]any{"slide": i + 1}})
		}
	}
	if len(sections) == 0 {
		return nil, fmt.Errorf("presentation contains no readable slides")
	}
	return sections, nil
}

func parsePDF(path string) (sections []documentSection, err error) {
	defer recoverPDFPanic(&err)
	r, e := pdf.Open(path)
	if e != nil {
		return nil, e
	}
	sections = make([]documentSection, 0, r.NumPage())
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

func parsePDFBlocks(path string) (blocks []Block, err error) {
	defer recoverPDFPanic(&err)
	doc, err := pdf.Open(path)
	if err != nil {
		return nil, err
	}
	for page := 1; page <= doc.NumPage(); page++ {
		items := doc.Page(page).Content().Text
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Y == items[j].Y {
				return items[i].X < items[j].X
			}
			return items[i].Y > items[j].Y
		})
		rows := pdfRows(items)
		if len(rows) == 0 {
			continue
		}
		maxFont := 0.0
		for _, item := range items {
			if item.FontSize > maxFont {
				maxFont = item.FontSize
			}
		}
		start := 0
		if len(rows[0].items) > 0 && maxFont > 0 && rows[0].items[0].FontSize >= maxFont*1.2 {
			text := cleanText(rowText(rows[0]))
			if text != "" {
				blocks = append(blocks, Block{Type: "title", Level: 1, Text: text, Metadata: map[string]any{"page": page, "title_inferred": true}})
			}
			start = 1
		}
		if tableRows := pdfBasicTable(rows[start:]); len(tableRows) > 0 {
			blocks = append(blocks, Block{Type: "table", Rows: tableRows, Metadata: map[string]any{"page": page}})
			start += len(tableRows)
		}
		var paragraph strings.Builder
		for _, row := range rows[start:] {
			text := cleanText(rowText(row))
			if text == "" {
				continue
			}
			if paragraph.Len() > 0 {
				paragraph.WriteByte('\n')
			}
			paragraph.WriteString(text)
		}
		if text := cleanText(paragraph.String()); text != "" {
			blocks = append(blocks, Block{Type: "paragraph", Text: text, Metadata: map[string]any{"page": page}})
		}
	}
	return blocks, nil
}

type pdfRow struct {
	items []pdf.Text
}

func pdfRows(items []pdf.Text) []pdfRow {
	var rows []pdfRow
	for _, item := range items {
		if len(rows) == 0 || absFloat(rows[len(rows)-1].items[0].Y-item.Y) > item.FontSize*0.45 {
			rows = append(rows, pdfRow{items: []pdf.Text{item}})
		} else {
			rows[len(rows)-1].items = append(rows[len(rows)-1].items, item)
		}
	}
	return rows
}

func rowText(row pdfRow) string {
	var b strings.Builder
	for i, item := range row.items {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(item.S)
	}
	return b.String()
}

func pdfBasicTable(rows []pdfRow) [][]string {
	if len(rows) < 2 {
		return nil
	}
	result := make([][]string, 0, len(rows))
	var anchors []float64
	columns := 0
	for _, row := range rows {
		cells := make([]string, 0, len(row.items))
		positions := make([]float64, 0, len(row.items))
		for i, item := range row.items {
			if i > 0 && item.X-row.items[i-1].X < item.FontSize*1.5 {
				cells[len(cells)-1] += " " + item.S
			} else {
				cells = append(cells, item.S)
				positions = append(positions, item.X)
			}
		}
		if len(cells) < 2 {
			return nil
		}
		if columns == 0 {
			columns = len(cells)
			anchors = positions
		}
		if len(cells) != columns {
			return nil
		}
		for i := range positions {
			if absFloat(positions[i]-anchors[i]) > 8 {
				return nil
			}
		}
		for i := range cells {
			cells[i] = cleanText(cells[i])
		}
		result = append(result, cells)
	}
	return result
}

func absFloat(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

// recoverPDFPanic converts panics from the third-party PDF parser into normal
// parse errors. A malformed PDF must fail its attachment, never terminate the
// HTTP handler and surface as a proxy 502.
func recoverPDFPanic(errp *error) {
	if recovered := recover(); recovered != nil {
		*errp = fmt.Errorf("parse PDF: %v", recovered)
	}
}

func cleanText(text string) string {
	text = strings.ReplaceAll(text, "\x00", "")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.FieldsFunc(line, unicode.IsSpace), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
