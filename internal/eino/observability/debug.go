// Package observability 注册 Eino Callback，用于观察模型和工具执行过程。
package observability

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	cbutils "github.com/cloudwego/eino/utils/callbacks"
)

// startedKey 用作 context 私有键，在开始和结束 Callback 之间传递开始时间。
type startedKey struct{}

// EnableDebug 注册全局调试 Callback；仅应在程序启动时调用一次。
func EnableDebug() {
	// HandlerHelper 按组件类型分发事件，避免在一个通用回调里做类型断言。
	handler := cbutils.NewHandlerHelper().
		ChatModel(&cbutils.ModelCallbackHandler{
			OnStart: func(ctx context.Context, _ *callbacks.RunInfo, _ *model.CallbackInput) context.Context {
				fmt.Fprintln(os.Stderr, "[model] start")
				return context.WithValue(ctx, startedKey{}, time.Now())
			},
			OnEnd: func(ctx context.Context, _ *callbacks.RunInfo, _ *model.CallbackOutput) context.Context {
				logEnd(ctx, "model", nil)
				return ctx
			},
			OnEndWithStreamOutput: func(ctx context.Context, _ *callbacks.RunInfo, _ *schema.StreamReader[*model.CallbackOutput]) context.Context {
				logEnd(ctx, "model-stream-ready", nil)
				return ctx
			},
			OnError: func(ctx context.Context, _ *callbacks.RunInfo, err error) context.Context {
				logEnd(ctx, "model", err)
				return ctx
			},
		}).
		Tool(&cbutils.ToolCallbackHandler{
			OnStart: func(ctx context.Context, _ *callbacks.RunInfo, _ *einotool.CallbackInput) context.Context {
				fmt.Fprintln(os.Stderr, "[tool-callback] start")
				return ctx
			},
			OnEnd: func(ctx context.Context, _ *callbacks.RunInfo, _ *einotool.CallbackOutput) context.Context {
				fmt.Fprintln(os.Stderr, "[tool-callback] end")
				return ctx
			},
			OnError: func(ctx context.Context, _ *callbacks.RunInfo, err error) context.Context {
				fmt.Fprintf(os.Stderr, "[tool-callback] error=%s\n", Redact(fmt.Sprint(err)))
				return ctx
			},
		}).Handler()
	callbacks.AppendGlobalHandlers(handler)
}

// logEnd 输出组件结束状态；如果能取得开始时间，同时输出总耗时。
func logEnd(ctx context.Context, component string, err error) {
	if started, ok := ctx.Value(startedKey{}).(time.Time); ok {
		fmt.Fprintf(os.Stderr, "[%s] end duration=%s err=%s\n", component, time.Since(started), Redact(fmt.Sprint(err)))
	} else {
		fmt.Fprintf(os.Stderr, "[%s] end err=%s\n", component, Redact(fmt.Sprint(err)))
	}
}
