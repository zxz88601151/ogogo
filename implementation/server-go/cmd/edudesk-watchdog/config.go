package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"edudesk/internal/heartbeat"
)

// Config 是看门狗服务的运行配置，落在 %ProgramData%\EduDesk\watchdog.json。
//
// 由 `install` 子命令写入一次，服务启动时读取。
// 之所以用配置文件而非注册表：便于运维直接查看/留证（与心跳文件同一目录）。
type Config struct {
	// ExePath 被守护的主进程（展示/后端进程）绝对路径。
	ExePath string `json:"exe_path"`
	// Args 主进程命令行参数。
	Args []string `json:"args"`
	// WorkDir 主进程工作目录。
	WorkDir string `json:"work_dir"`
	// TerminalCode 进入 FALLBACK 时展示的设备编号（设计 05 §2.1：TERMINAL-xx）。
	TerminalCode string `json:"terminal_code"`
	// TickSec 监督轮询间隔（秒）。设计未规定；2s 足以支撑 5s 重启目标。
	TickSec int `json:"tick_sec"`
	// LogDir 日志目录。
	LogDir string `json:"log_dir"`
	// RecoveryScript Windows 服务恢复策略第 3 次动作要运行的脚本。
	RecoveryScript string `json:"recovery_script"`
}

// ConfigPath 返回配置文件路径（与心跳同目录）。
func ConfigPath() string { return filepath.Join(heartbeat.Dir(), "watchdog.json") }

// StatePath 返回终端状态文件路径。
//
// 该文件是 FALLBACK 的**留证与联动载体**：服务写入当前模式与终端编号，
// 供运维巡检读取；原生展示进程形态下也可由展示进程读取后渲染 FALLBACK 屏。
func StatePath() string { return filepath.Join(heartbeat.Dir(), "terminal-state.json") }

// Tick 返回轮询间隔，零值回退 2s。
func (c Config) Tick() time.Duration {
	if c.TickSec <= 0 {
		return 2 * time.Second
	}
	return time.Duration(c.TickSec) * time.Second
}

// LoadConfig 读取配置文件。
func LoadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(ConfigPath())
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}

// SaveConfig 写配置文件。
func SaveConfig(c Config) error {
	if err := os.MkdirAll(heartbeat.Dir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), b, 0o644)
}

// TerminalState 是写入 terminal-state.json 的终态快照。
type TerminalState struct {
	Mode          string `json:"mode"` // RUNNING | FALLBACK | DOWN
	TerminalCode  string `json:"terminal_code"`
	PID           int    `json:"pid"`
	HeartbeatMS   int64  `json:"heartbeat_age_ms"`
	Crashes       int    `json:"crashes"`
	Reason        string `json:"reason,omitempty"`
	UpdatedAt     string `json:"updated_at"`
}
