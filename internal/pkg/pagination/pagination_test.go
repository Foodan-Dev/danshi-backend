package pagination_test

import (
	"testing"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
)

// §4.3 破坏性变更：Python 把非法分页参数悄悄回落成默认值并返回 200，
// 掩盖了客户端 bug。Go 侧一律 422。
func TestStrictRejection(t *testing.T) {
	for _, c := range []struct{ page, limit, wantField string }{
		{"abc", "", "page"},
		{"0", "", "page"},
		{"-1", "", "page"},
		{"", "999", "limit"},
		{"", "0", "limit"},
		{"", "-5", "limit"},
		{"", "x", "limit"},
	} {
		_, err := pagination.Parse(c.page, c.limit)
		if err == nil {
			t.Fatalf("page=%q limit=%q 应被拒绝，却通过了", c.page, c.limit)
		}
		e := apierr.As(err)
		if e.Status != 422 {
			t.Fatalf("期望 422，实际 %d", e.Status)
		}
		if len(e.Fields) != 1 || e.Fields[0].Field != c.wantField {
			t.Fatalf("期望字段 %s，实际 %+v", c.wantField, e.Fields)
		}
	}
}

func TestDefaultsAndOffset(t *testing.T) {
	p, err := pagination.Parse("", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Page != 1 || p.Limit != 20 || p.Offset() != 0 {
		t.Fatalf("默认值不对: %+v", p)
	}
	p, _ = pagination.Parse("3", "10")
	if p.Offset() != 20 {
		t.Fatalf("offset 应为 20，实际 %d", p.Offset())
	}
}

func TestMeta(t *testing.T) {
	m := pagination.NewMeta(pagination.Params{Page: 1, Limit: 20}, 41)
	if m.TotalPages != 3 {
		t.Fatalf("41 条按 20/页 应为 3 页，实际 %d", m.TotalPages)
	}
	if pagination.NewMeta(pagination.Params{Page: 1, Limit: 20}, 0).TotalPages != 0 {
		t.Fatal("零条应为 0 页")
	}
}
