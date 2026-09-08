// Package usernamepolicy 定义应用与数据库共用的用户名字符集合。
package usernamepolicy

import (
	"fmt"
	"strings"
	"unicode"
)

// ValidCharacters 接受 Unicode 字母、数字和下划线；规范化、长度和保留名由业务层处理。
func ValidCharacters(value string) bool {
	for _, r := range value {
		if !allowed(r) {
			return false
		}
	}
	return value != ""
}

func allowed(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// SQLPattern 生成 PostgreSQL U& 字符串内容。显式码点范围配合 C collation，
// 不使用随数据库 locale 改变含义的 POSIX 字符类别。
func SQLPattern() string {
	var pattern strings.Builder
	pattern.WriteString("^[")
	writeRune := func(r rune) {
		if r <= 0xffff {
			fmt.Fprintf(&pattern, "\\%04X", r)
		} else {
			fmt.Fprintf(&pattern, "\\+%06X", r)
		}
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !allowed(r) {
			continue
		}
		first := r
		for r < unicode.MaxRune && allowed(r+1) {
			r++
		}
		writeRune(first)
		if r != first {
			pattern.WriteByte('-')
			writeRune(r)
		}
	}
	pattern.WriteString("]+$")
	return pattern.String()
}
