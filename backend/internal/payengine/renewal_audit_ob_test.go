package payengine

import (
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// D-2 / D-9：待人工单的会话留到人工定论；定论成功后会话再按 SessionKeep 从定论时刻计时，
// 关闭续费至少试一次，不会被「结束满 1 小时」的清理抢先记成 failed。

// obPresetReview 插一张待人工的真实开通单（带会话、账号 ID、已提交），结束时间为测试时钟 now + ago。
func obPresetReview(t *testing.T, h *harness, email string, ago time.Duration) *Order {
	t.Helper()
	o := presetOrder(t, email, StateNeedsReview, map[string]any{"submitted_at": h.clock.Now().Unix(), "account_id": "acc-1", "card_id": testCardID})
	if _, err := db.DB.Exec(`UPDATE pay_orders SET finished_at = ? WHERE order_no = ?`, h.clock.Now().Add(ago).Unix(), o.OrderNo); err != nil {
		t.Fatal(err)
	}
	return mustOrder(t, o.OrderNo)
}

func obNoAlertContains(t *testing.T, h *harness, s string) {
	t.Helper()
	for _, a := range h.alerts.Texts() {
		if strings.Contains(a, s) {
			t.Fatalf("不应有含 %q 的告警: %v", s, h.alerts.Texts())
		}
	}
}

// D-2：管理员两小时后才定论成功，会话还在，照样关闭续费，不会因为会话被清而记 skipped。
func TestObNeedsReviewKeepsSessionUntilResolved(t *testing.T) {
	h := newRenewalHarness(t)
	o := obPresetReview(t, h, "review-late@example.com", 0)
	h.start(t)
	h.clock.Advance(2 * time.Hour)
	time.Sleep(60 * time.Millisecond) // 续费协程每 5 毫秒扫一次，给它足够的轮次
	if g := mustOrder(t, o.OrderNo); g.SessionSealed == "" || g.State != StateNeedsReview {
		t.Fatalf("人工定论之前不能清掉关闭续费要用的会话: %+v", g)
	}
	if err := h.svc.Resolve(ctx, o.OrderNo, StateSucceeded, "核对已开通", "alice"); err != nil {
		t.Fatal(err)
	}
	g := waitRenewal(t, o.OrderNo, RenewalCancelled)
	if g.State != StateSucceeded || g.SessionSealed != "" || g.RenewalAttempts != 1 {
		t.Fatalf("定论成功后应关闭续费并清空会话: %+v", g)
	}
	if n := h.gpt.count("CancelRenewal"); n != 1 {
		t.Fatalf("应调用 1 次取消接口: %d", n)
	}
	obNoAlertContains(t, h, "会话已清空")
}

// D-9：第 59 分钟定论成功，续费协程下一轮时结束时刻已满 1 小时：先清理不能把它记成 failed，关闭续费要真的试一次。
func TestObResolveNearSessionDeadlineStillTriesRenewal(t *testing.T) {
	h := newRenewalHarness(t)
	o := obPresetReview(t, h, "review-59@example.com", -59*time.Minute)
	if err := h.svc.Resolve(ctx, o.OrderNo, StateSucceeded, "核对已开通", "alice"); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(2 * time.Minute) // 结束时刻已满 61 分钟，定论才过去 2 分钟
	h.start(t)
	g := waitRenewal(t, o.OrderNo, RenewalCancelled)
	if g.State != StateSucceeded || g.SessionSealed != "" {
		t.Fatalf("应关掉续费: %+v", g)
	}
	if n := h.gpt.count("CancelRenewal"); n != 1 {
		t.Fatalf("关闭续费应试过一次: %d", n)
	}
	obNoAlertContains(t, h, "保留已满")
}

// 定论成功后关闭续费一直失败：会话从定论时刻起留满 SessionKeep 给管理员手动重试，到点才清，期间不会被提前清掉。
func TestObResolvedSessionKeptFromResolveTime(t *testing.T) {
	h := newRenewalHarness(t)
	h.gpt.subStatusFn = func(int) (chatgpt.RenewalInfo, error) { return chatgpt.RenewalInfo{WillRenew: boolPtr(true)}, nil }
	o := obPresetReview(t, h, "review-keep-after@example.com", -3*time.Hour)
	h.start(t)
	if err := h.svc.Resolve(ctx, o.OrderNo, StateSucceeded, "核对已开通", "alice"); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, h, "CancelRenewal", 1)
	g := waitRenewalAttempts(t, o.OrderNo, 1)
	if g.RenewalState != RenewalPending || g.SessionSealed == "" {
		t.Fatalf("第一次失败后应排队重试、会话还在: %+v", g)
	}
	h.clock.Advance(50 * time.Minute)
	time.Sleep(60 * time.Millisecond)
	if g := mustOrder(t, o.OrderNo); g.SessionSealed == "" {
		t.Fatalf("定论后不到 1 小时不能清会话: %+v", g)
	}
	h.clock.Advance(11 * time.Minute)
	g = waitSessionCleared(t, o.OrderNo)
	if g.State != StateSucceeded || g.RenewalState != RenewalFailed {
		t.Fatalf("定论满 1 小时后清会话，续费转 failed: %+v", g)
	}
}
