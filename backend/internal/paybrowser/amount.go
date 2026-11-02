package paybrowser

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// amountRe 匹配一段以数字开头、以数字结尾、中间可夹 . 与 , 的数字。
var amountRe = regexp.MustCompile(`\d(?:[\d.,]*\d)?`)

// CurrencyExponent 返回币种的小数位数：JPY、CLP 为 0，其余为 2。
func CurrencyExponent(cur string) int {
	switch strings.ToUpper(strings.TrimSpace(cur)) {
	case "JPY", "CLP":
		return 0
	}
	return 2
}

// ParseAmount 把支付页上的金额原文（如 "Total due today ₱1,120.00"）解析为最小单位金额。
// 取原文中最后一段数字：无小数的币种把 . 与 , 都当千分位；两位小数的币种以最后一个 . 为小数点、, 为千分位。
// 小数位数或千分位分组不符时报错，宁可读不出也不误读。
func ParseAmount(text, currency string) (int64, error) {
	nums := amountRe.FindAllString(text, -1)
	if len(nums) == 0 {
		return 0, fmt.Errorf("金额原文中没有数字：%q", text)
	}
	num := nums[len(nums)-1]
	exp := CurrencyExponent(currency)

	whole, frac := num, ""
	if exp > 0 {
		if i := strings.LastIndexByte(num, '.'); i >= 0 {
			whole, frac = num[:i], num[i+1:]
			if len(frac) != exp {
				return 0, fmt.Errorf("金额 %q 的小数位数与币种 %s 不符", num, currency)
			}
		}
		if strings.Contains(whole, ".") {
			return 0, fmt.Errorf("金额格式无法识别：%q", num)
		}
	}
	digits, ok := ungroup(whole)
	if !ok {
		return 0, fmt.Errorf("金额格式无法识别：%q", num)
	}
	if frac == "" {
		frac = strings.Repeat("0", exp)
	}
	v, err := strconv.ParseInt(digits+frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("金额超出范围：%q", num)
	}
	return v, nil
}

// ungroup 去掉千分位分隔符（. 或 ,）；有分隔符时首组须为 1～3 位、其余各组恰为 3 位。
func ungroup(s string) (string, bool) {
	groups := strings.Split(strings.ReplaceAll(s, ".", ","), ",")
	if len(groups) == 1 {
		return s, s != ""
	}
	if n := len(groups[0]); n < 1 || n > 3 {
		return "", false
	}
	for _, g := range groups[1:] {
		if len(g) != 3 {
			return "", false
		}
	}
	return strings.Join(groups, ""), true
}

// currencyMarkers 是各币种在支付页上的写法，区分大小写；表外币种只认币种代码本身。
var currencyMarkers = map[string][]string{
	"USD": {"US$", "USD", "$"},
	"PHP": {"₱", "PHP"},
	"JPY": {"JP¥", "¥", "￥", "JPY"},
	"CLP": {"CLP$", "CL$", "CLP"},
	"EGP": {"E£", "EGP"},
}

// CurrencyMatches 判断金额原文里最后一段数字紧贴的币种标记是否是 currency 的写法。
// 标记两侧（远离数字的一侧）不能紧挨字母，所以 CLP$、A$、CN¥ 里的裸 $、¥ 与 20 USDT 都不会误认。
// 没有数字、没有标记或标记不符一律返回 false。
func CurrencyMatches(text, currency string) bool {
	locs := amountRe.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return false
	}
	last := locs[len(locs)-1]
	prefix := strings.TrimSpace(text[:last[0]])
	suffix := strings.TrimSpace(text[last[1]:])
	markers, ok := currencyMarkers[currency]
	if !ok {
		markers = []string{currency}
	}
	for _, m := range markers {
		if strings.HasSuffix(prefix, m) {
			if r, _ := utf8.DecodeLastRuneInString(prefix[:len(prefix)-len(m)]); !unicode.IsLetter(r) {
				return true
			}
		}
		if strings.HasPrefix(suffix, m) {
			if r, _ := utf8.DecodeRuneInString(suffix[len(m):]); !unicode.IsLetter(r) {
				return true
			}
		}
	}
	return false
}
