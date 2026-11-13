package chatgpt

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
)

// 测试出口时返回的错误都是固定文案：底层网络错误可能夹带代理地址或账号，一律不透传。
var (
	ErrTraceTimeout   = errors.New("请求超时，代理可能不通或太慢")
	ErrTraceProxyAuth = errors.New("代理认证失败，请检查账号密码")
	ErrTraceConnect   = errors.New("连不上代理或目标站点，请检查代理地址、端口与网络")
)

// TraceInfo 是 Cloudflare /cdn-cgi/trace 的解析结果。
type TraceInfo struct {
	IP   string
	Loc  string
	Colo string
}

// Trace 访问 /cdn-cgi/trace 取出口 IP 与 OpenAI（Cloudflare）识别的国家，不带任何会话与认证头。
// 返回的错误只会是 ErrBlocked、ErrTraceTimeout、ErrTraceProxyAuth、ErrTraceConnect 或不含地址的固定描述。
func (c *Client) Trace(ctx context.Context) (TraceInfo, error) {
	b, err := c.do(ctx, fhttp.MethodGet, "/cdn-cgi/trace", nil, nil, nil)
	if err != nil {
		return TraceInfo{}, classifyTraceErr(ctx, err)
	}
	info := ParseTrace(string(b))
	if info.IP == "" && info.Loc == "" {
		return TraceInfo{}, errors.New("返回内容不是有效的 trace 结果")
	}
	return info, nil
}

// proxyAuthStatus 认带边界的 407 状态码：前面不能是数字、点、冒号，后面不能是数字。
// 连接错误的文字里带着代理地址和本机临时端口（如 :4070、:54071），裸找「407」会把连不上误报成代理认证失败。
var proxyAuthStatus = regexp.MustCompile(`(^|[^0-9.:])407([^0-9]|$)`)

func classifyTraceErr(ctx context.Context, err error) error {
	if errors.Is(err, ErrBlocked) {
		return ErrBlocked
	}
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return ErrTraceTimeout
	}
	if errors.Is(err, ErrUnauthorized) {
		return errors.New("被目标站点拒绝（HTTP 403）")
	}
	// 只报状态码，不带响应里的说明：测试出口的结果固定只给可控的文案
	var he *HTTPError
	if errors.As(err, &he) {
		return fmt.Errorf("HTTP %d", he.Status)
	}
	msg := strings.ToLower(err.Error())
	switch {
	// socks5 认证失败的原文：账号密码不对是 "username/password authentication failed"，
	// 代理要账号密码而地址里没写是 "no acceptable authentication methods"
	case proxyAuthStatus.MatchString(msg) || strings.Contains(msg, "proxy authentication") ||
		strings.Contains(msg, "username/password") || strings.Contains(msg, "no acceptable authentication methods"):
		return ErrTraceProxyAuth
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		return ErrTraceTimeout
	}
	return ErrTraceConnect
}

// ParseTrace 解析每行一个 key=value 的纯文本。
func ParseTrace(s string) TraceInfo {
	var t TraceInfo
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "ip":
			t.IP = v
		case "loc":
			t.Loc = strings.ToUpper(v)
		case "colo":
			t.Colo = v
		}
	}
	return t
}
