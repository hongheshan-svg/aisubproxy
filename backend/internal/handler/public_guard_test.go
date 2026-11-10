package handler

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

// bigJSON 是一个超过 256 KB 的合法 JSON：码是假上游 testup 认领的，进了业务逻辑就会转发给它。
func bigJSON() string {
	return `{"code":"TU-AAAA-0001","text":"TU-AAAA-0001","pad":"` + strings.Repeat("x", 300<<10) + `"}`
}

// readCounter 记下请求体有没有被读过；长度未知，走 MaxBytesReader 的路径。
type readCounter struct {
	r     io.Reader
	reads atomic.Int32
}

func (c *readCounter) Read(p []byte) (int, error) {
	c.reads.Add(1)
	return c.r.Read(p)
}

func (e *localEnv) doBody(method, path string, body io.Reader, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	if ip != "" {
		req.RemoteAddr = ip + ":1234"
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// F1：公开兑换和查询接口的请求体超过 256 KB 返回 413，不进业务逻辑（不转发上游、不建单）。
// 长度已知（Content-Length）和长度未知（分块上传）两种都要拦。
func TestPublicBodyTooLarge(t *testing.T) {
	e := newLocalEnv(t)
	for _, path := range []string{"/public/cdk/preview", "/public/cdk/redeem", "/lookup/cdk/batch"} {
		for name, body := range map[string]func() io.Reader{
			"已知长度": func() io.Reader { return strings.NewReader(bigJSON()) },
			"未知长度": func() io.Reader { return &readCounter{r: strings.NewReader(bigJSON())} },
		} {
			w := e.doBody("POST", path, body(), "")
			if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "请求太大") {
				t.Fatalf("%s %s: %d %s", path, name, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"error_code":"body_too_large"`) {
				t.Fatalf("%s %s: 缺 error_code: %s", path, name, w.Body.String())
			}
		}
	}
	if e.up.callCount() != 0 {
		t.Fatalf("请求太大不应进业务逻辑: %s", e.up.log())
	}
	// 没超的照常处理
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	if w := e.do("POST", "/public/cdk/preview", `{"code":"`+code+`"}`); w.Code != http.StatusOK {
		t.Fatalf("正常大小的请求应照常处理: %d %s", w.Code, w.Body.String())
	}
}

// F1：限流放在读请求体之前，所有方法、各家服务商的码都算：超了额度的 IP 带着大请求体来，也不读体就拒。
func TestPublicRateLimitBeforeBody(t *testing.T) {
	e := newLocalEnv(t)
	const ip = "198.51.100.7"
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	for i := 0; i < localRateLimits[localRateMutate]; i++ {
		if w := e.doBody("POST", "/public/cdk/preview", strings.NewReader(`{"code":"`+code+`"}`), ip); w.Code != http.StatusOK {
			t.Fatalf("第 %d 次: %d %s", i, w.Code, w.Body.String())
		}
	}
	body := &readCounter{r: strings.NewReader(bigJSON())}
	if w := e.doBody("POST", "/public/cdk/redeem", body, ip); w.Code != http.StatusTooManyRequests {
		t.Fatalf("超额应 429: %d %s", w.Code, w.Body.String())
	} else if !strings.Contains(w.Body.String(), "请求过于频繁，请稍后再试") || !strings.Contains(w.Body.String(), `"error_code":"rate_limited"`) {
		t.Fatalf("429 的 error 文字或 error_code 不对: %s", w.Body.String())
	}
	if body.reads.Load() != 0 {
		t.Fatal("限流应在读请求体之前")
	}
	// 查询接口是另一个桶，照常可用；别的 IP 不受影响
	if w := e.doBody("POST", "/lookup/cdk/batch", strings.NewReader(`{"codes":["`+code+`"]}`), ip); w.Code == http.StatusTooManyRequests {
		t.Fatalf("查询不该被变更接口的额度挡住: %d", w.Code)
	}
	if w := e.doBody("POST", "/public/cdk/preview", strings.NewReader(`{"code":"`+code+`"}`), "198.51.100.8"); w.Code != http.StatusOK {
		t.Fatalf("别的 IP 不受影响: %d", w.Code)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("连接断了") }

// 读请求体出错（不是超大）：400，原文 invalid request 不变，带 error_code。
func TestPublicGuardReadErrorCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/x", publicGuard(func(*gin.Context) string { return localRateQuery }), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest("POST", "/x", failingReader{})
	req.ContentLength = -1
	req.RemoteAddr = "203.0.113.77:1234"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"error":"invalid request"`) || !strings.Contains(w.Body.String(), `"error_code":"invalid_request"`) {
		t.Fatalf("读体出错应 400 且带 invalid_request: %d %s", w.Code, w.Body.String())
	}
}
