package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
)

const secretProxy = "http://user:pw-secret@px.secret-host.example:8080"

// fakeBrowser 替换浏览器那条路的检测（不起真 Chrome），记录收到的代理地址与调用次数。
func fakeBrowser(t *testing.T, fn func(ctx context.Context, proxy string) (string, error)) (*string, *int) {
	t.Helper()
	var got string
	var calls int
	old := browserTrace
	browserTrace = func(ctx context.Context, proxy string) (string, error) {
		got = proxy
		calls++
		return fn(ctx, proxy)
	}
	t.Cleanup(func() { browserTrace = old })
	return &got, &calls
}

// fakeTrace 把测试客户端指向假服务器，并记录收到的代理地址。浏览器那条路默认当作本机没有 Chrome。
func fakeTrace(t *testing.T, h http.HandlerFunc, timeout time.Duration) *string {
	t.Helper()
	fakeBrowser(t, func(context.Context, string) (string, error) { return "", paybrowser.ErrNoChrome })
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	var got string
	oldF, oldT := traceClientFactory, traceTimeout
	traceClientFactory = func(proxy string) (*chatgpt.Client, error) {
		got = proxy
		c, err := chatgpt.New("")
		if err != nil {
			return nil, err
		}
		c.BaseURL = srv.URL
		return c, nil
	}
	traceTimeout = timeout
	t.Cleanup(func() { traceClientFactory, traceTimeout = oldF, oldT })
	return &got
}

func traceDo(body string) (int, map[string]any, string) {
	w := payDo(AdminPayTestProxy, "POST", "/proxies/test", "/proxies/test", body)
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m, w.Body.String()
}

func traceOK(loc string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cdn-cgi/trace" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			w.WriteHeader(500)
			return
		}
		w.Write([]byte("fl=1\nip=1.2.3.4\ncolo=MNL\nloc=" + loc + "\nts=1\n"))
	}
}

func TestProxyTestParsesTrace(t *testing.T) {
	payEnv(t)
	if err := payengine.SaveProxies(map[string]string{"PH": secretProxy}); err != nil {
		t.Fatal(err)
	}
	got := fakeTrace(t, traceOK("PH"), 5*time.Second)
	code, m, body := traceDo(`{"region":"PH"}`)
	if code != 200 || m["ok"] != true || m["ip"] != "1.2.3.4" || m["loc"] != "PH" || m["colo"] != "MNL" ||
		m["matches_region"] != true || m["proxy_configured"] != true {
		t.Fatalf("%d %s", code, body)
	}
	if *got != secretProxy {
		t.Errorf("应使用已配置的代理")
	}
	if a := payAudit(t); !strings.Contains(a, "pay_proxy_test") || strings.Contains(a, "secret-host") || strings.Contains(a, "1.2.3.4") {
		t.Errorf("审计: %s", a)
	}
}

func TestProxyTestDirectAndMismatch(t *testing.T) {
	payEnv(t)
	got := fakeTrace(t, traceOK("SG"), 5*time.Second)
	code, m, body := traceDo(`{"region":"PH"}`)
	if code != 200 || m["ok"] != true || m["matches_region"] != false || m["proxy_configured"] != false || m["loc"] != "SG" {
		t.Fatalf("%d %s", code, body)
	}
	if *got != "" {
		t.Errorf("未配置应直连")
	}
}

func TestProxyTestCFBlockedAndNoLeak(t *testing.T) {
	payEnv(t)
	_ = payengine.SaveProxies(map[string]string{"PH": secretProxy})
	fakeTrace(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("cf-mitigated", "challenge")
		w.WriteHeader(403)
	}, 5*time.Second)
	code, m, body := traceDo(`{"region":"PH"}`)
	if code != 200 || m["ok"] != false || !strings.Contains(m["error"].(string), "Cloudflare") {
		t.Fatalf("%d %s", code, body)
	}
	for _, s := range []string{"secret-host", "pw-secret", "user:"} {
		if strings.Contains(body, s) || strings.Contains(payAudit(t), s) {
			t.Errorf("泄露 %s", s)
		}
	}
}

func TestProxyTestTimeout(t *testing.T) {
	payEnv(t)
	fakeTrace(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(600 * time.Millisecond) }, 150*time.Millisecond)
	code, m, body := traceDo(`{"region":"PH"}`)
	if code != 200 || m["ok"] != false || !strings.Contains(m["error"].(string), "超时") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestProxyTestConnectErrorNoLeak(t *testing.T) {
	payEnv(t)
	_ = payengine.SaveProxies(map[string]string{"PH": secretProxy})
	oldF := traceClientFactory
	t.Cleanup(func() { traceClientFactory = oldF })
	traceClientFactory = func(proxy string) (*chatgpt.Client, error) { return chatgpt.New("http://127.0.0.1:1") }
	fakeBrowser(t, func(context.Context, string) (string, error) {
		return "", errors.New("浏览器打不开检测页（net::ERR_PROXY_CONNECTION_FAILED）")
	})
	code, m, body := traceDo(`{"region":"PH"}`)
	if code != 200 || m["ok"] != false || m["error"] == "" || strings.Contains(body, "127.0.0.1") || strings.Contains(body, "secret-host") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestProxyTestBadRegion(t *testing.T) {
	payEnv(t)
	for _, b := range []string{`{"region":"XX"}`, `{"region":""}`, `{}`, `bad`} {
		if code, _, body := traceDo(b); code != 400 {
			t.Errorf("%s: %d %s", b, code, body)
		}
	}
}

func TestProxyTestBusy409(t *testing.T) {
	payEnv(t)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	fakeTrace(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		traceOK("PH")(w, r)
	}, 5*time.Second)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); traceDo(`{"region":"PH"}`) }()
	<-started
	if code, _, body := traceDo(`{"region":"US"}`); code != 409 {
		t.Errorf("应 409: %d %s", code, body)
	}
	close(release)
	wg.Wait()
	if code, _, _ := traceDo(`{"region":"PH"}`); code != 200 {
		t.Errorf("结束后应可再测: %d", code)
	}
}

// 配了代理时，浏览器那条路也要测，结果放在 browser 里，并核对两条路的出口 IP 是否一致。
func TestProxyTestBrowserPath(t *testing.T) {
	payEnv(t)
	const socks = "socks5://user:pw-secret@px.secret-host.example:1080"
	if err := payengine.SaveProxies(map[string]string{"PH": socks}); err != nil {
		t.Fatal(err)
	}
	fakeTrace(t, traceOK("PH"), 5*time.Second)
	got, _ := fakeBrowser(t, func(context.Context, string) (string, error) { return "fl=1\nip=1.2.3.4\nloc=PH\n", nil })
	code, m, body := traceDo(`{"region":"PH"}`)
	b, _ := m["browser"].(map[string]any)
	if code != 200 || m["ok"] != true || b == nil || b["checked"] != true || b["ok"] != true || b["ip"] != "1.2.3.4" ||
		b["loc"] != "PH" || b["matches_region"] != true || b["same_ip"] != true {
		t.Fatalf("%d %s", code, body)
	}
	if *got != socks {
		t.Error("浏览器那条路应使用已配置的代理")
	}
	a := payAudit(t)
	if !strings.Contains(a, "浏览器:成功，出口地区一致") || strings.Contains(a, "secret") {
		t.Errorf("审计: %s", a)
	}
}

func TestProxyTestBrowserPathFailures(t *testing.T) {
	cases := []struct {
		name  string
		trace func(context.Context, string) (string, error)
		check func(b map[string]any) bool
		audit string
	}{
		{"认证失败", func(context.Context, string) (string, error) {
			return "", errors.New("代理认证失败，请检查账号密码")
		},
			func(b map[string]any) bool {
				return b["checked"] == true && b["ok"] == false && b["error"] == "代理认证失败，请检查账号密码"
			},
			"浏览器:失败 代理认证失败"},
		{"没有 Chrome", func(context.Context, string) (string, error) { return "", paybrowser.ErrNoChrome },
			func(b map[string]any) bool {
				return b["checked"] == false && strings.Contains(b["note"].(string), "没有 Chrome")
			},
			"浏览器:未测"},
		{"出口 IP 不同", func(context.Context, string) (string, error) { return "ip=5.6.7.8\nloc=PH\n", nil },
			func(b map[string]any) bool { return b["ok"] == true && b["same_ip"] == false && b["ip"] == "5.6.7.8" },
			"浏览器:成功"},
		{"不是 trace", func(context.Context, string) (string, error) { return "Just a moment...", nil },
			func(b map[string]any) bool {
				return b["checked"] == true && b["ok"] == false && strings.Contains(b["error"].(string), "Cloudflare")
			},
			"浏览器:失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payEnv(t)
			_ = payengine.SaveProxies(map[string]string{"PH": secretProxy})
			fakeTrace(t, traceOK("PH"), 5*time.Second)
			fakeBrowser(t, tc.trace)
			code, m, body := traceDo(`{"region":"PH"}`)
			b, _ := m["browser"].(map[string]any)
			if code != 200 || m["ok"] != true || b == nil || !tc.check(b) {
				t.Fatalf("%d %s", code, body)
			}
			if a := payAudit(t); !strings.Contains(a, tc.audit) {
				t.Errorf("审计应含 %q: %s", tc.audit, a)
			}
		})
	}
}

// 没配代理（测直连）不起浏览器：浏览器同样直连，出口与服务器一致。
func TestProxyTestDirectSkipsBrowser(t *testing.T) {
	payEnv(t)
	fakeTrace(t, traceOK("PH"), 5*time.Second)
	_, calls := fakeBrowser(t, func(context.Context, string) (string, error) { return "", errors.New("不该调用") })
	code, m, body := traceDo(`{"region":"PH"}`)
	if _, has := m["browser"]; code != 200 || has || *calls != 0 {
		t.Fatalf("直连不应测浏览器: %d %s（调用 %d 次）", code, body, *calls)
	}
}
