package handler

import (
	"testing"
	"time"
)

// H1-11：failed 只在「这一次刚好到上限」时返回 true。
func TestFailLimiterReportsLockOnce(t *testing.T) {
	l := newFailLimiter(3, time.Hour, 10)
	var got []bool
	for i := 0; i < 5; i++ {
		got = append(got, l.failed("k"))
	}
	want := []bool{false, false, true, false, false}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 次: got %v want %v（全部 %v）", i+1, got[i], want[i], got)
		}
	}
}

// 并发输错：到上限之后继续记失败，告警只发一条。
func TestNoteSwapPasswordFailureAlertsOnce(t *testing.T) {
	swapPasswordFails.succeeded(swapPasswordFailKey)
	t.Cleanup(func() { swapPasswordFails.succeeded(swapPasswordFailKey); SetOpsAlert(nil) })
	n := 0
	SetOpsAlert(func(string) { n++ })
	for i := 0; i < swapPasswordMaxFailures+3; i++ {
		noteSwapPasswordFailure()
	}
	if n != 1 {
		t.Fatalf("告警应只有 1 条，实际 %d", n)
	}
}
