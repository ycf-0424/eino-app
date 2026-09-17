// password.go 实现自有账号的口令哈希与校验（步骤 2.11）。
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 口令哈希参数。
//
// 绝不能用 sha256 直接摘要：自有账号的口令由用户自选，强度不可控，必须用慢哈希
// 拉高离线爆破成本。PBKDF2-HMAC-SHA256 由标准库 crypto/pbkdf2 提供（Go 1.24 起），
// 零新增依赖 —— 不为了 bcrypt 引入 golang.org/x/crypto。
const (
	pbkdf2Scheme     = "pbkdf2-sha256"
	pbkdf2Iterations = 210000 // OWASP 对 PBKDF2-HMAC-SHA256 的建议量级
	pbkdf2KeyLength  = 32
	pbkdf2SaltLength = 16
	// pbkdf2MaxIterations 是解析存量哈希时允许的迭代次数上限。哈希串存在库里，
	// 一旦被改成 10^9，每次登录都会变成一次自我 DoS；上限只拦异常值，
	// 将来提高迭代次数仍在范围内。
	pbkdf2MaxIterations = 5_000_000
)

// ErrInvalidHash 表示库里的哈希串不符合本包的存储格式。
var ErrInvalidHash = errors.New("invalid password hash")

// HashPassword 生成自描述的口令哈希，存储格式：
//
//	pbkdf2-sha256$<iter>$<base64(salt)>$<base64(key)>
//
// 迭代次数写进串里：将来提高 pbkdf2Iterations 时，旧哈希仍能验证通过
// （本方案不做自动重哈希，但格式已预留这条路）。
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	salt := make([]byte, pbkdf2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key, err := deriveKey(password, salt, pbkdf2Iterations, pbkdf2KeyLength)
	if err != nil {
		return "", err
	}
	encoding := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", pbkdf2Scheme, pbkdf2Iterations,
		encoding.EncodeToString(salt), encoding.EncodeToString(key)), nil
}

// VerifyPassword 校验口令：从 encoded 里读回迭代次数与盐重新计算，
// 用常量时间比较避免时序侧信道。
//
// 任何格式错误一律返回 false 而不报错 —— 调用方只关心「能不能登」。
func VerifyPassword(encoded, password string) bool {
	iterations, salt, want, err := parsePasswordHash(encoded)
	if err != nil {
		return false
	}
	got, err := deriveKey(password, salt, iterations, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func deriveKey(password string, salt []byte, iterations, keyLength int) ([]byte, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyLength)
	if err != nil {
		return nil, fmt.Errorf("derive password key: %w", err)
	}
	return key, nil
}

// parsePasswordHash 解析存储格式；任何异常都归为 ErrInvalidHash。
func parsePasswordHash(encoded string) (iterations int, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != pbkdf2Scheme {
		return 0, nil, nil, ErrInvalidHash
	}
	iterations, convErr := strconv.Atoi(parts[1])
	if convErr != nil || iterations <= 0 || iterations > pbkdf2MaxIterations {
		return 0, nil, nil, ErrInvalidHash
	}
	encoding := base64.RawStdEncoding
	salt, saltErr := encoding.DecodeString(parts[2])
	if saltErr != nil || len(salt) == 0 {
		return 0, nil, nil, ErrInvalidHash
	}
	key, keyErr := encoding.DecodeString(parts[3])
	if keyErr != nil || len(key) == 0 {
		return 0, nil, nil, ErrInvalidHash
	}
	return iterations, salt, key, nil
}
