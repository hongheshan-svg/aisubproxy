package chatgpt

import (
	"context"
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
)

// fakeTLS 只替换 Do：panic，或者返回 (nil, nil)（tls-client 开了 catchPanics 时就会这样）。其余方法用不到。
type fakeTLS struct {
	tls_client.HttpClient
	panicDo bool
}

func (f *fakeTLS) Do(*fhttp.Request) (*fhttp.Response, error) {
	if f.panicDo {
		panic("tls 栈内部出错 st-secret-456")
	}
	return nil, nil
}

// F2：tls-client 在异常响应上会 panic；do() 自己兜住转成错误，不让付款单协程、确认协程崩掉。返回 nil 响应也一样。
func TestDoRecoversPanicAndNilResponse(t *testing.T) {
	for name, f := range map[string]*fakeTLS{"panic": {panicDo: true}, "nil 响应": {}} {
		t.Run(name, func(t *testing.T) {
			c := &Client{BaseURL: "https://chatgpt.invalid", http: f}
			_, err := c.Account(context.Background(), Session{AccessToken: "at", SessionToken: "st-secret-456"})
			if err == nil || !strings.Contains(err.Error(), "ChatGPT 请求内部错误") {
				t.Fatalf("应返回内部错误: %v", err)
			}
			if strings.Contains(err.Error(), "st-secret-456") {
				t.Fatalf("错误不应带 panic 的值: %v", err)
			}
		})
	}
}
