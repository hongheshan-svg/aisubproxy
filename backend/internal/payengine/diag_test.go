package payengine

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// PF-9：打开支付页超时要把定位信息写进时间线，再截一张整页图（还没填卡）。
// 步骤 ctx 这时已经超时，诊断必须用单据的 ctx，否则最需要的时候读不到。
func TestOpenTimeoutWritesDiagnosis(t *testing.T) {
	h := newPayHarness(t, func(to *Timeouts) { to.PageLoad = 20 * time.Millisecond })
	h.br.openGate = make(chan struct{})
	var live atomic.Bool
	h.br.diagnose = func(c context.Context) []string {
		live.Store(c.Err() == nil)
		return []string{"地址：checkout.example/pay", "正文开头：Signed in as buyer@example.com"}
	}
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)
	assertNotSubmitted(t, g, "page_failed")
	if !live.Load() {
		t.Fatal("诊断拿到的 ctx 已失效")
	}
	waitEvent(t, o.OrderNo, "打开支付页失败 地址：checkout.example/pay")
	waitEvent(t, o.OrderNo, "打开支付页失败 正文开头：Signed in as ***") // 账号邮箱经 scrub 抹掉
	if s := theBrowser(t, h).lastArgs("Shot"); s[0] != "01-open-failed" || s[1] != paybrowser.ShotPage {
		t.Fatalf("应截整页图: %v", s)
	}
	if h.pool.total() != 0 {
		t.Fatalf("打不开支付页不应碰卡池: %d", h.pool.total())
	}
}

// PF-9：填表失败写报告和定位信息，不截图（页面上可能有卡号）。
func TestFillFailureWritesDiagnosisWithoutShot(t *testing.T) {
	h := newPayHarness(t, nil)
	h.br.fill = func(paybrowser.Card, paybrowser.Billing) error { return errors.New("找不到「城市」输入框") }
	h.br.report = paybrowser.FillReport{
		Fields:        []paybrowser.FieldCheck{{Label: "卡号", Selector: "#cardNumber", Status: "已填写"}},
		ManualAddress: "已点击 2 次",
	}
	h.br.diagnose = func(context.Context) []string {
		return []string{"选择器可见命中（主页面|子 frame）：城市 0/0|0/0"}
	}
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)
	assertNotSubmitted(t, g, "fill_failed")
	assertReleases(t, h, cardpool.ResultAborted)
	assertSubmitCount(t, h, 0)
	waitEvent(t, o.OrderNo, "填表 字段「卡号」：命中 #cardNumber（主页面），已填写")
	waitEvent(t, o.OrderNo, "填表 手动地址：已点击 2 次")
	waitEvent(t, o.OrderNo, "填表失败 选择器可见命中")
	if b := theBrowser(t, h); b.count("Shot") != 1 {
		t.Fatalf("填卡后不应再截图，只有打开时那 1 张: %d", b.count("Shot"))
	}
}

// PF-3：真实流程把账号邮箱交给 Fill；是否真的填由页面决定，时间线绝不出现邮箱。
func TestFillGetsAccountEmailNotInTimeline(t *testing.T) {
	h := newPayHarness(t, nil)
	h.gpt.accountFn = planFrom(2)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess, text: "Thanks buyer@example.com"})
	h.br.report = paybrowser.FillReport{Email: "必填且为空，已填账号邮箱"}
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)
	if g.State != StateSucceeded {
		t.Fatalf("应成功: %+v", g)
	}
	if bill := theBrowser(t, h).lastArgs("Fill")[1].(paybrowser.Billing); bill.Email != "buyer@example.com" {
		t.Fatalf("Fill 收到的邮箱 = %q", bill.Email)
	}
	waitEvent(t, o.OrderNo, "填表 邮箱：必填且为空，已填账号邮箱")
	for _, e := range eventStates(t, o.OrderNo) {
		if strings.Contains(e.Message, "buyer@example.com") {
			t.Fatalf("时间线含账号邮箱: %+v", e)
		}
	}
}

func TestScrubAccountEmail(t *testing.T) {
	j := &job{o: &Order{AccountEmail: "buyer@example.com"}}
	if got := j.scrub("sent to buyer@example.com"); got != "sent to ***" {
		t.Fatalf("scrub = %q", got)
	}
	// 恢复阶段脱敏用的 job 没有单据
	if got := (&job{}).scrub("x"); got != "x" {
		t.Fatalf("scrub = %q", got)
	}
}
