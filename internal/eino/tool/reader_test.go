package tool

import (
	"context"
	"errors"
	"testing"
)

// 包装层最容易出的错是「参数名对不上」或「返回值被吞掉」，所以这里验证的是
// 端到端转发：JSON 入参 → 业务函数 → 文本结果。
func TestNewNameListToolForwardsNames(t *testing.T) {
	var got []string
	reader := NewNameListTool("load_x", "读取 X 的完整规则", "名称数组", func(_ context.Context, names []string) (string, error) {
		got = names
		return "ok", nil
	})

	info, err := reader.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "load_x" || info.Desc != "读取 X 的完整规则" {
		t.Fatalf("工具元信息未透传: name=%q desc=%q", info.Name, info.Desc)
	}

	out, err := reader.InvokableRun(context.Background(), `{"names":["a","b"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("返回值未透传: %q", out)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("名称列表未转发: %v", got)
	}
}

func TestNewNameListToolForwardsError(t *testing.T) {
	sentinel := errors.New("boom")
	reader := NewNameListTool("load_x", "d", "p", func(_ context.Context, _ []string) (string, error) {
		return "", sentinel
	})

	if _, err := reader.InvokableRun(context.Background(), `{"names":["a"]}`); !errors.Is(err, sentinel) {
		t.Fatalf("业务错误应原样返回，实际 %v", err)
	}
}

// 空列表要能一路走到业务函数：校验职责在业务侧（各工具对空输入的处理不同）。
func TestNewNameListToolPassesEmptyList(t *testing.T) {
	called := false
	reader := NewNameListTool("load_x", "d", "p", func(_ context.Context, names []string) (string, error) {
		called = true
		if len(names) != 0 {
			t.Fatalf("空数组不应被填充: %v", names)
		}
		return "empty", nil
	})

	out, err := reader.InvokableRun(context.Background(), `{"names":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !called || out != "empty" {
		t.Fatalf("空列表未被转发: called=%v out=%q", called, out)
	}
}
