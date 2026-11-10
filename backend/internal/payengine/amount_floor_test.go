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
	// 美国、菲律宾的最低金额有内置价，空着会被补上；没有内置价的日本才会出现「未配置最低金额」
	saveWith(t, "plus", "JP", RegionConf{Enabled: true, Currency: "JPY", CapMinor: 200000, FundCents: 3000})
	setPage(h, 112000, "¥112,000")
	h.start(t)
	req := Request{Provider: testProvider, PlanKey: "plus", Region: "JP", Session: testSession, Source: "admin_test", SourceRef: "admin", DryRun: true}
	sub, err := h.svc.Submit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	o := waitDone(t, sub.OrderNo)
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

// 点数单的最低金额同样在注资前把关：内置最低金额 49437（预期 56500 × 7/8），页面 49436 放弃。
func TestCreditAmountBelowFloorAbortsBeforeFunding(t *testing.T) {
	h := newCreditHarness(t)
	setPage(h, 49436, "₱494.36")
	h.start(t)
	o := waitDone(t, h.submitCredit(t, false, false).OrderNo)
	assertNotSubmitted(t, o, "amount_below_floor")
	for _, sub := range []string{"494.36", "494.37", "PHP"} {
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

// 0 元开通的事件原文：真实单、试运行各一条。
const (
	zeroAllowedNote       = "支付页显示 0 元（首月免费等优惠），该套餐在该地区允许 0 元开通，照常开通"
	zeroAllowedDryRunNote = "支付页显示 0 元（首月免费等优惠），该套餐在该地区允许 0 元开通；真实付款会照常开通"
)

// phConf 是 newHarness 里 plus/PH 的配置，allow 为「允许 0 元开通」（nil 表示沿用当前表的值）。
func phConf(allow *bool) RegionConf {
	return RegionConf{Enabled: true, Currency: "PHP", CapMinor: 120000, FloorMinor: 10000, FundCents: 3000, AllowZero: allow}
}

// mustZeroAllowed 确认套餐表里该套餐该地区的「允许 0 元开通」是 want，免得夹具默认值变了测试却空过。
func mustZeroAllowed(t *testing.T, plan, region string, want bool) {
	t.Helper()
	_, rc, err := ResolvePlan(plan, region, true)
	if err != nil {
		t.Fatal(err)
	}
	if rc.ZeroAllowed() != want {
		t.Fatalf("%s/%s 允许 0 元开通应为 %v: %+v", plan, region, want, rc)
	}
}

// countEventExact 数出级别为 level、原文正好是 msg 的事件条数。
func countEventExact(t *testing.T, no, level, msg string) int {
	t.Helper()
	n := 0
	for _, e := range eventStates(t, no) {
		if e.Level == level && e.Message == msg {
			n++
		}
	}
	return n
}

// Plus 允许 0 元开通、页面 ₱0.00：照常取卡、注资、点支付，按 Plus 确认成功，时间线记一条 info。
func TestZeroAmountAllowedProceeds(t *testing.T) {
	h := newPayHarness(t, nil)
	mustZeroAllowed(t, "plus", "PH", true)
	h.gpt.accountFn = planFrom(2)
	h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess, text: "Thanks"})
	setPage(h, 0, "₱0.00")
	h.start(t)
	o := waitDone(t, h.submit(t, false).OrderNo)
	if h.pool.count("Acquire") == 0 {
		t.Fatalf("允许 0 元开通应走到取卡: %+v", o)
	}
	if o.State != StateSucceeded || o.ErrorCode == "amount_below_floor" {
		t.Fatalf("允许 0 元开通应照常开通成功: %+v", o)
	}
	if n := countEventExact(t, o.OrderNo, "info", zeroAllowedNote); n != 1 {
		t.Fatalf("应有一条 info「%s」: %d %+v", zeroAllowedNote, n, eventStates(t, o.OrderNo))
	}
}

// Plus 取消勾选、页面 ₱0.00：照旧以 amount_below_floor 放弃，没碰卡池。
func TestZeroAmountNotAllowedAborts(t *testing.T) {
	h := newPayHarness(t, nil)
	saveWith(t, "plus", "PH", phConf(boolPtr(false)))
	mustZeroAllowed(t, "plus", "PH", false)
	setPage(h, 0, "₱0.00")
	h.start(t)
	o := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, o, "amount_below_floor")
	if h.pool.total() != 0 {
		t.Fatalf("不允许 0 元开通不应碰卡池: %d", h.pool.total())
	}
}

// 允许 0 元开通只放行正好是 0：₱50.00（0 以上、低于最低 ₱100.00）照旧以 amount_below_floor 放弃。
func TestPartialDiscountStillAborts(t *testing.T) {
	h := newPayHarness(t, nil)
	mustZeroAllowed(t, "plus", "PH", true)
	setPage(h, 5000, "₱50.00")
	h.start(t)
	o := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, o, "amount_below_floor")
	if h.pool.total() != 0 {
		t.Fatalf("低于最低金额不应碰卡池: %d", h.pool.total())
	}
}

// Pro 默认不允许 0 元开通：页面 ₱0.00 照旧以 amount_below_floor 放弃。
func TestZeroAmountProPlanAborts(t *testing.T) {
	h := newPayHarness(t, nil)
	saveWith(t, "pro_20x", "PH", phConf(nil))
	mustZeroAllowed(t, "pro_20x", "PH", false)
	setPage(h, 0, "₱0.00")
	h.start(t)
	sub, err := h.svc.Submit(ctx, Request{Provider: testProvider, PlanKey: "pro_20x", Region: "PH", Session: testSession, Source: "admin_test", SourceRef: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	o := waitDone(t, sub.OrderNo)
	assertNotSubmitted(t, o, "amount_below_floor")
	if h.pool.total() != 0 {
		t.Fatalf("不应碰卡池: %d", h.pool.total())
	}
}

// 点数单没有 0 元开通：页面 ₱0.00 照旧以 amount_below_floor 放弃。
func TestZeroAmountCreditAborts(t *testing.T) {
	h := newCreditHarness(t)
	mustZeroAllowed(t, "credit250", "PH", false)
	setPage(h, 0, "₱0.00")
	h.start(t)
	o := waitDone(t, h.submitCredit(t, false, false).OrderNo)
	assertNotSubmitted(t, o, "amount_below_floor")
	if h.pool.count("Acquire") != 0 || h.pool.count("Fund") != 0 || h.pool.total() != 0 {
		t.Fatalf("不应碰卡池: %d", h.pool.total())
	}
}

// 试运行页面 ₱0.00：允许时记 info「真实付款会照常开通」，不记低于最低金额的 warn；不允许时照旧记 warn「真实付款会在此放弃」。
func TestDryRunZeroAmountNotes(t *testing.T) {
	run := func(t *testing.T, allow bool) *Order {
		h := newHarness(t)
		if !allow {
			saveWith(t, "plus", "PH", phConf(boolPtr(false)))
		}
		mustZeroAllowed(t, "plus", "PH", allow)
		setPage(h, 0, "₱0.00")
		h.start(t)
		o := waitDone(t, h.submit(t, true).OrderNo)
		if o.State != StatePreviewed {
			t.Fatalf("试运行应完成预览: %+v", o)
		}
		return o
	}
	t.Run("允许", func(t *testing.T) {
		o := run(t, true)
		if n := countEventExact(t, o.OrderNo, "info", zeroAllowedDryRunNote); n != 1 {
			t.Fatalf("应有一条 info「%s」: %d %+v", zeroAllowedDryRunNote, n, eventStates(t, o.OrderNo))
		}
		if n := countEvents(t, o.OrderNo, "低于最低金额"); n != 0 {
			t.Fatalf("允许 0 元开通不应再记低于最低金额: %d", n)
		}
	})
	t.Run("不允许", func(t *testing.T) {
		o := run(t, false)
		n := 0
		for _, e := range eventStates(t, o.OrderNo) {
			if e.Level == "warn" && strings.Contains(e.Message, "低于最低金额") && strings.Contains(e.Message, "真实付款会在此放弃") {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("应有一条 warn「…真实付款会在此放弃」: %d", n)
		}
		if n := countEvents(t, o.OrderNo, "允许 0 元开通"); n != 0 {
			t.Fatalf("不允许时不应记 0 元开通: %d", n)
		}
	})
}

// 「允许 0 元开通」和最低金额一样在建会话时取值：之后管理员取消勾选，不影响这一单。
// 包内直接调 createLink、openPage（不启动工作协程），才能在两步之间改套餐表。
func TestZeroAllowedTakenAtCreateLink(t *testing.T) {
	h := newPayHarness(t, nil)
	mustZeroAllowed(t, "plus", "PH", true)
	o := h.submit(t, false) // 不启动工作协程
	j := &job{o: o, gpt: h.gpt}
	if err := h.svc.advance(ctx, j, StateChecking, "检查账号", nil); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.createLink(ctx, j); err != nil {
		t.Fatal(err)
	}
	if !j.allowZero {
		t.Fatal("建会话时 plus/PH 允许 0 元开通，应取到 true")
	}
	saveWith(t, "plus", "PH", phConf(boolPtr(false)))
	mustZeroAllowed(t, "plus", "PH", false)
	setPage(h, 0, "₱0.00")
	j.o.CheckoutURL = testCheckoutURL
	defer j.closeBrowser()
	if err := h.svc.openPage(ctx, j); err != nil {
		t.Fatalf("应按建会话时取到的「允许」放行: %v", err)
	}
	if h.pool.total() != 0 {
		t.Fatalf("openPage 不应碰卡池: %d", h.pool.total())
	}
}
