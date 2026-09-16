# 执行手册（RUNBOOK · 逐 commit 操作序列）

> **本手册是什么**：`docs/EXECUTION-PLAN.md` 的**执行序列**。它只回答四件事——按什么顺序做、敲哪些命令、什么时候提交与推送、出问题怎么回退。
>
> **本手册不是什么**：不复制内容表。**每一步具体改什么**（字段值、SQL、代码片段、工具名清单）以 `EXECUTION-PLAN.md` 的对应步骤为准。两份文档分工明确，避免内容漂移。
>
> **基线**：`92b2833`（2026-09-16 建立并推送）。**剩余 22 个 commit、6 次推送。**
> **工作目录**：`E:\11\my-eino-app`
> **外部依赖**：全流程只有一处——阶段 2 需要一个**飞书自建应用**（见第一节末）。其余阶段不需要任何项目之外的资源。

---

## 一、环境适配（先读，否则命令会失败）

已实测本机工具链：

| 工具 | 状态 | 影响 |
|---|---|---|
| `git` | ✅ `E:\git\Git\cmd\git.exe` | 正常 |
| `go` | ✅ `go1.26.3 windows/amd64` | 正常 |
| `make` | ✅ `C:\ProgramData\chocolatey\bin\make.exe` | `make check` / `make boundary` 可用 |
| `docker` | ✅ Docker Desktop | 阶段 5 用 |
| `grep` | ❌ **PATH 里没有** | 方案里所有 `grep` 验收命令需换成下方 `Select-String` 写法，或在 Git Bash 里跑 |

**三条铁律**

1. 测试一律 `go test -p 1 ./...`。**`-p 1` 不能省**——本机内存不足，并行编译会失败（`Makefile:38` 有注释）。
2. **一个步骤 = 一个 commit**。只有方案明确要求合并的（`1.2+1.3+1.4`、`1.5+1.6`）才合并提交。
3. `make check` 内部是 `fmt test vet`，其中 `go fmt ./...` **会改写文件**。所以顺序是：**改代码 → 验收 → `make check` → `git add -A` → `git commit`**。

**grep 的等价写法**

```powershell
# Git Bash 里的写法（方案原文）
grep -rn "^name:\|^metadata:" skills/

# PowerShell 等价写法（有输出 = 不合规）
Select-String -Path "skills\*\SKILL.md" -Pattern '^name:|^metadata:'
```

### 外部依赖（只有阶段 2 需要，但建议现在就准备）

全流程只有一处需要项目之外的资源：**飞书自建应用**。阶段 2 的登录建立在飞书 OAuth 之上，没有它，C4–C11 的代码仍能写完，但 **2.10 的验收无法进行**。

「飞书自建应用」= 在飞书开放平台（`open.feishu.cn`）注册、**只在你自己的组织内部使用**的应用。对本项目而言它只是一个**登录服务商**——等价于「用 GitHub 登录」里的 GitHub，不涉及任何业务逻辑。它提供三个值，正好对应步骤 2.1 的三个配置项：

| 配置项 | 飞书开发者后台位置 | 性质 | 进仓库？ |
|---|---|---|---|
| `AppID` | 凭证与基础信息 | 公开标识 | 否，走环境变量 |
| `AppSecret` | 凭证与基础信息 | **密钥** | 否，走环境变量 |
| `RedirectURL` | 开发配置 → 安全设置 | 回调地址 | 否，走环境变量 |

**创建流程（约 10 分钟）**

1. 确认有一个飞书组织。没有就免费建一个（免费版 ¥0，支持 100 人以内）——**自己创建组织即自动是管理员**，第 6 步的审核由你自己点。
2. `open.feishu.cn` → 开发者后台 → **创建企业自建应用**（填名称、描述、图标）。
3. 左侧「凭证与基础信息」→ 复制 **App ID** 与 **App Secret**。
4. 左侧「开发配置 → 安全设置 → 重定向 URL」→ 添加 `http://<服务地址>:18180/auth/callback`。
5. 左侧「开发配置 → 权限管理」→ 开通 **获取用户基本信息**（权限标识 `contact:user.base:readonly`）。
6. 左侧「应用发布 → 版本管理与发布」→ 创建版本（如 `1.0.0`）→ 申请线上发布 → 管理员审核通过。

⚠️ **两个卡点**

- **第 6 步不做，第 4/5 步的配置不生效。** 飞书的回调地址与权限都是「发布后生效」，这是最常踩的一个空。
- **第 4 步的 URL 必须与 `FEISHU_REDIRECT_URL` 完全一致**（含协议、端口、路径，末尾斜杠都不能差），否则授权后报 `redirect_uri` 不匹配。

**2.10 的 A/B 交叉测试需要两个身份**：理想是两个普通飞书账号（两个手机号）。若只有一个账号，可用 `/auth/local` 管理员入口充当第二身份来验证隔离机制，但**管理员有额外权限、不完全等价于普通用户**，严格的越权用例仍需两个普通账号。

---

## 二、提交与推送节奏

**提交**：一个步骤一个 commit（本手册已把 28 步归并为 **22 个 commit**）。

**推送**：**阶段级推送 + `3.1` 单独推**。理由——commit 是给「回退」用的，push 是给「离开这台机器」用的，两者粒度不必一致；阶段内每步都推会产生大量噪声，而阶段边界正好是 `make check` 全绿的天然检查点。`3.1` 要单独推是因为它一次平移 37 个文件，是全程唯一「本地磁盘出事就得重做」的一步。

| 推送 | 时机 | 命令 |
|---|---|---|
| **P0** | 现在（阶段 0 的文档提交尚未推送，见第四节） | `git push` |
| P1 | 阶段 1 验收通过 | `git push` |
| P2 | 阶段 2 验收通过 | `git push` |
| **P2.5** | **C13（3.1）提交后立即** | `git push` |
| P3 | 阶段 3 验收通过 | `git push` |
| P4 | 阶段 4 验收通过 | `git push` |
| P5 | 阶段 5 验收通过 | `git push` |

> 沙箱/自动化环境以下执行 `git push` 会返回 128（拿不到凭据管理器登录态），**推送固定由你在自己的终端执行**。这不是坏事：每个阶段末你会看到一次真实 diff。

---

## 三、总进度表

| Commit | 步骤 | 内容 | 状态 |
|---|---|---|---|
| — | 0.1 | 基线提交 `92b2833` | ✅ 已完成（已推送） |
| — | — | 阶段 0 的两条文档提交（基线记录 + 本手册） | ✅ 已完成（**待推送**） |
| — | — | **P0 推送** | ☐（见第四节） |
| **C1** | 1.1 | 删除死配置 | ☐ |
| **C2** | 1.2+1.3+1.4 | 补能力边界 / 改写主信号 / 补互斥分界 | ☐ |
| **C3** | 1.5+1.6 | 打开事件采集 / 放宽输出上限 | ☐ |
| — | 1.7 | 阶段 1 验收（无 commit）→ **P1** | ☐ |
| **C4** | 2.1 | auth 配置层 | ☐ |
| **C5** | 2.2 | 数据库迁移 | ☐ |
| **C6** | 2.3 | 认证模块 | ☐ |
| **C7** | 2.4 | 路由与中间件 | ☐ |
| **C8** | 2.5 | Session 隔离 | ☐ |
| **C9** | 2.6 | Memory 隔离 | ☐ |
| **C10** | 2.7 | Checkpoint 隔离 | ☐ |
| **C11** | 2.8 | Execution 隔离 | ☐ |
| **C12** | 2.9 | 前端 | ☐ |
| — | 2.10 | 阶段 2 验收 A/B 交叉测试（无 commit）→ **P2** | ☐ |
| **C13** | 3.1 | eino 机械平移 | ☐ → **P2.5** |
| **C14** | 3.2 | 边界守卫 | ☐ |
| **C15** | 3.3 | 消除构造泄漏 | ☐ |
| — | — | 阶段 3 验收 → **P3** | ☐ |
| **C16** | 4.1 | 评测路由维度 | ☐ |
| **C17** | 4.2 | 评测集扩题 | ☐ |
| — | — | 阶段 4 验收 → **P4** | ☐ |
| **C18** | 5.1 | 优雅停机 | ☐ |
| **C19** | 5.2 | 限流与配额 | ☐ |
| **C20** | 5.3 | 备份与恢复演练 | ☐ |
| **C21** | 5.4 | 健康检查分离 | ☐ |
| **C22** | 5.5 | 上线前配置切换 | ☐ |
| — | — | 阶段 5 验收 → **P5** | ☐ |

---

## 四、P0：先把已有的 commit 推上去

本地当前**领先远程若干个 commit**（全部是阶段 0 的文档提交，不含任何代码改动）。执行正式改动前先把它们推上去，避免后面第一次 `git add -A` 时和代码改动混在一起。

```powershell
cd E:\11\my-eino-app
git status                 # 应干净（无输出）
git log --oneline          # 顶部应为文档提交（不含任何代码改动）
git push
git rev-list --left-right --count master...origin/master   # 应为 0  0
```

**验收**
- [ ] `git status` 无输出（工作区干净）
- [ ] `git push` 成功
- [ ] `git rev-list --left-right --count master...origin/master` 输出 `0	0`

---

## 五、阶段 1：技能路由质量（C1–C3）

> **依据**：`EXECUTION-PLAN.md` → 三、阶段 1。
> ⚠️ **C2 必须一次做完 1.2 / 1.3 / 1.4**——`description` 与同块 `not_for` 一旦自相矛盾，模型会多选或全选（坑 A1）。

### C1 · 步骤 1.1　删除死配置

- **改动**：13 个 `skills/*/SKILL.md`，共删 15 行——11 个文件的第 4 行 `name:`，加 2 个文件（`skill-installer`、`skill-creator`）的第 6–7 行 `metadata:` 与 `  short-description:`。
- **不要做的事**：不要「修好它」。让 `name` 生效等于给技能身份开第二个来源，还要同步改 registry key、`load_skills` 参数校验、`validName` 正则与路径穿越防护（坑 A7）。

```powershell
cd E:\11\my-eino-app
# 定位（应列出 13 处：11 个 name + 2 个 metadata）
Select-String -Path "skills\*\SKILL.md" -Pattern '^name:|^metadata:' | Select-Object Path,LineNumber
# 逐个删除上一步列出的行，然后：
go test ./internal/skill/...
make check
git add -A
git commit -m "chore(skills): 删除未被解析的 name / metadata 死配置"
```

**验收**
- [ ] `Select-String -Path "skills\*\SKILL.md" -Pattern '^name:|^metadata:'` **无输出**
- [ ] `go test ./internal/skill/...` 全绿

**回滚**：`git checkout -- skills/`

---

### C2 · 步骤 1.2 + 1.3 + 1.4　补能力边界 / 改写主信号 / 补互斥分界

- **依据**：`EXECUTION-PLAN.md` 步骤 1.2（`required_tools` 表）、1.3（12 条 `description` 草稿）、1.4（7 条 `not_for` 追加）。
- **改动**：同样是这 13 个 `SKILL.md` 的 frontmatter。
- **注意**：`knowledge_qa` 已有 `required_tools` 且 `description` 已达标，**无需改**。所以是「12 个技能补 `required_tools`」「12 条 `description`」「7 个技能补 `not_for`」。

```powershell
cd E:\11\my-eino-app
go test ./internal/skill/...
make check
git add -A
git commit -m "feat(skills): 补能力边界、改写路由主信号、补互斥分界"
```

**验收**
- [ ] 13 条 `description` 全为中文，且与同技能块的 `not_for` 不再矛盾
- [ ] 9 个未接入工具名与步骤 1.2 表格**逐字一致**（命名即契约，坑 A2）

**回滚**：`git checkout -- skills/`

---

### C3 · 步骤 1.5 + 1.6　打开事件采集 / 放宽输出上限

| 文件 | 改动 | 现状行号 |
|---|---|---|
| `config.yaml` | `execution_events.enabled`: `false` → **`true`**；`max_completion_tokens`: `512` → **`1024`** | 39–40 / 9 |
| `config.ark.yaml` | `execution_events.enabled`: `false` → **`true`**；token **保持 `4096`** | 54–55 / 26 |
| `config.docker.yaml` | `execution_events.enabled`: `false` → **`true`**；`max_completion_tokens`: `512` → **`2048`** | 34–35 / 8 |

⚠️ **只动 `execution_events`，不要动 `debug`**（坑 A4）。`debug` 三份分别是 `true` / `true` / `false`，**保持原样**。

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "chore(config): 打开执行事件采集、放宽输出上限"
```

**验收**（需启动服务与 MySQL）
- [ ] `debug: true` 下界面执行面板出现「加载技能」条目
- [ ] `debug: false` 重启后 `GET /skills` 404、界面无技能入口，但 `GET /sessions/{id}/execution` **仍有事件**（证明两个开关确实解耦）

**回滚**：`git checkout -- config.yaml config.ark.yaml config.docker.yaml`

---

### 1.7 · 阶段 1 验收（无 commit）

```powershell
cd E:\11\my-eino-app
Select-String -Path "skills\*\SKILL.md" -Pattern '^name:|^metadata:'   # 应无输出
go test ./internal/skill/...
go test ./internal/server/... -run "TestSkillExposureIsDebugOnly|TestWebSocketReady"
make check
```

人工断言（`debug: true` 启动后）：
- [ ] 问「目前都有什么技能」→ 每个技能的能力状态与步骤 1.2 表格一致
- [ ] 问「我要总结 Word 工单，推荐什么技能」→ 只说 `documents`，不牵扯 `pdf` / `spreadsheets`
- [ ] 问「分析这个 Excel 并生成文件」→ **不承诺产出文件**，说明缺少工具

**回滚整个阶段**：`git reset --hard HEAD~3`

**然后推送**：`git push`　（= **P1**）

---

## 六、阶段 2：鉴权与多用户隔离（C4–C12）

> **依据**：`EXECUTION-PLAN.md` → 四、阶段 2。这是**上线阻塞项**，也是代码量最大的阶段。
> **本地联调前提**：需要一个飞书自建应用（拿 `AppID` / `AppSecret`），并在开发者后台登记 `RedirectURL`。**没有它就无法完成 2.10 的 A/B 交叉测试**——这一步要提前准备。

### C4 · 步骤 2.1　配置层

- **新增**：`internal/config/auth.go`（`Auth` struct）。
- **修改**：`internal/config/config.go`（`Config` 加字段 + 环境变量覆盖 + `ValidateAuth()`）、`.env.example`、三份配置各加 `auth:` 段（**`enabled: false`**）。
- ⚠️ `ValidateAuth()` 必须**强制 `Session.Store == "mysql"`**（坑 B5：`file` 模式没有 owner 维度）。

```powershell
cd E:\11\my-eino-app
go build ./...
make check
git add -A
git commit -m "feat(auth): 新增 auth 配置段与启动校验"
```

**验收**
- [ ] `go build ./...` 通过
- [ ] 手工把 `auth.enabled` 置 `true` 且不设 `AppID` → 启动**失败且报错清晰**（验完记得改回 `false`）

**回滚**：`git reset --hard HEAD`

---

### C5 · 步骤 2.2　数据库迁移

- **新增**：`internal/session/migrations/004_auth.sql`（`auth_users` + `auth_sessions` 两张表）。
- **修改**：`internal/session/mysql.go`——新增 `IncludeAuth` 选项，**`ALTER` 语句写在 Go 侧做幂等**（坑 B2：现有 `migrationTableRE` 只匹配 `CREATE TABLE IF NOT EXISTS`，`ALTER` 每次都会重跑，第二次报 `Duplicate column name`）。
- **别忘了**：迁移后手工执行一次历史数据归属 `UPDATE conversations SET owner_id = '<admin_open_id>' WHERE owner_id = '';`。

```powershell
cd E:\11\my-eino-app
make db-migrate          # 第一次
make db-migrate          # 第二次——必须同样成功
make check
git add -A
git commit -m "feat(db): 新增 auth 表与 conversations.owner_id 幂等迁移"
```

**验收**
- [ ] `make db-migrate` **连跑两次都成功**（第二次不报 `Duplicate column name`）
- [ ] `conversations` 多出 `owner_id` 列与 `idx_conversations_owner` 索引

**回滚**：`git reset --hard HEAD`（**数据库改动需手工回退**：`DROP TABLE auth_users, auth_sessions;` + `ALTER TABLE conversations DROP COLUMN owner_id;`）

---

### C6 · 步骤 2.3　认证模块

- **新增**：`internal/auth/` 四个文件——`feishu.go`、`session.go`、`middleware.go`、`identity.go`。
- ⚠️ **端点用新版**：授权页 `accounts.feishu.cn/open-apis/authen/v1/authorize`、换 token `accounts.feishu.cn/oauth/v3/token`、取用户 `open.feishu.cn/open-apis/authen/v1/user_info`。网上大量教程仍写已弃用的 `v2/oauth/token`，照抄会直接失败（坑 B4）。
- ⚠️ **授权码 5 分钟有效且只能用一次**——`/auth/callback` 拿到 code 必须**立即**兑换。
- 登录态存储按 D11 用**进程内 map**；`auth_sessions` 表建好备用。
- **state 校验**必须做（`HttpOnly` + `SameSite=Lax` 短时 Cookie）。

```powershell
cd E:\11\my-eino-app
go build ./...
make check
git add -A
git commit -m "feat(auth): 飞书 OAuth 登录与会话存储"
```

**回滚**：`git reset --hard HEAD`

---

### C7 · 步骤 2.4　路由与中间件接入

- **新增 4 条路由**：`GET /auth/login`、`GET /auth/callback`、`GET /auth/logout`、`GET /auth/me`。
- **免认证白名单**：`/auth/login`、`/auth/callback`、`/health`、`/health/ready`。
- ⚠️ **不要把认证中间件包在最外层**：`http.go:59-72` 的超时包装是最外层 `HandlerFunc` 且对 `/ws` 跳过超时，包在外面会让 `/ws` 丢掉超时豁免。**放在 mux 内部逐路由包装**。
- ⚠️ `/ws` 的认证**只能靠 Cookie**（浏览器 WebSocket API 无法自定义 Header）；同时**保留 `ws.go` 的 `CheckOrigin`**，两者都要。

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "feat(auth): 挂载认证中间件与 4 条 auth 路由"
```

**验收**
- [ ] 未登录访问 `/` → 302 到 `/auth/login`
- [ ] 未登录调 `POST /chat` → **401 JSON（不是重定向）**
- [ ] 完整登录流程走通，`Set-Cookie` 生效
- [ ] state 不匹配的回调被拒绝；同一个 code 重复使用失败

**回滚**：`git reset --hard HEAD`

---

### C8 · 步骤 2.5　Session 隔离

- **改签名**：`List` / `Load` / `Save` / `Delete` 四个方法加 `owner`（`CleanupOlderThanBatch` **保持不变**，它是跨 owner 的全局运维清理）。
- ⚠️ **写路径必须改**：`mysql.go:222-224` 的 `INSERT IGNORE INTO conversations(id) VALUES (?)` 遇到已存在的 id 会静默跳过——**B 提交 A 的 session id 能继续往 A 的会话里追加消息**。改成 `ON DUPLICATE KEY UPDATE` 显式比对，或 `SELECT ... FOR UPDATE` 后校验。
- **9 个调用点同步改**（清单见方案步骤 2.5 表格）。
- ⚠️ `service.go:334` 的 `SetPersistence` 闭包注册在 `newAgent` 里，**`owner` 必须从 `ctx` 取出后捕获进闭包**，不能在里面再读一次 context。
- **`service.go` 公开签名不变**：owner 从 `ctx` 取，CLI（`cmd/console`、`cmd/eval`）不受影响。

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "feat(session): 会话按 owner 隔离"
```

**回滚**：`git reset --hard HEAD`

---

### C9 · 步骤 2.6　Memory 隔离

- **核心手段**：给 `Repository` 加一个 `For(ownerID)` 方法。因为 `Config` 是**值类型**，派生副本安全；而 `r.Config.OwnerID` 的 31 处引用**全部自动拿到正确 owner，一行都不用改**。
- ⚠️ **坑 B1（最致命，且无报错）**：`memory/mysql.go:163/168` 的 claim job SQL **写死了 `owner_id=?`** → 多用户上线后，**B 的记忆抽取 job 永远不会被 worker 捞出，功能静默失效**。解法：SQL 去掉 `owner_id=?`，`ProcessOne` 从 `job.owner_id` 取 owner 再 `For()`。job 表**已有 `owner_id` 字段，无需改表**。
- ⚠️ **同源问题在 `Maintain()`**（`worker.go:257-540`）：快照统计、过期回收、保留期清理全按 `Config.OwnerID` 过滤 → 多用户下**只清理默认用户的数据，其他人的数据无限增长**。解法：maintenance 是运维性质全局操作，**直接去掉 owner 过滤**。
- **放开** `internal/config/memory.go:59-61`，接受 `identity_mode: multi_user`，并新增该模式下的校验（`Auth.Enabled` 必须 true、`OwnerID` 被忽略但启动时打日志说明）。

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "feat(memory): 记忆按 owner 隔离并修正 worker job 过滤"
```

**回滚**：`git reset --hard HEAD`

---

### C10 · 步骤 2.7　Checkpoint 隔离

- **根因**：session id 可由客户端指定（`http.go:85`、`ws.go:116`、`ws.go:159`）→ 知道别人的 id 就能读审批、恢复执行（坑 B6）。
- **两层动作**：
  1. **服务端强制生成 session id**——新增 `POST /sessions`；`http.go:85` 与 `ws.go:116/159` 一律忽略客户端值；前端 `startNewChat()` 改**异步**，移除 `app.js:452` 的 `makeSessionId()`。
  2. **checkpoint 路径加 owner 分层**（兜底）——`path(id)` → `path(owner, id)`，所有方法签名加 owner。
- **7 个调用点**同步改；`cmd/console/main.go` 是 CLI 单用户路径，**保持不动**。

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "feat(checkpoint): session id 服务端生成与审批归属校验"
```

**验收**
- [ ] B 拿 A 的 session id 调 `GET /sessions/{id}` → **403/404，不是 200**
- [ ] B 调 `DELETE /sessions/{id}` 删 A 的会话 → 403
- [ ] B 调 `POST /sessions/{id}/approval` 审批 A 的中断 → 403

**回滚**：`git reset --hard HEAD`

---

### C11 · 步骤 2.8　Execution 隔离

- **无需改表**：`execution_runs.conversation_id` 已 FK 关联 `conversations`，C8 把 `conversations.owner_id` 做对即可。
- **改动**：`handleExecution` 查 execution 前先校验会话归属，不匹配返回 403；`handleDeleteSession` 因 `Delete(owner, id)` 已含校验，后续自然安全。
- **决定**：**不给 `execution_runs` 加 `owner_id` 冗余列**（会引入「两处 owner 可能不一致」的新问题）。

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "feat(execution): 执行记录归属校验"
```

**回滚**：`git reset --hard HEAD`

---

### C12 · 步骤 2.9　前端

- **改动**：`index.html` 加登出按钮与用户名；`app.js` 加 401 处理与 `startNewChat()` 异步化。
- **无需改的**：登录跳转靠服务端 302，Cookie 同源自动携带。
- ⚠️ **不要回退技能暴露面门禁**：`app.js` 的下拉框显隐、技能事件过滤、`skill` 字段按 debug 发送，全部保留。

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "feat(web): 登录态显示、登出与 401 处理"
```

**回滚**：`git reset --hard HEAD`

---

### 2.10 · 阶段 2 验收（无 commit）

准备**两个飞书账号 A、B** 交叉测试。完整清单见 `EXECUTION-PLAN.md` 步骤 2.10，四组：

- [ ] **认证**：302 / 401 / 登录 / state / code 复用 / 登出
- [ ] **会话隔离**：B 看不到 A 的会话；`GET` / `DELETE` / `approval` / `POST /chat` 四项越权全部被拒，且**不污染 A 的历史**
- [ ] **记忆隔离**：`GET /memory/facts` 只返回自己的；**A 与 B 的对话都能触发记忆抽取**（专验坑 B1：查 `memory_jobs` 两个 owner 的行都应从 pending 变 succeeded）；maintenance 跑一轮后**两个 owner** 的过期数据都被清理
- [ ] **执行记录 / 容错**：A 的执行记录 B 查不到；管理员后门可用；**CLI 未配置 auth 时行为不变**

**回滚整个阶段**：`git reset --hard HEAD~9`（数据库改动需手工回退）

**然后推送**：`git push`　（= **P2**）

---

## 七、阶段 3：eino 框架收敛（C13–C15）

> **依据**：`EXECUTION-PLAN.md` → 五、阶段 3。
> **性质：行为零变化**。严格限定为路径移动，**不碰任何 eino API 写法**（坑 C6）。验收标准是测试通过数与基线**完全一致**（17 个包全绿）。

### C13 · 步骤 3.1　机械平移　⚠️ 必须一次提交

```powershell
cd E:\11\my-eino-app
mkdir internal\eino
git mv internal\agent internal\eino\agent
git mv internal\chain internal\eino\chain
git mv internal\model internal\eino\model
git mv internal\prompt internal\eino\prompt
git mv internal\rag internal\eino\rag
git mv internal\tool internal\eino\tool
git mv internal\observability internal\eino\observability
# 然后改 29 行 import（15 个文件，清单见方案步骤 3.1 表格）
go build ./... && go vet ./... && go test -p 1 ./...
git add -A
git commit -m "refactor: converge eino code into internal/eino namespace"
git push          # ⚠️ P2.5：立即单独推送
```

**三条最容易踩的坑**
- ⚠️ **路径前缀二次替换**：`internal/model` 与 `internal/eino/model` 有前缀关系。必须匹配**引号内完整串** `"my-eino-app/internal/model"`，或先替换更长的路径，否则会变成 `internal/eino/eino/model`（坑 C3）。
- ⚠️ **测试文件的 import 容易漏**：29 行里有 3 行在测试文件 → `go build` 全绿但 `go test` 挂（坑 C2）。
- ⚠️ **别名不能丢**：`modelset` / `toolset` 这类别名一旦丢掉就会与 `einomodel` / `einotool` 混在一起。**只改路径，不动别名**（坑 C5）。

**验收**
- [ ] `go build ./...` && `go vet ./...` && `go test -p 1 ./...` 全绿
- [ ] 测试通过数与基线（17 个包）**完全一致**

**回滚**：`git reset --hard HEAD`（`git mv` 也是暂存区操作，一条命令即可回退）

---

### C14 · 步骤 3.2　边界守卫

- **改动**：`Makefile` 新增 `boundary` 目标，并把它并入 `check`（`check: fmt boundary test vet`）。
- **作用**：防止几个月后新代码又把 `compose` / `adk` / `callbacks` 扩散到 `internal/eino/` 之外。

```powershell
cd E:\11\my-eino-app
make boundary
make check
git add -A
git commit -m "chore(build): 增加 eino 边界守卫"
```

**验收**：`make boundary` 通过（当前应通过——5 个引用文件全在集内）。

**回滚**：`git checkout -- Makefile`

---

### C15 · 步骤 3.3　消除构造泄漏

三项独立，可以分开做但一起提交：

| 子项 | 动作 |
|---|---|
| 3.3.1 | 新增 `internal/eino/embedding/embedding.go`，rag 与 memory 改调它（memory 不再 import `eino-ext/components/embedding/openai`）——两处各构造一份 embedder 会因配置漂移导致**向量空间不一致** |
| 3.3.2 | `internal/skill/runtime.go` 的 `utils.NewTool` 构造下沉到 `internal/eino/tool`。⚠️ 按 D10，**只做构造下沉，不动提示词文案**（提示词是步骤 1.7 的验收对象） |
| 3.3.3 | 新增 `internal/eino/facade.go`（类型别名，零运行时开销），业务包改 import `internal/eino` |

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "refactor: 消除 embedder 与工具构造泄漏"
```

**验收**
- [ ] `make check` 通过（此时已含 `boundary`）
- [ ] `server` / `memory` / `skill` 三个业务包对 `cloudwego/eino/components/*` 的依赖降为 0（仅保留 `schema`）

**回滚**：`git reset --hard HEAD`

**然后推送**：`git push`　（= **P3**）

---

## 八、阶段 4：评测与可观测性收口（C16–C17）

> **依据**：`EXECUTION-PLAN.md` → 六、阶段 4。
> ⚠️ **必须在 C13 之后**：本阶段要改的 `internal/evaluation/evaluation.go` 与 `cmd/eval/main.go` 正在 C13 的 import 改写清单里，先做会被二次改写。

### C16 · 步骤 4.1　评测增加「路由」维度

- **埋点已现成**：`runtime.go` 已写入 `skill_names` / `requested_names`，不用新增采集。
- **动作**：`Case` 加 `ExpectSkills []string`；评测执行时用 `execution.NewRecorder(0)` 挂到 `ChatWithSink`；跑完扫 `Events()` 里 `SkillLoaded` 的 `payload.skill_names` 做集合断言；`Report` 加 `RoutingAccuracy`；`cmd/eval` 输出该指标。

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "feat(eval): 评测增加路由准确率维度"
```

**验收**：`make eval` 输出含 `routing_accuracy`。

**回滚**：`git reset --hard HEAD`

---

### C17 · 步骤 4.2　评测集扩到 ≥20 题

- **改动**：`internal/integration/eval_cases.json` 从 3 题扩到 **≥20 题**（D3），覆盖步骤 1.4 的易混场景，每题带 `expect_skills`。

```powershell
cd E:\11\my-eino-app
make eval
make check
git add -A
git commit -m "test(eval): 评测集扩到 20 题并标注期望技能"
```

**验收**
- [ ] `routing_accuracy` 有意义（不是 0 或 1 的极端值）
- [ ] 易混场景（Excel + 生成文件、Word 工单推荐）判定正确

**回滚**：`git reset --hard HEAD`

**然后推送**：`git push`　（= **P4**）

---

## 九、阶段 5：上线收口（C18–C22）

> **依据**：`EXECUTION-PLAN.md` → 七、阶段 5。
> ⚠️ **5.5 必须是最后一步**——它把系统切到鉴权开启状态，没做完前面四步就切会直接把服务锁死。

### C18 · 步骤 5.1　优雅停机（一行修复）

```powershell
cd E:\11\my-eino-app
# cmd/server/main.go：import "syscall"
# signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
make check
git add -A
git commit -m "fix(server): 捕获 SIGTERM 以支持优雅停机"
```

**依据**：`main.go:22` 只捕获 `os.Interrupt`，而 `docker stop` 默认发 **SIGTERM** → 走不到 `server.Shutdown`，10 秒优雅期与 `defer service.Close()` 全部失效。

**验收**：容器内起服务，`docker stop` 后日志出现优雅停机路径（不是直接被 kill）。

**回滚**：`git checkout -- cmd/server/main.go`

---

### C19 · 步骤 5.2　限流与配额

- **新增** `internal/server/ratelimit.go`（进程内令牌桶，key 取 `auth.OwnerFromContext(ctx)`，未认证退回客户端 IP）。
- **新增配置** `runtime.rate_limit`（`enabled: true` / `per_minute: 30` / `burst: 10`），**三份配置都写**。
- **应用在** `POST /chat` 与 `GET /ws` 两条路径。
- 超限返回 **`429` + `Retry-After`** + JSON 错误体。
- **注释里必须写清边界**：这是**单副本**限流，进程内计数；多副本需换 Redis，当前不部署多副本。同时**不做 token 预算**。

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "feat(server): 新增按用户/IP 的进程内限流"
```

**验收**：脚本连续打 40 次 `POST /chat`，第 31 次起返回 429；`burst` 内允许突发；`Retry-After` 合理。

**回滚**：`git reset --hard HEAD`

---

### C20 · 步骤 5.3　备份与恢复演练

- **新增**：`scripts/backup.ps1`（mysqldump + Milvus 卷打包 + `manifest.txt` 含时间/行数摘要/SHA256）、`scripts/restore-check.ps1`（**恢复演练**：恢复到临时库 `eino_restore_check`，比对三张表行数，比对完 DROP）。
- **`Makefile`** 新增 `backup` 目标。
- **保留策略**：最近 **14** 份，超出删最旧，**删除前打印被删目录**。
- **定时**：Windows 任务计划每 6 小时执行 `make backup`。

```powershell
cd E:\11\my-eino-app
make backup
# 手工跑 restore-check.ps1 验证
git add -A
git commit -m "chore(ops): 新增备份与恢复演练脚本"
```

**验收**
- [ ] `make backup` 产出带时间戳的目录 + 完整 `manifest.txt`
- [ ] `restore-check` 行数比对全部一致
- [ ] 连跑两次不互相覆盖
- [ ] 手工造「第 15 份」，确认最旧一份被清理且清理前有打印

**回滚**：`git reset --hard HEAD`

---

### C21 · 步骤 5.4　健康检查分离

- `/health` **保持 liveness 语义**（静态 ok + `debug` 字段）—— `Dockerfile` 的 HEALTHCHECK **保持不动**。
- **新增** `GET /health/ready`：检查 MySQL / Milvus / Ollama 连通性，单项超时 2 秒，逐项报状态。
- 该路由已在 C7 加入免认证白名单。
- compose 中 app 服务的健康检查改用 `/health/ready`。

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "feat(server): 分离 liveness 与 readiness 健康检查"
```

**验收**：停掉 MySQL 后 `/health` 仍 200、`/health/ready` 非 200 且指出哪一项失败；恢复后自动回 200。

**回滚**：`git reset --hard HEAD`

---

### C22 · 步骤 5.5　上线前配置切换（最后一步）

| 配置 | 改为 |
|---|---|
| `config.docker.yaml` 的 `auth.enabled` | `false` → **`true`** |
| `FEISHU_APP_ID` / `FEISHU_APP_SECRET` / `FEISHU_REDIRECT_URL` | 填真实凭据，**走环境变量，不写进仓库** |
| `config.docker.yaml` 的 `memory.identity_mode` | `local_single_user` → **`multi_user`** |
| `config.docker.yaml` 的 `debug` | **保持 `false`** |
| 监听地址 | 保持 `:18180`（容器内），对外暴露面由 compose 端口映射决定 |

```powershell
cd E:\11\my-eino-app
# 改完逐项核对下方「上线前最终检查」
make check
git add -A
git commit -m "chore(config): 上线前切换到鉴权与多用户模式"
git push          # = P5
```

**验收（上线前最终检查）**
- [ ] `config.docker.yaml`：`auth.enabled: true`、`identity_mode: multi_user`、`debug: false`、`execution_events.enabled: true`
- [ ] `FEISHU_*` 通过环境变量注入，**不在仓库中**
- [ ] `REDIRECT_URL` 与飞书开发者后台登记的**完全一致**（含端口与路径）
- [ ] `CookieSecure` 与部署协议匹配（内网 http 必须 `false`，否则浏览器不保存 Cookie）
- [ ] 管理员 `AdminOpenID` 已配置，本地后门可用
- [ ] 备份定时任务已生效，`restore-check` 至少跑过一次
- [ ] `make check` 全绿

---

## 十、回滚速查

| 场景 | 命令 |
|---|---|
| 回退**未提交**的改动 | `git checkout -- <文件或目录>` |
| 回退**最近一个 commit** | `git reset --hard HEAD~1` |
| 回退**整个阶段** | `git reset --hard HEAD~<该阶段 commit 数>`（阶段 1 是 3，阶段 2 是 9，阶段 3 是 3，阶段 4 是 2，阶段 5 是 5） |
| 回退到**基线** | `git reset --hard 92b2833` |
| 看某一步改了什么 | `git show <commit>` / `git diff HEAD~1` |
| 已推送后又想回退 | 先 `git revert <commit>` 生成反向提交，**不要** `push --force` |

⚠️ **`git reset --hard` 会丢弃未提交改动**。执行前先 `git status` 确认没有你还想要的东西。

⚠️ **数据库与数据卷不在 git 里**：C5 的建表/加列、C20 的备份目录，回滚代码不会回滚它们，需按该步「回滚」栏手工处理。

---

## 十一、卡住时的排查顺序

1. **`go test` 挂了但 `go build` 过** → 测试文件的 import/签名漏改（坑 C2）。
2. **多用户下某个人的记忆不工作、且无报错** → 坑 B1（worker claim 按单 owner 过滤）。
3. **`make db-migrate` 第二次报 `Duplicate column name`** → 坑 B2（`ALTER` 写进了 `.sql`）。
4. **飞书登录直接失败** → 坑 B4（用了已弃用的 v2 token 端点）。
5. **`/ws` 超时行为变了** → 认证中间件包在了最外层，挤掉了 `/ws` 的超时豁免。
6. **`internal/eino/eino/model` 这种双 eino 路径** → 坑 C3（路径前缀二次替换）。
7. **编译内存不足** → 忘了 `-p 1`。

---

## 十二、与 EXECUTION-PLAN 的分工

| 文档 | 负责 |
|---|---|
| `EXECUTION-PLAN.md` | **改什么**——字段值、SQL、代码片段、工具名清单、坑的完整说明、文件清单 |
| **本手册（RUNBOOK）** | **怎么走**——顺序、命令、commit message、推送点、回滚、进度勾选 |

两份文档内容**不重复**：手册里出现的数字（行号、计数）仅用于「定位」，与方案冲突时**以 `EXECUTION-PLAN.md` 为准**。
