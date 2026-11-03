package chatgpt

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// PF-4：Cloudflare 挑战返回 403，但不是会话失效，要报 ErrBlocked。
func TestCloudflareChallengeMapsToErrBlocked(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"cf-mitigated 头": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("cf-mitigated", "challenge")
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("<html><title>Just a moment...</title></html>"))
		},
		"只有 HTML 挑战页": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("<!DOCTYPE html><html><head><title>Just a moment...</title></head></html>"))
		},
		"Server 是 cloudflare 的 HTML 403": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Server", "cloudflare")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("<html>blocked</html>"))
		},
		"挑战头配 200 状态": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("cf-mitigated", "challenge")
			w.Write([]byte("{}"))
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, h)
			s := Session{AccessToken: "x", SessionToken: "y"}
			if err := c.Refresh(context.Background(), &s); !errors.Is(err, ErrBlocked) || errors.Is(err, ErrUnauthorized) {
				t.Errorf("Refresh: %v", err)
			}
			if _, err := c.Account(context.Background(), s); !errors.Is(err, ErrBlocked) || errors.Is(err, ErrUnauthorized) {
				t.Errorf("Account: %v", err)
			}
			if _, err := c.CreateCheckout(context.Background(), s, "p", "PH", "PHP"); !errors.Is(err, ErrBlocked) || errors.Is(err, ErrUnauthorized) {
				t.Errorf("CreateCheckout: %v", err)
			}
		})
	}
}

// ChatGPT 自己的 401/403 是 JSON，仍然是会话失效，不能被误判成被拦。
func TestJSONForbiddenStaysUnauthorized(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"detail":"Forbidden"}`))
	}))
	s := Session{AccessToken: "x", SessionToken: "y"}
	if err := c.Refresh(context.Background(), &s); !errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrBlocked) {
		t.Errorf("JSON 403 应是会话失效: %v", err)
	}
}

func TestIsCFBlocked(t *testing.T) {
	cases := []struct {
		name               string
		status             int
		mit, ctype, server string
		body               string
		want               bool
	}{
		{"头优先", 403, "challenge", "", "", "", true},
		{"200 的 HTML 不算", 200, "", "text/html", "cloudflare", "just a moment", false},
		{"普通 HTML 500", 500, "", "text/html", "nginx", "<html>oops</html>", false},
		{"挑战平台脚本", 503, "", "text/html", "", `<script src="/cdn-cgi/challenge-platform/h/b"></script>`, true},
	}
	for _, c := range cases {
		if got := isCFBlocked(c.status, c.mit, c.ctype, c.server, []byte(c.body)); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
