// Package chatgpt 用用户的 ChatGPT 会话刷新 token、查询账号并创建支付会话。
package chatgpt

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/gptcheck"
)

// ErrBadSession 表示提供的会话内容不可用。
var ErrBadSession = errors.New("会话无效")

const (
	sessionCookieName = "__Secure-next-auth.session-token"
	cookieChunkSize   = 3933
)

// Session 是一个 ChatGPT 登录会话。
type Session struct {
	AccessToken  string
	SessionToken string
	Email        string
}

// Cookie 是请求中携带的 cookie。
type Cookie struct{ Name, Value string }

// ParseSession 解析 /api/auth/session 返回的 JSON，要求含 sessionToken 与邮箱。
func ParseSession(raw string) (Session, error) {
	v := strings.TrimSpace(raw)
	if !strings.HasPrefix(v, "{") {
		return Session{}, fmt.Errorf("%w：请粘贴完整的 session JSON，而不是单独的 token", ErrBadSession)
	}
	var data struct {
		User *struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(v), &data); err != nil {
		return Session{}, fmt.Errorf("%w：session JSON 解析失败", ErrBadSession)
	}
	s := Session{SessionToken: gptcheck.ParseSessionToken(v)}
	if at, err := gptcheck.ParseAccessToken(v); err == nil {
		s.AccessToken = at
	}
	if data.User != nil {
		s.Email = strings.ToLower(strings.TrimSpace(data.User.Email))
	}
	if s.SessionToken == "" {
		return Session{}, fmt.Errorf("%w：缺少 sessionToken", ErrBadSession)
	}
	if s.Email == "" {
		return Session{}, fmt.Errorf("%w：缺少用户邮箱（user.email）", ErrBadSession)
	}
	return s, nil
}

// SessionCookies 把 sessionToken 转成 cookie，超长时自动分片。
func SessionCookies(sessionToken string) []Cookie {
	if len(sessionToken) <= cookieChunkSize {
		return []Cookie{{Name: sessionCookieName, Value: sessionToken}}
	}
	var out []Cookie
	for i, idx := 0, 0; i < len(sessionToken); i, idx = i+cookieChunkSize, idx+1 {
		end := min(i+cookieChunkSize, len(sessionToken))
		out = append(out, Cookie{Name: fmt.Sprintf("%s.%d", sessionCookieName, idx), Value: sessionToken[i:end]})
	}
	return out
}
