package payengine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// *cardpool.Service 须能直接作为 Pool 使用。
var _ Pool = (*cardpool.Service)(nil)

const (
	testAccessToken  = "at-secret-123"
	testSessionToken = "st-secret-456"
	testCheckoutURL  = "https://pay.example/c/cs_live_abc"
)

func sessionFor(email string) string {
	return fmt.Sprintf(`{"user":{"email":%q},"accessToken":%q,"sessionToken":%q,"expires":"2099-01-01T00:00:00Z"}`,
		email, testAccessToken, testSessionToken)
}

var testSession = sessionFor(" Buyer@Example.com ")

type harness struct {
	svc     *Service
	pool    *fakePool
	gpt     *fakeChatGPT
	br      *browserFactory
	alerts  *alertLog
	shotDir string
}

// newHarness 建内存库与主密钥，启用 plus/PH（上限 ₱1,200.00、注资 30 美元），装好全部替身；不启动工作协程。
func newHarness(t *testing.T) *harness {
	t.Helper()
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	tbl := DefaultPlanTable()
	for i := range tbl {
		if tbl[i].Key == "plus" {
			tbl[i].Regions["PH"] = RegionConf{Enabled: true, Currency: "PHP", CapMinor: 120000, FundCents: 3000}
		}
	}
	if err := SavePlanTable(tbl); err != nil {
		t.Fatal(err)
	}
	h := &harness{
		pool: &fakePool{},
		gpt: &fakeChatGPT{
			account:  chatgpt.Account{AccountID: "acc-1", PlanType: "free"},
			checkout: chatgpt.Checkout{SessionID: "cs_live_abc", URL: testCheckoutURL},
		},
		br:      &browserFactory{amount: 112000, amountText: "₱1,120.00"},
		alerts:  &alertLog{},
		shotDir: t.TempDir(),
	}
	h.svc = New(Options{
		Pool: h.pool,
		ChatGPT: func(proxyURL string) (ChatGPT, error) {
			h.gpt.hit("New", proxyURL)
			return h.gpt, nil
		},
		Browser: h.br.New,
		Alert:   h.alerts.add,
		ShotDir: h.shotDir,
	})
	return h
}

// start 启动工作协程，测试结束时取消并等它退出。
func (h *harness) start(t *testing.T) {
	t.Helper()
	runCtx, cancel := context.WithCancel(context.Background())
	h.svc.Start(runCtx)
	t.Cleanup(func() {
		cancel()
		h.svc.Wait()
	})
}

func (h *harness) submitAs(t *testing.T, session string, dryRun bool) *Order {
	t.Helper()
	o, err := h.svc.Submit(ctx, Request{PlanKey: "plus", Region: "PH", Session: session, Source: "admin_test", SourceRef: "admin", DryRun: dryRun})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func (h *harness) submit(t *testing.T, dryRun bool) *Order {
	t.Helper()
	return h.submitAs(t, testSession, dryRun)
}

// waitDone 轮询到单据进入终态，最多 5 秒。
func waitDone(t *testing.T, no string) *Order {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		o, err := getOrder(ctx, no)
		if err == nil && IsTerminal(o.State) {
			return o
		}
		if time.Now().After(deadline) {
			t.Fatalf("单据 %s 未在时限内结束: %+v %v", no, o, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertNotSubmitted(t *testing.T, o *Order, code string) {
	t.Helper()
	if o.State != StateFailed || o.Failure != FailureNotSubmitted || o.ErrorCode != code {
		t.Fatalf("期望 failed / not_submitted / %s，得到 %s / %s / %s（%s）", code, o.State, o.Failure, o.ErrorCode, o.ErrorMessage)
	}
	if o.SessionSealed != "" {
		t.Fatal("终态应清空会话")
	}
}

func TestSubmitValidates(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Submit(ctx, Request{PlanKey: "pro_5x", Region: "PH", Session: testSession, Source: "admin_test"})
	if !errors.Is(err, ErrPlanUnavailable) {
		t.Fatalf("套餐未启用应为 ErrPlanUnavailable: %v", err)
	}
	_, err = h.svc.Submit(ctx, Request{PlanKey: "plus", Region: "PH", Session: `{"user":{"email":"a@x.com"},"accessToken":"at"}`, Source: "admin_test"})
	if !errors.Is(err, ErrBadSession) {
		t.Fatalf("缺 sessionToken 应为 ErrBadSession: %v", err)
	}

	t.Setenv("APP_MASTER_KEY", "")
	if _, err := h.svc.Submit(ctx, Request{PlanKey: "plus", Region: "PH", Session: testSession, Source: "admin_test"}); err == nil {
		t.Fatal("主密钥缺失应报错")
	}
	if _, total, _ := listOrders(ctx, "", 1, 0); total != 0 {
		t.Fatalf("出错时不应建单: %d", total)
	}
	dbtest.SetMasterKey(t)

	o := h.submit(t, false)
	if o.State != StateQueued {
		t.Fatalf("新单应为 queued: %s", o.State)
	}
	if _, err := h.svc.Submit(ctx, Request{PlanKey: "plus", Region: "PH", Session: testSession, Source: "admin_test"}); !errors.Is(err, ErrInflight) {
		t.Fatalf("同邮箱进行中应为 ErrInflight: %v", err)
	}
	g, err := getOrder(ctx, o.OrderNo)
	if err != nil {
		t.Fatal(err)
	}
	if g.State != StateQueued || g.AccountEmail != "buyer@example.com" || g.Currency != "PHP" ||
		g.CapMinor != 120000 || g.FundCents != 3000 || g.Source != "admin_test" || g.SourceRef != "admin" || g.DryRun {
		t.Fatalf("单据字段不符: %+v", g)
	}
	if g.SessionSealed == "" || strings.Contains(g.SessionSealed, testAccessToken) || strings.Contains(g.SessionSealed, testSessionToken) {
		t.Fatalf("会话应以密文存储: %q", g.SessionSealed)
	}
	key, _ := secretbox.LoadKey()
	if raw, err := secretbox.Open(key, "pay_order_session", g.SessionSealed); err != nil || raw != testSession {
		t.Fatalf("密文应能用 pay_order_session 解开: %v", err)
	}
}

func eventStates(t *testing.T, no string) []Event {
	t.Helper()
	ev, err := listEvents(ctx, no)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

// containsInOrder 判断 want 是否按顺序出现在事件的状态序列里。
func containsInOrder(ev []Event, want ...string) bool {
	i := 0
	for _, e := range ev {
		if i < len(want) && e.State == want[i] {
			i++
		}
	}
	return i == len(want)
}

func TestDryRunPreviewed(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	o := h.submit(t, true)
	g := waitDone(t, o.OrderNo)
	if g.State != StatePreviewed || g.AmountMinor != 112000 || g.AmountText != "₱1,120.00" ||
		g.CheckoutSessionID != "cs_live_abc" || g.CheckoutURL != testCheckoutURL || g.AccountID != "acc-1" {
		t.Fatalf("试运行结果不符: %+v", g)
	}
	if g.SessionSealed != "" || g.FinishedAt == nil {
		t.Fatalf("终态应清空会话并记结束时间: %+v", g)
	}
	if n := h.pool.total(); n != 0 {
		t.Fatalf("试运行不应调用卡池，实际 %d 次", n)
	}
	ev := eventStates(t, o.OrderNo)
	if !containsInOrder(ev, StateChecking, StateLinkCreated, StateOpening, StatePreviewed) {
		t.Fatalf("事件顺序不符: %+v", ev)
	}
	for _, e := range ev {
		if strings.Contains(e.Message, testSessionToken) || strings.Contains(e.Message, testAccessToken) {
			t.Fatalf("事件不应含会话: %+v", e)
		}
	}
	if a := h.gpt.lastArgs("CreateCheckout"); len(a) != 3 || a[0] != "chatgptplusplan" || a[1] != "PH" || a[2] != "PHP" {
		t.Fatalf("创建支付会话参数不符: %v", a)
	}
	if a := h.gpt.lastArgs("Refresh"); len(a) != 1 || a[0] != testSessionToken {
		t.Fatalf("应用原会话刷新: %v", a)
	}
	if a := h.gpt.lastArgs("Account"); len(a) != 1 || a[0] != "at-refreshed" {
		t.Fatalf("查账号应使用刷新后的 token: %v", a)
	}

	bs := h.br.browsers()
	if len(bs) != 1 {
		t.Fatalf("应启动 1 个浏览器: %d", len(bs))
	}
	b := bs[0]
	if b.count("Close") != 1 {
		t.Fatal("浏览器应已关闭")
	}
	open := b.lastArgs("Open")
	cookies, _ := open[1].([]paybrowser.Cookie)
	if open[0] != testCheckoutURL || len(cookies) != 1 || cookies[0].Domain != ".chatgpt.com" ||
		cookies[0].Value != testSessionToken || cookies[0].Name != "__Secure-next-auth.session-token" {
		t.Fatalf("打开支付页参数不符: %v", open)
	}
	if a := b.lastArgs("ReadAmount"); len(a) != 1 || a[0] != "PHP" {
		t.Fatalf("读金额币种不符: %v", a)
	}
	if a := b.lastArgs("Shot"); len(a) != 2 || a[0] != "01-opening" || a[1] != paybrowser.ShotPage {
		t.Fatalf("应截一张整页图 01-opening: %v", a)
	}
	if b.count("Fill") != 0 || b.count("Submit") != 0 {
		t.Fatal("试运行不应填卡或提交")
	}
	shot := false
	for _, e := range ev {
		if e.Screenshot == "01-opening.png" && e.State == StateOpening {
			shot = true
		}
	}
	if !shot {
		t.Fatalf("时间线应记截图: %+v", ev)
	}
	if _, dirs := h.br.received(); len(dirs) != 1 || dirs[0] != filepath.Join(h.shotDir, o.OrderNo) {
		t.Fatalf("截图目录应为 <ShotDir>/<单号>: %v", dirs)
	}
}

func TestAlreadySubscribedFails(t *testing.T) {
	h := newHarness(t)
	h.gpt.account = chatgpt.Account{AccountID: "acc-1", PlanType: "plus", HasActiveSubscription: true}
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, g, "already_subscribed")
	if h.gpt.count("CreateCheckout") != 0 {
		t.Fatal("已有订阅不应创建支付会话")
	}
	if len(h.br.browsers()) != 0 {
		t.Fatal("不应启动浏览器")
	}
}

func TestUnauthorizedFails(t *testing.T) {
	cases := map[string]func(*fakeChatGPT){
		"查账号未授权": func(g *fakeChatGPT) { g.accountErr = chatgpt.ErrUnauthorized },
		"刷新未授权": func(g *fakeChatGPT) {
			g.refreshErr = fmt.Errorf("%w（会话未返回 accessToken）", chatgpt.ErrUnauthorized)
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			set(h.gpt)
			h.start(t)
			g := waitDone(t, h.submit(t, false).OrderNo)
			assertNotSubmitted(t, g, "session_invalid")
			if h.gpt.count("CreateCheckout") != 0 {
				t.Fatal("会话失效不应创建支付会话")
			}
		})
	}
}

func TestAmountOverCapAborts(t *testing.T) {
	h := newHarness(t)
	h.br.amount, h.br.amountText = 130000, "₱1,300.00"
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, g, "amount_over_cap")
	if g.AmountMinor != 130000 || g.AmountText != "₱1,300.00" {
		t.Fatalf("应记下读到的金额: %+v", g)
	}
	if n := h.pool.total(); n != 0 {
		t.Fatalf("超上限不应调用卡池，实际 %d 次", n)
	}
	bs := h.br.browsers()
	if len(bs) != 1 || bs[0].count("Close") != 1 {
		t.Fatal("浏览器应已关闭")
	}
}

func TestAmountUnreadableAborts(t *testing.T) {
	h := newHarness(t)
	h.br.amountErr = errors.New("支付页上没有找到金额")
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, g, "amount_unreadable")
	if bs := h.br.browsers(); len(bs) != 1 || bs[0].count("Close") != 1 {
		t.Fatal("浏览器应已关闭")
	}
	if h.pool.total() != 0 {
		t.Fatal("不应调用卡池")
	}
}

func TestRealOrderStopsBeforePaying(t *testing.T) {
	h := newHarness(t)
	h.start(t)
	g := waitDone(t, h.submit(t, false).OrderNo)
	assertNotSubmitted(t, g, "pay_not_ready")
	if g.AmountMinor != 112000 || g.SubmittedAt != nil {
		t.Fatalf("应记金额且未提交: %+v", g)
	}
	if h.pool.total() != 0 {
		t.Fatal("不应调用卡池")
	}
	if bs := h.br.browsers(); len(bs) != 1 || bs[0].count("Close") != 1 || bs[0].count("Submit") != 0 {
		t.Fatal("浏览器应已关闭且未点支付")
	}
}

func TestProxyPassedThrough(t *testing.T) {
	h := newHarness(t)
	const proxy = "http://u:pw@proxy.example:8080"
	if err := SaveProxies(map[string]string{"PH": proxy}); err != nil {
		t.Fatal(err)
	}
	h.start(t)
	g := waitDone(t, h.submit(t, true).OrderNo)
	if g.State != StatePreviewed {
		t.Fatalf("应完成试运行: %+v", g)
	}
	if a := h.gpt.lastArgs("New"); len(a) != 1 || a[0] != proxy {
		t.Fatalf("ChatGPT 工厂应收到代理: %v", a)
	}
	if px, _ := h.br.received(); len(px) != 1 || px[0] != proxy {
		t.Fatalf("浏览器工厂应收到同一代理: %v", px)
	}
}

func TestBrowserLaunchFailureAlerts(t *testing.T) {
	h := newHarness(t)
	const proxy = "http://u:pw-secret@proxy.example:8080"
	if err := SaveProxies(map[string]string{"PH": proxy}); err != nil {
		t.Fatal(err)
	}
	h.br.err = fmt.Errorf("启动 Chrome 失败: 代理 %s 无响应", proxy)
	h.start(t)
	o := h.submit(t, false)
	g := waitDone(t, o.OrderNo)
	assertNotSubmitted(t, g, "browser_failed")
	alerts := h.alerts.Texts()
	if len(alerts) != 1 || !strings.Contains(alerts[0], "浏览器") {
		t.Fatalf("应告警 1 次且含「浏览器」: %v", alerts)
	}
	texts := []string{alerts[0], g.ErrorMessage}
	for _, e := range eventStates(t, o.OrderNo) {
		texts = append(texts, e.Message)
	}
	for _, s := range []string{"pw-secret", testSessionToken, testAccessToken} {
		for _, txt := range texts {
			if strings.Contains(txt, s) {
				t.Fatalf("文本不应含敏感信息 %q: %q", s, txt)
			}
		}
	}
}

func TestConcurrencyLimit(t *testing.T) {
	h := newHarness(t)
	if err := SaveConcurrency(1); err != nil {
		t.Fatal(err)
	}
	h.br.openGate = make(chan struct{})
	h.start(t)
	a := h.submitAs(t, sessionFor("a@x.com"), true)
	b := h.submitAs(t, sessionFor("b@x.com"), true)

	deadline := time.Now().Add(5 * time.Second)
	for cur, _ := h.br.openingNow(); cur == 0; cur, _ = h.br.openingNow() {
		if time.Now().After(deadline) {
			t.Fatal("第一张单未进入 Open")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if _, peak := h.br.openingNow(); peak != 1 {
		t.Fatalf("并发 1 时同时 Open 的最大数应为 1: %d", peak)
	}
	if g, _ := getOrder(ctx, b.OrderNo); g.State != StateQueued {
		t.Fatalf("第二张单应仍在排队: %s", g.State)
	}
	close(h.br.openGate)
	for _, no := range []string{a.OrderNo, b.OrderNo} {
		if g := waitDone(t, no); g.State != StatePreviewed {
			t.Fatalf("%s 应完成试运行: %+v", no, g)
		}
	}
	if _, peak := h.br.openingNow(); peak != 1 {
		t.Fatalf("同时 Open 的最大数应为 1: %d", peak)
	}
}

func TestGetAndList(t *testing.T) {
	h := newHarness(t)
	o := h.submit(t, true)
	got, ev, err := h.svc.Get(ctx, o.OrderNo)
	if err != nil || got.OrderNo != o.OrderNo || len(ev) != 1 {
		t.Fatalf("Get 不符: %v %+v %+v", err, got, ev)
	}
	if _, _, err := h.svc.Get(ctx, "PO-none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应为 ErrNotFound: %v", err)
	}
	list, total, err := h.svc.List(ctx, StateQueued, 1, 20)
	if err != nil || total != 1 || len(list) != 1 || list[0].OrderNo != o.OrderNo {
		t.Fatalf("List 不符: %v %d %+v", err, total, list)
	}
}

func TestCurrencyMismatchAborts(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry=%v", dry), func(t *testing.T) {
			h := newHarness(t)
			h.br.amount, h.br.amountText = 2000, "US$20.00"
			h.start(t)
			g := waitDone(t, h.submit(t, dry).OrderNo)
			assertNotSubmitted(t, g, "currency_mismatch")
			if g.AmountText != "US$20.00" || g.AmountMinor != 0 {
				t.Fatalf("应保留原文且不记金额: %+v", g)
			}
			if !strings.Contains(g.ErrorMessage, "US$20.00") || !strings.Contains(g.ErrorMessage, "PHP") {
				t.Fatalf("消息应带原文与期望币种: %s", g.ErrorMessage)
			}
			if h.pool.total() != 0 {
				t.Fatal("不应调用卡池")
			}
			if bs := h.br.browsers(); len(bs) != 1 || bs[0].count("Close") != 1 {
				t.Fatal("浏览器应已关闭")
			}
		})
	}
}

func TestUnexpectedErrorFailsOrder(t *testing.T) {
	h := newHarness(t)
	o := h.submit(t, true)
	j := &job{o: o}
	if err := h.svc.advance(ctx, j, StateChecking, "检查账号", nil); err != nil {
		t.Fatal(err)
	}
	j.sess.SessionToken = "tok-secret-xyz"
	h.svc.settle(ctx, j, errors.New("写库失败 tok-secret-xyz"))
	g, _ := getOrder(ctx, o.OrderNo)
	assertNotSubmitted(t, g, "internal_error")
	if strings.Contains(g.ErrorMessage, "tok-secret-xyz") {
		t.Fatalf("错误文本应已脱敏: %s", g.ErrorMessage)
	}
}

func TestScrubSecrets(t *testing.T) {
	j := &job{proxy: "http://us:p%40s@h:8080"}
	j.sess.SessionToken = "st-token-1"
	out := j.scrub("dial http://us:p%40s@h:8080 failed; pw p@s enc p%40s tok st-token-1")
	for _, bad := range []string{"p@s", "p%40s", "st-token-1", "us:"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%q 不应出现: %s", bad, out)
		}
	}
	j = &job{proxy: "http://u:ab@h:1"}
	if out := j.scrub("bad ab pass"); strings.Contains(out, "ab") {
		t.Fatalf("短密码不应出现: %s", out)
	}
}

func TestConcurrencyTwoRunsInParallel(t *testing.T) {
	h := newHarness(t)
	if err := SaveConcurrency(2); err != nil {
		t.Fatal(err)
	}
	h.br.openGate = make(chan struct{})
	h.start(t)
	a := h.submitAs(t, sessionFor("a@x.com"), true)
	b := h.submitAs(t, sessionFor("b@x.com"), true)
	deadline := time.Now().Add(5 * time.Second)
	for cur, _ := h.br.openingNow(); cur < 2; cur, _ = h.br.openingNow() {
		if time.Now().After(deadline) {
			t.Fatal("并发 2 时应有两张单同时 Open")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(h.br.openGate)
	for _, no := range []string{a.OrderNo, b.OrderNo} {
		if g := waitDone(t, no); g.State != StatePreviewed {
			t.Fatalf("%s 应完成试运行: %+v", no, g)
		}
	}
	if _, peak := h.br.openingNow(); peak != 2 {
		t.Fatalf("峰值应为 2: %d", peak)
	}
}
