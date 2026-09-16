# P8 代码实现方案

> 配套 `docs/ROADMAP.md` 的 P8 章节，只做设计、不实施。文中行号对应当前代码。
> 默认 `execution_events.enabled: false`，关闭时行为必须与 P7 完全一致。

## 0. 一句话思路

新增 `internal/execution` 包承载「事件模型 + 投递接口」；事件通过 **context** 流到工具中间件与工具本体；正文 `chunk` 与执行事件**共用同一个 run 级 sequence 和同一个写队列**；工具只「标注」结构化结果，事件统一由中间件/Agent 发出，避免重复上报。

## 1. 新增包 `internal/execution`

### 1.1 `event.go` — 事件模型

```go
package execution

type Type string

const (
	RunStarted       Type = "run_started"
	ModelWaiting     Type = "model_waiting"
	ToolStarted      Type = "tool_started"
	ToolCompleted    Type = "tool_completed"
	ToolFailed       Type = "tool_failed"
	SkillPreloaded   Type = "skill_preloaded"
	SkillLoaded      Type = "skill_loaded"
	FileReadDone     Type = "file_read_completed"
	ApprovalRequired Type = "approval_required"
	RunCompleted     Type = "run_completed"
	RunFailed        Type = "run_failed"
	RunCancelled     Type = "run_cancelled"
	Chunk            Type = "chunk" // 正文增量，与执行记录分开
)

const Version = 1

type Event struct {
	Version    int            `json:"version"`
	EventID    string         `json:"event_id"`
	RunID      string         `json:"run_id"`
	SessionID  string         `json:"session_id,omitempty"`
	Sequence   int64          `json:"sequence"`
	OccurredAt time.Time      `json:"occurred_at"`
	Type       Type           `json:"type"`
	Summary    string         `json:"summary,omitempty"` // 仅展示
	Payload    map[string]any `json:"payload,omitempty"` // 断言/入库以此为准
	Content    string         `json:"content,omitempty"` // 仅 chunk
}

func (t Type) Terminal() bool { return t == RunCompleted || t == RunFailed || t == RunCancelled }

// 完成事件按工具名分流：load_skills → skill_loaded；local_file_read → file_read_completed；其余 → tool_completed
func completionTypeFor(tool string) Type
```

要点：`Summary` 与 `Payload` 的分工写进注释；`Payload` 只放允许披露的字段。

### 1.2 `emitter.go` — 投递与标注

```go
type Emitter interface {
	RunID() string
	SessionID() string
	NextSequence() int64 // 单调递增，正文与事件共用同一计数器
	Emit(Event)           // 内部补齐 version/event_id/sequence/occurred_at
}
```

实现：
- `Recorder`：加锁切片，HTTP 同步路径用；`Events() []Event`。
- `Nop`：开关关闭或控制台用。
- `Session`：会话级发射器，持有 `Store`（持久化）与 `Sink`（WS 队列）。

context 传递 + 工具标注：

```go
// 每轮开始（service.Chat）创建一次，随 runner.Run(ctx,...) 一路传下去。
func NewScope(ctx context.Context) (context.Context, *Scope)
func WithEmitter(ctx context.Context, e Emitter) context.Context
func FromContext(ctx context.Context) Emitter // 缺省返回 Nop，永不 nil

// 工具只标注结构化结果，不直接发事件，避免与中间件重复上报。
func Annotate(ctx context.Context, kv map[string]any)
func MergeAnnotations(ctx context.Context, into map[string]any)
```

关键：**Scope 必须在调用工具之前就存在于 ctx 上**，否则工具里的 `Annotate` 与中间件读到的不是同一份数据。`MergeAnnotations` 在中间件读完即清空。

### 1.3 `queue.go` — WebSocket 有界队列（对应 P8-3 背压）

- 容量默认 256，单一写循环消费。
- `Emit` 非阻塞 `select`；队列满 → 丢中间事件并置 `dropped`，下一条事件带 `dropped=true`，前端凭 `after_sequence` 补取。
- 终态事件（`Terminal()`）**必须先入队**：先清空队列再强制入队；仍失败则关闭连接并记日志，绝不静默丢弃。

### 1.4 `store.go` / `mysql.go` / `file.go` — 持久化（对应 P8-4）

```go
type Store interface {
	StartRun(ctx context.Context, run Run) error
	LinkRunMessage(ctx context.Context, runID string, userSeq, assistantSeq int64) error
	FinishRun(ctx context.Context, runID, status string) error
	AppendEvents(ctx context.Context, runID string, events []Event) error
	ListEvents(ctx context.Context, sessionID string, afterSequence int64, limit int) ([]Event, error)
	DeleteBySession(ctx context.Context, sessionID string) error
	RecoverStale(ctx context.Context, olderThan time.Duration) (int, error)
}
```

- MySQL 实现复用 `internal/session` 的 `*sql.DB`。
- 文件实现写 `data/executions/<sessionID>.jsonl`（append-only），`ListEvents` 顺序扫描，`DeleteBySession` 删文件。**本地默认 `session.store: file`，不实现文件后端则 P8 无法在本地验收。**
- `RecoverStale`：启动时把 `status='running'` 且超时的 run 置为 `failed{error_code:"interrupted"}`，保证刷新后不永久显示「运行中」。

## 2. 逐文件改动清单

| 文件 | 动作 | 要点 |
|------|------|------|
| `internal/execution/*.go` | 新增 | 见 §1 |
| `internal/agent/middleware.go` | 改 | 挂 Emitter，发 started/completed/failed，识别超时/取消/中断 |
| `internal/agent/agent.go` | 改 | 三处 Agent 都挂 middleware；chunk 走 Emitter；发 model_waiting/approval_required/终态；占位消息写 run_id |
| `internal/skill/runtime.go` | 改 | `Runtime` 返回预加载技能名，由 service 发 `skill_preloaded` |
| `internal/tool/local_file.go` | 改 | 成功时 Annotate 结构化结果；新增哨兵错误供中间件归类 |
| `internal/tool/knowledge.go` | 改 | 命中时 Annotate `hit_count/top_score/sources` |
| `internal/server/service.go` | 改 | 建 run、注入 Emitter、关联消息序号、写终态、恢复审批续用同 run |
| `internal/server/http.go` | 改 | HTTP 同步路径返回事件数组；新增 `/sessions/{id}/execution` |
| `internal/server/ws.go` | 新增 | 单写者 + 读循环分离 + cancel 协议 |
| `internal/checkpoint/store.go` | 改 | `PendingApproval` 增加 `RunID` |
| `internal/config/config.go` | 改 | 新增 `ExecutionEvents` 配置与默认值 |
| `config.yaml` / `config.docker.yaml` | 改 | 增加 `execution_events` 段（默认 false） |
| `internal/session/migrations/002_execution.sql` | 新增 | 两张表 + 外键级联 |
| `internal/server/web/app.js` `index.html` `app.css` | 改 | 执行面板、回放、补取、stop 改 cancel |
| `cmd/console/main.go` | 改 | 适配签名（传 `execution.Nop()`） |
| `docs/API.md` `docs/MYSQL.md` | 改 | 补新接口与表 |

### 2.1 `internal/agent/middleware.go`

```go
func safeToolMiddleware(debug bool, em execution.Emitter, agentName string) compose.ToolMiddleware {
	return compose.ToolMiddleware{Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			toolCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			callID := uuid.NewString()
			started := time.Now()
			em.Emit(execution.Event{Type: execution.ToolStarted, Payload: map[string]any{
				"tool_name": input.Name, "tool_call_id": callID, "agent_name": agentName,
				"args_digest": execution.DigestArgs(input.Arguments),
			}})
			output, err := next(toolCtx, input)
			if debug { /* 保留原有审计日志 */ }
			switch {
			case err == nil:
				payload := map[string]any{
					"tool_name": input.Name, "tool_call_id": callID,
					"agent_name": agentName, "duration_ms": time.Since(started).Milliseconds(),
				}
				execution.MergeAnnotations(ctx, payload) // 合并工具标注的 file_name/hit_count/...
				em.Emit(execution.Event{Type: completionTypeFor(input.Name), Payload: payload})
			case execution.IsInterrupt(err):
				// 审批中断：不补 completed、也不记 failed；由 agent.collect 发 approval_required
			case errors.Is(toolCtx.Err(), context.DeadlineExceeded):
				em.Emit(execution.Event{Type: execution.ToolFailed, Payload: map[string]any{
					"tool_name": input.Name, "tool_call_id": callID, "error_code": "timeout"}})
			case ctx.Err() != nil:
				// 用户取消/断线：交给取消链路，不再补发 failed
			default:
				em.Emit(execution.Event{Type: execution.ToolFailed, Payload: map[string]any{
					"tool_name": input.Name, "tool_call_id": callID,
					"error_code": toolset.ErrorCode(err),
					"error_message": observability.Redact(err.Error())}})
			}
			return output, err
		}
	}}
}
```

### 2.2 `internal/agent/agent.go`

- `ChatAgent` 增加 `em execution.Emitter`、`runID string`。
- `NewWithInstruction`（现 51 行）增加 emitter 参数；三处 `ToolsConfig` 都挂 middleware（现只有根 Agent 挂了）：

```go
root:      Tools: tools,                ToolCallMiddlewares: []compose.ToolMiddleware{safeToolMiddleware(debug, em, "xinghe-system-assistant")}
knowledge: Tools: extraTools,           ToolCallMiddlewares: []compose.ToolMiddleware{safeToolMiddleware(debug, em, "knowledge-agent")}
writer:    Tools: skillReadTools(extraTools), ToolCallMiddlewares: []compose.ToolMiddleware{safeToolMiddleware(debug, em, "writer-agent")}
```

- `AskTo`：
  - `runner.Run` 之前发 `model_waiting{model, attempt}`；
  - `writer` 由 `os.Stdout`/`socketWriter` 改为 `execution.ChunkWriter{em}`，`Write` 内部 `em.NextSequence()+em.Emit(chunk)`——这样正文和事件天然同序列。
- `beginReply`（现 149 行）：占位消息 `Extra` 增加 `"run_id": a.runID`，供前端/§2.6 关联。
- `collect`（现 235–245 行审批分支）：发 `approval_required{tool_name, tool_call_id=interrupt.ID}`。
- `finishReply`（现 121 行）按 status 发终态：`completed`→`run_completed`、`failed`→`run_failed`、`cancelled`→`run_cancelled`、`awaiting_approval`→**不发终态**。
- `ResumeApprovalTo`：沿用同一 `runID`，最终发终态。

### 2.3 `internal/skill/runtime.go`

```go
func (l *Loader) Runtime(preferred string) (instruction string, t tool.InvokableTool, preloaded []string, err error)
```

`preloaded = [preferred]`（命中时）。service 拿到后发 `skill_preloaded{skill_names}`。
`load_skills` 闭包内（现 48–71 行）返回前标注：

```go
execution.Annotate(ctx, map[string]any{"skill_names": namesOf(loaded), "requested_names": input.Names})
```

中间件据工具名 `load_skills` → 发 `skill_loaded`。

### 2.4 `internal/tool/local_file.go`

- `read` 签名 `(_ context.Context, ...)` → `(ctx context.Context, ...)`，成功分支标注：

```go
execution.Annotate(ctx, map[string]any{
	"file_name": filepath.Base(resolved), "file_ext": filepath.Ext(resolved),
	"bytes": len(text), "truncated": false,
})
```

- 新增哨兵错误 + `ErrorCode(err) string`（供 middlewar 归类 `error_code`）：

```go
var (
	ErrOutsideRoots = errors.New("file is outside the allowed local directories or does not exist")
	ErrTooLarge     = errors.New("file is too large")
	ErrNotText      = errors.New("file is not UTF-8 text")
)
```

### 2.5 `internal/tool/knowledge.go`

```go
if len(docs) == 0 {
	execution.Annotate(ctx, map[string]any{"hit_count": 0})
	return "No relevant knowledge was found.", nil
}
execution.Annotate(ctx, map[string]any{
	"hit_count": len(docs), "top_score": docs[0].Score(), "sources": sourcesOf(docs),
})
```

`sourcesOf` 只取 `filepath.Base(fmt.Sprint(doc.MetaData["source"]))`，不泄露绝对路径。模型侧返回文本保持不变。

### 2.6 `internal/server/service.go`

```go
// Chat 入口，拿到 session 锁之后：
runID := uuid.NewString()
ctx, scope := execution.NewScope(ctx)
sink := s.newSink(runID, id, writer)   // WS→Queue；HTTP→Recorder；关闭→Nop
ctx = execution.WithEmitter(ctx, execution.NewSession(runID, id, s.store, sink))
if s.store != nil { s.store.StartRun(ctx, execution.Run{RunID: runID, SessionID: id, StartedAt: time.Now()}) }
em.Emit(run_started{query_chars: utf8.RuneCountInString(query), skill_preferred: skillName})
...
chat.SetEmitter(em); chat.SetRunID(runID)
err = chat.AskTo(ctx, query, writer)
// 关联消息序号：userSeq = len(history)-2，assistantSeq = len(history)-1（首轮为 0/1）
if s.store != nil && len(chat.History()) >= 2 { s.store.LinkRunMessage(...) }
if s.store != nil { s.store.FinishRun(runID, statusOf(err, approval)) }
```

- `Approve` 从 `checkpoints.LoadApproval` 取 `RunID` 续用同一个 run（见 §2.8）。
- `ChatResult` 增加 `Events []execution.Event`，供 HTTP 同步返回。
- **注意坑**：`execution_runs.conversation_id` 外键要求 `conversations` 行已存在，而该行是首次 `session.Save` 时 `INSERT IGNORE` 才建的。首轮 `run_started` 早于 Save → `StartRun` 必须先 `INSERT IGNORE INTO conversations(id) VALUES(?)`。

### 2.7 `internal/server/http.go` + 新增 `internal/server/ws.go`

- `handleChat`/`handleApproval`（现 62/84 行）：sink 用 `Recorder`，把 `result.Events` 放进 `data.events`；正文仍取 buffer。
- 新增路由：`GET /sessions/{id}/execution?after_sequence=N&limit=M`。
- WebSocket 重写为**单写者 + 读循环分离**（现 152–182 行是「读一条 → 同步跑完 → 再读」）：

```go
// wsWriter：唯一 goroutine 串行 WriteJSON，有界队列 + 写超时
// 读循环：chat 请求 → runCh；{"type":"cancel"} → cancel 当前 turn
// 运行：per-turn ctx, cancel := context.WithCancel(baseCtx)
```

- 出站帧：正文沿用 `{"type":"chunk",...}` 并补 `run_id`/`sequence`；事件用 `{"type":"event","event":{...}}`；新增 `{"type":"cancel"}` 入站。
- 写失败 / 断线：cancel 当前 run → 尽力发 `run_cancelled{reason:"disconnect"}` → 关闭。

### 2.8 `internal/checkpoint/store.go`

`PendingApproval` 增加 `RunID string \`json:"run_id"\``，`SaveApproval` 时由 service 写入。旧文件缺字段时视为空（重开新 run，并在 `docs/API.md` 说明）。

### 2.9 `internal/session/migrations/002_execution.sql`（新增）

```sql
CREATE TABLE IF NOT EXISTS execution_runs (
  run_id             VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  conversation_id    VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  user_sequence      BIGINT NULL,
  assistant_sequence BIGINT NULL,
  status             VARCHAR(24) NOT NULL,
  started_at         DATETIME(6) NOT NULL,
  finished_at        DATETIME(6) NULL,
  INDEX idx_runs_conversation (conversation_id, started_at),
  CONSTRAINT fk_runs_conversation FOREIGN KEY (conversation_id)
    REFERENCES conversations(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS execution_events (
  event_id    VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  run_id      VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  sequence    BIGINT NOT NULL,
  type        VARCHAR(40) NOT NULL,
  occurred_at DATETIME(6) NOT NULL,
  payload     JSON NOT NULL,
  UNIQUE KEY uk_events_run_sequence (run_id, sequence),
  CONSTRAINT fk_events_run FOREIGN KEY (run_id)
    REFERENCES execution_runs(run_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

不改写 `001_sessions.sql`；迁移用专用账号执行。

### 2.10 `internal/config/config.go` + `config.yaml`

```go
type ExecutionEvents struct {
	Enabled         bool `yaml:"enabled"`
	QueueSize       int  `yaml:"queue_size"`
	RetentionDays   int  `yaml:"retention_days"`
	MaxEventsPerRun int  `yaml:"max_events_per_run"`
}
```

`Validate()` 在 `Enabled` 时补默认：`QueueSize=256`、`RetentionDays=7`、`MaxEventsPerRun=5000`。

```yaml
# P8 实时执行进度与执行记录；默认关闭，关闭时行为与 P7 完全一致
execution_events:
  enabled: false
  queue_size: 256
  retention_days: 7
  max_events_per_run: 5000
```

### 2.11 `internal/server/web/*`

- `messageNode`（app.js:62）为 assistant 建可折叠 `execPanel`，挂到返回对象的 `execPanel` 字段。
- `onmessage`（app.js:167）新增分支：

```js
} else if (message.type === "event") {
  if (message.event.sequence > state.lastSeq) { state.lastSeq = message.event.sequence; renderEvent(panel, message.event); }
}
```

- `openSession`（app.js:229）：拉完消息再拉 `/sessions/{id}/execution`，按 `run_id`/`assistant_sequence` 把面板挂到对应助手消息；无事件则 `hidden`。
- 停止生成：`ui.composer` 分支（app.js:342）由 `closeSocket()` 改为 `state.socket.send(JSON.stringify({type:"cancel"}))`，断线兜底不变。
- 渲染一律 `textContent`（现 172 行已是），**绝不 innerHTML**；参数只显示摘要。
- 重连：按 run 记 `lastSeq`，重连后 `GET /sessions/{id}/execution?after_sequence=lastSeq` 补取。

### 2.12 `cmd/console/main.go`

`:112` 改三返回值；`:118` 传 `execution.Nop()`。

## 3. 两个必须先验证的技术点

1. **审批中断能否从中间件的 `next()` 错误里识别**。用最小用例触发 `write_note` 中断，在 middleware 里打印 `%T`。若 Eino 有 `compose.IsInterruptError` 之类判定就用它；没有就用保守策略（err 非超时非取消 → 不发任何工具终态），审批事件由 `collect` 兜底。
2. **`event.Action.Interrupted` 能否拿到 `tool_call_id` / `agent_name`**。`ApprovalInfo` 只有 `ToolName`、`ArgumentsInJSON`；agent_name 可能要从 supervisor 事件层级推断，取不到**不要伪造**（P8-0 的展示边界）。

## 4. 实施顺序（对应 P8-1…P8-7）

1. **P8-1 / P8-7**：`internal/execution`（event/emitter/scope/queue/nop）+ config 开关。先不接线，单测：sequence 单调、终态唯一、脱敏、未知 type 忽略、Queue 背压。
2. **P8-2**：middleware 三处挂载 + 工具 Annotate + `skill.Runtime` 返回值。用 `Recorder` 单测事件序列。
3. **P8-3**：service 注入 Emitter；`ws.go` 单写者 + 读循环分离 + cancel；HTTP 同步返回 events。
4. **P8-4**：002 迁移 + mysql/file Store + 关联消息 + 终态写库 + 删会话级联 + `RecoverStale`。
5. **P8-5**：前端面板 + 回放 + 补取 + stop 改 cancel。
6. **P8-6**：全链路验收。

## 5. 验收自检（对应 P8-6）

- 事件按 `(run_id, sequence)` 唯一有序；每 run 至多一个终态。
- 开关关闭：`go test -p 1 ./...`、`/chat`、原 Web UI 与 P7 一致（无回归）。
- 慢模型下 `run_started` 先于首段正文到达。
- 时间工具 / 文件成功 / 文件越权失败 / 零命中检索 / 多技能 / 重复工具调用 / 子 Agent 调用均准确显示，且**子 Agent 与根 Agent 不重复上报**。
- 审批恢复事件连贯、沿用同 `run_id`；取消、断线重连、多 Agent 并发不串会话。
- 刷新后执行记录关联正确消息；删会话后无孤儿 run/event；故障恢复后不永久显示「运行中」。
- 一轮约 20 次工具调用的事件量与首字延迟差可量化。
