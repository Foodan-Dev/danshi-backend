package middleware

import (
	"errors"
	"testing"
)

// 缺失与格式错误必须是两个可区分的哨兵错误：它们会作为 cause 进入服务端日志，
// 而排查线上问题时，「客户端根本没带令牌」和「带了但格式坏了」指向完全不同的原因。
func TestBearerTokenDistinguishesMissingFromMalformed(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   error
	}{
		{"空头", "", errMissingAuthorization},
		{"只有空白", "   ", errMissingAuthorization},
		{"没有 scheme", "abc.def.ghi", errMalformedAuthorization},
		{"scheme 不对", "Basic abc", errMalformedAuthorization},
		{"有 Bearer 但没令牌", "Bearer ", errMalformedAuthorization},
		{"Bearer 后只有空白", "Bearer    ", errMalformedAuthorization},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			token, err := bearerToken(c.header)
			if !errors.Is(err, c.want) {
				t.Fatalf("bearerToken(%q) 的错误 = %v，期望 %v", c.header, err, c.want)
			}
			if token != "" {
				t.Fatalf("失败时不应返回令牌，实际 %q", token)
			}
		})
	}
}

func TestBearerTokenAcceptsValidHeader(t *testing.T) {
	cases := map[string]string{
		"标准写法":      "Bearer abc.def.ghi",
		"scheme 小写": "bearer abc.def.ghi",
		"两侧有空白":     "  Bearer   abc.def.ghi  ",
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			token, err := bearerToken(header)
			if err != nil {
				t.Fatalf("bearerToken(%q) 返回错误 %v", header, err)
			}
			if token != "abc.def.ghi" {
				t.Fatalf("解析出的令牌 = %q，期望 %q", token, "abc.def.ghi")
			}
		})
	}
}
