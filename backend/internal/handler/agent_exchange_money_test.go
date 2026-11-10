package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

const (
	exchangeOldCode  = "ZC-EXCHANGE-OLD-CODE-0001"
	exchangePassword = "swap-secret-1"
)

func seedExchange(t *testing.T, codes ...string) {
	t.Helper()
	if err := setAgentSwapPassword(exchangePassword); err != nil {
		t.Fatal(err)
	}
	resetLocalRate()
	for i, code := range codes {
		if err := db.SaveCardplatformCDKCode(int64(77+i), code, code[:14], "plus", 0, "PH"); err != nil {
			t.Fatal(err)
		}
	}
}

// exchangeRouter 只建一次：gin.SetMode 写全局变量，并发调用会触发竞态。
var exchangeRouter = sync.OnceValue(func() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/exchange", PublicAgentCDKExchange)
	return r
})

func exchangeRow(t *testing.T, code string) (state string, newID int64, rows int) {
	t.Helper()
	h := db.HashCDKCode(code)
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_cdk_exchanges WHERE old_code_hash = ?`, h).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		return "", 0, 0
	}
	if err := db.DB.QueryRow(`SELECT state, COALESCE(new_upstream_id, 0) FROM agent_cdk_exchanges WHERE old_code_hash = ?`, h).Scan(&state, &newID); err != nil {
		t.Fatal(err)
	}
	return state, newID, rows
}

func TestAgentSwapDailyLimitSetting(t *testing.T) {
	dbtest.Open(t)
	if got := agentSwapDailyLimit(); got != 20 {
		t.Fatalf("默认应为 20: %d", got)
	}
	for _, bad := range []string{"0", "-5", "abc", " "} {
		if err := db.SetSetting(agentSwapDailyLimitKey, bad); err != nil {
			t.Fatal(err)
		}
		if got := agentSwapDailyLimit(); got != 20 {
			t.Fatalf("库里是 %q 时应按默认值，不能变不限: %d", bad, got)
		}
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/settings", AdminPutSettings)
	r.GET("/settings", AdminGetSettings)
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, bad := range []string{`{"agent_swap_daily_limit":0}`, `{"agent_swap_daily_limit":-1}`} {
		if w := put(bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%s 应拒绝: %d", bad, w.Code)
		}
	}
	w := put(`{"agent_swap_daily_limit":7}`)
	if w.Code != 200 {
		t.Fatalf("应保存: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["agent_swap_daily_limit"] != float64(7) {
		t.Fatalf("响应: %s", w.Body.String())
	}
	if got := agentSwapDailyLimit(); got != 7 {
		t.Fatalf("应生效: %d", got)
	}
	// 不传时不改
	if w := put(`{"brand_name":"x"}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if got := agentSwapDailyLimit(); got != 7 {
		t.Fatalf("不传不应改动: %d", got)
	}
}

// 进程在发码途中崩溃：占位留在 pending，维护循环升级成 unknown 并告警一次。
func TestOpsMaintenanceEscalatesStaleExchange(t *testing.T) {
	dbtest.Open(t)
	alerts := captureOpsAlerts(t)
	now := time.Date(2026, 11, 3, 4, 0, 0, 0, time.UTC)
	id, err := db.ReserveAgentCDKExchange(db.AgentExchangeReservation{
		OldHash: db.HashCDKCode("MW-STALE-AAAA-BBBB-CCCC"), OldID: 0, OldPrefix: "MW-STALE", Plan: "plus", OrderStatus: "not_submitted", PasswordFP: "fp",
	}, 20, now.Add(-20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	runOpsMaintenance(now)
	if e, _ := db.GetAgentExchange(id); e.State != db.AgentExchangeUnknown {
		t.Fatalf("应升级: %+v", e)
	}
	// 升级告警一条，「还有待核对」提醒一条
	if n := alerts.count(); n != 2 {
		t.Fatalf("告警: %v", alerts.texts())
	}
	runOpsMaintenance(now.Add(10 * time.Minute))
	if n := alerts.count(); n != 2 {
		t.Fatalf("当天不应重复: %v", alerts.texts())
	}
	runOpsMaintenance(now.Add(24 * time.Hour))
	if n := alerts.count(); n != 3 {
		t.Fatalf("第二天应再提醒待核对: %v", alerts.texts())
	}
}

func adminExchangeRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("username", "admin1"); c.Next() })
	r.GET(adminExchangesPath, AdminListAgentExchanges)
	r.POST(adminExchangesPath+"/:id/resolve", AdminResolveAgentExchange)
	return r
}

func adminExchangeDo(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ---- 本站码（MW-，走 localCDKExchange）的资金测试：并发只发一张、失败释放可重试、结果不明保留占位、单日上限、两种定论 ----

// adminExchangesPath 是换码记录接口的前缀，和正式路由一样。
const adminExchangesPath = "/api/v1/admin/agent-exchanges"

// localSwap 以 ip 为来源走公开的代理换码接口（和正式路由一样挂着 PublicGuard）。
func localSwap(e *localEnv, code, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/public/cdk/exchange", strings.NewReader(`{"password":"`+sealedSwapPassword+`","code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// localExchangedFrom 是从这张旧码换出来的新码张数。
func localExchangedFrom(t *testing.T, oldID int64) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM local_cdks WHERE exchanged_from = ?`, oldID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func localCDKCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM local_cdks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func localExchangeID(t *testing.T, code string) int64 {
	t.Helper()
	var id int64
	if err := db.DB.QueryRow(`SELECT id FROM agent_cdk_exchanges WHERE old_code_hash = ?`, db.HashCDKCode(provider.NormalizeCode(code))).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// localCDKStatus 是这张码的派生状态（unused、disabled ……）。
func localCDKStatus(t *testing.T, id int64) string {
	t.Helper()
	row, err := db.GetLocalCDK(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	v, err := db.LocalCDKViewOf(context.Background(), row)
	if err != nil {
		t.Fatal(err)
	}
	return v.Status
}

// localStaleUnknown 模拟进程在占位之后、ExchangeLocalCDK 之前退出：按 localCDKExchange 的写法占位，之后不收尾；
// 等超过 agentExchangeStaleAfter，维护循环把它升级成结果不明。返回换码记录 id。
func localStaleUnknown(t *testing.T, code string, id int64) int64 {
	t.Helper()
	row, err := db.GetLocalCDK(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	exID, err := db.ReserveAgentCDKExchange(db.AgentExchangeReservation{
		OldHash: db.HashCDKCode(provider.NormalizeCode(code)), OldID: 0, OldPrefix: row.CodePrefix, Plan: row.PlanKey,
		OrderStatus: "not_submitted", IP: "203.0.113.9", PasswordFP: agentSwapPasswordFP(),
	}, agentSwapDailyLimit(), now.Add(-agentExchangeStaleAfter-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	escalateAgentExchanges(now)
	if e, err := db.GetAgentExchange(exID); err != nil || e.State != db.AgentExchangeUnknown {
		t.Fatalf("超时占位应升级成结果不明: %+v %v", e, err)
	}
	return exID
}

// localSwapConcurrent 同一张本站码 20 个请求同时换：只有 1 个 200，其余 409；local_cdks 只多出 1 张新码，换码记录 1 行且已完成。
func localSwapConcurrent(t *testing.T, e *localEnv) {
	t.Helper()
	code, id := exchangeableCode(t, e)
	before := localCDKCount(t)
	const n = 20
	start := make(chan struct{})
	others := make(chan string, n)
	var wg sync.WaitGroup
	var ok, conflict atomic.Int32
	var newID atomic.Int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			w := localSwap(e, code, fmt.Sprintf("203.0.113.%d", i+1))
			switch w.Code {
			case http.StatusOK:
				ok.Add(1)
				var m struct {
					NewCDKID int64 `json:"new_cdk_id"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &m); err == nil {
					newID.Store(m.NewCDKID)
				}
			case http.StatusConflict:
				conflict.Add(1)
			default:
				others <- fmt.Sprintf("%d %s", w.Code, w.Body.String())
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(others)
	for s := range others {
		t.Errorf("只应有 200 或 409: %s", s)
	}
	if ok.Load() != 1 || conflict.Load() != n-1 {
		t.Fatalf("应 1 个 200、%d 个 409: ok=%d conflict=%d", n-1, ok.Load(), conflict.Load())
	}
	if got := localCDKCount(t) - before; got != 1 {
		t.Fatalf("local_cdks 应只多 1 张新码，实际多 %d 张", got)
	}
	if got := localExchangedFrom(t, id); got != 1 {
		t.Fatalf("从旧码换出的新码应是 1 张: %d", got)
	}
	st, gotNew, rows := exchangeRow(t, code)
	if rows != 1 || st != db.AgentExchangeDone || gotNew <= 0 || gotNew != newID.Load() {
		t.Fatalf("换码记录不符: rows=%d st=%s new=%d resp=%d", rows, st, gotNew, newID.Load())
	}
	if e.up.callCount() != 0 {
		t.Fatalf("本站码换码不应发出任何上游请求: %s", e.up.log())
	}
}

// 内存库（单连接）：并发只发一张。
func TestLocalAgentExchangeConcurrentIssuesOnce(t *testing.T) {
	localSwapConcurrent(t, newSealedExchangeEnv(t))
}

// 真实文件库、多连接（DSN 和生产一样带 _txlock=immediate）：并发只发一张。
func TestLocalAgentExchangeConcurrentOnRealFileDB(t *testing.T) {
	e := newAgentEnvFileDB(t)
	e.seedRealPayment()
	if err := setAgentSwapPassword(sealedSwapPassword); err != nil {
		t.Fatal(err)
	}
	resetLocalRate()
	localSwapConcurrent(t, e.localEnv)
}

// ExchangeLocalCDK 在事务里发现不符合换码条件（处理器校验通过之后、事务之前，这张码有了一张在途单）：
// 事务回滚、不发新码，占位释放，不转人工；条件恢复后同一张码能再换一次，也只发一张。
func TestLocalAgentExchangeFailureReleasesAndRetries(t *testing.T) {
	e := newSealedExchangeEnv(t)
	code, id := exchangeableCode(t, e)
	// 占位那一刻插进一张在途单：前面的校验都已经过了，ExchangeLocalCDK 的条件更新命不中
	if _, err := db.DB.Exec(fmt.Sprintf(`CREATE TRIGGER swap_race AFTER INSERT ON agent_cdk_exchanges BEGIN
		INSERT INTO pay_orders (order_no, provider, source, source_ref, dry_run, plan_key, region, account_email, state, failure, created_at, updated_at)
		VALUES ('PO-SWAP-RACE', 'marswallex', 'cdk', '%s', 0, 'plus', 'PH', 'race@example.com', 'funding', '', 0, 0); END`,
		db.LocalCDKSourceRef(id))); err != nil {
		t.Fatal(err)
	}
	alertsBefore := e.alertTexts()
	w := localSwap(e, code, "203.0.113.9")
	m := localJSON(t, w)
	if w.Code != http.StatusBadRequest || m["error_code"] != "not_eligible_unpaid" || m["error"] != "不符合换码条件（需充值失败且未发起支付）" {
		t.Fatalf("应报告不符合换码条件: %d %v", w.Code, m)
	}
	if _, ok := m["outcome"]; ok {
		t.Fatalf("确定失败不是结果不明: %v", m)
	}
	if st, _, rows := exchangeRow(t, code); rows != 0 {
		t.Fatalf("应释放占位: rows=%d st=%s", rows, st)
	}
	if got := localExchangedFrom(t, id); got != 0 {
		t.Fatalf("事务回滚，不应有新码: %d", got)
	}
	if got := e.alertTexts(); got != alertsBefore {
		t.Fatalf("确定失败不转人工、不告警: %s", got)
	}

	// 条件恢复：去掉触发器，那张单结束为干净失败（没点过支付）
	if _, err := db.DB.Exec(`DROP TRIGGER swap_race`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE pay_orders SET state = 'failed', failure = 'not_submitted', submitted_at = NULL, finished_at = 1 WHERE order_no = 'PO-SWAP-RACE'`); err != nil {
		t.Fatal(err)
	}
	w = localSwap(e, code, "203.0.113.9")
	if w.Code != http.StatusOK {
		t.Fatalf("重试应成功: %d %s", w.Code, w.Body.String())
	}
	if st, _, rows := exchangeRow(t, code); rows != 1 || st != db.AgentExchangeDone {
		t.Fatalf("重试后记录应完成: rows=%d st=%s", rows, st)
	}
	if got := localExchangedFrom(t, id); got != 1 {
		t.Fatalf("重试只发一张: %d", got)
	}
}

// 占位之后进程退出、没收尾：维护循环把超时的占位转成结果不明并告警；之后同一张码再换回 409（state=unknown），
// 不发新码，旧码不动。
func TestLocalAgentExchangeUnknownKeepsReservation(t *testing.T) {
	e := newSealedExchangeEnv(t)
	code, id := exchangeableCode(t, e)
	exID := localStaleUnknown(t, code, id)
	alerts := e.alertTexts()
	if !strings.Contains(alerts, "已转人工") || !strings.Contains(alerts, fmt.Sprintf("记录 %d", exID)) {
		t.Fatalf("升级成结果不明应告警: %s", alerts)
	}
	if strings.Contains(alerts, code) {
		t.Fatalf("告警不应含完整卡密: %s", alerts)
	}

	w := localSwap(e, code, "203.0.113.9")
	m := localJSON(t, w)
	if w.Code != http.StatusConflict || m["error_code"] != "conflict" || m["state"] != db.AgentExchangeUnknown ||
		m["error"] != "该卡密上次换码结果不明，已转人工核对，请勿重试" {
		t.Fatalf("结果不明的码再换应 409 且 state=unknown: %d %v", w.Code, m)
	}
	if got := localExchangedFrom(t, id); got != 0 {
		t.Fatalf("不应发新码: %d", got)
	}
	if st := localCDKStatus(t, id); st != db.LocalCDKUnused {
		t.Fatalf("旧码不应被动过: %s", st)
	}
	if st, _, rows := exchangeRow(t, code); rows != 1 || st != db.AgentExchangeUnknown {
		t.Fatalf("占位应保留: rows=%d st=%s", rows, st)
	}
}

// 单日上限设为 2：前 2 张照常换，第 3、4 张回 429（daily_limit、limit=2），不占位、不发新码，同一天只告警一次。
func TestLocalAgentExchangeDailyLimit(t *testing.T) {
	e := newSealedExchangeEnv(t)
	if err := db.SetSetting(agentSwapDailyLimitKey, "2"); err != nil {
		t.Fatal(err)
	}
	codes := make([]string, 4)
	ids := make([]int64, 4)
	// 先把 4 张码都发出来再写失败单：连续 3 张填表失败会让付款熔断，之后就发不了码
	for i := range codes {
		codes[i], ids[i] = e.issueCode("plus", "PH")
	}
	for _, id := range ids {
		e.insertFundedFailure(id, "a@example.com")
	}
	before := strings.Count(e.alertTexts(), "上限")
	for i := 0; i < 2; i++ {
		if w := localSwap(e, codes[i], "203.0.113.9"); w.Code != http.StatusOK {
			t.Fatalf("额度内应成功: %d %s", w.Code, w.Body.String())
		}
	}
	for i := 2; i < 4; i++ {
		w := localSwap(e, codes[i], "203.0.113.9")
		m := localJSON(t, w)
		if w.Code != http.StatusTooManyRequests || m["error_code"] != "daily_limit" || m["limit"] != float64(2) ||
			m["error"] != "代理换码已达今日上限（2 张），请联系管理员" {
			t.Fatalf("第 %d 张应超限: %d %v", i+1, w.Code, m)
		}
		if _, _, rows := exchangeRow(t, codes[i]); rows != 0 {
			t.Fatal("超限不应占位")
		}
		if got := localExchangedFrom(t, ids[i]); got != 0 {
			t.Fatalf("超限不应发新码: %d", got)
		}
	}
	for i := 0; i < 2; i++ {
		if got := localExchangedFrom(t, ids[i]); got != 1 {
			t.Fatalf("额度内的第 %d 张应发 1 张新码: %d", i+1, got)
		}
	}
	if got := strings.Count(e.alertTexts(), "上限") - before; got != 1 {
		t.Fatalf("同一天只告警一次: %d %s", got, e.alertTexts())
	}
}

// 结果不明的本站码记录：人工核对确认没发码，定论 released，占位释放，同一张码能重新换（只发一张）。
// 输入不全、记录不存在、重复定论都要拒绝；列表不泄露旧码哈希和完整码；定论写审计。
func TestAdminResolveLocalExchangeReleased(t *testing.T) {
	e := newSealedExchangeEnv(t)
	code, id := exchangeableCode(t, e)
	exID := localStaleUnknown(t, code, id)
	r := adminExchangeRouter()

	list := adminExchangeDo(r, "GET", adminExchangesPath+"?state=unknown", "")
	var got struct {
		List []db.AgentExchange `json:"list"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil || list.Code != http.StatusOK || len(got.List) != 1 {
		t.Fatalf("列表: %d %s %v", list.Code, list.Body.String(), err)
	}
	if body := list.Body.String(); strings.Contains(body, db.HashCDKCode(provider.NormalizeCode(code))) || strings.Contains(body, code) {
		t.Fatalf("列表不应含旧码哈希或完整码: %s", body)
	}
	if item := got.List[0]; item.ID != exID || item.State != db.AgentExchangeUnknown || item.OldUpstreamID != 0 || item.Note == "" {
		t.Fatalf("行内容: %+v", item)
	}
	path := fmt.Sprintf("%s/%d/resolve", adminExchangesPath, exID)
	for _, bad := range []string{
		`{"result":"released"}`,          // 没有备注
		`{"result":"nope","note":"x"}`,   // 非法结果
		`{"result":"issued","note":"x"}`, // 已发却没写新码
		`{"result":"issued","note":"x","new_cdk_id":-1}`,
	} {
		if w := adminExchangeDo(r, "POST", path, bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400: %d %s", bad, w.Code, w.Body.String())
		}
	}
	if w := adminExchangeDo(r, "POST", adminExchangesPath+"/9999/resolve", `{"result":"released","note":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404: %d", w.Code)
	}
	if st, _, _ := exchangeRow(t, code); st != db.AgentExchangeUnknown {
		t.Fatalf("被拒的定论不应改记录: %s", st)
	}

	w := adminExchangeDo(r, "POST", path, `{"result":"released","note":"本站码表里没有换出的新码"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"result":"released"`) {
		t.Fatalf("释放应 200: %d %s", w.Code, w.Body.String())
	}
	if w := adminExchangeDo(r, "POST", path, `{"result":"released","note":"再来一次"}`); w.Code != http.StatusNotFound {
		t.Fatalf("释放后行已删除: %d %s", w.Code, w.Body.String())
	}
	var detail string
	if err := db.DB.QueryRow(`SELECT detail FROM admin_audit_logs WHERE action = 'agent_exchange_resolve'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "released") || !strings.Contains(detail, "本站码表里没有换出的新码") {
		t.Fatalf("审计: %s", detail)
	}

	if w := localSwap(e, code, "203.0.113.9"); w.Code != http.StatusOK {
		t.Fatalf("释放后重试应成功: %d %s", w.Code, w.Body.String())
	}
	if st, _, rows := exchangeRow(t, code); rows != 1 || st != db.AgentExchangeDone {
		t.Fatalf("重试后记录应完成: rows=%d st=%s", rows, st)
	}
	if got := localExchangedFrom(t, id); got != 1 {
		t.Fatalf("只发一张: %d", got)
	}
}

// 本站码已换出新码、但收尾写库失败：占位停在 pending，维护循环升级成结果不明。管理员核对后定论 issued，
// new_cdk_id 填 local_cdks.id：记录转 disabling，维护循环补成 done。返回里没有 old_disabled，不发任何上游请求，也不多发码。
func TestAdminResolveLocalExchangeIssued(t *testing.T) {
	e := newSealedExchangeEnv(t)
	code, id := exchangeableCode(t, e)
	// pending → disabling 那一步写库失败
	if _, err := db.DB.Exec(`CREATE TRIGGER swap_finish_fail BEFORE UPDATE OF state ON agent_cdk_exchanges
		WHEN NEW.state = 'disabling' BEGIN SELECT RAISE(ABORT, 'finish fail'); END`); err != nil {
		t.Fatal(err)
	}
	w := localSwap(e, code, "203.0.113.9")
	if w.Code != http.StatusOK {
		t.Fatalf("新码已发出，仍应交给代理: %d %s", w.Code, w.Body.String())
	}
	newID := int64(localJSON(t, w)["new_cdk_id"].(float64))
	if st, _, _ := exchangeRow(t, code); st != db.AgentExchangePending {
		t.Fatalf("收尾失败，记录应停在 pending: %s", st)
	}
	if !strings.Contains(e.alertTexts(), "写库失败") {
		t.Fatalf("收尾失败应告警: %s", e.alertTexts())
	}
	if _, err := db.DB.Exec(`DROP TRIGGER swap_finish_fail`); err != nil {
		t.Fatal(err)
	}
	escalateAgentExchanges(time.Now().Add(agentExchangeStaleAfter + time.Minute))
	if st, _, _ := exchangeRow(t, code); st != db.AgentExchangeUnknown {
		t.Fatalf("超时应升级成结果不明: %s", st)
	}
	exID := localExchangeID(t, code)

	r := adminExchangeRouter()
	path := fmt.Sprintf("%s/%d/resolve", adminExchangesPath, exID)
	w = adminExchangeDo(r, "POST", path, fmt.Sprintf(`{"result":"issued","new_cdk_id":%d,"note":"本站码表查到换出的新码"}`, newID))
	if w.Code != http.StatusOK {
		t.Fatalf("定论应成功: %d %s", w.Code, w.Body.String())
	}
	out := localJSON(t, w)
	if out["ok"] != true || out["result"] != "issued" {
		t.Fatalf("定论响应: %v", out)
	}
	for _, k := range []string{"old_disabled", "old_cdk_id"} {
		if _, ok := out[k]; ok {
			t.Fatalf("定论响应不应再有 %s: %v", k, out)
		}
	}
	if st, gotNew, _ := exchangeRow(t, code); st != db.AgentExchangeDisabling || gotNew != newID {
		t.Fatalf("定论 issued 后应是 disabling、新码是本站码 id: st=%s new=%d want=%d", st, gotNew, newID)
	}
	retryAgentExchangeDisables(time.Now())
	if st, gotNew, _ := exchangeRow(t, code); st != db.AgentExchangeDone || gotNew != newID {
		t.Fatalf("维护循环应补成 done: st=%s new=%d", st, gotNew)
	}
	if w := adminExchangeDo(r, "POST", path, `{"result":"released","note":"改主意"}`); w.Code != http.StatusConflict {
		t.Fatalf("已定论的不能再改: %d %s", w.Code, w.Body.String())
	}
	if w := localSwap(e, code, "203.0.113.9"); w.Code != http.StatusConflict {
		t.Fatalf("定论为已发后不能再换: %d %s", w.Code, w.Body.String())
	}
	if got := localExchangedFrom(t, id); got != 1 {
		t.Fatalf("不应多发码: %d", got)
	}
	var detail string
	if err := db.DB.QueryRow(`SELECT detail FROM admin_audit_logs WHERE action = 'agent_exchange_resolve'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "result=issued") || !strings.Contains(detail, fmt.Sprintf("new_cdk=%d", newID)) {
		t.Fatalf("审计: %s", detail)
	}
	if e.up.callCount() != 0 {
		t.Fatalf("不应发出任何上游请求: %v", e.up.log())
	}
}
