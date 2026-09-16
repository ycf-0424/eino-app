# Docker 与 Makefile 使用说明

完整的日常启动、测试、诊断和停止命令见 [COMMANDS.md](COMMANDS.md)。

MySQL 会话接入、已有实例迁移和历史导入见 [MYSQL.md](MYSQL.md)。当前 Compose 复用原 `gozero_mysql_data` 数据卷重建 MySQL；启用 `SESSION_STORE=mysql` 后，会话保存到 `eino` 数据库。

项目支持两种运行方式。开发时推荐宿主机运行 Go、Docker 只运行 Milvus；完整
容器化时，应用、Milvus、etcd 和 MinIO 全部由 Compose 管理。Ollama 仍运行在
Windows 宿主机，因为本地模型需要直接使用主机 GPU。

## 开发模式

```powershell
make infra-up
make index
make run
```

访问 `http://localhost:18180`。这一模式读取项目根目录的 `config.yaml`，其中
Ollama 和 Milvus 地址均为 `localhost`。

## 完整 Docker 模式

先确认 Windows 上的 Ollama 已启动且模型存在：

```powershell
ollama list
docker compose -f docker-compose.milvus.yml up -d --build
docker compose -f docker-compose.milvus.yml ps
```

应用容器读取 `config.docker.yaml` 构建出的 `/app/config.yaml`：

- Ollama：`host.docker.internal:11434`
- Milvus：`milvus:19530`
- Web：`http://localhost:18180`

文件存储模式的 Session、Checkpoint 和工具输出保存在 `app-data` 命名卷中；MySQL 模式的 Session 保存在 Compose 管理的 MySQL 容器及原数据卷。Milvus 数据保存在
三个 `milvus-*` 命名卷中。执行 `make down` 只停止容器，不会删除这些数据卷。

## 常用命令

```powershell
make check       # 格式化、单元测试、静态检查
make image       # 只构建 my-eino-app:local 镜像
make up          # 启动完整服务
make ps          # 查看健康状态
make app-logs    # 查看应用日志
make down        # 停止服务并保留数据
```

不要随意执行 `docker compose down -v`，`-v` 会同时删除 Milvus 知识库和应用会话。


