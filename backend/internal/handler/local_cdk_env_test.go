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
	"sync/atomic"
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
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

const (
	mwPathRecharge = "/v1/card/recharge-transaction"
	mwPathWithdraw = "/v1/card/withdraw-transaction"
	mwPathQuery    = "/v1/card/transfer-order"
	mwPathAccounts = "/v1/accounts/page"

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
	cancels        int
	// creditReady 为 true 时账号是可以买点数的个人 Plus 账号（计费币种 PHP、没有欠费）
	creditReady   bool
	creditBuys    int
	creditBalance string // 非空时余额接口可用，返回它
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
	if g.creditReady {
		no := false
		return chatgpt.Account{AccountID: "acc-1", PlanType: "plus", HasActiveSubscription: true, Structure: "personal",
			BillingCurrency: "PHP", IsDelinquent: &no}, nil
	}
	if g.subscribedFrom > 0 && g.accountCalls >= g.subscribedFrom {
		return chatgpt.Account{AccountID: "acc-1", PlanType: "plus", HasActiveSubscription: true}, nil
	}
	return chatgpt.Account{AccountID: "acc-1", PlanType: "free"}, nil
}

func (g *localFakeGPT) CreateCheckout(context.Context, chatgpt.Session, string, string, string) (chatgpt.Checkout, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checkouts++
	return chatgpt.Checkout{SessionID: "cs_live_abc", URL: "https://pay.openai.com/c/pay/cs_live_abc"}, nil
}

func (g *localFakeGPT) CreateCreditCheckout(context.Context, chatgpt.Session, string, int) (chatgpt.CreditCheckoutResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checkouts++
	g.creditBuys++
	return chatgpt.CreditCheckoutResult{Tag: chatgpt.CheckoutTagHosted, SessionID: "cs_live_credit", URL: "https://checkout.stripe.com/c/pay/cs_live_credit"}, nil
}

// CancelRenewal 记次数；之后的 SubscriptionStatus 报告不再续费。
func (g *localFakeGPT) CancelRenewal(context.Context, chatgpt.Session, string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cancels++
	return nil
}

func (g *localFakeGPT) SubscriptionStatus(context.Context, chatgpt.Session, string) (chatgpt.RenewalInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	renew := g.cancels == 0
	return chatgpt.RenewalInfo{WillRenew: &renew}, nil
}

func (g *localFakeGPT) SavedCards(context.Context, chatgpt.Session, string) ([]chatgpt.SavedCard, error) {
	return []chatgpt.SavedCard{}, nil
}

func (g *localFakeGPT) CreditBalance(context.Context, chatgpt.Session, string) (chatgpt.CreditBalanceInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.creditBalance == "" {
		return chatgpt.CreditBalanceInfo{}, nil
	}
	return chatgpt.CreditBalanceInfo{Available: true, Balance: g.creditBalance}, nil
}

// creditBuyCount 是建点数结账会话的次数。
func (g *localFakeGPT) creditBuyCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.creditBuys
}

func (g *localFakeGPT) cancelCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cancels
}

func (g *localFakeGPT) setRefreshErr(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refreshErr = err
}

// setSubscribedFrom 从现在起第 n 次 Account 变成已开通。
func (g *localFakeGPT) setSubscribedFrom(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.subscribedFrom, g.accountCalls = n, 0
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

	fillErr       error
	submitErr     error // Submit 返回它时按钮没被点到
	amountChanges bool  // 填表之后金额变了
	filled        bool
	savePayChecks int // EnsureNoSavePayment 的调用次数
}

func (b *localFakeBrowser) setAmountChanges(on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.amountChanges = on
}

func (b *localFakeBrowser) setErrs(fill, submit error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fillErr, b.submitErr = fill, submit
}

func (b *localFakeBrowser) New(context.Context, string, string) (payengine.Browser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.made++
	return b, nil
}

func (b *localFakeBrowser) Open(context.Context, string, paybrowser.PageKind, []paybrowser.Cookie) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.filled = false
	return nil
}

func (b *localFakeBrowser) ReadAmount(context.Context, string) (int64, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.amountChanges && b.filled {
		return 115000, "₱1,150.00", nil
	}
	return 112000, "₱1,120.00", nil
}

func (b *localFakeBrowser) Fill(context.Context, paybrowser.Card, paybrowser.Billing) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fills++
	b.filled = b.fillErr == nil
	return b.fillErr
}

func (b *localFakeBrowser) Submit(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.submitErr != nil {
		return b.submitErr
	}
	b.submits++
	return nil
}

func (b *localFakeBrowser) WaitOutcome(context.Context, time.Duration) (paybrowser.Outcome, string, error) {
	return b.outcome, "", nil
}

func (b *localFakeBrowser) EnterOTP(context.Context, string) error { return nil }

func (b *localFakeBrowser) FillReport() paybrowser.FillReport { return paybrowser.FillReport{} }

func (b *localFakeBrowser) PayButtonState(context.Context) string { return "enabled" }

// EnsureNoSavePayment 记次数：只有点数单会调用。
func (b *localFakeBrowser) EnsureNoSavePayment(context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.savePayChecks++
	return "页面没有", nil
}

func (b *localFakeBrowser) Diagnose(context.Context) []string { return nil }

// LockSubmit 返回此前是否开始过点击。
func (b *localFakeBrowser) LockSubmit() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.submits > 0
}

func (b *localFakeBrowser) Notices() []string { return nil }

func (b *localFakeBrowser) Shot(_ context.Context, name string, _ paybrowser.ShotScope) (string, error) {
	return name + ".png", nil
}

func (b *localFakeBrowser) Close() {}

func (b *localFakeBrowser) WaitRendered(context.Context, time.Duration) bool { return true }

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
	query []string
}

// zovoPreflightBody 不带 email：真实卡台回的是它核验过的账号邮箱（与会话一致），绑定会按它记（审计 F-2）。
// 这个桩对所有会话回同一份响应，带上固定邮箱就会把每个测试账号的绑定都记成这一个。
const (
	zovoPreviewBody   = `{"redemption_token":"zc-tok-1","plan":"plus","upstream_only":"x"}`
	zovoPreflightBody = `{"ok":true,"preflight_token":"pf-1"}`
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
		z.query = append(z.query, r.URL.RawQuery)
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

// requestQueries 返回收到的所有请求的查询串（令牌走 GET 查询参数的接口用）。
func (z *localZovoStub) requestQueries() string {
	z.mu.Lock()
	defer z.mu.Unlock()
	return strings.Join(z.query, "\n")
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
	tm.RenewalTick = 5 * time.Millisecond
	return &tm
}

// waitRenewalCancelled 等这张单关闭续费并清空会话，最多 5 秒。
func (e *localEnv) waitRenewalCancelled(no string) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var renewal, sealed string
		if err := db.DB.QueryRow(`SELECT renewal_state, session_sealed FROM pay_orders WHERE order_no = ?`, no).Scan(&renewal, &sealed); err != nil {
			e.t.Fatal(err)
		}
		if renewal == "cancelled" && sealed == "" {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("单据 %s 没有关闭续费: %q 会话为空=%v", no, renewal, sealed == "")
		}
		time.Sleep(5 * time.Millisecond)
	}
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
			tbl[i].Regions["PH"] = payengine.RegionConf{Enabled: true, Currency: "PHP", CapMinor: 120000, FloorMinor: 10000, FundCents: 3000}
		}
		if tbl[i].Key == "pro_20x" {
			tbl[i].ExpectPlanType = "pro"
			tbl[i].Regions["US"] = payengine.RegionConf{Enabled: true, Currency: "USD", ExpectMinor: 2000, CapMinor: 25000, FloorMinor: 1000, FundCents: 3000}
		}
	}
	if err := payengine.SavePlanTable(tbl); err != nil {
		t.Fatal(err)
	}

	e.engine = payengine.New(payengine.Options{
		Pools:    map[string]payengine.Pool{string(provider.MarsWallex): e.pool},
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
	// 和正式路由一样挂上公开接口的中间件（限流、请求体上限）
	pub := r.Group("/public", PublicGuard())
	pub.POST("/cdk/preview", PublicCDKPreview)
	pub.POST("/cdk/preflight", PublicCDKPreflight)
	pub.POST("/cdk/redeem", PublicCDKRedeem)
	pub.GET("/cdk/result", PublicCDKResult)
	pub.GET("/cdk/result-by-code", PublicCDKResultByCode)
	pub.POST("/cdk/exchange", PublicAgentCDKExchange)
	pub.POST("/cdk/recover-subscription", PublicCDKGraceRecovery)
	lookup := r.Group("/lookup", LookupGuard())
	lookup.GET("/cdk", LookupCDKStatus)
	lookup.POST("/cdk/batch", LookupCDKStatusBatch)
	lookup.POST("/cdk/results", LookupCDKResultBatch)
	admin := r.Group("/admin", func(c *gin.Context) { c.Set("username", "alice") })
	admin.POST("/cardplatform/cdks", CardPlatformIssueCDKs)
	admin.POST("/cdks/issue", CardPlatformIssueCDKs)
	admin.GET("/cdks", AdminListLocalCDKs)
	admin.POST("/cdks/:id/disable", AdminDisableLocalCDK)
	admin.POST("/cdks/:id/enable", AdminEnableLocalCDK)
	admin.GET("/providers", AdminListProviders)
	admin.PUT("/providers/config", AdminPutProviderConfig)
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
	e.mw.On("GET", "/v1/card/sensitive/"+id, mwtest.OK(`{"card_id":"`+id+`","card_no":"`+localTestCardNo+`","cvv2":"`+localTestCVV+`","expiry_date":"1230"}`))
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

// insertOrder 直接写一张 MarsWallex 付款单。
func (e *localEnv) insertOrder(source, ref string, dry int, email, state, failure string, submitted bool) string {
	e.t.Helper()
	return e.insertProviderOrder("marswallex", source, ref, dry, email, state, failure, submitted)
}

// insertProviderOrder 直接写一张指定服务商的付款单。
func (e *localEnv) insertProviderOrder(prov, source, ref string, dry int, email, state, failure string, submitted bool) string {
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
	if _, err := db.DB.Exec(`INSERT INTO pay_orders (order_no, provider, source, source_ref, dry_run, plan_key, region, account_email, state, failure, submitted_at, finished_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'plus', 'PH', ?, ?, ?, ?, ?, ?, ?)`, no, prov, source, ref, dry, email, state, failure, sub, finished, now, now); err != nil {
		e.t.Fatal(err)
	}
	return no
}

// insertFundedFailure 写一张走到注资后失败的单：没点过支付，但已经选过卡，占重兑次数。
func (e *localEnv) insertFundedFailure(id int64, email string) {
	e.t.Helper()
	no := e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, email, "failed", "not_submitted", false)
	if _, err := db.DB.Exec(`UPDATE pay_orders SET card_id = 'c1', error_code = 'fill_failed' WHERE order_no = ?`, no); err != nil {
		e.t.Fatal(err)
	}
}

// waitOrdersFailed 等这张码有 n 张单结束为 failed，最多 5 秒。
func (e *localEnv) waitOrdersFailed(id int64, n int) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var done int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM pay_orders WHERE source = 'cdk' AND source_ref = ? AND state = 'failed'`, db.LocalCDKSourceRef(id)).Scan(&done); err != nil {
			e.t.Fatal(err)
		}
		if done >= n {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("第 %d 单没结束，现在 %d 张", n, done)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// seedRealPayment 写一笔系统自动确认成功、已关闭续费的真实开通单：过订阅开通码的发码硬门槛。
func (e *localEnv) seedRealPayment() {
	e.t.Helper()
	e.markRenewalCancelled(e.insertOrder("admin_test", "admin", 0, "seed@example.com", "succeeded", "", true))
}

// markRenewalCancelled 把一张成功的开通单记成已关闭续费并核实。
func (e *localEnv) markRenewalCancelled(no string) {
	e.t.Helper()
	if _, err := db.DB.Exec(`UPDATE pay_orders SET renewal_state = 'cancelled' WHERE order_no = ?`, no); err != nil {
		e.t.Fatal(err)
	}
}

// seedRealCreditPayment 写一笔系统自动确认成功的真实点数单：过点数码的发码硬门槛。
func (e *localEnv) seedRealCreditPayment() {
	e.t.Helper()
	no := e.insertOrder("admin_test", "admin", 0, "seed-credit@example.com", "succeeded", "", true)
	if _, err := db.DB.Exec(`UPDATE pay_orders SET kind = 'credit', plan_key = 'credit250', credit_quantity = 250 WHERE order_no = ?`, no); err != nil {
		e.t.Fatal(err)
	}
}

// do 发一个请求。发码接口每次带一个新的幂等键，等于每次都是新的一次点击；专门测幂等的用 doIssue。
func (e *localEnv) do(method, path, body string) *httptest.ResponseRecorder {
	key := ""
	if method == "POST" && (strings.HasSuffix(path, "/cdks/issue") || strings.HasSuffix(path, "/cardplatform/cdks")) {
		key = fmt.Sprintf("test-issue-key-%d", localIssueKeySeq.Add(1))
	}
	return e.doIssue(method, path, body, key)
}

var localIssueKeySeq atomic.Int64

// doIssue 带指定的幂等键发请求，key 为空时不带这个头。
func (e *localEnv) doIssue(method, path, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
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
