package validate

import "time"

func parseTime(s string) (time.Time, error) {
	// 接受 RFC3339（含时区偏移），这是设计 03 对时间字段的唯一要求。
	return time.Parse(time.RFC3339, s)
}