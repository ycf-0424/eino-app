package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"

	"my-eino-app/internal/eino/observability"
	toolset "my-eino-app/internal/eino/tool"
	"my-eino-app/internal/execution"
)

// toolTimeout 是单个工具调用的上限，防止工具永久阻塞 Agent。
const toolTimeout = 10 * time.Second

type writeNotePermissionKey struct{}

func withWriteNotePermission(ctx context.Context, allowed bool) context.Context {
	return context.WithValue(ctx, writeNotePermissionKey{}, allowed)
}

func writeNoteAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(writeNotePermissionKey{}).(bool)
	return allowed
}

// recoverableToolResult 判断某个工具错误是否应该「降级为一段交回模型的结果」。
//
// 为什么必须降级（2026-09-17 评测实测）：eino 的工具节点遇到工具返回 error 会中止整轮
// （`[NodeRunError] failed to invoke tool ...`），于是「模型把文件路径写错」这种小事
// 会直接变成「用户拿到空回答」。实测中一条 `/workspace-files/README.md` 读不到，
// 整题 0 字输出 —— 模型既没机会换一个路径，也没机会如实说明文件不存在。
//
// 只降级「成因在输入」的错误：路径越界/不存在、不是文本、不是普通文件、文件过大。
// 这些都指向模型自己的选择，把原因告诉它，它能在同一轮里改正或如实告知用户。
//
// 不降级的（仍然向上抛）：超时、用户取消、未知的 tool_error，以及审批中断。
// 那些不是模型能靠重试解决的问题，掩盖它们只会让真正的故障更难查。
// 注意：无论是否降级，ToolFailed 事件照常发出，可观测性不受影响。
//
// 关于 code 的取值范围：本函数只可能收到 toolset.ErrorCode 的输出，即
// outside_roots / not_found / not_text / not_regular_file / too_large / tool_error 六种。
// **"timeout" 与「用户取消」不会到这里** —— 它们在 middleware.go:129/132 的更早分支
// 就被分流了（前者只发事件、后者交给取消链路收尾）。default 分支的 false 是兜底，
// 不是「还没实现」，不要为了「补齐 timeout」在这里加 case。
func recoverableToolResult(code, toolName string, err error) (string, bool) {
	reason := ""
	switch code {
	case "outside_roots":
		reason = "路径不在授权目录内，或该文件不存在"
	case "not_found":
		reason = "文件不存在"
	case "not_text":
		reason = "该文件不是可读取的 UTF-8 文本"
	case "not_regular_file":
		reason = "该路径不是普通文件"
	case "too_large":
		reason = "文件超出大小上限"
	default:
		return "", false
	}
	return fmt.Sprintf(
		"工具 %s 执行失败：%s（%s）。这是本次调用的输入问题，不是系统故障。\n"+
			"请改用正确的输入重试；若确实无法取得该内容，请如实告诉用户缺少什么，不要编造内容。",
		toolName, reason, err.Error()), true
}

// safeToolMiddleware 为所有非流式工具统一增加超时、审计日志和执行事件。
// 它不接收 Emitter 参数，而是从 context 读取，因此根 Agent 与子 Agent 可以共用同一份实现。
func safeToolMiddleware(debug bool, agentName string) compose.ToolMiddleware {
	return compose.ToolMiddleware{Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			em := execution.FromContext(ctx)
			toolCtx, cancel := context.WithTimeout(ctx, toolTimeout)
			defer cancel()
			// 每次工具调用使用独立标注容器：并发执行时不串数据。
			bag := execution.NewBag()
			toolCtx = execution.WithBag(toolCtx, bag)

			callID := input.CallID
			if callID == "" {
				callID = uuid.NewString()
			}
			started := time.Now()
			em.Emit(execution.Event{Type: execution.ToolStarted, Payload: map[string]any{
				"tool_name":    input.Name,
				"tool_call_id": callID,
				"agent_name":   agentName,
				"args_digest":  execution.DigestArgs(input.Arguments),
			}})
			if debug {
				fmt.Fprintf(os.Stderr, "[tool] start name=%s args=%s\n", input.Name, observability.Redact(input.Arguments))
			}
			if input.Name == "write_note" && !writeNoteAllowed(ctx) {
				// The model may still select write_note despite the system prompt.
				// Return a successful, recoverable tool result so it can answer the
				// user without creating a file or entering an approval checkpoint.
				payload := map[string]any{
					"tool_name":    input.Name,
					"tool_call_id": callID,
					"agent_name":   agentName,
					"duration_ms":  time.Since(started).Milliseconds(),
					"error_code":   "write_note_not_requested",
				}
				em.Emit(execution.Event{Type: execution.ToolFailed, Payload: payload})
				return &compose.ToolOutput{Result: "当前请求未明确要求写入笔记或文件，因此不能调用 write_note。请直接回答用户，并由系统自动处理长期记忆。"}, nil
			}

			// next 代表真正的工具实现；中间件只负责调用前后的横切逻辑。
			output, err := next(toolCtx, input)
			if debug {
				fmt.Fprintf(os.Stderr, "[tool] end name=%s duration=%s err=%s\n", input.Name, time.Since(started), observability.Redact(fmt.Sprint(err)))
			}

			if _, interrupted := compose.IsInterruptRerunError(err); interrupted {
				// 审批中断不是失败：只发 tool_started，终态由 approval_required 兜底。
				return output, err
			}

			payload := map[string]any{
				"tool_name":    input.Name,
				"tool_call_id": callID,
				"agent_name":   agentName,
				"duration_ms":  time.Since(started).Milliseconds(),
			}
			switch {
			case err == nil:
				bag.MergeInto(payload)
				em.Emit(execution.Event{Type: execution.CompletionTypeFor(input.Name), Payload: payload})
			case errors.Is(toolCtx.Err(), context.DeadlineExceeded):
				payload["error_code"] = "timeout"
				em.Emit(execution.Event{Type: execution.ToolFailed, Payload: payload})
			case ctx.Err() != nil:
				// 用户取消或断线：由取消链路统一收尾，不补发 failed。
			default:
				payload["error_code"] = toolset.ErrorCode(err)
				payload["error_message"] = observability.Redact(err.Error())
				em.Emit(execution.Event{Type: execution.ToolFailed, Payload: payload})
				// 输入类错误降级为可恢复结果，理由见 recoverableToolResult 的注释。
				if result, ok := recoverableToolResult(payload["error_code"].(string), input.Name, err); ok {
					return &compose.ToolOutput{Result: result}, nil
				}
			}
			return output, err
		}
	}}
}
