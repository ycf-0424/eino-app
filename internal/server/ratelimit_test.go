package server

import (
	"net/http/httptest"
	"testing"
	"time"
)

// 令牌桶：先按突发容量放行，超出后拒绝并给出可用的 Retry-After。
func TestRateLimiterAllowsBurstThenBlocks(t *testing.T) {
	limiter := newRateLimiter(loginRatePerMinute, loginBurst)
	now := time.Now()
	limiter.now = func() time.Time { return now }

	for i := 0; i < loginBurst; i++ {
		if ok, _ := limiter.Allow("ip:10.0.0.1"); !ok {
			t.Fatalf("第 %d 次尝试应放行（突发容量 %d）", i+1, loginBurst)
		}
	}
	ok, wait := limiter.Allow("ip:10.0.0.1")
	if ok {
		t.Fatalf("超出突发容量后仍放行")
	}
	if wait <= 0 || wait > time.Minute {
		t.Fatalf("Retry-After 不合理: %v", wait)
	}

	// 不同 key 互不影响：一个人被限流不该牵连其他人。
	if ok, _ := limiter.Allow("ip:10.0.0.2"); !ok {
		t.Fatal("另一个 key 不应受影响")
	}

	// 时间推进后按速率补充：10 次/分钟 → 6 秒补 1 个。
	now = now.Add(6 * time.Second)
	if ok, _ := limiter.Allow("ip:10.0.0.1"); !ok {
		t.Fatal("补充令牌后应放行")
	}
}

// 登录成功要能归还令牌，否则共享出口 IP 的多人会互相拖累。
func TestRateLimiterRefund(t *testing.T) {
	limiter := newRateLimiter(loginRatePerMinute, loginBurst)
	now := time.Now()
	limiter.now = func() time.Time { return now }

	for i := 0; i < loginBurst; i++ {
		limiter.Allow("user:admin")
	}
	if ok, _ := limiter.Allow("user:admin"); ok {
		t.Fatal("桶已耗尽，应被拒")
	}
	limiter.Refund("user:admin")
	if ok, _ := limiter.Allow("user:admin"); !ok {
		t.Fatal("归还一个令牌后应放行")
	}

	// Refund 不能把桶撑到超过容量，否则「成功一次抵消失败一次」会成为绕行通道。
	for i := 0; i < 100; i++ {
		limiter.Refund("user:admin")
	}
	allowed := 0
	for i := 0; i < 100; i++ {
		if ok, _ := limiter.Allow("user:admin"); ok {
			allowed++
		}
	}
	if allowed != loginBurst {
		t.Fatalf("Refund 后可用次数 = %d, want %d", allowed, loginBurst)
	}
}

// 空闲桶要能被回收：key 里含用户名与 IP，都是外部可控的，不回收就是内存泄漏。
func TestRateLimiterSweepsIdleBuckets(t *testing.T) {
	limiter := newRateLimiter(loginRatePerMinute, loginBurst)
	now := time.Now()
	limiter.now = func() time.Time { return now }

	limiter.Allow("user:old") // 首次调用只记录清扫起点
	if limiter.size() != 1 {
		t.Fatalf("桶数量 = %d, want 1", limiter.size())
	}
	now = now.Add(31 * time.Minute)
	limiter.Allow("user:new")
	if limiter.size() != 1 {
		t.Fatalf("空闲桶未被回收，桶数量 = %d, want 1", limiter.size())
	}
	if _, ok := limiter.buckets["user:old"]; ok {
		t.Fatal("超时空闲桶仍在 map 中")
	}
}

// 限流要同时覆盖 IP 与用户名两个维度，且用户名做大小写归一。
func TestLoginRateKeysCoverBothDimensions(t *testing.T) {
	req := httptest.NewRequest("POST", "/auth/local", nil)
	req.RemoteAddr = "10.1.2.3:54321"
	service := &Service{}

	keys := service.loginRateKeys(req, "Admin")
	if len(keys) != 2 {
		t.Fatalf("应有 IP 与用户名两个 key，实际 %v", keys)
	}
	if keys[0] != "ip:10.1.2.3" {
		t.Fatalf("IP key = %q", keys[0])
	}
	// 账号本身大小写敏感，但爆破者会拿大小写变体刷桶，所以计数键统一小写。
	if keys[1] != "user:admin" {
		t.Fatalf("用户名 key = %q", keys[1])
	}

	// RemoteAddr 无端口时原样返回，不能因为解析失败就丢掉落限维度。
	req.RemoteAddr = "unix-socket"
	if keys := service.loginRateKeys(req, "a"); keys[0] != "ip:unix-socket" {
		t.Fatalf("无端口地址 key = %q", keys[0])
	}
}
