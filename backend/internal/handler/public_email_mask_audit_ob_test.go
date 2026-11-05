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
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
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

func TestObResultByCodeMasksEmailAndHasNoInvoiceURL(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	u := newCDKUpstream(t)
	hub := obStubAccounthub(t)
	u.mu.Lock()
	u.resultEmail = cdkSessEmail
	u.mu.Unlock()
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"running", "completed"} {
		u.setResult(st)
		w := cdkRequest(PublicCDKResultByCode, "GET", "/r?code="+cdkSessCode, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s 应 200: %d %s", st, w.Code, w.Body.String())
		}
		obAssertNoLeak(t, "按码查结果（"+st+"）", w.Body.String())
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

func TestObResultByTokenMasksEmail(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	u := newCDKUpstream(t)
	u.mu.Lock()
	u.resultEmail = cdkSessEmail
	u.mu.Unlock()
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	w := cdkRequest(PublicCDKResult, "GET", "/r?token=rt-1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	obAssertNoLeak(t, "按令牌查结果", w.Body.String())
	var d map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d["account_email"] != obMaskedSessEmail || d["status"] != "running" {
		t.Fatalf("应打码且其余字段不变: %s", w.Body.String())
	}
}
