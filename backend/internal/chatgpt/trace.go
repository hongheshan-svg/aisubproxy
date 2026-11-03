package chatgpt

import (
	"context"
	"errors"
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
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "407") || strings.Contains(msg, "proxy authentication"):
		return ErrTraceProxyAuth
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline"):
		return ErrTraceTimeout
	case strings.HasPrefix(err.Error(), "ChatGPT 返回 HTTP"):
		// 该文案只含状态码
		return errors.New(strings.TrimPrefix(err.Error(), "ChatGPT 返回 "))
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
