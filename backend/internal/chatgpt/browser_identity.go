package chatgpt

import (
	"crypto/rand"
	"fmt"
	"net/url"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
)

// 浏览器身份：让发给 ChatGPT 的请求和网页前端一致。
//   - 每个客户端一个设备号，放在 oai-did cookie 和 oai-device-id 头里；ChatGPT 下发了 oai-did 就改用它。
//   - 保存 ChatGPT 下发的 cookie（如 Cloudflare 的 __cf_bm），后续请求带上；调用方显式给的同名 cookie 优先。
//   - 非 GET/HEAD 请求带 Origin。
//
// 只用于 /api/auth/ 和 /backend-api/；/cdn-cgi/trace（地区代理测试）照旧不带任何 cookie 和设备号。
// 设备号和 cookie 只在内存里，随客户端（每张单据、每次探测各一个）一起销毁，不落盘、不进日志。

const (
	deviceIDCookie = "oai-did"
	deviceIDHeader = "oai-device-id"
)

// newDeviceID 生成 UUID v4 形式的设备号，和网页前端生成的格式一致。
func newDeviceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read 不会失败
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func browserScoped(path string) bool {
	return strings.HasPrefix(path, "/backend-api/") || strings.HasPrefix(path, "/api/auth/")
}

// deviceIDFor 返回这次请求用的设备号：ChatGPT 下发过 oai-did 就用它，否则用客户端自己生成的。
func (c *Client) deviceIDFor(u *url.URL) string {
	for _, ck := range c.jar.Cookies(u) {
		if ck.Name == deviceIDCookie && ck.Value != "" {
			return ck.Value
		}
	}
	return c.deviceID
}

// applyBrowserIdentity 在调用方的头和 cookie 都设置好之后调用，只补调用方没给的。
func (c *Client) applyBrowserIdentity(req *fhttp.Request, path string, explicit []Cookie) {
	given := make(map[string]bool, len(explicit))
	for _, ck := range explicit {
		given[ck.Name] = true
	}
	did := c.deviceIDFor(req.URL)
	for _, ck := range c.jar.Cookies(req.URL) {
		if !given[ck.Name] {
			req.AddCookie(&fhttp.Cookie{Name: ck.Name, Value: ck.Value})
			given[ck.Name] = true
		}
	}
	if !given[deviceIDCookie] {
		req.AddCookie(&fhttp.Cookie{Name: deviceIDCookie, Value: did})
	}
	if strings.HasPrefix(path, "/backend-api/") && req.Header.Get(deviceIDHeader) == "" {
		req.Header.Set(deviceIDHeader, did)
	}
	if req.Method != fhttp.MethodGet && req.Method != fhttp.MethodHead && req.Header.Get("Origin") == "" {
		req.Header.Set("Origin", defaultBaseURL)
	}
}
