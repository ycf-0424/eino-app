# my-eino-app 完善路线图

> 本文档基于对 eino 官方示例（`E:\11\eino-examples`）的对比分析整理，
> 用于指导本项目的渐进式完善。每个条目都是一个可独立验收的任务。
>
> 勾选方式：完成任务后把 `- [ ]` 改为 `- [x]`。

---

## 0. 现状快照（更新于 2026-09-09）

已完成：P0-P7 后端能力以及内置 Web 对话界面。

| 能力 | 状态 | 说明 |
|------|------|------|
| 配置与代码分离 | ✅ | `config.yaml` 三字段切 Ollama / 火山方舟 |
| 多轮对话 | ✅ | Agent 管理历史并持久化 Session |
| 流式输出 | ✅ | 控制台与 WebSocket 均支持增量输出 |
| Agent/Runner | ✅ | Eino ADK Runner + Supervisor 多 Agent |
| 工具调用 | ✅ | 时间、知识库检索与审批写入工具 |

技术栈：eino v0.9.17 + eino-ext openai 组件 + gopkg.in/yaml.v3，Go 1.26.3。

## 长期存储路线：MySQL + Milvus

2026-09-14 已接入 MySQL 实时会话存储（保留文件模式），使用 Milvus 保存知识库向量，并完成自动长期记忆闭环。操作说明见 [MYSQL.md](MYSQL.md)。长期记忆使用独立表和 `my_eino_memory_v1` 集合，普通聊天不向量化；多人认证和更大规模的管理平台仍需后续建设：

```text
MySQL：用户、会话、消息、文档/分片元数据、长期记忆
Milvus：知识库分片和长期记忆的向量
```

推荐表：`users`、`conversations`、`messages`、`knowledge_documents`、`knowledge_chunks`、`memories`。`knowledge_chunks.milvus_id` 和 `memories.milvus_id` 用于关联 Milvus 中的向量记录。

写入流程：知识库导入时先在 MySQL 登记文档和分片，再用 Embedding 模型生成向量写入 Milvus，并回写 Milvus ID；每轮对话的用户消息和模型回复写入 MySQL，只有筛选出的重要长期记忆才同时写入 Milvus。现有 `data/sessions/*.json` 需要通过一次性迁移脚本导入 `conversations` 和 `messages`。

部署方案确定为：复用现有 MySQL 实例的数据卷，在当前 Compose 中重建同版本 MySQL 服务。旧容器先停止并保留，Compose 使用 external 卷 `gozero_mysql_data`；验证通过后也不自动删除旧容器。项目不创建新的 MySQL 数据卷，原有数据库保持独立。

### 实例连接与数据库初始化（已完成；多人认证仍待实施）

- 确认现有实例的容器名、MySQL 版本、宿主机映射端口和持久化目录。
- 在现有实例中执行以下初始化 SQL，只创建项目数据库：

```sql
CREATE DATABASE IF NOT EXISTS eino
  CHARACTER SET utf8mb4
  COLLATE utf8mb4_unicode_ci;
```

- 创建仅能访问 `eino` 的应用专用账号；日常应用使用读写权限，建表和升级由迁移账号执行。密码从环境变量或部署密钥注入，不提交到仓库。
- Windows 上运行 Go 时，连接 `localhost:<MySQL 宿主机映射端口>`。
- 应用在 Docker 中运行时，可通过 `host.docker.internal:<MySQL 宿主机映射端口>` 访问已发布的 MySQL 端口；也可将应用和现有 MySQL 容器加入同一个用户自定义 Docker 网络，通过网络别名和容器内部端口连接。只有共享该网络时才能使用容器名解析地址。
- MySQL 由当前 Compose 管理；`make down` 会停止 Compose 创建的 MySQL 容器，但不会删除 external 数据卷。旧容器 `gozero-mysql` 保留为回退选项，回退前先停止新容器。

### 一次完整迁移的实施与验收清单

以下为整体路线的验收清单；MySQL 会话基础接入已完成，但复合条目需要全部验收后再勾选。后续新增业务仍需通过数据库迁移演进表结构。

- [x] **连接与配置**：增加 MySQL 配置、连接池、连接超时与健康检查，验证宿主机和 Docker 两种运行方式均可连接 `eino`。
- [x] **建表与升级**：提供显式、按配置选择的迁移脚本，覆盖会话、执行记录和自动记忆表。
- [x] **会话存储**：通过存储接口接入 MySQL，保留消息顺序和状态，处理并发写入、重复请求、生成中断和失败状态。
- [x] **知识库同步**：文档索引使用稳定 ID、增量对账、写入后清理和跨进程锁；知识集合与记忆集合隔离。
- [x] **长期记忆**：自动提取、规则判定、MySQL 版本化存储、独立 Milvus 索引、范围过滤、删除和重试已完成；当前仅支持 `local_single_user`。
- [ ] **历史迁移**：提供可预览、可重复执行的 JSON 导入工具，保留原会话 ID 和完整消息；同时覆盖宿主机 `data/sessions/` 与 Docker `app-data` 卷中的会话。核对会话数、消息数和抽样内容，保留源文件。
- [ ] **切换与恢复**：备份后暂停会话写入，完成最终导入和校验再切换存储；明确回滚步骤，切换后产生的新消息必须纳入恢复方案。Checkpoint 和工具输出的存储安排单独记录，避免误认为迁移消息就已迁移全部运行状态。
- [ ] **端到端验收**：验证重启恢复会话、多用户隔离、并发消息、导入重跑不重复，以及 Milvus 不可用后的补偿同步；完成 `eino` 备份恢复演练并更新部署和操作文档。

RAG 仍使用 `rag.store: "milvus"`，MySQL 保存业务记录，Milvus 负责向量检索。

---

## P0 — 工程基本盘（建议最先做，成本低收益快）

### P0-1 流式输出
- [x] **目标**：回答边生成边打印，不用等全部完成
- [x] **验收**：`go run ./cmd/console "写一首诗"` 文字逐字出现
- [ ] **实现要点**：改用 `cm.Stream(ctx, msgs)` + `stream.Recv()` 循环，`errors.Is(err, io.EOF)` 收尾；交互模式下每轮单独开一个 stream
- [x] **改动位置**：`cmd/console/main.go`（Agent 事件处理）
- [ ] **参考**：`eino-examples/quickstart/chatwitheino/cmd/ch01/main.go` 第 68~88 行

### P0-2 错误重试
- [x] **目标**：网络抖动/限流时自动重试，而不是直接报错退出
- [x] **验收**：临时断网 2 秒再恢复，单次调用可自动恢复成功（或给出清晰重试日志）
- [ ] **实现要点**：对 Generate/Stream 包一层指数退避重试（最多 3 次）；区分"可重试错误"（网络/5xx/限流）与"不可重试错误"（参数错误）
- [x] **改动位置**：`internal/model/retry.go` 模型包装器
- [ ] **参考**：`eino-examples/quickstart/chatwitheino/helpers/retry.go`

### P0-3 配置必填校验
- [x] **目标**：config.yaml 缺失关键字段时启动即报清晰错误
- [x] **验收**：把 `model` 清空后运行，报"config: model is required"，而不是晦涩的请求失败
- [ ] **实现要点**：`config.Load()` 后校验 `OpenAI.Model`/`BaseURL` 非空（APIKey 在 Ollama 下可空，做成"base_url 非 localhost 时才必填"）
- [x] **改动位置**：`internal/config/config.go`
- [ ] **参考**：`eino-examples/quickstart/eino_assistant/pkg/env/env.go`（MissingEnv 的校验思路）

### P0-4 单元测试
- [x] **目标**：核心逻辑有最小测试覆盖
- [x] **验收**：`go test ./...` 通过
- [ ] **实现要点**：config 解析测试（合法 yaml / 缺字段 / 文件不存在三例）；多轮历史拼接测试（可抽成纯函数）
- [x] **改动位置**：`internal/config/config_test.go`、`internal/model/retry_test.go`、`internal/session/session_test.go`
- [ ] **参考**：`eino-examples/quickstart/chatwitheino/msgops/msgops_test.go`

---

## P1 — 从"对话"升级为"Agent"（核心一跃，对应教程 ch02/ch03）

### P1-1 引入 Agent + Runner
- [x] **目标**：通过 Agent/Runner 统一执行多轮对话，控制台不再直接维护 history
- [x] **验收**：交互模式下历史由 ChatAgent 封装维护，控制台不再出现 `[]*schema.Message` 的手动 append；多轮记忆行为与现在一致
- [x] **实现要点**：
  - `adk.NewTypedChatModelAgent[*schema.Message]` 包住现有 ChatModel
  - `adk.NewTypedRunner[*schema.Message]` 承载 Agent
  - 每轮提问通过 `runner.Stream(ctx, &adk.RunRequest{...})`，事件循环里处理增量输出
- [x] **改动位置**：新增 `internal/agent/agent.go`；重写 `cmd/console/main.go` 的调用部分
- [x] **参考**：`eino-examples/quickstart/chatwitheino/docs/ch02_chatmodel_agent_runner_console.md`（第 237 行起有完整 runTyped 写法）

### P1-2 Session 持久化
- [x] **目标**：程序重启后对话记录可恢复
- [x] **验收**：退出后重新运行同一会话，模型还记得之前的对话
- [x] **实现要点**：本项目使用带原子写入和 ID 校验的本地 JSON Store + Session ID；参考 chatwitheino 的 `mem/` 封装与 `SESSION_DIR` 约定
- [x] **改动位置**：`internal/agent/agent.go`、新增会话目录配置（config.yaml 加 `session_dir` 字段）
- [x] **参考**：`eino-examples/quickstart/chatwitheino/cmd/ch03`、`adk/intro/session`

---

## P2 — 让 Agent 能"干活"（对应 ch04~ch06）

### P2-1 工具调用（Tool）
- [x] **目标**：Agent 能调用真实工具（先做 1 个：如查时间/读文件）
- [x] **验收**：问"现在几点"Agent 调用 time 工具并正确回答；工具调用过程可见（callback 日志）
- [ ] **实现要点**：`compose.ToolsNodeConfig` 挂 `tool.BaseTool`；模型需支持 tool calling（方舟 ✅ / qwen3.5:9b 支持但偏慢）
- [x] **改动位置**：`internal/agent/agent.go`、新增 `internal/tool/`（放自定义工具）
- [ ] **参考**：`eino-examples/quickstart/chatwitheino/cmd/ch04`、`adk/common/tool`

### P2-2 Middleware（安全护栏/重试）
- [x] **目标**：给工具调用加统一拦截层（当前为只读工具的超时与审计）
- [x] **验收**：middleware 能记录工具参数、耗时、错误并执行超时控制
- [ ] **实现要点**：实现 `adk.TypedChatModelAgentMiddleware`，参考 chatwitheino `helpers.NewSafeToolMiddleware` 与 approval 拦截模式
- [x] **改动位置**：`internal/agent/` 下新增 middleware 文件
- [ ] **参考**：`eino-examples/quickstart/chatwitheino/helpers/`、`cmd/ch05`

### P2-3 Callback 可观测
- [x] **目标**：能看到 Agent 每一步在干什么（调模型/调工具/耗时）
- [x] **验收**：交互模式开启 debug 开关后，打印模型与工具调用、耗时和错误
- [ ] **实现要点**：用 eino `callbacks` 包注册 Handler（`cbutils.NewHandlerHelper()`），config.yaml 加 `debug: true` 开关
- [x] **改动位置**：`internal/observability/debug.go`、`internal/agent/agent.go`、config.yaml
- [ ] **参考**：`eino-examples/quickstart/chatwitheino/cmd/ch06`、`adk/common/model/chat_model.go` 里的 GetInputLoggerCallback

---

## P3 — 按需生产化（业务明确后再做）

| 任务 | 触发条件 | 参考 | 状态 |
|------|---------|------|------|
| RAG（bge-m3 embedding + 向量存储） | Redis Stack 与 Milvus 均可通过 `rag.store` 切换 | `eino-examples/quickstart/eino_assistant` | Redis ✅ / Milvus ✅ |
| Eino Checkpoint Backend | 保存 Runner 中断状态并支持跨进程恢复 | ch07 | ✅ |
| 中断 + 人工审批 | `write_note` 在执行前产生 StatefulInterrupt，审批后 Resume | ch07 | ✅ |
| Skill 技能包 | 需按需注入知识指令 | ch09 | ✅ |
| Web 界面 / A2UI | 从命令行走向网页 | 内置响应式 Web UI + HTTP/WebSocket | ✅ |
| 多 Agent 协同 | Supervisor + knowledge-agent + writer-agent | `adk/prebuilt/supervisor` | ✅ |
| 密钥管理进阶 | 要上生产/多人协作 | .env + gitignore / 密钥服务 | ✅（环境变量 + 脱敏） |

> RAG 提醒：纯对话不需要向量库。Milvus 较重，学习期文档少可先用轻量替代；Embedding 可直接用已装好的 Ollama `bge-m3`。

### P3 已完成验收范围

当前已通过本地 Ollama + Docker Milvus 的真实链路验收：文档切分、`bge-m3` 向量化、Milvus 入库/检索、单 Agent RAG、多 Agent 汇总、人工审批以及跨进程 Checkpoint 恢复。后续任务不再重复实现这些基础能力，而是在其上增强可维护性和产品能力。

---

## 当前状态

上述课程能力已分别在 P4-P7 中落地。项目当前具备 Prompt/Chain、结构化输出、
RAG 质量控制、多格式增量索引、Ollama 健康检查、Session 治理、Skill、
HTTP/WebSocket API、内置 Web UI，以及分层集成测试与固定问答评测。

---

## P4 — Prompt、Chain 与结构化输出

### P4-1 Prompt / ChatTemplate
- [x] **目标**：将系统指令、RAG 指令、Supervisor 和 Writer 指令模板化，支持变量渲染。
- [x] **验收**：新增 `internal/prompt` 模板和单元测试，变量可正确渲染。
- [x] **改动位置**：新增 `internal/prompt/`，由 RAG Chain 注入。

### P4-2 RAG Chain
- [x] **目标**：提供固定的“检索 → 上下文格式化 → Ollama 生成”链路，与 Agent 工具模式并存。
- [x] **验收**：新增 `--mode rag-chain`，检索失败有明确错误；真实模型验收需在 Ollama GPU/内存正常后执行。
- [x] **改动位置**：新增 `internal/chain/`；复用 `internal/rag.Store`。

### P4-3 结构化答案与引用
- [x] **目标**：统一输出 `answer`、`sources`、`confidence`、`error` 字段，避免业务解析自然语言。
- [x] **验收**：`--json` 输出结构化 JSON；来源包含文档路径；非法 JSON 有纯文本降级和提示；单元测试已覆盖。
- [x] **改动位置**：新增 `internal/output/`，控制台和未来 API 共用。

---

## P5 — 知识库与 Ollama 运行增强

### P5-1 Ollama 健康检查与模型校验
- [x] **目标**：启动或 index 前检查 Ollama、聊天模型 `qwen3.5:9b`、Embedding 模型 `bge-m3` 是否可用，并校验向量维度。
- [x] **验收**：服务未启动、模型不存在或维度不匹配时，在请求前给出明确错误；真实 Ollama 检查通过。
- [x] **改动位置**：`internal/config/`、`internal/health/`。

### P5-2 文档解析与增量索引
- [x] **目标**：支持 Markdown、TXT、HTML、JSON、DOCX、PDF，并进行清洗、标题感知切分、哈希去重和增量更新/删除。
- [x] **验收**：重复 index 跳过未变化块，修改只更新变化块，删除文件会清理旧 ID；PDF 按页保留页码元数据。
- [x] **改动位置**：`internal/rag/documents.go`、`internal/rag/parsers.go`、`internal/rag/manifest.go`、`cmd/indexer/main.go`、`rag.Deleter`。

### P5-3 检索质量与安全
- [x] **目标**：增加相似度阈值、TopK、元数据过滤、结果去重、引用标记和 Prompt 注入防护。
- [x] **验收**：低于阈值时明确回答“知识库无依据”；回答可追溯到来源；恶意文档指令不会覆盖系统规则。
- [x] **改动位置**：`internal/rag/`、`internal/tool/knowledge.go`、`internal/prompt/`。

### P5-4 Session 历史治理
- [x] **目标**：限制历史长度，支持摘要压缩、Session 列表/删除和过期清理。
- [x] **验收**：长对话不会无限增长；重启后摘要和最近消息仍可恢复；清理操作不影响其他 Session。
- [x] **改动位置**：`internal/session/`、`cmd/console/main.go`、`cmd/sessions/`。

---

## P6 — Skill 与应用接口

### P6-1 Skill 技能包
- [x] **目标**：将 Prompt、工具、RAG 规则和输出格式封装为可加载 Skill。
- [x] **验收**：提供 `knowledge_qa` 和 `report_writer` 两个 Skill，可按配置、CLI 或 API 选择。
- [x] **改动位置**：新增 `skills/` 和 `internal/skill/`。

### P6-2 HTTP/WebSocket API
- [x] **目标**：提供聊天、流式事件、Session、审批和恢复接口。
- [x] **验收**：客户端可创建会话、接收增量回答、提交审批并恢复中断任务；HTTP 与 WebSocket 测试通过。
- [x] **改动位置**：`internal/server/`、`cmd/server/`、`docs/API.md`。

### P6-3 内置 Web 对话界面
- [x] **目标**：提供接近主流 AI 对话产品的信息架构，并直接复用现有 Go API。
- [x] **验收**：支持响应式会话侧栏、历史加载/删除、Skill 选择、WebSocket 流式回答、停止生成、回答复制、人工审批和服务状态；无需 Node.js 即可随 Go 服务启动。
- [x] **改动位置**：`internal/server/web/`、`internal/server/web.go`、`internal/server/http.go`。

---

## P7 — 测试、性能与生产安全

### P7-1 集成测试与评测
- [x] **目标**：将单元测试与依赖 Ollama/Milvus 的真实集成测试分层，并建立固定 RAG 问答集。
- [x] **验收**：`go test -p 1 ./...` 不依赖外部服务；显式运行 integration 标签才连接 Ollama/Milvus；固定 3 题正确率、引用率、拒答率均为 1.0。
- [x] **改动位置**：新增 `internal/integration/`、`internal/evaluation/`、`cmd/eval/`、`cmd/retrieve/` 和 `scripts/test-integration.ps1`。

### P7-2 资源与并发治理
- [x] **目标**：统一模型/工具超时、并发限制、请求取消、日志脱敏和指标统计。
- [x] **验收**：并发和同 Session 排队均受上限与上下文取消控制；模型/HTTP/工具有超时；`/metrics` 实测记录请求、错误、活动数和平均耗时；日志脱敏测试通过。
- [x] **改动位置**：`internal/model/`、`internal/agent/`、`internal/server/`、`internal/observability/`、`cmd/server/`。

### P7-3 密钥与配置安全
- [x] **目标**：API Key 从环境变量或 Secret 注入，配置文件不保存真实密钥。
- [x] **验收**：`.gitignore` 覆盖本地密钥与运行数据；日志隐藏常见 JSON、Bearer 和 URL 密钥；Session/Checkpoint 不写入配置密钥；远程服务缺少密钥或环境变量时启动即报错。
- [x] **改动位置**：`internal/config/config.go`、`.env.example`、`.gitignore`、`docs/SECURITY.md`。

### P7-4 容器化与开发命令
- [x] **目标**：统一本地检查、构建、Milvus 依赖和完整容器服务的启动方式。
- [x] **验收**：Makefile 可执行 `check/run/infra-up/up/down/logs`；Compose 配置校验通过；Linux 静态服务编译通过；容器可访问宿主机 Ollama。
- [x] **改动位置**：`Dockerfile`、`.dockerignore`、`Makefile`、`config.docker.yaml`、`docker-compose.milvus.yml`、`docs/DEPLOYMENT.md`。

### P7 实际验收（2026-09-09）

- `go test -p 1 ./...` 与 `go vet ./...` 通过。
- Ollama + Milvus integration 测试通过；普通单元测试不连接外部服务。
- RAG 固定评测：正确率 `1.0`、引用率 `1.0`、拒答率 `1.0`、平均耗时 `4360ms`。
- HTTP 实测：`/health`、`/chat`、`/metrics` 正常；一次请求后指标为 requests=1、errors=0、active=0。

---

## P8 — Web 实时执行进度与执行记录（2026-09-10 已实施，开关默认关闭）

目标：用户提交问题后立即看到真实执行状态，并在回答正文之外查看工具调用、文件读取和技能加载记录；刷新页面后可以恢复已保存的执行记录。

实施状态（2026-09-10）：代码已落地，灰度开关 `execution_events.enabled` 默认 `false`，关闭时创建的执行记录后端为 `nil`，行为与 P7 完全一致。已通过的检测见文末「P8 验收记录」；依赖真实模型/浏览器/压测的条目在 P8-6 中保持未勾选。

### P8-0 当前代码核对与展示边界

- `internal/agent/agent.go` 的 `collect` 已消费 Runner 事件和模型流，但主要把助手 `Content` 写到 `io.Writer`；当前未把工具生命周期和独立推理字段转发给页面。不能仅凭页面等待就断定后端在缓存全部回答。
- `internal/server/http.go` 的 `socketWriter.Write` 发送 `chunk`，WebSocket 另发送 `ready/error/approval/done`。实现时沿用现有 `chunk` 协议，不把旧协议误写成 `delta`。
- `internal/agent/middleware.go` 当前主要输出 debug 审计日志；根 Agent 注册了该工具中间件，子 Agent 的 ToolsConfig 没有同样注册，采集时需覆盖所有实际工具执行入口。
- `internal/skill/runtime.go` 扫描技能建立目录快照；`preferred` 会预加载正文，`load_skills` 按需返回正文。扫描文件不代表模型使用技能；该工具不执行技能中的命令。`loader.go` 的文件读取不能直接全部显示成“使用了技能”。
- `internal/server/web/app.js` 的审批恢复走 HTTP POST，并非原 WebSocket 流；历史页面只恢复消息，新增进度不能遗漏审批恢复和历史加载路径。
- 展示“处理状态/执行摘要”，不推测或编造模型内部思考。若供应商提供可展示的推理摘要字段，先验证模型、SDK 和事件传递能力，再作为可选展示；没有字段时显示“等待模型响应”，不宣称正在执行未发生的步骤。

### P8-1 事件协议和生命周期

- [x] **实现**：新增独立的执行事件模块（如 `internal/execution/`）及请求级事件接收接口（Emitter），避免让工具直接依赖 WebSocket。服务层在技能预加载和 Agent 初始化之前创建执行上下文。`skills.Runtime` 当前是纯函数，需增加可接收 Emitter 的参数才能发出 `skill_preloaded`。
- [x] **信封协议**：所有事件统一携带固定信封字段，正文 `chunk` 沿用现有结构但补齐 `run_id`/`sequence`：

```json
{
  "version": 1,
  "event_id": "uuid",
  "run_id": "uuid",
  "session_id": "string",
  "sequence": 12,
  "occurred_at": "2026-09-10T16:00:00.123+08:00",
  "type": "tool_completed",
  "summary": "检索知识库完成，命中 3 条",
  "payload": { "tool_name": "knowledge_search", "duration_ms": 412, "hit_count": 3 }
}
```

- [x] **载荷字段**：`summary` 只用于展示，**断言、入库和前端取值一律使用 `payload` 结构化字段**，避免前端与数据库各自解析文本。各类型 `payload` 约定：

| type | payload 字段 |
|------|-------------|
| `run_started` | `query_chars`、`skill_preferred` |
| `model_waiting` | `model`、`attempt` |
| `tool_started` | `tool_name`、`tool_call_id`、`agent_name`、`args_digest`（脱敏摘要，不含完整参数） |
| `tool_completed` | `tool_name`、`tool_call_id`、`agent_name`、`duration_ms`、`result_bytes`；工具专属：`knowledge_search` → `hit_count`/`top_score`/`sources[]`；`local_file_read` → `file_name`/`file_ext`/`bytes`/`truncated` |
| `tool_failed` | `tool_name`、`tool_call_id`、`agent_name`、`duration_ms`、`error_code`、`error_message` |
| `skill_preloaded` | `skill_names[]` |
| `skill_loaded` | `skill_names[]`、`requested_names[]` |
| `file_read_completed` | 同 `local_file_read` 载荷 |
| `approval_required` | `tool_name`、`tool_call_id`、`approval_id` |
| `run_completed` | `answer_chars`、`duration_ms`、`tool_calls` |
| `run_failed` | `error_code`、`error_message` |
| `run_cancelled` | `reason`（`user`/`disconnect`/`timeout`） |

- [x] **类型**：`run_started`、`model_waiting`、`tool_started`、`tool_completed`、`tool_failed`、`skill_preloaded`、`skill_loaded`、`file_read_completed`、`approval_required`、`run_completed`、`run_failed`、`run_cancelled`。正文继续使用 `chunk`，与执行记录分开。
- [x] **正文与事件排序**：正文 `chunk` 也带 `run_id` 与 `sequence`，与执行事件共用同一单调递增序列，并由同一写循环串行发出；保证同一 run 内“事件—正文”相对顺序稳定，禁止两路并发写同一连接。
- [x] **版本演进**：`version` 为整数。新增可选 `payload` 字段不升版本；删除字段、改变字段语义或增删 `type` 枚举必须升版本。消费端遇到未知 `type` 或更高 `version` 时忽略该事件并记录日志，不得让整轮失败。
- [x] **约束**：同一调用的开始和结束使用同一 ID；多次调用同一工具不能合并。审批暂停不算完成，恢复执行**沿用原 `run_id`、`sequence` 继续递增，不新建 run**；每个 run 最多一个终态。
- [x] **`run_started` 归属**：在 `service.Chat` 通过并发闸门与 `acquireSession` 之后、`newAgent`（含 `skills.Runtime` 预加载与 `agent.NewWithInstruction`）之前发出，确保模型尚未输出正文时前端已收到。
- [x] **验收**：模型尚未输出正文时，前端已经收到 `run_started`；事件 ID 唯一、排序稳定、重复接收可去重；未知 `type`/高版本事件被安全忽略；事件与正文可凭 `sequence` 重放排序。

### P8-2 真实工具、文件和技能采集

- [x] **工具**：在 `internal/agent/middleware.go` 增加事件采集（开始、结束、耗时、错误），并在 `agent.NewWithInstruction` 中把**同一个** middleware 挂到根 Agent、`knowledge-agent`、`writer-agent` 三处 `ToolsNodeConfig`（现状只有根 Agent 挂载，子 Agent 未挂）。采集逻辑需区分三类结果：
  - `next` 正常返回 → `tool_completed`；
  - `next` 返回错误且 `toolCtx.Err()==DeadlineExceeded` → `tool_failed{error_code:"timeout"}`；`next` 返回错误且上下文被取消 → 交给取消链路，不再补发 `tool_failed`；
  - `next` 返回审批中断 → **只发 `tool_started`，不补 `tool_completed`**，改由审批链路发 `approval_required`。
  避免中间件与 Runner 对同一调用重复上报。
- [x] **文件**：`internal/tool/local_file.go` 的成功返回值改为可解析的结构化结果（`file_name`、`file_ext`、`bytes`、`truncated`），事件从工具返回值解析，**不猜测调用参数**。读取被拒绝只能显示失败，不显示“已打开”。当前工具读取文件，不等于打开桌面编辑器。
- [x] **技能**：在 `internal/skill/runtime.go` 分别报告显式预加载（`skill_preloaded`）和 `load_skills` 成功返回正文（`skill_loaded`）；多技能加载按实际成功语义记录。技能目录扫描只记系统准备状态，不能生成大量“已使用”记录。
- [x] **检索**：知识库工具返回值需可解析出命中数、最高分和允许披露的来源（`hit_count`、`top_score`、`sources[]`），事件从结果解析；Milvus 命中片段不能冒充本轮从磁盘打开了来源文件。
- [ ] **验收**：时间工具、文件成功/越权失败、零命中检索、多个技能、重复工具调用和子 Agent 调用均可准确显示；子 Agent 与根 Agent 的调用都能采集且不重复；不把“加载了规则”描述成“已执行技能中的命令”。

### P8-3 实时传输、审批和取消

- [x] **改动位置**：`internal/server/service.go`、`internal/server/http.go`、`internal/agent/agent.go`。给 Chat 和 Approve 共用的执行链路注入事件接收接口（Emitter），正文 Writer 保留兼容。
- [x] **HTTP 与同步路径**：`POST /chat` 与 `POST /sessions/{id}/approval` 目前用 `bytes.Buffer` 同步返回，事件无处投递。明确二者行为：**同步接口在响应体返回本轮事件数组**（供非 WS 客户端与测试使用），**实时推送仅在 WebSocket 提供**；两条路径必须产出同一套事件，只是投递方式不同。
- [x] **连接管理**：WebSocket 通过单一写循环发送正文及事件，设置有界队列（建议 256）和写超时，避免多 Agent 并发 `WriteJSON`。
- [x] **队列与背压**：中间事件队列满时允许丢弃，并在下一条事件置 `dropped=true` 或记录丢弃区间，前端凭 `after_sequence` 补取；`run_completed`/`run_failed`/`run_cancelled` 三个终态事件**必须送达**，终态入队失败时主动断开连接并记录日志，绝不静默丢弃。
- [x] **审批恢复**：明确采用 WebSocket 审批命令或独立可订阅事件流，使 HTTP 审批恢复时也能实时观察执行；不能只在结束后更新结果。
- [x] **取消**：当前 WebSocket 在 Chat 返回前不继续读取客户端消息，需要拆分读循环和执行任务；建立**每轮 cancel 上下文**（不使用连接生命周期 `r.Context()`），处理用户停止、断线、写失败和超时，并及时释放资源。
- [ ] **验收**：慢模型下先显示状态再显示正文；中途停止可到达后端；审批前后事件连贯；断线重连和多 Agent 并发不发生并发写连接或串会话；HTTP 同步接口返回的事件数组与 WS 事件一致。

### P8-4 MySQL 执行记录与历史恢复

- [x] **表结构**：新增 `execution_runs`（run ID、会话 ID、用户/助手消息序号、状态、开始/结束时间）及 `execution_events`（事件 ID、run ID、序号、类型、时间、脱敏 payload）。设置 `(run_id, sequence)` 唯一约束和会话查询索引。
- [x] **关联校正**：已核对 `messages.sequence` 是**完整历史数组下标**（`internal/session/mysql.go` 保存时用切片下标 `i`），且 MySQL 明确拒绝截断历史（`refusing to truncate history`），因此 sequence 稳定可作关联键；但依赖“历史只增不减”这一前提，若将来引入历史裁剪必须同步迁移关联。assistant 占位消息在 `agent.beginReply()` 落库后即可取回其 sequence，`execution_runs` 记录 `(user_sequence, assistant_sequence)`。另在 `messages.payload` 写入本轮 `run_id`，供历史回放按消息反查 run。运行记录允许先创建、待占位消息保存后再回填关联，覆盖初始化失败。
- [x] **迁移**：新增版本化迁移及应用兼容检查，不改写 `001_sessions.sql` 假定旧表会自动升级；迁移通过专用账号执行。现有历史消息没有执行事件时显示空记录，不伪造补全。
- [x] **可靠性**：定义事件保存和发布次序、批量保存策略、数据库失败时的行为及异常退出后的状态修复。事件保存不得长时间阻塞模型，也不能在保存失败时声称历史记录完整。
- [x] **级联清理**：`execution_runs`/`execution_events` 通过外键 `ON DELETE CASCADE` 跟随 `conversations`，删除会话时同步清理（现有 CASCADE 只覆盖 `messages`）。
- [x] **保留策略**：执行事件量远大于消息，需定义保留策略（如按 N 天或每会话上限归档/清理），并说明清理不影响会话消息、历史回放与“运行中”状态修复的正确性。
- [x] **查询**：提供按会话/run 查询事件的分页接口，支持 `after_sequence` 补取。当前文件存储模式需实现对应事件存储，或明确只提供实时进度、不支持刷新回放。
- [x] **验收**：刷新后执行记录关联正确消息；重复事件不重复入库；故障恢复后不永久显示运行中；删除会话后无孤儿事件；原有 JSON 导入、聊天历史和过期清理继续可用。

### P8-5 Web 执行面板

- [x] **改动位置**：`internal/server/web/app.js`、`app.css`，并在 `index.html` 增加执行面板容器与折叠模板（每轮助手气泡内嵌“执行过程”节点）。正文保持独立。
- [x] **交互**：运行中显示当前状态、工具/技能/文件及耗时；完成、失败、取消和待审批有不同标识。
- [x] **历史回放**：加载历史时按 `messages.payload.run_id` 查出对应 run 与事件，恢复折叠面板；重连后带 `after_sequence` 补取缺失事件，按 `event_id` 去重更新已有条目。
- [x] **渲染**：文件名、参数摘要、模型文本均作为不可信输入安全渲染；默认显示相对路径或文件名，只展示允许披露的参数，不显示密钥和完整工具输出。
- [ ] **验收**：无工具的普通问答不出现虚构步骤；长文件名、多个技能、窄屏、键盘展开、滚动位置、失败重试及刷新回放均可正常使用。

### P8-6 一次交付验收与说明

- [x] **自动验证**：事件排序与终态唯一性、`payload` 字段契约、未知 `type`/高版本安全忽略、脱敏、请求隔离、工具真实结果、技能扫描/加载区别、审批恢复、取消、重连去重、MySQL 保存及删除同步。
- [ ] **集成验证**：使用可控 SSE 模型测试“正文前有进度”，以真实 MySQL 测试回放；保留一轮 Ollama 实测，分别记录事件到达时间和首段正文时间，定位等待发生在哪一层。
- [ ] **规模与延迟**：构造一轮约 20 次工具调用的会话，记录事件总条数、序列化与入库耗时，以及开启采集前后的首字延迟差；确认采集不长时间阻塞模型（异步或批量保存，且失败不谎报完整）。
- [ ] **界面验证**：浏览器实际操作普通问答、技能加载、文件读取、失败、停止、审批和历史恢复；检查无控制台错误、无敏感参数泄漏。
- [x] **文档与注释**：同步 `docs/API.md`、`docs/MYSQL.md` 和操作说明，解释事件来源、状态含义及模式限制；新代码添加说明事件关联、并发与持久化取舍的中文注释。
- [ ] **发布门槛**：适用单元测试、MySQL 集成测试、静态检查和 Compose 配置检查通过，旧 `chunk` 消费端与原聊天功能保持可用；开关关闭（默认）与开启两态均需通过。

### P8-7 灰度开关与回滚

- [x] **开关**：新增配置 `execution_events.enabled`（默认 `false`）。关闭时完全不创建执行上下文、不发送也不保存事件，`chunk` 行为与现状完全一致，前端不显示执行面板。
- [x] **回滚**：开关为运行时配置，回滚只需改回 `false` 并重启；新表保留不删、不影响旧功能。
- [x] **验收**：关闭状态下 `go test -p 1 ./...`、`/chat` 与原 Web UI 行为与 P7 一致（无回归）；开启后灰度逐步放量，出现问题时可在不改代码的前提下关闭。

### P8 验收记录（2026-09-10）

已通过：

- `go build ./...`、`go vet ./...`、`go test -p 1 -count=1 ./...` 全部通过（默认开关关闭，即关闭态无回归）。
- 新增单测：`internal/execution`（sequence 单调、未知类型忽略、高版本忽略、参数脱敏、按 bag 隔离、队列背压、终态必达、文件后端往返、`RecoverStale`）、`internal/agent`（中间件开始/完成/失败、审批中断不补终态、错误码映射）、`internal/server`（执行查询接口空/过滤/删除清理）、`internal/config`（开关默认值仅在启用时填充）。
- 迁移 SQL 在 MySQL 9.7.1 实际执行成功：`001` + `002` 建出 `conversations`/`messages`/`execution_runs`/`execution_events`，外键与唯一约束有效。
- `internal/execution` MySQL 集成测试（`-tags mysql_integration`）：`StartRun` 幂等并自动补 `conversations` 行、`AppendEvents` 重复不重复入库、`LinkRunMessage` 回填 `assistant_sequence`、`ListEvents` 的 `after_sequence` 补取、`RecoverStale` 修复超期 running、`DeleteBySession` 清理干净。
- `internal/server` 端到端集成测试：`POST /chat` 创建 run、同步响应内联事件数组（不含 `chunk`）、`GET /sessions/{id}/execution` 按 run 回放且状态为 `completed`、`DELETE /sessions/{id}` 后无孤儿 run/event。

尚未执行（保持未勾选）：

- Ollama/真实模型下的首字延迟实测，未分别记录事件到达时间与首段正文时间。
- 约 20 次工具调用一轮的规模与延迟压测。
- 浏览器人工验收（窄屏、键盘展开、审批、停止、刷新回放的视觉效果）。
- Compose 配置检查。

实施顺序：P8-0/1 定义能力和协议 → P8-2 采集真实操作 → P8-3 实时传输 → P8-4 持久化回放 → P8-5 界面 → P8-6 全链路验收；P8-7 开关自 P8-1 起即贯穿，默认关闭，随各阶段增量打开。先确定协议和关联键，再实施采集与数据库，避免前后端各自定义不兼容状态。

### 二次核对结论

已对照当前 Agent、工具中间件、技能 Runtime、WebSocket、前端审批/历史路径及 MySQL 初始表结构核对。相较最初设想，明确补齐了子 Agent 采集、技能扫描与正文加载区分、审批 HTTP 路径、取消读循环、WebSocket 单写者、消息复合主键、版本迁移和刷新回放；上述检查是文档与代码结构核对，不是尚未实现功能的测试通过声明。

本次补充（2026-09-10）在上表基础上进一步补齐：事件 `payload` 结构化字段契约、正文与事件共用 sequence 的排序保证、版本演进规则、`run_started` 触发点与 `skills.Runtime` 签名改动、子 Agent 中间件挂载点、超时/取消/审批中断的终态区分、工具结构化返回值、HTTP 同步路径的事件投递、队列满与背压的具体策略、每轮 cancel 上下文、`sequence` 可作关联键的代码依据、级联清理与事件保留策略、历史回放按 `run_id` 关联、规模与延迟验收，以及 `execution_events.enabled` 灰度开关与回滚。上述条目除「P8 验收记录」中标注为尚未执行的四项外，均已在同日实施并通过对应检测。

## 推荐实施顺序（后续按此方案执行）

```
P0-1 流式 → P0-3 配置校验 → P1-1 Agent/Runner → P1-2 Session
  → 回到主线学完 ch04（工具）→ P2-1 工具 → P2-3 callback
  → P3 RAG/Checkpoint/多 Agent（已完成）
  → P4 Prompt/Chain/结构化输出
  → P5 Ollama 健康检查/文档增量索引/检索质量/Session 治理
  → P6 Skill/Web API/Web UI
  → P7 集成测试/性能/安全
  → P8 实时执行进度与执行记录（协议→采集→传输→持久化→界面→验收，已实施，开关默认关闭灰度上线）
```

依赖关系：P1-1 是 P1-2/P2-1/P2-2/P2-3 的前置；P0 各项互不依赖可并行。

---

## 参考索引（eino-examples 绝对路径）

- 主线教程：`E:\11\eino-examples\quickstart\chatwitheino`（docs/ 下每章文档 + cmd/chXX 代码）
- ADK 示例：`E:\11\eino-examples\adk`
- 完整 RAG 应用：`E:\11\eino-examples\quickstart\eino_assistant`

---

## 环境注意事项（本机已踩坑）

1. 依赖下载：`GOPROXY=https://goproxy.cn,direct go get/go mod tidy`
2. 并行编译会 OOM：报 `cannot allocate memory` 时用 `go build -p 1 ./...`
3. 本地 9B 模型推理慢（复杂问题约 3 分钟）；调试 Agent/工具建议切火山方舟（改 config.yaml 三行）
4. 当前默认使用本地 Ollama：聊天模型 `qwen3.5:9b`，Embedding 模型 `bge-m3`；复杂问题可能较慢，应配置超时并优先使用小型测试问题
5. 换 OpenAI 兼容服务时只需改 `config.yaml` 的 openai 段：`api_key` / `model` / `base_url`；本路线不要求接入 DeepSeek
