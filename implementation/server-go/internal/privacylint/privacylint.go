// Package privacylint 实现数据防火墙（对齐设计 02 / ADR-016 / ADR-017）。
//
// 职责：
//  1. 黑名单硬拒绝：内容项出现 PII / 成绩 / 考勤 / 联系方式 / 教师备注 / 考试字段，整包拒绝；
//  2. 白名单严格校验：内容项键必须是白名单子集，未知键一律拒绝；
//  3. 不提供任何绕过白名单的例外。
package privacylint

import (
	"fmt"
	"sort"
)

// ItemWhitelist 内容项允许出现的字段（设计 02 白名单）。
var ItemWhitelist = map[string]bool{
	"id":              true,
	"type":            true,
	"title":           true,
	"summary":         true,
	"audience":        true,
	"location":        true,
	"priority":        true,
	"published_at":    true,
	"effective_from":  true,
	"effective_until": true,
	"updated_at":      true,
	"state":           true,
}

// NestedWhitelist 嵌套容器允许的子键。
var NestedWhitelist = map[string]map[string]bool{
	"audience": {"grade": true, "class": true},
}

// ItemBlacklist 命中即整包拒绝（PII / 成绩 / 考勤 / 联系方式 / 备注 / 考试）。
var ItemBlacklist = map[string]bool{
	"student_id": true, "student_full_name": true, "student_name": true, "name": true,
	"grade_score": true, "score": true, "exam": true, "exam_score": true, "grades": true, "rank": true,
	"attendance": true, "attend": true, "phone": true, "mobile": true, "tel": true,
	"parent": true, "parent_contact": true,
	"teacher_notes": true, "note": true, "remark": true,
	"address": true, "birth": true, "birthday": true,
	"id_card": true, "face": true, "photo": true, "avatar": true,
	"is_exam": true,
}

// LintItem 校验单个内容项，返回违规描述列表（空表示通过）。
func LintItem(item map[string]interface{}) []string {
	var violations []string
	scan(item, "item", "", &violations)
	sort.Strings(violations)
	return violations
}

// LintItems 校验内容项数组。
func LintItems(items []interface{}) (violations []string, checked int) {
	for i, it := range items {
		m, ok := it.(map[string]interface{})
		if !ok {
			violations = append(violations, fmt.Sprintf("#%d NOT_OBJECT", i))
			continue
		}
		for _, v := range LintItem(m) {
			violations = append(violations, fmt.Sprintf("#%d %s", i, v))
		}
	}
	return violations, len(items)
}

func scan(node interface{}, path, parentKey string, out *[]string) {
	switch t := node.(type) {
	case []interface{}:
		for i, child := range t {
			scan(child, fmt.Sprintf("%s[%d]", path, i), parentKey, out)
		}
	case map[string]interface{}:
		sub := ItemWhitelist
		if parentKey != "" {
			if nested, ok := NestedWhitelist[parentKey]; ok {
				sub = nested
			}
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			here := path + "." + k
			if ItemBlacklist[k] {
				*out = append(*out, "BLACKLIST:"+here)
			} else if !sub[k] {
				*out = append(*out, "UNKNOWN:"+here)
			}
			scan(t[k], here, k, out)
		}
	}
}