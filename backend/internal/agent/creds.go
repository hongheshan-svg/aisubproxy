package agent

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

const passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"

var (
	usernameRe    = regexp.MustCompile(`^[a-z0-9_-]{3,32}$`)
	productCodeRe = regexp.MustCompile(`^[a-z0-9-]{2,48}$`)
	requestNoRe   = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
)

// GeneratePassword 生成 16 位随机初始密码（rand.Int 保证均匀）。
func GeneratePassword() (string, error) {
	limit := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, 16)
	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out), nil
}

// NormalizeUsername 去空白、转小写并校验格式。
func NormalizeUsername(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !usernameRe.MatchString(s) {
		return "", false
	}
	return s, true
}

func ValidProductCode(s string) bool { return productCodeRe.MatchString(s) }

// DefaultProductCode 例如 ("pro_5x","PH") -> "mw-pro-5x-ph"。
func DefaultProductCode(plan, region string) string {
	return "mw-" + strings.ReplaceAll(plan, "_", "-") + "-" + strings.ToLower(region)
}

func ValidRequestNo(s string) bool { return requestNoRe.MatchString(s) }

// FormatCents 把美分格式化成 "12.34"，纯整数运算。
func FormatCents(c int64) string {
	neg := c < 0
	u := uint64(c)
	if neg {
		u = -u // 对 MinInt64 取补码仍得到正确的无符号绝对值
	}
	s := fmt.Sprintf("%d.%02d", u/100, u%100)
	if neg {
		return "-" + s
	}
	return s
}
