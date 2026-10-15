package model

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"edudesk/internal/canon"
)

// 回归保护：含 audience（嵌套对象）与 tombstones 的 Bundle，
// 其签名与 content_hash 必须能被「用 JSON 解析后的等价对象」独立重算复现。
//
// 背景：ToMap 曾把 audience 输出为 Go 结构体指针，canon.Value 遇结构体退化为
// fmt.Sprint 字符串，导致服务端签名与终端（JSON.parse 后）计算结果不一致 ——
// 终端会因此永久进入 DEGRADED。此测试锁定该缺陷不复发。
func TestBundleSignatureReproducibleWithNestedObjects(t *testing.T) {
	s := &Source{
		Items: []ContentItem{
			{
				ID: "ann-1", Type: "announcement", Title: "公告A", State: "PUBLISHED",
				Summary: "摘要", Priority: 1,
				Audience:      &Audience{Grade: "高一", Class: "3"},
				EffectiveFrom: "2026-10-15T09:00:00+08:00",
			},
			{ID: "hw-1", Type: "homework", Title: "作业B", State: "PUBLISHED", Audience: &Audience{Grade: "高二"}},
		},
		Tombstones: []Tombstone{
			{ID: "old-1", WithdrawnAt: "2026-10-15T10:00:00+08:00", Reason: "内容有误", WithdrawnBy: "teacher01"},
		},
	}

	// 由于 tombstone 的存在会同时校验撤回硬删除逻辑，这里只关心签名/摘要：
	// 撤回项本就不在 items 中，不影响。
	b := BuildBundle("classroom", s, "k")
	if b.Err != "" {
		t.Fatalf("构建失败: %s", b.Err)
	}

	// 1) 签名可验
	if ok, reason := canon.Verify(b.Bundle, "k"); !ok {
		t.Fatalf("签名无法验证: %s", reason)
	}

	// 2) content_hash 可独立重算（模拟终端：仅用 items + tombstones）
	payload := map[string]interface{}{
		"items":      b.Bundle["items"],
		"tombstones": b.Bundle["tombstones"],
	}
	sum := sha256.Sum256([]byte(canon.Value(payload)))
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got := b.Bundle["content_hash"]; got != want {
		t.Errorf("content_hash 不可复现（服务端=%v 重算=%s）", got, want)
	}

	// 3) 关键：载荷中不得出现结构体退化产物（"&{" ），audience 必须为对象
	cv := canon.Value(map[string]interface{}{"items": b.Bundle["items"], "tombstones": b.Bundle["tombstones"]})
	if contains(cv, "&{") || contains(cv, "string:") {
		t.Errorf("规范化载荷疑似包含结构体退化内容: %s", cv)
	}
	if !contains(cv, `"audience":{"class":"3","grade":"高一"}`) {
		t.Errorf("audience 未被规范化为嵌套对象: %s", cv)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
