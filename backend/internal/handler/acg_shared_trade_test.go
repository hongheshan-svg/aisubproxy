package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// ---- trade 测试辅助 ----

// 单号从 202611050000000001 起。
const (
	tradeNo1 = "202611050000000001"
	tradeNo2 = "202611050000000002"
	tradeNo3 = "202611050000000003"
)

// tradeFields 是一次 trade 的表单：shared_code、num、request_no 再加上下游会带的 contact、device、card_id 等。
func tradeFields(reqNo, code, num string, extra ...[2]string) [][2]string {
	fields := [][2]string{
		{"shared_code", code}, {"num", num}, {"contact", "buyer@example.com"}, {"device", "0"},
		{"password", ""}, {"card_id", "0"}, {"race", ""}, {"request_no", reqNo},
	}
	return append(fields, extra...)
}

// tradeOK 是成功响应的 data。Amount、Stock 是原始 JSON（要看 10.00 这样的写法）。
type tradeOK struct {
	URL, Amount, TradeNo, Secret, LeaveMessage, Stock string
}

// decodeTrade 解开成功响应的 data，要求恰好这 6 个字段。
func decodeTrade(t *testing.T, data json.RawMessage) tradeOK {
	t.Helper()
	m := rawFields(t, data)
	if len(m) != 6 {
		t.Fatalf("trade 的 data 应只有 6 个字段: %s", data)
	}
	return tradeOK{
		URL: jsonString(t, m["url"]), Amount: string(m["amount"]), TradeNo: jsonString(t, m["tradeNo"]),
		Secret: jsonString(t, m["secret"]), LeaveMessage: jsonString(t, m["leave_message"]), Stock: string(m["stock"]),
	}
}

// mustTrade 要求成功，返回 data。
func (f *sharedFix) mustTrade(fields [][2]string) tradeOK {
	f.t.Helper()
	return decodeTrade(f.t, f.mustShared("commodity/trade", fields))
}

var tradeSecretLine = regexp.MustCompile(`^(\S+)  兑换：https://example\.com/recharge\?code=(\S+)$`)

// secretCodes 拆出发货文本里的码，要求每行都符合格式、两处的码相同。
func secretCodes(t *testing.T, secret string) []string {
	t.Helper()
	var codes []string
	for _, line := range strings.Split(secret, "\n") {
		m := tradeSecretLine.FindStringSubmatch(line)
		if m == nil || m[1] != m[2] {
			t.Fatalf("发货文本这一行格式不对: %q", line)
		}
		codes = append(codes, m[1])
	}
	return codes
}

// sharedFailures 是 alice 的全部取码失败记录，按对方订单号索引。
func (f *sharedFix) sharedFailures() map[string]db.AgentSharedFailure {
	f.t.Helper()
	list, _, err := db.ListAgentSharedFailures(context.Background(), f.aid, 0, 1000, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]db.AgentSharedFailure{}
	for _, x := range list {
		out[x.RequestNo] = x
	}
	return out
}

// failure 取这一号的失败记录，没有就失败。
func (f *sharedFix) failure(reqNo string) db.AgentSharedFailure {
	f.t.Helper()
	x, ok := f.sharedFailures()[reqNo]
	if !ok {
		f.t.Fatalf("单号 %s 应有失败记录: %v", reqNo, f.sharedFailures())
	}
	return x
}

// expectFailure 要求这一号的失败记录是这个原因、提示、次数、商品和数量，并且没有标已解决。
func (f *sharedFix) expectFailure(reqNo, reason, msg string, attempts int, product string, qty int) {
	f.t.Helper()
	x := f.failure(reqNo)
	if x.Reason != reason || x.Message != msg || x.Attempts != attempts || x.ProductCode != product || x.Quantity != qty || x.ResolvedOrderID != nil {
		f.t.Fatalf("单号 %s 的失败记录不对: %+v（应为 %s/%s/%d 次/%s/%d）", reqNo, x, reason, msg, attempts, product, qty)
	}
}

// tradeRefused 要求 trade 失败，并且余额、流水、订单、码都没变。
func (f *sharedFix) tradeRefused(fields [][2]string, want string) {
	f.t.Helper()
	bal, led, ords, cdks := balanceOf(f.t, f.aid), countRows(f.t, `SELECT COUNT(*) FROM agent_ledger WHERE agent_id = ?`, f.aid),
		countRows(f.t, `SELECT COUNT(*) FROM agent_orders WHERE agent_id = ?`, f.aid), countRows(f.t, `SELECT COUNT(*) FROM local_cdks WHERE agent_id = ?`, f.aid)
	f.sharedRefused("commodity/trade", fields, want)
	if b := balanceOf(f.t, f.aid); b != bal {
		f.t.Fatalf("余额变了: %d -> %d", bal, b)
	}
	if n := countRows(f.t, `SELECT COUNT(*) FROM agent_ledger WHERE agent_id = ?`, f.aid); n != led {
		f.t.Fatalf("流水变了: %d -> %d", led, n)
	}
	if n := countRows(f.t, `SELECT COUNT(*) FROM agent_orders WHERE agent_id = ?`, f.aid); n != ords {
		f.t.Fatalf("订单变了: %d -> %d", ords, n)
	}
	if n := countRows(f.t, `SELECT COUNT(*) FROM local_cdks WHERE agent_id = ?`, f.aid); n != cdks {
		f.t.Fatalf("码变了: %d -> %d", cdks, n)
	}
}

// onlyOrderID 要求 alice 恰好有 1 笔订单，返回它的 ID。
func (f *sharedFix) onlyOrderID() int64 {
	f.t.Helper()
	if n := countRows(f.t, `SELECT COUNT(*) FROM agent_orders`); n != 1 {
		f.t.Fatalf("应只有 1 笔订单: %d", n)
	}
	var id int64
	if err := db.DB.QueryRow(`SELECT id FROM agent_orders WHERE agent_id = ?`, f.aid).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// 本站码的哈希都在这笔订单里：发货文本里的码确实是这一单发出的。
func (f *sharedFix) checkCodesOfOrder(orderID int64, codes []string) {
	f.t.Helper()
	if n := countRows(f.t, `SELECT COUNT(*) FROM local_cdks WHERE agent_order_id = ?`, orderID); n != len(codes) {
		f.t.Fatalf("订单 %d 应有 %d 张码: %d", orderID, len(codes), n)
	}
	for _, c := range codes {
		if n := countRows(f.t, `SELECT COUNT(*) FROM local_cdks WHERE agent_order_id = ? AND code_hash = ?`, orderID, db.HashCDKCode(c)); n != 1 {
			f.t.Fatalf("码不是订单 %d 发出的", orderID)
		}
	}
}

// ---- 成功和重放 ----

func TestSharedTrade_Success(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	r := f.mustTrade(tradeFields(tradeNo1, "mw-plus-ph", "2"))
	oid := f.onlyOrderID()
	if r.TradeNo != fmt.Sprintf("AO%d", oid) {
		t.Fatalf("tradeNo 应为 AO%d: %q", oid, r.TradeNo)
	}
	if r.Amount != "10.00" || r.Stock != "200" || r.URL != "" || r.LeaveMessage != "" {
		t.Fatalf("amount=%s stock=%s url=%q leave_message=%q", r.Amount, r.Stock, r.URL, r.LeaveMessage)
	}
	if strings.HasPrefix(r.Secret, "\n") || strings.HasSuffix(r.Secret, "\n") {
		t.Fatalf("发货文本首尾不应有换行: %q", r.Secret)
	}
	codes := secretCodes(t, r.Secret)
	if len(codes) != 2 || codes[0] == codes[1] {
		t.Fatalf("应有 2 张不同的码: %d", len(codes))
	}
	f.checkCodesOfOrder(oid, codes)

	var source, ext, reqNo string
	if err := db.DB.QueryRow(`SELECT source, external_ref, request_no FROM agent_orders WHERE id = ?`, oid).Scan(&source, &ext, &reqNo); err != nil {
		t.Fatal(err)
	}
	if source != "acg" || ext != tradeNo1 || reqNo != "acg:"+tradeNo1 {
		t.Fatalf("source=%q external_ref=%q request_no=%q", source, ext, reqNo)
	}
	if b := balanceOf(t, f.aid); b != 0 {
		t.Fatalf("余额应为 0: %d", b)
	}
	want := fmt.Sprintf("agent:alice|agent_order|source=acg ext=%s product=mw-plus-ph quantity=2 total_cents=1000 order=%d\n", tradeNo1, oid)
	if audits := auditRows(t); !strings.Contains(audits, want) {
		t.Fatalf("审计应有 %q:\n%s", want, audits)
	}
	if fs := f.sharedFailures(); len(fs) != 0 {
		t.Fatalf("成功不应有失败记录: %v", fs)
	}
}

func TestSharedTrade_Replay(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	fields := tradeFields(tradeNo1, "mw-plus-ph", "2")
	first := f.mustTrade(fields)
	bal := balanceOf(t, f.aid)
	again := f.mustTrade(fields)
	if again != first || again.Secret == "" {
		t.Fatalf("重放应拿回同一批码和同一个 tradeNo:\n%+v\n%+v", first, again)
	}
	if b := balanceOf(t, f.aid); b != bal {
		t.Fatalf("重放不应扣款: %d -> %d", bal, b)
	}
	oid := f.onlyOrderID()
	want := fmt.Sprintf("agent:alice|agent_codes_export|order=%d count=2 replay=true source=acg ext=%s\n", oid, tradeNo1)
	audits := auditRows(t)
	if strings.Count(audits, want) != 1 || strings.Count(audits, "|agent_order|") != 1 {
		t.Fatalf("审计应有 1 条新单、1 条重放 %q:\n%s", want, audits)
	}
	// 下游只改了 contact 之类忽略的字段也算同一笔
	if r := f.mustTrade(tradeFields(tradeNo1, "mw-plus-ph", "2", [2]string{"contact", "other"})); r != first {
		t.Fatalf("忽略的字段不同也应重放: %+v", r)
	}
}

func TestSharedTrade_Conflict(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	f.mustTrade(tradeFields(tradeNo1, "mw-plus-ph", "2"))
	f.tradeRefused(tradeFields(tradeNo1, "mw-plus-ph", "1"), "请求号冲突")
	f.expectFailure(tradeNo1, "request_conflict", "请求号冲突", 1, "mw-plus-ph", 1)
}

// 同一个进程里同号并发：只建一单、只扣一次，所有成功的响应发货文本相同。
func TestSharedTrade_ConcurrentSameRequestNo(t *testing.T) {
	f := newSharedFix(t, newAgentEnvFileDB(t), true)
	fields := tradeFields(tradeNo1, "mw-plus-ph", "2")
	const n = 8
	res := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res[i] = f.post("commodity/trade", fields)
		}(i)
	}
	close(start)
	wg.Wait()

	secret, ok := "", 0
	for i, w := range res {
		code, msg, data := decodeShared(t, w)
		if code != 200 {
			t.Fatalf("第 %d 个请求应成功: %d %q", i, code, msg)
		}
		r := decodeTrade(t, data)
		if r.Secret == "" {
			t.Fatalf("第 %d 个请求没有发货文本: %s", i, data)
		}
		if secret == "" {
			secret = r.Secret
		} else if r.Secret != secret {
			t.Fatalf("第 %d 个请求拿到的码不同", i)
		}
		ok++
	}
	if ok != n {
		t.Fatalf("应全部成功: %d", ok)
	}
	oid := f.onlyOrderID()
	f.checkCodesOfOrder(oid, secretCodes(t, secret))
	if c := countRows(t, `SELECT COUNT(*) FROM agent_ledger WHERE kind = 'purchase' AND agent_id = ?`, f.aid); c != 1 {
		t.Fatalf("应只扣一次: %d 条下单流水", c)
	}
	if b := balanceOf(t, f.aid); b != 0 {
		t.Fatalf("余额应为 0: %d", b)
	}
	audits := auditRows(t)
	if strings.Count(audits, "|agent_order|") != 1 || strings.Count(audits, "|agent_codes_export|") != n-1 {
		t.Fatalf("审计应有 1 条新单、%d 条重放:\n%s", n-1, audits)
	}
}

// ---- 失败和失败记录 ----

func TestSharedTrade_InsufficientThenResolved(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	if code, m := adjust(f.agentEnv, f.aid, "trade-cut", `{"amount_cents":-900,"note":"测试扣到 100"}`); code != 200 {
		t.Fatalf("调账: %d %v", code, m)
	}
	fields := tradeFields(tradeNo1, "mw-plus-ph", "1")
	f.tradeRefused(fields, "货源余额不足，请联系商家")
	f.expectFailure(tradeNo1, "insufficient_balance", "货源余额不足，请联系商家", 1, "mw-plus-ph", 1)
	first := f.failure(tradeNo1)

	f.clock = f.clock.Add(time.Minute)
	f.tradeRefused(fields, "货源余额不足，请联系商家")
	f.expectFailure(tradeNo1, "insufficient_balance", "货源余额不足，请联系商家", 2, "mw-plus-ph", 1)
	if x := f.failure(tradeNo1); x.FirstAt != first.FirstAt || x.LastAt != f.clock.Unix() {
		t.Fatalf("first_at 不变、last_at 更新: %+v", x)
	}

	if code, m := adjust(f.agentEnv, f.aid, "trade-topup", `{"amount_cents":1000,"note":"补足余额"}`); code != 200 {
		t.Fatalf("调账: %d %v", code, m)
	}
	r := f.mustTrade(fields)
	oid := f.onlyOrderID()
	if r.TradeNo != fmt.Sprintf("AO%d", oid) || r.Amount != "5.00" {
		t.Fatalf("tradeNo=%q amount=%s", r.TradeNo, r.Amount)
	}
	if b := balanceOf(t, f.aid); b != 600 {
		t.Fatalf("余额应为 600: %d", b)
	}
	x := f.failure(tradeNo1)
	if x.ResolvedOrderID == nil || *x.ResolvedOrderID != oid || x.Attempts != 2 {
		t.Fatalf("成功后失败记录应标已解决（订单 %d）: %+v", oid, x)
	}
	total, _, err := db.CountAgentSharedFailures(context.Background(), f.aid, 0)
	if err != nil || total != 0 {
		t.Fatalf("已解决的不应再算未解决: %d %v", total, err)
	}
}

func TestSharedTrade_NotReady(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), false)
	f.tradeRefused(tradeFields(tradeNo1, "mw-plus-ph", "2"), "商品暂时缺货，请稍后再试")
	f.expectFailure(tradeNo1, "not_ready", "商品暂时缺货，请稍后再试", 1, "mw-plus-ph", 2)
}

func TestSharedTrade_Unavailable(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	f.tradeRefused(tradeFields(tradeNo1, "no-such-product", "1"), "当前商品已停售")
	f.expectFailure(tradeNo1, "product_unavailable", "当前商品已停售", 1, "no-such-product", 1)

	f.seedProduct("mw-plus-us", "plus", "US") // 没定价
	f.tradeRefused(tradeFields(tradeNo2, "mw-plus-us", "1"), "当前商品已停售")
	f.expectFailure(tradeNo2, "product_unavailable", "当前商品已停售", 1, "mw-plus-us", 1)

	f.setEnabled(f.pid, false)
	f.tradeRefused(tradeFields(tradeNo3, "mw-plus-ph", "1"), "当前商品已停售")
	f.expectFailure(tradeNo3, "product_unavailable", "当前商品已停售", 1, "mw-plus-ph", 1)

	// 商品码按收到的原样存，截到 64 个字
	long := strings.Repeat("码", 70)
	f.tradeRefused(tradeFields("202611050000000004", long, "1"), "当前商品已停售")
	f.expectFailure("202611050000000004", "product_unavailable", "当前商品已停售", 1, strings.Repeat("码", 64), 1)
	// 首尾空白不去掉，原样交给下单核心（这样的码查不到）
	f.setEnabled(f.pid, true)
	f.tradeRefused(tradeFields("202611050000000005", " mw-plus-ph", "1"), "当前商品已停售")
	f.expectFailure("202611050000000005", "product_unavailable", "当前商品已停售", 1, " mw-plus-ph", 1)
}

func TestSharedTrade_InvalidParams(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	noReqNo := [][2]string{{"shared_code", "mw-plus-ph"}, {"num", "1"}}
	f.tradeRefused(noReqNo, "请求号不正确")
	for _, no := range []string{"", "abc-1", "acg:1", strings.Repeat("1", 65), "单号1"} {
		f.tradeRefused(tradeFields(no, "mw-plus-ph", "1"), "请求号不正确")
	}
	f.tradeRefused(append(noReqNo, [2]string{"request_no[a]", "1"}), "请求号不正确") // 不是标量
	if fs := f.sharedFailures(); len(fs) != 0 {
		t.Fatalf("请求号不合法时不应记失败: %v", fs)
	}

	// 64 位刚好合法
	ok64 := strings.Repeat("9", 64)
	f.mustTrade(tradeFields(ok64, "mw-plus-ph", "1"))

	f.tradeRefused(tradeFields(tradeNo1, "mw-plus-ph", "0"), "购买数量需为 1–200")
	f.expectFailure(tradeNo1, "invalid_request", "购买数量需为 1–200", 1, "mw-plus-ph", 0)
	f.tradeRefused(tradeFields(tradeNo1, "mw-plus-ph", "201"), "购买数量需为 1–200")
	f.expectFailure(tradeNo1, "invalid_request", "购买数量需为 1–200", 2, "mw-plus-ph", 0)
	f.tradeRefused([][2]string{{"shared_code", "mw-plus-ph"}, {"request_no", tradeNo2}}, "购买数量需为 1–200")
	f.expectFailure(tradeNo2, "invalid_request", "购买数量需为 1–200", 1, "mw-plus-ph", 0)

	f.tradeRefused(tradeFields(tradeNo3, "mw-plus-ph", "1", [2]string{"race", "x"}), "该商品没有可选规格")
	f.expectFailure(tradeNo3, "invalid_request", "该商品没有可选规格", 1, "mw-plus-ph", 1)
	f.tradeRefused(tradeFields("202611050000000004", "mw-plus-ph", "1", [2]string{"sku[a]", "b"}), "该商品没有可选规格")
	f.expectFailure("202611050000000004", "invalid_request", "该商品没有可选规格", 1, "mw-plus-ph", 1)
}

func TestSharedTrade_RateLimit(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), false)
	for i := 1; i <= 60; i++ {
		f.tradeRefused(tradeFields(fmt.Sprintf("2026110500000001%02d", i), "mw-plus-ph", "1"), "商品暂时缺货，请稍后再试")
	}
	f.tradeRefused(tradeFields("202611050000000161", "mw-plus-ph", "1"), "请求太频繁，请稍后再试")
	f.expectFailure("202611050000000161", "rate_limited", "请求太频繁，请稍后再试", 1, "mw-plus-ph", 1)
	// 按商户计，只读接口不受影响；过了一分钟恢复
	f.mustShared("authentication/connect", nil)
	f.clock = f.clock.Add(time.Minute)
	f.tradeRefused(tradeFields("202611050000000161", "mw-plus-ph", "1"), "商品暂时缺货，请稍后再试")
	f.expectFailure("202611050000000161", "not_ready", "商品暂时缺货，请稍后再试", 2, "mw-plus-ph", 1)
}

// 整个调用带 sharedTradeTimeout（默认 20 秒）的 ctx 进下单核心；超时按「系统繁忙」，记 busy，不建单。
func TestSharedTrade_Timeout(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	oldLock, oldTimeout := agentOrderLock, sharedTradeTimeout
	t.Cleanup(func() { agentOrderLock, sharedTradeTimeout = oldLock, oldTimeout })

	// 默认 20 秒
	var left time.Duration
	agentOrderLock = func(ctx context.Context, agentID int64, requestNo string) (func(), bool) {
		if d, ok := ctx.Deadline(); ok {
			left = time.Until(d)
		}
		return oldLock(ctx, agentID, requestNo)
	}
	f.mustTrade(tradeFields(tradeNo1, "mw-plus-ph", "1"))
	if left <= 19*time.Second || left > 20*time.Second {
		t.Fatalf("下单核心的 ctx 应带 20 秒超时: %v", left)
	}

	sharedTradeTimeout = 50 * time.Millisecond
	agentOrderLock = func(ctx context.Context, _ int64, _ string) (func(), bool) {
		<-ctx.Done()
		return nil, false
	}
	start := time.Now()
	f.tradeRefused(tradeFields(tradeNo2, "mw-plus-ph", "1"), "系统繁忙，请稍后再试")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("应在超时后立即返回: %v", d)
	}
	f.expectFailure(tradeNo2, "busy", "系统繁忙，请稍后再试", 1, "mw-plus-ph", 1)
}

func TestSharedTrade_ChargesCurrentPrice(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	m := rawFields(t, f.mustShared("commodity/valuation", [][2]string{{"code", "mw-plus-ph"}, {"num", "1"}}))
	if string(m["price"]) != "5.00" {
		t.Fatalf("询价: %s", m["price"])
	}
	f.setPrice(f.pid, 600)
	r := f.mustTrade(tradeFields(tradeNo1, "mw-plus-ph", "1"))
	if r.Amount != "6.00" {
		t.Fatalf("应按现价扣: %s", r.Amount)
	}
	if b := balanceOf(t, f.aid); b != 400 {
		t.Fatalf("余额应为 400: %d", b)
	}
}

// 重放时有码解不开：不交出不完整的一批码，回「系统维护中」，记 busy，不标已解决、不写导出审计。
func TestSharedTrade_ReplayWithUnavailableCode(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	fields := tradeFields(tradeNo1, "mw-plus-ph", "2")
	f.mustTrade(fields)
	oid := f.onlyOrderID()
	if _, err := db.DB.Exec(`UPDATE local_cdks SET code_sealed = 'x' WHERE id = (SELECT MIN(id) FROM local_cdks WHERE agent_order_id = ?)`, oid); err != nil {
		t.Fatal(err)
	}
	f.tradeRefused(fields, "系统维护中")
	f.expectFailure(tradeNo1, "busy", "系统维护中", 1, "mw-plus-ph", 2)
	if audits := auditRows(t); strings.Contains(audits, "agent_codes_export") {
		t.Fatalf("没交出码不应写导出审计:\n%s", audits)
	}
}

// 下单途中代理商被停用：回「代理商账号已停用」，不记失败。
func TestSharedTrade_AgentDisabledMidway(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	t.Cleanup(func() { agentOrderPausePoint = nil })
	agentOrderPausePoint = func() {
		if err := db.SetAgentStatus(context.Background(), f.aid, db.AgentDisabled, f.clock); err != nil {
			t.Error(err)
		}
	}
	f.tradeRefused(tradeFields(tradeNo1, "mw-plus-ph", "1"), "代理商账号已停用")
	if fs := f.sharedFailures(); len(fs) != 0 {
		t.Fatalf("代理商停用不应记失败: %v", fs)
	}
}

// 码加密失败（主密钥在下单途中不可用）：回「系统维护中」，不记失败。
func TestSharedTrade_SealFailure(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	t.Cleanup(func() { agentOrderPausePoint = nil })
	agentOrderPausePoint = func() { t.Setenv("APP_MASTER_KEY", "") }
	f.tradeRefused(tradeFields(tradeNo1, "mw-plus-ph", "1"), "系统维护中")
	if fs := f.sharedFailures(); len(fs) != 0 {
		t.Fatalf("主密钥不可用不应记失败: %v", fs)
	}
}

func TestSharedTrade_LogNoSecrets(t *testing.T) {
	f := newSharedFix(t, newAgentEnv(t), true)
	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })

	ok := tradeFields(tradeNo1, "mw-plus-ph", "2")
	codes := secretCodes(t, f.mustTrade(ok).Secret)
	f.mustTrade(ok)
	short := tradeFields(tradeNo2, "mw-plus-ph", "1")
	f.tradeRefused(short, "货源余额不足，请联系商家")

	out := buf.String()
	banned := append([]string{f.key, "app_key=", signOf(t, sharedBody(ok, f.appID, f.key)), signOf(t, sharedBody(short, f.appID, f.key))}, codes...)
	for _, s := range banned {
		if strings.Contains(out, s) {
			t.Fatalf("日志不应含码、密钥或签名:\n%s", out)
		}
	}
	for _, want := range []string{
		"[acg-shared] app_id=" + f.appID + " trade request_no=" + tradeNo1 + " 结果=成功",
		"[acg-shared] app_id=" + f.appID + " trade request_no=" + tradeNo1 + " 结果=重放",
		"[acg-shared] app_id=" + f.appID + " trade request_no=" + tradeNo2 + " 结果=insufficient_balance",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("日志应有 %q:\n%s", want, out)
		}
	}
}
