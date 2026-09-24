# 统一执行方案（自包含 · 可线性执行）

> 本文是项目生产与后续产品化的**唯一执行来源**：
> `AUTH-PLAN.md`（飞书登录 + 多用户隔离）、`REFACTOR-PLAN.md`（P-REFACTOR + P-SKILL-QUALITY）、`PLAN-SKILL-QUALITY-AND-LAUNCH-GAP.md`（上线差距评估）。
>
> **合并原则**
> 1. 上线主线步骤重排为单一线性编号（0.1 → 5.10，共 38 步）；上线后的业务数据与多模态能力统一归入阶段 6，不再有「P-REFACTOR 阶段 / S1–S8 / AUTH 阶段 1–5」三套体系并存；
> 2. 源文档里 11 条待决策事项及生产新增决策都给出推荐默认值，评估确认后作为执行约束；
> 3. 每一步都给出「改什么 / 具体动作 / 验收 / 回滚」，无跨文档跳转；
> 4. 源文档保留作证据与设计动因的历史记录，**执行一律以本文为准**；生产部署与灰度要求也在阶段 5 中统一维护。
>
> **行号基准**：本文所有行号对应基线提交 **`92b2833`**（已于 2026-09-16 建立并推送）。超过该提交之后的行号会漂移，核对时以函数名/语句为准。

---

## 〇、执行前必读

### 全局约定

| 约定 | 内容 |
|---|---|
| 执行单元 | 代码/配置步骤原则上**一个步骤 = 一个 commit**；存在硬依赖时按“关键交集”表合并。历史步骤已有实际提交，5.10b 也可能只形成灰度记录，因此不再给出容易失真的总提交数；剩余工作以对应阶段子步骤表中的提交边界为准 |
| 验收时机 | 每步验收全部通过才进入下一步 |
| **推送节奏** | **阶段级推送 + `3.1` 单独推**，共 6 次（见 `RUNBOOK.md` 第二节）。commit 是给「回退」用的，push 是给「离开这台机器」用的，粒度不必一致。推送由**用户在自己的终端执行**——自动化环境拿不到凭据管理器登录态 |
| 回滚方式 | 每个子步骤开始前先确认 `git status --short` 干净；提交后用 `git revert <commit>` 回退该步骤。工作区非空时禁止执行 `git reset --hard`、`git checkout --` 或 `git clean`，必须先把已验证改动提交或另存补丁 |
| 测试命令 | `go test -p 1 ./...`。**`-p 1` 不能省略**——本机内存不足，并行编译会失败（`Makefile:38` 已有注释） |
| 收尾检查 | 每个阶段结束跑 `make check`。**注意**：步骤 3.2 之前定义是 `fmt test vet`，之后并入 `boundary` 变成 `fmt boundary test vet` |
| 工作目录 | `E:/11/my-eino-app` |
| **执行手册** | 逐 commit 的命令、commit message、推送点、回滚速查见 **`docs/RUNBOOK.md`**。本文负责「改什么」，手册负责「怎么走」，内容不重复；本文是唯一方案入口 |

### 七个阶段总览

| 阶段 | 内容 | 步骤数 | 性质 | 阻塞上线 |
|---|---|---|---|---|
| **0** | 建立基线 | 1（0.1） | 版本控制 | 前置 → ✅ 已完成 |
| **1** | 技能路由质量 | 7（1.1–1.7） | 纯文本 + 配置 | 否 |
| **2** | 鉴权、多用户隔离与项目自有账号体系 | 15（2.1–2.15） | 代码 + 数据库 | **是** |
| **3** | eino 框架收敛 | 3（3.1–3.3） | 结构与 import | 否 |
| **4** | 评测与可观测性收口 | 2（4.1–4.2） | 代码 | 否 |
| **5** | 上线收口、生产部署与内部灰度 | 10（5.1–5.10） | 代码 + 运维 + 业务验收 | **是** |
| **6** | 可选业务数据连接、多模态预处理与五类意图路由 | 10（6.1–6.10） | 上线后的产品能力增量 | **否；按实际需求选择实施** |

**上线主线合计 38 个基础步骤**。其中 5.5a、5.6–5.10 拆为 18 个可执行子步骤；阶段 6 另有 10 个产品化步骤，不改变阶段 5 的上线判定。提交数量以各阶段子步骤表和实际历史为准。步骤编号连续、无跳号，从头执行到尾即可。

**顺序的三条依据**

| 约束 | 理由 |
|---|---|
| 阶段 1 排在阶段 2、3 之前 | 只碰 `skills/*.md` 与三份配置的不同段落，与另两个阶段零文件交集，成本最低而收益立即 |
| 阶段 2 排在阶段 3 之前 | 阶段 2 是上线阻塞项；且阶段 3 会平移 37 个文件、重写 29 行 import，先做会让阶段 2 的行号基准全部失效 |
| 阶段 4 排在阶段 3 之后 | 步骤 4.1 改的 `internal/evaluation/evaluation.go` 与 `cmd/eval/main.go` 正在阶段 3 的 import 改写清单里，先做会被二次改写 |

### 已确认的现状事实（不要重做）

以下是**已经完成或已经具备**的能力，执行时不要重复实现，也不要回退：

| 事实 | 说明 |
|---|---|
| 技能暴露面已收进 debug 门禁 | `GET /skills` 仅在 `debug: true` 时注册（`http.go:48-50`）；前端下位框与技能事件按 debug 显隐（`app.js:917/918/518`）；非 debug 时提示词禁止提及技能名（`runtime.go:107-119` 的 `exposureRule`） |
| WS 同源校验 | `ws.go` 的 `CheckOrigin` 比对 Host。这是 CSRF 防护，**不是认证**，阶段 2 要另做认证 |
| 请求体大小限制 | 1 MiB |
| 容器加固 | Dockerfile 非 root（`USER eino`）、HEALTHCHECK 打 `/health`；compose 依赖健康门控 + `restart` |
| `SkillPreloaded` 事件 | `internal/execution/event.go` 已定义并列入 `knownTypes`；`internal/server/service.go` 已有发送路径。阶段 5.7c 复用它补生产态服务端预加载；知识预检索复用标准工具事件，不新增自定义事件 |
| 迁移机制 | 幂等 `CREATE TABLE IF NOT EXISTS` + `//go:embed`，但**没有版本表**，且不幂等处理 `ALTER TABLE`（见坑 B2） |
| 已有组件 | `github.com/google/uuid`（生成 state / session id）、`github.com/joho/godotenv`（读 .env）。**`go.mod` 无 `golang.org/x/oauth2`**——飞书接口是标准 HTTP，手写 3 个调用，不引依赖 |
| 测试规模 | 35 个测试文件 |

### 当前关键配置值（改造前）

| 配置 | `config.yaml`（本地） | `config.ark.yaml`（方舟联调） | `config.docker.yaml`（部署） |
|---|---|---|---|
| `debug` | `true` | `true` | `false` |
| `execution_events.enabled` | `false` | `false` | `false` |
| `openai.max_completion_tokens` | `512` | `4096` | `512` |
| `runtime.max_concurrency` | `2` | `8` | `2` |
| `memory.identity_mode` | `local_single_user` | `local_single_user` | `local_single_user` |

⚠️ **`execution_events.enabled`（采集）与 `debug`（展示 + 技能名暴露 + 目录查询短路）是两个独立开关**，改造前被混为一谈过。阶段 1 只动前者。

---

## 一、预置决策表

源文档里的 11 条待决策事项，加上本方案新增的生产决策（D12–D22），统一放在此处。下列内容是**推荐默认值**，先供你评估可实施性；确认后再作为执行约束，避免后续出现两套口径。

| # | 决策 | 已定值 | 理由 |
|---|---|---|---|
| D1 | 9 个未接入工具名怎么定 | 按本方案步骤 1.2 表格**原样声明，不加前缀** | 命名即契约。加 `todo_` 前缀会导致将来真正接入时名字对不上，而技能永远判「缺失」 |
| D2 | `config.docker.yaml` 是否开 `execution_events` | **开**（`debug` 保持 `false`） | 部署态内部留痕、用户面无感；不开则上线后无法复盘 |
| D3 | 评测集扩到多少题 | **≥20 题** | 3 题算不出有意义的准确率 |
| D4 | S8（提示词可校验输出）是否做 | **不做** | 额外消耗 token，且价值有限。若将来要做，位置在步骤 3.3 之后 |
| D5 | 是否加「中文友好展示名」配置 | **不加** | 现由 `app.js:928` 的 `replaceAll("_", " ")` 承担，够用 |
| D6 | 是否给所有技能补 `required_tools` | **是，但如实声明** | 空声明与「声明了但缺失」语义完全不同，见坑 A3 |
| D7 | `internal/tool` 整体移入还是拆分 | **整体移入** `internal/eino/tool` | 9 个文件的导出类型就是 `einotool.InvokableTool`，拆开只会制造碎片 |
| D8 | `internal/rag` 整体移入还是拆分 | **整体移入** `internal/eino/rag` | eino touchpoint 分散在 `factory.go` / `redis.go` / `milvus.go`，拆开会让「一半在外、一半在内」 |
| D9 | `internal/observability` 是否拆出 `Redact` | **不拆，整体移入** | 拆出需移 2 个文件 + 改 5 处调用点。当前收敛目标已达成，收益不足以抵消改动面 |
| D10 | `internal/skill/runtime.go` 提示词是否改 | **只做阶段 3.3 的工具构造下沉，不动提示词文案** | 提示词是阶段 1 的验收对象，改它会让阶段 1 的人工断言失效 |
| D11 | 登录态存储用进程内 map 还是 MySQL | **进程内 map**；`auth_sessions` 表仍建好备用 | 内网单副本部署，实现量小；重启后重新登录在企业内部场景可接受。多副本部署时切换（见「明确不做」） |
| D12 | 项目自有账号的注册入口开放到什么程度 | **仅管理员创建**（`register: admin_only`） | 内部场景下本地账号是**例外通道**而非主通道（主通道是飞书）。使用者少且可预期 → 管理员建号成本极低，准入权留在管理员手里。开放注册会把准入权交给网络层（访客网络、外包、离职人员、内网任何一台被拿下的机器都能建号），而项目本已有飞书作为身份源，能做到「只让在职同事进」 |
| D13 | 是否保留 `/auth/local` 单口令后门 | **不保留**，升级为本地账号登录 | 本地账号体系落地后，管理员自己就是一个带 `is_admin` 标记的 local 账号，后门的「能进去」不再独有。保留它等于多一个「口令即管理员」的永久入口，而它唯一能救的场景（DB 挂 + 进程重启后仍要登录）下，会话 / 记忆 / 执行记录已全部不可用，系统本就不具备可用性 |
| D14 | 本地账号与飞书账号是否绑定合并 | **不绑定，两套身份完全独立** | 用户选定。`owner` 加命名空间前缀后天然隔离，零额外代码；绑定需要邮箱 / 手机比对与验证流程，复杂度与当前需求不匹配，另立项 |
| D15 | 管理员如何创建本地账号 | **命令行工具 `cmd/user-admin`**，不做管理端 API | ① 项目已有 8 个 `cmd/*` 工具，风格一致；② **零 HTTP 暴露面**——建号能力只属于「能登上服务器的人」；③ 天然解决「第一个管理员从哪来」的引导问题（管理端 API 方案反而需要额外的 bootstrap 机制） |
| D16 | 第一阶段承诺哪些文件能力 | **报告生成 + 文本/DOCX 读取 + 知识库检索 + 审批后写笔记** | `pdf`、`spreadsheets`、`presentations` 当前没有真实文件工具，不能把技能说明当成已完成能力 |
| D17 | 技能路由策略 | **混合路由；知识库请求由服务端先强制 `knowledge_search`，报告/文档走已接入技能，其他任务保留模型按需选技能** | 模型自主路由只作补充，关键场景由服务端确定；保留渐进加载，不增加全局启用/禁用开关。技能加载与工具执行分开验收；实际工具注册和权限才是执行能力的依据 |
| D18 | 第一阶段上线形态 | **内部 3–10 人灰度，单副本；通过 HTTPS 反向代理从内网或 VPN 访问** | 先验证真实需求、路由、备份和运维；多副本另立项 |
| D19 | 生产网络暴露面 | **只暴露反向代理入口；MySQL、Milvus、Ollama、Attu 走内部网络** | 当前 Compose 发布了多个管理和数据库端口，开发配置不能直接用于生产 |
| D20 | 数据保护目标 | **RPO ≤ 6 小时，RTO ≤ 2 小时；备份包含 MySQL、Milvus 和 `app-data`，并放在不同存储位置** | 只备 MySQL 或只看脚本退出码不足以证明可恢复 |
| D21 | 生产运行环境 | **明确选择 Windows/Docker Desktop 或 Linux/Docker，并在同一环境验证备份和定时任务** | 当前脚本和任务计划偏向 Windows，跨 OS 搬运会使运维流程失效 |
| D22 | `knowledge_qa` 的定位 | **保留为知识问答能力的说明层，实际检索由服务端强制 `knowledge_search` 完成** | 避免模型跳过检索，同时不重复建设第二套知识库工具 |
| D23 | 知识库无命中时的回答 | **先查项目长期记忆；文档和长期记忆都无命中时放行通用模型，并明确标注不是项目内部资料结论** | 保留证据边界，同时避免知识库为空时普通问题完全无法回答 |
| D24 | 第一阶段传输协议 | **HTTPS；反向代理终止 TLS，应用 Cookie `Secure=true`** | 灰度也可能包含敏感业务内容，避免后续从 HTTP 切 HTTPS 造成 Cookie 和代理配置二次变更 |
| D25 | 真实 IP 限流位置 | **代理层按真实 IP 主限流；应用只保留用户名维度和代理失效时的 RemoteAddr 兜底** | 代理后的 `RemoteAddr` 是代理地址，不能把应用 IP 桶当作正常用户限流；当前 Go 应用刻意不信任客户端 XFF |
| D26 | 五类意图的判定方式 | **服务端确定性规则优先；低置信度时使用结构化分类器；分类结果必须是固定枚举** | 不能让模型自由输出路由文字，也不能让普通问题误触发知识库、记忆库或业务查询 |
| D27 | 结构化数据查询边界 | **只允许服务端注册的只读查询和 API 操作；模型不能生成任意 SQL、URL 或命令** | 防止越权、注入、SSRF、全表扫描和把业务系统当成通用工具 |
| D28 | 实时数据来源 | **只有项目确实存在需要查询的内部系统时，才接入业务只读数据库或企业 API；与会话 MySQL 分离，并具备 owner 过滤、超时、字段白名单和审计记录** | 当前项目没有电商或订单系统时跳过本项，不为“完整”而新增无业务价值的连接器 |
| D29 | 实时数据时效 | **启用动态数据查询时，结果必须带 `as_of` 和 `fresh_until`；超过 TTL 直接标记过期，不用长期记忆替代当前值** | 长期记忆适合项目背景，不适合任何会变化的内部业务字段 |
| D30 | 多模态第一批范围 | **先做图片与扫描 PDF 的 OCR/版面解析；Excel、音频、视频分别立项，不在第一批伪装已支持** | 先交付可验证的文件理解链路，避免一次引入过多解析器和模型依赖 |
| D31 | 多模态存储边界 | **原文件保存到受控对象/`app-data`，解析产物保存文件元数据和文本块，向量只索引可检索文本；不把原图或原音频写入长期记忆** | 原文件、解析证据、项目记忆和文档向量分别治理，便于删除、权限和审计 |
| D32 | 文件输入方式 | **只处理用户明确上传或明确指定的 `attachment_id`；不扫描整个工作区，不猜测文件范围** | 延续现有 `local_file_read` 的最小权限原则 |
| D33 | 多模态模型接入 | **视觉/OCR/语音模型通过独立 provider 配置接入，缺失时降级到文本解析或明确提示能力不可用** | 多模态模型不可用时不能静默编造图像、表格或音视频内容 |
| D34 | 执行操作边界 | **只有项目确实需要操作内部系统时，才增加 allowlist 工具；写操作必须审批、幂等键和审计事件；查询和执行严格分开** | 查询项目状态和修改项目状态不是同一种能力，不能共用一个自由参数工具 |
| D35 | 阶段 6 上线方式 | **按意图和数据源逐项灰度，默认关闭新增实时查询和多模态入口；每项达到门槛后再扩大范围** | 现有文档→项目记忆→通用模型链路保持稳定，可随时关闭新增能力 |

**与源文档的两处主动偏离**（已核实，非笔误）：

| 项 | 源文档 | 本方案 | 原因 |
|---|---|---|---|
| `internal/observability` | REFACTOR-PLAN 第七节决策 3「建议拆」 | **不拆**（D9） | 该建议的成本（移 2 文件 + 5 处调用点改名）与阶段 3 的其余三项无协同，且拆完边界收益不明显 |
| session id 归属 | AUTH-PLAN G 节「首选服务端强制生成，**需确认前端是否依赖**」 | **确认：前端依赖**（`app.js:452` 的 `makeSessionId()`）。因此**采纳服务端生成**，并配套改前端 | 原文档把这一步标成待确认。核实结果是前端确实自己生成 id，所以服务端强制生成必须配套前端改造，工作量已计入步骤 2.7 |

---

## 二、阶段 0：建立基线

### 步骤 0.1　git init + 基线提交　✅ **已完成（2026-09-16）**

**为什么必须做**：项目当时**没有 git 仓库**。后续阶段 1 会一次改 13 个 `SKILL.md`，阶段 3 会平移 37 个文件、改写 29 行 import。没有基线提交，这些操作全部不可回退。

> **执行结果（实测，2026-09-16）**
>
> | 项 | 值 |
> |---|---|
> | 基线提交 | **`92b2833`** — `baseline before skill-quality / auth / eino-convergence` |
> | 分支 / 远程 | `master` → `origin` = `https://gitee.com/yangchengfeng/eino-app.git`，**已推送**（`ahead/behind = 0 0`） |
> | 提交内容 | **152 个文件 / 23,402 行**；`.workbuddy/`、`.idea/` 已排除 |
> | `config.yaml` | ✅ 已跟踪 |
> | 工作区 | 干净 |
>
> **基线测试标尺（阶段 3 的「行为零变化」以此为准）**：
> `go build ./...` exit 0；`go vet ./...` exit 0；`go test -p 1 ./...` **exit 0，17 个测试包全部 `ok`、0 失败**（`agent` / `chain` / `checkpoint` / `config` / `evaluation` / `execution` / `health` / `memory` / `model` / `observability` / `output` / `prompt` / `rag` / `server` / `session` / `skill` / `tool`；另有 8 个 `cmd/*` 为 `[no test files]`）。
>
> **基线本身无失败用例**，因此后续任何阶段出现失败，都可直接归因于该阶段的改动，无需再区分「环境缺失」。下面的动作记录保留作为背景，**不需要重跑**。

**动作**

```bash
cd E:/11/my-eino-app
git init
```

先核对 `.gitignore`（根目录已有该文件）是否覆盖以下运行时产物。缺哪条补哪条：

```
data/                     # 含会话 JSON、备份 dump
.env
eino-server               # 构建产物
*.checkpoint
*.approval.json
```

**核对结果（已实测）**：现有 `.gitignore` 已覆盖上述全部条目，另有 `*.exe`、`*.log`、`build/`、`docs/knowledge/.index-manifest.json`，**不需要新建**。

⚠️ **`config.yaml` 是唯一例外，必须单独处理。** `.gitignore` 只写死了 `config.yaml` 一个文件名（`config.ark.yaml`、`config.docker.yaml` 不受影响），而**步骤 1.5（开 `execution_events.enabled`）与 1.6（`max_completion_tokens` 512→1024）都要改它**。若它保持被忽略，这两处改动在基线之后**没有回退点**。

已核实它当前不含真凭据：`openai.api_key` 与 `rag.embedding.api_key` 均为占位 `"ollama"`，`rag.redis.password` 与 `rag.milvus.password` 均为空串；MySQL 凭据来自 `.env`（已被忽略）。且 `config.go:196-197` 对这 4 个密钥字段**都支持 `${ENV_NAME}` 展开**（变量未设置时启动直接报错，不会静默发送空密钥）——所以将来切换到方舟密钥的正确做法是写 `${ARK_API_KEY}` 或维护 `config.ark.yaml`，而不是把密钥写进 `config.yaml`。

**动作**：删除 `.gitignore` 中的 `config.yaml` 一行，让它随基线一并纳入版本控制。

> 替代做法（若你坚持它保持本地私有）：保留忽略规则，但在基线提交后执行 `git add -f config.yaml`。git 的规则是 **`.gitignore` 只对未跟踪文件生效**，文件一旦被跟踪，后续改动就不再受该规则约束，步骤 1.5/1.6 因此同样有回退点。代价是 `.gitignore` 里那条会永久失效，容易让后来人困惑，故不作为首选。

然后提交并记录基线：

```bash
git add -A
git commit -m "baseline before skill-quality / auth / eino-convergence"
go build ./... && go vet ./... && go test -p 1 ./...
```

**验收（已全部通过）**
- [x] `git log --oneline` 有一条提交 → `92b2833`
- [x] `git status` 干净
- [x] `git ls-files config.yaml` **有输出**（步骤 1.5/1.6 改它时有回退点）
- [x] 基线测试通过数已记录：**17 个包全绿 / 0 失败**（见上方执行结果表）

**回滚**：无（这是起点）。

---

## 三、阶段 1：技能路由质量

**目标**：让模型在 13 个技能里选得准，并让「选得准不准」可以被测量。

**问题定位**：模型每轮匹配时能看到的全部信息只有 4 行（`runtime.go:33-47`）：

```
- {目录名}: {description}
  适用场景：{scenarios}
  不适用于：{not_for}
  能力状态：{由 required_tools 与已接入工具推导}
```

其中 **`description` 是唯一的主匹配信号**（与技能名同一行）。13 个技能里 11 个的 `description` 仍是上游 Codex 英文原文，承诺本项目不具备的能力，并**与同一技能块里的 `not_for` 正面矛盾**——模型在同一条目录项里同时读到「能生成 docx」和「不支持生成 Word 文件」。这是路由不稳的根本原因。

> **步骤 1.2 / 1.3 / 1.4 必须同批完成，不要只改一半**（坑 A1）。

### 步骤 1.1　删除死配置

**改什么**：13 个 `skills/*/SKILL.md`，共删除 15 行（`name:` 11 行 + `metadata:` 与其 `short-description:` 共 4 行）。

**依据**：`loader.go:59-64` 的 routing struct 只解析 `description` / `scenarios` / `not_for` / `required_tools`，**没有 `name`**；yaml.v3 默认忽略未知键，全项目两处 `yaml.Unmarshal`（`config.go:380`、`loader.go:71`）都没开 `KnownFields`，所以 `name` 被静默丢弃。`Skill.Name` 的唯一写入点是 `loader.go:88`，值来自目录名。

**当前不一致的写法**（正好说明写的人以为它能控制展示）：

| 文件 | 现状 |
|---|---|
| `presentations/SKILL.md` | `name: Presentations` |
| `spreadsheets/SKILL.md` | `name: "Spreadsheets"` |
| `pdf`、`excel-live-control` | 带引号的 `name` |

**动作**：删掉这些行。不要「修好它」——让 `name` 生效等于给技能身份开第二个来源，还要同步改 registry key（`runtime.go:83-86`）、`load_skills` 参数校验、`validName` 正则（`loader.go:46`）与路径穿越防护，收益为负。

**验收**
- [ ] `grep -rn "^name:\|^metadata:" skills/` 无输出
- [ ] `go test ./internal/skill/...` 全绿

**回滚**：`git checkout -- skills/`

### 步骤 1.2　补 `required_tools`（12 个技能）

**依据**：`runtime.go:40-47`。空声明时输出固定文案「未声明工具依赖，不能据此保证可执行」——对模型没有任何行动指导。声明之后才会输出真正有用的一句：「缺少工具 X，只可提供说明或替代方案，**不能承诺执行**」。

**动作**：按下表在 frontmatter 增加 `required_tools`。

| 技能 | `required_tools` | 接入状态 |
|---|---|---|
| `knowledge_qa` | `knowledge_search` | ✅ 已有，**无需改** |
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

**项目真实注册的工具只有 5 个**（`service.go:306-313` 硬编码 3 个 + 按开关注册 2 个）：`current_time`、`write_note`、`load_skills`、`local_file_read`、`knowledge_search`。匹配是**精确字符串匹配**（`routing.go` 的 `missingTools`），差一个字就判缺失。

⚠️ 上表「❌ 未接入」那 9 个名字尚未写进任何代码。一旦声明，将来真正接入时**必须用同一个名字**。

**验收**
- [ ] `documents` 的能力状态行从「未声明工具依赖」变为「声明的工具已接入」
- [ ] `pdf` 变为「缺少工具 pdf_render, pdf_extract，不能承诺执行」

**回滚**：`git checkout -- skills/`

### 步骤 1.3　改写 `description`（12 条）

**规则**：中文；句式 `做什么；不做什么`；不写工具名、脚本名、外部平台名；不承诺未接入能力。

**动作**：按下表逐条替换。

| 技能 | 现状问题 | 改写后 `description` |
|---|---|---|
| `documents` | 承诺 `render_docx.py` 生成 docx + 视觉 QA | 读取授权目录下的 UTF-8 文本与 DOCX 正文，做摘要、信息提取和内容组织；不生成、不编辑、不渲染文档文件 |
| `pdf` | 承诺 Poppler / reportlab / pdfplumber / pypdf | 讨论 PDF 的解析与生成方案，处理用户直接粘贴的文本；当前未接入 PDF 读写工具，不能读取或产出 PDF 文件 |
| `spreadsheets` | 承诺创建和校验 xlsx / Google Sheets | 分析用户提供的表格文本数据，设计表格结构与公式；当前未接入工作簿读写工具，不能打开或产出表格文件 |
| `presentations` | 承诺创建/编辑 PPT 与 Google Slides | 组织演示大纲与叙事结构，给出幻灯片内容建议；当前未接入演示文稿生成工具，不能产出幻灯片文件 |
| `excel-live-control` | 承诺通过 ChatGPT add-in 控制活动工作簿 | 说明活动 Excel 工作簿的操作方案；当前未接入 Excel 会话，不能操作已打开的工作簿 |
| `visualize` | 承诺直接产出可视化与交互工具 | 在对话中解释流程、比较方案、给出图表结构设计；不生成 HTML 页面，当前未接入可视化渲染工具 |
| `control-in-app-browser` | 承诺打开/导航/点击/输入/截图/本地测试 | 说明浏览器页面检查与交互的实现方案；当前未接入浏览器控制工具，不能访问或操作页面 |
| `computer-use` | 来自上游的同名能力声明 | 说明 Windows 桌面自动化的实现方案；当前未接入桌面控制工具，不能实际操作应用 |
| `skill-creator` | 承诺创建/更新 Codex skill | 设计技能规则草稿与元数据（description、scenarios、not_for、required_tools）；当前未接入技能目录写入工具，不能直接创建技能 |
| `skill-installer` | 承诺装入 `$CODEX_HOME/skills` | 说明技能安装流程与来源要求；当前未接入安装工具，不能安装或声明已安装技能 |
| `template-creator` | 承诺从多种来源创建模板技能 | 设计可复用的文档或表格模板内容与结构；当前未接入模板文件写入工具，不能落盘模板文件 |
| `report_writer` | 中文但未写清边界 | 基于已获得的事实撰写中文结构化报告，可写入笔记（需人工审批）；缺少事实依据时不编造内容 |
| `knowledge_qa` | **已基本达标，可不改** | 检索私有知识库并给出带来源的回答；无检索依据时不推测私有事实 |

⚠️ **正文里的承诺同样落空**：导入时只带了 `SKILL.md`，`scripts/` / `references/` / `assets/` / `agents/openai.yaml` 全未导入。所以正文里写的 `render_docx.py`、「Use the helper scripts」、`.curated` 路径指向的文件在项目里**并不存在**。正文只在 `load_skills` 时被读到，危害小一级，本步骤不改，但要知道这个事实。

**验收**
- [ ] 13 条 `description` 全为中文，且与同技能块的 `not_for` 不再矛盾
- [ ] 问「分析这个 Excel 并生成文件」→ 模型**不承诺产出文件**，而是说明缺少工具

**回滚**：`git checkout -- skills/`

### 步骤 1.4　补 `not_for` 互斥分界（7 个技能）

**依据**：`documents` / `pdf` / `spreadsheets` / `presentations` / `template-creator` 的场景短语彼此不互斥，模型没有可用的区分信号。把 `not_for` 改成**对比式**，直接点名该走谁。

**动作**：追加以下句子。

| 技能 | 追加的 `not_for` |
|---|---|
| `documents` | 纯 PDF 文件处理应走 pdf；表格数据分析应走 spreadsheets；演示稿应走 presentations |
| `pdf` | 用户粘贴的文本或 DOCX 正文不属于 pdf，应走 documents |
| `spreadsheets` | 活动 Excel 工作簿应走 excel-live-control；表格视觉设计应走 visualize |
| `presentations` | 文档排版应走 documents；图表本身应走 visualize |
| `template-creator` | 一次性产出内容不属于模板设计；应走 documents / spreadsheets / presentations |
| `visualize` | 技能推荐与普通问答不得生成可视化 |
| `computer-use` | 网页内的操作应走 control-in-app-browser |

**提交**：步骤 1.2 + 1.3 + 1.4 合成一个 commit：

```bash
git commit -m "feat(skills): 补能力边界、改写路由主信号、补互斥分界"
```

### 步骤 1.5　打开执行事件采集

**依据**：三份配置的 `execution_events.enabled` 全是 `false`，`service.go` 的 execution 分支直接 `return ctx, nil` → **`skill_loaded` / `skill_preloaded` 事件根本不发**，前端 `app.js:495/544/558` 的渲染分支收不到事件（白写）。

⚠️ **只动 `execution_events`，不要动 `debug`**（坑 A4）。

**动作**

| 文件 | `execution_events.enabled` | `debug` |
|---|---|---|
| `config.yaml`（本地） | `false` → **`true`** | 保持 `true` |
| `config.ark.yaml`（方舟） | `false` → **`true`** | 保持 `true` |
| `config.docker.yaml`（部署） | `false` → **`true`** | 保持 `false` |

**验收**
- [ ] `GET /sessions/{id}/execution` 能返回本轮事件
- [ ] `debug: true` 时界面执行面板出现「加载技能」条目
- [ ] `debug: false` 重启后 `GET /skills` 404、界面无技能入口，但 `GET /sessions/{id}/execution` **仍有事件**（验证两个开关确实解耦）

**回滚**：`git checkout -- config*.yaml`

### 步骤 1.6　放宽输出上限

**依据**：多技能组合会让提示词侧膨胀（提示词不受此限），但**最终结论仍被输出上限卡住**，多技能交叉分析的答案会被截断。

**动作**

| 文件 | 现值 | 改为 | 理由 |
|---|---|---|---|
| `config.yaml:9` | `512` | **`1024`** | 本地 9B 推理慢（复杂问题约 3 分钟），并发只有 2，不宜一次放太高（坑 A6） |
| `config.docker.yaml:8` | `512` | **`2048`** | 容器内同 ark 视角，放宽更有价值 |
| `config.ark.yaml:26` | `4096` | **保持** | 已足够 |

### 步骤 1.7　阶段验收

```bash
grep -rn "^name:\|^metadata:" skills/          # 应无输出
go test ./internal/skill/...                    # 元数据解析、目录不泄露正文
go test ./internal/server/... -run "TestSkillExposureIsDebugOnly|TestWebSocketReady"
make check
```

人工断言（`debug: true` 下）：
- [ ] 问「目前都有什么技能」→ 逐条核对每个技能的能力状态与步骤 1.2 表格一致
- [ ] 问「我要总结 Word 工单，推荐什么技能」→ 只说 documents，不牵扯 pdf / spreadsheets
- [ ] 问「分析这个 Excel 并生成文件」→ 不承诺产出文件

**回滚**：`git reset --hard HEAD~1`（本阶段共 3 个 commit：1.1 / 1.2-1.4 / 1.5-1.6）

---

## 四、阶段 2：鉴权、多用户隔离与项目自有账号体系

**目标**：把「配置文件里的单用户」改为「登录态里的当前用户」，使多人可安全共用同一套服务。

**两条登录路径，共用一套隔离机制**：

| 路径 | 步骤 | 身份来源 | `owner` 取值 |
|---|---|---|---|
| 飞书 OAuth | 2.1–2.9 | 飞书 `open_id` | `feishu:<open_id>` |
| 项目自有账号 | 2.10–2.14 | 本地用户名 | `local:<uuid>` |

> **为什么加第二条几乎不动隔离层**：`owner` 在本文中自始至终是**一个不透明字符串** —— `middleware` 把它塞进 `ctx`，下游 session / memory / checkpoint / execution 只按字符串过滤，不关心它是谁签发的。因此新增一种身份来源 = 新增一个入口 + 一张表，**步骤 2.5–2.8 的隔离改造一行都不用重做**。这是当初把 owner 抽成字符串、而非绑死 `open_id` 的直接收益。
>
> **代价同样明确**：两套身份**完全独立**（D14），同一个人用两种方式登录会被视为两个用户，会话与记忆互不可见。

**当前缺口**（已核实）

| 层 | 事实 |
|---|---|
| 认证 | `internal/server/` 全局搜 `Authorization` / `Bearer` / `token` / `401` **零命中**；`Handler()`（`http.go:38-73`）注册 11 条路由 + `memory.go` 4 条，**无任何认证中间件** |
| 会话 | `001_sessions.sql` 的 `conversations(id, updated_at, created_at)` **无 user 字段**；`List()` 无参数、`load()`/`delete()` 只按 id |
| 记忆 | ✅ 地基已有——`003_memory.sql` 全表带 `owner_id`，`CheckBinding()` 已有归属校验，但 owner 来自**静态配置** `r.Config.OwnerID` |
| Checkpoint | `store.go:44` 按 session id 存文件；`LoadApproval(id)` 不校验归属；**session id 允许客户端传入**（`http.go:85`、`ws.go:116/159`）→ 越权根源 |
| Execution | `002_execution.sql` 无 user 字段，但通过 `conversation_id` FK 关联 → 只要会话隔离做对即可 |

**最要命的一条**：`POST /sessions/{id}/approval` 目前任何人都能**代替你批准工具调用**，而 `write_note` 获批后真的会写文件。

### 步骤 2.1　配置层

**新增 `internal/config/auth.go`**

```go
type Auth struct {
    Enabled      bool      `yaml:"enabled"`
    Provider     string    `yaml:"provider"`       // 仅支持 "feishu"
    AppID        string    `yaml:"app_id"`
    AppSecret    string    `yaml:"app_secret"`
    RedirectURL  string    `yaml:"redirect_url"`   // http://192.168.x.x:18180/auth/callback
    SessionTTL   Duration  `yaml:"session_ttl"`    // 建议 12h
    CookieSecure bool      `yaml:"cookie_secure"`  // 生产 HTTPS 必须 true；仅开发 HTTP 才为 false
    Local        LocalAuth `yaml:"local"`          // 项目自有账号，字段与校验见步骤 2.10
}

// LocalAuth 是项目自有账号（本地账号）的配置 —— D12 / D13。
type LocalAuth struct {
    Enabled           bool `yaml:"enabled"`
    MinPasswordLength int  `yaml:"min_password_length"` // 缺省 8；上限固定 128
}
```

**`internal/config/config.go`**

1. `Config` struct 加 `Auth Auth \`yaml:"auth"\``
2. `Validate()` 里仿照 `config.go:171-174` 的 map 写法读取环境变量：

```go
for key, dest := range map[string]*string{
    "FEISHU_APP_ID":       &c.Auth.AppID,
    "FEISHU_APP_SECRET":   &c.Auth.AppSecret,
    "FEISHU_REDIRECT_URL": &c.Auth.RedirectURL,
} { if value, ok := os.LookupEnv(key); ok { *dest = value } }
```

3. 新增 `ValidateAuth()`：
   - `Enabled` 时**至少启用一种登录方式**（飞书三字段齐全 **或** `auth.local.enabled: true`），否则启动失败（清晰失败优于静默）
     ⚠️ 本步骤初版写的是「三字段必填」，那会在「只用自有账号、不接飞书」时把启动直接堵死 —— **准确逻辑见步骤 2.10 的修正，两处必须一致**
   - `Provider` 只允许 `feishu`
   - `auth.local.enabled: true` 时 `MinPasswordLength` 缺省取 8
   - ⚠️ **`Enabled` 时强制 `Session.Store == "mysql"`**（坑 B5：file 模式没有 owner 维度）

**`.env.example` 追加**

```
# 飞书自建应用凭据（开发者后台 → 凭证与基础信息）
FEISHU_APP_ID=
FEISHU_APP_SECRET=
FEISHU_REDIRECT_URL=http://localhost:18180/auth/callback
```

**三份配置均加 `auth:` 段，`enabled: false`** —— 保持默认关，这样阶段 2 做到一半也不会破坏现有单用户流程；部署态在步骤 5.5b（本方案最后一步）置 `true`。三份都要加（不能只加 `config.yaml`），否则步骤 5.5b 无处可改。`auth.local` 子段在步骤 2.10 补入。

**验收**：`go build ./...` 通过；`auth.enabled: true` 但缺 `AppID` 时启动失败且报错清晰。

### 步骤 2.2　数据库迁移

**新增 `internal/session/migrations/004_auth.sql`**

```sql
CREATE TABLE IF NOT EXISTS auth_users (
  open_id     VARCHAR(64)  PRIMARY KEY,
  union_id    VARCHAR(64)  NOT NULL DEFAULT '',
  name        VARCHAR(128) NOT NULL DEFAULT '',
  avatar_url  VARCHAR(512) NOT NULL DEFAULT '',
  created_at  DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  last_seen_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS auth_sessions (
  token_hash  CHAR(64)     PRIMARY KEY,
  owner       VARCHAR(64)  NOT NULL,
  created_at  DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  expires_at  DATETIME(6)  NOT NULL,
  INDEX idx_auth_sessions_expiry(expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

**⚠️ 坑 B2（必读）**：`session/mysql.go:113-138` 的迁移循环用 `migrationTableRE`（`mysql.go:167`）判断是否跳过，而该正则**只匹配 `CREATE TABLE IF NOT EXISTS`**。所以 `ALTER TABLE ADD COLUMN` **每次跑都会重新执行** → 第二次报 `Duplicate column name 'owner_id'`。

**解法**：`ALTER` 语句**不写进 `.sql`**，改在 Go 侧做幂等。仿照 `mysql.go:140-163` 处理索引的写法，新增 `IncludeAuth` 选项：

```go
if options.IncludeAuth {
    for _, col := range []struct{ table, name, ddl string }{
        {"conversations", "owner_id",
         "ALTER TABLE conversations ADD COLUMN owner_id VARCHAR(64) NOT NULL DEFAULT ''"},
        {"conversations", "idx_conversations_owner",
         "ALTER TABLE conversations ADD INDEX idx_conversations_owner(owner_id, updated_at)"},
    } {
        var exists int
        // 列查 information_schema.columns，索引查 information_schema.statistics
        if exists == 0 { db.Exec(col.ddl) }
    }
}
```

**历史数据归属**：已有的 `conversations` 行 `owner_id` 会是空串。迁移后执行一次：

```bash
go run ./cmd/session-migrate -claim-owner=feishu:<open_id>
```

（已落成 `internal/session/mysql.go` 的 `ClaimLegacySessions`。**不要手写 UPDATE** —— 绕过代码路径，
且原先示例里的 `admin_open_id` 字段已废弃删除，见步骤 2.2 的说明。）

**验收**：`make db-migrate` 连跑**两次**都成功（第二次不报 `Duplicate column name`）；`conversations` 多一列 `owner_id` 与一个索引。

### 步骤 2.3　认证模块（全新 `internal/auth/`）

**`feishu.go` —— 三个调用**

| 步骤 | 请求 |
|---|---|
| 拼授权页 | `GET https://accounts.feishu.cn/open-apis/authen/v1/authorize?client_id={AppID}&redirect_uri={RedirectURL}&response_type=code&scope=contact:user.base:readonly&state={state}` |
| 换 token | `POST https://accounts.feishu.cn/oauth/v3/token`，body `{grant_type, client_id, client_secret, code, redirect_uri}` |
| 取用户信息 | `GET https://open.feishu.cn/open-apis/authen/v1/user_info`，header `Authorization: Bearer {user_access_token}` |

> **端点核对结果（2026-09 对照官方文档）**：三个端点全部为当前有效版本——授权页基本 URL 即 `accounts.feishu.cn/open-apis/authen/v1/authorize`（官方「获取授权码」文档）；token 端点官方已在 v2 文档顶部公告弃用、迁移至 `accounts.feishu.cn/oauth/v3/token`（英文站已明确标注 deprecated）；`authen/v1/user_info` 仍是当前版本。

three 处细节，方案初版漏写，缺一个就会失败：

| # | 细节 | 不做的后果 |
|---|---|---|
| ① | 授权页必须带 **`scope`** 查询参数（本方案用 `contact:user.base:readonly`），且该权限需先在后台「权限管理」开通 | 后台未开通时报 **20027**；不带 scope 则用户授权范围不含基本信息 |
| ② | 授权页里的 **`redirect_uri` 必须 URL 编码**（`https%3A%2F%2F...`），不是原样拼接 | 回调报 `redirect_uri` 不匹配（20071） |
| ③ | **不要引入 PKCE**。官方「获取授权码」文档写明：使用 PKCE（`code_challenge`）时**只能搭配 v2 token 端点**，v3 尚未支持 | 一旦加了 `code_challenge` 再用 v3 换 token 即失败；反之「不传 code_challenge 却传 code_verifier」v3 也会按 PKCE 语义拒绝 |

⚠️ **坑 B4**：网上大量教程还在用 `open.feishu.cn/open-apis/authen/v2/oauth/token`，该端点**已被官方弃用**；授权页域名也已换成 `accounts.feishu.cn`。照老教程写会直接失败。
⚠️ **授权码 5 分钟有效且只能用一次** —— `/auth/callback` 拿到 code 必须**立即**兑换，不能先做其他 I/O。
⚠️ **`token_type` 与 `expires_in` 不要硬编码** —— v3 响应里 `user_access_token` 有效期由 `expires_in` 返回（示例 7200 秒，非固定值）。本方案只在登录时用它换一次用户信息，**不做 refresh**（D11 的登录态是自签 Cookie，与 `user_access_token` 解耦），因此不需要 `offline_access`。

**`session.go` —— 登录态存储**

按 D11 用**进程内 map**：`map[tokenHash]Session` + 定时清理。零 DB 依赖，重启即失效（安全上更保守）。`auth_sessions` 表仍建好，将来切不用再迁移。

```go
type Session struct {
    Owner     string    // "feishu:<open_id>" 或 "local:<uuid>"
    Name      string
    AvatarURL string
    Provider  string    // "feishu" | "local"，供前端显示与 /auth/me 返回
    ExpiresAt time.Time
}
```

⚠️ **owner 命名空间（一次定死，后续所有步骤依赖它）**：`owner` 一律取 `<provider>:<subject>` 形式 —— 飞书写 `feishu:<open_id>`，本地账号写 `local:<uuid>`（步骤 2.12）。理由：`owner` 是不透明字符串、下游只做等值比较，加前缀**零成本**；而不加前缀时数据库里会长期混着两种格式，排查归属时无法一眼判断身份来源，且将来引入第三种身份必乱。代价只有两处：步骤 2.2 的 `UPDATE` 语句、本步骤的 owner 赋值。

**`middleware.go`**

```go
func (m *Middleware) Authenticate(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        token := tokenFromCookie(r)      // Cookie 名如 eino_session
        sess, ok := m.sessions.Get(token)
        if !ok {
            if wantsHTML(r) { http.Redirect(w, r, "/auth/login", http.StatusFound); return }
            writeJSON(w, 401, ...)
            return
        }
        next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ownerKey{}, sess.Owner)))
    })
}
```

**`identity.go`**

```go
type ownerKey struct{}

// OwnerFromContext 返回当前登录用户；未认证时返回空串。
func OwnerFromContext(ctx context.Context) string {
    if v, ok := ctx.Value(ownerKey{}).(string); ok { return v }
    return ""
}
```

**State 校验（CSRF）**：`/auth/login` 生成随机 state 写进短时 Cookie（`HttpOnly` + `SameSite=Lax`），`/auth/callback` 比对后立即清除。**不校验 state 等于开放登录 CSRF。**

**管理员入口改由本地账号承担**（D13：不保留单口令后门）

「飞书不可用时系统仍需可进入」这条需求，由步骤 **2.10–2.14 的本地账号体系**直接满足：管理员本身就是一个带 `is_admin` 标记的 local 账号，走常规的用户名 + 密码登录，与普通本地账号**共用同一套代码**。

因此本步骤**不再实现** `ADMIN_LOCAL_PASSWORD_HASH` 单口令后门。理由：那会引入第二条登录代码路径 + 第二个环境变量 + 一个「口令即管理员」的永久暴露面，而它唯一能救的场景（DB 挂且进程重启后仍要登录）下，会话 / 记忆 / 执行记录已全部不可用，系统本就不具备可用性。完整论证见 **D13**。

### 步骤 2.4　路由与中间件接入

**新增 5 条路由**（第 5 条条件注册）

| 路由 | 作用 | 注册条件 |
|---|---|---|
| `GET /auth/login` | 302 到飞书授权页 | 始终 |
| `GET /auth/callback` | code 换 token → 建登录态 → `Set-Cookie` → 302 到 `/` | 始终 |
| `GET /auth/logout` | 清除登录态与 Cookie | 始终 |
| `GET /auth/me` | 返回当前用户（供前端显示） | 始终 |
| `POST /auth/local` | 本地账号登录（步骤 2.12） | 仅 `auth.local.enabled: true` |

> **没有 `/auth/register`** —— 注册方式为「仅管理员创建」（D12），建号走命令行工具（D15，步骤 2.11），**不经 HTTP**。这是准入控制的关键：建号能力只属于能登上服务器的人。

**免认证白名单**：`/auth/login`、`/auth/callback`、`/auth/local`（**登录入口自身必须在白名单内**，否则未登录时根本访问不到）、`/health`、`/health/ready`（步骤 5.4 会新增该路由，此处一并放行）。
**需认证**：`/`、`/metrics`、`/skills`、`/chat`、`/sessions*`（含步骤 2.7 新增的 `POST /sessions`）、`/ws`、`/memory*`。

⚠️ **坑：`http.go:59-72` 的超时包装是最外层 `http.HandlerFunc`，且 `http.go:61` 对 `/ws` 跳过超时。** 如果把认证中间件包在最外层，`/ws` 会丢掉超时豁免。**采纳方案：把认证中间件放在 mux 内部，逐路由包装**（或把 `/ws` 也纳入认证，但在中间件里对 `/ws` 跳过超时）。

⚠️ **`/ws` 的认证只能靠 Cookie** —— 浏览器 WebSocket API 无法自定义 Header。好在登录态本来就是 Cookie。同时**保留 `ws.go:21-28` 的 `CheckOrigin`**：Cookie 认证 + Origin 校验两者都要。

**验收**：未登录访问 `/` → 302 到 `/auth/login`；未登录调 `POST /chat` → 401 JSON（不是重定向）；完整登录流程走通，`Set-Cookie` 生效；state 不匹配的回调被拒绝；同一个 code 重复使用失败。

### 步骤 2.5　Session 隔离

**方法签名加 owner**（`internal/session/session.go` + `mysql.go`）

| 方法 | 位置 | 改动 |
|---|---|---|
| `List()` | `session.go:166` / `mysql.go:300` | → `List(owner string)`；SQL 加 `WHERE c.owner_id=?` |
| `Load(id)` | `session.go:122` / `mysql.go:183` | → `Load(owner, id)`；SQL 加 `AND owner_id=?`，且**无结果要区分「不存在」与「非我所有」** |
| `Save(id, msgs)` | `session.go:144` / `mysql.go:211` | → `Save(owner, id, msgs)`，见下方 ⚠️ |
| `Delete(id)` | `session.go:190` / `mysql.go:319` | → `Delete(owner, id)`；SQL 加 `AND owner_id=?` |
| `CleanupOlderThanBatch` | `session.go:212` / `mysql.go:332` | **保持不变**（全局运维清理，跨 owner） |

⚠️ **写路径必须改**（`mysql.go:222-224`）：

```go
tx.ExecContext(ctx, "INSERT IGNORE INTO conversations(id) VALUES (?)", id)
```

`INSERT IGNORE` 遇到已存在的 id 会静默跳过 —— 如果 A 的 session id 被 B 提交，**B 能继续往 A 的会话里追加消息**。改成：

```go
tx.ExecContext(ctx, "INSERT INTO conversations(id, owner_id) VALUES (?, ?) ON DUPLICATE KEY UPDATE owner_id = IF(owner_id = VALUES(owner_id), owner_id, NULL)", id, owner)
// 或 SELECT owner_id FROM conversations WHERE id=? FOR UPDATE 后显式比对，不匹配返回 errors.New("session belongs to another user")
```

**9 个调用点同步改**

| 文件:行 | 调用 |
|---|---|
| `server/http.go:126` | `s.sessions.List()` |
| `server/http.go:135` | `s.sessions.Load(r.PathValue("id"))` |
| `server/http.go:177` | `s.sessions.Delete(id)` |
| `server/service.go:325` | `s.sessions.Load(id)`（`newAgent` 内） |
| `server/service.go:334` | `s.sessions.Save(id, messages)`（persistence 闭包）⚠️ |
| `server/service.go:339` | `s.sessions.Save(id, chat.History())` |
| `server/skill_catalog.go:42` | `s.sessions.Load(id)` |
| `server/skill_catalog.go:56` | `s.sessions.Save(id, history)` |

⚠️ `service.go:334` 的 `SetPersistence` 闭包注册在 `newAgent` 里，**`owner` 必须从 `ctx` 取出后捕获进闭包**，不能在里面再读一次 context（那时可能已被取消）。

**`service.go` 公开签名不变**：`ChatWithSink(ctx, id, query, skillName, writer, sink)` 里 owner 从 `ctx` 取（`auth.OwnerFromContext(ctx)`）。这样 CLI（`cmd/console`、`cmd/eval`）**不受影响**，继续以单用户身份运行。

### 步骤 2.6　Memory 隔离（工作量最大）

**核心事实：非测试代码里 `r.Config.OwnerID` 共 31 处**（`memory/mysql.go` 15、`memory/worker.go` 15、`memory/reindex.go` 1）。

**方案：`Repository.For(ownerID)` —— 只加一个方法，31 处自动生效**

`Repository.Config` 是**值类型**（`config.Memory`），可安全派生副本：

```go
// For 返回绑定到指定 owner 的 Repository 副本。
// Config 是值类型，副本之间互不影响，可安全并发使用。
func (r *Repository) For(ownerID string) *Repository {
    cfg := r.Config          // 值拷贝
    cfg.OwnerID = ownerID
    return &Repository{
        DB:         r.DB,
        Config:     cfg,
        Generation: r.Generation,
        Model:      r.Model,
    }
}
```

因为两处 15 处**都是读 `Config.OwnerID`**，`For()` 之后它们**全部自动拿到正确的 owner，一行都不用改**。

**调用侧改造**

| 位置 | 改动 |
|---|---|
| `service.go:299` | `s.memories.Repo.CheckBinding(ctx, id)` → `s.memories.Repo.For(owner).CheckBinding(ctx, id)` |
| `service.go:332` | `chat.SetMemoryContext(s.memories.Context)` — 确认 `Context` 内部 owner 读取路径，同样需要 `For(owner)` |
| `service.go:120` | `memory.Open(...)` 返回的 Engine 保持"未绑定 owner"的原始 Repo，仅用于派生 |

**⚠️ 坑 B1（最致命）：worker claim job 按单一 owner 过滤**

`memory/mysql.go:168` 的 `SELECT ... FROM memory_jobs WHERE owner_id=? AND ...` **写死了当前 owner**。多用户上线后：

> B 用户对话产生的抽取 job（`owner_id = B`）**永远不会被 worker 捞出**（worker 只查 `owner_id = A`）→ **B 的记忆功能静默失效，不报任何错。**

`mysql.go:163` 的 lease 超时回收同理。

**解法**：
1. `mysql.go:163/168` 的 SQL **去掉 `owner_id=?`**（保留或一并去掉 `project_id`）
2. `ProcessOne` 拿到 job 后从 `job.owner_id` 取 owner，用 `Repo.For(job.owner_id)` 处理
3. job 表已有 `owner_id` 字段（`003_memory.sql:42`），**无需改表**

**⚠️ 同样的问题在 `Maintain()`（`worker.go:257-540`）**：快照统计、过期回收、保留期清理全按 `Config.OwnerID` 过滤 → 多用户下**只清理默认用户的数据，其他人的数据无限增长**。

**解法**：maintenance 是运维性质的全局操作，**直接去掉 owner 过滤**（`worker.go:257/262/267` 快照统计、`worker.go:301` 过期 fact 回收、`worker.go:335-540` 保留期清理）。

**放开 `internal/config/memory.go:59-61`**

```go
// 现状
if m.IdentityMode != "local_single_user" {
    return fmt.Errorf("memory.identity_mode must be local_single_user; multi-user authentication is not implemented")
}
```

改为接受 `multi_user`，并**新增**该模式下的校验：
- `Auth.Enabled` 必须为 true（否则没有 owner 来源）
- `Session.Store` 必须为 mysql（`memory.go:56-58` 已覆盖）
- `OwnerID` 在 multi 模式下**不再使用**，启动时打一条日志说明它被忽略

**`memory_bindings` 与 `conversations.owner_id` 都保留**，语义不同：
- `memory_bindings(session_id, owner_id, project_id)` — 记忆引擎内部绑定，含 project 维度
- `conversations.owner_id` — 会话归属的**权威来源**

`CheckBinding`（`mysql.go:39-52`）会校验 `memory_bindings` 与当前 owner 一致。B 用 A 的 session id 访问时，这里先报 `memory session scope mismatch` —— **这是已存在的第二道防线，改造后自动生效。**

### 步骤 2.7　Checkpoint 隔离

**根因：session id 可由客户端指定**（`http.go:85` 非空时直接用、`ws.go:116` 从 query 取、`ws.go:159-161` 帧内还能覆盖）→ 知道别人的 session id 就能读他的审批内容、恢复执行。

**已核实**：前端**确实依赖自己生成 id** —— `app.js:452` 的 `state.sessionId = makeSessionId()`，并由 `app.js:471` 拼进 WS URL。所以 AUTH-PLAN G 节标为「待确认」的那一项，结论是**依赖**。

**动作（两层）**

1. **服务端强制签发 session id**
   - 新增 `POST /sessions`：服务端 `uuid.NewString()` 并写入 `conversations(id, owner_id)`，返回 `{id}`
   - 新会话不接受客户端自造 id；续聊只能携带一个已登记且属于当前 owner 的 id
   - `http.go` / `ws.go`：空 id 由服务端生成，未知 id 拒绝，他人 id 返回 403；帧内 id 一律忽略
   - 前端 `app.js:450` 的 `startNewChat()` 改为**异步**，先调 `POST /sessions` 拿 id 再继续；`app.js:452` 的 `makeSessionId()` 移除
2. **checkpoint 路径加 owner 分层（兜底）**
   - `checkpoint/store.go:40-44`：`path(id)` → `path(owner, id)`，返回 `dir/<owner>/<id>.checkpoint`
   - 所有方法签名加 owner

**7 个调用点**

| 文件:行 | 调用 |
|---|---|
| `server/service.go:418` | `s.checkpoints.LoadApproval(id)` |
| `server/service.go:444` | `s.checkpoints.SaveApproval(id, ...)` |
| `server/service.go:486` | `s.checkpoints.LoadApproval(id)` |
| `server/service.go:503` | `s.checkpoints.SaveApproval(id, ...)` |
| `server/service.go:517` | `s.checkpoints.ClearApproval(id)` |
| `server/http.go:181` | `s.checkpoints.Delete(ctx, id)` |
| `server/http.go:182` | `s.checkpoints.ClearApproval(id)` |

`cmd/console/main.go:151/297/313/317` 是 CLI 单用户路径，**保持不动**（传固定 owner）。

**验收**：B 拿 A 的 session id 调 `GET /sessions/{id}` → 403/404，不是 200；B 调 `DELETE /sessions/{id}` 删 A 的会话 → 403；B 调 `POST /sessions/{id}/approval` 审批 A 的中断 → 403。

### 步骤 2.8　Execution 隔离

**无需改表**：`002_execution.sql` 的 `execution_runs.conversation_id` 已 FK 关联 `conversations`，步骤 2.5 把 `conversations.owner_id` 做对即可。

| 位置 | 改动 |
|---|---|
| `handleExecution`（`http.go:145`） | 查 execution 前先 `s.sessions.Load(owner, id)` 校验归属，不匹配返回 403 |
| `handleDeleteSession`（`http.go:175`） | `s.sessions.Delete(owner, id)` 已含校验，后续 `DeleteExecutions` 自然安全 |

**决定：不给 `execution_runs` 加 `owner_id` 冗余列。** 当前规模下依赖 `conversation_id` 的 FK 做归属校验已足够；冗余列会引入「两处 owner 可能不一致」的新问题。

### 步骤 2.9　前端

`internal/server/web/` 只有 4 个文件（`index.html`、`app.js`、`app.css`、`favicon.svg`），手写无构建。

| 改动 | 说明 |
|---|---|
| 登录跳转 | **服务端 302 即可，前端零改动** |
| Cookie 携带 | 同源请求浏览器自动带 Cookie，`app.js` 的 fetch 无需改动 |
| 401 处理 | `app.js` 收到 401 跳转登录页（防会话过期后前端卡住） |
| 登出按钮 + 用户名 | `index.html` 加元素，调 `/auth/me` 填充、`/auth/logout` 登出 |
| **`startNewChat()` 改异步** | 见步骤 2.7：改为先 `POST /sessions` 拿服务端 id |

⚠️ **不要回退技能暴露面门禁**：`app.js:917/918` 的下拉框显隐、`app.js:518` 的技能事件过滤、`app.js:667` 的 `skill` 字段按 debug 发送，全部保留。

### 步骤 2.10　自有账号：配置层与数据层

**定位**（D12）：内部场景下自有账号是**例外通道**，不是主通道 —— 主通道是飞书（员工已有飞书账号，一键登录）。它服务两类人：没有飞书账号的（外包、合作方、临时人员），以及飞书故障时的兜底。使用者少且可预期 → 管理员建号成本极低。

**配置**（三份配置均加）

```yaml
auth:
  enabled: false          # 总开关，步骤 5.5 才置 true
  provider: feishu
  app_id: ""
  # ... 现有字段不动
  local:
    enabled: true         # 与 auth.enabled 独立
    min_password_length: 8
```

**⚠️ 对步骤 2.1 校验逻辑的修正（必读）**

步骤 2.1 原写「`auth.enabled: true` 时 `AppID` / `AppSecret` / `RedirectURL` 必填」。这在「只想用自有账号、不接飞书」的场景下会把启动直接堵死。改为：

```go
if !c.Auth.Enabled {
    return nil
}
feishuReady := c.Auth.AppID != "" && c.Auth.AppSecret != "" && c.Auth.RedirectURL != ""
if !feishuReady && !c.Auth.Local.Enabled {
    return fmt.Errorf("auth.enabled 为 true 时必须至少启用一种登录方式：飞书凭据（app_id/app_secret/redirect_url）或 auth.local.enabled")
}
```

即**三种组合都合法**：只用飞书、只用自有账号、两者并存。**但至少要有一个** —— 否则没人能登进来，等于系统不可达。

> 这条修正让「项目自有账号」可以**独立于飞书存在**，而不只是飞书的补充。若将来想彻底不依赖飞书，把飞书三个字段留空、只留 `auth.local.enabled: true` 即可。

**新增 `internal/session/migrations/005_local_users.sql`**

```sql
CREATE TABLE IF NOT EXISTS auth_local_users (
  id            CHAR(36)     PRIMARY KEY,
  username      VARCHAR(32)  NOT NULL,
  password_hash VARCHAR(255) NOT NULL,
  display_name  VARCHAR(64)  NOT NULL DEFAULT '',
  is_admin      TINYINT(1)   NOT NULL DEFAULT 0,
  disabled      TINYINT(1)   NOT NULL DEFAULT 0,
  created_at    DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  last_seen_at  DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  UNIQUE KEY uniq_auth_local_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

| 设计点 | 理由 |
|---|---|
| `id` 用 uuid、**不自增** | 避免通过 id 暴露用户数量与建号顺序 |
| `username` 唯一索引 | 兼作建号时的重名校验 |
| 语句形如 `CREATE TABLE IF NOT EXISTS` | 被 `migrationTableRE` 命中 → **迁移天然幂等**，不踩坑 B2 |
| **无邮箱 / 手机号字段** | 不做自助找回（D12），不收集用不上的个人信息 |

`mysql.go` 的 `MigrationOptions` 加 `IncludeLocalUsers`，与 `IncludeAuth` 同样处理。

**验收**：`make db-migrate` 连跑两次都成功；`auth_local_users` 存在且 `username` 有唯一索引。

**回滚**：`git checkout -- internal/config/ internal/session/` + `DROP TABLE auth_local_users`

### 步骤 2.11　自有账号：密码哈希 + 建号工具

**`internal/auth/password.go`**

⚠️ **绝对不要用 sha256。** 自有账号的口令由**用户自选**，强度不可控，必须慢哈希。

已实测：**Go 1.26.3 标准库自带 `crypto/pbkdf2`**（Go 1.24 起进入标准库；`go doc crypto/pbkdf2` 可验证签名 `func Key[Hash hash.Hash](h func() Hash, password string, salt []byte, iter, keyLength int) ([]byte, error)`）。因此**零新增依赖**，不需要引入 `golang.org/x/crypto/bcrypt`。

```go
const (
    pbkdf2Iterations = 210000   // OWASP 对 PBKDF2-HMAC-SHA256 的建议量级
    pbkdf2KeyLength  = 32
    pbkdf2SaltLength = 16
)

// 存储格式（自描述，将来提高迭代次数可平滑升级）：
//   pbkdf2-sha256$<iter>$<base64(salt)>$<base64(key)>
func HashPassword(password string) (string, error)

// VerifyPassword 从 encoded 解析出 iter 后重新计算，
// 用 subtle.ConstantTimeCompare 比对，避免时序侧信道。
func VerifyPassword(encoded, password string) bool
```

盐用 `crypto/rand`。`VerifyPassword` 从编码串里读 `iter` 而非用常量 —— 这样将来提高迭代次数时，旧哈希仍能验证通过（本方案不做自动重哈希，但格式已预留）。

**新增 `cmd/user-admin/main.go`**（D15 —— 建号不经 HTTP）

| 命令 | 作用 |
|---|---|
| `-create -username=<u> -password=<p> [-admin] [-name=<显示名>]` | 建号 |
| `-passwd -username=<u> -password=<p>` | 重设密码（**这就是忘记密码的解法**） |
| `-disable -username=<u>` / `-enable -username=<u>` | 停用 / 启用 |
| `-list` | 列出账号（**不输出哈希**） |

- 复用 `cmd/console` 已有的配置加载与 `godotenv` 读取方式，与项目另外 8 个 `cmd/*` 工具风格一致。
- **第一个管理员这样建**：`go run ./cmd/user-admin -create -username=admin -password=<强口令> -admin`。这正是管理端 API 方案解决不了、而 CLI 天然解决的问题。
- ⚠️ 口令走命令行会留在 shell 历史里 → **`-password` 缺省时必须从 stdin 交互读取且不回显**（`golang.org/x/term` 不在依赖里，可用 `golang.org/x/term` 或直接读 stdin 并提示"口令将明文显示"）。最低要求：支持 stdin 读取。

**验收**
- `go test ./internal/auth/...` 通过。用例至少覆盖：同口令两次哈希结果**不同**（盐生效）、正确口令验证通过、错误口令拒绝、空口令拒绝、超长口令不 panic
- `go run ./cmd/user-admin -create -username=admin -password=xxx -admin` 后 `-list` 能看到该账号
- 表里 `password_hash` 形如 `pbkdf2-sha256$210000$...`，**看不到明文**

**提交**：2.10 + 2.11 合成一个 commit —— 数据层与哈希是同一件事的两半，分开提交会留下「有表但不会算哈希」的中间态。

```bash
git commit -m "feat(auth): 自有账号的配置、用户表与密码哈希"
```

**回滚**：`git reset --hard HEAD`

### 步骤 2.12　自有账号：登录逻辑与路由

**`internal/auth/local.go`**

```go
// Login 校验用户名口令，成功后返回 Owner = "local:" + id 的 Session。
func (s *LocalStore) Login(ctx context.Context, username, password string) (Session, error)
```

必须遵守的四条：

| # | 要求 | 原因 |
|---|---|---|
| ① | 用户名**先做格式校验再查库**：`^[a-zA-Z0-9_]{3,32}$` | 必须拒绝含 `:` 的用户名 —— 否则有人用 `feishu:xxx` 这种名字，会与飞书身份在 owner 命名空间上撞车 |
| ② | 口令长度下限取 `auth.local.min_password_length`（默认 8），**上限 128** | 上限用于防止超长输入把 PBKDF2 拖成 DoS（每次登录要算 21 万轮） |
| ③ | **「用户名不存在」与「口令错误」返回同一响应** | 否则接口沦为用户名枚举器。虽然仅管理员能建号、风险有限，但成本为零 |
| ④ | `disabled=1` 一律拒绝，**且不提示「已停用」** | 停用状态不应泄露给尝试者 |

登录成功后更新 `last_seen_at`；`Session.Provider = "local"`，`Owner = "local:" + user.ID`。

**路由**（步骤 2.4 的路由表已预留 `POST /auth/local`）

| 路由 | 说明 |
|---|---|
| `POST /auth/local` | body `{username, password}` → 校验 → **复用步骤 2.3 的同一套 session 签发与 Cookie 写入** → 返回 `{owner, name, provider}` |

- **不做注册路由** —— D12 决定仅管理员创建（建号走步骤 2.11 的 CLI）。
- **不做改密路由** —— 改密走 `cmd/user-admin -passwd`（D15）。少一个需要校验旧口令、还要处理并发改密的接口。

**验收**
- 正确凭据 → 200 + `Set-Cookie`；错误凭据 → 401 JSON；`disabled` 账号 → 401
- 用户名填 `feishu:abc` → 被格式校验拒绝
- 用返回的 Cookie 访问 `/auth/me` → `provider` 为 `"local"`

### 步骤 2.13　登录限流与 owner 前缀收口

**登录限流（本步骤的真正目的）**

注册方式定为「仅管理员创建」后，注册接口不存在了，**限流的目标从「防刷库」变成「防口令爆破」** —— 这反而是更硬的需求，因为用户名是可枚举的（内部系统，人名拼音就那些）。

- 新增 `internal/server/ratelimit.go`：进程内令牌桶，按 key 计数，带空闲桶回收
- 本阶段先用于 `POST /auth/local`，**按 IP + 用户名 双维度计数**：只按 IP 会漏掉内网多机尝试，只按用户名会漏掉批量撞库
- 超限返回 `429` + `Retry-After` 头 + JSON 错误体

⚠️ **这份实现就是步骤 5.2 要复用的那一份**。因此 **5.2 的表述相应调整为「加配置项 + 扩展到 `/chat` 与 `/ws`」**，不再说「新增 `ratelimit.go`」—— 避免两套限流实现。

**owner 前缀收口**

- 确认 `internal/auth/feishu.go` 已用 `feishu:<open_id>` 作 owner（步骤 2.3 已定）
- 确认全项目**没有**裸 `open_id` 直接当 owner 的位置
- `auth_sessions` 的列名已在步骤 2.2 定为 `owner`，此处仅核对

**验收**
- 连续 10 次错误口令 → 触发 429；且在限流窗口内**即使口令正确也被拒**（这是预期行为，不是 bug）
- 窗口过后恢复正常
- 全局搜索确认不存在裸 open_id 当 owner 的写法

**提交**：2.12 + 2.13 合并（登录逻辑与它的防护措施同批落地，避免出现「能登录但可被无限爆破」的中间态）

```bash
git commit -m "feat(auth): 自有账号登录、登录限流与 owner 前缀收口"
```

**回滚**：`git reset --hard HEAD~1`

### 步骤 2.14　自有账号：前端登录表单

`internal/server/web/index.html` + `app.js`

| 元素 | 说明 |
|---|---|
| 登录方式切换 | 并列两个入口：「飞书登录」（按钮，跳 `/auth/login`）与「账号登录」（用户名 + 密码表单）。**某一种未启用则不渲染该入口** |
| 启用状态从哪来 | 扩展 `GET /health` 的返回体，加 `login: {feishu: bool, local: bool}`。该路由本就在免认证白名单内，登录页必须能在未登录状态下读到它 —— **不新增路由**（步骤 2.4 的路由表因此不用改） |
| 用户名显示 | 取 `/auth/me` 的 `name` 与 `provider`，显示成「张三（飞书）」/「zhangsan（账号）」之类，让用户清楚自己当前身份 |
| 登出 | 调 `/auth/logout` |
| 401 处理 | 任意 fetch 收到 401 → 回登录界面（会话过期时不至于卡死在空白页） |
| **无注册入口** | D12 决定，页面上不放任何注册链接或提示 |

⚠️ **不要回退步骤 2.9 的既有约定**：技能暴露面门禁（下拉框显隐、技能事件过滤、`startNewChat()` 异步化）全部保留。

**验收**：两种登录方式都能走通且显示正确来源；停用某一种后该入口从页面消失；会话过期自动回登录页。

**回滚**：`git checkout -- internal/server/web/`

### 步骤 2.15　阶段验收

准备两个飞书账号 A、B，以及两个本地账号 L1、L2（用 `cmd/user-admin` 建），交叉测试：

**认证**
- [ ] 未登录访问 `/` → 302 到 `/auth/login`
- [ ] 未登录调 `POST /chat` → 401（JSON，不是重定向）
- [ ] 完整登录流程走通，`Set-Cookie` 生效
- [ ] state 不匹配的回调被拒绝；重复使用同一个 code 失败
- [ ] 登出后 Cookie 失效

**会话隔离**
- [ ] A 建 2 个会话 → B 登录 → `GET /sessions` **看不到 A 的会话**
- [ ] B 拿 A 的 session id 调 `GET /sessions/{id}` → **403/404，不是 200**
- [ ] B 调 `DELETE /sessions/{id}` 删 A 的会话 → 403
- [ ] B 调 `POST /sessions/{id}/approval` 审批 A 的中断 → 403
- [ ] B 用 A 的 session id 发 `POST /chat` → 拒绝，且**不污染 A 的历史**

**记忆隔离**
- [ ] `GET /memory/facts` 只返回自己的 fact
- [ ] A 与 B 分别对话 → A 检索不到 B 提取出的记忆
- [ ] **A 与 B 的对话都能触发记忆抽取**（专门验证坑 B1：查 `memory_jobs` 两个 owner 的行都应从 pending 变为 succeeded）
- [ ] maintenance 跑一轮后，**两个 owner** 的过期数据都被清理

**自有账号（本地登录）**
- [ ] `cmd/user-admin -create` 建的账号能登录；`-disable` 后**立即**无法登录
- [ ] `-passwd` 重置后新口令生效、旧口令失效
- [ ] `auth.local.enabled: false` 时 `POST /auth/local` 返回 404（路由未注册），页面也不显示账号登录入口
- [ ] 连续错误口令触发 429，且窗口内正确口令同样被拒（预期行为）
- [ ] 页面上**不存在任何注册入口**

**跨身份隔离（本阶段最关键的一组 —— 验证 D14）**
- [ ] L1 与 L2 之间的会话 / 记忆 / 执行记录互不可见（同 A/B 那套用例，换 L1/L2 重跑一遍）
- [ ] **L1 拿飞书账号 A 的 session id 访问 → 403/404**（反向亦然）—— 这是「两套身份完全独立」的判定性用例
- [ ] L1 调 `POST /sessions/{A 的 id}/approval` → 403
- [ ] L1 与 A 分别对话 → 记忆互不串台

**执行记录 / 容错**
- [ ] A 的执行记录 B 查不到
- [ ] 管理员本地账号（`is_admin`）登录成功
- [ ] **模拟飞书不可用**（清空三个 `FEISHU_*` 环境变量并重启）后，本地账号仍可正常登录 —— 这是本地账号体系存在的首要理由
- [ ] **只启用本地账号**（飞书三字段留空 + `auth.local.enabled: true`）时服务能正常启动并登录（验证步骤 2.10 的校验修正）
- [ ] CLI（`cmd/console`、`cmd/eval`）未配置 auth 时**行为不变**

---

## 五、阶段 3：eino 框架收敛

**目标**：让业务包不再直接依赖 eino 框架，把 7 个包收进 `internal/eino/` 一个命名空间。

**性质**：**行为零变化**——严格限定为路径移动，**不碰任何 eino API 写法**（坑 C6）。

### 步骤 3.1　机械平移（核心，一次性）

**动作**（必须一次提交完成：Go 不支持部分重命名，中间状态必然编译不过）

```bash
cd E:/11/my-eino-app
mkdir -p internal/eino
for p in agent chain model prompt rag tool observability; do git mv internal/$p internal/eino/$p; done
# 再改 29 行 import 路径（用编辑器全局替换，注意只替换引号内的路径）
go build ./... && go vet ./... && go test -p 1 ./...
git commit -m "refactor: converge eino code into internal/eino namespace"
```

**15 个文件 / 29 行 import**

| 文件 | 行数 |
|---|---|
| `internal/agent/agent.go` | 3 行 + 1 处分组错位修正 |
| `internal/agent/middleware.go` | 2 行 |
| `internal/agent/middleware_test.go` | 1 行 |
| `internal/chain/rag_chain.go` | 2 行 |
| `internal/tool/knowledge.go` | 1 行 |
| `internal/server/service.go` | 4 行 |
| `internal/evaluation/evaluation.go` | 1 行 |
| `internal/memory/live_test.go` | 1 行 |
| `internal/integration/integration_test.go` | 1 行 |
| `cmd/console/main.go` | 6 行 |
| `cmd/eval/main.go` | 3 行 |
| `cmd/server/main.go` | 1 行 |
| `cmd/indexer/main.go` | 1 行 |
| `cmd/retrieve/main.go` | 1 行 |
| `cmd/memory-reindex/main.go` | 1 行 |

⚠️ **坑 C1**：`go test` 不能用默认并行度 → 一律 `go test -p 1 ./...`
⚠️ **坑 C2**：测试文件的 import 容易漏改（29 行里有 3 行在测试文件）→ `go build` 全绿但 `go test` 挂
⚠️ **坑 C3**：**路径前缀二次替换** —— `internal/model` 与 `internal/eino/model` 有前缀关系，必须匹配引号内完整串 `"my-eino-app/internal/model"`，或先替换更长的路径，否则会替换成 `internal/eino/eino/model`
⚠️ **坑 C5**：**别名约定必须保住** —— `modelset` / `toolset` 这类别名一旦丢掉就会与 `einomodel` / `einotool` 混在一起。阶段 3 只改路径，**不动别名**

**验收**：`go build ./...` && `go vet ./...` && `go test -p 1 ./...` 全绿，且测试通过数与步骤 0.1 的基线**完全一致**（行为零变化）。

### 步骤 3.2　边界守卫（成本极低）

不加守卫，这次收敛会在几个月内被新代码重新扩散掉。

**动作**：`Makefile` 新增目标并并入 `check`：

```makefile
boundary: ## 校验 eino 编排 API 未扩散到 internal/eino 之外
	@! grep -rl "cloudwego/eino/\(compose\|adk\|callbacks\)" --include="*.go" internal/ cmd/ \
	  | grep -v "^internal/eino/" \
	  || (echo "❌ compose/adk/callbacks 出现在 internal/eino/ 之外" && exit 1)
	@echo "✅ eino 编排边界完好"

check: fmt boundary test vet
```

**验收**：`make boundary` 通过（当前应通过——5 个引用文件全在集内）。

### 步骤 3.3　消除构造泄漏

把「构造 eino 组件」这件事从业务包收回到 eino 层。三项独立，可分开做。

**3.3.1 embedder 构造去重**

新增 `internal/eino/embedding/embedding.go`：

```go
// NewFromConfig 按配置构造 Embedder。
// rag 与 memory 共用同一份构造逻辑，避免两处配置漂移导致向量空间不一致。
func NewFromConfig(ctx context.Context, cfg config.Embedding) (embedding.Embedder, error)
```

改造：`internal/eino/rag/factory.go:22-25` 与 `internal/memory/coordinator.go:32-34` 改调它，memory 从此不再 import `eino-ext/components/embedding/openai`。

**3.3.2 工具构造下沉**

`internal/skill/runtime.go:70` 的 `utils.NewTool(info, func...)` 是直接构造 eino 工具。在 `internal/eino/tool` 暴露一个接收业务函数的构造函数（如 `toolset.NewReaderTool(fn)`），skill 只传函数，不再 import `components/tool/utils`。

⚠️ 按 D10：**只做构造下沉，不动提示词文案**（提示词是步骤 1.7 的验收对象）。

**3.3.3 业务包改用门面**

新增 `internal/eino/facade.go`（类型别名，零运行时开销、不产生包装层）：

```go
package eino

import (
    einomodel "github.com/cloudwego/eino/components/model"
    einotool  "github.com/cloudwego/eino/components/tool"
    "github.com/cloudwego/eino/components/embedding"
)

type ChatModel = einomodel.ToolCallingChatModel
type BaseTool   = einotool.BaseTool
type Embedder   = embedding.Embedder
```

改造 `internal/server/service.go:16-17`、`internal/memory/extractor.go:7`、`internal/memory/milvus.go:7`、`cmd/console/main.go`，改为只 import `internal/eino`。

**阶段结束后效果**：`server` / `memory` / `skill` 三个业务包对 `cloudwego/eino/components/*` 的依赖降为 0（仅保留 `schema`）。

**验收**：`make check` 通过（含 `make boundary`）；`make boundary` 仍通过。

---

## 六、阶段 4：评测与可观测性收口

### 步骤 4.1　评测增加「路由」维度

**现状**：`internal/evaluation/evaluation.go:17-22` 的 `Case` 只有 `question` / `expected` / `must_cite` / `should_refuse`——纯看答案文本，**技能选得对不对完全测不出来**。

**埋点已现成**：`runtime.go:94-97` 已写入 `skill_names` / `requested_names`。

**动作**

1. `Case` 增加 `ExpectSkills []string \`json:"expect_skills,omitempty"\``
2. 评测执行时用 `execution.NewRecorder(0)` 挂到 `ChatWithSink` 上
3. 跑完扫 `Events()` 里 `SkillLoaded` 的 `payload.skill_names`，做集合断言
4. `Report` 增加 `RoutingAccuracy float64`
5. `cmd/eval/main.go` 输出该指标

**验收**：`make eval` 输出含 `routing_accuracy`。

**地址**：`internal/evaluation/evaluation.go`、`cmd/eval/main.go`、`internal/integration/eval_cases.json`

### 步骤 4.2　评测集扩到 ≥20 题

**动作**：`internal/integration/eval_cases.json` 从 3 题扩到 **≥20 题**（D3），覆盖步骤 1.4 里那几组易混场景，每题带 `expect_skills`。

**验收**：`make eval` 输出的 `routing_accuracy` 有意义（不是 0 或 1 的极端值）；易混场景（Excel + 生成文件、Word 工单推荐）判定正确。

---

## 七、阶段 5：上线收口

### 步骤 5.1　优雅停机（一行修复）　✅ 已实现并验证

**问题**：`cmd/server/main.go:22` 是 `signal.NotifyContext(context.Background(), os.Interrupt)` —— **只捕获 SIGINT**，而 `docker stop` 默认发 **SIGTERM**。走不到 `server.Shutdown`（`main.go:49`），10 秒优雅期与 `defer service.Close()` 全部失效 → 正在跑的请求被硬切、执行事件可能丢终态。

**动作**

```go
import "syscall"

ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
```

**验收**：容器内起服务，`docker stop` 后日志出现优雅停机路径（不是直接被 kill）；用 `docker stop -t 30` 观察 10 秒优雅期生效。

### 步骤 5.2　限流与配额　✅ 已实现并在独立生产测试栈验证

> **本节为新增设计，不在原三份方案内**（原方案仅在「尚未归属的缺口」中登记）。这是对外上线前的硬要求。

**问题**：当前只有全局 `runtime.max_concurrency`（本地/部署 2，ark 8）+ 同 session 排队，**无 per-user / per-IP 限流，无 token 预算**。`config.ark.yaml` 的并发 8 意味着一个脚本能同时打满 **8 路计费调用**。

**动作**

1. **复用步骤 2.13 已建的 `internal/server/ratelimit.go`** —— 自有账号登录限流那一步已经落地了令牌桶实现，本步骤**不再新建**，只做扩展：key 取 `auth.OwnerFromContext(ctx)`；未认证时退回客户端 IP（当前实现只取可信链路上的 `RemoteAddr`，不直接采信客户端可伪造的 `X-Forwarded-For`）。若部署在反向代理后，必须由代理层覆盖并固定真实客户端地址，确保应用看到的 `RemoteAddr` 可用于按 IP 限流。空闲桶回收在 2.13 已实现。
2. 新增配置 `runtime.rate_limit`：

```yaml
runtime:
  rate_limit:
    enabled: true
    per_minute: 30
    burst: 10
```

三份配置均按上述默认值写入。

3. 在 `POST /chat` 与 `GET /ws` 两条路径上应用（其余路由是只读或轻量）。
4. 超限返回 `429` + `Retry-After` 头 + JSON 错误体。

**边界（必须写进注释）**：这是**单副本**限流，进程内计数。多副本部署需换 Redis，当前不部署多副本。同时**不做 token 预算**——token 计量需要解析模型响应，收益与复杂度不匹配，登记为后续项。

**验收**：脚本连续打 40 次 `POST /chat`，第 31 次起返回 429；`burst` 内允许突发；`Retry-After` 合理。

### 步骤 5.3　备份与恢复演练　✅ Windows/Linux 实现已完成；Windows 本机恢复演练通过

> **本节为新增设计，不在原三份方案内。**

**问题**：`data/backups/` 下只有 **2026-09-10 的一次手工全库 dump**，无定时任务、无恢复演练、无保留策略；Milvus 三个数据卷无快照流程。**只备份不恢复等于没备份。**

**动作**

1. 新增 `scripts/backup.ps1`：
   - `mysqldump` 全库 → `data/backups/<yyyyMMdd-HHmmss>/mysql.sql`
   - Milvus 数据卷打包（`docker run --rm -v <vol>:/data -v <backupdir>:/backup alpine tar czf /backup/<vol>.tgz -C /data .`）
   - 写 `manifest.txt`：时间、MySQL 表行数摘要、各文件 SHA256
2. 新增 `make backup` 目标调用它
3. **保留策略**：保留最近 **14** 份，超出删除最旧（脚本内实现，删除前打印被删目录）
4. 新增 `scripts/restore-check.ps1`（**恢复演练**）：
   - 把最近一份 dump 恢复到临时库 `eino_restore_check`
   - 比对 `conversations` / `messages` / `memory_facts` 行数与 `manifest.txt`
   - 比对完成后 `DROP DATABASE eino_restore_check`
5. 定时：Windows 任务计划每 6 小时执行 `make backup`；Linux 用 cron（给出命令）

**验收**
- [ ] `make backup` 产出带时间戳的目录 + 完整 `manifest.txt`
- [ ] `restore-check` 行数比对全部一致
- [ ] 连续跑两次 `make backup` 不互相覆盖（时间戳不同）
- [ ] 手工造一个"第 15 份"，确认最旧一份被清理且清理前有打印

### 步骤 5.4　健康检查分离　✅ 已实现并验证

**问题**：`/health`（`http.go:43-45`）返回静态 ok，**不检查 MySQL / Milvus / Ollama**，而 `Dockerfile:35-36` 的 HEALTHCHECK 正是打它 → liveness 与 readiness 未分离。

**动作**
- `/health` 保持 liveness 语义（静态 ok，含 `debug` 字段）—— `Dockerfile` 的 HEALTHCHECK **保持不动**
- 新增 `GET /health/ready`：检查 MySQL / Milvus / Ollama 连通性，单项超时 2 秒，返回逐项状态
- 该路由已在步骤 2.4 加入免认证白名单
- compose 中 app 服务的健康检查改用 `/health/ready`（`Dockerfile` 的 HEALTHCHECK 不变，两者语义不同）

**验收**：停掉 MySQL 后 `/health` 仍 200、`/health/ready` 返回非 200 且指出是哪一项失败；恢复 MySQL 后 `/health/ready` 自动回到 200。

### 步骤 5.5a　生产 bootstrap（正式上线前的一次性初始化）

生产镜像包含 `cmd/server`、`session-migrate` 和 `user-admin`；`indexer`、`eval` 仍只在宿主机或 CI 中运行。bootstrap 使用同一生产镜像的维护 CLI，通过 Compose 内部网络连接 MySQL，不需要把 CLI 暴露为用户流量，也不需要宿主机 Go 环境：

bootstrap 只用于正式上线前的一次性初始化，不是最终运行形态。现有开发 Compose 已读取 `${MYSQL_BIND:-0.0.0.0}`；bootstrap 时必须显式设置 `MYSQL_BIND=127.0.0.0` 后启动数据库，禁止在本项目操作中使用 `0.0.0.0`。这条操作纪律不改变其他项目共用数据卷的默认配置。

bootstrap 验收通过后，必须切换到 5.6a 的独立生产 Compose。正式生产启动不得再使用开发 Compose，不发布 MySQL、Milvus、Attu 的宿主机端口，也不依赖宿主机上的 `user-admin` 或 `session-migrate` 才能启动应用。运维 CLI 只在 bootstrap 和后续维护窗口使用；应用通过 Docker 内部网络连接 MySQL。若选择把 CLI 打进独立运维镜像，也不能把它作为正式用户流量容器运行。

维护 CLI 必须显式指向生产 MySQL，不能依赖当前目录的 `config.yaml` 默认 file store。宿主机维护窗口仍可按下面方式执行；正式生产初始化推荐使用镜像内的 `docker compose run --rm --no-deps --entrypoint` 命令：

```powershell
$env:SESSION_STORE="mysql"
$env:MYSQL_HOST="127.0.0.1"
$env:MYSQL_PORT="3306"
$env:MYSQL_DATABASE=(Get-Content .env | Select-String '^MYSQL_DATABASE=').ToString().Split('=',2)[1]
$env:MYSQL_USER="<production-mysql-user>"
$env:MYSQL_PASSWORD="<production-mysql-password>"
go run ./cmd/user-admin -create -username=admin -admin
go run ./cmd/session-migrate -claim-owner=<owner>
```

**提交边界**：`chore(deploy): define production bootstrap procedure`；现有 Compose 的 loopback 绑定和生产变量说明属于本子步骤。

**验收**：管理员登录成功；`local-owner` 下历史会话和记忆已迁移；bootstrap 期间 MySQL 只监听 `127.0.0.1`；CLI 不依赖应用容器存在；切换后由生产 Compose 内部网络连接数据库。

### 步骤 5.5b　上线前配置切换（生产部署最后一步）

前置：先执行 5.5a 完成管理员建号和历史数据归属；再完成 5.1–5.4 以及 5.6–5.10 的全部子步骤，最后执行本步。5.5b 是实际切换生产配置的最后一步。

| 配置 | 改为 | 说明 |
|---|---|---|
| `config.docker.yaml` 的 `auth.enabled` | `false` → **`true`** | 打开鉴权 |
| `FEISHU_APP_ID` / `FEISHU_APP_SECRET` / `FEISHU_REDIRECT_URL` | 填入真实凭据 | 走**环境变量**，不写进仓库 |
| `FEISHU_REDIRECT_URL` / 飞书后台回调地址 | **统一改为 HTTPS 可达地址** | 应用配置、`.env` 和飞书后台三处必须完全一致；证书必须被浏览器和飞书回调链路信任 |
| `config.docker.yaml` 的 `auth.local.enabled` | **保持 `true`** | 保留本地账号入口，作为飞书故障时的兜底 |
| `config.docker.yaml` 的 `memory.identity_mode` | `local_single_user` → **`multi_user`** | 与 auth 配套 |
| `config.docker.yaml` 的 `debug` | **保持 `false`** | 部署态不暴露技能入口 |
| `cmd/server` 监听地址 | 保持 `:18180`（容器内） | 对外暴露面由 compose 端口映射决定 |

⚠️ 管理员建号和历史数据归属已移到 5.5a，必须先完成；否则一旦飞书认证服务出问题，系统将没有兜底入口。

⚠️ `auth.enabled: true` 时若 `session.store != mysql`（`config.docker.yaml` 已是 mysql）或 `FEISHU_*` 缺失，`ValidateAuth()` 会让启动**直接失败**——这是刻意设计（清晰失败优于静默）。

**验收**：按「十一、总验收清单 → 上线前最终检查」逐项打勾；容器重启后走一遍完整登录 + 一次对话 + 一次工具审批。

### 步骤 5.6　生产部署边界与网络收口　✅ 配置完成并在独立生产测试栈验证

第一阶段采用**内部用户灰度、单副本部署**。反向代理负责 HTTPS、访问来源限制、请求大小限制和可信真实客户端 IP；应用负责认证、授权、会话隔离和技能路由。MySQL、Milvus、Ollama 和 Attu 不直接暴露给用户网络。

当前 Compose 文件仍发布 3306、19530、9091、Attu 和应用端口；这只是开发配置，不能直接作为生产网络配置。生产配置必须删除不必要的 `ports`，改用内部 Docker 网络；Attu 默认关闭或仅绑定管理网段。生产 MySQL 使用独立数据卷或实例，避免与其他项目共用执行表后被 `RecoverStale` 误处理。

反向代理按自身可信的真实客户端地址执行主要 IP 限流。应用不直接采信客户端可伪造的 `X-Forwarded-For`；代理后的应用 `RemoteAddr` 是代理地址，因此应用侧 IP 桶只作为代理失效时的兜底，日常防爆破由代理 IP 桶和应用用户名桶共同承担。HTTP 读超时至少覆盖当前 180 秒的 `request_timeout`；WebSocket 透传 `Upgrade`/`Connection` 并关闭缓冲；第一阶段必须启用 HTTPS 和 `cookie_secure`；`/auth/callback` 也必须限流。

**验收**：完成 5.5a 后关闭临时 loopback MySQL 映射；从用户网络无法连接 MySQL、Milvus、Attu；只有 HTTPS 反向代理入口可访问应用；WebSocket 可建立；代理层登录限流按真实客户端 IP 生效；应用用户名桶仍生效；代理读超时不短于应用超时。

### 步骤 5.7　生产能力边界与确定性路由　✅ 当前基线已实现；调度改进与复评见 5.7d–f

第一阶段只承诺以下已接入能力：报告文本生成、授权路径下的文本/DOCX 读取、知识库检索和审批后的笔记写入。当前真正注册的工具只有 `current_time`、`write_note`、`load_skills`、`local_file_read`、`knowledge_search`。

表格、PDF、PPT 文件处理目前只有技能说明，没有真实文件工具，不能承诺读取、编辑或生成文件；用户提出这些需求时必须明确告知限制和替代方式，不能加载空壳技能制造“已支持”的假象。

路由顺序：

1. 私有知识问题由服务端强制执行 `knowledge_search`，答案必须基于返回资料并带来源；预检索结果注入 agent instruction/history 上下文，不依赖模型再次选择工具。
2. 报告生成命中 `report_writer`；文档读取命中 `documents`。
3. 表格、PDF、PPT 文件请求进入能力限制回复，不进入伪执行流程。
4. 未命中或低风险问题才允许模型自主选择。

路由规则必须结合动作和对象，而不是只匹配单个关键词；技能、工具、提示词或 embedding 模型变更后必须重新执行 `make eval`。

**验收**：知识库请求都有检索事件和来源；文档无命中时先查询项目长期记忆；两者都无命中时实际调用模型并明确通用回答边界；报告/文档场景有实际技能事件；空壳技能场景不声称已生成文件；`debug=false` 的用户可见回答不出现技能名；评测集覆盖上述边界。

### 步骤 5.7c　已接入技能的服务端选择

在 `internal/server/service.go` 的请求装配路径增加动作 + 对象判定：命中报告生成时服务端预加载 `report_writer`，命中文本/DOCX 读取时预加载 `documents`。该预加载属于服务端内部上下文，不重新开放 `GET /skills`、请求体 `skill` 字段或技能名展示；`requestedSkill()` 的 debug 门禁只继续保护客户端指定技能和目录暴露。

预加载复用 agent 现有 instruction/context 注入路径，并使用已有的 `SkillPreloaded` 事件记录；知识预检索复用标准 `tool_started`/`tool_completed` 事件，payload 保留 `hit_count`、`sources` 和 `source=server_preflight`。新增路由单测覆盖动作、对象、组合请求、未命中和 debug=false，不允许通过单个关键词误触发。

**提交边界**：`feat(routing): preload connected skills server side`。

**验收**：`report_writer` 和 `documents` 的 4 类正例均产生预加载事件；空壳技能不产生预加载事件；非 debug 响应和 `/skills` 不暴露技能名；`go test -p 1 ./internal/server/...` 全绿。

### 步骤 5.7d　收紧技能路由规则并补齐反例

**为什么做**：当前 `internal/routing/routing.go` 用关键词判断是否预检索、预加载或返回能力限制。它已覆盖关键正例，但宽泛关键词容易把“提到某个文件类型”误判成实际文件操作；其余任务仍依赖模型主动调用 `load_skills`。技能目录中的 `scenarios` / `not_for` 是给模型看的自然语言，不应被当成另一套机器路由规则。

**动作**

1. 保留 `internal/routing` 为服务端与评测共用的确定性路由入口；不新增分类模型调用，也不复制一份关键词规则到 `service.go` 或评测程序。
2. 给路由结果补充稳定的来源/原因标识，供单测和评测报告诊断；例如 `knowledge_preflight`、`connected_skill`、`unsupported_capability`、`no_match`。标识是内部诊断信息，不展示给普通用户。
3. 固定优先级：私有项目事实走服务端知识预检索；已接入且动作/对象明确的报告与文档任务预加载对应技能；真正要求读写未接入文件能力时返回限制说明且不预加载空壳技能；未命中或只是讨论、推荐、拟方案时不强行加载技能，继续交给模型正常回答或澄清。
4. 路由要同时检查“动作 + 对象”，覆盖否定、能力询问、文本大纲与文件产出区别，以及复合任务。复合任务中的已支持部分仍可使用对应技能；不得因用户只提到 PDF/Excel/PPT 就宣称正在读写文件。
5. 保留两阶段加载：目录摘要可用于模型选择，完整正文只在服务端确定性预加载或模型调用 `load_skills` 时加载。`required_tools` 缺失只代表不能执行对应工具能力，不代表该技能无用或应删除；用户要求设计方案时仍可提供指导。

**受影响文件**：`internal/routing/routing.go`、`internal/routing/routing_test.go`、`internal/server/service.go` 及路由相关服务端测试。除非测试证明现有事件无法表达，不新增执行事件类型。

**提交边界**：`fix(routing): tighten deterministic skill dispatch`。

**验收**

- [x] 每类关键正例、普通问答、易混淆反例、否定/能力询问、复合请求和未接入能力都有路由单测；同一输入重复运行结果一致。
- [x] 私有知识请求仍由服务端预检索；支持的报告/文档动作命中正确技能；普通闲聊不触发技能预加载；未接入文件操作不预加载空壳技能。
- [x] 技能目录正文仍按需加载；`debug=false` 不泄露技能名；缺少工具时回答不承诺已执行。
- [x] `go test -p 1 ./internal/routing ./internal/server/... ./internal/skill/...` 通过。

**回滚**：提交后使用 `git revert <本步骤提交>`；不使用 `reset`、`checkout` 或清理命令覆盖共享工作区。

### 步骤 5.7e　把技能证据和工具执行证据拆开

**为什么做**：当前 `cmd/eval/main.go` 的 `routingFromEvents` 把 `skill_preloaded` 也算作 `ToolEvidence`。因此一轮只把技能正文放进提示词、实际没有调用任何工具，也可能被计为“有工具执行证据”；现有 `tool_evidence_rate=1` 不能单独证明工具真的执行过。技能使用和工具执行是两个不同判据，必须分开报告。

**动作**

1. 在评测结果中分别统计：`skill_preloaded` / `skill_loaded`（技能正文证据）、`tool_started`（工具尝试）、`tool_completed`（工具成功）、`tool_failed`（工具失败）。**技能事件不得再增加工具执行率的分子**；回答里自称“调用过工具”也不算证据。
2. 扩展评测用例断言，使其可声明 `expect_skills`、`expect_no_skills`、`expect_tools`（必须出现 `tool_started`）和 `expect_tool_success`（必须出现 `tool_completed`）；需要时声明禁止调用的工具。没有任何这些断言的题目不进入路由/工具指标分母。
3. `routing_accuracy` 只描述期望技能是否加载，额外加载单独列出；另报关键服务端路由召回率、普通/反例误预加载率、工具尝试率和工具成功率。工具指标只以明确要求工具的用例为分母，不拿纯文本报告等不需要工具的任务稀释或虚增结果。
4. 对知识检索用例，必须核对 `tool_name=knowledge_search` 的 `tool_started` 及成功时的 `tool_completed`，并校验来源字段；无命中是成功完成检索，不等于工具失败。对失败用例，必须能区分已尝试但失败与成功执行。
5. 将“没发生工具成功事件却声称已完成”的情形判失败；技能加载成功不能替代文件读取、写入或知识检索的工具证据。

**受影响文件**：`internal/evaluation/evaluation.go`、`internal/evaluation/routing_test.go`、`cmd/eval/main.go`、`cmd/eval` 相关测试以及 `internal/integration/eval_cases.json`。

**提交边界**：`fix(eval): separate skill and tool execution evidence`。

**验收**

- [x] 单测证明只有 `skill_preloaded` / `skill_loaded` 时工具尝试率和成功率都是 0。
- [x] 单测证明 `tool_started` 只算尝试，只有 `tool_completed` 才算成功；`tool_failed` 不会误算成功。
- [x] 缺少工具、普通问答、应预加载但不需工具的技能任务，都使用各自正确的分母。
- [x] `make eval` 输出技能路由、工具尝试、工具成功和答案质量指标；工具证据或禁止工具断言失败时命令返回非零。
- [x] `go test -p 1 ./internal/evaluation/... ./cmd/eval/...` 通过。

**回滚**：提交后使用 `git revert <本步骤提交>`。

### 步骤 5.7f　扩充路由评测并完成前后对照

**评测集**：把 `internal/integration/eval_cases.json` 扩到至少 30 道技能/工具路由用例，至少包括：12 道关键正例（知识、报告、文档）、8 道普通问答或易混反例、5 道组合技能任务、5 道未接入能力边界题。题目可兼属多个分组，但每题必须明确技能/工具断言；计入答案正确率的题必须有非空 `expected` 或 `should_refuse`，不可再用空字符串制造恒真结果。模型自主选择非关键技能的用例单独标注，作为诊断指标，不与服务端关键路由准确率混成一个分数。

**实操顺序**

1. 开始实现前，在干净提交或独立工作区保存当前评测基线；共享工作区有未提交改动时不得把它们覆盖、清理或混入基线。记录代码版本、模型 ID、`debug` 状态、RAG 阈值/知识库快照、评测集版本和完整命令。当前 `cmd/eval` 通过 `config.Load()` 读取 cwd/上级的 `config.yaml`，没有 `-config` 参数；需要 `debug=false` 时在独立工作区准备对应配置，不要覆盖共享工作区中的配置，也不要传入未实现的参数。
2. 修改后用同一模型、同一配置、同一知识库和同一题集复跑；至少执行一次 `debug=false` 的生产态评测。若模型输出有随机波动，再重复一轮并同时保存两次原始 JSON。
3. 保留历史 `RUNBOOK.md` 记录不改写；将这次结果追加为新的运行期实测，附上逐题失败项和失败归因，而不只记录一个汇总分数。

**最终门槛**

- [x] 服务端关键路由召回率 ≥95%；普通/反例不应预加载技能的用例中误预加载为 0；未接入能力题 100% 不进入伪执行流程。
- [x] 声明需要调用工具的成功用例 100% 有对应 `tool_started` 与 `tool_completed` 证据；失败用例明确显示失败，不能报告完成。无需工具的用例不纳入工具成功率分母。
- [x] 必须引用的私有知识题引用率 100%；明确应拒答/说明无依据的题目拒答率 100%；答案断言正确率达到现有门槛 ≥80%。
- [x] 自主模型路由准确率单独展示。它不替代关键路由门槛；本轮为 1/3，说明非关键技能不能依赖模型自主加载。
- [x] 最后执行 `go test -p 1 ./...`、`go build -p 1 ./...`、`go vet -p 1 ./...` 和真实 `make eval`；评测依赖的 Ollama/Milvus 不可用时明确标为“未评测”，不得以单测通过代替真实评测。

**可比性说明**：本轮修改前没有保存与当前相同 40 题、`qwen-fast`、`debug=false`、阈值 0.5 和同一知识库快照的完整基线；此前 20 题历史结果设置不一致，因此不能声称严格的前后对照。当前实现和门槛已完成实测，但 5.7f 的严格前后比较仍待补齐。

**提交边界**：`test(eval): add skill dispatch regression set`；实测结果追加到 `docs/RUNBOOK.md` 的下一条运行记录（当前最后一条为 14.26），不另建方案或评测清单。

**回滚**：提交后使用 `git revert <本步骤提交>`；评测结果保留为历史证据，并标注对应代码版本。

### 步骤 5.8　发布、回滚和并发保护　✅ 已实现并完成标签切换演练

应用发布使用不可变版本标签，并至少保留上一版本镜像或二进制；不能依赖固定的 `my-eino-app:local` 配合 `up -d --build` 回滚。路由规则使用功能开关，允许关闭单个问题能力。

并发槽满时必须有排队上限和排队超时；不能无限等待。模型超时应返回 `504`，并与普通 `500` 分开统计。单副本阶段保留现有限流；多副本前必须另行引入共享会话、Redis 限流和共享文件存储。

**验收**：新旧版本可切换；排队超过 5 秒或队列达到上限立即返回 `503`；模型超时返回 `504`；回滚不需要重新构建源码。

### 步骤 5.9　备份、日志和告警　✅ 代码与本机演练完成；异地复制待目标环境执行

备份范围必须包括 MySQL dump、Milvus 数据卷以及 `app-data` 中的 notes、checkpoints、executions；同机同盘副本不算灾备。备份成功的判据包括 manifest SHA256、恢复后表行数比对和非空业务数据检查，不能只看脚本退出码。目标暂定 RPO 6 小时、RTO 2 小时。

当前 `/metrics` 是需登录访问的进程内 JSON 计数，重启后归零，不能直接被 Prometheus 抓取。5.9c 已增加一个仅允许内网/代理管理网访问、使用 bearer/token 校验的指标入口，不能把整个 `/metrics` 匿名暴露；通知可由该入口或定时探测脚本驱动。Compose 已设置日志大小上限、保留周期和轮转策略；`execution_events` 已增加周期清理，不依赖启动时的一次清理。

`scripts/backup.ps1`、`scripts/backup.sh` 现已覆盖三个 Milvus 依赖卷和 `app-data`；5.9b 的代码实现已完成，仍须在目标环境执行备份、异地复制和恢复演练，不能把静态配置检查误认为灾备验证。

**验收**：定时备份成功；恢复检查通过；备份失败、健康检查失败、模型不可用、技能工具连续失败和磁盘不足都有可见告警；日志不会无限增长。

### 步骤 5.10　内部灰度和上线门槛　🟡 门槛出口与本机实测已通过；目标环境灰度仍待执行

先开放给 3–10 名内部用户，灰度 3–7 天，再决定是否扩大范围。扩大范围前满足：认证和跨用户隔离 100% 通过，关键技能必需场景召回率不低于 95%，需要工具的请求有真实 `tool_started` / `tool_completed` 证据（不能把技能预加载算成工具执行），关键流程无凭空声称已完成，非超时 5xx 小于 1%，备份成功率 100%，最近一次恢复检查不超过 7 天，严重安全问题为 0。

启用多用户前必须运行 `go run ./cmd/session-migrate -claim-owner=<owner>` 处理 `local-owner` 下的历史会话和记忆。生产运行环境必须明确为 Windows/Docker Desktop 或 Linux/Docker，并按同一环境验证备份、定时任务和卷路径。`cmd/user-admin` 当前没有删除账号操作，离职账号按禁用处理；管理员口令必须在上线前改掉。

**验收**：灰度期间每天检查错误、路由、备份和资源使用；认证、数据隔离、备份或恢复出现严重问题时停止扩大范围。

### 阶段 5 的可执行拆分、提交边界与命令

5.5a、5.6–5.10 不是“做完代码再一起验收”的工作包，按下列边界逐项提交。每个子步骤验收失败时只回退该子步骤；所有子步骤完成后，才允许执行 5.5b 的生产配置切换和灰度。

| 子步骤 | 必须动作与受影响文件 | 提交边界 | 验收命令/断言 |
|---|---|---|---|
| 5.6a 网络 Compose | 新增独立生产 Compose 文件 `docker-compose.prod.yml`，不与开发 Compose 叠加；生产文件不包含 MySQL、Milvus、Attu 的宿主机 `ports`，应用只绑定代理内网；Attu 默认不启动；应用镜像使用 `${APP_IMAGE}` 不可变标签 | `chore(deploy): add production network compose` | 停止开发 Compose 后执行 `docker compose -f docker-compose.prod.yml --env-file .env.prod config` 和 `docker compose -f docker-compose.prod.yml --env-file .env.prod up -d --no-build`；确认 MySQL/Milvus/Attu 无宿主机端口，应用健康检查通过，从用户网段只能访问 HTTPS 入口 |
| 5.6b 代理配置 | 新增并纳入部署文档的 Nginx 配置：TLS、HSTS、`proxy_read_timeout 210s`、WebSocket Upgrade、关闭 buffering、`limit_req`；按真实客户端 IP 在代理层限流 | `chore(proxy): add production reverse proxy policy` | `nginx -t`；浏览器登录/回调；WebSocket 连通；同一真实 IP 超过阈值返回 429 |
| 5.6c 回调保护 | `internal/server/auth.go` 为 `/auth/callback` 增加按 IP 和回调状态的限流；应用侧仍只使用 `RemoteAddr` 兜底，不读取 XFF | `fix(auth): throttle oauth callback` | 新增回调限流单测；超过阈值返回 `429 + Retry-After`；合法回调仍能完成登录 |
| 5.7a 确定性预检索 | 在 `internal/server/service.go` 的请求装配路径增加服务端知识意图判定和 `knowledge_search` 预检索；文档命中时只注入文档资料；文档无命中时改查项目范围长期记忆；两者都无命中时放行模型并注入通用回答边界；复用标准 `tool_started`/`tool_completed` 事件，不通过 debug 技能目录，不向用户暴露技能名 | `feat(routing): enforce knowledge preflight` | 路由单测覆盖命中、文档无命中+记忆命中、双无命中和普通问答；集成测试检查检索顺序、模型确实被调用、来源和通用回答标记；非 debug 响应不含技能名 |
| 5.7b 评测重写 | 重写 `internal/integration/eval_cases.json`：执行类题增加可判定答案断言；空壳文件技能改为能力限制断言；增加知识库无命中用例 | `test(eval): assert production routing and answer boundaries` | `go test -p 1 ./internal/evaluation/...`；`make eval` 输出路由、断言、拒答和工具证据指标 |
| 5.7c 已接入技能预加载 | `SkillPreloaded` 已在 `internal/execution/event.go` 定义并列入 `knownTypes`；本步骤在 `internal/skill/runtime.go` 支持多技能预加载，在 `internal/server/service.go` 增加报告/文档动作+对象判定，服务端预加载 `report_writer`/`documents`；`requestedSkill()` 继续保护客户端技能指定 | `feat(routing): preload connected skills server side` | `go test -p 1 ./internal/server/... ./internal/skill/...`；报告、文档和组合请求产生预加载事件，空壳技能不预加载，debug=false 不暴露技能名，知识来源可见 |
| 5.7d 路由规则收紧 | 完善 `internal/routing/routing.go` 的动作+对象规则、来源/原因标识和正反例；保持它为服务端与评测共用实现 | `fix(routing): tighten deterministic skill dispatch` | `go test -p 1 ./internal/routing ./internal/server/... ./internal/skill/...`；正例命中、反例不误加载、组合/未接入能力边界通过 |
| 5.7e 技能/工具证据分离 | 修正 `internal/evaluation/evaluation.go` / `cmd/eval/main.go`：技能加载、工具尝试、工具成功/失败分别统计；评测用例支持期望无技能、期望工具调用和禁止工具调用 | `fix(eval): separate skill and tool execution evidence` | 单测确保 `skill_preloaded` 不增加工具率；`tool_started`/`tool_completed`/`tool_failed` 口径正确；`make eval` 输出分列指标 |
| 5.7f 回归集与前后评测 | `internal/integration/eval_cases.json` ≥30 路由用例；相同模型、配置、知识库完成基线和修改后对照；追加运行结果到 `docs/RUNBOOK.md` | `test(eval): add skill dispatch regression set` | 关键路由召回 ≥95%、无技能反例零误预加载、未接入能力 100% 明确边界、工具成功证据符合预期；`make check` 与真实 `make eval` 通过 |
| 5.8a 并发保护 | 在 `internal/server/service.go:enter` 增加 `runtime.queue_limit` 和 `runtime.queue_timeout`；满队列立即返回可识别错误 | `feat(runtime): bound concurrency queue` | 单测：并发槽满时等待不超过 5 秒，返回 503；配置校验拒绝非正数 |
| 5.8b 超时和状态指标 | `internal/server/http.go` 将 `context.DeadlineExceeded` 映射为 `504`；扩展 `Stats` 按状态码和错误原因计数，区分超时与其他 5xx | `fix(http): report model timeout as 504` | HTTP 单测断言超时为 504；`/metrics` 输出 `status_5xx`、`timeouts`；非超时 5xx 可单独计算 |
| 5.8c 发布回滚 | 生产 Compose 文件不含 `build:`，只保留 `image: ${APP_IMAGE}`；`make image VERSION=x` 构建不可变标签，`make prod-up VERSION=x` 启动，`make prod-rollback VERSION=x` 切换上一版本 | `chore(release): make image versions rollbackable` | 连续构建两个版本；切换标签后 `docker compose ... up -d --no-build`；上一版本健康检查通过 |
| 5.9a 执行记录清理 | 在 `internal/server/service.go` 增加可取消的定时清理 goroutine，复用 `RetentionDays`，停机时等待退出 | `feat(execution): schedule retention cleanup` | fake store 单测确认周期调用；关闭服务后 goroutine 退出；长期运行不会只在启动时清理 |
| 5.9b 全量备份 | Windows 脚本和新增 Linux 脚本覆盖 MySQL dump、3 个 Milvus 依赖卷及 `app-data`；manifest 包含 SHA256、非空业务数据判据和恢复演练入口；备份复制到不同存储位置 | `feat(ops): back up app data and verify contents` | Windows 执行 `make backup`/`make restore-check`；Linux 执行 `make backup-linux`/`make restore-check-linux`；故意空 dump 时命令失败 |
| 5.9c 日志与通知 | Compose 增加日志大小/轮转配置；`/metrics` 支持受保护 bearer token；新增 `scripts/health-alert.sh`，探测 readiness/metrics 并通过可选 webhook 通知 | `chore(ops): add log rotation and alerts` | `docker compose -f docker-compose.prod.yml --env-file .env.prod config` 检查 logging；无 bearer 不能抓指标；探测失败返回非零并在配置 webhook 时发送通知 |
| 5.10a 门槛出口 | `internal/evaluation/evaluation.go` / `cmd/eval/main.go` 输出关键路由召回、技能加载证据、工具尝试/成功率；`internal/server/http.go`/`service.go` 的受保护 `/metrics` 输出 `status_5xx`、`timeouts`、`non_timeout_5xx_rate` | `feat(eval): export launch gate metrics` | `make eval` JSON 分列技能与工具指标；带 bearer 抓取指标；固定失败样本验证分母和分子可失败。旧 `tool_evidence_rate` 若把 `skill_preloaded` 算作工具证据，必须按 5.7e 修正后重新评测 |
| 5.10b 灰度验收 | 使用已存在的 `docs/RUNBOOK.md` 14.23 灰度章节，填写 3–10 名用户、3–7 天窗口、每日检查和停止条件；不新增第二份方案或独立检查清单 | `docs(launch): record internal canary result`（仅在记录尚未填写时提交） | 连续 3 天指标达到门槛；负责人签字记录；完成 5.5b 前置检查 |

**门槛计算口径**：关键技能召回率只统计确定性路由用例；模型自主技能选择另报诊断值。技能正文证据（`skill_preloaded` / `skill_loaded`）与工具证据必须分开。工具尝试只认 `tool_started`，成功只认 `tool_completed`，失败只认 `tool_failed`；技能预加载不计入工具率，模型自述不计入任何工具证据。工具成功率分母只包括用例明确声明需要成功调用工具的题。知识预检索不是独立事件类型，而是标准工具事件的 `tool_name=knowledge_search` 且 `source=server_preflight` 标记；检查清单第 11 项使用同一组标准事件名。非超时 5xx 由受保护 `/metrics` 统计 HTTP 500–599 中排除 `context deadline exceeded` 的部分。各出口都必须直接产出指标，不能依靠人工从日志估算。

**最终上线顺序**：5.5a 一次性 bootstrap → 5.6a–c → 5.7a–f → 5.8a–c → 5.9a–c → 5.10a–b → 总验收清单 → 5.5b 配置切换 → 停止开发 Compose → 用 `docker-compose.prod.yml` 从零启动生产栈 → 3–7 天内部灰度。任一子步骤失败都不得进入下一组。

### 当前执行状态（2026-09-19）

| 范围 | 状态 | 实测结果或剩余动作 |
|---|---|---|
| 0.1–4.2 | ✅ | 历史提交已落地；`make check`、MySQL 集成测试和真实 `make eval` 已通过 |
| 5.1–5.4 | ✅ | SIGTERM、限流、备份/恢复、liveness/readiness 已实现并通过本机验证 |
| 5.5a | ✅ | 独立生产卷完成迁移和管理员建号；维护 CLI 已包含在生产镜像内 |
| 5.6a–c | ✅/⏳ | 独立生产 Compose、OpenResty 模板和回调限流已完成；真实 1Panel 证书、域名和 `nginx -t` 需在目标服务器执行 |
| 5.7a–c | ✅ | 知识服务端预检索、报告/文档确定性预加载、能力边界和现有评测已落地 |
| 5.7d–e | ✅ | 路由反例已收紧；技能、工具尝试/成功、来源和禁止工具调用分开统计并进入门禁 |
| 5.7f | 🟡 | 40 题本机真实评测通过；严格的同题集、同配置修改前基线缺失，不能与 20 题旧结果作差值比较 |
| 5.8a–c | ✅ | 队列上限、504、指标拆分和不可变标签 A/B 切换回滚演练通过 |
| 5.9a–c | ✅/⏳ | Windows 备份恢复、日志轮转、受保护指标和告警脚本已验证；Linux 目标机异地复制需执行 |
| 5.10a | ✅ | 新门禁区分技能/工具证据并检查禁止工具调用；40 题 `make eval` 达标，详细结果见 `RUNBOOK.md` 14.27 |
| 5.10b、5.5b | ⏳ | 需在真实 1Panel/HTTPS/飞书回调环境完成 3–7 天内部灰度；完成前不宣称正式上线 |
| 6.1–6.3 | ✅ | 五类意图、服务端分流、只读注册表/工具和来源评测已实现；注册表保持空白，未发现可接入的业务数据源 |
| 6.4–6.5 | N/A | 当前仓库没有内部业务 DB/API connector 或确认的业务对象；不新增虚构数据源，也不产生连接器提交 |
| 6.6–6.8 | 🟡 | owner 隔离附件生命周期、OCR/表格/音频/视频解析接口与默认关闭的前端入口已实现；真实 ClamAV 病毒库、多模态 provider 和样本集验收须在目标环境完成 |
| 6.9 | N/A | 当前没有需写入的内部业务系统；现有 `write_note` 仍走原审批边界，不代表新增业务执行连接器 |
| 6.10 | ✅/⏳ | `make eval-intent` 离线 57 题通过（意图/来源准确率 100%，普通问题误触发率 0%，含附件 ID 正反例）；完整 `make eval` 已在云端与本地模型环境实际运行，但模型相关答案/工具证据门禁未全部通过；3–7 天目标环境灰度及真实 OCR 成功率/来源覆盖记录待执行 |

---

## 八、阶段 6：可选业务数据连接、多模态预处理与五类意图路由

### 阶段目标

阶段 5 解决的是“系统能安全上线”；阶段 6 解决的是“上线后回答得更像企业助手”。本阶段不改变已经验证的文档知识库 → 项目长期记忆 → 通用模型链路，而是在它前面增加明确的问题分类，并为文件理解及**未来确有需要时的内部动态数据**提供各自的证据来源。当前项目没有可核实的业务数据源或内部写操作；只保留空的只读注册表和明确的“不适用”边界，不能以示例数据冒充已接入。

```text
用户请求
  → 服务端五类意图判定
      ├─ general            → 通用模型
      ├─ project_fact       → 文档知识库 → 项目长期记忆 → 通用模型边界
      ├─ realtime_data      → 注册的只读业务查询/API → 时间戳与来源
      ├─ file_understanding → attachment_id → 解析/OCR/视觉 → 文件证据
      └─ execution          → allowlist 工具 → 审批/幂等/审计 → 执行结果
```

阶段 6 的核心原则：

1. **分类先于检索**。普通问题不经过知识库和长期记忆；实时问题不使用过期记忆冒充当前值。
2. **可选动态数据查询不是向量检索**。如果未来需要查询项目资产、流程状态或其他内部动态字段，必须从只读数据库或企业 API 查询，不能把每次结果写入 Milvus 再检索。
3. **文件理解不是自动入库**。用户上传的文件默认只服务当前请求；只有用户或管理员明确执行“加入项目知识库”并通过权限检查后，才进入文档索引流程。
4. **模型只能选择已注册能力**。模型不能生成任意 SQL、URL、shell 命令、文件路径或执行参数。
5. **所有新增能力可关闭、可审计、可回滚**。阶段 6 的新增入口默认关闭，按意图和数据源逐项灰度。

### 步骤 6.1　定义五类意图契约和证据优先级

**改什么**：把当前 `internal/routing/routing.go` 的关键词布尔判断扩展为固定枚举的 `IntentPlan`，保留现有 `Decision` 兼容包装，避免一次改动所有调用方。

**建议类型**：

```go
type IntentKind string

const (
    IntentGeneral          IntentKind = "general"
    IntentProjectFact      IntentKind = "project_fact"
    IntentRealtimeData     IntentKind = "realtime_data"
    IntentFileUnderstanding IntentKind = "file_understanding"
    IntentExecution        IntentKind = "execution"
)

type IntentPlan struct {
    Kind              IntentKind
    Confidence        float64
    ReasonCode        string
    RequiredSources   []string
    AttachmentIDs     []string
    Operation         string
    RequiresApproval  bool
    FreshnessRequired bool
    NeedsClarification bool
}
```

判定优先级固定为：显式执行动作和安全规则 → 明确附件/文件请求 → 实时数据信号 → 项目内部事实信号 → 通用问题。无法从请求确定对象、时间范围、附件或操作名称时，返回 `NeedsClarification=true`，不能猜测。

第一版采用“确定性规则优先 + 结构化分类器兜底”：

- 确定性规则处理安全、上传文件、已注册业务操作、明显的项目事实词；
- 只有规则无法区分时，才调用低成本分类器；分类器只能输出 JSON 枚举，解析失败按 `general` 或要求澄清处理；
- 分类器不得直接决定 SQL、URL、owner、文件路径或审批结果；
- 分类结果写入 `intent_classified` 事件，payload 只保留类别、置信度和规则码，不保存敏感原文。

**受影响文件**：`internal/routing/intent.go`（新增）、`internal/routing/routing.go`、`internal/routing/routing_test.go`、`internal/execution/event.go`、对应事件测试。

**提交边界**：`feat(routing): define typed intent plans`。

**验收**：至少 50 个固定样本覆盖五类意图、歧义和越权表达；规则结果稳定；分类器返回非法枚举、额外字段或低置信度时不会进入业务查询或执行工具；`make check` 通过。

### 步骤 6.2　把五类意图接入服务端编排器

**改什么**：在 `internal/server/service.go` 的 `ChatWithSink` 中，把当前“先判断是否知识问题”的路径改为统一 `IntentPlan` 编排。每类意图只能进入对应分支：

| 意图 | 首选证据 | 无证据时的行为 | 禁止行为 |
|---|---|---|---|
| `general` | 通用模型 | 直接回答通用知识 | 无理由调用知识库、长期记忆或业务查询 |
| `project_fact` | 文档知识库 → 项目长期记忆 | 允许通用模型，但必须标明不是项目事实结论 | 用用户偏好或过期业务数据冒充项目事实 |
| `realtime_data` | 已配置的只读内部 DB/API 查询 | 明确说明没有配置数据源、数据过期或无权限 | 回退到长期记忆回答当前状态 |
| `file_understanding` | 指定 `attachment_id` 的解析产物 | 返回解析失败、文件不支持或需补充附件 | 扫描整个工作区或猜测文件 |
| `execution` | allowlist 工具执行 | 需要澄清、审批或报告失败 | 自由生成命令、SQL、URL 或写操作 |

普通对话不再被 `knowledge_search` 拖慢；项目事实仍维持已经实现的文档 → 记忆 → 通用模型顺序；实时数据和文件理解不共享项目事实的兜底逻辑。

**受影响文件**：`internal/server/service.go`、`internal/server/http.go`、`internal/server/ws.go`、`internal/eino/agent/agent.go`、`internal/eino/prompt/prompt.go`。

**提交边界**：`feat(server): route chat by typed intent`。

**验收**：普通问题没有 `knowledge_search`/memory 事件；项目问题顺序正确；实时问题不会调用项目记忆作为当前值；未提供附件的文件问题要求补充；执行问题没有审批前不会产生写操作。

### 步骤 6.3　建立结构化数据查询注册表

**改什么**：新增 `internal/dataquery`，将业务数据查询从 Agent 工具和会话存储中隔离出来。模型只接触一个受限的 `structured_query` 工具，其输入是注册的 `source`、`operation` 和经过 schema 校验的参数。

查询注册表必须包含：

- `source`：业务库或 API 名称；
- `operation`：固定操作 ID，例如 `project_assets.summary`、`workflow_item.by_id`；具体名称必须来自项目实际业务，不预设电商对象；
- 参数类型、必填项、最大长度和允许枚举；
- 返回字段白名单和脱敏规则；
- owner/tenant 过滤方式；
- 超时、最大行数、最大响应字节数和 `freshness_ttl`；
- 是否只读、是否需要审批、是否允许批量查询。

业务查询不能接收模型生成的原始 SQL 或完整 URL。SQL 使用代码或受审查的模板注册，参数使用 `database/sql` 的占位符；HTTP 使用固定 base URL、固定 path、固定 method，禁止根据用户输入拼接目标主机。配置只保存连接名和环境变量引用，不把口令写入仓库。

**建议接口**：

```go
type QueryRequest struct {
    Source    string
    Operation string
    Arguments map[string]any
    OwnerID   string
}

type QueryResult struct {
    Rows       []map[string]any
    Source     string
    Operation  string
    AsOf       time.Time
    FreshUntil time.Time
    Redactions []string
}
```

**受影响文件**：`internal/dataquery/types.go`、`registry.go`、`mysql.go`、`http.go`、`internal/config/config.go`、`config.yaml`、`config.docker.yaml`、`config.prod.yaml`、`internal/eino/tool/structured_query.go`。

**提交边界**：`feat(data): add allowlisted structured query registry`。

**验收**：单测覆盖非法 source、非法 operation、缺少参数、超长参数、owner 不匹配、超过行数和超时；任意 SQL/URL 输入都被拒绝；真实测试库使用只读账号；`go test -p 1 ./internal/dataquery/... ./internal/eino/tool/...` 通过。

### 步骤 6.4　接入第一个实时业务数据源

**改什么**：如果项目确实存在需要实时查询的内部系统，只接入一个高价值、低风险的业务对象作为样板，例如项目资产、流程节点或内部任务状态；对象名称、字段和来源必须先由项目负责人确认。当前没有此类需求时，本步骤标记为“不适用”，不新增连接器。业务数据使用独立只读账号或企业 API token，不复用会话 MySQL 的写账号。

若确认存在实际数据源，推荐先实现两个操作：

1. `by_id`：按明确 ID 查询单条记录；
2. `summary`：按明确时间范围返回聚合结果。

每个结果必须附带 `source`、`operation`、`as_of`、`fresh_until` 和查询范围。业务 API 的响应要先按 schema 解码，再交给模型；未知字段、错误码和空结果都要结构化表示。

**受影响文件**：`internal/dataquery/mysql.go` 或 `http.go`、`internal/server/service.go`、`internal/eino/tool/structured_query.go`、`internal/execution/event.go`、集成测试和 `.env.example`。

**提交边界**：`feat(data): add first realtime business connector`。

**验收**：mock API/测试数据库能返回正确结果和时间戳；网络超时、401/403、空结果、旧数据和上游 5xx 均能区分；回答引用操作 ID 和数据时间；业务查询失败时不会退回长期记忆猜测当前状态。

### 步骤 6.5　（条件步骤）完成动态数据的新鲜度和权限控制

**改什么**：只有在 6.4 确认了真实内部数据源后执行。把 `freshness_ttl` 变成强约束，而不是提示词建议。`fresh_until` 之后查询结果只能标记为过期并重新拉取；重新拉取失败时返回“当前数据不可用/已过期”，不能继续使用旧结果。长期记忆只可以补充字段含义、项目背景或历史决策。

每次查询必须执行：

1. 从认证上下文取得 owner/tenant；
2. 由服务端注入 owner 条件，模型不能覆盖；
3. 校验参数范围与时间窗口；
4. 设置独立超时和最大返回量；
5. 记录 `data_query_started/completed/failed` 事件，payload 脱敏；
6. 在回答中显示数据时间，必要时提示用户刷新。

**受影响文件**：`internal/dataquery/`、`internal/auth/`、`internal/server/service.go`、`internal/server/http.go`、`internal/execution/event.go`、`internal/evaluation/evaluation.go`。

**提交边界**：`fix(data): enforce freshness and owner isolation`。

**验收**：跨 owner 查询返回 403 或空结果；过期数据不能进入最终上下文；查询事件不含 token、密码或完整 SQL；同一问题重复查询按 TTL 工作；数据 P95 延迟和错误率可从 `/metrics` 区分。若 6.4 标记为不适用，本步骤不产生代码提交。

### 步骤 6.6　建立附件和多模态预处理基础设施

**改什么**：新增显式附件流程，不让模型直接读取任意本地路径。新增 `attachments` 和 `attachment_artifacts` 数据结构，保存 owner、原始文件 hash、MIME、大小、存储 key、处理状态、错误原因、保留期限和解析器版本。

建议状态：`uploaded` → `processing` → `ready` / `failed` / `expired`。文件原件保存到受控 `app-data` 或对象存储；数据库只保存元数据和索引，不把大文件塞进 MySQL。聊天请求新增 `attachment_ids`，服务端校验 owner 和状态后才允许进入 `file_understanding` 分支。

第一批限制：单文件大小、总附件数、总解析页数和总处理时长都配置化；文件名只作为不可信展示文本；压缩包默认拒绝，防止路径穿越和解压炸弹；杀毒/扩展名与 MIME 双重校验。

**受影响文件**：`internal/attachments/`（新增）、`internal/session/migrations/`、`internal/config/config.go`、`internal/server/http.go`、`internal/server/ws.go`、`internal/server/service.go`、`docker-compose.prod.yml`、`scripts/backup.sh`、`scripts/backup.ps1`。

**提交边界**：`feat(files): add owner-scoped attachment lifecycle`。

**验收**：上传、状态查询、失败重试、过期清理和删除均按 owner 隔离；越权 attachment ID 返回 404/403；原文件进入备份范围；未明确指定 attachment 时不会扫描工作区；`make check` 通过。

### 步骤 6.7　接入图片与扫描 PDF 的 OCR/视觉解析

**改什么**：新增 `internal/multimodal` 的 extractor 接口，第一批只实现 PNG/JPEG 和扫描 PDF。对可提取文本的 PDF 继续走现有文本解析；页面无文本或文本质量不足时再渲染页面并执行 OCR。需要理解图表、印章或布局时，调用独立视觉模型并保存模型/版本信息。

解析产物至少包含：

- `source_ref`：文件名、页码、图片区域或坐标；
- `text`：OCR/文本提取结果；
- `blocks`：标题、段落、表格、图片说明等结构；
- `ocr_confidence` 和 `extractor_version`；
- `created_at`、`content_hash` 和权限范围。

解析产物可在用户明确选择“加入项目知识库”后进入 `my_eino_knowledge` 的文档索引，并保留 `attachment_id/page/block` 元数据；默认只作为本次文件问题的上下文。不得把原图、OCR 全文或视觉模型猜测自动写入 `my_eino_memory`。

**受影响文件**：`internal/multimodal/`（新增）、`internal/eino/rag/documents.go`、`internal/eino/rag/parsers.go`、`internal/eino/rag/manifest.go`、`internal/eino/tool/local_file.go`、`internal/eino/agent/agent.go`、`internal/server/service.go`。

**提交边界**：`feat(multimodal): parse images and scanned pdfs with provenance`。

**验收**：文本 PDF 不重复 OCR；扫描 PDF 能返回页码和 OCR 置信度；图片问答能引用文件/页码/区域；OCR 失败、视觉模型不可用或低置信度时明确告知；恶意文件、超限文件和不支持 MIME 被拒绝；现有文档知识库索引无回归。

### 步骤 6.8　第二批文件能力：表格、音频和视频（条件步骤）

本步骤只有在 6.6–6.7 的附件隔离、来源和删除流程稳定后才开始。它是第二批能力，不得提前在产品文案中宣称已完成。

- **表格**：使用结构化解析器读取工作表、列名、公式和类型；简单聚合优先在受限表格引擎中执行，向量索引只保存摘要和字段说明；回答必须指出工作表、单元格范围或计算口径。
- **音频**：先语音转文字，保存说话人（若可用）、时间戳和转写置信度；检索返回时间段，不把转写猜测写成事实。
- **视频**：拆出字幕、音频转写和按间隔抽取的关键帧；查询返回时间戳和帧引用，限制最大时长与帧数。

**受影响文件**：`internal/multimodal/`、`internal/attachments/`、`internal/eino/rag/`、对应 provider 配置、前端附件展示和评测夹具。

**提交边界**：按模态分别提交，例如 `feat(multimodal): add spreadsheet extraction`、`feat(multimodal): add audio transcription`；不把三种模态合并成一个不可回滚提交。

**验收**：每种模态有真实样本、失败样本和越权样本；来源包含页/表/单元格/时间戳；解析失败不会调用通用模型编造内容；处理耗时、队列长度和存储量有指标。

### 步骤 6.9　（条件步骤）执行类意图和内部系统操作安全收口

**改什么**：只有项目确实需要修改内部系统时执行。把“查询动态数据”和“修改内部状态”彻底分开。执行工具每个操作必须有固定 ID、参数 schema、权限要求、是否需要审批、幂等键生成规则、超时和补偿说明。沿用现有 `write_note` 的审批中断机制，但不能把任意内部 API 直接暴露给模型。

执行流程：解析操作 → 校验 owner/权限 → 生成 dry-run 摘要 → 用户审批 → 带幂等键执行 → 记录结果和外部 request ID。审批前可以读取信息和生成预览，但不能产生写入副作用。重复提交同一幂等键必须返回同一执行结果或明确处理中状态。

**受影响文件**：`internal/eino/tool/`、`internal/server/service.go`、`internal/auth/`、`internal/checkpoint/`、`internal/execution/event.go`、前端审批组件、`docs/API.md`。

**提交边界**：`feat(execution): add allowlisted business operations`。

**验收**：未授权用户、越权 owner、缺少审批和重复请求均不能产生写操作；dry-run 与实际执行事件可区分；失败重试不重复修改内部状态；审计记录包含操作 ID、执行人、审批人、幂等键和结果摘要，不包含密钥。若项目没有内部写操作需求，本步骤不适用。

### 步骤 6.10　评测、指标和分阶段灰度

**改什么**：扩展 `internal/integration/eval_cases.json` 和 `cmd/eval`，让评测不只看答案，还检查“是否选对来源、是否避免不必要查询、是否返回新鲜度和文件来源”。新增指标：

| 指标 | 阶段 6 门槛 |
|---|---:|
| 五类意图准确率 | ≥ 95%（至少 50 个固定样本） |
| 普通问题误触发知识库/记忆/业务查询率 | ≤ 1% |
| 实时数据过期结果进入最终回答 | 0 |
| 未注册 SQL/URL/操作被执行 | 0 |
| 业务查询 owner 越权成功率 | 0 |
| 文件来源可追溯率 | ≥ 95% |
| 图片/扫描 PDF 解析成功率 | ≥ 95%（有效样本） |
| 执行操作审批绕过率 | 0 |

灰度顺序固定为：

1. 只启用 `general` 与现有 `project_fact` 新路由，确认普通问题不再触发无关检索；
2. 若项目存在真实内部数据源，再只开放一个只读连接器给内部用户；
3. 再开放图片/扫描 PDF 附件，限制用户、大小和并发；
4. 通过 3–7 天指标后，按实际需求逐项开放表格、音频、视频或内部系统执行操作；
5. 任一安全指标为非零，立即关闭对应 feature flag，不回滚已验证的阶段 5 主链路。

**受影响文件**：`internal/evaluation/evaluation.go`、`cmd/eval/main.go`、`internal/integration/eval_cases.json`、`internal/server/service.go`、`internal/server/http.go`、`docs/API.md`、`docs/RUNBOOK.md`。

**提交边界**：`test(product): add intent data and multimodal launch gates`。

**验收**：`make check`、结构化数据集成测试、附件/解析测试、权限交叉测试和 `make eval` 全部通过；生产灰度记录包含意图分布、查询延迟、解析失败、存储增长、来源覆盖和停止条件；每项新增能力可独立关闭。

### 阶段 6 的数据存储边界

| 数据 | 保存位置 | 是否进入现有文档知识库 | 是否进入长期记忆 |
|---|---|---|---|
| 项目文档 | `my_eino_knowledge` | 是 | 否 |
| 项目长期事实/决策 | MySQL Memory 表 + `my_eino_memory_v1` | 否 | 是，按既有规则 |
| 实时业务数据查询结果 | 业务 DB/API；回答只保存必要审计摘要 | 否 | 否，除非用户明确要求记录决策 |
| 用户上传原文件 | `app-data`/对象存储 | 默认否 | 否 |
| 文件解析文本/页块 | `attachment_artifacts`，用户明确入库后才索引 | 可选 | 否 |
| 执行结果与审计 | MySQL `execution_*`/业务审计表 | 否 | 否 |

**阶段 6 的完成定义**：适用步骤通过代码、固定样本和权限验收；条件步骤在缺少真实系统时明确记为 N/A；文件理解必须有真实解析证据和来源。当前阶段 6 的代码/固定评测已落地，但 6.6–6.8 仍是默认关闭的候选能力：完成目标环境病毒库、provider/样本验收及 3–7 天灰度前，不得称为生产支持，也不能写成“已支持所有 PDF、表格、音视频和业务系统操作”。

---

## 九、明确不做的事

| 项 | 出处 | 不做理由 |
|---|---|---|
| 切断 `internal/eino/` 的反向依赖 | REFACTOR-PLAN 阶段 4 | 其中 `agent → execution` 是 25 行跨 4 文件，需引入事件接口 + 适配器，**风险高于收益**。其余各项收益低 |
| S8 提示词「可校验输出」 | P-SKILL-QUALITY S8 | 额外消耗 token，校验价值有限（D4） |
| 中文友好展示名配置 | P-SKILL-QUALITY 决策 5 | 现由 `app.js:928` 的 `replaceAll` 承担，够用（D5） |
| 多副本部署 | — | 登录态（D11）与限流（步骤 5.2）都按**单副本**设计。要多副本需换 Redis 会话 + Redis 限流，另立项 |
| `file` 模式的多用户隔离 | AUTH-PLAN 坑 5 | `auth.enabled` 时强制 `session.store=mysql`，不额外支持 file 模式的分层 |
| token 预算 | 步骤 5.2 | 需解析模型响应计量，复杂度与收益不匹配，登记为后续项 |
| 给所有技能补「假」`required_tools` | 坑 A3 | 空声明与「声明了但缺失」语义完全不同，为「好看」全填会导致能力状态全部是缺失 |
| **自助注册入口** | D12 | 内部场景下准入权必须留在管理员手里。开放注册会把准入权交给网络层（访客网络、外包、离职人员、内网任何一台被拿下的机器），而项目本已有飞书作身份源，能做到「只让在职同事进」 |
| **本地账号与飞书账号绑定合并** | D14 | 需要邮箱 / 手机比对与验证流程，复杂度与当前需求不匹配。两套身份独立反而让隔离逻辑更简单（`owner` 前缀天然区分） |
| **密码自助找回** | D12 | 内网自建无邮件服务，无法做验证。忘记密码由管理员 `cmd/user-admin -passwd` 重置 |
| **管理端建号 API / 页面** | D15 | 建号不经 HTTP，零暴露面，且天然解决「第一个管理员从哪来」的引导问题 |
| **本地账号的邮箱 / 手机号字段** | 步骤 2.10 | 不做自助找回，不收集用不上的个人信息 |

---

## 十、必须处理的坑（合并去重 · 统一编号）

### 技能侧

| # | 坑 | 后果 | 应对 |
|---|---|---|---|
| A1 | **`description` 与同块 `not_for` 自相矛盾** | 模型多选或全选，路由不稳的**根本原因** | 步骤 1.2/1.3/1.4 **必须同批完成** |
| A2 | **未接入工具名一旦写进技能即成契约** | 将来接入时必须同名，否则技能永远判「缺失」 | 用步骤 1.2 表格的名字，**不加前缀**（D1） |
| A3 | **空 `required_tools` 与「声明了但缺失」语义完全不同** | 为「好看」全填会导致能力状态全部是缺失 | 如实声明：已接入写真实名，未接入写规划名 |
| A4 | **把事件开关与展示开关混为一谈** | 以为部署态留痕了，实际什么都没存 | 步骤 1.5 只动 `execution_events`，**不动 `debug`** |
| A5 | **改完 `SKILL.md` 忘了模型侧缓存** | 每轮扫描 `skills/` 会重新读取（`runtime.go:21`），无需重启 | 无需处理，但要知道「改完立即生效」意味着**改错了也立即生效** |
| A6 | **本地 `max_completion_tokens` 一次调到 4096** | 本地 9B 单请求可占用数分钟，并发只有 2，直接拖垮本机 | 本地封顶 `1024`；要长输出就把 `config.yaml` 切到方舟配置 |
| A7 | **给 `name` 开第二套语义** | 技能身份出现两个来源，`load_skills` 校验与路径穿越防护要跟着改 | 步骤 1.1 直接删除，目录名是唯一事实来源 |

### 鉴权侧

| # | 坑 | 后果 | 位置 |
|---|---|---|---|
| B1 | **worker claim job 按单 owner 过滤** | 多用户下**其他用户的记忆静默失效**（最致命，无报错） | `memory/mysql.go:163/168`、`worker.go:257-540` |
| B2 | **`ALTER TABLE` 在现有迁移机制下不幂等** | 第二次迁移报 `Duplicate column name` | `session/mysql.go:113-138` |
| B3 | **只做查询过滤 ≠ 归属校验** | `Load` / `Delete` / `approval` 是「知道 id 就能操作」，必须显式比对 owner | 步骤 2.5 / 2.7 / 2.8 |
| B4 | **飞书 v2 token 端点已弃用** | 照老教程写直接失败；授权码 5 分钟一次性 | `internal/auth/feishu.go` |
| B5 | **`file` 模式没有 owner 维度** | `SESSION_STORE=file` 时无法隔离 | `auth.enabled` 时**强制 mysql** |
| B6 | **session id 允许客户端指定** | 越权的根源 | `http.go:85`、`ws.go:116/159`；解法见步骤 2.7 |
| B7 | **用户名含 `:` 会撞 owner 命名空间** | 有人用 `feishu:xxx` 当用户名，即与某个飞书身份同名 → 直接读写对方的数据 | 步骤 2.12 的 `^[a-zA-Z0-9_]{3,32}$` 格式校验必须先于查库 |
| B8 | **登录接口不限期 = 口令爆破入口** | 用户名可枚举（内部系统就是人名拼音），错误口令可无限次尝试 | 步骤 2.13 按 **IP + 用户名** 双维度限流；只按 IP 会漏内网多机，只按用户名会漏批量撞库 |
| B9 | **自有账号口令用 sha256 存** | 用户自选口令强度不可控 + 单轮哈希 → 库一旦泄露即可秒破 | 步骤 2.11 用 `crypto/pbkdf2`（**Go 标准库自带，零新增依赖**），210000 轮 + 随机盐 |
| B10 | **打开 `auth.enabled` 却没建任何账号** | 飞书一旦故障，系统**彻底没有入口** —— 而本地账号的意义正在于此 | 步骤 5.5a 的前置动作：先 `cmd/user-admin` 建管理员账号，再由 5.5b 开 auth |

### 收敛侧

| # | 坑 | 后果 | 应对 |
|---|---|---|---|
| C1 | **`go test` 不能用默认并行度** | 本机内存不足导致编译失败 | 一律 `go test -p 1 ./...` |
| C2 | **测试文件的 import 容易漏改** | `go build` 全绿，`go test` 挂 | 步骤 3.1 的 29 行清单逐个核对（3 行在测试文件） |
| C3 | **路径前缀二次替换** | `internal/eino/eino/model` | 匹配引号内完整串，或先替换长路径 |
| C4 | **`internal/eino` 是普通包名 `eino`** | 若某文件同时需要 `github.com/cloudwego/eino` 根包与门面包，需起别名 | 现状**无任何文件导入 cloudwego/eino 根包**，暂无冲突；步骤 3.3.3 引入门面包时留意 |
| C5 | **别名约定必须保住** | 丢掉 `modelset` / `toolset` 会与 `einomodel` / `einotool` 混在一起 | 步骤 3.1 只改路径，**不动别名** |
| C6 | **别顺手改 eino API 写法** | 一旦混入 API 调整，就无法用「行为零变化」验收 | 步骤 3.1 严格限定为路径移动 |

---

## 十一、总验收清单

### 阶段级

| 阶段 | 自动检查 | 人工断言 |
|---|---|---|
| 0 | `git log` 有提交、`git status` 干净 | 记录基线测试通过数 |
| 1 | `grep -rn "^name:\|^metadata:" skills/` 无输出；`go test ./internal/skill/...`；`make check` | 能力状态与步骤 1.2 表格一致；Excel 场景不承诺产出文件；Word 工单只推荐 documents |
| 2 | `make db-migrate` 连跑两次；`go test -p 1 ./...` | A/B 加 L1/L2 **四种身份**交叉测试全项通过（见步骤 2.15） |
| 3 | `make boundary`；`go test -p 1 ./...`（通过数与基线一致） | — |
| 4 | `make eval` 输出含 `routing_accuracy` | 易混场景判定正确 |
| 5 | `go test -p 1 ./...`；`make backup` + `restore-check`；`docker compose -f docker-compose.prod.yml config`；代理/回调/队列/504/指标/清理测试；`make eval` 输出上线门槛 | 仅 HTTPS 入口可达；`/health` 与 `/health/ready` 分离；文档无命中先查项目记忆，双无命中实际调用模型并标明通用回答；空壳技能不承诺文件能力；可切换上一版本；备份、告警、灰度记录全部通过 |

### 上线前最终检查

- [ ] `config.docker.yaml`：`auth.enabled: true`、`auth.local.enabled: true`、`identity_mode: multi_user`、`debug: false`、`execution_events.enabled: true`
- [ ] **已用 `cmd/user-admin -create -admin` 建好管理员本地账号** —— 必须在打开 `auth.enabled` 之前完成（坑 B10）
- [ ] `FEISHU_APP_ID` / `FEISHU_APP_SECRET` / `FEISHU_REDIRECT_URL` 通过环境变量注入，**不在仓库中**
- [ ] `REDIRECT_URL` 与飞书开发者后台登记的**完全一致**（含端口与路径）
- [ ] 第一阶段通过 HTTPS 反向代理访问，`CookieSecure: true`；仅开发环境 HTTP 才允许 `false`
- [ ] 历史会话已用 `go run ./cmd/session-migrate -claim-owner=<owner>` 归属（**不要用 `admin_open_id`，该字段已废弃删除**；迁移路径见 RUNBOOK 第 293 行附近）
- [ ] 生产 Compose 已删除不必要的数据库、Milvus、Attu 端口发布；Attu 默认关闭或仅管理网可达
- [ ] 已停止开发 Compose，并用 `docker-compose.prod.yml` 从零启动生产栈；正式启动不依赖 `MYSQL_BIND`、宿主机 MySQL 端口或宿主机 CLI
- [ ] 反向代理已配置 HTTPS、真实客户端 IP、180 秒以上读超时和 WebSocket Upgrade 透传；`/auth/callback` 已限流
- [ ] 生产镜像使用不可变版本标签，上一版本可切换，回滚不依赖重新构建
- [ ] 表格、PDF、PPT 的未接入能力已在产品文案和验收中明确，未承诺生成或编辑文件
- [ ] 私有知识请求实际产生标准 `tool_started`/`tool_completed`（`tool_name=knowledge_search`、`source=server_preflight`）事件并带来源；技能或 embedding 变更后已重跑 `make eval`
- [ ] 并发排队有上限和超时；模型超时返回 `504`，与普通 `500` 分开统计
- [ ] 备份包含 MySQL、Milvus 和 `app-data`；manifest SHA256、恢复行数和非空数据检查均通过，备份不与生产同盘
- [ ] 备份告警、健康告警、模型/工具失败告警和磁盘告警已有通知通道；日志有大小上限和轮转策略
- [ ] `local-owner` 历史数据已迁移；生产 OS 和备份/定时任务方案已明确并在同一环境验证
- [ ] 备份定时任务已生效，`restore-check` 至少跑过一次
- [ ] `make check` 全绿

---

## 附录 A：完整受影响文件清单

### 阶段 1（16 个）

- `skills/` 下全部 **13** 个 `SKILL.md`（步骤 1.1–1.4）
- `config.yaml`、`config.ark.yaml`、`config.docker.yaml`（步骤 1.5–1.6）

### 阶段 2（30 个）

**新增（11）**
- `internal/config/auth.go`
- `internal/auth/feishu.go`、`session.go`、`middleware.go`、`identity.go`
- `internal/auth/password.go`（步骤 2.11，pbkdf2 慢哈希）
- `internal/auth/local.go`（步骤 2.12）
- `internal/server/ratelimit.go`（步骤 2.13；**步骤 5.2 复用同一份**）
- `internal/session/migrations/004_auth.sql`
- `internal/session/migrations/005_local_users.sql`（步骤 2.10）
- `cmd/user-admin/main.go`（步骤 2.11，建号 / 改密 / 停用 / 列表）

**修改（19）**
- `internal/config/config.go`（Auth 段 + `auth.local` 子段 + 环境变量 + **校验逻辑修正**，见步骤 2.10）
- `internal/config/memory.go`（放开 `identity_mode`）
- `internal/session/session.go`（5 个方法签名）
- `internal/session/mysql.go`（迁移幂等 + `IncludeAuth` / `IncludeLocalUsers` + 4 处 SQL）
- `internal/memory/mysql.go`（`For()` + claim 改造）
- `internal/memory/worker.go`（maintenance 去 owner 过滤）
- `internal/memory/reindex.go`（若走 `For()` 则可不改）
- `internal/checkpoint/store.go`（路径分层 + 签名）
- `internal/server/http.go`（**5 条** auth 路由 + `POST /sessions` + 认证包装 + 归属校验）
- `internal/server/service.go`（owner 流转 + 9 处调用点）
- `internal/server/ws.go`（session id 服务端生成）
- `internal/server/memory.go`（路由走 `For(owner)`）
- `cmd/server/main.go`（装配 auth 模块）
- `.env.example`（追加 3 个飞书变量）
- `config.yaml`、`config.ark.yaml`、`config.docker.yaml`（各加 `auth:` 段，含 `auth.local` 子段）
- `internal/server/web/index.html`、`internal/server/web/app.js`（登出 / 用户名与来源 / 401 / **本地账号登录表单** / `startNewChat` 异步）

### 阶段 3（24 个）

**新增（3）**
- `internal/eino/`（37 个 .go 文件由 7 个包整体平移）
- `internal/eino/embedding/embedding.go`（步骤 3.3.1）
- `internal/eino/facade.go`（步骤 3.3.3）

**import 改写（15）**：见步骤 3.1 表格

**步骤 3.3 修改（6，不含上表已列的 `cmd/console/main.go`）**
- `Makefile`（`boundary` 目标 + 并入 `check`，步骤 3.2）
- `internal/eino/rag/factory.go`、`internal/memory/coordinator.go`（步骤 3.3.1）
- `internal/skill/runtime.go`、`internal/memory/extractor.go`、`internal/memory/milvus.go`（步骤 3.3.2 / 3.3.3）

### 阶段 4（3 个）

- `internal/evaluation/evaluation.go`、`cmd/eval/main.go`、`internal/integration/eval_cases.json`

### 阶段 5（10 个基础步骤 + 18 个可执行子步骤）

- `docker-compose.milvus.yml`（步骤 5.5a 的 loopback 运维绑定）
- `Dockerfile`、`cmd/user-admin`、`cmd/session-migrate`（步骤 5.5a 的生产镜像维护 CLI）
- `cmd/server/main.go`（步骤 5.1）
- `internal/server/ratelimit.go`（新增）+ `internal/config/config.go`（新增 `rate_limit` 段）+ 三份配置（步骤 5.2）
- `scripts/backup.ps1`、`scripts/restore-check.ps1`（新增）+ `Makefile`（步骤 5.3）
- `internal/server/http.go`（步骤 5.4）
- `docker-compose.prod.yml`（新增，步骤 5.6a）
- `deploy/openresty/my-eino-app.conf`（新增，步骤 5.6b）
- `internal/server/auth.go`、对应测试（步骤 5.6c）
- `internal/execution/event.go`、对应测试（`SkillPreloaded` 已存在；知识预检索复用标准工具事件）
- `internal/server/web/app.js`（步骤 5.7c 非 debug 事件过滤和预检索来源渲染）
- `internal/server/service.go`、`internal/routing/routing.go`、路由测试和 agent 装配代码（步骤 5.7a、5.7d、5.8a、5.9a）
- `internal/integration/eval_cases.json`、`internal/evaluation/evaluation.go`、`cmd/eval/main.go` 及评测测试（步骤 5.7b、5.7e–f、5.10a；阶段 4 历史改动已落地，新增路由评测按子步骤单独提交）
- `internal/server/http.go`、`internal/server/service.go`、配置校验和测试（步骤 5.8a–b）
- `Dockerfile`、`docker-compose.prod.yml`、`Makefile`（步骤 5.8c）
- `scripts/backup.ps1`、`scripts/restore-check.ps1`、`scripts/backup.sh`、`scripts/restore-check.sh`、`scripts/health-alert.sh`、Compose 日志配置（步骤 5.9b–c）
- `docs/RUNBOOK.md` 灰度操作章节（步骤 5.10b）

### 阶段 6（10 个产品化步骤）

- `internal/routing/intent.go`、`internal/routing/routing.go`、路由测试、`internal/execution/event.go`（步骤 6.1）
- `internal/server/service.go`、`internal/server/http.go`、`internal/server/ws.go`、`internal/eino/agent/agent.go`、`internal/eino/prompt/prompt.go`（步骤 6.2）
- `internal/dataquery/`、`internal/eino/tool/structured_query.go`、配置和只读查询测试（步骤 6.3–6.5）
- `internal/attachments/`、附件迁移、上传/状态/删除 API、生产备份配置（步骤 6.6）
- `internal/multimodal/`、`internal/eino/rag/documents.go`、`internal/eino/rag/parsers.go`、`internal/eino/rag/manifest.go`（步骤 6.7–6.8）
- `internal/eino/tool/`、`internal/checkpoint/`、执行事件、前端审批组件、`docs/API.md`（步骤 6.9）
- `internal/evaluation/evaluation.go`、`cmd/eval/main.go`、`internal/integration/eval_cases.json`、`docs/RUNBOOK.md`（步骤 6.10）

### 关键交集（必须按顺序，否则二次改写）

| 文件 | 阶段 | 约束 |
|---|---|---|
| `internal/evaluation/evaluation.go` | 3（import）+ 4（逻辑） | 步骤 4.1 必须在 3.1 之后 |
| `cmd/eval/main.go` | 3（import）+ 4（逻辑） | 步骤 4.1 必须在 3.1 之后 |
| `internal/server/service.go` | 2（owner + 9 处）+ 3（import）+ 5.7a/5.7c/5.8a/5.9a | 阶段 2 必须在阶段 3 之前；5.7 路由完成后再做队列和清理 |
| `cmd/server/main.go` | 2（装配 auth）+ 3（import）+ 5.1（信号） | 阶段 2 → 3 → 5 |
| `config.yaml` | 1（S5/S6）+ 2（auth 段） | 段落不重叠，但**不要同时提交同一文件** |
| `internal/server/ratelimit.go` | 2（步骤 2.13 创建）+ 5（步骤 5.2 扩展） | 5.2 **只扩展不重建**，否则会出现两套限流实现 |
| `internal/server/http.go` | 2（认证路由）+ 5.4（ready）+ 5.8b（504/状态指标） | 先完成认证，再补 readiness 和状态码指标 |
| `internal/config/config.go` | 2（auth）+ 5.2（rate_limit）+ 5.8a（queue_limit/timeout） | 配置字段一次校验，三份配置同步 |
| `internal/server/auth.go` | 2（登录/回调）+ 5.6c（回调限流） | 5.6c 只扩展现有登录限流，不重写认证流程 |
| `internal/integration/eval_cases.json` | 4（路由评测）+ 5.7b（答案边界） | 统一重写断言，避免旧模型自主路由期望残留 |
| `scripts/backup.ps1` / `scripts/restore-check.ps1` | 5.3（初版）+ 5.9b（app-data 扩展） | 5.9b 只扩展备份范围和判据，保留已验证的 SHA256/恢复流程 |
| `internal/evaluation/evaluation.go` / `cmd/eval/main.go` | 阶段 4 + 5.7b/5.7e–f/5.10a | 阶段 4 已完成；后续仅按独立子步骤拆分技能/工具证据并扩充路由评测，不能把 `skill_preloaded` 算作工具执行，也不计算 HTTP 5xx |
| `internal/routing/routing.go` | 5.7d + 6.1 | 阶段 5 先补确定性技能规则及反例；阶段 6.1 的 `IntentPlan` 必须在此基础上扩展，不得复制或覆盖成第二套餐路由规则 |
| `internal/server/service.go` | 阶段 5.7a/5.7c + 6.2/6.5/6.9 | 先保留已验证的文档→记忆链路，再按 IntentPlan 分支接入实时数据、附件和执行操作；新增能力必须有 feature flag |
| `internal/server/http.go` / `ws.go` | 阶段 2/5 + 6.2/6.6/6.9 | 认证、附件 owner 校验、查询超时和审批路径不能分散实现；HTTP/WS 使用同一服务编排器 |
| `internal/config/config.go` | 阶段 2/5 + 6.3/6.6/6.7 | 数据源、附件、provider 和 feature flag 一次完成配置校验，禁止从用户输入读取连接信息 |
| `internal/evaluation/evaluation.go` / `cmd/eval/main.go` | 阶段 4/5 + 6.10 | 评测指标新增意图、实时数据新鲜度、附件来源和越权断言；不能改变既有上线门槛口径 |

### 阶段 5 生产来源与执行对应

| 本方案步骤 | 具体来源/交付物 |
|---|---|
| 5.6a–c | 本仓库生产 Compose、Nginx 配置、`auth.go` 回调限流及测试 |
| 5.7a–f | 服务端预检索、已接入技能预加载、能力边界、反例路由规则、技能/工具证据拆分和最终评测 |
| 5.8a–c | 并发队列、HTTP 状态码/指标、不可变镜像和回滚命令 |
| 5.9a–c | 执行记录定时清理、全量备份恢复、日志轮转和通知规则 |
| 5.10a–b | `make eval`/`/metrics` 门槛指标、内部灰度记录和停止条件 |

### 阶段 6 产品来源与执行对应

| 本方案步骤 | 具体来源/交付物 |
|---|---|
| 6.1–6.2 | 五类意图枚举、分类器契约、服务端统一编排和 `intent_classified` 事件 |
| 6.3–6.5 | 只读业务查询注册表、数据库/API connector、owner 过滤、数据新鲜度和查询审计 |
| 6.6–6.8 | attachment 生命周期、OCR/视觉解析、文本/页块证据和后续表格/音频/视频适配器 |
| 6.9–6.10 | allowlist 执行操作、审批/幂等、评测门槛、feature flag 和灰度记录 |

---

## 附录 B：与源文档的对应关系

| 本方案 | 源文档编号 |
|---|---|
| 步骤 1.1 | `REFACTOR-PLAN.md` S1 |
| 步骤 1.2 | S2 |
| 步骤 1.3 | S3 |
| 步骤 1.4 | S4 |
| 步骤 1.5 | S5 |
| 步骤 1.6 | S6 |
| 步骤 2.1 | `AUTH-PLAN.md` A |
| 步骤 2.2 | B |
| 步骤 2.3 | C |
| 步骤 2.4 | D |
| 步骤 2.5 | E |
| 步骤 2.6 | F |
| 步骤 2.7 | G |
| 步骤 2.8 | H |
| 步骤 2.9 | I |
| 步骤 2.10–2.14 | **本方案新增**（项目自有账号体系）—— 不在原三份方案内，对应的决策 D12–D15 同样为新定 |
| 步骤 3.1 | `REFACTOR-PLAN.md` 阶段 1 |
| 步骤 3.2 | 阶段 2 |
| 步骤 3.3 | 阶段 3 |
| 步骤 4.1 | S7 |
| 步骤 4.2 | S7 的第 5 点 |
| 步骤 5.1 | `REFACTOR-PLAN.md` 第十九节第 3 条 |
| 步骤 5.2 | 第十九节第 1 条（**本方案补全设计**）；限流实现文件改由步骤 2.13 创建，5.2 只做扩展 |
| 步骤 5.3 | 第十九节第 2 条（**本方案补全设计**） |
| 步骤 5.4 | 从「已达成的现状」与 Dockerfile HEALTHCHECK 推导（**本方案新增**） |
| 步骤 5.5a–b | `AUTH-PLAN.md` A 节的配置常量 + 本方案新增的 bootstrap、历史归属和最后的生产配置切换 |
| 步骤 5.6a–c | **本方案新增**：生产 Compose、HTTPS 反向代理、回调限流和网络隔离 |
| 步骤 5.7a–c | **本方案新增**：服务端知识预检索、已接入技能预加载、能力边界回复、评测断言 |
| 步骤 5.7d–f | **本次补充**：收紧服务端技能路由、分开统计技能加载与真实工具执行、扩充回归集并复评 |
| 步骤 5.8a–c | **本方案新增**：有界并发、504 与指标拆分、不可变镜像回滚 |
| 步骤 5.9a–c | **本方案新增**：执行记录周期清理、完整备份、日志轮转与告警 |
| 步骤 5.10a–b | **本方案新增**：上线门槛度量与内部灰度流程 |
| 步骤 6.1–6.2 | **本方案新增**：五类意图契约与服务端统一编排 |
| 步骤 6.3–6.5 | **本方案新增**：只读结构化业务查询、owner 隔离与数据新鲜度 |
| 步骤 6.6–6.8 | **本方案新增**：附件生命周期、图片/扫描 PDF 解析及后续模态扩展 |
| 步骤 6.9–6.10 | **本方案新增**：allowlist 执行操作、评测门槛与分阶段灰度 |
| 「明确不做」 | `REFACTOR-PLAN.md` 阶段 4、S8、决策 5；`AUTH-PLAN.md` 坑 5 |

---

**文档结束**。执行时如遇本方案未覆盖的情况，以「先不改、先记录」为原则，不要临场扩大改动面。
