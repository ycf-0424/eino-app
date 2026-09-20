# Eino 企业智能助手

这是一个可私有部署的中文企业智能助手。项目把文档知识、项目长期记忆、会话数据和执行记录分开保存，并通过服务端路由保证私有问题先查证据。

## 当前能力

- 文档知识库检索：Milvus（也支持 Redis 适配层）
- 项目长期记忆：MySQL 元数据 + 独立 Milvus Memory Collection
- 问答链路：项目文档 → 项目长期记忆 → 通用模型兜底
- 文本和 DOCX 读取（仅限授权目录）
- 文本报告组织、审批后的笔记写入
- 飞书登录、本地账号登录和多用户会话隔离
- HTTP / WebSocket 聊天、执行事件、限流、队列超时和 504
- 备份、恢复演练、健康检查、指标和不可变镜像回滚

当前没有对外承诺完整的 Excel、PDF、PPT、音频、视频或业务系统操作能力。阶段 6 的多模态和内部系统连接器属于后续按需扩展，不影响阶段 5 的生产上线。

## 目录

| 路径 | 用途 |
|---|---|
| `cmd/server` | HTTP/WebSocket 服务 |
| `cmd/indexer` | 将授权知识文档写入向量库 |
| `cmd/eval` | 固定 RAG 和路由评测 |
| `internal/server` | 服务编排、认证、会话和 API |
| `internal/eino` | 模型、Agent、工具和 RAG 适配层 |
| `internal/memory` | 长期记忆提取、索引和查询 |
| `docker-compose.milvus.yml` | 本地开发栈 |
| `docker-compose.prod.yml` | 独立生产栈 |
| `deploy/openresty/my-eino-app.conf` | 1Panel/OpenResty 反向代理模板 |
| `docs/EXECUTION-PLAN.md` | 唯一整体执行方案 |
| `docs/RUNBOOK.md` | 命令、上线、备份、恢复和回滚手册 |

## 本地开发

要求 Go、Docker Compose、Ollama（或 OpenAI 兼容模型服务）和 Milvus 依赖。

```bash
docker compose -f docker-compose.milvus.yml up -d
make check
make index
make eval
```

启动服务：

```bash
make run
```

浏览器访问 `http://127.0.0.1:18181`。普通单元测试使用串行编译，避免低内存环境并行编译失败：

```bash
go test -p 1 ./...
go vet ./...
```

## 生产部署

生产环境不要使用开发 Compose。构建并推送不可变镜像后，在 1Panel 所在服务器准备 `.env.prod`，再使用独立生产 Compose：

```bash
docker compose --env-file .env.prod -f docker-compose.prod.yml config
docker compose --env-file .env.prod -f docker-compose.prod.yml up -d --no-build
docker compose --env-file .env.prod -f docker-compose.prod.yml ps
```

生产服务只在 Docker 内部网络通信，应用默认绑定服务器 `127.0.0.1:18180`，由 1Panel/OpenResty 提供 HTTPS、WebSocket 和域名入口。首次启动还需运行数据库迁移和 `user-admin` 建立管理员账号。完整顺序见 [`docs/RUNBOOK.md`](docs/RUNBOOK.md) 的生产章节。

`.env.prod` 含数据库、模型、飞书和指标凭据，禁止提交到 Git。生产数据卷与本地测试卷分开；正式上线前必须完成备份、恢复演练和 3–7 天内部灰度。

## 数据边界

- MySQL：会话、账号、长期记忆元数据和执行记录
- `my_eino_knowledge`：项目文档向量
- `my_eino_memory_v1`：允许进入长期记忆的项目事实、决策、里程碑和待办向量
- `app-data`：checkpoint、笔记和执行文件

聊天记录不会自动写入文档知识库。只有符合长期记忆规则的项目事实才会由后台异步提取并进入独立 Memory Collection。

## 文档入口

- [整体执行方案](docs/EXECUTION-PLAN.md)
- [上线与运维手册](docs/RUNBOOK.md)
- [HTTP/WebSocket API](docs/API.md)
- [生产环境变量模板](.env.prod.example)
- [1Panel/OpenResty 配置模板](deploy/openresty/my-eino-app.conf)

## 许可与凭据

仓库不应包含真实 API Key、数据库密码、飞书 Secret 或生产 `.env.prod`。部署时通过环境变量或 1Panel Secret 注入。
