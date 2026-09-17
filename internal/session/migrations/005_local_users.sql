-- 005：项目自有账号表（例外通道，仅管理员用 cmd/user-admin 建号）。
--
-- 与 004 一样只放 CREATE TABLE IF NOT EXISTS：迁移循环用 migrationTableRE 判断是否跳过，
-- 该正则只匹配这种形式，因此本文件天然幂等，不需要在 Go 侧做存在性预检。
--
-- 无邮箱 / 手机号字段：不做自助找回（D12），不收集用不上的个人信息。
-- id 用 uuid 而非自增：避免通过 id 暴露账号数量与建号顺序。
-- 排序规则沿用 utf8mb4_bin：用户名大小写敏感（Admin 与 admin 是两个账号），
-- 用默认的 *_ci 会让唯一索引把两者判为重复。
CREATE TABLE IF NOT EXISTS auth_local_users (
 id CHAR(36) PRIMARY KEY,
 username VARCHAR(32) NOT NULL,
 password_hash VARCHAR(255) NOT NULL,
 display_name VARCHAR(64) NOT NULL DEFAULT '',
 is_admin TINYINT(1) NOT NULL DEFAULT 0,
 disabled TINYINT(1) NOT NULL DEFAULT 0,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 last_seen_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE KEY uniq_auth_local_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
