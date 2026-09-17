package session

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/go-sql-driver/mysql"
	"my-eino-app/internal/config"
)

// mysqlStore 保持现有会话 API，避免 HTTP 和命令行各实现一套存储逻辑。
type mysqlStore struct {
	db   *sql.DB
	hook func(context.Context, *sql.Tx, string, []*schema.Message) error
}

// SetTransactionHook is installed once before serving requests. It shares the
// session transaction, so a completed message and its outbox job commit together.
func (s *Store) SetTransactionHook(h func(context.Context, *sql.Tx, string, []*schema.Message) error) error {
	if s.mysql == nil {
		return fmt.Errorf("transaction hook requires mysql")
	}
	s.mysql.hook = h
	return nil
}

// Open 只连接已有数据库，不在应用启动时隐式创建数据库或升级表。
func Open(cfg *config.Config) (*Store, error) {
	if cfg.Session.Store != "mysql" {
		return New(cfg.SessionDir)
	}
	c := mysql.NewConfig()
	c.Net = "tcp"
	c.Addr = net.JoinHostPort(cfg.MySQL.Host, cfg.MySQL.Port)
	c.User, c.Passwd, c.DBName = cfg.MySQL.User, cfg.MySQL.Password, cfg.MySQL.Database
	c.ParseTime = true
	c.Timeout, c.ReadTimeout, c.WriteTimeout = 5*time.Second, 10*time.Second, 10*time.Second
	c.Params = map[string]string{"charset": "utf8mb4"}
	db, err := sql.Open("mysql", c.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(3 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect mysql: %w", err)
	}
	return &Store{mysql: &mysqlStore{db: db}}, nil
}

// Close 在命令结束或服务退出时释放数据库连接池；文件模式无需释放资源。
func (s *Store) Close() error {
	if s.mysql != nil {
		return s.mysql.db.Close()
	}
	return nil
}

// DB 暴露底层连接池，供执行记录存储复用同一实例；文件模式返回 nil。
func (s *Store) DB() *sql.DB {
	if s.mysql == nil {
		return nil
	}
	return s.mysql.db
}

// MigrationOptions controls which optional schemas are included in an explicit
// migration. The base session tables are always included. Keeping execution
// events optional matters for deployments whose application account has only
// DML permissions: a disabled feature must not make an otherwise usable
// session/memory migration fail on unrelated DDL.
type MigrationOptions struct {
	IncludeExecution bool
	IncludeMemory    bool
	// IncludeMemoryIndexes requires ALTER privilege and is therefore opt-in.
	// The application account normally has DML-only access.
	IncludeMemoryIndexes bool
	// IncludeAuth creates the login tables and adds conversations.owner_id.
	// Unlike the execution/memory schemas this is not a feature toggle: owner_id
	// is part of the session table itself and every session read/write filters on
	// it, so a missing column breaks the core path rather than one optional
	// subsystem. Callers therefore pass true whenever auth may ever be enabled.
	IncludeAuth bool
}

// Migrate is the backwards-compatible full migration entry point. Commands
// that know the active configuration should prefer MigrateWithOptions so a
// disabled optional subsystem does not require its tables or CREATE privilege.
func (s *Store) Migrate() error {
	return s.MigrateWithOptions(MigrationOptions{IncludeExecution: true, IncludeMemory: true, IncludeAuth: true})
}

// MigrateWithOptions is the explicit schema initialization entry point; the
// database itself must already have been created by an administrator.
func (s *Store) MigrateWithOptions(options MigrationOptions) error {
	if s.mysql == nil {
		return fmt.Errorf("migration requires SESSION_STORE=mysql")
	}
	// 版本化迁移按顺序执行；已部署的旧表只追加、不改写。
	migrations := []string{migrationSQL}
	if options.IncludeExecution {
		migrations = append(migrations, executionMigrationSQL)
	}
	if options.IncludeMemory {
		migrations = append(migrations, memoryMigrationSQL)
	}
	if options.IncludeAuth {
		migrations = append(migrations, authMigrationSQL)
	}
	for _, migration := range migrations {
		for _, statement := range strings.Split(migration, ";") {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			// Existing deployments often grant the application only DML. A
			// repeat migration should still succeed when every table already
			// exists; a missing table is reported clearly and still requires a
			// DBA/schema account to create it.
			if match := migrationTableRE.FindStringSubmatch(statement); len(match) == 2 {
				var exists int
				if err := s.mysql.db.QueryRow("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", match[1]).Scan(&exists); err != nil {
					return fmt.Errorf("check migration table %s: %w", match[1], err)
				}
				if exists > 0 {
					continue
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_, err := s.mysql.db.ExecContext(ctx, statement)
			cancel()
			if err != nil {
				return fmt.Errorf("migrate sessions: %w", err)
			}
		}
	}
	if options.IncludeMemoryIndexes && options.IncludeMemory {
		for _, idx := range []struct {
			table string
			name  string
			cols  string
		}{
			{table: "memory_turns", name: "idx_memory_turns_cleanup", cols: "owner_id,state,updated_at"},
			{table: "memory_jobs", name: "idx_memory_jobs_cleanup", cols: "owner_id,status,updated_at"},
			{table: "memory_decisions", name: "idx_memory_decisions_cleanup", cols: "created_at"},
			{table: "memory_sources", name: "idx_memory_sources_turn", cols: "turn_id"},
			{table: "memory_jobs", name: "idx_memory_jobs_target", cols: "kind,target,status"},
		} {
			var exists int
			if err := s.mysql.db.QueryRow("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=?", idx.table, idx.name).Scan(&exists); err != nil {
				return fmt.Errorf("check migration index %s: %w", idx.name, err)
			}
			if exists > 0 {
				continue
			}
			if _, err := s.mysql.db.Exec("ALTER TABLE " + idx.table + " ADD INDEX " + idx.name + " (" + idx.cols + ")"); err != nil {
				return fmt.Errorf("add migration index %s: %w", idx.name, err)
			}
		}
	}
	if options.IncludeAuth {
		if err := s.migrateAuthColumns(); err != nil {
			return err
		}
	}
	return nil
}

// migrateAuthColumns 补齐 conversations 的归属列与索引。
//
// ALTER 不能写进 004_auth.sql：迁移循环用 migrationTableRE 判断是否跳过，而该正则只匹配
// CREATE TABLE IF NOT EXISTS，ALTER 每次迁移都会重跑，第二次报 Duplicate column name。
// 因此这里按「先查 information_schema，再做」的方式手工幂等。
func (s *Store) migrateAuthColumns() error {
	for _, item := range []struct {
		table, name, ddl string
		column           bool
	}{
		{table: "conversations", name: "owner_id", column: true,
			ddl: "ALTER TABLE conversations ADD COLUMN owner_id VARCHAR(64) NOT NULL DEFAULT ''"},
		{table: "conversations", name: "idx_conversations_owner", column: false,
			ddl: "ALTER TABLE conversations ADD INDEX idx_conversations_owner(owner_id, updated_at)"},
	} {
		var exists int
		var err error
		if item.column {
			err = s.mysql.db.QueryRow("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=?", item.table, item.name).Scan(&exists)
		} else {
			err = s.mysql.db.QueryRow("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=?", item.table, item.name).Scan(&exists)
		}
		if err != nil {
			return fmt.Errorf("check auth migration %s: %w", item.name, err)
		}
		if exists > 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, err = s.mysql.db.ExecContext(ctx, item.ddl)
		cancel()
		if err != nil {
			return fmt.Errorf("apply auth migration %s: %w", item.name, err)
		}
	}
	return nil
}

var migrationTableRE = regexp.MustCompile(`(?is)^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+([a-zA-Z0-9_]+)\s*\(`)

// ClaimLegacySessions 把 owner_id 为空的历史会话一次性归属给指定 owner。
//
// 接入认证前写入的会话没有归属，多用户隔离上线后会变成「谁都看不到」的孤儿数据。
// owner 应带命名空间前缀（如 feishu:ou_xxx / local:<uuid>），与运行期写法保持一致。
func (s *Store) ClaimLegacySessions(owner string) (int64, error) {
	if s.mysql == nil {
		return 0, fmt.Errorf("claim legacy owner requires mysql")
	}
	if strings.TrimSpace(owner) == "" {
		return 0, fmt.Errorf("claim legacy owner must not be empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := s.mysql.db.ExecContext(ctx, "UPDATE conversations SET owner_id=? WHERE owner_id=''", owner)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// 初始版本使用独立消息行和完整 payload，避免工具字段在迁移时丢失。
// 新版本应追加迁移，不能通过修改已部署的 CREATE TABLE 语句升级旧表。
//
//go:embed migrations/001_sessions.sql
var migrationSQL string

// 002 增加执行记录表；事件通过外键级联删除，避免删除会话后留下孤儿行。
//
//go:embed migrations/002_execution.sql
var executionMigrationSQL string

//go:embed migrations/003_memory.sql
var memoryMigrationSQL string

// 004 增加登录认证表；conversations.owner_id 的 ALTER 在 Go 侧另做（见 migrateAuthColumns）。
//
//go:embed migrations/004_auth.sql
var authMigrationSQL string

func (s *mysqlStore) load(owner, id string) ([]*schema.Message, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 先判归属：会话存在但不属于该 owner 时返回明确的错误（调用方映射 403），
	// 与「不存在 → 空历史」区分开，便于前端给出正确提示。
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversations WHERE id=?", id).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	var owned int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversations WHERE id=? AND owner_id=?", id, owner).Scan(&owned); err != nil {
		return nil, err
	}
	if owned == 0 {
		return nil, ErrForeignSession
	}
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM messages WHERE conversation_id=? ORDER BY sequence", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*schema.Message
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var msg schema.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, err
		}
		result = append(result, &msg)
	}
	return result, rows.Err()
}

// save 用短事务锁定会话，只追加新消息或更新生成中的最后一条助手消息。
// 历史前缀不匹配时拒绝保存，防止旧快照覆盖已完成的聊天记录。
//
// 写路径同样要校验归属：原来的 INSERT IGNORE 遇到已存在的 id 会静默跳过，
// 于是知道别人 session id 的人可以继续往那个会话里追加消息。这里改成
// 「不存在则连同 owner 一起插入，已存在则锁行比对 owner」，不匹配即拒绝。
func (s *mysqlStore) save(owner, id string, messages []*schema.Message) error {
	if err := validateID(id); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// ON DUPLICATE KEY UPDATE id=id 是空更新：不改变已有行的 owner_id，
	// 但语句本身会拿到该行的锁，后续 SELECT ... FOR UPDATE 才有意义。
	if _, err = tx.ExecContext(ctx, "INSERT INTO conversations(id, owner_id) VALUES (?, ?) ON DUPLICATE KEY UPDATE id=id", id, owner); err != nil {
		return err
	}
	var existingOwner string
	if err = tx.QueryRowContext(ctx, "SELECT owner_id FROM conversations WHERE id=? FOR UPDATE", id).Scan(&existingOwner); err != nil {
		return err
	}
	if existingOwner != owner {
		return ErrForeignSession
	}
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM messages WHERE conversation_id=? ORDER BY sequence", id)
	if err != nil {
		return err
	}
	var old []*schema.Message
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var m schema.Message
		if err = json.Unmarshal(raw, &m); err != nil {
			rows.Close()
			return err
		}
		old = append(old, &m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(messages) < len(old) {
		return fmt.Errorf("session changed: refusing to truncate history")
	}
	// 未结束的请求不能被另一进程追加的新一轮抢占；不持有跨模型调用的事务。
	if len(old) > 0 && MessageStatus(old[len(old)-1]) == "generating" && len(messages) > len(old) {
		return fmt.Errorf("session has an unfinished generation")
	}
	for i, m := range messages {
		if m == nil {
			return fmt.Errorf("nil message at sequence %d", i)
		}
		raw, e := json.Marshal(m)
		if e != nil {
			return e
		}
		if i < len(old) {
			previous, _ := json.Marshal(old[i])
			if string(previous) == string(raw) {
				continue
			}
			if i != len(old)-1 || MessageStatus(old[i]) != "generating" || m.Role != schema.Assistant || old[i].Extra["storage_turn"] != m.Extra["storage_turn"] {
				return fmt.Errorf("session changed at sequence %d; reload before retry", i)
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO messages(conversation_id,sequence,role,content,status,payload) VALUES(?,?,?,?,?,?) ON DUPLICATE KEY UPDATE content=VALUES(content),status=VALUES(status),payload=VALUES(payload)`, id, i, string(m.Role), m.Content, MessageStatus(m), raw)
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE conversations SET updated_at=CURRENT_TIMESTAMP(6) WHERE id=?", id); err != nil {
		return err
	}
	if s.hook != nil {
		if err := s.hook(ctx, tx, id, messages); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MessageStatus 同时保存在 JSON 中，HTTP 查询与 MySQL 中看到的状态保持一致。
func MessageStatus(m *schema.Message) string {
	if v, ok := m.Extra["storage_status"].(string); ok {
		return v
	}
	return "completed"
}

// ownerOf 返回会话归属；会话不存在时 exists 为 false。
func (s *mysqlStore) ownerOf(id string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var owner string
	err := s.db.QueryRowContext(ctx, "SELECT owner_id FROM conversations WHERE id=?", id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return owner, true, nil
}

// list 只返回该 owner 的会话；owner 为空串时匹配未归属的历史数据（单用户模式）。
func (s *mysqlStore) list(owner string) ([]Info, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,COALESCE(SUM(OCTET_LENGTH(m.payload)),0),c.updated_at FROM conversations c LEFT JOIN messages m ON m.conversation_id=c.id WHERE c.owner_id=? GROUP BY c.id,c.updated_at ORDER BY c.updated_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Info
	for rows.Next() {
		var item Info
		if err = rows.Scan(&item.ID, &item.Size, &item.ModifiedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// delete 先按 owner 限定删除，再区分「不存在」与「归属他人」。
//
// DELETE ... AND owner_id=? 命中 0 行时无法分辨两种情况，所以补一次存在性查询：
// 确实不存在 → 返回 nil（接口幂等）；存在但不是该 owner → ErrForeignSession（403）。
// owner_id 在会话创建后不再变更，故这个两步判断不存在竞态。
func (s *mysqlStore) delete(owner, id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := s.db.ExecContext(ctx, "DELETE FROM conversations WHERE id=? AND owner_id=?", id, owner)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected > 0 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversations WHERE id=?", id).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return ErrForeignSession
	}
	return nil
}

// cleanupOlderThan removes expired conversations in small transactions. Memory
// records are maintained by the memory engine, so this method stays usable when
// the optional memory schema is disabled.
func (s *mysqlStore) cleanupOlderThan(age time.Duration, batchSize int) (int, error) {
	if age <= 0 {
		return 0, fmt.Errorf("cleanup age must be greater than zero")
	}
	if batchSize < 1 {
		batchSize = 500
	}
	cutoff := time.Now().Add(-age)
	var memoryEnabled int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='memory_turns'`).Scan(&memoryEnabled); err != nil {
		return 0, err
	}
	removed := 0
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return removed, err
		}
		query := `SELECT id FROM conversations WHERE updated_at < ? ORDER BY updated_at LIMIT ? FOR UPDATE`
		args := []any{cutoff, batchSize}
		if memoryEnabled > 0 {
			query = `SELECT c.id FROM conversations c WHERE c.updated_at < ? AND NOT EXISTS (SELECT 1 FROM memory_turns t JOIN memory_jobs j ON j.target=t.turn_id AND j.kind='extract' WHERE t.session_id=c.id AND j.status IN ('pending','running')) ORDER BY c.updated_at LIMIT ? FOR UPDATE`
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			tx.Rollback()
			return removed, err
		}
		ids := make([]string, 0, batchSize)
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				tx.Rollback()
				return removed, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			tx.Rollback()
			return removed, err
		}
		if len(ids) == 0 {
			tx.Rollback()
			break
		}
		for _, id := range ids {
			if memoryEnabled > 0 {
				if _, err = tx.ExecContext(ctx, `DELETE d FROM memory_decisions d JOIN memory_turns t ON t.turn_id=d.turn_id WHERE t.session_id=?`, id); err != nil {
					tx.Rollback()
					return removed, err
				}
				if _, err = tx.ExecContext(ctx, `DELETE j FROM memory_jobs j JOIN memory_turns t ON t.turn_id=j.target WHERE j.kind='extract' AND t.session_id=?`, id); err != nil {
					tx.Rollback()
					return removed, err
				}
				if _, err = tx.ExecContext(ctx, `DELETE FROM memory_turns WHERE session_id=?`, id); err != nil {
					tx.Rollback()
					return removed, err
				}
				if _, err = tx.ExecContext(ctx, `DELETE FROM memory_bindings WHERE session_id=?`, id); err != nil {
					tx.Rollback()
					return removed, err
				}
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM conversations WHERE id=?`, id); err != nil {
				tx.Rollback()
				return removed, err
			}
		}
		if err = tx.Commit(); err != nil {
			return removed, err
		}
		removed += len(ids)
		if len(ids) < batchSize {
			break
		}
	}
	return removed, nil
}
