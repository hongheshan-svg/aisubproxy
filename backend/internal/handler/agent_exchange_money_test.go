package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

const (
	exchangeOldCode  = "ZC-EXCHANGE-OLD-CODE-0001"
	exchangePassword = "swap-secret-1"
)

// exchangeUpstream 是假卡台：订单查询返回「失败且未扣款」，发码行为由 onIssue 决定。
type exchangeUpstream struct {
	srv      *httptest.Server
	issued   atomic.Int32
	disabled atomic.Int32
	// 禁用旧码的失败开关与尝试次数（MO-08）：disableFail 为真时禁用接口返回 500
	disableFail  atomic.Bool
	disableTries atomic.Int32
	// detailFail 为真时订单详情接口返回 500（E-6）
	detailFail atomic.Bool
	mu         sync.Mutex
	keys       []string
	onIssue    func(n int32, w http.ResponseWriter, r *http.Request)
	onOrders   func()
}

func issueOK(n int32, w http.ResponseWriter, _ *http.Request) {
	fmt.Fprintf(w, `{"code":0,"data":{"requested":1,"issued":[{"id":%d,"code":"ZC-EXCHANGE-NEW-CODE-%04d","plan":"plus","payment_country":"PH"}]}}`, 900+n, n)
}

func newExchangeUpstream(t *testing.T) *exchangeUpstream {
	t.Helper()
	u := &exchangeUpstream{onIssue: issueOK}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/openapi/v1/gpt-direct/cdk-orders":
			if u.onOrders != nil {
				u.onOrders()
			}
			w.Write([]byte(`{"code":0,"data":{"total":1,"list":[{"order_id":5,"status":"failed","stage":"declined","final_amount_minor":0}]}}`))
		case r.Method == "GET" && r.URL.Path == "/openapi/v1/gpt-direct/cdk-orders/5":
			if u.detailFail.Load() {
				w.WriteHeader(500)
				w.Write([]byte(`{"code":500,"msg":"boom"}`))
				return
			}
			w.Write([]byte(`{"code":0,"data":{"order":{"status":"failed","stage":"declined","final_amount_minor":0},"events":[]}}`))
		case r.Method == "POST" && r.URL.Path == "/openapi/v1/gpt-direct/cdks":
			n := u.issued.Add(1)
			u.mu.Lock()
			u.keys = append(u.keys, r.Header.Get("Idempotency-Key"))
			u.mu.Unlock()
			u.onIssue(n, w, r)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/disable"):
			u.disableTries.Add(1)
			if u.disableFail.Load() {
				w.WriteHeader(500)
				w.Write([]byte(`{"code":500,"msg":"boom"}`))
				return
			}
			u.disabled.Add(1)
			w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"code":404,"msg":"unexpected"}`))
		}
	}))
	t.Cleanup(u.srv.Close)
	if err := db.SetSetting("card_api_base", u.srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting("card_api_key", "fixture-key"); err != nil {
		t.Fatal(err)
	}
	return u
}

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

func (u *exchangeUpstream) issueKeys() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.keys...)
}

func doExchange(code, ip string) *httptest.ResponseRecorder {
	r := exchangeRouter()
	req := httptest.NewRequest("POST", "/exchange", strings.NewReader(`{"password":"`+exchangePassword+`","code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

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

// 审计 TestAuditAgentExchangeConcurrentDoubleIssue 反过来：两个请求同时通过「已换过」检查，也只发一张码。
func TestAgentExchangeConcurrentRequestsIssueOnce(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	captureOpsAlerts(t)
	u := newExchangeUpstream(t)
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	u.onOrders = func() {
		arrived <- struct{}{}
		<-release
	}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = doExchange(exchangeOldCode, "203.0.113.9").Code
		}(i)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("两个请求没有都走到订单查询这一步")
		}
	}
	close(release)
	wg.Wait()
	if (codes[0] != 200 || codes[1] != 409) && (codes[0] != 409 || codes[1] != 200) {
		t.Fatalf("应一个 200 一个 409: %v", codes)
	}
	if u.issued.Load() != 1 || u.disabled.Load() != 1 {
		t.Fatalf("发码 %d 次、禁用 %d 次，都应是 1", u.issued.Load(), u.disabled.Load())
	}
	st, newID, rows := exchangeRow(t, exchangeOldCode)
	if rows != 1 || st != db.AgentExchangeDone || newID != 901 {
		t.Fatalf("记录不符: rows=%d st=%s new=%d", rows, st, newID)
	}
}

// 真实文件库、多连接：20 个请求同时到达，只发一张。
func TestAgentExchangeConcurrentOnRealFileDB(t *testing.T) {
	c, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "ex.db"))
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
	seedExchange(t, exchangeOldCode)
	captureOpsAlerts(t)
	u := newExchangeUpstream(t)
	const n = 20
	arrived := make(chan struct{}, n)
	release := make(chan struct{})
	u.onOrders = func() {
		arrived <- struct{}{}
		<-release
	}
	var wg sync.WaitGroup
	var ok, conflict atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch doExchange(exchangeOldCode, fmt.Sprintf("203.0.113.%d", i+1)).Code {
			case 200:
				ok.Add(1)
			case 409:
				conflict.Add(1)
			}
		}(i)
	}
	for i := 0; i < n; i++ {
		select {
		case <-arrived:
		case <-time.After(20 * time.Second):
			t.Fatal("请求没有都走到订单查询这一步")
		}
	}
	close(release)
	wg.Wait()
	if ok.Load() != 1 || conflict.Load() != n-1 || u.issued.Load() != 1 || u.disabled.Load() != 1 {
		t.Fatalf("ok=%d conflict=%d issued=%d disabled=%d", ok.Load(), conflict.Load(), u.issued.Load(), u.disabled.Load())
	}
}

// 卡台明确拒绝（业务错误）：释放占位，重试能成功，两次用同一个幂等键。
func TestAgentExchangeDefiniteFailureReleasesAndRetries(t *testing.T) {
	cases := map[string]func(http.ResponseWriter){
		"400 带 error_code": func(w http.ResponseWriter) {
			w.WriteHeader(400)
			w.Write([]byte(`{"code":400,"error":"insufficient_balance","msg":"余额不足"}`))
		},
		"200 但业务码非 0": func(w http.ResponseWriter) { w.Write([]byte(`{"code":10001,"msg":"业务失败"}`)) },
		"403 权限": func(w http.ResponseWriter) {
			w.WriteHeader(403)
			w.Write([]byte(`{"code":403,"msg":"ip not allowed"}`))
		},
		"401 密钥": func(w http.ResponseWriter) { w.WriteHeader(401); w.Write([]byte(`{"msg":"bad key"}`)) },
	}
	for name, fail := range cases {
		t.Run(name, func(t *testing.T) {
			dbtest.Open(t)
			seedExchange(t, exchangeOldCode)
			alerts := captureOpsAlerts(t)
			u := newExchangeUpstream(t)
			u.onIssue = func(n int32, w http.ResponseWriter, r *http.Request) {
				if n == 1 {
					fail(w)
					return
				}
				issueOK(n, w, r)
			}
			first := doExchange(exchangeOldCode, "203.0.113.9")
			if first.Code == 200 || strings.Contains(first.Body.String(), `"outcome"`) {
				t.Fatalf("应报告确定失败: %d %s", first.Code, first.Body.String())
			}
			if st, _, rows := exchangeRow(t, exchangeOldCode); rows != 0 {
				t.Fatalf("应释放占位: rows=%d st=%s", rows, st)
			}
			if alerts.count() != 0 {
				t.Fatalf("确定失败不转人工: %v", alerts.texts())
			}
			if u.disabled.Load() != 0 {
				t.Fatal("失败时不应禁用旧码")
			}
			second := doExchange(exchangeOldCode, "203.0.113.9")
			if second.Code != 200 {
				t.Fatalf("重试应成功: %d %s", second.Code, second.Body.String())
			}
			if st, _, _ := exchangeRow(t, exchangeOldCode); st != db.AgentExchangeDone {
				t.Fatalf("状态: %s", st)
			}
			if keys := u.issueKeys(); len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
				t.Fatalf("重试应沿用同一幂等键: %v", u.issueKeys())
			}
		})
	}
}

// 结果不明：保留占位、告警、转人工，重试被拒，绝不发第二次、不禁用旧码。
func TestAgentExchangeUnknownOutcomeKeepsReservation(t *testing.T) {
	hijackClose := func(w http.ResponseWriter) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}
	cases := map[string]func(http.ResponseWriter){
		"500":        func(w http.ResponseWriter) { w.WriteHeader(500); w.Write([]byte(`{"code":500,"msg":"boom"}`)) },
		"503 无正文":    func(w http.ResponseWriter) { w.WriteHeader(503) },
		"502 网关页面":   func(w http.ResponseWriter) { w.WriteHeader(502); w.Write([]byte(`<html>bad gateway</html>`)) },
		"409 处理中":    func(w http.ResponseWriter) { w.WriteHeader(409); w.Write([]byte(`{"code":409,"msg":"processing"}`)) },
		"429 限频":     func(w http.ResponseWriter) { w.WriteHeader(429); w.Write([]byte(`{"code":429,"msg":"slow down"}`)) },
		"连接中断":       hijackClose,
		"200 空结果":    func(w http.ResponseWriter) { w.Write([]byte(`{"code":0,"data":{"requested":1,"issued":[]}}`)) },
		"200 无完整码":   func(w http.ResponseWriter) { w.Write([]byte(`{"code":0,"data":{"issued":[{"id":5,"code":""}]}}`)) },
		"200 非 JSON": func(w http.ResponseWriter) { w.Write([]byte(`ok`)) },
		"400 无业务错误":  func(w http.ResponseWriter) { w.WriteHeader(400); w.Write([]byte(`<html>bad request</html>`)) },
	}
	for name, fail := range cases {
		t.Run(name, func(t *testing.T) {
			dbtest.Open(t)
			seedExchange(t, exchangeOldCode)
			alerts := captureOpsAlerts(t)
			u := newExchangeUpstream(t)
			u.onIssue = func(_ int32, w http.ResponseWriter, _ *http.Request) { fail(w) }

			w := doExchange(exchangeOldCode, "203.0.113.9")
			if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"outcome":"unknown"`) {
				t.Fatalf("应报告结果不明: %d %s", w.Code, w.Body.String())
			}
			if st, _, rows := exchangeRow(t, exchangeOldCode); rows != 1 || st != db.AgentExchangeUnknown {
				t.Fatalf("应保留占位: rows=%d st=%s", rows, st)
			}
			if alerts.count() != 1 {
				t.Fatalf("应告警一次: %v", alerts.texts())
			}
			if strings.Contains(alerts.texts()[0], exchangeOldCode) {
				t.Fatalf("告警不应含完整卡密: %s", alerts.texts()[0])
			}

			// Go 的 Transport 对带 Idempotency-Key 的请求，复用连接被断开时会自动重放一次，
			// 卡台靠同一个键只执行一次，所以这里断言的是「键不变」而不是「只到达一次」
			sent := u.issued.Load()
			if sent == 0 {
				t.Fatal("应该发过码")
			}
			keys := u.issueKeys()
			for _, k := range keys {
				if k != keys[0] {
					t.Fatalf("所有发码请求应是同一幂等键: %v", keys)
				}
			}

			retry := doExchange(exchangeOldCode, "203.0.113.9")
			if retry.Code != http.StatusConflict {
				t.Fatalf("重试应被拒: %d %s", retry.Code, retry.Body.String())
			}
			if u.issued.Load() != sent {
				t.Fatalf("重试不应再次发码: %d -> %d", sent, u.issued.Load())
			}
			if u.disabled.Load() != 0 {
				t.Fatal("结果不明时不应禁用旧码")
			}
		})
	}
}

func TestIssueDefinitelyFailedClassification(t *testing.T) {
	dial := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"连不上（请求没发出）", &url.Error{Op: "Post", URL: "http://x", Err: dial}, true},
		{"DNS 解析失败", &url.Error{Op: "Post", URL: "http://x", Err: &net.DNSError{Err: "no such host"}}, true},
		{"401", &cardplatform.APIError{HTTPStatus: 401, Msg: "x"}, true},
		{"400 业务码", &cardplatform.APIError{HTTPStatus: 400, Code: 1001}, true},
		{"400 错误码", &cardplatform.APIError{HTTPStatus: 400, ErrorCode: "bad"}, true},
		{"404 业务码", &cardplatform.APIError{HTTPStatus: 404, Code: 404}, true},
		{"400 无业务错误", &cardplatform.APIError{HTTPStatus: 400, Msg: "invalid json"}, false},
		{"500", &cardplatform.APIError{HTTPStatus: 500, Code: 500}, false},
		{"409", &cardplatform.APIError{HTTPStatus: 409, Code: 409}, false},
		{"429", &cardplatform.APIError{HTTPStatus: 429, Code: 429}, false},
		{"已发出后读超时", &url.Error{Op: "Post", URL: "http://x", Err: &net.OpError{Op: "read", Err: errors.New("reset")}}, false},
		{"超时", &url.Error{Op: "Post", URL: "http://x", Err: context.DeadlineExceeded}, false},
		{"取消", context.Canceled, false},
		{"其他", errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := issueDefinitelyFailed(c.err); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// 客户端在发码途中断开：发码和禁用旧码不能被连带取消，否则好好的请求会变成结果不明。
func TestAgentExchangeClientDisconnectDoesNotCancelIssue(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	captureOpsAlerts(t)
	u := newExchangeUpstream(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u.onIssue = func(n int32, w http.ResponseWriter, r *http.Request) {
		cancel()
		time.Sleep(50 * time.Millisecond)
		issueOK(n, w, r)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/exchange", PublicAgentCDKExchange)
	req := httptest.NewRequest("POST", "/exchange", strings.NewReader(`{"password":"`+exchangePassword+`","code":"`+exchangeOldCode+`"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("应成功: %d %s", w.Code, w.Body.String())
	}
	if st, _, _ := exchangeRow(t, exchangeOldCode); st != db.AgentExchangeDone {
		t.Fatalf("状态: %s", st)
	}
	if u.disabled.Load() != 1 {
		t.Fatal("旧码应被禁用")
	}
}

// 超过当日上限：明确报错、告警，卡台收不到发码请求。
func TestAgentExchangeDailyLimit(t *testing.T) {
	dbtest.Open(t)
	codes := []string{"ZC-LIMIT-CODE-000001", "ZC-LIMIT-CODE-000002", "ZC-LIMIT-CODE-000003", "ZC-LIMIT-CODE-000004"}
	seedExchange(t, codes...)
	alerts := captureOpsAlerts(t)
	u := newExchangeUpstream(t)
	if err := db.SetSetting(agentSwapDailyLimitKey, "2"); err != nil {
		t.Fatal(err)
	}
	for _, code := range codes[:2] {
		if w := doExchange(code, "203.0.113.9"); w.Code != 200 {
			t.Fatalf("额度内应成功: %d %s", w.Code, w.Body.String())
		}
	}
	for _, code := range codes[2:] {
		w := doExchange(code, "203.0.113.9")
		if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "上限") {
			t.Fatalf("应超限: %d %s", w.Code, w.Body.String())
		}
		if _, _, rows := exchangeRow(t, code); rows != 0 {
			t.Fatal("超限不应占位")
		}
	}
	if u.issued.Load() != 2 {
		t.Fatalf("卡台应只收到 2 次发码: %d", u.issued.Load())
	}
	if alerts.count() != 1 {
		t.Fatalf("同一天只告警一次: %v", alerts.texts())
	}
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
	id, err := db.ReserveAgentCDKExchange(db.AgentExchangeReservation{OldHash: "h", OldID: 77, PasswordFP: "fp"}, 20, now.Add(-20*time.Minute))
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
	r.GET("/agent-exchanges", AdminListAgentExchanges)
	r.POST("/agent-exchanges/:id/resolve", AdminResolveAgentExchange)
	return r
}

func adminExchangeDo(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAdminResolveAgentExchange(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	captureOpsAlerts(t)
	u := newExchangeUpstream(t)
	u.onIssue = func(n int32, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			w.WriteHeader(500)
			w.Write([]byte(`{"code":500,"msg":"boom"}`))
			return
		}
		issueOK(n, w, r)
	}
	if w := doExchange(exchangeOldCode, "203.0.113.9"); w.Code != http.StatusBadGateway {
		t.Fatalf("首次应结果不明: %d %s", w.Code, w.Body.String())
	}
	r := adminExchangeRouter()

	list := adminExchangeDo(r, "GET", "/agent-exchanges?state=unknown", "")
	var got struct {
		List []db.AgentExchange `json:"list"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil || len(got.List) != 1 {
		t.Fatalf("列表: %s %v", list.Body.String(), err)
	}
	if strings.Contains(list.Body.String(), db.HashCDKCode(exchangeOldCode)) || strings.Contains(list.Body.String(), exchangeOldCode) {
		t.Fatalf("列表不应含旧码哈希或完整码: %s", list.Body.String())
	}
	item := got.List[0]
	if item.State != "unknown" || item.OldUpstreamID != 77 || item.Note == "" {
		t.Fatalf("行内容: %+v", item)
	}
	path := fmt.Sprintf("/agent-exchanges/%d/resolve", item.ID)

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
	if w := adminExchangeDo(r, "POST", "/agent-exchanges/9999/resolve", `{"result":"released","note":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404: %d", w.Code)
	}

	// 人工确认没发：释放，代理可重试；幂等键不变，卡台若其实发过会原样返回首次结果
	if w := adminExchangeDo(r, "POST", path, `{"result":"released","note":"卡台库存里没有"}`); w.Code != 200 {
		t.Fatalf("释放应 200: %d %s", w.Code, w.Body.String())
	}
	if w := adminExchangeDo(r, "POST", path, `{"result":"released","note":"再来一次"}`); w.Code != http.StatusNotFound {
		t.Fatalf("释放后行已删除: %d %s", w.Code, w.Body.String())
	}
	var detail string
	if err := db.DB.QueryRow(`SELECT detail FROM admin_audit_logs WHERE action = 'agent_exchange_resolve'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "released") || !strings.Contains(detail, "卡台库存里没有") {
		t.Fatalf("审计: %s", detail)
	}
	if w := doExchange(exchangeOldCode, "203.0.113.9"); w.Code != 200 {
		t.Fatalf("释放后重试应成功: %d %s", w.Code, w.Body.String())
	}
	if keys := u.issueKeys(); len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("幂等键应不变: %v", u.issueKeys())
	}
}

// 人工确认已发：记下新码并禁用旧码（否则旧码和新码都能兑换）。已经定论的不能再改。
func TestAdminResolveAgentExchangeIssuedDisablesOldCode(t *testing.T) {
	dbtest.Open(t)
	seedExchange(t, exchangeOldCode)
	captureOpsAlerts(t)
	u := newExchangeUpstream(t)
	u.onIssue = func(_ int32, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"code":500}`))
	}
	if w := doExchange(exchangeOldCode, "203.0.113.9"); w.Code != http.StatusBadGateway {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	r := adminExchangeRouter()
	var id int64
	if err := db.DB.QueryRow(`SELECT id FROM agent_cdk_exchanges`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/agent-exchanges/%d/resolve", id)
	w := adminExchangeDo(r, "POST", path, `{"result":"issued","new_cdk_id":950,"note":"卡台库存查到新码"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"old_disabled":true`) {
		t.Fatalf("应成功并禁用旧码: %d %s", w.Code, w.Body.String())
	}
	if u.disabled.Load() != 1 {
		t.Fatal("旧码应被禁用")
	}
	if db.GetCardplatformCDKStatus(77) != "disabled" {
		t.Fatal("本地状态应更新为 disabled")
	}
	st, newID, _ := exchangeRow(t, exchangeOldCode)
	if st != db.AgentExchangeDone || newID != 950 {
		t.Fatalf("%s %d", st, newID)
	}
	if w := adminExchangeDo(r, "POST", path, `{"result":"released","note":"改主意"}`); w.Code != http.StatusConflict {
		t.Fatalf("已定论的不能再改: %d %s", w.Code, w.Body.String())
	}
	if w := doExchange(exchangeOldCode, "203.0.113.9"); w.Code != http.StatusConflict {
		t.Fatalf("定论为已发后不能再换: %d %s", w.Code, w.Body.String())
	}
}

// ---- 本站码（MW-，走 localCDKExchange）的资金测试：并发只发一张、失败释放可重试、结果不明保留占位、单日上限、两种定论 ----

// adminExchangesPath 是测试路由里换码记录接口的前缀。
const adminExchangesPath = "/agent-exchanges"

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
	if e.zovo.count() != 0 {
		t.Fatalf("本站码换码不应碰卡台: %v", e.zovo.paths)
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
