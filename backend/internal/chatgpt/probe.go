package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
)

// 免付款探测用的 ChatGPT 客户端。
//
// 探测只允许下面白名单里的「方法 + 路径」，其余一律在发出前拒绝（ErrProbeForbidden）。
// 任何会改账号或付款的接口都不在其中：/subscriptions/update、/subscriptions/cancel、/subscriptions/renew、
// /payments/payment_method*（POST、DELETE）、/payments/checkout/confirm 等。
// 唯一会 POST 的是 /payments/checkout，且请求体只由本文件内的两个函数构造，只建会话、不付款。

// ErrProbeForbidden 表示请求不在探测白名单内，没有发出。
var ErrProbeForbidden = errors.New("探测白名单之外的请求，已拒绝")

// 探测白名单的各条目（方法 + 路径，路径不含查询串）。
const (
	probePathSession       = "/api/auth/session"
	probePathAccountCheck  = "/backend-api/accounts/check/v4-2023-04-27"
	probePathSubscriptions = "/backend-api/subscriptions"
	probePathUpdatePreview = "/backend-api/subscriptions/update/preview"
	probePathPaymentMethod = "/backend-api/payments/payment_methods"
	probePathPortal        = "/backend-api/payments/customer_portal"
	probePathUsage         = "/backend-api/wham/usage"
	probePathCheckout      = "/backend-api/payments/checkout"
)

// ProbeAllowlist 是探测允许的全部请求，写死，不可配置。
var ProbeAllowlist = []string{
	"GET " + probePathSession,
	"GET " + probePathAccountCheck,
	"GET " + probePathSubscriptions,
	"GET " + probePathUpdatePreview,
	"GET " + probePathPaymentMethod,
	"GET " + probePathPortal,
	"GET " + probePathUsage,
	"POST " + probePathCheckout,
}

var probeAllowed = func() map[string]bool {
	m := make(map[string]bool, len(ProbeAllowlist))
	for _, e := range ProbeAllowlist {
		m[e] = true
	}
	return m
}()

// ProbeCheckAllowed 判断「方法 + 路径」是否在白名单内。要求精确匹配：大小写、末尾斜杠、查询串、百分号编码、
// 路径穿越都不放过，免得 `/payments/checkout/confirm`、`/payments/checkout/../x` 之类的变体混进来。
func ProbeCheckAllowed(method, path string) error {
	if strings.ContainsAny(path, "?#%\\") || strings.Contains(path, "..") || strings.Contains(path, "//") || !probeAllowed[method+" "+path] {
		return fmt.Errorf("%w：%s %s", ErrProbeForbidden, method, probeShowPath(path))
	}
	return nil
}

func probeShowPath(p string) string {
	if len(p) > 80 {
		return p[:80] + "…"
	}
	return p
}

// Probe 是只能发白名单请求的客户端，包着一个普通 Client（共用 TLS 指纹与代理）。
type Probe struct{ c *Client }

// NewProbe 包装一个 Client。
func NewProbe(c *Client) *Probe { return &Probe{c: c} }

// accountHeaders 是账号作用域的接口都带的头：Bearer，加上前端附带的两个账号头（headerChatGPTAccount、headerExpectedAccount，
// 和真实下单共用 billing.go 里的常量，名字的来历见那里）。账号为空时只带 Bearer。
func accountHeaders(s Session, accountID string) map[string]string {
	h := bearer(s)
	if accountID != "" {
		h[headerChatGPTAccount] = accountID
		h[headerExpectedAccount] = accountID
	}
	return h
}

// request 先过白名单再发请求。query 只用于 GET。
func (p *Probe) request(ctx context.Context, method, path string, query url.Values, headers map[string]string, body []byte) ([]byte, error) {
	if err := ProbeCheckAllowed(method, path); err != nil {
		return nil, err
	}
	full := path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	return p.c.do(ctx, method, full, headers, nil, body)
}

// Refresh 用 sessionToken 换新的 accessToken（GET /api/auth/session）。
func (p *Probe) Refresh(ctx context.Context, s *Session) error {
	if err := ProbeCheckAllowed(fhttp.MethodGet, probePathSession); err != nil {
		return err
	}
	return p.c.Refresh(ctx, s)
}

// 探测支持读取的路径（只读 GET）。
const (
	ProbeAccountCheck   = probePathAccountCheck
	ProbeSubscriptions  = probePathSubscriptions
	ProbeUpdatePreview  = probePathUpdatePreview
	ProbePaymentMethod  = probePathPaymentMethod
	ProbeCustomerPortal = probePathPortal
	ProbeUsage          = probePathUsage
)

// GetJSON 对白名单内的路径发 GET，返回 JSON 对象。accountID 非空时带账号头。
func (p *Probe) GetJSON(ctx context.Context, s Session, path string, query url.Values, accountID string) (map[string]interface{}, error) {
	if query == nil {
		query = url.Values{}
	}
	if path == probePathAccountCheck {
		query.Set("timezone_offset_min", "-540")
	}
	headers := accountHeaders(s, accountID)
	if path == probePathUsage {
		// 与 CreditBalance 一致：带价格选择头，账号头只用 ChatGPT-Account-ID
		headers = bearer(s)
		headers["x-openai-codex-pricing-chooser"] = "1"
		if id := strings.TrimSpace(accountID); id != "" {
			headers[headerChatGPTAccount] = id
		}
	}
	b, err := p.request(ctx, fhttp.MethodGet, path, query, headers, nil)
	if err != nil {
		return nil, err
	}
	var data map[string]interface{}
	if json.Unmarshal(b, &data) != nil || data == nil {
		return nil, errors.New("返回不是 JSON 对象")
	}
	return data, nil
}

// CheckoutShape 是建会话返回的形态。
type CheckoutShape string

const (
	ShapeURL    CheckoutShape = "url"
	ShapeCustom CheckoutShape = "custom_checkout_session"
	ShapeOther  CheckoutShape = "other"
)

// ProbeCheckout 是建结账会话的返回摘要，已脱敏：不含完整链接、会话号。
type ProbeCheckout struct {
	Shape      CheckoutShape
	Host       string // 链接的域名（仅 url 形态）
	PathPrefix string // 链接路径的前两段
	Processor  string // processor_entity，如 openai_llc
	SessionPfx string // 会话号前缀（到第二个下划线或前 8 位）
	Currency   string // 返回里有币种字段时给出
	Keys       []string
}

// ProbeURLParts 把链接缩成域名和路径前缀（最多两段），丢掉查询串、片段、用户信息。
func ProbeURLParts(raw string) (host, prefix string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) > 2 {
		segs = segs[:2]
	}
	if len(segs) == 1 && segs[0] == "" {
		return strings.ToLower(u.Hostname()), "/"
	}
	return strings.ToLower(u.Hostname()), "/" + strings.Join(segs, "/") + "/…"
}

func sessionIDPrefix(id string) string {
	if id == "" {
		return ""
	}
	if i := strings.Index(id, "_"); i >= 0 {
		if j := strings.Index(id[i+1:], "_"); j >= 0 {
			return id[:i+1+j+1]
		}
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func parseProbeCheckout(b []byte) (ProbeCheckout, error) {
	var data map[string]interface{}
	if json.Unmarshal(b, &data) != nil || data == nil {
		return ProbeCheckout{}, errors.New("建会话返回的不是 JSON 对象")
	}
	out := ProbeCheckout{Shape: ShapeOther}
	for k := range data {
		out.Keys = append(out.Keys, k)
	}
	sort.Strings(out.Keys)
	tag := firstString(data["tag"])
	rawURL := firstString(data["url"])
	switch {
	case tag == "custom_checkout_session":
		out.Shape = ShapeCustom
	case rawURL != "":
		out.Shape = ShapeURL
		out.Host, out.PathPrefix = ProbeURLParts(rawURL)
	}
	out.Processor = firstString(data["processor_entity"])
	out.SessionPfx = sessionIDPrefix(firstString(data["checkout_session_id"], data["session_id"]))
	out.Currency = strings.ToUpper(firstString(data["currency"], data["billing_currency"]))
	return out, nil
}

// ModeCheckout 为没有订阅的账号建一次 Plus 开通结账会话（只建会话，不付款）。
// 请求体和请求头与自动付款的 CreateCheckout 完全相同。
func (p *Probe) ModeCheckout(ctx context.Context, s Session, planName, country, currency string) (ProbeCheckout, error) {
	return p.postCheckout(ctx, bearer(s), planCheckoutBody(planName, country, currency))
}

// probeCreditQuantity 是探测建点数会话用的数量（最小档 250 点）。
const probeCreditQuantity = 250

// CreditCheckout 为有生效订阅的账号建一次 250 点的点数结账会话（只建会话，不付款）。
// 请求体和请求头与自动付款的 CreateCreditCheckout 完全相同。
func (p *Probe) CreditCheckout(ctx context.Context, s Session, accountID string) (ProbeCheckout, error) {
	return p.postCheckout(ctx, accountScopedHeaders(s, accountID, true), creditCheckoutBody(accountID, probeCreditQuantity))
}

func (p *Probe) postCheckout(ctx context.Context, headers map[string]string, body map[string]interface{}) (ProbeCheckout, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return ProbeCheckout{}, err
	}
	b, err := p.request(ctx, fhttp.MethodPost, probePathCheckout, nil, headers, payload)
	if err != nil {
		return ProbeCheckout{}, err
	}
	return parseProbeCheckout(b)
}
