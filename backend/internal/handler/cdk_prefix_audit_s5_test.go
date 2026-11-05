package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

const s5OtherCode = "ZC-SESSION-TEST-CODE-0002" // 与 cdkSessCode 前 14 位相同

// R13：webhook 带的 cdk_id 在本站查不到时，不能按前缀把别人的码当成这张码去清会话。
func TestS5WebhookCompletedDoesNotClearByPrefix(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	t.Cleanup(webhookObservers.Wait)
	if err := db.SetSetting("webhook_secret", "whsec"); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCardplatformCDKCode(77, cdkSessCode, cdkSessCode[:14], "plus", 0, "PH"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindCDKSession("zovocard", cdkSessCode, "rt-1", cdkSessJSON()); err != nil {
		t.Fatal(err)
	}
	body := `{"event":"gpt_direct.completed","cdk_id":999,"code_prefix":"` + cdkSessCode[:14] + `","order_id":5}`
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
	if s := storedCDKSession(t, cdkSessCode); s == "" {
		t.Fatal("cdk_id 对不上时不应按前缀清掉别的码的会话")
	}
}

// R13：卡健康观察取完整码只按上游 ID；ID 对不上就当取不到，前缀相同也不行。
func TestS5ExactCDKCodeIgnoresPrefix(t *testing.T) {
	dbtest.Open(t)
	if err := db.SaveCardplatformCDKCode(77, s5OtherCode, s5OtherCode[:14], "plus", 0, "PH"); err != nil {
		t.Fatal(err)
	}
	if got := exactCDKCodeByID(77); got != s5OtherCode {
		t.Fatalf("按 ID 应取到: %q", got)
	}
	if got := exactCDKCodeByID(999); got != "" {
		t.Fatalf("ID 对不上应取不到，实际: %q", got)
	}
	if got := exactCDKCodeByID(0); got != "" {
		t.Fatalf("ID 为 0 应取不到，实际: %q", got)
	}
}
