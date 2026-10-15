// Package heartbeat 实现「主展示进程 -> 看门狗」的存活心跳（设计 05 §2.1）。
//
// 设计 05 §2.1 规定心跳通道为「共享内存时间戳 / 命名管道」二选一。
// 本实现采用**时间戳文件**（%ProgramData%\EduDesk\heartbeat.json），理由：
//
//  1. 看门狗以 Windows 服务运行在 Session 0，而主展示进程运行在用户会话；
//     Windows 的 `Local\` 命名对象按会话隔离，服务读不到用户会话的对象。
//     `Global\` 命名对象需要 SeCreateGlobalPrivilege，且会让普通用户进程创建失败。
//  2. 文件通道跨会话可读，无需额外特权，也不引入「谁先创建对象」的竞态。
//  3. 心跳文件可被运维直接观测（R-07 空屏排障需要「最后心跳时刻」这一证据）。
//  4. 原子写（临时文件 + rename）保证读者永远读不到半截 JSON。
//
// 判据与设计完全一致：心跳时间戳距当前 > 15s 视为「主进程挂起」。
// 写入周期 5s，与设计「主进程崩溃 -> Watchdog 5s 内重启」的响应目标匹配。
package heartbeat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// WriteInterval 心跳写入周期（设计 05 §2.1）。
	WriteInterval = 5 * time.Second
	// Timeout 心跳超时阈值：无心跳 > 15s 判为主进程挂起（设计 05 §2.1）。
	Timeout = 15 * time.Second

	fileName = "heartbeat.json"
	dirName  = "EduDesk"
)

// Snapshot 是一次心跳记录。
type Snapshot struct {
	PID       int    `json:"pid"` // 主进程 PID
	Seq       uint64 `json:"seq"` // 单调递增序号（便于发现「文件被旧内容覆盖」）
	Timestamp int64  `json:"ts"`  // UnixMilli

	// At 由 Timestamp 还原，读取侧填充，不参与序列化。
	At time.Time `json:"-"`
}

// Dir 返回心跳目录。优先 %ProgramData%\EduDesk（服务与用户会话均可访问），
// 若环境变量缺失则退回用户临时目录（仅用于开发态）。
func Dir() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return filepath.Join(pd, dirName)
	}
	return filepath.Join(os.TempDir(), dirName)
}

// Path 返回心跳文件绝对路径。
func Path() string { return filepath.Join(Dir(), fileName) }

// LogDir 返回看门狗日志目录。
func LogDir() string { return filepath.Join(Dir(), "logs") }

// Read 读取当前心跳。文件不存在或内容损坏时返回 error。
func Read() (Snapshot, error) {
	var s Snapshot
	b, err := os.ReadFile(Path())
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	s.At = time.UnixMilli(s.Timestamp)
	return s, nil
}

// IsFresh 报告心跳是否在 Timeout 内更新过。
//
// 返回 (新鲜?, 快照, 读取错误)。读取失败一律视为「不新鲜」，
// 但错误会被带出以便日志区分「文件缺失」与「文件损坏」。
func IsFresh(now time.Time, timeout time.Duration) (bool, Snapshot, error) {
	if timeout <= 0 {
		timeout = Timeout
	}
	s, err := Read()
	if err != nil {
		return false, s, err
	}
	return now.Sub(s.At) < timeout, s, nil
}

// Start 启动心跳写入协程，按 interval 周期落盘。
// 传入 interval <= 0 时使用 WriteInterval。返回的 stop 幂等。
//
// 写入失败不会中断主进程，也不会 panic —— 心跳是「尽力而为」的旁路信号；
// 但失败会打印到 stderr，便于运维发现「目录权限没配好」这类部署问题。
func Start(ctx context.Context, interval time.Duration) func() {
	if interval <= 0 {
		interval = WriteInterval
	}
	stopCh := make(chan struct{})
	var once sync.Once

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		var seq uint64
		write := func() {
			seq++
			s := Snapshot{PID: os.Getpid(), Seq: seq, Timestamp: time.Now().UnixMilli()}
			if err := writeAtomic(s); err != nil {
				// 旁路信号失败不应影响主服务；仅在首次或内容变化时提示。
				if seq == 1 || seq%60 == 0 {
					os.Stderr.WriteString("heartbeat: write failed: " + err.Error() + "\n")
				}
			}
		}

		write() // 立即写一次，缩短看门狗的启动宽限期
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				write()
			}
		}
	}()

	return func() { once.Do(func() { close(stopCh) }) }
}

// writeAtomic 以「临时文件 + 原子替换」写入心跳，避免读者读到半截内容。
func writeAtomic(s Snapshot) error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}
