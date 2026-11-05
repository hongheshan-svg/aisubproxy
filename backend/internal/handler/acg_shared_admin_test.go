package handler

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

func (f *setFix) adminShared(method, sub string) (int, map[string]any, string) {
	f.t.Helper()
	w := f.adminDo(method, fmt.Sprintf("%s/agents/%d/shared%s", adminBase, f.aid, sub), "", nil)
	return w.Code, decodeMap(f.t, w.Body.String()), w.Body.String()
}

func TestAdminAgentShared_GetAndToggle(t *testing.T) {
	f := newSetFix(t, true)
	mk, err := secretbox.LoadKey()
	if err != nil {
		t.Fatal(err)
	}
	f.appID, f.key = sharedFixAppID, sharedFixKey
	f.storeKey(mk, f.key)
	cr, err := db.GetAgentSharedCreds(context.Background(), f.aid)
	if err != nil || cr.KeySealed == "" {
		t.Fatalf("应已存密文: %v", err)
	}
	sealed := cr.KeySealed

	code, m, raw := f.adminShared("GET", "")
	if code != 200 || m["status"] != "active" || m["app_id"] != sharedFixAppID {
		t.Fatalf("%d %s", code, raw)
	}
	if strings.Contains(raw, sharedFixKey) || strings.Contains(raw, "key_sealed") || (sealed != "" && strings.Contains(raw, sealed)) {
		t.Fatalf("响应不应含密钥或密文: %s", raw)
	}
	if al, ok := m["ip_allowlist"].([]any); !ok || len(al) != 0 {
		t.Fatalf("ip_allowlist 应为 []: %s", raw)
	}
	for _, k := range []string{"key_created_at", "last_used_at", "low_balance_cents", "failures"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("缺字段 %s: %s", k, raw)
		}
	}

	code, m, raw = f.adminShared("POST", "/disable")
	if code != 200 || m["status"] != "admin_disabled" {
		t.Fatalf("%d %s", code, raw)
	}
	if _, m, _ = f.adminShared("GET", ""); m["status"] != "admin_disabled" {
		t.Fatalf("%v", m)
	}
	f.sharedRefused("authentication/connect", nil, "对接已被管理员关闭")

	code, m, raw = f.adminShared("POST", "/enable")
	if code != 200 || m["status"] != "active" {
		t.Fatalf("%d %s", code, raw)
	}
	f.mustShared("authentication/connect", nil)

	audit := auditRows(t)
	for _, act := range []string{"admin_agent_shared_disable", "admin_agent_shared_enable"} {
		if !strings.Contains(audit, act+"|id="+fmt.Sprint(f.aid)+" username=alice") {
			t.Fatalf("缺审计 %s:\n%s", act, audit)
		}
	}

	for _, sub := range []string{"", "/disable", "/enable"} {
		method := "POST"
		if sub == "" {
			method = "GET"
		}
		w := f.adminDo(method, adminBase+"/agents/99999/shared"+sub, "", nil)
		if w.Code != 404 {
			t.Fatalf("不存在的代理商应 404: %s %d", sub, w.Code)
		}
	}
}

func TestAdminAgentShared_EnableNeverOpened(t *testing.T) {
	f := newSetFix(t, true)
	code, m, raw := f.adminShared("POST", "/enable")
	if code != 200 || m["status"] != "inactive" {
		t.Fatalf("%d %s", code, raw)
	}
	if code, m, raw = f.adminShared("POST", "/disable"); code != 200 || m["status"] != "admin_disabled" {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestAdminAgentShared_Failures(t *testing.T) {
	f := newSetFix(t, true)
	ctx := context.Background()
	for i := 0; i < 60; i++ {
		nf := db.NewSharedFailure{AgentID: f.aid, RequestNo: fmt.Sprintf("r%02d", i), ProductCode: "mw-plus-ph", Quantity: 1, Reason: "insufficient_balance", Message: "余额不足"}
		if err := db.RecordAgentSharedFailure(ctx, nf, f.clock.Add(-time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	code, m, raw := f.adminShared("GET", "")
	items, _ := m["failures"].([]any)
	if code != 200 || len(items) != 50 {
		t.Fatalf("应返回 50 行: %d %d", code, len(items))
	}
	prev := float64(1 << 60)
	for _, it := range items {
		row := it.(map[string]any)
		la := row["last_at"].(float64)
		if la > prev {
			t.Fatalf("应按 last_at 降序: %s", raw)
		}
		prev = la
	}
	if items[0].(map[string]any)["request_no"] != "r00" {
		t.Fatalf("%v", items[0])
	}
}

func TestAdminAgentViews_Source(t *testing.T) {
	f, web, acg := sourceFix(t)
	want := map[int64][2]string{web: {"web", ""}, acg: {"acg", tradeNo1}}
	m := decodeMap(t, f.adminDo("GET", fmt.Sprintf("%s/agents/%d/orders", adminBase, f.aid), "", nil).Body.String())
	items := m["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("%v", m)
	}
	for _, it := range items {
		o := it.(map[string]any)
		w := want[int64(o["id"].(float64))]
		if o["source"] != w[0] || o["external_ref"] != w[1] {
			t.Fatalf("管理员订单来源不对: %v", o)
		}
	}
	m = decodeMap(t, f.adminDo("GET", fmt.Sprintf("%s/agents/%d/ledger", adminBase, f.aid), "", nil).Body.String())
	n := 0
	for _, it := range m["items"].([]any) {
		r := it.(map[string]any)
		if _, ok := r["source"]; !ok {
			t.Fatalf("流水缺 source: %v", r)
		}
		if r["kind"] == "purchase" {
			w := want[int64(r["order_id"].(float64))]
			if r["source"] != w[0] || r["external_ref"] != w[1] {
				t.Fatalf("管理员流水来源不对: %v", r)
			}
			n++
		}
	}
	if n != 2 {
		t.Fatalf("应有 2 条下单流水: %v", m)
	}
}
