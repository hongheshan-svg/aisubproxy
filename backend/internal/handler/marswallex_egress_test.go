package handler

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// egressFake 起一个假的 trace 服务；Dial 只放行 tcp4，tcp6 一律失败，并记录收到的网络类型。
type egressFake struct {
	srv      *httptest.Server
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
			f.networks = append(f.networks, network)
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
	if r := f.prober(false, nil).Probe(context.Background(), "https://api.example.com/x"); r.IPv4 != "203.0.113.9" {
		t.Errorf("环境变量里有代理也必须直连: %+v", r)
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
