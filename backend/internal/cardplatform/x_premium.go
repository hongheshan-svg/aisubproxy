package cardplatform

import (
	"errors"
	"strings"
)

func XPaymentCountry(country string) (string, error) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" {
		country = "US" // 2026-10-24 XPASS 改版:X 默认 US/USD(对齐 Grok)
	}
	switch country {
	case "US", "JP", "PH", "NG", "TR", "EG":
		return country, nil
	}
	//nolint:staticcheck // ST1005：X 是产品名（专有名词），错误文本保持不变（不改变行为）
	return "", errors.New("X supports US, JP, PH, NG, TR and EG only")
}
