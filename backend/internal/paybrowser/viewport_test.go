package paybrowser

import (
	"testing"

	"github.com/chromedp/cdproto/page"
)

// G3：GetLayoutMetrics 没给 cssVisualViewport 时返回错误，不能空指针 panic（这时已经点过支付）。
func TestChallengeClipNilViewport(t *testing.T) {
	if _, err := challengeClip(10, 20, 110, 220, nil); err == nil {
		t.Fatal("视口为空应返回错误")
	}
	clip, err := challengeClip(10.4, 20, 110, 220.6, &page.VisualViewport{PageX: 5, PageY: 100})
	if err != nil {
		t.Fatal(err)
	}
	if clip.X != 15 || clip.Y != 120 || clip.Width != 100 || clip.Height != 201 || clip.Scale != 1 {
		t.Fatalf("裁剪区不对: %+v", clip)
	}
}

// P2-01：3DS 截图区域超过窗口面积一半就不截。命中的 iframe 可能是铺满窗口、背景半透明的遮罩，
// 这时已经点过支付，下面的卡号框里是完整卡号，会被一起拍进去。
func TestCheckShotArea(t *testing.T) {
	vp := &page.VisualViewport{ClientWidth: 1366, ClientHeight: 900}
	for _, c := range []struct {
		name string
		w, h float64
		ok   bool
	}{
		{"小验证框", 420, 280, true},
		{"正好一半", 683, 900, true},
		{"超过一半", 684, 900, false},
		{"铺满窗口", 1366, 900, false},
		{"比窗口还大", 1366, 3000, false},
	} {
		err := checkShotArea(&page.Viewport{Width: c.w, Height: c.h, Scale: 1}, vp)
		if (err == nil) != c.ok {
			t.Errorf("%s（%v×%v）：checkShotArea = %v，期望能截 = %v", c.name, c.w, c.h, err, c.ok)
		}
	}
	if err := checkShotArea(&page.Viewport{Width: 10, Height: 10, Scale: 1}, &page.VisualViewport{}); err == nil {
		t.Error("浏览器给的窗口大小为 0 时不能截")
	}
}
