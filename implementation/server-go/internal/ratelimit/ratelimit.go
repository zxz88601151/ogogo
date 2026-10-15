// Package ratelimit 实现登录限流与失败锁定（P1-8）。
//
// 设计取舍：**内网也不能裸奔**。教师后台是内容唯一写入口，一旦口令被在线爆破，
// 攻击者即可向全校终端推送任意内容。因此即便部署在校园内网，也必须有速率上限。
//
// 策略（MVP 默认值，可由启动参数覆盖）：
//   - 同一来源 IP 在计数窗口内连续失败达到 maxFails 次 → 锁定 lockFor；
//   - 成功登录立即清零，避免误锁正常用户；
//   - 纯内存实现，进程重启即复位 —— 对"防爆破"足够（爆破需要持续在线），
//     且避免了把失败计数写成新的持久化状态面。
package ratelimit

import (
	"sync"
	"time"
)

// 默认策略：5 次失败锁 15 分钟，计数窗口 15 分钟。
const (
	DefaultMaxFails = 5
	DefaultLockFor  = 15 * time.Minute
	DefaultWindow   = 15 * time.Minute
)

type entry struct {
	fails       int
	firstFail   time.Time // 计数窗口起点
	lockedUntil time.Time
}

// Limiter 按 key（通常是来源 IP）计数的登录限流器。零值不可用，请用 New。
type Limiter struct {
	mu       sync.Mutex
	entries  map[string]*entry
	maxFails int
	lockFor  time.Duration
	window   time.Duration

	// now 便于单测注入时钟；生产用 time.Now。
	now func() time.Time
}

// New 构造限流器；任一端传 0 或负值则取该字段默认值。
func New(maxFails int, lockFor, window time.Duration) *Limiter {
	if maxFails <= 0 {
		maxFails = DefaultMaxFails
	}
	if lockFor <= 0 {
		lockFor = DefaultLockFor
	}
	if window <= 0 {
		window = DefaultWindow
	}
	return &Limiter{
		entries:  map[string]*entry{},
		maxFails: maxFails,
		lockFor:  lockFor,
		window:   window,
		now:      time.Now,
	}
}

// Allow 判断该 key 当前是否允许尝试登录。
// 返回 retryAfter（>0 表示被锁，需等待的时长）供 429 响应与 Retry-After 头使用。
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[key]
	if !ok {
		return true, 0
	}
	if e.lockedUntil.After(now) {
		return false, e.lockedUntil.Sub(now)
	}
	// 锁已到期或计数窗口已滑出 → 归零，等同新客
	if !e.lockedUntil.IsZero() || now.Sub(e.firstFail) > l.window {
		delete(l.entries, key)
		return true, 0
	}
	return true, 0
}

// Fail 记一次失败；达到阈值即锁定，并返回本次是否触发了锁定。
func (l *Limiter) Fail(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[key]
	if !ok || now.Sub(e.firstFail) > l.window {
		e = &entry{fails: 0, firstFail: now}
		l.entries[key] = e
	}
	e.fails++
	if e.fails >= l.maxFails {
		e.lockedUntil = now.Add(l.lockFor)
		e.fails = 0
		l.pruneLocked(now)
		return true
	}
	return false
}

// Succeed 登录成功后清零该 key 的失败计数。
func (l *Limiter) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// LockedFor 若该 key 正被锁定，返回剩余时长；否则返回 0。
func (l *Limiter) LockedFor(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return 0
	}
	if d := e.lockedUntil.Sub(l.now()); d > 0 {
		return d
	}
	return 0
}

// pruneLocked 顺带清理已解锁且窗口已过的条目，防止 map 无界增长
// （攻击者可用大量伪造源 IP 刷 key）。调用方必须已持锁。
func (l *Limiter) pruneLocked(now time.Time) {
	for k, e := range l.entries {
		if e.lockedUntil.After(now) {
			continue
		}
		if now.Sub(e.firstFail) > l.window {
			delete(l.entries, k)
		}
	}
}
