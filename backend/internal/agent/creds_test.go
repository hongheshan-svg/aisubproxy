package agent_test

import (
	"math"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
)

func TestGeneratePasswordAlphabet(t *testing.T) {
	for i := 0; i < 200; i++ {
		p, err := agent.GeneratePassword()
		if err != nil || len(p) != 16 {
			t.Fatalf("p=%q err=%v", p, err)
		}
		if strings.ContainsAny(p, "0Oo1lI") {
			t.Fatalf("含易混字符: %s", p)
		}
		for _, ch := range p {
			if !strings.ContainsRune("ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789", ch) {
				t.Fatalf("字符 %q 不在字符集内", ch)
			}
		}
	}
}

func TestNormalizeUsername(t *testing.T) {
	if u, ok := agent.NormalizeUsername(" Alice "); !ok || u != "alice" {
		t.Fatalf("%q %v", u, ok)
	}
	for _, bad := range []string{"ab", "a b c", strings.Repeat("a", 33), "", "ali!ce"} {
		if _, ok := agent.NormalizeUsername(bad); ok {
			t.Fatalf("%q 应为 false", bad)
		}
	}
	if u, ok := agent.NormalizeUsername("abc"); !ok || u != "abc" {
		t.Fatal("3 位应通过")
	}
	if _, ok := agent.NormalizeUsername(strings.Repeat("a", 32)); !ok {
		t.Fatal("32 位应通过")
	}
}

func TestValidProductCode(t *testing.T) {
	for s, want := range map[string]bool{"mw-pro-5x-ph": true, "ab": true, "a": false, "A-b": false, "a_b": false, strings.Repeat("a", 49): false, strings.Repeat("a", 48): true} {
		if agent.ValidProductCode(s) != want {
			t.Fatalf("%q want %v", s, want)
		}
	}
}

func TestDefaultProductCode(t *testing.T) {
	if got := agent.DefaultProductCode("pro_5x", "PH"); got != "mw-pro-5x-ph" {
		t.Fatal(got)
	}
}

func TestValidRequestNo(t *testing.T) {
	for s, want := range map[string]bool{"": false, strings.Repeat("a", 65): false, "a": true, "a b": false, "order-1:2.x_y": true, strings.Repeat("a", 64): true} {
		if agent.ValidRequestNo(s) != want {
			t.Fatalf("%q want %v", s, want)
		}
	}
}

func TestFormatCents(t *testing.T) {
	for in, want := range map[int64]string{-1234: "-12.34", 5: "0.05", 0: "0.00", 100: "1.00", -5: "-0.05", 123456: "1234.56"} {
		if got := agent.FormatCents(in); got != want {
			t.Fatalf("%d got %s want %s", in, got, want)
		}
	}
	_ = agent.FormatCents(math.MinInt64)
}
