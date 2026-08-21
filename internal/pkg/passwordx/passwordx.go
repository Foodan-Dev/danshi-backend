// Package passwordx 是密码哈希。
//
// 用 bcrypt 且**必须与 Python 侧二进制兼容**：迁移后存量用户要能用原密码直接登录。
// Python 侧用 bcrypt.gensalt() 默认 cost=12，这里保持一致。
package passwordx

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// Cost 与 Python 侧 bcrypt.gensalt() 的默认值一致。
const Cost = 12

// MaxLen 是 bcrypt 的硬上限。超过 72 字节的部分会被静默截断，
// 那等于两个不同的长密码可能哈希相同——必须显式拒绝而不是放任。
const MaxLen = 72

// Hash 使用与旧 Python 服务兼容的 bcrypt 参数哈希密码。
func Hash(plain string) (string, error) {
	if len(plain) > MaxLen {
		return "", fmt.Errorf("密码长度不能超过 %d 字节", MaxLen)
	}
	b, err := bcrypt.GenerateFromPassword([]byte(plain), Cost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Verify 校验明文与哈希是否匹配。
// 无论失败原因是什么都只返回 false，不区分「哈希损坏」与「密码不对」，
// 避免给调用方制造分支从而泄露信息。
func Verify(plain, hashed string) bool {
	if len(plain) > MaxLen {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(plain)) == nil
}
