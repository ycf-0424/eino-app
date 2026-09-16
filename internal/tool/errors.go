package tool

import (
	"errors"
	"os"
)

// 工具错误的稳定分类。执行事件只记录错误码，不把完整错误信息当作契约。
var (
	// ErrOutsideRoots 表示路径越权或文件不存在。
	ErrOutsideRoots = errors.New("file is outside the allowed local directories or does not exist")
	// ErrTooLarge 表示文件或解压后的正文超过上限。
	ErrTooLarge = errors.New("file is too large")
	// ErrNotText 表示文件不是可安全送入模型的 UTF-8 文本。
	ErrNotText = errors.New("file is not UTF-8 text")
	// ErrNotRegular 表示目标不是普通文件。
	ErrNotRegular = errors.New("path is not a regular file")
)

// ErrorCode 把工具错误归类为稳定错误码，供事件 payload 的 error_code 字段使用。
// 未知错误一律返回 tool_error，避免把原始错误文本泄露给前端。
func ErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrOutsideRoots):
		return "outside_roots"
	case errors.Is(err, ErrTooLarge):
		return "too_large"
	case errors.Is(err, ErrNotText):
		return "not_text"
	case errors.Is(err, ErrNotRegular):
		return "not_regular_file"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	default:
		return "tool_error"
	}
}
