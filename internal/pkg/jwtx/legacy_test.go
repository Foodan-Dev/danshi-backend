package jwtx_test

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// signLegacy 复刻 Python 侧 create_access_token 的 claims 形态：
// {sub: <uuid 字符串>, iat, exp, type: "access"}，**没有 sid**。
func signLegacy(t *testing.T, secret string) string {
	t.Helper()
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub":  "9a1b2c3d-4e5f-6789-abcd-ef0123456789",
		"iat":  now.Unix(),
		"exp":  now.Add(time.Hour).Unix(),
		"type": "access",
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
