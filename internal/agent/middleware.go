package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"

	"my-eino-app/internal/execution"
	"my-eino-app/internal/observability"
	toolset "my-eino-app/internal/tool"
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
			}
			return output, err
		}
	}}
}
