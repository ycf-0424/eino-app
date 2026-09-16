// Package execution 定义「实时执行进度」的事件模型、投递接口与执行记录存储。
//
// 设计要点：
//   - 事件与正文共用同一 run 级 sequence，排序不依赖约定，而是由同一发射器保证。
//   - 事件通过 context 流到工具中间件和工具本体，工具不直接依赖 WebSocket。
//   - Summary 只用于展示；断言、入库和自动验证一律读取 Payload 中的结构化字段。
package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

// newEventID 生成唯一事件 ID。
func newEventID() string { return uuid.NewString() }

// Type 是执行事件类型。
type Type string

const (
	// RunStarted 在一轮请求建立执行上下文后立即发出，早于模型首字。
	RunStarted Type = "run_started"
	// ModelWaiting 在真正调用模型之前发出，用于区分「等待模型」和「等待工具」。
	ModelWaiting Type = "model_waiting"
	// ToolStarted 在工具真正执行前发出。
	ToolStarted Type = "tool_started"
	// ToolCompleted 是普通工具的正常终态。
	ToolCompleted Type = "tool_completed"
	// ToolFailed 是工具的超时/取消以外的失败终态。
	ToolFailed Type = "tool_failed"
	// SkillPreloaded 表示本轮把某个技能正文直接注入提示词。
	SkillPreloaded Type = "skill_preloaded"
	// SkillLoaded 表示模型通过 load_skills 实际读取了技能正文。
	SkillLoaded Type = "skill_loaded"
	// FileReadDone 表示本地文件读取成功；越权失败仍是 ToolFailed。
	FileReadDone Type = "file_read_completed"
	// ApprovalRequired 表示工具因审批中断暂停，不是失败。
	ApprovalRequired Type = "approval_required"
	// RunCompleted、RunFailed、RunCancelled 是一轮 run 的终态，每个 run 至多一个。
	RunCompleted Type = "run_completed"
	RunFailed    Type = "run_failed"
	RunCancelled Type = "run_cancelled"
	// Chunk 是正文增量，与执行记录分开，但共用同一 sequence 计数器。
	Chunk Type = "chunk"
)

// Version 是当前事件信封版本。消费端遇到更高版本或未知类型时应安全忽略。
const Version = 1

var knownTypes = map[Type]bool{
	RunStarted: true, ModelWaiting: true, ToolStarted: true, ToolCompleted: true,
	ToolFailed: true, SkillPreloaded: true, SkillLoaded: true, FileReadDone: true,
	ApprovalRequired: true, RunCompleted: true, RunFailed: true, RunCancelled: true,
	Chunk: true,
}

// Known 报告事件类型是否由当前版本定义。
func (t Type) Known() bool { return knownTypes[t] }

// Terminal 报告事件是否为一轮 run 的终态。
func (t Type) Terminal() bool {
	return t == RunCompleted || t == RunFailed || t == RunCancelled
}

// Execution 报告事件是否属于执行记录（正文 chunk 不算）。
func (t Type) Execution() bool { return t != Chunk && t.Known() }

// Event 是统一事件信封。Payload 中的字段是唯一可信的结构化契约。
type Event struct {
	Version    int            `json:"version"`
	EventID    string         `json:"event_id"`
	RunID      string         `json:"run_id"`
	SessionID  string         `json:"session_id,omitempty"`
	Sequence   int64          `json:"sequence"`
	OccurredAt time.Time      `json:"occurred_at"`
	Type       Type           `json:"type"`
	Summary    string         `json:"summary,omitempty"` // 仅展示，不得用于断言
	Payload    map[string]any `json:"payload,omitempty"` // 断言与入库以此为准
	Content    string         `json:"content,omitempty"` // 仅 chunk
	// ReasoningContent 是推理模型单独返回的思考增量；仅用于 chunk/实时传输，
	// 不混入最终 Answer，避免前端把思考误当成正式回答。
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// Dropped 标记此前有中间事件被丢弃（队列背压），前端应凭 after_sequence 补取。
	Dropped bool `json:"dropped,omitempty"`
}

// Supported 报告事件是否可被当前版本安全消费。
func (e Event) Supported() bool { return e.Version <= Version && e.Type.Known() }

// CompletionTypeFor 按工具名把成功结果分流到更精确的事件类型。
func CompletionTypeFor(toolName string) Type {
	switch toolName {
	case "load_skills":
		return SkillLoaded
	case "local_file_read":
		return FileReadDone
	default:
		return ToolCompleted
	}
}

// DigestArgs 返回工具参数的短摘要，避免把原始参数写入事件。
// 空参数返回空串，调用方据此决定是否展示。
func DigestArgs(arguments string) string {
	if arguments == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(arguments))
	return hex.EncodeToString(sum[:6])
}

// WithoutChunks 过滤掉正文增量，供 HTTP 同步接口返回纯执行事件。
func WithoutChunks(events []Event) []Event {
	out := make([]Event, 0, len(events))
	for _, ev := range events {
		if ev.Type != Chunk {
			out = append(out, ev)
		}
	}
	return out
}

// CountExecution 统计执行事件数量（不含正文）。
func CountExecution(events []Event) int {
	count := 0
	for _, ev := range events {
		if ev.Type.Execution() {
			count++
		}
	}
	return count
}
