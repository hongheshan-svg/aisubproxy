package paybrowser_test

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

func TestParseAmount(t *testing.T) {
	ok := []struct {
		text, currency string
		want           int64
	}{
		{"₱1,120.00", "PHP", 112000},
		{"PHP 1,120.00", "PHP", 112000},
		{"US$20.00", "USD", 2000},
		{"$20", "USD", 2000},
		{"¥3,000", "JPY", 3000},
		{"CLP$19.000", "CLP", 19000},
		{"$19.000", "CLP", 19000},
		{"E£1,000.00", "EGP", 100000},
		{"Total due today ₱1,120.00", "PHP", 112000},
	}
	for _, c := range ok {
		got, err := paybrowser.ParseAmount(c.text, c.currency)
		if err != nil {
			t.Errorf("ParseAmount(%q, %s) 报错: %v", c.text, c.currency, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseAmount(%q, %s) = %d，期望 %d", c.text, c.currency, got, c.want)
		}
	}
	for _, text := range []string{"", "免费", "1,120.00.00"} {
		if got, err := paybrowser.ParseAmount(text, "PHP"); err == nil {
			t.Errorf("ParseAmount(%q) 应报错，得到 %d", text, got)
		}
	}
}

func TestCurrencyExponent(t *testing.T) {
	cases := map[string]int{"JPY": 0, "CLP": 0, "jpy": 0, "clp": 0, "PHP": 2, "USD": 2, "EGP": 2, "usd": 2}
	for cur, want := range cases {
		if got := paybrowser.CurrencyExponent(cur); got != want {
			t.Errorf("CurrencyExponent(%q) = %d，期望 %d", cur, got, want)
		}
	}
}
