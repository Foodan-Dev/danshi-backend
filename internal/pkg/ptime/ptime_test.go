package ptime_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
)

// 期望值由本机 Python 实际执行 datetime.isoformat() 产出，不是手写的。
// Go 默认的 RFC3339Nano 会输出 "Z" 并裁掉尾随 0，两条都与 Python 不符，
// 这组用例就是防止有人图省事换回标准库格式。
func TestFormatMatchesPythonIsoformat(t *testing.T) {
	cases := []struct {
		in   time.Time
		want string
	}{
		{time.Date(2026, 8, 14, 3, 21, 0, 0, time.UTC), "2026-08-14T03:21:00+00:00"},
		{time.Date(2026, 8, 14, 3, 21, 0, 123456000, time.UTC), "2026-08-14T03:21:00.123456+00:00"},
		{time.Date(2026, 1, 1, 0, 0, 0, 1000, time.UTC), "2026-01-01T00:00:00.000001+00:00"},
		{time.Date(2026, 12, 31, 23, 59, 59, 999999000, time.UTC), "2026-12-31T23:59:59.999999+00:00"},
	}
	for _, c := range cases {
		if got := ptime.Format(c.in); got != c.want {
			t.Errorf("Format(%v)\n  got  %q\n  want %q", c.in, got, c.want)
		}
	}
}

// 非 UTC 输入必须先转 UTC，否则偏移会写成 +08:00。
func TestFormatConvertsToUTC(t *testing.T) {
	shanghai := time.FixedZone("CST", 8*3600)
	in := time.Date(2026, 8, 14, 11, 21, 0, 0, shanghai)
	if got := ptime.Format(in); got != "2026-08-14T03:21:00+00:00" {
		t.Fatalf("got %q", got)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	orig := ptime.Time(time.Date(2026, 8, 14, 3, 21, 0, 123456000, time.UTC))
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"2026-08-14T03:21:00.123456+00:00"` {
		t.Fatalf("marshal got %s", b)
	}
	var back ptime.Time
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Std().Equal(orig.Std()) {
		t.Fatalf("round trip mismatch: %v vs %v", back.Std(), orig.Std())
	}
}
