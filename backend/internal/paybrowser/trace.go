package paybrowser

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/chromedp/chromedp"
)

// TraceURL 是浏览器出口检测打开的地址，与服务器直连检测（chatgpt.Client.Trace）是同一个：
// Cloudflare 按出口给出 ip=、loc= 等。测试里换成本机仿造页。
var TraceURL = "https://chatgpt.com/cdn-cgi/trace"

// TraceTimeout 是浏览器出口检测的总时限（含启动 Chrome）。
const TraceTimeout = 45 * time.Second

var netErrCode = regexp.MustCompile(`net::ERR_[A-Z0-9_]+`)

// Trace 按付款时完全一样的代理设置启动一个 Chrome（带账号密码的 socks5 也经本机中转），打开 TraceURL，返回页面正文。
// 用来确认付款浏览器那条路也走得通：服务器直连检测通过，不代表浏览器那条路通。
// 找不到 Chrome 时错误包着 ErrNoChrome。返回的错误都是固定说明，最多带 Chrome 的 net::ERR_ 错误码，不含代理地址与账号密码。
func Trace(ctx context.Context, proxyURL string) (string, error) {
	return trace(ctx, LaunchOptions{ProxyURL: proxyURL})
}

func trace(ctx context.Context, o LaunchOptions) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, TraceTimeout)
	defer cancel()
	b, err := Launch(ctx, o)
	if err != nil {
		switch {
		case errors.Is(err, ErrNoChrome):
			return "", ErrNoChrome
		case ctx.Err() != nil:
			return "", errors.New("启动浏览器超时")
		}
		return "", errors.New("浏览器启动失败，请先运行「浏览器自检」排查")
	}
	defer b.Close()
	var text string
	err = b.run(ctx,
		chromedp.Navigate(TraceURL),
		chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &text),
	)
	if err == nil {
		return text, nil
	}
	if r := b.proxyFailReason(); r != "" {
		return "", errors.New(r)
	}
	if ctx.Err() != nil {
		return "", errors.New("请求超时，代理可能不通或太慢")
	}
	if code := netErrCode.FindString(err.Error()); code != "" {
		return "", fmt.Errorf("浏览器打不开检测页（%s）", code)
	}
	return "", errors.New("浏览器打不开检测页")
}

// proxyFailReason 返回本机中转连上游失败的原因；没用中转或没失败过为空。
func (b *Browser) proxyFailReason() string {
	if b.relay == nil {
		return ""
	}
	return b.relay.failReason()
}
