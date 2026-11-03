package payengine

import (
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
	for _, sub := range []string{"99.99", "100.00", "档位"} {
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
	if countEvents(t, o.OrderNo, "未配置最低金额，跳过下限检查") != 1 {
		t.Fatalf("应记一条跳过下限检查: %+v", eventStates(t, o.OrderNo))
	}
}
