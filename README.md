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

### 启动依赖服务

先启动 MySQL、Milvus、etcd 和 MinIO。首次使用或清理过数据卷时，等待容器全部变为 healthy：

```bash
docker compose -f docker-compose.milvus.yml up -d mysql etcd minio milvus
docker compose -f docker-compose.milvus.yml ps
```

Windows PowerShell 也可以使用 Makefile：

```powershell
make infra-up
```

### 配置模型

项目支持 Ollama 与远程 OpenAI 兼容接口同时存在。直接在 [`config.yaml`](config.yaml) 的 `models` 段配置多个模型：

```yaml
active_model: ollama-local
models:
  - id: ollama-local
    provider: ollama
    api_key: "ollama"
    model: "qwen3.5:9b"
    base_url: "http://localhost:11434/v1"
    context_window_tokens: 0 # 填本地模型服务实际启用的上下文窗口
  - id: ark-doubao
    provider: volcengine-ark
    api_key: "${ARK_API_KEY}"
    model: "doubao-seed-2-0-pro"
    base_url: "https://ark.cn-beijing.volces.com/api/v3"
    max_completion_tokens: 4096
    context_window_tokens: 0 # 填提供商公布的总上下文窗口；0 表示未知，使用固定历史上限
```

前端会从 `/models` 动态显示模型。选择“自动混合”时，服务先用 Qwen 9B 做一次难度分类：简单问题继续由 Qwen 9B 回答，复杂问题、低置信度或分类失败时切换到 `strong_model`（当前为火山方舟 DeepSeek）；手动选择具体模型则跳过分类并固定使用该模型。API Key 建议写入 `.env`，例如：

设置 `context_window_tokens` 后，服务会为选中的模型按“总上下文窗口 − 回复额度 − 系统/工具提示估算 − 检索前缀 − 安全余量”动态裁剪历史；自动路由有回退模型时按两者中较小的窗口计算。Token 数通过保守估算而非供应商专用分词器计算。窗口值为 `0` 时使用旧的 `session.max_messages` / `session.max_chars` 固定上限；配置了窗口时则由 Token 预算控制历史，并受 `session.hard_max_messages` / `session.hard_max_chars` 兜底约束。旧对话摘要另受 `session.max_summary_chars` 限制。

```powershell
$env:ARK_API_KEY = "ark-xxx"
```

如果不配置 `models`，程序继续使用原来的 `openai.api_key`、`openai.model` 和 `openai.base_url` 配置。

自动路由可在 `config.yaml` 中调整：

```yaml
agent:
  auto_routing:
    enabled: true
    fast_model: qwen-fast
    strong_model: deepseek-ark
    confidence_threshold: 0.7
    classifier_timeout: 8s
```

认证开启时，首页允许未登录用户先打开；右上角个人信息区域会显示“未登录”和登录入口。未登录状态不能新建会话或发送消息，登录成功后才可以开始对话。

### 启动、索引和测试

首次启动或日常启动都推荐使用：

```bash
make start
```

`make start` 会依次启动 MySQL/Milvus 等依赖、执行数据库迁移，并启动后端和内置前端。启动后浏览器访问 `http://127.0.0.1:18181`。在 GoLand 中打开项目后，打开底部 **Terminal**，进入项目根目录，输入这一条命令即可：

```powershell
make start
```

知识库首次初始化或文档发生变化时，再单独执行：

```powershell
make index
```

运行中的管理员也可以在聊天页面右上角打开“知识库”：上传或删除文档后服务端会自动执行增量索引，并显示索引结果。普通员工只能检索知识库，普通聊天不会把回答自动写回知识库。管理员权限来自本地账号的 `is_admin`，或 `auth.admin_owners` 中配置的飞书 owner；所有写入仍由后端校验，前端按钮不是权限边界。

也可以直接调用受保护的管理接口：

```text
GET    /knowledge/documents
POST   /knowledge/documents       # multipart/form-data，字段名 file
DELETE /knowledge/documents/{name}
POST   /knowledge/reindex
```

文档修改工具生成的 DOCX 会按登录用户隔离保存，并在助手消息中提供受控下载链接；源文件不会被覆盖。

`make check` 是代码检查，`make eval` 是评测命令，都不是日常启动必需步骤。

普通单元测试使用串行编译，避免低内存环境并行编译失败：

```bash
go test -p 1 ./...
go vet ./...
```

不使用 Makefile 时，可直接运行 Go 命令：

```powershell
go run ./cmd/server -addr 127.0.0.1:18181
go run ./cmd/indexer
go run ./cmd/retrieve "星河系统使用什么技术栈？"
go run ./cmd/eval
```

如果只想启动 Docker 内的完整应用栈：

```bash
docker compose -f docker-compose.milvus.yml up -d --build
docker compose -f docker-compose.milvus.yml logs -f --tail=200 app
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
