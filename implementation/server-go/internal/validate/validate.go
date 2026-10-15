// Package validate 实现内容规范校验（对齐 G-4 §5.5 + 设计 02）。
//
// 原则：
//  - 任何校验失败一律拒绝保存，不做"部分合规"落盘；
//  - 隐私要求一律引用既有白名单规则，本包不新增任何绕过白名单的例外；
//  - 校验错误返回结构化 codes，便于后台逐项提示与审计留痕。
package validate

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"edudesk/internal/model"
)

// 长度上限（设计 02 / G-4 §5.5）。
const (
	MaxTitleRunes   = 40   // 标题
	MaxSummaryRunes = 120  // 摘要/正文
	MaxLocationRunes = 30  // 位置
)

// forbiddenPatterns 禁止内容（不创建绕过白名单的例外）。
var forbiddenPatterns = []struct {
	Code string
	Re   *regexp.Regexp
}{
	{"FORBIDDEN_CONTACT", regexp.MustCompile(`(1[3-9]\d{9})|(\b\d{3,4}-\d{7,8}\b)`)},        // 手机/座机
	{"FORBIDDEN_IDCARD", regexp.MustCompile(`\b\d{17}[\dXx]\b`)},                        // 身份证号
	{"FORBIDDEN_SCORE", regexp.MustCompile(`(成绩|分数|排名|排行|分数条)`)},               // 成绩类（ADR-010 红线）
	{"FORBIDDEN_ATTENDANCE", regexp.MustCompile(`(考勤|出勤|缺勤|点名|迟到名单)`)},        // 考勤类（ADR-010 红线）
	{"FORBIDDEN_EXAM", regexp.MustCompile(`(考试安排|考场分布|准考|考试座位|成绩单)`)},      // 考试类（D-07 冻结）
	{"FORBIDDEN_PARENT", regexp.MustCompile(`(家长|监护人|家长电话|家庭住址)`)},           // 家长联系方式
	{"FORBIDDEN_TEACHER_NOTE", regexp.MustCompile(`(教师备注|班主任备注|评语)`)},           // 教师备注
	{"FORBIDDEN_STUDENT_NAME", regexp.MustCompile(`(学生姓名|学生名单|学号)`)},           // 学生个人信息
}

// Error 单条校验错误。
type Error struct {
	Field  string `json:"field"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func (e Error) Error() string { return fmt.Sprintf("%s: %s (%s)", e.Field, e.Detail, e.Code) }

// Content 校验一条内容项。返回全部错误（空表示通过）。
func Content(it model.ContentItem, allowedTypes []string) []Error {
	var errs []Error
	add := func(field, code, detail string) {
		errs = append(errs, Error{Field: field, Code: code, Detail: detail})
	}

	// ID：非空、字符集受限（避免注入路径）
	if strings.TrimSpace(it.ID) == "" {
		add("id", "REQUIRED", "内容 ID 不能为空")
	} else if !idRe.MatchString(it.ID) {
		add("id", "INVALID", "内容 ID 仅允许字母、数字、连字符与下划线")
	}

	// 类型：必须在该 profile 白名单内
	if it.Type == "" {
		add("type", "REQUIRED", "内容类型不能为空")
	} else {
		ok := false
		for _, t := range allowedTypes {
			if t == it.Type {
				ok = true
				break
			}
		}
		if !ok {
			add("type", "NOT_ALLOWED", "内容类型不在该屏允许范围内："+strings.Join(allowedTypes, "/"))
		}
	}

	// 标题：非空 + 长度
	title := strings.TrimSpace(it.Title)
	if title == "" {
		add("title", "REQUIRED", "标题不能为空")
	} else if utf8.RuneCountInString(title) > MaxTitleRunes {
		add("title", "TOO_LONG", fmt.Sprintf("标题不超过 %d 字（当前 %d）", MaxTitleRunes, utf8.RuneCountInString(title)))
	}

	// 摘要：长度
	if utf8.RuneCountInString(it.Summary) > MaxSummaryRunes {
		add("summary", "TOO_LONG", fmt.Sprintf("摘要不超过 %d 字", MaxSummaryRunes))
	}
	if utf8.RuneCountInString(it.Location) > MaxLocationRunes {
		add("location", "TOO_LONG", fmt.Sprintf("位置不超过 %d 字", MaxLocationRunes))
	}

	// 优先级：1-3
	if it.Priority != 0 && (it.Priority < 1 || it.Priority > 3) {
		add("priority", "OUT_OF_RANGE", "优先级取值 1-3")
	}

	// 时间：可解析 + from ≤ until
	if it.EffectiveFrom != "" {
		if _, err := parseTime(it.EffectiveFrom); err != nil {
			add("effective_from", "INVALID", "生效时间格式无效")
		}
	}
	if it.EffectiveUntil != "" {
		until, err := parseTime(it.EffectiveUntil)
		if err != nil {
			add("effective_until", "INVALID", "失效时间格式无效")
		} else if from, ferr := parseTime(it.EffectiveFrom); ferr == nil && until.Before(from) {
			add("effective_until", "BEFORE_FROM", "失效时间必须晚于生效时间")
		}
	}

	// 禁止内容：标题 + 摘要
	for _, f := range forbiddenPatterns {
		if f.Re.MatchString(title) || f.Re.MatchString(it.Summary) {
			add("content", f.Code, "内容命中禁止发布规则（隐私/红线），请修改后重试")
		}
	}

	// 受众：仅匿名聚合
	if it.Audience != nil {
		if it.Audience.Grade == "" {
			add("audience", "REQUIRED", "受众范围不能为空（使用年级/班级聚合，不得使用个人）")
		}
	}

	// 状态：仅允许生命周期六态
	if it.State != "" && !allowedState(it.State) {
		add("state", "INVALID", "状态取值非法")
	}
	return errs
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func allowedState(s string) bool {
	for _, x := range model.LifecycleStates {
		if x == s {
			return true
		}
	}
	return false
}