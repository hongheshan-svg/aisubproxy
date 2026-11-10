package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/provider/providertest"
)

// 代理换码的每一处报错都带 error_code，error 文字、状态码、其他字段与原来一致（旧页面靠 error 文字显示）。

// swapPost 发一份原始请求体，password 和 code 之外的内容也能测。
func swapPost(body, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/exchange", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	exchangeRouter().ServeHTTP(w, req)
	return w
}

func swapBody(password, code string) string {
	b, _ := json.Marshal(map[string]string{"password": password, "code": code})
	return string(b)
}

func decodeSwap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("响应不是 JSON: %v: %s", err, w.Body.String())
	}
	return d
}

func TestAgentExchangeErrorCodes(t *testing.T) {
	const ip = "203.0.113.9"
	type tc struct {
		name   string
		run    func(t *testing.T) *httptest.ResponseRecorder
		status int
		code   string
		errMsg string // 空串表示不比对（带数字的文字另行断言）
		extra  map[string]any
	}
	seed := func(t *testing.T) {
		dbtest.Open(t)
		seedExchange(t, exchangeOldCode)
		captureOpsAlerts(t)
	}
	cases := []tc{
		{
			name: "限流", status: http.StatusTooManyRequests, code: "rate_limited", errMsg: "请求过于频繁，请稍后再试",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				var w *httptest.ResponseRecorder
				for i := 0; i < 21; i++ {
					w = swapPost(`bad`, ip)
				}
				return w
			},
		},
		{
			name: "功能未启用", status: http.StatusServiceUnavailable, code: "disabled", errMsg: "管理员尚未启用代理换码功能",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				dbtest.Open(t)
				resetLocalRate()
				return swapPost(swapBody(exchangePassword, exchangeOldCode), ip)
			},
		},
		{
			name: "请求体不是 JSON", status: http.StatusBadRequest, code: "invalid_request", errMsg: "invalid request",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				return swapPost(`not json`, ip)
			},
		},
		{
			name: "口令锁定", status: http.StatusTooManyRequests, code: "locked",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				t.Cleanup(func() { swapPasswordFails.succeeded(swapPasswordFailKey) })
				for i := 0; i < swapPasswordMaxFailures; i++ {
					swapPost(swapBody("wrong-password-x", exchangeOldCode), fmt.Sprintf("198.51.100.%d", i+1))
				}
				return swapPost(swapBody(exchangePassword, exchangeOldCode), "203.0.113.200")
			},
		},
		{
			name: "口令错误", status: http.StatusUnauthorized, code: "wrong_password", errMsg: "密码错误",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				return swapPost(swapBody("wrong-password-x", exchangeOldCode), ip)
			},
		},
		{
			name: "卡密太短", status: http.StatusBadRequest, code: "code_required", errMsg: "请输入完整卡密",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				return swapPost(swapBody(exchangePassword, "short"), ip)
			},
		},
		{
			name: "服务商不支持换码", status: http.StatusBadRequest, code: "provider_unsupported", errMsg: "该服务商不支持代理换码",
			run: func(t *testing.T) *httptest.ResponseRecorder {
				seed(t)
				// 上游型服务商认领的码：代理换码只有本站码一条路
				providertest.Register(t, providertest.NewTestUp())
				return swapPost(swapBody(exchangePassword, "TU-AAAA-BBBB-CCCC"), ip)
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := c.run(t)
			if w.Code != c.status {
				t.Fatalf("状态码应为 %d，实际 %d: %s", c.status, w.Code, w.Body.String())
			}
			d := decodeSwap(t, w)
			if d["error_code"] != c.code {
				t.Fatalf("error_code 应为 %q，实际 %v: %s", c.code, d["error_code"], w.Body.String())
			}
			if c.errMsg != "" && d["error"] != c.errMsg {
				t.Fatalf("error 文字应不变 %q，实际 %v", c.errMsg, d["error"])
			}
			for k, v := range c.extra {
				if d[k] != v {
					t.Fatalf("字段 %s 应为 %v，实际 %v: %s", k, v, d[k], w.Body.String())
				}
			}
			if c.code == "locked" {
				mins, ok := d["retry_after_minutes"].(float64)
				if !ok || mins < 1 || mins > 60 {
					t.Fatalf("locked 应带 1~60 的 retry_after_minutes: %s", w.Body.String())
				}
				if want := fmt.Sprintf("换码口令错误次数过多，已暂停换码，请 %d 分钟后再试", int(mins)); d["error"] != want {
					t.Fatalf("locked 的 error 应为 %q，实际 %v", want, d["error"])
				}
			}
		})
	}
}
