package chatgpt

import (
	"context"
	"net/http"
	"regexp"
	"sync"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
)

var deviceIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type seenReq struct {
	path, deviceID, origin string
	cookies                []*http.Cookie
}

func (s seenReq) cookie(name string) (vals []string) {
	for _, c := range s.cookies {
		if c.Name == name {
			vals = append(vals, c.Value)
		}
	}
	return vals
}

// identityServer 记下每个请求的设备号、Origin 和 cookie；setCookies 按路径给响应加 Set-Cookie。
func identityServer(setCookies map[string][]*http.Cookie) (http.Handler, func() []seenReq) {
	var mu sync.Mutex
	var got []seenReq
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, seenReq{r.Method + " " + r.URL.Path, r.Header.Get("oai-device-id"), r.Header.Get("Origin"), r.Cookies()})
		mu.Unlock()
		for _, c := range setCookies[r.URL.Path] {
			http.SetCookie(w, c)
		}
		switch r.URL.Path {
		case "/api/auth/session":
			w.Write([]byte(`{"accessToken":"at2"}`))
		case "/cdn-cgi/trace":
			w.Write([]byte("ip=9.9.9.9\nloc=US\n"))
		default:
			w.Write([]byte(`{}`))
		}
	})
	return h, func() []seenReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenReq(nil), got...)
	}
}

func TestBackendRequestsCarryDeviceIDAndOrigin(t *testing.T) {
	h, seen := identityServer(nil)
	c := newTestClient(t, h)
	ctx := context.Background()
	s := Session{AccessToken: "at"}
	if _, err := c.do(ctx, fhttp.MethodGet, "/backend-api/accounts/check/v4-2023-04-27", bearer(s), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.do(ctx, fhttp.MethodPost, pathCheckout, bearer(s), nil, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	got := seen()
	get, post := got[0], got[1]
	if !deviceIDRe.MatchString(get.deviceID) {
		t.Fatalf("GET 应带 UUID 形式的 oai-device-id，实际 %q", get.deviceID)
	}
	if post.deviceID != get.deviceID {
		t.Errorf("同一个客户端的设备号应一致: %q / %q", get.deviceID, post.deviceID)
	}
	for _, r := range []seenReq{get, post} {
		if v := r.cookie("oai-did"); len(v) != 1 || v[0] != r.deviceID {
			t.Errorf("%s 的 oai-did cookie 应与设备号头一致且只有一个: %v / %q", r.path, v, r.deviceID)
		}
	}
	if get.origin != "" {
		t.Errorf("GET 不该带 Origin，实际 %q", get.origin)
	}
	if post.origin != "https://chatgpt.com" {
		t.Errorf("POST 应带 Origin: https://chatgpt.com，实际 %q", post.origin)
	}

	c2 := newTestClient(t, h)
	if _, err := c2.do(ctx, fhttp.MethodGet, "/backend-api/me", bearer(s), nil, nil); err != nil {
		t.Fatal(err)
	}
	if other := seen()[2].deviceID; other == get.deviceID || !deviceIDRe.MatchString(other) {
		t.Errorf("不同客户端应各有自己的设备号: %q / %q", get.deviceID, other)
	}
}

func TestCookieJarKeepsServerCookies(t *testing.T) {
	h, seen := identityServer(map[string][]*http.Cookie{
		"/api/auth/session": {{Name: "__cf_bm", Value: "cfbm1", Path: "/"}, {Name: "oai-did", Value: "srv-did-1", Path: "/"}},
	})
	c := newTestClient(t, h)
	ctx := context.Background()
	s := Session{SessionToken: "st"}
	if err := c.Refresh(ctx, &s); err != nil {
		t.Fatal(err)
	}
	if _, err := c.do(ctx, fhttp.MethodGet, "/backend-api/me", bearer(s), nil, nil); err != nil {
		t.Fatal(err)
	}
	me := seen()[1]
	if v := me.cookie("__cf_bm"); len(v) != 1 || v[0] != "cfbm1" {
		t.Errorf("刷新会话时下发的 cookie 应在后续请求里带上: %v", v)
	}
	if me.deviceID != "srv-did-1" {
		t.Errorf("ChatGPT 下发了 oai-did 后，设备号头应改用它，实际 %q", me.deviceID)
	}
	if v := me.cookie("oai-did"); len(v) != 1 || v[0] != "srv-did-1" {
		t.Errorf("oai-did cookie 应只有一个且为下发的值: %v", v)
	}
}

func TestExplicitCookieWinsOverJar(t *testing.T) {
	h, seen := identityServer(map[string][]*http.Cookie{
		"/api/auth/session": {{Name: "k", Value: "from-jar", Path: "/"}},
	})
	c := newTestClient(t, h)
	ctx := context.Background()
	if _, err := c.do(ctx, fhttp.MethodGet, "/api/auth/session", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.do(ctx, fhttp.MethodGet, "/api/auth/session", nil, []Cookie{{Name: "k", Value: "explicit"}}, nil); err != nil {
		t.Fatal(err)
	}
	if v := seen()[1].cookie("k"); len(v) != 1 || v[0] != "explicit" {
		t.Errorf("调用方显式给的 cookie 应覆盖保存的同名 cookie，且不能重复: %v", v)
	}
}

func TestTraceSendsNoCookiesOrDeviceID(t *testing.T) {
	h, seen := identityServer(map[string][]*http.Cookie{
		"/api/auth/session": {{Name: "__cf_bm", Value: "cfbm1", Path: "/"}},
	})
	c := newTestClient(t, h)
	ctx := context.Background()
	s := Session{SessionToken: "st"}
	if err := c.Refresh(ctx, &s); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Trace(ctx); err != nil {
		t.Fatal(err)
	}
	tr := seen()[1]
	if tr.path != "GET /cdn-cgi/trace" || len(tr.cookies) != 0 || tr.deviceID != "" || tr.origin != "" {
		t.Errorf("trace 不该带 cookie、设备号或 Origin: %+v", tr)
	}
}
