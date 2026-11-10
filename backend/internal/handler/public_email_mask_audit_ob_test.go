package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// F-1：凭卡密就能调的公开接口（凭码查账单、按码 / 按令牌查兑换结果）不能给出完整邮箱和账单链接，
// 和售后查询的打码规则一致。知道卡密不等于本人：卖码的代理、看过截图的人都可能知道。

const obMaskedSessEmail = "bu***@example.com" // maskEmailForAdmin(cdkSessEmail)

func obAssertNoLeak(t *testing.T, what, body string) {
	t.Helper()
	for _, s := range []string{cdkSessEmail, "invoice.example", "Real Buyer Name", cdkSessToken} {
		if strings.Contains(body, s) {
			t.Fatalf("%s 的响应不应包含 %q: %s", what, s, body)
		}
	}
}

// 凭卡密查账单（本站码走完真实兑换后）：邮箱打码，会话、令牌、账单链接都不出现。
func TestObBillingCheckByCDKMasksEmail(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	calls := stubBillingCheck(t)
	code, id := e.issueCode("plus", "PH")
	token, pf := e.preflight(code, localSession(cdkSessEmail))
	if w := e.redeem(token, pf); w.Code != http.StatusOK {
		t.Fatalf("兑换: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.DB.Exec(`UPDATE pay_orders SET state = 'succeeded', finished_at = 1700000000, amount_minor = 112000, currency = 'PHP'
		WHERE source = 'cdk' AND source_ref = ?`, db.LocalCDKSourceRef(id)); err != nil {
		t.Fatal(err)
	}
	w := cdkRequest(SessionBillingCheck, "POST", "/billing", `{"cdk_code":"`+code+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	obAssertNoLeak(t, "凭码查账单", w.Body.String())
	for _, secret := range []string{payAccessToken, paySessionToken, "invoice_url", "invoice_pdf"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("凭码查账单不应带 %q: %s", secret, w.Body.String())
		}
	}
	var d struct {
		Record map[string]any `json:"record"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Record["email"] != obMaskedSessEmail {
		t.Fatalf("邮箱应打码: %v", d.Record["email"])
	}
	// 开通时间给；付款金额和币种不给（代理商进价和最终买家付的钱不一样）
	if d.Record["redeemed_at"] != float64(1700000000) || d.Record["amount"] != nil || d.Record["currency"] != nil ||
		strings.Contains(w.Body.String(), "1120") || strings.Contains(w.Body.String(), "PHP") {
		t.Fatalf("开通记录应只给时间、不给金额和币种: %s", w.Body.String())
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("不应用存储的会话调上游: %d 次", n)
	}
}

// 本站码（MW-）按码查结果：邮箱打码、不带账单链接；会话和令牌不出现在响应里。
func TestObResultByCodeMasksEmailAndHasNoInvoiceURL(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, id := e.issueCode("plus", "PH")
	token, pf := e.preflight(code, localSession(cdkSessEmail))
	if w := e.redeem(token, pf); w.Code != http.StatusOK {
		t.Fatalf("兑换: %d %s", w.Code, w.Body.String())
	}
	for _, st := range []string{"queued", "succeeded"} {
		if _, err := db.DB.Exec(`UPDATE pay_orders SET state = ? WHERE source = 'cdk' AND source_ref = ?`, st, db.LocalCDKSourceRef(id)); err != nil {
			t.Fatal(err)
		}
		w := e.do("GET", "/public/cdk/result-by-code?code="+code, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s 应 200: %d %s", st, w.Code, w.Body.String())
		}
		obAssertNoLeak(t, "按码查结果（"+st+"）", w.Body.String())
		for _, secret := range []string{payAccessToken, paySessionToken} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("%s 响应不应带会话内容: %s", st, w.Body.String())
			}
		}
		if !strings.Contains(w.Body.String(), obMaskedSessEmail) {
			t.Fatalf("%s 应给打码邮箱: %s", st, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "invoice_url") {
			t.Fatalf("%s 按码查结果不应带账单链接: %s", st, w.Body.String())
		}
	}
}

// 本站码按令牌查结果：邮箱打码，其余字段不变。
func TestObResultByTokenMasksEmail(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	token, pf := e.preflight(code, localSession(cdkSessEmail))
	if w := e.redeem(token, pf); w.Code != http.StatusOK {
		t.Fatalf("兑换: %d %s", w.Code, w.Body.String())
	}
	w := e.result(token)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	obAssertNoLeak(t, "按令牌查结果", w.Body.String())
	var d struct {
		Status string         `json:"status"`
		Order  map[string]any `json:"order"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Order["account_email"] != obMaskedSessEmail || d.Status != "queued" {
		t.Fatalf("应打码且其余字段不变: %s", w.Body.String())
	}
}
