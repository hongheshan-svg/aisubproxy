package marswallex

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 为客户端配置。
type Config struct {
	BaseURL           string // 如 https://api.feibeiglobal.com/portal/api
	DeveloperID       string
	APIToken          string
	PrivateKey        *rsa.PrivateKey // 我方私钥，签请求
	PlatformPublicKey *rsa.PublicKey  // 平台公钥，验响应
	// HTTPClient 为空时用 30 秒超时的默认客户端。其 Transport 不得自行重试，
	// 否则「拨号失败 = 未发出」的判断不再成立。重定向一律不跟随（会被覆盖）。
	HTTPClient *http.Client
	Now        func() time.Time
}

// Outcome 为一次调用的结果分类，业务代码只依据它决定下一步。
type Outcome int

const (
	OutcomeSuccess  Outcome = iota + 1 // 200 且验签通过
	OutcomeRejected                    // 400/401/403/404 且验签通过：确定未执行
	OutcomeUnknown                     // 可能已执行：必须查单，重试沿用原单号
	OutcomeNotSent                     // 能证明一个字节都没发出：确定未执行
)

func (o Outcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeRejected:
		return "rejected"
	case OutcomeUnknown:
		return "unknown"
	case OutcomeNotSent:
		return "not_sent"
	}
	return "Outcome(" + strconv.Itoa(int(o)) + ")"
}

// Reason 为分类的具体原因，便于排查。
type Reason string

const (
	ReasonConfig           Reason = "config"
	ReasonDNS              Reason = "dns"
	ReasonDial             Reason = "dial"
	ReasonTimeout          Reason = "timeout"
	ReasonConnection       Reason = "connection"
	ReasonSignatureMissing Reason = "signature_missing"
	ReasonSignatureInvalid Reason = "signature_invalid"
	ReasonStatus           Reason = "status"
	ReasonDecode           Reason = "decode"
)

// Error 为客户端返回的错误。Code/Message 取自响应体（如有）；
// Reason 为 signature_* 时它们未经验签，只供排查。
type Error struct {
	Outcome    Outcome
	Reason     Reason
	HTTPStatus int
	Code       string
	Message    string
	Err        error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("marswallex: ")
	b.WriteString(e.Outcome.String())
	if e.Reason != "" {
		b.WriteString("/" + string(e.Reason))
	}
	if e.HTTPStatus != 0 {
		fmt.Fprintf(&b, " HTTP %d", e.HTTPStatus)
	}
	if e.Code != "" {
		b.WriteString(" code=" + e.Code)
	}
	if e.Message != "" {
		b.WriteString(" message=" + e.Message)
	}
	if e.Err != nil {
		b.WriteString(": " + e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// OutcomeOf 返回错误对应的分类：nil 为成功；非 *Error 或分类无效的一律按未知处理。
func OutcomeOf(err error) Outcome {
	if err == nil {
		return OutcomeSuccess
	}
	var e *Error
	if errors.As(err, &e) && e != nil {
		switch e.Outcome {
		case OutcomeRejected, OutcomeUnknown, OutcomeNotSent:
			return e.Outcome
		}
	}
	return OutcomeUnknown
}

// Client 为 MarsWallex API 客户端，可并发使用。
type Client struct {
	base    string // scheme://host + 去掉尾部 / 与 /v1 的路径（保持转义形式）
	auth    string
	priv    *rsa.PrivateKey
	platPub *rsa.PublicKey
	hc      *http.Client
	now     func() time.Time
}

const (
	defaultTimeout  = 30 * time.Second
	maxResponseBody = 4 << 20
)

func configError(msg string, err error) error {
	return &Error{Outcome: OutcomeNotSent, Reason: ReasonConfig, Message: msg, Err: err}
}

// NewClient 校验并规范化配置。配置不完整或地址不安全时返回 NotSent/config。
func NewClient(cfg Config) (*Client, error) {
	switch {
	case strings.TrimSpace(cfg.BaseURL) == "":
		return nil, configError("缺少 BaseURL", nil)
	case strings.TrimSpace(cfg.DeveloperID) == "":
		return nil, configError("缺少 DeveloperID", nil)
	case strings.TrimSpace(cfg.APIToken) == "":
		return nil, configError("缺少 APIToken", nil)
	case cfg.PrivateKey == nil:
		return nil, configError("缺少商户私钥", nil)
	case cfg.PlatformPublicKey == nil:
		return nil, configError("缺少平台公钥", nil)
	}
	u, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil {
		return nil, configError("BaseURL 无法解析", err)
	}
	if u.Host == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, configError("BaseURL 无效", nil)
	}
	switch u.Scheme {
	case "https":
	case "http":
		// 明文只允许本机（联调代理 / 测试）。
		if h := u.Hostname(); h != "127.0.0.1" && !strings.EqualFold(h, "localhost") {
			return nil, configError("BaseURL 必须使用 https", nil)
		}
	default:
		return nil, configError("BaseURL 必须使用 https", nil)
	}
	p := strings.TrimRight(u.EscapedPath(), "/")
	p = strings.TrimRight(strings.TrimSuffix(p, "/v1"), "/")

	hc := &http.Client{Timeout: defaultTimeout}
	if cfg.HTTPClient != nil {
		cp := *cfg.HTTPClient // 复制，不改调用方的客户端
		hc = &cp
		if hc.Timeout == 0 {
			hc.Timeout = defaultTimeout
		}
	}
	// 不跟随重定向：30x 原样交给分类（归未知），避免 POST 被改成 GET 后把别的结果当成本次结果。
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		base:    u.Scheme + "://" + u.Host + p,
		auth:    "Basic " + base64.StdEncoding.EncodeToString([]byte(cfg.DeveloperID+":"+cfg.APIToken)),
		priv:    cfg.PrivateKey,
		platPub: cfg.PlatformPublicKey,
		hc:      hc,
		now:     now,
	}, nil
}

// buildQuery 按传入顺序拼接成对的键值，跳过空值，键值均用 url.QueryEscape。
func buildQuery(kv ...string) string {
	if len(kv)%2 != 0 {
		panic("marswallex: buildQuery 参数必须成对")
	}
	var b strings.Builder
	for i := 0; i < len(kv); i += 2 {
		if kv[i+1] == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(kv[i]) + "=" + url.QueryEscape(kv[i+1]))
	}
	return b.String()
}

// call 发送 JSON 请求；body 为 nil 时不发请求体。
func (c *Client) call(ctx context.Context, method, path, rawQuery string, body any, out any) error {
	var payload []byte
	contentType := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return configError("请求体序列化失败", err)
		}
		payload, contentType = b, "application/json"
	}
	return c.send(ctx, method, path, rawQuery, contentType, payload, payload, out)
}

// send 为底层发送：签名、发送、验签、分类。signedBody 为参与签名的字节（上传时为 nil）。
// 从不重试。
func (c *Client) send(ctx context.Context, method, path, rawQuery, contentType string, payload []byte, signedBody []byte, out any) error {
	if !strings.HasPrefix(path, "/") {
		return configError("路径必须以 / 开头", nil)
	}
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return configError("构造请求失败", err)
	}
	req.URL.RawQuery = rawQuery

	// 签名 URI 用线上实际发送的转义路径（RequestURI 同样取 EscapedPath）。
	epoch := strconv.FormatInt(c.now().Unix(), 10)
	sig, err := Sign(c.priv, SigningPayload(req.Method, req.URL.EscapedPath(), epoch, signedBody, rawQuery))
	if err != nil {
		return configError("请求签名失败", err)
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("F-Signature", FormatSignatureHeader(epoch, sig))
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		outcome, reason := classifyTransportErr(err)
		return &Error{Outcome: outcome, Reason: reason, Err: err}
	}
	defer resp.Body.Close()
	return c.handleResponse(resp, out)
}

// classifyTransportErr 对网络错误分类：只有能证明一个字节没发出去的才算 NotSent。
// （标准 Transport 仅在请求一个字节都没写出时才会内部重发非幂等请求，故最终的拨号错误仍表示未发出。）
func classifyTransportErr(err error) (Outcome, Reason) {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return OutcomeNotSent, ReasonDNS
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return OutcomeNotSent, ReasonDial
	}
	if isTimeout(err) {
		return OutcomeUnknown, ReasonTimeout
	}
	return OutcomeUnknown, ReasonConnection
}

func isTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err)
}

// handleResponse 先验签再看状态。收到响应后的任何问题都不可能是 NotSent。
func (c *Client) handleResponse(resp *http.Response, out any) error {
	status := resp.StatusCode
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		reason := ReasonConnection
		if isTimeout(err) {
			reason = ReasonTimeout
		}
		return &Error{Outcome: OutcomeUnknown, Reason: reason, HTTPStatus: status, Err: err}
	}
	fail := func(o Outcome, r Reason, err error) error {
		code, message := parseErrorBody(body)
		return &Error{Outcome: o, Reason: r, HTTPStatus: status, Code: code, Message: message, Err: err}
	}

	h := resp.Header.Get("F-Signature")
	if h == "" {
		return fail(OutcomeUnknown, ReasonSignatureMissing, nil)
	}
	epoch, sig, err := ParseSignatureHeader(h)
	if err != nil {
		return fail(OutcomeUnknown, ReasonSignatureInvalid, err)
	}
	// 响应验签原文：epoch 原样（秒或毫秒都不换算）+ "&" + 原始响应体。
	if err := Verify(c.platPub, epoch+"&"+string(body), sig); err != nil {
		return fail(OutcomeUnknown, ReasonSignatureInvalid, err)
	}

	switch status {
	case http.StatusOK:
		if err := decodeSuccess(body, out); err != nil {
			return &Error{Outcome: OutcomeUnknown, Reason: ReasonDecode, HTTPStatus: status, Err: err}
		}
		return nil
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return fail(OutcomeRejected, ReasonStatus, nil)
	}
	// 201/202/409/5xx/30x 等：可能已执行。
	return fail(OutcomeUnknown, ReasonStatus, nil)
}

// wholeBody 标记「整个响应体就是结果」的类型：分页响应 count 与 data 同级，不剥 data 外壳。
type wholeBody interface{ wholeBody() }

func (*Page[T]) wholeBody() {}

// decodeSuccess 把 200 响应解析到 out。out 非 nil 时响应必须带 data 字段，
// 缺失视为无法解析（宁可归未知，也不把空结果当成功）。
func decodeSuccess(body []byte, out any) error {
	if _, ok := out.(wholeBody); ok {
		return json.Unmarshal(body, out)
	}
	var env envelope[json.RawMessage]
	if err := json.Unmarshal(body, &env); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if env.Data == nil {
		return errors.New("marswallex: 响应缺少 data")
	}
	return json.Unmarshal(env.Data, out)
}

// parseErrorBody 尽量从响应体取 code/message，取不到返回空串。
func parseErrorBody(body []byte) (code, message string) {
	var e struct {
		Code    json.RawMessage `json:"code"`
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(body, &e) != nil {
		return "", ""
	}
	return rawText(e.Code), rawText(e.Message)
}

// rawText 字符串取其值，数字等其他字面量取原文，null/缺失为空串。
func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}
