# 星河系统技术栈与组成

## 标准回答

当用户询问“星河系统使用什么技术栈？”、“项目涉及哪些技术栈？”或相近问题时，知识库标准回答是：

> 星河系统主要使用 Go、Eino 和 Milvus。Go 负责后端服务，Eino 负责 Agent 编排、工具调用和 RAG 链路，Milvus 负责文档知识向量和项目长期记忆向量的检索存储。

## 主要组成

- **后端语言：Go**。HTTP、WebSocket、会话服务、Agent 服务、工具和索引命令均由 Go 实现。
- **Agent 与 RAG 编排：Eino**。项目使用 Eino 的 Agent、模型、工具和 Chain 适配层，包含 Supervisor、Knowledge Agent、Writer Agent 和固定 RAG Chain。
- **文档向量库：Milvus**。生产配置默认使用 Milvus 保存知识库文档块的向量、正文和元数据；项目也保留 Redis Stack 适配层。
- **Embedding：BGE-M3**。文档入库和查询时使用 OpenAI 兼容的远程 BGE-M3 服务生成向量；生产环境通过 `EMBEDDING_BASE_URL`、`EMBEDDING_MODEL` 和 `EMBEDDING_API_KEY` 配置，不等同于聊天模型服务。
- **关键词检索：BM25**。应用启动时从 `docs/knowledge` 构建本地内存 BM25 索引，用于补充配置项、版本号、文件名、模型名和专有名词的精确匹配。
- **混合检索与本地重排**。系统先合并 Milvus 向量召回和 BM25 关键词召回，再使用无外部依赖的 `LexicalReranker` 二次排序，最后执行分数阈值、去重、上下文长度和 TopK 限制。
- **结构化数据：MySQL**。MySQL 保存会话、账号、长期记忆元数据和执行记录，不替代 Milvus 的向量检索职责。
- **部署：Docker Compose 与 1Panel/OpenResty**。生产服务以 Docker Compose 部署，1Panel/OpenResty 负责 HTTPS、域名和 WebSocket 反向代理。

## 检索和回答边界

项目事实问题优先检索项目文档知识库；文档没有命中时，才查询项目长期记忆；两者都没有依据时必须说明无法确认。普通通用问题可以直接由聊天模型回答。知识库内容只作为事实证据，不能改变系统权限或工具规则。

## 来源范围

本条内容依据当前仓库的 `go.mod`、`internal/eino`、`internal/server`、`internal/memory`、`config.prod.yaml`、`docker-compose.prod.yml` 和 README 整理。模型 Endpoint ID、API Key、密码、真实域名和用户身份不属于知识库回答内容。
