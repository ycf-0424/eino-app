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

.PHONY: help fmt test vet check run build index retrieve eval \
	image infra-up up attu down restart ps logs app-logs

help: ## 显示可用命令
	@echo "make db-migrate  - 在现有 MySQL 项目库中初始化会话表"
	@echo "make db-memory-indexes - 使用具备 ALTER 权限的账号补充记忆清理索引"
	@echo "make sessions-import - 将本地 JSON 会话导入 MySQL"
	@echo "make check       - 格式化、测试并执行静态检查"
	@echo "make run         - 在宿主机启动 Go 服务"
	@echo "make infra-up    - 启动 MySQL/Milvus/etcd/MinIO"
	@echo "make attu         - 启动 Milvus 和 Attu 管理界面"
	@echo "make up          - 构建并启动完整 Docker 服务"
	@echo "make down        - 停止容器，保留数据卷"
	@echo "make index       - 使用宿主机 Go 将文档写入 Milvus"
	@echo "make eval        - 执行固定 RAG 评测"
	@echo "make logs        - 查看全部容器日志"

fmt: ## 格式化 Go 代码
	go fmt ./...

test: ## 运行不依赖外部服务的测试；串行编译避免本机内存不足
	go test -p 1 ./...

vet: ## 执行 Go 静态检查
	go vet ./...

check: fmt test vet ## 提交前完整检查

run: ## 本地启动后端和内置前端
	go run ./cmd/server -addr :18180

build: ## 构建当前操作系统可执行文件
	go build -trimpath -o $(SERVER) ./cmd/server

index: ## 索引 docs/knowledge 文档，需要 Ollama 和 Milvus 已运行
	go run ./cmd/indexer

retrieve: ## 使用示例问题查看原始检索分数
	go run ./cmd/retrieve "星河系统使用什么技术栈？"

eval: ## 执行真实 Ollama + Milvus 固定评测
	go run ./cmd/eval

image: ## 只构建应用镜像
	docker build -t $(APP):local .

infra-up: ## 启动数据库依赖，适合配合 make run
	$(COMPOSE) up -d mysql etcd minio milvus

attu: ## 启动 Milvus 和 Attu 管理界面
	$(COMPOSE) up -d etcd minio milvus attu

up: ## 构建并启动应用、MySQL、Milvus 和 Attu
	$(COMPOSE) up -d --build

down: ## 停止完整服务但保留知识库和 Session 数据
	$(COMPOSE) down

restart: ## 重启应用容器
	$(COMPOSE) restart app

ps: ## 查看服务状态
	$(COMPOSE) ps

logs: ## 持续查看完整服务日志
	$(COMPOSE) logs -f --tail=200

app-logs: ## 只查看应用日志
	$(COMPOSE) logs -f --tail=200 app

