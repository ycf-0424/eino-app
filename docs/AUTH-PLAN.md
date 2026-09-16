# 飞书登录 + 多用户隔离 改造清单

> ⚠️ **本文已归档为历史记录，不再是执行依据。**
> 合并后的**唯一执行来源**是 `docs/EXECUTION-PLAN.md` —— 本文的 A–I 各节对应其**阶段 2 的步骤 2.1–2.9**，验收清单对应步骤 2.10。
> 本文保留价值：逐行证据、六个坑的完整论证、飞书接口的三个调用细节。**执行时请以 EXECUTION-PLAN 为准。**
> 一处已解决的悬空项：G 节把「session id 是否改由服务端生成」标为待确认，最终核定**前端确实依赖自己生成 id**（`app.js:452`），因此采纳服务端生成并配套改前端 —— 见 EXECUTION-PLAN 步骤 2.7 与决策 D11 附近说明。

> 目标：把当前「配置文件里的单用户」改为「登录态里的当前用户」，使企业内部多人可安全共用同一套服务。
> 本文所有行号基于改造前的代码，可直接跳转核对。

---

## 一、现状核实

### 1.1 认证：完全缺失

| 位置 | 事实 |
|---|---|
| `internal/server/http.go:38-73` | `Handler()` 注册 11 条路由，**无任何认证中间件** |
| `internal/server/memory.go:7-50` | 4 条 memory 路由，同样无认证 |
| 全项目 grep | 无 `Authorization` / `Cookie` / `Bearer` / `Session` 相关代码 |
| `internal/server/ws.go:21-28` | `upgrader.CheckOrigin` 只校验 Origin 同源，这是 CSRF 防护，**不是认证** |

### 1.2 会话：没有任何归属概念

| 位置 | 事实 |
|---|---|
| `internal/session/migrations/001_sessions.sql:1-6` | `conversations(id, updated_at, created_at)` — **无 user 字段** |
| `internal/session/migrations/001_sessions.sql:7-17` | `messages` 无 user 字段（靠 FK 关联 conversations） |
| `internal/session/session.go:166` | `List()` **无参数** |
| `internal/session/mysql.go:303` | `SELECT c.id,... FROM conversations c LEFT JOIN messages m ...` — **无 WHERE** |
| `internal/session/mysql.go:189` | `load()` 只按 `conversation_id` 查 |
| `internal/session/mysql.go:325` | `delete()` 只按 `id` 删 |
| `internal/server/http.go:125/134/175` | `handleSessions` / `handleGetSession` / `handleDeleteSession` **不校验归属** |

### 1.3 记忆：地基已有，owner 来源是静态配置 ✅ 好消息

| 位置 | 事实 |
|---|---|
| `003_memory.sql` 全表 | `memory_bindings` / `memory_turns` / `memory_facts` / `memory_jobs` / `memory_controls` **全部带 `owner_id`** |
| `003_memory.sql:4-6` | `memory_locks(owner_id PRIMARY KEY)` — 按 owner 天然分片，无需改 |
| `internal/memory/mysql.go:25-31` | `scope()` 取 `r.Config.OwnerID`（**静态**） |
| `internal/memory/mysql.go:39-52` | `CheckBinding()` **已有归属校验**：`owner != Config.OwnerID` → 报 `memory session scope mismatch` |
| `internal/server/service.go:299` | Chat 入口**已经调用** `CheckBinding(ctx, id)` |
| `internal/config/memory.go:59-61` | **硬性拒绝** `identity_mode != "local_single_user"`，报 `multi-user authentication is not implemented` |

**结论：记忆层的隔离机制已经写好了，缺的只是把 owner 从"配置"换成"登录态"。**

### 1.4 执行记录

| 位置 | 事实 |
|---|---|
| `002_execution.sql:1-11` | `execution_runs` 无 user 字段，但通过 `conversation_id` FK 关联 `conversations` |

→ **只要会话隔离做对，执行记录可通过归属校验保证隔离，无需加字段。**

### 1.5 Checkpoint

| 位置 | 事实 |
|---|---|
| `internal/checkpoint/store.go:44` | `filepath.Join(s.dir, id+".checkpoint")` — 按 session id 存文件 |
| `internal/server/service.go:418/486` | `LoadApproval(id)` **不校验归属** |
| `internal/server/http.go:85-87` | **session id 允许客户端传入**（仅在为空时生成 uuid） |
| `internal/server/ws.go:116/159` | WebSocket 同样接受客户端指定的 session_id，且帧内可覆盖 |

→ **这是越权的根源**：知道别人的 session id 就能读审批内容、恢复别人的执行状态。

### 1.6 依赖

`go.mod` **无 `golang.org/x/oauth2`**。飞书接口是标准 HTTP，建议手写（3 个调用），不引依赖。
已有可用组件：`github.com/google/uuid`（生成 state）、`github.com/joho/godotenv`（读 .env）。

---

## 二、改造范围总览

| 层 | 改动性质 | 文件数 | 关键处数 |
|---|---|---|---|
| 配置 | 新增 Auth 配置段 | 2 新增 1 改 | — |
| 数据库 | 新增 004 迁移 + Go 侧幂等 | 1 新增 2 改 | 6 列 |
| 认证模块 | **全新** | 新增 4-5 | — |
| 路由/中间件 | 加认证包装 + 4 条 auth 路由 | 1 | 11+4 条路由 |
| Session | 方法加 owner 参数 | 2 | 9 个调用点 |
| **Memory** | **OwnerID 来源改造** | **3** | **31 处引用** |
| Checkpoint | 加 owner 校验 | 2 | 7 个调用点 |
| Execution | handler 层校验归属 | 1 | 2 处 |
| 前端 | 登出 + 用户名 | 2 | — |

---

## 三、逐项改造清单

### A. 配置层

**新增 `internal/config/auth.go`**

```go
type Auth struct {
    Enabled      bool     `yaml:"enabled"`
    Provider     string   `yaml:"provider"`       // 仅支持 "feishu"
    AppID        string   `yaml:"app_id"`
    AppSecret    string   `yaml:"app_secret"`
    RedirectURL  string   `yaml:"redirect_url"`   // http://192.168.x.x:18180/auth/callback
    SessionTTL   Duration `yaml:"session_ttl"`    // 建议 12h
    CookieSecure bool     `yaml:"cookie_secure"`  // 内网 http 必须 false
    AdminOpenID  string   `yaml:"admin_open_id"`  // 管理员后门
}
```

**`internal/config/config.go` 改动**

1. `Config` struct 加 `Auth Auth \`yaml:"auth"\``
2. `Validate()` 里仿照 `config.go:171-174` 的 map 写法读取环境变量：
   ```go
   for key, dest := range map[string]*string{
       "FEISHU_APP_ID":     &c.Auth.AppID,
       "FEISHU_APP_SECRET": &c.Auth.AppSecret,
       "FEISHU_REDIRECT_URL": &c.Auth.RedirectURL,
   } { if value, ok := os.LookupEnv(key); ok { *dest = value } }
   ```
3. 新增 `ValidateAuth()`：
   - `Enabled` 时 `AppID` / `AppSecret` / `RedirectURL` 必填，否则启动失败（清晰失败优于静默）
   - `Provider` 只允许 `feishu`
   - ⚠️ **`Enabled` 时强制 `Session.Store == "mysql"`**（理由见坑 5）

**`.env.example` 追加**

```
# 飞书自建应用凭据（开发者后台 → 凭证与基础信息）
FEISHU_APP_ID=
FEISHU_APP_SECRET=
FEISHU_REDIRECT_URL=http://localhost:18180/auth/callback
```

---

### B. 数据库迁移

**新增 `internal/session/migrations/004_auth.sql`**

```sql
-- 会话归属。历史数据默认空串，由管理员后门或迁移脚本归属到初始 owner。
ALTER TABLE conversations ADD COLUMN owner_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE conversations ADD INDEX idx_conversations_owner(owner_id, updated_at);

-- 登录态用户表（缓存飞书资料，避免每次请求都调飞书）
CREATE TABLE IF NOT EXISTS auth_users (
  open_id     VARCHAR(64)  PRIMARY KEY,
  union_id    VARCHAR(64)  NOT NULL DEFAULT '',
  name        VARCHAR(128) NOT NULL DEFAULT '',
  avatar_url  VARCHAR(512) NOT NULL DEFAULT '',
  created_at  DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  last_seen_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 服务端登录态（也可用进程内 map 替代，见 C 节决策）
CREATE TABLE IF NOT EXISTS auth_sessions (
  token_hash  CHAR(64)     PRIMARY KEY,
  open_id     VARCHAR(64)  NOT NULL,
  created_at  DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  expires_at  DATETIME(6)  NOT NULL,
  INDEX idx_auth_sessions_expiry(expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

**⚠️ 坑 2：现有迁移机制不支持 `ALTER TABLE` 的幂等重跑**

`internal/session/mysql.go:113-138` 的迁移循环是这样工作的：

```go
for _, statement := range strings.Split(migration, ";") {
    if match := migrationTableRE.FindStringSubmatch(statement); len(match) == 2 {
        // 只有匹配到 CREATE TABLE IF NOT EXISTS <表> 才会走"存在即跳过"
    }
    // 其余语句一律直接执行
}
```

而 `migrationTableRE`（`mysql.go:167`）只匹配 `CREATE TABLE IF NOT EXISTS`。
`ALTER TABLE ADD COLUMN` **不匹配** → 每次跑迁移都会重新执行 → 第二次报 `Duplicate column name 'owner_id'`。

**解法**：仿照 `mysql.go:140-163` 处理索引的方式，在 Go 侧用 `information_schema` 先查再做。新增一个 `IncludeAuth` 选项：

```go
// MigrateWithOptions 里新增，与既有 IncludeMemoryIndexes 同构
if options.IncludeAuth {
    for _, col := range []struct{ table, name, ddl string }{
        {"conversations", "owner_id",
         "ALTER TABLE conversations ADD COLUMN owner_id VARCHAR(64) NOT NULL DEFAULT ''"},
        {"conversations", "idx_conversations_owner",
         "ALTER TABLE conversations ADD INDEX idx_conversations_owner(owner_id, updated_at)"},
    } {
        var exists int
        // 列用 information_schema.columns，索引用 information_schema.statistics
        ...
        if exists == 0 { db.Exec(col.ddl) }
    }
}
```

这样 004_auth.sql 里**只保留 `CREATE TABLE`**，两个 `ALTER` 移到 Go 侧。参照 `mysql.go:140-163` 的写法即可。

**历史数据归属**：已有的 `conversations` 行 `owner_id` 会是空串。建议加一条一次性 SQL 把它们归属给管理员 open_id：
```sql
UPDATE conversations SET owner_id = '<admin_open_id>' WHERE owner_id = '';
```
放在迁移后手动执行，或写进 `cmd/session-migrate`。

---

### C. 认证模块（全新 `internal/auth/`）

**`feishu.go` —— 三个调用**

| 步骤 | 请求 |
|---|---|
| 拼授权页 | `GET https://accounts.feishu.cn/open-apis/authen/v1/authorize?client_id={AppID}&redirect_uri={RedirectURL}&response_type=code&state={state}` |
| 换 token | `POST https://accounts.feishu.cn/oauth/v3/token`，body `{grant_type, client_id, client_secret, code, redirect_uri}` |
| 取用户信息 | `GET https://open.feishu.cn/open-apis/authen/v1/user_info`，header `Authorization: Bearer {user_access_token}` |

⚠️ **坑 4：网上大量教程还在用 `open.feishu.cn/open-apis/authen/v2/oauth/token`，该 v2 端点已被官方弃用。** 授权页域名也换成了 `accounts.feishu.cn`。照老教程写会直接失败。

⚠️ **授权码 5 分钟有效且只能用一次** —— `/auth/callback` 里拿到 code 必须**立即**兑换，不能先做其他 I/O。

**`session.go` —— 登录态存储**

两个可选方案：

| 方案 | 实现 | 优点 | 代价 |
|---|---|---|---|
| 进程内 map | `map[tokenHash]Session` + 定时清理 | 零 DB 依赖，重启即失效（安全上更保守） | 多副本部署不共享；重启后所有人重新登录 |
| MySQL `auth_sessions` | 上面的表 | 重启不掉线，多副本共享 | 每请求一次 DB 查询（可加内存 LRU 缓解） |

**内网单副本部署推荐进程内 map**，实现量小且重启重新登录在企业场景可接受。表结构仍建议建好，将来要切不用再迁移。

**`middleware.go` —— 认证中间件**

```go
func (m *Middleware) Authenticate(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        token := tokenFromCookie(r)      // Cookie 名如 eino_session
        sess, ok := m.sessions.Get(token)
        if !ok {
            // API 请求返回 401 JSON，页面请求 302 到 /auth/login
            if wantsHTML(r) { http.Redirect(w, r, "/auth/login", http.StatusFound); return }
            writeJSON(w, 401, ...)
            return
        }
        next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ownerKey{}, sess.OpenID)))
    })
}
```

**`identity.go` —— 取当前用户**

```go
type ownerKey struct{}

// OwnerFromContext 返回当前登录用户；未认证时返回空串。
func OwnerFromContext(ctx context.Context) string {
    if v, ok := ctx.Value(ownerKey{}).(string); ok { return v }
    return ""
}
```

**State 校验（CSRF）**：`/auth/login` 生成随机 state 写进短时 Cookie（`HttpOnly` + `SameSite=Lax`），`/auth/callback` 比对后立即清除。不校验 state 等于开放登录 CSRF。

**保留管理员后门**：`AdminOpenID` 对应的账号额外获得权限，且**在飞书不可用时保留一个本地账号密码入口**（`/auth/local`，仅在 `AdminOpenID` 为空或显式开启时注册）。理由：万一飞书认证服务故障或管理员被挡在门外，系统仍可进入。

---

### D. 路由与中间件

**`internal/server/http.go` 改动**

新增 4 条路由：

| 路由 | 作用 |
|---|---|
| `GET /auth/login` | 302 到飞书授权页 |
| `GET /auth/callback` | code 换 token → 建登录态 → `Set-Cookie` → 302 到 `/` |
| `GET /auth/logout` | 清除登录态与 Cookie |
| `GET /auth/me` | 返回当前用户（供前端显示） |

`Handler()`（`http.go:38-73`）调整：

```
免认证：GET /auth/login、GET /auth/callback、GET /health
需认证：其余全部（/、/metrics、/skills、/chat、/sessions*、/ws、/memory*）
```

⚠️ **坑：`http.go:59-72` 的超时包装是最外层 `http.HandlerFunc`。** 认证中间件要包在 mux 之内、超时包装之外，或明确决定顺序——否则 `/ws` 的特殊处理（`http.go:61` 跳过超时）会和认证逻辑打架。推荐结构：

```go
return http.HandlerFunc(func(w, r) {
    if isPublic(r.URL.Path) { mux.ServeHTTP(w, r); return }
    authMiddleware.Authenticate(mux).ServeHTTP(w, r)
})
```
但这样 `/ws` 会丢掉超时豁免。更稳的做法是**把认证中间件放在 mux 内部**（每条路由单独包），或把 `/ws` 也纳入认证但在中间件里对 `/ws` 跳过超时。

⚠️ **`/ws` 的认证只能靠 Cookie** —— 浏览器 WebSocket API 无法自定义 Header。好在登录态本来就是 Cookie，天然可用。同时**保留 `ws.go:21-28` 的 `CheckOrigin`**，Cookie 认证 + Origin 校验两者都要。

---

### E. Session 隔离

**`internal/session/session.go` + `mysql.go` 方法签名加 owner**

| 方法 | 位置 | 改动 |
|---|---|---|
| `List()` | `session.go:166` / `mysql.go:300` | → `List(owner string)`；SQL 加 `WHERE c.owner_id=?` |
| `Load(id)` | `session.go:122` / `mysql.go:183` | → `Load(owner, id)`；SQL 加 `AND owner_id=?`，**无结果要区分"不存在"和"非我所有"** |
| `Save(id, msgs)` | `session.go:144` / `mysql.go:211` | → `Save(owner, id, msgs)`；见下方 ⚠️ |
| `Delete(id)` | `session.go:190` / `mysql.go:319` | → `Delete(owner, id)`；SQL 加 `AND owner_id=?` |
| `CleanupOlderThanBatch` | `session.go:212` / `mysql.go:332` | **保持不变**（全局运维清理，跨 owner） |

⚠️ **`save()` 的写路径要特别注意**（`mysql.go:222-224`）：

```go
tx.ExecContext(ctx, "INSERT IGNORE INTO conversations(id) VALUES (?)", id)
tx.QueryRowContext(ctx, "SELECT id FROM conversations WHERE id=? FOR UPDATE", id)
```

`INSERT IGNORE` 遇到已存在的 id 会静默跳过 —— 如果 A 的 session id 被 B 提交，B 能继续往 A 的会话里追加消息。必须改成：

```go
tx.ExecContext(ctx, "INSERT INTO conversations(id, owner_id) VALUES (?, ?) ON DUPLICATE KEY UPDATE owner_id = IF(owner_id = VALUES(owner_id), owner_id, NULL)", id, owner)
// 或用 SELECT ... FOR UPDATE 后显式比对 owner_id，不匹配直接返回错误
```

并新增归属校验：`SELECT owner_id FROM conversations WHERE id=? FOR UPDATE`，`owner_id != owner` → 返回 `errors.New("session belongs to another user")`。

**9 个调用点需要同步改**：

| 文件:行 | 调用 |
|---|---|
| `server/http.go:126` | `s.sessions.List()` |
| `server/http.go:135` | `s.sessions.Load(r.PathValue("id"))` |
| `server/http.go:177` | `s.sessions.Delete(id)` |
| `server/service.go:325` | `s.sessions.Load(id)`（newAgent 内） |
| `server/service.go:334` | `s.sessions.Save(id, messages)`（persistence 闭包）⚠️ **闭包要捕获 owner** |
| `server/service.go:339` | `s.sessions.Save(id, chat.History())` |
| `server/skill_catalog.go:42` | `s.sessions.Load(id)` |
| `server/skill_catalog.go:56` | `s.sessions.Save(id, history)` |

⚠️ `service.go:334` 的 `SetPersistence` 闭包注册在 `newAgent` 里，`owner` 必须从 `ctx` 取出后**捕获进闭包**，不能在里面再读一次 context（那时可能已被取消）。

**`service.go` 的方法签名**：`ChatWithSink(ctx, id, query, skillName, writer, sink)` 里 owner 从 `ctx` 取（`auth.OwnerFromContext(ctx)`），**不改公开签名**——这样 CLI（`cmd/console`、`cmd/eval`）不受影响，它们继续以单用户身份运行。

---

### F. Memory 隔离（工作量最大）

**核心事实：非测试代码里 `r.Config.OwnerID` 共 31 处**

| 文件 | 处数 |
|---|---|
| `internal/memory/mysql.go` | 15 |
| `internal/memory/worker.go` | 15 |
| `internal/memory/reindex.go` | 1 |

**推荐方案：`Repository.For(ownerID)` —— 只改一处，31 处自动生效**

`Repository.Config` 是**值类型**（`config.Memory`），所以可以安全地派生副本：

```go
// internal/memory/mysql.go
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

因为 `mysql.go` 的 15 处和 `worker.go` 的 15 处**都是读 `Config.OwnerID`**，`For()` 之后它们**全部自动拿到正确的 owner，一行都不用改**。

调用侧改造：

| 位置 | 改动 |
|---|---|
| `service.go:299` | `s.memories.Repo.CheckBinding(ctx, id)` → `s.memories.Repo.For(owner).CheckBinding(ctx, id)` |
| `service.go:332` | `chat.SetMemoryContext(s.memories.Context)` — 确认 `Context` 内部的 owner 读取路径，同样需要 `For(owner)` |
| `service.go:120` | `memory.Open(ctx, cfg, sessions, memoryModel)` — 返回的 Engine 保持"未绑定 owner"的原始 Repo，仅用于派生 |

**⚠️ 坑 1（最致命）：worker claim job 时按单一 owner 过滤，多用户下其他用户的记忆永远不会被抽取**

`internal/memory/mysql.go:168`：

```sql
SELECT id,kind,target,version,generation,attempts FROM memory_jobs
WHERE owner_id=? AND project_id=? AND attempts<? AND (...)
ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED
```

这个查询**写死了当前 owner**。多用户上线后：

> B 用户对话产生的抽取 job（`owner_id = B`）永远不会被 worker 捞出（worker 只查 `owner_id = A`）→ **B 的记忆功能静默失效，不报任何错**。

`mysql.go:163` 的 lease 超时回收同理。

**解法：claim 阶段去掉 owner 过滤，处理阶段用 job 自己的 owner**

1. `mysql.go:163/168` 的 SQL 去掉 `owner_id=?`（保留 `project_id` 或一并去掉）
2. `ProcessOne` 拿到 job 后，从 `job.owner_id` 取 owner，用 `Repo.For(job.owner_id)` 处理
3. job 表已有 `owner_id` 字段（`003_memory.sql:42`），无需改表

**⚠️ 同样的问题在 `Maintain()`（worker.go:257-540）**：快照统计、过期回收、保留期清理全部按 `Config.OwnerID` 过滤 → 多用户下**只清理默认用户的数据，其他人的数据无限增长**。

解法：maintenance 是**运维性质的全局操作**，直接**去掉 owner 过滤**：
- `worker.go:257/262/267` 快照统计去掉 `WHERE owner_id=?`
- `worker.go:301` 过期 fact 回收去掉 owner 过滤
- `worker.go:335-540` 全部保留期清理去掉 owner 过滤

**`internal/config/memory.go:59-61` 的硬校验要放开**

```go
// 现状：直接拒绝 multi-user
if m.IdentityMode != "local_single_user" {
    return fmt.Errorf("memory.identity_mode must be local_single_user; multi-user authentication is not implemented")
}
```

改为接受 `multi_user`，并**新增**该模式下的校验：
- `Auth.Enabled` 必须为 true（否则没有 owner 来源）
- `Session.Store` 必须为 mysql（已有 `memory.go:56-58` 覆盖）
- `OwnerID` 在 multi 模式下**不再使用**，建议启动时打一条日志说明它被忽略

**`memory_bindings` 与 `conversations.owner_id` 的关系**

两者都记录 session 归属，但语义不同，**都保留**：
- `memory_bindings(session_id, owner_id, project_id)` — 记忆引擎的内部绑定，含 project 维度
- `conversations.owner_id` — 会话归属的权威来源

`CheckBinding`（`mysql.go:39-52`）会校验 `memory_bindings` 与当前 owner 一致。B 用 A 的 session id 访问时，这里会先报 `memory session scope mismatch` —— **这是已经存在的第二道防线，改造后自动生效。**

---

### G. Checkpoint 隔离

**根因：session id 可由客户端指定**

| 位置 | 事实 |
|---|---|
| `server/http.go:85-87` | `if req.SessionID == "" { req.SessionID = uuid.NewString() }` — 非空时直接用 |
| `server/ws.go:116` | 从 query 取 `session_id` |
| `server/ws.go:159-161` | 帧内 `session_id` 还能再覆盖一次 |

→ 知道/猜到别人的 session id，就能读他的审批内容、恢复执行。

**推荐解法（两层）**

1. **服务端强制生成 session id**（首选，最彻底）
   - `http.go:85`：忽略客户端传入值，一律 `uuid.NewString()`
   - `ws.go:116/159`：同样忽略
   - 代价：前端不能再自己指定会话 id（需确认 `web/app.js` 是否依赖此能力，见 I 节）
2. **checkpoint 路径加 owner 分层**（兜底）
   - `checkpoint/store.go:40-44`：`path(id)` → `path(owner, id)`，返回 `dir/<owner>/<id>.checkpoint`
   - 所有方法签名加 owner

**7 个调用点**：

| 文件:行 | 调用 |
|---|---|
| `server/service.go:418` | `s.checkpoints.LoadApproval(id)` |
| `server/service.go:444` | `s.checkpoints.SaveApproval(id, ...)` |
| `server/service.go:486` | `s.checkpoints.LoadApproval(id)` |
| `server/service.go:503` | `s.checkpoints.SaveApproval(id, ...)` |
| `server/service.go:517` | `s.checkpoints.ClearApproval(id)` |
| `server/http.go:181` | `s.checkpoints.Delete(ctx, id)` |
| `server/http.go:182` | `s.checkpoints.ClearApproval(id)` |

`cmd/console/main.go:151/297/313/317` 是 CLI 单用户路径，可保持不动（传固定 owner 如 `"local-owner"`）。

---

### H. Execution 隔离

**无需改表。** `002_execution.sql:1-11` 的 `execution_runs.conversation_id` 已经 FK 关联 `conversations`，只要 E 节把 `conversations.owner_id` 做对，隔离可通过归属校验实现。

**`internal/server/http.go` 改动**

| 位置 | 改动 |
|---|---|
| `handleExecution`（`http.go:145`） | 查 execution 前先 `s.sessions.Load(owner, id)` 校验归属，不匹配返回 403 |
| `handleDeleteSession`（`http.go:175`） | `s.sessions.Delete(owner, id)` 已含校验，后续 `DeleteExecutions` 自然安全 |

（可选加固）给 `execution_runs` 加 `owner_id` 冗余列，使 execution 查询不依赖 join。当前规模不必。

---

### I. 前端

`internal/server/web/` 只有 4 个文件（`index.html`、`app.js` 37KB、`app.css`、`favicon.svg`），手写无构建。

| 改动 | 说明 |
|---|---|
| 登录跳转 | **服务端 302 即可，前端零改动**。未认证访问 `/` 时由中间件重定向到 `/auth/login` |
| Cookie 携带 | 同源请求浏览器自动带 Cookie，`app.js` 的 fetch **无需改动** |
| 401 处理 | `app.js` 收到 401 时应跳转登录页（防止会话过期后前端卡住） |
| 登出按钮 + 用户名 | `index.html` 加一个元素，调 `/auth/me` 填充、`/auth/logout` 登出 |
| **确认 session_id 依赖** | ⚠️ 若 `app.js` 依赖自己指定 session_id（如从 `location.hash` 恢复会话），G 节的"服务端强制生成"需要配套改成"会话列表由服务端返回、前端从列表选" |

---

## 四、六个必须处理的坑

| # | 坑 | 后果 | 位置 |
|---|---|---|---|
| 1 | **worker claim job 按单 owner 过滤** | 多用户下**其他用户的记忆静默失效**（最致命，无报错） | `memory/mysql.go:163/168`、`worker.go:257-540` |
| 2 | **`ALTER TABLE` 在现有迁移机制下不幂等** | 第二次迁移报 `Duplicate column name` | `session/mysql.go:113-138` |
| 3 | **只做查询过滤 ≠ 归属校验** | `Load`/`Delete`/`approval` 是"知道 id 就能操作"，必须显式比对 owner | E/G/H 各节 |
| 4 | **飞书 v2 token 端点已弃用** | 照老教程写直接失败；授权码 5 分钟一次性 | `auth/feishu.go` |
| 5 | **file 模式没有 owner 维度** | `SESSION_STORE=file` 时无法隔离 | 建议 auth 启用时**强制 mysql** |
| 6 | **session id 允许客户端指定** | 越权的根源 | `http.go:85`、`ws.go:116/159` |

**关于坑 5 的推论**：既然 `memory` 本来就要求 `session.store=mysql`（`config/memory.go:56-58`），而"给公司内部员工用"这个场景几乎必然开启记忆，那么**在 `Auth.Enabled` 时强制 mysql 是自然且一致的约束**，不必额外支持 file 模式的隔离。若确实需要 file 模式，则 `session.go:112` 的 `path()` 要改成 `dir/<owner>/<id>.json` 分层。

---

## 五、验收清单

准备两个飞书账号 A（普通）和 B（普通），交叉测试：

### 认证
- [ ] 未登录访问 `/` → 302 到 `/auth/login`
- [ ] 未登录调 `POST /chat` → 401（JSON，不是重定向）
- [ ] 完整登录流程走通，`Set-Cookie` 生效
- [ ] state 不匹配的回调被拒绝
- [ ] 重复使用同一个 code → 失败（验证一次性）
- [ ] 登出后 Cookie 失效

### 会话隔离
- [ ] A 建 2 个会话 → B 登录 → `GET /sessions` **看不到 A 的会话**
- [ ] B 拿 A 的 session id 调 `GET /sessions/{id}` → **403/404，不是 200**
- [ ] B 调 `DELETE /sessions/{id}` 删 A 的会话 → 403
- [ ] B 调 `POST /sessions/{id}/approval` 审批 A 的中断 → 403
- [ ] B 用 A 的 session id 发 `POST /chat` → 拒绝，且**不污染 A 的历史**

### 记忆隔离
- [ ] `GET /memory/facts` 只返回自己的 fact
- [ ] A 与 B 分别对话 → A 检索不到 B 提取出的记忆
- [ ] **A 与 B 的对话都能触发记忆抽取**（专门验证坑 1：查 `memory_jobs` 两张 owner 的行都应从 pending 变为 succeeded）
- [ ] maintenance 跑一轮后，**两个 owner** 的过期数据都被清理

### 执行记录
- [ ] A 的执行记录 B 查不到（`GET /sessions/{id}/execution`）

### 后门与容错
- [ ] 管理员账号登录成功且有额外权限
- [ ] 模拟飞书不可用时，本地管理员入口仍可登录
- [ ] CLII 工具（`cmd/console`、`cmd/eval`）在未配置 auth 时**行为不变**

---

## 六、建议实施顺序

分 5 个可独立验证的阶段，每阶段结束都能跑起来：

| 阶段 | 内容 | 完成后可验证 |
|---|---|---|
| 1 | A 配置 + B 迁移（`owner_id` 列就位） | 服务照常启动，`conversations` 多一列，旧功能不回归 |
| 2 | C 认证模块 + D 路由中间件 | **能登录**，但还没有隔离（此时仍单用户视角） |
| 3 | E Session 隔离（含 9 个调用点） | A/B 看不到彼此会话 |
| 4 | F Memory 隔离（`For()` + claim 改造）+ G Checkpoint | A/B 记忆互不可见，双方记忆都能抽取 |
| 5 | H Execution 校验 + I 前端 | 全部验收项通过 |

**阶段 1 之前务必先提交一次基线**——当前项目**没有 git 仓库**（`.git` 不存在），这批改动涉及约 20 个文件，没有版本控制无法回退。

```bash
cd E:/11/my-eino-app
git init && git add -A && git commit -m "baseline before multiuser auth"
```

---

## 七、与「框架收敛改造」的关系

> **本方案不含框架收敛改造。** 范围严格限定为「认证 + 多用户隔离」，不移动任何既有包的目录位置。

框架收敛指的是把 `agent` / `chain` / `model` / `prompt` / `rag` / `tool` / `observability` 收进 `internal/eino/` 一个父包，让业务包不再直接依赖 eino。这是**独立的第二件事**，与本方案分开做。

### 为什么不合并

1. **验证口径不同。** 本方案的验收是行为性的（登录成功、A 看不到 B 的会话），收敛改造的验收是"编译通过 + 行为零变化"。混在一起，一次测试失败无法归因是哪边引入的。
2. **收敛会批量重写 import 路径**，本节所有行号基准随之失效——本文档的直接跳转价值就没了。
3. 收敛改造**不影响本方案的正确性**，本方案也**不依赖**收敛改造。两者无前置关系。

### 实际重叠面很小：2 个文件

精确统计「引用了待收敛的 7 个包」的跨包文件后，与本方案改动清单交叉的只有：

| 文件 | 本方案的改动 | 收敛改造的改动 |
|---|---|---|
| `internal/server/service.go` | owner 流转、9 处调用点 | 4 行 import 路径 |
| `cmd/server/main.go` | 装配 auth 模块 | 1 行 import 路径 |

其余重叠为零：`session` / `checkpoint` / `config` / `memory` 都不引用那 7 个包（`memory` 的 embedding 模型是外部注入的，`memory/live_test.go` 除外）。

`cmd/console/main.go`（5 处 import）只在收敛时受影响，本方案明确不动它。

### 推荐顺序

**阶段 0（git init）→ 本方案 5 个阶段 → 框架收敛改造。**

理由：本方案是上线阻塞项（没有它连第二个人都不能用），收敛是结构优化。且收敛要重写大量 import，应在功能稳定的基线上做。

### 一条从现在起生效的约束

**新增的 `internal/auth/` 不得 import `cloudwego/eino`。** 它是纯 HTTP + OAuth + context 传递，没有任何理由依赖 eino。若在实现时顺手引入，就等于又制造了一个将来需要解耦的包——**这是本方案唯一需要为收敛改造让路的地方**。

### 一条对将来收敛有用的判定标准

不是"引用了 eino 就算污染"，要区分两种：

- **依赖 `cloudwego/eino/schema`（Message / Role 等数据类型）** → 不算污染。这是跨层共用的数据契约，`internal/session`（`session.go:14`、`mysql.go:14`）属于这一类，**收敛时不必动它**。
- **依赖 `compose` / `adk` / `components` 等编排与运行时机制** → 这才是要收拢的对象。

---

## 附：受影响文件清单（约 20 个）

**新增（6）**
- `internal/config/auth.go`
- `internal/auth/feishu.go`
- `internal/auth/session.go`
- `internal/auth/middleware.go`
- `internal/auth/identity.go`
- `internal/session/migrations/004_auth.sql`

**修改（14）**
- `internal/config/config.go`（Auth 段 + 环境变量 + 校验）
- `internal/config/memory.go`（放开 identity_mode）
- `internal/session/session.go`（5 个方法签名）
- `internal/session/mysql.go`（迁移幂等 + 4 处 SQL）
- `internal/memory/mysql.go`（`For()` + claim 改造）
- `internal/memory/worker.go`（maintenance 去 owner 过滤）
- `internal/memory/reindex.go`（若走 `For()` 则可不改）
- `internal/checkpoint/store.go`（路径分层 + 签名）
- `internal/server/http.go`（4 条 auth 路由 + 认证包装 + 归属校验）
- `internal/server/service.go`（owner 流转 + 9 处调用）
- `internal/server/ws.go`（session id 服务端生成）
- `internal/server/memory.go`（路由走 `For(owner)`）
- `cmd/server/main.go`（装配 auth 模块）
- `config.yaml` / `.env.example` / `internal/server/web/{index.html,app.js}`
