package paybrowser_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

const (
	embeddedPath = "/checkout/openai_llc/cs_test_embedded"
	cfPath       = "/checkout/openai_llc/cs_test_cf"
)

// fakeEmbedded 是仿造的 chatgpt.com 内嵌结账页：主页面由 main（127.0.0.1）提供，
// Stripe Payment Element 的卡片 iframe 与 3DS 页由 card（localhost）提供，两者跨站，同真实页面与 js.stripe.com 的关系。
// 页面的查询参数见 testdata/checkout-embedded.html 与 testdata/card-embedded.html 开头的说明。
type fakeEmbedded struct {
	main, card *httptest.Server

	mu      sync.Mutex
	session string
	pays    []payRecord
	done    atomic.Int32 // 跳到 /done（离开结账页）的次数
}

func newFakeEmbedded(t *testing.T) *fakeEmbedded {
	t.Helper()
	f := &fakeEmbedded{}
	files := http.FileServer(http.Dir("testdata"))
	f.card = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/card-embedded.html", "/acs.html":
			files.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.card.Close)
	_, port, _ := net.SplitHostPort(f.card.Listener.Addr().String())
	cardOrigin := "http://localhost:" + port

	page, err := os.ReadFile(filepath.Join("testdata", "checkout-embedded.html"))
	if err != nil {
		t.Fatal(err)
	}
	page = bytes.ReplaceAll(page, []byte("__CARD_ORIGIN__"), []byte(cardOrigin))
	cf, err := os.ReadFile(filepath.Join("testdata", "cf-challenge.html"))
	if err != nil {
		t.Fatal(err)
	}
	f.main = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == embeddedPath:
			if c, err := r.Cookie(sessionCookie); err == nil {
				f.mu.Lock()
				f.session = c.Value
				f.mu.Unlock()
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(page)
		case r.URL.Path == cfPath:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(cf)
		case r.URL.Path == "/pay" && r.Method == http.MethodPost:
			var rec payRecord
			if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.pays = append(f.pays, rec)
			f.mu.Unlock()
		case r.URL.Path == "/done":
			f.done.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<!doctype html><title>ChatGPT</title><p>Welcome to ChatGPT Pro</p>`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.main.Close)
	return f
}

// url 返回内嵌结账页带查询参数的地址。
func (f *fakeEmbedded) url(query string) string {
	u := f.main.URL + embeddedPath
	if query != "" {
		u += "?" + query
	}
	return u
}

// cfURL 返回仿造的 Cloudflare 挑战页地址（路径同样在 /checkout/ 下）。
func (f *fakeEmbedded) cfURL() string { return f.main.URL + cfPath }

func (f *fakeEmbedded) gotSession() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.session
}

// payments 返回仿造页服务端收到的提交记录。
func (f *fakeEmbedded) payments() []payRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]payRecord(nil), f.pays...)
}

// doneVisits 返回跳到 /done 的次数。
func (f *fakeEmbedded) doneVisits() int32 { return f.done.Load() }

// openEmbedded 启动浏览器并以内嵌页打开带查询参数的仿造页。
func openEmbedded(t *testing.T, f *fakeEmbedded, query string) (*paybrowser.Browser, context.Context) {
	t.Helper()
	b := launch(t, paybrowser.LaunchOptions{})
	ctx := openCtx(t)
	if err := b.Open(ctx, f.url(query), paybrowser.PageEmbedded, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return b, ctx
}

// embeddedBilling 是内嵌页用例的账单地址；内嵌页没有邮箱框，不给邮箱。
var embeddedBilling = paybrowser.Billing{Name: "A B", Line1: "1 Main St", City: "Portland", State: "OR", Zip: "97201", Country: "US"}

// fieldLabels 返回填表报告里各字段的中文名，并要求它们都已核对一致。
func fieldLabels(t *testing.T, r paybrowser.FillReport) []string {
	t.Helper()
	var out []string
	for _, f := range r.Fields {
		if f.Status != "核对一致" {
			t.Errorf("字段「%s」的状态是 %q，期望核对一致", f.Label, f.Status)
		}
		out = append(out, f.Label)
	}
	return out
}

func TestEmbeddedOpenAndReadAmount(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	host, _, _ := net.SplitHostPort(f.main.Listener.Addr().String())
	b := launch(t, paybrowser.LaunchOptions{})
	ctx := openCtx(t)

	cookies := []paybrowser.Cookie{{Name: sessionCookie, Value: "sess-abc123", Domain: host}}
	if err := b.Open(ctx, f.url(""), paybrowser.PageEmbedded, cookies); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if v := f.gotSession(); v != "sess-abc123" {
		t.Fatalf("服务端收到的会话 cookie = %q，期望 sess-abc123", v)
	}
	minor, text, err := b.ReadAmount(ctx, "USD")
	if err != nil {
		t.Fatalf("ReadAmount: %v", err)
	}
	if minor != 10000 || text != "100.00 USD" {
		t.Fatalf("ReadAmount = %d, %q，期望 10000, \"100.00 USD\"", minor, text)
	}
	if !b.WaitRendered(ctx, 10*time.Second) {
		t.Fatal("WaitRendered 应在卡号框可见后返回 true")
	}
}

func TestEmbeddedReadAmountPHP(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "amount=%E2%82%B11,120.00&currency=php")

	minor, text, err := b.ReadAmount(ctx, "PHP")
	if err != nil {
		t.Fatalf("ReadAmount: %v", err)
	}
	if minor != 112000 || text != "1,120.00 PHP" {
		t.Fatalf("ReadAmount = %d, %q，期望 112000, \"1,120.00 PHP\"", minor, text)
	}
	if paybrowser.CurrencyMatches(text, "USD") {
		t.Fatalf("%q 不应被认成 USD", text)
	}
	if !paybrowser.CurrencyMatches(text, "PHP") {
		t.Fatalf("%q 应被认成 PHP", text)
	}
}

// Stripe 改了读屏文字、没有 Currency 时，Open 照样成功，ReadAmount 必须报错，不能猜币种（Review Focus 3）。
func TestEmbeddedAmountWithoutCurrency(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "nocurrency=1")

	minor, text, err := b.ReadAmount(ctx, "USD")
	if err == nil {
		t.Fatalf("没有 Currency 时 ReadAmount 应报错，得到 %d, %q", minor, text)
	}
	if minor != 0 {
		t.Fatalf("报错时 minor = %d，期望 0", minor)
	}
	if !strings.Contains(err.Error(), "币种") {
		t.Fatalf("错误应说明读不到币种，得到 %v", err)
	}
}

func TestEmbeddedOpenRequiresSingleButton(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.SetPageLoadTimeout(3 * time.Second))
	f := newFakeEmbedded(t)
	for _, c := range []struct{ query, want string }{
		{"twobuttons=1", "找到 2 个"},
		{"nobutton=1", "找到 0 个"},
	} {
		t.Run(c.query, func(t *testing.T) {
			b := launch(t, paybrowser.LaunchOptions{})
			err := b.Open(openCtx(t), f.url(c.query), paybrowser.PageEmbedded, nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Open 应报错且含 %q，得到 %v", c.want, err)
			}
			if errors.Is(err, paybrowser.ErrCFChallenge) {
				t.Fatalf("按钮不唯一不是 Cloudflare 挑战：%v", err)
			}
		})
	}
}

func TestEmbeddedOpenNoAmountText(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.SetPageLoadTimeout(3 * time.Second))
	f := newFakeEmbedded(t)
	b := launch(t, paybrowser.LaunchOptions{})
	err := b.Open(openCtx(t), f.url("noamount=1"), paybrowser.PageEmbedded, nil)
	if err == nil || !strings.Contains(err.Error(), "Payment amount") {
		t.Fatalf("没有读屏金额时 Open 应报错并说明，得到 %v", err)
	}
	if errors.Is(err, paybrowser.ErrCFChallenge) {
		t.Fatalf("读不到金额不是 Cloudflare 挑战：%v", err)
	}
	// 只缺读屏金额：卡片 iframe 整个没加载时也会缺金额，那种情况不能让这个用例误通过
	if strings.Contains(err.Error(), "卡号框") || strings.Contains(err.Error(), "支付按钮") {
		t.Fatalf("只应缺 Payment amount，卡号框和支付按钮都该找到，得到 %v", err)
	}
}

func TestEmbeddedOpenCloudflare(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.SetPageLoadTimeout(3 * time.Second))
	f := newFakeEmbedded(t)
	b := launch(t, paybrowser.LaunchOptions{})
	err := b.Open(openCtx(t), f.cfURL(), paybrowser.PageEmbedded, nil)
	if !errors.Is(err, paybrowser.ErrCFChallenge) {
		t.Fatalf("Cloudflare 挑战页应返回 ErrCFChallenge，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "Just a moment...") {
		t.Fatalf("错误应带上挑战页标题，得到 %v", err)
	}
}

// 引擎给 Open 的时限和 pageLoadTimeout 一样长，超时往往是调用方的时限先到；这时也要认出 Cloudflare 挑战页。
func TestEmbeddedOpenCloudflareCallerDeadline(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b := launch(t, paybrowser.LaunchOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := b.Open(ctx, f.cfURL(), paybrowser.PageEmbedded, nil); !errors.Is(err, paybrowser.ErrCFChallenge) {
		t.Fatalf("调用方时限先到时也应返回 ErrCFChallenge，得到 %v", err)
	}
}

// 调用方主动取消（不是超时）时立即返回取消错误，不再读页面判断 Cloudflare。
func TestEmbeddedOpenCanceled(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.SetPageLoadTimeout(30 * time.Second))
	f := newFakeEmbedded(t)
	b := launch(t, paybrowser.LaunchOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceledAt := make(chan time.Time, 1)
	time.AfterFunc(time.Second, func() {
		canceledAt <- time.Now()
		cancel()
	})
	err := b.Open(ctx, f.cfURL(), paybrowser.PageEmbedded, nil)
	took := time.Since(<-canceledAt)
	if errors.Is(err, paybrowser.ErrCFChallenge) {
		t.Fatalf("取消时不应判成 Cloudflare 挑战：%v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消时应返回 context.Canceled，得到 %v", err)
	}
	if took > 2*time.Second {
		t.Fatalf("取消后 %v 才返回，应在 2 秒内", took)
	}
}

// 规范化只认 Currency 后面紧跟的三个字母：缺 Currency、币种代码后面还连着字母都报错，不猜币种。
// 金额部分只能有一段数字：夹着税费之类的第二个金额时报错，不能挑一段去比最低金额。
func TestNormalizeEmbeddedAmount(t *testing.T) {
	ok := []struct{ raw, want string }{
		{"Payment amount: $100.00 Currency: usd Payment mode: Live mode. Test data not accepted.", "100.00 USD"},
		{"Payment amount: ₱1,120.00 Currency: php", "1,120.00 PHP"},
		{"Payment amount: 1,120.00 Currency: php", "1,120.00 PHP"},
		{"Payment amount: US$100.00 Currency: usd", "100.00 USD"},
		{"Payment amount: US$ 200.00 Currency: USD", "200.00 USD"},
	}
	for _, c := range ok {
		got, err := paybrowser.NormalizeEmbeddedAmount(c.raw)
		if err != nil || got != c.want {
			t.Errorf("NormalizeEmbeddedAmount(%q) = %q, %v，期望 %q", c.raw, got, err, c.want)
		}
	}
	for _, raw := range []string{
		"Payment amount: $100.00 Payment mode: Live mode. Test data not accepted.",
		"Payment amount: $100.00 Currency: usdt",
		"Payment amount: free Currency: usd",
		"Currency: usd",
	} {
		if got, err := paybrowser.NormalizeEmbeddedAmount(raw); err == nil {
			t.Errorf("NormalizeEmbeddedAmount(%q) 应报错，得到 %q", raw, got)
		}
	}
	for _, raw := range []string{
		"Payment amount: $0.00 Tax: $20.00 Currency: usd",
		"Payment amount: $100.00 $20.00 Currency: usd",
	} {
		got, err := paybrowser.NormalizeEmbeddedAmount(raw)
		if err == nil || !strings.Contains(err.Error(), "Payment amount 里有多个金额") {
			t.Errorf("NormalizeEmbeddedAmount(%q) 应报「Payment amount 里有多个金额」，得到 %q, %v", raw, got, err)
		}
	}
}

// 内嵌页上没有地址字段（设计 §2 的摸底结果）：卡片三项照填，六个地址字段都记成「页面没有」，不报错。
func TestEmbeddedFillWithoutAddress(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	r := b.FillReport()
	if want := []string{"国家", "姓名", "街道地址", "城市", "州/省", "邮编"}; !slices.Equal(r.Absent, want) {
		t.Fatalf("Absent = %v，期望 %v", r.Absent, want)
	}
	if got, want := fieldLabels(t, r), []string{"卡号", "有效期", "CVC"}; !slices.Equal(got, want) {
		t.Fatalf("填过的字段 = %v，期望 %v", got, want)
	}
	lines := strings.Join(r.Lines(), "\n")
	for _, want := range []string{"页面没有的字段：国家、姓名、街道地址、城市、州/省、邮编", "手动地址：未点击"} {
		if !strings.Contains(lines, want) {
			t.Errorf("报告缺少 %q：\n%s", want, lines)
		}
	}
	if st := b.PayButtonState(ctx); !strings.HasPrefix(st, "可点击（等了 ") {
		t.Fatalf("支付按钮状态 = %q，期望「可点击（等了 N 秒）」", st)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只填表不提交：仿造页收到 %d 次提交", n)
	}
}

// 填完卡号后出现国家和邮编时照常填写并核对，只有没出现的记进 Absent。
func TestEmbeddedFillWithAddress(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=1")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	r := b.FillReport()
	if want := []string{"姓名", "街道地址", "城市", "州/省"}; !slices.Equal(r.Absent, want) {
		t.Fatalf("Absent = %v，期望 %v", r.Absent, want)
	}
	if got, want := fieldLabels(t, r), []string{"卡号", "有效期", "CVC", "国家", "邮编"}; !slices.Equal(got, want) {
		t.Fatalf("填过的字段 = %v，期望 %v", got, want)
	}
	for sel, want := range map[string]string{"#payment-countryInput": "US", "#payment-postalCodeInput": "97201"} {
		got, err := paybrowser.FieldValue(ctx, b, sel)
		if err != nil || got != want {
			t.Fatalf("页面上 %s = %q, %v，期望 %q", sel, got, err, want)
		}
	}
}

// 真实页面（2026-11-04 实测）：城市、州、邮编在 DOM 里但折叠不可见，要点「Enter address manually」展开后再填。
func TestEmbeddedFillStripeAddressRevealsHidden(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=stripe")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	r := b.FillReport()
	if len(r.Absent) != 0 {
		t.Fatalf("Absent = %v，期望为空", r.Absent)
	}
	want := []string{"卡号", "有效期", "CVC", "国家", "姓名", "街道地址", "城市", "州/省", "邮编"}
	if got := fieldLabels(t, r); !slices.Equal(got, want) {
		t.Fatalf("填过的字段 = %v，期望 %v", got, want)
	}
	if lines := strings.Join(r.Lines(), "\n"); !strings.Contains(lines, "手动地址：已点击 1 次") {
		t.Errorf("报告缺少「手动地址：已点击 1 次」：\n%s", lines)
	}
	for sel, want := range map[string]string{
		"#billingAddress-localityInput":           "Portland",
		"#billingAddress-administrativeAreaInput": "OR",
		"#billingAddress-postalCodeInput":         "97201",
		"#billingAddress-addressLine1Input":       "1 Main St",
		"#billingAddress-countryInput":            "US",
	} {
		got, err := paybrowser.FieldValue(ctx, b, sel)
		if err != nil || got != want {
			t.Fatalf("页面上 %s = %q, %v，期望 %q", sel, got, err, want)
		}
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只填表不提交：仿造页收到 %d 次提交", n)
	}
}

// 折叠的地址字段展不开时报错放弃，不能当成「页面没有」放过去。
// 这个变体既没有手动地址入口也没有地址联想：走「选地址联想展开」，联想一直不出现。
func TestEmbeddedFillStripeAddressHiddenFails(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=stripe-nolink")

	err := b.Fill(ctx, testCard, embeddedBilling)
	if err == nil || !strings.Contains(err.Error(), "地址联想没有出现") {
		t.Fatalf("Fill = %v，期望报地址联想没有出现", err)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("Fill 失败时仿造页收到 %d 次提交", n)
	}
}

// 真实页面（2026-11-04 实测）没有手动地址入口：在街道里逐字键入，点一条地址联想，城市、州、邮编才展开。
// 联想会改写街道、城市、州、邮编，之后用账单地址自己的值覆盖。
func TestEmbeddedFillPicksSuggestion(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=suggest")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	r := b.FillReport()
	if len(r.Absent) != 0 {
		t.Fatalf("Absent = %v，期望为空", r.Absent)
	}
	want := []string{"卡号", "有效期", "CVC", "国家", "姓名", "街道地址", "城市", "州/省", "邮编"}
	if got := fieldLabels(t, r); !slices.Equal(got, want) {
		t.Fatalf("填过的字段 = %v，期望 %v", got, want)
	}
	lines := strings.Join(r.Lines(), "\n")
	if !strings.Contains(lines, "手动地址：已选地址联想：第 1 个 iframe 的选项") {
		t.Errorf("报告缺少「手动地址：已选地址联想：第 1 个 iframe 的选项」：\n%s", lines)
	}
	if strings.Contains(lines, "重打") {
		t.Errorf("第一次键入就出了联想，不该重打街道：\n%s", lines)
	}
	for sel, want := range map[string]string{
		"#billingAddress-addressLine1Input":       "1 Main St",
		"#billingAddress-localityInput":           "Portland",
		"#billingAddress-administrativeAreaInput": "OR",
		"#billingAddress-postalCodeInput":         "97201",
		"#billingAddress-countryInput":            "US",
	} {
		got, err := paybrowser.FieldValue(ctx, b, sel)
		if err != nil || got != want {
			t.Fatalf("页面上 %s = %q, %v，期望 %q", sel, got, err, want)
		}
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只填表不提交：仿造页收到 %d 次提交", n)
	}
}

// 键入街道后地址联想一直不出现：城市、州、邮编展不开，报错放弃。
func TestEmbeddedFillSuggestionMissing(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=suggest-none")

	err := b.Fill(ctx, testCard, embeddedBilling)
	if err == nil || !strings.Contains(err.Error(), "地址联想没有出现") || !strings.Contains(err.Error(), "已重打 1 次街道") {
		t.Fatalf("Fill = %v，期望报地址联想没有出现、已重打 1 次街道", err)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("Fill 失败时仿造页收到 %d 次提交", n)
	}
}

// 点了地址联想，城市、州、邮编还是没展开：报错放弃。
func TestEmbeddedFillSuggestionDoesNotExpand(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=suggest-stuck")

	err := b.Fill(ctx, testCard, embeddedBilling)
	if err == nil || !strings.Contains(err.Error(), "仍未展开") {
		t.Fatalf("Fill = %v，期望报仍未展开", err)
	}
	const want = "手动地址：已点地址联想（未展开）：第 1 个 iframe 的选项"
	if lines := strings.Join(b.FillReport().Lines(), "\n"); !strings.Contains(lines, want) {
		t.Errorf("报告缺少「%s」：\n%s", want, lines)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("Fill 失败时仿造页收到 %d 次提交", n)
	}
}

// 第一个 frame 里的联想点了没展开（真实页面上地址 iframe 里也有一份选项）：再点下一个 frame（下拉框）里的，最多点两次。
func TestEmbeddedFillSuggestionSecondFrame(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=suggest-decoy")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	r := b.FillReport()
	const want = "手动地址：已选地址联想：第 1、2 个 iframe 的选项"
	if lines := strings.Join(r.Lines(), "\n"); !strings.Contains(lines, want) {
		t.Errorf("报告缺少「%s」：\n%s", want, lines)
	}
	wantFields := []string{"卡号", "有效期", "CVC", "国家", "姓名", "街道地址", "城市", "州/省", "邮编"}
	if got := fieldLabels(t, r); !slices.Equal(got, wantFields) {
		t.Fatalf("填过的字段 = %v，期望 %v", got, wantFields)
	}
	if got, err := paybrowser.FieldValue(ctx, b, "#billingAddress-localityInput"); err != nil || got != "Portland" {
		t.Fatalf("页面上城市 = %q, %v，期望 Portland", got, err)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只填表不提交：仿造页收到 %d 次提交", n)
	}
}

// 联想库懒加载（2026-11-04 实测）：第一次键入只是开始加载，不出联想；清空重打一次街道才出。报告记下重打过。
func TestEmbeddedFillSuggestionLazy(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=suggest-lazy")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	lines := strings.Join(b.FillReport().Lines(), "\n")
	for _, want := range []string{"手动地址：已选地址联想：第 1 个 iframe 的选项", "已重打 1 次街道"} {
		if !strings.Contains(lines, want) {
			t.Errorf("报告缺少「%s」：\n%s", want, lines)
		}
	}
	for sel, want := range map[string]string{
		"#billingAddress-addressLine1Input": "1 Main St",
		"#billingAddress-postalCodeInput":   "97201",
	} {
		if got, err := paybrowser.FieldValue(ctx, b, sel); err != nil || got != want {
			t.Fatalf("页面上 %s = %q, %v，期望 %q", sel, got, err, want)
		}
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只填表不提交：仿造页收到 %d 次提交", n)
	}
}

// 实页（2026-11-04 07:23、07:34）：覆盖街道时的键入触发了联想请求，Esc 先于异步结果到达，结果回来后下拉又打开了，
// 盖住城市、州、邮编那一片，Fill 返回 20 秒后还开着（仿造页 addr=suggest-async）。填完地址要把它关掉：
// Fill 返回后哪个 frame 里都没有可见的联想选项，街道仍是账单街道，报告在「手动地址」后面记下「联想下拉已关闭」。
func TestEmbeddedFillClosesSuggestions(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=suggest-async")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	time.Sleep(500 * time.Millisecond) // 等过仿造页联想的 300ms 延迟：关上之后不能又被还没回来的结果打开
	if n, err := paybrowser.VisibleCount(ctx, b, `[role="option"]`); err != nil || n != 0 {
		t.Fatalf("Fill 返回后可见的联想选项 = %d, %v，期望 0", n, err)
	}
	lines := strings.Join(b.FillReport().Lines(), "\n")
	if !strings.Contains(lines, "手动地址：已选地址联想：第 1 个 iframe 的选项") || !strings.Contains(lines, "联想下拉已关闭") {
		t.Errorf("报告缺少「手动地址：已选地址联想：第 1 个 iframe 的选项」或「联想下拉已关闭」：\n%s", lines)
	}
	if got, err := paybrowser.FieldValue(ctx, b, "#billingAddress-addressLine1Input"); err != nil || got != "1 Main St" {
		t.Fatalf("页面上街道 = %q, %v，期望 1 Main St", got, err)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只填表不提交：仿造页收到 %d 次提交", n)
	}
}

// 联想下拉按 Esc 关不掉（仿造页 addr=suggest-sticky）：不报错，Fill 照常成功，报告记下「联想下拉没关上」，
// 由点支付前的落点检查兜住点击。
func TestEmbeddedFillSuggestionsStayOpen(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=suggest-sticky")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	lines := strings.Join(b.FillReport().Lines(), "\n")
	if !strings.Contains(lines, "联想下拉没关上") {
		t.Errorf("报告缺少「联想下拉没关上」：\n%s", lines)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只填表不提交：仿造页收到 %d 次提交", n)
	}
}

// payClicks 返回 clip=1 时主页面支付按钮收到的可信点击次数。
func payClicks(t *testing.T, ctx context.Context, b *paybrowser.Browser) string {
	t.Helper()
	time.Sleep(300 * time.Millisecond) // 点击触发的提交是异步的，留点时间让它落地
	v, err := paybrowser.FieldValue(ctx, b, "#pay-clicks")
	if err != nil {
		t.Fatalf("读支付按钮点击次数: %v", err)
	}
	return v
}

// 字段在 iframe 可视区下沿以下、iframe 内又滚不动（真实页面 iframe 带 scrolling="no"、内文档比 iframe 高）：
// 按坐标点会点到主页面上紧贴 iframe 下沿的支付按钮。内嵌页上点字段前先看落点，不对就不发鼠标事件、直接设焦点。
func TestEmbeddedFillFocusOutsideFrame(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=1&clip=1")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if got, want := fieldLabels(t, b.FillReport()), []string{"卡号", "有效期", "CVC", "国家", "邮编"}; !slices.Equal(got, want) {
		t.Fatalf("填过的字段 = %v，期望 %v", got, want)
	}
	if got, err := paybrowser.FieldValue(ctx, b, "#payment-postalCodeInput"); err != nil || got != "97201" {
		t.Fatalf("页面上邮编 = %q, %v，期望 97201", got, err)
	}
	const want = "直接设焦点：邮编（点击落点是主页面上的 button，不是 iframe）"
	if lines := strings.Join(b.FillReport().Lines(), "\n"); !strings.Contains(lines, want) {
		t.Errorf("报告缺少「%s」：\n%s", want, lines)
	}
	if n := payClicks(t, ctx, b); n != "0" {
		t.Fatalf("填表时支付按钮收到 %s 次点击，期望 0", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只填表不提交：仿造页收到 %d 次提交", n)
	}
}

// 选了联想后城市、州、邮编虽然可见，但在 iframe 可视区下沿以下、点下去会落到主页面：不算展开，报错放弃，不点它们。
func TestEmbeddedFillSuggestionExpandsOutsideFrame(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=suggest&clip=1")

	err := b.Fill(ctx, testCard, embeddedBilling)
	if err == nil || !strings.Contains(err.Error(), "仍未展开") {
		t.Fatalf("Fill = %v，期望报仍未展开", err)
	}
	if n := payClicks(t, ctx, b); n != "0" {
		t.Fatalf("填表时支付按钮收到 %s 次点击，期望 0", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("Fill 失败时仿造页收到 %d 次提交", n)
	}
}

// 卡片 iframe 在主页面首屏下方，iframe 里元素的「滚进可视区」又没带动主页面（2026-11-04 实测：落点检查报「点击落点不在页面上」；
// 仿造页用 fold=1 模拟，见 checkout-embedded.html）：坐标超出主页面视口，落点检查一律不过。
// 落点检查要先用脚本把主页面滚到让元素居中（iframe 外面没有别的滚动容器，滚的是 document.scrollingElement）：
// 字段照常用鼠标点，联想能点中并展开，支付按钮 0 次点击。
func TestEmbeddedFillBelowFold(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=suggest&fold=1")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	lines := strings.Join(b.FillReport().Lines(), "\n")
	if !strings.Contains(lines, "手动地址：已选地址联想：第 1 个 iframe 的选项") {
		t.Errorf("报告缺少「手动地址：已选地址联想：第 1 个 iframe 的选项」：\n%s", lines)
	}
	if strings.Contains(lines, "直接设焦点") {
		t.Errorf("首屏下方的字段滚进可视区后应能用鼠标点，不该直接设焦点：\n%s", lines)
	}
	if got, err := paybrowser.FieldValue(ctx, b, "#billingAddress-postalCodeInput"); err != nil || got != "97201" {
		t.Fatalf("页面上邮编 = %q, %v，期望 97201", got, err)
	}
	if n := payClicks(t, ctx, b); n != "0" {
		t.Fatalf("填表时支付按钮收到 %s 次点击，期望 0", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只填表不提交：仿造页收到 %d 次提交", n)
	}
}

// 实页结构（2026-11-04 06:33 实测）：主页面 html、body 不滚动，滚动容器是 main，卡片 iframe 在 main 的首屏以下，
// iframe 里元素的滚入又不带动 main（仿造页 fold=main）。window.scrollBy 滚不动；落点检查要滚「装着这个 iframe 的滚动容器」：
// 字段照常用鼠标点，联想能点中并展开，支付按钮 0 次点击。
func TestEmbeddedFillBelowFoldInScroller(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=suggest&fold=main")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	lines := strings.Join(b.FillReport().Lines(), "\n")
	if !strings.Contains(lines, "手动地址：已选地址联想：第 1 个 iframe 的选项") {
		t.Errorf("报告缺少「手动地址：已选地址联想：第 1 个 iframe 的选项」：\n%s", lines)
	}
	if strings.Contains(lines, "直接设焦点") {
		t.Errorf("滚动容器里首屏以下的字段滚进可视区后应能用鼠标点，不该直接设焦点：\n%s", lines)
	}
	if got, err := paybrowser.FieldValue(ctx, b, "#billingAddress-postalCodeInput"); err != nil || got != "97201" {
		t.Fatalf("页面上邮编 = %q, %v，期望 97201", got, err)
	}
	if n := payClicks(t, ctx, b); n != "0" {
		t.Fatalf("填表时支付按钮收到 %s 次点击，期望 0", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只填表不提交：仿造页收到 %d 次提交", n)
	}
}

// 街道值里有控制字符时什么都不键入：逐字键入会把 \n、\r 当回车发出去，回车在 Stripe 输入框里可能触发提交。
// Fill 入口就拒绝（Billing.validate），卡号也不键入，所以地址区根本不会出现。
func TestEmbeddedFillStreetControlChars(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=suggest")

	bl := embeddedBilling
	bl.Line1 = "1 Main St\n"
	err := b.Fill(ctx, testCard, bl)
	if err == nil || !strings.Contains(err.Error(), "账单地址含控制字符（街道地址）") {
		t.Fatalf("Fill = %v，期望报账单地址含控制字符（街道地址）", err)
	}
	if got, err := paybrowser.FieldValue(ctx, b, "#payment-numberInput"); err != nil || got != "" {
		t.Fatalf("卡号框 = %q, %v，期望什么都没键入", got, err)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("Fill 失败时仿造页收到 %d 次提交", n)
	}
}

// 地址字段查的时候 DOM 里没有、填完地址前又出现了（这里是键入街道后才插入姓名框）：不能静默漏填，报错放弃。
// 插入由键入街道触发，不靠定时，结果不受机器快慢影响。
func TestEmbeddedFillAbsentReappears(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=late")

	err := b.Fill(ctx, testCard, embeddedBilling)
	if err == nil || !strings.Contains(err.Error(), "地址字段「姓名」填表时没找到、之后又出现了") {
		t.Fatalf("Fill = %v，期望报地址字段「姓名」填表时没找到、之后又出现了", err)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("Fill 失败时仿造页收到 %d 次提交", n)
	}
}

// clickOption 只在主页面上的点击落点正是承载选项的 iframe 时才点：
// 真实页面上地址 iframe 只露出上半截，选项坐标可能落到 iframe 外面，而支付按钮就在主页面上。选项本身是提交控件时也不点。
func TestClickOptionRefusesNonIframeTarget(t *testing.T) {
	needChrome(t)
	srv := httptest.NewServer(http.FileServer(http.Dir("testdata")))
	t.Cleanup(srv.Close)
	b := launch(t, paybrowser.LaunchOptions{})
	ctx := openCtx(t)
	if err := b.ProbeOpen(ctx, srv.URL+"/option-target.html", nil, 200*time.Millisecond); err != nil {
		t.Fatalf("ProbeOpen: %v", err)
	}

	// 对照：没被盖住的 iframe 里的选项能点到，说明下面的拒绝不是因为找不到或点不动
	if err := paybrowser.ClickOption(ctx, b, "#open-option"); err != nil {
		t.Fatalf("没被盖住的 iframe 里的选项应能点：%v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		v, err := paybrowser.FieldValue(ctx, b, "#open-clicks")
		if err == nil && v == "1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("没被盖住的选项收到的点击 = %q, %v，期望 1", v, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	for _, c := range []struct{ name, option, clicks, want string }{
		{"主页面上的选项", "#main-option", "#main-clicks", "不是 iframe，没有点击"},
		{"被别的 iframe 盖住的选项", "#covered-option", "#covered-clicks", "落点是别的 iframe，没有点击"},
		{"选项是提交按钮", "#submit-option", "#submit-clicks", "是提交控件"},
		{"选项还在移动", "#moving-option", "#moving-clicks", "位置还在变，没有点击"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := paybrowser.ClickOption(ctx, b, c.option)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ClickOption(%s) = %v，期望拒绝并说明「%s」", c.option, err, c.want)
			}
			time.Sleep(300 * time.Millisecond)
			if v, err := paybrowser.FieldValue(ctx, b, c.clicks); err != nil || v != "0" {
				t.Fatalf("%s 收到的点击 = %q, %v，期望 0", c.option, v, err)
			}
		})
	}
}

// 必填的卡片字段找不到时照样报错放弃，不能当成可选字段记进 Absent。
func TestEmbeddedFillRequiresCardFields(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.SetFieldWait(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "nocvc=1")

	err := b.Fill(ctx, testCard, embeddedBilling)
	if err == nil || !strings.Contains(err.Error(), "找不到「CVC」输入框") {
		t.Fatalf("Fill = %v，期望报找不到「CVC」输入框", err)
	}
	if strings.Contains(err.Error(), testCard.Number) || strings.Contains(err.Error(), testCard.CVC) {
		t.Fatalf("错误消息泄露了卡号或 CVC: %v", err)
	}
	if r := b.FillReport(); len(r.Absent) != 0 {
		t.Fatalf("必填字段不能记进 Absent，得到 %v", r.Absent)
	}
	if err := b.Submit(ctx); err == nil {
		t.Fatal("Fill 失败后 Submit 应报错")
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("Fill 失败时仿造页收到 %d 次提交", n)
	}
}

// 「保存付款信息」默认勾着时，填表后 EnsureNoSavePayment 把它取消。
func TestEmbeddedSavePaymentUnchecked(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "savepay=1")
	const box = "#payment-savePaymentInput"

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if on, err := paybrowser.FieldChecked(ctx, b, box); err != nil || !on {
		t.Fatalf("取消前 %s 勾选 = %v, %v，期望勾着", box, on, err)
	}
	note, err := b.EnsureNoSavePayment(ctx)
	if err != nil || note != "已取消" {
		t.Fatalf("EnsureNoSavePayment = %q, %v，期望已取消", note, err)
	}
	if on, err := paybrowser.FieldChecked(ctx, b, box); err != nil || on {
		t.Fatalf("取消后 %s 勾选 = %v, %v，期望未勾选", box, on, err)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只处理勾选框，不能提交：仿造页收到 %d 次提交", n)
	}
}

// 三个勾选框都在被裁掉的 iframe 可视区下沿以下，按坐标点会点到主页面上紧贴 iframe 下沿的支付按钮：
// 内嵌页上点勾选框前先看落点，不过就不发鼠标事件、改用脚本点并核对。勾选状态都对，支付按钮 0 次点击。
func TestEmbeddedCheckboxesOutsideFrame(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "clip=1&low=1&savepay=1&linksave=1&terms=1")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	note, err := b.EnsureNoSavePayment(ctx)
	if err != nil || note != "已取消" {
		t.Fatalf("EnsureNoSavePayment = %q, %v，期望已取消", note, err)
	}
	// 报告写明没用鼠标点、改用脚本点击，以及落点检查的原因
	const how = "；改用脚本点击（点击落点"
	r := b.FillReport()
	for name, got := range map[string]string{"Link 保存": r.LinkSave, "同意条款": r.Terms, "保存付款信息": r.SavePayment} {
		want := map[string]string{"Link 保存": "已取消", "同意条款": "已勾选", "保存付款信息": "已取消"}[name] + how
		if !strings.HasPrefix(got, want) || strings.Contains(got, "没有点击") {
			t.Errorf("%s = %q，期望以「%s」开头、写明原因", name, got, want)
		}
	}
	for sel, want := range map[string]bool{
		"#enableStripePass":              false,
		"#termsOfServiceConsentCheckbox": true,
		"#payment-savePaymentInput":      false,
	} {
		if on, err := paybrowser.FieldChecked(ctx, b, sel); err != nil || on != want {
			t.Fatalf("%s 勾选 = %v, %v，期望 %v", sel, on, err, want)
		}
	}
	if n := payClicks(t, ctx, b); n != "0" {
		t.Fatalf("处理勾选框时支付按钮收到 %s 次点击，期望 0", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只处理勾选框，不能提交：仿造页收到 %d 次提交", n)
	}
}

// 手动地址入口在被裁掉的 iframe 可视区下沿以下：落点不过就不点，城市、州、邮编展不开，报错放弃；支付按钮 0 次点击。
func TestEmbeddedManualEntryOutsideFrame(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=stripe&clip=1&low=1")

	if err := b.Fill(ctx, testCard, embeddedBilling); err == nil {
		t.Fatal("手动地址入口点不到时 Fill 应报错")
	}
	if got := b.FillReport().ManualAddress; !strings.HasPrefix(got, "没点（点击落点") {
		t.Errorf("手动地址 = %q，期望写明没点和落点检查的原因", got)
	}
	if n := payClicks(t, ctx, b); n != "0" {
		t.Fatalf("填表时支付按钮收到 %s 次点击，期望 0", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("Fill 失败时仿造页收到 %d 次提交", n)
	}
}

// showLinkVerify 让卡片 iframe 弹出 Link 内嵌验证框（linkverify=1），等它显示出来。
func showLinkVerify(t *testing.T, ctx context.Context, b *paybrowser.Browser) {
	t.Helper()
	const js = `document.getElementById('card-frame').contentWindow.postMessage({ type: 'linkverify' }, '*')`
	if err := paybrowser.EvalMain(ctx, b, js); err != nil {
		t.Fatalf("叫出 Link 验证框: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if v, err := paybrowser.FieldValue(ctx, b, "#link-state"); err == nil && v == "shown" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Link 验证框 5 秒内没有出现")
		}
	}
}

// 账号邮箱注册过 Link：内嵌页上点「Pay without Link」跳过验证，照常填卡（入口点击过落点检查）。
func TestEmbeddedPayWithoutLink(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "linkverify=1")
	showLinkVerify(t, ctx, b)

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if got := b.FillReport().LinkVerify; got != "已点「Pay without Link」" {
		t.Fatalf("Link 验证 = %q，期望已点「Pay without Link」", got)
	}
	if got, err := paybrowser.FieldValue(ctx, b, "#payment-cvcInput"); err != nil || got != testCard.CVC {
		t.Fatalf("CVC 框核对失败: %v", err)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只填表不提交：仿造页收到 %d 次提交", n)
	}
}

// 「Pay without Link」在被裁掉的 iframe 可视区下沿以下：落点不过就不点，按原逻辑等满后放弃；支付按钮 0 次点击。
func TestEmbeddedPayWithoutLinkOutsideFrame(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "linkverify=1&clip=1&low=1")
	showLinkVerify(t, ctx, b)

	err := b.Fill(ctx, testCard, embeddedBilling)
	if err == nil || !strings.Contains(err.Error(), "要求验证身份，「Pay without Link」点不了（点击落点") {
		t.Fatalf("Fill = %v，期望报 Link 要求验证身份、「Pay without Link」点不了并写明原因", err)
	}
	if got := b.FillReport().LinkVerify; !strings.HasPrefix(got, "要求验证身份，「Pay without Link」没点（点击落点") {
		t.Errorf("Link 验证 = %q，期望写明「Pay without Link」没点和原因", got)
	}
	if n := payClicks(t, ctx, b); n != "0" {
		t.Fatalf("填表时支付按钮收到 %s 次点击，期望 0", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("Fill 失败时仿造页收到 %d 次提交", n)
	}
}

// 打开后页面又冒出第二个提交按钮时，按钮状态要报「不唯一」。
func TestEmbeddedPayButtonStateNotUnique(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.SetFieldWait(time.Second)) // 不唯一时要等满 fieldWait 才下结论，缩短免得白等 10 秒
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "rerender=1")

	time.Sleep(4 * time.Second)
	if st := b.PayButtonState(ctx); st != "不唯一（2 个）" {
		t.Fatalf("支付按钮状态 = %q，期望「不唯一（2 个）」", st)
	}
}

// fillSubmitEmbedded 填表并点支付。
func fillSubmitEmbedded(t *testing.T, b *paybrowser.Browser, ctx context.Context) {
	t.Helper()
	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if err := b.Submit(ctx); err != nil {
		t.Fatalf("Submit: %v", err)
	}
}

// 点击后页面跳离 /checkout/：WaitOutcome 立即返回「页面已离开结账页」，不等到超时；只点一次，之后再提交被拒。
func TestEmbeddedSubmitOnceAndLeave(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "leave=1")

	fillSubmitEmbedded(t, b, ctx)
	start := time.Now()
	out, msg, err := b.WaitOutcome(ctx, 20*time.Second)
	took := time.Since(start)
	if err != nil || out != paybrowser.OutcomeUnknown || msg != "页面已离开结账页" {
		t.Fatalf("WaitOutcome = %q, %q, %v，期望 unknown、「页面已离开结账页」、无错误", out, msg, err)
	}
	if took >= 10*time.Second {
		t.Fatalf("WaitOutcome 用了 %v，离开结账页后应立即返回（10 秒内）", took)
	}
	if n := len(f.payments()); n != 1 {
		t.Fatalf("仿造页收到 %d 次提交，期望 1 次", n)
	}
	if n := f.doneVisits(); n != 1 {
		t.Fatalf("跳到 /done %d 次，期望 1 次", n)
	}
	if err := b.Submit(ctx); !errors.Is(err, paybrowser.ErrAlreadySubmitted) {
		t.Fatalf("再次 Submit = %v，期望 ErrAlreadySubmitted", err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(f.payments()); n != 1 {
		t.Fatalf("再次 Submit 后仿造页收到 %d 次提交，期望仍是 1 次", n)
	}
}

// 填表后页面重绘出第二个提交按钮（Review Focus 1）：Submit 不点，返回「不唯一」，LockSubmit 证明没点过。
func TestEmbeddedSubmitRefusesSecondButton(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	t.Cleanup(paybrowser.SetFieldWait(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "rerender=1")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	time.Sleep(4 * time.Second)
	err := b.Submit(ctx)
	if err == nil || !strings.Contains(err.Error(), "支付按钮不唯一（找到 2 个），没有点击") {
		t.Fatalf("Submit = %v，期望「支付按钮不唯一（找到 2 个），没有点击」", err)
	}
	if b.LockSubmit() {
		t.Fatal("按钮不唯一时没有点击，LockSubmit 应返回 false")
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(f.payments()); n != 0 {
		t.Fatalf("按钮不唯一时仿造页收到 %d 次提交，期望 0 次", n)
	}
}

// 主页面上的中文拒付提示不在拒付词表里（Review Focus 2）：不能判成被拒，也不能判成成功，等到超时返回 unknown。
func TestEmbeddedChineseDeclineIsUnknown(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	const text = "你的银行卡被拒绝"
	b, ctx := openEmbedded(t, f, "decline="+url.QueryEscape(text))

	fillSubmitEmbedded(t, b, ctx)
	out, msg, err := b.WaitOutcome(ctx, 3*time.Second)
	if err != nil || out != paybrowser.OutcomeUnknown {
		t.Fatalf("WaitOutcome = %q, %q, %v，期望 unknown、无错误", out, msg, err)
	}
	// 这段文字确实出现过、被当成认不出的提示记下，而不是根本没看到
	if msg != text {
		t.Fatalf("超时说明 = %q，期望最后一条未识别提示 %q", msg, text)
	}
}

// 点击后当前页面弹出 3DS 验证框：返回 challenge。
func TestEmbeddedChallenge(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "challenge=1")

	fillSubmitEmbedded(t, b, ctx)
	if out, msg, err := b.WaitOutcome(ctx, 15*time.Second); err != nil || out != paybrowser.OutcomeChallenge {
		t.Fatalf("WaitOutcome = %q, %q, %v，期望 challenge", out, msg, err)
	}
}

// 找到唯一按钮之后、点击之前页面变了：点击那一刻重新核对（Review Focus 1），不通过就不置位、不点击，LockSubmit 证明没点过。
func TestEmbeddedSubmitRechecksAtClick(t *testing.T) {
	cases := []struct {
		name string
		hook func(b *paybrowser.Browser)
		want string
	}{
		{"又冒出第二个按钮", func(b *paybrowser.Browser) {
			paybrowser.RunBeforeRecheck(b, `(() => {
				const b = document.createElement('button')
				b.type = 'submit'
				b.textContent = '升级至 Pro'
				document.getElementById('buttons').appendChild(b)
			})()`)
		}, "支付按钮不唯一（找到 2 个），没有点击"},
		// SPA 换路由不换文档，只看文档有没有换拦不住
		{"换路由离开结账页", func(b *paybrowser.Browser) {
			paybrowser.RunBeforeRecheck(b, `history.pushState({}, '', '/auth/login')`)
		}, "页面已离开结账页，没有点击"},
		{"按钮变成不可用", func(b *paybrowser.Browser) {
			paybrowser.RunBeforeRecheck(b, `(() => { document.querySelector('#buttons button[type="submit"]').disabled = true })()`)
		}, "支付按钮不可点击（可能有字段未通过页面校验），没有点击"},
		// 整页重新加载后，找到按钮时那个文档的执行环境已经失效
		{"复查读不出", paybrowser.ReloadBeforeRecheck, "支付按钮读不出，没有点击"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			needChrome(t)
			t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
			f := newFakeEmbedded(t)
			b, ctx := openEmbedded(t, f, "")

			if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
				t.Fatalf("Fill: %v", err)
			}
			c.hook(b)
			err := b.Submit(ctx)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Submit = %v，期望「%s」", err, c.want)
			}
			if b.LockSubmit() {
				t.Fatal("点击前复查不通过时没有点击，LockSubmit 应返回 false")
			}
			time.Sleep(300 * time.Millisecond)
			if n := len(f.payments()); n != 0 {
				t.Fatalf("点击前复查不通过时仿造页收到 %d 次提交，期望 0 次", n)
			}
		})
	}
}

// 找到按钮之后、点击之前按钮被换成另一个，但仍是唯一可用的提交按钮、仍在结账页：点新的那个，只点一次。
func TestEmbeddedSubmitClicksReplacedButton(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	// 新按钮被点到时把自己的 value 改成 clicked；旧按钮已脱离文档，点旧句柄会报错
	paybrowser.RunBeforeRecheck(b, `(() => {
		const old = document.querySelector('#buttons button[type="submit"]')
		const b = document.createElement('button')
		b.type = 'submit'
		b.id = 'replaced'
		b.textContent = '升级至 Pro'
		b.addEventListener('click', () => { b.value = 'clicked' })
		old.replaceWith(b)
	})()`)
	if err := b.Submit(ctx); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if v, err := paybrowser.FieldValue(ctx, b, "#replaced"); err != nil || v != "clicked" {
		t.Fatalf("新按钮的 value = %q, %v，期望 clicked（点的应是新按钮）", v, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(f.payments()) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if err := b.Submit(ctx); !errors.Is(err, paybrowser.ErrAlreadySubmitted) {
		t.Fatalf("再次 Submit = %v，期望 ErrAlreadySubmitted", err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(f.payments()); n != 1 {
		t.Fatalf("仿造页收到 %d 次提交，期望正好 1 次", n)
	}
}

// 填表后页面用 pushState 换到结账页以外的路径，按钮仍只有一个：Submit 不认这个按钮，不点。
func TestEmbeddedSubmitRefusesAfterLeavingCheckout(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	t.Cleanup(paybrowser.SetFieldWait(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "pushstate=1")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	time.Sleep(4 * time.Second)
	err := b.Submit(ctx)
	if err == nil || !strings.Contains(err.Error(), "已离开结账页") {
		t.Fatalf("Submit = %v，期望含「已离开结账页」", err)
	}
	if b.LockSubmit() {
		t.Fatal("离开结账页时没有点击，LockSubmit 应返回 false")
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(f.payments()); n != 0 {
		t.Fatalf("离开结账页时仿造页收到 %d 次提交，期望 0 次", n)
	}
}

// coverClicks 返回 cover=1 时盖住支付按钮的那一层收到的可信点击（误点）次数。先调 payClicks，它已经等过点击落地。
func coverClicks(t *testing.T, ctx context.Context, b *paybrowser.Browser) string {
	t.Helper()
	v, err := paybrowser.FieldValue(ctx, b, "#cover-clicks")
	if err != nil {
		t.Fatalf("读误点次数: %v", err)
	}
	return v
}

// 填完卡后有东西盖住支付按钮（仿造页 cover=1：透明的 position:fixed 层；实页上可能是地址联想下拉、横幅、弹层）：
// 点支付前看落点，落点不是按钮自己就不点，返回「支付按钮被挡住」，不置位，LockSubmit 证明没点过；按钮和盖着的层都没收到点击。
func TestEmbeddedSubmitRefusesCoveredButton(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "cover=1")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	err := b.Submit(ctx)
	if err == nil || !strings.Contains(err.Error(), "支付按钮被挡住（落点是 div），没有点击") {
		t.Fatalf("Submit = %v，期望「支付按钮被挡住（落点是 div），没有点击」", err)
	}
	if b.LockSubmit() {
		t.Fatal("支付按钮被挡住时没有点击，LockSubmit 应返回 false")
	}
	if n := payClicks(t, ctx, b); n != "0" {
		t.Fatalf("支付按钮收到 %s 次点击，期望 0", n)
	}
	if n := coverClicks(t, ctx, b); n != "0" {
		t.Fatalf("盖住按钮的层收到 %s 次误点，期望 0", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("支付按钮被挡住时仿造页收到 %d 次提交，期望 0 次", n)
	}
}

// 地址改了之后页面重算，支付按钮短暂 disabled、aria-busy（2026-11-04 07:23 实测约 1 秒；仿造页 busy=1 约 1.5 秒）：
// 试运行报按钮状态前和 Submit 一样等它可点，报「可点击」，不误报「存在但不可点击」。之后 Submit 照常只点一次。
func TestEmbeddedPayButtonStateWaitsBusy(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "busy=1&addr=1")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if st := b.PayButtonState(ctx); !strings.HasPrefix(st, "可点击（等了 ") {
		t.Fatalf("支付按钮状态 = %q，期望「可点击（等了 N 秒）」", st)
	}
	if err := b.Submit(ctx); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(f.payments()) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if n := payClicks(t, ctx, b); n != "1" {
		t.Fatalf("支付按钮收到 %s 次点击，期望 1", n)
	}
	if n := len(f.payments()); n != 1 {
		t.Fatalf("仿造页收到 %d 次提交，期望正好 1 次", n)
	}
}

// 试运行时支付按钮被挡住（仿造页 cover=1）：按钮唯一、可用，但落点不是它自己，报「可点击，但落点被挡住（原因）」。只检查，不点击。
func TestEmbeddedPayButtonStateReportsCovered(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "cover=1")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if st := b.PayButtonState(ctx); st != "可点击，但落点被挡住（落点是 div）" {
		t.Fatalf("支付按钮状态 = %q，期望「可点击，但落点被挡住（落点是 div）」", st)
	}
	if n := payClicks(t, ctx, b); n != "0" {
		t.Fatalf("支付按钮收到 %s 次点击，期望 0", n)
	}
	if n := coverClicks(t, ctx, b); n != "0" {
		t.Fatalf("盖住按钮的层收到 %s 次误点，期望 0", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只读按钮状态：仿造页收到 %d 次提交", n)
	}
}

// 支付按钮一直不可用：试运行等满 fieldWait 后报「存在但不可点击（等了 N 秒）」。
func TestEmbeddedPayButtonStateDisabled(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	t.Cleanup(paybrowser.SetFieldWait(time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if err := paybrowser.EvalMain(ctx, b, `document.querySelector('#buttons button[type="submit"]').disabled = true`); err != nil {
		t.Fatalf("禁用支付按钮: %v", err)
	}
	if st := b.PayButtonState(ctx); st != "存在但不可点击（等了 1 秒）" {
		t.Fatalf("支付按钮状态 = %q，期望「存在但不可点击（等了 1 秒）」", st)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("只读按钮状态：仿造页收到 %d 次提交", n)
	}
}

// keyCounts 返回主页面（#main-keys）和卡片 iframe（#frame-keys）各自收到的可信 keydown 次数。
func keyCounts(t *testing.T, ctx context.Context, b *paybrowser.Browser) (main, frame string) {
	t.Helper()
	var err error
	if main, err = paybrowser.FieldValue(ctx, b, "#main-keys"); err != nil {
		t.Fatalf("读主页面按键次数: %v", err)
	}
	if frame, err = paybrowser.FieldValue(ctx, b, "#frame-keys"); err != nil {
		t.Fatalf("读 iframe 按键次数: %v", err)
	}
	return main, frame
}

// iframe 失焦时页面重算，支付按钮短暂不可用（仿造页 busy=blur：iframe 里有元素失焦，按钮 disabled、aria-busy 约 800ms）。
// 直接用鼠标点，按下那一刻焦点离开 iframe，按钮在松开之前就变成不可用，点击丢了，可 submitted 已经置位。
// Fill 末尾先让 iframe 里的输入框失焦，Submit 先把焦点移到主页面、等按钮重新可用，再检查、再点：只点一次，点到了。
// Submit 期间主页面和 iframe 都没收到任何按键。
func TestEmbeddedSubmitAfterBlurRecompute(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "busy=blur")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	mainBefore, frameBefore := keyCounts(t, ctx, b)
	if err := b.Submit(ctx); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !b.LockSubmit() {
		t.Fatal("点击之后 LockSubmit 必须返回 true")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(f.payments()) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if n := payClicks(t, ctx, b); n != "1" {
		t.Fatalf("支付按钮收到 %s 次可信点击，期望 1", n)
	}
	if n := len(f.payments()); n != 1 {
		t.Fatalf("仿造页收到 %d 次提交，期望正好 1 次", n)
	}
	if m, fr := keyCounts(t, ctx, b); m != mainBefore || fr != frameBefore {
		t.Fatalf("Submit 期间收到了按键：主页面 %s→%s，iframe %s→%s", mainBefore, m, frameBefore, fr)
	}
}

// 焦点一离开 iframe，支付按钮就一直不可用（仿造页 busy=blur-stuck）：Submit 先把焦点移到主页面，等不到按钮可用就不点，
// 返回「不可点击……没有点击」，不置位，LockSubmit 证明没点过；0 次点击、0 次提交，也没发任何按键。
func TestEmbeddedSubmitRefusesWhenBlurDisablesButton(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	t.Cleanup(paybrowser.SetFieldWait(time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "busy=blur-stuck")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	mainBefore, frameBefore := keyCounts(t, ctx, b)
	err := b.Submit(ctx)
	if err == nil || !strings.Contains(err.Error(), "不可点击") || !strings.Contains(err.Error(), "没有点击") {
		t.Fatalf("Submit = %v，期望含「不可点击」和「没有点击」", err)
	}
	if b.LockSubmit() {
		t.Fatal("按钮一直不可用时没有点击，LockSubmit 应返回 false")
	}
	if n := payClicks(t, ctx, b); n != "0" {
		t.Fatalf("支付按钮收到 %s 次可信点击，期望 0", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("仿造页收到 %d 次提交，期望 0 次", n)
	}
	if m, fr := keyCounts(t, ctx, b); m != mainBefore || fr != frameBefore {
		t.Fatalf("Submit 期间收到了按键：主页面 %s→%s，iframe %s→%s", mainBefore, m, frameBefore, fr)
	}
}

// 输入框失焦时页面改写了格式（仿造页 blurzip=1：邮编失焦后变成「97201-0000」）：Fill 末尾先让它失焦，
// 改写发生在最后核对之前，被核对抓住，报错放弃，不点支付。
func TestEmbeddedFillBlurRewritesZip(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=1&blurzip=1")

	err := b.Fill(ctx, testCard, embeddedBilling)
	if err == nil || !strings.Contains(err.Error(), "「邮编」填完后被页面改掉了") {
		t.Fatalf("Fill = %v，期望报「邮编」填完后被页面改掉了", err)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("Fill 失败时仿造页收到 %d 次提交", n)
	}
}

// 关联想下拉的那一下 Esc 把街道清空了（仿造页 addr=suggest-revert）：关上后核对街道不一致，报错放弃。
func TestEmbeddedFillCloseSuggestionsReverts(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "addr=suggest-revert")

	err := b.Fill(ctx, testCard, embeddedBilling)
	if err == nil || !strings.Contains(err.Error(), "关联想下拉后街道地址被改了，已放弃") {
		t.Fatalf("Fill = %v，期望报「关联想下拉后街道地址被改了，已放弃」", err)
	}
	if lines := strings.Join(b.FillReport().Lines(), "\n"); !strings.Contains(lines, "联想下拉已关闭") {
		t.Errorf("报告缺少「联想下拉已关闭」：\n%s", lines)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("Fill 失败时仿造页收到 %d 次提交", n)
	}
}

// 支付按钮一直在动（这里用动画来回平移）：落点检查读两次位置不一样，试运行报「没过落点检查（位置还在变）」，
// Submit 不点、不置位。原因不是「落点是别的元素」，所以不说「被挡住」。
func TestEmbeddedSubmitRefusesMovingButton(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	const js = `document.querySelector('#buttons button[type="submit"]').animate(
		[{ transform: 'translateX(0px)' }, { transform: 'translateX(200px)' }],
		{ duration: 1000, iterations: Infinity, direction: 'alternate' })`
	if err := paybrowser.EvalMain(ctx, b, js); err != nil {
		t.Fatalf("让支付按钮动起来: %v", err)
	}
	if st := b.PayButtonState(ctx); st != "可点击，但没过落点检查（位置还在变）" {
		t.Fatalf("支付按钮状态 = %q，期望「可点击，但没过落点检查（位置还在变）」", st)
	}
	err := b.Submit(ctx)
	if err == nil || !strings.Contains(err.Error(), "支付按钮没过落点检查（位置还在变），没有点击") {
		t.Fatalf("Submit = %v，期望「支付按钮没过落点检查（位置还在变），没有点击」", err)
	}
	if b.LockSubmit() {
		t.Fatal("按钮还在动时没有点击，LockSubmit 应返回 false")
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(f.payments()); n != 0 {
		t.Fatalf("仿造页收到 %d 次提交，期望 0 次", n)
	}
}

// 支付按钮的中心在主页面可视区外（这里把它固定在左边缘外面一半多），滚也滚不过来：elementFromPoint 读不到元素，
// Submit 报「没过落点检查（点击落点不在页面上）」，不点、不置位。
func TestEmbeddedSubmitRefusesOffscreenButton(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "")

	if err := b.Fill(ctx, testCard, embeddedBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	const js = `document.querySelector('#buttons button[type="submit"]').style.cssText =
		'position: fixed; left: -150px; top: 10px; width: 200px; height: 40px; margin: 0'`
	if err := paybrowser.EvalMain(ctx, b, js); err != nil {
		t.Fatalf("把支付按钮挪到可视区外: %v", err)
	}
	err := b.Submit(ctx)
	if err == nil || !strings.Contains(err.Error(), "支付按钮没过落点检查（点击落点不在页面上），没有点击") {
		t.Fatalf("Submit = %v，期望「支付按钮没过落点检查（点击落点不在页面上），没有点击」", err)
	}
	if b.LockSubmit() {
		t.Fatal("落点不在页面上时没有点击，LockSubmit 应返回 false")
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(f.payments()); n != 0 {
		t.Fatalf("仿造页收到 %d 次提交，期望 0 次", n)
	}
}
