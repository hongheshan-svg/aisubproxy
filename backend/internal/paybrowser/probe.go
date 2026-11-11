package paybrowser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// 免付款探测用的「只看」能力：打开页面、等待、读文字、截图。
// 这里没有任何点击、键入、填表：探测打开的页面一律不交互。

// ProbePage 是页面的只读摘要。Text 只在内存里供调用方做关键词判断，调用方不得保存。
type ProbePage struct {
	Title string
	URL   string // 当前地址（可能含查询串，调用方必须先缩成域名加路径前缀再输出）
	Text  string // 可见文字，最多 probeTextLimit 个字符
	// CFMarkup 为真表示页面 HTML 里有 Cloudflare 挑战的特征（_cf_chl_opt、cf-chl-；正常页面也带 challenge-platform 的探测脚本，所以不把它当特征）。
	CFMarkup bool
}

const (
	probeLoadTimeout = 40 * time.Second
	probeTextLimit   = 20000
)

// ProbeOpen 先注入 cookie（可为空），再打开 url，等到 load 完成后再等 settle，让挑战页或前端渲染有时间跑完。
// 不等待任何元素出现，也不操作页面。
func (b *Browser) ProbeOpen(ctx context.Context, url string, cookies []Cookie, settle time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, probeLoadTimeout+settle)
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
		return openErr("打开页面失败", err)
	}
	// 等 readyState=complete；挑战页和单页应用可能一直不 complete，超时也继续往下读
	deadline := time.Now().Add(probeLoadTimeout)
	for time.Now().Before(deadline) {
		var state string
		if b.run(ctx, chromedp.Evaluate(`document.readyState`, &state)) == nil && state == "complete" {
			break
		}
		if err := sleep(ctx, pollInterval); err != nil {
			return err
		}
	}
	if settle <= 0 {
		return nil
	}
	return sleep(ctx, settle)
}

// ProbeRead 读当前页面的标题、地址、可见文字，以及 HTML 里有没有 Cloudflare 挑战特征。只读。
func (b *Browser) ProbeRead(ctx context.Context) (ProbePage, error) {
	var p ProbePage
	var cf bool
	err := b.run(ctx,
		chromedp.Title(&p.Title),
		chromedp.Location(&p.URL),
		chromedp.Evaluate(`(document.body ? (document.body.innerText || '') : '')`, &p.Text),
		chromedp.Evaluate(`(function(){var h=(document.documentElement?document.documentElement.innerHTML:'').slice(0,200000).toLowerCase();return h.indexOf('_cf_chl_opt')>=0||h.indexOf('cf-chl-')>=0})()`, &cf),
	)
	if err != nil {
		return ProbePage{}, fmt.Errorf("读取页面失败: %w", err)
	}
	p.CFMarkup = cf
	p.Title = strings.TrimSpace(p.Title)
	if r := []rune(p.Text); len(r) > probeTextLimit {
		p.Text = string(r[:probeTextLimit])
	}
	return p, nil
}

// IsCFChallengePage 判断页面是不是 Cloudflare 的挑战或拦截页：标题、可见文字或 HTML 特征任一命中。
func IsCFChallengePage(p ProbePage) bool {
	if p.CFMarkup || isCFChallengeTitle(p.Title) {
		return true
	}
	t := strings.ToLower(p.Text)
	for _, w := range []string{"verify you are human", "checking your browser", "checking if the site connection is secure", "performing security verification", "enable javascript and cookies to continue"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}
