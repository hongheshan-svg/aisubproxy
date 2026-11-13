package paybrowser

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// Chrome 不支持带账号密码的 SOCKS5 代理（--proxy-server 只认地址，SOCKS 认证不会触发 Fetch.authRequired），
// 很多静态 IP 服务商又只给 socks5。所以遇到这种代理时，在本机起一个只听 127.0.0.1 的 HTTP 代理（中转）：
// Chrome 连中转，中转带着账号密码经上游 SOCKS5 出去。域名原样交给上游解析，不在本机查 DNS。
//
// 中转自己也要账号密码：每个浏览器一组随机值，只交给 Chrome（经 Fetch 认证应答，见 proxy.go），
// 本机别的进程不知道就用不了这个口子。每个浏览器一个中转，随浏览器关闭。

const (
	relayDialTimeout = 30 * time.Second // 经上游 SOCKS5 连目标（含握手）的时限
	relayIdleTimeout = 2 * time.Minute  // 普通 HTTP 长连接上等下一个请求最多这么久
)

// relayHopHeaders 是只对一跳有效、不能往下传的头。
var relayHopHeaders = []string{
	"Proxy-Authorization", "Proxy-Authenticate", "Proxy-Connection", "Connection", "Keep-Alive",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// socksRelay 是一个浏览器专用的本机中转，见文件头说明。
type socksRelay struct {
	ln         net.Listener
	dial       func(ctx context.Context, network, addr string) (net.Conn, error) // 经上游 SOCKS5 连目标
	user, pass string                                                            // 中转自己的随机账号密码
	want       string                                                            // 对应的 Proxy-Authorization 头
	tr         *http.Transport                                                   // 转发普通 http:// 请求
	ctx        context.Context                                                   // Close 时取消，中止进行中的拨号
	cancel     context.CancelFunc

	mu      sync.Mutex
	conns   map[net.Conn]struct{} // 还开着的连接（两头都算），Close 时全部关掉
	closed  bool
	failMsg string // 第一次连上游失败的原因（不含地址与账号）；只记一次日志，免得一页几十个请求刷屏
	wg      sync.WaitGroup
}

// startSocksRelay 为上游 SOCKS5 代理 upstream（须带端口，可带账号密码）起一个中转。
// 返回的错误不含上游地址与账号密码。
func startSocksRelay(upstream *url.URL) (*socksRelay, error) {
	var auth *proxy.Auth
	if upstream.User != nil {
		pw, _ := upstream.User.Password()
		auth = &proxy.Auth{User: upstream.User.Username(), Password: pw}
	}
	d, err := proxy.SOCKS5("tcp", upstream.Host, auth, &net.Dialer{Timeout: relayDialTimeout, KeepAlive: 30 * time.Second})
	if err != nil {
		return nil, errors.New("创建 socks5 中转失败")
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("创建 socks5 中转失败：拨号器不支持超时控制")
	}
	user, pass, err := relayCredentials()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("创建 socks5 中转失败：本机端口监听不了")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &socksRelay{
		ln: ln, dial: cd.DialContext, user: user, pass: pass,
		want:   "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)),
		ctx:    ctx,
		cancel: cancel,
		conns:  map[net.Conn]struct{}{},
	}
	r.tr = &http.Transport{
		Proxy:               nil, // 不读环境变量里的代理
		DialContext:         r.dialUpstream,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
	}
	r.wg.Add(1)
	safego.Go("socks5 中转", r.serve)
	return r, nil
}

// relayCredentials 生成中转用的随机账号密码。
func relayCredentials() (string, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", errors.New("创建 socks5 中转失败：生成随机口令失败")
	}
	return hex.EncodeToString(b[:16]), hex.EncodeToString(b[16:]), nil
}

// server 是给 Chrome --proxy-server 用的地址。
func (r *socksRelay) server() string { return "http://" + r.ln.Addr().String() }

// auth 是 Chrome 应答中转认证用的账号密码。
func (r *socksRelay) auth() *proxyAuth { return newProxyAuth(r.user, r.pass) }

// Close 停止监听、关掉所有连接并等处理协程退出，可重复调用。
func (r *socksRelay) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	for c := range r.conns {
		_ = c.Close()
	}
	r.mu.Unlock()
	r.cancel()
	_ = r.ln.Close()
	r.tr.CloseIdleConnections()
	r.wg.Wait()
}

// track 记下一条连接；中转已关闭时返回 false，调用方自己关掉它。
func (r *socksRelay) track(c net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.conns[c] = struct{}{}
	return true
}

func (r *socksRelay) untrack(c net.Conn) {
	r.mu.Lock()
	delete(r.conns, c)
	r.mu.Unlock()
}

func (r *socksRelay) serve() {
	defer r.wg.Done()
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return // 监听已关闭
		}
		if !r.track(c) {
			_ = c.Close()
			return
		}
		r.wg.Add(1) // serve 自己还占着计数，这里加不会和 Close 的 Wait 抢
		safego.Go("socks5 中转连接", func() {
			defer r.wg.Done()
			defer r.untrack(c)
			defer c.Close()
			r.handle(c)
		})
	}
}

// handle 处理 Chrome 的一条连接：先认中转口令，CONNECT 建隧道，普通 http:// 请求代为转发。
func (r *socksRelay) handle(c net.Conn) {
	br := bufio.NewReader(c)
	for {
		_ = c.SetReadDeadline(time.Now().Add(relayIdleTimeout))
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		_ = c.SetReadDeadline(time.Time{})
		if subtle.ConstantTimeCompare([]byte(req.Header.Get("Proxy-Authorization")), []byte(r.want)) != 1 {
			// 不带请求体的请求可以在同一条连接上等 Chrome 带口令重发；带请求体的回完就关，免得残留的请求体被当成下一个请求
			keep := req.ContentLength == 0 && len(req.TransferEncoding) == 0
			hdr := "Proxy-Authenticate: Basic realm=\"relay\"\r\nContent-Length: 0\r\n"
			if !keep {
				hdr += "Connection: close\r\n"
			}
			if _, err := io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\n"+hdr+"\r\n"); err != nil || !keep {
				return
			}
			continue
		}
		if req.Method == http.MethodConnect {
			r.tunnel(c, br, req.Host)
			return
		}
		if !r.forward(c, req) {
			return
		}
	}
}

// tunnel 经上游连上 addr，回 200 后两头对拷，直到任一头断开。
func (r *socksRelay) tunnel(c net.Conn, br *bufio.Reader, addr string) {
	up, err := r.dialUpstream(r.ctx, "tcp", addr)
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	if !r.track(up) {
		_ = up.Close()
		return
	}
	defer r.untrack(up)
	defer up.Close()
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	safego.Go("socks5 中转上行", func() {
		_, _ = io.Copy(up, br) // br 里可能已缓存了 Chrome 紧跟着发来的数据
		done <- struct{}{}
	})
	safego.Go("socks5 中转下行", func() {
		_, _ = io.Copy(c, up)
		done <- struct{}{}
	})
	<-done
	// 一头断了就把两头都关掉，另一个方向的 Copy 随之返回
	_ = up.Close()
	_ = c.Close()
	<-done
}

// forward 转发普通 http:// 请求（absolute-form）。返回 false 表示这条连接不能再用。
func (r *socksRelay) forward(c net.Conn, req *http.Request) bool {
	if !req.URL.IsAbs() || !strings.EqualFold(req.URL.Scheme, "http") {
		_, _ = io.WriteString(c, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return false
	}
	out := req.Clone(r.ctx)
	out.RequestURI = ""
	out.Close = false
	for _, h := range relayHopHeaders {
		out.Header.Del(h)
	}
	resp, err := r.tr.RoundTrip(out)
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return false
	}
	defer resp.Body.Close()
	for _, h := range relayHopHeaders {
		if h != "Transfer-Encoding" { // Response.Write 按 resp.TransferEncoding 自己写分块
			resp.Header.Del(h)
		}
	}
	resp.Close = resp.Close || req.Close
	if err := resp.Write(c); err != nil {
		return false
	}
	return !resp.Close
}

// dialUpstream 经上游 SOCKS5 连目标；失败时按类别记一次日志（不含地址与账号）。
func (r *socksRelay) dialUpstream(ctx context.Context, network, addr string) (net.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, relayDialTimeout)
	defer cancel()
	conn, err := r.dial(dctx, network, addr)
	if err != nil && r.ctx.Err() == nil {
		reason := socksFailReason(err)
		r.mu.Lock()
		// 认证失败影响所有请求，是最该报的原因：先记下的别的原因（比如某个无关请求连不上）让位给它
		record := r.failMsg == "" || (reason == socksAuthFailed && r.failMsg != socksAuthFailed)
		if record {
			r.failMsg = reason
		}
		r.mu.Unlock()
		if record {
			log.Printf("[paybrowser] 经 socks5 代理连接失败：%s", reason)
		}
	}
	return conn, err
}

// failReason 返回连上游失败的原因（认证失败优先）；没失败过为空。
func (r *socksRelay) failReason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failMsg
}

const socksAuthFailed = "代理认证失败，请检查账号密码"

// socksFailReason 把 SOCKS5 拨号错误归成不含地址的固定说明。
func socksFailReason(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "username/password") || strings.Contains(msg, "authentication"):
		return socksAuthFailed
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timeout"):
		return "连接超时，代理可能不通或太慢"
	}
	return "连不上代理或目标站点"
}
