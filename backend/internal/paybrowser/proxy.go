package paybrowser

import (
	"errors"
	"net/url"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/chromedp"

	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// parseProxy 把代理地址拆成 --proxy-server 用的 scheme://host:port 与账号密码。
// 带账号密码的 socks5 Chrome 认不了，这时返回 socks 非 nil（上游地址），由调用方起本机中转（见 socks_relay.go）。
// socks5h 与 socks5 一样（Chrome 和中转都把域名交给代理端解析），交给 Chrome 时写成 socks5。
// 错误消息不含原地址，避免泄露密码。
func parseProxy(raw string) (server string, auth *proxyAuth, socks *url.URL, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil, nil, nil
	}
	bad := errors.New("代理地址格式无效，须为 http、https、socks5 或 socks5h 地址")
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", nil, nil, bad
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https":
	case "socks5", "socks5h":
		if u.Port() == "" {
			return "", nil, nil, errors.New("socks5 代理地址要写端口")
		}
		scheme = "socks5"
	default:
		return "", nil, nil, bad
	}
	server = scheme + "://" + u.Host
	if u.User == nil {
		return server, nil, nil, nil
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	if user == "" && pass == "" {
		return server, nil, nil, nil
	}
	if scheme == "socks5" {
		return "", nil, &url.URL{Scheme: scheme, Host: u.Host, User: u.User}, nil
	}
	return server, newProxyAuth(user, pass), nil, nil
}

func newProxyAuth(user, pass string) *proxyAuth {
	return &proxyAuth{user: user, pass: pass, answered: map[fetch.RequestID]bool{}}
}

// proxyAuth 保存代理账号密码，并记下已应答过的请求，凭据被拒时不再重复提交。
type proxyAuth struct {
	user, pass string
	mu         sync.Mutex
	answered   map[fetch.RequestID]bool
}

func (a *proxyAuth) response(ev *fetch.EventAuthRequired) *fetch.AuthChallengeResponse {
	cancel := &fetch.AuthChallengeResponse{Response: fetch.AuthChallengeResponseResponseCancelAuth}
	// 只把凭据交给代理，绝不交给网站自己的认证
	if ev.AuthChallenge == nil || ev.AuthChallenge.Source != fetch.AuthChallengeSourceProxy {
		return cancel
	}
	a.mu.Lock()
	again := a.answered[ev.RequestID]
	a.answered[ev.RequestID] = true
	a.mu.Unlock()
	if again {
		return cancel
	}
	return &fetch.AuthChallengeResponse{
		Response: fetch.AuthChallengeResponseResponseProvideCredentials,
		Username: a.user,
		Password: a.pass,
	}
}

// answerProxyAuth 处理 Fetch 事件：普通请求直接放行，代理认证时应答账号密码。
// 事件回调里不能同步发 CDP 命令，所以在协程中执行。
func (b *Browser) answerProxyAuth(a *proxyAuth) func(any) {
	return func(ev any) {
		switch ev := ev.(type) {
		case *fetch.EventRequestPaused:
			safego.Go("代理放行请求", func() { b.runQuiet(fetch.ContinueRequest(ev.RequestID)) })
		case *fetch.EventAuthRequired:
			safego.Go("代理认证应答", func() { b.runQuiet(fetch.ContinueWithAuth(ev.RequestID, a.response(ev))) })
		}
	}
}

// runQuiet 执行动作并忽略错误（浏览器关闭时会失败，属正常）。
func (b *Browser) runQuiet(action chromedp.Action) { _ = chromedp.Run(b.ctx, action) }
