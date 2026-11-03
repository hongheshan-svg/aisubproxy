package paybrowser_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
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
	if want := []string{"国家", "州/省", "城市", "邮编", "姓名", "街道地址"}; !slices.Equal(r.Absent, want) {
		t.Fatalf("Absent = %v，期望 %v", r.Absent, want)
	}
	if got, want := fieldLabels(t, r), []string{"卡号", "有效期", "CVC"}; !slices.Equal(got, want) {
		t.Fatalf("填过的字段 = %v，期望 %v", got, want)
	}
	lines := strings.Join(r.Lines(), "\n")
	for _, want := range []string{"页面没有的字段：国家、州/省、城市、邮编、姓名、街道地址", "手动地址：未点击"} {
		if !strings.Contains(lines, want) {
			t.Errorf("报告缺少 %q：\n%s", want, lines)
		}
	}
	if st := b.PayButtonState(ctx); st != "可点击" {
		t.Fatalf("支付按钮状态 = %q，期望可点击", st)
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
	if want := []string{"州/省", "城市", "姓名", "街道地址"}; !slices.Equal(r.Absent, want) {
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

// 打开后页面又冒出第二个提交按钮时，按钮状态要报「不唯一」。
func TestEmbeddedPayButtonStateNotUnique(t *testing.T) {
	needChrome(t)
	f := newFakeEmbedded(t)
	b, ctx := openEmbedded(t, f, "rerender=1")

	time.Sleep(4 * time.Second)
	if st := b.PayButtonState(ctx); st != "不唯一（2 个）" {
		t.Fatalf("支付按钮状态 = %q，期望「不唯一（2 个）」", st)
	}
}
