package privacylint

import "testing"

func TestBlacklistPII(t *testing.T) {
	cases := []struct {
		name string
		item map[string]interface{}
	}{
		{"手机号", map[string]interface{}{"id": "a", "phone": "13800000000"}},
		{"学生姓名", map[string]interface{}{"id": "a", "student_name": "张三"}},
		{"学号", map[string]interface{}{"id": "a", "student_id": "S001"}},
		{"成绩", map[string]interface{}{"id": "a", "score": 95}},
		{"考勤", map[string]interface{}{"id": "a", "attendance": "present"}},
		{"家长联系方式", map[string]interface{}{"id": "a", "parent_contact": "13800000000"}},
		{"教师备注", map[string]interface{}{"id": "a", "teacher_notes": "备注"}},
		{"考试标记", map[string]interface{}{"id": "a", "is_exam": true}},
		{"身份证", map[string]interface{}{"id": "a", "id_card": "320101199001011234"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs := LintItem(c.item)
			if len(vs) == 0 {
				t.Errorf("%s 应被黑名单拦截", c.name)
			}
		})
	}
}

func TestWhitelistRejectsUnknown(t *testing.T) {
	item := map[string]interface{}{
		"id": "a", "type": "announcement", "unknown_field": "x",
	}
	vs := LintItem(item)
	if len(vs) == 0 {
		t.Error("未知字段应被白名单拦截")
	}
}

func TestValidItemPasses(t *testing.T) {
	item := map[string]interface{}{
		"id": "ann-1", "type": "announcement", "title": "T", "summary": "S",
		"audience": map[string]interface{}{"grade": "all"},
		"priority": 1, "state": "PUBLISHED",
	}
	if vs := LintItem(item); len(vs) > 0 {
		t.Errorf("合法内容不应被拦截，实际: %v", vs)
	}
}

func TestAudienceNestedWhitelist(t *testing.T) {
	// audience 内只允许 grade / class
	item := map[string]interface{}{
		"id": "a", "audience": map[string]interface{}{"grade": "高一", "student": "x"},
	}
	vs := LintItem(item)
	if len(vs) == 0 {
		t.Error("audience 内非白名单子键应被拦截")
	}
	// 合法的 audience
	ok := map[string]interface{}{
		"id": "a", "audience": map[string]interface{}{"grade": "高一", "class": "3"},
	}
	if vs := LintItem(ok); len(vs) > 0 {
		t.Errorf("合法 audience 不应被拦截: %v", vs)
	}
}

func TestLintItems(t *testing.T) {
	items := []interface{}{
		map[string]interface{}{"id": "a", "title": "ok"},
		map[string]interface{}{"id": "b", "phone": "13800000000"},
	}
	vs, checked := LintItems(items)
	if checked != 2 {
		t.Errorf("checked 应为 2，实际 %d", checked)
	}
	if len(vs) == 0 {
		t.Error("含 PII 的数组应报告违规")
	}
}