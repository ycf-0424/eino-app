package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/schema"
	"github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
)

const milvusIDField = "id"

// MilvusConfig 包含创建 MilvusStore 所需的连接与 Collection 参数。
type MilvusConfig struct {
	Address, Username, Password, Collection string
	Dimension, TopK                         int
}

// MilvusStore 使用 Milvus SDK 写入向量，并通过相同的 Embedding 模型检索。
type MilvusStore struct {
	client   client.Client
	embedder embedding.Embedder
	config   MilvusConfig
}

// DeleteIDs 删除旧文档向量，避免文件删除后仍被检索到。
func (s *MilvusStore) DeleteIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	// Milvus 表达式要求 JSON 风格的逗号分隔字符串数组。
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = strconv.Quote(id)
	}
	return s.client.Delete(ctx, s.config.Collection, "", "id in ["+strings.Join(quoted, ",")+"]")
}

// Exists 返回 Milvus 中实际存在的主键，用于修复 Manifest 与向量库不一致。
func (s *MilvusStore) Exists(ctx context.Context, ids []string) (map[string]bool, error) {
	result := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := s.client.QueryByPks(ctx, s.config.Collection, nil, entity.NewColumnVarChar(milvusIDField, ids), []string{milvusIDField})
	if err != nil {
		return nil, fmt.Errorf("check milvus document ids: %w", err)
	}
	column := rows.GetColumn(milvusIDField)
	if column == nil {
		return result, nil
	}
	for i := 0; i < column.Len(); i++ {
		id, err := column.GetAsString(i)
		if err == nil {
			result[id] = true
		}
	}
	return result, nil
}

// ListIDs 列出 Milvus Collection 中的全部主键，用于清理旧版本索引留下的孤儿 chunk。
func (s *MilvusStore) ListIDs(ctx context.Context) ([]string, error) {
	count := func() (int64, error) {
		rs, err := s.client.Query(ctx, s.config.Collection, nil, "", []string{"count(*)"}, client.WithSearchQueryConsistencyLevel(entity.ClStrong))
		if err != nil {
			return 0, err
		}
		c := rs.GetColumn("count(*)")
		if c == nil || c.Len() != 1 {
			return 0, fmt.Errorf("invalid count response")
		}
		return c.GetAsInt64(0)
	}
	total, err := count()
	if err != nil {
		return nil, err
	}
	ids := []string{}
	cursor := ""
	seen := map[string]bool{}
	for {
		expr := ""
		if cursor != "" {
			expr = "id > " + strconv.Quote(cursor)
		}
		rs, err := s.client.Query(ctx, s.config.Collection, nil, expr, []string{milvusIDField}, client.WithLimit(1000), client.WithSearchQueryConsistencyLevel(entity.ClStrong))
		if err != nil {
			return nil, err
		}
		c := rs.GetColumn(milvusIDField)
		if c == nil {
			return nil, fmt.Errorf("missing primary key response")
		}
		if c.Len() == 0 {
			break
		}
		next := cursor
		for n := 0; n < c.Len(); n++ {
			id, e := c.GetAsString(n)
			if e != nil {
				return nil, e
			}
			if id <= cursor || seen[id] {
				return nil, fmt.Errorf("non-progressing primary key pagination")
			}
			seen[id] = true
			ids = append(ids, id)
			if id > next {
				next = id
			}
		}
		cursor = next
	}
	after, err := count()
	if err != nil {
		return nil, err
	}
	if int64(len(ids)) != total || after != total {
		return nil, fmt.Errorf("incomplete or concurrently modified collection: listed=%d before=%d after=%d", len(ids), total, after)
	}
	return ids, nil
}

// NewMilvusStore 连接 Milvus，并确保 Collection、向量索引和加载状态就绪。
func NewMilvusStore(ctx context.Context, cfg MilvusConfig, embedder embedding.Embedder) (*MilvusStore, error) {
	cli, err := client.NewClient(ctx, client.Config{
		Address: cfg.Address, Username: cfg.Username, Password: cfg.Password,
	})
	if err != nil {
		return nil, fmt.Errorf("connect milvus: %w", err)
	}
	if err := ensureMilvusCollection(ctx, cli, cfg); err != nil {
		_ = cli.Close()
		return nil, err
	}
	return &MilvusStore{client: cli, embedder: embedder, config: cfg}, nil
}

// Index 生成文档向量并 Upsert；稳定文档 ID 使重复入库不会不断产生副本。
func (s *MilvusStore) Index(ctx context.Context, docs []*schema.Document) ([]string, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	texts := make([]string, len(docs))
	ids := make([]string, len(docs))
	contents := make([]string, len(docs))
	metadata := make([]string, len(docs))
	for i, doc := range docs {
		ids[i], texts[i], contents[i] = doc.ID, doc.Content, doc.Content
		encoded, err := json.Marshal(doc.MetaData)
		if err != nil {
			return nil, fmt.Errorf("encode metadata: %w", err)
		}
		metadata[i] = string(encoded)
	}
	vectors64, err := s.embedder.EmbedStrings(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embed documents: %w", err)
	}
	if len(vectors64) != len(docs) {
		return nil, fmt.Errorf("embedding count mismatch")
	}
	vectors := make([][]float32, len(vectors64))
	for i, vector := range vectors64 {
		// Milvus Collection 的维度固定；更换 Embedding 模型时必须同步修改配置/Collection。
		if len(vector) != s.config.Dimension {
			return nil, fmt.Errorf("embedding dimension = %d, want %d", len(vector), s.config.Dimension)
		}
		vectors[i] = toFloat32(vector)
	}
	// Upsert 按主键新增或覆盖文档块，因此可以安全地重复执行 indexer。
	_, err = s.client.Upsert(ctx, s.config.Collection, "",
		entity.NewColumnVarChar(milvusIDField, ids),
		entity.NewColumnVarChar(contentField, contents),
		entity.NewColumnVarChar(metadataField, metadata),
		entity.NewColumnFloatVector(vectorField, s.config.Dimension, vectors),
	)
	if err != nil {
		return nil, fmt.Errorf("upsert milvus documents: %w", err)
	}
	if err := s.client.Flush(ctx, s.config.Collection, false); err != nil {
		return nil, fmt.Errorf("flush milvus collection: %w", err)
	}
	return ids, nil
}

// Search 把问题转成向量，并将 Milvus 相似度结果转换为 Eino Document。
func (s *MilvusStore) Search(ctx context.Context, query string, topK int) ([]*schema.Document, error) {
	// 查询和文档必须使用同一个 Embedding 模型，向量才处在同一语义空间。
	vectors, err := s.embedder.EmbedStrings(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vectors) != 1 || len(vectors[0]) != s.config.Dimension {
		return nil, fmt.Errorf("unexpected query embedding dimension")
	}
	if topK <= 0 {
		topK = s.config.TopK
	}
	// COSINE 用向量方向衡量文本语义相似度，分数越大通常越相关。
	sp, _ := entity.NewIndexAUTOINDEXSearchParam(1)
	results, err := s.client.Search(ctx, s.config.Collection, nil, "",
		[]string{contentField, metadataField},
		[]entity.Vector{entity.FloatVector(toFloat32(vectors[0]))},
		vectorField, entity.COSINE, topK, sp)
	if err != nil {
		return nil, fmt.Errorf("search milvus: %w", err)
	}
	var docs []*schema.Document
	for _, result := range results {
		for i := 0; i < result.ResultCount; i++ {
			id, _ := result.IDs.GetAsString(i)
			doc := &schema.Document{ID: id, MetaData: map[string]any{}}
			for _, field := range result.Fields {
				value, valueErr := field.GetAsString(i)
				if valueErr != nil {
					continue
				}
				switch field.Name() {
				case contentField:
					doc.Content = value
				case metadataField:
					_ = json.Unmarshal([]byte(value), &doc.MetaData)
				}
			}
			if i < len(result.Scores) {
				doc.WithScore(float64(result.Scores[i]))
			}
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

func ensureMilvusCollection(ctx context.Context, cli client.Client, cfg MilvusConfig) error {
	exists, err := cli.HasCollection(ctx, cfg.Collection)
	if err != nil {
		return fmt.Errorf("check milvus collection: %w", err)
	}
	if !exists {
		// 首次启动时创建主键、原文、元数据和向量四个字段。
		s := entity.NewSchema().WithName(cfg.Collection).WithDescription("my-eino-app knowledge base").
			WithField(entity.NewField().WithName(milvusIDField).WithDataType(entity.FieldTypeVarChar).WithIsPrimaryKey(true).WithMaxLength(512)).
			WithField(entity.NewField().WithName(contentField).WithDataType(entity.FieldTypeVarChar).WithMaxLength(65535)).
			WithField(entity.NewField().WithName(metadataField).WithDataType(entity.FieldTypeVarChar).WithMaxLength(65535)).
			WithField(entity.NewField().WithName(vectorField).WithDataType(entity.FieldTypeFloatVector).WithDim(int64(cfg.Dimension)))
		if err := cli.CreateCollection(ctx, s, entity.DefaultShardNumber); err != nil {
			return fmt.Errorf("create milvus collection: %w", err)
		}
		index, err := entity.NewIndexAUTOINDEX(entity.COSINE)
		if err != nil {
			return fmt.Errorf("create milvus index definition: %w", err)
		}
		if err := cli.CreateIndex(ctx, cfg.Collection, vectorField, index, false); err != nil {
			return fmt.Errorf("create milvus vector index: %w", err)
		}
	} else {
		// 已有 Collection 必须和当前 Embedding 维度一致，否则检索结果不可用。
		collection, err := cli.DescribeCollection(ctx, cfg.Collection)
		if err != nil {
			return fmt.Errorf("describe milvus collection: %w", err)
		}
		matched := false
		for _, field := range collection.Schema.Fields {
			if field.Name != vectorField {
				continue
			}
			dimension, parseErr := strconv.Atoi(field.TypeParams[entity.TypeParamDim])
			if parseErr != nil || dimension != cfg.Dimension {
				return fmt.Errorf("milvus vector dimension = %d, want %d", dimension, cfg.Dimension)
			}
			matched = true
		}
		if !matched {
			return fmt.Errorf("milvus collection is missing %q field", vectorField)
		}
	}
	// Collection 必须加载到 Milvus QueryNode 后才能执行向量搜索。
	if err := cli.LoadCollection(ctx, cfg.Collection, false); err != nil {
		return fmt.Errorf("load milvus collection: %w", err)
	}
	return nil
}

func toFloat32(input []float64) []float32 {
	// Eino Embedding 返回 float64，而 Milvus FloatVector 接收 float32。
	output := make([]float32, len(input))
	for i, value := range input {
		output[i] = float32(value)
	}
	return output
}

func (s *MilvusStore) Close() error { return s.client.Close() }
