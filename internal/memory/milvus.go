package memory

import (
	"context"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
	"my-eino-app/internal/config"
	"strconv"
	"strings"
)

type MilvusIndex struct {
	Client     client.Client
	Embedder   embedding.Embedder
	Collection string
	Dimension  int
}

func NewMilvusIndex(ctx context.Context, c config.RAG, collection string, embedder embedding.Embedder) (*MilvusIndex, error) {
	cli, e := client.NewClient(ctx, client.Config{Address: c.Milvus.Address, Username: c.Milvus.Username, Password: c.Milvus.Password})
	if e != nil {
		return nil, e
	}
	m := &MilvusIndex{cli, embedder, collection, c.Dimension}
	if e = m.ensure(ctx); e != nil {
		cli.Close()
		return nil, e
	}
	return m, nil
}
func (m *MilvusIndex) Close() error { return m.Client.Close() }
func (m *MilvusIndex) ensure(ctx context.Context) error {
	exists, e := m.Client.HasCollection(ctx, m.Collection)
	if e != nil {
		return e
	}
	if !exists {
		s := entity.NewSchema().WithName(m.Collection).WithDescription("selective memory v1")
		for _, name := range []string{"id", "memory_id", "owner_id", "scope_kind", "scope_id", "type", "content_hash", "embedding_model", "generation", "content"} {
			size := int64(256)
			if name == "content" {
				size = 16384
			}
			f := entity.NewField().WithName(name).WithDataType(entity.FieldTypeVarChar).WithMaxLength(size)
			if name == "id" {
				f.WithIsPrimaryKey(true)
			}
			s.WithField(f)
		}
		s.WithField(entity.NewField().WithName("version").WithDataType(entity.FieldTypeInt64))
		s.WithField(entity.NewField().WithName("embedding").WithDataType(entity.FieldTypeFloatVector).WithDim(int64(m.Dimension)))
		if e = m.Client.CreateCollection(ctx, s, entity.DefaultShardNumber); e != nil {
			return e
		}
	}
	coll, e := m.Client.DescribeCollection(ctx, m.Collection)
	if e != nil {
		return e
	}
	fields := map[string]*entity.Field{}
	for _, f := range coll.Schema.Fields {
		fields[f.Name] = f
	}
	for _, name := range []string{"id", "memory_id", "owner_id", "scope_kind", "scope_id", "type", "content_hash", "embedding_model", "generation", "content", "version", "embedding"} {
		if fields[name] == nil {
			return fmt.Errorf("memory collection missing %s; choose a new collection", name)
		}
	}
	if fields["embedding"].TypeParams[entity.TypeParamDim] != strconv.Itoa(m.Dimension) {
		return errors.New("memory dimension mismatch; rebuild into a new collection")
	}
	// Startup is idempotent. DescribeIndex succeeds with the existing index;
	// CreateIndex is only attempted for a newly created/unfinished collection.
	indices, describeErr := m.Client.DescribeIndex(ctx, m.Collection, "embedding")
	if describeErr != nil || len(indices) == 0 {
		idx, indexErr := entity.NewIndexAUTOINDEX(entity.COSINE)
		if indexErr != nil {
			return indexErr
		}
		if e = m.Client.CreateIndex(ctx, m.Collection, "embedding", idx, false); e != nil {
			return e
		}
	}
	return m.Client.LoadCollection(ctx, m.Collection, false)
}
func (m *MilvusIndex) vector(ctx context.Context, text string) ([]float32, error) {
	v, e := m.Embedder.EmbedStrings(ctx, []string{text})
	if e != nil {
		return nil, e
	}
	if len(v) != 1 || len(v[0]) != m.Dimension {
		return nil, errors.New("embedding dimension mismatch")
	}
	out := make([]float32, m.Dimension)
	for i, x := range v[0] {
		out[i] = float32(x)
	}
	return out, nil
}
func (m *MilvusIndex) UpsertVersion(ctx context.Context, f Fact) error {
	v, e := m.vector(ctx, f.Key+": "+f.Value)
	if e != nil {
		return e
	}
	cols := []entity.Column{}
	vals := map[string]string{"id": VectorID(f), "memory_id": f.ID, "owner_id": f.Scope.Owner, "scope_kind": f.Scope.Kind, "scope_id": f.Scope.ID, "type": f.Type, "content_hash": f.Hash, "embedding_model": f.Generation, "generation": f.Generation, "content": f.Value}
	for k, v := range vals {
		cols = append(cols, entity.NewColumnVarChar(k, []string{v}))
	}
	cols = append(cols, entity.NewColumnInt64("version", []int64{f.Version}), entity.NewColumnFloatVector("embedding", m.Dimension, [][]float32{v}))
	if _, e = m.Client.Upsert(ctx, m.Collection, "", cols...); e != nil {
		return e
	}
	return m.Client.Flush(ctx, m.Collection, false)
}
func (m *MilvusIndex) Search(ctx context.Context, scope Scope, q string, k int) ([]Hit, error) {
	if scope.Owner == "" || scope.ID == "" || (scope.Kind != "user" && scope.Kind != "project") {
		return nil, errors.New("scope required")
	}
	v, e := m.vector(ctx, q)
	if e != nil {
		return nil, e
	}
	filter := "owner_id == " + strconv.Quote(scope.Owner) + " && scope_kind == " + strconv.Quote(scope.Kind) + " && scope_id == " + strconv.Quote(scope.ID)
	sp, _ := entity.NewIndexAUTOINDEXSearchParam(1)
	results, e := m.Client.Search(ctx, m.Collection, nil, filter, nil, []entity.Vector{entity.FloatVector(v)}, "embedding", entity.COSINE, k, sp, client.WithSearchQueryConsistencyLevel(entity.ClStrong))
	if e != nil {
		return nil, e
	}
	out := []Hit{}
	for _, res := range results {
		if res.Err != nil {
			return nil, res.Err
		}
		for i := 0; i < res.ResultCount; i++ {
			id, e := res.IDs.GetAsString(i)
			if e != nil {
				return nil, e
			}
			out = append(out, Hit{id, float64(res.Scores[i])})
		}
	}
	return out, nil
}
func (m *MilvusIndex) DeleteVersions(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = strconv.Quote(id)
	}
	return m.Client.Delete(ctx, m.Collection, "", "id in ["+strings.Join(quoted, ",")+"]")
}
