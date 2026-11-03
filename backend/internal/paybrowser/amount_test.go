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

func TestCurrencyMatches(t *testing.T) {
	cases := []struct {
		text, cur string
		want      bool
	}{
		{"Total due today ₱1,120.00", "PHP", true},
		{"$20.00", "USD", true},
		{"US$20.00", "USD", true},
		{"USD 20.00", "USD", true},
		{"¥3,000", "JPY", true},
		{"CLP 20,000", "CLP", true},
		{"CLP 20,000", "CLP", true},
		{"EGP 500.00", "EGP", true},
		{"20.00 USD", "USD", true},
		{"EUR 5.00", "EUR", true},
		{"US$20.00", "PHP", false},
		{"€200.00", "PHP", false},
		{"$20,000", "CLP", false},
		{"CLP$20,000", "USD", false},
		{"A$20.00", "USD", false},
		{"CN¥100", "JPY", false},
		{"£5.00", "EGP", false},
		{"20 USDT", "USD", false},
		{"1,120.00", "PHP", false},
		{"", "PHP", false},
		{"₱1,120.00 US$20.00", "PHP", false},
	}
	for _, c := range cases {
		if got := paybrowser.CurrencyMatches(c.text, c.cur); got != c.want {
			t.Errorf("CurrencyMatches(%q, %q) = %v，期望 %v", c.text, c.cur, got, c.want)
		}
	}
}
