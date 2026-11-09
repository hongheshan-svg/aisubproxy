package chatgpt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
)

// 已知问题清单【涉及敏感数据】：客户端把底层错误原样包装返回，错误里带代理地址时会带出代理账号密码。
// 现在在客户端这一层就把代理地址、账号密码（含 URL 编码写法）换成 ***，错误类型照常可用。

const (
	scrubProxyUser = "proxyuser"
	scrubProxyPass = "Pr0xy-Pass!x"
	scrubProxyURL  = "http://" + scrubProxyUser + ":" + scrubProxyPass + "@10.0.0.1:3128"
)

type errTLS struct {
	tls_client.HttpClient
	err error
}

func (f *errTLS) Do(*fhttp.Request) (*fhttp.Response, error) { return nil, f.err }

func TestClientErrorsHideProxyCredentials(t *testing.T) {
	c, err := New(scrubProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	escaped := "http://" + scrubProxyUser + ":Pr0xy-Pass%21x@10.0.0.1:3128"
	c.http = &errTLS{err: fmt.Errorf("proxyconnect tcp: dial %s failed (raw %s:%s, pw %s): %w",
		escaped, scrubProxyUser, scrubProxyPass, scrubProxyPass, context.DeadlineExceeded)}

	_, err = c.Account(context.Background(), Session{AccessToken: "at"})
	if err == nil {
		t.Fatal("应返回错误")
	}
	text := err.Error()
	for _, leak := range []string{scrubProxyPass, "Pr0xy-Pass%21x", scrubProxyUser + ":"} {
		if strings.Contains(text, leak) {
			t.Fatalf("错误不应带代理账号密码 %q: %s", leak, text)
		}
	}
	if !strings.Contains(text, "请求 ChatGPT 失败") || !strings.Contains(text, "proxyconnect") {
		t.Fatalf("其余错误文字应保留，便于排查: %s", text)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("errors.Is 应照常可用（确认协程靠它区分超时）")
	}
}

// 直连和没有账号密码的代理：错误文本原样返回。
func TestClientErrorsUnchangedWithoutProxyCredentials(t *testing.T) {
	for _, proxy := range []string{"", "http://10.0.0.1:3128"} {
		c, err := New(proxy)
		if err != nil {
			t.Fatal(err)
		}
		c.http = &errTLS{err: errors.New("dial tcp 10.0.0.1:3128: connection refused")}
		_, err = c.Account(context.Background(), Session{AccessToken: "at"})
		if err == nil || !strings.Contains(err.Error(), "dial tcp 10.0.0.1:3128: connection refused") {
			t.Fatalf("代理 %q：错误文字应原样保留: %v", proxy, err)
		}
	}
}

// 很短的账号、没有密码时，不能把错误说明里常见的字母都换掉。
func TestProxySecretsSkipShort(t *testing.T) {
	for _, s := range proxySecrets("http://a@10.0.0.1:3128") {
		if len(s) < 4 {
			t.Fatalf("不足 4 个字符的写法不应替换: %q", s)
		}
	}
	if got := scrubErr(errors.New("a: dial failed"), proxySecrets("http://a@10.0.0.1:3128")); got.Error() != "a: dial failed" {
		t.Fatalf("短账号不应误换: %s", got)
	}
	if proxySecrets("http://10.0.0.1:3128") != nil || proxySecrets("::bad") != nil {
		t.Fatal("没有账号密码或地址无效时不需要替换")
	}
}
