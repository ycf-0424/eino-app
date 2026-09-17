package memory

import (
	"context"
	openaiembedding "github.com/cloudwego/eino-ext/components/embedding/openai"
	"my-eino-app/internal/config"
	modelset "my-eino-app/internal/eino/model"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveModelMilvus(t *testing.T) {
	if os.Getenv("MEMORY_LIVE") != "1" {
		t.Skip("set MEMORY_LIVE=1 and MEMORY_INTEGRATION=1")
	}
	r, s := integrationRepo(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.OpenAI.MaxCompletionTokens = 2048
	cm, err := modelset.NewChatModel(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	emb, err := openaiembedding.NewEmbedder(ctx, &openaiembedding.EmbeddingConfig{Model: cfg.RAG.Embedding.Model, BaseURL: cfg.RAG.Embedding.BaseURL, APIKey: cfg.RAG.Embedding.APIKey})
	if err != nil {
		t.Fatal(err)
	}
	collection := "memory_test_" + strings.ReplaceAll(r.Config.OwnerID, "-", "")
	idx, err := NewMilvusIndex(ctx, cfg.RAG, collection, emb)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	defer idx.Client.DropCollection(context.Background(), collection)
	e := &Engine{Repo: r, Index: idx, Extractor: &ModelExtractor{cm, 5}}
	saveTurn(t, s, "你好，谢谢你的帮助。")
	drain(t, e)
	fs, err := r.List(ctx)
	if err != nil || len(fs) != 0 {
		t.Fatalf("smalltalk %+v %v", fs, err)
	}
	saveTurn(t, s, "我们项目已经正式决定使用 Milvus 作为向量数据库。")
	drain(t, e)
	fs, err = r.List(ctx)
	if err != nil || len(fs) != 1 || !strings.Contains(fs[0].Value, "Milvus") || fs[0].IndexState != "indexed" {
		t.Fatalf("decision %+v %v", fs, err)
	}
	hits, err := idx.Search(ctx, r.scope("project"), "项目的向量数据库是什么", 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("real vector search %+v %v", hits, err)
	}
	wrong := r.scope("project")
	wrong.ID = "different-project"
	hits, err = idx.Search(ctx, wrong, "Milvus", 5)
	if err != nil || len(hits) != 0 {
		t.Fatalf("scope filter %+v %v", hits, err)
	}
	saveTurn(t, s, "之前的选型说错了，我们项目现在改用 PostgreSQL 作为向量数据库。")
	drain(t, e)
	fs, err = r.List(ctx)
	if err != nil || len(fs) != 1 || !strings.Contains(fs[0].Value, "PostgreSQL") || fs[0].Version != 2 {
		t.Fatalf("correction %+v %v", fs, err)
	}
	saveTurn(t, s, "忘掉之前保存的项目向量数据库选型。")
	drain(t, e)
	fs, err = r.List(ctx)
	if err != nil || len(fs) != 0 {
		t.Fatalf("forget %+v %v", fs, err)
	}
	hits, err = idx.Search(ctx, r.scope("project"), "数据库", 5)
	if err != nil || len(hits) != 0 {
		t.Fatalf("delete %+v %v", hits, err)
	}
}
