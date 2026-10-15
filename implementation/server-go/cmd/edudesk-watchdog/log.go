package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger 是看门狗服务的滚动文件日志。
//
// 设计 05 §5 要求「日志滚动 30 天 / 50 MB 上限」。这里实现按大小的滚动，
// 保留 maxFiles 个历史文件；30 天保留由运维侧清理（并在 README 说明）。
type Logger struct {
	mu       sync.Mutex
	dir      string
	path     string
	file     *os.File
	size     int64
	maxSize  int64
	maxFiles int
}

// NewLogger 创建/打开日志文件。
func NewLogger(dir string, maxSize int64, maxFiles int) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	l := &Logger{
		dir:      dir,
		path:     filepath.Join(dir, "watchdog.log"),
		maxSize:  maxSize,
		maxFiles: maxFiles,
	}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Logger) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.file = f
	l.size = st.Size()
	return nil
}

// Close 关闭日志文件。
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

func (l *Logger) rotateIfNeeded() {
	if l.size < l.maxSize {
		return
	}
	_ = l.file.Close()
	// watchdog.log.N-1 -> watchdog.log.N ... watchdog.log -> watchdog.log.1
	for i := l.maxFiles - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", l.path, i)
		to := fmt.Sprintf("%s.%d", l.path, i+1)
		_ = os.Rename(from, to)
	}
	_ = os.Rename(l.path, l.path+".1")
	l.file, _ = os.OpenFile(l.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	l.size = 0
}

// write 写一行（带 UTC+8 本地时间戳）。
func (l *Logger) write(level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	line := fmt.Sprintf("%s [%s] %s\n",
		time.Now().Format("2006-01-02 15:04:05.000"), level, msg)
	n, _ := l.file.WriteString(line)
	l.size += int64(n)
	l.rotateIfNeeded()
}

// Info 写信息级日志。
func (l *Logger) Info(format string, a ...interface{}) { l.write("INFO", fmt.Sprintf(format, a...)) }

// Warn 写告警级日志。
func (l *Logger) Warn(format string, a ...interface{}) { l.write("WARN", fmt.Sprintf(format, a...)) }

// Error 写错误级日志。
func (l *Logger) Error(format string, a ...interface{}) { l.write("ERROR", fmt.Sprintf(format, a...)) }

// State 写 JSON 快照（便于后续结构化分析）。
func (l *Logger) State(tag string, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		l.Warn("%s marshal failed: %v", tag, err)
		return
	}
	l.write("STATE", tag+" "+string(b))
}
