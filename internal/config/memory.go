package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Memory configures local, automatically extracted memory. Identity comes from
// server configuration, never from model output or arbitrary request fields.
type Memory struct {
	Enabled             bool     `yaml:"enabled"`
	IdentityMode        string   `yaml:"identity_mode"`
	OwnerID             string   `yaml:"owner_id"`
	ProjectID           string   `yaml:"project_id"`
	AutoExtract         bool     `yaml:"auto_extract"`
	ExtractorModel      string   `yaml:"extractor_model"`
	EmbeddingConfig     string   `yaml:"embedding_config"`
	Collection          string   `yaml:"milvus_collection"`
	VectorizeTypes      []string `yaml:"vectorize_types"`
	ContextMessages     int      `yaml:"extraction_context_messages"`
	MaxCandidates       int      `yaml:"max_candidates"`
	MaxCandidateChars   int      `yaml:"max_candidate_chars"`
	Workers             int      `yaml:"worker_concurrency"`
	TaskTimeout         Duration `yaml:"task_timeout"`
	LeaseDuration       Duration `yaml:"lease_duration"`
	MaxAttempts         int      `yaml:"max_attempts"`
	RetryInitial        Duration `yaml:"retry_initial_delay"`
	RetryMax            Duration `yaml:"retry_max_delay"`
	TopK                int      `yaml:"retrieval_top_k"`
	MaxContextChars     int      `yaml:"max_context_chars"`
	RetentionDays       int      `yaml:"audit_retention_days"`
	TurnRetentionDays   int      `yaml:"turn_retention_days"`
	JobSuccessDays      int      `yaml:"job_success_retention_days"`
	JobFailedDays       int      `yaml:"job_failed_retention_days"`
	SourceRetentionDays int      `yaml:"source_retention_days"`
	VersionKeepCount    int      `yaml:"version_keep_count"`
	CleanupBatchSize    int      `yaml:"cleanup_batch_size"`
	CleanupInterval     Duration `yaml:"cleanup_interval"`
}

func (c *Config) ValidateMemory() error {
	m := &c.Memory
	if v := os.Getenv("MEMORY_ENABLED"); v != "" {
		b, e := strconv.ParseBool(v)
		if e != nil {
			return e
		}
		m.Enabled = b
	}
	if !m.Enabled {
		return nil
	}
	if c.Session.Store != "mysql" {
		return fmt.Errorf("memory requires session.store=mysql")
	}
	if m.IdentityMode != "local_single_user" && m.IdentityMode != "multi_user" {
		return fmt.Errorf("memory.identity_mode must be local_single_user or multi_user")
	}
	if m.IdentityMode == "multi_user" {
		// 多用户模式下 owner 来自登录态（auth.OwnerFromContext），配置里的 owner_id
		// 不再是身份来源。没有认证就没有 owner，记忆隔离会退化成「所有人共用一份」，
		// 因此这里要求 auth.enabled=true 而不是静默降级。
		if !c.Auth.Enabled {
			return fmt.Errorf("memory.identity_mode=multi_user requires auth.enabled=true")
		}
		if strings.TrimSpace(m.OwnerID) != "" {
			log.Printf("memory: identity_mode=multi_user, memory.owner_id=%q is ignored (owner comes from the logged-in session)", m.OwnerID)
		}
	}
	// project_id 在两种模式下都是必填；owner_id 只在单用户模式下作为身份来源。
	if id := strings.TrimSpace(m.ProjectID); id == "" || len(id) > 64 {
		return fmt.Errorf("memory project_id must be 1..64 bytes")
	}
	if m.IdentityMode == "local_single_user" {
		if id := strings.TrimSpace(m.OwnerID); id == "" || len(id) > 64 {
			return fmt.Errorf("memory owner_id must be 1..64 bytes")
		}
	}
	if !c.RAG.Enabled || c.RAG.Embedding.Model == "" || c.RAG.Dimension <= 0 {
		return fmt.Errorf("memory requires valid rag embedding configuration")
	}
	if m.Collection == "" {
		m.Collection = "my_eino_memory_v1"
	}
	if m.Collection == c.RAG.Milvus.Collection {
		return fmt.Errorf("memory and document collections must differ")
	}
	if m.ExtractorModel == "" {
		m.ExtractorModel = "inherit_chat"
	}
	if m.EmbeddingConfig == "" {
		m.EmbeddingConfig = "inherit_rag"
	}
	if m.EmbeddingConfig != "inherit_rag" {
		return fmt.Errorf("memory.embedding_config must be inherit_rag")
	}
	if m.ContextMessages == 0 {
		m.ContextMessages = 8
	}
	if m.MaxCandidates == 0 {
		m.MaxCandidates = 5
	}
	if m.MaxCandidateChars == 0 {
		m.MaxCandidateChars = 500
	}
	if m.Workers == 0 {
		m.Workers = 1
	}
	if m.TaskTimeout == 0 {
		m.TaskTimeout = Duration(120 * time.Second)
	}
	if m.LeaseDuration == 0 {
		m.LeaseDuration = Duration(180 * time.Second)
	}
	if m.MaxAttempts == 0 {
		m.MaxAttempts = 5
	}
	if m.RetryInitial == 0 {
		m.RetryInitial = Duration(2 * time.Second)
	}
	if m.RetryMax == 0 {
		m.RetryMax = Duration(60 * time.Second)
	}
	if m.TopK == 0 {
		m.TopK = 5
	}
	if m.MaxContextChars == 0 {
		m.MaxContextChars = 2000
	}
	if m.RetentionDays == 0 {
		m.RetentionDays = 30
	}
	if m.TurnRetentionDays == 0 {
		m.TurnRetentionDays = 30
	}
	if m.JobSuccessDays == 0 {
		m.JobSuccessDays = 7
	}
	if m.JobFailedDays == 0 {
		m.JobFailedDays = 30
	}
	if m.SourceRetentionDays == 0 {
		m.SourceRetentionDays = 90
	}
	if m.VersionKeepCount == 0 {
		m.VersionKeepCount = 3
	}
	if m.CleanupBatchSize == 0 {
		m.CleanupBatchSize = 500
	}
	if m.CleanupInterval == 0 {
		m.CleanupInterval = Duration(24 * time.Hour)
	}
	if m.Workers < 1 || m.Workers > 4 || m.ContextMessages < 1 || m.ContextMessages > 20 || m.MaxCandidates < 1 || m.MaxCandidates > 10 || m.MaxCandidateChars < 1 || m.MaxCandidateChars > 2000 || m.TopK < 1 || m.TopK > 20 || m.MaxContextChars < 1 || m.MaxContextChars > 10000 {
		return fmt.Errorf("memory limits out of range")
	}
	if m.TaskTimeout <= 0 || m.LeaseDuration <= m.TaskTimeout || m.MaxAttempts < 1 || m.MaxAttempts > 10 || m.RetryInitial <= 0 || m.RetryMax < m.RetryInitial || m.RetentionDays < 1 || m.TurnRetentionDays < 1 || m.JobSuccessDays < 1 || m.JobFailedDays < 1 || m.SourceRetentionDays < 1 || m.VersionKeepCount < 1 || m.CleanupBatchSize < 1 || m.CleanupInterval <= 0 {
		return fmt.Errorf("invalid memory timeout, lease, retry or retention configuration")
	}
	if len(m.VectorizeTypes) == 0 {
		m.VectorizeTypes = []string{"project_fact", "decision", "milestone", "todo"}
	}
	for _, t := range m.VectorizeTypes {
		switch t {
		case "project_fact", "decision", "milestone", "todo":
		default:
			return fmt.Errorf("invalid vectorize type %s", t)
		}
	}
	return nil
}
