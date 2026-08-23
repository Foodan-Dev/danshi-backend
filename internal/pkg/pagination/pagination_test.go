package pagination_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
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

func TestCursorCodecRoundTripScopeAndTamperDetection(t *testing.T) {
	codec := pagination.NewCursorCodec("cursor-test-secret", "posts.latest")
	value := pagination.Cursor{
		CreatedAt: time.Date(2026, time.August, 23, 1, 2, 3, 456789000, time.UTC),
		ID:        42,
	}
	token, err := codec.Encode(value)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NotContains(t, token, "2026", "游标不得暴露时间字段")

	decoded, err := codec.Decode(token)
	require.NoError(t, err)
	require.Equal(t, value.CreatedAt, decoded.CreatedAt)
	require.Equal(t, value.ID, decoded.ID)

	_, err = pagination.NewCursorCodec("cursor-test-secret", "notifications").Decode(token)
	require.ErrorIs(t, err, pagination.ErrInvalidCursor, "端点作用域必须绑定到认证标签")

	tampered := token[:len(token)-1] + differentCursorCharacter(token[len(token)-1])
	_, err = codec.Decode(tampered)
	require.ErrorIs(t, err, pagination.ErrInvalidCursor)

	_, err = codec.DecodeRequest(pagination.CursorRequest{Token: tampered, Limit: 20})
	fieldErr := apierr.As(err)
	require.Equal(t, 422, fieldErr.Status)
	require.Equal(t, "cursor", fieldErr.Fields[0].Field)
	require.Equal(t, apierr.FieldInvalidFormat, fieldErr.Fields[0].Code)
}

func TestCursorRequestAndMetaContracts(t *testing.T) {
	request, err := pagination.ParseCursorRequest("opaque", "")
	require.NoError(t, err)
	require.Equal(t, pagination.DefaultLimit, request.Limit)
	_, err = pagination.ParseCursorRequest("opaque", "101")
	require.Error(t, err)
	require.Equal(t, "limit", apierr.As(err).Fields[0].Field)

	next := "next"
	encoded, err := json.Marshal(pagination.NewCursorHybridMeta(pagination.CursorMeta{
		Limit: 2, NextCursor: &next, HasMore: true,
	}))
	require.NoError(t, err)
	require.JSONEq(t, `{"limit":2,"next_cursor":"next","has_more":true}`, string(encoded))
	require.False(t, strings.Contains(string(encoded), "total"))

	encoded, err = json.Marshal(pagination.NewOffsetHybridMeta(
		pagination.NewMeta(pagination.Params{Page: 2, Limit: 2}, 5),
	))
	require.NoError(t, err)
	require.JSONEq(t, `{"page":2,"limit":2,"total":5,"total_pages":3}`, string(encoded))
	require.False(t, strings.Contains(string(encoded), "cursor"))
}

func differentCursorCharacter(value byte) string {
	if value == 'A' {
		return "B"
	}
	return "A"
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
