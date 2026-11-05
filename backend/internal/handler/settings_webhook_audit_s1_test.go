package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func s1Put(body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/settings", AdminPutSettings)
	req := httptest.NewRequest("PUT", "/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// A-6/C-2：保存后库里是密文，读回是原值；没主密钥报错。
func TestS1SettingsSecretsEncrypted(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	w := s1Put(`{"card_api_key":"sk_test_abcdef","webhook_secret":"whsec_xyz"}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	for k, want := range map[string]string{"card_api_key": "sk_test_abcdef", "webhook_secret": "whsec_xyz"} {
		raw, _ := db.GetSetting(k)
		if !strings.HasPrefix(raw, "v1:") {
			t.Fatalf("%s 应是密文: %q", k, raw)
		}
		if got, err := db.GetCardSecret(k); err != nil || got != want {
			t.Fatalf("%s 读回 %q %v", k, got, err)
		}
	}
	if strings.Contains(w.Body.String(), "sk_test_abcdef") || !strings.Contains(w.Body.String(), "cdef") {
		t.Fatalf("响应只应带脱敏提示: %s", w.Body.String())
	}
}

func TestS1SettingsSecretsNeedMasterKey(t *testing.T) {
	dbtest.Open(t)
	t.Setenv("APP_MASTER_KEY", "")
	w := s1Put(`{"brand_name":"x","card_api_key":"sk_test_abcdef"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("应 400: %d %s", w.Code, w.Body.String())
	}
	if v, _ := db.GetSetting("card_api_key"); v != "" {
		t.Fatalf("不能存明文: %q", v)
	}
	if v, _ := db.GetSetting("brand_name"); v != "" {
		t.Fatalf("失败时其他项也不能写一半: %q", v)
	}
}

// C-1：地址校验与改地址必须重填 Key。
func TestS1SettingsCardAPIBase(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	for _, base := range []string{"http://169.254.169.254", "http://zovocard.com", "https://u:p@evil.com"} {
		w := s1Put(`{"card_api_base":"` + base + `","card_api_key":"sk_new_key_1"}`)
		if w.Code != 400 {
			t.Fatalf("%s 应 400: %d", base, w.Code)
		}
	}
	if w := s1Put(`{"card_api_base":"https://card.example.com","card_api_key":"sk_first_1"}`); w.Code != 200 {
		t.Fatalf("首次设置带 Key: %d %s", w.Code, w.Body.String())
	}
	// 改地址不带 Key
	if w := s1Put(`{"card_api_base":"https://other.example.com"}`); w.Code != 400 {
		t.Fatalf("改地址不重填 Key 应 400: %d", w.Code)
	}
	if v, _ := db.GetSetting("card_api_base"); v != "https://card.example.com" {
		t.Fatalf("被拒后地址不能变: %q", v)
	}
	// 同地址不带 Key 可以
	if w := s1Put(`{"card_api_base":"https://card.example.com/"}`); w.Code != 200 {
		t.Fatalf("地址没变应放行: %d %s", w.Code, w.Body.String())
	}
	// 改地址并带新 Key
	if w := s1Put(`{"card_api_base":"https://other.example.com","card_api_key":"sk_second_2"}`); w.Code != 200 {
		t.Fatalf("改地址带新 Key: %d %s", w.Code, w.Body.String())
	}
	if got, _ := db.GetCardSecret("card_api_key"); got != "sk_second_2" {
		t.Fatalf("key=%q", got)
	}
	// 本机回环放行
	if w := s1Put(`{"card_api_base":"http://127.0.0.1:9000","card_api_key":"sk_third_3"}`); w.Code != 200 {
		t.Fatalf("回环地址: %d", w.Code)
	}
}

// A-15/B-3/C-9：按字符数判断，超长 400，不截断。
func TestS1SettingsBrandLength(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	name14 := strings.Repeat("牛", 14) // 42 字节，14 字符，应通过
	if w := s1Put(`{"brand_name":"` + name14 + `"}`); w.Code != 200 {
		t.Fatalf("14 字应通过: %d %s", w.Code, w.Body.String())
	}
	if v, _ := db.GetSetting("brand_name"); v != name14 || !utf8.ValidString(v) {
		t.Fatalf("存的值被截坏: %q", v)
	}
	if w := s1Put(`{"brand_name":"` + strings.Repeat("牛", 41) + `"}`); w.Code != 400 {
		t.Fatalf("41 字应 400: %d", w.Code)
	}
	if w := s1Put(`{"brand_sub":"` + strings.Repeat("a", 81) + `"}`); w.Code != 400 {
		t.Fatalf("副标题 81 应 400: %d", w.Code)
	}
	if v, _ := db.GetSetting("brand_name"); v != name14 {
		t.Fatalf("被拒后原值应保留: %q", v)
	}
}

// C-9：写库失败要返回错误。
func TestS1SettingsWriteFailureReturns500(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if _, err := db.DB.Exec(`DROP TABLE site_settings`); err != nil {
		t.Fatal(err)
	}
	if w := s1Put(`{"brand_name":"x"}`); w.Code != 500 {
		t.Fatalf("写库失败应 500: %d %s", w.Code, w.Body.String())
	}
}

func s1Webhook(t *testing.T, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	return s1WebhookSig(t, secret, secret, body)
}

func TestS1WebhookEncryptedSecretAndMasking(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	t.Cleanup(webhookObservers.Wait)
	if err := db.SetSecretSetting("webhook_secret", "whsec_s1"); err != nil {
		t.Fatal(err)
	}
	w := s1Webhook(t, "whsec_s1", `{"event":"card_transaction","auth_id":"a1","type":"auth","status":"ok","card_number":"5378727109708264","cvv":"123","expire":"08/29"}`)
	if w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	rows, _ := db.ListWebhookEvents(5)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	p := rows[0].Payload
	if strings.Contains(p, "5378727109708264") || strings.Contains(p, "123") || strings.Contains(p, "08/29") || !strings.Contains(p, "537872******8264") {
		t.Fatalf("入库原文未脱敏: %s", p)
	}
}

// C-7：同一 event_id 重发只入一条；不同 event_id 各一条。
func TestS1WebhookIdemUsesEventID(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	t.Cleanup(webhookObservers.Wait)
	_ = db.SetSecretSetting("webhook_secret", "whsec_s1")
	for i := 0; i < 3; i++ {
		if w := s1Webhook(t, "whsec_s1", `{"event":"gpt_direct.failed","event_id":"ev-1","cdk_id":0}`); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	s1Webhook(t, "whsec_s1", `{"event":"gpt_direct.failed","event_id":"ev-2","cdk_id":0}`)
	// 没有 event_id：完全相同的原文重发也要去重，不同原文不去重
	for i := 0; i < 2; i++ {
		s1Webhook(t, "whsec_s1", `{"event":"gpt_direct.failed","cdk_id":0,"order_id":9}`)
	}
	s1Webhook(t, "whsec_s1", `{"event":"gpt_direct.failed","cdk_id":0,"order_id":10}`)
	rows, _ := db.ListWebhookEvents(20)
	if len(rows) != 4 {
		t.Fatalf("应 4 条（ev-1、ev-2、order9、order10），实际 %d", len(rows))
	}
}

// C-13：入库失败（非唯一冲突）回 500，让卡台重试。
func TestS1WebhookStoreFailureReturns500(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	t.Cleanup(webhookObservers.Wait)
	_ = db.SetSecretSetting("webhook_secret", "whsec_s1")
	if _, err := db.DB.Exec(`DROP TABLE webhook_events`); err != nil {
		t.Fatal(err)
	}
	if w := s1Webhook(t, "whsec_s1", `{"event":"gpt_direct.failed","event_id":"e"}`); w.Code != 500 {
		t.Fatalf("应 500: %d", w.Code)
	}
}

func s1WebhookSig(t *testing.T, secret, _ string, body string) *httptest.ResponseRecorder {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/w", CardPlatformWebhook)
	req := httptest.NewRequest("POST", "/w", strings.NewReader(body))
	req.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
