package paybrowser_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

const sessionCookie = "__Secure-next-auth.session-token"

// TestMain 缩短与业务判断无关的等待，这个包的用例都开真实 Chrome，时间要省着用。
func TestMain(m *testing.M) {
	paybrowser.SetDeclineSettle(300 * time.Millisecond)
	paybrowser.SetEmailSettle(200 * time.Millisecond)
	os.Exit(m.Run())
}

// payRecord 是仿造页提交到 /pay 的全部字段。
type payRecord struct {
	Method, Number, Expiry, CVC            string
	NumberKeys                             int
	Name, Country, Line1, City, State, Zip string
	Email, Phone, DecoyNumber              string
	LinkSave                               bool
	SavePayment                            bool
}

// checkoutKind 是一份仿造支付页。旧页的字段迎合 selectors.go 的假设；Stripe 页照真实 DOM 写，
// 核心用例要在两份上都跑，才能发现代码的假设和真实页面不符。
type checkoutKind struct {
	name string
	file string
	// real 表示页面行为照真实 Stripe Checkout：成功后跳走、没有成功标志，邮箱必填
	real bool
}

var (
	oldCheckout    = checkoutKind{name: "旧仿造页", file: "checkout.html"}
	stripeCheckout = checkoutKind{name: "真实DOM仿造页", file: "checkout-stripe.html", real: true}
)

// eachCheckout 在两份仿造页上各跑一遍。
func eachCheckout(t *testing.T, run func(t *testing.T, k checkoutKind)) {
	t.Helper()
	for _, k := range []checkoutKind{oldCheckout, stripeCheckout} {
		t.Run(k.name, func(t *testing.T) {
			needChrome(t)
			run(t, k)
		})
	}
}

// fakeCheckout 是仿造的支付页：主页面由 main（127.0.0.1）提供，卡片 iframe 与 3DS 页由 card（localhost）提供。
// 两者跨站，同真实页面上 js.stripe.com 的 iframe 与支付页的关系。
type fakeCheckout struct {
	main, card *httptest.Server
	cardOrigin string

	mu            sync.Mutex
	session       string
	pays          []payRecord
	decoyClicks   atomic.Int32 // 卡片 iframe 里诱饵 submit 按钮被点击的次数
	promoClicks   atomic.Int32 // 主页面优惠码 Apply 按钮被点击的次数
	successVisits atomic.Int32 // 成功后跳转到 /success 的次数（真实 Stripe Checkout 成功靠跳转）
	submitClicks  atomic.Int32 // 真实DOM仿造页上点到支付按钮的次数，前端校验拦下、没发 /pay 时也计入
}

func newFakeCheckout(t *testing.T) *fakeCheckout {
	t.Helper()
	f := &fakeCheckout{}
	files := http.FileServer(http.Dir("testdata"))
	f.card = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/card.html", r.URL.Path == "/acs.html":
			files.ServeHTTP(w, r)
		case r.URL.Path == "/decoy" && r.Method == http.MethodPost:
			f.decoyClicks.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.card.Close)
	_, port, _ := net.SplitHostPort(f.card.Listener.Addr().String())
	f.cardOrigin = "http://localhost:" + port

	pages := map[string][]byte{}
	for _, k := range []checkoutKind{oldCheckout, stripeCheckout} {
		page, err := os.ReadFile(filepath.Join("testdata", k.file))
		if err != nil {
			t.Fatal(err)
		}
		pages["/"+k.file] = bytes.ReplaceAll(page, []byte("__CARD_ORIGIN__"), []byte(f.cardOrigin))
	}
	f.main = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, isPage := pages[r.URL.Path]
		switch {
		case isPage:
			if c, err := r.Cookie(sessionCookie); err == nil {
				f.mu.Lock()
				f.session = c.Value
				f.mu.Unlock()
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(page)
		case r.URL.Path == "/success":
			f.successVisits.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<!doctype html><title>Thanks</title><p>Thanks for your order</p>`))
		case r.URL.Path == "/pay" && r.Method == http.MethodPost:
			var rec payRecord
			if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.pays = append(f.pays, rec)
			f.mu.Unlock()
		case r.URL.Path == "/promo" && r.Method == http.MethodPost:
			f.promoClicks.Add(1)
		case r.URL.Path == "/submit-attempt" && r.Method == http.MethodPost:
			f.submitClicks.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.main.Close)
	return f
}

// url 返回旧仿造页带查询参数的地址。
func (f *fakeCheckout) url(query string) string { return f.urlOf(oldCheckout, query) }

// urlOf 返回指定仿造页带查询参数的地址。
func (f *fakeCheckout) urlOf(k checkoutKind, query string) string {
	u := f.main.URL + "/" + k.file
	if query != "" {
		u += "?" + query
	}
	return u
}

func (f *fakeCheckout) gotSession() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.session
}

// payments 返回仿造页服务端收到的提交记录。
func (f *fakeCheckout) payments() []payRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]payRecord(nil), f.pays...)
}

func openCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestOpenAndReadAmount(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		host, _, _ := net.SplitHostPort(f.main.Listener.Addr().String())
		b := launch(t, paybrowser.LaunchOptions{})
		ctx := openCtx(t)

		cookies := []paybrowser.Cookie{{Name: sessionCookie, Value: "sess-abc123", Domain: host}}
		if err := b.Open(ctx, f.urlOf(k, ""), cookies); err != nil {
			t.Fatalf("Open: %v", err)
		}
		if v := f.gotSession(); v != "sess-abc123" {
			t.Fatalf("服务端收到的会话 cookie = %q，期望 sess-abc123", v)
		}
		minor, text, err := b.ReadAmount(ctx, "PHP")
		if err != nil {
			t.Fatalf("ReadAmount: %v", err)
		}
		if minor != 112000 || text != "₱1,120.00" {
			t.Fatalf("ReadAmount = %d, %q，期望 112000, ₱1,120.00", minor, text)
		}
	})
}

// authProxy 是只做普通 HTTP 转发的最小代理：要求 Proxy-Authorization，只转发到本机（回环地址或 localhost）。
type authProxy struct {
	want       string
	challenged atomic.Int32 // 回过 407 的次数
	authorized atomic.Int32 // 凭据正确并转发的次数
	transport  http.RoundTripper
}

func newAuthProxy(user, pass string) *authProxy {
	return &authProxy{
		want:      "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)),
		transport: &http.Transport{},
	}
}

func (p *authProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Proxy-Authorization") != p.want {
		p.challenged.Add(1)
		w.Header().Set("Proxy-Authenticate", `Basic realm="test"`)
		w.WriteHeader(http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect || !r.URL.IsAbs() {
		http.Error(w, "测试代理只支持普通 HTTP 转发", http.StatusMethodNotAllowed)
		return
	}
	if ip := net.ParseIP(r.URL.Hostname()); r.URL.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
		http.Error(w, "测试代理只转发到回环地址", http.StatusForbidden)
		return
	}
	p.authorized.Add(1)
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Connection")
	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func TestProxyAuth(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	proxy := newAuthProxy("alice", "s3cret")
	ps := httptest.NewServer(proxy)
	t.Cleanup(ps.Close)
	pu, _ := url.Parse(ps.URL)
	pu.User = url.UserPassword("alice", "s3cret")

	b := launch(t, paybrowser.LoopbackProxy(paybrowser.LaunchOptions{ProxyURL: pu.String()}))
	if err := b.Open(openCtx(t), f.url(""), nil); err != nil {
		t.Fatalf("经代理 Open 失败: %v", err)
	}
	if proxy.challenged.Load() == 0 || proxy.authorized.Load() == 0 {
		t.Fatalf("代理认证流程不符：407 次数 %d，凭据正确的请求 %d", proxy.challenged.Load(), proxy.authorized.Load())
	}
}

const testEmail = "buyer@example.com"

var (
	testCard    = paybrowser.Card{Number: "4242424242424242", ExpMonth: "12", ExpYear: "30", CVC: "737"}
	testBilling = paybrowser.Billing{Name: "Jane Doe", Line1: "1 Main St", City: "Portland", State: "OR", Zip: "97201", Country: "US", Email: testEmail}
)

const outcomeWait = 15 * time.Second

// openCheckout 启动浏览器并打开带查询参数的旧仿造页。
func openCheckout(t *testing.T, f *fakeCheckout, query string, o paybrowser.LaunchOptions) (*paybrowser.Browser, context.Context) {
	t.Helper()
	return openKind(t, f, oldCheckout, query, o)
}

// openKind 启动浏览器并打开带查询参数的指定仿造页。
func openKind(t *testing.T, f *fakeCheckout, k checkoutKind, query string, o paybrowser.LaunchOptions) (*paybrowser.Browser, context.Context) {
	t.Helper()
	b := launch(t, o)
	ctx := openCtx(t)
	if err := b.Open(ctx, f.urlOf(k, query), nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return b, ctx
}

// waitPaid 等到付款成功的页面表现：旧页显示成功标志；真实页面没有成功标志，只会跳走，WaitOutcome 认不出来，返回 unknown。
func waitPaid(t *testing.T, f *fakeCheckout, b *paybrowser.Browser, ctx context.Context, k checkoutKind) {
	t.Helper()
	if !k.real {
		waitOutcome(t, b, ctx, paybrowser.OutcomeSuccess)
		return
	}
	deadline := time.Now().Add(outcomeWait)
	for f.successVisits.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("真实DOM仿造页付款后没有跳转到成功页")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got, msg, err := b.WaitOutcome(ctx, 500*time.Millisecond); err != nil || got != paybrowser.OutcomeUnknown {
		t.Fatalf("真实页面成功后没有成功标志，WaitOutcome 应为 unknown，实际 %q, %q, %v", got, msg, err)
	}
}

// fillAndSubmit 填卡并点击支付。
func fillAndSubmit(t *testing.T, b *paybrowser.Browser, ctx context.Context) {
	t.Helper()
	if err := b.Fill(ctx, testCard, testBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if err := b.Submit(ctx); err != nil {
		t.Fatalf("Submit: %v", err)
	}
}

func waitOutcome(t *testing.T, b *paybrowser.Browser, ctx context.Context, want paybrowser.Outcome) string {
	t.Helper()
	got, msg, err := b.WaitOutcome(ctx, outcomeWait)
	if err != nil || got != want {
		t.Fatalf("WaitOutcome = %q, %q, %v，期望 %q", got, msg, err, want)
	}
	return msg
}

// onlyPayment 断言仿造页服务端恰好收到一次提交并返回它。
func onlyPayment(t *testing.T, f *fakeCheckout) payRecord {
	t.Helper()
	pays := f.payments()
	if len(pays) != 1 {
		t.Fatalf("仿造页收到 %d 次提交，期望 1 次", len(pays))
	}
	return pays[0]
}

func TestFillSubmitSuccess(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, k, "mode=success", paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)
		waitPaid(t, f, b, ctx, k)

		got := onlyPayment(t, f)
		want := payRecord{
			Method: "new", Number: testCard.Number, Expiry: "1230", CVC: testCard.CVC, NumberKeys: len(testCard.Number),
			Name: testBilling.Name, Country: testBilling.Country, Line1: testBilling.Line1, City: testBilling.City,
			State: testBilling.State, Zip: testBilling.Zip,
		}
		if k.real {
			want.Email = testEmail // 真实页面邮箱必填且为空，填账号自己的邮箱；旧页非必填，不能填
		}
		if got != want {
			t.Fatalf("仿造页收到 %+v\n期望 %+v", got, want)
		}
	})
}

func TestDeclineReturnsMessage(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, k, "mode=decline", paybrowser.LaunchOptions{})

		// 还没提交：页面上没有任何结果，到时返回 unknown
		got, msg, err := b.WaitOutcome(ctx, 500*time.Millisecond)
		if err != nil || got != paybrowser.OutcomeUnknown || msg != "" {
			t.Fatalf("提交前 WaitOutcome = %q, %q, %v，期望 unknown", got, msg, err)
		}

		fillAndSubmit(t, b, ctx)
		msg = waitOutcome(t, b, ctx, paybrowser.OutcomeDeclined)
		if !strings.Contains(msg, "declined") {
			t.Fatalf("被拒消息 = %q，期望含 declined", msg)
		}
	})
}

func TestClassifyPageError(t *testing.T) {
	cases := map[string]paybrowser.Outcome{
		"Your card number is incomplete.":            paybrowser.OutcomeFormRejected,
		"Your card's security code is incomplete.":   paybrowser.OutcomeFormRejected,
		"Your postal code is incomplete.":            paybrowser.OutcomeFormRejected,
		"Your card's expiration date is incomplete.": paybrowser.OutcomeFormRejected,
		"Your card number is invalid.":               paybrowser.OutcomeFormRejected,
		"This field is required.":                    paybrowser.OutcomeFormRejected,
		"That card number is not valid.":             paybrowser.OutcomeFormRejected,
		"Please enter a valid address.":              paybrowser.OutcomeFormRejected,
		"Please fill in this field.":                 paybrowser.OutcomeFormRejected,
		"Your card's expiration year is in the past": paybrowser.OutcomeFormRejected,
		"YOUR POSTAL CODE IS INCOMPLETE.":            paybrowser.OutcomeFormRejected,

		"Your card was declined.":                                   paybrowser.OutcomeDeclined,
		"Your card has insufficient funds.":                         paybrowser.OutcomeDeclined,
		"Your card's security code is incorrect.":                   paybrowser.OutcomeDeclined,
		"Your card has been declined. Please try a different card.": paybrowser.OutcomeDeclined,
		"Your card has expired.":                                    paybrowser.OutcomeDeclined,
		"Do not honor":                                              paybrowser.OutcomeDeclined,
		"This card was reported lost.":                              paybrowser.OutcomeDeclined,
		"This card was reported stolen.":                            paybrowser.OutcomeDeclined,
		"Suspected fraud":                                           paybrowser.OutcomeDeclined,
		"We are unable to process your payment.":                    paybrowser.OutcomeDeclined,
		"Your payment could not be processed.":                      paybrowser.OutcomeDeclined,
		"YOUR CARD WAS DECLINED.":                                   paybrowser.OutcomeDeclined,
		// 同时含两类词时按被拒算：判断顺序不能反，真拒付不能被当成校验错误
		"Your card was declined: the postal code is invalid.": paybrowser.OutcomeDeclined,
		"Your card number is incorrect or invalid.":           paybrowser.OutcomeDeclined,
		// 认不出的文字不是结果（MO-02）。原来一律按被拒：只等 2 分钟就关浏览器，已扣款的单可能被定成失败，3DS 也走不下去
		"":                      paybrowser.OutcomeNotice,
		"Something went wrong.": paybrowser.OutcomeNotice,
		"Processing error":      paybrowser.OutcomeNotice,
		"您的银行卡被拒绝":              paybrowser.OutcomeNotice, // 词表没有核实过中文拒付文字，不猜
		// 过程提示：含 required 也不算校验错误
		"Authentication required": paybrowser.OutcomeNotice,
		"Verification required.":  paybrowser.OutcomeNotice,
		"Processing your payment": paybrowser.OutcomeNotice,
		"Please wait...":          paybrowser.OutcomeNotice,
		// 收紧的拒付词：这些都不是发卡行拒付
		"Connection lost.":         paybrowser.OutcomeNotice,
		"Your session has expired": paybrowser.OutcomeNotice,
	}
	for text, want := range cases {
		if got := paybrowser.ClassifyPageError(text); got != want {
			t.Errorf("ClassifyPageError(%q) = %q，期望 %q", text, got, want)
		}
	}
}

// checkoutQuery 把键值对编成查询串，值里可以有空格和撇号。
func checkoutQuery(kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return v.Encode()
}

// I-1：点击后才出现的提示按文字分类，不按选择器：真实拒付文字也可能出现在 .FieldError 里。全部命中元素都要看，不能只看第一个。
func TestPageErrorAfterClickClassified(t *testing.T) {
	const incomplete, declined = "Your postal code is incomplete.", "Your card was declined."
	cases := []struct {
		name  string
		query string
		want  paybrowser.Outcome
		text  string
	}{
		{"主页面校验错误", checkoutQuery("fielderror", incomplete), paybrowser.OutcomeFormRejected, incomplete},
		{"FieldError 里的真拒付", checkoutQuery("fielderror", declined), paybrowser.OutcomeDeclined, declined},
		{"第一条是校验错误第二条是拒付", checkoutQuery("fielderror", incomplete, "fielderror", declined), paybrowser.OutcomeDeclined, declined},
		{"第一条是拒付第二条是校验错误", checkoutQuery("fielderror", declined, "fielderror", incomplete), paybrowser.OutcomeDeclined, declined},
		{"两条校验错误取第一条", checkoutQuery("fielderror", incomplete, "fielderror", "Your card number is invalid."), paybrowser.OutcomeFormRejected, incomplete},
		{"卡片 iframe 里的校验错误", checkoutQuery("cardfielderror", incomplete), paybrowser.OutcomeFormRejected, incomplete},
		{"卡片 iframe 里的真拒付", checkoutQuery("cardfielderror", declined), paybrowser.OutcomeDeclined, declined},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			needChrome(t)
			f := newFakeCheckout(t)
			b, ctx := openCheckout(t, f, c.query, paybrowser.LaunchOptions{})
			fillAndSubmit(t, b, ctx)
			if msg := waitOutcome(t, b, ctx, c.want); msg != c.text {
				t.Fatalf("提示文字 = %q，期望 %q", msg, c.text)
			}
			if n := len(f.payments()); n != 0 {
				t.Fatalf("前端校验没过时仿造页不应收到提交: %d", n)
			}
		})
	}
}

// MO-02：认不出的文字只记下、继续等，之后的真结果照常识别。超时的话把它当说明返回，不当结果。
func TestUnrecognizedNoticeKeepsWaiting(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, k, checkoutQuery("notice", "Processing error", "noticedelay", "1500", "mode", "decline"), paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)

		got, msg, err := b.WaitOutcome(ctx, 800*time.Millisecond)
		if err != nil || got != paybrowser.OutcomeUnknown || msg != "Processing error" {
			t.Fatalf("WaitOutcome = %q, %q, %v，期望 unknown 并带上提示文字", got, msg, err)
		}
		if n := strings.Join(b.Notices(), "\n"); n != "Processing error" {
			t.Fatalf("Notices = %q", n)
		}
		if n := b.Notices(); len(n) != 0 {
			t.Fatalf("同一条提示只给一次: %q", n)
		}
		if msg := waitOutcome(t, b, ctx, paybrowser.OutcomeDeclined); msg != "Your card was declined." {
			t.Fatalf("被拒消息 = %q", msg)
		}
	})
}

// MO-02：认不出的提示之后才出现 3DS 框，要能等到；原来会判成被拒并关浏览器。
func TestNoticeThenChallenge(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, k, checkoutQuery("notice", "Something went wrong.", "noticedelay", "1000", "mode", "3ds"), paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)
		waitOutcome(t, b, ctx, paybrowser.OutcomeChallenge)
	})
}

// 3DS 框和提示同时在时先判 3DS。
func TestChallengeBeforeText(t *testing.T) {
	for _, notice := range []string{"Your card was declined.", "Your postal code is incomplete.", "Authentication required"} {
		t.Run(notice, func(t *testing.T) {
			needChrome(t)
			f := newFakeCheckout(t)
			b, ctx := openKind(t, f, stripeCheckout, checkoutQuery("notice", notice, "mode", "3ds"), paybrowser.LaunchOptions{})
			fillAndSubmit(t, b, ctx)
			waitOutcome(t, b, ctx, paybrowser.OutcomeChallenge)
		})
	}
}

// PF-5：可见的 hCaptcha（iframe 地址含 challenge）不是 3DS 框。原来的宽泛候选会把它当成 3DS，白等验证码。
func TestVisibleCaptchaIsNotChallenge(t *testing.T) {
	needChrome(t)
	for name, q := range map[string]string{
		"只有验证码框":    checkoutQuery("captcha", "1", "mode", "none"),
		"验证码框加提示文字": checkoutQuery("captcha", "1", "mode", "none", "notice", "Please try again."),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeCheckout(t)
			b, ctx := openKind(t, f, stripeCheckout, q, paybrowser.LaunchOptions{})
			fillAndSubmit(t, b, ctx)
			got, _, err := b.WaitOutcome(ctx, 3*time.Second)
			if err != nil || got != paybrowser.OutcomeUnknown {
				t.Fatalf("WaitOutcome = %q, %v，期望 unknown（不是 challenge）", got, err)
			}
			if _, err := b.Shot(ctx, "captcha", paybrowser.ShotChallenge); err == nil {
				t.Fatal("没有 3DS 框时不应截到验证框")
			}
		})
	}
}

// PF-5：去掉宽泛候选之后，真实 3DS 用的候选还在：按 name 识别、只按 src 里的 three-ds 识别，
// 以及页面上同时有人机验证框时仍认出真正的 3DS 框并能截图。
func TestRealChallengeCandidatesStillMatch(t *testing.T) {
	needChrome(t)
	for name, q := range map[string]string{
		"stripe-challenge-frame": checkoutQuery("mode", "3ds"),
		"src 含 three-ds":         checkoutQuery("mode", "3ds", "challengeby", "src"),
		"同时有人机验证框":               checkoutQuery("mode", "3ds", "captcha", "1"),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeCheckout(t)
			b, ctx := openKind(t, f, stripeCheckout, q, paybrowser.LaunchOptions{ShotDir: t.TempDir()})
			fillAndSubmit(t, b, ctx)
			waitOutcome(t, b, ctx, paybrowser.OutcomeChallenge)
			if _, err := b.Shot(ctx, "challenge", paybrowser.ShotChallenge); err != nil {
				t.Fatalf("应能截到 3DS 框: %v", err)
			}
		})
	}
}

// 拒付文字之后紧跟着出现 3DS 框：再看一会儿的窗口要能改判，否则浏览器一关验证就走不下去了。
func TestDeclineTextThenChallengeInSettleWindow(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.SetDeclineSettle(4 * time.Second))
	f := newFakeCheckout(t)
	b, ctx := openKind(t, f, stripeCheckout, checkoutQuery("notice", "Your card was declined.", "noticedelay", "1000", "mode", "3ds"), paybrowser.LaunchOptions{})
	fillAndSubmit(t, b, ctx)
	waitOutcome(t, b, ctx, paybrowser.OutcomeChallenge)
}

// 校验错误之后又出现拒付文字：按被拒，不能少算发卡行的拒付。
func TestFormErrorThenDeclineInSettleWindow(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.SetDeclineSettle(4 * time.Second))
	f := newFakeCheckout(t)
	b, ctx := openKind(t, f, stripeCheckout, checkoutQuery("notice", "Your postal code is incomplete.", "noticedelay", "500", "mode", "decline"), paybrowser.LaunchOptions{})
	fillAndSubmit(t, b, ctx)
	if msg := waitOutcome(t, b, ctx, paybrowser.OutcomeDeclined); msg != "Your card was declined." {
		t.Fatalf("被拒消息 = %q", msg)
	}
}

// 不认识的语言不算被拒。词表没有核实过中文拒付文字，猜错的代价是把已扣款的单定成失败。
func TestNonEnglishNoticeNotDeclined(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, ctx := openKind(t, f, stripeCheckout, checkoutQuery("notice", "您的银行卡被拒绝", "mode", "none"), paybrowser.LaunchOptions{})
	fillAndSubmit(t, b, ctx)
	got, msg, err := b.WaitOutcome(ctx, time.Second)
	if err != nil || got != paybrowser.OutcomeUnknown || msg != "您的银行卡被拒绝" {
		t.Fatalf("WaitOutcome = %q, %q, %v，期望 unknown", got, msg, err)
	}
}

// 提示文字进时间线之前先脱敏。
func TestNoticeMasked(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	const notice = "Contact buyer@example.com about 4242 4242 4242 4242"
	b, ctx := openKind(t, f, stripeCheckout, checkoutQuery("notice", notice, "mode", "none"), paybrowser.LaunchOptions{})
	fillAndSubmit(t, b, ctx)
	_, msg, err := b.WaitOutcome(ctx, time.Second)
	if err != nil || strings.Contains(msg, "buyer@") || strings.Contains(msg, "4242") {
		t.Fatalf("WaitOutcome 的说明未脱敏: %q, %v", msg, err)
	}
	if n := strings.Join(b.Notices(), "\n"); strings.Contains(n, "buyer@") || strings.Contains(n, "4242") || !strings.Contains(n, "***@***") {
		t.Fatalf("Notices 未脱敏: %q", n)
	}
}

func TestMaskPageText(t *testing.T) {
	cases := map[string]string{
		"a.b+c@mail.example.com 你好":  "***@*** 你好",
		"card 4242-4242 4242 4242 x": "card *** x",
		"tel 13800138000":            "tel ***",
		"₱1,120.00 due in 30 days":   "₱1,120.00 due in 30 days",
		"Order 123456":               "Order 123456",
	}
	for in, want := range cases {
		if got := paybrowser.MaskPageText(in); got != want {
			t.Errorf("MaskPageText(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// 真实DOM仿造页上，点了支付但前端校验拦下：form_rejected，没有发出扣款。
func TestFormRejectedOnStripePage(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	const incomplete = "Your postal code is incomplete."
	b, ctx := openKind(t, f, stripeCheckout, checkoutQuery("fielderror", incomplete), paybrowser.LaunchOptions{})
	fillAndSubmit(t, b, ctx)
	if msg := waitOutcome(t, b, ctx, paybrowser.OutcomeFormRejected); msg != incomplete {
		t.Fatalf("提示文字 = %q", msg)
	}
	time.Sleep(200 * time.Millisecond)
	if n, pays := f.submitClicks.Load(), len(f.payments()); n != 1 || pays != 0 {
		t.Fatalf("应点到 1 次、没有发 /pay，实际点了 %d 次，/pay %d 次", n, pays)
	}
}

// I-1：点击前已经显示的提示不是这次付款的结果，要忽略；之后真正出现的被拒文字仍要识别。
func TestExistingNoticeIgnored(t *testing.T) {
	const banner = "Prices are shown in your local currency."
	for name, key := range map[string]string{"主页面横幅": "banner", "卡片 iframe 里的提示": "cardbanner"} {
		t.Run(name, func(t *testing.T) {
			needChrome(t)
			f := newFakeCheckout(t)
			b, ctx := openCheckout(t, f, checkoutQuery(key, banner, "mode", "decline", "delay", "3000"), paybrowser.LaunchOptions{})
			fillAndSubmit(t, b, ctx)

			got, msg, err := b.WaitOutcome(ctx, 500*time.Millisecond)
			if err != nil || got != paybrowser.OutcomeUnknown || msg != "" {
				t.Fatalf("只有点击前的提示时 WaitOutcome = %q, %q, %v，期望 unknown", got, msg, err)
			}
			msg = waitOutcome(t, b, ctx, paybrowser.OutcomeDeclined)
			if msg != "Your card was declined." {
				t.Fatalf("被拒消息 = %q，不应带上点击前的提示", msg)
			}
		})
	}
}

// 点击前的提示不能挡住后面的判断：横幅一直在，3DS 框仍要识别。
func TestExistingNoticeDoesNotHideChallenge(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, k, checkoutQuery("banner", "Prices are shown in your local currency.", "mode", "3ds"), paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)
		waitOutcome(t, b, ctx, paybrowser.OutcomeChallenge)
	})
}

// 判断「点击前已有」按文字完全相同：点击前后文字一样时算旧提示，单据走结果不明的确认流程，这是安全方向。
func TestExistingNoticeSameTextIgnored(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, ctx := openCheckout(t, f, checkoutQuery("banner", "Your card was declined.", "mode", "decline"), paybrowser.LaunchOptions{})
	fillAndSubmit(t, b, ctx)
	got, msg, err := b.WaitOutcome(ctx, 1500*time.Millisecond)
	if err != nil || got != paybrowser.OutcomeUnknown || msg != "" {
		t.Fatalf("文字和点击前相同时 WaitOutcome = %q, %q, %v，期望 unknown", got, msg, err)
	}
	onlyPayment(t, f)
}

// 收集点击前的提示有短超时，页面卡死时当作没有提示，绝不挡住提交；Submit 本身不会因此报错（收集函数不返回错误）。
func TestScanBeforeClickBoundedOnBusyPage(t *testing.T) {
	needChrome(t)
	started := make(chan struct{})
	var once sync.Once
	page := amountDiv + `<div role="alert">Prices are shown in your local currency.</div>` +
		laterScript(`fetch('/started').finally(function () { while (true) {} })`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/started" {
			once.Do(func() { close(started) })
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)
	b := launch(t, paybrowser.LaunchOptions{})
	ctx := openCtx(t)
	if err := b.Open(ctx, srv.URL, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}

	// 页面还正常时能收到提示
	got, err := paybrowser.ScanBeforeClick(ctx, b)
	if err != nil || !got["Prices are shown in your local currency."] {
		t.Fatalf("正常页面应收到点击前的提示: %v, %v", got, err)
	}

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("页面脚本没有开始执行")
	}
	time.Sleep(300 * time.Millisecond) // 等请求收尾，脚本进入死循环

	begin := time.Now()
	got, err = paybrowser.ScanBeforeClick(ctx, b)
	if err != nil {
		t.Fatalf("收集失败不应报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("页面卡死时应当作空集: %v", got)
	}
	if d := time.Since(begin); d > 5*time.Second {
		t.Fatalf("收集用了 %v，超时太长会耽误点击", d)
	}
}

func TestChallengeThenOTP(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, k, "mode=3ds", paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)
		waitOutcome(t, b, ctx, paybrowser.OutcomeChallenge)
		if err := b.EnterOTP(ctx, "123456"); err != nil {
			t.Fatalf("EnterOTP: %v", err)
		}
		waitPaid(t, f, b, ctx, k)
	})
}

func TestWrongOTPDeclined(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, k, "mode=3ds", paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)
		waitOutcome(t, b, ctx, paybrowser.OutcomeChallenge)
		if err := b.EnterOTP(ctx, "000000"); err != nil {
			t.Fatalf("EnterOTP: %v", err)
		}
		waitOutcome(t, b, ctx, paybrowser.OutcomeDeclined)
	})
}

func TestOTPRetryAfterFailedSubmitClick(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, ctx := openCheckout(t, f, "mode=3ds&otpflaky=1", paybrowser.LaunchOptions{})
	fillAndSubmit(t, b, ctx)
	waitOutcome(t, b, ctx, paybrowser.OutcomeChallenge)

	// 第一次：验证码键完时提交按钮消失，点击失败
	if err := b.EnterOTP(ctx, "123456"); err == nil {
		t.Fatal("提交按钮点击失败时 EnterOTP 应报错")
	}
	// 没提交成功就不算已答：同一个验证框仍报为待验证
	waitOutcome(t, b, ctx, paybrowser.OutcomeChallenge)

	time.Sleep(2 * time.Second) // 等验证页恢复提交按钮
	if err := b.EnterOTP(ctx, "123456"); err != nil {
		t.Fatalf("重新 EnterOTP: %v", err)
	}
	waitOutcome(t, b, ctx, paybrowser.OutcomeSuccess)
}

func TestSubmitTwiceRefused(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, k, "mode=decline", paybrowser.LaunchOptions{})

		// 没填卡就提交：拒绝，以免用账号已存的卡付款
		if err := b.Submit(ctx); err == nil || errors.Is(err, paybrowser.ErrAlreadySubmitted) {
			t.Fatalf("未填卡时 Submit = %v，期望报「尚未填卡」", err)
		}

		fillAndSubmit(t, b, ctx)
		waitOutcome(t, b, ctx, paybrowser.OutcomeDeclined) // 被拒后支付按钮仍可点
		if err := b.Submit(ctx); !errors.Is(err, paybrowser.ErrAlreadySubmitted) {
			t.Fatalf("第二次 Submit = %v，期望 ErrAlreadySubmitted", err)
		}
		time.Sleep(500 * time.Millisecond)
		onlyPayment(t, f)
	})
}

func TestFillReportsMissingField(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, k, "nocvc=1", paybrowser.LaunchOptions{ShotDir: t.TempDir()})
		err := b.Fill(ctx, testCard, testBilling)
		if err == nil || !strings.Contains(err.Error(), "CVC") {
			t.Fatalf("Fill = %v，期望报错并含「CVC」", err)
		}
		if strings.Contains(err.Error(), testCard.Number) || strings.Contains(err.Error(), testCard.CVC) {
			t.Fatalf("错误消息泄露了卡号或 CVC: %v", err)
		}
		// 卡号已经键入页面：即使 Fill 失败也不许整页截图
		if _, err := b.Shot(ctx, "after-failed-fill", paybrowser.ShotPage); !errors.Is(err, paybrowser.ErrShotForbidden) {
			t.Fatalf("Fill 失败后 Shot(ShotPage) = %v，期望 ErrShotForbidden", err)
		}
		// 没填成功不能提交
		if err := b.Submit(ctx); err == nil {
			t.Fatal("Fill 失败后 Submit 应报错")
		}
		if n := len(f.payments()); n != 0 {
			t.Fatalf("仿造页收到 %d 次提交，期望 0 次", n)
		}
	})
}

// readPNG 读取截图文件并返回尺寸。
func readPNG(t *testing.T, dir, name string) image.Point {
	t.Helper()
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".png") {
		t.Fatalf("Shot 返回的文件名 %q 应是不带目录的 .png 文件名", name)
	}
	fh, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("截图文件不存在: %v", err)
	}
	defer fh.Close()
	img, err := png.Decode(fh)
	if err != nil {
		t.Fatalf("截图不是有效的 PNG: %v", err)
	}
	return img.Bounds().Size()
}

func TestShotRules(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	dir := t.TempDir()
	b, ctx := openCheckout(t, f, "mode=3ds", paybrowser.LaunchOptions{ShotDir: dir})

	if _, err := b.Shot(ctx, "../escape", paybrowser.ShotPage); err == nil {
		t.Fatal("带路径的截图名应报错")
	}
	if _, err := b.Shot(ctx, "no-challenge", paybrowser.ShotChallenge); err == nil {
		t.Fatal("页面上没有 3DS 框时 Shot(ShotChallenge) 应报错")
	}
	name, err := b.Shot(ctx, "before-fill", paybrowser.ShotPage)
	if err != nil {
		t.Fatalf("Fill 前 Shot(ShotPage): %v", err)
	}
	if size := readPNG(t, dir, name); size.X < 800 {
		t.Fatalf("整页截图尺寸 %v 过小", size)
	}

	fillAndSubmit(t, b, ctx)
	if _, err := b.Shot(ctx, "after-fill", paybrowser.ShotPage); !errors.Is(err, paybrowser.ErrShotForbidden) {
		t.Fatalf("Fill 后 Shot(ShotPage) = %v，期望 ErrShotForbidden", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "after-fill.png")); !os.IsNotExist(err) {
		t.Fatal("被拒绝的截图不应写出文件")
	}

	waitOutcome(t, b, ctx, paybrowser.OutcomeChallenge)
	name, err = b.Shot(ctx, "challenge", paybrowser.ShotChallenge)
	if err != nil {
		t.Fatalf("Shot(ShotChallenge): %v", err)
	}
	// 只截 3DS 框（420×280，宽高比 3:2），不是整页
	size := readPNG(t, dir, name)
	if size.X >= 1000 || size.X*2 < size.Y*3-6 || size.X*2 > size.Y*3+6 {
		t.Fatalf("3DS 截图尺寸 %v，期望只含 420×280 的验证框", size)
	}
}

func TestSavedCardChoosesNew(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, ctx := openCheckout(t, f, "saved=1&mode=success", paybrowser.LaunchOptions{})
	fillAndSubmit(t, b, ctx)
	waitOutcome(t, b, ctx, paybrowser.OutcomeSuccess)
	got := onlyPayment(t, f)
	if got.Method != "new" || got.Number != testCard.Number || got.CVC != testCard.CVC {
		t.Fatalf("仿造页收到 method=%q，卡号与 CVC 是否一致：%v、%v；期望用新卡", got.Method, got.Number == testCard.Number, got.CVC == testCard.CVC)
	}
}

func TestFillRetypesRewrittenText(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, ctx := openCheckout(t, f, "mangle=1&mode=success", paybrowser.LaunchOptions{})
	fillAndSubmit(t, b, ctx)
	waitOutcome(t, b, ctx, paybrowser.OutcomeSuccess)
	if got := onlyPayment(t, f).City; got != testBilling.City {
		t.Fatalf("城市 = %q，期望核对不符后逐字重打为 %q", got, testBilling.City)
	}
}

func TestFindPrefersEarlierSelectorAcrossFrames(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	// 主页面的 input[name="number"] 只命中卡号的第二个选择器，卡片 iframe 里的 #payment-numberInput 命中第一个
	b, ctx := openCheckout(t, f, "decoy=1&mode=success", paybrowser.LaunchOptions{})
	fillAndSubmit(t, b, ctx)
	waitOutcome(t, b, ctx, paybrowser.OutcomeSuccess)
	got := onlyPayment(t, f)
	if got.Number != testCard.Number || got.DecoyNumber != "" {
		t.Fatalf("卡号进了卡片 iframe：%v；主页面诱饵框为空：%v", got.Number == testCard.Number, got.DecoyNumber == "")
	}
}

func TestSubmitClicksMainPayButton(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		// 主页面支付按钮在填完后 1.5 秒才出现；iframe 里一直有个可见的 submit 按钮
		b, ctx := openKind(t, f, k, "decoy=1&latepay=1&mode=success", paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)
		waitPaid(t, f, b, ctx, k)
		onlyPayment(t, f)
		if n := f.decoyClicks.Load(); n != 0 {
			t.Fatalf("iframe 里的 submit 按钮被点了 %d 次，期望 0 次", n)
		}
	})
}

// M-2：主页面上排在前面的优惠码 Apply 也是 submit 按钮，点到它就白白用掉唯一一次提交。
func TestSubmitPrefersSubmitButtonOverPromoApply(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, k, "promo=1&mode=success", paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)
		waitPaid(t, f, b, ctx, k)
		onlyPayment(t, f)
		if n := f.promoClicks.Load(); n != 0 {
			t.Fatalf("优惠码按钮被点了 %d 次，期望 0 次", n)
		}
	})
}

// 按钮上的 SubmitButton--complete 不算付款成功：误判会立刻关掉浏览器，3DS 就走不下去了。
func TestFormCompleteClassIsNotSuccess(t *testing.T) {
	eachCheckout(t, func(t *testing.T, k checkoutKind) {
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, k, "formcomplete=1&mode=3ds", paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)
		waitOutcome(t, b, ctx, paybrowser.OutcomeChallenge)
	})
}

func TestFillRejectsBadCard(t *testing.T) {
	b := &paybrowser.Browser{} // 校验在碰浏览器之前完成
	for _, c := range []paybrowser.Card{
		{Number: "4242 4242", ExpMonth: "12", ExpYear: "30", CVC: "737"},
		{Number: "4242424242424242", ExpMonth: "1", ExpYear: "30", CVC: "737"},
		{Number: "4242424242424242", ExpMonth: "12", ExpYear: "2030", CVC: "737"},
		{Number: "4242424242424242", ExpMonth: "12", ExpYear: "30", CVC: "7a7"},
	} {
		err := b.Fill(context.Background(), c, testBilling)
		if err == nil {
			t.Errorf("Fill(%+v) 应报格式错误", c)
			continue
		}
		if strings.Contains(err.Error(), c.Number) || strings.Contains(err.Error(), c.CVC) {
			t.Errorf("错误消息泄露了卡号或 CVC: %v", err)
		}
	}
}

func TestTruncateMessage(t *testing.T) {
	long := strings.Repeat("拒", 600)
	if got := paybrowser.Truncate(long); utf8.RuneCountInString(got) != 500 {
		t.Fatalf("截断后 %d 字，期望 500", utf8.RuneCountInString(got))
	}
	if got := paybrowser.Truncate("  Your card was declined.\n "); got != "Your card was declined." {
		t.Fatalf("Truncate = %q", got)
	}
}
