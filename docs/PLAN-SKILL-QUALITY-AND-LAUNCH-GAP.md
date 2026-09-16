# 技能路由质量方案 与 上线差距评估

日期：2026-09-16
承接：`docs/SKILL-ROUTING.md`（技能暴露面 debug 门禁，已完成）

> ## ⚠️ 本文第一部分已迁移，请勿以本文为准
>
> **第一部分 A（S1–S8 技能路由质量改造方案）已合并进 [`docs/REFACTOR-PLAN.md`](REFACTOR-PLAN.md) 第二部分（P-SKILL-QUALITY）**，并在那里补齐了统一实施顺序、三方文件级交集分析与坑清单。
> 需要动手实施时，**以 REFACTOR-PLAN.md 为准**；本文第一部分仅作历史记录保留（含当时的问题定位过程）。
>
> **第二部分 B（上线差距评估）仍以本文为准。** 其中：
> - B2 的 P0-1（零鉴权）与 P0-2（会话无归属）已有专门方案 → [`docs/AUTH-PLAN.md`](AUTH-PLAN.md)
> - B2 的 P0-3（限流）、P0-4（备份）、P0-5（SIGTERM）**仍无归属方案**，已登记在 [`docs/REFACTOR-PLAN.md`](REFACTOR-PLAN.md) 第十九节

范围：第一部分 A 是技能元数据与可测性改造方案；第二部分 B 是做完 A 之后距离正式上线还差什么。

---

## 0. 结论速览

**一句话结论：A 全部做完，B 的 4 个 P0 阻断项一个都不会减少。**

A 和 B 解决的是两个不同层面的问题：A 让模型**选得准**（产品可用性），B 让服务**能被别人安全地访问**（可运营性）。它们之间没有重叠。

必须说清的一点：**上一轮的 debug 门禁把技能名藏了起来，但那不是访问控制。** 未授权的人依然可以直接调用 `/chat`、读取 `/sessions`、甚至替用户批准工具调用。信息暴露面和访问控制是两件事，不要用前者当后者的替代。

| 维度 | 现状 | 完成 A 后 | 上线门槛 |
|---|---|---|---|
| 技能路由质量 | 45% | 85% | — |
| 访问控制 | 0% | 0% | 100% |
| 身份与数据隔离 | 20% | 20% | 90% |
| 限流与配额 | 30% | 30% | 80% |
| 可观测性 | 40% | 60% | 80% |
| 部署与交付 | 55% | 55% | 85% |
| 测试与评测 | 50% | 70% | 75% |
| 数据治理与备份 | 25% | 25% | 80% |

---

# 第一部分 A：技能路由质量方案

## A0 问题定位

模型每轮做技能匹配时，能看到的全部信息只有四行（`internal/skill/runtime.go:33-47`）：

```
- documents: {description 原文}
  适用场景：{scenarios}
  不适用于：{not_for}
  能力状态：{由 required_tools 与已接入工具推导}
```

其中 `description` 是**唯一的主匹配信号**——它和技能名写在同一行。而当前 13 个技能里，11 个的 `description` 仍是上游 Codex/OpenAI 的英文原文，承诺了本项目并不具备的能力（`render_docx.py`、Poppler、`$CODEX_HOME/skills`、ChatGPT add-in），并且**与同一技能块里的 `not_for` 正面矛盾**。

模型在同一条目录项里同时读到"能生成 docx"和"不支持生成 Word 文件"，路由不稳是必然结果，不是模型能力问题。

## A1 补 `required_tools`（13 个技能只有 1 个有）

**依据**：`runtime.go:40-47`。空声明时输出固定文案「未声明工具依赖，不能据此保证可执行」，对模型没有任何行动指导。声明后才会输出「缺少工具 X，只可提供说明或替代方案，**不能承诺执行**」。

**当前真实接入的工具**（`service.go:306-313`，硬编码 3 个 + 按开关注册 2 个）：

`current_time`、`write_note`、`load_skills`、`local_file_read`（`local_files.enabled`）、`knowledge_search`（`rag.enabled`）

**建议映射**（未接入的工具名如实声明，这正是该字段的语义——不是造假，而是让模型知道哪些技能是空壳）：

| 技能 | `required_tools` | 接入状态 |
|---|---|---|
| `knowledge_qa` | `knowledge_search` | ✅ 已有，无需改 |
| `documents` | `local_file_read` | ✅ 已接入 |
| `report_writer` | `local_file_read`, `write_note` | ✅ 已接入（写笔记需审批） |
| `pdf` | `pdf_render`, `pdf_extract` | ❌ 未接入 → 判缺失 |
| `spreadsheets` | `workbook_read`, `workbook_write` | ❌ 未接入 |
| `presentations` | `deck_write` | ❌ 未接入 |
| `visualize` | `artifact_render` | ❌ 未接入 |
| `template-creator` | `template_write` | ❌ 未接入 |
| `skill-creator` | `skill_write` | ❌ 未接入 |
| `skill-installer` | `skill_install` | ❌ 未接入 |
| `computer-use` | `desktop_control` | ❌ 未接入 |
| `control-in-app-browser` | `browser_control` | ❌ 未接入 |
| `excel-live-control` | `excel_session` | ❌ 未接入 |

> **需要你确认**：右列 ❌ 那 9 个工具名是我拟的，它们还没写进任何代码。**命名即契约**——一旦声明，将来真接入工具时必须用同一个名字，否则技能会永远被判"缺失"。建议现在就一次定好，或者干脆先用占位统一前缀（如 `todo_pdf_render`）表明"尚未规划"。

**验收标准**：`documents` 的能力状态行从「未声明工具依赖」变成「声明的工具已接入」；`pdf` 变成「缺少工具 pdf_render, pdf_extract，不能承诺执行」。

## A2 改写 `description`（最要命的一条）

**规则**：中文；句式 `做什么；不做什么`；不写工具名、脚本名、外部平台名；不承诺未接入能力。

| 技能 | 现状问题 | 建议 description |
|---|---|---|
| `documents` | 承诺 `render_docx.py` 生成 docx + 视觉 QA | 读取授权目录下的 UTF-8 文本与 DOCX 正文，做摘要、信息提取和内容组织；不生成、不编辑、不渲染文档文件 |
| `pdf` | 承诺 Poppler/reportlab/pdfplumber/pypdf | 讨论 PDF 的解析与生成方案，处理用户直接粘贴的文本；当前未接入 PDF 读写工具，不能读取或产出 PDF 文件 |
| `spreadsheets` | 承诺创建和校验 xlsx/Google Sheets | 分析用户提供的表格文本数据，设计表格结构与公式；当前未接入工作簿读写工具，不能打开或产出表格文件 |
| `presentations` | 承诺创建/编辑 PPT 与 Google Slides | 组织演示大纲与叙事结构，给出幻灯片内容建议；当前未接入演示文稿生成工具，不能产出幻灯片文件 |
| `excel-live-control` | 承诺通过 ChatGPT add-in 控制活动工作簿 | 说明活动 Excel 工作簿的操作方案；当前未接入 Excel 会话，不能操作已打开的工作簿 |
| `visualize` | 承诺直接产出可视化与交互工具 | 在对话中解释流程、比较方案、给出图表结构设计；不生成 HTML 页面，当前未接入可视化渲染工具 |
| `control-in-app-browser` | 承诺打开/导航/点击/输入/截图/本地测试 | 说明浏览器页面检查与交互的实现方案；当前未接入浏览器控制工具，不能访问或操作页面 |
| `computer-use` | 来自上游的同名能力声明 | 说明 Windows 桌面自动化的实现方案；当前未接入桌面控制工具，不能实际操作应用 |
| `skill-creator` | 承诺创建/更新 Codex skill | 设计技能规则草稿与元数据（description、scenarios、not_for、required_tools）；当前未接入技能目录写入工具，不能直接创建技能 |
| `skill-installer` | 承诺装入 `$CODEX_HOME/skills` | 说明技能安装流程与来源要求；当前未接入安装工具，不能安装或声明已安装技能 |
| `template-creator` | 承诺从多种来源创建模板技能 | 设计可复用的文档或表格模板内容与结构；当前未接入模板文件写入工具，不能落盘模板文件 |
| `report_writer` | 中文但未写清边界 | 基于已获得的事实撰写中文结构化报告，可写入笔记（需人工审批）；缺少事实依据时不编造内容 |
| `knowledge_qa` | 中文但未写清边界 | 检索私有知识库并给出带来源的回答；无检索依据时不推测私有事实 |

## A3 `scenarios` / `not_for` 补互斥分界

五个技能的场景短语目前彼此不互斥，模型没有可用的区分信号。把 `not_for` 改成**对比式**，直接点名该走谁：

| 技能 | 追加的 `not_for` 分界句 |
|---|---|
| `documents` | 纯 PDF 文件处理应走 pdf；表格数据分析应走 spreadsheets；演示稿应走 presentations |
| `pdf` | 用户粘贴的文本或 DOCX 正文不属于 pdf，应走 documents |
| `spreadsheets` | 活动 Excel 工作簿应走 excel-live-control；表格视觉设计应走 visualize |
| `presentations` | 文档排版应走 documents；图表本身应走 visualize |
| `template-creator` | 一次性产出内容不属于模板设计；应走 documents / spreadsheets / presentations |
| `visualize` | 技能推荐与普通问答不得生成可视化 |
| `computer-use` | 网页内的操作应走 control-in-app-browser |

## A4 清理死配置

**依据**：`loader.go:59-64` 的 routing struct 只解析 `description` / `scenarios` / `not_for` / `required_tools`。`name` 与 `metadata` **被完全忽略**，模型看到的技能名永远是目录名（`loader.go:88` 用目录名覆盖、`runtime.go:33` 输出 `s.Name`）。

现状不一致：

- `presentations/SKILL.md`: `name: Presentations`
- `spreadsheets/SKILL.md`: `name: "Spreadsheets"`
- `pdf`、`excel-live-control`: 带引号的 `name`

它们从未生效，大小写还不统一，会误导后来接手的人以为模型看到的是首字母大写。**动作**：删掉 13 个文件里的所有 `name:` 与 `metadata:` 行。

### 这两个字段的来历（说明为什么是"删除"而不是"修好"）

`skills/` 下 13 个技能里，11 个是从上游 Codex skills（`github.com/openai/skills`）整份导入的，只带了 `SKILL.md`，`scripts/`、`references/`、`assets/`、`agents/openai.yaml` 都没进来。上游格式与本地实现的对应关系：

| 上游字段 | 上游用途 | 本项目状态 |
|---|---|---|
| `name`（必填，≤64） | 技能唯一标识符（slug）。上游靠它支持 `$skill-name` 显式调用、并按 name 去重 | ❌ 无消费者。本项目没有 `$name` 调用面，显式选择走 `skills.default` 配置、`preferred` 参数和 `load_skills` 的 `names`，三者都以目录名为准 |
| `description`（必填，≤1024） | 隐式路由的**唯一**触发面（上游原话：Codex 只读这两个字段判断何时使用技能） | ✅ 生效（`runtime.go:33`） |
| `metadata.short-description` | 技能列表超上下文预算被裁剪时的短描述（上游技能列表上限约 2% 上下文 / 8000 字符，超了优先砍 description） | ❌ 无消费者。本项目目录无条件全量进提示词，没有这个预算机制 |
| `agents/openai.yaml` 的 `interface.display_name` | 面向人的展示名、图标、品牌色 | ❌ 未导入（文件不存在） |
| `scenarios` / `not_for` / `required_tools` | 上游**没有**这三个字段 | ✅ 生效（`loader.go:61-63`），是本项目自己加的 |

规律很整齐：**上游带进来的字段全是死的，本项目自己加的字段全是活的。**

顺带两条旁证：

- `name: Presentations` 连上游语义都不满足——上游的展示名一直放在 `agents/openai.yaml` 的 `interface.display_name` 里，`name` 始终是 slug 标识符。所以那个大写是双重误解。
- 被导入的正文里写着的 `render_docx.py`、"Use the helper scripts"、`.curated` 路径，指向的文件**在项目里并不存在**（导入时就没带 `scripts/`）。这也是 A2 必须改写正文承诺的原因，不只是 description。

另外，项目**自己**写的两个技能（`knowledge_qa`、`report_writer`，见 `docs/ROADMAP.md` P6-1）正是唯一没有 `name` 的两个——本地格式从一开始就是"只要 description + scenarios + not_for"。

## A5 让路由可测（唯一需要写代码的部分）

### A5-1 解耦两个开关（重要）

`execution_events.enabled`（采集与落库）与 `debug`（前端展示 + 技能名暴露 + 目录查询短路）**是两个独立开关**，此前容易混为一谈。这意味着部署态完全可以做到「内部留痕、用户面无感」：

| 配置 | `execution_events.enabled` | `debug` | 效果 |
|---|---|---|---|
| 当前 `config.docker.yaml` | `false` | `false` | 用户看不到，**你也不留痕**——上线后无法复盘 |
| 建议部署态 | `true` | `false` | 事件进 MySQL 可复盘，用户面干净 |
| 当前 `config.yaml`（本地） | `false` | `true` | 能看到下拉框，但**没有事件可选** |

**动作**：`config.docker.yaml` 的 `execution_events.enabled` 改 `true`，`debug` 保持 `false`。本地 `config.yaml` 也建议把 `execution_events.enabled` 打开，否则 `skill_loaded` 事件不发，验证路由只能靠肉眼读回答。

### A5-2 评测增加"路由"维度

现状：`internal/evaluation/evaluation.go:17-22` 的 `Case` 只有 `question` / `expected` / `must_cite` / `should_refuse`，纯看答案文本，技能选得对不对完全测不出来。评测集只有 3 题（`internal/integration/eval_cases.json`），没有统计意义。

**做法**（埋点已经现成）：

1. `Case` 增加 `ExpectSkills []string \`json:"expect_skills,omitempty"\``
2. 评测执行时用 `execution.NewRecorder(0)` 挂到 `ChatWithSink` 上
3. 跑完扫 `Events()` 里 `SkillLoaded` 的 `payload.skill_names`（`runtime.go:94-97` 已写入 `skill_names` / `requested_names`），做集合断言
4. `Report` 增加 `RoutingAccuracy float64`
5. 评测集从 3 题扩到 ≥20 题，覆盖 A3 里那几组易混场景

做完这一步，`make eval` 才能吐出"路由准确率"这个数字，而不是靠感觉。

### A5-3 放宽 `max_completion_tokens`

多技能组合会让提示词侧膨胀（提示词不受此限），但**最终结论仍被输出上限卡住**，多技能交叉分析的答案大概率被截断。

现状核对：`config.ark.yaml` 已经是 `4096`，但 `config.yaml` 和 `config.docker.yaml` 仍是 `512`。

**动作**：本地 `config.yaml` 改 `1024`（本地 9B 推理慢，不宜一次放太高），`config.docker.yaml` 改 `2048`。纯配置改动，收益直接。

### A5-4（可选，成本高）提示词加"可校验输出"

要求模型在回答前先输出选定技能名列表，把"模型遵循"变成可断言的文本。**只在校验阶段用**，生产提示词不加（会额外吃 token，且 512 上限下更紧张）。

## A6 实施顺序

| 步骤 | 内容 | 涉及文件 | 性质 |
|---|---|---|---|
| S1 | A4 清理 `name`/`metadata` | 13 个 `skills/*/SKILL.md` | 纯文本 |
| S2 | A1 补 `required_tools` | 12 个 `SKILL.md` | 纯文本 |
| S3 | A2 改写 `description` | 12 个 `SKILL.md` | 纯文本 |
| S4 | A3 补 `not_for` 分界 | 7 个 `SKILL.md` | 纯文本 |
| S5 | A5-1 打开采集 | `config.docker.yaml`、`config.yaml` | 配置 |
| S6 | A5-3 放宽 token 上限 | 3 个配置文件 | 配置 |
| S7 | A5-2 评测路由维度 | `evaluation/evaluation.go`、`integration/eval_cases.json`、`cmd/eval/main.go` | 代码 |
| S8 | A5-4 可校验输出 | `skill/runtime.go`（校验模式） | 代码，可选 |

理由：先删噪声（S1）→ 再给模型能力状态（S2）→ 再改主信号（S3）→ 再补边界（S4）。S1–S6 是一小时内能完成的量，且立刻影响路由质量；S7 是唯一有实质工作量的部分。

## A7 验收

```bash
# 元数据无语法错误、能力状态行正确渲染
go test ./internal/skill/... -run TestRuntimeHidesSkillNamesUnlessExposed -v

# 技能目录仍不泄露正文
go test ./internal/skill/... -v

# 暴露面门禁未被破坏
go test ./internal/server/... -run "TestSkillExposureIsDebugOnly|TestWebSocketReady" -v

# 全量
make check
```

人工检查：把 `debug: true` 打开，问"目前都有什么技能"，逐条核对每个技能的「能力状态」是否与 A1 表格一致；然后问一个"分析这个 Excel 并生成文件"，确认模型**不再承诺产出文件**，而是说明缺少工具。

---

# 第二部分 B：上线差距

## B1 已经具备的（不要重复做）

| 能力 | 证据 |
|---|---|
| 流式输出、错误重试、配置必填校验 | `internal/model/retry.go`、`internal/config/config.go` |
| Agent/Runner、工具调用、审批中断、多 Agent | `internal/agent/agent.go:68-102` |
| Session 持久化（file + MySQL 双后端） | `internal/session/mysql.go`、`config.yaml` |
| RAG（Milvus + bge-m3）、增量索引、检索阈值 | `internal/rag/`、`internal/chain/rag_chain.go` |
| Checkpoint 跨进程恢复 | `internal/checkpoint/` |
| 分层测试：35 个 `_test.go`，`make check` 不依赖外部服务 | `Makefile` |
| 并发治理：全局信号量 + 同 session 排队 + 上下文取消 | `service.go:99`、`service.go:235-242`、`locks` |
| 密钥走环境变量 + 日志脱敏 | `docs/SECURITY.md`、`internal/observability/redact.go` |
| 容器化：多阶段构建、非 root 用户、`HEALTHCHECK` | `Dockerfile` |
| 编排：依赖健康检查门控、`restart: unless-stopped`、数据卷 | `docker-compose.milvus.yml` |
| WebSocket 同源校验（防跨站劫持） | `ws.go:21-28`（`CheckOrigin` 比对 Host） |
| 请求体大小限制 1 MiB | `http.go:199` |
| 幂等迁移脚本 | `internal/session/migrations/001-003*.sql`（`//go:embed`） |
| 一次手工全库备份（2026-09-10） | `data/backups/mysql-compose-20260910-112150/all-databases.sql` |

## B2 P0 阻断项

### P0-1 零鉴权（唯一一条"不做就是裸奔"）

**证据**：

- `internal/server/` 全局搜索 `Authorization` / `Bearer` / `token` / `401` / `403` —— **零命中**，没有任何认证/授权代码。
- 全部路由无门禁（`http.go:41-57`）：`GET /`（Web UI）、`POST /chat`、`GET /sessions`、`GET /sessions/{id}`、`GET /sessions/{id}/execution`、`DELETE /sessions/{id}`、`POST /sessions/{id}/approval`、`GET /ws`、`GET /metrics`。
- 监听地址无主机限制：`main.go:20` 默认 `:18180`，`main.go:40` 直接 `ListenAndServe`——绑定全部网卡，同局域网可直连。

**逐条后果**：

| 未授权访问者可以做 | 影响 |
|---|---|
| `POST /chat` | 消耗你的 GPU / API 额度。本地 9B 一问可达 3 分钟，几个并发就把机器占满 |
| `GET /sessions`、`GET /sessions/{id}` | 读取**全部历史对话**（含可能粘进对话的敏感内容） |
| `DELETE /sessions/{id}` | 删除任意会话 |
| `POST /sessions/{id}/approval` | **代替用户批准工具调用**——`write_note` 获批后会真的写文件 |
| `GET /metrics` | 无实质风险，但属不必要暴露 |

**最小可行方案**（按成本排序）：

1. **一行缓解**：监听地址改 `127.0.0.1:18180`，只用本机访问。不做任何开发就能关掉局域网暴露面。
2. **反代层**：前面挂 Caddy/Nginx，用 Basic Auth 或 OIDC 终止认证 + TLS。
3. **应用层中间件**：在 `http.go` 的 mux 外层包一个 token 校验中间件（静态 bearer 起步，后续换会话/JWT）。注意 WebSocket 也要覆盖——浏览器的 `WebSocket` API 不能自定义 header，需要走 query 参数或 cookie，这点在实现时要一起设计。

> 提醒：把 `/skills` 收进 debug 门禁**没有**降低这里的任何风险。这两件事必须分开对待。

### P0-2 身份与数据隔离缺失

**证据**：

- `config.yaml` / `config.docker.yaml`：`identity_mode: "local_single_user"`、`owner_id: "local-owner"` **写死**。
- `internal/session/migrations/001_sessions.sql`：`conversations` 与 `messages` 表**没有 owner/user 字段**，只按 `session_id` 区分——会话没有归属。
- `internal/session/migrations/003_memory.sql`：记忆表（`memory_bindings`、`memory_facts`、`memory_turns`）**有 `owner_id`**，但值来自写死的配置。
- `docs/ROADMAP.md:39`：标题即写明「多人认证仍待实施」。

**后果**：接入第二个用户后，两个人共享同一份会话列表和同一份长期记忆。记忆串台会产生"模型记得我没说过的事"这类最难排查的问题。

**方案**：会话表加 `owner_id`；认证中间件把身份注入 context；记忆的 `owner_id` 从 context 取而不是从配置取；`/sessions` 按 owner 过滤。

### P0-3 无限流与无配额

**证据**：只有全局 `runtime.max_concurrency`（本地与部署配置为 `2`，`config.ark.yaml` 为 `8`，见 `service.go:99`）+ 同 session 排队（`locks`）。**没有** per-IP / per-user 速率限制，没有调用配额，没有 token 预算。

注意 `config.ark.yaml` 把并发提到 8 是配合云端模型的做法，但这同时也意味着**未授权请求能同时打满 8 路计费调用**——并发越高，缺限流的问题越贵。

**后果**：一个脚本就能持续占满那 2 个并发槽，合法用户全部排队超时。

**方案**：反代层 `limit_req`（最快）+ 应用层 per-owner 令牌桶（更准）；再往上加 token 预算与超限告警。

### P0-4 备份与恢复未形成流程

**证据**：`data/backups/` 下只有 2026-09-10 的**一次手工**全库 dump；无定时任务、无恢复演练记录、无保留策略。Milvus 三个数据卷无快照流程。迁移是幂等 `CREATE TABLE IF NOT EXISTS`，**没有版本表**，所以后续加列/加索引只能手工——`Makefile` 里专门有个 `db-memory-indexes` 目标要求用具备 `ALTER` 权限的账号执行，就是这个原因。

**方案**：`mysqldump` 定时 + 至少做一次**恢复演练**（只备份不恢复等于没备份）；Milvus 卷快照；给迁移加版本表，把"手工 ALTER"收进版本化脚本。

### P0-5 容器停机不优雅（一行修复）

**证据**：`main.go:24` 是 `signal.NotifyContext(context.Background(), os.Interrupt)`——**只捕获 SIGINT**。而 `docker stop` 默认发 **SIGTERM**。容器停止时走不到 `server.Shutdown`（`main.go:45-49`），10 秒优雅期和 `defer service.Close()` 全部失效，正在跑的请求被硬切、执行事件可能丢终态。

**修复**：`append([]os.Signal{os.Interrupt}, syscall.SIGTERM)`。Linux 下需注意平台差异，可用 `signal.Notify` 配合构建标签。

## B3 P1（上线前应做）

| 项 | 现状 | 目标 |
|---|---|---|
| 指标 | `/metrics` 只有 4 个进程内原子计数（`service.go:49-52`），无 Prometheus 格式、重启归零 | Prometheus 端点 + 关键指标持久化 |
| 日志 | 全项目仅 2 处 `log.Printf`（`service.go:142/146`），无结构化、无 `run_id` 贯穿 | `slog` JSON + run_id/session_id 贯穿 |
| 健康检查 | `/health` 返回静态 `{"status":"ok"}`（`http.go:43-45`），**不检查** MySQL / Milvus / Ollama；而 `Dockerfile` 的 `HEALTHCHECK` 正是打这个端点 | 拆 liveness / readiness，readiness 检查依赖 |
| CI | 仓库无 `.github/`，无任何流水线，`make check` 靠手工 | 至少 lint + vet + test 的 PR 检查 |
| 评测门槛 | 3 题；见 A5-2 | ≥20 题 + 路由准确率，挂进 CI 作为模型/知识库变更的回归门 |
| 发布 | 镜像 tag 固定 `my-eino-app:local`，无版本号、无 CHANGELOG、无发布流程 | 语义化 tag + 发布检查单 |
| 前端 | `app.js` 900+ 行单文件，无构建、无类型、无测试、无错误边界 | 拆分 + 最小测试（可延后） |
| 规模验证 | 无压测，无长会话/大执行记录的前端性能验证 | 一次基准压测 + 分页验证 |

## B4 P2（上线后迭代）

- 输入输出内容安全审核（9B 本地模型 + 私有知识库场景下风险较低，但对外服务仍需）
- 用户协议、隐私说明、"删除我的数据"通道
- 成本与预算（切火山方舟后的 token 预算与超限告警）
- 多租户记忆隔离精细化（用户级 / 项目级作用域切换已有表结构基础）
- 前端重构与可访问性
- 备份自动化 + 异地副本

## B5 两种上线口径

**口径 A：单人自用 / 内网演示**

完成 A 之后基本就绪。建议再补三件小事，成本都很低：

1. **P0-5**（SIGTERM，一行）
2. **A5-1**（打开 `execution_events`，配置一行）
3. **P0-1 第 1 条**（监听收 `127.0.0.1`，一行）

外加把备份做成定时任务。**但严格说这不算"上线"**——它是"给自己用"，前提是没人能访问到那个端口。

**口径 B：对外提供服务（多人）**

4 个 P0 全是硬门槛，无法通过"先上再补"绕过：

1. **P0-1 鉴权 + P0-2 身份归属**——这两条必须一起做，只做鉴权不做归属，第二个人一进来就会串数据
2. **反代 + TLS**（`docs/SECURITY.md` 里也已写明"生产部署还应限制监听地址、通过反向代理启用 TLS 和身份认证"）
3. **P0-3 限流**
4. **P0-4 备份 + 恢复演练**
5. 之后是 B3：可观测性 → CI → 评测门槛

## B6 建议的下一步（三选一）

1. **先把 A 全部做完**（S1–S7）。我可以直接动手，改完把 diff 逐条给你过；需要你先确认 A1 里那 9 个未接入工具名怎么定。
2. **先做 P0-1 最小版**：token 中间件 + 反代示例配置，把裸奔状态关掉。这条的收益比做 A 更大——A 是让产品变好，这条是让它别出事。
3. **先做两个"一行修改"**：P0-5（SIGTERM）+ A5-1（打开采集）。改动量最小，但一个是防止丢数据，一个是让你能看见系统在干什么。

## 需要你确认的三件事

1. A1 表格里 9 个未接入工具名——按我拟的定，还是有别的命名习惯？
2. `config.docker.yaml` 是否接受 `execution_events.enabled: true`（`debug` 保持 false）？
3. 上线口径是 A（自用）还是 B（对外）？这决定 B 部分哪些是"必须现在做"。
