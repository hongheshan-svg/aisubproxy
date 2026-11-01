package paybrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Cookie 是打开支付页前注入的 cookie。Path 固定为 /，并按原型 lib/session.js 设为 Secure、HttpOnly、SameSite=Lax。
type Cookie struct{ Name, Value, Domain string }

const (
	pageLoadTimeout = 60 * time.Second
	pollInterval    = 250 * time.Millisecond
)

// amountJS 按 Sel.Amount 的顺序找第一个含数字的元素，返回其文字（空白已归一）；找不到返回空串。
const amountJS = `(sels => {
	for (const s of sels) {
		let el
		try { el = document.querySelector(s) } catch (e) { continue }
		if (!el) continue
		const t = (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim()
		if (/\d/.test(t)) return t
	}
	return ''
})`

// Open 先注入 cookie 再打开支付页，等到金额元素出现为止，最多 60 秒。
func (b *Browser) Open(ctx context.Context, url string, cookies []Cookie) error {
	ctx, cancel := context.WithTimeout(ctx, pageLoadTimeout)
	defer cancel()

	for _, c := range cookies {
		set := network.SetCookie(c.Name, c.Value).WithDomain(c.Domain).WithPath("/").
			WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteLax)
		if err := b.run(ctx, set); err != nil {
			return fmt.Errorf("注入 cookie %s 失败: %w", c.Name, err)
		}
	}

	var errText string
	err := b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		_, _, errText, _, err = page.Navigate(url).Do(ctx)
		return err
	}))
	if err == nil && errText != "" {
		err = errors.New(errText)
	}
	if err != nil {
		return openErr("打开支付页失败", err)
	}

	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		if text, _ := b.amountText(ctx); text != "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return openErr("支付页上始终没有出现金额", ctx.Err())
		case <-tick.C:
		}
	}
}

func openErr(what string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s：页面加载超时（%d 秒）", what, int(pageLoadTimeout/time.Second))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// ReadAmount 读取支付页汇总区的应付总额，返回最小单位金额与页面原文。
func (b *Browser) ReadAmount(ctx context.Context, currency string) (minor int64, text string, err error) {
	text, err = b.amountText(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("读取金额失败: %w", err)
	}
	if text == "" {
		return 0, "", errors.New("支付页上没有找到金额")
	}
	minor, err = ParseAmount(text, currency)
	if err != nil {
		return 0, text, err
	}
	return minor, text, nil
}

// amountText 在主页面按 Sel.Amount 取一次金额原文。
func (b *Browser) amountText(ctx context.Context) (string, error) {
	sels, err := json.Marshal(Sel.Amount)
	if err != nil {
		return "", err
	}
	var text string
	err = b.run(ctx, chromedp.Evaluate(fmt.Sprintf("%s(%s)", amountJS, sels), &text))
	return text, err
}
