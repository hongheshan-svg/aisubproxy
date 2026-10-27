package marswallex

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type pingOut struct {
	X string `json:"x"`
}

// ping 发一个只读调用驱动客户端。
func ping(c *Client, out any) error {
	return c.call(context.Background(), "GET", "/v1/ping", "a=1&b=2", nil, out)
}

// asError 断言 err 为 *Error 且分类、原因符合预期。
func asError(t *testing.T, err error, outcome Outcome, reason Reason) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v (%T), want *Error", err, err)
	}
	if e.Outcome != outcome || e.Reason != reason {
		t.Fatalf("got %s/%s (%v), want %s/%s", e.Outcome, e.Reason, err, outcome, reason)
	}
	if OutcomeOf(err) != outcome {
		t.Fatalf("OutcomeOf = %s, want %s", OutcomeOf(err), outcome)
	}
	return e
}

func TestCallSuccessSignedResponse(t *testing.T) {
	m, c := newMockPlatform(t)
	m.Reply = func(w http.ResponseWriter, r *http.Request) { m.signedJSON(w, 200, `{"data":{"x":"1"}}`) }
	var out pingOut
	if err := ping(c, &out); err != nil {
		t.Fatalf("err = %v", err)
	}
	if out.X != "1" {
		t.Errorf("out.X = %q", out.X)
	}
	r := m.Last
	if r.Method != "GET" || r.URL.Path != "/portal/api/v1/ping" || r.URL.RawQuery != "a=1&b=2" {
		t.Errorf("请求 = %s %s ? %s", r.Method, r.URL.Path, r.URL.RawQuery)
	}
	if r.Header.Get("Accept") != "application/json" || r.Header.Get("Content-Type") != "" || len(m.LastBody) != 0 {
		t.Errorf("无体 GET：Accept=%q Content-Type=%q body=%q", r.Header.Get("Accept"), r.Header.Get("Content-Type"), m.LastBody)
	}

	// 带 JSON 体：发送与签名用同一份字节，Content-Type 为 application/json。
	if err := c.call(context.Background(), "POST", "/v1/thing", "", map[string]string{"k": "v"}, nil); err != nil {
		t.Fatalf("POST err = %v", err)
	}
	if string(m.LastBody) != `{"k":"v"}` || m.Last.Header.Get("Content-Type") != "application/json" {
		t.Errorf("POST body=%q Content-Type=%q", m.LastBody, m.Last.Header.Get("Content-Type"))
	}

	// 路径参数含转义字符：按线上实际的转义路径签名，mock 用 EscapedPath 验签。
	if err := c.call(context.Background(), "GET", "/v1/item/a%2Fb", "", nil, nil); err != nil {
		t.Fatalf("escaped path err = %v", err)
	}
	if got := m.Last.URL.EscapedPath(); got != "/portal/api/v1/item/a%2Fb" {
		t.Errorf("EscapedPath = %q", got)
	}

	// 分页响应 count 与 data 同级：*Page 整体解析，不剥 data。
	m.Reply = func(w http.ResponseWriter, r *http.Request) {
		m.signedJSON(w, 200, `{"count":"2","data":[{"x":"1"},{"x":"2"}]}`)
	}
	var page Page[pingOut]
	if err := ping(c, &page); err != nil {
		t.Fatalf("page err = %v", err)
	}
	if page.Count != 2 || len(page.Data) != 2 || page.Data[1].X != "2" {
		t.Errorf("page = %+v", page)
	}
}

func TestCallSuccessMillisecondSignature(t *testing.T) {
	m, c := newMockPlatform(t)
	m.Reply = func(w http.ResponseWriter, r *http.Request) { m.signedJSONMillis(w, 200, `{"data":{"x":"1"}}`) }
	var out pingOut
	if err := ping(c, &out); err != nil || out.X != "1" {
		t.Fatalf("err = %v, out = %+v", err, out)
	}
}

func TestCallUnsignedOKIsUnknown(t *testing.T) {
	m, c := newMockPlatform(t)
	m.Reply = func(w http.ResponseWriter, r *http.Request) { plainJSON(w, 200, `{"data":{"x":"1"}}`) }
	var out pingOut
	e := asError(t, ping(c, &out), OutcomeUnknown, ReasonSignatureMissing)
	if e.HTTPStatus != 200 {
		t.Errorf("HTTPStatus = %d", e.HTTPStatus)
	}
	if out.X != "" {
		t.Errorf("未验签的数据不应写入 out: %+v", out)
	}
}

func TestCallBadSignatureIsUnknown(t *testing.T) {
	m, c := newMockPlatform(t)
	ourPriv, platPriv := testKeys(t)
	epoch := strconv.FormatInt(time.Now().Unix(), 10)
	sigOf := func(payload string) string {
		s, err := Sign(platPriv, payload)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	wrongKey, err := Sign(ourPriv, epoch+`&{"data":{"x":"1"}}`) // 不是平台私钥
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"签的是别的响应体": FormatSignatureHeader(epoch, sigOf(epoch+`&{"data":{"x":"2"}}`)),
		"签的是别的时间戳": FormatSignatureHeader(epoch+"1", sigOf(epoch+`&{"data":{"x":"1"}}`)),
		"不是平台私钥":   FormatSignatureHeader(epoch, wrongKey),
		"签名头格式错误":  "garbage",
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			m.Reply = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("F-Signature", header)
				plainJSON(w, 200, `{"data":{"x":"1"}}`)
			}
			var out pingOut
			asError(t, ping(c, &out), OutcomeUnknown, ReasonSignatureInvalid)
			if out.X != "" {
				t.Errorf("验签失败的数据不应写入 out: %+v", out)
			}
		})
	}
}

func TestCallSignedRejected(t *testing.T) {
	m, c := newMockPlatform(t)
	for _, status := range []int{400, 401, 403, 404} {
		m.Reply = func(w http.ResponseWriter, r *http.Request) {
			m.signedJSON(w, status, `{"code":"PARAM_ERROR","message":"bad"}`)
		}
		e := asError(t, ping(c, &pingOut{}), OutcomeRejected, ReasonStatus)
		if e.HTTPStatus != status || e.Code != "PARAM_ERROR" || e.Message != "bad" {
			t.Errorf("status %d: got %+v", status, e)
		}
	}
}

func TestCallUnsignedRejectionIsUnknown(t *testing.T) {
	m, c := newMockPlatform(t)
	m.Reply = func(w http.ResponseWriter, r *http.Request) {
		plainJSON(w, 400, `{"code":"PARAM_ERROR","message":"bad"}`)
	}
	e := asError(t, ping(c, &pingOut{}), OutcomeUnknown, ReasonSignatureMissing)
	if e.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d", e.HTTPStatus)
	}
}

func TestCallAmbiguousStatusesAreUnknown(t *testing.T) {
	m, c := newMockPlatform(t)
	for _, status := range []int{201, 202, 409, 500} {
		m.Reply = func(w http.ResponseWriter, r *http.Request) {
			m.signedJSON(w, status, `{"code":"SYS_ERROR","message":"try later"}`)
		}
		e := asError(t, ping(c, &pingOut{}), OutcomeUnknown, ReasonStatus)
		if e.HTTPStatus != status || e.Code != "SYS_ERROR" || e.Message != "try later" {
			t.Errorf("status %d: got %+v", status, e)
		}
	}

	// 重定向不跟随：跟随后的 200 不能被当成本次请求的结果。
	m.Reply = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/portal/api/v1/elsewhere" {
			m.signedJSON(w, 200, `{"data":{"x":"1"}}`)
			return
		}
		w.Header().Set("Location", "/portal/api/v1/elsewhere")
		m.signedJSON(w, http.StatusFound, `{}`)
	}
	var out pingOut
	e := asError(t, ping(c, &out), OutcomeUnknown, ReasonStatus)
	if e.HTTPStatus != http.StatusFound || m.Last.URL.Path != "/portal/api/v1/ping" || out.X != "" {
		t.Errorf("重定向被跟随了: status=%d last=%s out=%+v", e.HTTPStatus, m.Last.URL.Path, out)
	}
}

func TestCallUndecodableOKIsUnknown(t *testing.T) {
	m, c := newMockPlatform(t)
	for _, body := range []string{`not json`, `{"code":"OK"}`} {
		m.Reply = func(w http.ResponseWriter, r *http.Request) { m.signedJSON(w, 200, body) }
		e := asError(t, ping(c, &pingOut{}), OutcomeUnknown, ReasonDecode)
		if e.HTTPStatus != 200 {
			t.Errorf("%q: HTTPStatus = %d", body, e.HTTPStatus)
		}
	}
}

func TestCallTimeoutIsUnknown(t *testing.T) {
	m, _ := newMockPlatform(t)
	m.Reply = func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done(): // 客户端超时断开后立即返回，测试不必等满
		}
		m.signedJSON(w, 200, `{"data":{"x":"1"}}`)
	}
	cfg := m.cfg
	cfg.HTTPClient = &http.Client{Timeout: 100 * time.Millisecond}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	asError(t, ping(c, &pingOut{}), OutcomeUnknown, ReasonTimeout)
}

func TestCallConnectionDropIsUnknown(t *testing.T) {
	// reset=true 时发 RST：客户端读到 *net.OpError{Op:"read"}，不能因为是 OpError 就当成未发出。
	// 每轮用新的 mock：断连时服务端不写响应，复用 mock 改 Reply 会被 -race 判为数据竞争。
	for _, reset := range []bool{false, true} {
		m, c := newMockPlatform(t)
		m.Reply = func(w http.ResponseWriter, r *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			if tc, ok := conn.(*net.TCPConn); ok && reset {
				tc.SetLinger(0)
			}
			conn.Close()
		}
		asError(t, ping(c, &pingOut{}), OutcomeUnknown, ReasonConnection)
	}
}

func TestCallConnectionRefusedIsNotSent(t *testing.T) {
	m, _ := newMockPlatform(t)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	cfg := m.cfg
	cfg.BaseURL = closed.URL + "/portal/api"
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	asError(t, ping(c, &pingOut{}), OutcomeNotSent, ReasonDial)
}

func TestCallDNSFailureIsNotSent(t *testing.T) {
	m, _ := newMockPlatform(t)
	cfg := m.cfg
	cfg.BaseURL = "https://no-such-host.invalid/portal/api"
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	asError(t, ping(c, &pingOut{}), OutcomeNotSent, ReasonDNS)
}

func TestNewClientRejectsIncompleteConfig(t *testing.T) {
	m, _ := newMockPlatform(t)
	cases := map[string]func(*Config){
		"缺 BaseURL":         func(c *Config) { c.BaseURL = "" },
		"缺 DeveloperID":     func(c *Config) { c.DeveloperID = "" },
		"缺 APIToken":        func(c *Config) { c.APIToken = " " },
		"缺私钥":               func(c *Config) { c.PrivateKey = nil },
		"缺平台公钥":             func(c *Config) { c.PlatformPublicKey = nil },
		"http 非本机":          func(c *Config) { c.BaseURL = "http://api.example.com/portal/api" },
		"非 http(s)":         func(c *Config) { c.BaseURL = "ftp://api.example.com/portal/api" },
		"无 host":            func(c *Config) { c.BaseURL = "https:///portal/api" },
		"相对地址":              func(c *Config) { c.BaseURL = "/portal/api" },
		"带查询串":              func(c *Config) { c.BaseURL = "https://api.example.com/portal/api?x=1" },
		"无法解析":              func(c *Config) { c.BaseURL = "https://api.example.com:bad/portal/api" },
		"http 冒充 localhost": func(c *Config) { c.BaseURL = "http://localhost.evil.com/portal/api" },
	}
	for name, mutate := range cases {
		cfg := m.cfg
		mutate(&cfg)
		c, err := NewClient(cfg)
		if err == nil || c != nil {
			t.Errorf("%s: 应报错，got client=%v err=%v", name, c, err)
			continue
		}
		if OutcomeOf(err) != OutcomeNotSent {
			t.Errorf("%s: OutcomeOf = %s", name, OutcomeOf(err))
		}
		var e *Error
		if !errors.As(err, &e) || e.Reason != ReasonConfig {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for _, ok := range []string{"https://api.example.com/portal/api", "http://127.0.0.1:8080/portal/api", "http://localhost/portal/api"} {
		cfg := m.cfg
		cfg.BaseURL = ok
		if _, err := NewClient(cfg); err != nil {
			t.Errorf("%s: err = %v", ok, err)
		}
	}
}

func TestNewClientNormalizesBaseURL(t *testing.T) {
	m, _ := newMockPlatform(t)
	for _, suffix := range []string{"/portal/api/", "/portal/api/v1", "/portal/api/v1/"} {
		cfg := m.cfg
		cfg.BaseURL = m.Server.URL + suffix
		c, err := NewClient(cfg)
		if err != nil {
			t.Fatalf("%s: %v", suffix, err)
		}
		if err := ping(c, &pingOut{}); err != nil {
			t.Errorf("%s: err = %v", suffix, err)
		}
		if got := m.Last.URL.Path; got != "/portal/api/v1/ping" {
			t.Errorf("%s: 请求路径 = %q", suffix, got)
		}
	}
}

func TestOutcomeOf(t *testing.T) {
	if OutcomeOf(nil) != OutcomeSuccess {
		t.Error("nil 应为 Success")
	}
	if OutcomeOf(errors.New("x")) != OutcomeUnknown {
		t.Error("普通错误应为 Unknown")
	}
	wrapped := fmt.Errorf("业务: %w", &Error{Outcome: OutcomeRejected, Reason: ReasonStatus})
	if OutcomeOf(wrapped) != OutcomeRejected {
		t.Error("包装后的 *Error 应能识别")
	}
	if OutcomeOf(&Error{Outcome: OutcomeNotSent}) != OutcomeNotSent {
		t.Error("NotSent 应原样返回")
	}
	if OutcomeOf(&Error{}) != OutcomeUnknown {
		t.Error("未设置 Outcome 的 *Error 应按 Unknown 处理")
	}
	want := map[Outcome]string{OutcomeSuccess: "success", OutcomeRejected: "rejected", OutcomeUnknown: "unknown", OutcomeNotSent: "not_sent"}
	for o, s := range want {
		if o.String() != s {
			t.Errorf("%d.String() = %q, want %q", int(o), o.String(), s)
		}
	}
	if OutcomeSuccess != 1 {
		t.Errorf("OutcomeSuccess = %d, want 1", OutcomeSuccess)
	}
}

func TestBuildQuery(t *testing.T) {
	if got := buildQuery("page_no", "1", "skip", "", "k", "a b&c", "page_size", "10"); got != "page_no=1&k=a+b%26c&page_size=10" {
		t.Errorf("got %q", got)
	}
	if got := buildQuery("a", ""); got != "" {
		t.Errorf("全空应为空串，got %q", got)
	}
}
