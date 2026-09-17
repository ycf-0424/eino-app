package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/cloudwego/eino/schema"
	"os"
	"path/filepath"
)

// Manifest 记录文档块哈希，避免重复生成 Embedding。
type Manifest struct {
	Version        int               `json:"version,omitempty"`
	EmbeddingModel string            `json:"embedding_model,omitempty"`
	Dimension      int               `json:"dimension,omitempty"`
	ChunkSize      int               `json:"chunk_size,omitempty"`
	ChunkOverlap   int               `json:"chunk_overlap,omitempty"`
	Store          string            `json:"store,omitempty"`
	Target         string            `json:"target,omitempty"`
	Files          map[string]string `json:"files"`
}

// ManifestOptions 标识一次索引生成方式。生成方式变化时必须重新计算所有向量。
type ManifestOptions struct {
	EmbeddingModel string
	Dimension      int
	ChunkSize      int
	ChunkOverlap   int
	Store          string
	Target         string
}

// Compatible 判断清单是否由同一套索引参数生成。
func (m Manifest) Compatible(options ManifestOptions) bool {
	return m.Version == 1 && m.EmbeddingModel == options.EmbeddingModel && m.Dimension == options.Dimension &&
		m.ChunkSize == options.ChunkSize && m.ChunkOverlap == options.ChunkOverlap &&
		m.Store == options.Store && m.Target == options.Target
}

// LoadManifest 函数。
func LoadManifest(path string) (Manifest, error) {
	m := Manifest{Files: map[string]string{}}
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return m, nil
	}
	if e != nil {
		return m, e
	}
	e = json.Unmarshal(b, &m)
	if m.Files == nil {
		m.Files = map[string]string{}
	}
	return m, e
}

// SaveManifest 函数。
func SaveManifest(path string, m Manifest) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	b, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return e
	}
	t := path + ".tmp"
	if e = os.WriteFile(t, b, 0600); e != nil {
		return e
	}
	return os.Rename(t, path)
}

// ChangedDocs 返回新增或内容发生变化的块。
func ChangedDocs(docs []*schema.Document, m Manifest) ([]*schema.Document, Manifest) {
	return ChangedDocsWithConfig(docs, m, "", 0, 0, 0)
}

// ChangedDocsWithConfig 除了比较 chunk 内容，也比较索引参数，避免换模型或切分规则后静默跳过。
func ChangedDocsWithConfig(docs []*schema.Document, m Manifest, embeddingModel string, dimension, chunkSize, chunkOverlap int) ([]*schema.Document, Manifest) {
	return ChangedDocsWithOptions(docs, m, ManifestOptions{EmbeddingModel: embeddingModel, Dimension: dimension, ChunkSize: chunkSize, ChunkOverlap: chunkOverlap})
}

// ChangedDocsWithOptions 使用内容哈希和索引指纹计算增量文档。
func ChangedDocsWithOptions(docs []*schema.Document, m Manifest, options ManifestOptions) ([]*schema.Document, Manifest) {
	n := Manifest{Version: 1, EmbeddingModel: options.EmbeddingModel, Dimension: options.Dimension, ChunkSize: options.ChunkSize, ChunkOverlap: options.ChunkOverlap, Store: options.Store, Target: options.Target, Files: map[string]string{}}
	var c []*schema.Document
	configChanged := !m.Compatible(options)
	for _, d := range docs {
		if d == nil {
			continue
		}
		h := sha256.Sum256([]byte(d.Content))
		v := hex.EncodeToString(h[:])
		n.Files[d.ID] = v
		if configChanged || m.Files[d.ID] != v {
			c = append(c, d)
		}
	}
	return c, n
}
