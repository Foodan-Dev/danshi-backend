// Package pagination 是分页参数。
//
// 与 Python 侧的关键差异（§4.3，破坏性变更）：**严格校验，不做宽松回落**。
// Python 把 ?page=abc 回落成 1、?limit=999 钳到 100 并返回 200，
// 这掩盖了客户端 bug，本次改为一律 422。
package pagination

import (
	"strconv"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
)

// 分页默认值与服务端上限。
const (
	DefaultPage  = 1
	DefaultLimit = 20
	MaxLimit     = 100
)

// Params 是校验后的分页参数。
type Params struct {
	Page  int
	Limit int
}

// Offset 返回 SQL 查询的零基偏移量。
func (p Params) Offset() int { return (p.Page - 1) * p.Limit }

// Parse 解析原始 query 值。空串取默认值；非空但不合法一律报错，不钳制。
func Parse(rawPage, rawLimit string) (Params, error) {
	page, err := parseOne(rawPage, "page", DefaultPage, 1, 0)
	if err != nil {
		return Params{}, err
	}
	limit, err := parseOne(rawLimit, "limit", DefaultLimit, 1, MaxLimit)
	if err != nil {
		return Params{}, err
	}
	return Params{Page: page, Limit: limit}, nil
}

func parseOne(raw, field string, defaultValue, minimum, maximum int) (int, error) {
	if raw == "" {
		return defaultValue, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apierr.InvalidField(field, apierr.CodeInvalidFormat, "%s 必须是整数", field)
	}
	if v < minimum {
		return 0, apierr.InvalidField(field, apierr.CodeOutOfRange, "%s 不能小于 %d", field, minimum)
	}
	if maximum > 0 && v > maximum {
		return 0, apierr.InvalidField(field, apierr.CodeOutOfRange, "%s 不能大于 %d", field, maximum)
	}
	return v, nil
}

// Meta 是响应里的分页信息，字段名沿用 Python 契约。
type Meta struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// NewMeta 根据分页参数和总记录数构造分页元信息。
func NewMeta(p Params, total int64) Meta {
	pages := 0
	if p.Limit > 0 {
		pages = int((total + int64(p.Limit) - 1) / int64(p.Limit))
	}
	return Meta{Page: p.Page, Limit: p.Limit, Total: total, TotalPages: pages}
}
