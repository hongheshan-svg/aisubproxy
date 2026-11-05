package handler

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

const sharedSetPath = agentBase + "/shared"

// setFix 在 /shared 夹具之上多存一份 alice 的两步验证密钥，用来算动态码。alice 还没有开通对接。
type setFix struct {
	*sharedFix
	secret []byte
}

func newSetFix(t *testing.T, ready bool) *setFix {
	t.Helper()
	t.Setenv("TRUSTED_PROXIES", "")
	e := newAgentEnv(t)
	if ready {
		e.seedRealPayment()
	}
	o := &orderFix{agentEnv: e, tier: e.seedTier("普通")}
	o.pid = o.seedProduct("mw-plus-ph", "plus", "PH")
	o.setPrice(o.pid, 500)
	id, pw, secret := e.seedAgent("alice", o.tier)
	if code, m := adjust(e, id, "seed-alice", `{"amount_cents":1000,"note":"测试充值"}`); code != 200 {
		t.Fatalf("调账: %d %v", code, m)
	}
	o.aid, o.s = id, e.login("alice", pw, secret)
	return &setFix{sharedFix: &sharedFix{orderFix: o}, secret: secret}
}

// totp 把时钟推后 30 秒（登录和上一次用过的时间片不能再用），再算一个当前的动态码。
func (f *setFix) totp() string {
	f.clock = f.clock.Add(30 * time.Second)
	return agent.TOTPCode(f.secret, agent.TOTPStep(f.clock))
}

func (f *setFix) do(method, sub, body string) *httptest.ResponseRecorder {
	return f.agentDo(f.s, method, sharedSetPath+sub, body)
}

func (f *setFix) codeBody() string { return fmt.Sprintf(`{"code":%q}`, f.totp()) }

type keyReply struct {
	AppID  string `json:"app_id"`
	AppKey string `json:"app_key"`
}

// mustKey 生成或重置密钥，要求成功。成功后把凭据写进夹具，/shared 请求就用它签名。
func (f *setFix) mustKey() keyReply {
	f.t.Helper()
	w := f.do("POST", "/key", f.codeBody())
	if w.Code != 200 {
		f.t.Fatalf("生成密钥: %d %s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		f.t.Fatalf("应带 no-store: %q", cc)
	}
	var k keyReply
	if err := json.Unmarshal(w.Body.Bytes(), &k); err != nil {
		f.t.Fatal(err)
	}
	f.appID, f.key = k.AppID, k.AppKey
	return k
}

func (f *setFix) getSettings() map[string]any {
	f.t.Helper()
	w := f.do("GET", "", "")
	if w.Code != 200 {
		f.t.Fatalf("GET /shared: %d %s", w.Code, w.Body.String())
	}
	return decodeMap(f.t, w.Body.String())
}

func (f *setFix) wantErr(w *httptest.ResponseRecorder, status int, code, msg string) {
	f.t.Helper()
	m := decodeMap(f.t, w.Body.String())
	if w.Code != status || m["error"] != code || (msg != "" && m["message"] != msg) {
		f.t.Fatalf("应为 %d %s %q: %d %s", status, code, msg, w.Code, w.Body.String())
	}
}

func TestAgentShared_GetInactive(t *testing.T) {
	f := newSetFix(t, true)
	m := f.getSettings()
	if m["status"] != "inactive" || m["app_id"] != "" || m["min_acg_version"] != "3.6.1" {
		t.Fatalf("%v", m)
	}
	if m["key_invalid"] != false || m["low_balance_cents"] != float64(0) || m["key_created_at"] != float64(0) || m["last_used_at"] != float64(0) {
		t.Fatalf("%v", m)
	}
	if l, ok := m["ip_allowlist"].([]any); !ok || len(l) != 0 {
		t.Fatalf("ip_allowlist 应为 []: %v", m["ip_allowlist"])
	}
}

func TestAgentShared_CreateNeedsTOTP(t *testing.T) {
	f := newSetFix(t, true)
	bad := agent.TOTPCode(f.secret, agent.TOTPStep(f.clock.Add(10*time.Hour)))
	f.wantErr(f.do("POST", "/key", fmt.Sprintf(`{"code":%q}`, bad)), 400, "totp_invalid", agentMsgBadTOTP)
	if c, _ := db.GetAgentSharedCreds(context.Background(), f.aid); c.AppID != "" {
		t.Fatalf("错码不应开通: %+v", c)
	}
	k := f.mustKey()
	if !regexp.MustCompile(`^ag[A-Za-z0-9]{14}$`).MatchString(k.AppID) || !regexp.MustCompile(`^[A-Za-z0-9]{32}$`).MatchString(k.AppKey) {
		t.Fatalf("格式不对: %+v", k)
	}
	w := f.do("GET", "", "")
	m := decodeMap(t, w.Body.String())
	if m["status"] != "active" || m["app_id"] != k.AppID || m["key_invalid"] != false || m["key_created_at"] != float64(f.clock.Unix()) {
		t.Fatalf("%v", m)
	}
	if strings.Contains(w.Body.String(), k.AppKey) {
		t.Fatal("GET /shared 不应含 app_key")
	}
	f.mustShared("authentication/connect", nil)
	audit := auditRows(t)
	if !strings.Contains(audit, "agent:alice|agent_shared_key_create|app_id="+k.AppID) || strings.Contains(audit, k.AppKey) {
		t.Fatalf("审计不对:\n%s", audit)
	}
	if strings.Contains(audit, "agent_shared_key_reset") {
		t.Fatal("首次不应记 reset")
	}
}

func TestAgentShared_ResetInvalidatesOldKey(t *testing.T) {
	f := newSetFix(t, true)
	old := f.mustKey()
	nw := f.mustKey()
	if nw.AppID != old.AppID || nw.AppKey == old.AppKey {
		t.Fatalf("重置应保留商户 ID、换密钥: %+v %+v", old, nw)
	}
	f.sharedRefused("authentication/connect", nil, "密钥错误", signWith(old.AppKey))
	f.mustShared("authentication/connect", nil)
	if audit := auditRows(t); !strings.Contains(audit, "agent:alice|agent_shared_key_reset|app_id="+nw.AppID) || strings.Contains(audit, nw.AppKey) || strings.Contains(audit, old.AppKey) {
		t.Fatalf("审计不对:\n%s", audit)
	}
}

func TestAgentShared_Disable(t *testing.T) {
	f := newSetFix(t, true)
	old := f.mustKey()
	f.wantErr(f.do("POST", "/disable", `{"code":"000000"}`), 400, "totp_invalid", "")
	if m := f.getSettings(); m["status"] != "active" {
		t.Fatalf("错码不应停用: %v", m)
	}
	if w := f.do("POST", "/disable", f.codeBody()); w.Code != 200 || decodeMap(t, w.Body.String())["ok"] != true {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if m := f.getSettings(); m["status"] != "inactive" || m["app_id"] != "" {
		t.Fatalf("%v", m)
	}
	f.sharedRefused("authentication/connect", nil, "商户ID不存在")
	if audit := auditRows(t); !strings.Contains(audit, "agent:alice|agent_shared_disable|app_id="+old.AppID) {
		t.Fatalf("缺审计:\n%s", audit)
	}
	if nw := f.mustKey(); nw.AppID == old.AppID {
		t.Fatalf("停用后再生成应换新商户 ID: %s", nw.AppID)
	}
}

func TestAgentShared_AdminDisabledBlocksCreate(t *testing.T) {
	f := newSetFix(t, true)
	ctx := context.Background()
	if err := db.SetAgentSharedAdminDisabled(ctx, f.aid, true, f.clock); err != nil {
		t.Fatal(err)
	}
	body := f.codeBody()
	f.wantErr(f.do("POST", "/key", body), 403, "shared_admin_disabled", "对接已被管理员关闭")
	if m := f.getSettings(); m["status"] != "admin_disabled" {
		t.Fatalf("%v", m)
	}
	if err := db.SetAgentSharedAdminDisabled(ctx, f.aid, false, f.clock); err != nil {
		t.Fatal(err)
	}
	if w := f.do("POST", "/key", body); w.Code != 200 {
		t.Fatalf("动态码不应被消耗: %d %s", w.Code, w.Body.String())
	}
}

func TestAgentShared_NoMasterKey(t *testing.T) {
	f := newSetFix(t, true)
	f.mustKey()
	t.Setenv("APP_MASTER_KEY", "")
	f.wantErr(f.do("POST", "/key", f.codeBody()), 503, "maintenance", "系统维护中，暂不能生成密钥")
	if m := f.getSettings(); m["status"] != "active" || m["key_invalid"] != true {
		t.Fatalf("%v", m)
	}
}

func TestAgentShared_KeyInvalidWhenSealedByOtherKey(t *testing.T) {
	f := newSetFix(t, true)
	f.mustKey()
	other := make([]byte, 32)
	f.sharedFix.appID = f.getSettings()["app_id"].(string)
	f.storeKey(other, "x")
	if m := f.getSettings(); m["key_invalid"] != true {
		t.Fatalf("主密钥解不开应为 true: %v", m)
	}
	mk, _ := secretbox.LoadKey()
	f.storeKey(mk, "y")
	if m := f.getSettings(); m["key_invalid"] != false {
		t.Fatalf("%v", m)
	}
}

func TestAgentShared_Allowlist(t *testing.T) {
	f := newSetFix(t, true)
	put := func(ips string, code string) *httptest.ResponseRecorder {
		return f.do("PUT", "/allowlist", fmt.Sprintf(`{"code":%q,"ips":%s}`, code, ips))
	}
	stored := func() []string {
		c, _ := db.GetAgentSharedCreds(context.Background(), f.aid)
		return c.IPAllowlist
	}
	w := put(`["1.2.3.4"," 10.0.0.0/8 ","2001:db8::/32","1.2.3.4",""]`, f.totp())
	want := []string{"1.2.3.4", "10.0.0.0/8", "2001:db8::/32"}
	if w.Code != 200 || strings.Join(stored(), ",") != strings.Join(want, ",") {
		t.Fatalf("%d %s %v", w.Code, w.Body.String(), stored())
	}
	if l := decodeMap(t, w.Body.String())["ip_allowlist"].([]any); len(l) != 3 || l[0] != "1.2.3.4" || l[2] != "2001:db8::/32" {
		t.Fatalf("%v", l)
	}
	if audit := auditRows(t); !strings.Contains(audit, "agent:alice|agent_shared_allowlist|count=3") {
		t.Fatalf("缺审计:\n%s", audit)
	}
	if m := f.getSettings(); len(m["ip_allowlist"].([]any)) != 3 {
		t.Fatalf("%v", m)
	}
	f.wantErr(put(`["1.1.1.1","1.1.1.2","1.1.1.3","1.1.1.4","1.1.1.5","1.1.1.6"]`, f.totp()), 400, "invalid_request", "IP 白名单最多 5 项")
	f.wantErr(put(`["abc"]`, f.totp()), 400, "invalid_request", "「abc」不是合法的 IP 或网段")
	f.wantErr(put(`["1.1.1.1/33"]`, f.totp()), 400, "invalid_request", "「1.1.1.1/33」不是合法的 IP 或网段")
	// 错动态码：400 totp_invalid 且不保存
	f.wantErr(put(`["9.9.9.9"]`, "000000"), 400, "totp_invalid", agentMsgBadTOTP)
	if strings.Join(stored(), ",") != strings.Join(want, ",") {
		t.Fatalf("不应被改: %v", stored())
	}
	// 去重按规范化后的字符串；网段先取网络地址；空列表清空
	if w := put(`["10.1.2.3/8","10.0.0.0/8","2001:DB8::1","2001:db8:0::1"]`, f.totp()); w.Code != 200 || strings.Join(stored(), ",") != "10.0.0.0/8,2001:db8::1" {
		t.Fatalf("%d %s %v", w.Code, w.Body.String(), stored())
	}
	if w := put(`[]`, f.totp()); w.Code != 200 || len(stored()) != 0 {
		t.Fatalf("%d %s %v", w.Code, w.Body.String(), stored())
	}
}

func TestAgentShared_AllowlistUnmapsIPv4InIPv6(t *testing.T) {
	f := newSetFix(t, true)
	w := f.do("PUT", "/allowlist", fmt.Sprintf(`{"code":%q,"ips":["::ffff:1.2.3.4","::ffff:10.0.0.0/104"]}`, f.totp()))
	c, _ := db.GetAgentSharedCreds(context.Background(), f.aid)
	if w.Code != 200 || strings.Join(c.IPAllowlist, ",") != "1.2.3.4,10.0.0.0/8" {
		t.Fatalf("%d %s %v", w.Code, w.Body.String(), c.IPAllowlist)
	}
	f.wantErr(f.do("PUT", "/allowlist", fmt.Sprintf(`{"code":%q,"ips":["::ffff:0:0/80"]}`, f.totp())), 400, "invalid_request", "「::ffff:0:0/80」不是合法的 IP 或网段")
}

func TestAgentShared_LowBalance(t *testing.T) {
	f := newSetFix(t, true)
	put := func(v string) *httptest.ResponseRecorder {
		return f.do("PUT", "/low-balance", `{"low_balance_cents":`+v+`}`)
	}
	if w := put("500"); w.Code != 200 || decodeMap(t, w.Body.String())["low_balance_cents"] != float64(500) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if m := f.getSettings(); m["low_balance_cents"] != float64(500) {
		t.Fatalf("%v", m)
	}
	for _, v := range []string{"-1", "1000001"} {
		f.wantErr(put(v), 400, "invalid_request", "提醒线需为 0 到 10000 美元")
	}
	if w := put("1000000"); w.Code != 200 {
		t.Fatalf("上限应可存: %d %s", w.Code, w.Body.String())
	}
	if w := put("0"); w.Code != 200 {
		t.Fatalf("0 应可存: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(auditRows(t), "low") {
		t.Fatal("提醒线不审计")
	}
}

func TestAgentShared_Failures(t *testing.T) {
	f := newSetFix(t, true)
	ctx := context.Background()
	rec := func(no string, at time.Time) {
		t.Helper()
		if err := db.RecordAgentSharedFailure(ctx, db.NewSharedFailure{AgentID: f.aid, RequestNo: no, ProductCode: "mw-plus-ph", Quantity: 2, Reason: "insufficient_balance", Message: "余额不足"}, at); err != nil {
			t.Fatal(err)
		}
	}
	rec("old", f.clock.Add(-31*24*time.Hour))
	rec("r1", f.clock.Add(-2*time.Hour))
	rec("r2", f.clock.Add(-time.Hour))
	w := f.do("GET", "/failures?page=1&page_size=20", "")
	m := decodeMap(t, w.Body.String())
	items, _ := m["items"].([]any)
	if w.Code != 200 || m["total"] != float64(2) || len(items) != 2 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	first, second := items[0].(map[string]any), items[1].(map[string]any)
	if first["request_no"] != "r2" || second["request_no"] != "r1" {
		t.Fatalf("应按 last_at 降序: %v", items)
	}
	for _, k := range []string{"id", "request_no", "product_code", "quantity", "reason", "message", "attempts", "first_at", "last_at", "resolved_order_id"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("缺字段 %s: %v", k, first)
		}
	}
	if first["quantity"] != float64(2) || first["attempts"] != float64(1) || first["resolved_order_id"] != nil || first["reason"] != "insufficient_balance" {
		t.Fatalf("%v", first)
	}
	// 分页
	m = decodeMap(t, f.do("GET", "/failures?page=2&page_size=1", "").Body.String())
	if its := m["items"].([]any); len(its) != 1 || its[0].(map[string]any)["request_no"] != "r1" || m["total"] != float64(2) {
		t.Fatalf("%v", m)
	}
}

func TestAgentOverview_SharedFields(t *testing.T) {
	f := newSetFix(t, true)
	ctx := context.Background()
	rec := func(no, reason string, at time.Time) {
		t.Helper()
		if err := db.RecordAgentSharedFailure(ctx, db.NewSharedFailure{AgentID: f.aid, RequestNo: no, ProductCode: "p", Quantity: 1, Reason: reason, Message: "m"}, at); err != nil {
			t.Fatal(err)
		}
	}
	rec("a", "insufficient_balance", f.clock.Add(-time.Hour))
	rec("b", "insufficient_balance", f.clock.Add(-2*time.Hour))
	rec("c", "busy", f.clock.Add(-3*time.Hour))
	rec("d", "busy", f.clock.Add(-4*time.Hour))
	rec("old", "insufficient_balance", f.clock.Add(-25*time.Hour))
	if err := db.ResolveAgentSharedFailure(ctx, f.aid, "d", 1); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAgentSharedLowBalance(ctx, f.aid, 2000, f.clock); err != nil {
		t.Fatal(err)
	}
	ov := func() map[string]any {
		w := f.agentDo(f.s, "GET", agentBase+"/overview", "")
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		return decodeMap(t, w.Body.String())
	}
	m := ov()
	sf := m["shared_failures_24h"].(map[string]any)
	if sf["total"] != float64(3) || sf["insufficient_balance"] != float64(2) {
		t.Fatalf("%v", sf)
	}
	lb := m["low_balance"].(map[string]any)
	if lb["threshold_cents"] != float64(2000) || lb["below"] != true {
		t.Fatalf("余额 1000 低于 2000: %v", lb)
	}
	if err := db.SetAgentSharedLowBalance(ctx, f.aid, 0, f.clock); err != nil {
		t.Fatal(err)
	}
	if lb = ov()["low_balance"].(map[string]any); lb["threshold_cents"] != float64(0) || lb["below"] != false {
		t.Fatalf("提醒线 0 为关闭: %v", lb)
	}
	if err := db.SetAgentSharedLowBalance(ctx, f.aid, 1000, f.clock); err != nil {
		t.Fatal(err)
	}
	if lb = ov()["low_balance"].(map[string]any); lb["below"] != false {
		t.Fatalf("余额等于提醒线不算低: %v", lb)
	}
}

// sourceFix 造一笔后台单、一笔异次元单。
func sourceFix(t *testing.T) (*setFix, int64, int64) {
	f := newSetFix(t, true)
	mk, err := secretbox.LoadKey()
	if err != nil {
		t.Fatal(err)
	}
	f.appID, f.key = sharedFixAppID, sharedFixKey
	f.storeKey(mk, f.key)
	web := f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-web-1", 500)).Order.ID
	f.mustTrade(tradeFields(tradeNo1, "mw-plus-ph", "1"))
	var acg int64
	if err := db.DB.QueryRow(`SELECT id FROM agent_orders WHERE agent_id = ? AND source = 'acg'`, f.aid).Scan(&acg); err != nil {
		t.Fatal(err)
	}
	return f, web, acg
}

func TestAgentViews_Source(t *testing.T) {
	f, web, acg := sourceFix(t)
	want := map[int64][2]string{web: {"web", ""}, acg: {"acg", tradeNo1}}
	// 订单列表
	m := decodeMap(t, f.agentDo(f.s, "GET", agentBase+"/orders", "").Body.String())
	items := m["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("%v", m)
	}
	for _, it := range items {
		o := it.(map[string]any)
		w := want[int64(o["id"].(float64))]
		if o["source"] != w[0] || o["external_ref"] != w[1] {
			t.Fatalf("订单来源不对: %v", o)
		}
	}
	// 订单详情
	w := f.agentDo(f.s, "GET", fmt.Sprintf("%s/orders/%d", agentBase, acg), "")
	if o := decodeMap(t, w.Body.String())["order"].(map[string]any); o["source"] != "acg" || o["external_ref"] != tradeNo1 {
		t.Fatalf("%v", o)
	}
	// 码列表和码导出
	checkCodes := func(items []any) {
		t.Helper()
		if len(items) != 2 {
			t.Fatalf("应有 2 张码: %v", items)
		}
		for _, it := range items {
			c := it.(map[string]any)
			w := want[int64(c["order_id"].(float64))]
			if c["source"] != w[0] || c["external_ref"] != w[1] {
				t.Fatalf("码来源不对: %v", c)
			}
		}
	}
	checkCodes(decodeMap(t, f.agentDo(f.s, "GET", codesPath, "").Body.String())["items"].([]any))
	checkCodes(decodeMap(t, f.agentDo(f.s, "POST", codesPath+"/export", `{"filter":{}}`).Body.String())["codes"].([]any))
	// 流水
	m = decodeMap(t, f.agentDo(f.s, "GET", ledgerPath, "").Body.String())
	n := 0
	for _, it := range m["items"].([]any) {
		r := it.(map[string]any)
		switch r["kind"] {
		case "purchase":
			w := want[int64(r["order_id"].(float64))]
			if r["source"] != w[0] || r["external_ref"] != w[1] {
				t.Fatalf("下单行来源不对: %v", r)
			}
			n++
		default:
			if r["source"] != "" || r["external_ref"] != "" {
				t.Fatalf("调账行应为空串: %v", r)
			}
			if s, ok := r["source"]; !ok || s == nil {
				t.Fatalf("source 字段应存在: %v", r)
			}
		}
	}
	if n != 2 {
		t.Fatalf("应有 2 条下单流水: %v", m)
	}
	// 管理员看订单、流水同样有来源
	am := decodeMap(t, f.adminDo("GET", fmt.Sprintf("%s/agents/%d/orders", adminBase, f.aid), "", nil).Body.String())
	for _, it := range am["items"].([]any) {
		o := it.(map[string]any)
		w := want[int64(o["id"].(float64))]
		if o["source"] != w[0] || o["external_ref"] != w[1] {
			t.Fatalf("管理员订单来源不对: %v", o)
		}
	}
}

func readCSV(t *testing.T, body string) [][]string {
	t.Helper()
	body = strings.TrimPrefix(body, "\xEF\xBB\xBF")
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestAgentLedgerExport_SourceColumns(t *testing.T) {
	f, web, acg := sourceFix(t)
	f.clock = f.clock.Add(5 * time.Second) // 导出截到当前秒（右开），刚写的流水要过一秒才进范围
	w := f.agentDo(f.s, "GET", ledgerPath+"/export", "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	rows := readCSV(t, w.Body.String())
	if strings.Join(rows[0], ",") != "时间,类型,金额,变动后余额,订单号,备注,来源,对方订单号" {
		t.Fatalf("表头: %v", rows[0])
	}
	got := map[string][2]string{}
	for _, r := range rows[1:] {
		got[r[1]+"/"+r[4]] = [2]string{r[6], r[7]}
	}
	if got[fmt.Sprintf("下单/%d", acg)] != [2]string{"异次元", tradeNo1} || got[fmt.Sprintf("下单/%d", web)] != [2]string{"后台", ""} || got["调账/"] != [2]string{"", ""} {
		t.Fatalf("来源列不对: %v", got)
	}
	aw := f.adminDo("GET", fmt.Sprintf("%s/agents/%d/ledger/export", adminBase, f.aid), "", nil)
	ar := readCSV(t, aw.Body.String())
	if strings.Join(ar[0], ",") != "时间,类型,金额,变动后余额,订单号,备注,操作人,来源,对方订单号" {
		t.Fatalf("管理员表头: %v", ar[0])
	}
	for _, r := range ar[1:] {
		if r[1] == "下单" && r[4] == fmt.Sprint(acg) && (r[7] != "异次元" || r[8] != tradeNo1) {
			t.Fatalf("管理员版来源列不对: %v", r)
		}
	}
}

func TestPruneSharedFailures(t *testing.T) {
	f := newSetFix(t, true)
	ctx := context.Background()
	now := f.clock
	for no, age := range map[string]time.Duration{"old": 31 * 24 * time.Hour, "new": 29 * 24 * time.Hour} {
		if err := db.RecordAgentSharedFailure(ctx, db.NewSharedFailure{AgentID: f.aid, RequestNo: no, ProductCode: "p", Quantity: 1, Reason: "busy", Message: "m"}, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	runOpsMaintenance(now)
	list, _, err := db.ListAgentSharedFailures(ctx, f.aid, 0, 10, 0)
	if err != nil || len(list) != 1 || list[0].RequestNo != "new" {
		t.Fatalf("%+v %v", list, err)
	}
}
