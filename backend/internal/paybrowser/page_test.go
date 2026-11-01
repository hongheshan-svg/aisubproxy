package paybrowser_test

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

const sessionCookie = "__Secure-next-auth.session-token"

// checkoutServer 提供 testdata 下的仿造支付页，并记下请求支付页时带的会话 cookie。
func checkoutServer(t *testing.T, gotSession *atomic.Value) *httptest.Server {
	t.Helper()
	files := http.FileServer(http.Dir("testdata"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/checkout.html" && gotSession != nil {
			if c, err := r.Cookie(sessionCookie); err == nil {
				gotSession.Store(c.Value)
			}
		}
		files.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func openCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestOpenAndReadAmount(t *testing.T) {
	needChrome(t)
	var gotSession atomic.Value
	srv := checkoutServer(t, &gotSession)
	host, _, _ := net.SplitHostPort(srv.Listener.Addr().String())
	b := launch(t, paybrowser.LaunchOptions{})
	ctx := openCtx(t)

	cookies := []paybrowser.Cookie{{Name: sessionCookie, Value: "sess-abc123", Domain: host}}
	if err := b.Open(ctx, srv.URL+"/checkout.html", cookies); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if v, _ := gotSession.Load().(string); v != "sess-abc123" {
		t.Fatalf("服务端收到的会话 cookie = %q，期望 sess-abc123", v)
	}
	minor, text, err := b.ReadAmount(ctx, "PHP")
	if err != nil {
		t.Fatalf("ReadAmount: %v", err)
	}
	if minor != 112000 || text != "₱1,120.00" {
		t.Fatalf("ReadAmount = %d, %q，期望 112000, ₱1,120.00", minor, text)
	}
}

// authProxy 是只做普通 HTTP 转发的最小代理：要求 Proxy-Authorization，只转发到回环地址。
type authProxy struct {
	want       string
	challenged atomic.Int32 // 回过 407 的次数
	authorized atomic.Int32 // 凭据正确并转发的次数
	transport  http.RoundTripper
}

func newAuthProxy(user, pass string) *authProxy {
	return &authProxy{
		want:      "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)),
		transport: &http.Transport{},
	}
}

func (p *authProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Proxy-Authorization") != p.want {
		p.challenged.Add(1)
		w.Header().Set("Proxy-Authenticate", `Basic realm="test"`)
		w.WriteHeader(http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect || !r.URL.IsAbs() {
		http.Error(w, "测试代理只支持普通 HTTP 转发", http.StatusMethodNotAllowed)
		return
	}
	if ip := net.ParseIP(r.URL.Hostname()); ip == nil || !ip.IsLoopback() {
		http.Error(w, "测试代理只转发到回环地址", http.StatusForbidden)
		return
	}
	p.authorized.Add(1)
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Connection")
	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func TestProxyAuth(t *testing.T) {
	needChrome(t)
	srv := checkoutServer(t, nil)
	proxy := newAuthProxy("alice", "s3cret")
	ps := httptest.NewServer(proxy)
	t.Cleanup(ps.Close)
	pu, _ := url.Parse(ps.URL)
	pu.User = url.UserPassword("alice", "s3cret")

	b := launch(t, paybrowser.LoopbackProxy(paybrowser.LaunchOptions{ProxyURL: pu.String()}))
	if err := b.Open(openCtx(t), srv.URL+"/checkout.html", nil); err != nil {
		t.Fatalf("经代理 Open 失败: %v", err)
	}
	if proxy.challenged.Load() == 0 || proxy.authorized.Load() == 0 {
		t.Fatalf("代理认证流程不符：407 次数 %d，凭据正确的请求 %d", proxy.challenged.Load(), proxy.authorized.Load())
	}
}
