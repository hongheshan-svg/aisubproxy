package payengine

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// errFakeUnset 是测试替身未配置返回值时的错误。
var errFakeUnset = errors.New("测试替身未配置返回值")

// callLog 记录各方法的调用次数与最近一次参数，并发安全。
type callLog struct {
	mu    sync.Mutex
	calls map[string]int
	args  map[string][]any
	seq   []string // 按发生顺序记下的方法名
}

func (c *callLog) hit(name string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls, c.args = map[string]int{}, map[string][]any{}
	}
	c.calls[name]++
	c.args[name] = args
	c.seq = append(c.seq, name)
}

// order 返回按发生顺序记下的方法名。
func (c *callLog) order() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seq...)
}

func (c *callLog) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

func (c *callLog) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.calls {
		n += v
	}
	return n
}

func (c *callLog) lastArgs(name string) []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.args[name]
}

// alertLog 记录告警文本，并发安全。
type alertLog struct {
	mu    sync.Mutex
	texts []string
}

func (a *alertLog) add(text string) {
	a.mu.Lock()
	a.texts = append(a.texts, text)
	a.mu.Unlock()
}

func (a *alertLog) Texts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.texts...)
}

// releaseCall 是一次 Release 的参数。
type releaseCall struct {
	Lease   cardpool.Lease
	Outcome cardpool.Outcome
}

// testProvider 是测试单据所属的供卡型服务商名。引擎只按名字取卡池，不认识具体是哪家。
const testProvider = "marswallex"

// fakePool 记录调用；对应的函数字段未设置时方法返回 errFakeUnset。函数字段须在 Start 之前设好。
type fakePool struct {
	callLog
	acquire   func(cardpool.AcquireRequest) (*cardpool.Lease, error)
	fund      func(context.Context, *cardpool.Lease, int64) (*cardpool.Order, error)
	waitOrder func(context.Context, string) (*cardpool.Order, error)
	sensitive func(*cardpool.Lease) (*cardpool.CardSensitive, error)
	waitOTP   func(context.Context, string, time.Time) (string, error)
	release   func(*cardpool.Lease, cardpool.Outcome) error
	cardInfo  func(string) (cardpool.CardInfo, error)
	listTxns  func(cardpool.TxnFilter) ([]cardpool.Transaction, int, error)
	backfill  func(context.Context, int) (cardpool.BackfillState, error)

	relMu    sync.Mutex
	releases []releaseCall
}

func (p *fakePool) Acquire(_ context.Context, r cardpool.AcquireRequest) (*cardpool.Lease, error) {
	p.hit("Acquire", r)
	if p.acquire != nil {
		return p.acquire(r)
	}
	return nil, errFakeUnset
}

func (p *fakePool) Fund(c context.Context, l *cardpool.Lease, cents int64) (*cardpool.Order, error) {
	p.hit("Fund", l, cents)
	if p.fund != nil {
		return p.fund(c, l, cents)
	}
	return nil, errFakeUnset
}

func (p *fakePool) WaitOrder(c context.Context, no string) (*cardpool.Order, error) {
	p.hit("WaitOrder", no)
	if p.waitOrder != nil {
		return p.waitOrder(c, no)
	}
	return nil, errFakeUnset
}

func (p *fakePool) Sensitive(_ context.Context, l *cardpool.Lease) (*cardpool.CardSensitive, error) {
	p.hit("Sensitive", l)
	if p.sensitive != nil {
		return p.sensitive(l)
	}
	return nil, errFakeUnset
}

func (p *fakePool) WaitOTP(c context.Context, cardID string, since time.Time) (string, error) {
	p.hit("WaitOTP", cardID, since)
	if p.waitOTP != nil {
		return p.waitOTP(c, cardID, since)
	}
	return "", errFakeUnset
}

func (p *fakePool) Release(_ context.Context, l *cardpool.Lease, o cardpool.Outcome) error {
	p.hit("Release", l, o)
	p.relMu.Lock()
	p.releases = append(p.releases, releaseCall{Lease: *l, Outcome: o})
	p.relMu.Unlock()
	if p.release != nil {
		return p.release(l, o)
	}
	return errFakeUnset
}

// released 返回全部 Release 调用。
func (p *fakePool) released() []releaseCall {
	p.relMu.Lock()
	defer p.relMu.Unlock()
	return append([]releaseCall(nil), p.releases...)
}

func (p *fakePool) CardInfo(_ context.Context, cardID string) (cardpool.CardInfo, error) {
	p.hit("CardInfo", cardID)
	if p.cardInfo != nil {
		return p.cardInfo(cardID)
	}
	return cardpool.CardInfo{}, errFakeUnset
}

func (p *fakePool) ListTransactions(_ context.Context, f cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
	p.hit("ListTransactions", f)
	if p.listTxns != nil {
		return p.listTxns(f)
	}
	return nil, 0, errFakeUnset
}

func (p *fakePool) BackfillRecent(c context.Context, days int) (cardpool.BackfillState, error) {
	p.hit("BackfillRecent", days)
	if p.backfill != nil {
		return p.backfill(c, days)
	}
	return cardpool.BackfillState{}, errFakeUnset
}

// fakeChatGPT 按配置返回账号与支付会话；配置须在 Start 之前设好。
type fakeChatGPT struct {
	callLog
	refreshErr, accountErr, checkoutErr error
	account                             chatgpt.Account
	checkout                            chatgpt.Checkout
	// accountFn 非 nil 时优先于 account / accountErr，n 为第几次调用 Account（从 1 起）。
	accountFn func(n int) (chatgpt.Account, error)
	// accountSess 非 nil 时优先于 accountFn，能按会话（例如邮箱）区分单据。
	accountSess func(s chatgpt.Session, n int) (chatgpt.Account, error)

	// 关闭续费：cancelFn、subStatusFn 非 nil 时按第 n 次调用取值；未设置时取消成功、核实为不再续费。
	cancelFn    func(n int) error
	subStatusFn func(n int) (chatgpt.RenewalInfo, error)

	// 点数：未设置时结账返回 Stripe 托管页，已存卡为空，余额不可用。
	creditCheckout    *chatgpt.CreditCheckoutResult
	creditCheckoutErr error
	savedCardsFn      func(n int) ([]chatgpt.SavedCard, error)
	balanceFn         func(n int) (chatgpt.CreditBalanceInfo, error)
}

func (g *fakeChatGPT) CreateCreditCheckout(_ context.Context, s chatgpt.Session, accountID string, quantity int) (chatgpt.CreditCheckoutResult, error) {
	g.hit("CreateCreditCheckout", accountID, quantity)
	if g.creditCheckoutErr != nil {
		return chatgpt.CreditCheckoutResult{}, g.creditCheckoutErr
	}
	if g.creditCheckout != nil {
		return *g.creditCheckout, nil
	}
	return chatgpt.CreditCheckoutResult{Tag: chatgpt.CheckoutTagHosted, SessionID: "cs_live_credit", URL: "https://checkout.stripe.com/c/pay/cs_live_credit"}, nil
}

func (g *fakeChatGPT) CancelRenewal(_ context.Context, s chatgpt.Session, accountID string) error {
	g.hit("CancelRenewal", accountID, s.AccessToken)
	if g.cancelFn != nil {
		return g.cancelFn(g.count("CancelRenewal"))
	}
	return nil
}

func (g *fakeChatGPT) SubscriptionStatus(_ context.Context, s chatgpt.Session, accountID string) (chatgpt.RenewalInfo, error) {
	g.hit("SubscriptionStatus", accountID)
	if g.subStatusFn != nil {
		return g.subStatusFn(g.count("SubscriptionStatus"))
	}
	off := false
	return chatgpt.RenewalInfo{WillRenew: &off}, nil
}

func (g *fakeChatGPT) SavedCards(_ context.Context, s chatgpt.Session, accountID string) ([]chatgpt.SavedCard, error) {
	g.hit("SavedCards", accountID)
	if g.savedCardsFn != nil {
		return g.savedCardsFn(g.count("SavedCards"))
	}
	return []chatgpt.SavedCard{}, nil
}

func (g *fakeChatGPT) CreditBalance(_ context.Context, s chatgpt.Session, accountID string) (chatgpt.CreditBalanceInfo, error) {
	g.hit("CreditBalance", accountID)
	if g.balanceFn != nil {
		return g.balanceFn(g.count("CreditBalance"))
	}
	return chatgpt.CreditBalanceInfo{}, nil
}

func (g *fakeChatGPT) Refresh(_ context.Context, s *chatgpt.Session) error {
	g.hit("Refresh", s.SessionToken)
	if g.refreshErr != nil {
		return g.refreshErr
	}
	s.AccessToken = "at-refreshed"
	return nil
}

func (g *fakeChatGPT) Account(_ context.Context, s chatgpt.Session) (chatgpt.Account, error) {
	g.hit("Account", s.AccessToken)
	if g.accountSess != nil {
		return g.accountSess(s, g.count("Account"))
	}
	if g.accountFn != nil {
		return g.accountFn(g.count("Account"))
	}
	return g.account, g.accountErr
}

func (g *fakeChatGPT) CreateCheckout(_ context.Context, s chatgpt.Session, plan, country, currency string) (chatgpt.Checkout, error) {
	g.hit("CreateCheckout", plan, country, currency)
	return g.checkout, g.checkoutErr
}

// browserFactory 是 Options.Browser 的替身：记录收到的代理与截图目录，造出 fakeBrowser。
// 金额等配置须在 Start 之前设好。
type browserFactory struct {
	err        error // 非 nil 时工厂直接返回它（模拟浏览器无法启动）
	amount     int64
	amountText string
	amountErr  error
	readAmount func(n int) (int64, string, error) // 非 nil 时按第 n 次（从 1 起）调用取值，覆盖上面三项
	openGate   chan struct{}                      // 非 nil 时 Open 阻塞到它被关闭
	blockNew   atomic.Bool                        // true 时 New 阻塞到 ctx 结束（模拟 Chrome 启动卡住），返回 ctx 的错误

	// 付款步骤的替身；未设置时方法返回 errFakeUnset。n 为该浏览器第几次调用 WaitOutcome（从 1 起）。
	fill        func(paybrowser.Card, paybrowser.Billing) error
	submit      func() error
	waitOutcome func(c context.Context, n int) (paybrowser.Outcome, string, error)
	enterOTP    func(string) error
	notices     func() []string // 未设置时没有提示
	openErr     error           // 非 nil 时 Open 返回它
	report      paybrowser.FillReport
	diagnose    func(context.Context) []string // 未设置时返回一行固定文字
	lockSubmit  func() bool                    // 未设置时返回 true：不能证明没点击，这是安全方向
	savePayment func() (string, error)         // EnsureNoSavePayment 的结果；未设置时为「页面没有」
	notRendered bool                           // true 时 WaitRendered 返回 false（页面一直没显示完整）

	mu         sync.Mutex
	proxies    []string
	shotDirs   []string
	made       []*fakeBrowser
	opening    int // 正在 Open 中的数量
	maxOpening int
}

func (f *browserFactory) New(ctx context.Context, proxyURL, shotDir string) (Browser, error) {
	if f.blockNew.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proxies = append(f.proxies, proxyURL)
	f.shotDirs = append(f.shotDirs, shotDir)
	if f.err != nil {
		return nil, f.err
	}
	b := &fakeBrowser{f: f}
	f.made = append(f.made, b)
	return b, nil
}

func (f *browserFactory) browsers() []*fakeBrowser {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeBrowser(nil), f.made...)
}

// received 返回工厂收到过的代理地址与截图目录。
func (f *browserFactory) received() (proxies, shotDirs []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.proxies...), append([]string(nil), f.shotDirs...)
}

// submitted 返回所有浏览器上 Submit 被调用的总次数。
func (f *browserFactory) submitted() int {
	n := 0
	for _, b := range f.browsers() {
		n += b.count("Submit")
	}
	return n
}

func (f *browserFactory) openingNow() (cur, peak int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opening, f.maxOpening
}

type fakeBrowser struct {
	callLog
	f        *browserFactory
	openKind paybrowser.PageKind // 最近一次 Open 收到的页面类型
}

func (b *fakeBrowser) Open(ctx context.Context, url string, kind paybrowser.PageKind, cookies []paybrowser.Cookie) error {
	b.hit("Open", url, cookies)
	b.f.mu.Lock()
	b.openKind = kind
	b.f.opening++
	if b.f.opening > b.f.maxOpening {
		b.f.maxOpening = b.f.opening
	}
	b.f.mu.Unlock()
	defer func() {
		b.f.mu.Lock()
		b.f.opening--
		b.f.mu.Unlock()
	}()
	if b.f.openGate != nil {
		select {
		case <-b.f.openGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.f.openErr
}

func (b *fakeBrowser) FillReport() paybrowser.FillReport {
	b.hit("FillReport")
	return b.f.report
}

func (b *fakeBrowser) EnsureNoSavePayment(context.Context) (string, error) {
	b.hit("EnsureNoSavePayment")
	if b.f.savePayment != nil {
		return b.f.savePayment()
	}
	return "页面没有", nil
}

func (b *fakeBrowser) PayButtonState(context.Context) string {
	b.hit("PayButtonState")
	return "可点击"
}

func (b *fakeBrowser) Diagnose(c context.Context) []string {
	b.hit("Diagnose")
	if b.f.diagnose != nil {
		return b.f.diagnose(c)
	}
	return []string{"地址：example.test/pay"}
}

func (b *fakeBrowser) ReadAmount(_ context.Context, currency string) (int64, string, error) {
	b.hit("ReadAmount", currency)
	if b.f.readAmount != nil {
		return b.f.readAmount(b.count("ReadAmount"))
	}
	return b.f.amount, b.f.amountText, b.f.amountErr
}

func (b *fakeBrowser) Fill(_ context.Context, c paybrowser.Card, bl paybrowser.Billing) error {
	b.hit("Fill", c, bl)
	if b.f.fill != nil {
		return b.f.fill(c, bl)
	}
	return errFakeUnset
}

func (b *fakeBrowser) Submit(context.Context) error {
	b.hit("Submit")
	if b.f.submit != nil {
		return b.f.submit()
	}
	return errFakeUnset
}

func (b *fakeBrowser) LockSubmit() bool {
	b.hit("LockSubmit")
	if b.f.lockSubmit != nil {
		return b.f.lockSubmit()
	}
	return true
}

func (b *fakeBrowser) WaitOutcome(c context.Context, timeout time.Duration) (paybrowser.Outcome, string, error) {
	b.hit("WaitOutcome", timeout)
	if b.f.waitOutcome != nil {
		return b.f.waitOutcome(c, b.count("WaitOutcome"))
	}
	return paybrowser.OutcomeUnknown, "", errFakeUnset
}

func (b *fakeBrowser) Notices() []string {
	b.hit("Notices")
	if b.f.notices != nil {
		return b.f.notices()
	}
	return nil
}

func (b *fakeBrowser) EnterOTP(_ context.Context, otp string) error {
	b.hit("EnterOTP", otp)
	if b.f.enterOTP != nil {
		return b.f.enterOTP(otp)
	}
	return errFakeUnset
}

func (b *fakeBrowser) Shot(_ context.Context, name string, scope paybrowser.ShotScope) (string, error) {
	b.hit("Shot", name, scope)
	return name + ".png", nil
}

func (b *fakeBrowser) Close() { b.hit("Close") }

func (b *fakeBrowser) WaitRendered(context.Context, time.Duration) bool {
	b.hit("WaitRendered")
	return !b.f.notRendered
}
