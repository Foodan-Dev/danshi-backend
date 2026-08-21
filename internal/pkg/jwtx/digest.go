package jwtx

import (
	"crypto/sha256"
	"encoding/hex"
)

// Digest 计算 refresh token 的 sha256 十六进制摘要。
// user_sessions 只存摘要，明文不落库——数据库泄露不等于会话可被冒用。
// 数据库侧有 CHECK 要求 64 位小写十六进制，这里的输出正好满足。
func Digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
