package handler

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// egressFake 起一个假的 trace 服务；Dial 只放行 tcp4，tcp6 一律失败，并记录收到的网络类型。
type egressFake struct {
	srv      *httptest.Server
	mu       sync.Mutex
	networks []string
}

func newEgressFake(t *testing.T, trace string) *egressFake {
	t.Helper()
	f := &egressFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cdn-cgi/trace" {
			t.Errorf("路径 %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(trace))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *egressFake) prober(hasV6 bool, lookupErr error) *egressProber {
	addr := strings.TrimPrefix(f.srv.URL, "http://")
	return &egressProber{
		TraceURL: "http://trace.invalid/cdn-cgi/trace",
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			f.mu.Lock()
			f.networks = append(f.networks, network)
			f.mu.Unlock()
			if network == "tcp6" {
				return nil, errors.New("dial tcp6: network is unreachable 2001:db8::1")
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp4", addr)
		},
		Lookup: func(ctx context.Context, host string) ([]net.IPAddr, error) {
			if lookupErr != nil {
				return nil, lookupErr
			}
			out := []net.IPAddr{{IP: net.ParseIP("1.2.3.4")}}
			if hasV6 {
				out = append(out, net.IPAddr{IP: net.ParseIP("2001:db8::2")})
			}
			return out, nil
		},
	}
}

func TestEgressProbeIPv4OkIPv6Fails(t *testing.T) {
	f := newEgressFake(t, "fl=1\nip=203.0.113.9\nloc=US\n")
	r := f.prober(true, nil).Probe(context.Background(), "https://api.example.com/portal/api")
	if r.IPv4 != "203.0.113.9" || r.IPv4Error != "" {
		t.Errorf("ipv4 %+v", r)
	}
	if r.IPv6 != "" || r.IPv6Error == "" {
		t.Errorf("ipv6 应失败: %+v", r)
	}
	if strings.Contains(r.IPv6Error, "2001:db8") {
		t.Errorf("底层错误不应原样透传: %q", r.IPv6Error)
	}
	if r.PlatformHost != "api.example.com" || !r.PlatformHasIPv6 {
		t.Errorf("host %+v", r)
	}
	seen := map[string]bool{}
	for _, n := range f.networks {
		seen[n] = true
	}
	if !seen["tcp4"] || !seen["tcp6"] || len(seen) != 2 {
		t.Errorf("应分别强制 tcp4 与 tcp6，实际 %v", f.networks)
	}
}

func TestEgressProbeNoAAAAAndLookupError(t *testing.T) {
	f := newEgressFake(t, "ip=203.0.113.9\n")
	if r := f.prober(false, nil).Probe(context.Background(), "https://api.example.com/x"); r.PlatformHasIPv6 {
		t.Errorf("没有 AAAA 应为 false: %+v", r)
	}
	if r := f.prober(true, errors.New("no such host")).Probe(context.Background(), "https://api.example.com/x"); r.PlatformHasIPv6 || r.PlatformHost != "api.example.com" {
		t.Errorf("查不到应为 false: %+v", r)
	}
}

func TestEgressProbeBadTrace(t *testing.T) {
	f := newEgressFake(t, "hello world")
	r := f.prober(false, nil).Probe(context.Background(), "https://api.example.com/x")
	if r.IPv4 != "" || r.IPv4Error == "" {
		t.Errorf("无 ip= 行应报错: %+v", r)
	}
}

func TestEgressProbeNoProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	f := newEgressFake(t, "ip=203.0.113.9\n")
	p := f.prober(false, nil)
	var addrs []string
	inner := p.Dial
	var amu sync.Mutex
	p.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		amu.Lock()
		addrs = append(addrs, addr)
		amu.Unlock()
		return inner(ctx, network, addr)
	}
	if r := p.Probe(context.Background(), "https://api.example.com/x"); r.IPv4 != "203.0.113.9" {
		t.Errorf("环境变量里有代理也必须直连: %+v", r)
	}
	for _, a := range addrs {
		if a != "trace.invalid:80" {
			t.Errorf("拨号地址应是目标主机端口，实际 %q（疑似走了代理）", a)
		}
	}
	if len(addrs) == 0 {
		t.Error("没有拨号记录")
	}
	// 探测用的 Transport 不能带代理函数
	if tr := p.transport("tcp4", p.Dial); tr.Proxy != nil {
		t.Error("Transport.Proxy 必须为 nil")
	}
}

func TestAdminMarsWallexEgressHandler(t *testing.T) {
	mwSetup(t)
	f := newEgressFake(t, "ip=198.51.100.7\n")
	old := marsWallexEgressProber
	marsWallexEgressProber = f.prober(true, nil)
	t.Cleanup(func() { marsWallexEgressProber = old })
	w := mwDo(AdminMarsWallexEgress, "GET", "/x", "", nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m := mwJSON(t, w)
	for _, k := range []string{"ipv4", "ipv6", "ipv4_error", "ipv6_error", "platform_host", "platform_has_ipv6"} {
		if _, ok := m[k]; !ok {
			t.Errorf("缺字段 %s: %v", k, m)
		}
	}
	if m["ipv4"] != "198.51.100.7" || m["platform_has_ipv6"] != true || m["platform_host"] != "api.feibeiglobal.com" {
		t.Errorf("%v", m)
	}
}

// 底层超时（不是 ctx 到期）也要归为「请求超时」，不能报成「连不上」。
func TestEgressProbeTimeoutClassified(t *testing.T) {
	f := newEgressFake(t, "ip=1.1.1.1\n")
	p := f.prober(false, nil)
	p.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, timeoutErr{} }
	r := p.Probe(context.Background(), "https://api.example.com/x")
	if r.IPv4Error != "请求超时" || r.IPv6Error != "请求超时" {
		t.Errorf("应归为超时: %+v", r)
	}
}

// 三步（IPv4、IPv6、AAAA）并发：每步 300ms 时总耗时应明显小于 900ms。
func TestEgressProbeRunsConcurrently(t *testing.T) {
	f := newEgressFake(t, "ip=1.1.1.1\n")
	p := f.prober(true, nil)
	inner, lookup := p.Dial, p.Lookup
	var mu sync.Mutex
	p.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		time.Sleep(300 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return inner(ctx, network, addr)
	}
	p.Lookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		time.Sleep(300 * time.Millisecond)
		return lookup(ctx, host)
	}
	start := time.Now()
	r := p.Probe(context.Background(), "https://api.example.com/x")
	if d := time.Since(start); d > 600*time.Millisecond {
		t.Errorf("应并发，实际耗时 %v", d)
	}
	if r.IPv4 != "1.1.1.1" || !r.PlatformHasIPv6 || r.IPv6Error == "" {
		t.Errorf("结果不对: %+v", r)
	}
}

func TestEgressProbeEnvProxyHost(t *testing.T) {
	old := egressEnvProxy
	t.Cleanup(func() { egressEnvProxy = old })
	var gotHost string
	egressEnvProxy = func(r *http.Request) (*url.URL, error) {
		gotHost = r.URL.Host
		return url.Parse("http://user:secret@proxy.example:8080")
	}
	f := newEgressFake(t, "ip=1.1.1.1\n")
	r := f.prober(false, nil).Probe(context.Background(), "https://api.example.com/portal/api")
	if r.EnvProxyHost != "proxy.example:8080" {
		t.Errorf("EnvProxyHost=%q", r.EnvProxyHost)
	}
	if gotHost != "api.example.com" {
		t.Errorf("应对 Base URL 的主机取代理，实际 %q", gotHost)
	}
	egressEnvProxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	if r := f.prober(false, nil).Probe(context.Background(), "https://api.example.com/x"); r.EnvProxyHost != "" {
		t.Errorf("没代理应为空: %q", r.EnvProxyHost)
	}
	egressEnvProxy = func(*http.Request) (*url.URL, error) { return nil, errors.New("bad proxy env") }
	if r := f.prober(false, nil).Probe(context.Background(), "https://api.example.com/x"); r.EnvProxyHost != "" {
		t.Errorf("取代理出错应为空: %q", r.EnvProxyHost)
	}
}

func TestAdminMarsWallexEgressHandlerEnvProxyNoSecret(t *testing.T) {
	mwSetup(t)
	old, oldP := egressEnvProxy, marsWallexEgressProber
	t.Cleanup(func() { egressEnvProxy, marsWallexEgressProber = old, oldP })
	egressEnvProxy = func(*http.Request) (*url.URL, error) { return url.Parse("http://user:secret@proxy.example:8080") }
	f := newEgressFake(t, "ip=198.51.100.7\n")
	marsWallexEgressProber = f.prober(false, nil)
	w := mwDo(AdminMarsWallexEgress, "GET", "/x", "", nil)
	if w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	body := w.Body.String()
	if m := mwJSON(t, w); m["env_proxy_host"] != "proxy.example:8080" {
		t.Errorf("%v", m)
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "user") {
		t.Errorf("响应不得带代理账号密码: %s", body)
	}
}

// 读 Base URL 设置失败：退回默认地址，但要记日志（只写固定说明和错误类型）。
func TestAdminMarsWallexEgressSettingReadFailureLogged(t *testing.T) {
	c := mwSetup(t)
	if _, err := c.Exec(`DROP TABLE site_settings`); err != nil {
		t.Fatal(err)
	}
	f := newEgressFake(t, "ip=198.51.100.7\n")
	old := marsWallexEgressProber
	marsWallexEgressProber = f.prober(false, nil)
	t.Cleanup(func() { marsWallexEgressProber = old })
	var logs strings.Builder
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	w := mwDo(AdminMarsWallexEgress, "GET", "/x", "", nil)
	if w.Code != 200 || mwJSON(t, w)["platform_host"] != "api.feibeiglobal.com" {
		t.Fatalf("应退回默认地址: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), "读取 Base URL 设置失败") {
		t.Errorf("应记日志: %q", logs.String())
	}
}
