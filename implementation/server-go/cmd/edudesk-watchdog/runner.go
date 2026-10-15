package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
)

// procRunner 是 watchdog.Runner 的真实实现：拉起、终止、探测主进程。
type procRunner struct {
	cfg Config
	log *Logger

	mu  sync.Mutex
	cmd *exec.Cmd
	out *os.File
}

func newProcRunner(cfg Config, log *Logger) *procRunner {
	return &procRunner{cfg: cfg, log: log}
}

// Start 启动主进程，返回其 PID。
//
// 主进程的 stdout/stderr 重定向到日志文件，便于崩溃后回溯。
func (p *procRunner) Start() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var out *os.File
	if p.cfg.LogDir != "" {
		if err := os.MkdirAll(p.cfg.LogDir, 0o755); err == nil {
			f, err := os.OpenFile(filepath.Join(p.cfg.LogDir, "display-stdout.log"),
				os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err == nil {
				out = f
			}
		}
	}

	cmd := exec.Command(p.cfg.ExePath, p.cfg.Args...)
	cmd.Dir = p.cfg.WorkDir
	if out != nil {
		cmd.Stdout = out
		cmd.Stderr = out
	}

	if err := cmd.Start(); err != nil {
		if out != nil {
			_ = out.Close()
		}
		return 0, err
	}

	p.cmd = cmd
	p.out = out

	// 回收进程句柄；不做 Wait 会导致句柄泄漏。
	go func(c *exec.Cmd, f *os.File) {
		_ = c.Wait()
		if f != nil {
			_ = f.Close()
		}
	}(cmd, out)

	return cmd.Process.Pid, nil
}

// Kill 终止指定 PID。
func (p *procRunner) Kill(pid int) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// stillActive 是 GetExitCodeProcess 在进程仍存活时返回的伪退出码
// （Win32 常量 STILL_ACTIVE = STATUS_PENDING = 0x103 = 259）。
// golang.org/x/sys/windows 未导出该常量，故本地定义。
const stillActive = 259

// Alive 报告 PID 是否仍存活。
//
// 使用 OpenProcess + GetExitCodeProcess == STILL_ACTIVE 判断，
// 避免依赖信号（Windows 不支持 signal 0）。
func (p *procRunner) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
