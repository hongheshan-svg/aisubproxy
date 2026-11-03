package payengine

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

func (h *harness) submitFillPreview(t *testing.T) *Order {
	t.Helper()
	o, err := h.svc.Submit(ctx, Request{PlanKey: "plus", Region: "PH", Session: testSession, Source: "admin_test", SourceRef: "admin", DryRunFill: true})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// assertNeverPaid 断言这单走不到点支付，卡池任何方法都没被调用。
func assertNeverPaid(t *testing.T, h *harness) {
	t.Helper()
	assertSubmitCount(t, h, 0)
	if n := h.pool.total(); n != 0 {
		t.Fatalf("试运行不应碰卡池，调用了 %d 次: %v", n, h.pool.calls)
	}
	for _, b := range h.br.browsers() {
		if n := b.count("WaitOutcome") + b.count("EnterOTP"); n != 0 {
			t.Fatalf("试运行不应等支付结果或填验证码: %d", n)
		}
	}
}

// 试运行到填表：走完 Fill 但 Submit 调用 0 次，卡池任何方法调用 0 次，以 previewed 结束，时间线有字段结果、没有卡号和邮箱。
func TestDryRunFillNeverPays(t *testing.T) {
	h := newHarness(t)
	var card paybrowser.Card
	var bill paybrowser.Billing
	h.br.fill = func(c paybrowser.Card, b paybrowser.Billing) error {
		card, bill = c, b
		return nil
	}
	h.br.report = paybrowser.FillReport{
		Fields:   []paybrowser.FieldCheck{{Label: "卡号", Selector: "#cardNumber", Status: "核对一致"}},
		LinkSave: "已取消",
		Email:    "必填且为空，已填账号邮箱",
		Phone:    "未显示",
	}
	h.start(t)
	o := h.submitFillPreview(t)
	if !o.DryRun || !o.DryRunFill {
		t.Fatalf("返回的单据应标明试运行到填表: %+v", o)
	}
	g := waitDone(t, o.OrderNo)
	if g.State != StatePreviewed || g.Failure != "" || g.SubmittedAt != nil || g.LeaseToken != "" || g.CardID != "" {
		t.Fatalf("应以 previewed 结束且没有提交、没有租约: %+v", g)
	}
	if !g.DryRun || !g.DryRunFill || g.SessionSealed != "" || g.AmountText != "₱1,120.00" {
		t.Fatalf("单据字段不符: %+v", g)
	}
	assertNeverPaid(t, h)

	b := theBrowser(t, h)
	if b.count("Fill") != 1 || b.count("Close") != 1 {
		t.Fatalf("应填表 1 次并关闭浏览器: %d %d", b.count("Fill"), b.count("Close"))
	}
	if card.Number != "4242424242424242" || card.ExpMonth != "12" || len(card.ExpYear) != 2 || card.CVC != "123" {
		t.Fatalf("应使用公开测试卡: %+v", card)
	}
	if bill.Email != "buyer@example.com" || bill.Name != previewHolder || bill.Country != "US" {
		t.Fatalf("账单信息不符: %+v", bill)
	}
	if b.count("Shot") != 1 {
		t.Fatalf("只在填卡前截 1 张图: %d", b.count("Shot"))
	}

	waitEvent(t, o.OrderNo, "试运行填表 字段「卡号」：命中 #cardNumber（主页面），核对一致")
	waitEvent(t, o.OrderNo, "试运行填表 Link 保存：已取消")
	waitEvent(t, o.OrderNo, "试运行填表 邮箱：必填且为空，已填账号邮箱")
	waitEvent(t, o.OrderNo, "试运行页面 地址：")
	waitEvent(t, o.OrderNo, "支付按钮：可点击，未点击")
	for _, e := range eventStates(t, o.OrderNo) {
		if strings.Contains(e.Message, card.Number) || strings.Contains(e.Message, "buyer@example.com") {
			t.Fatalf("时间线含卡号或邮箱: %+v", e)
		}
	}
	if ev := eventStates(t, o.OrderNo); !containsInOrder(ev, StateOpening, StateFilling, StatePreviewed) {
		t.Fatalf("事件顺序不符: %+v", ev)
	}
}

// 填表失败：failed / not_submitted，错误码单独一个，免得被付款熔断算成真实填表失败。同样不碰卡池、不点支付。
func TestDryRunFillFailure(t *testing.T) {
	h := newHarness(t)
	h.br.fill = func(paybrowser.Card, paybrowser.Billing) error { return errors.New("找不到「城市」输入框") }
	h.br.report = paybrowser.FillReport{Fields: []paybrowser.FieldCheck{{Label: "卡号", Selector: "#cardNumber", Status: "已填写"}}}
	h.start(t)
	o := h.submitFillPreview(t)
	g := waitDone(t, o.OrderNo)
	assertNotSubmitted(t, g, "dry_run_fill_failed")
	if !strings.Contains(g.ErrorMessage, "城市") {
		t.Fatalf("应写明失败原因: %+v", g)
	}
	assertNeverPaid(t, h)
	waitEvent(t, o.OrderNo, "试运行填表 字段「卡号」")
	waitEvent(t, o.OrderNo, "试运行填表失败 地址：")
	if a := h.alerts.Texts(); len(a) != 0 {
		t.Fatalf("试运行失败不告警: %v", a)
	}
}

// 测试卡的有效期始终在未来，年份是两位数。
func TestPreviewCardExpiry(t *testing.T) {
	for now, want := range map[time.Time]string{
		time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC):  "29",
		time.Date(2097, 12, 31, 0, 0, 0, 0, time.UTC): "00",
		time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC):   "02",
	} {
		if c := previewCard(now); c.ExpYear != want || c.ExpMonth != "12" {
			t.Errorf("%v: 有效期 %s/%s，期望 12/%s", now, c.ExpMonth, c.ExpYear, want)
		}
	}
}

// 试运行单停在 filling 时服务重启：和其它提交前的单一样收敛，没有租约，不碰卡池。
func TestDryRunFillRestartNeverTouchesPool(t *testing.T) {
	h := newHarness(t)
	o := &Order{OrderNo: "PODRYFILLRESTART", Source: "admin_test", SourceRef: "admin", DryRun: true, DryRunFill: true,
		PlanKey: "plus", Region: "PH", Currency: "PHP", AccountEmail: "buyer@example.com", State: StateFilling}
	if err := insertOrder(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.recoverOrders(ctx); err != nil {
		t.Fatal(err)
	}
	assertNotSubmitted(t, mustOrder(t, o.OrderNo), "service_restart")
	if n := h.pool.total(); n != 0 {
		t.Fatalf("不应碰卡池: %d", n)
	}
}

// 第 1 层：填表用的接口没有任何能导致付款的方法。
func TestFormBrowserCannotPay(t *testing.T) {
	typ := reflect.TypeOf((*FormBrowser)(nil)).Elem()
	for _, name := range []string{"Submit", "WaitOutcome", "EnterOTP"} {
		if _, ok := typ.MethodByName(name); ok {
			t.Fatalf("FormBrowser 不能有 %s", name)
		}
	}
}

// 第 2 层：试运行单一创建浏览器就锁定，在打开页面之前；真实付款单不锁定。
func TestDryRunLocksBrowserBeforeOpen(t *testing.T) {
	for name, submit := range map[string]func(*testing.T, *harness) *Order{
		"试运行到填表":  func(t *testing.T, h *harness) *Order { return h.submitFillPreview(t) },
		"试运行到读金额": func(t *testing.T, h *harness) *Order { return h.submit(t, true) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.br.fill = func(paybrowser.Card, paybrowser.Billing) error { return nil }
			var opened atomic.Int32
			opened.Store(-1)
			h.br.lockSubmit = func() bool {
				opened.Store(int32(h.br.browsers()[0].count("Open")))
				return false
			}
			h.start(t)
			g := waitDone(t, submit(t, h).OrderNo)
			if g.State != StatePreviewed {
				t.Fatalf("应 previewed: %+v", g)
			}
			if n := opened.Load(); n != 0 {
				t.Fatalf("应在打开页面之前锁定，锁定时 Open 已调用 %d 次（-1 表示没锁定）", n)
			}
			assertNeverPaid(t, h)
		})
	}
	t.Run("真实付款单不锁定", func(t *testing.T) {
		h := newPayHarness(t, nil)
		h.gpt.accountFn = planFrom(2)
		h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess})
		h.start(t)
		g := waitDone(t, h.submit(t, false).OrderNo)
		if g.State != StateSucceeded {
			t.Fatalf("应成功: %+v", g)
		}
		if n := theBrowser(t, h).count("LockSubmit"); n != 0 {
			t.Fatalf("真实付款单不应锁定: %d", n)
		}
	})
}

// 第 4 层：pay 入口拒绝试运行单，不碰卡池。
func TestPayRefusesDryRun(t *testing.T) {
	h := newHarness(t)
	for _, o := range []*Order{{DryRun: true}, {DryRun: true, DryRunFill: true}} {
		var f *stepFailure
		if err := h.svc.pay(ctx, &job{o: o}); !errors.As(err, &f) || f.code != "dry_run_refused" {
			t.Fatalf("pay = %v，期望拒绝", err)
		}
	}
	if n := h.pool.total(); n != 0 {
		t.Fatalf("不应碰卡池: %d", n)
	}
}

// dry_run 列存 0、1、2：读回来三种单据各自分得清，试运行的 dry_run 都是 true。
func TestDryRunColumnRoundTrip(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		req       Request
		dry, fill bool
	}{
		{Request{}, false, false},
		{Request{DryRun: true}, true, false},
		{Request{DryRunFill: true}, true, true},
		{Request{DryRun: true, DryRunFill: true}, true, true},
	}
	for i, c := range cases {
		r := c.req
		r.PlanKey, r.Region, r.Source, r.SourceRef = "plus", "PH", "admin_test", "admin"
		r.Session = sessionFor(string(rune('a'+i)) + "@example.com")
		o, err := h.svc.Submit(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		g := mustOrder(t, o.OrderNo)
		if g.DryRun != c.dry || g.DryRunFill != c.fill {
			t.Fatalf("%+v 读回 DryRun=%v DryRunFill=%v", c.req, g.DryRun, g.DryRunFill)
		}
	}
}

// 真实 Chrome + 真实 launchBrowser + 真实DOM仿造页：页面上支付按钮 0 次点击，/pay 0 次，时间线没有卡号和邮箱。
func TestDryRunFillEndToEndRealChrome(t *testing.T) {
	if _, err := paybrowser.FindChrome(); err != nil {
		t.Skip("本机没有 Chrome")
	}
	page, err := os.ReadFile(filepath.Join("..", "paybrowser", "testdata", "checkout-stripe.html"))
	if err != nil {
		t.Fatal(err)
	}
	var pays, clicks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checkout-stripe.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(bytes.ReplaceAll(page, []byte("__CARD_ORIGIN__"), []byte("http://localhost:1")))
		case "/pay":
			pays.Add(1)
		case "/submit-attempt":
			clicks.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	h := newHarness(t)
	h.svc.opts.Browser = launchBrowser // 显式给工厂，不算默认的真实浏览器：不清理系统临时目录，免得影响并行的其它包
	h.gpt.checkout.URL = "http://127.0.0.1:" + port + "/checkout-stripe.html?mode=success&token=secret-token"
	h.start(t)
	o := h.submitFillPreview(t)

	var g *Order
	for deadline := time.Now().Add(90 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		g = mustOrder(t, o.OrderNo)
		if IsTerminal(g.State) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("试运行没有在时限内结束: %+v", g)
		}
	}
	if g.State != StatePreviewed {
		t.Fatalf("应 previewed: %+v\n%+v", g, eventStates(t, o.OrderNo))
	}
	time.Sleep(300 * time.Millisecond)
	if n, p := clicks.Load(), pays.Load(); n != 0 || p != 0 {
		t.Fatalf("支付按钮被点了 %d 次，/pay %d 次，都应为 0", n, p)
	}
	if n := h.pool.total(); n != 0 {
		t.Fatalf("不应碰卡池: %d", n)
	}

	var all []string
	for _, e := range eventStates(t, o.OrderNo) {
		all = append(all, e.Message)
	}
	text := strings.Join(all, "\n")
	for _, want := range []string{
		"字段「卡号」：命中 #cardNumber（主页面），核对一致",
		"字段「城市」：命中 #shippingLocality（主页面），核对一致",
		"手动地址：已点击 1 次",
		"Link 保存：已取消",
		"邮箱：必填且为空，已填账号邮箱",
		"手机：未显示",
		"支付按钮：可点击，未点击",
		"选择器可见命中",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("时间线缺少 %q：\n%s", want, text)
		}
	}
	for _, secret := range []string{previewCardNumber, "buyer@example.com", "secret-token", testSessionToken, testAccessToken} {
		if strings.Contains(text, secret) {
			t.Errorf("时间线泄露了 %q：\n%s", secret, text)
		}
	}
	// 只有填卡前那张整页图
	shots, err := os.ReadDir(filepath.Join(h.shotDir, o.OrderNo))
	if err != nil || len(shots) != 1 {
		t.Fatalf("应只有 1 张截图: %v %v", shots, err)
	}
}
