package health

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 逐项结果必须完整返回：只报「不健康」而不说哪一项坏，排查时会浪费大量时间。
func TestRunReportsEveryItem(t *testing.T) {
	checks := Run(context.Background(), time.Second,
		Checker{Name: "good", Check: func(context.Context) error { return nil }},
		Checker{Name: "bad", Check: func(context.Context) error { return errors.New("dial tcp: refused") }},
	)
	if len(checks) != 2 {
		t.Fatalf("探测项数 = %d, want 2", len(checks))
	}
	byName := map[string]Check{}
	for _, item := range checks {
		byName[item.Name] = item
	}
	if !byName["good"].OK || byName["good"].Error != "" {
		t.Fatalf("good 项 = %+v", byName["good"])
	}
	if byName["bad"].OK || byName["bad"].Error == "" {
		t.Fatalf("bad 项 = %+v", byName["bad"])
	}
	if AllOK(checks) {
		t.Fatal("存在失败项时 AllOK 应为 false")
	}
}

// 单项超时必须是硬上限：一个卡住的依赖不能让整个探活接口挂住。
func TestRunEnforcesPerItemTimeout(t *testing.T) {
	started := time.Now()
	checks := Run(context.Background(), 50*time.Millisecond,
		Checker{Name: "hang", Check: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}},
	)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("超时未生效，耗时 %v", elapsed)
	}
	if checks[0].OK {
		t.Fatal("超时的探测项不应判定为健康")
	}
	if !strings.Contains(checks[0].Error, "deadline exceeded") {
		t.Fatalf("超时应报出上下文超时，实际: %q", checks[0].Error)
	}
}

// 未装配任何依赖时应全部通过（空集合的 AllOK 为 true），ready 退化为 liveness。
func TestAllOKOnEmpty(t *testing.T) {
	if !AllOK(nil) {
		t.Fatal("空探测列表应判定为健康")
	}
}

// DialChecker 只证明端口可达：连得上为健康，连不上为失败。
func TestDialChecker(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	if err := DialChecker(listener.Addr().String())(context.Background()); err != nil {
		t.Fatalf("可达端口应健康: %v", err)
	}
	// 关掉监听后再探：地址已被释放，必然失败。
	listener.Close()
	if err := DialChecker(listener.Addr().String())(context.Background()); err == nil {
		t.Fatal("不可达端口应判定为失败")
	}
}

// HTTPChecker 校验 2xx；非 2xx 与连接失败都要报错。
func TestHTTPChecker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	if err := HTTPChecker(server.URL + "/ok")(context.Background()); err != nil {
		t.Fatalf("2xx 应健康: %v", err)
	}
	err := HTTPChecker(server.URL + "/bad")(context.Background())
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("非 2xx 应报出状态码，实际: %v", err)
	}
}

// OllamaTagsURL 把 OpenAI 兼容地址换成原生探测地址，且不产生重复斜杠。
func TestOllamaTagsURL(t *testing.T) {
	cases := map[string]string{
		"http://localhost:11434/v1":  "http://localhost:11434/api/tags",
		"http://localhost:11434/v1/": "http://localhost:11434/api/tags",
		"http://localhost:11434":     "http://localhost:11434/api/tags",
		"  https://ark.example/v3  ": "https://ark.example/v3/api/tags",
		"":                           "",
	}
	for input, want := range cases {
		if got := OllamaTagsURL(input); got != want {
			t.Fatalf("OllamaTagsURL(%q) = %q, want %q", input, got, want)
		}
	}
}
