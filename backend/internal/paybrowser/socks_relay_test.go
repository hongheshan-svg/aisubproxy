package paybrowser

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func startTestRelay(t *testing.T, upstream string) *socksRelay {
	t.Helper()
	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	r, err := startSocksRelay(u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

// relayClient 是带中转口令、经中转发请求的 HTTP 客户端（CONNECT 与普通转发都会带 Proxy-Authorization）。
func relayClient(r *socksRelay, user, pass string) *http.Client {
	pu, _ := url.Parse(r.server())
	if user != "" {
		pu.User = url.UserPassword(user, pass)
	}
	tr := &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func TestParseProxySocks(t *testing.T) {
	server, auth, socks, err := parseProxy("socks5://alice:p%40ss@127.0.0.1:1080")
	if err != nil || server != "" || auth != nil || socks == nil {
		t.Fatalf("带账号密码的 socks5 应交给中转: %q %v %v %v", server, auth, socks, err)
	}
	if pw, _ := socks.User.Password(); socks.Host != "127.0.0.1:1080" || socks.User.Username() != "alice" || pw != "p@ss" {
		t.Fatalf("上游地址不符: %v", socks)
	}
	for in, want := range map[string]string{
		"socks5://127.0.0.1:1080":  "socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080": "socks5://127.0.0.1:1080", // Chrome 不认 socks5h，写成 socks5（同样由代理端解析域名）
		"SOCKS5H://h:1080":         "socks5://h:1080",
	} {
		server, auth, socks, err := parseProxy(in)
		if err != nil || server != want || auth != nil || socks != nil {
			t.Errorf("parseProxy(%q) = %q %v %v %v，期望 %q", in, server, auth, socks, err, want)
		}
	}
	if _, _, _, err := parseProxy("socks5h://alice:topsecret@h"); err == nil || strings.Contains(err.Error(), "topsecret") {
		t.Errorf("socks5 不写端口应报错且不含密码: %v", err)
	}
}

func TestSocksRelayForwardsThroughUpstream(t *testing.T) {
	up := NewFakeSOCKS5(t, "alice", "s3cret")
	r := startTestRelay(t, "socks5://alice:s3cret@"+up.Addr)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Proxy-Authorization") != "" {
			t.Error("中转口令不能往下传给网站")
		}
		io.WriteString(w, "hello "+req.URL.Path)
	}))
	t.Cleanup(site.Close)
	tlsSite := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		io.WriteString(w, "secure")
	}))
	t.Cleanup(tlsSite.Close)

	c := relayClient(r, r.user, r.pass)
	// 普通 http://：中转代为转发；用 localhost 确认域名原样交给上游解析
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(site.URL, "http://"))
	resp, err := c.Get("http://localhost:" + port + "/plain")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello /plain" {
		t.Fatalf("普通转发结果不符: %d %q", resp.StatusCode, body)
	}
	// https://：走 CONNECT 隧道
	tc := relayClient(r, r.user, r.pass)
	tc.Transport.(*http.Transport).TLSClientConfig = tlsSite.Client().Transport.(*http.Transport).TLSClientConfig
	resp, err = tc.Get(tlsSite.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "secure" {
		t.Fatalf("隧道结果不符: %q", body)
	}
	if up.AuthOK.Load() < 2 || up.AuthFail.Load() != 0 {
		t.Fatalf("上游认证次数不符：成功 %d，失败 %d", up.AuthOK.Load(), up.AuthFail.Load())
	}
	if ts := up.Targets(); len(ts) == 0 || ts[0] != "localhost:"+port {
		t.Fatalf("域名应原样交给上游解析: %v", ts)
	}
}

// 中转只认自己的随机口令：没带、带错都回 407，不会经上游出去。
func TestSocksRelayRequiresItsOwnCredentials(t *testing.T) {
	up := NewFakeSOCKS5(t, "alice", "s3cret")
	r := startTestRelay(t, "socks5://alice:s3cret@"+up.Addr)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { io.WriteString(w, "x") }))
	t.Cleanup(site.Close)
	for _, cred := range [][2]string{{"", ""}, {r.user, "wrong"}, {"alice", "s3cret"}} {
		resp, err := relayClient(r, cred[0], cred[1]).Get(site.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusProxyAuthRequired || !strings.Contains(resp.Header.Get("Proxy-Authenticate"), "Basic") {
			t.Errorf("口令 %q 应 407，得到 %d", cred[0], resp.StatusCode)
		}
	}
	if len(up.Targets()) != 0 || up.AuthOK.Load() != 0 {
		t.Fatal("口令不对时不应连上游")
	}
	// 同一条连接上 407 之后可以带口令重发（Chrome 的做法）
	conn, err := net.Dial("tcp", strings.TrimPrefix(r.server(), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	host := strings.TrimPrefix(site.URL, "http://")
	io.WriteString(conn, "CONNECT "+host+" HTTP/1.1\r\nHost: "+host+"\r\n\r\n")
	if resp, err := http.ReadResponse(br, nil); err != nil || resp.StatusCode != 407 {
		t.Fatalf("应 407: %v %v", resp, err)
	}
	req, _ := http.NewRequest(http.MethodConnect, "http://"+host, nil)
	req.Host = host
	req.Header.Set("Proxy-Authorization", r.want)
	req.Write(conn)
	if resp, err := http.ReadResponse(br, nil); err != nil || resp.StatusCode != 200 {
		t.Fatalf("带口令重发应 200: %v %v", resp, err)
	}
}

// 上游账号密码不对：Chrome 收到 502，中转记下不含地址的原因。
func TestSocksRelayUpstreamAuthFailure(t *testing.T) {
	up := NewFakeSOCKS5(t, "alice", "s3cret")
	r := startTestRelay(t, "socks5://alice:wrong-pw@"+up.Addr)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { io.WriteString(w, "x") }))
	t.Cleanup(site.Close)
	resp, err := relayClient(r, r.user, r.pass).Get(site.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("应 502，得到 %d", resp.StatusCode)
	}
	if got := r.failReason(); got != "代理认证失败，请检查账号密码" {
		t.Fatalf("失败原因不符: %q", got)
	}
	if up.AuthFail.Load() == 0 {
		t.Fatal("上游应收到错误的账号密码")
	}
}

// Close 要关掉进行中的隧道并等协程退出，之后不再接受连接。
func TestSocksRelayCloseEndsTunnels(t *testing.T) {
	up := NewFakeSOCKS5(t, "", "")
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	u, _ := url.Parse("socks5://" + up.Addr)
	r, err := startSocksRelay(u)
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimPrefix(r.server(), "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "CONNECT "+echo.Addr().String()+" HTTP/1.1\r\nHost: x\r\nProxy-Authorization: "+r.want+"\r\n\r\n")
	br := bufio.NewReader(conn)
	if resp, err := http.ReadResponse(br, nil); err != nil || resp.StatusCode != 200 {
		t.Fatalf("隧道应建立: %v %v", resp, err)
	}
	io.WriteString(conn, "ping")
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("隧道不通: %q %v", buf, err)
	}
	done := make(chan struct{})
	go func() { r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close 卡住")
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("Close 后隧道应断开")
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Fatal("Close 后不应再接受连接")
	}
	r.Close() // 可重复调用
}
