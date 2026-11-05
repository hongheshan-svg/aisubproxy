package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const orderPath = agentBase + "/orders"

type orderCode struct {
	ID          int64  `json:"id"`
	Code        string `json:"code"`
	Unavailable bool   `json:"unavailable"`
}

type orderView struct {
	ID             int64  `json:"id"`
	ProductCode    string `json:"product_code"`
	ProductName    string `json:"product_name"`
	Plan           string `json:"plan"`
	Region         string `json:"region"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	TotalCents     int64  `json:"total_cents"`
	RequestNo      string `json:"request_no"`
	Note           string `json:"note"`
	CreatedAt      int64  `json:"created_at"`
}

type orderReply struct {
	Order                 orderView   `json:"order"`
	Codes                 []orderCode `json:"codes"`
	Replayed              bool        `json:"replayed"`
	Error                 string      `json:"error"`
	Message               string      `json:"message"`
	ShortfallCents        int64       `json:"shortfall_cents"`
	CurrentUnitPriceCents int64       `json:"current_unit_price_cents"`
}

// orderFix 是下单测试的现成环境：等级「普通」、商品 mw-plus-ph（等级价 500 美分）、代理商 alice（余额 1000 美分，已登录）。
type orderFix struct {
	*agentEnv
	tier, pid, aid int64
	s              *agentSession
}

// newOrderFix ready 为 true 时先写一笔真实付款，让 plus/PH 能出码。
func newOrderFix(t *testing.T, e *agentEnv, ready bool) *orderFix {
	t.Helper()
	if ready {
		e.seedRealPayment()
	}
	f := &orderFix{agentEnv: e, tier: e.seedTier("普通")}
	f.pid = f.seedProduct("mw-plus-ph", "plus", "PH")
	f.setPrice(f.pid, 500)
	f.aid, f.s = f.seedBuyer("alice", 1000)
	return f
}

func (f *orderFix) seedProduct(code, plan, region string) int64 {
	f.t.Helper()
	w := f.adminDo("POST", adminBase+"/agent-products", fmt.Sprintf(`{"product_code":%q,"name":"商品 %s","plan":%q,"region":%q,"sort":0}`, code, code, plan, region), nil)
	if w.Code != 200 {
		f.t.Fatalf("建商品: %d %s", w.Code, w.Body.String())
	}
	return int64(decodeMap(f.t, w.Body.String())["product"].(map[string]any)["id"].(float64))
}

// setPrice 改等级价，0 表示这个等级不卖。
func (f *orderFix) setPrice(pid, cents int64) {
	f.t.Helper()
	w := f.adminDo("PUT", adminBase+"/agent-prices", fmt.Sprintf(`{"cells":[{"tier_id":%d,"product_id":%d,"price_cents":%d}]}`, f.tier, pid, cents), nil)
	if w.Code != 200 {
		f.t.Fatalf("改价: %d %s", w.Code, w.Body.String())
	}
}

func (f *orderFix) setEnabled(pid int64, enabled bool) {
	f.t.Helper()
	w := f.adminDo("PUT", fmt.Sprintf("%s/agent-products/%d", adminBase, pid), fmt.Sprintf(`{"name":"下架测试","enabled":%v,"sort":0}`, enabled), nil)
	if w.Code != 200 {
		f.t.Fatalf("上下架: %d %s", w.Code, w.Body.String())
	}
}

// seedBuyer 建一个同等级的代理商，调账加余额，然后登录（登录会把时钟推后 30 秒）。
func (f *orderFix) seedBuyer(name string, cents int64) (int64, *agentSession) {
	f.t.Helper()
	id, pw, secret := f.seedAgent(name, f.tier)
	if cents > 0 {
		if code, m := adjust(f.agentEnv, id, "seed-"+name, fmt.Sprintf(`{"amount_cents":%d,"note":"测试充值"}`, cents)); code != 200 {
			f.t.Fatalf("调账: %d %v", code, m)
		}
	}
	return id, f.login(name, pw, secret)
}

func orderReq(product string, qty any, note, reqNo string, expected int64) map[string]any {
	return map[string]any{"product_code": product, "quantity": qty, "note": note, "request_no": reqNo, "expected_unit_price_cents": expected}
}

// post 发下单请求。可以在 goroutine 里调用（不调 t.Fatal）。
func (f *orderFix) post(s *agentSession, req map[string]any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(req)
	return f.agentDo(s, "POST", orderPath, string(b))
}

func readOrder(t *testing.T, w *httptest.ResponseRecorder) orderReply {
	t.Helper()
	var r orderReply
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("响应不是 JSON: %d %s", w.Code, w.Body.String())
	}
	return r
}

// mustOrder 下单并要求 200。
func (f *orderFix) mustOrder(s *agentSession, req map[string]any) orderReply {
	f.t.Helper()
	w := f.post(s, req)
	if w.Code != 200 {
		f.t.Fatalf("下单应成功: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		f.t.Fatalf("下单响应应带 no-store: %q", w.Header().Get("Cache-Control"))
	}
	return readOrder(f.t, w)
}

// expectRefused 要求返回指定的状态和错误编号，并且余额、流水、订单、码都没变。
func (f *orderFix) expectRefused(s *agentSession, id int64, req map[string]any, status int, errCode string) orderReply {
	f.t.Helper()
	bal, led, ords, cdks := balanceOf(f.t, id), countRows(f.t, `SELECT COUNT(*) FROM agent_ledger WHERE agent_id = ?`, id),
		countRows(f.t, `SELECT COUNT(*) FROM agent_orders WHERE agent_id = ?`, id), countRows(f.t, `SELECT COUNT(*) FROM local_cdks WHERE agent_id = ?`, id)
	w := f.post(s, req)
	r := readOrder(f.t, w)
	if w.Code != status || r.Error != errCode || r.Message == "" {
		f.t.Fatalf("%v 应 %d %s: %d %s", req, status, errCode, w.Code, w.Body.String())
	}
	if len(r.Codes) != 0 {
		f.t.Fatalf("失败响应不应带码: %s", w.Body.String())
	}
	if b := balanceOf(f.t, id); b != bal {
		f.t.Fatalf("余额变了: %d -> %d", bal, b)
	}
	if n := countRows(f.t, `SELECT COUNT(*) FROM agent_ledger WHERE agent_id = ?`, id); n != led {
		f.t.Fatalf("流水变了: %d -> %d", led, n)
	}
	if n := countRows(f.t, `SELECT COUNT(*) FROM agent_orders WHERE agent_id = ?`, id); n != ords {
		f.t.Fatalf("订单变了: %d -> %d", ords, n)
	}
	if n := countRows(f.t, `SELECT COUNT(*) FROM local_cdks WHERE agent_id = ?`, id); n != cdks {
		f.t.Fatalf("码变了: %d -> %d", cdks, n)
	}
	return r
}

func countRows(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *localEnv) alertList() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.alerts...)
}

func sameCodes(a, b []orderCode) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func auditRows(t *testing.T) string {
	t.Helper()
	rows, err := db.DB.Query(`SELECT username || '|' || action || '|' || COALESCE(detail, '') FROM admin_audit_logs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		sb.WriteString(s + "\n")
	}
	return sb.String()
}

func TestAgentOrderSuccess(t *testing.T) {
	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	f := newOrderFix(t, newAgentEnv(t), true)

	// 商品列表：价格和能否出码
	w := f.agentDo(f.s, "GET", agentBase+"/products", "")
	if w.Code != 200 {
		t.Fatalf("商品列表: %d %s", w.Code, w.Body.String())
	}
	items := decodeMap(t, w.Body.String())["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("应有 1 个商品: %s", w.Body.String())
	}
	it := items[0].(map[string]any)
	if it["product_code"] != "mw-plus-ph" || it["plan"] != "plus" || it["region"] != "PH" || it["unit_price_cents"] != float64(500) || it["ready"] != true || it["name"] == "" {
		t.Fatalf("商品项不对: %v", it)
	}

	r := f.mustOrder(f.s, orderReq("mw-plus-ph", 2, "给客户 A", "req-success-1", 500))
	if r.Replayed || len(r.Codes) != 2 {
		t.Fatalf("应新建并有 2 张码: %+v", r)
	}
	o := r.Order
	if o.ID <= 0 || o.ProductCode != "mw-plus-ph" || o.Plan != "plus" || o.Region != "PH" || o.Quantity != 2 ||
		o.UnitPriceCents != 500 || o.TotalCents != 1000 || o.RequestNo != "req-success-1" || o.Note != "给客户 A" || o.CreatedAt == 0 || o.ProductName == "" {
		t.Fatalf("订单对象不对: %+v", o)
	}
	if b := balanceOf(t, f.aid); b != 0 {
		t.Fatalf("余额应为 0: %d", b)
	}
	ctx := context.Background()
	for _, c := range r.Codes {
		if !strings.HasPrefix(c.Code, "MW-") || c.Unavailable || c.ID <= 0 {
			t.Fatalf("码不对: %+v", c)
		}
		row, err := db.GetLocalCDKByCode(ctx, c.Code)
		if err != nil {
			t.Fatalf("按码查不到: %v", err)
		}
		if row.ID != c.ID || row.AgentID != f.aid || row.AgentOrderID != o.ID || row.IssuedBy != "agent:alice" || row.Note != "给客户 A" ||
			row.Provider != "marswallex" || row.Product != "gpt" || row.PlanKey != "plus" || row.Region != "PH" {
			t.Fatalf("码的行不对: %+v", row)
		}
	}
	if r.Codes[0].Code == r.Codes[1].Code {
		t.Fatal("两张码不应相同")
	}
	if n := countRows(t, `SELECT COUNT(*) FROM agent_ledger WHERE agent_id = ? AND kind = 'purchase' AND order_id = ? AND amount_cents = -1000`, f.aid, o.ID); n != 1 {
		t.Fatalf("应有 1 条下单流水: %d", n)
	}

	audit := auditRows(t)
	if !strings.Contains(audit, fmt.Sprintf("agent:alice|agent_order|product=mw-plus-ph quantity=2 total_cents=1000 order=%d", o.ID)) {
		t.Fatalf("缺下单审计:\n%s", audit)
	}
	for _, c := range r.Codes {
		if strings.Contains(audit, c.Code) || strings.Contains(buf.String(), c.Code) {
			t.Fatalf("审计或日志里出现了完整码")
		}
	}
}

func TestAgentOrderReplaySameRequestNo(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	req := orderReq("mw-plus-ph", 2, "第一次", "req-replay-1", 500)
	first := f.mustOrder(f.s, req)
	led := countRows(t, `SELECT COUNT(*) FROM agent_ledger WHERE agent_id = ?`, f.aid)

	// 备注不参与比较
	req["note"] = "第二次"
	again := f.mustOrder(f.s, req)
	if !again.Replayed || again.Order.ID != first.Order.ID || again.Order.Note != "第一次" {
		t.Fatalf("应重放原订单: %+v", again)
	}
	if !sameCodes(first.Codes, again.Codes) {
		t.Fatalf("重放的码应和第一次相同: %+v vs %+v", first.Codes, again.Codes)
	}
	if b := balanceOf(t, f.aid); b != 0 {
		t.Fatalf("不应再扣钱: %d", b)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM agent_ledger WHERE agent_id = ?`, f.aid); n != led {
		t.Fatalf("不应多出流水: %d -> %d", led, n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM agent_orders WHERE agent_id = ?`, f.aid); n != 1 {
		t.Fatalf("应只有 1 笔订单: %d", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM local_cdks WHERE agent_id = ?`, f.aid); n != 2 {
		t.Fatalf("应只有 2 张码: %d", n)
	}
}

func TestAgentOrderReplayAfterProductDisabled(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	req := orderReq("mw-plus-ph", 2, "", "req-disabled-1", 500)
	first := f.mustOrder(f.s, req)

	f.setEnabled(f.pid, false)
	f.setPrice(f.pid, 600)
	again := f.mustOrder(f.s, req)
	if !again.Replayed || again.Order.ID != first.Order.ID || again.Order.UnitPriceCents != 500 || !sameCodes(first.Codes, again.Codes) {
		t.Fatalf("下架、改价后同一个请求号应重放原订单: %+v", again)
	}
	// 换一个请求号就是新订单，会被拦下
	f.expectRefused(f.s, f.aid, orderReq("mw-plus-ph", 1, "", "req-disabled-2", 600), 404, "product_unavailable")
}

func TestAgentOrderRequestNoConflict(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-conflict-1", 500))
	r := f.expectRefused(f.s, f.aid, orderReq("mw-plus-ph", 2, "", "req-conflict-1", 500), 409, "request_no_conflict")
	if r.Message != "请求号已用于另一笔订单" {
		t.Fatalf("提示不对: %q", r.Message)
	}
	// 商品编码不同也冲突；请求号先于商品检查，所以不存在的商品也报冲突
	f.expectRefused(f.s, f.aid, orderReq("mw-other", 1, "", "req-conflict-1", 500), 409, "request_no_conflict")
}

func TestAgentOrderPriceChanged(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.setPrice(f.pid, 600)
	r := f.expectRefused(f.s, f.aid, orderReq("mw-plus-ph", 1, "", "req-price-1", 500), 409, "price_changed")
	if r.CurrentUnitPriceCents != 600 || r.Message != "价格已变化，请刷新后重新下单" {
		t.Fatalf("应附当前价: %+v", r)
	}
	if b := balanceOf(t, f.aid); b != 1000 {
		t.Fatalf("余额不应变: %d", b)
	}
}

func TestAgentOrderInsufficient(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	r := f.expectRefused(f.s, f.aid, orderReq("mw-plus-ph", 3, "", "req-poor-1", 500), 409, "insufficient_balance")
	if r.ShortfallCents != 500 || !strings.Contains(r.Message, "$5.00") {
		t.Fatalf("应附还差多少: %+v", r)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM agent_orders`); n != 0 {
		t.Fatalf("不应有订单: %d", n)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM local_cdks`); n != 0 {
		t.Fatalf("不应有码: %d", n)
	}
}

func TestAgentOrderBoundaries(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	// message 要说明是哪一项
	cases := []struct {
		req  map[string]any
		want string
	}{
		{orderReq("mw-plus-ph", 0, "", "req-b-1", 500), "数量"},
		{orderReq("mw-plus-ph", 201, "", "req-b-2", 500), "数量"},
		{orderReq("mw-plus-ph", 1.5, "", "req-b-3", 500), "数量"},
		{orderReq("mw-plus-ph", -1, "", "req-b-4", 500), "数量"},
		{orderReq("mw-plus-ph", "1", "", "req-b-5", 500), "数量"},
		{orderReq("mw-plus-ph", 1, strings.Repeat("字", 201), "req-b-6", 500), "备注"},
		{orderReq("mw-plus-ph", 1, "", "a b", 500), "请求号"},
		{orderReq("mw-plus-ph", 1, "", strings.Repeat("a", 65), 500), "请求号"},
		{orderReq("mw-plus-ph", 1, "", "", 500), "请求号"},
		{orderReq("", 1, "", "req-b-7", 500), "商品"},
		{orderReq("mw-plus-ph", 1, "", "req-b-8", 0), "单价"},
	}
	for _, tc := range cases {
		if r := f.expectRefused(f.s, f.aid, tc.req, 400, "invalid_request"); !strings.Contains(r.Message, tc.want) {
			t.Fatalf("%v 的提示应说明%s: %q", tc.req, tc.want, r.Message)
		}
	}
	w := f.agentDo(f.s, "POST", orderPath, `{bad`)
	if w.Code != 400 || jsonField(t, w.Body.String(), "error") != "invalid_request" {
		t.Fatalf("坏 JSON 应 400: %d %s", w.Code, w.Body.String())
	}
	if b := balanceOf(t, f.aid); b != 1000 {
		t.Fatalf("余额不应变: %d", b)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM agent_ledger WHERE agent_id = ?`, f.aid); n != 1 {
		t.Fatalf("流水应只有调账 1 条: %d", n)
	}
	// 恰好在边界上的能下单
	r := f.mustOrder(f.s, orderReq("mw-plus-ph", 1, strings.Repeat("字", 200), strings.Repeat("Az09_.:-", 8), 500))
	if r.Order.Quantity != 1 || len(r.Codes) != 1 {
		t.Fatalf("边界值应能下单: %+v", r)
	}
}

func TestAgentOrderNotReady(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), false)
	w := f.agentDo(f.s, "GET", agentBase+"/products", "")
	items := decodeMap(t, w.Body.String())["items"].([]any)
	if w.Code != 200 || len(items) != 1 || items[0].(map[string]any)["ready"] != false {
		t.Fatalf("没有真实付款时 ready 应为 false: %d %s", w.Code, w.Body.String())
	}
	f.expectRefused(f.s, f.aid, orderReq("mw-plus-ph", 1, "", "req-notready-1", 500), 409, "product_not_ready")
}

func TestAgentOrderNoMasterKey(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	before := len(f.alertList())
	t.Setenv("APP_MASTER_KEY", "")
	r := f.expectRefused(f.s, f.aid, orderReq("mw-plus-ph", 1, "", "req-nokey-1", 500), 503, "maintenance")
	if r.Message != "系统维护中，暂不能下单" {
		t.Fatalf("提示不对: %q", r.Message)
	}
	f.expectRefused(f.s, f.aid, orderReq("mw-plus-ph", 1, "", "req-nokey-2", 500), 503, "maintenance")
	alerts := f.alertList()[before:]
	if len(alerts) != 1 || !strings.Contains(alerts[0], "APP_MASTER_KEY") {
		t.Fatalf("应有 1 条告警: %v", alerts)
	}
}

func TestAgentOrderUnpricedProduct(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	f.seedProduct("mw-plus-us", "plus", "US") // 没定价
	off := f.seedProduct("mw-pro-20x-us", "pro_20x", "US")
	f.setPrice(off, 800)
	f.setEnabled(off, false) // 定了价但下架

	w := f.agentDo(f.s, "GET", agentBase+"/products", "")
	items := decodeMap(t, w.Body.String())["items"].([]any)
	if w.Code != 200 || len(items) != 1 || items[0].(map[string]any)["product_code"] != "mw-plus-ph" {
		t.Fatalf("只应列出能买的商品: %d %s", w.Code, w.Body.String())
	}
	r := f.expectRefused(f.s, f.aid, orderReq("mw-plus-us", 1, "", "req-unpriced-1", 500), 404, "product_unavailable")
	if r.Message != "该商品暂不可售" {
		t.Fatalf("提示不对: %q", r.Message)
	}
	f.expectRefused(f.s, f.aid, orderReq("mw-pro-20x-us", 1, "", "req-unpriced-2", 800), 404, "product_unavailable")
	f.expectRefused(f.s, f.aid, orderReq("no-such-product", 1, "", "req-unpriced-3", 500), 404, "product_unavailable")
}

// postConcurrently 让 n 个 goroutine 同时发同一个下单请求。
func (f *orderFix) postConcurrently(n int, req map[string]any) []*httptest.ResponseRecorder {
	res := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res[i] = f.post(f.s, req)
		}(i)
	}
	close(start)
	wg.Wait()
	return res
}

// checkOneOrder 要求同一个请求号的这些响应全部 200、码完全相同、恰好 1 个新建，并且只扣了一次钱、只有一笔订单。
func (f *orderFix) checkOneOrder(res []*httptest.ResponseRecorder, qty int, total int64) {
	t := f.t
	t.Helper()
	var first []orderCode
	fresh := 0
	for i, w := range res {
		if w.Code != 200 {
			t.Fatalf("第 %d 个请求: %d %s", i, w.Code, w.Body.String())
		}
		r := readOrder(t, w)
		if !r.Replayed {
			fresh++
		}
		if len(r.Codes) != qty {
			t.Fatalf("第 %d 个请求的码: %+v", i, r.Codes)
		}
		if first == nil {
			first = r.Codes
		} else if !sameCodes(first, r.Codes) {
			t.Fatalf("各请求拿到的码不同: %+v vs %+v", first, r.Codes)
		}
	}
	if fresh != 1 {
		t.Fatalf("应恰好 1 个请求新建订单: %d", fresh)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM agent_ledger WHERE kind = 'purchase' AND agent_id = ? AND amount_cents = ?`, f.aid, -total); n != 1 {
		t.Fatalf("应只扣一次: %d 条下单流水", n)
	}
	if c := countRows(t, `SELECT COUNT(*) FROM agent_orders`); c != 1 {
		t.Fatalf("应只有 1 笔订单: %d", c)
	}
	if c := countRows(t, `SELECT COUNT(*) FROM local_cdks WHERE agent_id = ?`, f.aid); c != qty {
		t.Fatalf("应只有 %d 张码: %d", qty, c)
	}
}

// agentOrderLockRefs 这个「代理商 + 请求号」的锁现在有几个持有者和等待者。
func agentOrderLockRefs(agentID int64, requestNo string) int {
	agentOrderLocksMu.Lock()
	defer agentOrderLocksMu.Unlock()
	if l := agentOrderLocks[agentOrderLockKey(agentID, requestNo)]; l != nil {
		return l.refs
	}
	return 0
}

func agentOrderLockCount() int {
	agentOrderLocksMu.Lock()
	defer agentOrderLocksMu.Unlock()
	return len(agentOrderLocks)
}

// 同一进程里同号请求按锁串行：只有第一个建单，其余都在第 4 步查到订单后重放，不会撞上唯一约束。
func TestAgentOrderConcurrentSameRequestNo(t *testing.T) {
	f := newOrderFix(t, newAgentEnvFileDB(t), true)
	var collided atomic.Int32
	agentOrderCollided = func() { collided.Add(1) }
	t.Cleanup(func() { agentOrderCollided = nil })

	f.checkOneOrder(f.postConcurrently(8, orderReq("mw-plus-ph", 2, "", "req-concurrent-1", 500)), 2, 1000)
	if n := collided.Load(); n != 0 {
		t.Fatalf("进程内同号请求应排队，不应撞唯一约束: %d 次", n)
	}
	if b := balanceOf(t, f.aid); b != 0 {
		t.Fatalf("只应扣一次: 余额 %d", b)
	}
	if n := agentOrderLockCount(); n != 0 {
		t.Fatalf("锁用完应回收: 还剩 %d 个", n)
	}
}

// 锁不共享时（多进程）的兜底：8 个请求都过了第 5 步才一起放行，必然都没在第 4 步查到订单；
// 只有 1 个建单成功，其余 7 个撞上唯一约束后回到第 4 步重放同一批码。
func TestAgentOrderConcurrentCollisionFallback(t *testing.T) {
	f := newOrderFix(t, newAgentEnvFileDB(t), true)
	const n = 8
	oldLock := agentOrderLock
	agentOrderLock = func(context.Context, int64, string) (func(), bool) { return func() {}, true }
	gate := make(chan struct{})
	var arrived, collided atomic.Int32
	agentOrderPausePoint = func() {
		if arrived.Add(1) == n {
			close(gate)
		}
		select {
		case <-gate:
		case <-time.After(10 * time.Second): // 有请求没走到这里时不卡死，由下面的断言报错
		}
	}
	agentOrderCollided = func() { collided.Add(1) }
	t.Cleanup(func() { agentOrderLock, agentOrderPausePoint, agentOrderCollided = oldLock, nil, nil })

	f.checkOneOrder(f.postConcurrently(n, orderReq("mw-plus-ph", 2, "", "req-collide-1", 500)), 2, 1000)
	if got := collided.Load(); got != n-1 {
		t.Fatalf("应有 %d 个请求撞上唯一约束后重放: %d", n-1, got)
	}
	if b := balanceOf(t, f.aid); b != 0 {
		t.Fatalf("只应扣一次: 余额 %d", b)
	}
}

// 请求 A 已过第 5 步还没提交时，管理员改了价；同号重试 B 要等 A 提交后拿到重放，而不是 price_changed。
func TestAgentOrderSameRequestNoWaitsForInflight(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	const no = "req-inflight-1"
	req := orderReq("mw-plus-ph", 2, "", no, 500)
	reached, proceed := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(proceed) })
	var first atomic.Bool
	agentOrderPausePoint = func() {
		if first.CompareAndSwap(false, true) {
			close(reached)
			<-proceed
		}
	}
	t.Cleanup(func() { release(); agentOrderPausePoint = nil })

	var wa, wb *httptest.ResponseRecorder
	doneA, doneB := make(chan struct{}), make(chan struct{})
	go func() { defer close(doneA); wa = f.post(f.s, req) }()
	select {
	case <-reached:
	case <-doneA:
		t.Fatalf("A 没停在第 5 步之后: %d %s", wa.Code, wa.Body.String())
	case <-time.After(10 * time.Second):
		t.Fatal("A 没走到第 5 步之后")
	}
	f.setPrice(f.pid, 600) // A 已经按 500 通过检查
	go func() { defer close(doneB); wb = f.post(f.s, req) }()
	deadline := time.Now().Add(10 * time.Second)
	for agentOrderLockRefs(f.aid, no) < 2 {
		select {
		case <-doneB:
			release()
			<-doneA
			t.Fatalf("A 还没提交，同号的 B 就返回了: %d %s", wb.Code, wb.Body.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("B 没有在等同号的锁")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	<-doneA
	<-doneB
	if wa.Code != 200 || wb.Code != 200 {
		t.Fatalf("A、B 都应 200: A %d %s / B %d %s", wa.Code, wa.Body.String(), wb.Code, wb.Body.String())
	}
	ra, rb := readOrder(t, wa), readOrder(t, wb)
	if ra.Replayed || !rb.Replayed || rb.Order.ID != ra.Order.ID || rb.Order.UnitPriceCents != 500 || !sameCodes(ra.Codes, rb.Codes) {
		t.Fatalf("B 应重放 A 的订单: A %+v / B %+v", ra, rb)
	}
	if b := balanceOf(t, f.aid); b != 0 {
		t.Fatalf("只应扣一次: 余额 %d", b)
	}
	if n := agentOrderLockCount(); n != 0 {
		t.Fatalf("锁用完应回收: 还剩 %d 个", n)
	}
}

// 商品列表和下单的就绪检查走限频的钱包同步，代理商不能带着打 MarsWallex 的账户接口。
func TestAgentOrderWalletSyncThrottled(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	other := f.seedProduct("mw-pro-20x-us", "pro_20x", "US")
	f.setPrice(other, 800)
	resetWalletSyncThrottle()
	base := len(f.mw.Requests("GET", mwPathAccounts))
	for i := 0; i < 2; i++ {
		w := f.agentDo(f.s, "GET", agentBase+"/products", "")
		if w.Code != 200 || len(decodeMap(t, w.Body.String())["items"].([]any)) != 2 {
			t.Fatalf("商品列表: %d %s", w.Code, w.Body.String())
		}
	}
	f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-sync-1", 500))
	if n := len(f.mw.Requests("GET", mwPathAccounts)) - base; n > 1 {
		t.Fatalf("两次商品列表（2 个组合）加一次下单，上游钱包最多同步 1 次: %d", n)
	}
}

// 导出时主密钥不可用：503 maintenance，日志里「主密钥不可用」只出现一次。
func TestAgentOrderExportNoMasterKey(t *testing.T) {
	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	f := newOrderFix(t, newAgentEnv(t), true)
	r := f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-export-nokey", 500))
	t.Setenv("APP_MASTER_KEY", "")
	w := f.agentDo(f.s, "POST", fmt.Sprintf("%s/%d/export", orderPath, r.Order.ID), "")
	if w.Code != 503 || jsonField(t, w.Body.String(), "error") != "maintenance" || jsonField(t, w.Body.String(), "message") != "系统维护中，暂不能导出码" {
		t.Fatalf("应 503 maintenance: %d %s", w.Code, w.Body.String())
	}
	logs := buf.String()
	if strings.Count(logs, "主密钥不可用") != 1 || !strings.Contains(logs, "主密钥不可用: secretbox.ErrNoMasterKey") {
		t.Fatalf("日志里主密钥不可用应只出现一次:\n%s", logs)
	}
	if strings.Contains(logs, r.Codes[0].Code) {
		t.Fatal("日志里出现了完整码")
	}
}

func TestAgentOrderRateLimit(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	bid, bs := f.seedBuyer("bob", 0)
	for i := 0; i < 30; i++ {
		if w := f.post(f.s, orderReq("mw-plus-ph", 0, "", fmt.Sprintf("req-rate-%d", i), 500)); w.Code != 400 {
			t.Fatalf("第 %d 次应照常处理: %d %s", i+1, w.Code, w.Body.String())
		}
	}
	r := f.expectRefused(f.s, f.aid, orderReq("mw-plus-ph", 1, "", "req-rate-31", 500), 429, "rate_limited")
	if r.Message != "操作太频繁，请稍后再试" {
		t.Fatalf("提示不对: %q", r.Message)
	}
	// 按代理商计：bob 不受影响
	f.expectRefused(bs, bid, orderReq("mw-plus-ph", 0, "", "req-rate-bob", 500), 400, "invalid_request")
	// 过了这一分钟就恢复
	f.clock = f.clock.Add(61 * time.Second)
	f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-rate-31", 500))
}

func TestAgentOrderExportScoped(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	bid, bs := f.seedBuyer("bob", 1000)
	mine := f.mustOrder(f.s, orderReq("mw-plus-ph", 2, "", "req-export-a", 500))
	theirs := f.mustOrder(bs, orderReq("mw-plus-ph", 2, "", "req-export-b", 500))
	if theirs.Order.ID == mine.Order.ID {
		t.Fatal("两笔订单不应相同")
	}
	_ = bid

	for _, path := range []string{
		fmt.Sprintf("%s/%d/export", orderPath, theirs.Order.ID),
		orderPath + "/99999/export",
		orderPath + "/abc/export",
	} {
		w := f.agentDo(f.s, "POST", path, "")
		if w.Code != 404 || jsonField(t, w.Body.String(), "error") != "not_found" {
			t.Fatalf("%s 应 404: %d %s", path, w.Code, w.Body.String())
		}
		for _, c := range theirs.Codes {
			if strings.Contains(w.Body.String(), c.Code) {
				t.Fatal("不应拿到别人的码")
			}
		}
	}
	if w := f.agentDo(f.s, "GET", fmt.Sprintf("%s/%d", orderPath, theirs.Order.ID), ""); w.Code != 404 || jsonField(t, w.Body.String(), "error") != "not_found" {
		t.Fatalf("看别人的订单应 404: %d %s", w.Code, w.Body.String())
	}

	w := f.agentDo(f.s, "POST", fmt.Sprintf("%s/%d/export", orderPath, mine.Order.ID), "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("导出自己的订单: %d %q %s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
	}
	var ex struct {
		Codes []orderCode `json:"codes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ex); err != nil || !sameCodes(ex.Codes, mine.Codes) {
		t.Fatalf("导出的码应和下单时一样: %v %+v vs %+v", err, ex.Codes, mine.Codes)
	}
	audit := auditRows(t)
	if !strings.Contains(audit, fmt.Sprintf("agent:alice|agent_codes_export|order=%d count=2", mine.Order.ID)) {
		t.Fatalf("缺导出审计:\n%s", audit)
	}
	for _, c := range append(mine.Codes, theirs.Codes...) {
		if strings.Contains(audit, c.Code) {
			t.Fatal("审计里出现了完整码")
		}
	}
}

func TestAgentOrderListScoped(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	_, bs := f.seedBuyer("bob", 1000)
	o1 := f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-list-a1", 500)).Order
	o2 := f.mustOrder(f.s, orderReq("mw-plus-ph", 1, "", "req-list-a2", 500)).Order
	f.mustOrder(bs, orderReq("mw-plus-ph", 1, "", "req-list-b1", 500))

	list := func(s *agentSession, q string) (int, []any) {
		w := f.agentDo(s, "GET", orderPath+q, "")
		if w.Code != 200 {
			t.Fatalf("订单列表 %s: %d %s", q, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), `"codes"`) || strings.Contains(w.Body.String(), "MW-") {
			t.Fatalf("订单列表不应带码: %s", w.Body.String())
		}
		m := decodeMap(t, w.Body.String())
		return int(m["total"].(float64)), m["items"].([]any)
	}
	total, items := list(f.s, "")
	if total != 2 || len(items) != 2 {
		t.Fatalf("alice 应只看到自己的 2 笔: %d %v", total, items)
	}
	if id0, id1 := items[0].(map[string]any)["id"], items[1].(map[string]any)["id"]; id0 != float64(o2.ID) || id1 != float64(o1.ID) {
		t.Fatalf("应按 id 降序: %v %v", id0, id1)
	}
	for _, it := range items {
		if no := it.(map[string]any)["request_no"]; no != "req-list-a1" && no != "req-list-a2" {
			t.Fatalf("混进了别人的订单: %v", it)
		}
	}
	// 分页：page_size 小于 1 取 1，第 2 页是更早的那笔
	if total, items = list(f.s, "?page_size=0"); total != 2 || len(items) != 1 {
		t.Fatalf("page_size=0 应取 1: %d %d", total, len(items))
	}
	if _, items = list(f.s, "?page=2&page_size=1"); len(items) != 1 || items[0].(map[string]any)["id"] != float64(o1.ID) {
		t.Fatalf("第 2 页: %v", items)
	}
	if _, items = list(f.s, "?page=0&page_size=500"); len(items) != 2 {
		t.Fatalf("越界取边界值: %v", items)
	}
	if total, _ = list(bs, ""); total != 1 {
		t.Fatalf("bob 应只看到 1 笔: %d", total)
	}

	w := f.agentDo(f.s, "GET", fmt.Sprintf("%s/%d", orderPath, o1.ID), "")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"codes"`) {
		t.Fatalf("订单详情: %d %s", w.Code, w.Body.String())
	}
	if got := decodeMap(t, w.Body.String())["order"].(map[string]any); got["id"] != float64(o1.ID) || got["request_no"] != "req-list-a1" {
		t.Fatalf("订单详情不对: %v", got)
	}

	// 管理员的订单列表用同一套分页
	w = f.adminDo("GET", fmt.Sprintf("%s/agents/%d/orders?page_size=0", adminBase, f.aid), "", nil)
	if m := decodeMap(t, w.Body.String()); w.Code != 200 || m["total"] != float64(2) || len(m["items"].([]any)) != 1 {
		t.Fatalf("管理员分页 page_size=0 应取 1: %d %s", w.Code, w.Body.String())
	}
}

func TestAgentOrderExportUnsealFailure(t *testing.T) {
	f := newOrderFix(t, newAgentEnv(t), true)
	req := orderReq("mw-plus-ph", 2, "", "req-unseal-1", 500)
	r := f.mustOrder(f.s, req)
	bad := r.Codes[1].ID
	if _, err := db.DB.Exec(`UPDATE local_cdks SET code_sealed = 'v1:AAAA' WHERE id = ?`, bad); err != nil {
		t.Fatal(err)
	}
	before := len(f.alertList())
	w := f.agentDo(f.s, "POST", fmt.Sprintf("%s/%d/export", orderPath, r.Order.ID), "")
	var ex struct {
		Codes []orderCode `json:"codes"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ex) != nil || len(ex.Codes) != 2 {
		t.Fatalf("导出: %d %s", w.Code, w.Body.String())
	}
	if ex.Codes[0] != r.Codes[0] || ex.Codes[1].ID != bad || !ex.Codes[1].Unavailable || ex.Codes[1].Code != "" {
		t.Fatalf("解不开的码应标 unavailable，其余照常: %+v", ex.Codes)
	}
	if !strings.Contains(w.Body.String(), `"unavailable":true`) || strings.Count(w.Body.String(), `"unavailable"`) != 1 {
		t.Fatalf("只有解不开的那张带 unavailable: %s", w.Body.String())
	}
	alerts := f.alertList()[before:]
	if len(alerts) != 1 || !strings.Contains(alerts[0], fmt.Sprint(bad)) {
		t.Fatalf("应有 1 条只写码 ID 的告警: %v", alerts)
	}
	for _, c := range r.Codes {
		if strings.Contains(alerts[0], c.Code) {
			t.Fatal("告警里出现了完整码")
		}
	}
	// 重放也一样
	again := f.mustOrder(f.s, req)
	if !again.Replayed || !again.Codes[1].Unavailable || again.Codes[0] != r.Codes[0] {
		t.Fatalf("重放: %+v", again)
	}
}
