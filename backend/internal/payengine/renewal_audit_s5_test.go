package payengine

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
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

// P1-01：清理待人工超期会话时写事件失败，事务要回滚、连接要还回去，不能一直占着写锁；故障消除后下一轮照常清。
func TestS5ReviewSweepRollsBackWhenEventInsertFails(t *testing.T) {
	h := newRenewalHarness(t)
	o := obPresetReview(t, h, "s5-trigger@example.com", -31*24*time.Hour)
	if _, err := db.DB.Exec(`CREATE TRIGGER s5_fail_review_event BEFORE INSERT ON pay_order_events
		WHEN NEW.message LIKE '待人工已满%' BEGIN SELECT RAISE(ABORT, 'x'); END`); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // 漏掉的事务要等 ctx 结束才回收：测试失败时也不能卡住后面的清理
	h.svc.sweepReviewSessions(runCtx)
	if n := db.DB.Stats().InUse; n != 0 {
		t.Fatalf("写事件失败后事务应回滚、连接应归还，仍占用 %d 个连接", n)
	}
	if g := mustOrder(t, o.OrderNo); g.SessionSealed == "" || g.State != StateNeedsReview {
		t.Fatalf("写事件失败应整笔回滚，会话不能被清: %+v", g)
	}
	wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wcancel()
	if _, err := db.DB.ExecContext(wctx, `UPDATE pay_orders SET updated_at = updated_at WHERE order_no = ?`, o.OrderNo); err != nil {
		t.Fatalf("回滚之后普通写入应立刻成功: %v", err)
	}
	if _, err := db.DB.Exec(`DROP TRIGGER s5_fail_review_event`); err != nil {
		t.Fatal(err)
	}
	h.svc.sweepReviewSessions(runCtx)
	if g := mustOrder(t, o.OrderNo); g.SessionSealed != "" || g.State != StateNeedsReview {
		t.Fatalf("故障消除后下一轮应清空会话、单据仍待人工: %+v", g)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM pay_order_events WHERE order_no = ? AND message LIKE '待人工已满%'`, o.OrderNo).Scan(&n); err != nil || n != 1 {
		t.Fatalf("应写入 1 条清空会话的事件: %d %v", n, err)
	}
}
