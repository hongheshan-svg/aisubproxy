package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const (
	codesPath  = agentBase + "/codes"
	ledgerPath = agentBase + "/ledger"
)

type codeRowReply struct {
	ID          int64   `json:"id"`
	CodePrefix  string  `json:"code_prefix"`
	ProductCode string  `json:"product_code"`
	Plan        string  `json:"plan"`
	Region      string  `json:"region"`
	Status      string  `json:"status"`
	Reason      string  `json:"reason"`
	CreatedAt   int64   `json:"created_at"`
	OrderID     int64   `json:"order_id"`
	Note        string  `json:"note"`
	Email       *string `json:"email"`
	UsedAt      *int64  `json:"used_at"`
	Code        string  `json:"code"`
	Unavailable bool    `json:"unavailable"`
}

// topUp 给 alice 加 5000 美分余额，下多张码用。
func (f *orderFix) topUp() {
	f.t.Helper()
	if code, m := adjust(f.agentEnv, f.aid, "topup-codes", `{"amount_cents":5000,"note":"补"}`); code != 200 {
		f.t.Fatalf("调账: %d %v", code, m)
	}
}

func (f *orderFix) listCodes(s *agentSession, q string) (int, []codeRowReply) {
	f.t.Helper()
	w := f.agentDo(s, "GET", codesPath+q, "")
	if w.Code != 200 {
		f.t.Fatalf("GET /codes%s: %d %s", q, w.Code, w.Body.String())
	}
	var r struct {
		Items []codeRowReply `json:"items"`
		Total int            `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		f.t.Fatal(err)
	}
	return r.Total, r.Items
}

func codeIDs(list []codeRowReply) map[int64]bool {
	m := map[int64]bool{}
	for _, c := range list {
		m[c.ID] = true
	}
	return m
}

func (f *orderFix) exportCodes(s *agentSession, body string) (int, []codeRowReply, string) {
	f.t.Helper()
	w := f.agentDo(s, "POST", codesPath+"/export", body)
	var r struct {
		Codes []codeRowReply `json:"codes"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	if w.Code == 200 && w.Header().Get("Cache-Control") != "no-store" {
		f.t.Fatalf("导出响应应带 no-store: %q", w.Header().Get("Cache-Control"))
	}
	return w.Code, r.Codes, w.Body.String()
}

func TestAgentCodesListScopedAndMapped(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	_, bs := f.seedBuyer("bob", 1000)
	mine := f.mustOrder(f.s, orderReq("mw-plus-ph", 2, "给客户", "req-codes-a", 500))
	theirs := f.mustOrder(bs, orderReq("mw-plus-ph", 1, "", "req-codes-b", 500))

	total, items := f.listCodes(f.s, "")
	if total != 2 || len(items) != 2 {
		t.Fatalf("alice 应只看到自己的 2 张码: %d %+v", total, items)
	}
	if items[0].ID != mine.Codes[1].ID || items[1].ID != mine.Codes[0].ID {
		t.Fatalf("应按 id 降序: %+v", items)
	}
	for _, it := range items {
		if codeIDs(items)[theirs.Codes[0].ID] {
			t.Fatal("混进了别人的码")
		}
		if it.Status != "unused" || it.Email != nil || it.UsedAt != nil || it.ProductCode != "mw-plus-ph" ||
			it.Plan != "plus" || it.Region != "PH" || it.OrderID != mine.Order.ID || it.Note != "给客户" || it.CodePrefix == "" {
			t.Fatalf("未使用的码: %+v", it)
		}
	}

	// 一张码兑换成功后：used，邮箱打码，有使用时间
	used := mine.Codes[0].ID
	f.insertOrder("cdk", db.LocalCDKSourceRef(used), 0, "alice.smith@example.com", "succeeded", "", true)
	_, items = f.listCodes(f.s, "")
	var got codeRowReply
	for _, it := range items {
		if it.ID == used {
			got = it
		}
	}
	if got.Status != "used" || got.Email == nil || *got.Email != "al***@example.com" || got.UsedAt == nil || *got.UsedAt <= 0 {
		t.Fatalf("已使用的码: %+v", got)
	}
	w := f.agentDo(f.s, "GET", codesPath, "")
	if strings.Contains(w.Body.String(), "alice.smith") {
		t.Fatal("邮箱应打码")
	}
	// 在途 → processing
	f.insertOrder("cdk", db.LocalCDKSourceRef(mine.Codes[1].ID), 0, "x@example.com", "running", "", true)
	if _, items = f.listCodes(f.s, "?status=processing"); len(items) != 1 || items[0].ID != mine.Codes[1].ID || items[0].Email != nil {
		t.Fatalf("在途的码应是 processing: %+v", items)
	}
	// 没登录
	if w := f.send("GET", codesPath, "", nil, nil); w.Code != 401 {
		t.Fatalf("未登录应 401: %d", w.Code)
	}
}

func TestAgentCodesFilters(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.topUp()
	o1 := f.mustOrder(f.s, orderReq("mw-plus-ph", 2, "", "req-filter-1", 500))
	o2 := f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-filter-2", 500))
	if _, err := db.DB.Exec(`UPDATE local_cdks SET created_at = 1000 WHERE agent_order_id = ?`, o1.Order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE local_cdks SET created_at = 2000 WHERE agent_order_id = ?`, o2.Order.ID); err != nil {
		t.Fatal(err)
	}
	f.insertOrder("cdk", db.LocalCDKSourceRef(o2.Codes[0].ID), 0, "u@example.com", "succeeded", "", true)

	count := func(q string) int { _, items := f.listCodes(f.s, q); return len(items) }
	cases := []struct {
		q    string
		want int
	}{
		{"", 3},
		{"?product=mw-plus-ph", 3},
		{"?product=other", 0},
		{"?status=unused", 2},
		{"?status=used", 1},
		{"?status=failed", 0},
		{fmt.Sprintf("?order_id=%d", o1.Order.ID), 2},
		{fmt.Sprintf("?order_id=%d", o2.Order.ID), 1},
		{"?from=1000&to=2000", 2}, // from <= created_at < to
		{"?from=1001&to=2001", 1},
		{"?to=1000", 0},
		{"?from=2000", 1},
		{fmt.Sprintf("?order_id=%d&status=used", o1.Order.ID), 0},
	}
	for _, c := range cases {
		if n := count(c.q); n != c.want {
			t.Errorf("%q: 想要 %d 张，得到 %d", c.q, c.want, n)
		}
	}
	if total, items := f.listCodes(f.s, "?page=2&page_size=2"); total != 3 || len(items) != 1 {
		t.Fatalf("分页: %d %d", total, len(items))
	}
	for _, q := range []string{"?from=abc", "?to=1.5", "?from=10&to=5", "?status=bogus", "?order_id=x"} {
		w := f.agentDo(f.s, "GET", codesPath+q, "")
		if w.Code != 400 || jsonField(t, w.Body.String(), "error") != "invalid_request" {
			t.Errorf("%s 应 400 invalid_request: %d %s", q, w.Code, w.Body.String())
		}
	}
}

func TestAgentCodesExportByIDsScoped(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	_, bs := f.seedBuyer("bob", 1000)
	mine := f.mustOrder(f.s, orderReq("mw-plus-ph", 2, "", "req-cx-a", 500))
	theirs := f.mustOrder(bs, orderReq("mw-plus-ph", 1, "", "req-cx-b", 500))

	body := fmt.Sprintf(`{"ids":[%d,%d,%d,99999]}`, mine.Codes[0].ID, theirs.Codes[0].ID, mine.Codes[1].ID)
	code, out, raw := f.exportCodes(f.s, body)
	if code != 200 || len(out) != 2 {
		t.Fatalf("应只返回自己的 2 张: %d %s", code, raw)
	}
	if strings.Contains(raw, theirs.Codes[0].Code) {
		t.Fatal("拿到了别人的码")
	}
	for i, c := range out {
		if c.ID != mine.Codes[i].ID || c.Code != mine.Codes[i].Code || c.Status != "unused" || c.OrderID != mine.Order.ID || c.ProductCode != "mw-plus-ph" || c.Unavailable {
			t.Fatalf("第 %d 项不对: %+v", i, c)
		}
	}
	// 只有别人的 ID：空结果
	if code, out, raw = f.exportCodes(f.s, fmt.Sprintf(`{"ids":[%d]}`, theirs.Codes[0].ID)); code != 200 || len(out) != 0 {
		t.Fatalf("只有别人的码应得空: %d %s", code, raw)
	}
	// 按筛选导出
	if code, out, _ = f.exportCodes(f.s, fmt.Sprintf(`{"filter":{"order_id":%d}}`, mine.Order.ID)); code != 200 || len(out) != 2 || out[0].Code == "" {
		t.Fatalf("按筛选导出: %d %+v", code, out)
	}
	// ids 和 filter 二选一
	for _, b := range []string{`{}`, `{"ids":[1],"filter":{}}`, `{"ids":[]}`, `{"ids":[0]}`, `{"filter":{"status":"bogus"}}`, `not json`} {
		if code, _, raw = f.exportCodes(f.s, b); code != 400 || jsonField(t, raw, "error") != "invalid_request" {
			t.Errorf("%s 应 400: %d %s", b, code, raw)
		}
	}
	audit := auditRows(t)
	for _, row := range append(out, f.listAll(t)...) {
		if row.CodePrefix != "" && strings.Contains(audit, row.CodePrefix) {
			t.Fatalf("审计里不应出现码的前缀 %q:\n%s", row.CodePrefix, audit)
		}
	}
	if !strings.Contains(audit, "agent:alice|agent_codes_export|count=2 mode=ids") || !strings.Contains(audit, "mode=filter") {
		t.Fatalf("缺导出审计:\n%s", audit)
	}
	for _, c := range append(mine.Codes, theirs.Codes...) {
		if strings.Contains(audit, c.Code) {
			t.Fatal("审计里出现了完整码")
		}
	}
}

func TestAgentCodesExportUnavailable(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.topUp()
	r := f.mustOrder(f.s, orderReq("mw-plus-ph", 3, "", "req-cu-1", 500))
	bad := r.Codes[1].ID
	if _, err := db.DB.Exec(`UPDATE local_cdks SET code_sealed = 'v1:AAAA' WHERE id = ?`, bad); err != nil {
		t.Fatal(err)
	}
	before := len(f.alertList())
	code, out, raw := f.exportCodes(f.s, `{"filter":{}}`)
	if code != 200 || len(out) != 3 {
		t.Fatalf("导出: %d %s", code, raw)
	}
	for _, c := range out {
		if c.ID == bad {
			if !c.Unavailable || c.Code != "" {
				t.Fatalf("解不开的应 unavailable 且码为空: %+v", c)
			}
			continue
		}
		if c.Unavailable || c.Code == "" {
			t.Fatalf("其余应正常: %+v", c)
		}
	}
	if strings.Count(raw, `"unavailable"`) != 1 {
		t.Fatalf("只有解不开的那张带 unavailable: %s", raw)
	}
	alerts := f.alertList()[before:]
	if len(alerts) != 1 || !strings.Contains(alerts[0], fmt.Sprint(bad)) {
		t.Fatalf("应有 1 条告警: %v", alerts)
	}
	for _, c := range r.Codes {
		if strings.Contains(alerts[0], c.Code) {
			t.Fatal("告警里出现了完整码")
		}
	}
}

func TestAgentCodesExportNoMasterKey(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-ck-1", 500))
	t.Setenv("APP_MASTER_KEY", "")
	code, _, raw := f.exportCodes(f.s, `{"filter":{}}`)
	if code != 503 || jsonField(t, raw, "error") != "maintenance" {
		t.Fatalf("没有主密钥应 503 maintenance: %d %s", code, raw)
	}
	if alerts := f.alertList(); len(alerts) != 1 || !strings.Contains(alerts[0], "APP_MASTER_KEY") {
		t.Fatalf("应有 1 条每日告警: %v", alerts)
	}
	f.exportCodes(f.s, `{"filter":{}}`)
	if n := len(f.alertList()); n != 1 {
		t.Fatalf("每日告警同一天只发一次: %d", n)
	}
}

func TestAgentCodesExportLimit(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.topUp()
	r := f.mustOrder(f.s, orderReq("mw-plus-ph", 3, "", "req-cl-1", 500))
	old := agentExportMax
	agentExportMax = 2
	t.Cleanup(func() { agentExportMax = old })
	code, _, raw := f.exportCodes(f.s, `{"filter":{}}`)
	if code != 400 || jsonField(t, raw, "error") != "export_too_large" || !strings.Contains(raw, "一次最多导出 2 张，请缩小筛选范围") {
		t.Fatalf("超过上限应 400: %d %s", code, raw)
	}
	if code, out, _ := f.exportCodes(f.s, fmt.Sprintf(`{"ids":[%d,%d]}`, r.Codes[0].ID, r.Codes[1].ID)); code != 200 || len(out) != 2 {
		t.Fatalf("刚好等于上限应可导出: %d", code)
	}
	if code, _, _ = f.exportCodes(f.s, fmt.Sprintf(`{"ids":[%d,%d,%d]}`, r.Codes[0].ID, r.Codes[1].ID, r.Codes[2].ID)); code != 400 {
		t.Fatalf("ids 超过上限应 400: %d", code)
	}
}

// 换码或禁用后作废的码，GET /codes 和 POST /codes/export 都要如实显示 disabled。
func TestAgentCodesDisabledShownInListAndExport(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.topUp()
	r := f.mustOrder(f.s, orderReq("mw-plus-ph", 2, "", "req-cd-1", 500))
	off := r.Codes[0].ID
	if ok, err := db.DisableLocalCDK(context.Background(), off, time.Now()); err != nil || !ok {
		t.Fatalf("禁用: %v %v", ok, err)
	}
	_, items := f.listCodes(f.s, "?status=disabled")
	if len(items) != 1 || items[0].ID != off || items[0].Reason != "manual" {
		t.Fatalf("GET /codes 应显示 disabled: %+v", items)
	}
	code, out, raw := f.exportCodes(f.s, fmt.Sprintf(`{"ids":[%d,%d]}`, r.Codes[0].ID, r.Codes[1].ID))
	if code != 200 || len(out) != 2 || out[0].Status != "disabled" || out[0].Code != r.Codes[0].Code || out[1].Status != "unused" {
		t.Fatalf("POST /codes/export 应带 status: %d %s", code, raw)
	}
}

func TestAgentOverviewCounts(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.topUp()
	_, bs := f.seedBuyer("bob", 1000)
	var orders []orderReply
	for i := 0; i < 6; i++ {
		orders = append(orders, f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", fmt.Sprintf("req-ov-%d", i), 500)))
	}
	f.mustOrder(bs, orderReq("mw-plus-ph", 1, "", "req-ov-bob", 500))
	f.insertOrder("cdk", db.LocalCDKSourceRef(orders[0].Codes[0].ID), 0, "u@example.com", "succeeded", "", true)
	f.insertOrder("cdk", db.LocalCDKSourceRef(orders[1].Codes[0].ID), 0, "u@example.com", "running", "", true)
	f.insertOrder("cdk", db.LocalCDKSourceRef(orders[2].Codes[0].ID), 0, "u@example.com", "failed", "provider_declined", true)
	if _, err := db.DisableLocalCDK(context.Background(), orders[3].Codes[0].ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	// 订单的流水都按时钟（2026-11-04，北京时间 11 月）记账：把 alice 的流水挪出本月，只留 3 笔在本月
	if _, err := db.DB.Exec(`UPDATE agent_ledger SET created_at = ? WHERE agent_id = ? AND kind = 'purchase' AND order_id IN (?, ?, ?)`,
		time.Date(2026, 10, 15, 0, 0, 0, 0, beijingZone).Unix(), f.aid, orders[0].Order.ID, orders[1].Order.ID, orders[2].Order.ID); err != nil {
		t.Fatal(err)
	}

	w := f.agentDo(f.s, "GET", agentBase+"/overview", "")
	if w.Code != 200 {
		t.Fatalf("概览: %d %s", w.Code, w.Body.String())
	}
	var ov struct {
		Balance int64            `json:"balance_cents"`
		Month   int64            `json:"month_order_cents"`
		Counts  map[string]int   `json:"status_counts"`
		Recent  []map[string]any `json:"recent_orders"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if want := balanceOf(t, f.aid); ov.Balance != want || want != 1000+5000-6*500 {
		t.Fatalf("余额: %d / %d", ov.Balance, want)
	}
	if ov.Month != 3*500 {
		t.Fatalf("本月下单应为 1500（另外 3 笔在上个月）: %d", ov.Month)
	}
	if ov.Counts["used"] != 1 || ov.Counts["processing"] != 1 || ov.Counts["failed"] != 1 || ov.Counts["disabled"] != 1 || ov.Counts["unused"] != 2 {
		t.Fatalf("各状态码数: %v", ov.Counts)
	}
	if len(ov.Recent) != 5 || ov.Recent[0]["id"] != float64(orders[5].Order.ID) || ov.Recent[4]["id"] != float64(orders[1].Order.ID) {
		t.Fatalf("最近 5 笔: %v", ov.Recent)
	}
	if !strings.Contains(w.Body.String(), `"status_counts":{"disabled":1,"failed":1,"processing":1,"unused":2,"used":1}`) {
		t.Fatalf("status_counts 应含 5 个键: %s", w.Body.String())
	}
}

// ---- 流水 ----

func (f *orderFix) ledgerGet(path string) (int, map[string]any) {
	w := f.agentDo(f.s, "GET", path, "")
	return w.Code, decodeMap(f.t, w.Body.String())
}

// ledgerFix 造两条流水（调账 +1000、下单 -500），时间固定。
func ledgerFix(t *testing.T) (*orderFix, int64) {
	f := newOrderFix(t, newAgentEnv(t), true)
	o := f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-led-1", 500))
	const t0 = int64(1_700_000_000)
	if _, err := db.DB.Exec(`UPDATE agent_ledger SET created_at = ? WHERE agent_id = ? AND kind = 'adjust'`, t0, f.aid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE agent_ledger SET created_at = ? WHERE agent_id = ? AND kind = 'purchase'`, t0+100, f.aid); err != nil {
		t.Fatal(err)
	}
	return f, o.Order.ID
}

func TestAgentLedgerSummaryAndRange(t *testing.T) {
	f, orderID := ledgerFix(t)
	const t0 = int64(1_700_000_000)
	code, m := f.ledgerGet(fmt.Sprintf("%s?from=%d&to=%d", ledgerPath, t0-10, t0+1000))
	if code != 200 || m["total"] != float64(2) {
		t.Fatalf("流水: %d %v", code, m)
	}
	sum := m["summary"].(map[string]any)
	if sum["opening_cents"] != float64(0) || sum["closing_cents"] != float64(500) || sum["income_cents"] != float64(1000) || sum["expense_cents"] != float64(500) {
		t.Fatalf("汇总: %v", sum)
	}
	items := m["items"].([]any)
	first := items[0].(map[string]any)
	if first["kind"] != "purchase" || first["amount_cents"] != float64(-500) || first["balance_after_cents"] != float64(500) || first["order_id"] != float64(orderID) {
		t.Fatalf("第一行应是下单: %v", first)
	}
	if _, has := first["operator"]; has {
		t.Fatalf("代理商看到的流水不应含操作人: %v", first)
	}
	if _, has := first["ext_ref"]; has {
		t.Fatalf("代理商看到的流水不应含外部单号: %v", first)
	}
	if items[1].(map[string]any)["order_id"] != float64(0) {
		t.Fatalf("没有订单时 order_id 为 0: %v", items[1])
	}
	// 范围从下单之后开始：期初 = 调账后的 1000
	_, m = f.ledgerGet(fmt.Sprintf("%s?from=%d&to=%d", ledgerPath, t0+50, t0+1000))
	sum = m["summary"].(map[string]any)
	if m["total"] != float64(1) || sum["opening_cents"] != float64(1000) || sum["closing_cents"] != float64(500) || sum["income_cents"] != float64(0) || sum["expense_cents"] != float64(500) {
		t.Fatalf("后半段: %v", m)
	}
	// 范围内没有流水：期末等于期初
	_, m = f.ledgerGet(fmt.Sprintf("%s?from=%d&to=%d", ledgerPath, t0+500, t0+600))
	sum = m["summary"].(map[string]any)
	if m["total"] != float64(0) || len(m["items"].([]any)) != 0 || sum["opening_cents"] != float64(500) || sum["closing_cents"] != float64(500) {
		t.Fatalf("空范围: %v", m)
	}
	day := int64(86400)
	if code, m = f.ledgerGet(fmt.Sprintf("%s?from=%d&to=%d", ledgerPath, t0, t0+366*day)); code != 200 {
		t.Fatalf("恰好 366 天应可查: %d %v", code, m)
	}
	if code, m = f.ledgerGet(fmt.Sprintf("%s?from=%d&to=%d", ledgerPath, t0, t0+366*day+1)); code != 400 || m["error"] != "range_too_large" {
		t.Fatalf("超过 366 天应 400 range_too_large: %d %v", code, m)
	}
	for _, q := range []string{"?from=x", "?to=1.5", "?from=10&to=5"} {
		if code, m = f.ledgerGet(ledgerPath + q); code != 400 || m["error"] != "invalid_request" {
			t.Errorf("%s 应 400 invalid_request: %d %v", q, code, m)
		}
	}
	// 默认最近 30 天：流水都在 2027 年，当前时钟是 2026-11，默认范围里没有
	if code, m = f.ledgerGet(ledgerPath); code != 200 || m["total"] != float64(0) {
		t.Fatalf("默认范围: %d %v", code, m)
	}
	// 默认范围包含此刻
	now := f.clock.Unix()
	if _, err := db.DB.Exec(`UPDATE agent_ledger SET created_at = ? WHERE kind = 'purchase'`, now-3600); err != nil {
		t.Fatal(err)
	}
	if _, m = f.ledgerGet(ledgerPath); m["total"] != float64(1) {
		t.Fatalf("默认最近 30 天应含 1 条: %v", m)
	}
	if _, m = f.ledgerGet(ledgerPath + "?page_size=0"); len(m["items"].([]any)) != 1 {
		t.Fatalf("分页: %v", m)
	}
}

func TestAgentLedgerExportCSV(t *testing.T) {
	f, orderID := ledgerFix(t)
	const t0 = int64(1_700_000_000)
	if _, err := db.DB.Exec(`UPDATE agent_ledger SET note = '=HYPERLINK("http://x")' WHERE kind = 'adjust'`); err != nil {
		t.Fatal(err)
	}
	q := fmt.Sprintf("?from=%d&to=%d", t0-10, t0+1000)
	w := f.agentDo(f.s, "GET", ledgerPath+"/export"+q, "")
	if w.Code != 200 {
		t.Fatalf("导出: %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Fatalf("Content-Type: %q", ct)
	}
	wantName := `attachment; filename="ledger-alice-2026-11-04.csv"`
	if cd := w.Header().Get("Content-Disposition"); cd != wantName {
		t.Fatalf("Content-Disposition: %q 想要 %q", cd, wantName)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "\xEF\xBB\xBF时间,类型,金额,变动后余额,订单号,备注\n") && !strings.HasPrefix(body, "\xEF\xBB\xBF时间,类型,金额,变动后余额,订单号,备注\r\n") {
		t.Fatalf("应有 BOM 和表头: %q", body)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 3 {
		t.Fatalf("应有表头加 2 行: %q", body)
	}
	buy := strings.TrimSpace(lines[1])
	if !strings.Contains(buy, ",下单,-5.00,5.00,"+fmt.Sprint(orderID)+",") {
		t.Fatalf("下单行（金额 -5.00 不加引号）: %q", buy)
	}
	adj := strings.TrimSpace(lines[2])
	if !strings.Contains(adj, ",调账,10.00,10.00,,") || !strings.Contains(adj, `'=HYPERLINK`) {
		t.Fatalf("调账行，备注以 = 开头要加单引号: %q", adj)
	}
	if strings.Contains(adj, `,=HYPERLINK`) || strings.Contains(adj, `"=HYPERLINK`) {
		t.Fatalf("备注不应原样以 = 开头: %q", adj)
	}
	if !strings.HasPrefix(buy, time.Unix(t0+100, 0).In(beijingZone).Format("2006-01-02 15:04:05")) {
		t.Fatalf("时间应按北京时间: %q", buy)
	}
	// 代理商导出不含操作人列
	if strings.Contains(lines[0], "操作人") {
		t.Fatalf("代理商 CSV 不应有操作人列: %q", lines[0])
	}
	if code, _ := f.ledgerGet(ledgerPath + "/export?from=1&to=" + fmt.Sprint(1+367*86400)); code != 400 {
		t.Fatalf("导出也限 366 天: %d", code)
	}
}

func TestCSVSafe(t *testing.T) {
	for in, want := range map[string]string{"": "", "abc": "abc", "=1+1": "'=1+1", "+1": "'+1", "-1": "'-1", "@a": "'@a", "\tx": "'\tx", "\rx": "'\rx", "a=b": "a=b", "备注": "备注"} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q)=%q 想要 %q", in, got, want)
		}
	}
}

func TestAdminAgentLedgerSharesQuery(t *testing.T) {
	f, _ := ledgerFix(t)
	const t0 = int64(1_700_000_000)
	if _, err := db.DB.Exec(`UPDATE agent_ledger SET operator = 'admin', ext_ref = 'adj-ref-1' WHERE kind = 'adjust'`); err != nil {
		t.Fatal(err)
	}
	q := fmt.Sprintf("?from=%d&to=%d", t0-10, t0+1000)
	_, mine := f.ledgerGet(ledgerPath + q)
	w := f.adminDo("GET", fmt.Sprintf("%s/agents/%d/ledger%s", adminBase, f.aid, q), "", nil)
	if w.Code != 200 {
		t.Fatalf("管理员流水: %d %s", w.Code, w.Body.String())
	}
	adm := decodeMap(t, w.Body.String())
	if adm["total"] != mine["total"] || fmt.Sprint(adm["summary"]) != fmt.Sprint(mine["summary"]) {
		t.Fatalf("总数、汇总应一致: %v vs %v", adm, mine)
	}
	ai, mi := adm["items"].([]any), mine["items"].([]any)
	if len(ai) != len(mi) || len(ai) != 2 {
		t.Fatalf("条数: %d %d", len(ai), len(mi))
	}
	for i := range ai {
		a, m := ai[i].(map[string]any), mi[i].(map[string]any)
		for k, v := range m {
			if a[k] != v {
				t.Fatalf("第 %d 行 %s 不一致: %v vs %v", i, k, a[k], v)
			}
		}
		if _, ok := a["operator"]; !ok {
			t.Fatalf("管理员行应带 operator: %v", a)
		}
	}
	if ai[1].(map[string]any)["operator"] != "admin" || ai[1].(map[string]any)["ext_ref"] != "adj-ref-1" {
		t.Fatalf("操作人、外部单号: %v", ai[1])
	}
	// 范围规则相同
	w = f.adminDo("GET", fmt.Sprintf("%s/agents/%d/ledger?from=0&to=%d", adminBase, f.aid, 400*86400), "", nil)
	if w.Code != 400 || jsonField(t, w.Body.String(), "error") != "range_too_large" {
		t.Fatalf("管理员范围超限: %d %s", w.Code, w.Body.String())
	}
	// CSV
	w = f.adminDo("GET", fmt.Sprintf("%s/agents/%d/ledger/export%s", adminBase, f.aid, q), "", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "\xEF\xBB\xBF") || !strings.Contains(w.Header().Get("Content-Disposition"), "ledger-alice-") {
		t.Fatalf("管理员 CSV: %d %q %s", w.Code, w.Header().Get("Content-Disposition"), w.Body.String())
	}
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if !strings.HasSuffix(strings.TrimSpace(lines[0]), "备注,操作人") || !strings.HasSuffix(strings.TrimSpace(lines[2]), ",admin") {
		t.Fatalf("管理员 CSV 多一列操作人: %q", w.Body.String())
	}
	for _, p := range []string{"/agents/99999/ledger", "/agents/99999/ledger/export", "/agents/abc/ledger"} {
		if w := f.adminDo("GET", adminBase+p, "", nil); w.Code != 404 {
			t.Errorf("%s 应 404: %d", p, w.Code)
		}
	}
}

// listAll 取 alice 的全部码行（带前缀），给审计测试比对用。
func (f *orderFix) listAll(t *testing.T) []codeRowReply {
	t.Helper()
	_, items := f.listCodes(f.s, "?page_size=200")
	return items
}

// 两个商品各下一单：每张码的 product_code 对，按商品筛选只返回对应的码（把 product 写死或忽略筛选的实现会失败）。
func TestAgentCodesProductCodeAndFilter(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.topUp()
	// 套餐 × 地区唯一，同一地区建不了第二个商品：第二单下完后把订单改成另一个商品编码
	a := f.mustOrder(f.s, orderReq("mw-plus-ph", 2, "", "req-pf-a", 500))
	b := f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-pf-b", 500))
	if _, err := db.DB.Exec(`UPDATE agent_orders SET product_code = 'mw-plus-ph-b' WHERE id = ?`, b.Order.ID); err != nil {
		t.Fatal(err)
	}
	_, all := f.listCodes(f.s, "")
	if len(all) != 3 {
		t.Fatalf("应有 3 张: %+v", all)
	}
	for _, c := range all {
		want := "mw-plus-ph"
		if c.OrderID == b.Order.ID {
			want = "mw-plus-ph-b"
		}
		if c.ProductCode != want {
			t.Fatalf("码 %d product_code=%q 想要 %q", c.ID, c.ProductCode, want)
		}
	}
	if _, items := f.listCodes(f.s, "?product=mw-plus-ph-b"); len(items) != 1 || items[0].ID != b.Codes[0].ID {
		t.Fatalf("按商品 b 筛选: %+v", items)
	}
	if _, items := f.listCodes(f.s, "?product=mw-plus-ph"); len(items) != 2 || items[0].OrderID != a.Order.ID {
		t.Fatalf("按商品 a 筛选: %+v", items)
	}
	_, out, raw := f.exportCodes(f.s, `{"filter":{"product":"mw-plus-ph-b"}}`)
	if len(out) != 1 || out[0].ProductCode != "mw-plus-ph-b" {
		t.Fatalf("导出按商品筛选: %s", raw)
	}
}

// 别人名下（或后台发的）本地码有付款单，不影响 alice 的码列表、状态和概览。
func TestAgentCodesUnaffectedByOthersPayOrders(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	_, bs := f.seedBuyer("bob", 1000)
	mine := f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-sc-a", 500))
	theirs := f.mustOrder(bs, orderReq("mw-plus-ph", 1, "", "req-sc-b", 500))
	adminCode, adminID := f.issueCode("plus", "PH")
	_ = adminCode
	f.insertOrder("cdk", db.LocalCDKSourceRef(theirs.Codes[0].ID), 0, "bob@example.com", "succeeded", "", true)
	f.insertOrder("cdk", db.LocalCDKSourceRef(adminID), 0, "adm@example.com", "succeeded", "", true)
	// 引用到 alice 码 ID 以外的 source_ref 前缀相近的干扰单
	f.insertOrder("cdk", db.LocalCDKSourceRef(mine.Codes[0].ID)+"9", 0, "x@example.com", "succeeded", "", true)

	total, items := f.listCodes(f.s, "")
	if total != 1 || len(items) != 1 || items[0].ID != mine.Codes[0].ID || items[0].Status != "unused" || items[0].Email != nil {
		t.Fatalf("alice 的码不应受别人的付款单影响: %+v", items)
	}
	w := f.agentDo(f.s, "GET", agentBase+"/overview", "")
	if !strings.Contains(w.Body.String(), `"unused":1`) || !strings.Contains(w.Body.String(), `"used":0`) {
		t.Fatalf("概览: %s", w.Body.String())
	}
	_, bitems := f.listCodes(bs, "")
	if len(bitems) != 1 || bitems[0].Status != "used" {
		t.Fatalf("bob 的码应是 used: %+v", bitems)
	}
}

// 概览的「本月」按北京时间：北京 11-01 00:30（UTC 10-31 16:30）算进 11 月；北京 10-31 23:30 不算。
func TestAgentOverviewMonthBoundaryBeijing(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-mb-1", 500))
	f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-mb-2", 500))
	in := time.Date(2026, 11, 1, 0, 30, 0, 0, beijingZone).Unix()
	out := time.Date(2026, 10, 31, 23, 30, 0, 0, beijingZone).Unix()
	var ids []int64
	rows, err := db.DB.Query(`SELECT id FROM agent_ledger WHERE agent_id = ? AND kind = 'purchase' ORDER BY id`, f.aid)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if _, err := db.DB.Exec(`UPDATE agent_ledger SET created_at = ? WHERE id = ?`, in, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE agent_ledger SET created_at = ? WHERE id = ?`, out, ids[1]); err != nil {
		t.Fatal(err)
	}
	w := f.agentDo(f.s, "GET", agentBase+"/overview", "")
	if m := decodeMap(t, w.Body.String()); m["month_order_cents"] != float64(500) {
		t.Fatalf("本月应只含北京 11-01 00:30 的一笔 500: %s", w.Body.String())
	}
}

// 默认窗口是最近 30 天：31 天前的流水不在，29 天前的在。
func TestAgentLedgerDefaultWindow(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-dw-1", 500))
	now := f.clock.Unix()
	day := int64(86400)
	if _, err := db.DB.Exec(`UPDATE agent_ledger SET created_at = ? WHERE agent_id = ? AND kind = 'adjust'`, now-31*day, f.aid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE agent_ledger SET created_at = ? WHERE agent_id = ? AND kind = 'purchase'`, now-29*day, f.aid); err != nil {
		t.Fatal(err)
	}
	_, m := f.ledgerGet(ledgerPath)
	items := m["items"].([]any)
	if m["total"] != float64(1) || len(items) != 1 || items[0].(map[string]any)["kind"] != "purchase" {
		t.Fatalf("默认只含 30 天内的流水: %v", m)
	}
	if m["summary"].(map[string]any)["opening_cents"] != float64(1000) {
		t.Fatalf("31 天前的流水计入期初: %v", m["summary"])
	}
}

// from、to 为负数或极端值：400，不能因为减法溢出而放行。
func TestAgentLedgerNegativeAndOverflowRange(t *testing.T) {
	f, _ := ledgerFix(t)
	for _, q := range []string{
		"?from=-9223372036854775808", "?from=-1", "?to=-1", "?from=-9223372036854775808&to=9223372036854775807",
		"?from=0&to=9223372036854775807", "?from=-9223372036854775808&to=0",
	} {
		code, m := f.ledgerGet(ledgerPath + q)
		if code != 400 {
			t.Errorf("%s 应 400: %d %v", q, code, m)
		}
		w := f.agentDo(f.s, "GET", ledgerPath+"/export"+q, "")
		if w.Code != 400 {
			t.Errorf("导出 %s 应 400: %d", q, w.Code)
		}
	}
}

// CSV 导出把 to 截到当前秒：未来时间的行不会进来，所以导出过程中新插入的行也不会和翻页重叠。
func TestAgentLedgerExportClampsToNow(t *testing.T) {
	f, _ := ledgerFix(t)
	now := f.clock.Unix()
	if _, err := db.DB.Exec(`UPDATE agent_ledger SET created_at = ? WHERE agent_id = ? AND kind = 'purchase'`, now+1000, f.aid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE agent_ledger SET created_at = ? WHERE agent_id = ? AND kind = 'adjust'`, now-1000, f.aid); err != nil {
		t.Fatal(err)
	}
	q := fmt.Sprintf("?from=%d&to=%d", now-86400, now+5000)
	w := f.agentDo(f.s, "GET", ledgerPath+"/export"+q, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), ",下单,") || !strings.Contains(w.Body.String(), ",调账,") {
		t.Fatalf("to 应被截到当前秒，未来的下单行不应导出: %d %q", w.Code, w.Body.String())
	}
}
