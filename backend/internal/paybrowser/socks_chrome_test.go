package paybrowser_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// 带账号密码的 socks5：Chrome 经本机中转打开支付页，上游 SOCKS5 收到正确的账号密码。
func TestSocks5ProxyAuth(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	up := paybrowser.NewFakeSOCKS5(t, "alice", "s3cret")
	b := launch(t, paybrowser.LoopbackProxy(paybrowser.LaunchOptions{ProxyURL: "socks5://alice:s3cret@" + up.Addr}))
	if err := b.Open(openCtx(t), f.url(""), paybrowser.PageHosted, nil); err != nil {
		t.Fatalf("经 socks5 代理 Open 失败: %v", err)
	}
	if up.AuthOK.Load() == 0 || up.AuthFail.Load() != 0 {
		t.Fatalf("上游认证次数不符：成功 %d，失败 %d", up.AuthOK.Load(), up.AuthFail.Load())
	}
}

// 不带账号密码的 socks5h：Chrome 直接连（写成 socks5），同样走得通。
func TestSocks5hProxyNoAuth(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	up := paybrowser.NewFakeSOCKS5(t, "", "")
	b := launch(t, paybrowser.LoopbackProxy(paybrowser.LaunchOptions{ProxyURL: "socks5h://" + up.Addr}))
	if err := b.Open(openCtx(t), f.url(""), paybrowser.PageHosted, nil); err != nil {
		t.Fatalf("经 socks5h 代理 Open 失败: %v", err)
	}
	if len(up.Targets()) == 0 {
		t.Fatal("请求应经过 socks5 代理")
	}
}

// 代理测试的浏览器那条路：经 socks5 读到 trace；密码不对时报代理认证失败，不带地址与密码。
func TestTraceThroughSocks5(t *testing.T) {
	needChrome(t)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "fl=1\nip=9.9.9.9\nloc=PH\n")
	}))
	t.Cleanup(site.Close)
	old := paybrowser.TraceURL
	paybrowser.TraceURL = site.URL + "/cdn-cgi/trace"
	t.Cleanup(func() { paybrowser.TraceURL = old })
	up := paybrowser.NewFakeSOCKS5(t, "alice", "s3cret")

	text, err := paybrowser.TraceLoopback(openCtx(t), "socks5://alice:s3cret@"+up.Addr)
	if err != nil || !strings.Contains(text, "ip=9.9.9.9") || !strings.Contains(text, "loc=PH") {
		t.Fatalf("经 socks5 读 trace 失败: %q %v", text, err)
	}
	if up.AuthOK.Load() == 0 {
		t.Fatal("上游应收到正确的账号密码")
	}

	_, err = paybrowser.TraceLoopback(openCtx(t), "socks5://alice:topsecret-wrong@"+up.Addr)
	if err == nil || err.Error() != "代理认证失败，请检查账号密码" {
		t.Fatalf("密码不对应报代理认证失败: %v", err)
	}
}
