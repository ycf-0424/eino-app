package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 用户名格式是 owner 命名空间的第一道闸门：含 ':' 的名字必须被拒，
// 否则 "feishu:ou_xxx" 这样的账号会和飞书身份撞在同一个 owner 上。
func TestValidateUsername(t *testing.T) {
	for _, valid := range []string{"abc", "admin_01", "A_1b", strings.Repeat("a", 32)} {
		if err := ValidateUsername(valid); err != nil {
			t.Errorf("合法用户名 %q 被拒: %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"",
		"ab",                      // 太短
		strings.Repeat("a", 33),   // 太长
		"feishu:ou_abc",           // 与飞书身份撞命名空间
		"local:1234",              // 与自有账号自身的 owner 形式撞
		"中文名",                     // 非 ASCII
		"a-b", "a.b", "a b", "a!", // 非法字符
		"admin\n", // 换行
	} {
		if err := ValidateUsername(invalid); err == nil {
			t.Errorf("非法用户名 %q 未被拒绝", invalid)
		}
	}
}

// 口令下限按字符数（用户感知的长度），上限按字节数（PBKDF2 开销随字节增长）。
func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("12345678", 8); err != nil {
		t.Fatalf("8 位口令被拒: %v", err)
	}
	if err := ValidatePassword("1234567", 8); err == nil {
		t.Fatal("7 位口令未被拒绝")
	}
	// 8 个汉字 = 24 字节，按字符数算刚好达标。
	if err := ValidatePassword("口令口令口令口令", 8); err != nil {
		t.Fatalf("8 个汉字的口令被拒: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("a", 128), 8); err != nil {
		t.Fatalf("128 字节口令被拒: %v", err)
	}
	if err := ValidatePassword(strings.Repeat("a", 129), 8); err == nil {
		t.Fatal("超长口令未被拒绝（PBKDF2 会被拖成 DoS）")
	}
	// minLength 传 0 时用默认下限，不出现「不校验长度」的空档。
	if err := ValidatePassword("1234567", 0); err == nil {
		t.Fatal("传 0 时应回落到默认下限")
	}
	if err := ValidatePassword("12345678", 0); err != nil {
		t.Fatalf("传 0 时默认下限应为 8: %v", err)
	}
}

// 没有数据库连接时必须明确报错，而不是 panic（auth.local 关闭时调用方仍可能持有它）。
func TestLocalStoreWithoutDatabaseReturnsError(t *testing.T) {
	store := NewLocalStore(nil)
	if _, err := store.List(context.Background()); err == nil {
		t.Error("List 应报错")
	}
	if _, _, err := store.ByUsername(context.Background(), "admin"); err == nil {
		t.Error("ByUsername 应报错")
	}
	if _, err := store.Create(context.Background(), "admin", "12345678", "", false, 8); err == nil {
		t.Error("Create 应报错")
	}
	if err := store.SetDisabled(context.Background(), "admin", true); err == nil {
		t.Error("SetDisabled 应报错")
	}
}

// 账号不存在与数据库错误必须可区分：命令行据此给出不同提示。
func TestAccountErrorsAreDistinct(t *testing.T) {
	if ErrAccountNotFound == nil || ErrUsernameTaken == nil {
		t.Fatal("错误哨兵必须存在")
	}
	if ErrAccountNotFound.Error() == ErrUsernameTaken.Error() {
		t.Fatal("两种错误不能共用同一条信息")
	}
}

// 登录的四条纪律里，前两条（格式与长度）必须在查库之前生效 ——
// 这样非法输入既不落库也不进 PBKDF2。
func TestLoginRejectsBeforeQuery(t *testing.T) {
	store := NewLocalStore(nil) // 没有数据库：任何走到查库的调用都会报错
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		username string
		password string
	}{
		{name: "用户名含冒号（会与飞书身份撞命名空间）", username: "feishu:ou_abc", password: "Str0ng-Passw0rd!"},
		{name: "用户名太短", username: "ab", password: "Str0ng-Passw0rd!"},
		{name: "用户名含非法字符", username: "admin!", password: "Str0ng-Passw0rd!"},
		{name: "口令过短", username: "admin", password: "short"},
		{name: "口令超长", username: "admin", password: strings.Repeat("a", 129)},
		{name: "空口令", username: "admin", password: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Login(ctx, tc.username, tc.password, 8); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("Login 错误 = %v, want ErrInvalidCredentials（且不应触达数据库）", err)
			}
		})
	}
}

// 非法输入通过校验后才会连库；没有连接时应报连接错误，而不是 ErrInvalidCredentials
// —— 否则运维会把「数据库挂了」读成「口令错了」。
func TestLoginWithoutDatabaseReportsInfrastructureError(t *testing.T) {
	store := NewLocalStore(nil)
	if _, err := store.Login(context.Background(), "admin", "Str0ng-Passw0rd!", 8); errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("缺少数据库连接时不应报「凭据无效」")
	} else if err == nil {
		t.Fatal("缺少数据库连接时应报错")
	}
}
