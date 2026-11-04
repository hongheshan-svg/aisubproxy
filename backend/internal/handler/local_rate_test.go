package handler

import (
	"testing"
	"time"
)

// G9：限流表定时清理：一小时以前开始的窗口在清理时删掉，新的留着。
func TestLocalRateSweep(t *testing.T) {
	resetLocalRate()
	t.Cleanup(resetLocalRate)
	base := time.Now()
	setLocalRateNow(t, func() time.Time { return base })
	localRateAllow(localRateQuery, "203.0.113.1")
	setLocalRateNow(t, func() time.Time { return base.Add(30 * time.Minute) })
	localRateAllow(localRateQuery, "203.0.113.2")
	if n := sweepLocalRate(base.Add(61 * time.Minute)); n != 1 {
		t.Fatalf("应只清掉 1 个过期的键: %d", n)
	}
	if got := localRateKeys(); got != 1 {
		t.Fatalf("应剩 1 个键: %d", got)
	}
}

// G9：键数到上限时淘汰最旧的一个，请求路径里不再遍历整张表；新键照常放行。
func TestLocalRateEvictsOldestWhenFull(t *testing.T) {
	resetLocalRate()
	t.Cleanup(resetLocalRate)
	old := localRateMaxKeys
	localRateMaxKeys = 3
	t.Cleanup(func() { localRateMaxKeys = old })
	base := time.Now()
	for i, ip := range []string{"a", "b", "c"} {
		at := base.Add(time.Duration(i) * time.Second)
		setLocalRateNow(t, func() time.Time { return at })
		localRateAllow(localRateMutate, ip)
	}
	setLocalRateNow(t, func() time.Time { return base.Add(10 * time.Second) })
	if !localRateAllow(localRateMutate, "d") {
		t.Fatal("新键应放行")
	}
	if got := localRateKeys(); got != 3 {
		t.Fatalf("键数不应超过上限: %d", got)
	}
	if localRateHas(localRateMutate, "a") || !localRateHas(localRateMutate, "b") || !localRateHas(localRateMutate, "d") {
		t.Fatal("应淘汰最旧的 a")
	}
}

func setLocalRateNow(t *testing.T, now func() time.Time) {
	t.Helper()
	old := localRateNow
	localRateNow = now
	t.Cleanup(func() { localRateNow = old })
}
