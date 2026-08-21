package money_test

import (
	"encoding/json"
	"testing"

	"github.com/jingyijun/danshi_backend_go/internal/pkg/money"
)

func TestParseAndFormat(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"18", "18.00"}, {"18.5", "18.50"}, {"0", "0.00"}, {"1234.56", "1234.56"},
	} {
		a, err := money.Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		if a.String() != c.want {
			t.Fatalf("Parse(%q).String() = %q, want %q", c.in, a.String(), c.want)
		}
	}
}

func TestRejectsBadInput(t *testing.T) {
	for _, in := range []string{"", "abc", "-1", "1.234"} {
		if _, err := money.Parse(in); err == nil {
			t.Fatalf("%q 应被拒绝", in)
		}
	}
}

// 前端漏改时会传数字，必须报错而不是静默按 float 走——那正是本次要消灭的缺陷。
func TestRejectsJSONNumber(t *testing.T) {
	var a money.Amount
	if err := json.Unmarshal([]byte(`18.5`), &a); err == nil {
		t.Fatal("数字形态的金额应被拒绝")
	}
	if err := json.Unmarshal([]byte(`"18.5"`), &a); err != nil {
		t.Fatalf("字符串形态应被接受: %v", err)
	}
	if a.String() != "18.50" {
		t.Fatalf("got %s", a.String())
	}
}
