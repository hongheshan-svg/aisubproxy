package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// F-1：凭卡密就能调的公开接口（凭码查账单、按码 / 按令牌查兑换结果）不能给出完整邮箱和账单链接，
// 和售后查询的打码规则一致。知道卡密不等于本人：卖码的代理、看过截图的人都可能知道。

const obMaskedSessEmail = "bu***@example.com" // maskEmailForAdmin(cdkSessEmail)

// obStubAccounthub 起一个假 accounthub，账单里带链接、PDF 和客户邮箱。返回调用次数。
func obStubAccounthub(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"invoice_url":"https://invoice.example/hosted-1","invoices":[{
			"id":"in_1","number":"AB-0001","total":2000,"currency":"usd","paid":true,"status":"paid","created":1700000000,
			"hosted_invoice_url":"https://invoice.example/hosted-1","invoice_pdf":"https://invoice.example/pdf-1",
			"customer_email":"` + cdkSessEmail + `","customer_name":"Real Buyer Name","description":"x"}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ACCOUNTHUB_BASE_URL", srv.URL)
	return &n
}

func obAssertNoLeak(t *testing.T, what, body string) {
	t.Helper()
	for _, s := range []string{cdkSessEmail, "invoice.example", "Real Buyer Name", cdkSessToken} {
		if strings.Contains(body, s) {
			t.Fatalf("%s 的响应不应包含 %q: %s", what, s, body)
		}
	}
}

func TestObBillingCheckByCDKMasksEmailAndHidesInvoiceLinks(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	stubBillingCheck(t)
	obStubAccounthub(t)
	if err := bindCDKSession("testup", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	w := cdkRequest(SessionBillingCheck, "POST", "/billing", `{"cdk_code":"`+cdkSessCode+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	obAssertNoLeak(t, "凭码查账单", w.Body.String())
	var d struct {
		Summary  map[string]any   `json:"summary"`
		Invoices []map[string]any `json:"invoices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Summary["email"] != obMaskedSessEmail {
		t.Fatalf("邮箱应打码: %v", d.Summary["email"])
	}
	// 金额和时间这类摘要仍然给
	if len(d.Invoices) != 1 || d.Invoices[0]["total"] != float64(2000) || d.Invoices[0]["currency"] != "usd" || d.Invoices[0]["created"] != float64(1700000000) {
		t.Fatalf("账单摘要应保留金额、币种、时间: %s", w.Body.String())
	}
}

// 本站码（MW-）按码查结果：邮箱打码、不带账单链接，也不去取账单；会话和令牌不出现在响应里。
func TestObResultByCodeMasksEmailAndHasNoInvoiceURL(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	hub := obStubAccounthub(t)
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
	if n := hub.Load(); n != 0 {
		t.Fatalf("按码查结果不应再去 accounthub 取账单: %d 次", n)
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
