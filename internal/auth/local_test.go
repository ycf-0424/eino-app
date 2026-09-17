package auth

import (
	"context"
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
