package handler

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/gptcheck"
)

const (
	cdkSessCode  = "TU-SESSION-TEST-CODE-0001"
	cdkSessToken = "CDK-SESSION-SECRET-TOKEN-777"
	cdkSessEmail = "buyer@example.com"
)

func cdkSessJSON() string {
	b, _ := json.Marshal(map[string]any{"sessionToken": cdkSessToken, "user": map[string]string{"email": cdkSessEmail}})
	return string(b)
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

// captureLog 把这段时间的标准日志收集起来。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// 旧绑定表里留着会话和邮箱的码（已移除的 ZovoCard 留下的）：凭卡密查账单认不出，回 404，
// 不读绑定表里的会话和邮箱，也绝不拿存储的会话调上游（审计 F-1）。会话清空前后都一样。
func TestBillingCheckByCDKIgnoresLegacyBindings(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	calls := stubBillingCheck(t)
	if err := bindCDKSession("testup", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	for _, clear := range []bool{false, true} {
		if clear {
			if err := clearCDKSessionByCode(cdkSessCode); err != nil {
				t.Fatal(err)
			}
		}
		for _, body := range []string{`{"cdk_code":"` + cdkSessCode + `"}`, `{"code":"` + cdkSessCode + `"}`} {
			w := cdkRequest(SessionBillingCheck, "POST", "/billing", body)
			if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"cdk_not_found"`) {
				t.Fatalf("clear=%v 应 404 cdk_not_found: %d %s", clear, w.Code, w.Body.String())
			}
			for _, bad := range []string{cdkSessToken, cdkSessEmail, maskEmailForAdmin(cdkSessEmail)} {
				if strings.Contains(w.Body.String(), bad) {
					t.Fatalf("响应不应含 %q: %s", bad, w.Body.String())
				}
			}
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

func TestOpsMaintenanceClearsAndAlertsOncePerDay(t *testing.T) {
	dbtest.Open(t)
	t.Setenv("APP_MASTER_KEY", "")
	alerts := captureOpsAlerts(t)
	if _, err := db.DB.Exec(`INSERT INTO cdk_session_bindings (cdk_code, session_payload, session_bound_at) VALUES ('TU-P', ?, 1)`, cdkSessJSON()); err != nil {
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
	if s := storedCDKSession(t, "TU-P"); !strings.Contains(s, cdkSessToken) {
		t.Fatal("缺密钥时不应动明文数据")
	}
	runOpsMaintenance(now.Add(24 * time.Hour))
	if n := alerts.count(); n != 2 {
		t.Fatalf("第二天应再提醒: %d", n)
	}
	// 配上密钥后到期的行被清掉，邮箱加密保全
	dbtest.SetMasterKey(t)
	runOpsMaintenance(now.Add(48 * time.Hour))
	if s := storedCDKSession(t, "TU-P"); s != "" {
		t.Fatalf("应清空: %q", s)
	}
	if em, err := db.GetAccountEmailByCDK("TU-P"); err != nil || em != cdkSessEmail {
		t.Fatalf("邮箱应保全: %q %v", em, err)
	}
}
