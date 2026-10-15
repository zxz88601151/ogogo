// Package audit 实现操作审计留痕（对齐 G-4 §5.8）。
//
// 要求：
//  - 创建/编辑/撤回/续期/权限变更等操作全部留痕；
//  - 审计记录只追加（append-only），不可篡改；
//  - 与撤回硬删除解耦：撤回内容从展示层消失，但审计证据保留。
package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Action 操作类型。
type Action string

const (
	ActionLoginSuccess Action = "LOGIN_SUCCESS"
	ActionLoginFailure Action = "LOGIN_FAILURE"
	ActionCreate       Action = "CREATE"
	ActionUpdate       Action = "UPDATE"
	ActionWithdraw     Action = "WITHDRAW"
	ActionPublishGate  Action = "PUBLISH_GATE_DENY"
)

// Entry 审计条目。
type Entry struct {
	Time     time.Time `json:"time"`
	Actor    string    `json:"actor"`
	Role     string    `json:"role"`
	Action   Action    `json:"action"`
	Target   string    `json:"target,omitempty"`
	Result   string    `json:"result"` // OK / DENIED / ERROR
	Reason   string    `json:"reason,omitempty"`
	ClientIP string    `json:"client_ip,omitempty"`
}

// Log 追加式审计日志。
type Log struct {
	mu   sync.Mutex
	path string
}

// New 创建审计日志（追加模式，不存在则创建）。
func New(path string) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	return &Log{path: path}, nil
}

// Append 追加一条审计记录。
func (l *Log) Append(e Entry) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
}

// Tail 读取最近 n 条（供后台查看；只读不改）。
func (l *Log) Tail(n int) ([]Entry, error) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return nil, err
	}
	var all []Entry
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var e Entry
		if json.Unmarshal(line, &e) == nil {
			all = append(all, e)
		}
	}
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}