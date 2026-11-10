// Package credential 判断一段凭据文本是哪类登录态（X、Grok），供各处避免把它们当 ChatGPT 账单会话。
package credential

import (
	"encoding/json"
	"strings"
)

// X cookies are execution credentials, not reusable ChatGPT invoice sessions.
// 2026-10-24 凭据简化:只需 auth_token(裸串或 JSON{auth_token});ct0/billing_email 不再要。
func IsXPremiumCredential(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false
	}
	if !strings.HasPrefix(s, "{") {
		// 裸 auth_token(x.com cookie 的登录 token,十六进制约 40 位)
		return len(s) >= 16 && !strings.ContainsAny(s, " \t\n")
	}
	var x map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &x) != nil {
		return false
	}
	return len(x["auth_token"]) > 0 || len(x["authToken"]) > 0 ||
		(len(x["cookieHeader"]) > 0 && (len(x["billing_email"]) > 0 || len(x["billingEmail"]) > 0))
}

// IsGrokCredential Grok 登录态（grok.com 的 sso cookie，含 sso= / sso-rw=）。
// 它是可复用的登录凭据，不是 ChatGPT 账单会话，不能绑到卡密供账单页查询。
func IsGrokCredential(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "{") {
		return false
	}
	return strings.HasPrefix(s, "sso=") || strings.HasPrefix(s, "sso-rw=") ||
		strings.Contains(s, "; sso=") || strings.Contains(s, ";sso=") ||
		strings.Contains(s, "; sso-rw=") || strings.Contains(s, ";sso-rw=")
}
