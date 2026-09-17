// ready.go 提供依赖项的连通性探测，供 GET /health/ready 使用（步骤 5.4）。
//
// 与 /health 的分工是刻意的：
//   - /health 是 liveness：进程还活着就返回 200，不检查任何外部依赖。
//     容器编排用它决定「要不要重启这个进程」—— 依赖挂了重启本进程毫无意义。
//   - /health/ready 是 readiness：外部依赖不可用时返回非 200，
//     负载均衡/编排用它决定「要不要把流量打进来」。
//
// 两者合成一个接口会出现最糟的组合：MySQL 抖动一下，编排把整个进程重启一遍。
package health

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Check 是单项依赖的探测结果。
type Check struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// Checker 是一个带名字的探测函数；返回 nil 表示该项健康。
type Checker struct {
	Name  string
	Check func(ctx context.Context) error
}

// DefaultReadyTimeout 是单项探测的超时。
//
// 2 秒是「明显不可用」与「只是慢」之间的分界：探测本身不该成为压垮依赖的
// 额外负载，所以宁可快速判定失败，也不要挂在这里等。
const DefaultReadyTimeout = 2 * time.Second

// Run 并发执行所有探测并返回逐项结果。任一失败都不影响其他项被探测 ——
// 只报「不健康」而不指出是哪一项，排查时会浪费大量时间。
func Run(ctx context.Context, perItem time.Duration, checkers ...Checker) []Check {
	if perItem <= 0 {
		perItem = DefaultReadyTimeout
	}
	results := make([]Check, len(checkers))
	var wg sync.WaitGroup
	for i, item := range checkers {
		wg.Add(1)
		go func(index int, c Checker) {
			defer wg.Done()
			started := time.Now()
			itemCtx, cancel := context.WithTimeout(ctx, perItem)
			defer cancel()
			err := c.Check(itemCtx)
			results[index] = Check{Name: c.Name, OK: err == nil, LatencyMS: time.Since(started).Milliseconds()}
			if err != nil {
				results[index].Error = err.Error()
			}
		}(i, item)
	}
	wg.Wait()
	return results
}

// AllOK 报告所有探测项是否全部通过。
func AllOK(checks []Check) bool {
	for _, item := range checks {
		if !item.OK {
			return false
		}
	}
	return true
}

// DialChecker 返回一个只做 TCP 建连的探测函数。
//
// 它证明的是「这个进程能连上目标端口」，不证明对端数据完好或 schema 正确。
// 对 Milvus 这类需要较长握手的依赖，把深度校验放进 readiness 只会让探测本身
// 变成新的故障点，因此这里刻意停在建连。
func DialChecker(address string) func(context.Context) error {
	return func(ctx context.Context) error {
		dialer := net.Dialer{}
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return err
		}
		return conn.Close()
	}
}

// HTTPChecker 返回一个 GET 指定 URL 并校验 2xx 的探测函数。
// 请求体不回传也不记录，避免把依赖的错误页当成应用日志。
func HTTPChecker(url string) func(context.Context) error {
	client := &http.Client{Timeout: DefaultReadyTimeout}
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return &statusError{code: resp.StatusCode}
		}
		return nil
	}
}

type statusError struct{ code int }

func (e *statusError) Error() string {
	return "unexpected HTTP status " + strconv.Itoa(e.code)
}

// OllamaTagsURL 把 OpenAI 兼容的 base_url 换成 Ollama 原生探测地址。
// 例：http://localhost:11434/v1 → http://localhost:11434/api/tags
//
// 先剥尾斜杠再剥 /v1：顺序反过来的话 "…/v1/" 匹配不到 "/v1"，
// 会拼出 "…/v1/api/tags" 这种必然 404 的地址。
func OllamaTagsURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	base = strings.TrimRight(strings.TrimSuffix(base, "/v1"), "/")
	if base == "" {
		return ""
	}
	return base + "/api/tags"
}
