package payengine

import (
	"errors"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// MO-04：填表之后、点支付之前再读一次金额。一致才点；其余一律在写 submitted_at 之前放弃。
func TestAmountRecheckConsistentSubmits(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(3)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess, text: "Thanks"})
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	if g.State != StateSucceeded {
		t.Fatalf("金额一致应照常付款: %+v", g)
	}
	if n := theBrowser(t, h).count("ReadAmount"); n != 2 {
		t.Fatalf("应读金额 2 次（打开页面、点支付前）: %d", n)
	}
	assertSubmitCount(t, h, 1)
}

func TestAmountRecheckAborts(t *testing.T) {
	cases := []struct {
		name, code string
		second     func() (int64, string, error)
	}{
		{"超过上限", "amount_over_cap", func() (int64, string, error) { return 130000, "₱1,300.00", nil }},
		{"没超上限但与第一次不一致", "amount_changed", func() (int64, string, error) { return 100000, "₱1,000.00", nil }},
		{"币种变了", "amount_changed", func() (int64, string, error) { return 112000, "US$1,120.00", nil }},
		{"第二次读取失败", "amount_unreadable", func() (int64, string, error) { return 0, "", errors.New("支付页上没有找到金额") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newPayHarness(t, nil)
			h.br.readAmount = func(n int) (int64, string, error) {
				if n == 1 {
					return 112000, "₱1,120.00", nil
				}
				return c.second()
			}
			h.start(t)
			g := waitDone(t, h.submit(t, false).OrderNo)
			assertNotSubmitted(t, g, c.code)
			assertReleases(t, h, cardpool.ResultAborted)
			assertSubmitCount(t, h, 0)
			if g.SubmittedAt != nil || g.VerifyUntil != nil {
				t.Fatalf("放弃时不应留下提交时间: %+v", g)
			}
			b := theBrowser(t, h)
			if b.count("Fill") != 1 || b.count("ReadAmount") != 2 || b.count("WaitOutcome") != 0 || b.count("Close") != 1 {
				t.Fatalf("应填表 1 次、读金额 2 次、不等结果、关闭: %d %d %d %d", b.count("Fill"), b.count("ReadAmount"), b.count("WaitOutcome"), b.count("Close"))
			}
		})
	}
}

// 试运行到填表也在填表后再读一次金额并写入时间线，不点支付；金额变了只告警，读取失败也只记录。
func TestDryRunFillRereadsAmount(t *testing.T) {
	cases := []struct {
		name, event string
		second      func() (int64, string, error)
	}{
		{"一致", "试运行填表后复核金额一致", func() (int64, string, error) { return 112000, "₱1,120.00", nil }},
		{"变了", "试运行填表后应付金额 ₱1,300.00 与打开页面时的 ₱1,120.00 不一致", func() (int64, string, error) { return 130000, "₱1,300.00", nil }},
		{"读取失败", "试运行填表后再次读取应付金额失败", func() (int64, string, error) { return 0, "", errors.New("没找到") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.br.fill = func(paybrowser.Card, paybrowser.Billing) error { return nil }
			h.br.readAmount = func(n int) (int64, string, error) {
				if n == 1 {
					return 112000, "₱1,120.00", nil
				}
				return c.second()
			}
			h.start(t)
			o := h.submitFillPreview(t)
			g := waitDone(t, o.OrderNo)
			if g.State != StatePreviewed {
				t.Fatalf("试运行应照常结束: %+v", g)
			}
			waitEvent(t, o.OrderNo, c.event)
			if n := theBrowser(t, h).count("ReadAmount"); n != 2 {
				t.Fatalf("应读金额 2 次: %d", n)
			}
			assertNeverPaid(t, h)
		})
	}
}
