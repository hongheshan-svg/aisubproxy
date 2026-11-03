package handler

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// 发码一路走到兑换成功：注资和点支付都只发生一次，码变 consumed，会话清空，整条链路不碰 ZovoCard，日志里没有码和会话。
func TestLocalCDKEndToEnd(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	e := newLocalEnv(t)
	e.seedRealPayment()
	e.gpt.subscribedFrom = 2 // 第 1 次 Account 是步骤 2 的预检，还是 free
	e.start()

	const email = "buyer@example.com"
	sess := localSession(email)
	code, id := e.issueCode("plus", "PH")
	token, pf := e.preflight(code, sess)

	w := e.redeem(token, pf)
	if w.Code != http.StatusOK {
		t.Fatalf("兑换失败 %d %s", w.Code, w.Body.String())
	}
	m := localJSON(t, w)
	if m["redemption_token"] != token || m["provider"] != "marswallex" {
		t.Fatalf("%v", m)
	}
	if o := m["order"].(map[string]any); o["status"] != "queued" {
		t.Fatalf("%v", o)
	}

	res := e.waitResult(token, "completed")

	// 注资一次、点支付一次
	if n := len(e.mw.Requests("POST", mwPathRecharge)); n != 1 {
		t.Fatalf("注资 %d 次", n)
	}
	if _, fills, submits := e.br.counts(); fills != 1 || submits != 1 {
		t.Fatalf("填表 %d 次，点支付 %d 次", fills, submits)
	}
	if e.zovo.count() != 0 {
		t.Fatalf("MW 码不能碰 ZovoCard: %v", e.zovo.paths)
	}

	// 只有一张单，来源和引用正确，会话已清空
	var no, source, ref, sealed, plan, region string
	if err := db.DB.QueryRow(`SELECT order_no, source, source_ref, session_sealed, plan_key, region FROM pay_orders WHERE source = 'cdk'`).
		Scan(&no, &source, &ref, &sealed, &plan, &region); err != nil {
		t.Fatal(err)
	}
	if ref != db.LocalCDKSourceRef(id) || sealed != "" || plan != "plus" || region != "PH" {
		t.Fatalf("单据不符: ref=%s sealed=%q %s/%s", ref, sealed, plan, region)
	}

	// 码是 consumed，再兑就被拒，且不再建单
	row, _ := db.GetLocalCDK(context.Background(), id)
	v, _ := db.LocalCDKViewOf(context.Background(), row)
	if v.Status != db.LocalCDKConsumed {
		t.Fatalf("%+v", v)
	}
	if w := e.redeem(token, pf); w.Code != http.StatusConflict {
		t.Fatalf("已用的码再兑应 409: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code})); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "已使用") {
		t.Fatalf("预览已用的码: %d %s", w.Code, w.Body.String())
	}
	if n := cdkOrderCount(t, id); n != 1 {
		t.Fatalf("单数 %d", n)
	}

	// 公开结果只有白名单字段
	assertNoSecrets(t, localBody(t, res), code, sess)
	if strings.Contains(localBody(t, res), email) {
		t.Fatalf("公开结果里邮箱要脱敏: %v", res)
	}
	if o := res["order"].(map[string]any); o["account_email"] != "bu***@example.com" {
		t.Fatalf("%v", o)
	}

	// 整条链路的日志、审计、ZovoCard 请求里都没有码和会话
	for what, text := range map[string]string{"日志": logs.String(), "审计": localAudit(t), "告警": e.alertTexts()} {
		if strings.Contains(text, code) || strings.Contains(text, payAccessToken) || strings.Contains(text, paySessionToken) {
			t.Fatalf("%s里有敏感内容: %s", what, text)
		}
	}
}

// assertNoSecrets 公开响应里不能有卡号、卡尾号、CVV、代理、会话、单号这些内部内容。
func assertNoSecrets(t *testing.T, body string, extra ...string) {
	t.Helper()
	for _, s := range extra {
		if strings.Contains(body, s) {
			t.Fatalf("公开响应含 %q: %s", s, body)
		}
	}
	// 随机的令牌和码前缀可能碰巧含 987、4242，先去掉再比
	stripped := localRandomText.ReplaceAllString(body, "")
	for _, s := range []string{
		localTestCardNo, localTestCVV, localTestLast4, "card_last", "card_id", "c1\"", "proxy", "order_no", "PO-LOCAL",
		"fund_order", "lease", "checkout_url", "error_message", "error_code", "session_sealed", payAccessToken, paySessionToken,
	} {
		if strings.Contains(stripped, s) {
			t.Fatalf("公开响应含 %q: %s", s, body)
		}
	}
}

var localRandomText = regexp.MustCompile(`mwt_[0-9a-f]+|MW-[0-9A-Z-]+`)

// 公开查询不返回内部错误和时间线原文，文字来自白名单。
func TestLocalCDKPublicResponsesWhitelisted(t *testing.T) {
	const leak = "http://user:proxypass@10.0.0.1:8080 card 4242424242424242 内部错误"
	cases := []struct {
		name, state, failure, code string
		submitted                  bool
		status, text               string
	}{
		{"被拒", "failed", "declined", "payment_declined", true, "declined", "支付未成功"},
		{"待人工", "needs_review", "", "fund_unknown", true, "review", "人工核对"},
		{"会话失效", "failed", "not_submitted", "session_invalid", false, "failed_precharge", "登录已失效"},
		{"已有订阅", "failed", "not_submitted", "already_subscribed", false, "failed_precharge", "已有订阅"},
		{"其他错误码", "failed", "not_submitted", "proxy_unavailable", false, "failed_precharge", "本次未能开通"},
		{"按钮没点到", "failed", "not_submitted", "submit_not_clicked", false, "failed_precharge", "本次未能开通"},
		{"试运行拒绝", "failed", "not_submitted", "dry_run_refused", false, "failed_precharge", "本次未能开通"},
		{"注资超时", "failed", "not_submitted", "fund_timeout", false, "failed_precharge", "本次未能开通"},
		{"填表后金额变了", "failed", "not_submitted", "amount_changed", false, "failed_precharge", "本次未能开通"},
		{"被 Cloudflare 拦截", "failed", "not_submitted", "cf_challenge", false, "failed_precharge", "本次未能开通"},
		{"服务重启", "failed", "not_submitted", "service_restart", false, "failed_precharge", "本次未能开通"},
		{"在途", "funding", "", "", false, "running", "正在处理"},
		{"异常", "failed", "", "weird", false, "failed", "处理异常"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLocalEnv(t)
			e.seedRealPayment()
			code, id := e.issueCode("plus", "PH")
			no := e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, "buyer@example.com", tc.state, tc.failure, tc.submitted)
			if _, err := db.DB.Exec(`UPDATE pay_orders SET error_code = ?, error_message = ?, card_id = 'c1', card_last4 = ?, checkout_url = ? WHERE order_no = ?`,
				tc.code, leak, localTestLast4, leak, no); err != nil {
				t.Fatal(err)
			}
			if _, err := db.DB.Exec(`INSERT INTO pay_order_events (order_no, at, state, level, message) VALUES (?, 1, ?, 'info', ?)`, no, tc.state, leak); err != nil {
				t.Fatal(err)
			}
			token := provider.TokenForCode(code)
			for _, path := range []string{
				"/public/cdk/result?token=" + token,
				"/public/cdk/result-by-code?code=" + code,
			} {
				w := e.do("GET", path, "")
				if w.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
				}
				body := w.Body.String()
				if strings.Contains(body, "proxypass") || strings.Contains(body, "内部错误") || strings.Contains(body, tc.code+"\"") && tc.code != "" {
					t.Fatalf("%s 泄露内部内容: %s", path, body)
				}
				assertNoSecrets(t, body)
				m := localJSON(t, w)
				if m["status"] != tc.status || !strings.Contains(m["message"].(string), tc.text) {
					t.Fatalf("%s: %v", path, m)
				}
				if evs, ok := m["events"].([]any); !ok || len(evs) != 0 {
					t.Fatalf("时间线不能给: %v", m["events"])
				}
			}
			w := e.do("GET", "/lookup/cdk?code="+code, "")
			if strings.Contains(w.Body.String(), "proxypass") {
				t.Fatalf("查询接口泄露: %s", w.Body.String())
			}
			assertNoSecrets(t, w.Body.String())
			if m := localJSON(t, w); m["account_email"] != "bu***@example.com" {
				t.Fatalf("%v", m)
			}
		})
	}
}

// 派生状态对单据状态的归类要和引擎的终态定义一致。
func TestLocalCDKDeriveMatchesEngineStates(t *testing.T) {
	for _, st := range []string{
		payengine.StateQueued, payengine.StateChecking, payengine.StateLinkCreated, payengine.StateOpening, payengine.StateFunding,
		payengine.StateFilling, payengine.StateSubmitted, payengine.StateRequires3DS, payengine.StateVerifying,
	} {
		if payengine.IsTerminal(st) {
			t.Fatalf("%s 不该是终态", st)
		}
		v := db.DeriveLocalCDK(db.LocalCDK{Manual: db.LocalCDKManualActive}, []db.LocalCDKOrder{{State: st}})
		if v.Status != db.LocalCDKReserved || v.NeedsReview {
			t.Errorf("%s 应归为在途: %+v", st, v)
		}
		if localStage[st] == "" {
			t.Errorf("%s 没有公开阶段", st)
		}
	}
	for st, want := range map[string]string{
		payengine.StateSucceeded:   db.LocalCDKConsumed,
		payengine.StateNeedsReview: db.LocalCDKReserved,
		payengine.StateFailed:      db.LocalCDKFailed, // failure 为空
		payengine.StatePreviewed:   db.LocalCDKFailed,
	} {
		if !payengine.IsTerminal(st) {
			t.Fatalf("%s 应是终态", st)
		}
		if v := db.DeriveLocalCDK(db.LocalCDK{Manual: db.LocalCDKManualActive}, []db.LocalCDKOrder{{State: st}}); v.Status != want {
			t.Errorf("%s: %+v", st, v)
		}
	}
	if payengine.FailureNotSubmitted != "not_submitted" || payengine.FailureDeclined != "declined" {
		t.Fatal("索引和派生里写死了这两个值，改了要一起改")
	}
}

// 并发 10 次兑换同一张码，恰好 1 张单。用真实文件库和多连接：内存库只有一条连接，测不出并发问题。
func TestLocalCDKRedeemOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "local.db")
	c, err := sql.Open("sqlite3", path)
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
	code, id := e.issueCode("plus", "PH")
	token := provider.TokenForCode(code)

	var wg sync.WaitGroup
	var ok, rejected atomic.Int32
	start := make(chan struct{})
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			// 每个请求一个不同的账号，引擎的同邮箱索引帮不上忙
			w := e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{
				"redemption_token": token, "credential": map[string]any{"mode": "session", "session": localSession(fmt.Sprintf("u%d@example.com", i))},
			}))
			switch w.Code {
			case http.StatusOK:
				ok.Add(1)
			case http.StatusConflict, http.StatusServiceUnavailable:
				rejected.Add(1)
			default:
				t.Errorf("意外响应 %d %s", w.Code, w.Body.String())
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if ok.Load() != 1 || rejected.Load() != 9 {
		t.Fatalf("成功 %d，拒绝 %d", ok.Load(), rejected.Load())
	}
	if n := cdkOrderCount(t, id); n != 1 {
		t.Fatalf("应恰好 1 张单: %d", n)
	}

	// 库文件（含 WAL 和日志文件）里没有完整码（只存哈希和前缀），也没有会话明文
	files, err := filepath.Glob(path + "*")
	if err != nil || len(files) == 0 {
		t.Fatalf("%v %v", files, err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(code)) || bytes.Contains(raw, []byte(token)) || bytes.Contains(raw, []byte(paySessionToken)) || bytes.Contains(raw, []byte(payAccessToken)) {
			t.Fatalf("%s 里有完整码、令牌或会话明文", f)
		}
	}
}

// 绕过处理器里的检查和锁，直接并发调 Submit：库级索引是最后一道防线。
func TestLocalCDKOneOrderIndexStopsDirectSubmit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "direct.db"))
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
	var wg sync.WaitGroup
	var ok, conflict atomic.Int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := e.engine.Submit(context.Background(), payengine.Request{
				Provider: string(provider.MarsWallex), PlanKey: "plus", Region: "PH", Session: localSession(fmt.Sprintf("d%d@example.com", i)), Source: "cdk", SourceRef: "42",
			})
			switch {
			case err == nil:
				ok.Add(1)
			case db.IsLocalCDKOrderConflict(err):
				conflict.Add(1)
			case strings.Contains(err.Error(), "database is locked"):
				// 文件库写锁竞争，这一单没建成，不影响结论
			default:
				t.Errorf("意外错误: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatalf("应恰好 1 张单: 成功 %d，冲突 %d", ok.Load(), conflict.Load())
	}
}

// 无效会话在注资之前被拒：走到引擎 check，一分钱没动，卡和浏览器都没碰。
func TestLocalCDKInvalidSessionRejectedBeforeFunding(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	e.gpt.refreshErr = chatgpt.ErrUnauthorized
	e.start()
	code, id := e.issueCode("plus", "PH")
	token, pf := e.preflight(code, localSession("buyer@example.com"))
	if w := e.redeem(token, pf); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m := e.waitResult(token, "failed_precharge")
	if !strings.Contains(m["message"].(string), "登录已失效") {
		t.Fatalf("%v", m)
	}
	if n := len(e.mw.Requests("POST", mwPathRecharge)); n != 0 {
		t.Fatalf("不能注资: %d", n)
	}
	if made, _, submits := e.br.counts(); made != 0 || submits != 0 {
		t.Fatalf("不该开浏览器: %d %d", made, submits)
	}
	if e.gpt.checkoutCount() != 0 {
		t.Fatal("不该创建支付会话")
	}
	var state string
	db.DB.QueryRow(`SELECT state FROM pay_orders WHERE source = 'cdk' AND source_ref = ?`, db.LocalCDKSourceRef(id)).Scan(&state)
	if state != payengine.StateFailed {
		t.Fatalf("%s", state)
	}
}

// 解析不了的会话连单都不建。
func TestLocalCDKBadSessionBeforeOrder(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, id := e.issueCode("plus", "PH")
	token := provider.TokenForCode(code)
	for _, sess := range []string{"", "not-json", `{"user":{"email":"a@example.com"}}`, `{"sessionToken":"x"}`, strings.Repeat("x", localSessionMaxLen+1)} {
		for _, path := range []string{"/public/cdk/preflight", "/public/cdk/redeem"} {
			w := e.do("POST", path, localBody(t, map[string]any{"redemption_token": token, "credential": map[string]any{"mode": "session", "session": sess}}))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%s %q: %d %s", path, sess[:min(len(sess), 20)], w.Code, w.Body.String())
			}
		}
	}
	// 邮箱密码模式走不了
	w := e.do("POST", "/public/cdk/preflight", localBody(t, map[string]any{"redemption_token": token, "credential": map[string]any{"mode": "mailbox", "email": "a@example.com", "password": "p"}}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// 没有预检令牌也没带会话
	if w := e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{"redemption_token": token})); w.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if n := cdkOrderCount(t, id); n != 0 {
		t.Fatalf("不应建单: %d", n)
	}
}

// 预检令牌：篡改、过期、跨码都用不了。
func TestLocalCDKPreflightTokenGuards(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	codeA, idA := e.issueCode("plus", "PH")
	codeB, idB := e.issueCode("plus", "PH")
	tokenA, pfA := e.preflight(codeA, localSession("a@example.com"))
	tokenB := provider.TokenForCode(codeB)

	if !strings.HasPrefix(pfA, localPreflightPrefix) || strings.Contains(pfA, paySessionToken) {
		t.Fatalf("预检令牌不能带会话明文: %s", pfA)
	}
	// 篡改
	tampered := pfA[:len(pfA)-4] + "AAAA"
	if w := e.redeem(tokenA, tampered); w.Code != http.StatusBadRequest {
		t.Fatalf("篡改: %d %s", w.Code, w.Body.String())
	}
	// 没有前缀、乱写
	for _, bad := range []string{"garbage", localPreflightPrefix + "v1:AAAA", strings.Repeat("x", localPreflightMaxLen+1)} {
		if w := e.redeem(tokenA, bad); w.Code != http.StatusBadRequest {
			t.Fatalf("%q: %d %s", bad[:min(len(bad), 10)], w.Code, w.Body.String())
		}
	}
	// 跨码：A 的预检不能兑 B
	if w := e.redeem(tokenB, pfA); w.Code != http.StatusBadRequest {
		t.Fatalf("跨码: %d %s", w.Code, w.Body.String())
	}
	// 过期
	expired, err := sealLocalPreflight(idA, localSession("a@example.com"), time.Now().Add(-2*localPreflightTTL))
	if err != nil {
		t.Fatal(err)
	}
	if w := e.redeem(tokenA, expired); w.Code != http.StatusBadRequest {
		t.Fatalf("过期: %d %s", w.Code, w.Body.String())
	}
	if cdkOrderCount(t, idA) != 0 || cdkOrderCount(t, idB) != 0 {
		t.Fatal("不应建单")
	}
	// 主密钥缺失：不能降级
	t.Setenv("APP_MASTER_KEY", "")
	if w := e.do("POST", "/public/cdk/preflight", localBody(t, map[string]any{"redemption_token": tokenA, "credential": map[string]any{"mode": "session", "session": localSession("a@example.com")}})); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("无主密钥预检应拒绝: %d %s", w.Code, w.Body.String())
	}
}

// 套餐和地区只取自码本身，请求里传的不算。
func TestLocalCDKRequestCannotOverridePlan(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, id := e.issueCode("plus", "PH")
	w := e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{
		"code": code, "plan": "pro_50x", "region": "US", "plan_key": "pro_50x", "dry_run": true, "source_ref": "999",
		"credential": map[string]any{"mode": "session", "session": localSession("a@example.com")},
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var plan, region, ref string
	var dry int
	if err := db.DB.QueryRow(`SELECT plan_key, region, source_ref, dry_run FROM pay_orders WHERE source = 'cdk'`).Scan(&plan, &region, &ref, &dry); err != nil {
		t.Fatal(err)
	}
	if plan != "plus" || region != "PH" || ref != db.LocalCDKSourceRef(id) || dry != 0 {
		t.Fatalf("%s %s %s %d", plan, region, ref, dry)
	}
	// 只带码也能拿到令牌继续查进度
	if m := localJSON(t, w); m["redemption_token"] != provider.TokenForCode(code) {
		t.Fatalf("%v", m)
	}
}

// declined 不能重兑，needs_review 等人工，not_submitted 可重兑但有上限。
func TestLocalCDKRedeemByDerivedState(t *testing.T) {
	sess := localSession("buyer@example.com")
	for _, tc := range []struct {
		name, state, failure string
		submitted            bool
	}{
		{"被拒", "failed", "declined", true},
		{"待人工", "needs_review", "", true},
		{"已成功", "succeeded", "", true},
		{"在途", "funding", "", false},
		{"点过支付的失败", "failed", "not_submitted", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newLocalEnv(t)
			e.seedRealPayment()
			code, id := e.issueCode("plus", "PH")
			e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, "other@example.com", tc.state, tc.failure, tc.submitted)
			before := cdkOrderCount(t, id)
			token := provider.TokenForCode(code)
			if w := e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{"redemption_token": token, "credential": map[string]any{"mode": "session", "session": sess}})); w.Code != http.StatusConflict {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code})); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "已使用") {
				t.Fatalf("预览: %d %s", w.Code, w.Body.String())
			}
			if n := cdkOrderCount(t, id); n != before {
				t.Fatalf("不应新增单: %d", n)
			}
		})
	}
}

// 注资之前的失败不花钱，不计次数，也不转人工：连续 5 次失效会话后这张码仍可兑换，补上有效会话就能成功。
func TestLocalCDKPrechargeFailuresDoNotCount(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	e.gpt.setRefreshErr(chatgpt.ErrUnauthorized)
	e.start()
	code, id := e.issueCode("plus", "PH")
	token := provider.TokenForCode(code)
	body := localBody(t, map[string]any{"redemption_token": token, "credential": map[string]any{"mode": "session", "session": localSession("buyer@example.com")}})
	assertRetryable := func(what, text string) {
		t.Helper()
		m := e.waitResult(token, "failed_precharge")
		o := m["order"].(map[string]any)
		if o["can_retry"] != true || o["attempts_left"].(float64) != db.MaxLocalCDKFundedFailures || !strings.Contains(m["message"].(string), text) {
			t.Fatalf("%s: %v", what, m)
		}
	}
	for i := 1; i <= 5; i++ {
		if w := e.do("POST", "/public/cdk/redeem", body); w.Code != http.StatusOK {
			t.Fatalf("第 %d 次: %d %s", i, w.Code, w.Body.String())
		}
		e.waitOrdersFailed(id, i)
		assertRetryable(fmt.Sprintf("第 %d 次", i), "登录已失效")
	}
	// 被 Cloudflare 拦截也发生在注资之前：不计次数，原因不给用户看
	e.gpt.setRefreshErr(fmt.Errorf("%w: 403", chatgpt.ErrBlocked))
	if w := e.do("POST", "/public/cdk/redeem", body); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	e.waitOrdersFailed(id, 6)
	assertRetryable("Cloudflare", "本次未能开通")
	if s := e.result(token).Body.String(); strings.Contains(s, "cf_challenge") || strings.Contains(s, "Cloudflare") {
		t.Fatalf("拦截原因不能给用户: %s", s)
	}
	// 已有订阅也一样
	e.gpt.setRefreshErr(nil)
	e.gpt.setSubscribedFrom(1)
	if w := e.do("POST", "/public/cdk/redeem", body); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	e.waitOrdersFailed(id, 7)
	assertRetryable("已有订阅", "已有订阅")

	row, err := db.GetLocalCDK(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := db.LocalCDKViewOf(context.Background(), row); v.Status != db.LocalCDKUnused || v.PrechargeFailures != 7 || v.FundedFailures != 0 {
		t.Fatalf("%+v", v)
	}
	if w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code})); w.Code != http.StatusOK {
		t.Fatalf("预览还要能过: %d %s", w.Code, w.Body.String())
	}
	if n := len(e.mw.Requests("POST", mwPathRecharge)); n != 0 {
		t.Fatalf("没有一单走到注资: %d", n)
	}
	if made, _, _ := e.br.counts(); made != 0 {
		t.Fatalf("没有一单走到浏览器: %d", made)
	}
	if a := e.alertTexts(); strings.Contains(a, "人工") {
		t.Fatalf("不该转人工: %s", a)
	}

	// 同一张码换有效会话，正常走完
	e.gpt.setSubscribedFrom(2)
	if w := e.do("POST", "/public/cdk/redeem", body); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	e.waitResult(token, "completed")
	if n := len(e.mw.Requests("POST", mwPathRecharge)); n != 1 {
		t.Fatalf("注资 %d 次", n)
	}
	if _, _, submits := e.br.counts(); submits != 1 {
		t.Fatalf("点支付 %d 次", submits)
	}
}

// 3 次走到注资后失败，再兑换被拒并转人工。用真实引擎跑：填表失败、按钮没点到、填表后金额变了都发生在注资之后，都要计次数。
func TestLocalCDKFundedFailuresLimit(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	e.addCard("c2")
	e.addCard("c3")
	e.addCard("c4")
	e.br.setErrs(errors.New("填表失败"), nil)
	e.start()
	code, id := e.issueCode("plus", "PH")
	token := provider.TokenForCode(code)
	body := localBody(t, map[string]any{"redemption_token": token, "credential": map[string]any{"mode": "session", "session": localSession("buyer@example.com")}})
	for i := 1; i <= db.MaxLocalCDKFundedFailures; i++ {
		// 第 2 次是按钮没点到：它记过提交时间又清掉，紧接着的第 3 次不能被一码一单的索引挡住
		switch i {
		case 1:
			e.br.setErrs(errors.New("填表失败"), nil)
		case 2:
			e.br.setErrs(nil, errors.New("按钮没有点到"))
		case 3:
			e.br.setErrs(nil, nil)
			e.br.setAmountChanges(true)
		}
		if w := e.do("POST", "/public/cdk/redeem", body); w.Code != http.StatusOK {
			t.Fatalf("第 %d 次: %d %s", i, w.Code, w.Body.String())
		}
		e.waitOrdersFailed(id, i)
		if n := len(e.mw.Requests("POST", mwPathRecharge)); n != i {
			t.Fatalf("第 %d 次后注资 %d 次", i, n)
		}
		if i < db.MaxLocalCDKFundedFailures {
			m := e.waitResult(token, "failed_precharge")
			o := m["order"].(map[string]any)
			if o["can_retry"] != true || o["attempts_left"].(float64) != float64(db.MaxLocalCDKFundedFailures-i) {
				t.Fatalf("第 %d 次: %v", i, m)
			}
			if body := localBody(t, m); strings.Contains(body, "fill_failed") || strings.Contains(body, "submit_not_clicked") || strings.Contains(body, "amount_changed") {
				t.Fatalf("错误码不能给用户: %v", m)
			}
		}
	}
	// 三张单都选过卡、没有提交时间，错误码照实记着
	rows, err := db.DB.Query(`SELECT error_code, card_id, submitted_at FROM pay_orders WHERE source = 'cdk' AND source_ref = ? ORDER BY id`, db.LocalCDKSourceRef(id))
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for rows.Next() {
		var errCode, card string
		var submitted sql.NullInt64
		if err := rows.Scan(&errCode, &card, &submitted); err != nil {
			t.Fatal(err)
		}
		if card == "" || submitted.Valid {
			t.Fatalf("%s: 卡 %q，提交时间 %v", errCode, card, submitted)
		}
		codes = append(codes, errCode)
	}
	rows.Close()
	if got := strings.Join(codes, ","); got != "fill_failed,submit_not_clicked,amount_changed" {
		t.Fatalf("错误码 %s", got)
	}
	if _, _, submits := e.br.counts(); submits != 0 {
		t.Fatalf("没有一次真的点到: %d", submits)
	}

	m := e.waitResult(token, "failed")
	if strings.Contains(localBody(t, m), "amount_changed") || !strings.Contains(m["message"].(string), "人工") {
		t.Fatalf("%v", m)
	}
	for i := 0; i < 2; i++ {
		w := e.do("POST", "/public/cdk/redeem", body)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "人工") {
			t.Fatalf("再兑换应拒绝: %d %s", w.Code, w.Body.String())
		}
	}
	if n := cdkOrderCount(t, id); n != db.MaxLocalCDKFundedFailures {
		t.Fatalf("单数 %d", n)
	}
	if n := len(e.mw.Requests("POST", mwPathRecharge)); n != db.MaxLocalCDKFundedFailures {
		t.Fatalf("注资 %d 次", n)
	}
	// 告警一天一条，不带完整码
	a := e.alertTexts()
	if strings.Count(a, "走到注资后失败") != 1 || !strings.Contains(a, "请人工处理") || strings.Contains(a, code) {
		t.Fatalf("%s", a)
	}
	// 失败再多次也绝不换服务商重试
	if e.zovo.count() != 0 {
		t.Fatalf("不能转去 ZovoCard: %v", e.zovo.paths)
	}
}

// 注资之前的失败不计次数，全靠限流防滥用：兑换入口用完额度后，能兑的请求也被挡住、不建单，别的 IP 不受影响。
func TestLocalCDKRedeemRateLimited(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, id := e.issueCode("plus", "PH")
	token := provider.TokenForCode(code)
	bad := localBody(t, map[string]any{"redemption_token": token, "credential": map[string]any{"mode": "session", "session": "not-json"}})
	for i := 0; i < localRateLimits[localRateMutate]; i++ {
		if w := e.do("POST", "/public/cdk/redeem", bad); w.Code != http.StatusBadRequest {
			t.Fatalf("第 %d 次: %d %s", i, w.Code, w.Body.String())
		}
	}
	good := localBody(t, map[string]any{"redemption_token": token, "credential": map[string]any{"mode": "session", "session": localSession("buyer@example.com")}})
	if w := e.do("POST", "/public/cdk/redeem", good); w.Code != http.StatusTooManyRequests {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if n := cdkOrderCount(t, id); n != 0 {
		t.Fatalf("被限流不应建单: %d", n)
	}
	req := httptest.NewRequest("POST", "/public/cdk/redeem", strings.NewReader(good))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.9:1234"
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("别的 IP: %d %s", w.Code, w.Body.String())
	}
}

// 熔断暂停或未就绪时，兑换直接拒绝，不建单、不占用码；恢复后同一张码还能用。
func TestLocalCDKUnavailableRefusesWithoutOrder(t *testing.T) {
	sess := localSession("buyer@example.com")
	setups := map[string]func(e *localEnv){
		"熔断暂停": func(e *localEnv) {
			for i := 0; i < 3; i++ {
				e.insertOrder("admin_test", "admin", 0, fmt.Sprintf("x%d@example.com", i), "failed", "declined", true)
			}
		},
		"没有可用卡": func(e *localEnv) {
			if _, err := db.DB.Exec(`UPDATE mw_cards SET pool_state = 'retired'`); err != nil {
				e.t.Fatal(err)
			}
		},
		"主钱包余额不足": func(e *localEnv) { e.setWallet("1.00") },
		"平台账户接口不通": func(e *localEnv) {
			e.mw.On("GET", mwPathAccounts, mwtest.Unknown())
			resetWalletSyncThrottle()
		},
		"套餐被关": func(e *localEnv) {
			tbl := payengine.DefaultPlanTable()
			if err := payengine.SavePlanTable(tbl); err != nil {
				e.t.Fatal(err)
			}
		},
		"未配置":   func(e *localEnv) { localSet(e.t, db.SettingMarsWallexDeveloperID, "") },
		"引擎未启动": func(*localEnv) { SetPayEngine(nil) },
		"卡池未启动": func(*localEnv) { SetCardPool(nil) },
		"无主密钥":  func(e *localEnv) { e.t.Setenv("APP_MASTER_KEY", "") },
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			e := newLocalEnv(t)
			e.seedRealPayment()
			code, id := e.issueCode("plus", "PH")
			token := provider.TokenForCode(code)
			resetWalletSyncThrottle()
			setup(e)
			body := localBody(t, map[string]any{"redemption_token": token, "credential": map[string]any{"mode": "session", "session": sess}})
			for _, path := range []string{"/public/cdk/preflight", "/public/cdk/redeem"} {
				w := e.do("POST", path, body)
				if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "稍后再试") {
					t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
				}
				// 原因不能给公开接口
				for _, s := range []string{"熔断", "主钱包", "APP_MASTER_KEY", "developerId", "problems"} {
					if strings.Contains(w.Body.String(), s) {
						t.Fatalf("%s 泄露内部原因: %s", path, w.Body.String())
					}
				}
			}
			if n := cdkOrderCount(t, id); n != 0 {
				t.Fatalf("不应建单: %d", n)
			}
			// 码没被占用：状态还是未使用，预览还能过（除非引擎这类读不了的情况）
			row, err := db.GetLocalCDK(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if v, _ := db.LocalCDKViewOf(context.Background(), row); v.Status != db.LocalCDKUnused || v.AttemptsLeft != db.MaxLocalCDKFundedFailures {
				t.Fatalf("码不该被占用: %+v", v)
			}
		})
	}
}

// 硬门槛：只有系统自动确认成功的真实付款单才算，试运行单和人工定论的单都不算。没过门槛时兑换也拒绝。
func TestLocalCDKHardGateAlsoGuardsRedeem(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, id := e.issueCode("plus", "PH")
	// 把门槛那笔单删掉，模拟门槛失效的库
	if _, err := db.DB.Exec(`DELETE FROM pay_orders`); err != nil {
		t.Fatal(err)
	}
	e.insertOrder("admin_test", "admin", 1, "dry1@example.com", "succeeded", "", false)
	e.insertOrder("admin_test", "admin", 2, "dry2@example.com", "succeeded", "", false)
	manual := e.insertOrder("admin_test", "admin", 0, "manual@example.com", "succeeded", "", true)
	if _, err := db.DB.Exec(`UPDATE pay_orders SET resolved_by = 'alice' WHERE order_no = ?`, manual); err != nil {
		t.Fatal(err)
	}
	w := e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{"code": code, "credential": map[string]any{"mode": "session", "session": localSession("a@example.com")}}))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if n := cdkOrderCount(t, id); n != 0 {
		t.Fatalf("不应建单: %d", n)
	}
}

// 小写、带空白、带零宽字符的 MW 码也不能漏给 ZovoCard。
func TestLocalCDKRoutingIsCaseInsensitive(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	for _, v := range []string{strings.ToLower(code), "  " + code + "  ", "\xe2\x80\x8b" + code + "\xef\xbb\xbf"} {
		w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": v}))
		if w.Code != http.StatusOK || localJSON(t, w)["redemption_token"] != provider.TokenForCode(code) {
			t.Fatalf("%q: %d %s", v, w.Code, w.Body.String())
		}
	}
	// 不存在的 MW 码、错的 mwt 令牌，就地 404
	for _, w := range []struct {
		method, path, body string
	}{
		{"POST", "/public/cdk/preview", `{"code":"MW-NOPE"}`},
		{"POST", "/public/cdk/preflight", `{"code":"mw-nope","credential":{"session":"x"}}`},
		{"POST", "/public/cdk/redeem", `{"redemption_token":"mwt_nope"}`},
		{"GET", "/public/cdk/result?token=mwt_nope", ""},
		{"GET", "/public/cdk/result-by-code?code=MW-NOPE", ""},
	} {
		if r := e.do(w.method, w.path, w.body); r.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", w.path, r.Code, r.Body.String())
		}
	}
	// mwt 令牌配 ZovoCard 码、MW 码配 ZovoCard 令牌，都不能发往 ZovoCard
	for _, body := range []string{
		`{"code":"ZC-AAAA","redemption_token":"mwt_nope","credential":{"session":"x"}}`,
		localBody(t, map[string]any{"code": code, "redemption_token": "zc-tok-1", "credential": map[string]any{"session": "x"}}),
	} {
		for _, path := range []string{"/public/cdk/preflight", "/public/cdk/redeem"} {
			e.do("POST", path, body)
		}
	}
	// 宽限期恢复
	r := e.do("POST", "/public/cdk/recover-subscription", `{"redemption_token":"mwt_abc","preflight_token":"mwp_x","confirmed":true}`)
	if r.Code != http.StatusBadRequest {
		t.Fatalf("宽限期: %d %s", r.Code, r.Body.String())
	}
	// 账单查询凭 MW 码：查不到绑定，不发往任何上游
	billing := gin.New()
	billing.POST("/billing", SessionBillingCheck)
	if w := cdkRequest(SessionBillingCheck, "POST", "/billing", localBody(t, map[string]any{"cdk_code": code})); w.Code != http.StatusNotFound {
		t.Fatalf("账单: %d %s", w.Code, w.Body.String())
	}
	if e.zovo.count() != 0 {
		t.Fatalf("不应碰 ZovoCard: %v", e.zovo.paths)
	}
	if strings.Contains(e.zovo.requestBodies(), code) {
		t.Fatal("码发往了 ZovoCard")
	}
	// 预览绝不写绑定表（BindCDKRedemptionToken 会存完整码明文）
	var n int
	db.DB.QueryRow(`SELECT COUNT(*) FROM cdk_session_bindings`).Scan(&n)
	if n != 0 {
		t.Fatalf("MW 码不能进绑定表: %d", n)
	}
}

// 分流只看码：ZovoCard 码的处理和响应字段完全不变，MW 码按本地走。
func TestRedeemRoutesByCDKProvider(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	// ZovoCard 码：透传上游字节，不建付款单，也没有 provider 字段。
	sess := localSession("zc@example.com")
	for _, tc := range []struct {
		method, path, body, want string
	}{
		{"POST", "/public/cdk/preview", `{"code":"ZC-FAKE-0001"}`, zovoPreviewBody},
		{"POST", "/public/cdk/preflight", localBody(t, map[string]any{"code": "ZC-FAKE-0001", "redemption_token": "zc-tok-1", "credential": map[string]any{"mode": "session", "session": sess}}), zovoPreflightBody},
		{"POST", "/public/cdk/redeem", `{"redemption_token":"zc-tok-1","preflight_token":"pf-1"}`, zovoRedeemBody},
		{"GET", "/public/cdk/result?token=zc-tok-1", "", zovoResultBody},
	} {
		w := e.do(tc.method, tc.path, tc.body)
		if w.Code != http.StatusOK || w.Body.String() != tc.want {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	for _, p := range []string{"preview", "preflight", "redeem", "result"} {
		if e.zovo.hits("/api/v1/cdk/"+p) != 1 {
			t.Fatalf("%s 应打到 ZovoCard 一次: %v", p, e.zovo.paths)
		}
	}
	var orders int
	db.DB.QueryRow(`SELECT COUNT(*) FROM pay_orders WHERE source = 'cdk'`).Scan(&orders)
	if orders != 0 {
		t.Fatalf("ZovoCard 码不应建付款单: %d", orders)
	}

	// MW 码：ZovoCard 桩收不到新请求，建一张单
	before := e.zovo.count()
	code, id := e.issueCode("plus", "PH")
	token, pf := e.preflight(code, localSession("mw@example.com"))
	if w := e.redeem(token, pf); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := e.result(token); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if e.zovo.count() != before {
		t.Fatalf("MW 码碰了 ZovoCard: %v", e.zovo.paths)
	}
	if n := cdkOrderCount(t, id); n != 1 {
		t.Fatalf("%d", n)
	}
}

// 预览的字段前端要用，也不该多给。
func TestLocalCDKPreviewFields(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, _ := e.issueCode("pro_20x", "US")
	w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code}))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m := localJSON(t, w)
	for k, want := range map[string]any{
		"provider": "marswallex", "plan": "pro_20x", "plan_type": "pro_20x", "product": "gpt", "plan_flow": "direct",
		"region": "US", "payment_country": "US", "currency": "USD", "status": "unused", "plan_note": "仅限无订阅账号",
		"requires_no_subscription": true, "attempts_left": float64(db.MaxLocalCDKFundedFailures),
	} {
		if m[k] != want {
			t.Errorf("%s: %v，期望 %v", k, m[k], want)
		}
	}
	assertNoSecrets(t, w.Body.String())
	// 预览是只读的：多次预览令牌不变，单也不建
	again := localJSON(t, e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code})))
	if again["redemption_token"] != m["redemption_token"] {
		t.Fatal("令牌要稳定")
	}
	// plus 没有那句备注
	code2, _ := e.issueCode("plus", "PH")
	if m := localJSON(t, e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code2}))); m["plan_note"] != nil {
		t.Fatalf("%v", m)
	}
}

// 预检响应不能带 plan：前端把它当账号当前套餐，会误判「已满足」。
func TestLocalCDKPreflightOmitsCurrentPlan(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	token := provider.TokenForCode(code)
	w := e.do("POST", "/public/cdk/preflight", localBody(t, map[string]any{"code": code, "redemption_token": token, "credential": map[string]any{"mode": "session", "session": localSession("Buyer@Example.com")}}))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m := localJSON(t, w)
	for _, k := range []string{"plan", "currentPlan", "current_plan", "subscription_has_active", "code"} {
		if _, ok := m[k]; ok {
			t.Fatalf("不该有 %s: %v", k, m)
		}
	}
	if m["email"] != "buyer@example.com" || m["preflight_token"] == "" {
		t.Fatalf("%v", m)
	}
}

// PR-7：跨服务商同账号在途互斥，两个方向。
func TestCrossProviderInflightBlocked(t *testing.T) {
	const email = "both@example.com"
	sess := localSession(email)
	zcPreflight := func(s string) string {
		return localBody(t, map[string]any{"code": "ZC-FAKE-0001", "redemption_token": "zc-tok-1", "credential": map[string]any{"mode": "session", "session": s}})
	}

	t.Run("MarsWallex 在途时 ZovoCard 预检和兑换被拒", func(t *testing.T) {
		for _, state := range []string{payengine.StateQueued, payengine.StateFunding, payengine.StateVerifying, payengine.StateNeedsReview} {
			e := newLocalEnv(t)
			e.insertOrder("cdk", "1", 0, email, state, "", false)
			// 大小写和空白不影响
			w := e.do("POST", "/public/cdk/preflight", zcPreflight(localSession("  BOTH@Example.COM ")))
			if w.Code != http.StatusConflict {
				t.Fatalf("%s 预检: %d %s", state, w.Code, w.Body.String())
			}
			if e.zovo.hits("/api/v1/cdk/preflight") != 0 {
				t.Fatalf("%s: 不应转发", state)
			}
			// 兑换靠请求自带的凭据、或令牌绑定的邮箱判断
			w = e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{"redemption_token": "zc-tok-1", "credential": map[string]any{"mode": "session", "session": sess}}))
			if w.Code != http.StatusConflict {
				t.Fatalf("%s 兑换: %d %s", state, w.Code, w.Body.String())
			}
			if err := db.BindCDKSession("zovocard", "ZC-FAKE-0001", "zc-tok-1", sess); err != nil {
				t.Fatal(err)
			}
			w = e.do("POST", "/public/cdk/redeem", `{"redemption_token":"zc-tok-1","preflight_token":"pf-1"}`)
			if w.Code != http.StatusConflict {
				t.Fatalf("%s 按令牌兑换: %d %s", state, w.Code, w.Body.String())
			}
			if e.zovo.hits("/api/v1/cdk/redeem") != 0 {
				t.Fatalf("%s: 不应转发", state)
			}
			if strings.Contains(w.Body.String(), "provider") {
				t.Fatalf("响应不该透露服务商: %s", w.Body.String())
			}
		}
	})

	t.Run("终态和试运行的单不拦 ZovoCard", func(t *testing.T) {
		for _, tc := range []struct {
			state, failure string
			dry            int
		}{{"succeeded", "", 0}, {"failed", "not_submitted", 0}, {"failed", "declined", 0}, {"queued", "", 1}, {"previewed", "", 1}} {
			e := newLocalEnv(t)
			e.insertOrder("cdk", "1", tc.dry, email, tc.state, tc.failure, false)
			w := e.do("POST", "/public/cdk/preflight", zcPreflight(sess))
			if w.Code != http.StatusOK || w.Body.String() != zovoPreflightBody {
				t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
			}
		}
	})

	t.Run("其他账号和没有邮箱的凭据不受影响", func(t *testing.T) {
		e := newLocalEnv(t)
		e.insertOrder("cdk", "1", 0, email, "queued", "", false)
		if w := e.do("POST", "/public/cdk/preflight", zcPreflight(localSession("other@example.com"))); w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		// X 的 auth_token、Grok 凭据没有 ChatGPT 账号
		if w := e.do("POST", "/public/cdk/preflight", zcPreflight("6eac90ba41206ea277d1dd308d4b28c3f441094f")); w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		// 邮箱密码模式按邮箱判断
		w := e.do("POST", "/public/cdk/preflight", localBody(t, map[string]any{"code": "ZC-1", "redemption_token": "zc-2", "credential": map[string]any{"mode": "mailbox", "email": email, "password": "p"}}))
		if w.Code != http.StatusConflict {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	})

	t.Run("ZovoCard 绑定 15 分钟内 MarsWallex 被拒", func(t *testing.T) {
		e := newLocalEnv(t)
		e.seedRealPayment()
		// 走真实的 ZovoCard 预检，绑定由它写入（密文）
		if w := e.do("POST", "/public/cdk/preflight", zcPreflight(localSession(" BOTH@example.com "))); w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		code, id := e.issueCode("plus", "PH")
		token := provider.TokenForCode(code)
		body := localBody(t, map[string]any{"redemption_token": token, "credential": map[string]any{"mode": "session", "session": sess}})
		for _, path := range []string{"/public/cdk/preflight", "/public/cdk/redeem"} {
			if w := e.do("POST", path, body); w.Code != http.StatusConflict {
				t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
			}
		}
		if n := cdkOrderCount(t, id); n != 0 {
			t.Fatalf("不应建单: %d", n)
		}
		// 复用现有加密字段，绑定表里没有明文邮箱
		var payload, sealed string
		if err := db.DB.QueryRow(`SELECT session_payload, account_email_sealed FROM cdk_session_bindings WHERE cdk_code = 'ZC-FAKE-0001'`).Scan(&payload, &sealed); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(payload+sealed), email) || sealed == "" {
			t.Fatalf("邮箱不能存明文: %q %q", payload, sealed)
		}
		// 窗口过了就放行
		if _, err := db.DB.Exec(`UPDATE cdk_session_bindings SET session_bound_at = ?`, time.Now().Add(-crossProviderWindow-time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
		if w := e.do("POST", "/public/cdk/redeem", body); w.Code != http.StatusOK {
			t.Fatalf("窗口外: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("同账号待人工单拦 MarsWallex", func(t *testing.T) {
		e := newLocalEnv(t)
		e.seedRealPayment()
		e.insertOrder("admin_test", "admin", 0, email, payengine.StateNeedsReview, "", true)
		code, id := e.issueCode("plus", "PH")
		w := e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{"code": code, "credential": map[string]any{"mode": "session", "session": sess}}))
		if w.Code != http.StatusConflict {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if n := cdkOrderCount(t, id); n != 0 {
			t.Fatalf("%d", n)
		}
	})

	t.Run("解不开绑定时拒绝，不能当成没有绑定", func(t *testing.T) {
		e := newLocalEnv(t)
		e.seedRealPayment()
		code, id := e.issueCode("plus", "PH")
		if _, err := db.DB.Exec(`INSERT INTO cdk_session_bindings (cdk_code, session_payload, redemption_token, account_email_sealed, session_bound_at, updated_at)
			VALUES ('ZC-BROKEN', '', 'x', 'v1:not-a-real-ciphertext', ?, CURRENT_TIMESTAMP)`, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		w := e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{"code": code, "credential": map[string]any{"mode": "session", "session": sess}}))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		if n := cdkOrderCount(t, id); n != 0 {
			t.Fatalf("%d", n)
		}
	})

	t.Run("账号锁：ZovoCard 转发期间 MarsWallex 进不来，反过来也一样", func(t *testing.T) {
		release, ok := holdAccountShared(email)
		if !ok {
			t.Fatal("共享锁")
		}
		rel2, ok := holdAccountShared(email)
		if !ok {
			t.Fatal("ZovoCard 之间不互斥")
		}
		if _, ok := holdAccountExclusive(email); ok {
			t.Fatal("有共享锁时不能取独占锁")
		}
		release()
		if _, ok := holdAccountExclusive(email); ok {
			t.Fatal("还有一把共享锁")
		}
		rel2()
		rel3, ok := holdAccountExclusive(email)
		if !ok {
			t.Fatal("独占锁")
		}
		if _, ok := holdAccountShared(email); ok {
			t.Fatal("有独占锁时不能取共享锁")
		}
		if _, ok := holdAccountExclusive(email); ok {
			t.Fatal("独占锁不能重入")
		}
		rel3()
		accountHoldMu.Lock()
		defer accountHoldMu.Unlock()
		if len(accountHolds) != 0 {
			t.Fatalf("释放后不该留记录: %d", len(accountHolds))
		}
	})
}

// 查询接口的本地分支。
func TestLocalCDKLookup(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, id := e.issueCode("plus", "PH")
	get := func() map[string]any {
		t.Helper()
		w := e.do("GET", "/lookup/cdk?code="+code, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		return localJSON(t, w)
	}
	if m := get(); m["status"] != "unused" || m["used"] != false || m["can_resubmit"] != false || m["plan"] != "plus" {
		t.Fatalf("%v", m)
	}
	ref := db.LocalCDKSourceRef(id)
	no := e.insertOrder("cdk", ref, 0, "buyer@example.com", "failed", "not_submitted", false)
	if m := get(); m["status"] != "unused" || m["can_resubmit"] != true {
		t.Fatalf("%v", m)
	}
	if _, err := db.DB.Exec(`UPDATE pay_orders SET state = 'failed', failure = 'declined', submitted_at = 1 WHERE order_no = ?`, no); err != nil {
		t.Fatal(err)
	}
	if m := get(); m["status"] != "failed" || m["can_resubmit"] != false {
		t.Fatalf("%v", m)
	}
	if _, err := db.DB.Exec(`UPDATE pay_orders SET state = 'needs_review', failure = '' WHERE order_no = ?`, no); err != nil {
		t.Fatal(err)
	}
	if m := get(); m["status"] != "processing" {
		t.Fatalf("%v", m)
	}
	if _, err := db.DB.Exec(`UPDATE pay_orders SET state = 'succeeded', finished_at = 1700000000 WHERE order_no = ?`, no); err != nil {
		t.Fatal(err)
	}
	m := get()
	if m["status"] != "used" || m["used"] != true || m["used_at"] != "2023-11-14T22:13:20Z" || m["account_email"] != "bu***@example.com" {
		t.Fatalf("%v", m)
	}

	// 批量：MW 和 ZovoCard 混在一起，MW 码不发往 ZovoCard，ZovoCard 走原路径
	code2, id2 := e.issueCode("plus", "PH")
	if _, err := db.DisableLocalCDK(context.Background(), id2, time.Now()); err != nil {
		t.Fatal(err)
	}
	w := e.do("POST", "/lookup/cdk/batch", localBody(t, map[string]any{"codes": []string{code, code2, "MW-NOPE"}}))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"status":"used"`) || !strings.Contains(body, `"status":"disabled"`) || !strings.Contains(body, `"status":"unknown"`) {
		t.Fatal(body)
	}
	if e.zovo.count() != 0 {
		t.Fatalf("不应碰 ZovoCard: %v", e.zovo.paths)
	}
	// 不存在：404
	if w := e.do("GET", "/lookup/cdk?code=MW-NOPE", ""); w.Code != http.StatusNotFound {
		t.Fatalf("%d", w.Code)
	}
}

// 公开接口限流：MW 分支有，ZovoCard 分支不受影响。
func TestLocalCDKRateLimit(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	for i := 0; i < localRateLimits[localRateMutate]; i++ {
		if w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code})); w.Code != http.StatusOK {
			t.Fatalf("第 %d 次: %d", i, w.Code)
		}
	}
	for _, r := range []struct{ path, body string }{
		{"/public/cdk/preview", localBody(t, map[string]any{"code": code})},
		{"/public/cdk/preflight", localBody(t, map[string]any{"code": code})},
		{"/public/cdk/redeem", localBody(t, map[string]any{"code": code})},
	} {
		if w := e.do("POST", r.path, r.body); w.Code != http.StatusTooManyRequests {
			t.Fatalf("%s: %d", r.path, w.Code)
		}
	}
	// 结果查询是另一个桶，额度大得多
	if w := e.result(provider.TokenForCode(code)); w.Code == http.StatusTooManyRequests {
		t.Fatal("轮询不该被变更接口的额度挡住")
	}
	if localRateLimits[localRateQuery] < 3600/3*5 {
		t.Fatal("结果接口的额度要够 3 秒一次、同时轮询多张码")
	}
	// ZovoCard 码不走这个限流
	if w := e.do("POST", "/public/cdk/preview", `{"code":"ZC-FAKE-0001"}`); w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
}

// 公开接口的令牌只有哈希入库：只知道入库哈希推不出能用的令牌。
func TestLocalCDKOnlyHashesStored(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, id := e.issueCode("plus", "PH")
	token := provider.TokenForCode(code)
	var codeHash, tokenHash, prefix string
	if err := db.DB.QueryRow(`SELECT code_hash, token_hash, code_prefix FROM local_cdks WHERE id = ?`, id).Scan(&codeHash, &tokenHash, &prefix); err != nil {
		t.Fatal(err)
	}
	if codeHash != db.HashCDKCode(code) || tokenHash != provider.HashToken(token) || prefix != code[:8] {
		t.Fatalf("%s %s %s", codeHash, tokenHash, prefix)
	}
	// 拿入库哈希当令牌用不了。不带 mwt_ 的不是本地令牌，交给 ZovoCard 判断，这里只要没有读到本地的单就行
	for _, tok := range []string{"mwt_" + tokenHash, "mwt_" + codeHash, tokenHash, codeHash} {
		w := e.result(tok)
		if strings.HasPrefix(tok, "mwt_") && w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", tok, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "marswallex") {
			t.Fatalf("%s 读到了本地的单: %s", tok, w.Body.String())
		}
	}
}

// 测试用错误，确认映射不泄露内部信息。
func TestLocalCDKSubmitErrorMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := errors.New("sqlite: internal secret detail")
	for _, tc := range []struct {
		err  error
		code int
	}{
		{payengine.ErrInflight, http.StatusConflict},
		{payengine.ErrBadSession, http.StatusBadRequest},
		{payengine.ErrPlanUnavailable, http.StatusServiceUnavailable},
		{secret, http.StatusServiceUnavailable},
	} {
		w := httpRecorderFor(func(c *gin.Context) { localCDKSubmitFailed(c, db.LocalCDK{ID: 1}, tc.err) })
		if w.Code != tc.code || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("%v: %d %s", tc.err, w.Code, w.Body.String())
		}
	}
}

// 引擎结果是 needs_review 时公开接口报 review，码占着等人工，不会自动重试。
func TestLocalCDKUnknownOutcomeWaitsForReview(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	e.gpt.subscribedFrom = 0
	e.br.outcome = paybrowser.OutcomeUnknown
	code, id := e.issueCode("plus", "PH")
	token, pf := e.preflight(code, localSession("buyer@example.com"))
	if w := e.redeem(token, pf); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// 引擎没启动，手工把单改成待人工：这里测的是码的反应
	if _, err := db.DB.Exec(`UPDATE pay_orders SET state = 'needs_review', submitted_at = 1 WHERE source = 'cdk' AND source_ref = ?`, db.LocalCDKSourceRef(id)); err != nil {
		t.Fatal(err)
	}
	if m := localJSON(t, e.result(token)); m["status"] != "review" {
		t.Fatalf("%v", m)
	}
	if w := e.redeem(token, pf); w.Code != http.StatusConflict {
		t.Fatalf("待人工时不能重兑: %d %s", w.Code, w.Body.String())
	}
	// 人工定论失败：不能重兑
	var no string
	db.DB.QueryRow(`SELECT order_no FROM pay_orders WHERE source = 'cdk'`).Scan(&no)
	if err := e.engine.Resolve(context.Background(), no, payengine.StateFailed, "核对未扣款", "alice"); err != nil {
		t.Fatal(err)
	}
	if m := localJSON(t, e.result(token)); m["status"] != "declined" {
		t.Fatalf("%v", m)
	}
	if w := e.redeem(token, pf); w.Code != http.StatusConflict {
		t.Fatalf("定论失败后不能重兑: %d %s", w.Code, w.Body.String())
	}
	if n := cdkOrderCount(t, id); n != 1 {
		t.Fatalf("%d", n)
	}
}
