# MySQL 会话存储

项目复用已有 MySQL 实例，在 `eino` 中保存会话。Milvus 继续保存知识库向量，聊天不会自动写入 Milvus。

## 本机已完成的接入

- 原实例：`gozero-mysql`，宿主机映射端口 `3306`；当前 Compose 服务：`mysql`，使用同一 external 数据卷 `gozero_mysql_data`。
- 数据库：`eino`；表：`conversations`、`messages`，以及执行记录表 `execution_runs`、`execution_events`（由 `execution_events.enabled` 控制，默认关闭时不会写入）。
- 应用账号：`eino_app`，仅拥有 `eino.*` 的 SELECT、INSERT、UPDATE、DELETE 权限。
- 本机 `.env` 已配置 MySQL 模式；密码不提交到仓库，也不写入本文档。
- 已导入宿主机 `data/sessions/` 中的 3 个会话、9 条消息，源 JSON 保留。

重启 Go 应用后生效：

```powershell
make run
```

Docker 应用需要重新构建以包含新代码：

```powershell
make up
```

Compose 中的 `mysql` 服务固定为原镜像 digest，并挂载同一 external 数据卷；应用通过 Compose 网络的 `mysql:3306` 连接。原 `gozero-mysql` 容器已停止但保留，确认新服务稳定后可手动删除；回退时先停止新服务再启动旧容器。宿主机 Go 仍使用 `localhost:3306`。未配置 `SESSION_STORE` 的旧部署默认继续使用文件存储。

## 实时保存行为

1. 调用模型之前，在短事务中保存用户消息和 `generating` 助手占位消息。
2. 流式输出期间，每隔至少三秒在新片段到达时保存当前内容。
3. 正常结束写入 `completed`；失败写入 `failed`，上下文取消写入 `cancelled`；工具等待审批写入 `awaiting_approval`。
4. 存储完整历史，只有发送给模型的输入才使用长度限制和压缩。

每条消息的 `payload` 保存完整 `schema.Message` JSON，`content`、`role` 和 `status` 便于 Navicat 查询。现有 Agent 对外保存的是用户消息和汇总回复；框架内部每次工具调用的完整事件审计尚不在本次范围。导入源 JSON 中已有的工具字段会保留。

生成期间不持有长事务，不需要额外定时任务。MySQL 保存使用会话行锁和历史前缀检查，拒绝旧快照覆盖历史。每个生成占位消息具有独立请求标识，但目前 HTTP 尚未提供客户端幂等键：用户再次发送同一问题仍视为新请求。

## 在 Navicat 查看

使用 `localhost:3306`、数据库 `eino`、账号 `eino_app` 和 `.env` 中的密码连接。

```sql
SELECT id, created_at, updated_at
FROM conversations ORDER BY updated_at DESC;

SELECT conversation_id, sequence, role, status, content
FROM messages ORDER BY conversation_id, sequence;

-- 执行记录（开启 execution_events 后才有数据）
SELECT run_id, conversation_id, status, user_sequence, assistant_sequence, started_at, finished_at
FROM execution_runs ORDER BY started_at DESC;

SELECT run_id, sequence, type, occurred_at, payload
FROM execution_events ORDER BY run_id, sequence;
```

`sequence` 从 0 开始。同一会话与序号的唯一键保证相同快照重复保存不增加消息。

## 新环境初始化和导入

先由管理员在已有实例中创建 `eino` 和应用账号。不要在日常应用启动时自动创建数据库或使用 root。迁移前应备份原数据卷；本次备份记录在 `data/backups/`，该目录已被忽略，不会提交。

初始建表 SQL 位于 `internal/session/migrations/001_sessions.sql`，可在 Navicat 中选中 `eino` 后执行。也可以临时通过进程环境变量配置有建表权限的迁移账号，再运行 `make db-migrate`。`session-migrate` 会按当前配置显式执行基础会话表、自动记忆表和已启用的执行记录表迁移；关闭 `execution_events` 时不会要求创建 `002_execution.sql` 中的表，因此 DML-only 应用账号可以重复检查已有表。若之后开启执行记录，需先由 DBA 或具备 CREATE 权限的迁移账号执行一次迁移。当前是初始幂等 DDL，未来表结构升级需增加有版本记录的迁移流程，修改 CREATE TABLE 不会升级已有表。

执行记录表结构：

- `execution_runs`：`run_id`（主键）、`conversation_id`（外键，`ON DELETE CASCADE`）、`user_sequence`、`assistant_sequence`、`status`、`started_at`、`finished_at`。
- `execution_events`：`event_id`（主键）、`run_id`（外键，`ON DELETE CASCADE`）、`sequence`、`type`、`occurred_at`、`payload`（JSON），并有 `(run_id, sequence)` 唯一约束。

关联键说明：`messages` 主键是 `(conversation_id, sequence)`，没有独立 `id`。这里 `sequence` 是完整历史数组下标，且 MySQL 明确拒绝截断历史，因此可稳定用作关联键。`RunStarted` 早于首次 `session.Save`，所以 `StartRun` 会先 `INSERT IGNORE INTO conversations(id)` 保证外键成立。删除会话时先删 `conversations` 行，`messages` 与执行记录由外键级联清理；文件后端则由 `DeleteBySession` 删除对应 JSONL。

保留策略：启动时按 `execution_events.retention_days`（默认 7 天）清理过期记录，并把超时仍为 `running` 的 run 置为 `failed{interrupted}`，避免前端刷新后永久显示运行中。

应用账号没有建表权限，导入命令只读写已有表：

```powershell
# 预览，不连接数据库
go run ./cmd/session-migrate -source data/sessions -dry-run

# 正式导入；先停止旧应用写入 JSON
make sessions-import
```

### 数据保留与清理

服务在 MySQL 会话模式下按 `session.expire_days` 定期分批删除过期会话；删除会话时会同步删除对应的记忆轮次、提取任务、判定和绑定，消息由 `conversations -> messages` 外键级联删除。记忆来源会由 Worker 按 `source_retention_days` 清理，但当前有效事实的来源会保留。记忆 Worker 按 `memory.turn_retention_days`、`job_success_retention_days`、`job_failed_retention_days`、`source_retention_days`、`audit_retention_days` 和 `version_keep_count` 清理终态历史数据；当前有效事实不删除，`pending/running` 任务保留。撤销或过期事实的 Milvus 向量仍通过 `delete` 任务异步回收。

清理使用小批量事务，默认每批 500 行、每天运行一次。新建记忆表会自动带清理索引；已有表补索引需要 DBA 使用具备 `ALTER` 权限的账号执行：

```powershell
go run ./cmd/session-migrate -memory-indexes
```

应用账号只有 DML 权限时，普通 `go run ./cmd/session-migrate` 不会尝试 `ALTER TABLE`，不会阻断服务启动；缺少索引时清理仍可运行，但大数据量下建议尽快由 DBA 补齐索引。

每次记忆维护都会输出一条结构化 `memory maintenance` 日志。日志包含各表的清理前后规模（`information_schema.TABLE_ROWS` 是 InnoDB 估算值，`bytes` 为数据和索引大小）、当前有效事实数、待处理/终态任务数，以及本轮删除、脱敏、过期事实撤销和向量删除任务数量。日志不包含用户消息、事实正文或模型输入；快照失败只影响统计，不会阻断实际清理。

导入保留会话 ID、消息顺序及源消息字段。重复导入相同快照不会重复插入；若数据库历史已经变化，则拒绝覆盖。源文件未记录单条消息时间，因此旧消息的 `created_at` 是导入时间。之前已被 JSON 压缩删除的原文无法恢复。

如果旧应用运行在 Docker，先从原应用容器的 `/app/data/sessions` 导出 JSON，再把该目录作为 `-source`；宿主机导入不会自动遍历 `app-data` 卷。

## 异常退出和恢复

进程被强制结束可能留下 `generating` 状态；会保留最后一次保存的部分回答。确认原请求或进程已经停止后运行：

```powershell
go run ./cmd/sessions -interrupt <会话ID>
```

该命令将状态改为 `interrupted`，然后可以继续对话。仍在运行的原请求将无法覆盖该状态。不要对活跃请求执行此命令。

Checkpoint、待审批任务和工具输出仍保存在配置的文件目录或 `app-data` 卷中；P8 的执行记录表只保存事件元数据与脱敏 `payload`，不迁入完整工具输出。长期记忆已通过 `memory_*` 表和独立 Milvus 集合实现，当前身份模式仍是受控的 `local_single_user`，多人部署需另行接入认证。

切回 `SESSION_STORE=file` 会读取旧 JSON，不会自动反向同步迁移后的新消息。回退前应备份 MySQL 并另行导出新消息，不能直接把旧 JSON 当作最新数据。

## 验证

```powershell
go test -p 1 ./...
go test -tags mysql_integration -count=1 ./internal/server ./internal/session
```

MySQL 集成测试使用唯一测试会话并清理，不依赖 Ollama，也不删除已有会话。服务层测试通过模拟 SSE 模型响应验证调用前入库及连续对话保留完整历史。
