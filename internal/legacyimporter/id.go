package legacyimporter

import (
	"strings"
)

const javaScriptMaxSafeInteger int64 = 9_007_199_254_740_991

func isUUID(raw string) bool {
	if len(raw) != 36 || raw[8] != '-' || raw[13] != '-' || raw[18] != '-' || raw[23] != '-' {
		return false
	}
	for index, value := range raw {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !isHexDigit(value) {
			return false
		}
	}
	return true
}

func isHexDigit(value rune) bool {
	return (value >= '0' && value <= '9') ||
		(value >= 'a' && value <= 'f') ||
		(value >= 'A' && value <= 'F')
}

func relationKey(values ...string) string {
	return strings.Join(values, ":")
}
