package paybrowser_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

func lineWith(lines []string, prefix string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

// 开始填卡之前：地址去掉 query，文字脱敏，能看到各候选选择器的命中数。
func TestDiagnoseBeforeFill(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, ctx := openKind(t, f, stripeCheckout, checkoutQuery("banner", "Signed in as buyer@example.com, ref 4242424242424242", "token", "secret-token"), paybrowser.LaunchOptions{})
	lines := b.Diagnose(ctx)
	all := strings.Join(lines, "\n")

	if got := lineWith(lines, "地址："); !strings.HasSuffix(got, "/checkout-stripe.html") {
		t.Fatalf("地址行 = %q，期望只有主机和路径", got)
	}
	if lineWith(lines, "标题：Stripe Checkout") == "" {
		t.Fatalf("缺标题: %s", all)
	}
	text := lineWith(lines, "正文开头：")
	if !strings.Contains(text, "Pure Glow Cream") {
		t.Fatalf("缺正文: %s", all)
	}
	for _, secret := range []string{"secret-token", "buyer@example.com", "4242424242424242"} {
		if strings.Contains(all, secret) {
			t.Fatalf("诊断泄露了 %q: %s", secret, all)
		}
	}
	// 卡号候选依次是 #payment-numberInput、#cardNumber、autocomplete、name=cardNumber、name=number；卡字段在主页面
	hits := lineWith(lines, "选择器可见命中")
	if !strings.Contains(hits, "卡号 0/1/1/1/0|0/0/0/0/0") {
		t.Fatalf("卡号命中数不符: %s", hits)
	}
	// 城市默认隐藏：可见命中都是 0，但要看得出元素其实在
	if !strings.Contains(hits, "城市 0/0/0/0/0/0/0/0|0/0/0/0/0/0/0/0（另有 3 处不可见）") {
		t.Fatalf("城市命中数不符: %s", hits)
	}
}

// 字段在 iframe 里的页面：命中数落在竖线右边，缺的字段全是 0，一眼能看出页面类型。
func TestDiagnoseFramePage(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, ctx := openCheckout(t, f, "nocvc=1", paybrowser.LaunchOptions{})
	time.Sleep(200 * time.Millisecond) // 等卡片 iframe 加载完
	lines := b.Diagnose(ctx)
	hits := lineWith(lines, "选择器可见命中")
	if !strings.Contains(hits, "卡号 0/0/0/0/0|1/0/1/0/1") {
		t.Fatalf("卡号命中数不符: %s", hits)
	}
	if !strings.Contains(hits, "CVC 0/0/0/0/0|0/0/0/0/0") {
		t.Fatalf("CVC 命中数不符: %s", hits)
	}
	if got := lineWith(lines, "可见 frame："); !strings.Contains(got, "localhost:") || !strings.Contains(got, "127.0.0.1:") {
		t.Fatalf("可见 frame 行不符: %s", got)
	}
}

// 开始填卡之后：正文不再出页面（可能有卡号），Fill 失败也不许整页截图。
func TestDiagnoseAfterFillHasNoBodyText(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, ctx := openKind(t, f, stripeCheckout, "nocvc=1", paybrowser.LaunchOptions{ShotDir: t.TempDir()})
	if err := b.Fill(ctx, testCard, testBilling); err == nil {
		t.Fatal("没有 CVC 框时 Fill 应报错")
	}
	lines := b.Diagnose(ctx)
	all := strings.Join(lines, "\n")
	if lineWith(lines, "正文开头：") != "" {
		t.Fatalf("填卡后不应读正文: %s", all)
	}
	if lineWith(lines, "地址：") == "" || lineWith(lines, "选择器可见命中") == "" {
		t.Fatalf("地址和命中数仍要有: %s", all)
	}
	if strings.Contains(all, testCard.Number) {
		t.Fatalf("诊断泄露了卡号: %s", all)
	}
	if _, err := b.Shot(ctx, "after-failed-fill", paybrowser.ShotPage); !errors.Is(err, paybrowser.ErrShotForbidden) {
		t.Fatalf("Shot(ShotPage) = %v，期望 ErrShotForbidden", err)
	}
}

// 页面卡死时也要按 ctx 返回，不能耽误收尾。
func TestDiagnoseBoundedOnBusyPage(t *testing.T) {
	needChrome(t)
	started := make(chan struct{})
	var once sync.Once
	page := amountDiv + laterScript(`fetch('/started').finally(function () { while (true) {} })`)
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
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("页面脚本没有开始执行")
	}
	time.Sleep(300 * time.Millisecond)

	dctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	begin := time.Now()
	lines := b.Diagnose(dctx)
	if d := time.Since(begin); d > 5*time.Second {
		t.Fatalf("诊断用了 %v", d)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "读取超时") {
		t.Fatalf("应说明读取超时: %q", lines)
	}
}

func TestSafeLocation(t *testing.T) {
	cases := map[string]string{
		"https://pay.openai.com/c/pay/cs_live_a1B2c3D4e5F6g7H8i9J0k1?token=x#fid=y": "pay.openai.com/c/pay/cs_live_…",
		"https://checkout.stripe.com/pay?session=abc":                               "checkout.stripe.com/pay",
		"https://user:pw@example.com/a":                                             "example.com/a",
		"about:blank":                                                               "（about）",
		"http://127.0.0.1:8080/checkout.html":                                       "127.0.0.1:8080/checkout.html",
	}
	for in, want := range cases {
		if got := paybrowser.SafeLocation(in); got != want {
			t.Errorf("SafeLocation(%q) = %q，期望 %q", in, got, want)
		}
	}
}
