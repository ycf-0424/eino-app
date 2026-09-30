# 生产上线修正执行方案

> 版本：2026-09-29  
> 目标：把当前仓库从“本机代码基本可运行”推进到“目标服务器可内部灰度”，再根据灰度结果扩大范围。  
> 当前基线：分支 codex/skill-routing-eval，工作区有 16 个未提交文件，当前本机运行的是开发 Compose。

## 0. 执行规则

- 每一步完成验收后再进入下一步。
- 不执行 docker compose down -v，避免删除 MySQL、Milvus 和 app-data。
- 真实密钥只放在部署机的 .env.prod 或 Secret 管理中，不提交 Git。
- 生产镜像使用不可变版本标签；镜像、config.prod.yaml、docker-compose.prod.yml 必须对应同一个发布版本。
- 任一步出现认证、数据隔离、备份恢复或严重数据错误，立即停止扩大用户范围并保留日志。

## 1. 修复仓库内的两个红灯

### 1.1 修复 MySQL 集成测试配置

当前失败原因：

1. HTTP 集成测试直接访问 /chat，但 config.yaml 已开启认证。
2. 测试只修改旧的 cfg.OpenAI，而当前配置使用 models 多模型目录，mock 模型没有被选中。

在两个 MySQL 集成测试创建配置后，统一补齐测试配置：

~~~go
cfg.Auth.Enabled = false
cfg.Auth.Local.Enabled = false
cfg.Models = nil
cfg.ActiveModel = ""
cfg.OpenAI.Model = "test"
cfg.OpenAI.APIKey = "test"
cfg.OpenAI.BaseURL = mock.URL + "/v1"
cfg.Agent.AutoRouting.Enabled = false
~~~

建议把这段配置抽成测试辅助函数，避免两个测试以后再次漂移。认证本身已经有独立测试，不需要在这两个 MySQL 数据持久化测试里重复覆盖。

验收命令：

~~~powershell
go test -tags mysql_integration -p 1 -count=1 ./...
go test -p 1 ./...
go vet ./...
~~~

通过标准：所有包退出码为 0，不能保留 authentication required 或 mock 模型未生效的失败。

### 1.2 修正生产迁移账号的使用方式

迁移程序会读取 MYSQL_MIGRATE_USER 和 MYSQL_MIGRATE_PASSWORD，但生产 Compose 的 app 服务没有传递这两个变量。不要把 DDL 密码长期放进运行中的 app 容器；bootstrap 时按一次性命令传递：

~~~bash
set -a
source .env.prod
set +a

docker compose --env-file .env.prod -f docker-compose.prod.yml run --rm --no-deps \
  -e MYSQL_MIGRATE_USER \
  -e MYSQL_MIGRATE_PASSWORD \
  --entrypoint ./session-migrate app
~~~

如果没有独立迁移账号，确认应用账号确实有建表、索引和 ALTER 权限后可以省略这两个变量。生产推荐使用独立迁移账号，并把上述命令写入部署手册。

长期改进可以在 Compose 中增加带 bootstrap profile 的一次性 migrate service，但不能让 DDL 凭据进入长期运行的 app 服务。

## 2. 生成并固定发布版本

### 2.1 审查工作区

~~~powershell
git status --short
git diff --stat
git diff --check
~~~

逐项确认当前 16 个修改文件都属于本次上线修正。确认后提交：

~~~powershell
git add .env.prod.example Dockerfile Makefile README.md cmd/console/main.go cmd/eval/main.go cmd/indexer/main.go cmd/retrieve/main.go cmd/server/main.go config.prod.yaml docker-compose.prod.yml docs/RUNBOOK.md internal/health/ollama_test.go internal/health/ready.go internal/server/auto_router.go internal/server/ready.go
git commit -m "chore(release): prepare production deployment"
~~~

如果集成测试修复修改了其他文件，把这些文件一并纳入同一个发布审查。

### 2.2 构建并推送不可变镜像

把占位值替换成真实镜像仓库和 Git SHA：

~~~bash
IMAGE=registry.example.com/my-eino-app:2026.09.29-<git-sha>

docker buildx build \
  --platform linux/amd64 \
  -t "$IMAGE" \
  --push .
~~~

记录镜像 digest：

~~~bash
docker buildx imagetools inspect "$IMAGE"
~~~

不要使用 latest 作为生产版本。

## 3. 准备生产配置和凭据

在部署机创建文件：

~~~bash
cp .env.prod.example .env.prod
chmod 600 .env.prod
~~~

填写以下内容：

- APP_IMAGE：刚推送的不可变镜像标签或 digest。
- MySQL 数据库、应用账号、根账号和迁移账号。
- MinIO 用户名和密码。
- 方舟 API Key、Base URL、5 个聊天模型 Endpoint ID。
- 独立 BGE-M3 Embedding 服务的 API Key、模型名和 Base URL。
- 飞书 App ID、App Secret、HTTPS 回调地址。
- 随机生成的 METRICS_TOKEN。
- 可选的备份账号、告警 Webhook。

校验是否有模板残留：

~~~bash
grep -nE 'replace_with|example.com|<git-sha>' .env.prod
~~~

输出为空后渲染 Compose：

~~~bash
docker compose --env-file .env.prod -f docker-compose.prod.yml config > /tmp/eino-prod-compose.yml
~~~

检查点：

- app 只绑定 127.0.0.1:18180。
- MySQL、Milvus、etcd、MinIO 没有宿主机端口。
- app 使用固定 APP_IMAGE，没有 build。
- Feishu 回调是 HTTPS，并且与后台登记值逐字符一致。

## 4. 目标服务器首次 bootstrap

### 4.1 停止开发栈

如果开发 Compose 与生产栈在同一台机器上，先停止开发服务：

~~~bash
docker compose -f docker-compose.milvus.yml down
~~~

不要加 -v。生产数据卷必须使用 eino-prod-* 独立卷。

### 4.2 启动基础依赖

~~~bash
docker compose --env-file .env.prod -f docker-compose.prod.yml up -d mysql etcd minio milvus
docker compose --env-file .env.prod -f docker-compose.prod.yml ps
~~~

等待 MySQL 和 Milvus 都是 healthy。

### 4.3 执行数据库迁移

使用 1.2 的一次性迁移命令。迁移完成后再继续建号。需要附件表时追加 -attachments，需要索引清理字段时追加 -memory-indexes。

### 4.4 创建第一个管理员

~~~bash
docker compose --env-file .env.prod -f docker-compose.prod.yml run --rm --no-deps \
  --entrypoint ./user-admin app \
  -create -username=admin -admin
~~~

省略 -password，让程序从标准输入读取强口令，避免口令进入 shell 历史。

### 4.5 索引知识库和长期记忆

~~~bash
docker compose --env-file .env.prod -f docker-compose.prod.yml run --rm --no-deps \
  --entrypoint ./indexer app

docker compose --env-file .env.prod -f docker-compose.prod.yml run --rm --no-deps \
  --entrypoint ./memory-reindex app -execute
~~~

如果导入开发环境历史数据，先确认管理员 UUID，再归属旧数据：

~~~bash
docker compose --env-file .env.prod -f docker-compose.prod.yml run --rm --no-deps \
  -e MYSQL_MIGRATE_USER \
  -e MYSQL_MIGRATE_PASSWORD \
  --entrypoint ./session-migrate app \
  -claim-owner=local:<管理员UUID>
~~~

不要把历史数据归属到废弃的 admin_open_id。

## 5. 启动应用并配置 HTTPS

启动 app：

~~~bash
docker compose --env-file .env.prod -f docker-compose.prod.yml up -d --no-build app
docker compose --env-file .env.prod -f docker-compose.prod.yml ps
curl -i http://127.0.0.1:18180/health
curl -i http://127.0.0.1:18180/health/ready
~~~

把 deploy/openresty/my-eino-app.conf 复制到 1Panel 网站配置中，并替换：

- server_name。
- 证书和私钥路径。
- 上游保持 127.0.0.1:18180。

保留 HTTPS、HSTS、WebSocket Upgrade、proxy_buffering off、210 秒读写超时和登录/回调限流。检查并重载：

~~~bash
nginx -t
systemctl reload openresty
~~~

飞书后台回调地址必须设置为：

~~~text
https://<真实域名>/auth/callback
~~~

## 6. 生产冒烟验收

按顺序完成以下动作，每项保存结果和时间：

1. 本地账号登录、退出、重新登录。
2. 飞书登录和回调。
3. 两个账号互相读取、删除会话，确认全部被拒绝。
4. 创建会话并完成 WebSocket 流式对话。
5. 发送知识库问题，确认有来源和 knowledge_search 事件。
6. 上传、索引、删除一份知识文档。
7. 生成一次文档并完成审批下载。
8. 使用每个生产聊天模型至少完成一次请求。
9. 使用生产 Embedding 服务完成一次索引和一次检索。
10. 无 Token 请求 /metrics 返回 401，正确 Token 返回 200。
11. 验证队列满时返回 503，模型超时返回 504。

/health/ready 只证明数据库、Milvus 和本地依赖探测结果，不能替代远程方舟和 Embedding 的真实请求验收。

## 7. 备份、恢复和告警

在目标机执行：

~~~bash
chmod +x scripts/*.sh
make backup-linux
make restore-check-linux
make health-alert-linux
~~~

验收要求：

- 备份包含 MySQL、Milvus、etcd、MinIO 和 app-data。
- manifest SHA256 校验通过。
- 恢复后表行数和非空业务数据检查通过。
- 备份复制到另一台机器或对象存储。
- 定时任务已经生效，建议每 6 小时一次。
- 健康、模型、工具、磁盘和备份失败都有通知。
- Docker 日志有大小和保留上限。

## 8. 内部灰度

先开放给 3–10 名内部用户，连续 3–7 天。每天记录：

- 请求数、非超时 5xx、超时和 P95 延迟。
- 文档命中、记忆命中和无命中比例。
- 工具开始、成功和失败事件。
- 认证失败、跨用户访问拒绝和附件权限结果。
- 备份成功、恢复检查和磁盘使用率。
- 模型和 Embedding 错误。

达到以下条件后才能扩大范围：

- 认证和跨用户隔离 100% 通过。
- 关键路由召回率至少 95%。
- 需要工具的请求都有真实工具事件。
- 非超时 5xx 小于 1%。
- 备份成功率 100%。
- 最近一次恢复检查不超过 7 天。
- 严重安全问题为 0。

任一严重安全、数据隔离、备份恢复或模型持续不可用问题出现，立即停止扩大范围，保留当前镜像和日志。

## 9. 回滚

保留上一版镜像和对应的 config.prod.yaml。出现严重问题时：

~~~bash
make prod-rollback VERSION=<previous-version>
docker compose --env-file .env.prod -f docker-compose.prod.yml ps
curl -i http://127.0.0.1:18180/health/ready
~~~

回滚后重新走登录、对话、知识检索和 WebSocket 冒烟。数据库迁移不可逆变更必须提前准备兼容方案，不能只依赖镜像回滚。

## 10. 最终完成条件

- [ ] 集成测试、单元测试和 vet 全绿。
- [ ] 生产镜像已推送并记录 digest。
- [ ] .env.prod 无模板值，密钥未进 Git。
- [ ] 数据库迁移、管理员建号、知识库索引、记忆重建完成。
- [ ] 历史数据已正确归属，或明确不迁移。
- [ ] 生产 Compose 冷启动通过。
- [ ] HTTPS、WebSocket、飞书回调通过。
- [ ] 两个生产聊天模型和 Embedding 服务真实调用通过。
- [ ] 备份、恢复、定时任务和告警通过。
- [ ] 3–7 天内部灰度记录完整。
- [ ] 发布配置、镜像和回滚版本已归档。

完成最后一项后，才能把系统状态写成“正式上线”。
