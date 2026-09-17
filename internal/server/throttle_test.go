package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"my-eino-app/internal/auth"
)

// nextHandler 记录被真正放行的请求数，用来确认被限流时下游完全没有被调用。
func nextHandler(calls *int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*calls++
		w.WriteHeader(http.StatusOK)
	})
}

// 开关关闭（chatLimiter 为 nil）时必须完全透传，行为与改造前一致。
func TestThrottlePassesThroughWhenDisabled(t *testing.T) {
	calls := 0
	service := &Service{}
	handler := service.throttle(nextHandler(&calls))

	for i := 0; i < 100; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/chat", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求 code = %d, want 200", i+1, rec.Code)
		}
	}
	if calls != 100 {
		t.Fatalf("下游调用次数 = %d, want 100", calls)
	}
}

// 超出突发容量后返回 429 + Retry-After，且不再调用下游。
func TestThrottleBlocksAfterBurst(t *testing.T) {
	calls := 0
	service := &Service{chatLimiter: newRateLimiter(30, 10)}
	now := time.Now()
	service.chatLimiter.now = func() time.Time { return now }
	handler := service.throttle(nextHandler(&calls))

	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/chat", nil)
		req = req.WithContext(auth.WithOwner(req.Context(), "local:alice"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	for i := 0; i < 10; i++ {
		if rec := request(); rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求 code = %d, want 200", i+1, rec.Code)
		}
	}
	blocked := request()
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("超出突发后 code = %d, want 429", blocked.Code)
	}
	if blocked.Header().Get("Retry-After") == "" {
		t.Fatal("429 响应缺少 Retry-After 头")
	}
	seconds, err := strconv.Atoi(blocked.Header().Get("Retry-After"))
	if err != nil || seconds < 1 {
		t.Fatalf("Retry-After = %q, want 正整数秒", blocked.Header().Get("Retry-After"))
	}
	if calls != 10 {
		t.Fatalf("被限流的请求不应触达下游，下游调用次数 = %d, want 10", calls)
	}
}

// 限流按 owner 分桶：一个人打满配额不能牵连其他人。
func TestThrottleKeysByOwner(t *testing.T) {
	calls := 0
	service := &Service{chatLimiter: newRateLimiter(30, 10)}
	now := time.Now()
	service.chatLimiter.now = func() time.Time { return now }
	handler := service.throttle(nextHandler(&calls))

	request := func(owner, remoteAddr string) int {
		req := httptest.NewRequest("POST", "/chat", nil)
		req.RemoteAddr = remoteAddr
		req = req.WithContext(auth.WithOwner(req.Context(), owner))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	// 两个不同的人从同一个出口 IP 打过来，各自有独立配额。
	for i := 0; i < 10; i++ {
		if code := request("local:alice", "10.0.0.9:1111"); code != http.StatusOK {
			t.Fatalf("alice 第 %d 次 code = %d", i+1, code)
		}
		if code := request("local:bob", "10.0.0.9:2222"); code != http.StatusOK {
			t.Fatalf("bob 第 %d 次 code = %d", i+1, code)
		}
	}
	if code := request("local:alice", "10.0.0.9:1111"); code != http.StatusTooManyRequests {
		t.Fatalf("alice 配额耗尽后 code = %d, want 429", code)
	}
	if code := request("local:bob", "10.0.0.9:2222"); code != http.StatusTooManyRequests {
		t.Fatalf("bob 配额耗尽后 code = %d, want 429", code)
	}
	if calls != 20 {
		t.Fatalf("下游调用次数 = %d, want 20", calls)
	}
}

// 未认证（owner 为空）时退回 IP 维度，且不采信 X-Forwarded-For。
func TestThrottleFallsBackToIPWithoutOwner(t *testing.T) {
	calls := 0
	service := &Service{chatLimiter: newRateLimiter(30, 10)}
	now := time.Now()
	service.chatLimiter.now = func() time.Time { return now }
	handler := service.throttle(nextHandler(&calls))

	request := func(remoteAddr, forwarded string) int {
		req := httptest.NewRequest("POST", "/chat", nil)
		req.RemoteAddr = remoteAddr
		if forwarded != "" {
			req.Header.Set("X-Forwarded-For", forwarded)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < 10; i++ {
		if code := request("10.0.0.1:9999", ""); code != http.StatusOK {
			t.Fatalf("第 %d 次 code = %d", i+1, code)
		}
	}
	// 换伪造的 XFF 头也必须继续被拒：否则限流形同虚设。
	if code := request("10.0.0.1:9999", "203.0.113.7"); code != http.StatusTooManyRequests {
		t.Fatalf("伪造 XFF 后 code = %d, want 429", code)
	}
	// 真正不同的来源 IP 才应该是另一个桶。
	if code := request("10.0.0.2:9999", ""); code != http.StatusOK {
		t.Fatalf("另一 IP code = %d, want 200", code)
	}
}

// 桶耗尽后按补充速率恢复：30 次/分钟 → 2 秒补 1 个。
func TestThrottleRefillsOverTime(t *testing.T) {
	calls := 0
	service := &Service{chatLimiter: newRateLimiter(30, 10)}
	now := time.Now()
	service.chatLimiter.now = func() time.Time { return now }
	handler := service.throttle(nextHandler(&calls))

	request := func() int {
		req := httptest.NewRequest("POST", "/chat", nil)
		req = req.WithContext(auth.WithOwner(req.Context(), "local:alice"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	for i := 0; i < 10; i++ {
		request()
	}
	if code := request(); code != http.StatusTooManyRequests {
		t.Fatalf("桶耗尽后 code = %d, want 429", code)
	}
	now = now.Add(2 * time.Second)
	if code := request(); code != http.StatusOK {
		t.Fatalf("补充一个令牌后 code = %d, want 200", code)
	}
}
