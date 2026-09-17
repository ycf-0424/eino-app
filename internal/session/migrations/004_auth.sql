-- 004：登录认证相关表。
--
-- 本文件只放 CREATE TABLE IF NOT EXISTS —— 迁移循环用 migrationTableRE 判断是否跳过，
-- 该正则只匹配这种形式，因此本文件天然幂等。ALTER 语句一律不写在这里（每次都会重跑，
-- 第二次直接报 Duplicate column name），改在 mysql.go 的 Go 侧用 information_schema 先查再做。
--
-- 排序规则统一用 utf8mb4_bin：owner / username 都是大小写敏感的不透明标识
-- （飞书 open_id 是 base62，本地用户名允许大小写），默认的 *_ci 会把 Admin 与 admin
-- 视为同一个值，既会让唯一索引误判重名，也会让登录查询匹配到错误的行。
CREATE TABLE IF NOT EXISTS auth_users (
 open_id VARCHAR(64) PRIMARY KEY, union_id VARCHAR(64) NOT NULL DEFAULT '',
 name VARCHAR(128) NOT NULL DEFAULT '', avatar_url VARCHAR(512) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 last_seen_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS auth_sessions (
 token_hash CHAR(64) PRIMARY KEY, owner VARCHAR(64) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 expires_at DATETIME(6) NOT NULL,
 INDEX idx_auth_sessions_expiry(expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
