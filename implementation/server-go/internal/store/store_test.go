package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"edudesk/internal/model"
)

func nowStub() time.Time { return time.Now() }

func writeSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "content-source.json")
	raw := `{"meta":{"school":"测试"},"items":[{"id":"a","type":"announcement","title":"T","state":"PUBLISHED"}],"tombstones":[]}`
	if err := os.WriteFile(p, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAndSnapshot(t *testing.T) {
	s, err := Load(writeSource(t))
	if err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if len(snap.Items) != 1 || snap.Items[0].ID != "a" {
		t.Errorf("快照内容异常: %+v", snap.Items)
	}
	// 修改快照不得影响内部状态
	snap.Items[0].Title = "改了"
	if s.Snapshot().Items[0].Title == "改了" {
		t.Error("快照必须是深拷贝，外部修改不应污染内部状态")
	}
}

func TestTransactionPersists(t *testing.T) {
	p := writeSource(t)
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	err = s.WithTransaction(func(src *model.Source) error {
		src.Upsert(model.ContentItem{ID: "b", Type: "event", Title: "新内容", State: "PUBLISHED"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 重新从磁盘加载，验证已落盘
	s2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	snap := s2.Snapshot()
	if _, ok := snap.Find("b"); !ok {
		t.Error("事务应已原子落盘")
	}
}

func TestTransactionRollbackOnError(t *testing.T) {
	p := writeSource(t)
	s, _ := Load(p)
	wantErr := os.ErrInvalid
	_ = s.WithTransaction(func(src *model.Source) error {
		src.Upsert(model.ContentItem{ID: "c", Type: "event", Title: "不该出现", State: "PUBLISHED"})
		return wantErr
	})
	// 内存与磁盘都不应被改变
	snap := s.Snapshot()
	if _, ok := snap.Find("c"); ok {
		t.Error("事务失败后内存不应变更")
	}
	s2, _ := Load(p)
	snap2 := s2.Snapshot()
	if _, ok := snap2.Find("c"); ok {
		t.Error("事务失败不应落盘")
	}
}

// 撤回通过 store 事务落盘，并保留 tombstone（G-4 §5.8 审计与硬删除解耦）。
func TestWithdrawPersistsTombstone(t *testing.T) {
	p := writeSource(t)
	s, _ := Load(p)
	err := s.WithTransaction(func(src *model.Source) error {
		if !src.Withdraw("a", "测试撤回", "teacher01", nowStub()) {
			return os.ErrNotExist
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := Load(p)
	snap := s2.Snapshot()
	if _, ok := snap.Find("a"); ok {
		t.Error("撤回应硬删除")
	}
	if len(snap.Tombstones) != 1 || snap.Tombstones[0].ID != "a" {
		t.Error("撤回应保留 tombstone")
	}
}