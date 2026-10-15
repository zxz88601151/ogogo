// Package watchdog 实现设计 05 §2.1 的双进程看门狗**判定逻辑**。
//
// 本包只做纯逻辑与状态迁移，不直接触碰进程、文件或时钟：
// 进程启停通过 Runner 接口注入，时间通过 Options.Now 注入。
// 这样「15s 心跳超时 -> 重启」「5min 内连续 3 次崩溃 -> FALLBACK」
// 这些关键安全/可用性行为可以被真实单元测试覆盖，而不是只靠架构描述。
//
// 设计依据（05 §2.1）：
//
//	| 场景                | 策略                                  |
//	| 主进程崩溃           | Watchdog 5 s 内重启                    |
//	| 主进程挂起（心跳超时） | 心跳超时 15 s -> kill + restart         |
//	| 连续崩溃 >= 3 次      | 停止重启循环；FALLBACK 屏 + 写本地日志    |
package watchdog

import (
	"fmt"
	"time"
)

// Runner 抽象「被守护进程」的启停与存活查询。
type Runner interface {
	// Start 启动主进程并返回其 PID。
	Start() (int, error)
	// Kill 终止指定 PID（进程已退出时应返回 nil 或可忽略的错误）。
	Kill(pid int) error
	// Alive 报告 PID 是否仍存活。
	Alive(pid int) bool
}

// Options 是监督参数。零值会被 Normalize 填为设计默认值。
type Options struct {
	HeartbeatTimeout time.Duration // 心跳超时（设计：15s）
	CrashWindow      time.Duration // 崩溃统计窗口（设计：5min）
	CrashLimit       int           // 窗口内崩溃上限（设计：3）
	StartGrace       time.Duration // 启动宽限：新进程尚未写出心跳的容忍时间
	FallbackCooldown time.Duration // FALLBACK 后允许再次试探的冷却时间
	Now              func() time.Time
}

// 设计 05 §2.1 的默认参数。
const (
	DefaultHeartbeatTimeout = 15 * time.Second
	DefaultCrashWindow      = 5 * time.Minute
	DefaultCrashLimit       = 3
	// DefaultStartGrace 是工程补充：进程刚刚拉起、尚未写出首个心跳前的宽限。
	// 设计未规定该值；若不设，每一轮冷启动都会被误判为「挂起」。
	DefaultStartGrace = 30 * time.Second
	// DefaultFallbackCooldown 是工程补充：设计规定崩溃风暴时「停止重启循环」，
	// 但未规定是否永久停止。永久停止会复现设计明确反对的「走廊长期黑屏」，
	// 因此这里允许冷却后**单次试探**恢复。该补充已登记为待 Owner 确认项。
	DefaultFallbackCooldown = 30 * time.Minute
)

// Normalize 补齐零值。
func (o Options) Normalize() Options {
	if o.HeartbeatTimeout <= 0 {
		o.HeartbeatTimeout = DefaultHeartbeatTimeout
	}
	if o.CrashWindow <= 0 {
		o.CrashWindow = DefaultCrashWindow
	}
	if o.CrashLimit <= 0 {
		o.CrashLimit = DefaultCrashLimit
	}
	if o.StartGrace <= 0 {
		o.StartGrace = DefaultStartGrace
	}
	if o.FallbackCooldown <= 0 {
		o.FallbackCooldown = DefaultFallbackCooldown
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// State 是监督器状态。
type State int

const (
	// StateDown 主进程未运行，尚未（或无需）拉起。
	StateDown State = iota
	// StateRunning 主进程运行中。
	StateRunning
	// StateFallback 崩溃风暴，已停止重启循环并进入 FALLBACK。
	StateFallback
)

func (s State) String() string {
	switch s {
	case StateDown:
		return "DOWN"
	case StateRunning:
		return "RUNNING"
	case StateFallback:
		return "FALLBACK"
	default:
		return "UNKNOWN"
	}
}

// EventKind 是监督事件类型，供调用方写日志/审计。
type EventKind string

const (
	EvStarted     EventKind = "STARTED"       // 拉起成功
	EvAdopted     EventKind = "ADOPTED"       // 接管已在运行且心跳健康的主进程
	EvStartFailed EventKind = "START_FAILED"  // 拉起失败
	EvCrash       EventKind = "CRASH"         // 检测到进程消失
	EvHang        EventKind = "HANG"          // 心跳超时（进程仍在）
	EvFallback    EventKind = "FALLBACK"      // 进入 FALLBACK
	EvRecoverTry  EventKind = "RECOVER_TRY"   // 冷却后单次试探恢复
	EvKillFailed  EventKind = "KILL_FAILED"   // 终止进程失败
)

// Event 是一次监督动作的留痕。
type Event struct {
	Kind    EventKind
	PID     int
	Detail  string
	At      time.Time
	State   State
	Crashes int // 当前窗口内崩溃计数
}

func (e Event) String() string {
	msg := fmt.Sprintf("%s pid=%d state=%s crashes=%d", e.Kind, e.PID, e.State, e.Crashes)
	if e.Detail != "" {
		msg += " " + e.Detail
	}
	return msg
}

// Supervisor 是设计 05 §2.1 的判定核心。
//
// 典型用法：调用方每隔一个 tick（建议 2s）调用一次 Step，并把心跳新鲜度传入。
type Supervisor struct {
	opt    Options
	runner Runner

	state  State
	pid    int
	startedAt  time.Time
	fallbackAt time.Time
	crashes    []time.Time

	// OnEvent 若非 nil，每个事件都会被回调（用于日志）。
	OnEvent func(Event)
}

// New 创建一个监督器。
func New(r Runner, opt Options) *Supervisor {
	return &Supervisor{opt: opt.Normalize(), runner: r, state: StateDown}
}

// State 返回当前状态。
func (s *Supervisor) State() State { return s.state }

// PID 返回被守护进程的 PID（0 表示未知）。
func (s *Supervisor) PID() int { return s.pid }

// Crashes 返回当前崩溃窗口内的崩溃次数。
func (s *Supervisor) Crashes() int { return len(s.crashes) }

// FallbackAt 返回进入 FALLBACK 的时刻（零值表示未进入）。
func (s *Supervisor) FallbackAt() time.Time { return s.fallbackAt }

// Step 推进一次监督循环。
//
// hbFresh 表示心跳是否新鲜；hbPID 是心跳文件里记录的 PID（0 表示读不到）。
// 两者用于「接管」判定：服务重启后若已有健康主进程，应先接管而非重复拉起。
// 返回本次产生的事件（可能为 nil）。
func (s *Supervisor) Step(hbFresh bool, hbPID int) *Event {
	now := s.opt.Now()

	switch s.state {
	case StateDown:
		// 接管（Adoption）：若心跳文件指向一个**仍然存活且心跳新鲜**的进程，
		// 说明主进程正在健康运行，只是本监督器刚启动（服务重启、服务恢复策略
		// 重启服务、就地升级等场景）。
		//
		// 此时若盲目再拉起一个实例，会与既有实例争抢监听端口，导致新实例启动
		// 即失败并被判为崩溃，反复几次后触发 FALLBACK —— 把本来健康的终端打黑。
		// 因此必须先接管、后启动。
		if hbPID > 0 && hbFresh && s.runner.Alive(hbPID) {
			s.pid = hbPID
			s.startedAt = now
			s.state = StateRunning
			ev := s.emit(EvAdopted, now, "adopted live process from heartbeat")
			return &ev
		}
		return s.start(now, "initial")

	case StateRunning:
		// 1) 进程是否还在？
		if !s.runner.Alive(s.pid) {
			return s.handleCrash(now, EvCrash, "process gone")
		}
		// 2) 心跳是否陈旧？（仅在过了启动宽限之后判定）
		if now.Sub(s.startedAt) >= s.opt.StartGrace && !hbFresh {
			// 进程还在但心跳停了 —— 挂起，需要先 kill 再重启
			if err := s.runner.Kill(s.pid); err != nil {
				ev := s.emit(EvKillFailed, now, err.Error())
				// kill 失败不应让状态机卡死：仍按崩溃处理，交由下一轮重试
				_ = ev
			}
			return s.handleCrash(now, EvHang, "heartbeat stale")
		}
		return nil

	case StateFallback:
		// 冷却期后允许一次试探性恢复（工程补充，见 DefaultFallbackCooldown）。
		if now.Sub(s.fallbackAt) >= s.opt.FallbackCooldown {
			s.crashes = nil
			s.state = StateDown
			ev := s.emit(EvRecoverTry, now, "cooldown elapsed, retrying")
			return &ev
		}
		return nil
	}
	return nil
}

// start 拉起主进程。
func (s *Supervisor) start(now time.Time, reason string) *Event {
	pid, err := s.runner.Start()
	if err != nil {
		ev := s.emit(EvStartFailed, now, fmt.Sprintf("%s: %v", reason, err))
		return &ev
	}
	s.pid = pid
	s.startedAt = now
	s.state = StateRunning
	ev := s.emit(EvStarted, now, reason)
	return &ev
}

// handleCrash 记录一次崩溃，并决定「重启」还是「进入 FALLBACK」。
func (s *Supervisor) handleCrash(now time.Time, kind EventKind, detail string) *Event {
	s.crashes = append(s.crashes, now)
	// 裁剪窗口外的记录
	cut := now.Add(-s.opt.CrashWindow)
	kept := s.crashes[:0]
	for _, t := range s.crashes {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	s.crashes = kept

	// 先发崩溃事件
	_ = s.emit(kind, now, detail)

	if len(s.crashes) >= s.opt.CrashLimit {
		s.state = StateFallback
		s.fallbackAt = now
		ev := s.emit(EvFallback, now,
			fmt.Sprintf("%d crashes within %s, restart loop halted", len(s.crashes), s.opt.CrashWindow))
		return &ev
	}

	// 未达上限 -> 立即重启（受心跳周期约束，实际恢复时间由调用方 tick 粒度决定）
	s.state = StateDown
	return s.start(now, "restart after "+string(kind))
}

// emit 构造事件并回调。
func (s *Supervisor) emit(kind EventKind, now time.Time, detail string) Event {
	ev := Event{
		Kind:    kind,
		PID:     s.pid,
		Detail:  detail,
		At:      now,
		State:   s.state,
		Crashes: len(s.crashes),
	}
	if s.OnEvent != nil {
		s.OnEvent(ev)
	}
	return ev
}
