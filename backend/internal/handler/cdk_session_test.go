package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/gptcheck"
)

const (
	cdkSessCode  = "ZC-SESSION-TEST-CODE-0001"
	cdkSessToken = "CDK-SESSION-SECRET-TOKEN-777"
	cdkSessEmail = "buyer@example.com"
)

func cdkSessJSON() string {
	b, _ := json.Marshal(map[string]any{"sessionToken": cdkSessToken, "user": map[string]string{"email": cdkSessEmail}})
	return string(b)
}

// cdkUpstream 是假卡台：记录预检收到的请求体，结果接口返回 resultStatus。
type cdkUpstream struct {
	srv          *httptest.Server
	mu           sync.Mutex
	resultStatus string
	resultEmail  string // 非空时结果里带 account_email
	resultCode   int    // 非 0 时结果接口回这个 HTTP 状态码
	preflights   []string
	results      int
}

func (u *cdkUpstream) setResult(status string) {
	u.mu.Lock()
	u.resultStatus = status
	u.mu.Unlock()
}

func newCDKUpstream(t *testing.T) *cdkUpstream {
	t.Helper()
	u := &cdkUpstream{resultStatus: "running"}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/cdk/preflight":
			b, _ := io.ReadAll(r.Body)
			u.mu.Lock()
			u.preflights = append(u.preflights, string(b))
			u.mu.Unlock()
			w.Write([]byte(`{"ok":true,"preflight_token":"pf-1"}`))
		case "/api/v1/cdk/result":
			u.mu.Lock()
			st, em, code := u.resultStatus, u.resultEmail, u.resultCode
			u.results++
			u.mu.Unlock()
			if code != 0 {
				w.WriteHeader(code)
			}
			if em != "" {
				w.Write([]byte(`{"status":"` + st + `","account_email":"` + em + `","order":{"status":"` + st + `"}}`))
				return
			}
			w.Write([]byte(`{"status":"` + st + `","order":{"status":"` + st + `"}}`))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"code":404}`))
		}
	}))
	t.Cleanup(u.srv.Close)
	if err := db.SetSetting("card_api_base", u.srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("card_api_key", "fixture-key"); err != nil {
		t.Fatal(err)
	}
	return u
}

func cdkRequest(h gin.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Handle(method, strings.SplitN(path, "?", 2)[0], h)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func storedCDKSession(t *testing.T, code string) string {
	t.Helper()
	var s string
	if err := db.DB.QueryRow(`SELECT session_payload FROM cdk_session_bindings WHERE cdk_code = ?`, code).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// stubBillingCheck 换掉「用会话调上游」的函数并计数；测试绝不碰真实的 chatgpt.com。
func stubBillingCheck(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := billingSessionCheck
	billingSessionCheck = func(string) (*gptcheck.Result, error) {
		n.Add(1)
		return &gptcheck.Result{Summary: map[string]interface{}{"plan": "plus"}}, nil
	}
	t.Cleanup(func() { billingSessionCheck = old })
	return &n
}

// stubAccounthub 起一个假 accounthub。status 非 200 时返回错误。
func stubAccounthub(t *testing.T, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status == 200 {
			w.Write([]byte(`{"invoice_url":"https://invoice.example/1","invoices":[{"hosted_invoice_url":"https://invoice.example/1"}]}`))
			return
		}
		w.Write([]byte(`{"error":"boom"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ACCOUNTHUB_BASE_URL", srv.URL)
}

// captureLog 把这段时间的标准日志收集起来。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func preflightBody() string {
	b, _ := json.Marshal(map[string]any{
		"code": cdkSessCode, "redemption_token": "rt-1",
		"credential": map[string]any{"session": cdkSessJSON()},
	})
	return string(b)
}

// 预检写库是密文，但发给卡台的请求体不受影响（兑换靠的是这一份）。
func TestPreflightStoresCiphertextAndForwardsSession(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	u := newCDKUpstream(t)
	w := cdkRequest(PublicCDKPreflight, "POST", "/preflight", preflightBody())
	if w.Code != 200 || !strings.Contains(w.Body.String(), "pf-1") {
		t.Fatalf("应透传卡台响应: %d %s", w.Code, w.Body.String())
	}
	if s := storedCDKSession(t, cdkSessCode); strings.Contains(s, cdkSessToken) || !strings.HasPrefix(s, "v1:") {
		t.Fatalf("库里应是密文: %q", s)
	}
	if len(u.preflights) != 1 || !strings.Contains(u.preflights[0], cdkSessToken) {
		t.Fatalf("卡台应收到用户会话: %v", u.preflights)
	}
}

// 主密钥缺失：拒绝落库，但用户的兑换不受影响；日志里没有会话，也没有完整卡密。
func TestPreflightWithoutKeyDoesNotFailRedemption(t *testing.T) {
	dbtest.Open(t)
	t.Setenv("APP_MASTER_KEY", "")
	buf := captureLog(t)
	alerts := captureOpsAlerts(t)
	u := newCDKUpstream(t)
	if err := db.BindCDKRedemptionToken("zovocard", cdkSessCode, "rt-1"); err != nil {
		t.Fatal(err)
	}
	w := cdkRequest(PublicCDKPreflight, "POST", "/preflight", preflightBody())
	if w.Code != 200 || !strings.Contains(w.Body.String(), "pf-1") {
		t.Fatalf("缺密钥不应影响兑换: %d %s", w.Code, w.Body.String())
	}
	if len(u.preflights) != 1 {
		t.Fatalf("预检应照常发给卡台: %v", u.preflights)
	}
	if s := storedCDKSession(t, cdkSessCode); s != "" {
		t.Fatalf("缺密钥时不应落库: %q", s)
	}
	if b, err := db.GetBindingByCDK(cdkSessCode); err != nil || b == nil || b.RedemptionToken != "rt-1" {
		t.Fatalf("token 应保留: %+v %v", b, err)
	}
	if got := buf.String(); strings.Contains(got, cdkSessToken) || strings.Contains(got, cdkSessCode) {
		t.Fatalf("日志不应含会话或完整卡密: %s", got)
	}
	if n := alerts.count(); n != 1 {
		t.Fatalf("应告警一次: %d", n)
	}
}

// 凭卡密查账单绝不回退到用存储的会话调上游。
func TestBillingCheckByCDKNeverCallsUpstreamWithStoredSession(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	calls := stubBillingCheck(t)

	t.Run("accounthub 不可用", func(t *testing.T) {
		stubAccounthub(t, 500)
		if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
			t.Fatal(err)
		}
		w := cdkRequest(SessionBillingCheck, "POST", "/billing", `{"cdk_code":"`+cdkSessCode+`"}`)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("应 503: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("会话里没有邮箱", func(t *testing.T) {
		if err := db.BindCDKSession("zovocard", "ZC-RAW-TOKEN-CODE-1", "rt-2", "eyJhbGciOi.raw.accesstoken"); err != nil {
			t.Fatal(err)
		}
		w := cdkRequest(SessionBillingCheck, "POST", "/billing", `{"code":"ZC-RAW-TOKEN-CODE-1"}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("应 404: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("没有绑定", func(t *testing.T) {
		w := cdkRequest(SessionBillingCheck, "POST", "/billing", `{"cdk_code":"ZC-NOT-BOUND-CODE-1"}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("应 404: %d %s", w.Code, w.Body.String())
		}
	})
	if n := calls.Load(); n != 0 {
		t.Fatalf("不应用存储的会话调上游: %d 次", n)
	}
}

// 账单查询走邮箱：会话清空之后照常可用，响应里没有会话。
func TestBillingCheckByCDKUsesEmailAndSurvivesClear(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	calls := stubBillingCheck(t)
	stubAccounthub(t, 200)
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	for _, clear := range []bool{false, true} {
		if clear {
			if err := db.ClearCDKSessionByCode(cdkSessCode); err != nil {
				t.Fatal(err)
			}
		}
		w := cdkRequest(SessionBillingCheck, "POST", "/billing", `{"cdk_code":"`+cdkSessCode+`"}`)
		if w.Code != 200 {
			t.Fatalf("clear=%v 应 200: %d %s", clear, w.Code, w.Body.String())
		}
		var d struct {
			Summary    map[string]any   `json:"summary"`
			Invoices   []map[string]any `json:"invoices"`
			InvoiceURL string           `json:"invoice_url"`
			AuthSource string           `json:"auth_source"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		if d.Summary["email"] != cdkSessEmail || d.AuthSource != "accounthub" || len(d.Invoices) != 1 {
			t.Fatalf("clear=%v 响应不符: %s", clear, w.Body.String())
		}
		if strings.Contains(w.Body.String(), cdkSessToken) {
			t.Fatalf("响应不应含会话: %s", w.Body.String())
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("不应用存储的会话调上游: %d 次", n)
	}
}

// 用户自己粘贴会话查询不受影响：这不是存储的会话。
func TestBillingCheckPastedSessionStillWorks(t *testing.T) {
	dbtest.Open(t)
	calls := stubBillingCheck(t)
	w := cdkRequest(SessionBillingCheck, "POST", "/billing", `{"token_input":"pasted-token"}`)
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("粘贴查询应走上游: %d %s calls=%d", w.Code, w.Body.String(), calls.Load())
	}
}

// 清空后，正在兑换的用户刷新进度不受影响：result-by-code 靠 token，不靠会话。
func TestResultByCodeWorksAfterSessionCleared(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	newCDKUpstream(t)
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearCDKSessionByCode(cdkSessCode); err != nil {
		t.Fatal(err)
	}
	w := cdkRequest(PublicCDKResultByCode, "GET", "/r?code="+cdkSessCode, "")
	if w.Code != 200 {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
	var d map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d["redemption_token"] != "rt-1" || d["cdk_code"] != cdkSessCode || d["status"] != "running" {
		t.Fatalf("响应不符: %s", w.Body.String())
	}
	if d["has_session_binding"] != true {
		t.Fatalf("还能凭卡密查账单，has_session_binding 应为 true: %s", w.Body.String())
	}
}

// 密钥缺失时，已加密的行也不能让进度查询报错。
func TestResultByCodeWorksWithoutKey(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	newCDKUpstream(t)
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_MASTER_KEY", "")
	w := cdkRequest(PublicCDKResultByCode, "GET", "/r?code="+cdkSessCode, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "rt-1") {
		t.Fatalf("应 200: %d %s", w.Code, w.Body.String())
	}
}

func TestResultByCodeClearsSessionOnlyWhenCompleted(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	u := newCDKUpstream(t)
	stubAccounthub(t, 200)
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"running", "queued", "failed_precharge", "declined"} {
		u.setResult(st)
		if w := cdkRequest(PublicCDKResultByCode, "GET", "/r?code="+cdkSessCode, ""); w.Code != 200 {
			t.Fatalf("%s 应 200: %d", st, w.Code)
		}
		if s := storedCDKSession(t, cdkSessCode); s == "" {
			t.Fatalf("%s 不算完成，不应清空", st)
		}
	}
	u.setResult("completed")
	w := cdkRequest(PublicCDKResultByCode, "GET", "/r?code="+cdkSessCode, "")
	if w.Code != 200 {
		t.Fatalf("应 200: %d", w.Code)
	}
	if s := storedCDKSession(t, cdkSessCode); s != "" {
		t.Fatalf("完成后应清空: %q", s)
	}
	if !strings.Contains(w.Body.String(), "invoice.example") {
		t.Fatalf("完成时仍应带账单链接: %s", w.Body.String())
	}
}

func TestResultByTokenClearsSessionWhenCompleted(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	u := newCDKUpstream(t)
	if err := db.BindCDKSession("zovocard", "", "rt-only", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	if w := cdkRequest(PublicCDKResult, "GET", "/r?token=rt-only", ""); w.Code != 200 {
		t.Fatalf("应 200: %d", w.Code)
	}
	if s := storedCDKSession(t, "RT:rt-only"); s == "" {
		t.Fatal("进行中不应清空")
	}
	u.setResult("completed")
	if w := cdkRequest(PublicCDKResult, "GET", "/r?token=rt-only", ""); w.Code != 200 {
		t.Fatalf("应 200: %d", w.Code)
	}
	if s := storedCDKSession(t, "RT:rt-only"); s != "" {
		t.Fatalf("完成后应清空: %q", s)
	}
}

func TestWebhookCompletedClearsSession(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	t.Cleanup(webhookObservers.Wait) // 先于关库执行
	if err := db.SetSetting("webhook_secret", "whsec"); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCardplatformCDKCode(77, cdkSessCode, cdkSessCode[:14], "plus", 0, "PH"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	send := func(body string) {
		t.Helper()
		mac := hmac.New(sha256.New, []byte("whsec"))
		mac.Write([]byte(body))
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.POST("/w", CardPlatformWebhook)
		req := httptest.NewRequest("POST", "/w", strings.NewReader(body))
		req.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("webhook 应 200: %d", w.Code)
		}
	}
	send(`{"event":"gpt_direct.failed","cdk_id":77,"order_id":4,"status":"failed_precharge"}`)
	if s := storedCDKSession(t, cdkSessCode); s == "" {
		t.Fatal("失败事件不应清空：用户可能还要重试")
	}
	send(`{"event":"gpt_direct.completed","cdk_id":77,"order_id":5}`)
	if s := storedCDKSession(t, cdkSessCode); s != "" {
		t.Fatalf("完成事件应清空: %q", s)
	}
}

// 卡状态查询：邮箱有无会影响「已用/未用」的判断，清空会话后不能把已用的码显示成未用。
func TestLookupKeepsEmailAndStatusAfterClear(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	newCDKUpstream(t)
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	before := lookupOneCDK(context.Background(), cdkSessCode, "")
	if err := db.ClearCDKSessionByCode(cdkSessCode); err != nil {
		t.Fatal(err)
	}
	after := lookupOneCDK(context.Background(), cdkSessCode, "")
	// 售后查询公共层一律给打码后的邮箱（有意的行为变化：以前这里给完整邮箱）
	if after.AccountEmail != "bu***@example.com" {
		t.Fatalf("清空后邮箱应还在（打码）: %+v", after)
	}
	if before.Status != after.Status || before.Used != after.Used || before.CanResubmit != after.CanResubmit {
		t.Fatalf("清空前后状态不应变化: %+v -> %+v", before, after)
	}
}

// 卡健康按邮箱去重：清空会话后如果退回脱敏邮箱，同一账号会被算成两个邮箱，把好卡误判成坏卡。
func TestCardHealthEmailSurvivesClear(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearCDKSessionByCode(cdkSessCode); err != nil {
		t.Fatal(err)
	}
	email, src := resolveEmailForOrder(cdkSessCode, "bu***@example.com")
	if email != cdkSessEmail || src != "session" {
		t.Fatalf("应取到完整邮箱: %q %q", email, src)
	}
}

func TestOpsMaintenanceClearsAndAlertsOncePerDay(t *testing.T) {
	dbtest.Open(t)
	t.Setenv("APP_MASTER_KEY", "")
	alerts := captureOpsAlerts(t)
	if _, err := db.DB.Exec(`INSERT INTO cdk_session_bindings (cdk_code, session_payload, session_bound_at) VALUES ('ZC-P', ?, 1)`, cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 3, 4, 0, 0, 0, time.UTC)
	runOpsMaintenance(now)
	runOpsMaintenance(now.Add(10 * time.Minute))
	if n := alerts.count(); n != 1 {
		t.Fatalf("同一天应只告警一次: %d", n)
	}
	if got := alerts.texts(); strings.Contains(got[0], cdkSessToken) || !strings.Contains(got[0], "APP_MASTER_KEY") {
		t.Fatalf("告警文本不符: %v", got)
	}
	if s := storedCDKSession(t, "ZC-P"); !strings.Contains(s, cdkSessToken) {
		t.Fatal("缺密钥时不应动明文数据")
	}
	runOpsMaintenance(now.Add(24 * time.Hour))
	if n := alerts.count(); n != 2 {
		t.Fatalf("第二天应再提醒: %d", n)
	}
	// 配上密钥后到期的行被清掉，邮箱加密保全
	dbtest.SetMasterKey(t)
	runOpsMaintenance(now.Add(48 * time.Hour))
	if s := storedCDKSession(t, "ZC-P"); s != "" {
		t.Fatalf("应清空: %q", s)
	}
	if em, err := db.GetAccountEmailByCDK("ZC-P"); err != nil || em != cdkSessEmail {
		t.Fatalf("邮箱应保全: %q %v", em, err)
	}
}
