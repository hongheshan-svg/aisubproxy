package auth

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

// A-14：TRUSTED_PROXIES 支持单个 IP、CIDR、IPv6，带空格；对端不在名单或名单为空都不信。
func TestOAFromTrustedProxy(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", " 127.0.0.1 ,172.16.0.0/12, ::1,bad-entry")
	for addr, want := range map[string]bool{
		"127.0.0.1:5000":   true,
		"172.18.0.3:443":   true,
		"[::1]:8080":       true,
		"10.0.0.1:80":      false,
		"203.0.113.1":      false,
		"not-an-ip:80":     false,
		"[2001:db8::1]:80": false,
	} {
		if got := FromTrustedProxy(addr); got != want {
			t.Errorf("%s：got %v want %v", addr, got, want)
		}
	}
	t.Setenv("TRUSTED_PROXIES", "")
	if FromTrustedProxy("127.0.0.1:5000") {
		t.Error("没配 TRUSTED_PROXIES 时谁都不信")
	}
}

func TestOARequestIsHTTPS(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", "127.0.0.1")
	req := func(remote, proto string, viaTLS bool) bool {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		if viaTLS {
			r.TLS = &tls.ConnectionState{}
		}
		return RequestIsHTTPS(r)
	}
	cases := []struct {
		name          string
		remote, proto string
		viaTLS, want  bool
	}{
		{"直连 TLS", "203.0.113.1:1", "", true, true},
		{"可信反代标 https", "127.0.0.1:1", "https", false, true},
		{"可信反代多级标记取第一个", "127.0.0.1:1", "HTTPS, http", false, true},
		{"可信反代标 http", "127.0.0.1:1", "http", false, false},
		{"直连客户端自带 https 头", "203.0.113.1:1", "https", false, false},
		{"无标记", "127.0.0.1:1", "", false, false},
	}
	for _, c := range cases {
		if got := req(c.remote, c.proto, c.viaTLS); got != c.want {
			t.Errorf("%s：got %v want %v", c.name, got, c.want)
		}
	}
}
