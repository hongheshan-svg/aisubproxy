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
