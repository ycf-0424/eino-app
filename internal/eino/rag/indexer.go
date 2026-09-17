package rag

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/gofrs/flock"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

// IndexOptions 描述一次知识库索引任务使用的输入和向量目标。
type IndexOptions struct {
	DocumentDir    string
	ManifestPath   string
	EmbeddingModel string
	Dimension      int
	ChunkSize      int
	ChunkOverlap   int
	Store          string
	Target         string
}

// IndexReport 是一次索引任务的可审计结果。
type IndexReport struct {
	TotalChunks    int       `json:"total_chunks"`
	ChangedChunks  int       `json:"changed_chunks"`
	MissingChunks  int       `json:"missing_chunks"`
	IndexedChunks  int       `json:"indexed_chunks"`
	RemovedChunks  int       `json:"removed_chunks"`
	ManifestPath   string    `json:"manifest_path"`
	EmbeddingModel string    `json:"embedding_model"`
	Store          string    `json:"store"`
	Target         string    `json:"target"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
}

// Indexer 统一 cmd/indexer 和 HTTP 管理接口的入库流程。
type Indexer struct {
	store   Store
	options IndexOptions
	mu      sync.Mutex
}

func NewIndexer(store Store, options IndexOptions) (*Indexer, error) {
	if store == nil {
		return nil, fmt.Errorf("indexer requires store")
	}
	if options.DocumentDir == "" {
		return nil, fmt.Errorf("indexer requires document directory")
	}
	if options.ManifestPath == "" {
		options.ManifestPath = filepath.Join(options.DocumentDir, ".index-manifest.json")
	}
	return &Indexer{store: store, options: options}, nil
}

// Run 执行解析、增量判断、向量写入、删除和清单保存。
// 同一个 Indexer 串行运行，防止两个管理请求交叉更新 Manifest。
func (i *Indexer) Run(ctx context.Context) (report IndexReport, err error) {
	return i.run(ctx, false)
}

// RunForce 重新计算当前知识目录中的全部向量，但仍使用稳定 ID Upsert，不会自动删除整个 Collection。
func (i *Indexer) RunForce(ctx context.Context) (report IndexReport, err error) {
	return i.run(ctx, true)
}

func (i *Indexer) run(ctx context.Context, force bool) (report IndexReport, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	lockName := fmt.Sprintf("eino-index-%x.lock", sha256.Sum256([]byte(i.options.Store+":"+i.options.Target)))
	processLock := flock.New(filepath.Join(os.TempDir(), lockName))
	locked, lockErr := processLock.TryLockContext(ctx, 100*time.Millisecond)
	if lockErr != nil {
		return report, lockErr
	}
	if !locked {
		return report, fmt.Errorf("index target is locked")
	}
	defer processLock.Unlock()
	report.StartedAt = time.Now().UTC()
	report.ManifestPath = i.options.ManifestPath
	report.EmbeddingModel = i.options.EmbeddingModel
	report.Store = i.options.Store
	report.Target = i.options.Target
	defer func() { report.FinishedAt = time.Now().UTC() }()

	docs, err := LoadDocuments(i.options.DocumentDir, i.options.ChunkSize, i.options.ChunkOverlap)
	if err != nil {
		return report, fmt.Errorf("load documents: %w", err)
	}
	report.TotalChunks = len(docs)
	previous, err := LoadManifest(i.options.ManifestPath)
	if err != nil {
		return report, fmt.Errorf("load manifest: %w", err)
	}
	options := ManifestOptions{EmbeddingModel: i.options.EmbeddingModel, Dimension: i.options.Dimension, ChunkSize: i.options.ChunkSize, ChunkOverlap: i.options.ChunkOverlap, Store: i.options.Store, Target: i.options.Target}
	changed, next := ChangedDocsWithOptions(docs, previous, options)
	if force {
		changed = append([]*schema.Document(nil), docs...)
	}
	report.ChangedChunks = len(changed)

	changedIDs := make(map[string]bool, len(changed))
	for _, doc := range changed {
		if doc != nil {
			changedIDs[doc.ID] = true
		}
	}
	// Manifest 记录存在不代表向量库仍有记录；对未变化块做一次实际存在性对账。
	if exister, ok := i.store.(Exister); ok && len(changed) < len(docs) {
		candidateIDs := make([]string, 0, len(docs)-len(changed))
		for _, doc := range docs {
			if doc != nil && !changedIDs[doc.ID] {
				candidateIDs = append(candidateIDs, doc.ID)
			}
		}
		present, existsErr := exister.Exists(ctx, candidateIDs)
		if existsErr != nil {
			return report, fmt.Errorf("reconcile vector store: %w", existsErr)
		}
		for _, doc := range docs {
			if doc != nil && !changedIDs[doc.ID] && !present[doc.ID] {
				changed = append(changed, doc)
				changedIDs[doc.ID] = true
				report.MissingChunks++
			}
		}
	}
	// Never delete unknown collection rows. Only the previous manifest proves
	// ownership; other importers and memories may share an older collection.
	ids, err := i.store.Index(ctx, changed)
	if err != nil {
		return report, fmt.Errorf("index documents: %w", err)
	}
	report.IndexedChunks = len(ids)

	if deleter, ok := i.store.(Deleter); ok && previous.Store == options.Store && previous.Target == options.Target {
		removed := make([]string, 0)
		for id := range previous.Files {
			if _, exists := next.Files[id]; !exists {
				removed = append(removed, id)
			}
		}
		if err := deleter.DeleteIDs(ctx, removed); err != nil {
			return report, fmt.Errorf("delete stale documents: %w", err)
		}
		report.RemovedChunks += len(removed)
	}
	if err := SaveManifest(i.options.ManifestPath, next); err != nil {
		return report, fmt.Errorf("save manifest: %w", err)
	}
	return report, nil
}

// Options 返回只读副本，供管理接口展示。
func (i *Indexer) Options() IndexOptions { return i.options }
