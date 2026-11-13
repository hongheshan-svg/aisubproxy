package paybrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// 在网页里建订阅结账会话。
//
// 2026-11-13 实测：纯接口调 POST /backend-api/payments/checkout 会被 OpenAI 风控 400 unusual activity（换号、换出口 IP 都一样）；
// 网页自己的请求能拿到会话：它带 Sentinel 令牌（flow=chatgpt_checkout）、一组网页头（originator、x-openai-web-frontend 等）
// 和 checkout_ui_mode=custom。所以建会话这一步必须放进真实浏览器里做。
//
// 本方法打开 chatgpt.com，用页面里的官方 Sentinel SDK 生成令牌，再按网页的请求头与请求体发一次 fetch。
// 只建会话，不填卡、不付款。不伪造令牌、不绕 Cloudflare：令牌由官方 SDK 在页面里生成，页面停在检查页就直接放弃。

// CheckoutRequest 是建订阅结账会话的参数。
type CheckoutRequest struct {
	PlanName string // 套餐名：chatgptplusplan、chatgptprolite、chatgptpro、chatgptpromax
	Country  string // 账单国家（两位，如 US）
	Currency string // 账单币种（三位，如 USD）
}

// CheckoutResult 是建会话返回的关键字段。Tag 为 custom_checkout_session 时没有 URL，链接要用 processor_entity 和会话号拼。
type CheckoutResult struct {
	SessionID       string
	ProcessorEntity string
	Tag             string
	URL             string
}

// ErrCheckoutBlocked 表示没能建会话：页面还停在 Cloudflare 检查页。
var ErrCheckoutBlocked = errors.New("页面停在 Cloudflare 检查页，没有建结账会话")

const (
	// checkoutPageTimeout 是打开 chatgpt.com、等检查页放行、建会话的总时限。
	checkoutPageTimeout = 90 * time.Second
	// checkoutCFWait 是等 Cloudflare 检查页放行的时限。
	checkoutCFWait = 70 * time.Second
	// checkoutSentinelWait 是页面里等 Sentinel SDK 初始化并算出令牌的时间，之后才开始建会话。
	checkoutSentinelWait = 3 * time.Second
)

// CreateCheckout 在页面里建一次订阅结账会话（不付款），返回会话号与结账页类型。
// cookies 是登录会话 cookie；调用方负责关浏览器。
func (b *Browser) CreateCheckout(ctx context.Context, req CheckoutRequest, cookies []Cookie) (CheckoutResult, error) {
	if strings.TrimSpace(req.PlanName) == "" || strings.TrimSpace(req.Country) == "" || strings.TrimSpace(req.Currency) == "" {
		return CheckoutResult{}, errors.New("建结账会话缺少套餐、国家或币种")
	}
	ctx, cancel := context.WithTimeout(ctx, checkoutPageTimeout)
	defer cancel()

	for _, ck := range cookies {
		set := network.SetCookie(ck.Name, ck.Value).WithDomain(ck.Domain).WithPath("/").
			WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteLax)
		if err := b.run(ctx, set); err != nil {
			return CheckoutResult{}, fmt.Errorf("注入 cookie %s 失败: %w", ck.Name, err)
		}
	}
	var navErr string
	err := b.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		var e error
		_, _, navErr, _, e = page.Navigate("https://chatgpt.com/").Do(c)
		return e
	}))
	if err == nil && navErr != "" {
		err = errors.New(navErr)
	}
	if err != nil {
		return CheckoutResult{}, fmt.Errorf("打开 chatgpt.com 失败: %w", err)
	}
	if err := b.waitCFPass(ctx); err != nil {
		return CheckoutResult{}, err
	}
	// 让应用脚本把 Sentinel SDK 与登录态装上；只是等待，页面里没有任何点击。
	select {
	case <-ctx.Done():
		return CheckoutResult{}, ctx.Err()
	case <-time.After(2 * time.Second):
	}

	expr := fmt.Sprintf(checkoutJS,
		mustJSON(map[string]string{
			"plan": req.PlanName, "country": strings.ToUpper(req.Country), "currency": strings.ToUpper(req.Currency),
		}),
		int(checkoutSentinelWait/time.Millisecond))
	var raw string
	await := func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }
	if err := b.run(ctx, chromedp.Evaluate(expr, &raw, await)); err != nil {
		return CheckoutResult{}, fmt.Errorf("在页面里建结账会话失败: %w", err)
	}
	var out struct {
		Session bool   `json:"session"`
		Status  int    `json:"status"`
		Tag     string `json:"tag"`
		SID     string `json:"sid"`
		PE      string `json:"pe"`
		URL     string `json:"url"`
		Detail  string `json:"detail"`
		Err     string `json:"err"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return CheckoutResult{}, errors.New("页面返回的建会话结果无法解析")
	}
	if out.Err != "" {
		return CheckoutResult{}, fmt.Errorf("页面里的建会话脚本出错: %s", out.Err)
	}
	if !out.Session {
		return CheckoutResult{}, ErrUnauthorizedSession
	}
	if out.Status != 200 {
		detail := strings.TrimSpace(out.Detail)
		if detail == "" {
			detail = "没有说明"
		}
		return CheckoutResult{}, fmt.Errorf("建结账会话被拒（HTTP %d）：%s", out.Status, truncateText(detail, 200))
	}
	if strings.TrimSpace(out.SID) == "" {
		return CheckoutResult{}, errors.New("建结账会话没有返回会话号")
	}
	return CheckoutResult{SessionID: out.SID, ProcessorEntity: out.PE, Tag: out.Tag, URL: out.URL}, nil
}

// ErrUnauthorizedSession 表示页面里读不到登录态：会话 cookie 无效或已过期。
var ErrUnauthorizedSession = errors.New("页面里没有登录态，会话可能已失效")

// waitCFPass 等 Cloudflare 检查页放行；超过时限仍停在检查页返回 ErrCheckoutBlocked。
func (b *Browser) waitCFPass(ctx context.Context) error {
	deadline := time.Now().Add(checkoutCFWait)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var title string
		if err := b.run(ctx, chromedp.Evaluate(`document.title`, &title)); err != nil {
			return err
		}
		if title != "" && !isCFChallengeTitle(title) {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrCheckoutBlocked
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func truncateText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// checkoutJS 是页面里的建会话脚本：读登录态、用官方 Sentinel SDK 生成 chatgpt_checkout 令牌，
// 再按网页的请求头与请求体发一次 fetch。返回的 JSON 只有状态码、会话号、结账页类型和错误说明，不含令牌。
const checkoutJS = `(async () => {
  const out = {}; const cfg = %s;
  const sleep = (ms) => new Promise(r => setTimeout(r, ms));
  try {
    const s = await fetch('/api/auth/session', {credentials: 'include'}).then(r => r.json());
    const at = s && s.accessToken; const acct = s && s.account && s.account.id;
    out.session = !!at; out.acct = !!acct;
    if (!at || !acct) return JSON.stringify(out);
    if (!window.SentinelSDK) {
      await new Promise((res, rej) => {
        const sc = document.createElement('script');
        sc.src = '/backend-api/sentinel/sdk.js';
        sc.onload = res; sc.onerror = () => rej(new Error('sentinel sdk load failed'));
        document.head.append(sc);
      });
    }
    if (window.SentinelSDK.init) { try { await window.SentinelSDK.init('chatgpt_checkout'); } catch (e) {} }
    await sleep(%d);
    const tok = await window.SentinelSDK.token('chatgpt_checkout');
    let tel = '[1,null]'; try { tel = await window.SentinelSDK.timing(); } catch (e) {}
    const did = (document.cookie.match(/(?:^|; )oai-did=([^;]+)/) || [])[1] || '';
    const body = {
      plan_name: cfg.plan, checkout_source: 'chatgpt_web_merged', entry_point: 'all_plans_pricing_modal',
      billing_details: {country: cfg.country, currency: cfg.currency},
      supports_saved_payment_link: true, checkout_ui_mode: 'custom', checkout_appearance: 'light',
      cancel_url: location.origin + '/?codex_upgrade_location=sidebar+profile#pricing',
      purchase_attempt_id: crypto.randomUUID(),
    };
    const headers = {
      'Content-Type': 'application/json', 'Authorization': 'Bearer ' + at,
      'ChatGPT-Account-ID': acct, 'X-OpenAI-Expected-Account-Id': acct,
      'OpenAI-Sentinel-Token': tok, 'OAI-Telemetry': tel,
      'OAI-Language': (document.documentElement.lang || navigator.language || 'en-US'),
      'originator': 'Codex Browser', 'x-openai-web-frontend': 'codex_webview',
      'x-openai-checkout-source': 'chatgpt_web_merged', 'x-openai-codex-window-type': 'browser',
      'x-openai-rate-limit-join-workspace': '1', 'x-openai-rejoin-pro-cta': '1',
    };
    if (did) headers['OAI-Device-Id'] = did;
    const r = await fetch('/backend-api/payments/checkout', {method: 'POST', credentials: 'include', headers, body: JSON.stringify(body)});
    out.status = r.status;
    const t = await r.text();
    try {
      const j = JSON.parse(t);
      out.tag = j.tag || ''; out.sid = j.checkout_session_id || ''; out.pe = j.processor_entity || '';
      out.url = j.url || ''; out.detail = (typeof j.detail === 'string' ? j.detail : '').slice(0, 200);
    } catch (e) { out.detail = t.slice(0, 120); }
  } catch (e) { out.err = String(e).slice(0, 200); }
  return JSON.stringify(out);
})()`
