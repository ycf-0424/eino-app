package agent

import (
	"errors"
	"strings"
	"testing"

	toolset "my-eino-app/internal/eino/tool"
)

// 锁定「哪些工具错误可以降级为交回模型的结果」这条分界线。
//
// 背景（2026-09-17 评测实测）：工具返回 error 时 eino 会中止整轮，实测出现
// `local_file_read: file is outside the allowed local directories` 让整题 0 字输出。
// 降级是必要的，但**不能降级全部错误** —— 把超时或未知故障也说成「输入问题」，
// 模型会去重试一个根本不会成功的调用，真正的故障反而被掩盖。
func TestRecoverableToolResultCoversInputErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		code     string
		recovery bool
	}{
		{"路径越权", toolset.ErrOutsideRoots, "outside_roots", true},
		{"不是文本", toolset.ErrNotText, "not_text", true},
		{"不是普通文件", toolset.ErrNotRegular, "not_regular_file", true},
		{"文件过大", toolset.ErrTooLarge, "too_large", true},
		{"未知故障不降级", errors.New("boom"), "tool_error", false},
		// 注意这里断言的 code 是 "tool_error"，不是 "timeout"：超时在中间件里被
		// 更早的一个分支（errors.Is(toolCtx.Err(), context.DeadlineExceeded)，
		// middleware.go:129）单独分流了，压根不会走到调用 recoverableToolResult 的
		// default 分支；toolset.ErrorCode 也没有 timeout 归类。
		// 本用例一度写成 want="timeout"，结果全量测试直接红 —— 那是在验证一个
		// 不存在的契约（真实归类见下一行的断言）。
		{"超时不降级", errors.New("deadline exceeded"), "tool_error", false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			// ErrorCode 的实际归类必须与用例声明一致，否则这个测试会悄悄失去意义。
			if got := toolset.ErrorCode(item.err); got != item.code {
				t.Fatalf("ErrorCode 归类变化: got=%q want=%q", got, item.code)
			}
			result, ok := recoverableToolResult(item.code, "local_file_read", item.err)
			if ok != item.recovery {
				t.Fatalf("降级判断错误: ok=%t want=%t", ok, item.recovery)
			}
			if !ok {
				if result != "" {
					t.Errorf("不降级时不应返回结果文本，实际为: %s", result)
				}
				return
			}
			// 降级后的文本要让模型能行动：说明是哪个工具、原因是输入、下一步怎么办。
			for _, want := range []string{"local_file_read", "输入问题", "不要编造内容"} {
				if !strings.Contains(result, want) {
					t.Errorf("降级提示应包含 %q，实际为: %s", want, result)
				}
			}
		})
	}
}

// TestRecoverableToolResultNeverDegradesTimeout 是上面表格的补充：
// 即使有人把 "timeout" 这个 code 递进来（例如以后给 toolset.ErrorCode 加上归类，
// 或新增调用点），也必须坚持不降级 —— 超时不是「输入问题」，让模型去重试一个
// 注定超时的调用只会把真正的故障掩盖过去。
func TestRecoverableToolResultNeverDegradesTimeout(t *testing.T) {
	for _, code := range []string{"timeout", "tool_error", ""} {
		result, ok := recoverableToolResult(code, "local_file_read", errors.New("deadline exceeded"))
		if ok || result != "" {
			t.Fatalf("code=%q 不应被降级: ok=%t result=%q", code, ok, result)
		}
	}
}
