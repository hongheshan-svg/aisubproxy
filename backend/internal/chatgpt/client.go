package chatgpt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"sort"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/cookiejar"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"

	"github.com/tuzi/cdk-recharge-system/internal/gptcheck"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
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
	secrets  []string // 代理地址里的账号密码等，返回错误前换成 ***（见 scrubErr）
}

// New 创建客户端；proxyURL 为空则直连，仅接受 http、https、socks5、socks5h（两者都由代理端解析域名）。
func New(proxyURL string) (*Client, error) {
	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(30),
		tls_client.WithClientProfile(profiles.Chrome_131),
		tls_client.WithForceHttp1(),
		tls_client.WithNotFollowRedirects(),
	}
	var secrets []string
	if p := strings.TrimSpace(proxyURL); p != "" {
		secrets = proxySecrets(p)
		u, err := url.Parse(p)
		if err != nil || u.Host == "" {
			return nil, errors.New("代理地址格式无效")
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, fmt.Errorf("不支持的代理协议 %q，仅支持 http、https、socks5、socks5h", u.Scheme)
		}
		opts = append(opts, tls_client.WithProxyUrl(p))
	}
	h, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
	if err != nil {
		return nil, scrubErr(fmt.Errorf("创建 HTTP 客户端失败: %w", err), secrets)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("创建 cookie 存储失败: %w", err)
	}
	return &Client{BaseURL: defaultBaseURL, http: h, jar: jar, deviceID: newDeviceID(), secrets: secrets}, nil
}

// proxySecrets 列出代理地址里不能出现在错误文本里的写法：整条地址、账号密码（原样和 URL 编码的）、单独的密码。
// 没有账号密码的代理返回 nil：地址本身不是秘密，留着便于排查。不足 4 个字符的写法不替换，免得把错误说明换得面目全非。
func proxySecrets(p string) []string {
	u, err := url.Parse(p)
	if err != nil || u.User == nil {
		return nil
	}
	user := u.User.Username()
	pw, hasPw := u.User.Password()
	cands := []string{p, u.String(), u.User.String()}
	if hasPw {
		cands = append(cands, user+":"+pw, pw, url.QueryEscape(pw), url.PathEscape(pw))
	}
	var out []string
	for _, c := range cands {
		if len(c) >= 4 {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) }) // 长的先换，免得被短的拆开
	return out
}

// scrubbedError 是换掉了代理账号密码的错误：Error() 只给换过的文本，Unwrap 指向原错误，errors.Is / errors.As 照常可用。
type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

// scrubErr 把错误文本里的 secrets 换成 ***；没有可换的时原样返回。
func scrubErr(err error, secrets []string) error {
	if err == nil || len(secrets) == 0 {
		return err
	}
	msg := err.Error()
	red := msg
	for _, s := range secrets {
		red = strings.ReplaceAll(red, s, "***")
	}
	if red == msg {
		return err
	}
	return &scrubbedError{msg: red, err: err}
}

// ErrInternal 表示请求过程中 HTTP 库内部出错（panic，或返回了空响应），不是 ChatGPT 的回应，不能当作账号状态的证据。
var ErrInternal = errors.New("ChatGPT 请求内部错误")

// do 发一次请求。tls-client 在异常响应上可能 panic：这里自己兜住转成 ErrInternal（不开它的 WithCatchPanics，
// 那样 Do 会返回 (nil, nil)，后面照样空指针）。panic 的值可能带请求里的会话，日志只记类型和位置。
func (c *Client) do(ctx context.Context, method, path string, headers map[string]string, cookies []Cookie, body []byte) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[chatgpt] 请求内部错误（panic），已兜住：%s", safego.Describe(r))
			out, err = nil, ErrInternal
		}
	}()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := fhttp.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return nil, scrubErr(err, c.secrets)
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
		return nil, scrubErr(fmt.Errorf("请求 ChatGPT 失败: %w", err), c.secrets) // 底层错误可能带代理地址
	}
	if resp == nil || resp.Body == nil {
		return nil, ErrInternal
	}
	defer resp.Body.Close()
	if scoped {
		c.jar.SetCookies(req.URL, resp.Cookies())
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, scrubErr(fmt.Errorf("读取 ChatGPT 响应失败: %w", err), c.secrets)
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
	// tag 和 processor_entity 在源头去掉首尾空白（设计 §5.2），和点数那条路（billing.go）一致
	return Checkout{SessionID: data.SessionID, URL: data.URL, Tag: strings.TrimSpace(data.Tag), ProcessorEntity: strings.TrimSpace(data.ProcessorEntity)}, nil
}
