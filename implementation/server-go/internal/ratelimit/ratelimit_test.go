package ratelimit

import (
	"testing"
	"time"
)

// newTestLimiter 返回一个时钟可控的限流器。
func newTestLimiter(maxFails int, lockFor time.Duration) (*Limiter, func(time.Duration)) {
	l := New(maxFails, lockFor, DefaultWindow)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	return l, func(d time.Duration) { now = now.Add(d) }
}

func TestAllowBeforeAnyFailure(t *testing.T) {
	l, _ := newTestLimiter(DefaultMaxFails, DefaultLockFor)
	if ok, _ := l.Allow("10.0.0.1"); !ok {
		t.Error("未失败过的来源应允许登录")
	}
}

func TestLockAfterThreshold(t *testing.T) {
	l, _ := newTestLimiter(5, 15*time.Minute)
	key := "10.0.0.1"
	for i := 0; i < 4; i++ {
		if locked := l.Fail(key); locked {
			t.Fatalf("第 %d 次失败不应触发锁定", i+1)
		}
		if ok, _ := l.Allow(key); !ok {
			t.Fatalf("第 %d 次失败后仍应允许重试", i+1)
		}
	}
	if locked := l.Fail(key); !locked {
		t.Fatal("第 5 次失败应触发锁定")
	}
	ok, retry := l.Allow(key)
	if ok {
		t.Error("锁定期间不应允许登录")
	}
	if retry <= 0 || retry > 15*time.Minute {
		t.Errorf("Retry-After 应在 (0, 15m]，实际 %v", retry)
	}
}

func TestUnlockAfterLockFor(t *testing.T) {
	l, advance := newTestLimiter(3, 10*time.Minute)
	key := "10.0.0.2"
	l.Fail(key)
	l.Fail(key)
	l.Fail(key)
	if ok, _ := l.Allow(key); ok {
		t.Fatal("应处于锁定状态")
	}
	advance(10*time.Minute + time.Second)
	if ok, _ := l.Allow(key); !ok {
		t.Error("锁定期满后应恢复放行")
	}
	// 恢复后计数已清零：再失败 2 次不应立刻锁定
	l.Fail(key)
	if locked := l.Fail(key); locked {
		t.Error("解锁后计数应从零开始，2 次失败不应锁定")
	}
}

func TestSucceedResetsCounter(t *testing.T) {
	l, _ := newTestLimiter(3, 10*time.Minute)
	key := "10.0.0.3"
	l.Fail(key)
	l.Fail(key)
	l.Succeed(key) // 一次成功登录清零
	l.Fail(key)
	l.Fail(key)
	if locked := l.Fail(key); !locked {
		t.Error("清零后应重新数满 3 次才锁定")
	}
}

func TestWindowSlidesOut(t *testing.T) {
	l, advance := newTestLimiter(3, 10*time.Minute)
	key := "10.0.0.4"
	l.Fail(key)
	l.Fail(key)
	advance(DefaultWindow + time.Second) // 计数窗口滑出
	l.Fail(key)
	if locked := l.Fail(key); locked {
		t.Error("窗口已过期，失败计数应重新起算（2 次不应锁定）")
	}
}

func TestKeysAreIsolated(t *testing.T) {
	l, _ := newTestLimiter(2, 10*time.Minute)
	l.Fail("a")
	l.Fail("a")
	if ok, _ := l.Allow("a"); ok {
		t.Error("a 应被锁定")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Error("b 不应受 a 的锁定影响")
	}
}
