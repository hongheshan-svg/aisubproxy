package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/cookiejar"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"

	"github.com/tuzi/cdk-recharge-system/internal/gptcheck"
)

const (
	defaultBaseURL = "https://chatgpt.com"
	userAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	maxBodyBytes   = 4 << 20
)

// ErrUnauthorized 表示 ChatGPT 返回 401/403，会话或 token 已失效。
var ErrUnauthorized = errors.New("ChatGPT 会话未授权或已失效")

// ErrBlocked 表示请求被 Cloudflare 挑战或拦截（cf-mitigated 头，或 HTML 挑战页），不是会话的问题：
// 出口 IP 被拦，换会话没有用，要检查该地区代理的出口 IP。它不能当作账号状态的任何证据。
var ErrBlocked = errors.New("请求被 Cloudflare 拦截，请检查该地区代理的出口 IP")

// Client 是带 TLS 指纹的 ChatGPT 客户端，每个实例独立连接（可走独立代理），
// 并有自己的设备号和 cookie（见 browser_identity.go）。
type Client struct {
	BaseURL  string
	http     tls_client.HttpClient
	jar      *cookiejar.Jar
	deviceID string
}

// New 创建客户端；proxyURL 为空则直连，仅接受 http、https、socks5。
func New(proxyURL string) (*Client, error) {
	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(30),
		tls_client.WithClientProfile(profiles.Chrome_131),
		tls_client.WithForceHttp1(),
		tls_client.WithNotFollowRedirects(),
	}
	if p := strings.TrimSpace(proxyURL); p != "" {
		u, err := url.Parse(p)
		if err != nil || u.Host == "" {
			return nil, errors.New("代理地址格式无效")
		}
		switch u.Scheme {
		case "http", "https", "socks5":
		default:
			return nil, fmt.Errorf("不支持的代理协议 %q，仅支持 http、https、socks5", u.Scheme)
		}
		opts = append(opts, tls_client.WithProxyUrl(p))
	}
	h, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
	if err != nil {
		return nil, fmt.Errorf("创建 HTTP 客户端失败: %w", err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("创建 cookie 存储失败: %w", err)
	}
	return &Client{BaseURL: defaultBaseURL, http: h, jar: jar, deviceID: newDeviceID()}, nil
}

func (c *Client) do(ctx context.Context, method, path string, headers map[string]string, cookies []Cookie, body []byte) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := fhttp.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", defaultBaseURL+"/")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, ck := range cookies {
		req.AddCookie(&fhttp.Cookie{Name: ck.Name, Value: ck.Value})
	}
	scoped := browserScoped(path) && c.jar != nil
	if scoped {
		c.applyBrowserIdentity(req, path, cookies)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 ChatGPT 失败: %w", err)
	}
	defer resp.Body.Close()
	if scoped {
		c.jar.SetCookies(req.URL, resp.Cookies())
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("读取 ChatGPT 响应失败: %w", err)
	}
	if isCFBlocked(resp.StatusCode, resp.Header.Get("cf-mitigated"), resp.Header.Get("Content-Type"), resp.Header.Get("Server"), b) {
		return nil, fmt.Errorf("%w（HTTP %d）", ErrBlocked, resp.StatusCode)
	}
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return nil, fmt.Errorf("%w（HTTP %d）", ErrUnauthorized, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, newHTTPError(resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Retry-After"), b)
	}
	return b, nil
}

// isCFBlocked 判断响应是不是 Cloudflare 的挑战或拦截。必须在按状态码判 401/403 之前：被挑战时同样是 403，
// 但那不是会话失效。依据：cf-mitigated 头；或 4xx/5xx 的 HTML 页面且带 Cloudflare 的特征。
// ChatGPT 自己的 401/403 是 JSON，不会被误判。
func isCFBlocked(status int, cfMitigated, contentType, server string, body []byte) bool {
	if strings.TrimSpace(cfMitigated) != "" {
		return true
	}
	if status < 400 || !strings.Contains(strings.ToLower(contentType), "text/html") {
		return false
	}
	if strings.Contains(strings.ToLower(server), "cloudflare") {
		return true
	}
	head := body
	if len(head) > 8192 {
		head = head[:8192]
	}
	l := strings.ToLower(string(head))
	return strings.Contains(l, "just a moment") || strings.Contains(l, "challenge-platform") || strings.Contains(l, "cf-chl")
}

func bearer(s Session) map[string]string {
	return map[string]string{"Authorization": "Bearer " + s.AccessToken}
}

// Refresh 用 sessionToken 换取新的 accessToken 并写回 s。
func (c *Client) Refresh(ctx context.Context, s *Session) error {
	b, err := c.do(ctx, fhttp.MethodGet, "/api/auth/session", nil, SessionCookies(s.SessionToken), nil)
	if err != nil {
		return err
	}
	var data struct {
		AccessToken      string `json:"accessToken"`
		AccessTokenSnake string `json:"access_token"`
	}
	if json.Unmarshal(b, &data) != nil {
		return errors.New("刷新会话返回非 JSON")
	}
	token := strings.TrimSpace(data.AccessToken)
	if token == "" {
		token = strings.TrimSpace(data.AccessTokenSnake)
	}
	if token == "" {
		return fmt.Errorf("%w（会话未返回 accessToken）", ErrUnauthorized)
	}
	s.AccessToken = token
	return nil
}

// Account 是默认账号的订阅概况。
type Account struct {
	AccountID             string
	Email                 string
	PlanType              string
	HasActiveSubscription bool

	// 以下是 Codex 点数的 checking 要用的字段，取自 accounts/check（调研 §1.4、§1.5），接口没返回时为空或 nil，调用方按「核实不了」处理。
	Structure       string // account.structure：personal、workspace 等
	BillingCurrency string // entitlement.billing_currency，转成大写
	IsDelinquent    *bool  // entitlement.is_delinquent，其次 last_active_subscription.is_delinquent
	WillRenew       *bool  // last_active_subscription.will_renew，其次 entitlement.will_renew
}

// Account 查询默认账号；无有效订阅时 PlanType 为 "free"。
func (c *Client) Account(ctx context.Context, s Session) (Account, error) {
	b, err := c.do(ctx, fhttp.MethodGet, "/backend-api/accounts/check/v4-2023-04-27?timezone_offset_min=-540", bearer(s), nil, nil)
	if err != nil {
		return Account{}, err
	}
	var data map[string]interface{}
	if json.Unmarshal(b, &data) != nil {
		return Account{}, errors.New("账号查询返回非 JSON")
	}
	acc := gptcheck.DefaultAccount(data)
	info, _ := acc["account"].(map[string]interface{})
	ent, _ := acc["entitlement"].(map[string]interface{})
	out := Account{Email: s.Email}
	out.AccountID = firstString(info["account_id"], info["id"])
	out.PlanType = firstString(info["plan_type"])
	out.HasActiveSubscription, _ = ent["has_active_subscription"].(bool)
	if !out.HasActiveSubscription {
		out.PlanType = "free"
	}
	last, _ := acc["last_active_subscription"].(map[string]interface{})
	out.Structure = strings.ToLower(firstString(info["structure"]))
	out.BillingCurrency = strings.ToUpper(firstString(ent["billing_currency"]))
	out.IsDelinquent = firstBool(ent["is_delinquent"], last["is_delinquent"])
	out.WillRenew = firstBool(last["will_renew"], ent["will_renew"])
	return out, nil
}

// firstBool 取第一个 JSON 布尔值，都不是返回 nil。
func firstBool(vals ...interface{}) *bool {
	for _, v := range vals {
		if b := optBool(v); b != nil {
			return b
		}
	}
	return nil
}

func firstString(vals ...interface{}) string {
	for _, v := range vals {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// Checkout 是创建出的支付会话。
type Checkout struct{ SessionID, URL, Tag, ProcessorEntity string }

// CreateCheckout 创建订阅支付会话。
// planCheckoutBody 是开通订阅建结账会话的请求体；自动付款和免付款探测共用，保证探测的请求和真实下单一致。
func planCheckoutBody(planName, country, currency string) map[string]interface{} {
	return map[string]interface{}{
		"plan_name":       planName,
		"billing_details": map[string]string{"country": country, "currency": currency},
	}
}

func (c *Client) CreateCheckout(ctx context.Context, s Session, planName, country, currency string) (Checkout, error) {
	payload, err := json.Marshal(planCheckoutBody(planName, country, currency))
	if err != nil {
		return Checkout{}, err
	}
	b, err := c.do(ctx, fhttp.MethodPost, "/backend-api/payments/checkout", bearer(s), nil, payload)
	if err != nil {
		return Checkout{}, err
	}
	var data struct {
		SessionID string `json:"checkout_session_id"`
		URL       string `json:"url"`
		Tag       string `json:"tag"`
		// ProcessorEntity 内嵌结账页拼链接用。
		ProcessorEntity string `json:"processor_entity"`
	}
	if json.Unmarshal(b, &data) != nil {
		return Checkout{}, errors.New("创建支付会话返回非 JSON")
	}
	if data.SessionID == "" {
		return Checkout{}, errors.New("创建支付会话未返回 checkout_session_id")
	}
	return Checkout{SessionID: data.SessionID, URL: data.URL, Tag: data.Tag, ProcessorEntity: strings.TrimSpace(data.ProcessorEntity)}, nil
}
