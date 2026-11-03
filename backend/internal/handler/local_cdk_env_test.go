package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
)

const (
	mwPathRecharge  = "/v1/card/recharge-transaction"
	mwPathWithdraw  = "/v1/card/withdraw-transaction"
	mwPathQuery     = "/v1/card/transfer-order"
	mwPathSensitive = "/v1/card/sensitive/c1"
	mwPathAccounts  = "/v1/accounts/page"

	localTestCardNo = "4242424242424242"
	localTestCVV    = "987"
	localTestLast4  = "4242"
)

// localFakeGPT 是假 ChatGPT。subscribedFrom 大于 0 时，第 n 次 Account 起账号变成已开通 Plus。
type localFakeGPT struct {
	mu             sync.Mutex
	refreshErr     error
	subscribedFrom int
	accountCalls   int
	checkouts      int
}

func (g *localFakeGPT) Refresh(_ context.Context, s *chatgpt.Session) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refreshErr != nil {
		return g.refreshErr
	}
	s.AccessToken = "at-refreshed"
	return nil
}

func (g *localFakeGPT) Account(context.Context, chatgpt.Session) (chatgpt.Account, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.accountCalls++
	if g.subscribedFrom > 0 && g.accountCalls >= g.subscribedFrom {
		return chatgpt.Account{AccountID: "acc-1", PlanType: "plus", HasActiveSubscription: true}, nil
	}
	return chatgpt.Account{AccountID: "acc-1", PlanType: "free"}, nil
}

func (g *localFakeGPT) CreateCheckout(context.Context, chatgpt.Session, string, string, string) (chatgpt.Checkout, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checkouts++
	return chatgpt.Checkout{SessionID: "cs_live_abc", URL: "https://pay.example/c/cs_live_abc"}, nil
}

func (g *localFakeGPT) checkoutCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.checkouts
}

// localFakeBrowser 是假浏览器，每个实例共用一份计数。
type localFakeBrowser struct {
	mu      sync.Mutex
	made    int
	fills   int
	submits int
	outcome paybrowser.Outcome
}

func (b *localFakeBrowser) New(context.Context, string, string) (payengine.Browser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.made++
	return b, nil
}

func (b *localFakeBrowser) Open(context.Context, string, []paybrowser.Cookie) error { return nil }

func (b *localFakeBrowser) ReadAmount(context.Context, string) (int64, string, error) {
	return 112000, "₱1,120.00", nil
}

func (b *localFakeBrowser) Fill(context.Context, paybrowser.Card, paybrowser.Billing) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fills++
	return nil
}

func (b *localFakeBrowser) Submit(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.submits++
	return nil
}

func (b *localFakeBrowser) WaitOutcome(context.Context, time.Duration) (paybrowser.Outcome, string, error) {
	return b.outcome, "", nil
}

func (b *localFakeBrowser) EnterOTP(context.Context, string) error { return nil }

func (b *localFakeBrowser) Shot(_ context.Context, name string, _ paybrowser.ShotScope) (string, error) {
	return name + ".png", nil
}

func (b *localFakeBrowser) Close() {}

func (b *localFakeBrowser) counts() (made, fills, submits int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.made, b.fills, b.submits
}

// localZovoStub 是假 ZovoCard，记下收到的每个请求。
type localZovoStub struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	body  []string
}

const (
	zovoPreviewBody   = `{"redemption_token":"zc-tok-1","plan":"plus","upstream_only":"x"}`
	zovoPreflightBody = `{"ok":true,"preflight_token":"pf-1","email":"zc@example.com"}`
	zovoRedeemBody    = `{"status":"queued","order":{"status":"queued"}}`
	zovoResultBody    = `{"status":"running","order":{"status":"running"}}`
)

func newLocalZovoStub(t *testing.T) *localZovoStub {
	t.Helper()
	z := &localZovoStub{}
	z.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		z.mu.Lock()
		z.paths = append(z.paths, r.URL.Path)
		z.body = append(z.body, string(b))
		z.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/cdk/preview":
			io.WriteString(w, zovoPreviewBody)
		case "/api/v1/cdk/preflight":
			io.WriteString(w, zovoPreflightBody)
		case "/api/v1/cdk/redeem":
			io.WriteString(w, zovoRedeemBody)
		case "/api/v1/cdk/result":
			io.WriteString(w, zovoResultBody)
		case "/openapi/v1/gpt-direct/plans":
			io.WriteString(w, `{"code":0,"data":{"version":1,"plans":{"plus":{"enabled":true,"currency":"PHP"}},"registry":[{"key":"plus","label":"Plus"}]}}`)
		case "/openapi/v1/gpt-direct/cdks":
			io.WriteString(w, `{"code":0,"data":{"requested":1,"issued":[{"id":7001,"code":"ZC-FAKE-ISSUED-CODE-0001","plan":"plus"}]}}`)
		case "/openapi/v1/balance":
			io.WriteString(w, `{"code":0,"data":{"balance":12.5,"spendable_balance":10}}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"code":404}`)
		}
	}))
	t.Cleanup(z.srv.Close)
	return z
}

func (z *localZovoStub) count() int {
	z.mu.Lock()
	defer z.mu.Unlock()
	return len(z.paths)
}

func (z *localZovoStub) hits(path string) int {
	z.mu.Lock()
	defer z.mu.Unlock()
	n := 0
	for _, p := range z.paths {
		if p == path {
			n++
		}
	}
	return n
}

func (z *localZovoStub) requestBodies() string {
	z.mu.Lock()
	defer z.mu.Unlock()
	return strings.Join(z.body, "\n")
}

// localEnv 是真实卡池 + 真实引擎 + mwtest + 假 ChatGPT + 假浏览器。引擎默认不启动。
type localEnv struct {
	t      *testing.T
	mw     *mwtest.Server
	pool   *cardpool.Service
	engine *payengine.Service
	gpt    *localFakeGPT
	br     *localFakeBrowser
	zovo   *localZovoStub
	router *gin.Engine

	mu     sync.Mutex
	alerts []string
}

var (
	localMWKeyOnce sync.Once
	localMWKeyPEM  string
)

func localEnvTimeouts() *payengine.Timeouts {
	tm := payengine.DefaultTimeouts()
	tm.PageLoad = 5 * time.Second
	tm.Fill = 5 * time.Second
	tm.SubmitResult = 5 * time.Second
	tm.OTP = 5 * time.Second
	tm.Fund = 5 * time.Second
	tm.Verify = 10 * time.Minute
	tm.VerifyEvery = 0
	tm.DeclineGrace = 2 * time.Minute
	tm.VerifyTick = 5 * time.Millisecond
	return &tm
}

// newLocalEnv 建好内存库、MarsWallex 配置、plus/PH 套餐、一张可用的 USD 卡和有钱的主钱包。
// 硬门槛没过：要的测试自己调 seedRealPayment。
func newLocalEnv(t *testing.T) *localEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbtest.Open(t)
	return setupLocalEnv(t)
}

// setupLocalEnv 在已经打开的库上装好环境，真实并发测试用文件库时也走这里。
func setupLocalEnv(t *testing.T) *localEnv {
	t.Helper()
	dbtest.SetMasterKey(t)
	resetLocalRate()
	resetWalletSyncThrottle()
	resetOpsAlertDedupe()
	t.Cleanup(func() {
		resetLocalRate()
		resetWalletSyncThrottle()
		resetOpsAlertDedupe()
		SetOpsAlert(nil)
	})

	e := &localEnv{t: t, mw: mwtest.New(t), gpt: &localFakeGPT{}, br: &localFakeBrowser{outcome: paybrowser.OutcomeSuccess}}
	SetOpsAlert(e.addAlert)

	// ZovoCard 桩
	e.zovo = newLocalZovoStub(t)
	localSet(t, "card_api_base", e.zovo.srv.URL)
	localSet(t, "card_api_key", "fixture-key")

	// MarsWallex 配置齐全，MarsWallexClient 能构造出来
	localMWKeyOnce.Do(func() {
		pemText, err := marswallex.GeneratePrivateKeyPEM()
		if err != nil {
			t.Fatal(err)
		}
		localMWKeyPEM = pemText
	})
	localSet(t, db.SettingMarsWallexDeveloperID, "dev-local")
	localSet(t, db.SettingMarsWallexPlatformPublicKey, e.mw.PlatformPublicKeyPEM())
	if err := db.SetSecretSetting(db.SettingMarsWallexAPIToken, "token-local"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSecretSetting(db.SettingMarsWallexPrivateKey, localMWKeyPEM); err != nil {
		t.Fatal(err)
	}

	// 真实卡池 + 平台桩
	e.pool = cardpool.New(cardpool.Options{
		Client: func() (*marswallex.Client, error) { return e.mw.Client(), nil },
		Alert:  e.addAlert,
	})
	e.setWallet("100.00")
	e.mw.On("POST", mwPathRecharge, mwtest.OK(`{"out_order_no":"x","transaction_id":"T1","card_id":"c1","status":"SUCCESS"}`))
	e.mw.On("GET", mwPathQuery, mwtest.OK(`{"out_order_no":"x","transaction_id":"T1","card_id":"c1","status":"SUCCESS"}`))
	e.mw.On("POST", mwPathWithdraw, mwtest.OK(`{"out_order_no":"x","transaction_id":"T2","card_id":"c1","status":"SUCCESS"}`))
	e.mw.On("GET", mwPathSensitive, mwtest.OK(`{"card_id":"c1","card_no":"`+localTestCardNo+`","cvv2":"`+localTestCVV+`","expiry_date":"1230"}`))

	now := time.Now().Unix()
	if _, err := db.DB.Exec(`INSERT INTO mw_cardholders (card_holder_id, first_name, last_name, status, created_at)
		VALUES ('h1', 'JOHN', 'DOE', 'APPROVED', ?)`, now); err != nil {
		t.Fatal(err)
	}
	e.addCard("c1")
	pol, err := cardpool.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	pol.PlanProducts["plus"] = []string{"p1"}
	pol.PlanProducts["pro_20x"] = []string{"p1"}
	if err := cardpool.SavePolicy(pol); err != nil {
		t.Fatal(err)
	}

	tbl := payengine.DefaultPlanTable()
	for i := range tbl {
		if tbl[i].Key == "plus" {
			tbl[i].Regions["PH"] = payengine.RegionConf{Enabled: true, Currency: "PHP", CapMinor: 120000, FundCents: 3000}
		}
		if tbl[i].Key == "pro_20x" {
			tbl[i].ExpectPlanType = "pro"
			tbl[i].Regions["US"] = payengine.RegionConf{Enabled: true, Currency: "USD", CapMinor: 25000, FundCents: 3000}
		}
	}
	if err := payengine.SavePlanTable(tbl); err != nil {
		t.Fatal(err)
	}

	e.engine = payengine.New(payengine.Options{
		Pool:     e.pool,
		ChatGPT:  func(string) (payengine.ChatGPT, error) { return e.gpt, nil },
		Browser:  e.br.New,
		Alert:    e.addAlert,
		ShotDir:  t.TempDir(),
		Timeouts: localEnvTimeouts(),
	})
	SetPayEngine(e.engine)
	SetCardPool(e.pool)
	t.Cleanup(func() { SetPayEngine(nil); SetCardPool(nil) })

	e.router = localTestRouter()
	return e
}

func localTestRouter() *gin.Engine {
	r := gin.New()
	r.POST("/public/cdk/preview", PublicCDKPreview)
	r.POST("/public/cdk/preflight", PublicCDKPreflight)
	r.POST("/public/cdk/redeem", PublicCDKRedeem)
	r.GET("/public/cdk/result", PublicCDKResult)
	r.GET("/public/cdk/result-by-code", PublicCDKResultByCode)
	r.POST("/public/cdk/exchange", PublicAgentCDKExchange)
	r.POST("/public/cdk/recover-subscription", PublicCDKGraceRecovery)
	r.GET("/lookup/cdk", LookupCDKStatus)
	r.POST("/lookup/cdk/batch", LookupCDKStatusBatch)
	admin := r.Group("/admin", func(c *gin.Context) { c.Set("username", "alice") })
	admin.POST("/cardplatform/cdks", CardPlatformIssueCDKs)
	admin.POST("/cdks/issue", CardPlatformIssueCDKs)
	admin.GET("/cdks", AdminListLocalCDKs)
	admin.POST("/cdks/:id/disable", AdminDisableLocalCDK)
	admin.POST("/cdks/:id/enable", AdminEnableLocalCDK)
	admin.GET("/providers/default", AdminGetDefaultProvider)
	admin.PUT("/providers/default", AdminPutDefaultProvider)
	admin.GET("/providers/readiness", AdminProviderReadiness)
	return r
}

func localSet(t *testing.T, key, value string) {
	t.Helper()
	if err := db.SetSetting(key, value); err != nil {
		t.Fatal(err)
	}
}

func (e *localEnv) addAlert(text string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.alerts = append(e.alerts, text)
}

func (e *localEnv) alertTexts() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.alerts, "\n")
}

func (e *localEnv) setWallet(usd string) {
	e.mw.On("GET", mwPathAccounts, mwtest.Reply{Body: `{"count":"1","data":[{"account_no":"M1","account_name":"main","account_status":"ACTIVE","account_type":"MAIN","balance_list":[{"currency":"USD","available_balance":"` + usd + `"}]}]}`})
	resetWalletSyncThrottle()
}

func (e *localEnv) addCard(id string) {
	e.t.Helper()
	now := time.Now().Unix()
	if _, err := db.DB.Exec(`INSERT INTO mw_cards (card_id, card_holder_id, product_id, currency, last4, expiry, platform_status,
		source, pool_state, created_at, updated_at) VALUES (?, 'h1', 'p1', 'USD', ?, '1230', 'ACTIVE', 'opened', ?, ?, ?)`,
		id, localTestLast4, cardpool.PoolAvailable, now, now); err != nil {
		e.t.Fatal(err)
	}
}

// start 启动引擎工作协程，测试结束时停掉。
func (e *localEnv) start() {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.engine.Start(ctx)
	e.t.Cleanup(func() {
		cancel()
		e.engine.Wait()
	})
}

var localOrderSeq int

// insertOrder 直接写一张付款单。
func (e *localEnv) insertOrder(source, ref string, dry int, email, state, failure string, submitted bool) string {
	e.t.Helper()
	localOrderSeq++
	no := fmt.Sprintf("PO-LOCAL-%d", localOrderSeq)
	var sub, finished any
	now := time.Now().Unix()
	if submitted {
		sub = now
	}
	if state == "succeeded" || state == "failed" || state == "needs_review" {
		finished = now
	}
	if _, err := db.DB.Exec(`INSERT INTO pay_orders (order_no, source, source_ref, dry_run, plan_key, region, account_email, state, failure, submitted_at, finished_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'plus', 'PH', ?, ?, ?, ?, ?, ?, ?)`, no, source, ref, dry, email, state, failure, sub, finished, now, now); err != nil {
		e.t.Fatal(err)
	}
	return no
}

// seedRealPayment 写一笔真实付款成功的单：过发码硬门槛。
func (e *localEnv) seedRealPayment() {
	e.t.Helper()
	e.insertOrder("admin_test", "admin", 0, "seed@example.com", "succeeded", "", true)
}

func (e *localEnv) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

func localJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是 JSON: %s", w.Body.String())
	}
	return m
}

func localBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// issueCode 走后台接口发一张码。
func (e *localEnv) issueCode(plan, region string) (code string, id int64) {
	e.t.Helper()
	w := e.do("POST", "/admin/cdks/issue", localBody(e.t, map[string]any{
		"provider": "marswallex", "plan": plan, "region": region, "count": 1, "funding_confirmed": true,
	}))
	if w.Code != http.StatusOK {
		e.t.Fatalf("发码失败 %d %s", w.Code, w.Body.String())
	}
	it := localJSON(e.t, w)["issued"].([]any)[0].(map[string]any)
	return it["code"].(string), int64(it["id"].(float64))
}

func localSession(email string) string { return paySession(email) }

// preflight 走预览和预检，返回兑换令牌和预检令牌。
func (e *localEnv) preflight(code, session string) (token, preflight string) {
	e.t.Helper()
	w := e.do("POST", "/public/cdk/preview", localBody(e.t, map[string]any{"code": code}))
	if w.Code != http.StatusOK {
		e.t.Fatalf("预览失败 %d %s", w.Code, w.Body.String())
	}
	token = localJSON(e.t, w)["redemption_token"].(string)
	w = e.do("POST", "/public/cdk/preflight", localBody(e.t, map[string]any{
		"code": code, "redemption_token": token, "credential": map[string]any{"mode": "session", "session": session},
	}))
	if w.Code != http.StatusOK {
		e.t.Fatalf("预检失败 %d %s", w.Code, w.Body.String())
	}
	return token, localJSON(e.t, w)["preflight_token"].(string)
}

func (e *localEnv) redeem(token, preflight string) *httptest.ResponseRecorder {
	return e.do("POST", "/public/cdk/redeem", localBody(e.t, map[string]any{
		"redemption_token": token, "preflight_token": preflight, "client_request_id": "c1",
	}))
}

// result 按令牌查公开结果。
func (e *localEnv) result(token string) *httptest.ResponseRecorder {
	return e.do("GET", "/public/cdk/result?token="+token, "")
}

// waitResult 轮询公开结果直到 status 是期望值，最多 5 秒。
func (e *localEnv) waitResult(token, want string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		w := e.result(token)
		if w.Code == http.StatusOK {
			m := localJSON(e.t, w)
			if m["status"] == want {
				return m
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("结果未到 %s: %d %s", want, w.Code, w.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func cdkOrderCount(t *testing.T, id int64) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM pay_orders WHERE source = 'cdk' AND source_ref = ?`, db.LocalCDKSourceRef(id)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func localAudit(t *testing.T) string {
	t.Helper()
	return cpAudit(t, db.DB)
}
