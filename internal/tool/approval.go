package tool

import (
	"context"
	"fmt"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ApprovalInfo 是中断时展示给用户的工具名称和参数。
type ApprovalInfo struct{ ToolName, ArgumentsInJSON string }

// ApprovalResult 是用户恢复任务时传回的审批决定。
type ApprovalResult struct {
	Approved bool
	Reason   string
}

func (a *ApprovalInfo) String() string {
	return fmt.Sprintf("approve tool %s with %s", a.ToolName, a.ArgumentsInJSON)
}

func init() {
	// Checkpoint 需要知道如何序列化/反序列化中断状态中的自定义类型。
	schema.Register[*ApprovalInfo]()
	schema.Register[*ApprovalResult]()
}

// ApprovableTool 在第一次执行时产生 StatefulInterrupt，批准后才调用真实工具。
type ApprovableTool struct{ einotool.InvokableTool }

func (a ApprovableTool) InvokableRun(ctx context.Context, args string, opts ...einotool.Option) (string, error) {
	info, err := a.Info(ctx)
	if err != nil {
		return "", err
	}
	interrupted, _, stored := einotool.GetInterruptState[string](ctx)
	if !interrupted {
		// write_note 的参数在产生审批前就校验，避免用户批准后才发现文件名非法。
		if info.Name == "write_note" {
			if err := ValidateNoteArguments(args); err != nil {
				return "", err
			}
		}
		// 第一次执行只保存状态并暂停，不触碰真实写入逻辑。
		return "", einotool.StatefulInterrupt(ctx, &ApprovalInfo{ToolName: info.Name, ArgumentsInJSON: args}, args)
	}
	target, hasData, result := einotool.GetResumeContext[*ApprovalResult](ctx)
	if target && hasData {
		if result.Approved {
			// 用户批准后，使用中断前保存的原始参数继续执行。
			return a.InvokableTool.InvokableRun(ctx, stored, opts...)
		}
		// 用户拒绝时不执行真实工具，并把原因返回给模型。
		return fmt.Sprintf("tool %s was rejected: %s", info.Name, result.Reason), nil
	}
	if !target {
		return "", einotool.StatefulInterrupt(ctx, &ApprovalInfo{ToolName: info.Name, ArgumentsInJSON: stored}, stored)
	}
	return a.InvokableTool.InvokableRun(ctx, stored, opts...)
}
