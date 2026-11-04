package handler

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const (
	egressTraceURL = "https://www.cloudflare.com/cdn-cgi/trace"
	egressTimeout  = 10 * time.Second
)

// egressResult 是出口 IP 检测结果；查失败的一项只给固定说明，不透传底层错误（可能带地址）。
type egressResult struct {
	IPv4            string `json:"ipv4"`
	IPv6            string `json:"ipv6"`
	IPv4Error       string `json:"ipv4_error"`
	IPv6Error       string `json:"ipv6_error"`
	PlatformHost    string `json:"platform_host"`
	PlatformHasIPv6 bool   `json:"platform_has_ipv6"`
	// EnvProxyHost 是 HTTPS_PROXY 等环境变量给 Base URL 选出的代理（只有 host:port）；没有则为空。
	EnvProxyHost string `json:"env_proxy_host"`
}

// egressEnvProxy 取环境代理；包级变量，便于测试替换（ProxyFromEnvironment 在进程内只读一次环境变量）。
var egressEnvProxy = http.ProxyFromEnvironment

// egressProber 检测服务器直连时的出口 IP。Dial、Lookup 可注入，测试里不访问外网。
type egressProber struct {
	TraceURL string
	Dial     func(ctx context.Context, network, addr string) (net.Conn, error) // nil 时用 net.Dialer
	Lookup   func(ctx context.Context, host string) ([]net.IPAddr, error)      // nil 时用 net.DefaultResolver
}

var marsWallexEgressProber = &egressProber{}

// Probe 分别强制走 IPv4 与 IPv6 访问 trace，并查平台主机名有没有 AAAA 记录。每个请求超时 10 秒。
func (p *egressProber) Probe(ctx context.Context, baseURL string) egressResult {
	var r egressResult
	if u, err := url.Parse(baseURL); err == nil {
		r.PlatformHost = u.Hostname()
	}
	// IPv4、IPv6、AAAA 三步并发，最坏等待约一个超时而不是三个。
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); r.IPv4, r.IPv4Error = p.trace(ctx, "tcp4") }()
	go func() { defer wg.Done(); r.IPv6, r.IPv6Error = p.trace(ctx, "tcp6") }()
	go func() {
		defer wg.Done()
		if r.PlatformHost != "" {
			r.PlatformHasIPv6 = p.hasAAAA(ctx, r.PlatformHost)
		}
	}()
	wg.Wait()
	r.EnvProxyHost = envProxyHost(baseURL)
	return r
}

// envProxyHost 返回环境变量代理对 baseURL 的 host:port；没有代理、取不到或地址无效时为空。绝不返回账号、密码或完整 URL。
func envProxyHost(baseURL string) string {
	req, err := http.NewRequest(http.MethodGet, baseURL, nil)
	if err != nil {
		return ""
	}
	pu, err := egressEnvProxy(req)
	if err != nil || pu == nil {
		return ""
	}
	return pu.Host
}

func (p *egressProber) hasAAAA(ctx context.Context, host string) bool {
	ctx, cancel := context.WithTimeout(ctx, egressTimeout)
	defer cancel()
	lookup := p.Lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if a.IP.To4() == nil && a.IP.To16() != nil {
			return true
		}
	}
	return false
}

// transport 构造探测用的 Transport：显式不走代理（也不读环境变量），只用指定的网络类型拨号。
func (p *egressProber) transport(network string, dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Transport {
	return &http.Transport{
		Proxy:             nil,
		DialContext:       func(ctx context.Context, _, addr string) (net.Conn, error) { return dial(ctx, network, addr) },
		DisableKeepAlives: true,
	}
}

// trace 用 network（tcp4 或 tcp6）直连取 trace 里的 ip= 行；不走代理。
func (p *egressProber) trace(ctx context.Context, network string) (ip, errText string) {
	ctx, cancel := context.WithTimeout(ctx, egressTimeout)
	defer cancel()
	dial := p.Dial
	if dial == nil {
		d := &net.Dialer{Timeout: egressTimeout}
		dial = d.DialContext
	}
	tr := p.transport(network, dial)
	defer tr.CloseIdleConnections()
	target := p.TraceURL
	if target == "" {
		target = egressTraceURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", "请求构造失败"
	}
	resp, err := (&http.Client{Transport: tr, Timeout: egressTimeout}).Do(req)
	if err != nil {
		if ctx.Err() != nil || os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
			return "", "请求超时"
		}
		if network == "tcp6" {
			return "", "连不上（服务器可能没有 IPv6 出口）"
		}
		return "", "连不上"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "对方返回 HTTP " + strconv.Itoa(resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", "读取失败"
	}
	ip = chatgpt.ParseTrace(string(b)).IP
	if ip == "" {
		return "", "返回内容里没有 ip= 一行"
	}
	return ip, ""
}

// AdminMarsWallexEgress 只读：检测服务器直连的出口 IPv4 / IPv6，供管理员填进 MarsWallex 的 IP 白名单。
func AdminMarsWallexEgress(c *gin.Context) {
	baseURL, err := db.GetSetting(db.SettingMarsWallexBaseURL)
	if err != nil {
		// 退回默认地址继续检测；只记固定说明和错误类型，不写别的内容。
		log.Printf("marswallex egress: 读取 Base URL 设置失败（%T）", err)
		baseURL = ""
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = marsWallexProdURL
	}
	c.JSON(http.StatusOK, marsWallexEgressProber.Probe(c.Request.Context(), baseURL))
}
