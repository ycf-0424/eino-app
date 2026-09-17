package agent

import (
	"context"
	"testing"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	toolset "my-eino-app/internal/eino/tool"
	"my-eino-app/internal/execution"
)

func runMiddleware(t *testing.T, endpoint compose.InvokableToolEndpoint) []execution.Event {
	t.Helper()
	recorder := execution.NewRecorder(0)
	em := execution.NewSession("run-1", "session-1", nil, recorder, execution.SessionOptions{})
	ctx := execution.WithEmitter(context.Background(), em)
	wrapped := safeToolMiddleware(false, "test-agent").Invokable(endpoint)
	_, _ = wrapped(ctx, &compose.ToolInput{Name: "current_time", Arguments: `{}`, CallID: "call-1"})
	return recorder.Events()
}

func TestMiddlewareEmitsStartedAndCompletedWithAnnotations(t *testing.T) {
	events := runMiddleware(t, func(ctx context.Context, _ *compose.ToolInput) (*compose.ToolOutput, error) {
		// 工具只标注结构化结果，事件由中间件统一发出。
		execution.Annotate(ctx, map[string]any{"hit_count": 3, "sources": []string{"a.md"}})
		return &compose.ToolOutput{Result: "ok"}, nil
	})
	if len(events) != 2 {
		t.Fatalf("events=%d, want 2: %+v", len(events), events)
	}
	if events[0].Type != execution.ToolStarted || events[1].Type != execution.ToolCompleted {
		t.Fatalf("unexpected types: %s, %s", events[0].Type, events[1].Type)
	}
	payload := events[1].Payload
	if payload["tool_name"] != "current_time" || payload["tool_call_id"] != "call-1" || payload["agent_name"] != "test-agent" {
		t.Fatalf("payload=%+v", payload)
	}
	if _, ok := payload["duration_ms"]; !ok {
		t.Fatalf("duration_ms missing: %+v", payload)
	}
	if payload["hit_count"] != 3 {
		t.Fatalf("annotation missing: %+v", payload)
	}
	// args_digest 只保留短摘要，绝不写入原始参数。
	if events[0].Payload["args_digest"] == "" || events[0].Payload["args_digest"] == `{}` {
		t.Fatalf("args_digest=%v", events[0].Payload["args_digest"])
	}
}

// 审批中断不能记成失败：只发 tool_started，终态由 Agent 层的 approval_required 兜底。
func TestMiddlewareTreatsApprovalInterruptAsNonTerminal(t *testing.T) {
	events := runMiddleware(t, func(ctx context.Context, _ *compose.ToolInput) (*compose.ToolOutput, error) {
		return nil, einotool.StatefulInterrupt(ctx, &toolset.ApprovalInfo{ToolName: "write_note", ArgumentsInJSON: `{"name":"n"}`}, `{"name":"n"}`)
	})
	if len(events) != 1 {
		t.Fatalf("events=%d, want 1 (only tool_started): %+v", len(events), events)
	}
	if events[0].Type != execution.ToolStarted {
		t.Fatalf("type=%s, want tool_started", events[0].Type)
	}
	for _, ev := range events {
		if ev.Type.Terminal() || ev.Type == execution.ToolCompleted || ev.Type == execution.ToolFailed {
			t.Fatalf("interrupt must not produce a tool terminal event: %+v", ev)
		}
	}
}

// 越权/超限等已知错误要归类成稳定错误码，而不是把原始错误文本当作契约。
func TestMiddlewareMapsToolErrorToCode(t *testing.T) {
	events := runMiddleware(t, func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return nil, toolset.ErrOutsideRoots
	})
	if len(events) != 2 || events[1].Type != execution.ToolFailed {
		t.Fatalf("events=%+v", events)
	}
	if events[1].Payload["error_code"] != "outside_roots" {
		t.Fatalf("error_code=%v", events[1].Payload["error_code"])
	}
	if _, ok := events[1].Payload["error_message"]; !ok {
		t.Fatalf("error_message missing: %+v", events[1].Payload)
	}
}

// load_skills / local_file_read 的成功要分流到更精确的事件类型。
func TestCompletionTypeForKnownTools(t *testing.T) {
	if got := execution.CompletionTypeFor("load_skills"); got != execution.SkillLoaded {
		t.Fatalf("load_skills -> %s", got)
	}
	if got := execution.CompletionTypeFor("local_file_read"); got != execution.FileReadDone {
		t.Fatalf("local_file_read -> %s", got)
	}
	if got := execution.CompletionTypeFor("other"); got != execution.ToolCompleted {
		t.Fatalf("other -> %s", got)
	}
}

func TestMiddlewareBlocksImplicitWriteNote(t *testing.T) {
	called := false
	recorder := execution.NewRecorder(0)
	em := execution.NewSession("run-implicit-note", "session-1", nil, recorder, execution.SessionOptions{})
	ctx := withWriteNotePermission(execution.WithEmitter(context.Background(), em), false)
	wrapped := safeToolMiddleware(false, "test-agent").Invokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		called = true
		return &compose.ToolOutput{Result: "written"}, nil
	})
	out, err := wrapped(ctx, &compose.ToolInput{Name: "write_note", Arguments: `{"name":"n","content":"x"}`, CallID: "call-note"})
	if err != nil || called || out == nil || out.Result == "written" {
		t.Fatalf("implicit write_note was executed: out=%+v err=%v called=%v", out, err, called)
	}
	events := recorder.Events()
	if len(events) != 2 || events[1].Type != execution.ToolFailed || events[1].Payload["error_code"] != "write_note_not_requested" {
		t.Fatalf("events=%+v", events)
	}
}
