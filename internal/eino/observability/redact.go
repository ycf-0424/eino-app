package observability

import "regexp"

var sensitiveJSONValue = regexp.MustCompile(`(?i)("(?:api[_-]?key|(?:access|refresh)[_-]?token|client[_-]?secret|token|password|passphrase|secret|authorization)"\s*:\s*")[^"]*(")`)
var bearerToken = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/-]+`)
var sensitiveParameter = regexp.MustCompile(`(?i)((?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|token|password|secret)=)[^&\s]+`)

// Redact 隐藏日志中的常见密钥字段和 Bearer Token。
// 它只用于日志展示，原始工具参数仍会传给实际工具。
func Redact(value string) string {
	value = sensitiveJSONValue.ReplaceAllString(value, `${1}[REDACTED]${2}`)
	value = bearerToken.ReplaceAllString(value, `${1}[REDACTED]`)
	return sensitiveParameter.ReplaceAllString(value, `${1}[REDACTED]`)
}
