package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"

	"edudesk/internal/heartbeat"
	"edudesk/internal/watchdog"
)

// serviceHandler 实现 svc.Handler：把 SCM 生命周期接到监督循环上。
type serviceHandler struct {
	cfg Config
	log *Logger
}

// Execute 是 Windows 服务的入口（由 SCM 调用）。
func (h *serviceHandler) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}
	h.log.Info("service starting | exe=%s | args=%v | tick=%s | heartbeat=%s",
		h.cfg.ExePath, h.cfg.Args, h.cfg.Tick(), heartbeat.Path())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := newProcRunner(h.cfg, h.log)
	sup := watchdog.New(runner, watchdog.Options{})
	sup.OnEvent = func(e watchdog.Event) {
		switch e.Kind {
		case watchdog.EvFallback:
			h.log.Error("event: %s", e.String())
		case watchdog.EvKillFailed, watchdog.EvStartFailed:
			h.log.Warn("event: %s", e.String())
		default:
			h.log.Info("event: %s", e.String())
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.monitor(ctx, sup)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepted}

	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			changes <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			h.log.Info("service stopping (display process is NOT terminated by design)")
			changes <- svc.Status{State: svc.StopPending}
			cancel()
			<-done
			return false, 0
		}
	}

	cancel()
	<-done
	return false, 0
}

// monitor 是监督循环主体。
func (h *serviceHandler) monitor(ctx context.Context, sup *watchdog.Supervisor) {
	tick := time.NewTicker(h.cfg.Tick())
	defer tick.Stop()
	// 设计 05 W-09：每 8h 写一条心跳日志（长周期存活证据）。
	hbTick := time.NewTicker(8 * time.Hour)
	defer hbTick.Stop()
	// 状态文件写入节流：状态变化时立即写，否则每 30s 刷新一次。
	stateTick := time.NewTicker(30 * time.Second)
	defer stateTick.Stop()

	var lastState watchdog.State
	hasLast := false
	var lastSnap heartbeat.Snapshot
	var lastFresh bool

	step := func() {
		fresh, snap, err := heartbeat.IsFresh(time.Now(), heartbeat.Timeout)
		if err != nil {
			// 文件缺失/损坏在冷启动阶段是正常的，降为 debug 噪音级别：仅在状态变化时提示
		}
		lastSnap, lastFresh = snap, fresh
		sup.Step(fresh, snap.PID)
		if !hasLast || sup.State() != lastState {
			h.log.Info("state %s -> %s (pid=%d fresh=%v age=%s)",
				stateName(lastState, hasLast), sup.State(), sup.PID(), fresh, ageOf(snap))
			lastState = sup.State()
			hasLast = true
			h.writeState(sup, fresh, snap)
		}
	}

	for {
		select {
		case <-ctx.Done():
			h.log.Info("monitor loop stopped")
			h.writeState(sup, lastFresh, lastSnap)
			return
		case <-tick.C:
			step()
		case <-stateTick.C:
			h.writeState(sup, lastFresh, lastSnap)
		case <-hbTick.C:
			h.log.Info("heartbeat | pid=%d state=%s fresh=%v age=%s crashes=%d",
				sup.PID(), sup.State(), lastFresh, ageOf(lastSnap), sup.Crashes())
		}
	}
}

// writeState 落盘终端状态（FALLBACK 的留证与联动载体）。
func (h *serviceHandler) writeState(sup *watchdog.Supervisor, fresh bool, snap heartbeat.Snapshot) {
	mode := sup.State().String()
	reason := ""
	switch sup.State() {
	case watchdog.StateFallback:
		mode = "FALLBACK"
		reason = "restart loop halted: crash storm detected"
		if !sup.FallbackAt().IsZero() {
			reason += " at " + sup.FallbackAt().Format(time.RFC3339)
		}
	case watchdog.StateRunning:
		mode = "RUNNING"
	case watchdog.StateDown:
		mode = "DOWN"
	}

	st := TerminalState{
		Mode:         mode,
		TerminalCode: h.cfg.TerminalCode,
		PID:          sup.PID(),
		Crashes:      sup.Crashes(),
		Reason:       reason,
		UpdatedAt:    time.Now().Format(time.RFC3339),
	}
	if !snap.At.IsZero() {
		st.HeartbeatMS = time.Since(snap.At).Milliseconds()
	}
	_ = fresh

	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	tmp := StatePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, StatePath())
}

func stateName(s watchdog.State, has bool) string {
	if !has {
		return "(init)"
	}
	return s.String()
}

func ageOf(s heartbeat.Snapshot) string {
	if s.At.IsZero() {
		return "n/a"
	}
	return time.Since(s.At).Truncate(time.Millisecond).String()
}
