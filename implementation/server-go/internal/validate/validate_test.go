package validate

import (
	"testing"
	"time"

	"edudesk/internal/model"
)

var types = model.AllowedTypes("classroom")

func okItem() model.ContentItem {
	return model.ContentItem{
		ID: "ann-test", Type: "announcement", Title: "科技节通知",
		Summary:   "本周五举办",
		Audience:  &model.Audience{Grade: "all"},
		Priority:  1,
		State:     "PUBLISHED",
		EffectiveFrom:  time.Now().Format(time.RFC3339),
		EffectiveUntil: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	}
}

func TestValidContentPasses(t *testing.T) {
	if errs := Content(okItem(), types); len(errs) > 0 {
		t.Errorf("合法内容不应报错: %v", errs)
	}
}

func TestTitleRequiredAndLength(t *testing.T) {
	it := okItem()
	it.Title = ""
	if len(Content(it, types)) == 0 {
		t.Error("空标题应被拒")
	}
	it.Title = string(make([]rune, 0, 0)) // reset
	long := make([]rune, MaxTitleRunes+1)
	for i := range long {
		long[i] = 'x'
	}
	it.Title = string(long)
	if len(Content(it, types)) == 0 {
		t.Error("超长标题应被拒")
	}
}

// G-4 §5.5 / ADR-010：禁止内容（成绩、考勤、考试、家长联系方式、身份证、手机号）。
func TestForbiddenContent(t *testing.T) {
	cases := []struct{ name, title, summary string }{
		{"成绩", "期末成绩公布", ""},
		{"考勤", "今日考勤情况", ""},
		{"考试安排", "考试安排通知", ""},
		{"家长电话", "", "请家长联系 13800000000"},
		{"身份证号", "身份证 320101199001011234 补办", ""},
		{"手机号", "", "联系电话13812345678"},
		{"学号", "学生学号核对", ""},
		{"教师备注", "班主任备注", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it := okItem()
			it.Title = c.title
			it.Summary = c.summary
			if len(Content(it, types)) == 0 {
				t.Errorf("含「%s」的禁止内容应被拦截", c.name)
			}
		})
	}
}

func TestTypeMustBeAllowed(t *testing.T) {
	it := okItem()
	it.Type = "not-a-type"
	if len(Content(it, types)) == 0 {
		t.Error("未知类型应被拒")
	}
	// event 不在教室屏白名单（走廊屏才有）
	it2 := okItem()
	it2.Type = "event"
	if len(Content(it2, types)) == 0 {
		t.Error("教室屏不应接受 event 类型")
	}
	// 但走廊屏允许
	if errs := Content(it2, model.AllowedTypes("corridor")); len(errs) > 0 {
		t.Errorf("走廊屏应接受 event: %v", errs)
	}
}

func TestIDCharset(t *testing.T) {
	it := okItem()
	it.ID = "../../etc/passwd"
	if len(Content(it, types)) == 0 {
		t.Error("非法 ID 字符应被拒（防路径注入）")
	}
}

func TestPriorityRange(t *testing.T) {
	it := okItem()
	it.Priority = 9
	if len(Content(it, types)) == 0 {
		t.Error("越界优先级应被拒")
	}
}

func TestTimeOrder(t *testing.T) {
	it := okItem()
	it.EffectiveFrom = time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	it.EffectiveUntil = time.Now().Format(time.RFC3339)
	if len(Content(it, types)) == 0 {
		t.Error("失效早于生效应被拒")
	}
}

func TestAudienceRequired(t *testing.T) {
	it := okItem()
	it.Audience = &model.Audience{} // grade 为空
	if len(Content(it, types)) == 0 {
		t.Error("受众年级为空应被拒")
	}
}