package auth

import (
	"net"
	"net/http"
	"os"
	"strings"
)

// RequestIsHTTPS 判断浏览器到本站这一跳是不是 HTTPS，决定是否下发 HSTS 和 Secure cookie。
// 直连 TLS 算；反代标的 X-Forwarded-Proto=https 只在请求来自 TRUSTED_PROXIES 里的地址时才算，
// 直连的客户端自己带这个头不算数（审计 A-14）。TRUSTED_PROXIES 和 gin 的可信代理用同一个环境变量。
func RequestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	if !strings.EqualFold(strings.TrimSpace(proto), "https") {
		return false
	}
	return FromTrustedProxy(r.RemoteAddr)
}

// FromTrustedProxy 判断 TCP 对端地址是否在 TRUSTED_PROXIES（逗号分隔的 IP 或 CIDR）里。没配就谁都不信。
func FromTrustedProxy(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	valid, _ := SplitTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	for _, part := range valid {
		if strings.Contains(part, "/") {
			if _, n, _ := net.ParseCIDR(part); n != nil && n.Contains(ip) {
				return true
			}
			continue
		}
		if p := net.ParseIP(part); p != nil && p.Equal(ip) {
			return true
		}
	}
	return false
}

// SplitTrustedProxies 把 TRUSTED_PROXIES（逗号分隔）拆成能用的项（IP 或 CIDR）和写错的项。
// gin 的 SetTrustedProxies 和 FromTrustedProxy 共用这一个规则：坏项跳过，其余照常生效（B1-01）。
func SplitTrustedProxies(raw string) (valid, invalid []string) {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			if _, _, err := net.ParseCIDR(part); err == nil {
				valid = append(valid, part)
			} else {
				invalid = append(invalid, part)
			}
			continue
		}
		if net.ParseIP(part) != nil {
			valid = append(valid, part)
		} else {
			invalid = append(invalid, part)
		}
	}
	return valid, invalid
}
