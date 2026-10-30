# Agent Runtime 增强方案

本方案把 DeepSeek Harness 中最值得吸收的三类设计，落到当前 Eino 企业智能助手的现有代码上：

1. 持久化运行时：让一次 Agent 任务可以恢复、回放、审计和定位。
2. 通用 Agent Loop：让任务执行拥有明确的预算、步骤、重试和完成判定。
3. MCP 与外部能力接入：让外部工具和资源通过统一协议进入现有权限、审批、Owner 隔离和执行审计体系。

方案采用增量改造。保留当前的 RAG、项目长期记忆、Supervisor + knowledge-agent + writer-agent、MySQL/Milvus 和 HTTP/WebSocket 接口，不把 Go 服务整体重写成插件运行时。

## 一、当前基础与目标差距

当前项目已经具备：

- `execution.Run`、`execution.Event`、MySQL/文件执行记录和按 `run_id + sequence` 回放；
- Agent Checkpoint、审批中断与恢复；
- Token 估算、上下文预算、历史压缩和摘要；
- 工具超时、错误降级、权限根目录、Owner 隔离和审计事件；
- Supervisor、knowledge-agent、writer-agent 三个 Agent 角色；
- RAG、Milvus/Redis、长期记忆、附件和多模态预处理。

当前主要缺口：

- `run` 基本等同于一轮请求，没有稳定的 `turn / step / attempt` 层级；
- 模型请求、实际上下文、工具 Schema 和失败尝试没有统一的可恢复记录；
- 工具预算、步骤预算、Token 预算和任务完成判定分散在不同层；
- 外部工具只能逐个编写 Go Tool，没有 MCP 统一接入层；
- 现有事件偏向实时进度，尚未成为完整的 Agent Session Log。

目标运行结构：

```text
session
  └─ turn
      ├─ step
      │   ├─ attempt
      │   │   ├─ model_request / context_snapshot
      │   │   ├─ tool_call / tool_result
      │   │   └─ retry / failure
      │   └─ step_end
      └─ turn_end
```

## 二、总体设计原则

### 1. 现有 `run_id` 继续兼容

第一阶段不改变外部 API 中的 `run_id` 含义。现有一轮 HTTP/WebSocket 请求仍创建一个 `run_id`，内部增加：

- `turn_id`：用户一次目标任务的稳定标识；通常第一版与 `run_id` 一对一，后续允许同一 Turn 跨多个 Run 恢复；
- `step_id`：一次模型请求及其工具调用集合；
- `attempt_id`：同一 Step 的第几次模型尝试或重试。

这样旧客户端只看 `run_id` 仍可正常工作，新客户端和回放器可以使用完整层级。

### 2. 模型可见内容必须可追溯

任何进入模型请求的内容都必须能从持久化记录重建，包括：

- 模型和路由信息；
- 系统提示词版本、Skill 版本和工具 Schema 版本；
- 历史消息选择结果；
- RAG、长期记忆、附件和预检索上下文；
- 模型请求参数和工具调用参数；
- 模型响应、工具结果、压缩和截断决策。

默认不把密码、Token、原始文件路径、完整敏感参数和完整附件正文写入普通执行事件。需要恢复完整模型输入时，使用受控的快照对象，并按 Owner、保留期和脱敏策略管理。

### 3. 运行控制集中在 Loop

Agent、Tool、RAG 和 MCP Provider 提供能力；预算、停止原因、重试和完成判定由 Loop 统一处理。避免每个工具自行实现一套循环和超时规则。

### 4. MCP 只负责连接，不绕过安全边界

MCP 工具必须经过现有工具中间件、Owner 注入、权限判断、审批、超时、事件记录和结果大小限制。MCP Server 不能自行获得项目根目录、用户身份或内部凭据。

## 三、持久化运行时方案

### 3.1 运行对象

新增运行时类型，放在 `internal/execution`：

```go
type Turn struct {
    TurnID       string
    RunID        string
    SessionID    string
    Goal         string
    Status       string
    StopReason   string
    StartedAt    time.Time
    FinishedAt   *time.Time
}

type Step struct {
    StepID       string
    TurnID       string
    Index        int
    Status       string
    StopReason   string
    StartedAt    time.Time
    FinishedAt   *time.Time
}

type Attempt struct {
    AttemptID    string
    StepID       string
    Index        int
    ModelID      string
    Status       string
    StopReason   string
    StartedAt    time.Time
    FinishedAt   *time.Time
}
```

第一版可以把 `Run` 作为 Turn 的外部兼容包装；不要立即把现有 `Run` 删除或重命名。

### 3.2 事件扩展

保留当前 `execution.Event` 信封和 `sequence`，增加可选结构化字段：

```go
type Event struct {
    Version    int
    EventID    string
    RunID      string
    SessionID  string
    TurnID     string
    StepID     string
    AttemptID  string
    Sequence   int64
    OccurredAt time.Time
    Type       Type
    Payload    map[string]any
    Content    string
}
```

建议新增事件：

| 事件 | 用途 |
|---|---|
| `turn_started` / `turn_completed` / `turn_failed` / `turn_cancelled` | 记录用户目标级任务生命周期 |
| `step_started` / `step_completed` / `step_failed` | 记录一次模型请求及工具集合 |
| `attempt_started` / `attempt_completed` / `attempt_failed` | 区分首次调用、重试、模型切换和取消 |
| `model_request_recorded` | 记录模型、参数、提示词版本和上下文快照引用 |
| `context_compacted` | 记录历史压缩、RAG 截断、工具结果裁剪和摘要 |
| `tool_call_recorded` / `tool_result_recorded` | 记录稳定的工具身份、参数摘要和结果引用 |
| `loop_budget_exceeded` | 记录达到 Step、Tool、Token 或时间预算 |
| `completion_checked` | 记录任务完成判定及未完成原因 |
| `human_handoff_required` | 记录需要人工接管的节点 |
| `mcp_server_state_changed` | 记录 MCP 发现、重连、失效和工具集合变更 |

事件 `Payload` 必须只放结构化、脱敏字段。大对象通过快照引用保存，不直接塞入事件 JSON。

### 3.3 数据表

在现有 `002_execution.sql` 之后新增迁移，例如 `007_agent_runtime.sql`：

```sql
CREATE TABLE agent_turns (
  turn_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  conversation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  goal TEXT NOT NULL,
  status VARCHAR(24) NOT NULL,
  stop_reason VARCHAR(64) NULL,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NULL,
  INDEX idx_agent_turns_conversation(conversation_id, started_at),
  CONSTRAINT fk_agent_turns_run FOREIGN KEY(run_id) REFERENCES execution_runs(run_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE agent_steps (
  step_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  turn_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  step_index INT NOT NULL,
  status VARCHAR(24) NOT NULL,
  stop_reason VARCHAR(64) NULL,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NULL,
  UNIQUE KEY uk_agent_steps_turn_index(turn_id, step_index),
  CONSTRAINT fk_agent_steps_turn FOREIGN KEY(turn_id) REFERENCES agent_turns(turn_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE agent_attempts (
  attempt_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  step_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  attempt_index INT NOT NULL,
  model_id VARCHAR(128) NOT NULL,
  status VARCHAR(24) NOT NULL,
  stop_reason VARCHAR(64) NULL,
  input_tokens INT NULL,
  output_tokens INT NULL,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NULL,
  UNIQUE KEY uk_agent_attempts_step_index(step_id, attempt_index),
  CONSTRAINT fk_agent_attempts_step FOREIGN KEY(step_id) REFERENCES agent_steps(step_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE agent_snapshots (
  snapshot_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  turn_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  attempt_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  kind VARCHAR(32) NOT NULL,
  schema_version INT NOT NULL,
  content_json JSON NOT NULL,
  content_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  redaction_version VARCHAR(32) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  expires_at DATETIME(6) NULL,
  INDEX idx_agent_snapshots_turn(turn_id, created_at),
  CONSTRAINT fk_agent_snapshots_turn FOREIGN KEY(turn_id) REFERENCES agent_turns(turn_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

实现时可以先只建 `agent_turns`、`agent_steps`、`agent_attempts`，快照表在确认敏感数据保留策略后启用。快照必须支持：哈希校验、Owner 校验、过期清理和按会话删除。

### 3.4 恢复与回放

恢复流程：

1. 根据 `session_id` 找到未完成 Turn；
2. 找到最后一个未完成 Step/Attempt；
3. 校验模型、工具、Skill 和 MCP 能力版本仍然存在；
4. 从最后一个已提交事件重建 Loop 状态；
5. 对未收到终态的模型调用标记为 `interrupted`，不能假设成功；
6. 从安全的恢复点重新发起 Attempt；
7. 生成新的 `attempt_id`，保留旧 Attempt 作为失败历史。

回放模式只读执行事件和快照，不调用模型、不执行工具。回放输出应包含：步骤时间线、工具调用、失败/重试、审批、Token 和停止原因。

### 3.5 持久化阶段的受影响文件

- `internal/execution/event.go`：层级字段、事件类型、版本兼容；
- `internal/execution/store.go`、`mysql.go`、`file.go`：Turn/Step/Attempt 和快照接口；
- `internal/session/migrations/007_agent_runtime.sql`：新表；
- `internal/eino/agent/agent.go`：创建 Step/Attempt、恢复和停止原因；
- `internal/eino/agent/middleware.go`：工具调用关联 Attempt 和策略结果；
- `internal/server/service.go`：请求创建 Turn、恢复未完成任务、接入快照；
- `internal/server/http.go`：新增回放和运行详情接口；
- `docs/API.md`：接口契约和权限说明。

## 四、通用 Agent Loop 方案

### 4.1 Loop 状态机

```text
TurnStarted
  → StepStarted
      → AttemptStarted
          → ModelRequest
          → ToolCall* / ToolResult*
          → AttemptCompleted | AttemptFailed | AttemptCancelled
      → CompletionChecked
      → StepCompleted
  → 继续下一 Step
  → TurnCompleted
```

任何状态都可以转为：

- `awaiting_approval`：等待人工审批；
- `human_handoff_required`：需要人工接管；
- `budget_exceeded`：预算耗尽；
- `cancelled`：用户取消或连接断开；
- `failed`：不可恢复错误。

### 4.2 预算模型

新增 `internal/eino/agent/budget.go`，每个 Turn 创建不可变的预算快照：

```go
type Budget struct {
    MaxSteps             int
    MaxToolCalls         int
    MaxAttemptsPerStep   int
    MaxInputTokens       int
    MaxOutputTokens      int
    MaxTotalTokens       int
    MaxWallTime          time.Duration
    MaxToolTime          time.Duration
    MaxParallelTools     int
}
```

预算消耗必须由服务端计算，不能由模型声明。每次模型请求前检查输入预算，每次模型返回后累加实际/估算 Token，每次工具开始前检查工具次数、并发和时间预算。

建议默认值先采用保守配置：

```yaml
agent:
  loop:
    max_steps: 8
    max_tool_calls: 20
    max_attempts_per_step: 2
    max_total_tokens: 32000
    max_wall_time: 180s
    max_parallel_tools: 2
    completion_check: deterministic
```

### 4.3 重试策略

重试只针对可恢复错误：网络暂时失败、模型 5xx、明确的限流、可恢复工具输入错误。以下情况不得自动重试：权限拒绝、审批拒绝、用户取消、越权访问、预算耗尽、未知写操作结果。

每次重试都生成新的 `attempt_id`，保存：

- 原 Attempt 的错误码；
- 重试原因；
- 延迟和次数；
- 是否更换模型；
- 是否改变上下文或工具集合。

现有 `internal/eino/model/retry.go` 保留为传输层重试；Loop 层新增任务级重试，两者不能重复计算预算。

### 4.4 任务完成判定

第一版不让模型自由返回“完成”。采用三层判定：

1. **结构判定**：模型是否产生最终回答、是否仍有未处理工具调用；
2. **执行判定**：必须完成的工具是否成功、是否存在审批未决；
3. **用户目标判定**：请求是否属于需要文件、知识库、实时数据或执行结果的任务。

后续再增加可配置的 `CompletionPolicy`：

```go
type CompletionPolicy interface {
    Check(ctx context.Context, state LoopState) (Decision, error)
}
```

完成判定失败时只能输出“未完成及原因”，不能把模型的自我陈述当成执行证据。

### 4.5 人工接管

把现有审批扩展成三种明确状态：

- `approval_required`：只批准一个具体工具调用；
- `clarification_required`：缺少任务参数，等待用户补充；
- `human_handoff_required`：任务交由人工处理，Loop 停止自动推进。

所有接管点都写入 Checkpoint 和 Session/Execution 事件，恢复时必须校验 Owner、Turn、Step、Attempt 和目标工具一致。

### 4.6 Loop 阶段验收

- 连续工具调用达到最大次数时返回 `budget_exceeded`；
- 模型失败重试后仍失败，能区分 `attempt_failed` 与 `turn_failed`；
- 用户取消不会被记录为模型失败；
- 审批恢复不会重复执行已经成功的工具；
- 达到 Token/时间预算时不再发起新的模型请求；
- 回放能还原每个 Step 和 Attempt 的顺序；
- 同一请求在 HTTP、WebSocket、CLI 三种入口使用同一 Loop 规则。

## 五、MCP 与外部能力接入方案

### 5.1 目标范围

第一版只实现 MCP Client，不实现 MCP Server。支持：

- `stdio`：本机受控 MCP 进程；
- `Streamable HTTP`：远程 MCP 服务；
- 工具发现与工具 Schema；
- `tools/call`；
- 资源发现和按需读取；
- 连接超时、调用超时、取消、重连和工具集合更新。

暂不实现：MCP Prompt Template、Task Extension、任意远程进程执行和无审批写操作。

### 5.2 内部抽象

新增 `internal/mcp`：

```go
type ServerConfig struct {
    Name              string
    Transport         string
    Command           string
    Args              []string
    URL               string
    Env               map[string]string
    AllowedTools      []string
    AllowedResources  []string
    Timeout           time.Duration
    Enabled           bool
}

type Client interface {
    ListTools(ctx context.Context) ([]Tool, error)
    CallTool(ctx context.Context, name string, args json.RawMessage) (Result, error)
    ListResources(ctx context.Context, cursor string) (ResourcePage, error)
    ReadResource(ctx context.Context, uri string) (Resource, error)
    Close() error
}

type Registry interface {
    Register(ctx context.Context, cfg ServerConfig) error
    Tools(ctx context.Context, owner string) []ExternalTool
    ResourceServers(ctx context.Context, owner string) []string
}
```

模型可见工具使用稳定名称：

```text
mcp__<server>__<tool>
```

外部 Server 的原始名称只用于协议调用，不能直接作为全局工具名。

### 5.3 MCP 安全边界

每个 MCP Server 必须经过：

1. 配置校验：名称、传输方式、命令/URL、超时和工具白名单；
2. Owner/租户作用域检查；
3. 环境变量清洗，禁止继承 `KEY`、`PASSWORD`、`SECRET`、`TOKEN` 等敏感环境变量；
4. `stdio` 进程使用受控工作目录和现有执行超时；
5. MCP 工具统一进入 `safeToolMiddleware`；
6. 写操作默认需要审批；
7. 结果限制最大字节数、最大图片数、最大资源深度；
8. Server 指令、资源文本和工具描述只能作为不可信资料，不能覆盖系统规则；
9. 每次发现、调用、失败、重连和工具集合变更都写执行事件。

MCP 凭据只来自环境变量或外部 Secret，不写入 `config.yaml`、Session、事件 Payload 或快照正文。

### 5.4 配置示例

```yaml
mcp:
  enabled: false
  servers:
    - name: github
      transport: stdio
      command: npx
      args: ["-y", "@modelcontextprotocol/server-github"]
      env:
        GITHUB_PERSONAL_ACCESS_TOKEN: "${GITHUB_PERSONAL_ACCESS_TOKEN}"
      allowed_tools: ["search_repositories", "get_file_contents"]
      allowed_resources: []
      timeout: 30s
      enabled: false
```

默认关闭。第一批只启用一个只读 Server，例如项目文档或 Git 仓库读取服务；不要从一开始接入可写 GitHub、邮件或生产系统。

### 5.5 MCP 与现有能力的关系

| 能力 | 现有实现 | MCP 后的处理 |
|---|---|---|
| 项目文档 RAG | Milvus/Redis | 保留，作为项目内部事实主来源 |
| 本地文件读取 | `local_file_read` | 保留，MCP 不能绕过授权根目录 |
| 结构化查询 | `structured_query` | 保留，业务数据仍优先使用服务端注册查询 |
| 写笔记/DOCX | 审批工具 | 保留现有审批；MCP 写工具也必须走同一策略 |
| 外部工具 | 目前逐个 Go Tool | 优先通过 MCP 接入，稳定后再决定是否下沉为原生 Tool |
| 外部资源 | 无统一接口 | 通过 `mcp-resources` 风格的按需读取工具接入 |

### 5.6 MCP 验收

- 无 MCP 配置时，模型看不到 MCP 工具和资源提示；
- Server 启动失败不会影响核心聊天，除非配置为 required；
- 两个 Server 可暴露同名原始工具而不冲突；
- 工具列表更新采用整代替换，失败时保留上一代工具；
- MCP 工具调用有 `tool_started/completed/failed` 和 Server 名称；
- 越权、未白名单工具、超时、`isError`、超大结果均能明确失败；
- MCP 工具写操作会触发审批；
- Server 指令不能改变系统 Prompt、Owner 或工具权限；
- 重启后工具命名、权限和 Session 回放保持稳定；
- 使用 mock MCP Server 完成单测、集成测试和断线重连测试。

## 六、分阶段实施计划

### 阶段 A：运行时数据模型和事件兼容

目标：不改变用户行为，先建立 Turn/Step/Attempt 字段和事件版本兼容。

主要改动：

- 新增 `agent_turns`、`agent_steps`、`agent_attempts`；
- 扩展事件信封并保持旧事件可读；
- 让现有 `run_started`、`model_waiting`、工具事件和终态事件自动带层级 ID；
- 增加 `execution` 详情查询和事件回放测试。

验收：全量 Go 测试通过；旧客户端可继续显示原事件；新接口可看到层级关系。

### 阶段 B：上下文快照和回放

目标：模型请求、工具 Schema、RAG/记忆前缀和压缩决策可以恢复。

主要改动：

- 增加快照接口、哈希、脱敏和过期清理；
- 记录 `model_request_recorded`、`context_compacted`；
- 增加只读回放服务；
- 对敏感内容默认只记录引用和摘要，提供管理员受控的完整快照开关。

验收：进程在模型调用前、工具调用中、审批等待中和模型失败时退出，重启后都能解释当前状态；回放不触发副作用。

### 阶段 C：通用 Loop 和预算

目标：将 Agent 的步数、工具次数、Token、时间、重试和完成判定统一收口。

主要改动：

- 新增 Loop 状态机和 Budget；
- 区分传输层重试和任务层重试；
- 增加 `completion_check` 和 `human_handoff_required`；
- 统一 CLI、HTTP、WebSocket 入口；
- 增加 Token Meter 和工具结果裁剪指标。

验收：预算门禁、重试、取消、审批恢复、完成判定和人工接管覆盖固定测试集。

### 阶段 D：MCP 只读接入

目标：接入一个只读 MCP Server，不影响现有 RAG 和工具权限。

主要改动：

- 新增 `internal/mcp` Client/Registry/Adapter；
- 增加配置、工具命名、连接生命周期和资源读取；
- 所有 MCP 工具复用现有 middleware、事件、Owner 和审计；
- 默认关闭，增加 mock Server。

验收：发现、调用、断线、重连、工具列表变更、越权、超时和超大结果全部有测试。

### 阶段 E：Subagent、Goal/Todo 和外部写操作

目标：在 Loop 稳定后扩展通用子 Agent 和任务管理，再按业务需要接入可写 MCP。

前置条件：阶段 A–D 的回放、预算和权限测试全部通过。

## 七、接口与运维计划

建议新增受保护接口：

```text
GET  /sessions/{id}/runs/{run_id}
GET  /sessions/{id}/runs/{run_id}/timeline
GET  /sessions/{id}/runs/{run_id}/replay
POST /sessions/{id}/runs/{run_id}/resume
POST /sessions/{id}/runs/{run_id}/cancel
GET  /mcp/servers
POST /mcp/servers/{name}/refresh
```

所有接口都必须先校验当前 Owner；`replay` 只读；`resume` 必须使用原会话和 Checkpoint；MCP 管理接口只允许管理员。

新增指标：

- `agent_turn_duration_seconds`；
- `agent_step_total`；
- `agent_attempt_total`；
- `agent_retry_total`；
- `agent_budget_exceeded_total`；
- `agent_context_tokens`；
- `agent_tool_calls_total`；
- `agent_replay_failures_total`；
- `mcp_server_ready`；
- `mcp_tool_call_total`；
- `mcp_reconnect_total`。

## 八、测试和验收矩阵

### 单元测试

- ID 层级、状态转换和终态唯一性；
- 事件版本兼容、重复事件幂等和顺序；
- Budget 计算和边界；
- 重试分类；
- 完成判定；
- MCP 工具名、Schema、结果和错误映射。

### 集成测试

- MySQL 迁移、恢复和 Owner 隔离；
- 进程中断后的 Run/Turn/Step/Attempt 恢复；
- 审批暂停和恢复不重复执行；
- RAG、记忆、附件上下文快照可回放；
- mock MCP Server 断线重连和工具集合更新；
- HTTP、WebSocket、CLI 事件一致。

### 安全测试

- MCP Server 不能读取未授权目录；
- MCP 工具不能伪造 Owner；
- Server 指令不能覆盖系统规则；
- 凭据不进入日志、事件、Session 和快照；
- 超大工具结果、恶意资源 URI、重定向和协议异常被拒绝；
- 写操作没有审批不会产生副作用。

### 性能测试

- 长会话上下文组装 P95；
- 事件写入延迟和批量大小；
- 快照存储增长和清理；
- MCP 工具发现与重连时间；
- 并发工具调用在预算和队列限制内运行。

## 九、回滚策略

- 每个阶段独立提交和迁移，不混合业务功能；
- 新事件字段全部可选，旧事件继续可读；
- `agent.loop.enabled`、`agent.snapshots.enabled`、`mcp.enabled` 独立开关；
- 关闭快照只停止新快照，不删除历史数据；
- 关闭 MCP 只卸载 MCP 工具，不影响原生工具和 RAG；
- 预算策略异常时回退到现有请求超时和工具超时；
- 数据库迁移只新增表和索引，不修改现有消息主键；
- 回滚代码前保留事件和快照，恢复后仍能被旧版本安全忽略。

## 十、最终建议

实施重点按以下顺序推进：

```text
事件层级与回放
  → 上下文快照
  → Loop 预算/重试/完成判定
  → MCP 只读接入
  → Subagent 与 Goal/Todo
  → Workflow/PTC/Schedule
```

第一阶段不追求增加更多工具，而是先让现有 RAG、记忆、审批和多 Agent 运行过程可解释、可恢复、可回放。完成这一层后，再接 MCP 才不会把外部工具的不确定性直接放大到生产环境。
