# 项目启动与常用命令

以下命令均在 Windows PowerShell 中执行。除 Docker/Ollama 状态检查外，先进入
项目目录：

```powershell
cd E:\11\my-eino-app
```

## 一、日常推荐启动

推荐架构：Ollama 运行在 Windows，Milvus/etcd/MinIO 运行在 Docker，Go 应用
运行在 Windows。这样最适合本地开发和使用 GPU。

```powershell
# 1. 确认 Ollama 及模型正常
ollama list

# 2. 启动 Milvus、etcd、MinIO
make infra-up

# 3. 查看容器状态
make ps

# 4. 启动 Go 后端和内置前端
make run
```

浏览器访问：

```text
http://localhost:18181
```

本地模式（`make run`）固定用 **18181**，容器模式（`make up`）固定用 **18180**：
compose 已把宿主 18180 映射给 app 容器，两者同时运行会抢占同一个宿主端口。

测试本地文件读取：先把 UTF-8 文本放入 `workspace-files`，然后在网页或控制台明确
输入“读取 README.txt 并概括内容”。默认不会读取该目录之外的文件。

停止 Go 服务：在运行 `make run` 的终端按 `Ctrl+C`。Milvus 可以继续运行，
下次无需重新启动。

## 二、首次运行或知识文档改变

首次运行需要确认模型存在，并把 `docs/knowledge` 中的文档写入 Milvus：

```powershell
ollama pull qwen3.5:9b
ollama pull bge-m3
make infra-up
make index
make run
```

以后新增、修改或删除知识库文档后，只需重新执行：

```powershell
make index
```

索引器会根据哈希清单增量处理，不会重复写入未变化的内容。

## 三、不使用 Makefile 的等价命令

```powershell
docker compose -f docker-compose.milvus.yml up -d etcd minio milvus
docker compose -f docker-compose.milvus.yml ps
go run ./cmd/indexer
go run ./cmd/server -addr 127.0.0.1:18181
```

## 四、完整 Docker 启动

完整容器模式会把 Go 应用也放入 Docker；Ollama 仍运行在 Windows，并由容器
通过 `host.docker.internal:11434` 访问。

```powershell
ollama list
make up
make ps
make app-logs
```

访问 `http://localhost:18180`。首次构建需要从 Docker Hub 下载 Go/Alpine 基础
镜像；若出现 TLS timeout 或 EOF，先检查 Docker Desktop 网络后重试 `make up`。

停止完整 Docker 服务并保留 Milvus 和 Session 数据：

```powershell
make down
```

## 五、测试与验收

```powershell
# 格式化、单元测试和静态检查
make check

# 单独运行各项
make fmt
make test
make vet

# 真实 Ollama + Milvus 集成测试
.\scripts\test-integration.ps1

# 集成测试同时执行固定 RAG 评测
$env:RUN_RAG_EVAL = "1"
.\scripts\test-integration.ps1
Remove-Item Env:RUN_RAG_EVAL

# 单独运行固定评测
make eval

# 查看某个问题的 Milvus 原始检索分数
go run ./cmd/retrieve "星河系统使用什么技术栈？"
```

## 六、控制台和 Session 管理

```powershell
# 交互式控制台
go run ./cmd/console

# 单次提问
go run ./cmd/console "星河系统使用什么技术栈？"

# 查看 Session 命令参数
go run ./cmd/sessions -help
```

启用自动记忆时，首次部署先执行显式迁移；命令按当前配置迁移基础会话表、记忆表和已启用的执行记录表：

```powershell
go run ./cmd/session-migrate
# 已有记忆表缺少清理索引时，由 DBA/迁移账号单独执行：
go run ./cmd/session-migrate -memory-indexes
go run ./cmd/memory-reindex       # 只统计当前范围内需要向量化的记忆
go run ./cmd/memory-reindex -execute
```

记忆写入由服务后台自动完成。普通闲聊只保存会话，不进入记忆或向量库；项目决策、里程碑和待办等通过规则校验后进入 MySQL，需向量检索的类型再进入 `my_eino_memory_v1`。

MySQL 容量控制：会话按 `session.expire_days` 清理；记忆轮次、任务、判定、来源和历史版本按 `memory.*_retention` 配置由后台维护任务分批清理。当前有效 `memory_facts` 不会按时间删除，`pending/running` 任务不会被清理。首次部署可以直接执行 `go run ./cmd/session-migrate`；已有表补充索引需要具备 `ALTER` 权限的迁移账号执行 `go run ./cmd/session-migrate -memory-indexes`。

每轮维护会在应用日志输出一条 `memory maintenance` JSON 记录，包含清理前后表规模、数据与索引大小、有效事实数、队列数量和本轮删除/脱敏/撤销/向量删除任务数量。表规模来自 InnoDB 的估算值，适合观察增长趋势；日志不包含用户原文。

## 七、Docker 状态和日志

```powershell
make ps
make logs
make app-logs

# Docker 引擎状态
docker version
docker info

# 查看正在运行的容器
docker ps

# 只停止应用容器
docker compose -f docker-compose.milvus.yml stop app

# 重启应用容器
make restart
```

## 八、端口占用检查

```powershell
Get-NetTCPConnection -LocalPort 18180 -ErrorAction SilentlyContinue
Get-NetTCPConnection -LocalPort 18181 -ErrorAction SilentlyContinue
Get-NetTCPConnection -LocalPort 11434 -ErrorAction SilentlyContinue
Get-NetTCPConnection -LocalPort 19530 -ErrorAction SilentlyContinue
```

主要端口：

- `18180`：Go Web/API 容器模式的宿主机端口（容器内仍监听 8080）
- `18181`：本地 `make run` 的监听端口，仅绑定 `127.0.0.1`，不对外暴露
- `11434`：Ollama
- `19530`：Milvus gRPC
- `9091`：Milvus 健康检查

## 九、数据清理注意事项

正常停止请使用：

```powershell
make down
```

不要在需要保留知识库和会话时执行：

```powershell
docker compose -f docker-compose.milvus.yml down -v
```

其中 `-v` 会删除 Milvus 和应用的 Docker 数据卷，需要重新建立知识库索引。



