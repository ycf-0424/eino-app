package auth

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
)

const samplePassword = "Str0ng-Passw0rd!"

func TestHashPasswordUsesRandomSalt(t *testing.T) {
	first, err := HashPassword(samplePassword)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword(samplePassword)
	if err != nil {
		t.Fatal(err)
	}
	// 盐每次随机：同一口令两次哈希必须不同，否则彩虹表可以复用。
	if first == second {
		t.Fatal("同一口令两次哈希结果相同，盐未生效")
	}
	if !strings.HasPrefix(first, pbkdf2Scheme+"$") {
		t.Fatalf("哈希串缺少方案前缀: %q", first)
	}
	if strings.Contains(first, samplePassword) {
		t.Fatal("哈希串里出现了明文口令")
	}
}

func TestHashPasswordRejectsEmpty(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Fatal("空口令必须被拒绝")
	}
}

func TestVerifyPassword(t *testing.T) {
	encoded, err := HashPassword(samplePassword)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(encoded, samplePassword) {
		t.Fatal("正确口令未通过校验")
	}
	if VerifyPassword(encoded, samplePassword+"x") {
		t.Fatal("错误口令通过了校验")
	}
	if VerifyPassword(encoded, "") {
		t.Fatal("空口令通过了校验")
	}
	if VerifyPassword(encoded, strings.ToUpper(samplePassword)) {
		t.Fatal("口令校验不应忽略大小写")
	}
}

// 超长口令不能 panic，也不能让校验永远为真。
func TestHashPasswordHandlesLongInput(t *testing.T) {
	long := strings.Repeat("a", 4096)
	encoded, err := HashPassword(long)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(encoded, long) {
		t.Fatal("超长口令未通过校验")
	}
	if VerifyPassword(encoded, strings.Repeat("a", 4095)) {
		t.Fatal("截断后的口令不应通过校验")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for _, encoded := range []string{
		"",
		"sha256$1$c2FsdA$a2V5",                  // 方案不符
		"pbkdf2-sha256$abc$c2FsdA$a2V5",         // 迭代次数不是数字
		"pbkdf2-sha256$0$c2FsdA$a2V5",           // 迭代次数为 0
		"pbkdf2-sha256$-1$c2FsdA$a2V5",          // 迭代次数为负
		"pbkdf2-sha256$99999999$c2FsdA$a2V5",    // 迭代次数超上限（自我 DoS 防护）
		"pbkdf2-sha256$1000$$a2V5",              // 盐为空
		"pbkdf2-sha256$1000$c2FsdA$",            // key 为空
		"pbkdf2-sha256$1000$!!!$a2V5",           // 盐不是 base64
		"pbkdf2-sha256$1000$c2FsdA$!!!",         // key 不是 base64
		"pbkdf2-sha256$1000$c2FsdA",             // 段数不足
		"$1000$c2FsdA$a2V5",                     // 缺少方案
		"pbkdf2-sha256$1000$c2FsdA$a2V5$extras", // 段数过多
	} {
		if VerifyPassword(encoded, samplePassword) {
			t.Errorf("非法哈希串被接受: %q", encoded)
		}
	}
}

// 迭代次数存在哈希串里：提高默认值后，旧哈希仍要能验证通过（格式已预留升级路径）。
func TestVerifyPasswordHonoursStoredIterations(t *testing.T) {
	if !VerifyPassword(hashWithIterations(t, samplePassword, 1000), samplePassword) {
		t.Fatal("低迭代次数的存量哈希未通过校验")
	}
	if VerifyPassword(hashWithIterations(t, samplePassword, 1000), samplePassword+"x") {
		t.Fatal("低迭代次数的哈希接受了错误口令")
	}
}

func hashWithIterations(t *testing.T, password string, iterations int) string {
	t.Helper()
	salt := []byte("0123456789abcdef")
	key, err := deriveKey(password, salt, iterations, pbkdf2KeyLength)
	if err != nil {
		t.Fatal(err)
	}
	encoding := base64.RawStdEncoding
	return strings.Join([]string{
		pbkdf2Scheme,
		strconv.Itoa(iterations),
		encoding.EncodeToString(salt),
		encoding.EncodeToString(key),
	}, "$")
}
