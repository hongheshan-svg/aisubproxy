package paybrowser_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// fadeInPage 仿照 Stripe 支付页的加载方式：金额一开始就在 DOM 里，但内容区透明、上面盖着骨架，
// showAfterMs 毫秒后去掉骨架并淡入；showAfterMs < 0 表示一直不显示。
func fadeInPage(t *testing.T, showAfterMs int) string {
	t.Helper()
	html := fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"></head><body style="margin:0">
<div id="skeleton" style="position:absolute;top:0;left:0;right:0;bottom:0;background:#eee;z-index:2"></div>
<div id="app" style="opacity:0;transition:opacity 300ms">
  <div style="padding:40px"><span id="OrderDetails-TotalAmount">$20.00</span></div>
</div>
<script>
const d = %d
if (d >= 0) setTimeout(() => { document.getElementById('skeleton').remove(); document.getElementById('app').style.opacity = '1' }, d)
</script></body></html>`, showAfterMs)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestWaitRenderedWaitsForFadeIn(t *testing.T) {
	u := fadeInPage(t, 600)
	b := launch(t, paybrowser.LaunchOptions{})
	ctx := openCtx(t)
	if err := b.Open(ctx, u, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if ok, err := paybrowser.AmountRendered(ctx, b); err != nil || ok {
		t.Fatalf("刚打开时金额还被骨架盖着、内容区透明，不应判为已显示: ok=%v err=%v", ok, err)
	}
	start := time.Now()
	if !b.WaitRendered(ctx, 5*time.Second) {
		t.Fatal("页面约 1 秒内显示完成，WaitRendered 应返回 true")
	}
	if ok, err := paybrowser.AmountRendered(ctx, b); err != nil || !ok {
		t.Fatalf("WaitRendered 返回后金额应已真正显示: ok=%v err=%v", ok, err)
	}
	if el := time.Since(start); el > 4*time.Second {
		t.Fatalf("显示后应尽快返回，实际等了 %v", el)
	}
}

func TestWaitRenderedGivesUpWithoutError(t *testing.T) {
	u := fadeInPage(t, -1)
	b := launch(t, paybrowser.LaunchOptions{})
	ctx := openCtx(t)
	if err := b.Open(ctx, u, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	start := time.Now()
	if b.WaitRendered(ctx, 700*time.Millisecond) {
		t.Fatal("页面一直没显示，WaitRendered 应返回 false")
	}
	if el := time.Since(start); el < 600*time.Millisecond || el > 3*time.Second {
		t.Fatalf("应等满上限后放弃，实际 %v", el)
	}
	if _, _, err := b.ReadAmount(ctx, "USD"); err != nil {
		t.Fatalf("放弃等待后浏览器仍应可用: %v", err)
	}
}
