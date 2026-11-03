package chatgpt

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// ChatGPT 返回非 2xx 时，错误里要带上响应里能说明原因的那部分（detail、error、Retry-After、纯文本、HTML 标题），
// 否则只看得到「HTTP 400」「HTTP 429」，没法判断是请求体不对、被限流还是别的原因。令牌一律打码，正文不整段透出。
func TestHTTPErrorKeepsSafeDetail(t *testing.T) {
	long := strings.Repeat("很长的说明", 200)
	cases := []struct {
		name                    string
		status                  int
		ctype, retryAfter, body string
		want, notWant           []string
		exact                   string
	}{
		{name: "detail 字符串", status: 400, ctype: "application/json", body: `{"detail":"Invalid plan_name"}`,
			want: []string{"ChatGPT 返回 HTTP 400", "Invalid plan_name"}},
		{name: "校验错误数组", status: 400, ctype: "application/json",
			body: `{"detail":[{"loc":["body","checkout_ui_mode"],"msg":"field required","type":"value_error.missing"}]}`,
			want: []string{"HTTP 400", "body.checkout_ui_mode", "field required"}},
		{name: "detail 对象", status: 400, ctype: "application/json",
			body: `{"detail":{"code":"unsupported_country","message":"Country not supported"}}`,
			want: []string{"unsupported_country", "Country not supported"}},
		{name: "error 对象", status: 400, ctype: "application/json",
			body: `{"error":{"message":"Bad currency","type":"invalid_request_error","code":"invalid_currency"}}`,
			want: []string{"Bad currency", "invalid_currency"}},
		{name: "429 带 Retry-After", status: 429, ctype: "application/json", retryAfter: "30",
			body: `{"detail":"Too many requests"}`, want: []string{"HTTP 429", "30 秒", "Too many requests"}},
		{name: "429 纯文本", status: 429, ctype: "text/plain; charset=utf-8", body: "error code: 1015",
			want: []string{"HTTP 429", "error code: 1015"}},
		{name: "令牌打码", status: 400, ctype: "application/json",
			body: `{"detail":"bad token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJlLXNpZ25hdHVyZQ"}`,
			want: []string{"HTTP 400", "bad token"}, notWant: []string{"eyJ"}},
		{name: "HTML 只取标题", status: 404, ctype: "text/html",
			body: `<html><head><title>Not Found</title></head><body>页面正文不要</body></html>`,
			want: []string{"HTTP 404", "Not Found"}, notWant: []string{"页面正文不要"}},
		{name: "过长截断", status: 400, ctype: "application/json", body: `{"detail":"` + long + `"}`,
			want: []string{"HTTP 400", "很长的说明"}},
		{name: "空响应体", status: 500, ctype: "application/json", body: ``, exact: "ChatGPT 返回 HTTP 500"},
		{name: "JSON 里没有说明", status: 500, ctype: "application/json", body: `{"foo":1}`, exact: "ChatGPT 返回 HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.ctype)
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			_, err := c.Account(context.Background(), Session{AccessToken: "at"})
			if err == nil {
				t.Fatal("应返回错误")
			}
			msg := err.Error()
			if tc.exact != "" && msg != tc.exact {
				t.Fatalf("错误 = %q，应为 %q", msg, tc.exact)
			}
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("错误 %q 缺少 %q", msg, w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(msg, w) {
					t.Errorf("错误 %q 不应包含 %q", msg, w)
				}
			}
			if n := utf8.RuneCountInString(msg); n > 260 {
				t.Errorf("错误太长（%d 字）：%q", n, msg)
			}
		})
	}
}
