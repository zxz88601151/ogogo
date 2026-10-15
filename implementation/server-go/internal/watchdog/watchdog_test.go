package watchdog

import (
	"errors"
	"testing"
	"time"
)

// fakeRunner 是可编程的进程替身，记录 Start/Kill 调用。
type fakeRunner struct {
	nextPID  int
	alive    map[int]bool
	killed   []int
	starts   int
	failNext bool
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{nextPID: 1000, alive: map[int]bool{}}
}

func (f *fakeRunner) Start() (int, error) {
	if f.failNext {
		f.failNext = false
		return 0, errors.New("spawn failed")
	}
	f.starts++
	pid := f.nextPID
	f.nextPID++
	f.alive[pid] = true
	return pid, nil
}

func (f *fakeRunner) Kill(pid int) error {
	f.killed = append(f.killed, pid)
	delete(f.alive, pid)
	return nil
}

func (f *fakeRunner) Alive(pid int) bool { return f.alive[pid] }

// fakeClock 是可控时钟。
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newSup(t *testing.T) (*Supervisor, *fakeRunner, *fakeClock) {
	t.Helper()
	r := newFakeRunner()
	c := &fakeClock{t: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)}
	sup := New(r, Options{Now: c.now})
	return sup, r, c
}

// 冷启动：StateDown -> 拉起。
func TestInitialStart(t *testing.T) {
	sup, r, _ := newSup(t)
	ev := sup.Step(false, 0)
	if ev == nil || ev.Kind != EvStarted {
		t.Fatalf("首次 Step 应产生 STARTED，实际 %v", ev)
	}
	if sup.State() != StateRunning {
		t.Fatalf("应为 RUNNING，实际 %s", sup.State())
	}
	if r.starts != 1 {
		t.Fatalf("应启动 1 次，实际 %d", r.starts)
	}
}

// 启动宽限内，即使心跳尚未来得及写，也不得误判为挂起。
//
// 这条对应真实场景：进程刚 spawn、首个心跳还没落盘。
func TestStartGraceAvoidsFalseHang(t *testing.T) {
	sup, r, c := newSup(t)
	sup.Step(false, 0) // 拉起

	// 宽限内多次 tick，心跳始终不新鲜
	for i := 0; i < 5; i++ {
		c.advance(5 * time.Second)
		if ev := sup.Step(false, 0); ev != nil {
			t.Fatalf("宽限期内不应产生事件，实际 %v", ev)
		}
	}
	if r.starts != 1 {
		t.Fatalf("宽限期内不得重复启动，实际启动 %d 次", r.starts)
	}
	if sup.State() != StateRunning {
		t.Fatalf("应保持 RUNNING，实际 %s", sup.State())
	}
}

// 挂起：进程仍在但心跳超时 > 15s -> kill + restart。
func TestHangTriggersKillAndRestart(t *testing.T) {
	sup, r, c := newSup(t)
	sup.Step(false, 0)
	first := sup.PID()

	c.advance(DefaultStartGrace)          // 过宽限
	c.advance(DefaultHeartbeatTimeout + 1) // 心跳超时

	ev := sup.Step(false, first)
	if ev == nil || ev.Kind != EvStarted {
		t.Fatalf("挂起后应重启，实际事件 %v", ev)
	}
	if len(r.killed) != 1 || r.killed[0] != first {
		t.Fatalf("应先 kill 旧进程 %d，实际 killed=%v", first, r.killed)
	}
	if sup.PID() == first {
		t.Fatal("重启后 PID 应变化")
	}
	if sup.State() != StateRunning {
		t.Fatalf("应回到 RUNNING，实际 %s", sup.State())
	}
}

// 进程消失 -> 立即重启（对应设计「主进程崩溃 -> 5s 内重启」）。
func TestCrashTriggersRestart(t *testing.T) {
	sup, r, c := newSup(t)
	sup.Step(false, 0)
	first := sup.PID()

	// 模拟进程被杀
	delete(r.alive, first)
	c.advance(2 * time.Second)

	ev := sup.Step(false, 0)
	if ev == nil || ev.Kind != EvStarted {
		t.Fatalf("崩溃后应重启，实际 %v", ev)
	}
	if r.starts != 2 {
		t.Fatalf("应启动 2 次，实际 %d", r.starts)
	}
}

// 崩溃风暴：5min 内累计 3 次 -> FALLBACK，并停止重启。
func TestCrashStormEntersFallback(t *testing.T) {
	sup, r, c := newSup(t)
	sup.Step(false, 0)

	for i := 0; i < 2; i++ {
		pid := sup.PID()
		delete(r.alive, pid)
		c.advance(2 * time.Second)
		ev := sup.Step(false, 0)
		if ev == nil || ev.Kind != EvStarted {
			t.Fatalf("第 %d 次崩溃应重启，实际 %v", i+1, ev)
		}
	}
	if sup.State() != StateRunning {
		t.Fatalf("2 次崩溃后应仍在 RUNNING，实际 %s", sup.State())
	}

	// 第 3 次
	pid := sup.PID()
	delete(r.alive, pid)
	c.advance(2 * time.Second)
	ev := sup.Step(false, 0)
	if ev == nil || ev.Kind != EvFallback {
		t.Fatalf("第 3 次崩溃应进入 FALLBACK，实际 %v", ev)
	}
	if sup.State() != StateFallback {
		t.Fatalf("应处于 FALLBACK，实际 %s", sup.State())
	}

	// FALLBACK 期间不得再重启
	startedBefore := r.starts
	for i := 0; i < 10; i++ {
		c.advance(10 * time.Second)
		if ev := sup.Step(false, 0); ev != nil && ev.Kind == EvStarted {
			t.Fatalf("FALLBACK 期间不得重启，出现 %v", ev)
		}
	}
	if r.starts != startedBefore {
		t.Fatalf("FALLBACK 期间启动次数不应变化：%d -> %d", startedBefore, r.starts)
	}
}

// 窗口外崩溃不计入：4 次崩溃但每次间隔 > 5min，不应触发 FALLBACK。
func TestCrashesOutsideWindowNotCounted(t *testing.T) {
	sup, r, c := newSup(t)
	sup.Step(false, 0)

	for i := 0; i < 4; i++ {
		pid := sup.PID()
		delete(r.alive, pid)
		c.advance(6 * time.Minute) // 超出 5min 窗口
		ev := sup.Step(false, 0)
		if ev == nil || ev.Kind != EvStarted {
			t.Fatalf("第 %d 次崩溃应重启，实际 %v", i+1, ev)
		}
		if sup.State() == StateFallback {
			t.Fatalf("窗口外崩溃不应触发 FALLBACK（第 %d 次）", i+1)
		}
	}
}

// FALLBACK 冷却结束后允许一次试探恢复（工程补充项）。
func TestFallbackCooldownRetry(t *testing.T) {
	sup, r, c := newSup(t)
	sup.Step(false, 0)
	for i := 0; i < 3; i++ {
		delete(r.alive, sup.PID())
		c.advance(time.Second)
		sup.Step(false, 0)
	}
	if sup.State() != StateFallback {
		t.Fatalf("应先进入 FALLBACK，实际 %s", sup.State())
	}

	c.advance(DefaultFallbackCooldown)
	ev := sup.Step(false, 0)
	if ev == nil || ev.Kind != EvRecoverTry {
		t.Fatalf("冷却后应产生 RECOVER_TRY，实际 %v", ev)
	}
	if sup.State() != StateDown {
		t.Fatalf("RECOVER_TRY 后应回到 DOWN 以便下一轮 Step 拉起，实际 %s", sup.State())
	}

	// 下一轮 Step 真正拉起
	ev = sup.Step(false, 0)
	if ev == nil || ev.Kind != EvStarted {
		t.Fatalf("试探后应重新拉起，实际 %v", ev)
	}
	if sup.Crashes() != 0 {
		t.Fatalf("试探恢复后崩溃计数应清零，实际 %d", sup.Crashes())
	}
}

// 拉不起时不得误判为 RUNNING。
func TestStartFailureKeepsDown(t *testing.T) {
	sup, r, c := newSup(t)
	r.failNext = true
	ev := sup.Step(false, 0)
	if ev == nil || ev.Kind != EvStartFailed {
		t.Fatalf("启动失败应产生 START_FAILED，实际 %v", ev)
	}
	if sup.State() != StateDown {
		t.Fatalf("启动失败后应保持 DOWN，实际 %s", sup.State())
	}
	// 下一轮应重试
	c.advance(time.Second)
	ev = sup.Step(false, 0)
	if ev == nil || ev.Kind != EvStarted {
		t.Fatalf("下一轮应重试启动，实际 %v", ev)
	}
}

// 事件回调必须被触发（日志依赖）。
func TestEventCallbackFires(t *testing.T) {
	sup, r, c := newSup(t)
	var kinds []EventKind
	sup.OnEvent = func(e Event) { kinds = append(kinds, e.Kind) }

	sup.Step(false, 0)
	delete(r.alive, sup.PID())
	c.advance(time.Second)
	sup.Step(false, 0)

	if len(kinds) < 3 || kinds[0] != EvStarted {
		t.Fatalf("事件序列异常: %v", kinds)
	}
}

// ------------------------------------------------------------------
// 接管（Adoption）：服务重启后不得重复拉起已在健康运行的主进程。
//
// 反例后果：新实例与既有实例争抢端口 -> 启动即失败 -> 被判崩溃 ->
// 反复数次触发 FALLBACK，把本来健康的终端打黑。
// ------------------------------------------------------------------

func TestAdoptHealthyExistingProcess(t *testing.T) {
	sup, r, _ := newSup(t)
	r.alive[7777] = true // 已有一个健康进程

	ev := sup.Step(true, 7777) // 心跳新鲜且 pid 存活
	if ev == nil || ev.Kind != EvAdopted {
		t.Fatalf("应产生 ADOPTED，实际 %v", ev)
	}
	if sup.PID() != 7777 {
		t.Fatalf("应接管 pid=7777，实际 %d", sup.PID())
	}
	if sup.State() != StateRunning {
		t.Fatalf("接管后应为 RUNNING，实际 %s", sup.State())
	}
	if r.starts != 0 {
		t.Fatalf("接管场景不得重复拉起，实际启动 %d 次", r.starts)
	}

	// 接管后应正常监控：进程消失 -> 重启
	delete(r.alive, 7777)
	ev = sup.Step(false, 7777)
	if ev == nil || ev.Kind != EvStarted {
		t.Fatalf("接管后进程消失应重启，实际 %v", ev)
	}
}

func TestNoAdoptWhenHeartbeatStale(t *testing.T) {
	sup, r, _ := newSup(t)
	r.alive[7777] = true

	ev := sup.Step(false, 7777) // 进程在但心跳陈旧
	if ev == nil || ev.Kind != EvStarted {
		t.Fatalf("心跳陈旧不应接管，应冷启动，实际 %v", ev)
	}
	if r.starts != 1 {
		t.Fatalf("应启动 1 次，实际 %d", r.starts)
	}
}

func TestNoAdoptWhenProcessGone(t *testing.T) {
	sup, r, _ := newSup(t)
	// 心跳新鲜但指向的进程已不存在（例如 pid 复用/进程刚退出）
	ev := sup.Step(true, 9999)
	if ev == nil || ev.Kind != EvStarted {
		t.Fatalf("进程不存在不应接管，应冷启动，实际 %v", ev)
	}
	if r.starts != 1 {
		t.Fatalf("应启动 1 次，实际 %d", r.starts)
	}
}
