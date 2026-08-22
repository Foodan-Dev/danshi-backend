// Package money 处理价格。
//
// 硬规则（docs/go-rewrite-plan.md §4.4）：**价格一律用 decimal 存储、用 string 收发**，
// 任何环节都不许出现 float。Python 侧把 price 声明成 float 是既有缺陷，这次一并纠正。
package money

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

var amountPattern = regexp.MustCompile(`^(?:0|[0-9]+)(?:\.[0-9]{1,2})?$`)

// Amount 是金额。JSON 里是字符串，数据库里是 numeric(10,2)。
type Amount struct {
	d decimal.Decimal
}

// FromDecimal 从精确十进制值构造金额。
func FromDecimal(d decimal.Decimal) Amount { return Amount{d: d} }

// Parse 解析客户端传来的字符串。空串视为非法，可空场景请用 *Amount。
func Parse(s string) (Amount, error) {
	if s == "" {
		return Amount{}, fmt.Errorf("金额不能为空")
	}
	if !amountPattern.MatchString(s) {
		return Amount{}, fmt.Errorf("金额格式不正确")
	}
	integerPart, _, _ := strings.Cut(s, ".")
	if len(integerPart) > 8 {
		return Amount{}, fmt.Errorf("金额整数部分最多八位")
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return Amount{}, fmt.Errorf("金额格式不正确")
	}
	if d.IsNegative() {
		return Amount{}, fmt.Errorf("金额不能为负数")
	}
	if d.Exponent() < -2 {
		return Amount{}, fmt.Errorf("金额最多保留两位小数")
	}
	return Amount{d: d.Round(2)}, nil
}

// Decimal 返回金额的精确十进制值。
func (a Amount) Decimal() decimal.Decimal { return a.d }

// String 固定两位小数，保证 "18" 与 "18.5" 都输出成 "18.00" / "18.50"，
// 前端不必自己补零。
func (a Amount) String() string { return a.d.StringFixed(2) }

// MarshalJSON 把金额编码为 JSON 字符串。
func (a Amount) MarshalJSON() ([]byte, error) { return json.Marshal(a.String()) }

// UnmarshalJSON 只接受字符串形式的金额。
func (a *Amount) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		// 明确拒绝数字形态，避免前端漏改时静默按 float 走
		return fmt.Errorf("金额必须是字符串，不能是数字")
	}
	parsed, err := Parse(s)
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}
