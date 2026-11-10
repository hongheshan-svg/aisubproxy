package handler

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/gptcheck"
)

// billingByCDK 凭卡密查账单，返回状态码和解析后的 JSON。
func billingByCDK(t *testing.T, code string) (int, map[string]any, string) {
	t.Helper()
	w := cdkRequest(SessionBillingCheck, "POST", "/billing", localBody(t, map[string]any{"cdk_code": code}))
	var d map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("响应不是 JSON: %d %s", w.Code, w.Body.String())
	}
	return w.Code, d, w.Body.String()
}

// forbidStoredSessionCheck 凭卡密查账单绝不能拿存储的会话去调 ChatGPT（审计 F-1）。
func forbidStoredSessionCheck(t *testing.T) {
	t.Helper()
	old := billingSessionCheck
	billingSessionCheck = func(string) (*gptcheck.Result, error) {
		t.Fatal("凭卡密查账单不能调 ChatGPT")
		return nil, nil
	}
	t.Cleanup(func() { billingSessionCheck = old })
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 开通成功的 MarsWallex 码：返回本站开通记录，字段只走白名单，邮箱打码，不给成本、卡、订单号，不调上游。
func TestBillingByCDKShowsLocalRecord(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	forbidStoredSessionCheck(t)
	until := time.Now().Add(30 * 24 * time.Hour).Unix()

	code, id := e.issueCode("plus", "PH")
	no := e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, "buyer@example.com", "succeeded", "", true)
	setOrderSQL(t, no, `amount_minor = ?, currency = ?, renewal_state = ?, access_until = ?, fund_cents = ?, card_id = ?, card_last4 = ?`,
		112000, "PHP", "cancelled", until, int64(98765432123), "card-secret-id", "L4ST")
	var finished int64
	if err := db.DB.QueryRow(`SELECT finished_at FROM pay_orders WHERE order_no = ?`, no).Scan(&finished); err != nil {
		t.Fatal(err)
	}

	status, d, raw := billingByCDK(t, code)
	if status != http.StatusOK {
		t.Fatalf("应 200: %d %s", status, raw)
	}
	if d["auth_source"] != "cdk" {
		t.Fatalf("来源应为 cdk: %v", d["auth_source"])
	}
	if got := sortedKeys(d); strings.Join(got, ",") != "auth_source,note,record" {
		t.Fatalf("顶层字段只能有 auth_source、note、record: %v", got)
	}
	if d["note"] != billingCDKNote {
		t.Fatalf("说明文字: %v", d["note"])
	}
	rec, _ := d["record"].(map[string]any)
	want := map[string]any{
		"email":        maskEmailForAdmin("buyer@example.com"),
		"plan":         "plus",
		"plan_kind":    "subscription",
		"redeemed_at":  float64(finished),
		"access_until": float64(until),
		"renewal_off":  true,
		"amount":       "1120.00",
		"currency":     "PHP",
	}
	if strings.Join(sortedKeys(rec), ",") != strings.Join(sortedKeys(want), ",") {
		t.Fatalf("record 字段不对: %v", sortedKeys(rec))
	}
	for k, v := range want {
		if rec[k] != v {
			t.Fatalf("record.%s = %v，想要 %v", k, rec[k], v)
		}
	}
	for _, bad := range []string{"buyer@example.com", no, "card-secret-id", "L4ST", "98765432123", "fund", "order", "session", "invoice_url", "hosted"} {
		if strings.Contains(raw, bad) {
			t.Fatalf("响应里不该出现 %q: %s", bad, raw)
		}
	}
	if e.up.callCount() != 0 {
		t.Fatalf("不应碰上游: %s", e.up.log())
	}
}

func TestBillingByCDKRecordVariants(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	forbidStoredSessionCheck(t)

	consumed := func(t *testing.T, plan, set string, args ...any) map[string]any {
		t.Helper()
		code, id := e.issueCode(plan, "PH")
		no := e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, "buyer@example.com", "succeeded", "", true)
		if set != "" {
			setOrderSQL(t, no, set, args...)
		}
		status, d, raw := billingByCDK(t, code)
		if status != http.StatusOK {
			t.Fatalf("应 200: %d %s", status, raw)
		}
		rec, _ := d["record"].(map[string]any)
		return rec
	}

	t.Run("续费还没核实关掉：renewal_off=false", func(t *testing.T) {
		for _, st := range []string{"pending", "failed", "skipped", ""} {
			rec := consumed(t, "plus", `renewal_state = ?, amount_minor = 112000, currency = 'PHP'`, st)
			if rec["renewal_off"] != false {
				t.Fatalf("renewal_state=%q 时 renewal_off 应为 false: %v", st, rec["renewal_off"])
			}
		}
	})
	t.Run("日元金额不带小数", func(t *testing.T) {
		rec := consumed(t, "plus", `amount_minor = 3000, currency = 'JPY'`)
		if rec["amount"] != "3000" || rec["currency"] != "JPY" {
			t.Fatalf("%v", rec)
		}
	})
	t.Run("金额未知时不给金额和币种，也不给到期时间", func(t *testing.T) {
		rec := consumed(t, "plus", "")
		for _, k := range []string{"amount", "currency", "access_until"} {
			if _, ok := rec[k]; ok {
				t.Fatalf("不该有 %s: %v", k, rec)
			}
		}
		if rec["redeemed_at"] == nil || rec["plan"] != "plus" {
			t.Fatalf("%v", rec)
		}
	})
	t.Run("点数码", func(t *testing.T) {
		e.enableCredit()
		e.seedRealCreditPayment()
		rec := consumed(t, "credit250", `amount_minor = 55000, currency = 'PHP'`)
		if rec["plan"] != "credit250" || rec["plan_kind"] != "credit" || rec["amount"] != "550.00" {
			t.Fatalf("%v", rec)
		}
	})
}

// 没开通成功、已换码、认不出的码：各回 404 和对应错误码；读库出错回 500。都不调上游。
func TestBillingByCDKNotRedeemed(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	forbidStoredSessionCheck(t)
	ref := db.LocalCDKSourceRef

	expect := func(t *testing.T, code string, status int, errCode, errMsg string) map[string]any {
		t.Helper()
		got, d, raw := billingByCDK(t, code)
		if got != status || d["error_code"] != errCode || d["error"] != errMsg {
			t.Fatalf("想要 %d %s %q，得到 %d %s", status, errCode, errMsg, got, raw)
		}
		if _, ok := d["record"]; ok {
			t.Fatalf("出错时不该有 record: %s", raw)
		}
		return d
	}

	t.Run("从未兑换", func(t *testing.T) {
		code, _ := e.issueCode("plus", "PH")
		d := expect(t, code, http.StatusNotFound, "cdk_not_redeemed", billingCDKNotRedeemedText)
		if d["status"] != "unused" {
			t.Fatalf("应附状态 unused: %v", d)
		}
	})
	t.Run("处理中", func(t *testing.T) {
		code, id := e.issueCode("plus", "PH")
		e.insertOrder("cdk", ref(id), 0, "a@example.com", "queued", "", false)
		if d := expect(t, code, http.StatusNotFound, "cdk_not_redeemed", billingCDKNotRedeemedText); d["status"] != "processing" {
			t.Fatalf("%v", d)
		}
	})
	t.Run("失败转人工", func(t *testing.T) {
		code, id := e.issueCode("plus", "PH")
		e.insertOrder("cdk", ref(id), 0, "a@example.com", "failed", "declined", true)
		if d := expect(t, code, http.StatusNotFound, "cdk_not_redeemed", billingCDKNotRedeemedText); d["status"] != "failed" {
			t.Fatalf("%v", d)
		}
	})
	t.Run("人工禁用", func(t *testing.T) {
		code, id := e.issueCode("plus", "PH")
		if _, err := db.DB.Exec(`UPDATE local_cdks SET status = 'disabled' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if d := expect(t, code, http.StatusNotFound, "cdk_not_redeemed", billingCDKNotRedeemedText); d["status"] != "disabled" {
			t.Fatalf("%v", d)
		}
	})
	t.Run("认不出的码（含历史 ZovoCard 码）", func(t *testing.T) {
		for _, code := range []string{"ZC-AAAA-BBBB-CCCC-DDDD", "TU-NOT-BOUND-CODE-1", "not-a-code"} {
			expect(t, code, http.StatusNotFound, "cdk_not_found", billingCDKNotFoundText)
		}
	})
	t.Run("读库出错", func(t *testing.T) {
		code, _ := e.issueCode("plus", "PH")
		if _, err := db.DB.Exec(`DROP TABLE pay_orders`); err != nil {
			t.Fatal(err)
		}
		expect(t, code, http.StatusInternalServerError, "lookup_failed", billingCDKLookupFailedText)
	})
	if e.up.callCount() != 0 {
		t.Fatalf("不应碰上游: %s", e.up.log())
	}
}

func TestBillingByCDKReplaced(t *testing.T) {
	e := newSealedExchangeEnv(t)
	forbidStoredSessionCheck(t)
	code, _ := exchangeableCode(t, e)
	_, newCode, _ := doSealedExchange(t, e, code)

	status, d, raw := billingByCDK(t, code)
	if status != http.StatusNotFound || d["error_code"] != "cdk_replaced" || d["error"] != billingCDKReplacedText {
		t.Fatalf("已换码应 404 cdk_replaced: %d %s", status, raw)
	}
	if strings.Contains(raw, newCode) {
		t.Fatalf("凭旧码查账单不该给出新码: %s", raw)
	}
}
