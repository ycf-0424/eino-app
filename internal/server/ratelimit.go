// ratelimit.go 提供进程内令牌桶限流。
//
// 用途（步骤 2.13）：登录接口的防口令爆破。注册方式定为「仅管理员创建」后，
// 登录接口不存在刷库风险，真正的问题是——用户名是可枚举的（内部系统，人名拼音
// 就那些），口令却可以被无限次尝试。因此限流的目标是「让爆破变得不划算」。
//
// 这份实现也是步骤 5.2 要复用的那一份（届时加配置项并扩展到 /chat 与 /ws），
// 所以这里刻意做成与登录无关的通用限流器：调用方自己决定 key 的构造方式。
package server

import (
	"sync"
	"time"
)

// 登录限流参数：每分钟 10 次、突发 10 次。
//
// 取值理由：正常人不会在 1 分钟内失败 10 次（打错两次就会去重设口令），
// 而爆破者要试完「人名拼音 + 常见弱口令」的空间需要远超这个量级的时间。
// 这里的数字是硬编码的（登录接口没有对应配置项）；/chat 与 /ws 的限流走
// runtime.rate_limit 配置，两者共用同一份 rateLimiter 实现但互不影响计数。
const (
	loginRatePerMinute = 10
	loginBurst         = 10
)

// rateLimiter 是按 key 计数的令牌桶：桶满时每次请求消耗一个令牌，
// 令牌按固定速率补充。key 由调用方构造（如 "ip:1.2.3.4"、"user:admin"）。
//
// 令牌桶而非固定窗口计数器：固定窗口下「窗口末尾 + 下一窗口开头」可以瞬间打出
// 两倍流量，而登录爆破恰恰擅长集中火力。
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	// refillPerSecond 是每秒补充的令牌数；capacity 是桶容量（即突发上限）。
	refillPerSecond float64
	capacity        float64
	// idleTTL 是桶的空闲回收阈值，防止 key 无限增长（用户名/IP 都是外部可控的）。
	idleTTL   time.Duration
	lastSweep time.Time
	now       func() time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// newRateLimiter 创建限流器：perMinute 是每分钟允许的次数，burst 是突发上限。
// 两者都非正时退化为「每分钟 10 次、突发 10 次」这一保守默认。
func newRateLimiter(perMinute int, burst int) *rateLimiter {
	if perMinute <= 0 {
		perMinute = 10
	}
	if burst <= 0 {
		burst = perMinute
	}
	return &rateLimiter{
		buckets:         make(map[string]*tokenBucket),
		refillPerSecond: float64(perMinute) / 60,
		capacity:        float64(burst),
		idleTTL:         30 * time.Minute,
		now:             time.Now,
	}
}

// Allow 判断该 key 是否放行；被拒时第二个返回值是建议的 Retry-After 时长。
func (l *rateLimiter) Allow(key string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)

	bucket, ok := l.buckets[key]
	if !ok {
		// 新 key 从满桶开始：正常用户第一次登录不该被拦。
		bucket = &tokenBucket{tokens: l.capacity, last: now}
		l.buckets[key] = bucket
	} else {
		elapsed := now.Sub(bucket.last).Seconds()
		if elapsed > 0 {
			bucket.tokens = min(l.capacity, bucket.tokens+elapsed*l.refillPerSecond)
			bucket.last = now
		}
	}
	if bucket.tokens < 1 {
		missing := 1 - bucket.tokens
		wait := time.Duration(missing / l.refillPerSecond * float64(time.Second))
		if wait < time.Second {
			wait = time.Second
		}
		return false, wait
	}
	bucket.tokens--
	return true, 0
}

// Refund 归还一个令牌，用于「这次请求其实是合法的」场景（例如登录成功）。
//
// 没有它，共享出口 IP 的多人各登一次就会把 IP 桶耗光；有了它，
// 计数只反映失败尝试——正是要限制的对象。
func (l *rateLimiter) Refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if bucket, ok := l.buckets[key]; ok {
		bucket.tokens = min(l.capacity, bucket.tokens+1)
	}
}

// sweepLocked 机会式回收空闲桶。放在请求路径上而不是起一个后台 goroutine：
// 限流器不该有自己的生命周期，调用方也不用记得关它。
func (l *rateLimiter) sweepLocked(now time.Time) {
	if l.lastSweep.IsZero() {
		l.lastSweep = now
		return
	}
	if now.Sub(l.lastSweep) < l.idleTTL {
		return
	}
	l.lastSweep = now
	for key, bucket := range l.buckets {
		if now.Sub(bucket.last) > l.idleTTL {
			delete(l.buckets, key)
		}
	}
}

// size 返回当前桶数量，仅供测试与运维观察。
func (l *rateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
