package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// N-1：地址和 Key 在同一个事务里写；后面的项写失败时，地址和 Key 都保持原样。
func TestS6SettingsBaseAndKeyWrittenAtomically(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if w := s1Put(`{"card_api_base":"https://old.example.com","card_api_key":"sk_old_key_1"}`); w.Code != 200 {
		t.Fatalf("首次保存: %d %s", w.Code, w.Body.String())
	}
	// 让 webhook_secret 这一项写库失败
	if _, err := db.DB.Exec(`CREATE TRIGGER s6_fail BEFORE UPDATE ON site_settings WHEN NEW.key='webhook_secret' BEGIN SELECT RAISE(ABORT,'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`CREATE TRIGGER s6_fail2 BEFORE INSERT ON site_settings WHEN NEW.key='webhook_secret' BEGIN SELECT RAISE(ABORT,'boom'); END`); err != nil {
		t.Fatal(err)
	}
	w := s1Put(`{"card_api_base":"https://new.example.com","card_api_key":"sk_new_key_2","webhook_secret":"whsec_new"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("应 500: %d %s", w.Code, w.Body.String())
	}
	if b, _ := db.GetSetting("card_api_base"); !strings.Contains(b, "old.example.com") {
		t.Fatalf("地址不应被改: %q", b)
	}
	if k, _ := db.GetCardSecret("card_api_key"); k != "sk_old_key_1" {
		t.Fatalf("Key 不应被改: %q", k)
	}
}

// N-2：同一个 event_id 重发，不再重跑状态回写。
func TestS6DuplicateWebhookDoesNotRewriteStatus(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	t.Cleanup(webhookObservers.Wait)
	_ = db.SetSecretSetting("webhook_secret", "whsec_s6")
	if _, err := db.DB.Exec(`INSERT INTO cardplatform_cdk_codes (upstream_id, code, code_prefix, plan, status) VALUES (77, 'CODE-S6-77', 'CODE-S6', 'plus', 'reserved')`); err != nil {
		t.Fatal(err)
	}
	body := `{"event":"gpt_direct.failed","event_id":"ev-s6","cdk_id":77,"cdk_status":"unused"}`
	if w := s1Webhook(t, "whsec_s6", body); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if st := db.GetCardplatformCDKStatus(77); st != "unused" {
		t.Fatalf("首次推送应回写 unused: %q", st)
	}
	// 之后这张码被重新预留，卡台重发同一事件
	if err := db.UpdateCardplatformCDKStatus(77, "reserved"); err != nil {
		t.Fatal(err)
	}
	if w := s1Webhook(t, "whsec_s6", body); w.Code != 200 {
		t.Fatalf("重复事件仍应回 200: %d", w.Code)
	}
	if st := db.GetCardplatformCDKStatus(77); st != "reserved" {
		t.Fatalf("重复事件不应把状态改回去: %q", st)
	}
}

// N-5：订单列表和单笔订单透传前不带完整卡号。
func TestS6OrderListNeverReturnsFullCardNumber(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/", nil)
	raw := json.RawMessage(`{"list":[{"order_id":1,"cdk_id":0,"code_prefix":"X","cdk_status":"unused","card_number":"5378727109708264","pan":"5378727109708264"}],"total":1}`)
	out := enrichCDKOrderList(c, nil, raw)
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "5378727109708264") || strings.Contains(string(b), "card_number") {
		t.Fatalf("列表含完整卡号: %s", b)
	}
	if !strings.Contains(string(b), `"card_last_four":"8264"`) {
		t.Fatalf("应补 card_last_four: %s", b)
	}
	m := map[string]any{"order_id": 1, "card_number": "5378727109708264", "card_last_four": "9999"}
	enrichOneCDKOrder(c, nil, m)
	if _, ok := m["card_number"]; ok || m["card_last_four"] != "9999" {
		t.Fatalf("单笔订单处理不对: %v", m)
	}
}
