// Package model 定义内容源数据模型与 Display Bundle 构建。
//
// 设计依据：03-内容生命周期与同步状态机（6 态生命周期、撤回硬删、FULL-only）、
// 04-只读展示API（双 Profile 过滤）、ADR-007（撤回=硬删除+tombstone）。
package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"edudesk/internal/canon"
	"edudesk/internal/privacylint"
)

// SchemaVersion Bundle 结构版本。
const SchemaVersion = "1.0"

// ServerTimezone 服务端时区（设计 03）。
const ServerTimezone = "Asia/Shanghai"

// TTLSec 内容新鲜度 TTL（秒）。设计 03 §4：ttl_sec=900（15min）后进入 STALE（S6）。
const TTLSec = 900

// LifecycleStates 生命周期六态（仅作内容项标注，不新增状态机状态）。
var LifecycleStates = []string{"DRAFT", "PUBLISHED", "UPDATING", "WITHDRAWN", "EXPIRED", "ERROR"}

// Profile 内容类型白名单：走廊屏不含作业/课表；考试相关已由黑名单拦截（D-07 冻结）。
var profileTypes = map[string]map[string]bool{
	"classroom": {"announcement": true, "homework": true, "schedule": true, "weather": true},
	"corridor":  {"announcement": true, "event": true, "utility": true, "weather": true},
}

// AllowedTypes 返回指定 profile 允许展示的内容类型（供后台表单限定选项）。
func AllowedTypes(profile string) []string {
	set := profileTypes[profile]
	if set == nil {
		set = profileTypes["classroom"]
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Audience 受众（匿名聚合，不含个人）。
type Audience struct {
	Grade string `json:"grade,omitempty"`
	Class string `json:"class,omitempty"`
}

// ContentItem 内容项。字段与 privacylint 白名单一一对应。
type ContentItem struct {
	ID             string   `json:"id"`
	Type           string   `json:"type"`
	Title          string   `json:"title"`
	Summary        string   `json:"summary"`
	Audience       *Audience `json:"audience,omitempty"`
	Location       string   `json:"location,omitempty"`
	Priority       int      `json:"priority,omitempty"`
	PublishedAt    string   `json:"published_at,omitempty"`
	EffectiveFrom  string   `json:"effective_from,omitempty"`
	EffectiveUntil string   `json:"effective_until,omitempty"`
	UpdatedAt      string   `json:"updated_at,omitempty"`
	State          string   `json:"state"`
}

// ToMap 转为通用 map，供 privacylint 与签名使用。
func (c ContentItem) ToMap() map[string]interface{} {
	m := map[string]interface{}{
		"id":    c.ID,
		"type":  c.Type,
		"title": c.Title,
		"state": c.State,
	}
	if c.Summary != "" {
		m["summary"] = c.Summary
	}
	if c.Audience != nil {
		// 必须输出为嵌套 map，而非 Audience 结构体指针：
		// 签名的规范化 JSON 会把无法识别的类型退化为字符串，导致与终端计算结果不一致。
		a := map[string]interface{}{}
		if c.Audience.Grade != "" {
			a["grade"] = c.Audience.Grade
		}
		if c.Audience.Class != "" {
			a["class"] = c.Audience.Class
		}
		if len(a) > 0 {
			m["audience"] = a
		}
	}
	if c.Location != "" {
		m["location"] = c.Location
	}
	if c.Priority != 0 {
		m["priority"] = c.Priority
	}
	if c.PublishedAt != "" {
		m["published_at"] = c.PublishedAt
	}
	if c.EffectiveFrom != "" {
		m["effective_from"] = c.EffectiveFrom
	}
	if c.EffectiveUntil != "" {
		m["effective_until"] = c.EffectiveUntil
	}
	if c.UpdatedAt != "" {
		m["updated_at"] = c.UpdatedAt
	}
	return m
}

// Tombstone 撤回墓碑（ADR-007）。
type Tombstone struct {
	ID           string `json:"id"`
	WithdrawnAt  string `json:"withdrawn_at"`
	Reason       string `json:"reason"`
	WithdrawnBy  string `json:"withdrawn_by,omitempty"`
}

// Source 内容源（服务端唯一写入方落盘文件）。
type Source struct {
	Meta       MetaData     `json:"meta"`
	Items      []ContentItem `json:"items"`
	Tombstones []Tombstone  `json:"tombstones"`
}

// MetaData 内容源元信息。
type MetaData struct {
	School string `json:"school"`
	Note   string `json:"note"`
}

// LintResult 数据防火墙校验结果。
type LintResult struct {
	OK         bool     `json:"ok"`
	Violations []string `json:"violations"`
	Checked    int      `json:"checked"`
}

// BuildResult 构建 Bundle 结果。Err 非空表示整包被拒。
type BuildResult struct {
	Bundle map[string]interface{}
	Lint   LintResult
	Err    string
}

// BuildBundle 构建指定 profile 的 Display Bundle（FULL-only，每次下发完整包）。
//
// 规则：
//  - 仅下发该 profile 允许的类型；
//  - WITHDRAWN 状态不随包下发（硬删除）；
//  - 数据防火墙违规整包拒绝（不降级、不部分下发）；
//  - 全包签名（canon.Stamp）。
func BuildBundle(profile string, src *Source, secret string) BuildResult {
	if profile != "corridor" {
		profile = "classroom"
	}
	allowed := profileTypes[profile]

	visible := make([]interface{}, 0, len(src.Items))
	for _, it := range src.Items {
		if it.State == "WITHDRAWN" {
			continue // 硬删除
		}
		if !allowed[it.Type] {
			continue // 双 Profile 差异
		}
		visible = append(visible, it.ToMap())
	}

	lint := LintResult{OK: true, Checked: len(visible)}
	itemsRaw := make([]interface{}, 0, len(visible))
	for _, v := range visible {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		if vs := privacylint.LintItem(m); len(vs) > 0 {
			lint.OK = false
			lint.Violations = append(lint.Violations, vs...)
		}
		itemsRaw = append(itemsRaw, m)
	}
	sort.Strings(lint.Violations)
	if !lint.OK {
		return BuildResult{Lint: lint, Err: "PRIVACYLINT_BLOCKED"}
	}

	tombs := make([]interface{}, 0, len(src.Tombstones))
	for _, t := range src.Tombstones {
		// 同样必须输出为 map（不能直接放 Tombstone 结构体），否则签名与终端不一致。
		tm := map[string]interface{}{
			"id":           t.ID,
			"withdrawn_at": t.WithdrawnAt,
			"reason":       t.Reason,
		}
		if t.WithdrawnBy != "" {
			tm["withdrawn_by"] = t.WithdrawnBy
		}
		tombs = append(tombs, tm)
	}

	now := time.Now()
	// content_hash 覆盖 items + tombstones（03 §4 / §S3 的完整性校验）。
	// 终端可用同一载荷重新哈希并比对，与 signature 形成"签名 + 内容摘要"双重校验。
	payload := map[string]interface{}{
		"items":      itemsRaw,
		"tombstones": tombs,
	}
	sum := sha256.Sum256([]byte(canon.Value(payload)))
	contentHash := "sha256:" + hex.EncodeToString(sum[:])

	base := map[string]interface{}{
		"schema_version":  SchemaVersion,
		"bundle_id":       newULID(now),
		"profile":         profile,
		"generated_at":    now.Format(time.RFC3339),
		"ttl_sec":         TTLSec,
		"content_hash":    contentHash,
		"server_timezone": ServerTimezone,
		"server_epoch":    now.UnixMilli(),
		"lifecycle": map[string]interface{}{
			"model":  "6-state",
			"states": toIfaceSlice(LifecycleStates),
			"note":   "WITHDRAWN=hard-delete+tombstone; terminal must purge on reconnect",
		},
		"items":      itemsRaw,
		"tombstones": tombs,
	}
	return BuildResult{Bundle: canon.Stamp(base, secret), Lint: lint}
}

// crockford 为 ULID 使用的 Crockford Base32 字母表（去除 I L O U）。
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newULID 生成 26 字符 ULID（48bit 毫秒时间戳 + 80bit 随机数）。
// 设计 03 §4 要求 bundle_id 为 ULID。
func newULID(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	if _, err := rand.Read(b[6:]); err != nil {
		// 熵源异常时退化为时间戳填充，仍保证长度与可达性（不 panic）。
		for i := 6; i < 16; i++ {
			b[i] = byte(ms >> uint((i%6)*8))
		}
	}
	// 16 字节 = 128bit → 26 个 base32 字符（首字符仅用 2bit）。
	var out [26]byte
	// 前 10 个字符编码 50bit？ULID 编码：从高位开始每 5bit 一字符。
	// 采用标准实现：把 128bit 左移使恰好 130bit，再每 5bit 取字符。
	var bits [130]byte
	for i := 0; i < 128; i++ {
		bits[i+2] = (b[i/8] >> uint(7-(i%8))) & 1
	}
	for i := 0; i < 26; i++ {
		var v byte
		for j := 0; j < 5; j++ {
			v = (v << 1) | bits[i*5+j]
		}
		out[i] = crockford[v]
	}
	return string(out[:])
}

func toIfaceSlice(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// Withdraw 撤回：硬删除 item + 追加 tombstone（ADR-007）。
// 返回是否命中。source 由调用方负责持久化。
func (s *Source) Withdraw(id, reason, operator string, now time.Time) bool {
	for i, it := range s.Items {
		if it.ID == id {
			s.Items = append(s.Items[:i], s.Items[i+1:]...)
			s.Tombstones = append(s.Tombstones, Tombstone{
				ID:          id,
				WithdrawnAt: now.Format(time.RFC3339),
				Reason:      reason,
				WithdrawnBy: operator,
			})
			return true
		}
	}
	return false
}

// Upsert 新增或更新内容项（写方唯一入口，ADR-016）。
func (s *Source) Upsert(item ContentItem) {
	for i, it := range s.Items {
		if it.ID == item.ID {
			s.Items[i] = item
			return
		}
	}
	s.Items = append(s.Items, item)
}

// Find 按 ID 查找内容项。
func (s *Source) Find(id string) (ContentItem, bool) {
	for _, it := range s.Items {
		if it.ID == id {
			return it, true
		}
	}
	return ContentItem{}, false
}

// Remove 物理删除（管理用，不产生 tombstone；撤回必须走 Withdraw）。
func (s *Source) Remove(id string) bool {
	for i, it := range s.Items {
		if it.ID == id {
			s.Items = append(s.Items[:i], s.Items[i+1:]...)
			return true
		}
	}
	return false
}