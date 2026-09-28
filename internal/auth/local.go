// local.go 提供项目自有账号的数据层（步骤 2.10 / 2.11 / 2.12）。
//
// 定位：内部场景下自有账号是「例外通道」而非主通道 —— 主通道是飞书。它服务两类人：
// 没有飞书账号的（外包、合作方、临时人员），以及飞书故障时的兜底。使用者少且可预期，
// 因此注册方式为「仅管理员创建」，建号走命令行（cmd/user-admin），不经 HTTP：
// 没有注册接口，也就没有被刷号的风险；忘记密码的解法是管理员重设而非自助找回。
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"

	"my-eino-app/internal/config"
)

// DefaultMinPasswordLength 与 config 的缺省值一致；调用方通常传
// cfg.Auth.Local.MinPasswordLength，这里只作为「传 0 时」的兜底。
const DefaultMinPasswordLength = 8

var (
	// ErrAccountNotFound 表示账号不存在。登录时**不可**把它与口令错误区分开返回给
	// 调用方（否则接口成了用户名枚举器），只在建号/改密等管理路径上使用。
	ErrAccountNotFound = errors.New("local account not found")
	// ErrUsernameTaken 表示用户名已被占用。
	ErrUsernameTaken = errors.New("local account username is already taken")
	// ErrInvalidCredentials 是登录失败的唯一对外错误：账号不存在、口令错误、
	// 账号被停用三种情况共用它，调用方与攻击者都无法据此区分。
	ErrInvalidCredentials = errors.New("invalid username or password")
)

// localUsernameRE 是用户名格式：3..32 位字母、数字或下划线。
//
// 必须拒绝含 ':' 等字符的名字：owner 形如 "<provider>:<subject>"，
// 一个叫 "feishu:ou_xxx" 的本地账号会让两种身份在命名空间上撞车。
var localUsernameRE = regexp.MustCompile(`^[a-zA-Z0-9_]{3,32}$`)

// ValidateUsername 校验用户名格式。
func ValidateUsername(username string) error {
	if !localUsernameRE.MatchString(username) {
		return fmt.Errorf("用户名只能由 3-32 位字母、数字或下划线组成")
	}
	return nil
}

// ValidatePassword 校验口令长度。
//
// 下限按字符数（用户能感知的长度），上限按字节数：PBKDF2 的开销随字节数增长，
// 128 字节足以覆盖任何正常口令，同时挡住把每次登录拖成 DoS 的超长输入。
func ValidatePassword(password string, minLength int) error {
	if minLength <= 0 {
		minLength = DefaultMinPasswordLength
	}
	if utf8.RuneCountInString(password) < minLength {
		return fmt.Errorf("口令至少需要 %d 个字符", minLength)
	}
	if len(password) > config.PasswordMaxLength {
		return fmt.Errorf("口令不能超过 %d 字节", config.PasswordMaxLength)
	}
	return nil
}

// LocalUser 是自有账号档案，不含口令哈希。
type LocalUser struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	IsAdmin     bool      `json:"is_admin"`
	Disabled    bool      `json:"disabled"`
	CreatedAt   time.Time `json:"created_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// LocalStore 是 auth_local_users 的读写入口，登录（步骤 2.12）与建号工具
// （步骤 2.11）共用同一套数据访问，避免两处各写一份 SQL 后行为漂移。
type LocalStore struct{ db *sql.DB }

// NewLocalStore 创建自有账号存储；db 为 nil 时所有方法都会明确失败，
// 而不是 panic（auth.local 关闭时调用方仍可能持有它）。
func NewLocalStore(db *sql.DB) *LocalStore { return &LocalStore{db: db} }

// Create 建号。用户名重复返回 ErrUsernameTaken，格式或口令不合规则返回校验错误。
func (s *LocalStore) Create(ctx context.Context, username, password, displayName string, admin bool, minPasswordLength int) (LocalUser, error) {
	if err := ValidateUsername(username); err != nil {
		return LocalUser{}, err
	}
	if err := ValidatePassword(password, minPasswordLength); err != nil {
		return LocalUser{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return LocalUser{}, err
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = username
	}
	user := LocalUser{ID: uuid.NewString(), Username: username, DisplayName: displayName, IsAdmin: admin}
	if err := s.mustDB(); err != nil {
		return LocalUser{}, err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO auth_local_users (id, username, password_hash, display_name, is_admin) VALUES (?, ?, ?, ?, ?)`,
		user.ID, user.Username, hash, user.DisplayName, admin)
	if err != nil {
		if isDuplicateKey(err) {
			return LocalUser{}, ErrUsernameTaken
		}
		return LocalUser{}, fmt.Errorf("create local account: %w", err)
	}
	return user, nil
}

// SetPassword 重设口令 —— 这就是「忘记密码」的解法（D15：不做自助找回）。
func (s *LocalStore) SetPassword(ctx context.Context, username, password string, minPasswordLength int) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	if err := ValidatePassword(password, minPasswordLength); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	if err := s.mustDB(); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE auth_local_users SET password_hash=? WHERE username=?`, hash, username)
	if err != nil {
		return fmt.Errorf("set local account password: %w", err)
	}
	return s.expectAffected(result)
}

// SetDisabled 停用或启用账号。停用后登录一律拒绝，且不向尝试者提示原因。
func (s *LocalStore) SetDisabled(ctx context.Context, username string, disabled bool) error {
	if err := ValidateUsername(username); err != nil {
		return err
	}
	if err := s.mustDB(); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE auth_local_users SET disabled=? WHERE username=?`, disabled, username)
	if err != nil {
		return fmt.Errorf("update local account state: %w", err)
	}
	return s.expectAffected(result)
}

// List 按用户名排序列出全部账号（不含口令哈希）。
func (s *LocalStore) List(ctx context.Context) ([]LocalUser, error) {
	if err := s.mustDB(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, display_name, is_admin, disabled, created_at, last_seen_at FROM auth_local_users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("list local accounts: %w", err)
	}
	defer rows.Close()
	var users []LocalUser
	for rows.Next() {
		var user LocalUser
		if err := rows.Scan(&user.ID, &user.Username, &user.DisplayName, &user.IsAdmin, &user.Disabled, &user.CreatedAt, &user.LastSeenAt); err != nil {
			return nil, fmt.Errorf("scan local account: %w", err)
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// ByUsername 返回账号档案与口令哈希，供登录校验使用。
// 账号不存在返回 ErrAccountNotFound。
func (s *LocalStore) ByUsername(ctx context.Context, username string) (LocalUser, string, error) {
	if err := s.mustDB(); err != nil {
		return LocalUser{}, "", err
	}
	var user LocalUser
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, display_name, is_admin, disabled, created_at, last_seen_at FROM auth_local_users WHERE username=?`,
		username).Scan(&user.ID, &user.Username, &hash, &user.DisplayName, &user.IsAdmin, &user.Disabled, &user.CreatedAt, &user.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return LocalUser{}, "", ErrAccountNotFound
	}
	if err != nil {
		return LocalUser{}, "", fmt.Errorf("load local account: %w", err)
	}
	return user, hash, nil
}

// TouchLastSeen 记录最近登录时间。失败不阻断登录：这只是审计信息，
// auth_local_users 在只读副本或权限收紧时不应该让人登不进来。
func (s *LocalStore) TouchLastSeen(ctx context.Context, id string) error {
	if err := s.mustDB(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE auth_local_users SET last_seen_at=CURRENT_TIMESTAMP(6) WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("touch local account: %w", err)
	}
	return nil
}

// dummyHashOnce 提供一个「口令必然不匹配」的合法哈希串。
//
// 账号不存在时也执行一次等价的口令校验，抹平「账号存在」与「账号不存在」的响应
// 时间差 —— 否则 21 万轮 PBKDF2 与立即返回之间的落差，足以让登录接口变成
// 用户名枚举器（这也正是第 ③ 条纪律想防的事）。惰性生成，避免每个 cmd 工具启动
// 都白付一次 PBKDF2 开销。
var dummyHashOnce = sync.OnceValue(func() string {
	hash, err := HashPassword("timing-equalizer-not-a-real-credential")
	if err != nil {
		return ""
	}
	return hash
})

// Login 校验用户名口令，返回该账号对应的登录身份（不含 token —— token 由
// auth.Sessions 统一签发）。失败一律返回 ErrInvalidCredentials。
//
// 四条纪律（步骤 2.12）：
//  1. 用户名先做格式校验再查库：含 ':' 的名字必须被拒，否则 "feishu:ou_xxx"
//     会与飞书身份在 owner 命名空间上撞车；
//  2. 口令长度上下限：下限按配置，上限固定，防止超长输入把 PBKDF2 拖成 DoS；
//  3. 「账号不存在」与「口令错误」返回同一错误，且耗时相当；
//  4. disabled 一律拒绝且不提示「已停用」—— 停用状态不泄露给尝试者。
func (s *LocalStore) Login(ctx context.Context, username, password string, minPasswordLength int) (Session, error) {
	if ValidateUsername(username) != nil {
		return Session{}, ErrInvalidCredentials
	}
	if ValidatePassword(password, minPasswordLength) != nil {
		return Session{}, ErrInvalidCredentials
	}
	if err := s.mustDB(); err != nil {
		return Session{}, err
	}

	user, hash, err := s.ByUsername(ctx, username)
	if errors.Is(err, ErrAccountNotFound) {
		VerifyPassword(dummyHashOnce(), password)
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	if !VerifyPassword(hash, password) {
		return Session{}, ErrInvalidCredentials
	}
	if user.Disabled {
		return Session{}, ErrInvalidCredentials
	}
	// 审计信息，失败不影响登录结果。
	_ = s.TouchLastSeen(ctx, user.ID)
	return Session{
		Owner:    LocalOwner(user.ID),
		Name:     user.DisplayName,
		Provider: "local",
		IsAdmin:  user.IsAdmin,
	}, nil
}

func (s *LocalStore) mustDB() error {
	if s == nil || s.db == nil {
		return errors.New("local account store requires a mysql connection")
	}
	return nil
}

// expectAffected 把「没有命中任何行」翻译成 ErrAccountNotFound，
// 让命令行能区分「账号不存在」与「数据库报错」。
func (s *LocalStore) expectAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows: %w", err)
	}
	if affected == 0 {
		return ErrAccountNotFound
	}
	return nil
}

// isDuplicateKey 判断是否为唯一索引冲突（MySQL 1062），用于把「用户名已存在」
// 从其他写库错误里区分出来。
func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
