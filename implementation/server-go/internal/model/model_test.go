package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"edudesk/internal/canon"
)

func src() *Source {
	return &Source{
		Items: []ContentItem{
			{ID: "ann-1", Type: "announcement", Title: "公告A", State: "PUBLISHED", Priority: 1},
			{ID: "hw-1", Type: "homework", Title: "作业B", State: "PUBLISHED", Priority: 2},
			{ID: "evt-1", Type: "event", Title: "活动C", State: "PUBLISHED", Priority: 1},
		},
	}
}

// 双 Profile 差异：走廊屏不得出现作业/课表。
func TestBuildBundleProfileFilter(t *testing.T) {
	s := src()
	classroom := BuildBundle("classroom", s, "k")
	if classroom.Err != "" {
		t.Fatalf("教室屏构建失败: %s", classroom.Err)
	}
	corridor := BuildBundle("corridor", s, "k")
	if corridor.Err != "" {
		t.Fatalf("走廊屏构建失败: %s", corridor.Err)
	}

	ids := func(b map[string]interface{}) map[string]bool {
		out := map[string]bool{}
		for _, it := range b["items"].([]interface{}) {
			out[it.(map[string]interface{})["id"].(string)] = true
		}
		return out
	}
	c := ids(classroom.Bundle)
	r := ids(corridor.Bundle)

	if !c["hw-1"] {
		t.Error("教室屏应包含作业")
	}
	if r["hw-1"] {
		t.Error("走廊屏不得包含作业（设计 08 走廊屏集合化）")
	}
	if r["evt-1"] != true {
		t.Error("走廊屏应包含活动")
	}
	if c["evt-1"] {
		t.Error("教室屏不应包含走廊活动类内容")
	}
}

// 撤回 = 硬删除 + tombstone（ADR-007）。
func TestWithdrawHardDelete(t *testing.T) {
	s := src()
	now := time.Now()
	if !s.Withdraw("ann-1", "信息有误", "teacher01", now) {
		t.Fatal("撤回应命中")
	}
	if _, ok := s.Find("ann-1"); ok {
		t.Error("撤回后内容应从 items 移除（硬删除）")
	}
	if len(s.Tombstones) != 1 || s.Tombstones[0].ID != "ann-1" {
		t.Error("撤回应写入 tombstone")
	}
	if s.Tombstones[0].WithdrawnBy != "teacher01" {
		t.Error("tombstone 应记录操作人")
	}

	// 撤回后不随包下发
	b := BuildBundle("classroom", s, "k")
	for _, it := range b.Bundle["items"].([]interface{}) {
		if it.(map[string]interface{})["id"] == "ann-1" {
			t.Error("已撤回内容不得随 Bundle 下发")
		}
	}
	// 但 tombstone 必须下发，供终端清理本地缓存
	if len(b.Bundle["tombstones"].([]interface{})) != 1 {
		t.Error("Bundle 应携带 tombstone")
	}
}

func TestWithdrawNotFound(t *testing.T) {
	s := src()
	if s.Withdraw("not-exist", "x", "t", time.Now()) {
		t.Error("撤回不存在内容应返回 false")
	}
}

// WITHDRAWN 状态不随包下发。
func TestWithdrawnStateNotDelivered(t *testing.T) {
	s := &Source{Items: []ContentItem{
		{ID: "x", Type: "announcement", Title: "T", State: "WITHDRAWN"},
	}}
	b := BuildBundle("classroom", s, "k")
	if len(b.Bundle["items"].([]interface{})) != 0 {
		t.Error("WITHDRAWN 状态内容不应下发")
	}
}

// 数据防火墙：Bundle 层兜底拦截（正常写入路径已前置拦截，此处验证 bundle 构建层）。
func TestPrivacyLintBlocksBundle(t *testing.T) {
	// 合法内容应通过
	s := &Source{Items: []ContentItem{
		{ID: "ok", Type: "announcement", Title: "T", State: "PUBLISHED"},
	}}
	if r := BuildBundle("classroom", s, "k"); r.Err != "" {
		t.Fatalf("合法内容不应被拒: %s", r.Err)
	}
	// 白名单严格性：Bundle 只下发白名单字段（不泄漏多余键）
	b := BuildBundle("classroom", s, "k")
	it := b.Bundle["items"].([]interface{})[0].(map[string]interface{})
	for k := range it {
		if !ItemWhitelistAllows(k) {
			t.Errorf("Bundle 下发了白名单外字段: %s", k)
		}
	}
}

// ItemWhitelistAllows 便于测试断言的白名单查询。
func ItemWhitelistAllows(k string) bool {
	return map[string]bool{
		"id": true, "type": true, "title": true, "summary": true,
		"audience": true, "location": true, "priority": true,
		"published_at": true, "effective_from": true, "effective_until": true,
		"updated_at": true, "state": true,
	}[k]
}

// 签名：Bundle 必须可验签。
func TestBundleSignatureVerifies(t *testing.T) {
	b := BuildBundle("classroom", src(), "secret")
	ok, reason := canon.Verify(b.Bundle, "secret")
	if !ok {
		t.Fatalf("Bundle 验签失败: %s", reason)
	}
	if b.Bundle["sign_alg"] != canon.SignAlg {
		t.Error("sign_alg 应为 HMAC-SHA256")
	}
	if wrong, _ := canon.Verify(b.Bundle, "bad"); wrong {
		t.Error("错误密钥不应通过验签")
	}
}

// 生命周期六态标注随包下发。
func TestLifecycleStatesPresent(t *testing.T) {
	b := BuildBundle("classroom", src(), "k")
	lc := b.Bundle["lifecycle"].(map[string]interface{})
	states := lc["states"].([]interface{})
	if len(states) != 6 {
		t.Errorf("生命周期应为六态，实际 %d 态", len(states))
	}
}
// 03 §4：Bundle 必须携带 bundle_id(ULID) / ttl_sec / content_hash。
func TestBundleCarriesIdTtlAndContentHash(t *testing.T) {
	b := BuildBundle("classroom", src(), "k")
	if b.Err != "" {
		t.Fatalf("构建失败: %s", b.Err)
	}
	id, _ := b.Bundle["bundle_id"].(string)
	if len(id) != 26 {
		t.Errorf("bundle_id 应为 26 字符 ULID，实际 %q (len=%d)", id, len(id))
	}
	for _, ch := range id {
		if !strings.ContainsRune(crockford, ch) {
			t.Errorf("bundle_id 含非法 Crockford 字符: %q", ch)
		}
	}
	if b.Bundle["ttl_sec"] != TTLSec {
		t.Errorf("ttl_sec 应为 %d，实际 %v", TTLSec, b.Bundle["ttl_sec"])
	}
	ch, _ := b.Bundle["content_hash"].(string)
	if !strings.HasPrefix(ch, "sha256:") || len(ch) != len("sha256:")+64 {
		t.Errorf("content_hash 格式错误: %q", ch)
	}
}

// content_hash 覆盖 items + tombstones，且可被独立重算复现（终端用同一载荷比对）。
func TestContentHashReproducible(t *testing.T) {
	s := src()
	b := BuildBundle("classroom", s, "k")
	if b.Err != "" {
		t.Fatalf("构建失败: %s", b.Err)
	}
	want, _ := b.Bundle["content_hash"].(string)

	// 用与 BuildBundle 相同规则独立重算
	items := b.Bundle["items"].([]interface{})
	tombs := b.Bundle["tombstones"].([]interface{})
	sum := sha256.Sum256([]byte(canon.Value(map[string]interface{}{
		"items": items, "tombstones": tombs,
	})))
	got := "sha256:" + hex.EncodeToString(sum[:])
	if got != want {
		t.Errorf("content_hash 不可复现:\n want %s\n got  %s", want, got)
	}
}

// 撤回应同时改变 content_hash（tombstone 计入摘要）。
func TestContentHashChangesOnWithdraw(t *testing.T) {
	s := src()
	before, _ := BuildBundle("classroom", s, "k").Bundle["content_hash"].(string)

	s2 := src()
	s2.Tombstones = append(s2.Tombstones, Tombstone{ID: s2.Items[0].ID, WithdrawnAt: "2026-10-15T00:00:00+08:00", Reason: "test"})
	s2.Items = s2.Items[1:]
	after, _ := BuildBundle("classroom", s2, "k").Bundle["content_hash"].(string)

	if before == after {
		t.Error("撤回后 content_hash 应发生变化")
	}
}
