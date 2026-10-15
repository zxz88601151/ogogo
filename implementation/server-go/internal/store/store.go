// Package store 实现内容源的持久化（服务端唯一写入方，ADR-016）。
//
// 关键规则：
//  - 只有本包可以写 content-source.json，终端永不写；
//  - 落盘采用「临时文件 + 原子重命名」，避免断电/崩溃产生半截文件；
//  - 每次写入重新签名是读取时的事，这里只保证数据完整性。
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"edudesk/internal/model"
)

// Store 内容源存储（带读写锁）。
type Store struct {
	mu   sync.RWMutex
	path string
	src  *model.Source
}

// Load 从磁盘加载内容源。
func Load(path string) (*Store, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取内容源失败: %w", err)
	}
	var src model.Source
	if err := json.Unmarshal(raw, &src); err != nil {
		return nil, fmt.Errorf("解析内容源失败: %w", err)
	}
	return &Store{path: path, src: &src}, nil
}

// Snapshot 返回内容源的深拷贝（只读快照，避免调用方并发修改内部状态）。
func (s *Store) Snapshot() model.Source {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.src.Clone()
}

// WithTransaction 在锁内执行读改写并原子落盘。
// fn 收到可修改的源；返回 error 时不落盘，保持原状。
func (s *Store) WithTransaction(fn func(src *model.Source) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 在副本上操作，失败可回滚
	work := s.src.Clone()
	if err := fn(&work); err != nil {
		return err
	}
	if err := s.persist(&work); err != nil {
		return err
	}
	s.src = &work
	return nil
}

// persist 原子落盘：写临时文件 → fsync → 重命名。
func (s *Store) persist(src *model.Source) error {
	data, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化内容源失败: %w", err)
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".content-source-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 失败路径清理

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("刷盘失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("原子替换失败: %w", err)
	}
	return nil
}