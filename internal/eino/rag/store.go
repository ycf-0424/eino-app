// Package rag 实现与具体向量数据库解耦的知识库能力。
package rag

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// Store 是 RAG 上层唯一依赖的接口；切换 Redis/Milvus 时 Agent 无需修改。
type Store interface {
	Index(ctx context.Context, docs []*schema.Document) ([]string, error)
	Search(ctx context.Context, query string, topK int) ([]*schema.Document, error)
}

// Deleter 是支持清理已删除文档块的可选扩展。
type Deleter interface {
	DeleteIDs(ctx context.Context, ids []string) error
}

// Exister 用于索引对账：Manifest 记录存在不代表向量库中的记录仍然存在。
type Exister interface {
	Exists(ctx context.Context, ids []string) (map[string]bool, error)
}

// Lister 用于发现向量库中的孤儿记录，避免历史 Manifest 之外的旧 chunk 残留。
type Lister interface {
	ListIDs(ctx context.Context) ([]string, error)
}
