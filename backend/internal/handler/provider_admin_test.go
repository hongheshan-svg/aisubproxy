package handler

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// 旧前端保存卡池策略时不传新字段：补卡开关不能被悄悄关掉，显式传时才改。
func TestPutPolicyKeepsReplenishWhenUnselected(t *testing.T) {
	newLocalEnv(t)
	const base = `"card_name_prefix":"POOL2","validity_months":12,"max_uses_per_card":1,"lease_minutes":30,"cancel_after_days":7,"health_window_hours":24,"product_fail_threshold":3,"card_fail_threshold":2`
	put := func(body string) cardpool.Policy {
		t.Helper()
		if w := cpDo(AdminCardPoolPutPolicy, "PUT", "/x", "/x", body); w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		p, err := cardpool.LoadPolicy()
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := put(`{` + base + `}`); p.ReplenishWhenUnselected {
		t.Fatal("默认应关闭")
	}
	if p := put(`{` + base + `,"replenish_when_unselected":true}`); !p.ReplenishWhenUnselected {
		t.Fatal("显式打开要生效")
	}
	if p := put(`{` + base + `}`); !p.ReplenishWhenUnselected {
		t.Fatal("旧前端不传时要沿用")
	}
	if p := put(`{` + base + `,"replenish_when_unselected":false}`); p.ReplenishWhenUnselected {
		t.Fatal("显式关闭要生效")
	}
}

func httpRecorderFor(fn func(*gin.Context)) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/", nil)
	fn(c)
	return w
}

func TestDefaultProviderReadWriteAudited(t *testing.T) {
	e := newLocalEnv(t)
	m := localJSON(t, e.do("GET", "/admin/providers/default", ""))
	if m["provider"] != "zovocard" {
		t.Fatalf("初始值应是 zovocard: %v", m)
	}
	for _, bad := range []string{`{"provider":"bogus"}`, `{"provider":""}`, `not-json`} {
		if w := e.do("PUT", "/admin/providers/default", bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, w.Code)
		}
	}
	if n, _ := provider.LoadDefault(); n != provider.ZovoCard {
		t.Fatal("非法值不能生效")
	}

	// 没过硬门槛也能保存，响应带警告：发码接口另外拦
	w := e.do("PUT", "/admin/providers/default", `{"provider":"marswallex"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m = localJSON(t, w)
	if m["provider"] != "marswallex" || m["previous"] != "zovocard" {
		t.Fatalf("%v", m)
	}
	ready := m["readiness"].(map[string]any)
	if ready["ok"] != false || !strings.Contains(localBody(t, ready["problems"]), "真实付款") {
		t.Fatalf("应带上未就绪原因: %v", ready)
	}
	if n, _ := provider.LoadDefault(); n != provider.MarsWallex {
		t.Fatal("应保存")
	}
	if a := localAudit(t); !strings.Contains(a, "provider_default_update from=zovocard to=marswallex") {
		t.Fatalf("切换要写审计: %s", a)
	}
}

// 切换默认服务商只影响新发的码；已发的码、在途单都不动，也绝不自动换服务商重试。
func TestDefaultProviderOnlyAffectsNewIssues(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	// 默认 zovocard：老接口发码走卡台
	w := e.do("POST", "/admin/cardplatform/cdks", `{"plan":"plus","count":1,"funding_confirmed":true}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ZC-FAKE-ISSUED-CODE-0001") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if m := localJSON(t, w); m["server_stored"] != true || m["provider"] != nil {
		t.Fatalf("ZovoCard 响应字段不能变: %v", m)
	}
	if e.zovo.hits("/openapi/v1/gpt-direct/cdks") != 1 {
		t.Fatalf("%v", e.zovo.paths)
	}
	mwCode, mwID := e.issueCode("plus", "PH") // 显式指定 MarsWallex，默认还是 zovocard

	if w := e.do("PUT", "/admin/providers/default", `{"provider":"marswallex"}`); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// 老接口不传 provider：现在走本地发码，卡台收不到新发码请求
	w = e.do("POST", "/admin/cardplatform/cdks", `{"plan":"plus","payment_country":"PH","count":2,"funding_confirmed":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m := localJSON(t, w)
	if m["provider"] != "marswallex" || m["server_stored"] != false || m["count"].(float64) != 2 {
		t.Fatalf("%v", m)
	}
	if !strings.HasPrefix(m["issued"].([]any)[0].(map[string]any)["code"].(string), "MW-") {
		t.Fatalf("%v", m)
	}
	if e.zovo.hits("/openapi/v1/gpt-direct/cdks") != 1 {
		t.Fatal("切到 MarsWallex 后不应再向卡台发码")
	}
	// 切换后，存量 ZovoCard 码还在 ZovoCard 兑换
	if w := e.do("POST", "/public/cdk/preview", `{"code":"ZC-FAKE-ISSUED-CODE-0001"}`); w.Body.String() != zovoPreviewBody {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if e.zovo.hits("/api/v1/cdk/preview") != 1 {
		t.Fatal("存量 ZovoCard 码应打 ZovoCard")
	}
	// 显式指定 zovocard 也能发
	if w := e.do("POST", "/admin/cdks/issue", `{"provider":"zovocard","plan":"plus","count":1,"funding_confirmed":true}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ZC-FAKE") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}

	// 切回 zovocard：存量 MW 码、在途单不受影响
	token, pf := e.preflight(mwCode, localSession("mw@example.com"))
	if w := e.redeem(token, pf); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	before := e.zovo.count()
	if w := e.do("PUT", "/admin/providers/default", `{"provider":"zovocard"}`); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var state string
	if err := db.DB.QueryRow(`SELECT state FROM pay_orders WHERE source = 'cdk' AND source_ref = ?`, db.LocalCDKSourceRef(mwID)).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != payengine.StateQueued {
		t.Fatalf("切换不该动在途单: %s", state)
	}
	if w := e.result(token); w.Code != http.StatusOK || localJSON(t, w)["provider"] != "marswallex" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// 切换本身（查 ZovoCard 就绪）之外，存量 MW 码查结果不碰 ZovoCard
	if e.zovo.hits("/api/v1/cdk/result") != 0 {
		t.Fatalf("%v (之前 %d 个)", e.zovo.paths, before)
	}
}

// MarsWallex 不支持的产品 400，不回落到 ZovoCard。
func TestIssueRejectsUnsupportedProductForMarsWallex(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	localSet(t, provider.SettingDefault, string(provider.MarsWallex))
	for _, body := range []string{
		`{"plan":"premium_monthly","product":"x","count":1,"funding_confirmed":true,"region":"PH"}`,
		`{"plan":"plus_monthly","count":1,"funding_confirmed":true,"region":"PH"}`, // 推断成 X
		`{"plan":"credit250","count":1,"funding_confirmed":true,"region":"PH"}`,
		`{"plan":"claude_pro","count":1,"funding_confirmed":true,"region":"PH"}`,
		`{"plan":"plus","product":"grok","count":1,"funding_confirmed":true,"region":"PH"}`,
		`{"plan":"pro_20x_renew","count":1,"funding_confirmed":true,"region":"PH"}`,
	} {
		for _, path := range []string{"/admin/cardplatform/cdks", "/admin/cdks/issue"} {
			w := e.do("POST", path, body)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "不支持") {
				t.Fatalf("%s %s: %d %s", path, body, w.Code, w.Body.String())
			}
		}
	}
	if e.zovo.count() != 0 {
		t.Fatalf("ZovoCard 桩不应收到请求: %v", e.zovo.paths)
	}
	var n int
	db.DB.QueryRow(`SELECT COUNT(*) FROM local_cdks`).Scan(&n)
	if n != 0 {
		t.Fatalf("不应发码: %d", n)
	}
	// plus 能发
	w := e.do("POST", "/admin/cardplatform/cdks", `{"plan":"plus","count":1,"funding_confirmed":true,"region":"PH"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

// 就绪检查挡住本地发码，并列出原因。
func TestReadinessBlocksLocalIssue(t *testing.T) {
	issue := func(e *localEnv, plan, region string) (int, string) {
		w := e.do("POST", "/admin/cdks/issue", localBody(t, map[string]any{"provider": "marswallex", "plan": plan, "region": region, "count": 1, "funding_confirmed": true}))
		return w.Code, w.Body.String()
	}
	cases := []struct {
		name  string
		setup func(e *localEnv)
		want  string
	}{
		{"没有真实付款成功的单", func(e *localEnv) {
			// 把 seed 换成试运行单和没成功的单
			db.DB.Exec(`DELETE FROM pay_orders`)
			e.insertOrder("admin_test", "admin", 1, "a@example.com", "succeeded", "", false)
			e.insertOrder("admin_test", "admin", 2, "b@example.com", "succeeded", "", false)
			e.insertOrder("admin_test", "admin", 1, "c@example.com", "previewed", "", false)
			e.insertOrder("admin_test", "admin", 0, "d@example.com", "needs_review", "", true)
		}, "系统自动确认成功的真实付款"},
		{"只有人工定论成功的单", func(e *localEnv) {
			db.DB.Exec(`DELETE FROM pay_orders`)
			no := e.insertOrder("admin_test", "admin", 0, "manual@example.com", "needs_review", "", true)
			if err := e.engine.Resolve(context.Background(), no, payengine.StateSucceeded, "核对已开通", "alice"); err != nil {
				t.Fatal(err)
			}
			var state, by string
			if err := db.DB.QueryRow(`SELECT state, resolved_by FROM pay_orders WHERE order_no = ?`, no).Scan(&state, &by); err != nil || state != payengine.StateSucceeded || by != "alice" {
				t.Fatalf("%s %s %v", state, by, err)
			}
		}, "系统自动确认成功的真实付款"},
		{"没有可用的 USD 卡", func(e *localEnv) { db.DB.Exec(`UPDATE mw_cards SET currency = 'PHP'`) }, "没有可用的 USD 卡"},
		{"卡全停用", func(e *localEnv) {
			db.DB.Exec(`INSERT INTO mw_product_health (product_id, plan_key, suspended_at) VALUES ('p1', 'plus', 100)`)
		}, "没有可用的 USD 卡"},
		{"套餐未启用", func(e *localEnv) {
			if err := payengine.SavePlanTable(payengine.DefaultPlanTable()); err != nil {
				t.Fatal(err)
			}
		}, "未启用"},
		{"MarsWallex 未配置", func(e *localEnv) { localSet(t, db.SettingMarsWallexDeveloperID, "") }, "凭证未配置完整"},
		{"主钱包余额不足", func(e *localEnv) { e.setWallet("1.00") }, "主钱包余额不足"},
		{"今日注资额度用完", func(e *localEnv) {
			db.DB.Exec(`INSERT INTO mw_card_orders (out_order_no, kind, amount_cents, state, created_at, updated_at) VALUES ('O1', 'recharge', 200000, 'success', strftime('%s','now'), 1)`)
		}, "今日注资额度"},
		{"付款熔断", func(e *localEnv) {
			for i := 0; i < 3; i++ {
				e.insertOrder("admin_test", "admin", 0, fmt.Sprintf("x%d@example.com", i), "failed", "declined", true)
			}
		}, "熔断"},
		{"引擎未启动", func(e *localEnv) { SetPayEngine(nil) }, "付款引擎未启动"},
		{"卡池未启动", func(e *localEnv) { SetCardPool(nil) }, "卡池未启动"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLocalEnv(t)
			e.seedRealPayment()
			tc.setup(e)
			code, body := issue(e, "plus", "PH")
			if code != http.StatusBadRequest || !strings.Contains(body, tc.want) {
				t.Fatalf("%d %s", code, body)
			}
			var n int
			db.DB.QueryRow(`SELECT COUNT(*) FROM local_cdks`).Scan(&n)
			if n != 0 {
				t.Fatalf("不应发码: %d", n)
			}
			if e.zovo.count() != 0 {
				t.Fatalf("不应回落到 ZovoCard: %v", e.zovo.paths)
			}
		})
	}

	t.Run("补上之后能发", func(t *testing.T) {
		e := newLocalEnv(t)
		e.seedRealPayment()
		if _, err := db.DB.Exec(`UPDATE mw_cards SET pool_state = 'retired'`); err != nil {
			t.Fatal(err)
		}
		if code, body := issue(e, "plus", "PH"); code != http.StatusBadRequest {
			t.Fatalf("%d %s", code, body)
		}
		e.addCard("c2")
		if code, body := issue(e, "plus", "PH"); code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
	})
	t.Run("注资金额超单笔上限也算未就绪", func(t *testing.T) {
		// 策略保存时已经校验过，这里直接改库模拟配置被旁路修改
		e := newLocalEnv(t)
		e.seedRealPayment()
		raw, _ := db.GetSetting("mw_cardpool_policy")
		localSet(t, "mw_cardpool_policy", strings.Replace(raw, `"max_fund_cents":50000`, `"max_fund_cents":100`, 1))
		if code, body := issue(e, "plus", "PH"); code != http.StatusBadRequest || !strings.Contains(body, "单笔上限") {
			t.Fatalf("%d %s", code, body)
		}
	})
}

func TestLocalIssueValidation(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	for _, tc := range []struct{ body, want string }{
		{`{"provider":"bogus","plan":"plus","region":"PH","funding_confirmed":true}`, "服务商"},
		{`{"provider":"marswallex","region":"PH","funding_confirmed":true}`, "plan"},
		{`{"provider":"marswallex","plan":"plus","funding_confirmed":true}`, "地区"},
		{`{"provider":"marswallex","plan":"plus","region":"PH"}`, "funding_confirmed"},
		{fmt.Sprintf(`{"provider":"marswallex","plan":"plus","region":"PH","funding_confirmed":true,"count":%d}`, cardplatform.MaxIssueCount+1), "count"},
		{`{"provider":"marswallex","plan":"plus","region":"PH","funding_confirmed":true,"note":"` + strings.Repeat("字", localCDKNoteMax+1) + `"}`, "备注"},
		{`not json`, "invalid"},
	} {
		w := e.do("POST", "/admin/cdks/issue", tc.body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%s: %d %s", tc.body[:min(len(tc.body), 60)], w.Code, w.Body.String())
		}
	}
	var n int
	db.DB.QueryRow(`SELECT COUNT(*) FROM local_cdks`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d", n)
	}
}

// 发码幂等：同一次点击只发一批。完整码只存哈希拿不回来，所以重试不返回码，返回 409 说明发过哪一批。
func TestLocalIssueIdempotency(t *testing.T) {
	const body = `{"provider":"marswallex","plan":"plus","region":"PH","count":2,"funding_confirmed":true}`
	rows := func() int {
		t.Helper()
		var n int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM local_cdks`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("缺键或键过长被拒，两条路由一样", func(t *testing.T) {
		e := newLocalEnv(t)
		e.seedRealPayment()
		localSet(t, provider.SettingDefault, string(provider.MarsWallex))
		for _, path := range []string{"/admin/cdks/issue", "/admin/cardplatform/cdks"} {
			for _, key := range []string{"", "   ", strings.Repeat("k", 129)} {
				w := e.doIssue("POST", path, body, key)
				if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Idempotency-Key") {
					t.Fatalf("%s %q: %d %s", path, key, w.Code, w.Body.String())
				}
			}
		}
		if n := rows(); n != 0 {
			t.Fatalf("%d", n)
		}
	})

	t.Run("同一个键只发一批，重试不返回码", func(t *testing.T) {
		e := newLocalEnv(t)
		e.seedRealPayment()
		first := e.doIssue("POST", "/admin/cdks/issue", body, "click-1")
		if first.Code != http.StatusOK {
			t.Fatalf("%d %s", first.Code, first.Body.String())
		}
		issued := localJSON(t, first)["issued"].([]any)
		firstID := issued[0].(map[string]any)["id"].(float64)
		lastID := issued[1].(map[string]any)["id"].(float64)
		for i := 0; i < 2; i++ {
			w := e.doIssue("POST", "/admin/cdks/issue", body, "click-1")
			if w.Code != http.StatusConflict {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			m := localJSON(t, w)
			if m["already_issued"] != true || m["first_id"] != firstID || m["last_id"] != lastID || m["count"].(float64) != 2 || !strings.Contains(m["error"].(string), "禁用") {
				t.Fatalf("%v", m)
			}
			for _, it := range issued {
				if strings.Contains(w.Body.String(), it.(map[string]any)["code"].(string)) {
					t.Fatal("重试不能返回完整码")
				}
			}
		}
		if n := rows(); n != 2 {
			t.Fatalf("只能有一批: %d", n)
		}
		if a := localAudit(t); strings.Count(a, "local_cdk_issue") != 1 {
			t.Fatalf("只写一条审计: %s", a)
		}
		// 新的一次点击照常发
		if w := e.doIssue("POST", "/admin/cdks/issue", body, "click-2"); w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if n := rows(); n != 4 {
			t.Fatalf("%d", n)
		}
		// 键用过了，就算现在未就绪，也如实说发过了，不是报未就绪
		if _, err := db.DB.Exec(`UPDATE mw_cards SET pool_state = 'retired'`); err != nil {
			t.Fatal(err)
		}
		if w := e.doIssue("POST", "/admin/cdks/issue", body, "click-1"); w.Code != http.StatusConflict {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})

	t.Run("没发成不占用键，重试能发", func(t *testing.T) {
		e := newLocalEnv(t)
		e.seedRealPayment()
		if _, err := db.DB.Exec(`UPDATE mw_cards SET pool_state = 'retired'`); err != nil {
			t.Fatal(err)
		}
		if w := e.doIssue("POST", "/admin/cdks/issue", body, "click-1"); w.Code != http.StatusBadRequest {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		e.addCard("c2")
		if w := e.doIssue("POST", "/admin/cdks/issue", body, "click-1"); w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})

	t.Run("并发的重复点击也只发一批", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		c, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "issue.db"))
		if err != nil {
			t.Fatal(err)
		}
		c.SetMaxOpenConns(25)
		old := db.DB
		db.DB = c
		t.Cleanup(func() { db.DB = old; c.Close() })
		if err := db.Migrate(); err != nil {
			t.Fatal(err)
		}
		e := setupLocalEnv(t)
		e.seedRealPayment()
		var wg sync.WaitGroup
		var ok, conflict atomic.Int32
		start := make(chan struct{})
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				switch w := e.doIssue("POST", "/admin/cdks/issue", body, "double-click"); w.Code {
				case http.StatusOK:
					ok.Add(1)
				case http.StatusConflict:
					conflict.Add(1)
				default:
					t.Errorf("%d %s", w.Code, w.Body.String())
				}
			}()
		}
		close(start)
		wg.Wait()
		if ok.Load() != 1 || conflict.Load() != 9 {
			t.Fatalf("成功 %d，冲突 %d", ok.Load(), conflict.Load())
		}
		if n := rows(); n != 2 {
			t.Fatalf("只能有一批: %d", n)
		}
	})
}

// 本地码的旧码和新码在同一个事务里换好，换码记录不能停在 disabling。人工定论 issued 之后也要能走到 done，不能去卡台禁用。
func TestLocalExchangeRecordNotStuckInDisabling(t *testing.T) {
	e := newLocalEnv(t)
	if _, err := db.DB.Exec(`INSERT INTO agent_cdk_exchanges (old_code_hash, old_upstream_id, old_code_prefix, plan, order_status, ip, state, created_at, updated_at)
		VALUES ('h-local', 0, 'MW-AAAAA', 'plus', 'not_submitted', '', 'disabling', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	retryAgentExchangeDisables(time.Now())
	if st := db.AgentCDKExchangeState("h-local"); st != db.AgentExchangeDone {
		t.Fatalf("%s", st)
	}
	if e.zovo.count() != 0 {
		t.Fatalf("不能去卡台禁用: %v", e.zovo.paths)
	}
}

// 完整码只在发码响应里出现一次，不进日志、审计、列表和库。
func TestLocalIssueCodeOnlyInResponse(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	e := newLocalEnv(t)
	e.seedRealPayment()
	w := e.do("POST", "/admin/cdks/issue", `{"provider":"marswallex","plan":"pro_20x","region":"us","count":3,"note":"批次A","funding_confirmed":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("完整码响应不能被缓存")
	}
	m := localJSON(t, w)
	issued := m["issued"].([]any)
	if len(issued) != 3 || m["stored_count"].(float64) != 0 || m["server_stored"] != false {
		t.Fatalf("%v", m)
	}
	var codes []string
	for _, it := range issued {
		row := it.(map[string]any)
		code := row["code"].(string)
		codes = append(codes, code)
		if row["full_code"] != code || row["plan"] != "pro_20x" || row["region"] != "US" || row["plan_note"] != "仅限无订阅账号" ||
			row["stored"] != false || row["has_full_code"] != true || row["provider"] != "marswallex" || row["product"] != "gpt" {
			t.Fatalf("%v", row)
		}
	}
	list := e.do("GET", "/admin/cdks", "")
	audit := localAudit(t)
	if !strings.Contains(audit, "local_cdk_issue provider=marswallex plan=pro_20x region=US count=3") {
		t.Fatalf("审计: %s", audit)
	}
	var dump strings.Builder
	rows, err := db.DB.Query(`SELECT code_hash, token_hash, code_prefix, note FROM local_cdks`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a, b, c, d string
		rows.Scan(&a, &b, &c, &d)
		dump.WriteString(a + b + c + d)
	}
	rows.Close()
	for _, code := range codes {
		for what, text := range map[string]string{"列表": list.Body.String(), "审计": audit, "日志": logs.String(), "库": dump.String(), "告警": e.alertTexts()} {
			if strings.Contains(text, code) {
				t.Fatalf("%s里有完整码", what)
			}
		}
	}
}

func TestAdminListLocalCDKs(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	_, a := e.issueCode("plus", "PH")
	_, b := e.issueCode("plus", "PH")
	_, c := e.issueCode("pro_20x", "US")
	e.insertOrder("cdk", db.LocalCDKSourceRef(a), 0, "buyer@example.com", "succeeded", "", true)
	if _, err := db.DB.Exec(`UPDATE pay_orders SET fund_cents = 3000 WHERE source = 'cdk'`); err != nil {
		t.Fatal(err)
	}
	e.insertOrder("cdk", db.LocalCDKSourceRef(b), 0, "x@example.com", "failed", "not_submitted", false)
	e.insertFundedFailure(b, "x@example.com")

	m := localJSON(t, e.do("GET", "/admin/cdks", ""))
	if m["total"].(float64) != 3 || m["page"].(float64) != 1 {
		t.Fatalf("%v", m)
	}
	list := m["list"].([]any)
	if int64(list[0].(map[string]any)["id"].(float64)) != c {
		t.Fatalf("新的在前: %v", list)
	}
	byID := map[int64]map[string]any{}
	for _, it := range list {
		row := it.(map[string]any)
		byID[int64(row["id"].(float64))] = row
		if row["code"] != nil || row["full_code"] != nil || row["code_hash"] != nil || row["token_hash"] != nil {
			t.Fatalf("列表不能有完整码或哈希: %v", row)
		}
	}
	if r := byID[a]; r["status"] != "consumed" || r["fund_cents"].(float64) != 3000 || r["order_state"] != "succeeded" || r["account_email"] != "bu***@example.com" || r["order_no"] == "" {
		t.Fatalf("%v", r)
	}
	if r := byID[b]; r["status"] != "unused" || r["precharge_failures"].(float64) != 1 || r["funded_failures"].(float64) != 1 || r["attempts_left"].(float64) != 2 {
		t.Fatalf("%v", r)
	}
	if r := byID[c]; r["status"] != "unused" || r["plan_note"] != "仅限无订阅账号" || r["order_no"] != "" || r["fund_cents"].(float64) != 0 {
		t.Fatalf("%v", r)
	}

	// 筛选和分页
	if m := localJSON(t, e.do("GET", "/admin/cdks?status=consumed", "")); m["total"].(float64) != 1 {
		t.Fatalf("%v", m)
	}
	if m := localJSON(t, e.do("GET", "/admin/cdks?plan=pro_20x", "")); m["total"].(float64) != 1 {
		t.Fatalf("%v", m)
	}
	if m := localJSON(t, e.do("GET", "/admin/cdks?provider=zovocard", "")); m["total"].(float64) != 0 {
		t.Fatalf("%v", m)
	}
	m = localJSON(t, e.do("GET", "/admin/cdks?page=2&page_size=2", ""))
	if m["total"].(float64) != 3 || len(m["list"].([]any)) != 1 {
		t.Fatalf("%v", m)
	}
	if m := localJSON(t, e.do("GET", "/admin/cdks?page=9", "")); len(m["list"].([]any)) != 0 {
		t.Fatalf("%v", m)
	}
}

func TestAdminDisableEnableLocalCDK(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, id := e.issueCode("plus", "PH")
	path := fmt.Sprintf("/admin/cdks/%d/", id)

	if w := e.do("POST", path+"disable", ""); w.Code != http.StatusOK || localJSON(t, w)["changed"] != true {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if a := localAudit(t); !strings.Contains(a, fmt.Sprintf("local_cdk_disable id=%d", id)) {
		t.Fatalf("%s", a)
	}
	// 禁用后不能预览、预检、兑换
	token := provider.TokenForCode(code)
	sessBody := localBody(t, map[string]any{"redemption_token": token, "credential": map[string]any{"mode": "session", "session": localSession("a@example.com")}})
	if w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code})); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "禁用") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", "/public/cdk/redeem", sessBody); w.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if n := cdkOrderCount(t, id); n != 0 {
		t.Fatalf("%d", n)
	}
	// 重复禁用不写审计
	if w := e.do("POST", path+"disable", ""); w.Code != http.StatusOK || localJSON(t, w)["changed"] != false {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if a := localAudit(t); strings.Count(a, "local_cdk_disable") != 1 {
		t.Fatalf("%s", a)
	}

	if w := e.do("POST", path+"enable", ""); w.Code != http.StatusOK || localJSON(t, w)["changed"] != true {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if a := localAudit(t); !strings.Contains(a, fmt.Sprintf("local_cdk_enable id=%d", id)) {
		t.Fatalf("%s", a)
	}
	if w := e.do("POST", "/public/cdk/redeem", sessBody); w.Code != http.StatusOK {
		t.Fatalf("启用后能兑: %d %s", w.Code, w.Body.String())
	}

	// 在途时禁用：单照跑，结束后如果是干净失败也不能再兑
	if w := e.do("POST", path+"disable", ""); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if m := localJSON(t, e.result(token)); m["status"] != "queued" {
		t.Fatalf("在途单不受影响: %v", m)
	}
	if _, err := db.DB.Exec(`UPDATE pay_orders SET state = 'failed', failure = 'not_submitted' WHERE source = 'cdk'`); err != nil {
		t.Fatal(err)
	}
	if w := e.do("POST", "/public/cdk/redeem", sessBody); w.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}

	// 成功、被拒的码不能禁用
	for _, st := range []struct{ state, failure string }{{"succeeded", ""}, {"failed", "declined"}} {
		_, other := e.issueCode("plus", "PH")
		e.insertOrder("cdk", db.LocalCDKSourceRef(other), 0, fmt.Sprintf("o%s@example.com", st.state), st.state, st.failure, true)
		if w := e.do("POST", fmt.Sprintf("/admin/cdks/%d/disable", other), ""); w.Code != http.StatusConflict {
			t.Fatalf("%s: %d %s", st.state, w.Code, w.Body.String())
		}
	}
	// 非法 ID、不存在
	for _, p := range []string{"/admin/cdks/abc/disable", "/admin/cdks/0/enable"} {
		if w := e.do("POST", p, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
	for _, p := range []string{"/admin/cdks/99999/disable", "/admin/cdks/99999/enable"} {
		if w := e.do("POST", p, ""); w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
}

// 就绪接口：两家状态、组合明细、能力矩阵。
func TestAdminProviderReadiness(t *testing.T) {
	e := newLocalEnv(t)
	m := localJSON(t, e.do("GET", "/admin/providers/readiness", ""))
	if m["default"] != "zovocard" {
		t.Fatalf("%v", m)
	}
	providers := m["providers"].(map[string]any)
	mw := providers["marswallex"].(map[string]any)
	if mw["ok"] != false || !strings.Contains(localBody(t, mw["problems"]), "需要一笔系统自动确认成功的真实付款") {
		t.Fatalf("硬门槛要列为原因: %v", mw)
	}
	checks := map[string]bool{}
	for _, it := range mw["checks"].([]any) {
		c := it.(map[string]any)
		checks[c["key"].(string)] = c["ok"].(bool)
	}
	if checks["first_real_payment"] || !checks["config"] || !checks["breaker"] || !checks["engine"] || !checks["wallet_sync"] {
		t.Fatalf("%v", checks)
	}
	zc := providers["zovocard"].(map[string]any)
	if zc["ok"] != true {
		t.Fatalf("%v", zc)
	}
	if det := m["zovocard"].(map[string]any); det["balance"] == nil {
		t.Fatalf("%v", det)
	}
	if !strings.Contains(localBody(t, m["capabilities"]), "仅限无订阅账号") {
		t.Fatalf("%v", m["capabilities"])
	}
	// 组合明细
	combos := m["combos"].([]any)
	if len(combos) != 2 {
		t.Fatalf("%v", combos)
	}
	first := combos[0].(map[string]any)
	if first["plan"] != "plus" || first["region"] != "PH" || first["fund_cents"].(float64) != 3000 || first["cap_minor"].(float64) != 120000 ||
		first["currency"] != "PHP" || first["cards"].(float64) != 1 || first["ok"] != true {
		t.Fatalf("%v", first)
	}

	// 过了门槛
	e.seedRealPayment()
	m = localJSON(t, e.do("GET", "/admin/providers/readiness?plan=plus&region=ph", ""))
	mw = m["providers"].(map[string]any)["marswallex"].(map[string]any)
	if mw["ok"] != true || len(m["combos"].([]any)) != 1 {
		t.Fatalf("%v", m)
	}
	// 不支持或没启用的组合
	m = localJSON(t, e.do("GET", "/admin/providers/readiness?plan=plus&region=JP", ""))
	mw = m["providers"].(map[string]any)["marswallex"].(map[string]any)
	if mw["ok"] != false || !strings.Contains(localBody(t, mw["problems"]), "未启用") {
		t.Fatalf("%v", mw)
	}
	// 全部未启用
	if err := payengine.SavePlanTable(payengine.DefaultPlanTable()); err != nil {
		t.Fatal(err)
	}
	m = localJSON(t, e.do("GET", "/admin/providers/readiness", ""))
	mw = m["providers"].(map[string]any)["marswallex"].(map[string]any)
	if mw["ok"] != false || !strings.Contains(localBody(t, mw["problems"]), "没有可用的套餐") {
		t.Fatalf("%v", mw)
	}
	// ZovoCard 没配 Key
	localSet(t, "card_api_key", "")
	m = localJSON(t, e.do("GET", "/admin/providers/readiness", ""))
	if zc := m["providers"].(map[string]any)["zovocard"].(map[string]any); zc["ok"] != false || !strings.Contains(localBody(t, zc["problems"]), "API Key") {
		t.Fatalf("%v", zc)
	}
}

// 凭证未配置时，就绪项的说明是人话，原始串只进 raw 详情字段。
func TestReadinessConfigProblemIsPlainText(t *testing.T) {
	e := newLocalEnv(t)
	localSet(t, "marswallex_developer_id", "")
	m := localJSON(t, e.do("GET", "/admin/providers/readiness", ""))
	mw := m["providers"].(map[string]any)["marswallex"].(map[string]any)
	var cfg map[string]any
	for _, it := range mw["checks"].([]any) {
		if c := it.(map[string]any); c["key"] == "config" {
			cfg = c
		}
	}
	if cfg == nil || cfg["ok"] != false {
		t.Fatalf("config 应未通过: %v", mw)
	}
	detail, _ := cfg["detail"].(string)
	if detail != "MarsWallex 凭证未配置完整，去 MarsWallex 对接页填写" {
		t.Fatalf("说明不是人话: %q", detail)
	}
	if raw, _ := cfg["raw"].(string); !strings.Contains(raw, "not_sent/config") {
		t.Fatalf("原始串应放进 raw: %q", raw)
	}
	if problems := localBody(t, mw["problems"]); strings.Contains(problems, "not_sent/") || strings.Contains(detail, "not_sent/") {
		t.Fatalf("problems 与 detail 不能含原始串: %s", problems)
	}
}

// 公开接口的主钱包同步有限频，不能让它带着打平台账户接口。
func TestPublicReadinessThrottlesWalletSync(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	base := len(e.mw.Requests("GET", mwPathAccounts))
	resetWalletSyncThrottle()
	body := localBody(t, map[string]any{"code": code, "credential": map[string]any{"mode": "session", "session": localSession("a@example.com")}})
	for i := 0; i < 5; i++ {
		if w := e.do("POST", "/public/cdk/preflight", body); w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	if n := len(e.mw.Requests("GET", mwPathAccounts)) - base; n != 1 {
		t.Fatalf("5 次预检应只同步 1 次: %d", n)
	}
}

// 代理换码的本地分支。
func TestAgentExchangeLocalCDK(t *testing.T) {
	const pw = "swap-secret-1"
	setup := func(t *testing.T) *localEnv {
		e := newLocalEnv(t)
		e.seedRealPayment()
		if err := setAgentSwapPassword(pw); err != nil {
			t.Fatal(err)
		}
		swapRateMu.Lock()
		swapRateHits = map[string][]time.Time{}
		swapRateMu.Unlock()
		return e
	}
	exchange := func(e *localEnv, code string) (int, map[string]any) {
		w := e.do("POST", "/public/cdk/exchange", localBody(t, map[string]any{"password": pw, "code": code}))
		return w.Code, localJSON(t, w)
	}

	t.Run("not_submitted 可换，旧码禁用，新码能用且继承次数", func(t *testing.T) {
		e := setup(t)
		code, id := e.issueCode("plus", "PH")
		for i := 0; i < 2; i++ {
			e.insertFundedFailure(id, fmt.Sprintf("a%d@example.com", i))
		}
		st, m := exchange(e, strings.ToLower(code))
		if st != http.StatusOK {
			t.Fatalf("%d %v", st, m)
		}
		newCode := m["new_code"].(string)
		if !strings.HasPrefix(newCode, "MW-") || newCode == code || m["plan"] != "plus" || m["payment_country"] != "PH" || m["provider"] != "marswallex" {
			t.Fatalf("%v", m)
		}
		// 旧码禁用
		old, _ := db.GetLocalCDK(context.Background(), id)
		v, _ := db.LocalCDKViewOf(context.Background(), old)
		if v.Status != db.LocalCDKDisabled || v.Reason != db.LocalReasonExchanged {
			t.Fatalf("%+v", v)
		}
		if w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code})); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "新码") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		// 被替代的旧码不能启用
		if w := e.do("POST", fmt.Sprintf("/admin/cdks/%d/enable", id), ""); w.Code != http.StatusConflict {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		// 新码继承失败次数：不能靠换码绕过重兑上限
		w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": newCode}))
		if w.Code != http.StatusOK || localJSON(t, w)["attempts_left"].(float64) != 1 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		// 同一张旧码不能换第二次
		if st, m := exchange(e, code); st != http.StatusConflict {
			t.Fatalf("%d %v", st, m)
		}
		// 记录：OldID 是 0，防止人工定论时去禁用无关的卡台码；新码 ID 是本地的
		var oldUp, newUp int64
		var state string
		if err := db.DB.QueryRow(`SELECT COALESCE(old_upstream_id, 0), COALESCE(new_upstream_id, 0), state FROM agent_cdk_exchanges`).Scan(&oldUp, &newUp, &state); err != nil {
			t.Fatal(err)
		}
		if oldUp != 0 || newUp != int64(m["new_cdk_id"].(float64)) && newUp <= 0 || state != db.AgentExchangeDone {
			t.Fatalf("%d %d %s", oldUp, newUp, state)
		}
		a := localAudit(t)
		if !strings.Contains(a, "local_cdk_exchange") || strings.Contains(a, newCode) || strings.Contains(a, code) {
			t.Fatalf("%s", a)
		}
		if e.zovo.count() != 0 {
			t.Fatalf("不应碰卡台: %v", e.zovo.paths)
		}
	})

	t.Run("换码绕不过重兑上限", func(t *testing.T) {
		e := setup(t)
		code, id := e.issueCode("plus", "PH")
		for i := 0; i < 2; i++ {
			e.insertFundedFailure(id, fmt.Sprintf("a%d@example.com", i))
		}
		st, m := exchange(e, code)
		if st != http.StatusOK {
			t.Fatalf("%d %v", st, m)
		}
		next := m["new_code"].(string)
		w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": next}))
		if localJSON(t, w)["attempts_left"].(float64) != 1 {
			t.Fatalf("%s", w.Body.String())
		}
		// 第 3 次注资后失败，新码转人工，也不能再换
		e.insertFundedFailure(int64(m["new_cdk_id"].(float64)), "z@example.com")
		if st, m := exchange(e, next); st != http.StatusBadRequest {
			t.Fatalf("%d %v", st, m)
		}
	})

	for _, tc := range []struct {
		name, state, failure string
		submitted            bool
	}{
		{"被拒的拒绝", "failed", "declined", true},
		{"待人工的拒绝", "needs_review", "", true},
		{"在途的拒绝", "funding", "", false},
		{"成功的拒绝", "succeeded", "", true},
		{"点过支付的拒绝", "failed", "not_submitted", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			code, id := e.issueCode("plus", "PH")
			e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, "a@example.com", tc.state, tc.failure, tc.submitted)
			if st, m := exchange(e, code); st != http.StatusBadRequest {
				t.Fatalf("%d %v", st, m)
			}
			var n int
			db.DB.QueryRow(`SELECT COUNT(*) FROM local_cdks WHERE exchanged_from = ?`, id).Scan(&n)
			if n != 0 {
				t.Fatal("不应发新码")
			}
			db.DB.QueryRow(`SELECT COUNT(*) FROM agent_cdk_exchanges`).Scan(&n)
			if n != 0 {
				t.Fatal("不应占位")
			}
		})
	}

	t.Run("注资之前的失败可以换，新码不带次数", func(t *testing.T) {
		e := setup(t)
		code, id := e.issueCode("plus", "PH")
		for i := 0; i < 5; i++ {
			e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, fmt.Sprintf("a%d@example.com", i), "failed", "not_submitted", false)
		}
		st, m := exchange(e, code)
		if st != http.StatusOK {
			t.Fatalf("%d %v", st, m)
		}
		w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": m["new_code"].(string)}))
		if localJSON(t, w)["attempts_left"].(float64) != db.MaxLocalCDKFundedFailures {
			t.Fatalf("%s", w.Body.String())
		}
	})
	t.Run("次数用完的不能换", func(t *testing.T) {
		e := setup(t)
		code, id := e.issueCode("plus", "PH")
		for i := 0; i < db.MaxLocalCDKFundedFailures; i++ {
			e.insertFundedFailure(id, fmt.Sprintf("a%d@example.com", i))
		}
		if st, m := exchange(e, code); st != http.StatusBadRequest {
			t.Fatalf("%d %v", st, m)
		}
	})
	t.Run("从未兑换的不能换", func(t *testing.T) {
		e := setup(t)
		code, _ := e.issueCode("plus", "PH")
		st, m := exchange(e, code)
		if st != http.StatusBadRequest || !strings.Contains(m["error"].(string), "尚无兑换失败记录") {
			t.Fatalf("%d %v", st, m)
		}
	})
	t.Run("继承了次数的新码没用过也不能再换", func(t *testing.T) {
		e := setup(t)
		code, id := e.issueCode("plus", "PH")
		e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, "a@example.com", "failed", "not_submitted", false)
		st, m := exchange(e, code)
		if st != http.StatusOK {
			t.Fatalf("%d %v", st, m)
		}
		if st, m := exchange(e, m["new_code"].(string)); st != http.StatusBadRequest {
			t.Fatalf("%d %v", st, m)
		}
	})
	t.Run("禁用的码不能换", func(t *testing.T) {
		e := setup(t)
		code, id := e.issueCode("plus", "PH")
		e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, "a@example.com", "failed", "not_submitted", false)
		if _, err := db.DisableLocalCDK(context.Background(), id, time.Now()); err != nil {
			t.Fatal(err)
		}
		if st, m := exchange(e, code); st != http.StatusBadRequest {
			t.Fatalf("%d %v", st, m)
		}
	})
	t.Run("硬门槛没过不能换", func(t *testing.T) {
		e := setup(t)
		code, id := e.issueCode("plus", "PH")
		if _, err := db.DB.Exec(`DELETE FROM pay_orders`); err != nil {
			t.Fatal(err)
		}
		e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, "a@example.com", "failed", "not_submitted", false)
		if st, m := exchange(e, code); st != http.StatusServiceUnavailable {
			t.Fatalf("%d %v", st, m)
		}
	})
	t.Run("密码错误不看码", func(t *testing.T) {
		e := setup(t)
		code, _ := e.issueCode("plus", "PH")
		w := e.do("POST", "/public/cdk/exchange", localBody(t, map[string]any{"password": "wrong-password", "code": code}))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})
	t.Run("不存在", func(t *testing.T) {
		e := setup(t)
		if st, m := exchange(e, "MW-NOPE-NOPE-NOPE"); st != http.StatusNotFound {
			t.Fatalf("%d %v", st, m)
		}
		if e.zovo.count() != 0 {
			t.Fatalf("不应碰卡台: %v", e.zovo.paths)
		}
	})
}

// 自动补卡闸的判定：默认服务商或未结清的码。
func TestMarsWallexSelectedForReplenish(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	ctx := context.Background()
	check := func(want bool) {
		t.Helper()
		got, err := provider.MarsWallexSelected(ctx)
		if err != nil || got != want {
			t.Fatalf("%v %v，期望 %v", got, err, want)
		}
	}
	check(false)
	localSet(t, provider.SettingDefault, string(provider.MarsWallex))
	check(true)
	localSet(t, provider.SettingDefault, string(provider.ZovoCard))
	check(false)
	_, id := e.issueCode("plus", "PH")
	check(true) // 还有没用的 MarsWallex 码
	e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, "a@example.com", "succeeded", "", true)
	check(false)
}
