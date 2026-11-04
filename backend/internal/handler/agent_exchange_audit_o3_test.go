package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// 审计 O3 组 money N-5：换码禁用旧码后，本地状态同步失败不能静默忽略（和 E-8 一致：记日志并告警）。

// o3FailLocalDisable 让「把本地缓存的码状态改成 disabled」写库失败，其余写库照常。
func o3FailLocalDisable(t *testing.T) {
	t.Helper()
	if _, err := db.DB.Exec(`CREATE TRIGGER o3_fail_local_disable BEFORE UPDATE OF status ON cardplatform_cdk_codes
		WHEN NEW.status = 'disabled' BEGIN SELECT RAISE(ABORT, 'o3 本地写库失败'); END`); err != nil {
		t.Fatal(err)
	}
}

func o3DropFailLocalDisable(t *testing.T) {
	t.Helper()
	if _, err := db.DB.Exec(`DROP TRIGGER o3_fail_local_disable`); err != nil {
		t.Fatal(err)
	}
}

// 换码成功、卡台已禁用旧码，但本地状态写库失败：新码照常交给代理，记录照常转 done，告警说明本地状态没同步。
func TestO3AgentExchangeLocalStatusSyncFailureAlerts(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	alerts := captureOpsAlerts(t)
	u := newExchangeUpstream(t)
	o3FailLocalDisable(t)

	w := doExchange(exchangeOldCode, "203.0.113.9")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ZC-EXCHANGE-NEW-CODE-0001") {
		t.Fatalf("新码已发出，应交给代理: %d %s", w.Code, w.Body.String())
	}
	if u.disabled.Load() != 1 {
		t.Fatalf("卡台应禁用旧码 1 次: %d", u.disabled.Load())
	}
	if st, _, _ := exchangeRow(t, exchangeOldCode); st != db.AgentExchangeDone {
		t.Fatalf("卡台已禁用旧码，记录应转 done: %s", st)
	}
	if db.GetCardplatformCDKStatus(77) == "disabled" {
		t.Fatal("前提：本地写库应失败")
	}
	if n := countContaining(alerts, "本站列表状态没能同步"); n != 1 {
		t.Fatalf("本地状态同步失败应告警 1 次: %v", alerts.texts())
	}
	if a := alerts.texts()[0]; !strings.Contains(a, "旧码 ID 77") {
		t.Fatalf("告警应含旧码 ID: %s", a)
	}

	// 写库恢复：同步成功不再告警
	o3DropFailLocalDisable(t)
	before := alerts.count()
	if err := finishDisableOldCode(context.Background(), nil, 0, 77); err == nil {
		t.Fatal("没有卡台客户端应报错")
	}
	if alerts.count() != before {
		t.Fatalf("卡台禁用没做成时不应发本地同步告警: %v", alerts.texts())
	}
}

// 人工定论 issued 走同一个收尾：本地状态同步失败同样告警，不影响定论结果。
func TestO3AdminResolveIssuedLocalStatusSyncFailureAlerts(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	alerts := captureOpsAlerts(t)
	u := newExchangeUpstream(t)
	u.onIssue = func(_ int32, w2 http.ResponseWriter, _ *http.Request) {
		w2.WriteHeader(500)
		w2.Write([]byte(`{"code":500}`))
	}
	if w := doExchange(exchangeOldCode, "203.0.113.9"); w.Code != 502 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var id int64
	if err := db.DB.QueryRow(`SELECT id FROM agent_cdk_exchanges`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	o3FailLocalDisable(t)
	before := countContaining(alerts, "本站列表状态没能同步")
	r := adminExchangeRouter()
	w := adminExchangeDo(r, "POST", fmt.Sprintf("/agent-exchanges/%d/resolve", id), `{"result":"issued","new_cdk_id":950,"note":"卡台库存查到新码"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"old_disabled":true`) {
		t.Fatalf("卡台已禁用旧码，定论应成功: %d %s", w.Code, w.Body.String())
	}
	if got := countContaining(alerts, "本站列表状态没能同步") - before; got != 1 {
		t.Fatalf("本地状态同步失败应告警 1 次: %v", alerts.texts())
	}
}
