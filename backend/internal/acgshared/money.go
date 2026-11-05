package acgshared

import (
	"fmt"
	"strconv"
)

// Money 是美分，序列化成带两位小数的 JSON 数字，不经过浮点。
type Money int64

// MarshalJSON 把 325 写成 3.25。
func (m Money) MarshalJSON() ([]byte, error) {
	v := int64(m)
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	return []byte(fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)), nil
}

// MaxQuantity 与 cardplatform.MaxIssueCount 相同。
const MaxQuantity = 200

// ValidRequestNo 判断对方订单号是否是 1..64 位字母数字。
func ValidRequestNo(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// ParseQuantity 只接受纯十进制数字，范围 1..MaxQuantity。
func ParseQuantity(s string) (int, bool) {
	if s == "" || len(s) > 6 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > MaxQuantity {
		return 0, false
	}
	return n, true
}
