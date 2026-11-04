package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

func lastReconcile(t *testing.T) agentReconcileView {
	t.Helper()
	raw, err := db.GetSetting("agent_reconcile_last")
	if err != nil || raw == "" {
		t.Fatalf("没有核对结果: %q %v", raw, err)
	}
	var v agentReconcileView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestReconcileRunsOncePerDayAndAlerts(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	id, _, _ := e.seedAgent("alice", tier)
	if code, m := adjust(e, id, "rc-1", `{"amount_cents":1000,"note":"充"}`); code != 200 {
		t.Fatalf("调账: %d %v", code, m)
	}
	day1 := time.Date(2026, 11, 4, 10, 0, 0, 0, beijingZone)

	// 账平：写 ok=true，不告警
	reconcileAgentBalances(day1)
	if v := lastReconcile(t); !v.OK || len(v.Mismatches) != 0 || v.At != day1.Unix() {
		t.Fatalf("账平: %+v", v)
	}
	raw, _ := db.GetSetting("agent_reconcile_last")
	if !strings.Contains(raw, `"mismatches":[]`) {
		t.Fatalf("mismatches 应是空数组: %s", raw)
	}
	if n := len(e.alertList()); n != 0 {
		t.Fatalf("账平不应告警: %v", e.alertList())
	}

	// 同一个北京日改坏余额再跑：不再查
	if _, err := db.DB.Exec(`UPDATE agents SET balance_cents = balance_cents + 77 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	reconcileAgentBalances(day1.Add(3 * time.Hour))
	if !lastReconcile(t).OK || len(e.alertList()) != 0 {
		t.Fatalf("同一天不应再核对: %+v %v", lastReconcile(t), e.alertList())
	}

	// 清掉今天的结果，模拟新一天：重新核对，对不上
	if err := db.SetSetting("agent_reconcile_last", ""); err != nil {
		t.Fatal(err)
	}
	reconcileAgentBalances(day1.Add(4 * time.Hour))
	v := lastReconcile(t)
	if v.OK || len(v.Mismatches) != 1 || v.Mismatches[0].AgentID != id || v.Mismatches[0].DiffCents != 77 {
		t.Fatalf("应对不上 77 美分: %+v", v)
	}
	alerts := e.alertList()
	if len(alerts) != 1 || !strings.Contains(alerts[0], "代理商余额与流水对不上") || !strings.Contains(alerts[0], "0.77") {
		t.Fatalf("应有 1 条告警: %v", alerts)
	}

	// 同一个北京日再跑：告警数不变，结果不变
	at := v.At
	reconcileAgentBalances(day1.Add(5 * time.Hour))
	if len(e.alertList()) != 1 || lastReconcile(t).At != at {
		t.Fatalf("同一天不应再查: %v %+v", e.alertList(), lastReconcile(t))
	}
	// 北京日的边界：23:59 仍是同一天
	reconcileAgentBalances(time.Date(2026, 11, 4, 23, 59, 0, 0, beijingZone))
	if len(e.alertList()) != 1 {
		t.Fatalf("23:59 仍是同一天: %v", e.alertList())
	}

	// 下一天：重新核对，仍对不上就再告警一次
	day2 := time.Date(2026, 11, 5, 0, 1, 0, 0, beijingZone)
	reconcileAgentBalances(day2)
	if v := lastReconcile(t); v.At != day2.Unix() || v.OK {
		t.Fatalf("下一天应重新核对: %+v", v)
	}
	if len(e.alertList()) != 2 {
		t.Fatalf("下一天应再告警一次: %v", e.alertList())
	}

	// 修好后第三天：ok
	if _, err := db.DB.Exec(`UPDATE agents SET balance_cents = balance_cents - 77 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	reconcileAgentBalances(time.Date(2026, 11, 6, 9, 0, 0, 0, beijingZone))
	if v := lastReconcile(t); !v.OK || len(v.Mismatches) != 0 {
		t.Fatalf("修好后应 ok: %+v", v)
	}
	if len(e.alertList()) != 2 {
		t.Fatalf("修好后不应再告警: %v", e.alertList())
	}
}

func TestRunOpsMaintenanceReconciles(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	e.seedAgent("alice", tier)
	runOpsMaintenance(time.Date(2026, 11, 4, 10, 0, 0, 0, beijingZone))
	if !lastReconcile(t).OK {
		t.Fatal("维护任务应跑每日核对")
	}
}

func TestAdminAgentSummaryReconcileWithoutTimeIsNull(t *testing.T) {
	e := newAgentEnv(t)
	for _, raw := range []string{`{}`, `null`, `{"ok":true}`} {
		if err := db.SetSetting("agent_reconcile_last", raw); err != nil {
			t.Fatal(err)
		}
		w := e.adminDo("GET", adminBase+"/agent-summary", "", nil)
		m := decodeMap(t, w.Body.String())
		if w.Code != 200 || m["reconcile"] != nil {
			t.Fatalf("%s 应按还没对过账处理: %d %s", raw, w.Code, w.Body.String())
		}
	}
	// 正常内容的 mismatches 缺省时补成空数组
	if err := db.SetSetting("agent_reconcile_last", `{"at":1700000000,"ok":true}`); err != nil {
		t.Fatal(err)
	}
	w := e.adminDo("GET", adminBase+"/agent-summary", "", nil)
	if !strings.Contains(w.Body.String(), `"mismatches":[]`) {
		t.Fatalf("缺省 mismatches 应为空数组: %s", w.Body.String())
	}
}
