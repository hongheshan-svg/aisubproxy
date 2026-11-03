package payengine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// harness 的套餐是 plus/PH（PHP，小数位 2），最低金额 10000（₱100.00）。页面金额由假浏览器给出。

func setPage(h *harness, minor int64, text string) {
	h.br.amount, h.br.amountText = minor, text
}

func TestAmountBelowFloorAbortsBeforeFunding(t *testing.T) {
	h := newPayHarness(t, nil)
	setPage(h, 9999, "₱99.99")
	h.start(t)
	o := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, o, "amount_below_floor")
	for _, sub := range []string{"99.99", "100.00", "PHP", "档位"} {
		if !strings.Contains(o.ErrorMessage, sub) {
			t.Fatalf("说明应含 %q: %s", sub, o.ErrorMessage)
		}
	}
	if h.pool.total() != 0 {
		t.Fatalf("低于最低金额不应碰卡池: %d", h.pool.total())
	}
}

func TestAmountAtFloorProceeds(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess, text: "Thanks"})
	setPage(h, 10000, "₱100.00")
	h.start(t)
	o := h.submit(t, false)
	deadline := time.Now().Add(5 * time.Second)
	for h.pool.count("Acquire") == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("应走到注资（取卡）: %+v", mustOrder(t, o.OrderNo))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if g := mustOrder(t, o.OrderNo); g.ErrorCode == "amount_below_floor" {
		t.Fatalf("等于最低金额不应放弃: %+v", g)
	}
}

func TestDryRunBelowFloorOnlyWarns(t *testing.T) {
	h := newHarness(t)
	setPage(h, 5000, "₱50.00")
	h.start(t)
	o := waitDone(t, h.submit(t, true).OrderNo)
	if o.State != StatePreviewed {
		t.Fatalf("试运行应完成预览: %+v", o)
	}
	n := 0
	for _, e := range eventStates(t, o.OrderNo) {
		if e.Level == "warn" && strings.Contains(e.Message, "低于最低金额") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("应有一条 warn「低于最低金额」: %d", n)
	}
}

func TestDryRunWithoutFloorSkipsCheck(t *testing.T) {
	h := newHarness(t)
	saveWith(t, "plus", "PH", RegionConf{Enabled: true, Currency: "PHP", CapMinor: 120000, FundCents: 3000})
	h.start(t)
	o := waitDone(t, h.submit(t, true).OrderNo)
	if o.State != StatePreviewed {
		t.Fatalf("试运行应完成预览: %+v", o)
	}
	n := 0
	for _, e := range eventStates(t, o.OrderNo) {
		if strings.Contains(e.Message, "未配置最低金额，跳过下限检查") {
			n++
			if e.Level != "info" {
				t.Fatalf("跳过下限检查应是 info: %+v", e)
			}
		}
	}
	if n != 1 {
		t.Fatalf("应记一条跳过下限检查: %+v", eventStates(t, o.OrderNo))
	}
}

// 点数单的最低金额同样在注资前把关：出厂最低金额 56500，页面 56499 放弃。
func TestCreditAmountBelowFloorAbortsBeforeFunding(t *testing.T) {
	h := newCreditHarness(t)
	setPage(h, 56499, "₱564.99")
	h.start(t)
	o := waitDone(t, h.submitCredit(t, false, false).OrderNo)
	assertNotSubmitted(t, o, "amount_below_floor")
	for _, sub := range []string{"564.99", "565.00", "PHP"} {
		if !strings.Contains(o.ErrorMessage, sub) {
			t.Fatalf("说明应含 %q: %s", sub, o.ErrorMessage)
		}
	}
	if h.pool.count("Acquire") != 0 || h.pool.count("Fund") != 0 || h.pool.total() != 0 {
		t.Fatalf("不应碰卡池: %d", h.pool.total())
	}
}

// 真实单走到 openPage 时最低金额为 0（以后重构漏赋值）：直接放弃，不能静默放行。包内直接调 openPage，因为正常路径有 ResolvePlan 兜底造不出这个状态。
func TestRealOrderWithZeroFloorAborts(t *testing.T) {
	h := newPayHarness(t, nil)
	o := h.submit(t, false) // 不启动工作协程
	j := &job{o: o}
	if err := h.svc.advance(ctx, j, StateChecking, "检查账号", nil); err != nil {
		t.Fatal(err)
	}
	j.o.CheckoutURL = testCheckoutURL
	defer j.closeBrowser()
	err := h.svc.openPage(ctx, j)
	var f *stepFailure
	if !errors.As(err, &f) || f.code != "plan_unavailable" || !strings.Contains(f.msg, "未配置最低金额") {
		t.Fatalf("应以 plan_unavailable 放弃: %v", err)
	}
	if h.pool.total() != 0 {
		t.Fatalf("不应碰卡池: %d", h.pool.total())
	}
}
