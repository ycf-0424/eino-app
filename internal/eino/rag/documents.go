package rag

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/schema"
)

var supportedDocumentExtensions = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".html": true, ".htm": true,
	".json": true, ".docx": true, ".pdf": true, ".pptx": true,
}

// LoadDocuments 递归读取知识库目录，将所有支持的格式转换为 Eino Document。
func LoadDocuments(dir string, chunkSize, overlap int) ([]*schema.Document, error) {
	if chunkSize <= 0 || overlap < 0 || overlap >= chunkSize {
		return nil, fmt.Errorf("invalid chunk configuration: size=%d overlap=%d", chunkSize, overlap)
	}
	var docs []*schema.Document
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() == ".index-manifest.json" {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !supportedDocumentExtensions[ext] {
			return nil
		}
		blocks, err := ParseBlocks(path)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		sections := blocksToSections(blocks)
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = path
		}
		source := filepath.ToSlash(rel)
		chunkIndex := 0
		for _, section := range sections {
			for _, content := range SplitText(section.Content, chunkSize, overlap) {
				meta := map[string]any{"source": source, "chunk": chunkIndex, "format": strings.TrimPrefix(ext, ".")}
				for key, value := range section.MetaData {
					meta[key] = value
				}
				docs = append(docs, &schema.Document{
					// 相对路径加块序号构成稳定 ID，移动整个知识库目录不会导致重复记录。
					ID: fmt.Sprintf("%s-%d", stableID(source), chunkIndex), Content: content, MetaData: meta,
				})
				chunkIndex++
			}
		}
		return nil
	})
	return docs, err
}

func blocksToSections(blocks []Block) []documentSection {
	var sections []documentSection
	var current *documentSection
	var blockTypes []string
	var tables [][][]string
	flush := func() {
		if current == nil {
			return
		}
		current.Content = cleanText(current.Content)
		if current.Content != "" {
			if len(blockTypes) > 0 {
				current.MetaData["block_types"] = append([]string(nil), blockTypes...)
			}
			if len(tables) > 0 {
				current.MetaData["tables"] = append([][][]string(nil), tables...)
			}
			sections = append(sections, *current)
		}
		current = nil
		blockTypes = nil
		tables = nil
	}
	for _, block := range blocks {
		if block.Type == "title" {
			flush()
			meta := map[string]any{"title": block.Text, "title_level": block.Level, "block_type": "title"}
			for key, value := range block.Metadata {
				meta[key] = value
			}
			current = &documentSection{Content: block.Text, MetaData: meta}
			blockTypes = append(blockTypes, block.Type)
			continue
		}
		if current == nil {
			current = &documentSection{MetaData: map[string]any{}}
		}
		if existing, ok := current.MetaData["block_type"].(string); !ok || existing == "" {
			current.MetaData["block_type"] = block.Type
		} else if existing != block.Type {
			current.MetaData["block_type"] = "mixed"
		}
		for key, value := range block.Metadata {
			current.MetaData[key] = value
		}
		if block.HeadingPath != nil && len(block.HeadingPath) > 0 {
			current.MetaData["heading_path"] = append([]string(nil), block.HeadingPath...)
		}
		if block.Type == "table" {
			current.MetaData["has_table"] = true
			rows := make([][]string, len(block.Rows))
			for i := range block.Rows {
				rows[i] = append([]string(nil), block.Rows[i]...)
			}
			tables = append(tables, rows)
			var table strings.Builder
			for _, row := range block.Rows {
				if table.Len() > 0 {
					table.WriteByte('\n')
				}
				table.WriteString(strings.Join(row, " | "))
			}
			if table.Len() > 0 {
				current.Content += "\n" + table.String()
			}
		} else if block.Text != "" {
			current.Content += "\n" + block.Text
		}
		blockTypes = append(blockTypes, block.Type)
	}
	flush()
	return sections
}

// SplitText 使用 rune 切分以避免截断中文，重叠区用于保留跨块上下文。
func SplitText(text string, size, overlap int) []string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 || size <= 0 || overlap < 0 || overlap >= size {
		return nil
	}
	step := size - overlap
	var chunks []string
	for start := 0; start < len(runes); start += step {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
	}
	return chunks
}

func stableID(value string) string {
	value = strings.ReplaceAll(filepath.ToSlash(value), "/", "-")
	value = strings.ReplaceAll(value, ":", "")
	return strings.Trim(value, "-.")
}
