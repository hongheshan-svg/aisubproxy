package handler

import (
	"net/http"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/provider/providertest"
)

// 代理换码的本站码（MarsWallex）路径：每一处报错也带 error_code，error 文字和状态码与原来一致。
func TestLocalCDKExchangeErrorCodes(t *testing.T) {
	post := func(e *localEnv, code string) (int, map[string]any) {
		w := e.do("POST", "/public/cdk/exchange", localBody(e.t, map[string]any{"password": sealedSwapPassword, "code": code}))
		return w.Code, localJSON(e.t, w)
	}
	cases := []struct {
		name   string
		run    func(t *testing.T) (int, map[string]any)
		status int
		code   string
		errMsg string
		extra  map[string]any
	}{
		{
			name: "无法识别的卡密", status: http.StatusNotFound, code: "code_unrecognized", errMsg: "无法识别的卡密",
			run: func(t *testing.T) (int, map[string]any) {
				e := newSealedExchangeEnv(t)
				// 没有兜底上游时，认不出的码在分派那一步就被拒
				t.Cleanup(provider.TestOnlySnapshot())
				provider.TestOnlyReset()
				provider.Register(providertest.NewTestCard())
				return post(e, "ZC-AAAA-BBBB-CCCC-DDDD")
			},
		},
		{
			name: "本站没有这张码", status: http.StatusNotFound, code: "code_not_found", errMsg: "本站未找到该完整卡密（仅支持本站发过的码）",
			run: func(t *testing.T) (int, map[string]any) {
				e := newSealedExchangeEnv(t)
				code, id := e.issueCode("plus", "PH")
				if _, err := db.DB.Exec(`DELETE FROM local_cdks WHERE id = ?`, id); err != nil {
					t.Fatal(err)
				}
				return post(e, code)
			},
		},
		{
			name: "没有失败记录", status: http.StatusBadRequest, code: "no_failed_record", errMsg: "该卡密尚无兑换失败记录，不能换新码",
			extra: map[string]any{"hint": "仅限充值失败且未扣款的卡密"},
			run: func(t *testing.T) (int, map[string]any) {
				e := newSealedExchangeEnv(t)
				code, _ := e.issueCode("plus", "PH")
				return post(e, code)
			},
		},
		{
			name: "已发起过支付", status: http.StatusBadRequest, code: "not_eligible_unpaid", errMsg: "不符合换码条件（需充值失败且未发起支付）",
			run: func(t *testing.T) (int, map[string]any) {
				e := newSealedExchangeEnv(t)
				code, id := e.issueCode("plus", "PH")
				e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, "a@example.com", "failed", "declined", true)
				return post(e, code)
			},
		},
		{
			name: "同一张码正在处理", status: http.StatusConflict, code: "busy", errMsg: "该卡密正在处理中，请勿重复提交",
			run: func(t *testing.T) (int, map[string]any) {
				e := newSealedExchangeEnv(t)
				code, id := exchangeableCode(t, e)
				unlock, ok := lockLocalCDK(id)
				if !ok {
					t.Fatal("拿不到锁")
				}
				t.Cleanup(unlock)
				return post(e, code)
			},
		},
		{
			name: "硬门槛没过", status: http.StatusServiceUnavailable, code: "service_unavailable", errMsg: "兑换通道暂时不可用，请稍后再试",
			run: func(t *testing.T) (int, map[string]any) {
				e := newSealedExchangeEnv(t)
				code, _ := exchangeableCode(t, e)
				// 发码之后撤掉那笔真实付款记录，换码的硬门槛过不去
				if _, err := db.DB.Exec(`DELETE FROM pay_orders WHERE source = 'admin_test'`); err != nil {
					t.Fatal(err)
				}
				return post(e, code)
			},
		},
		{
			name: "已换过新码", status: http.StatusConflict, code: "conflict", errMsg: "该卡密已换过新码，不可重复兑换",
			extra: map[string]any{"state": db.AgentExchangeDone},
			run: func(t *testing.T) (int, map[string]any) {
				e := newSealedExchangeEnv(t)
				code, _ := exchangeableCode(t, e)
				if st, m := post(e, code); st != 200 {
					t.Fatalf("第一次应成功: %d %v", st, m)
				}
				return post(e, code)
			},
		},
		{
			name: "今日额度用完", status: http.StatusTooManyRequests, code: "daily_limit", errMsg: "代理换码已达今日上限（1 张），请联系管理员",
			extra: map[string]any{"limit": float64(1)},
			run: func(t *testing.T) (int, map[string]any) {
				e := newSealedExchangeEnv(t)
				if err := db.SetSetting(agentSwapDailyLimitKey, "1"); err != nil {
					t.Fatal(err)
				}
				c1, _ := exchangeableCode(t, e)
				c2, _ := exchangeableCode(t, e)
				if st, m := post(e, c1); st != 200 {
					t.Fatalf("额度内应成功: %d %v", st, m)
				}
				return post(e, c2)
			},
		},
		{
			name: "占位失败", status: http.StatusInternalServerError, code: "unavailable", errMsg: "换码暂时不可用，请稍后再试",
			run: func(t *testing.T) (int, map[string]any) {
				e := newSealedExchangeEnv(t)
				code, _ := exchangeableCode(t, e)
				if _, err := db.DB.Exec(`DROP TABLE agent_cdk_exchanges`); err != nil {
					t.Fatal(err)
				}
				return post(e, code)
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, m := c.run(t)
			if status != c.status {
				t.Fatalf("状态码应为 %d，实际 %d: %v", c.status, status, m)
			}
			if m["error_code"] != c.code {
				t.Fatalf("error_code 应为 %q，实际 %v: %v", c.code, m["error_code"], m)
			}
			if m["error"] != c.errMsg {
				t.Fatalf("error 文字应不变 %q，实际 %v", c.errMsg, m["error"])
			}
			for k, v := range c.extra {
				if m[k] != v {
					t.Fatalf("字段 %s 应为 %v，实际 %v: %v", k, v, m[k], m)
				}
			}
		})
	}
}
