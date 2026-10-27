package cardpool

import (
	"regexp"
	"testing"
	"time"
)

func TestOutOrderNoFormat(t *testing.T) {
	now := time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC)
	re := regexp.MustCompile(`^MI\d{12}[0-9a-f]{8}$`)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		n := newOutOrderNo(KindRecharge, now)
		if len(n) != 22 || !re.MatchString(n) {
			t.Fatalf("格式不符: %q", n)
		}
		if seen[n] {
			t.Fatalf("重复: %q", n)
		}
		seen[n] = true
	}
	if n := newOutOrderNo(KindRecharge, now); n[2:14] != "261028040000" {
		t.Fatalf("时间段不符: %q", n)
	}
	for kind, prefix := range map[string]string{KindApply: "MA", KindRecharge: "MI", KindWithdraw: "MO", KindStatus: "MS"} {
		if n := newOutOrderNo(kind, now); n[:2] != prefix {
			t.Fatalf("%s 前缀 %q", kind, n)
		}
	}
}
