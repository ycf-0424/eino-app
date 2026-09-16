package memory

import (
	"context"
	"fmt"
	openaiembedding "github.com/cloudwego/eino-ext/components/embedding/openai"
	"github.com/cloudwego/eino/components/model"
	"my-eino-app/internal/config"
	"my-eino-app/internal/session"
	"time"
)

func Open(ctx context.Context, cfg *config.Config, sessions *session.Store, model model.BaseChatModel) (*Engine, error) {
	if !cfg.Memory.Enabled {
		return nil, nil
	}
	if sessions.DB() == nil {
		return nil, fmt.Errorf("memory requires mysql")
	}
	r := &Repository{DB: sessions.DB(), Config: cfg.Memory, Model: cfg.OpenAI.Model}
	if cfg.Memory.ExtractorModel != "inherit_chat" && cfg.Memory.ExtractorModel != "" {
		r.Model = cfg.Memory.ExtractorModel
	}
	r.Generation = Hash(cfg.RAG.Embedding.BaseURL + "|" + cfg.RAG.Embedding.Model + "|" + fmt.Sprint(cfg.RAG.Dimension) + "|" + cfg.Memory.Collection)[:16]
	check, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, table := range []string{"memory_turns", "memory_facts", "memory_jobs", "memory_bindings", "memory_controls", "memory_versions", "memory_sources", "memory_decisions", "memory_locks"} {
		if _, err := r.DB.ExecContext(check, "SELECT 1 FROM "+table+" LIMIT 0"); err != nil {
			return nil, fmt.Errorf("memory tables missing: run cmd/session-migrate: %w", err)
		}
	}
	emb, err := openaiembedding.NewEmbedder(check, &openaiembedding.EmbeddingConfig{APIKey: cfg.RAG.Embedding.APIKey, Model: cfg.RAG.Embedding.Model, BaseURL: cfg.RAG.Embedding.BaseURL})
	if err != nil {
		return nil, err
	}
	index, err := NewMilvusIndex(check, cfg.RAG, cfg.Memory.Collection, emb)
	if err != nil {
		return nil, err
	}
	if _, err = index.vector(check, "memory dimension check"); err != nil {
		index.Close()
		return nil, err
	}
	e := &Engine{Repo: r, Extractor: &ModelExtractor{model, cfg.Memory.MaxCandidates}, Index: index}
	if err = sessions.SetTransactionHook(r.Capture); err != nil {
		index.Close()
		return nil, err
	}
	return e, nil
}
