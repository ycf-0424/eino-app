# 执行手册（RUNBOOK · 逐 commit 操作序列）

> **本手册是什么**：`docs/EXECUTION-PLAN.md` 的**执行序列**。它只回答四件事——按什么顺序做、敲哪些命令、什么时候提交与推送、出问题怎么回退。
>
> **本手册不是什么**：不复制内容表。**每一步具体改什么**（字段值、SQL、代码片段、工具名清单）以 `EXECUTION-PLAN.md` 的对应步骤为准。两份文档分工明确，避免内容漂移。
>
> **基线**：`92b2833`（2026-09-16 建立并推送）。**剩余 25 个 commit、6 次推送。**
> **工作目录**：`E:\11\my-eino-app`
> **外部依赖**：阶段 2 需要一个**飞书自建应用**（见第一节末），另需一次 **DBA 动作**给数据库加一个迁移账号（见 C5）。其余阶段不需要任何项目之外的资源。

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
2. **一个步骤 = 一个 commit**。只有方案明确要求合并的（`1.2+1.3+1.4`、`1.5+1.6`、`2.10+2.11`、`2.12+2.13`）才合并提交。
3. `make check` 内部是 `fmt test vet`，其中 `go fmt ./...` **会改写文件**。所以顺序是：**改代码 → 验收 → `make check` → `git add -A` → `git commit`**。

**grep 的等价写法**

```powershell
# Git Bash 里的写法（方案原文）
grep -rn "^name:\|^metadata:" skills/

# PowerShell 等价写法（有输出 = 不合规）
Select-String -Path "skills\*\SKILL.md" -Pattern '^name:|^metadata:'
```

### 外部依赖（只有阶段 2 需要，但建议现在就准备）

阶段 2 有两处需要项目之外的资源，**都已解决或可自行解决**：

| 依赖 | 谁提供 | 影响范围 | 状态 |
|---|---|---|---|
| **飞书自建应用** | 飞书开放平台 | 飞书登录链路；2.15 的「跨身份隔离」验收 | 待你创建 |
| **数据库迁移账号** | 用 root/DBA 执行一条 `GRANT` | C5、C13 的建表；`execution_runs` 缺失时服务起不来 | ✅ 已完成（`eino_migrate`，见 C5） |

飞书那部分：阶段 2 的飞书登录链路（C4–C12）建立在它之上，没有它这些代码仍能写完，但 **2.15 验收里的「跨身份隔离」那组无法进行**。本地账号那部分（C13–C15 及其验收）**不依赖飞书**，可以先做。

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
5. 左侧「开发配置 → 权限管理」→ 开通 **获取用户基本信息**（权限标识 `contact:user.base:readonly`）。**这个名字要与步骤 2.3 授权页 URL 里 `scope=` 的值逐字一致**，否则用户点授权时直接报 **20027**（授权页拼了应用未开通的权限）。
6. 左侧「应用发布 → 版本管理与发布」→ 创建版本（如 `1.0.0`）→ 申请线上发布 → 管理员审核通过。
7. **检查可用范围** → 确认要参与 A/B 测试的账号**都在可用范围内**（一般选「全员」）。不在范围内的人登录会报 **20010（用户无应用使用权限）**；这个错误发生在飞书侧，本项目日志里什么都看不到，最难排查。

⚠️ **三个卡点**

- **第 6 步不做，第 4/5 步的配置不生效。** 飞书的回调地址与权限都是「发布后生效」，这是最常踩的一个空。
- **第 4 步的 URL 必须与 `FEISHU_REDIRECT_URL` 完全一致**（含协议、端口、路径，末尾斜杠都不能差），否则授权后报 `redirect_uri` 不匹配（20071）。
- **第 7 步漏做 → 20010。** 表现是「别人登录不进来、你自己登录正常」，很容易误判成本项目的 bug。

**加速路径（可选）**：飞书为开发阶段准备了**测试企业 + 测试版本** —— 在「测试企业和人员」页创建测试企业并关联应用，切到测试版本后，**第 5、6 步的权限与配置变更直接生效、无需管理员审核**，联调完再切回正式版提交一次审核。只有你既是开发者又是管理员时，直接走正式版也一样快（第 6 步自己点通过）。

**本地联调的三个前提**（不满足会卡住，且报错点分散在各处）

- **不需要公网，也不需要内网穿透。** 回调是「浏览器 302 跳转」，请求由你本机的浏览器发出，`http://localhost:18180/auth/callback` 对它天然可达；飞书服务器**从不主动访问**该地址，只在换 token 时做一次**字符串比对**（`internal/auth/feishu.go` 的 `Exchange` 把 `cfg.RedirectURL` 原样回传）。所以本地把飞书登录跑穿是完全可行的。
- **全程统一一个主机名。** `state` 防 CSRF 走 Cookie 校验（`auth.NewState` / `auth.VerifyState`），而 Cookie 按主机名隔离。若顺手用 `127.0.0.1:18180` 打开登录页、而 `FEISHU_REDIRECT_URL` 写的是 `localhost:18180`，回调时 `state` 读不到 → **`state mismatch`（400）**，且看起来像「飞书那边配错了」。两种写法都行，但**必须从头到尾一致**，并与飞书后台登记值逐字符相同。
- **改完 `.env` 必须重建容器。** `environment` 在容器**创建时**注入，`docker compose up -d` 不会让已存在的容器读到新值，要 `--force-recreate app`。漏了这步的表现还是老样子：`/health` 的 `login.feishu=false`。

**2.15 的交叉测试需要哪些身份**：两个飞书账号（两个手机号）+ 两个本地账号（用 `cmd/user-admin` 建）。

- **好消息**：本地账号可以随时创建，所以**即使你只有一个飞书账号，也能用两个本地账号把「会话 / 记忆 / 执行记录隔离」那几组用例完整跑完**。
- **但跨身份那组必须两者都有**：验证「本地账号看不到飞书账号的数据」（D14）至少需要 1 个可用的飞书账号。
- 本地账号之间的用例完全不依赖飞书是否就绪 —— **飞书应用还没批下来时就能先跑一半**。

**凭据填完先验一遍，别急着开浏览器**（2026-09-17 实测踩过的坑）

`/health` 的 `login.feishu` 只反映**三个值非空**，不反映**值正确**。App Secret 抄错、或后台点过 Secret 右侧的「重置」（循环箭头）后旧值立刻作废，这两种情况下链路会**一路绿灯**：`/health` 报 true、授权页正常打开、能授权、能回跳 —— 直到服务端换 token 那一步才炸：

```
feishu exchange: http 400 {"error":"invalid_client",
  "error_description":"The client secret is invalid.","code":20002}
```

此时**授权码已作废** —— 实测拿到后 3 分半即报 `20004 code has expired`；而且回调一被处理，
服务端就立刻清掉 `state` Cookie（失败路径也清，见 `internal/server/auth.go` 的 `ClearStateCookie`），
**刷新那页只会得到 400 `state mismatch`**。所以只能回 `/auth/login` 重新发起，白跑一趟浏览器。
用脚本先验再填：

```powershell
# 先验证再落盘：飞书不认这个值就一个字节都不动 .env（交互式粘贴，不回显）
powershell -File scripts/feishu-set-secret.ps1

# 五个观察点一次过：凭据有效性 → /health → 入口 302 → 授权 URL 自洽 → 回调拒绝伪造 state
powershell -File scripts/feishu-check.ps1
```

两个脚本都用 `tenant_access_token/internal` 校验凭据，该端点会**区分失败维度**（实测对照）：

| 请求 | 返回 |
|---|---|
| 真 app_id + 假 secret | `10014 app secret invalid` |
| 假 app_id + 真 secret | `10014 app id not exists` |

所以报错能直接定位到具体是哪一件凭据错。另注意：App Secret 是**逐字符敏感**的，从网页复制常带尾部空白 —— `feishu-set-secret.ps1` 会自动 trim，并对比新值与 `.env` 原值：若**完全一致**，说明不是抄错而是这个 Secret 已失效。

---

## 二、提交与推送节奏

**提交**：一个步骤一个 commit（本手册已把 33 步归并为 **25 个 commit**）。

**推送**：**阶段级推送 + `3.1` 单独推**。理由——commit 是给「回退」用的，push 是给「离开这台机器」用的，两者粒度不必一致；阶段内每步都推会产生大量噪声，而阶段边界正好是 `make check` 全绿的天然检查点。`3.1` 要单独推是因为它一次平移 37 个文件，是全程唯一「本地磁盘出事就得重做」的一步。

| 推送 | 时机 | 命令 |
|---|---|---|
| **P0** | 现在（阶段 0 的文档提交尚未推送，见第四节） | `git push` |
| P1 | 阶段 1 验收通过 | `git push` |
| P2 | 阶段 2 验收通过 | `git push` |
| **P2.5** | **C16（3.1）提交后立即** | `git push` |
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
| **C13** | 2.10+2.11 | 自有账号：配置与数据层 / 密码哈希与建号 CLI | ☐ |
| **C14** | 2.12+2.13 | 自有账号：登录逻辑与路由 / 登录限流与 owner 前缀收口 | ☐ |
| **C15** | 2.14 | 自有账号：前端登录表单 | ☐ |
| — | 2.15 | 阶段 2 验收（A/B × L1/L2 交叉测试，无 commit）→ **P2** | ☐ |
| **C16** | 3.1 | eino 机械平移 | ☐ → **P2.5** |
| **C17** | 3.2 | 边界守卫 | ☐ |
| **C18** | 3.3 | 消除构造泄漏 | ☐ |
| — | — | 阶段 3 验收 → **P3** | ☐ |
| **C19** | 4.1 | 评测路由维度 | ☐ |
| **C20** | 4.2 | 评测集扩题 | ☐ |
| — | — | 阶段 4 验收 → **P4** | ☐ |
| **C21** | 5.1 | 优雅停机 | ☐ |
| **C22** | 5.2 | 限流与配额 | ☐ |
| **C23** | 5.3 | 备份与恢复演练 | ☐ |
| **C24** | 5.4 | 健康检查分离 | ☐ |
| **C25** | 5.5 | 上线前配置切换 | ☐ |
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

## 六、阶段 2：鉴权、多用户隔离与自有账号体系（C4–C15）

> **依据**：`EXECUTION-PLAN.md` → 四、阶段 2。这是**上线阻塞项**，也是代码量最大的阶段。
> **本地联调前提**：需要一个飞书自建应用（拿 `AppID` / `AppSecret`），并在开发者后台登记 `RedirectURL`。**没有它就无法完成 2.15 的跨身份用例**——这一步要提前准备。但**本地账号那部分（C13–C15 与相应验收）不依赖飞书**，可以先做。

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
- [ ] 手工把 `auth.enabled` 置 `true`、**飞书三字段留空、且 `auth.local.enabled: false`** → 启动**失败且报错清晰**（此时一种登录方式都没启用）。⚠️ C13 会把这套校验改成「至少启用一种」，届时只要 `auth.local.enabled: true` 就能正常启动 —— 验收完记得把 `auth.enabled` 改回 `false`

**回滚**：`git reset --hard HEAD`

---

### C5 · 步骤 2.2　数据库迁移

- **新增**：`internal/session/migrations/004_auth.sql`（`auth_users` + `auth_sessions` 两张表）。
- **修改**：`internal/session/mysql.go`——新增 `IncludeAuth` 选项，**`ALTER` 语句写在 Go 侧做幂等**（坑 B2：现有 `migrationTableRE` 只匹配 `CREATE TABLE IF NOT EXISTS`，`ALTER` 每次都会重跑，第二次报 `Duplicate column name`）。
- **别忘了**：迁移后执行一次历史数据归属 —— `go run ./cmd/session-migrate -claim-owner=<owner>`（**owner 带 `feishu:` 前缀**，理由见 C6；`auth_sessions` 的归属列名也是 `owner`）。归属逻辑在 `internal/session/mysql.go` 的 `ClaimLegacySessions`，**不要手写 UPDATE 绕过它**。
  - 原文档此处写的是 `UPDATE conversations SET owner_id = 'feishu:<admin_open_id>' WHERE owner_id = '';` —— 那个 `auth.admin_open_id` 配置字段**全项目零使用点，已于本轮删除**（详见第十四章 14.10）。

> ⚠️ **本步的前置条件：DDL 权限。** 应用账号按设计只有 `SELECT/INSERT/UPDATE/DELETE`（`MigrationOptions` 的注释与 `IncludeMemoryIndexes` 的 opt-in 都基于这个前提），**建表、加列、加索引都会报 `Error 1142: CREATE command denied`**。所以本项目用**独立的迁移账号**：
>
> ```sql
> -- 用 DBA/root 执行一次
> CREATE USER IF NOT EXISTS 'eino_migrate'@'%' IDENTIFIED BY '<强口令>';
> GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,ALTER,INDEX,REFERENCES ON eino.* TO 'eino_migrate'@'%';
> FLUSH PRIVILEGES;
> ```
>
> 然后把这组凭据写进 `.env`（已被 `.gitignore` 排除）：
>
> ```
> MYSQL_MIGRATE_USER=eino_migrate
> MYSQL_MIGRATE_PASSWORD=<强口令>
> ```
>
> `cmd/session-migrate` 会**优先使用这组凭据**（仅本命令，应用进程仍用 `MYSQL_USER`），因此 `make db-migrate` 开箱可用；未配置时回落到应用账号，适用于应用账号本身持有 DDL 权限的部署。

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
- ⚠️ **owner 一律取 `<provider>:<subject>` 形式**：飞书写 `feishu:<open_id>`，本地账号在 C14 写 `local:<uuid>`。下游只做等值比较，加前缀零成本；不加前缀则数据库里会长期混着两种格式，排查归属时看不出身份来源。
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

- **新增 5 条路由**：`GET /auth/login`、`GET /auth/callback`、`GET /auth/logout`、`GET /auth/me`，以及**条件注册**的 `POST /auth/local`（本地账号登录，C14 落地）。
- **免认证白名单**：`/auth/login`、`/auth/callback`、`/auth/local`、`/health`、`/health/ready`。⚠️ **登录入口自身必须在白名单内**，否则未登录时根本访问不到。
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

### C13 · 步骤 2.10+2.11　自有账号：配置与数据层 / 密码哈希与建号 CLI

- **配置**：三份配置加 `auth.local` 子段（`enabled: true`、`min_password_length: 8`）；`internal/config/auth.go` 加 `LocalAuth` 结构。
- ⚠️ **必须同时修正步骤 2.1 的校验**：C4 原写「`auth.enabled` 时飞书三字段必填」，会堵死「只用自有账号、不接飞书」的场景。改成「飞书三字段齐全 **或** `auth.local.enabled` —— 至少一个即可」。
- **迁移**：新增 `internal/session/migrations/005_local_users.sql`（表 `auth_local_users`）；`mysql.go` 加 `IncludeLocalUsers`。该语句是 `CREATE TABLE IF NOT EXISTS`，**天然幂等**，不踩坑 B2。
- **密码哈希**：新增 `internal/auth/password.go`。⚠️ **用 Go 标准库 `crypto/pbkdf2`（1.24+ 自带，零新增依赖），绝不用 sha256** —— 自有账号口令由用户自选，强度不可控，必须慢哈希（坑 B9）。210000 轮 + 16 字节随机盐，存储格式 `pbkdf2-sha256$<iter>$<b64salt>$<b64key>`。
- **建号工具**：新增 `cmd/user-admin/main.go`（`-create` / `-passwd` / `-disable` / `-enable` / `-list`）。建号**不经 HTTP**（D15）。
- 建第一个管理员：`go run ./cmd/user-admin -create -username=admin -admin`（口令走 stdin 交互输入，不进 shell 历史）。

```powershell
cd E:\11\my-eino-app
go test ./internal/auth/...
make db-migrate          # 连跑两次，第二次不应报 Duplicate
make check
git add -A
git commit -m "feat(auth): 自有账号的配置、用户表与密码哈希"
```

**验收**
- [ ] `make db-migrate` 连跑**两次**都成功
- [ ] `auth_local_users` 表存在，`username` 有唯一索引
- [ ] `-list` 能看到刚建的账号；表里 `password_hash` 形如 `pbkdf2-sha256$210000$...`，**无明文**

**回滚**：`git reset --hard HEAD`（数据库侧 `DROP TABLE auth_local_users`）

---

### C14 · 步骤 2.12+2.13　自有账号：登录逻辑与路由 / 登录限流与 owner 前缀收口

- **登录逻辑**：新增 `internal/auth/local.go`。四条硬要求（详见方案 2.12 表格）：用户名**先做 `^[a-zA-Z0-9_]{3,32}$` 格式校验再查库**（必须**拒绝含 `:`**，否则与飞书 owner 命名空间撞车，坑 B7）；口令长度 8–128（上限防 PBKDF2 被拖成 DoS）；**「用户名不存在」与「口令错误」返回同一响应**；`disabled` 账号一律拒绝且**不提示「已停用」**。
- **路由**：`POST /auth/local`，**复用步骤 2.3 的同一套 session 签发与 Cookie 写入**。**不做注册路由、不做改密路由**。
- ⚠️ **登录限流必须有**：新增 `internal/server/ratelimit.go`（进程内令牌桶，**按 IP + 用户名双维度**：只按 IP 漏内网多机，只按用户名漏批量撞库）。用户名可枚举，无限流等于开放爆破（坑 B8）。
- ⚠️ **这份 `ratelimit.go` 就是步骤 5.2（C22）要复用的那一份** —— 到那一步**只扩展、不重建**，否则会出现两套限流实现。
- **owner 前缀收口**：确认飞书侧写 `feishu:<open_id>`、本地账号写 `local:<uuid>`，全项目**没有**裸 `open_id` 当 owner 的位置。

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "feat(auth): 自有账号登录、登录限流与 owner 前缀收口"
```

**验收**
- [ ] 正确凭据 200 + `Set-Cookie`；错误凭据 401 JSON；`disabled` 账号 401
- [ ] 用户名填 `feishu:abc` 被格式校验拒绝
- [ ] 连续 10 次错误口令触发 429，且窗口内**即使口令正确也被拒**（预期行为）

**回滚**：`git reset --hard HEAD`

---

### C15 · 步骤 2.14　自有账号：前端登录表单

- **改动**：`index.html` + `app.js` 加登录方式切换（飞书按钮 / 账号表单），**某一种未启用则不渲染该入口**；用户名显示带来源（「张三（飞书）」/「zhangsan（账号）」）。
- **启用状态从哪来**：扩展 `GET /health` 返回体加 `login: {feishu, local}` —— 该路由已在免认证白名单内，**不新增路由**。
- **页面上不放任何注册入口**（D12）。
- ⚠️ **不要回退技能暴露面门禁**：`app.js` 的下拉框显隐、技能事件过滤、`skill` 字段按 debug 发送，全部保留。

```powershell
cd E:\11\my-eino-app
make check
git add -A
git commit -m "feat(web): 飞书与自有账号双入口登录页"
```

**验收**
- [ ] 两种登录方式都能走通，且显示正确的用户名与来源
- [ ] 停用某一种后该入口从页面消失
- [ ] 页面上不存在注册入口

**回滚**：`git reset --hard HEAD`

---

### 2.15 · 阶段 2 验收（无 commit）

准备**两个飞书账号 A、B** 与**两个本地账号 L1、L2**（用 `cmd/user-admin` 建）交叉测试。完整清单见 `EXECUTION-PLAN.md` 步骤 2.15，六组：

- [ ] **认证**：302 / 401 / 登录 / state / code 复用 / 登出
- [ ] **会话隔离**：B 看不到 A 的会话；`GET` / `DELETE` / `approval` / `POST /chat` 四项越权全部被拒，且**不污染 A 的历史**
- [ ] **记忆隔离**：`GET /memory/facts` 只返回自己的；**A 与 B 的对话都能触发记忆抽取**（专验坑 B1：查 `memory_jobs` 两个 owner 的行都应从 pending 变 succeeded）；maintenance 跑一轮后**两个 owner** 的过期数据都被清理
- [ ] **自有账号**：建号能登录、`-disable` 后立即失效、`-passwd` 重置生效、错误口令触发 429、页面**无注册入口**
- [ ] **跨身份隔离（本阶段最关键的一组）**：L1 与 L2 之间互不可见；**L1 拿飞书账号 A 的 session id 访问 → 403/404**（反向亦然）—— 这是「两套身份完全独立」（D14）的判定性用例
- [ ] **执行记录 / 容错**：A 的执行记录 B 查不到；**清空 `FEISHU_*` 并重启后，本地账号仍可登录**（这是本地账号存在的首要理由）；**只启用本地账号**（飞书三字段留空 + `auth.local.enabled: true`）时服务能正常启动并登录；**CLI 未配置 auth 时行为不变**

**回滚整个阶段**：`git reset --hard HEAD~12`（数据库改动需手工回退）

**然后推送**：`git push`　（= **P2**）

---

## 七、阶段 3：eino 框架收敛（C16–C18）

> **依据**：`EXECUTION-PLAN.md` → 五、阶段 3。
> **性质：行为零变化**。严格限定为路径移动，**不碰任何 eino API 写法**（坑 C6）。验收标准是测试通过数与基线**完全一致**（17 个包全绿）。

### C16 · 步骤 3.1　机械平移　⚠️ 必须一次提交

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

### C17 · 步骤 3.2　边界守卫

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

### C18 · 步骤 3.3　消除构造泄漏

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

## 八、阶段 4：评测与可观测性收口（C19–C20）

> **依据**：`EXECUTION-PLAN.md` → 六、阶段 4。
> ⚠️ **必须在 C16 之后**：本阶段要改的 `internal/evaluation/evaluation.go` 与 `cmd/eval/main.go` 正在 C16 的 import 改写清单里，先做会被二次改写。

### C19 · 步骤 4.1　评测增加「路由」维度

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

### C20 · 步骤 4.2　评测集扩到 ≥20 题

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

## 九、阶段 5：上线收口（C21–C25）

> **依据**：`EXECUTION-PLAN.md` → 七、阶段 5。
> ⚠️ **5.5 必须是最后一步**——它把系统切到鉴权开启状态，没做完前面四步就切会直接把服务锁死。

### C21 · 步骤 5.1　优雅停机（一行修复）

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

### C22 · 步骤 5.2　限流与配额

- ⚠️ **复用 C14 已建的 `internal/server/ratelimit.go`，本步不新建** —— 只做扩展：key 取 `auth.OwnerFromContext(ctx)`，未认证退回客户端 IP。若在此重新实现一套，就会出现两套限流。
- **新增配置** `runtime.rate_limit`（`enabled: true` / `per_minute: 30` / `burst: 10`），**三份配置都写**。
- **扩展到** `POST /chat` 与 `GET /ws` 两条路径（C14 已用于 `POST /auth/local`，保持不变）。
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

### C23 · 步骤 5.3　备份与恢复演练

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

### C24 · 步骤 5.4　健康检查分离

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

### C25 · 步骤 5.5　上线前配置切换（最后一步）

| 配置 | 改为 |
|---|---|
| `config.docker.yaml` 的 `auth.enabled` | `false` → **`true`** |
| `FEISHU_APP_ID` / `FEISHU_APP_SECRET` / `FEISHU_REDIRECT_URL` | 填真实凭据，**走环境变量，不写进仓库** |
| `config.docker.yaml` 的 `auth.local.enabled` | **保持 `true`**（飞书故障时的兜底入口） |
| `config.docker.yaml` 的 `memory.identity_mode` | `local_single_user` → **`multi_user`** |
| `config.docker.yaml` 的 `debug` | **保持 `false`** |
| 监听地址 | 保持 `:18180`（容器内），对外暴露面由 compose 端口映射决定 |

⚠️ **执行顺序（最容易漏的一步）**：**先建管理员本地账号，再打开 `auth.enabled`。**

```powershell
cd E:\11\my-eino-app
go run ./cmd/user-admin -create -username=admin -admin   # 口令走 stdin 交互输入
# 然后再改配置、重启
```

否则飞书认证服务一旦出问题，系统将**没有任何入口**（坑 B10）—— 而本地账号存在的首要理由正是这一刻。

```powershell
cd E:\11\my-eino-app
# 改完逐项核对下方「上线前最终检查」
make check
git add -A
git commit -m "chore(config): 上线前切换到鉴权与多用户模式"
git push          # = P5
```

**验收（上线前最终检查）**
- [ ] `config.docker.yaml`：`auth.enabled: true`、`auth.local.enabled: true`、`identity_mode: multi_user`、`debug: false`、`execution_events.enabled: true`
- [ ] **已用 `cmd/user-admin` 建好管理员本地账号**（在打开 auth **之前**完成）
- [ ] `FEISHU_*` 通过环境变量注入，**不在仓库中**
- [ ] `REDIRECT_URL` 与飞书开发者后台登记的**完全一致**（含端口与路径）
- [ ] 飞书后台**已完成「发布」**，且**可用范围**包含所有要用的人（否则报 20010）
- [ ] `CookieSecure` 与部署协议匹配（内网 http 必须 `false`，否则浏览器不保存 Cookie）
- [ ] 历史会话已用 `go run ./cmd/session-migrate -claim-owner=<owner>` 归属（**不是 `AdminOpenID`** —— 该字段已于 2026-09-17 删除，见 14.10）
- [ ] 备份定时任务已生效，`restore-check` 至少跑过一次
- [ ] `make check` 全绿

---

## 十、回滚速查

| 场景 | 命令 |
|---|---|
| 回退**未提交**的改动 | `git checkout -- <文件或目录>` |
| 回退**最近一个 commit** | `git reset --hard HEAD~1` |
| 回退**整个阶段** | `git reset --hard HEAD~<该阶段 commit 数>`（阶段 1 是 3，阶段 2 是 **12**，阶段 3 是 3，阶段 4 是 2，阶段 5 是 5） |
| 回退到**基线** | `git reset --hard 92b2833` |
| 看某一步改了什么 | `git show <commit>` / `git diff HEAD~1` |
| 已推送后又想回退 | 先 `git revert <commit>` 生成反向提交，**不要** `push --force` |

⚠️ **`git reset --hard` 会丢弃未提交改动**。执行前先 `git status` 确认没有你还想要的东西。

⚠️ **数据库与数据卷不在 git 里**：C5 的建表/加列、C13 的 `auth_local_users` 表、C23 的备份目录，回滚代码不会回滚它们，需按该步「回滚」栏手工处理。

---

## 十一、卡住时的排查顺序

1. **`go test` 挂了但 `go build` 过** → 测试文件的 import/签名漏改（坑 C2）。
2. **多用户下某个人的记忆不工作、且无报错** → 坑 B1（worker claim 按单 owner 过滤）。
3. **`make db-migrate` 第二次报 `Duplicate column name`** → 坑 B2（`ALTER` 写进了 `.sql`）。
4. **飞书登录直接失败** → 坑 B4（用了已弃用的 v2 token 端点）。
5. **`/ws` 超时行为变了** → 认证中间件包在了最外层，挤掉了 `/ws` 的超时豁免。
6. **`internal/eino/eino/model` 这种双 eino 路径** → 坑 C3（路径前缀二次替换）。
7. **本地账号登录被持续拒绝（口令确认没错）** → 触发了 C14 的登录限流，等窗口过去；若 `disabled=1` 则无论口令对错都是 401（且不提示「已停用」，这是刻意的）。
8. **`cmd/user-admin` 建号报错或登录失败** → 用户名需匹配 `^[a-zA-Z0-9_]{3,32}$`（**不能含 `:`**），口令长度需 ≥ `auth.local.min_password_length`。
9. **打开 `auth.enabled` 后谁都进不来** → 坑 B10：没建任何本地账号，且飞书侧未配通。
7. **编译内存不足** → 忘了 `-p 1`。

---

## 十二、与 EXECUTION-PLAN 的分工

| 文档 | 负责 |
|---|---|
| `EXECUTION-PLAN.md` | **改什么**——字段值、SQL、代码片段、工具名清单、坑的完整说明、文件清单 |
| **本手册（RUNBOOK）** | **怎么走**——顺序、命令、commit message、推送点、回滚、进度勾选 |

两份文档内容**不重复**：手册里出现的数字（行号、计数）仅用于「定位」，与方案冲突时**以 `EXECUTION-PLAN.md` 为准**。

---

## 十三、执行记录：阶段 4–5（2026-09-17）

> 本章记录**实际执行结果**与**对上面步骤的偏离**。上面章节仍是操作说明；本章是「实际发生了什么」。
> 与方案冲突时，优先看这里的偏离理由——它们是执行中才暴露出来的。

### 阶段 4（C19–C20）

| 步骤 | 状态 | 说明 |
|---|---|---|
| C19 路由评测维度 | ✅ 代码完成 | 见下方 3 条偏离 |
| C20 评测集 ≥20 题 | ✅ 22 题已落 | 路由准确率实测见本节末 |

**偏离 1：不经过 `ChatWithSink`，改为按服务端口径直接装配 Agent。**
方案写的是「用 `execution.NewRecorder(0)` 挂到 `ChatWithSink`」，但 `cmd/eval` 从不构造
`server.Service`——那会把 MySQL、Memory 一整套依赖拖进一个只做离线评测的命令里。
实际做法是照 `internal/server/service.go:341-355` 的方式自建 Agent（不落库，
`checkpoint` 只给目录、不接 session），再用
`execution.NewSession(runID, sessionID, nil /*store*/, recorder /*sink*/, ...)` 取事件。

⚠️ **`execution.Recorder` 本身不是 `execution.Emitter`**（它只有 `Push`/`Events`，缺
`RunID`/`SessionID`/`NextSequence`/`Emit`）。所以不能直接
`execution.WithEmitter(ctx, recorder)`；`NewSession` 才是 Emitter，Recorder 是它的 sink。

**偏离 2：`cmd/eval` 原先没把「已注册工具」告诉技能目录。**
`skill.Loader.Runtime(preferred, availableTools...)` 的第二个参数决定目录里每个技能的
「能力状态」。`internal/server` 传了（`service.go:341-349`），`cmd/eval` 与 `cmd/console`
都没传 → 目录里**每个**技能都被标注「缺少工具 … 只可提供说明或替代方案，不能承诺执行」。
这等于在提示词里劝模型不要加载技能，路由评测会失真。已按服务端口径修正 `cmd/eval`。

**偏离 3（评测发现的真实缺陷）：技能名被模型当成工具名调用。**
首轮实测（4 题，显式点名技能）出现 `[NodeRunError] tool report_writer not found in
toolsNode indexes` —— 目录里 `- report_writer: 组织演示大纲…` 这种「名字 + 一句话描述」
的排版与工具定义长得一样，模型直接把技能名当工具调用，整轮失败。

修法（`internal/skill/runtime.go`）：
1. 把「可用工具：…」提到技能目录**之前**；
2. 显式写明「技能目录里的名字是技能名，不是工具名；要使用技能必须调用 `load_skills`」；
3. 补一条正向规则：「命中技能适用场景时必须先 `load_skills` 再执行」。

**附带修复：`cmd/eval` 在向量库不可达时会无限期挂住。**
Milvus 停掉后实测空转 **18 分钟、零输出、CPU 占用 ≈ 0**——检索调用在连接断开时不是返回
错误，而是一直阻塞。已在开跑前加 5 秒 TCP 探测（`probeVectorStore`），12 秒内明确报错。

**新增测试**：`internal/evaluation/routing_test.go`（6 条，覆盖覆盖式断言、跳过无期望题、
分母只算可评题等）。

### 阶段 5（C21–C25）

| 步骤 | 状态 | 说明 |
|---|---|---|
| C21 SIGTERM 优雅停机 | ✅ | `cmd/server/main.go` 已加 `syscall.SIGTERM` |
| C22 限流 | ✅ 代码 + 单测完成 | 见下方偏离 |
| C23 备份与恢复演练 | ✅ 脚本完成 | 实测需 Docker 引擎在运行 |
| C24 liveness / readiness 分离 | ✅ 代码 + 单测完成 | 实测需 Docker 引擎在运行 |
| C25 上线前配置切换 | ⏸ 待办 | **前置：飞书凭据未配置**（见下） |

**C22 偏离：未认证时退回 `RemoteAddr`，不采信 `X-Forwarded-For`。**
方案 5.2 写的是「`X-Forwarded-For` 优先，回退 `RemoteAddr`」。实际沿用步骤 2.13 已有的
`clientIP()`（`internal/server/auth.go:252`）：**只信 `RemoteAddr`**。XFF 是客户端可任意
伪造的头，采信它等于把 IP 维度的配额直接让给攻击者（每次换个假 IP 就绕开）。
已在 `throttle` 的注释里写明，并有单测锁定（伪造 XFF 仍需继续被拒）。

**C22 实现要点**
- `RateLimit{Enabled, PerMinute, Burst}` 加进 `config.Runtime`；三份配置均为
  `enabled: true / per_minute: 30 / burst: 10`。
- 限流中间件 `Service.throttle` **包在 `protected` 内部**：owner 由认证中间件注入，
  包在外面就只能按 IP 限流，同一出口 IP 后的人会互相挤配额。
- 只挂在 `POST /chat` 与 `GET /ws`；登录限流（`loginLimiter`）与它**分开两套桶**，
  共用一套会让一次登录失败扣掉聊天配额。
- 边界已写进注释：**单副本**、进程内计数，多副本需换 Redis；**不做 token 预算**。

**C24 实现要点**
- `/health` 保持 liveness（静态 ok + `debug` + `login`）；`Dockerfile` 的 HEALTHCHECK 不动。
- 新增 `GET /health/ready`（免认证）：MySQL 用 `PingContext`、Milvus 用 TCP 建连、
  Ollama 用 `GET /api/tags`；单项超时 2 秒，并发执行，逐项返回 `name/ok/latency_ms/error`。
- 只探测**本次实际装配了的**依赖：`session.store=file` 不探 MySQL，`rag.enabled=false`
  不探 Milvus/Ollama。未装配任何依赖时返回 200 + 空列表（ready 退化为 liveness）。
- compose 的 `app` 服务健康检查改用 `/health/ready`（与 Dockerfile 语义不同，两者并存）。

**顺带修掉的缺陷**：`health.OllamaTagsURL` 原先「先剥 `/v1` 再剥尾斜杠」，
输入 `…/v1/` 会拼出 `…/v1/api/tags`（必然 404）。单测抓到，已改为先剥尾斜杠。

**C23 脚本设计要点（与方案的三处偏离）**
1. **MySQL 用 `mysqldump` 而不是打包数据卷**：运行中的 InnoDB 数据目录随时在写，
   直接 tar 出来不是一致性快照。方案原文也是 mysqldump，此处只是明确原因。
2. **dump 不带 `--databases`**：带了它，dump 内会含 `CREATE DATABASE` / `USE`，
   恢复演练就没法把同一份 dump 导进 `eino_restore_check`——那些语句会把数据写回生产库。
   代价是灾难恢复时要先手工 `CREATE DATABASE`。
3. **容器名与卷名都不硬编码**：容器 id 问 `docker compose ps -q mysql`；
   Milvus 卷按 `*_milvus-data` 等后缀在 `docker volume ls` 里匹配（项目名随目录名变化）。
4. `restore-check` 先校验 **SHA256** 再恢复：归档损坏必须在连数据库之前发现，
   否则会把「文件坏了」误判成「恢复逻辑坏了」。
5. 两个脚本都是 **UTF-8 with BOM**。PowerShell 5.1 对无 BOM 的 UTF-8 `.ps1` 按 ANSI
   解码，中文注释会变成乱码，极端情况下会把引号吃掉导致脚本解析失败。

**定时备份**
```powershell
# Windows 任务计划：每 6 小时（⚠️ 必须在**以管理员身份运行**的终端里执行）
# /RL HIGHEST 要求提权，普通终端下 schtasks 会直接失败，任务不会被创建。
schtasks /Create /TN "eino-backup" /SC HOURLY /MO 6 /RL HIGHEST ^
  /TR "powershell -NoProfile -ExecutionPolicy Bypass -File E:\11\my-eino-app\scripts\backup.ps1"
```
```bash
# Linux（本机 Ollama/Milvus 不在容器里时同样适用）
0 */6 * * * cd /srv/my-eino-app && make backup >> /var/log/eino-backup.log 2>&1
```

创建后核对与撤销：
```powershell
schtasks /Query  /TN "eino-backup" /FO LIST      # 确认真的建上了
schtasks /Delete /TN "eino-backup" /F            # 撤销
```
`backup.ps1` 自己读 `.env` 取凭据（`Read-DotEnv`），所以定时任务**不需要额外注入环境变量**。
Docker 引擎没起来时它会以非零码退出并打印原因，不会产出「看起来成功」的空备份。

### C25 的前置阻塞项（`auth.enabled` / 飞书）

`config.docker.yaml` 的 `auth.enabled` 与 `memory.identity_mode` **已于早前单独切换**
（commit `209bde1`：`auth.enabled: true` + `identity_mode: multi_user`），当时用的
是**本地账号**入口，已实测完整登录链路。

⚠️ **飞书凭据的当前状态（2026-09-17 晚，实测）**：三行**已写入 `.env`**
（`len=20/32/36`；`docker exec my-eino-app-app-1 env` 与 `.env` 逐项长度一致 → 透传链路已验证），
`/health` 的 `login.feishu` 已是 `true`。**但 App Secret 被飞书判为无效**：

- `tenant_access_token` 返回 `10014 app secret invalid`；
- 真人在浏览器走完授权、回跳后换 token 报 `20002 invalid_client / The client secret is invalid.`；
- **App ID 侧已确认正确** —— 假 app_id 会返回 `app id not exists`，我们这对返回的是 secret 那条。

凭据截图与本机 `.env` 逐字符一致，所以不是抄错，而是这个 Secret 已失效（最可能是在后台
点过 Secret 右侧的「重置」循环箭头 —— 旧值立刻作废）。处置：回「凭证与基础信息」复制当前
Secret → `powershell -File scripts/feishu-set-secret.ps1` → 重建容器。

在 App Secret 修好之前：

- 登录页**已能渲染飞书按钮**（凭据非空即渲染），点击也能走到飞书授权页并成功回跳；
- 但回跳后换 token 必失败，**飞书登录仍不可用**；
- **`login.feishu=true` 不代表凭据可用** —— 判据要换成 `scripts/feishu-check.ps1` 的第 1 项
  （或 `feishu-set-secret.ps1 -DryRun`）；
- **本地账号仍是可用入口，不要把它关掉**（坑 B10）。

---

## 十四、运行期实测记录（2026-09-17 下午）

> 第十三章记录的是「代码写完了没有」，本章记录的是「**真跑起来是什么结果**」。
> Docker 引擎恢复可用后补做的实测：C20 评测、C21 优雅停机、C22 限流、C23 备份/恢复、C24 健康检查。
> 实测共 **10 处「只有真跑才暴露」的修复**，分布是：限流脚本请求体字段写错 1（14.1）、
> 备份链 4（14.2）、AI 侧整轮硬失败路径 2（14.5）、构建缓存导致「重建了但产物没换」1（14.3）、
> 后台任务失败日志丢原因 1（14.8）、评测指标里 16/22 题的「正确」恒为真 1（14.14）。
> **14.1–14.6 是第一轮**（发现问题的过程），**14.7 是镜像修正后的第二轮**（四项全部通过），
> **14.12 是认证态补测**，**14.13–14.14 是 debug/生产两态对照与它暴露的评测缺陷**。
> **14.10–14.11 记的是方案自身与部署接线的缺陷**（不是执行遗漏）。

### 14.1 C22 限流实测 ✅

新增 `scripts/ratelimit-check.ps1`（方案 5.2 的验收要求「脚本连续打 40 次 `POST /chat`」，
此前**并没有这个脚本**，只有单测）。`Makefile` 未加目标，直接调脚本即可：

```powershell
pwsh -File scripts/ratelimit-check.ps1 -BaseUrl http://127.0.0.1:18181
pwsh -File scripts/ratelimit-check.ps1 -BaseUrl http://localhost:18180 -Cookie "eino_session=<token>"
```

**为什么不用 `Invoke-WebRequest`**：Windows PowerShell 5.1 没有 `-SkipHttpErrorCheck`，
429 会直接抛异常，拿不到状态码与响应头。脚本改用 `curl.exe` 的 `-w`/`-D` 取两者，5.1 与 7 行为一致。

⚠️ **方案 5.2 的验收描述与令牌桶语义不符**，实测时以本节为准：

| 写法 | 实际语义 |
|---|---|
| 「第 31 次起返回 429」 | 桶容量 = `burst`（10），新 key **从满桶开始**（首次请求不该被拦）。因此**连续快速请求下第 11 次起就是 429**；长期平均才是 `per_minute`（30/分钟） |
| 「`burst` 内允许突发」 | 正确。桶满时前 `burst` 次放行 |

也就是说 `per_minute: 30 / burst: 10` 的含义是「**长期最多 30 次/分钟，允许一次性突发 10 次**」，
不是「先放 30 次再放 10 次」。方案原文的「第 31 次」是把 burst 当成了额外额度，
按令牌桶实现拿不到这个数——除非把 `burst` 设成 40，但那等于允许 40 次瞬时突发，与「配额」意图相反。

**实测结果**（`data/verify/config.yaml`，18183，40 次）：

```
[ratelimit] 2xx=0 other4xx5xx=15 throttled=25 first_throttle=13 retry_after_missing=0
[ratelimit] status_by_index: 1:500 … 12:500 13:429 14:429 … 20:500 21:429 … 38:500 39:429 40:429
[ratelimit] sample 429 body: {"error":"请求过于频繁，请稍后再试"}
[ratelimit] sample Retry-After: 2
[ratelimit] OK: 限流行为与预期一致
```

`first_throttle=13` 而不是 11，是**补充速率**造成的：每 2 秒（30/分钟）补 1 个令牌，
12 次请求耗时约 5 秒 → 中途补回约 2 个 → 实际放行 12 次。下标 20、38 处零星出现的 2xx/5xx
是同一原因（那一刻刚好补出一个令牌）。所以**「第一次被拒的下标」不是固定值**，
它取决于单次请求耗时；脚本因此只断言「不能是第 1 次」（桶必须从满的开始），
不断言上界——真打到模型时单次耗时会大得多，硬编码上界必然误报。

⚠️ **脚本自身的一个 bug（已修）**：请求体原本写的是 `{"message": …}`，
而 `POST /chat` 的字段是 **`query`**（`internal/server/http.go` 的 `chatRequest`）。
后果是**放行的每次请求都被 400 挡在 handler 之外**。限流结论仍然成立
（429 在进入 handler 之前就产生了），但脚本并没有在测真实聊天路径,已改成 `{"query": …}`，
修正后放行的请求返回 500（验证配置里模型指向死端口，是预期行为）。

### 14.2 C23 备份与恢复演练：挖出 4 个缺陷 ✅

`make backup` 现在能产出完整备份（`mysql.sql` + 3 个 Milvus 卷包 + 含行数摘要与 SHA256 的
`manifest.txt`）。从「脚本看起来写完了」到「真能跑」，中间踩了 **4 个坑**：

**缺陷 1（阻塞）：`mysqldump` 需要全局 `FLUSH_TABLES`，而应用账号只有 DML。**
```
mysqldump: Couldn't execute 'FLUSH /*!40101 LOCAL */ TABLES': Access denied;
you need (at least one of) the RELOAD or FLUSH_TABLES privilege(s) for this operation (1227)
```
MySQL 8.0.21+ 起 `--single-transaction` 会先执行 `FLUSH TABLES` 取一致性快照，需要全局权限。
本机 mysqldump 是 **9.7.1**，所以「升级客户端」这条路不通。
**修法：新建专用最小权限账号 `eino_backup`，不动应用账号**（应用账号每天被服务使用，
为一次备份给它挂全局权限等于长期扩大攻击面）：

```sql
CREATE USER 'eino_backup'@'%' IDENTIFIED BY '<口令>';
GRANT FLUSH_TABLES, SHOW_ROUTINE ON *.* TO 'eino_backup'@'%';           -- 比 RELOAD 窄
GRANT SELECT, SHOW VIEW, TRIGGER, EVENT, LOCK TABLES ON `eino`.* TO 'eino_backup'@'%';
GRANT ALL PRIVILEGES ON `eino_restore_check`.* TO 'eino_backup'@'%';    -- 恢复演练的临时库
```
凭据放 `.env` 的 `MYSQL_BACKUP_USER` / `MYSQL_BACKUP_PASSWORD`（脚本优先用它，缺失时回落到
应用账号并打印 WARNING）。同时给 mysqldump 加 `--no-tablespaces`（避免再要 PROCESS 权限）。

**缺陷 2（阻塞）：服务器开了 GTID，dump 里带特权语句，恢复必然失败。**
dump 第 18 行是 `SET @@SESSION.SQL_LOG_BIN= 0;`、第 24 行是 `SET @@GLOBAL.GTID_PURGED='…:1-4586';`，
恢复时报 `ERROR 1227 ... need SUPER, SYSTEM_VARIABLES_ADMIN or SESSION_VARIABLES_ADMIN`。
**修法：mysqldump 加 `--set-gtid-purged=OFF`**。这不只是绕权限——`GTID_PURGED` 记的是**源库**的
GTID 集合，本来就**不该**灌进别的库；备份的目的是「能恢复到任意目标库」。

**缺陷 3：打包器镜像写死 `alpine`（= `alpine:latest`），拉不到 Docker Hub 就直接失败。**
本机实测 `failed to resolve reference "docker.io/library/alpine:latest": … EOF`，
而 `alpine:3.22` 其实早就在本地。
**修法：从 `docker images` 里按优先级挑本地已有的打包器 + `--pull=never`**；
一个都找不到时**明确失败**，不静默跳过卷备份（一份号称成功、实则没有 Milvus 数据的备份
比没有备份更危险）。

**缺陷 4：备份失败会留下一个「名字像备份、内容是空」的目录。**
目录名就是时间戳，`restore-check` 按名字挑最近一份 → 下次演练会挑中这个空目录，
报「manifest 不存在」，把「这次 dump 失败了」误报成「备份格式不对」。
**修法：两处都加固**——`backup.ps1` 在 `manifest.txt` 写成功之前失败时**删掉目标目录**；
`restore-check.ps1` 只认**有 `manifest.txt` 的**目录，跳过其余并打印 WARNING。

### 14.3 C24 健康检查：容器里的 `/health/ready` 是 401 —— 根因是「重建了但产物没换」

实测容器 `/health` 200、`/health/ready` **401**。代码侧 `GET /health/ready` 是直接
`mux.HandleFunc` 注册的、没包 `protected`，理应免认证；`internal/server/auth.go` 的白名单
注释也已列出它。第一轮排查只到「镜像旧了，重建一次就好」，**重建后仍然 401**。

⚠️ **真正的原因是构建缓存复用了旧的编译产物**：`docker compose ... up -d --build app`
**报告了 `Image ... Built`、退出码 0，但镜像里的二进制是旧的**。判定方法不是看构建日志，
而是**查产物本身**：

```bash
# 容器里的二进制有没有这条路由的字符串
docker exec my-eino-app-app-1 sh -c 'grep -c "health/ready" /app/eino-server'   # 旧的 → 0
# 对照：确认 grep 本身有效（该字符串必然存在）
docker exec my-eino-app-app-1 sh -c 'grep -c "GET /health" /app/eino-server'    # → 1
```

实测旧二进制里 `GET /health` / `GET /metrics` / `GET /skills` 都在、`health/ready` 计数为 **0**，
即该二进制编译自加入路由之前的源码。`docker compose build --progress=plain app` 再跑一次后，
镜像内二进制变为 2，`docker compose up -d --force-recreate app` 重建容器，路由随即生效。

> **教训（写进习惯）**：`Built` 只说明「构建流程走完了」，不说明「产物是新的」。
> 改完 `internal/` 下的代码，**用产物验证**（上面那条 `grep -c`，或直接 `curl /health/ready`），
> 不要用「构建成功」当验收。顺带一提，`--no-cache` 重建在本机**不可用**——
> `RUN apk add …` 会因 Docker Hub 网络问题报
> `SSL routines::unexpected eof while reading` 而失败，所以不能靠它兜底。

### 14.4 本机跑评测的两个环境前提

1. **必须独占跑。** 与备份/镜像构建并发时实测出现两类失败：
   - `fatal error: out of memory allocating heap arena map`（docker CLI 自己 OOM）；
   - `ollama embedding returned HTTP 500`。
   先跑完评测再做别的。
2. **Ollama 偶发 CUDA 崩溃，但框架会重试。** 日志里有
   `llama-server process has terminated: exit status 0xc0000409 ... CUDA error: shared object initialization failed`，
   随后 `model request failed; retrying in 1.11s (1/3)` 并成功。所以日志里出现它**不代表评测失败**，
   要看最终结论；但也说明这台机器的显卡不能承受并发压测。

### 14.5 评测暴露的两个「整轮失败」路径（已修）

第一轮 22 题实测里，有 **4 题整轮零字输出**，分两类，都值得记：

**A. 模型把技能名当工具名调用** → `[NodeRunError] tool report_writer not found in toolsNode indexes`。
第十三章的「把工具清单前置」只能**降低发生概率**，不能消除。真正的兜底是 eino 的
`ToolsNodeConfig.UnknownToolsHandler`（`v0.9.13` 提供）：把幻觉出来的工具调用**当作一次工具结果
交回模型**，模型在同一轮里就能改用 `load_skills`。
实现在 `internal/eino/agent/agent.go` 的 `unknownToolHint`，主 Agent 与两个子 Agent 都挂上，
提示里带上**真实可用工具名**与「技能名不是工具名」的指引。
> 一句话：措辞负责少发生，兜底负责发生时不致命。

**B. 工具执行报错也中止整轮** → `failed to invoke tool, toolName=local_file_read,
err=file is outside the allowed local directories or does not exist`。
评测题问 `README.md`，而 `workspace-files/` 里实际是 `README.txt`（这是**题目**的问题，
已改），但「模型把路径写错 → 用户拿到空回答」这个链路是真缺陷。
修法在 `internal/eino/agent/middleware.go` 的 `recoverableToolResult`：
只把**成因在输入**的错误（`outside_roots` / `not_found` / `not_text` / `not_regular_file` /
`too_large`）降级为交回模型的结果，并说明「这是输入问题，可以改正后重试；无法取得就如实说明，
不要编造」。**超时、用户取消、未知 `tool_error`、审批中断仍然向上抛**——
那些不是模型重试能解决的，掩盖只会让故障更难看。`ToolFailed` 事件照常发出，可观测性不受影响。

### 14.6 C20 实测结果：答案维度几乎满分，路由维度几乎为零

`make eval` 本机全量一次约 **15 分钟**（22 题 × 答案 + 路由两个维度）。两轮实测：

| 轮次 | 评测集 | 答案正确率 | 引用率 | 拒答率 | 路由准确率 | 整轮硬失败 |
|---|---|---|---|---|---|---|
| 第 1 轮（修复前） | 22 题 | — | — | — | **0.045**（1/22） | **4 题** |
| 第 3 轮（修复后） | 22 题 / 16 题带 `expect_skills` | **0.955**（21/22） | 1.0 | 1.0 | **0.0625**（1/16） | **0 条** |

**兜底修复是有效的**：两处 `[NodeRunError]` 硬失败全部消失；日志里能直接看到自愈过程——
模型先误调 `report_writer`，拿到 `unknownToolHint` 的提示后改成
`[tool] start name=load_skills args={"names":["report_writer"]}`。

**但路由准确率仍然是「基本不发生」**（0.045 → 0.0625，两轮一致，不是噪声）。结论要分两层看：

1. **这不是评测脚手架的问题。** 事件采集、覆盖式断言、`availableTools` 传参都已对齐服务端口径；
   唯一命中的那题（`report_writer`）证明整条链路是通的。
2. **这是模型行为的事实**：本地 `qwen3.5:9b` 不会主动做「先读技能说明再执行」这一步。
   它倾向于直接作答 —— 而且**答案是对的**（0.955），因为它有 `knowledge_search` 工具与基础
   提示词里的引用规则。也就是说，**在当前 7 个技能的内容下，不加载技能并不影响回答质量**。

⚠️ **测量口径的一个已知限制**：本轮是在 `config.yaml`（`debug: true`）下跑的，而 debug 分支的
`exposureRule` 明确写着「技能推荐……**不调用 `load_skills`**」。如果模型把执行类问题判成了
「推荐/咨询」意图，它就是在**遵守提示词**而不是失败。因此这个 0.0625 应理解为
「debug 模式下的路由行为」，**生产模式（`debug: false`）下的路由准确率尚未测量**，是明确的下一步。

**关于 4.2 的验收口径（本次修正）**：`expect_skills` 现在只用于**执行类请求**
（写报告 / 读文件分析 / 设计模板 / 操作工作簿等）；6 条**事实问答**题不再带它。
理由是那 6 题的正确答案本来就是「用 `knowledge_search` 取事实并给出引用」，
其正确性已由 `expected` / `must_cite` / `should_refuse` 三个断言覆盖（实测 6/6 通过、
引用率 1.0）。把「必须加载 `knowledge_qa`」当成它们的验收条件，会让指标反映的是一件
**不影响用户结果的事**。修正后 `routing_total` 从 22 变为 16。

> 附带结论：`knowledge_qa` 这个技能在当前提示词体系下**没有被使用到**（6/6 未加载），
> 但答案与引用完全达标。它要么是冗余的，要么需要与 `knowledge_search` 合并 —— 登记为待定项，
> 不在阶段 4 内处理。

### 14.7 第二轮实测（镜像修正后）：C21 / C22 / C23 / C24 全部通过 ✅

修掉 14.3 的构建缓存问题、并把容器换成新镜像后，四项验收一次性跑通。

| 项 | 实测 | 结论 |
|---|---|---|
| **C21 优雅停机** | `docker stop` 耗时 **2 秒**，`ExitCode=0`，`OOMKilled=false` | ✅ |
| **C22 限流** | 40 次 `POST /chat` → 放行 12、**429 共 25 次**、`Retry-After` 无缺失 | ✅ |
| **C23 备份** | 4 个产物（`mysql.sql` + 3 个 Milvus 卷包 + `manifest.txt`） | ✅ |
| **C23 恢复演练** | 4 个 SHA256 全对；dump 灌入临时库后 **16/16 张表行数一致**；临时库已 DROP | ✅ |
| **C24 健康分离** | 停 MySQL → `/health` **200**、`/health/ready` **503** 且点名 `mysql`；重启 MySQL → `/health/ready` 回到 **200** | ✅ |

**C21 的判读依据是退出码，不是日志。** `cmd/server` 在停机路径上不打任何日志，
所以「有没有走优雅路径」只能从进程结局反推：Go 运行时对**未被捕获**的 `SIGTERM`
走默认动作（进程被信号打死），`docker stop` 会记到 **143**；捕获后正常从 `main` 返回才是 **0**。
本次 `ExitCode=0` 且 2 秒即退出（远小于 `Shutdown` 的 10 秒上限，说明不是等到超时被 `SIGKILL`，
那会是 137）。**修复前的行为（只捕 `os.Interrupt`）在本机没有留档**，所以这里是正向证据，
不是对照实验。

`/health/ready` 的响应体是自解释的，运维能直接看出是哪一项不通：

```json
{"data":{"checks":[
  {"name":"mysql","ok":false,"latency_ms":2000,"error":"dial tcp: lookup mysql: i/o timeout"},
  {"name":"milvus","ok":true,"latency_ms":0},
  {"name":"ollama","ok":true,"latency_ms":8}],"status":"unavailable"}}
```

**顺带观察到一个良性但值得记的现象**：MySQL 停掉的 20 秒里，app 日志连刷 8 行
`memory worker: task failed`（06:50:50–06:51:09），MySQL 恢复后**自动停止**，
不需要人工干预。memory worker 的失败是「本轮跳过、下轮再取」，不是终态失败。

### 14.8 顺手修掉的第 5 个缺陷：后台任务失败日志把原因整条丢弃

上面那 8 行 `memory worker: task failed` 后面原本跟着的是 **`(details withheld)`** ——
`internal/memory/worker.go` 只打印这句话，**一个字符的原因都不留**。停库期间运维看到的
只有「失败了 8 次」，既不知道是数据库不通、还是模型超时、还是数据损坏，只能靠猜。
`memory maintenance failed` 同样如此。这行代码来自基线提交 `92b2833`，在任何文档里
都没有「刻意如此」的记录。

**修法**：抽一个 `logWorkerFailure(prefix, err)`，用项目既有的脱敏口径
（`observability.Redact`，与 `internal/eino/agent/middleware.go` 的工具失败事件一致）
打印错误，并**按 rune 截断到 300 字符**，避免一次异常把整段模型响应体灌进日志。

> 「脱敏后打印」和「整条丢弃」不是二选一；后者只是把风险换成了盲区。

### 14.10 方案自身的缺陷：`auth.admin_open_id` 已删除（这是方案漏写，不是执行漏做）

**现象**：`auth.admin_open_id` 在 `internal/config/auth.go` 有声明、三份配置都写了（都是空值），
但 `grep -rn AdminOpenID --include=*.go` **只命中声明本身，零使用点**。
而上线前最终检查里有一项是「管理员 `AdminOpenID` 已配置，本地后门可用」——
**33 个步骤里没有任何一步要求实现它**，这一项永远打不了勾。

**为什么删而不是补**：补实现等于新增一份「飞书侧管理员能做什么」的功能定义，方案里没有这个需求，
属于超范围。而且留着一个什么都不做的配置项**比没有更坏**——它让人以为后门存在。

**处置**（字段与两处清单项一起改，避免只改一半）：

| 原设计职责 | 现在实际由谁承担 |
|---|---|
| 历史数据归属 | `go run ./cmd/session-migrate -claim-owner=<owner>`（`internal/session/mysql.go` 的 `ClaimLegacySessions`） |
| 管理员标记 | `auth_local_users.is_admin` + `cmd/user-admin -admin` |

具体改动：删 `internal/config/auth.go` 的字段、三份 `config.*.yaml` 的 `admin_open_id` 行、
`EXECUTION-PLAN.md` 里的结构体示例；两处清单项（`EXECUTION-PLAN.md` 与 `RUNBOOK.md` 各一份，
是重复内容）都改成指向 `-claim-owner`。

**顺带修掉一个会误导人的示例**：C5 与 `EXECUTION-PLAN.md` 步骤 2.2 原文写的是裸 SQL
`UPDATE conversations SET owner_id = 'feishu:<admin_open_id>' WHERE owner_id = '';`。
裸 SQL 绕过 `ClaimLegacySessions`，而且它没提 owner 必须带 `feishu:` 前缀 —— 已换成 `-claim-owner` 命令。

**归档文档不改写**：`docs/AUTH-PLAN.md` 是设计阶段的证据链，只在两处加了「归档注记」说明最终实现
与原文的差异。其中 D 节「保留管理员后门」**只实现了一半**：`/auth/local` 的开关最终是独立的
`auth.local.enabled`，与 `AdminOpenID` 无关；「额外获得权限」**没有实现**。

> **遗留**：`auth_local_users.is_admin` 同样没有任何授权用途，只在 `user-admin -list` 里显示。
> 它不像 `admin_open_id` 那样暗示一个不存在的后门，先保留。

### 14.11 飞书凭据的接线缺口：compose 没有透传 `FEISHU_*`

**这是照着文档做会静默失败的一环。**

`docker-compose.milvus.yml` 的 `app.environment` 原先只透传 `SESSION_STORE` 与 `MYSQL_*`，
**没有 `FEISHU_APP_ID` / `FEISHU_APP_SECRET` / `FEISHU_REDIRECT_URL`**。

关键机制：**compose 读 `.env` 只用于变量插值，不会把变量注入容器。**
所以「按文档把凭据写进 `.env`」这一步在容器里读到的仍是空值。

后果不会报错：`ValidateAuth` 只在「飞书凭据不全 **且** `auth.local.enabled=false`」时拒绝启动。
本项目 `auth.local.enabled=true`，于是服务照常起来，只剩登录页**静默少了飞书按钮**。
实测证据 —— `/health` 回传的登录方式：

```json
{"data":{"debug":false,"login":{"feishu":false,"local":true},"status":"ok"}}
```

`feishu:false` 就是「凭据没进来」的唯一信号，除此外一切正常。

**修法**：在 `app.environment` 显式透传三个变量（`docker-compose.milvus.yml`），
并在 `config.docker.yaml` 与 `.env.example` 写明这个坑。

> 同类教训与 14.3 一样：**「配置写对了」不等于「运行态拿到了」**。
> 判读方式是看 `/health` 的 `login.feishu`，不是看 `.env` 里有没有值。

### 14.12 认证态补测：per-owner 限流 ✅

`config.yaml` 的 `auth.enabled` 是 `false`，所以之前 14.1 只测到了 **per-IP 回落路径**。
补测的做法是起一个**认证态的本地实例**（`data/verify-auth/config.yaml`，data/ 已 gitignore）：
`auth.enabled=true` + `auth.local.enabled=true` + `session.store=mysql`（`ValidateAuth` 强制），
模型仍指向死端口 —— 放行的那几次立刻失败返回，**429 在到达 handler 之前就产生，本来就不需要模型**。

**实验一：per-owner 计数生效**（`scripts/ratelimit-check.ps1 -Cookie ...`，40 次）

```
2xx=0 other4xx5xx=14 throttled=26 first_throttle=14 retry_after_missing=0
status_by_index: 1:500 ... 13:500 14:429 15:429 ... 40:429
sample Retry-After: 1
OK: 限流行为与预期一致
```

**实验二：同 IP 下两个 owner 的桶互不影响**（这是「per-owner」的定义，也是 `TestThrottleKeysByOwner`
的单测在运行态的对照）。先打空 admin 的桶，**紧接着**用另一个账号跑同一脚本：

| 阶段 | 结果 |
|---|---|
| A：`admin`（`local:8288b1c8-…`）40 次 | `throttled=26 first_throttle=14`，桶已打空 |
| B：`rlcheck`（`local:19677696-…`）紧接 40 次 | `throttled=26 first_throttle=14`，**又是从满桶开始** |

判读：如果限流 key 是 `ip:`，B 的第 1 次就该被拒，脚本会以「第 1 次请求即被拒 —— 新 key 未从满桶开始」
退出 1。**B 通过，即证明 key 是 `user:<owner>` 而不是 IP。**

> 验证用的 `rlcheck` 账号是临时建的，**已停用**（`user-admin -disable`），未删除——`user-admin` 没有删除操作。


### 14.13 生产模式（`debug: false`）路由准确率：0.125 —— 并且否证了原先的猜测

之前只有 `debug: true` 下的数（`routing_accuracy=0.0625`），并推测「debug 模式的
`exposureRule` 会劝模型别调 `load_skills`，所以这个数不能外推到部署态」。
本轮把 `config.yaml` 的 `debug` 临时翻成 `false` 跑完整一轮（22 题，15 分 19 秒），
**这个推测被否证了**：

| 指标 | `debug: true`（14.6） | `debug: false`（本轮） |
|---|---|---|
| `correct_rate`（旧口径，分母 22） | 0.9545 | 1.0 |
| `citation_rate` / `refusal_rate` | 1.0 / 1.0 | 1.0 / 1.0 |
| `routing_total` | 16 | 16 |
| **`routing_accuracy`** | **0.0625**（1/16） | **0.125**（2/16） |

⇒ **路由准确率低不是 debug 门禁造成的**，两种模式下都接近 0：本地 `qwen3.5:9b`
（Q4_K_M，9.7B）**基本不会主动调用 `load_skills`**。这与 14.6 的结论一致，现在有了
对照实验。

**路由失败的样子值得单独记下来**：模型不加载技能时，答案不是「不会」，而是
**装作会**——「请提供具体的文件路径」「请把您想分析的表格数据发给我」
「当前未接入 PDF 解析工具」。也就是说路由不准的直接用户可见后果是
**助手把该干活的事推回给用户**，而不是明确拒绝。

### 14.14 顺带挖出的评测缺陷：22 题里有 16 题的「正确」恒为真

上面那轮的全量答案里，`correct_rate=1.0`。但把它当成「答案质量满分」是错的。

判定逻辑在 `internal/evaluation/evaluation.go`：`Correct = strings.Contains(answer, expected)`，
而 **Go 的 `strings.Contains(x, "")` 恒为真**。16 道执行类题只声明 `expect_skills`、
不声明 `expected`（`expected: ""`），于是它们的 `Correct` **永远不可能失败**。

本轮实测的分布：

| | 题数 | 说明 |
|---|---|---|
| 有可判定断言（`should_refuse` 或 `expected` 非空） | **6** | 全是知识问答题 |
| 无断言（执行类：写报告 / 读文件 / 模板 / 表格 / 演示） | **16** | `correct` 结构性恒真 |
| 其中**直接拒答**却被判 correct 的 | **12** | 有一道耗时仅 **46ms**，没走完一次模型往返 |

所以旧口径的 `correct_rate=1.0` 里，16 个分子是白送的；真实可测的只有 6 道。
同理 `citation_rate` / `refusal_rate` 的分母本来就只数声明了 `must_cite` /
`should_refuse` 的题（6 道），那两项才是可信的。

**修法**（与同文件既有做法一致——`RunRouting` 本就「没有声明期望的题目既不执行也不计分」，
这次把同一条原则补到答案维度）：

- 新增 `Case.Asserted()`：`should_refuse` 或 `expected` 非空白才算「可判定」。
- 新增 `Report.AssertedTotal`，`CorrectRate` 的分母改成它。
- 新增 `CaseResult.Asserted`，逐题标出是否被真正判定，避免读到 `correct=false` 时误判为答错。
- 新增单测 `TestCaseAssertedRequiresAJudgementCriterion`（五种组合）。

> ⚠️ 这条改动**只可能让数字更保守，不会让任何指标变好**：本轮按新口径重算是 `6/6 = 1.0`，
> 与旧口径同为 1.0，但现在的分母是**能失败的**。重算是按逐题判定结果离线算的，
> 不是重跑；下一次 `make eval` 的 JSON 里会多出 `asserted_total` 字段。

**遗留**：16 道执行类题目前只有路由维度可判。要让答案维度也真正可测，需要给它们补
可判定的断言（例如「输出里必须出现报告结构的中文小标题」「不得以『请提供文件路径』结尾」）。
这是评测集的设计工作，登记为后续项。

### 14.15 仍未完成 / 需外部条件

| 项 | 状态 | 阻塞因素 |
|---|---|---|
| C25 飞书凭据 | ⏸ | 凭据**已写入**（三值非空、透传已验证），但 **App Secret 被判无效**（见 14.16）；只差换一个能用的 Secret |
| 生产模式路由准确率 | ✅ | 已测（14.13）：`0.125`，debug 与生产两态都接近 0 |
| **路由准确率本身** | ⏸ | 本地 `qwen3.5:9b` 不主动 `load_skills`；要提升得换模型或改提示词策略，另立项 |
| 16 道执行类题的答案断言 | ⏸ | 见 14.14 遗留：目前只有路由维度可判，需要补可判定的断言 |
| 备份定时任务（`schtasks`） | ⏸ | 命令需在**提权终端**执行（`/RL HIGHEST`），本环境无法代为创建 |
| `admin` / `admin123` 口令 | ⚠️ | 简单口令，**上线前必须改**：`go run ./cmd/user-admin -passwd -username=admin` |
| 多副本限流 / token 预算 | ⏸ | 方案明确不做，需换 Redis，另立项 |
| `knowledge_qa` 技能的存废 | ⏸ | 见 14.6 附带结论 |

### 14.16 飞书跑穿路上的判定点：`login.feishu=true` **不等于**凭据可用（已工具化）

**现象（本轮真实过程）**：凭据写入 `.env` → `login.feishu` 变 `true` → 授权页能打开 → 能授权 →
**回跳时炸**：

```
feishu exchange: http 400 {"error":"invalid_client",
  "error_description":"The client secret is invalid.","code":20002}
```

**根因**：`/health` 的 `login.feishu` 只断言「三个值非空」（`feishuReady()` 逐项判空），
**不校验值本身**。所以 App Secret 抄错或已失效时，除最后一步之外全链路没有任何异常信号。

**定位过程（可复用）**：直接拿凭据打 `tenant_access_token/internal`，该端点**区分失败维度**：

| 请求 | 返回 |
|---|---|
| 真 app_id + 假 secret | `10014 app secret invalid` |
| 假 app_id + 真 secret | `10014 app id not exists` |
| **本项目凭据** | `10014 app secret invalid` → **App ID 正确，Secret 无效** |

另一个易混点：本项目换 token 用的 `accounts.feishu.cn/oauth/v3/token` **先验 code、再验 client**
（拿假 code 打它返回 `invalid_grant` 而非 `invalid_client`），所以它**不能**当凭据校验器用 ——
校验凭据要用上面的 `tenant_access_token`。

**当时的初步判断（已被推翻，留作反面教材）**：凭据截图与本机 `.env` 逐字符一致，一度判定
「不是抄错，而是这个 Secret 已失效（可能点过重置）」。**这个判断是错的** —— 真因是末位把
`l`（小写 L）读成了 `I`（大写 i）：截图 OCR、肉眼读数、以及拿 `.env` 去「逐字符比对」，
**三处犯的是同一个错**，所以「两边一致」根本不能证明抄写无误。正解见 14.18。

**处置（已固化成三个脚本）**：

| 脚本 | 作用 |
|---|---|
| `scripts/feishu-check.ps1` | 五个观察点：**①凭据有效性** → `/health` → 入口 302 → 授权 URL 自洽 → 回调拒绝伪造 state |
| `scripts/feishu-set-secret.ps1` | 更新 `FEISHU_APP_SECRET`，**先验证再落盘**；飞书不认这个值就一个字节都不动 `.env` |
| `scripts/feishu-probe-secret.ps1` | 凭据被拒时定位「手抄误读形近字」：生成 40 余个形近字变体让飞书裁决，命中即给出正确值 |

更新的最短路径（三个动作，全程不用手抄）：

1. 飞书后台点 App Secret 右侧的**复制图标**
2. `powershell -File scripts/feishu-set-secret.ps1 -FromClipboard`
3. `docker compose -f docker-compose.milvus.yml up -d --force-recreate app`

`-FromClipboard` 直接读剪贴板（实测 32 字符原样传入），失败时给出成因；传空串则回退到交互式粘贴。
不用它也行：直接回车同样会读剪贴板。

**顺带记两个本机 / PowerShell 5.1 的坑**：

- `$PSScriptRoot` 在 `param()` 的**默认值表达式**里是空的（只在脚本体里有值），会在参数绑定阶段
  就抛 `Split-Path : Cannot bind argument to parameter 'Path' because it is an empty string`。
  凡是要按脚本位置定位文件，路径解析必须挪进脚本体。
- 改 `.env` 用「读原文 + 单行正则替换 + `New-Object System.Text.UTF8Encoding $false` 写回」，
  可保住其余行与混合行尾；正则要用 `[ \t]` 而不是 `\s`（多行模式下 `\s` 会跨行吃掉键名前的空行），
  行尾停在 `[^\r\n]*` 且**不带 `$`**（带 `$` 会吞掉 CRLF 的 `\r`）。实测结果：只改目标 1 行、
  CRLF/LF 计数不变、二次替换幂等。

### 14.17 「授权通过」不等于「登录完成」：回跳之后还有 6 步（失败页已改成人可读）

**现象**：飞书侧显示授权成功、地址栏也带着 `code=...` 回到 `/auth/callback`，但页面停在一屏错误。
用户会合理地认为「授权都通过了，为什么没进主页面」。

**机制**：飞书只回答「这个人是谁」。登录是否完成，取决于**我们服务端**在回跳之后做完这几步
（`internal/server/auth.go` 的 `handleAuthCallback`，顺序是硬的）：

| # | 动作 | 失败时 |
|---|---|---|
| 1 | 校验 `state` 与 Cookie | 400 |
| 2 | 取 `code`，空则拒 | 400 |
| 3 | **拿 code 去飞书换 token** ← 凭据无效时就断在这 | 502 |
| 4 | 拿 token 取用户信息 | 502 |
| 5 | 用户档案落库（注释写明「档案不阻断登录」） | — |
| 6 | 签发会话 + 种 Cookie | 500 |
| 7 | `http.Redirect(w, r, "/", http.StatusFound)` → 主页面 | — |

前 6 步任一失败，浏览器拿到的就是错误响应；**「进主页面」是最后一行，永远轮不到**。

**为什么刷新那一页没用（两条互不相关的机制）**：

1. 授权码寿命极短且一次性 —— 实测拿到后 3 分半已 `20004 The authorization code has expired.`；
2. `auth.ClearStateCookie(w)` 在**换 token 之前**执行 —— 成功路径清、**失败路径也清**。所以刷新
   同一 URL 必然得到 `400 state mismatch`。

> 别混淆 `20003`（`The authorization code is not found.`，码不存在/已用掉）与 `20004`
> （`The authorization code has expired.`，码还在、只是过期）。实测那次失败的换 token
> **并未消费掉授权码**，所以「失败即已消费」的说法不成立。

**改动**：`/auth/callback` 是本项目唯一「访问者一定是浏览器」的认证路由。失败时原先直接
`writeJSON` 吐裸 JSON，用户看到的是一屏 `{"error":"..."}` —— 既看不出断在哪一环，也没有出口，
唯一会做的动作是刷新（必然失败）。现改为 HTML 错误页（`renderCallbackError`）：

- 原始错误串保留（便于排查），但先经 `observability.Redact` 脱敏、再做 HTML 转义 ——
  它是**外部可控文本**（内嵌飞书返回的响应体）；
- 按错误特征给一句可执行的处置建议（`invalid_client` / `20071` / `20027` / `20010` / `20003` / `20004` / `state mismatch`）；
- 明确写出「刷新本页不会好」+ 一个「重新登录」按钮指向 `/auth/login`；
- **HTTP 状态码保持不变**（4xx/5xx 的语义仍准确），只换响应体。

回归测试见 `internal/server/auth_routes_test.go` 的三个新增用例：页面形态与出口、
外部文本的转义与脱敏、真实路由的 `Content-Type`。

### 14.18 真因落定：App Secret 末位是 `l`（小写 L），被读成了 `I`（大写 i）

14.16 当时判定「不是抄错、是这个 Secret 失效」——**错了**。最终定位到的真因是一条**形近字误读**：
控制台显示 `…R53lY`，写入 `.env` 的却是 `…R53IY`。`l` 与 `I` 在常见字体下几乎同形。

**为什么三处比对全都指向「没问题」**：截图 OCR、我的肉眼读数、以及「拿 `.env` 与截图逐字符比对」
**三者犯的是同一个错**。所以「两边一致」在"两边都是同一个人读的"前提下**没有证明力** ——
这是本轮最值得记住的一条方法论。

**定位手法（不需要人眼）**：拿候选的 App Secret 生成形近字变体，逐个打
`tenant_access_token/internal`，让飞书当裁判。实测 42 个候选里第 39 个命中，标签直接指出
`第 31 位 I -> l`。已固化为：

```
powershell -File scripts/feishu-probe-secret.ps1              # 检出（不写盘）
powershell -File scripts/feishu-probe-secret.ps1 -Write       # 检出并写回 .env
```

覆盖 `0/O/o`、`i/1/l/I`、`5/S`、`8/B`、`z/2`、`6/G`、`9/g` 与逐位大小写互换；只覆盖**单字符**误读。
若报 `app id not exists`，说明 App ID 本身不对，试 Secret 无意义（脚本会直接停）。

**教训（已写进 14.16 的最短路径）**：凭据永远不要从截图或纸面手抄 ——
控制台点**复制图标**，再跑 `feishu-set-secret.ps1 -FromClipboard`，让脚本先验证再落盘。

**顺带一个 PowerShell 5.1 的坑**：哈希表**字面量**（`@{ 'o' = ...; 'O' = ... }`）默认大小写不敏感，
`'o'`/`'O'`、`'i'`/`'I'`、`'g'`/`'G'` 会撞键并报
`Duplicate keys 'o' are not allowed in hash literals.`。形近字表必须用 `switch -CaseSensitive`，
或改用 `New-Object System.Collections.Hashtable`（其默认比较器是大小写敏感的）。

### 14.19 模型不调用工具却声称调用过；另有三处「中间态当成最终态」的前端缺陷

**A. 模型侧实测（2026-09-17，本地 `qwen3.5:9b`）**

同一句「现在几点？」，两个会话的工具调用行为**不一致**：

| 会话 | 事件序列 | 模型输出 |
|---|---|---|
| `1552e0e4`（浏览器） | chunk×70 → `tool_started(current_time)` → `tool_completed`(8ms) → chunk×25 | 先给出幻觉日期 `2024-07-16`，调完工具才自我纠正为真实时间 |
| `b90c2c52`（`cmd/console`） | chunk×43 → `run_completed`，**全程无 tool 事件** | 写「依据是调用 current_time 工具获取到的结果」，但时间是编的（19:10，实际 21:14） |

问「星河系统使用什么技术栈?」的会话 `3c3b1931` 同样**全程无 tool 事件**，模型却回答
「根据私有知识库查询结果，我目前无法找到关于⋯⋯的明确信息」。

**判据**：`execution_events` 里没有 `tool_started` 就是没调用。**模型的自我陈述不能作为证据** ——
它会明确写出「依据是调用 X 工具获取到的结果」，而那一轮根本没有工具事件。

**顺带否掉一个假说**（「是不是向量库空了」）：不是。

| 检查 | 结果 |
|---|---|
| `my_eino_knowledge` 实体数 | 2（`README.md-0`、`milvus-validation.md-0`） |
| `go run ./cmd/retrieve "星河系统的技术栈是什么"` | `milvus-validation.md-0` score=**0.6479** > `score_threshold: 0.50` |
| 该文档正文 | 含「系统名称：星河系统 / 技术栈：Go、Eino 和 Milvus」 |

即**库里有答案、检索也能命中，是模型没去查**。`current_time` 实现同样正确
（`time.Now().In(loc).Format(...)`，`internal/eino/tool/time.go`）。

结论：这不是代码缺陷，属模型路由能力，与 14.6 / 14.7 记录的 `routing_accuracy` 偏低同源。
代码侧只剩提示词可调，能否生效取决于模型；根治方向是换模型，或改为服务端按意图**强制预检索**
（不依赖模型自主调用），**尚未立项**。

**B. 三处前端缺陷（已修，提交 `891b2c0`）**

| 现象 | 根因 | 修法 |
|---|---|---|
| 回答结束后「正在整理回答…」一直不消失 | `setStreamStatus` 无条件把状态条显示出来；`openSession` 经 `replayExecution` 回放历史事件时把**已结束**的消息重新点亮，此后不再有任何事件关闭它 | `renderEvent` 增加 `live` 参数，状态条只由本轮实时事件驱动，回放路径不改写 |
| 正文里出现思考内容（表现为「先答错、再自我纠正」） | 模型只回吐 `</think>`（`<think>` 开标签由模板注入），「思考 + `</think>` + 正式回答」整段落在 `content` 里；`parseThink` 原本只处理成对标签与未闭合开标签 | 增加**孤立闭合标签**分支，其前内容归入思考块 |
| 侧栏出现只能显示成 UUID 的条目 | 点「新对话」即 `POST /sessions` 签发落库，用户没提问就离开便留下零消息空会话，标题回退成 session id | `list` 加 `HAVING COUNT(m.conversation_id)>0`；前端取不到首条用户消息的行直接移除；`startNewChat` 在当前会话还没有消息时复用而不新建 |

**验证方式**：`parseThink` 用临时脚本回放 6 个用例（含故障原文）全过；空会话过滤用等效 SQL 对照
（`1552e0e4`/`3c3b1931`/`e3db1304`/`81bb26d6` → 过滤后只剩前两条）。前端没有测试基建，
这类改动目前只能靠临时脚本加手动确认，**这是一处需要补的基础设施**。

### 14.20 「每个会话只能问一次」：事务钩子把记忆归属写成了配置里的 owner（已修）

**现象**（用户报「目前所有的会话都只能进行一次对话不能多次」）：第一轮问答正常，
第二轮无论走 HTTP 还是 WebSocket 都失败；浏览器侧表现为每轮提问都换成一个新会话。

**先看数据，不猜**：

| 查询 | 结果 |
|---|---|
| `conversations` 按消息数分组 | 每个会话**恒为 2 条**（1 问 1 答），从无第 3 条 |
| `execution_runs` 按 `conversation_id` 分组 | 每个会话**恒为 1 个 run**（`user_sequence=0`） |
| 同一会话连发两轮 `POST /chat` | 第一轮 200，第二轮 **HTTP 500 `memory session scope mismatch`，13ms** |

13ms 是关键信号：模型还没被调用，请求在准备阶段就失败了 —— 这不是模型问题、也不是前端问题。

**根因**：`session.Store` 的事务钩子在启动时只安装一次，闭包捕获的是
`memory.Repository` 的**配置副本**，于是 `Capture` 用 `memory.owner_id`（`local-owner`）
去写 `memory_bindings`；而第二轮进来时 `CheckBinding` 用的是登录态 owner
（`feishu:<open_id>` / `local:<uuid>`），两者不等 → 判归属不符并拒绝服务。

```
第一轮：bindings 里还没有这个 session → CheckBinding 返回 nil（放行）
        └─ Save 事务内 Capture 写入 binding，owner = local-owner  ← 埋雷
第二轮：CheckBinding 读到 local-owner ≠ 登录 owner → 500            ← 引爆
```

这也解释了为什么它躲过了所有既有验收：单元测试全绿、`/health` 正常、
`auth` 之后没人连着问过第二句。

**同一类缺陷还有 4 处**：`internal/server/memory.go` 的 4 条路由
（`GET /memory/facts`、`GET /memory/turns/{id}`、`DELETE /memory/facts/{id}`、
`POST /memory/jobs/{id}/retry`）直接用 `s.memories.Repo`，多用户下等于让 A 读、删 B 的记忆。

**修法**（沿用既有的「按请求身份派生副本」约定，不新增第二套身份来源）：

| 位置 | 改动 |
|---|---|
| `internal/session/mysql.go` | 钩子签名由 `(ctx, tx, id, msgs)` 改为 `(ctx, tx, id, owner, msgs)`，owner 由 `save` 逐次传入；类型层面就不允许钩子再自己找身份 |
| `internal/memory/coordinator.go` | 安装钩子时按 `e.Repo.For(e.OwnerScope(owner))` 派生；**新增 `Engine.OwnerScope`** 作为「请求身份 → 记忆归属」的唯一换算点 |
| `internal/memory/coordinator.go` | `OwnerScope`：`local_single_user` 一律返回配置 `owner_id`，`multi_user` 返回登录 owner，登录 owner 为空时**原样返回**（让归属校验去拒绝，不静默回退到配置身份） |
| `internal/server/service.go` | `CheckBinding` 与 `SetMemoryContext` 都改用 `OwnerScope(owner)` |
| `internal/server/memory.go` | 4 条路由改为按请求身份派生的 `memoryRepo(r)` |

`OwnerScope` 不是只为多用户写的：**单用户模式（`auth.enabled=false`）请求侧 owner 恒为空串**，
若不归一化，binding 记在 `""` 下、读写却走配置 owner，第二次请求同样被判归属不符 ——
也就是说本地 `make run` 的路径原本也是坏的，只是本地开发时同样没人连着问两句。

**数据清理**：`memory_bindings` / `memory_turns` / `memory_jobs` / `memory_locks` 里
8 条 `owner_id='local-owner'` 的残留会让**已经存在的会话**修完代码也仍然 500
（`Capture` 用的是 `INSERT IGNORE`，不会覆盖旧绑定）。处置：按 owner 删除，
**未触碰 `conversations` / `messages`**。

> ⚠️ **这次备份实际是失败的，不要把它当成成功案例**。`mysqldump` 报
> `LOCK TABLES` 被拒（1044）+ 缺 `PROCESS` 权限 + `column_masking_policy` 无 SELECT，
> 产物只有 999 字节、**0 条 `INSERT`**，而删除照旧执行了。可用留存只有
> `data/tmp/backup/local-owner-orphans.tsv`（被删行的 SELECT 明细）。
> 教训：**「有备份文件」≠「备份成功」**，破坏性操作前必须验证产物里真的有数据。
> 本库可用的导出参数见 14.21。

```bash
# 备份（可读 TSV）：data/tmp/backup/local-owner-orphans.tsv
docker exec -e MYSQL_PWD="$MYSQL_PASSWORD" -i my-eino-app-mysql-1 mysql -u"$MYSQL_USER" \
  -D "$MYSQL_DATABASE" --batch -e "SELECT * FROM memory_bindings WHERE owner_id='local-owner';"
# 清理（一个事务）
DELETE FROM memory_jobs     WHERE owner_id='local-owner';
DELETE FROM memory_facts    WHERE owner_id='local-owner';
DELETE FROM memory_turns    WHERE owner_id='local-owner';
DELETE FROM memory_bindings WHERE owner_id='local-owner';
DELETE FROM memory_locks    WHERE owner_id='local-owner';
```

**验证（四层，缺一层都不足以证明）**：

| 层 | 命令 / 断言 | 结果 |
|---|---|---|
| 单元 | `go test -p 1 ./internal/memory/` 的 `TestOwnerScope` | 4 个分支（单用户空/非空、多用户登录/空）全过 |
| 集成 | `MEMORY_INTEGRATION=1 go test -p 1 ./internal/memory/ -run TestHookBindsRequestOwner` | 通过；**反证**：把钩子改回「忽略 owner」后该测试立刻报 `memory session scope mismatch`，证明它抓得住这个缺陷 |
| 端点 | `node scripts/multiturn-check.mjs`（复刻前端顺序：`POST /auth/local` → `POST /sessions` → 两条 ws 带同一 id） | `same_session=true context_kept=true` |
| 落库 | 会话 `f0a31daa`：`messages=4` / `execution_runs=2` / `memory_bindings.owner_id=local:8288b1c8…`（登录 owner，不再是 `local-owner`） | 全部符合预期 |

**新增回归工具**：`scripts/multiturn-check.mjs`（Node 22+，用内置 `WebSocket`，无 npm 依赖）。
这条路径此前**完全没有任何测试覆盖** —— 缺陷恰恰只在这一步暴露，所以工具比结论更重要：

```bash
node scripts/multiturn-check.mjs                      # 默认 http://localhost:18180 / admin
node scripts/multiturn-check.mjs --base http://127.0.0.1:18181 --secret 41
```

**顺带修掉的前端泄漏**（`internal/server/web/app.js`）：每轮对话都 `new WebSocket(...)`，
但旧连接既不关闭也不回收（`finishGeneration` 只把 `state.socket` 置空）。服务端为每条连接
各留一个读循环与出站队列，长会话会一直堆积。现在发送前与结束后都走 `closeSocket()`。

---

### 14.21 「会话废了」的第二层：一次失败的回合会永久毒死这个会话（已修）

14.20 修完之后，**新建**的会话多轮正常，但**已经存在的**会话依然打不通。把
`multiturn-check.mjs` 加上 `--session <id>` 去续问一条历史会话，才暴露出来。

**现象**：对 `a55ae34b` 续问两次，都在 **1ms** 内返回 400（模型没被调用）：

```
[NodeRunError] error, status code: 400, message: invalid message content type: <nil>
node path: [node_1, ChatModel]
```

**根因**：一轮对话在调模型前会先落一条助手占位消息（`agent.go:beginReply`，
`storage_status=generating`，内容为空串），失败时 `finishReply` 把它标成 `failed`，
**内容仍是空串**。`load` 会原样把所有行读回历史，`CompactHistory` 再把它交给模型 ——
eino 不接受「既没有内容也没有工具调用」的助手消息，直接 400。

于是一个失败的回合会留下一条永久脏记录：**此后这个会话的每一轮都失败**，用户看到的就是
「这个会话废了」，而且新建会话不会复现。首轮被 14.20 那个缺陷快速失败过的会话
（44ms、模型没跑）全部落入这个状态。

**爆炸半径**（2026-09-18 实测）：`role='assistant' AND CHAR_LENGTH(content)=0` 共 **58 条、
status 全是 `failed`**，分布在 **56 个会话**里 —— `local:8288b1c8…` 42 个、
`local:19677696…` 14 个，**飞书账号 0 个**（用户真实浏览器会话没被污染）。

**修法**：只在 `CompactHistory`（`internal/session/session.go`）里过滤，不动数据库、
不动 `a.history`：

| 决策 | 理由 |
|---|---|
| 过滤放在 `CompactHistory` 而不是 `load` | `load` 的结果要用来做保存时的前缀比对（`save` 要求数组下标 == 库里的 `sequence`），少一条就报 `session changed`。`CompactHistory` 是「送模型」那一步，只影响输入 |
| 脏记录**保留在库里** | 它是「这一轮失败了」的唯一记录，前端要靠它显示失败；删数据能治症但丢信息 |
| 只剔助手消息，不剔用户消息 | 失败回合里用户问的那句是真实输入，该保留 |
| 判据是 Content + 多模态片段 + ToolCalls 全空 | 纯工具调用的助手消息是合法一轮，不能因 Content 为空就丢 |

改完等于**一次性修好全部 56 个存量会话**，不需要数据迁移。

**验证**：

| 层 | 命令 / 断言 | 结果 |
|---|---|---|
| 单元 | `go test -p 1 ./internal/session/ -run CompactHistory`（新增 5 条） | 全过：剔除空占位 / 保留工具调用 / 摘要不写空行 / 空白内容也剔 / 用户消息不误删 |
| 端点 | `node scripts/multiturn-check.mjs --session a55ae34b-…`（**就是那条被毒死的会话**） | `round 1=收到。` `round 2=41` `same_session=true context_kept=true` |
| 落库 | 同一会话：新增 `seq 6..9`（助手 3 字符 / 2 字符，均 completed），`execution_runs` 从 2 failed 变 2 failed + 2 completed | 符合预期 |
| 绑定 | `memory_bindings.owner_id` = `local:8288b1c8…`（登录 owner，不再是 `local-owner`） | 14.20 的修复也确认生效 |

**顺带定下的两件事**：

1. **`multiturn-check.mjs --session <id>`** —— 新会话多轮正常**不能**证明存量会话正常。
   回归时两种都要跑：新建（`create`）与续用（`reuse`）。
2. **本库可用的 `mysqldump` 参数**（14.20 那次备份失败的正解，实测 11 张表、174KB、exit 0）：

```bash
docker exec -e MYSQL_PWD="$MYSQL_PASSWORD" -i my-eino-app-mysql-1 \
  mysqldump -u"$MYSQL_USER" --no-tablespaces --single-transaction --skip-lock-tables \
    --set-gtid-purged=OFF --no-create-info --complete-insert "$MYSQL_DATABASE" \
    conversations messages memory_turns ... > backup.sql
grep -c "INSERT INTO" backup.sql   # 必须 > 0，否则备份是假的
```

`--skip-lock-tables`（避开被拒的 `LOCK TABLES`）+ `--no-tablespaces`（避开 `PROCESS`）
是关键；剩下的 `column_masking_policy` 报错只是警告，不影响数据。

### 14.22 Docker Desktop 冷启动：`Start-Process` 拉不起来，用 `docker desktop start`

机器重启后 Docker 引擎不在，容器 `Exited (255)`。用 PowerShell 的 `Start-Process` 打开
`Docker Desktop.exe` **会失败**：进程能短暂出现，但后端日志停在启动后 0.1s
（`com.docker.backend.exe.log` 只写到版本信息就没了），随后进程树被连同调用会话一起回收。
可用的路径是官方 CLI：

```bash
docker desktop start        # ✓ Starting Docker Desktop（约 25s 后引擎就绪）
docker compose -f docker-compose.milvus.yml up -d   # milvus/etcd/minio 需要单独补起
```

引擎重启后 `app`/`mysql` 会自动起来（restart 策略），但 `milvus`/`etcd`/`minio`/`attu`
停在 `Exited (255)`，要跑一次 `compose up -d` 补齐。**app 起来后会连续重试连 milvus
并打一堆 `Milvus Proxy is not ready yet`**，那是等待期噪音，不是故障；milvus healthy 后
`/health` 与 `/health/ready` 都会恢复。






