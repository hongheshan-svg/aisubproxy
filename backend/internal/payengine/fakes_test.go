package payengine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// errFakeUnset 是测试替身未配置返回值时的错误。
var errFakeUnset = errors.New("测试替身未配置返回值")

// callLog 记录各方法的调用次数与最近一次参数，并发安全。
type callLog struct {
	mu    sync.Mutex
	calls map[string]int
	args  map[string][]any
}

func (c *callLog) hit(name string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls, c.args = map[string]int{}, map[string][]any{}
	}
	c.calls[name]++
	c.args[name] = args
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

// fakePool 只记录调用，所有方法都返回 errFakeUnset。
type fakePool struct{ callLog }

func (p *fakePool) Acquire(_ context.Context, r cardpool.AcquireRequest) (*cardpool.Lease, error) {
	p.hit("Acquire", r)
	return nil, errFakeUnset
}

func (p *fakePool) Fund(_ context.Context, l *cardpool.Lease, cents int64) (*cardpool.Order, error) {
	p.hit("Fund", l, cents)
	return nil, errFakeUnset
}

func (p *fakePool) WaitOrder(_ context.Context, no string) (*cardpool.Order, error) {
	p.hit("WaitOrder", no)
	return nil, errFakeUnset
}

func (p *fakePool) Sensitive(_ context.Context, l *cardpool.Lease) (*marswallex.CardSensitive, error) {
	p.hit("Sensitive", l)
	return nil, errFakeUnset
}

func (p *fakePool) WaitOTP(_ context.Context, cardID string, since time.Time) (string, error) {
	p.hit("WaitOTP", cardID, since)
	return "", errFakeUnset
}

func (p *fakePool) Release(_ context.Context, l *cardpool.Lease, o cardpool.Outcome) error {
	p.hit("Release", l, o)
	return errFakeUnset
}

func (p *fakePool) CardInfo(_ context.Context, cardID string) (cardpool.CardInfo, error) {
	p.hit("CardInfo", cardID)
	return cardpool.CardInfo{}, errFakeUnset
}

func (p *fakePool) ListTransactions(_ context.Context, f cardpool.TxnFilter) ([]cardpool.Transaction, int, error) {
	p.hit("ListTransactions", f)
	return nil, 0, errFakeUnset
}

// fakeChatGPT 按配置返回账号与支付会话；配置须在 Start 之前设好。
type fakeChatGPT struct {
	callLog
	refreshErr, accountErr, checkoutErr error
	account                             chatgpt.Account
	checkout                            chatgpt.Checkout
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
	openGate   chan struct{} // 非 nil 时 Open 阻塞到它被关闭

	mu         sync.Mutex
	proxies    []string
	shotDirs   []string
	made       []*fakeBrowser
	opening    int // 正在 Open 中的数量
	maxOpening int
}

func (f *browserFactory) New(_ context.Context, proxyURL, shotDir string) (Browser, error) {
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

func (f *browserFactory) openingNow() (cur, peak int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opening, f.maxOpening
}

type fakeBrowser struct {
	callLog
	f *browserFactory
}

func (b *fakeBrowser) Open(ctx context.Context, url string, cookies []paybrowser.Cookie) error {
	b.hit("Open", url, cookies)
	b.f.mu.Lock()
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
	return nil
}

func (b *fakeBrowser) ReadAmount(_ context.Context, currency string) (int64, string, error) {
	b.hit("ReadAmount", currency)
	return b.f.amount, b.f.amountText, b.f.amountErr
}

func (b *fakeBrowser) Fill(_ context.Context, c paybrowser.Card, bl paybrowser.Billing) error {
	b.hit("Fill", c, bl)
	return errFakeUnset
}

func (b *fakeBrowser) Submit(context.Context) error {
	b.hit("Submit")
	return errFakeUnset
}

func (b *fakeBrowser) WaitOutcome(_ context.Context, timeout time.Duration) (paybrowser.Outcome, string, error) {
	b.hit("WaitOutcome", timeout)
	return paybrowser.OutcomeUnknown, "", errFakeUnset
}

func (b *fakeBrowser) EnterOTP(_ context.Context, otp string) error {
	b.hit("EnterOTP", otp)
	return errFakeUnset
}

func (b *fakeBrowser) Shot(_ context.Context, name string, scope paybrowser.ShotScope) (string, error) {
	b.hit("Shot", name, scope)
	return name + ".png", nil
}

func (b *fakeBrowser) Close() { b.hit("Close") }
