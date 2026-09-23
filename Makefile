APP := my-eino-app
SERVER := eino-server
COMPOSE := docker compose -f docker-compose.milvus.yml

.DEFAULT_GOAL := help

# 先 make db-migrate，再 make sessions-import；使用项目 .env 中的连接信息。
.PHONY: db-migrate db-memory-indexes sessions-import
db-migrate:
	go run ./cmd/session-migrate

db-memory-indexes:
	go run ./cmd/session-migrate -memory-indexes

sessions-import:
	go run ./cmd/session-migrate -source data/sessions

.PHONY: help fmt boundary test vet check start run stop restart restart-container build index retrieve eval \
	backup restore-check backup-linux restore-check-linux health-alert-linux \
	image prod-up prod-rollback infra-up up attu down restart ps logs app-logs

help: ## 显示可用命令
	@echo "make db-migrate  - 在现有 MySQL 项目库中初始化会话表"
	@echo "make db-memory-indexes - 使用具备 ALTER 权限的账号补充记忆清理索引"
	@echo "make sessions-import - 将本地 JSON 会话导入 MySQL"
	@echo "make check       - 格式化、边界校验、测试并执行静态检查"
	@echo "make run         - 在宿主机启动 Go 服务（127.0.0.1:18181）"
	@echo "make start       - 启动依赖、迁移数据库并启动应用（推荐）"
	@echo "make stop        - 停止占用 18181 的本地 Go 服务"
	@echo "make restart     - 停止旧服务并重新启动本地 Go 服务"
	@echo "make infra-up    - 启动 MySQL/Milvus/etcd/MinIO"
	@echo "make attu         - 启动 Milvus 和 Attu 管理界面"
	@echo "make up          - 构建并启动完整 Docker 服务"
	@echo "make down        - 停止容器，保留数据卷"
	@echo "make index       - 使用宿主机 Go 将文档写入 Milvus"
	@echo "make eval        - 执行固定 RAG 评测"
	@echo "make backup      - 备份 MySQL 与 Milvus 数据卷（保留最近 14 份）"
	@echo "make restore-check - 用最近一份备份做恢复演练并比对行数"
	@echo "make backup-linux - Linux 生产备份（使用 .env.prod）"
	@echo "make restore-check-linux - Linux 生产恢复演练"
	@echo "make prod-up VERSION=x - 使用不可变镜像启动生产 Compose"
	@echo "make prod-rollback VERSION=x - 切换到指定的上一版本镜像"
	@echo "make logs        - 查看全部容器日志"

fmt: ## 格式化 Go 代码
	go fmt ./...

# 依赖边界守卫：compose/adk/callbacks 只允许出现在 internal/eino/ 内。
# 用 Go 实现而非 grep 管道 —— 本机 make 走 cmd.exe，POSIX 写法在那里跑不起来。
boundary: ## 校验 eino 编排 API 未扩散到 internal/eino 之外
	go run ./cmd/boundary

test: ## 运行不依赖外部服务的测试；串行编译避免本机内存不足
	go test -p 1 ./...

vet: ## 执行 Go 静态检查
	go vet ./...

check: fmt boundary test vet ## 提交前完整检查

# 本地端口固定 18181：compose 的 app 服务已把宿主 18180 映射给容器（18180:8080），
# 两者同时运行会抢占同一个 (IP, 端口)。Windows 下 Go 不设 SO_REUSEADDR，无法共存。
# 要改回 18180，先停容器：docker compose -f docker-compose.milvus.yml stop app
# 绑定 127.0.0.1 而非全部网卡，避免本地服务被同局域网机器直连。
run: ## 本地启动后端和内置前端（127.0.0.1:18181）
	go run ./cmd/server -addr 127.0.0.1:18181

stop: ## 停止占用 18181 的本地 Go 服务（Windows）
	powershell -NoProfile -ExecutionPolicy Bypass -Command "$$c=Get-NetTCPConnection -LocalPort 18181 -State Listen -ErrorAction SilentlyContinue; if ($$c) { $$c | Select-Object -ExpandProperty OwningProcess -Unique | ForEach-Object { Stop-Process -Id $${_}.ToString() -Force -ErrorAction SilentlyContinue } }; exit 0"

restart: ## 停止旧服务并重新启动本地 Go 服务
	$(MAKE) stop
	$(MAKE) run

start: ## 启动本地依赖、执行数据库迁移并启动应用
	$(MAKE) infra-up
	$(MAKE) db-migrate
	$(MAKE) run

build: ## 构建当前操作系统可执行文件
	go build -trimpath -o $(SERVER) ./cmd/server

index: ## 索引 docs/knowledge 文档，需要 Ollama 和 Milvus 已运行
	go run ./cmd/indexer

retrieve: ## 使用示例问题查看原始检索分数
	go run ./cmd/retrieve "星河系统使用什么技术栈？"

eval: ## 执行生产态本地 Ollama + Milvus 评测并检查路由/工具门槛
	go run ./cmd/eval -debug=false -min-correct=0.8 -min-citation=1 -min-refusal=1 -min-server-route=0.95 -max-no-skill-false-positive=0 -max-forbidden-tool-violations=0 -min-tool-attempt=1 -min-tool-success=1 -min-tool-source=1

# 备份与恢复演练（步骤 5.3）。用 PowerShell 而非 make 内联命令：
# dump 与卷打包都涉及字节流，make 走 cmd.exe 更容易被引号与编码坑到。
backup: ## 备份 MySQL 与 Milvus 卷到 data/backups（保留最近 14 份）
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/backup.ps1

restore-check: ## 用最近一份备份做恢复演练并比对行数
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/restore-check.ps1

backup-linux: ## Linux 生产备份
	bash scripts/backup.sh

restore-check-linux: ## Linux 生产恢复演练
	bash scripts/restore-check.sh

health-alert-linux: ## Linux 生产健康探测与可选 webhook 告警
	bash scripts/health-alert.sh

VERSION ?= local

image: ## 构建带不可变版本标签的应用镜像
	docker build -t $(APP):$(VERSION) .

prod-up: ## 使用 .env.prod 和不可变镜像启动生产 Compose（不重新构建）
	bash scripts/prod-deploy.sh $(VERSION)

prod-rollback: ## 切换生产 Compose 到指定版本镜像（不重新构建）
	bash scripts/prod-rollback.sh $(VERSION)

infra-up: ## 启动数据库依赖，适合配合 make run
	$(COMPOSE) up -d mysql etcd minio milvus

attu: ## 启动 Milvus 和 Attu 管理界面
	$(COMPOSE) up -d etcd minio milvus attu

up: ## 构建并启动应用、MySQL、Milvus 和 Attu
	$(COMPOSE) up -d --build

down: ## 停止完整服务但保留知识库和 Session 数据
	$(COMPOSE) down

restart-container: ## 重启应用容器
	$(COMPOSE) restart app

ps: ## 查看服务状态
	$(COMPOSE) ps

logs: ## 持续查看完整服务日志
	$(COMPOSE) logs -f --tail=200

app-logs: ## 只查看应用日志
	$(COMPOSE) logs -f --tail=200 app
