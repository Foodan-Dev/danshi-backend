package jwtx_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jingyijun/danshi_backend_go/internal/pkg/jwtx"
)

const secret = "a-sufficiently-long-secret-value-1234567890"

func TestSignAndParse(t *testing.T) {
	c := jwtx.NewCodec(secret)
	tok, err := c.Sign(42, 7, jwtx.TypeAccess, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := c.Parse(tok, jwtx.TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := claims.UserID()
	if uid != 42 || claims.SessionID != 7 {
		t.Fatalf("claims 不符: uid=%d sid=%d", uid, claims.SessionID)
	}
}

// refresh token 不能当 access token 用，否则「短有效期的 access」就形同虚设。
func TestRejectsWrongType(t *testing.T) {
	c := jwtx.NewCodec(secret)
	refresh, _ := c.Sign(1, 1, jwtx.TypeRefresh, time.Hour)
	if _, err := c.Parse(refresh, jwtx.TypeAccess); err == nil {
		t.Fatal("refresh token 不应能当 access token 使用")
	}
}

func TestRejectsExpired(t *testing.T) {
	c := jwtx.NewCodec(secret)
	tok, _ := c.Sign(1, 1, jwtx.TypeAccess, -time.Minute)
	if _, err := c.Parse(tok, jwtx.TypeAccess); !errors.Is(err, jwtx.ErrExpired) {
		t.Fatalf("期望 ErrExpired，实际 %v", err)
	}
}

func TestRejectsWrongSecret(t *testing.T) {
	tok, _ := jwtx.NewCodec(secret).Sign(1, 1, jwtx.TypeAccess, time.Hour)
	other := jwtx.NewCodec("another-sufficiently-long-secret-000000000")
	if _, err := other.Parse(tok, jwtx.TypeAccess); !errors.Is(err, jwtx.ErrInvalid) {
		t.Fatalf("换密钥后应判无效，实际 %v", err)
	}
}

// 切换后存量 Python 令牌必须全部失效：它们没有 sid，sub 也是 uuid 而非整数。
// 这不是缺陷而是设计——全体用户重新登录（§5.2.6）。
func TestRejectsLegacyPythonToken(t *testing.T) {
	// 用同一密钥签一个「Python 形态」的令牌：sub 是 uuid、无 sid
	legacy := signLegacy(t, secret)
	c := jwtx.NewCodec(secret)
	if _, err := c.Parse(legacy, jwtx.TypeAccess); err == nil {
		t.Fatal("存量 Python 令牌必须被拒绝")
	}
}

func TestDigestIsLowercaseHex64(t *testing.T) {
	d := jwtx.Digest("some-refresh-token")
	if len(d) != 64 {
		t.Fatalf("摘要长度应为 64，实际 %d", len(d))
	}
	for _, ch := range d {
		isDigit := ch >= '0' && ch <= '9'
		isLowerHexLetter := ch >= 'a' && ch <= 'f'
		if !isDigit && !isLowerHexLetter {
			t.Fatalf("摘要必须是小写十六进制（数据库有 CHECK），实际含 %q", ch)
		}
	}
	if jwtx.Digest("a") == jwtx.Digest("b") {
		t.Fatal("不同输入不应得到相同摘要")
	}
}
