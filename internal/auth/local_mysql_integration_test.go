//go:build mysql_integration

package auth

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"my-eino-app/internal/config"
	"my-eino-app/internal/session"
)

// newLocalStore 在真实 MySQL 上准备 auth_local_users 访问；表不存在则跳过。
func newLocalStore(t *testing.T) (*LocalStore, context.Context) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Session.Store = "mysql"
	store, err := session.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	db := store.DB()
	if db == nil {
		t.Skip("mysql session store unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if _, err := db.ExecContext(ctx, "SELECT 1 FROM auth_local_users LIMIT 0"); err != nil {
		t.Skipf("auth_local_users is not installed, run make db-migrate: %v", err)
	}
	return NewLocalStore(db), ctx
}

// 建号 → 查回 → 口令哈希可验证 → 重设/停用生效：自有账号数据层的完整往返。
func TestLocalStoreLifecycle(t *testing.T) {
	accounts, ctx := newLocalStore(t)
	username := "it_admin_" + time.Now().Format("150405000")
	t.Cleanup(func() {
		if _, err := accounts.db.ExecContext(context.Background(), "DELETE FROM auth_local_users WHERE username=?", username); err != nil {
			t.Logf("cleanup %s: %v", username, err)
		}
	})

	created, err := accounts.Create(ctx, username, "Str0ng-Passw0rd!", "集成测试", true, 8)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Username != username || !created.IsAdmin {
		t.Fatalf("建号返回不正确: %+v", created)
	}

	user, hash, err := accounts.ByUsername(ctx, username)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != created.ID || user.DisplayName != "集成测试" || user.Disabled {
		t.Fatalf("账号档案不正确: %+v", user)
	}
	// 库里存的是自描述哈希，不是明文。
	if !VerifyPassword(hash, "Str0ng-Passw0rd!") {
		t.Fatalf("库里的哈希无法验证口令: %q", hash)
	}
	if hash == "Str0ng-Passw0rd!" {
		t.Fatal("库里存了明文口令")
	}
	// 格式必须是「方案$迭代次数$盐$key」：迭代次数随串存储，将来提高默认值后旧哈希仍可验证。
	if !strings.HasPrefix(hash, pbkdf2Scheme+"$"+strconv.Itoa(pbkdf2Iterations)+"$") {
		t.Fatalf("库里的哈希格式不符: %q", hash)
	}

	// 重设口令后旧口令失效。
	if err := accounts.SetPassword(ctx, username, "An0ther-Passw0rd!", 8); err != nil {
		t.Fatal(err)
	}
	_, newHash, err := accounts.ByUsername(ctx, username)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(newHash, "An0ther-Passw0rd!") || VerifyPassword(newHash, "Str0ng-Passw0rd!") {
		t.Fatal("重设口令后新旧口令的验证结果不正确")
	}

	// 停用 / 启用。
	if err := accounts.SetDisabled(ctx, username, true); err != nil {
		t.Fatal(err)
	}
	if user, _, err = accounts.ByUsername(ctx, username); err != nil || !user.Disabled {
		t.Fatalf("停用未生效: %+v %v", user, err)
	}
	if err := accounts.SetDisabled(ctx, username, false); err != nil {
		t.Fatal(err)
	}

	// 列表中能看到（且不含哈希列）。
	items, err := accounts.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range items {
		if item.Username == username {
			found = true
		}
	}
	if !found {
		t.Fatalf("账号未出现在列表中: %+v", items)
	}

	// 最近登录时间可写。
	if err := accounts.TouchLastSeen(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
}

// 唯一索引把「用户名已存在」翻译成 ErrUsernameTaken，而不是一句裸的 SQL 错误。
func TestLocalStoreDuplicateUsername(t *testing.T) {
	accounts, ctx := newLocalStore(t)
	username := "it_dup_" + time.Now().Format("150405000")
	t.Cleanup(func() {
		if _, err := accounts.db.ExecContext(context.Background(), "DELETE FROM auth_local_users WHERE username=?", username); err != nil {
			t.Logf("cleanup %s: %v", username, err)
		}
	})

	if _, err := accounts.Create(ctx, username, "Str0ng-Passw0rd!", "", false, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Create(ctx, username, "Str0ng-Passw0rd!", "", false, 8); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("重复用户名错误 = %v, want ErrUsernameTaken", err)
	}
}

// 管理路径要能区分「账号不存在」与「数据库报错」。
func TestLocalStoreMissingAccount(t *testing.T) {
	accounts, ctx := newLocalStore(t)
	missing := "it_missing_" + time.Now().Format("150405000")

	if _, _, err := accounts.ByUsername(ctx, missing); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("ByUsername 错误 = %v, want ErrAccountNotFound", err)
	}
	if err := accounts.SetPassword(ctx, missing, "Str0ng-Passw0rd!", 8); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("SetPassword 错误 = %v, want ErrAccountNotFound", err)
	}
	if err := accounts.SetDisabled(ctx, missing, true); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("SetDisabled 错误 = %v, want ErrAccountNotFound", err)
	}
}

// 格式与口令策略在写库之前生效：非法用户名不留下任何行。
func TestLocalStoreValidatesBeforeWrite(t *testing.T) {
	accounts, ctx := newLocalStore(t)

	if _, err := accounts.Create(ctx, "feishu:ou_injected", "Str0ng-Passw0rd!", "", false, 8); err == nil {
		t.Fatal("含冒号的用户名必须被拒绝")
	}
	if _, err := accounts.Create(ctx, "it_short_"+time.Now().Format("150405000"), "short", "", false, 8); err == nil {
		t.Fatal("过短的口令必须被拒绝")
	}
}
