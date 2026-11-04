package payengine

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// R10：待人工单的会话最多保留 30 天，超过就清并记一行日志（不含会话、邮箱）。

func s5CaptureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(log.Writer()) })
	return &buf
}

func TestS5ReviewSessionKept29DaysCleared31Days(t *testing.T) {
	h := newRenewalHarness(t)
	buf := s5CaptureLog(t)
	young := obPresetReview(t, h, "s5-young@example.com", -29*24*time.Hour)
	old := obPresetReview(t, h, "s5-old@example.com", -31*24*time.Hour)
	h.svc.sweepSessions(ctx)
	if g := mustOrder(t, young.OrderNo); g.SessionSealed == "" || g.State != StateNeedsReview {
		t.Fatalf("待人工 29 天的会话应保留: %+v", g)
	}
	g := mustOrder(t, old.OrderNo)
	if g.SessionSealed != "" || g.State != StateNeedsReview {
		t.Fatalf("待人工 31 天的会话应清空、单据仍待人工: %+v", g)
	}
	out := buf.String()
	if !strings.Contains(out, old.OrderNo) || strings.Contains(out, "s5-old@example.com") || strings.Contains(out, "example.com") {
		t.Fatalf("日志应记单号、不含邮箱: %q", out)
	}
	if strings.Contains(out, young.OrderNo) {
		t.Fatalf("没清的单不应写日志: %q", out)
	}
	// 清过之后再扫一轮不重复记日志
	buf.Reset()
	h.svc.sweepSessions(ctx)
	if buf.Len() != 0 {
		t.Fatalf("已清的单不应重复记日志: %q", buf.String())
	}
}

// 已人工定论成功的单照旧：从定论时刻起算 SessionKeep，不受 30 天上限影响。
func TestS5ResolvedOrderStillUsesSessionKeep(t *testing.T) {
	h := newRenewalHarness(t)
	o := obPresetReview(t, h, "s5-resolved@example.com", -40*24*time.Hour)
	if err := h.svc.Resolve(ctx, o.OrderNo, StateSucceeded, "核对已开通", "alice"); err != nil {
		t.Fatal(err)
	}
	if g := mustOrder(t, o.OrderNo); g.State != StateSucceeded || g.SessionSealed == "" {
		t.Fatalf("定论成功后应带着会话等关闭续费: %+v", g)
	}
	h.svc.sweepSessions(ctx)
	if g := mustOrder(t, o.OrderNo); g.SessionSealed == "" {
		t.Fatalf("刚定论的单不应被 30 天上限清掉会话: %+v", g)
	}
}
