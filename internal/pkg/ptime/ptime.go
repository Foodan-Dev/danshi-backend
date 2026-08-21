// Package ptime 让 Go 输出与 Python datetime.isoformat() 逐字节一致的时间串。
//
// Python 侧 service 层直接调 isoformat()（post_transformer.py:105 等 12 处），规则是：
//   - 微秒为 0 时**完全省略**小数部分
//   - 微秒非 0 时固定 6 位
//   - 偏移写 "+00:00" 而不是 "Z"
//
// Go 默认的 RFC3339Nano 输出 "Z" 且会裁掉尾随 0，两条都不兼容，所以必须自定义。
package ptime

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	layoutNoFraction = "2006-01-02T15:04:05-07:00"
	layoutMicros     = "2006-01-02T15:04:05.000000-07:00"
)

// Format 按 Python isoformat() 的规则格式化。
// 一律转 UTC——DSN 必须带 TimeZone=UTC，否则偏移会变成 +08:00。
func Format(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond() == 0 {
		return t.Format(layoutNoFraction)
	}
	return t.Format(layoutMicros)
}

// Time 是可直接嵌进 DTO 的时间类型，序列化走 Format。
type Time time.Time

// MarshalJSON 按 Python isoformat 兼容格式编码时间。
func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(Format(time.Time(t)))
}

// UnmarshalJSON 接受 RFC3339Nano 时间字符串。
func (t *Time) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	// 入参宽松：Z 与 +00:00 都接受
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil {
		return err
	}
	*t = Time(parsed)
	return nil
}

// Std 转回标准库 time.Time。
func (t Time) Std() time.Time { return time.Time(t) }

// Ptr 把可空时间转成指针形式，nil 序列化为 null。
func Ptr(t *time.Time) *Time {
	if t == nil {
		return nil
	}
	v := Time(*t)
	return &v
}
