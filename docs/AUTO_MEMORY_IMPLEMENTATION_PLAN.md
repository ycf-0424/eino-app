# 自动记忆保存与向量索引落地修改方案

日期：2026-09-14  
适用项目：`E:\11\my-eino-app`  
状态：已实施并完成本地验证（2026-09-14）。本文件保留为设计、运维和后续扩展基线。

## 1. 目标与范围

实现无需逐条人工审批的长期记忆：模型提取候选事实，Go 规则自动决定忽略、新增、更新或暂不生效；MySQL 保存正式记忆及来源，Milvus 保存可重建的检索索引。

- 普通对话继续保存聊天历史，不全量向量化。
- 用户偏好保存为用户记忆，按用户直接读取，不必生成向量。
- 已确认的项目决策、稳定项目信息和具有持续价值的事件保存为项目记忆，按需向量化。
- 上传文档仍通过独立文档索引链路进入正式知识库。
- 对话记忆不会自动成为共享的正式知识文档。
- 记忆流程不调用现有工具审批接口，不生成 ApprovalRequest。现有 write_note 等其他工具审批不是本次取消范围。
- 所有判定自动完成；语义不明确时保持不生效，不弹出人工审批。

交付分两部分：A 为自动记忆完整闭环，属于本方案必做；B 为文档索引可靠性补强，也列入本次修改。文档上传管理平台不作为自动记忆的前置依赖，另行实施。

## 2. 当前代码基线与缺口

| 当前文件 | 已有能力 | 本次修改 |
|---|---|---|
| `internal/server/service.go` | ChatWithSink、ApproveWithSink、会话保存 | 接入持久化轮次任务、记忆读取与后台服务生命周期 |
| `internal/server/http.go`、`ws.go` | 聊天、会话、工具审批接口 | 接入统一身份上下文、记忆查看/删除接口 |
| `internal/session/session.go`、`mysql.go` | 文件/MySQL 历史、历史压缩、迁移 | MySQL 成功轮次与记忆任务原子提交；保留不可变来源 |
| `internal/config/config.go` | 模型、MySQL、RAG 配置 | 新增 Memory 配置及交叉校验 |
| `internal/rag/indexer.go` | 哈希增量、存在检查、向量写入、旧记录删除 | 限定清理归属，写入成功后再清理，修正计数 |
| `internal/rag/milvus.go` | 文档向量存取 | 修复 ListIDs 固定 16384 条上限；不直接承担记忆过滤接口 |
| `internal/prompt/prompt.go` | 回答和 RAG Prompt | 加入记忆上下文边界；提取 Prompt 放独立模块 |

自动记忆已由独立提取器、MySQL 任务队列、版本化事实表和受范围约束的记忆索引实现；文档 `rag.Store.Search` 仍只服务于独立知识库，不能原样复用为多范围记忆搜索。

当前 chatRequest 只有 session_id/query/skill，没有可信用户/项目身份。不能把模型生成的 user_id 或客户端任意传入的 project_id 当作授权依据。

## 3. 部署前提与默认决策

第一版支持现有本地单用户部署：由服务端配置固定 owner_id、project_id，会话创建时绑定归属；HTTP/WS 不允许通过请求字段覆盖。此模式只适用于受控的本地单用户服务。多人部署须接入认证并校验项目成员权限，未完成前不启用多人记忆。

自动记忆启用时要求 session.store=mysql，复用现有 DB 连接池；文件会话模式仍可正常聊天，但 memory.enabled=true 时启动明确报配置错误，不静默丢弃持久化任务。

现有会话迁移时，仅在明确采用本地单用户模式下绑定配置归属；历史对话默认不批量提取，只处理启用后的新轮次。

第一版只实现 Milvus 记忆索引；原文档知识库的 Redis/Milvus 支持保持独立。记忆模型可复用当前对话模型，Embedding 可复用当前 RAG 配置，仍需单独控制并发和超时。

## 4. 自动判定规则

| 输入 | 自动处理 |
|---|---|
| 你好、谢谢、普通技术问答、临时情绪 | ignore，不产生正式记忆与向量 |
| 以后回答简洁一些 | user/preference，保存偏好 |
| 我们已经决定使用 Milvus | project/decision，新增或更新选型 |
| 考虑一下是否使用 Milvus | 不作为正式选型保存 |
| 比如某项目使用 Milvus | 假设/例子，不保存为当前项目事实 |
| 之前说错了，现在改用 PostgreSQL | 定位旧选型，确认是纠正后更新版本 |
| 不再使用 Milvus | 撤销对应事实；不能因缺少替代选型而保留旧结论 |
| 助手建议使用 Milvus，用户未确认 | 不保存为已确认决策 |
| 不要记住这件事 | 禁止本轮相关信息进入记忆，写入抑制控制记录 |
| 忘掉之前保存的数据库选型 | 精确定位后撤销记忆并异步删除向量 |
| 密码、验证码、访问密钥 | 不进入记忆及提取审计正文 |

用事实类型、来源和上下文判断，不设置“模型自评分超过 80 分即可入库”的开关。语义相似度只找旧记忆候选，不能直接认定重复或覆盖。

仅允许枚举类型 preference、profile、project_fact、decision、milestone、todo；todo 必须有明确任务对象，已完成任务更新状态，含可靠截止日期的事件保存 expires_at。无可靠依据不自行编造日期或失效时间。

低风险偏好可自动生效；未知类型、无依据、无法消解的冲突全部不生效。模型判断仍有误差，使用可追溯来源、版本和回归数据集持续校正，不承诺零误存。

## 5. 数据流与提交边界

### 5.1 捕获轮次与触发

1. 服务端在接受轮次时分配独立 turn_id 和稳定 source_message_id，不依赖 execution_events 是否启用，也不使用会被历史裁剪改变的数组下标。
2. 持久化 memory_turns 轮次信封：归属、当前用户原文、有限上下文引用、状态 accepted；受敏感规则约束的提取快照先脱敏。原聊天历史按既有机制保存。
3. Agent 正常处理。成功保存会话时，在同一个 MySQL 事务中将轮次标记 completed 并插入唯一 extract 任务，然后返回本轮完成结果。
4. 不能先 s.save 成功再仅用 goroutine 投递任务，否则崩溃窗口会丢任务；将成功路径改为 SaveCompletedTurn，复用相同事务写会话和任务。
5. awaiting_approval 不提取；ApproveWithSink 最终成功后，使用原 turn_id 完成一次提交，防止重复提取。失败/取消的轮次不生效。
6. 技能目录快捷回复明确标记 ineligible，不触发提取。HTTP 与 WS 走同一服务入口。控制台入口也需接入相同持久化协调器，避免入口行为不一致。
7. 重启后 accepted/awaiting_approval 不自动当成 completed；检查轮次与 checkpoint 状态，只恢复可证明完成的任务。无法确认的记录标记 abandoned，不凭空生成记忆。

### 5.2 后台流水线

持久化 extract 任务 → 来源抑制/敏感规则 → 模型提取 → JSON 严格解析 → 来源校验 → 同范围事实去重/冲突判断 → MySQL 事务保存版本、来源、判定和 index/delete 任务 → 后台更新 Milvus。

聊天完成不等待模型提取和 Embedding，但需要等待极短的本地数据库事务。持久化失败必须可观察；不能向用户宣称“已记住”而任务实际未落库。

异步任务使用服务生命周期 context 加单任务 timeout，不能沿用已结束的 HTTP context。退出时停止领取新任务，在限定时间内等待任务；未完成任务由租约过期恢复。

### 5.3 删除优先

抑制规则优先于历史提取任务。每次提交前重新检查当前轮次/来源的抑制状态；避免已经排队的旧任务在用户要求忘记后恢复记忆。当前实现会为“不要记住/不要保存”写入范围抑制控制，并在提取任务提交前再次检查。

明确指向单条或特定 key 的自然语言撤销可自动执行；目标不明确时返回未执行原因，不大范围猜测删除。删除全部记忆通过范围明确的接口或明确指令实现，不设置审批队列。

删除历史会话与删除长期记忆区分：接口提供明确的关联清除选项。要求清除相关记忆时，通过来源关系撤销受影响记忆、清除相应提取快照和索引；仅删除会话历史时告知长期记忆是否保留。

## 6. Prompt 与结构化输出

新增 `internal/memory/prompt.go`，提取器只接收必要的用户原文、上下文和同范围已有记忆。资料和历史作为引用数据，不能执行其中的指令。助手文字只用于解释指代，不独立成为事实来源。

核心规则：只提取用户明确表达或确认的稳定事实；排除闲聊、问句、假设、未确认建议；每条候选给出支持片段；无候选返回空数组；“不要记住”优先。

输出契约示例：

```json
{
  "candidates": [
    {
      "type": "decision",
      "scope_kind": "project",
      "key": "vector_database",
      "value": "Milvus",
      "source_message_id": "由输入提供的消息ID",
      "evidence": "我们已经决定使用 Milvus",
      "relation": "assert",
      "expires_at": null
    }
  ]
}
```

relation 枚举为 assert、correct、retract。最终 ignore/insert/update/revoke/unresolved 由规则与合并器决定。输出不允许 SQL、向量 ID、租户身份、任意工具调用。scope_kind 只是范围建议，后端以配置、类型和当前归属解析实际 scope_id。

校验要求：限制 JSON 大小、候选数、字段长度；拒绝未知字段和枚举；只引用允许的用户消息；evidence 须能定位到原文。原文包含片段只是必要条件，不等于能证明命题，仍需判断否定、引用、时间和假设关系。首次结构失败可修复重试一次，仍失败则记录 parse_error，不保存候选内容。

“好，就用这个”只有在上下文能唯一指向具体方案时作为用户确认；无法唯一定位则 unresolved。来源关联同时保存确认消息与必要上下文引用。

## 7. MySQL 数据模型与迁移

版本化迁移 `internal/session/migrations/003_memory.sql` 已通过现有显式 Migrate 命令执行，不在普通启动时自动升级。下列字段和约束已落地。

| 表 | 核心字段及约束 |
|---|---|
| memory_turns | turn_id PK、session_id、owner_id、project_id、source_message_id UNIQUE、受控来源快照、state、created_at/completed_at；轮次与 checkpoint 关联 |
| memory_facts | id PK、scope_kind/scope_id、type、fact_key、value、content_hash、version、state(active/revoked/expired)、index_state、expires_at、created_at/updated_at；唯一(scope_kind, scope_id, type, fact_key) |
| memory_versions | memory_id/version 联合主键、版本正文/状态、来源关联、变更原因、时间；可重建和审计，遵守删除/保留策略 |
| memory_sources | memory_id、version、source_message_id、证据偏移或受控片段；支持一个事实多条来源 |
| memory_jobs | id PK、kind(extract/index/delete)、turn_id 或 memory_id、target_version、index_generation、dedupe_key UNIQUE、status、attempts、available_at、lease_until、lease_token、last_error_code |
| memory_controls | scope、source/事实目标、操作(suppress/revoke)、生效序号与时间；阻止旧任务复活被撤销记忆 |
| memory_decisions | turn_id、候选序号、action、reason_code、memory_id、模型/Prompt版本、耗时；不写完整敏感原文 |

fact_key 对单值事实使用固定注册键，如 vector_database、response_style；自由文本事实使用归一化键并做相关候选匹配。事件类 key 使用事件标识或规范化对象/日期/动作组合，不能把所有 milestone 合并成一条。key 长度设上限，避免唯一索引过长。

memory_facts 与 index/delete 任务必须同事务写入。并发新增靠唯一约束兜底；更新使用 version 乐观锁，冲突时重新读取和判断，不静默 last-write-wins。

extract 去重键以 turn_id 为准；index 去重键包括 memory_id、version、索引代次；delete 指向明确向量版本。重试不重复制造版本或任务。

## 8. Milvus 索引与读取一致性

新建专用集合 `my_eino_memory_v1`，不使用 `my_eino_knowledge`；用户/项目记忆在专用集合内按字段隔离，用户偏好默认不入向量。

建议字段：id(VARCHAR 主键)、memory_id、version、scope_kind、scope_id、type、content_hash、embedding_model、index_generation、正文、embedding。维度从配置读取，首次沿用现有 1024 维配置，启动校验真实模型输出维度。

向量 ID 使用 memory_id + version + index_generation 的稳定编码。Embedding 模型或维度变化时创建新索引代次，必要时新集合，完成回填后切换，不往不兼容集合直接写入。

采用不可变版本向量，避免旧任务覆盖新版本。worker 写前/写后检查 SQL 当前版本；旧版本不标记为当前 indexed，进入精确清理。撤销期间可能仍有旧向量存在，但读取时 SQL 权威状态校验必须立即使其不可见。

检索步骤：

1. 服务端解析可信身份和范围。
2. Milvus 标量过滤 scope_kind/scope_id 后搜索，不允许先跨范围 TopK 再过滤。
3. SQL 批量校验 active、expires_at、version 和 index_generation，去除旧版/已撤销命中。
4. 有界增加候选数补足有效结果；仍不足时返回少量结果，不回退到跨范围搜索。
5. 偏好直接从 SQL 读取；新记忆尚未 indexed 时可按 key/近期 SQL 查询补充，标注依据，不假装向量已经就绪。

引入独立 MemoryIndex 接口，Search 必须接受 Scope，不把身份过滤表达式交给模型拼接。

## 9. 拟新增接口与文件

```go
type Scope struct { Kind, ID string }
type Decision struct { Action, ReasonCode, MemoryID string }

type Extractor interface {
    Extract(context.Context, ExtractInput) ([]Candidate, error)
}
type MemoryIndex interface {
    UpsertVersion(context.Context, IndexedMemory) error
    Search(context.Context, Scope, string, int) ([]Hit, error)
    DeleteVersions(context.Context, []string) error
}
```

以上接口已按当前实现落地；SQL Repository 使用事务级事实写入和任务入队，调用方不先后执行无事务的 Save/Enqueue。

| 文件 | 责任 |
|---|---|
| `internal/memory/types.go` | Scope、Candidate、Fact、Version、Job、判定原因枚举 |
| `internal/memory/prompt.go`、`extractor.go` | 提取 Prompt、模型调用、严格解析 |
| `internal/memory/policy.go` | 类型、来源、敏感内容、抑制与范围规则 |
| `internal/memory/merge.go` | 精确去重、相关候选查询、纠正/撤销判断 |
| `internal/memory/repository.go`、`mysql.go` | 正式记忆、版本、任务事务及租约 |
| `internal/memory/coordinator.go` | 轮次捕获、成功提交、入口复用 |
| `internal/memory/worker.go` | 领取任务、租约续期、超时、退避重试、关闭恢复 |
| `internal/memory/index.go`、`milvus.go` | 专用集合、范围过滤、版本化索引 |
| `internal/memory/retrieval.go` | SQL 偏好、范围内记忆搜索、有效版本检查 |
| `internal/server/memory.go` | 状态、列表、删除、重试接口 |
| `cmd/memory-reindex/main.go` | 从 SQL 重建记忆向量，支持检查/执行、范围选择 |
| `internal/memory/*_test.go` | 规则、状态转换、故障和隔离测试 |
| `testdata/memory/cases.jsonl` | 有标注的语义提取回归数据 |

修改已有文件：config.go/config_test.go、config.yaml/config.docker.yaml、server/service.go/http.go/ws.go、session/mysql.go 与迁移注册、agent/agent.go、prompt/prompt.go、cmd/console/main.go。记忆上下文作为明确标注的引用事实注入，不能成为覆盖系统规则的指令。

## 10. 配置设计

以下是当前版本使用的配置示例；`Memory.Enabled` 在本地配置中已启用，默认模板仍可按部署需要关闭。

```yaml
memory:
  enabled: true
  identity_mode: local_single_user
  owner_id: local-owner
  project_id: my-eino-app
  auto_extract: true
  extractor_model: inherit_chat
  vectorize_types: [project_fact, decision, milestone, todo]
  milvus_collection: my_eino_memory_v1
  embedding_config: inherit_rag
  extraction_context_messages: 8
  max_candidates: 5
  max_candidate_chars: 500
  worker_concurrency: 1
  task_timeout: 120s
  lease_duration: 180s
  max_attempts: 5
  retry_initial_delay: 2s
  retry_max_delay: 60s
  retrieval_top_k: 5
  max_context_chars: 2000
  audit_retention_days: 30
  turn_retention_days: 30
  job_success_retention_days: 7
  job_failed_retention_days: 30
  source_retention_days: 90
  version_keep_count: 3
  cleanup_batch_size: 500
  cleanup_interval: 24h
```

校验：MySQL 会话必须可用；集合不能等于文档集合；Embedding 配置有效；候选数/上下文/并发有上限；lease_duration 大于 task_timeout 或实现可靠续租。租约领取/完成使用 lease_token 条件更新，过期 worker 不能确认别人的任务。

敏感内容识别与自然语言判断不能宣称完全准确；通过最小化输入、规则和带标注样例评测降低误存，审计只保留必要原因与标识。来源快照的生命周期须与删除语义、审计保留策略一致，不能在日志中永久保留已经要求忘记的正文。

维护任务每次运行会记录结构化清理报告，包含表行数估算、数据/索引大小、有效事实和任务队列数量，以及删除、脱敏、过期撤销和向量删除任务数量；报告不记录用户原文。

## 11. 状态、接口与用户反馈

任务状态 pending → running → succeeded；暂时故障回到 pending 并设置重试时间，耗尽次数为 failed。failed 是可重试故障，不是待人工审批。无效提取为已处理的 ignore/unresolved，避免无限重试。

已提供：

- `GET /memory/facts`：当前可信范围内生效记忆，返回来源、版本、索引状态。
- `GET /memory/turns/{turn_id}`：pending/processed/failed 与判定原因。
- `DELETE /memory/facts/{id}`：先 SQL 撤销并排队删除向量，立即停止检索使用。
- `POST /memory/jobs/{id}/retry`：幂等重试失败任务，仍校验范围与当前版本。

HTTP/WS 均支持 turn_id 关联；回复完成后的记忆处理有独立状态事件，不在已经结束的 run 后继续伪造 run 完成事件。页面可以显示“已记住/未形成长期记忆/索引处理中/处理失败”，不显示审批按钮，不对每次闲聊弹通知。

日志指标：候选数、ignore 原因、insert/update/revoke 数、提取延迟、队列深度、索引失败/积压、重试次数。日志不输出用户敏感原文。

## 12. 文档索引同步补强

1. 固定知识与记忆集合隔离；若名称相同启动失败。
2. 当前 indexer 在新向量写入前清理 orphan，改为新写入成功后清理已知归属的旧向量，至少保证写入失败不先丢旧知识。
3. RemovedChunks 对 ID 去重，避免 orphan 和 stale 两条路径重复计数。
4. ListIDs 改为经过验证的完整分页/游标遍历，无法列全时返回错误，不把固定 16384 上限当作全量结果。
5. 指纹补充后端地址、数据库、集合/索引、Redis 前缀及索引版本，凭证不写入 Manifest。
6. 为同一索引目标建立跨进程串行锁，命令行和未来管理接口共同遵守，不能只依赖单个 Indexer 的 mutex。
7. 清理限定为该索引器拥有的文档 ID；现有集合若无法证明归属，只能按历史 Manifest 精确清理，不进行不加区分的全局删除。
8. 全量切换模型/维度采用新目标回填再切换；仅把删除移到后面不能保证多块更新原子性，不能宣称得到事务级发布。

## 13. 实施顺序与阶段完成条件

### P1：配置、身份与持久化基础

完成 Memory 配置、显式迁移、范围绑定、稳定 turn/message ID、来源生命周期、SQL 任务租约。完成条件：重启不丢已提交任务；文件模式误开功能有清晰错误；现有聊天测试通过。

### P2：自动提取与判定

实现 Prompt、严格解析、来源校验、类型/敏感/抑制规则、精确去重、同 key 更新和撤销；先 SQL 读写验证。完成条件：闲聊不生成记忆；用户确认与假设可区分；并发重复不重复插入；错误结构不写库。

### P3：接入所有对话成功入口

ChatWithSink、ApproveWithSink 最终完成、控制台使用统一协调器；快捷回复排除；错误/取消不生效；成功轮次与 extract 任务事务提交。完成条件：HTTP/WS/控制台行为一致，同轮审批恢复仅提取一次。

### P4：向量索引、检索与恢复

专用 Milvus 集合、不可变版本 ID、Outbox worker、范围内查询、SQL 版本复核、重建命令。完成条件：Embedding/Milvus 失败可恢复；旧任务和撤销并发不能使旧记忆再次可见；切换模型可回填。

### P5：查看、删除、可观察性与文档补强

实现状态/删除/重试接口、删除抑制、日志指标和第 12 节全部修复。完成条件：用户可追溯保存原因和来源；文档重建不影响记忆；索引超出单页仍完整对账。

### P6：本地真实环境验收

使用独立测试数据库或测试表前缀与独立 Milvus 集合，固定模型/Prompt版本运行下述验收。通过后在本地配置启用，记录实际结果、失败项和耗时；不能用少数问答成功代表自动记忆已完整完成。

## 14. 测试与验收矩阵

| 场景 | 必须观察到的结果 |
|---|---|
| 寒暄、感谢、普通技术问答 | 聊天可保存；正式记忆和向量新增均为 0 |
| 明确用户偏好 | SQL 生效一条；下轮使用；默认不生成向量 |
| 明确项目决策 | SQL 生效一条，最终出现对应版本向量 |
| 同一事实重复表达 | 无新增正式事实/版本；必要时仅补充来源 |
| 明确更换选型 | 版本增加，检索只使用新版本 |
| 否定、假设、引述他人、助手建议 | 不误记为用户/项目已确认事实 |
| “好，就这个”的唯一/歧义指代 | 唯一时确认，歧义时不生效 |
| 错误 JSON、无效枚举、伪造来源 | 不写正式记忆，记录原因 |
| 密钥、验证码、“不要记住” | 不进入记忆、向量及敏感审计正文 |
| 旧任务执行中要求忘记 | 即时不可检索，后台不能复活 |
| 已保存会话时进程崩溃 | 成功提交的 extract 任务可恢复，不丢失 |
| SQL 写入成功、Embedding/Milvus 失败 | 记忆存在，任务可重试并最终 indexed |
| 两个 worker、新旧版本任务乱序 | 无重复版本，SQL 当前版本是读取依据 |
| 工具审批中断及多次恢复 | 最终成功后同 turn_id 仅提取一次 |
| 不同用户/项目使用同一 key | 事实与搜索结果隔离，不串用 |
| 删除/失效记忆仍残留向量 | SQL 校验挡住，随后物理清理 |
| 关闭功能 | 不投递新记忆任务，不注入记忆；已有数据保留 |
| 文档索引失败/跨进程并发/超过 16384 条 | 不提前删旧数据，不交叉覆盖 Manifest，不静默漏项 |
| 运行文档索引后查询记忆 | 记忆集合无变化 |

测试分层：

1. Go 单元测试使用可控 Extractor/Embedder，检验规则、事务调用和状态转换。
2. MySQL 集成测试覆盖事务回滚、唯一约束、租约恢复、版本竞争和删除抑制。
3. Milvus 集成测试覆盖字段过滤、版本索引、清理和真实向量维度。
4. 真实模型评测至少 60 条标注样例，覆盖闲聊、稳定事实、假设、否定、纠正、指代和敏感信息；单独报告误存率、漏存率、类型/范围正确率，不把模型自报分数当准确率。
5. 初始发布门槛：隔离/删除/敏感阻断的确定性测试全部通过；标注负例无误存，正例提取召回率至少 90%；未通过则先调整规则/Prompt再复测。有限样例通过不代表生产中零错误。

检查命令以现有项目为准：`go test -p 1 ./...`、`go vet ./...`；真实模型与数据库测试使用显式集成测试入口，不要求普通单测访问外部服务。

## 15. 关闭、回滚与最终交付

memory.enabled=false 时停止新提取和记忆注入，并停止 worker 领取新任务；正在执行的任务按服务停机流程结束或租约恢复。重新开启后仅恢复未被抑制且仍有效的任务。

SQL 是权威记录。向量索引异常可从 SQL 重建，无需重新分析所有聊天。数据库迁移采用追加方式；回滚应用不自动删除记忆表或集合。要求彻底清除时走明确的范围删除流程。

最终交付应包含：实现代码、完整 SQL 迁移、配置说明、自动提取 Prompt、回归样例、真实测试报告、重建/故障恢复说明。完成条件是“自动判断—保存—索引—检索—更新—撤销—恢复”全部可验证，而不只是新增一段 Prompt 或成功插入一条向量。
