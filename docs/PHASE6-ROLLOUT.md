# 阶段 6 灰度与附件运维

## 当前边界

- `intent_routing.enabled`、`dataquery.enabled`、`attachments.enabled` 均默认关闭。
- 本项目目前没有经确认的业务数据源或内部业务写入目标。只读注册表可以承载未来的固定操作定义，但当前为空；打开 `dataquery.enabled` 不会自动连接数据库或允许模型执行 SQL/URL。
- 附件功能只处理用户显式上传且属于当前 owner 的文件。它不读取任意工作区路径，也不会把解析全文写入长期记忆或项目知识库。
- 6.4–6.5（真实实时业务连接）和 6.9（内部业务写操作）在当前仓库记为 N/A。新增真实业务对象前需要先确认数据所有者、权限、字段和业务需求。

## 启用附件前

先备份数据库和 `app-data`，再用具备 DDL 权限的账号执行附件迁移：

```powershell
make backup
make db-attachments-migrate
```

生产容器的迁移命令为：

```bash
docker compose --env-file .env.prod -f docker-compose.prod.yml run --rm --no-deps --entrypoint ./session-migrate app -attachments
```

应用账号需要对新表有 DML 权限；迁移账号只用于迁移。原文件存入 `data/attachments`（生产 Compose 的 `/app/data` 命名卷），元数据和解析产物存 MySQL。现有 Windows/Linux 全量备份已包含 `app-data` 卷。

附件开关要求 `session.store=mysql`，并且 `attachments.virus_scanner` 必须指向 ClamAV `clamscan` 可执行程序。标准容器镜像包含 ClamAV、Poppler 和 FFmpeg；病毒签名需初始化并定期更新到 `attachments.virus_database_dir`：

```bash
docker compose --env-file .env.prod -f docker-compose.prod.yml run --rm --no-deps --entrypoint freshclam app --datadir=/app/data/clamav
```

没有可用的签名库时扫描会失败并拒绝上传，不会跳过扫描。管理员需要通过例行更新/监控维持签名新鲜度。启用扫描 PDF OCR 时还要确认容器能运行 `pdftoppm`。

图片/扫描 PDF 与视频关键帧使用独立 OpenAI-compatible vision provider；音频/视频转写使用独立 `audio/transcriptions` provider。通过 `.env.prod` 设置 `ATTACHMENT_VISION_*`、`ATTACHMENT_TRANSCRIPTION_*`，不要把 API key 写入配置文件；在 `config.prod.yaml` 中按需将 `attachments.vision.enabled` 或 `attachments.transcription.enabled` 置为 `true`。provider 缺失、无效或超时会把附件标为 `failed`，不会伪造解析结果。

第一批限制默认是每文件 10 MiB、每轮最多 3 个文件、PDF 最多 10 页、处理最多 2 分钟、保留 30 天；图片渲染、表格单元格、音视频时长/帧数另有固定上限。压缩包不作为上传类型接受，DOCX/XLSX 只解析受限白名单 XML 部件，不解压到任意路径。

## API 与状态

- `POST /attachments`：`multipart/form-data`，字段名 `file`（也支持 `files`）；完成病毒扫描和解析后返回附件 ID 与 `ready`/`failed` 状态。
- `GET /attachments`、`GET /attachments/{id}`：仅返回当前 owner 的附件元数据/状态。
- `GET /attachments/{id}/artifacts`：仅返回当前 owner 的来源化解析产物。
- `POST /attachments/{id}/retry`：只重试 `failed` 附件。
- `DELETE /attachments/{id}`：删除原文件、元数据和解析产物。保留期限清理每天执行一次。
- `POST /chat` JSON 可传 `attachment_ids`；WebSocket 的 `chat` 帧同样支持。只有 `ready` 且属于请求 owner 的 ID 才进入本轮上下文。越权和不存在统一返回无附件信息的 404/能力提示，不泄露其他 owner 数据。

当前内置界面在 `GET /health` 报告 `attachments_enabled=true` 时显示上传按钮，可查看解析失败并重试；禁用开关后不会删除已经保存的文件。要立即撤回数据，请调用删除接口或等待保留期限清理。

## 本地授权文件读取

当用户明确要求读取 `local_files.roots` 中的具体文件时，服务端会在模型回答前直接调用同一只读工具完成预检索；成功的正文作为不可信证据注入本轮上下文，并记录 `tool_started` 与 `file_read_completed`（`source=server_preflight`、文件名和格式，不记录正文或绝对路径）。失败会记录 `tool_failed` 并返回不猜测文件内容的确定性提示。文件读取能力不暴露给聊天模型：成功后模型只接收本轮已读取的证据，失败时由服务端确定性回复；这样可避免重复读取、模型自选路径以及普通问题触发本地文件工具。服务端只接受用户明确给出的路径/文件名，不扫描目录，授权仍由工具对规范化路径、符号链接、UTF-8、文件大小及 DOCX 正文强制检查。

项目文档知识库的预检索证据也保留独立上下文块；若模型回答没有列出来源，服务端会在答案末尾补充实际命中的文件名。该兜底只补来源标签，不补写答案事实。

## 灰度、指标与停止条件

按此顺序单独放量：

1. 仅启用意图路由，先观察 `/metrics` 的 `intent_distribution`，确认普通问题不会进入项目/业务/附件来源。
2. 完成真实 ClamAV 签名、视觉/转写 provider、有效/损坏/越权/超限样本验收后，才给小范围内部用户开放附件。
3. 每日记录解析成功率、低置信度比例、失败/重试数、存储增长、来源可追溯率和请求耗时；使用附件上传/失败计数及意图分布作服务端核对。
4. 连续灰度 3–7 天后再决定是否扩用户。当前尚无可执行的真实业务连接器或内部写操作，所以不要打开相关产品承诺。

任何越权读取、恶意文件漏检、无来源产物进入回答或内容进入长期记忆时，立即关闭 `attachments.enabled`（必要时同时关闭 `intent_routing.enabled`），保留审计记录并删除受影响附件。`make eval-intent` 可离线执行 61 个固定意图样本（含已附 ID、需补充附件、授权本地路径请求及仅提及路径/询问修改位置的普通问题），并门禁类别准确率、来源选择准确率及普通问题误触发率（默认 ≥95%、≥95%、≤1%）；它不需要启动模型或向量库。2026-09-28 本次代码变更后的 `make eval-intent` 61/61 通过（意图/来源准确率 100%，普通问题误触发率 0%）；本地 Qwen + Milvus 的完整 `make eval` 40 个用例以退出码 0 通过：24 个答案断言、引用率和拒答率均 100%，服务端路由召回 100%，技能误触发率/禁止工具违规率均为 0%，工具尝试/成功/来源门禁均 100%。生产 provider 的解析成功率、真实附件权限交叉测试和 3–7 天灰度必须在目标环境另行记录，不能由单测或本地模型评测替代。
