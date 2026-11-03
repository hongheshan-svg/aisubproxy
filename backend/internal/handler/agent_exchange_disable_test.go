package handler

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func countContaining(a *opsAlertLog, sub string) int {
	n := 0
	for _, t := range a.texts() {
		if strings.Contains(t, sub) {
			n++
		}
	}
	return n
}

// MO-08：换码后禁用旧码失败，要立刻告警、写审计、记录停在 disabling（不是 done），并由维护循环重试，成功后才转 done。
func TestAgentExchangeDisableFailureAlertsAndRetries(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	alerts := captureOpsAlerts(t)
	u := newExchangeUpstream(t)
	u.disableFail.Store(true)

	w := doExchange(exchangeOldCode, "203.0.113.9")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ZC-EXCHANGE-NEW-CODE-0001") {
		t.Fatalf("新码已发出，仍应交给代理: %d %s", w.Code, w.Body.String())
	}
	st, newID, _ := exchangeRow(t, exchangeOldCode)
	if st != db.AgentExchangeDisabling || newID != 901 {
		t.Fatalf("旧码没禁用成功，记录不能是完成: st=%s new=%d", st, newID)
	}
	if n := countContaining(alerts, "禁用旧码失败"); n != 1 {
		t.Fatalf("应立刻告警 1 次: %v", alerts.texts())
	}
	if a := alerts.texts()[0]; !strings.Contains(a, "旧码 ID 77") || !strings.Contains(a, "新码 ID 901") {
		t.Fatalf("告警应含旧码和新码 ID: %s", a)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs WHERE action = 'agent_cdk_exchange_disable_failed'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("应写审计 agent_cdk_exchange_disable_failed: n=%d err=%v", n, err)
	}
	if db.GetCardplatformCDKStatus(77) == "disabled" {
		t.Fatal("没禁用成功，本地状态不能标成 disabled")
	}

	// 卡台仍然故障：维护循环再试一次，仍停在 disabling，同一天不重复告警
	now := time.Now()
	before := countContaining(alerts, "仍未能禁用")
	retryAgentExchangeDisables(now)
	retryAgentExchangeDisables(now)
	if st, _, _ := exchangeRow(t, exchangeOldCode); st != db.AgentExchangeDisabling {
		t.Fatalf("仍失败时应保持 disabling: %s", st)
	}
	if u.disableTries.Load() != 3 {
		t.Fatalf("应共尝试禁用 3 次（首次 + 重试 2 次）: %d", u.disableTries.Load())
	}
	if got := countContaining(alerts, "仍未能禁用") - before; got != 1 {
		t.Fatalf("同一条记录同一天只提醒 1 次: %d", got)
	}

	// 卡台恢复：下一轮禁用成功，转 done
	u.disableFail.Store(false)
	retryAgentExchangeDisables(now)
	if st, _, _ := exchangeRow(t, exchangeOldCode); st != db.AgentExchangeDone {
		t.Fatalf("禁用成功后应转 done: %s", st)
	}
	if u.disabled.Load() != 1 || db.GetCardplatformCDKStatus(77) != "disabled" {
		t.Fatalf("旧码应已禁用并更新本地状态: disabled=%d", u.disabled.Load())
	}
	tries := u.disableTries.Load()
	retryAgentExchangeDisables(now)
	if u.disableTries.Load() != tries {
		t.Fatal("done 的记录不应再重试")
	}
	// 重试期间，这张旧码始终不能再换
	if w := doExchange(exchangeOldCode, "203.0.113.9"); w.Code != 409 {
		t.Fatalf("已换过的旧码不能再换: %d", w.Code)
	}
	if u.issued.Load() != 1 {
		t.Fatalf("重试禁用不能再发码: %d", u.issued.Load())
	}
}

// 人工定论为已发码后，禁用旧码失败同样停在 disabling 并告警，不报成已完成。
func TestAdminResolveIssuedDisableFailureStaysDisabling(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	alerts := captureOpsAlerts(t)
	u := newExchangeUpstream(t)
	u.onIssue = func(_ int32, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"code":500}`))
	}
	if w := doExchange(exchangeOldCode, "203.0.113.9"); w.Code != 502 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	u.disableFail.Store(true)
	var id int64
	if err := db.DB.QueryRow(`SELECT id FROM agent_cdk_exchanges`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	r := adminExchangeRouter()
	w := adminExchangeDo(r, "POST", fmt.Sprintf("/agent-exchanges/%d/resolve", id), `{"result":"issued","new_cdk_id":950,"note":"卡台库存查到新码"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"old_disabled":false`) {
		t.Fatalf("应成功定论但提示旧码未禁用: %d %s", w.Code, w.Body.String())
	}
	if st, newID, _ := exchangeRow(t, exchangeOldCode); st != db.AgentExchangeDisabling || newID != 950 {
		t.Fatalf("旧码没禁用，不能是 done: %s %d", st, newID)
	}
	if countContaining(alerts, "禁用旧码失败") != 1 {
		t.Fatalf("应告警: %v", alerts.texts())
	}
	u.disableFail.Store(false)
	retryAgentExchangeDisables(time.Now())
	if st, _, _ := exchangeRow(t, exchangeOldCode); st != db.AgentExchangeDone {
		t.Fatalf("重试成功后应转 done: %s", st)
	}
}
