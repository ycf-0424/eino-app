// Package memory implements selective, durable conversation memory.
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

const PromptVersion = "memory-v1"

type Scope struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Owner string `json:"owner"`
}
type Message struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Text string `json:"text"`
}
type ExtractInput struct {
	Message     Message   `json:"message"`
	Context     []Message `json:"context"`
	Existing    []Fact    `json:"existing"`
	CurrentTime string    `json:"current_time"`
}
type Candidate struct {
	Type      string     `json:"type"`
	ScopeKind string     `json:"scope_kind"`
	Key       string     `json:"key"`
	Value     string     `json:"value"`
	SourceID  string     `json:"source_message_id"`
	Evidence  string     `json:"evidence"`
	Relation  string     `json:"relation"`
	ExpiresAt *time.Time `json:"expires_at"`
}
type Fact struct {
	ID         string     `json:"id"`
	Scope      Scope      `json:"scope"`
	Type       string     `json:"type"`
	Key        string     `json:"key"`
	Value      string     `json:"value"`
	Hash       string     `json:"-"`
	Version    int64      `json:"version"`
	State      string     `json:"state"`
	IndexState string     `json:"index_state"`
	Generation string     `json:"generation"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastTurn   int64      `json:"-"`
	Sources    []string   `json:"sources,omitempty"`
}
type Job struct {
	ID, Kind, Target, Generation, Token string
	// Owner 与 Project 来自 job 行本身。worker 是全局的，可能领到任意用户的
	// job，处理时必须按 job 自带的归属派生 Repository，不能用服务端配置里的 owner。
	Owner, Project string
	Version        int64
	Attempts       int
}
type Turn struct {
	ID, SessionID, State string
	Seq                  int64
	Suppressed           bool
	Input                ExtractInput
}
type Hit struct {
	ID    string
	Score float64
}
type Extractor interface {
	Extract(context.Context, ExtractInput) ([]Candidate, error)
}
type MemoryIndex interface {
	UpsertVersion(context.Context, Fact) error
	Search(context.Context, Scope, string, int) ([]Hit, error)
	DeleteVersions(context.Context, []string) error
}

func Hash(s string) string   { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func VectorID(f Fact) string { return f.ID + "_" + itoa(f.Version) + "_" + f.Generation }
