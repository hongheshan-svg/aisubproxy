package handler

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// E-2/F-6：换码限流表不能随不同来源 IP 无限增长。
func TestSwapRateTableBounded(t *testing.T) {
	resetLocalRate()
	t.Cleanup(resetLocalRate)
	old := localRateMaxKeys
	localRateMaxKeys = 50
	t.Cleanup(func() { localRateMaxKeys = old })
	for i := 0; i < 500; i++ {
		swapRateAllow(fmt.Sprintf("198.51.100.%d", i%250) + fmt.Sprintf("-%d", i))
	}
	if n := localRateKeys(); n > 50 {
		t.Fatalf("限流表键数 %d 超过上限 50", n)
	}
}

// 换码限流仍是每 IP 每小时 20 次。
func TestSwapRateStillLimitsPerIP(t *testing.T) {
	resetLocalRate()
	t.Cleanup(resetLocalRate)
	for i := 0; i < 20; i++ {
		if !swapRateAllow("203.0.113.5") {
			t.Fatalf("第 %d 次不该被拒", i+1)
		}
	}
	if swapRateAllow("203.0.113.5") {
		t.Fatal("第 21 次应被拒")
	}
	if !swapRateAllow("203.0.113.6") {
		t.Fatal("别的 IP 不受影响")
	}
}

// F-7：同一个 IPv6 /64 内的地址共用一个计数。
func TestRateKeyIPv6Collapsed64(t *testing.T) {
	resetLocalRate()
	t.Cleanup(resetLocalRate)
	for i := 0; i < 30; i++ {
		localRateAllow(localRateMutate, fmt.Sprintf("2001:db8:1:2::%x", i+1))
	}
	if n := localRateKeys(); n != 1 {
		t.Fatalf("同一 /64 应只占 1 个键，实际 %d", n)
	}
	localRateAllow(localRateMutate, "2001:db8:1:3::1")
	if n := localRateKeys(); n != 2 {
		t.Fatalf("不同 /64 应分开计数，实际 %d", n)
	}
	// IPv4 不受影响
	localRateAllow(localRateMutate, "203.0.113.1")
	localRateAllow(localRateMutate, "203.0.113.2")
	if n := localRateKeys(); n != 4 {
		t.Fatalf("IPv4 应各占一个键，实际 %d", n)
	}
	// 换码限流同样按 /64 聚合
	for i := 0; i < 20; i++ {
		swapRateAllow(fmt.Sprintf("2001:db8:9:9::%x", i+1))
	}
	if swapRateAllow("2001:db8:9:9::ffff") {
		t.Fatal("同 /64 的第 21 次换码尝试应被拒")
	}
}

// E-7：新口令至少 12 位。
func TestAgentSwapPasswordMinLength(t *testing.T) {
	dbtest.Open(t)
	if err := setAgentSwapPassword("elevenchars"); err == nil {
		t.Fatal("11 位口令应被拒")
	}
	if err := setAgentSwapPassword("twelve-chars"); err != nil {
		t.Fatalf("12 位口令应通过: %v", err)
	}
}

// E-7：已有的短口令继续可用，但标记为需要更换；换成长口令后标记清掉；口令错误按天计数。
func TestAgentSwapShortPasswordFlagAndWrongCount(t *testing.T) {
	dbtest.Open(t)
	hash, err := db.HashAdminPassword("short1")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(agentSwapPasswordKey, hash); err != nil {
		t.Fatal(err)
	}
	if agentSwapPasswordWeak() {
		t.Fatal("还没用过，不应有标记")
	}
	noteAgentSwapPasswordUsed("short1")
	if !agentSwapPasswordWeak() {
		t.Fatal("短口令验证通过后应标记需要更换")
	}
	if err := setAgentSwapPassword("a-long-password-1"); err != nil {
		t.Fatal(err)
	}
	if agentSwapPasswordWeak() {
		t.Fatal("换成长口令后标记应清掉")
	}
	before := agentSwapWrongToday()
	agentSwapWrongPassword("203.0.113.1")
	agentSwapWrongPassword("203.0.113.1")
	if got := agentSwapWrongToday(); got != before+2 {
		t.Fatalf("口令错误计数 %d -> %d，应 +2", before, got)
	}
}

// E-4：后台卡台码列表只按上游 id 取完整码，前缀相同的另一张码不能被填进来。
func TestCDKListNoPrefixFallbackForFullCode(t *testing.T) {
	dbtest.Open(t)
	// 本站只有 id=1 的码；上游列表里 id=2 的码前缀和它相同，但本站没有 id=2 的记录
	if err := db.SaveCardplatformCDKCode(1, "ZC-SAMEPREFIX-AAAA-1111", "ZC-SAMEPREFIX", "plus", 0, "US"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"data":{"total":2,"list":[` +
			`{"id":1,"plan":"plus","code_prefix":"ZC-SAMEPREFIX","payment_country":"US","status":"unused"},` +
			`{"id":2,"plan":"plus","code_prefix":"ZC-SAMEPREFIX","payment_country":"JP","status":"unused"}]}}`))
	}))
	t.Cleanup(srv.Close)
	if err := db.SetSetting("card_api_base", srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("card_api_key", "fixture-key"); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/l", CardPlatformListCDKs)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/l", nil))
	var body struct {
		List []struct {
			ID          int64  `json:"id"`
			FullCode    string `json:"full_code"`
			HasFullCode bool   `json:"has_full_code"`
		} `json:"list"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.List) != 2 {
		t.Fatalf("响应不对: %v %s", err, w.Body.String())
	}
	if !body.List[0].HasFullCode || body.List[0].FullCode != "ZC-SAMEPREFIX-AAAA-1111" {
		t.Fatalf("id=1 应取到自己的码: %+v", body.List[0])
	}
	if body.List[1].HasFullCode || body.List[1].FullCode != "" {
		t.Fatalf("id=2 不应按前缀拿到别人的码: %+v", body.List[1])
	}
	if got, _ := db.LookupStoredCDKRegion(2); got == "JP" {
		t.Fatal("不应给 id=2 写入地区")
	}
}
