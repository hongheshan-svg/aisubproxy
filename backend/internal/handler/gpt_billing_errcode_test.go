package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/gptcheck"
)

// 账单查询的每一处报错都带 error_code 和 error 文字（旧页面靠 error 文字显示）。
func TestBillingCheckErrorCodes(t *testing.T) {
	type tc struct {
		name   string
		setup  func(t *testing.T)
		body   string
		status int
		code   string
		errMsg string
	}
	cases := []tc{
		{
			name: "请求体不是 JSON", body: `not json`,
			status: http.StatusBadRequest, code: "invalid_request", errMsg: "invalid request",
		},
		{
			name: "什么都没填", body: `{}`,
			status: http.StatusBadRequest, code: "input_required", errMsg: "请输入卡密，或粘贴 session JSON / accessToken",
		},
		{
			name: "粘贴的登录信息无效", body: `{"token_input":"bad"}`,
			setup: func(t *testing.T) {
				old := billingSessionCheck
				billingSessionCheck = func(string) (*gptcheck.Result, error) { return nil, errors.New("登录信息已过期") }
				t.Cleanup(func() { billingSessionCheck = old })
			},
			status: http.StatusBadRequest, code: "session_invalid", errMsg: "登录信息已过期",
		},
		{
			// 凭卡密查询的其余报错（没开通成功、已换码、读库出错）见 gpt_billing_cdk_test.go
			name: "认不出的卡密", body: `{"cdk_code":"TU-NOT-BOUND-CODE-1"}`,
			status: http.StatusNotFound, code: "cdk_not_found", errMsg: "未找到这张卡密",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dbtest.Open(t)
			dbtest.SetMasterKey(t)
			if c.setup != nil {
				c.setup(t)
			}
			w := cdkRequest(SessionBillingCheck, "POST", "/billing", c.body)
			if w.Code != c.status {
				t.Fatalf("状态码应为 %d，实际 %d: %s", c.status, w.Code, w.Body.String())
			}
			var d map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
				t.Fatal(err)
			}
			if d["error_code"] != c.code {
				t.Fatalf("error_code 应为 %q，实际 %v: %s", c.code, d["error_code"], w.Body.String())
			}
			if d["error"] != c.errMsg {
				t.Fatalf("error 文字应不变 %q，实际 %v", c.errMsg, d["error"])
			}
		})
	}
}
