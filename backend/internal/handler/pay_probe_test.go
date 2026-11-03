package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

const (
	probeEmail    = "real.person@example.com"
	probeNewToken = "fresh-access-token-after-refresh-9876543210"
	probePortal   = "https://billing.stripe.com/p/session/live_SECRETPORTALTOKEN123"
	probePayURL   = "https://pay.openai.com/c/pay/cs_live_SECRETCHECKOUTID999#frag"
)

// probeFake 是假的 chatgpt.com：记录收到的每个请求，按路径给出固定的返回。
type probeFake struct {
	mu      sync.Mutex
	hits    []string
	hasSub  bool
	planTyp string
}

func (f *probeFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits = append(f.hits, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	switch r.URL.Path {
	case "/api/auth/session":
		w.Write([]byte(`{"accessToken":"` + probeNewToken + `"}`))
	case "/backend-api/accounts/check/v4-2023-04-27":
		plan := f.planTyp
		if plan == "" {
			plan = "plus"
		}
		w.Write([]byte(`{"default_account":"a1","accounts":{"a1":{"account":{"account_id":"acc-1234-5678-abcd","plan_type":"` + plan + `","structure":"personal"},` +
			`"entitlement":{"has_active_subscription":` + map[bool]string{true: "true", false: "false"}[f.hasSub] + `,"billing_period":"monthly"},` +
			`"last_active_subscription":{"will_renew":true,"purchase_origin_platform":"chatgpt_web"}}}}`))
	case "/backend-api/subscriptions":
		w.Write([]byte(`{"will_renew":true,"renews_at":"2099-01-01","billing_period":"monthly","is_delinquent":false,"status":"active, owner ` + probeEmail + ` ` + probePayURL + `","detail":{"nested":1}}`))
	case "/backend-api/payments/checkout":
		if req["checkout_ui_mode"] == "redirect" {
			w.Write([]byte(`{"checkout_session_id":"cs_live_SECRETCHECKOUTID999","url":"` + probePayURL + `","currency":"php"}`))
			return
		}
		w.Write([]byte(`{"tag":"custom_checkout_session","processor_entity":"openai_llc","checkout_session_id":"cs_live_SECRETCHECKOUTID999"}`))
	case "/backend-api/payments/payment_methods":
		w.Write([]byte(`{"payment_methods":[{"id":"pm_1ABCDEFGHIJKLMNOPQR","card":{"last4":"4242"}}],"default_payment_method_id":"pm_1ABCDEFGHIJKLMNOPQR","backup_payment_method_enabled":true}`))
	case "/backend-api/subscriptions/update/preview":
		w.Write([]byte(`{"amount_due":{"amount":18000,"tax_amount":0},"currency":"usd","renewal_date":"2099-02-01","default_payment_method":{"card_brand":"visa","card_last4":"4242"}}`))
	case "/backend-api/payments/customer_portal":
		w.Write([]byte(`{"url":"` + probePortal + `"}`))
	case "/backend-api/wham/usage":
		w.Write([]byte(`{"plan_type":"plus","credits":{"balance":12,"has_credits":true},"rate_limit":{"used_percent":3}}`))
	default:
		http.NotFound(w, r)
	}
}

var probeAllowedHits = map[string]bool{}

func init() {
	for _, e := range chatgpt.ProbeAllowlist {
		probeAllowedHits[e] = true
	}
}

type fakeProbeBrowser struct {
	opened  []string
	cookies int
	page    paybrowser.ProbePage
	closed  bool
}

func (b *fakeProbeBrowser) ProbeOpen(_ context.Context, u string, c []paybrowser.Cookie, _ time.Duration) error {
	b.opened = append(b.opened, u)
	b.cookies = len(c)
	return nil
}
func (b *fakeProbeBrowser) ProbeRead(context.Context) (paybrowser.ProbePage, error) {
	return b.page, nil
}
func (b *fakeProbeBrowser) Shot(_ context.Context, name string, _ paybrowser.ShotScope) (string, error) {
	return name + ".png", nil
}
func (b *fakeProbeBrowser) Close() { b.closed = true }

// probeEnv 装好假服务器、假浏览器和内存库；返回假服务器与假浏览器。
func probeEnv(t *testing.T, hasSub bool, page paybrowser.ProbePage) (*probeFake, *fakeProbeBrowser) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbtest.Open(t)
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "x.db"))
	fake := &probeFake{hasSub: hasSub}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	br := &fakeProbeBrowser{page: page}

	oldAPI, oldLaunch, oldSettle := probeNewAPI, probeLaunch, probeSettle
	t.Cleanup(func() { probeNewAPI, probeLaunch, probeSettle = oldAPI, oldLaunch, oldSettle })
	probeSettle = 0
	probeNewAPI = func(string) (probeAPI, error) {
		c, err := chatgpt.New("")
		if err != nil {
			return nil, err
		}
		c.BaseURL = srv.URL
		return chatgpt.NewProbe(c), nil
	}
	probeLaunch = func(context.Context, string, string) (probeBrowser, error) { return br, nil }
	probeBusy.Store(false)
	return fake, br
}

func probePost(t *testing.T, checks []string, region string) (*httptest.ResponseRecorder, map[string]probeResult) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"session": paySession(probeEmail), "checks": checks, "region": region})
	w := payDo(AdminPayProbe, "POST", "/pay/probe", "/pay/probe", string(body))
	out := map[string]probeResult{}
	var resp struct {
		Results []probeResult `json:"results"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	for _, r := range resp.Results {
		out[r.Key] = r
	}
	return w, out
}

var allProbeChecks = []string{"account", "subscriptions", "checkout_modes", "credit_checkout", "payment_methods", "upgrade_preview", "customer_portal", "chatgpt_browser", "credit_balance"}

func assertNoLeak(t *testing.T, what, text string) {
	t.Helper()
	for _, bad := range []string{
		payAccessToken, paySessionToken, probeNewToken, probeEmail, "real.person", "SECRETCHECKOUTID", "SECRETPORTALTOKEN",
		"pm_1ABCDEFGHIJKLMNOPQR", "acc-1234-5678-abcd", "last4", "card_brand", "#frag",
	} {
		if strings.Contains(text, bad) {
			t.Errorf("%s 里出现了敏感内容 %q", what, bad)
		}
	}
}

func TestProbeNoSubscriptionAccount(t *testing.T) {
	fake, br := probeEnv(t, false, paybrowser.ProbePage{Title: "ChatGPT", URL: "https://chatgpt.com/?x=secret", Text: "Log in Sign sign up"})
	w, res := probePost(t, allProbeChecks, "PH")
	if w.Code != 200 {
		t.Fatalf("状态 %d %s", w.Code, w.Body.String())
	}
	if res["checkout_modes"].Status != probeOK {
		t.Errorf("无订阅时 checkout_modes 应执行: %+v", res["checkout_modes"])
	}
	for _, k := range []string{"credit_checkout", "upgrade_preview"} {
		if res[k].Status != probeSkipped || res[k].Summary == "" {
			t.Errorf("无订阅时 %s 应为 skipped 并写明原因: %+v", k, res[k])
		}
	}
	d := res["checkout_modes"].Details
	if a := d["without_redirect"].(map[string]any); a["shape"] != "custom_checkout_session" {
		t.Errorf("不带 redirect: %v", a)
	}
	if a := d["with_redirect"].(map[string]any); a["shape"] != "url" || a["host"] != "pay.openai.com" || a["host_whitelisted"] != true {
		t.Errorf("带 redirect: %v", a)
	}
	// 所有发出的请求都必须在白名单内
	fake.mu.Lock()
	for _, h := range fake.hits {
		if !probeAllowedHits[h] {
			t.Errorf("发出了白名单外的请求 %s", h)
		}
	}
	fake.mu.Unlock()
	// 浏览器：门户页用无 cookie 打开，chatgpt 页带 cookie
	if len(br.opened) != 2 || !strings.HasPrefix(br.opened[0], "https://billing.stripe.com/") || br.opened[1] != "https://chatgpt.com/" {
		t.Errorf("浏览器打开的页面不对: %v", br.opened)
	}
	assertNoLeak(t, "响应", w.Body.String())
}

func TestProbeSubscribedAccount(t *testing.T) {
	fake, _ := probeEnv(t, true, paybrowser.ProbePage{Title: "ChatGPT", URL: "https://chatgpt.com/", Text: "What can I help with?"})
	_, res := probePost(t, allProbeChecks, "")
	if res["checkout_modes"].Status != probeSkipped {
		t.Errorf("有订阅时 checkout_modes 应 skipped: %+v", res["checkout_modes"])
	}
	for _, k := range []string{"credit_checkout", "upgrade_preview", "subscriptions", "payment_methods", "credit_balance", "account", "customer_portal", "chatgpt_browser"} {
		if res[k].Status != probeOK {
			t.Errorf("%s 应 ok: %+v", k, res[k])
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	checkouts, previews := 0, 0
	for _, h := range fake.hits {
		if h == "POST /backend-api/payments/checkout" {
			checkouts++
		}
		if h == "GET /backend-api/subscriptions/update/preview" {
			previews++
		}
		if !probeAllowedHits[h] {
			t.Errorf("发出了白名单外的请求 %s", h)
		}
	}
	if checkouts != 2 {
		t.Errorf("有订阅只应建 2 次点数会话，实际 POST checkout %d 次", checkouts)
	}
	if previews != 3 {
		t.Errorf("Plus 账号应预览 3 个更高档，实际 %d", previews)
	}
	pm := res["payment_methods"].Details
	if pm["count"] != float64(1) || pm["has_default"] != true {
		t.Errorf("payment_methods: %v", pm)
	}
}

func TestProbeSkipsCheckoutForFreeAndNeverCreatesWhenSubscribed(t *testing.T) {
	fake, _ := probeEnv(t, true, paybrowser.ProbePage{})
	_, _ = probePost(t, []string{"checkout_modes"}, "PH")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, h := range fake.hits {
		if h == "POST /backend-api/payments/checkout" {
			t.Fatal("有订阅的账号不能建开通会话")
		}
	}
}

// 结果、存档里不能出现会话、token、完整邮箱、完整卡 id、完整结账链接。
func TestProbeResultsAndArchiveAreScrubbed(t *testing.T) {
	probeEnv(t, true, paybrowser.ProbePage{Title: "x " + probeEmail, URL: probePortal + "?tok=1", Text: "Add payment method"})
	w, res := probePost(t, allProbeChecks, "PH")
	assertNoLeak(t, "响应", w.Body.String())
	if st := res["subscriptions"].Details["fields"].(map[string]any)["status"].(map[string]any)["value"].(string); strings.Contains(st, "SECRET") {
		t.Errorf("字段值里的链接没缩短: %s", st)
	}
	if acc := res["account"].Details["email"]; acc != "r***@e***.com" {
		t.Errorf("邮箱打码不对: %v", acc)
	}
	list := payDo(AdminPayProbeList, "GET", "/pay/probes", "/pay/probes", "")
	assertNoLeak(t, "历史记录", list.Body.String())
	var raw string
	if err := db.DB.QueryRow(`SELECT checks || results FROM pay_probes`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	assertNoLeak(t, "pay_probes 表", raw)
	if !strings.Contains(list.Body.String(), `"probes"`) {
		t.Errorf("历史列表为空: %s", list.Body.String())
	}
}

func TestProbeCloudflareChallengeRecordedAsFail(t *testing.T) {
	_, br := probeEnv(t, true, paybrowser.ProbePage{Title: "Just a moment...", URL: "https://chatgpt.com/", Text: "Verify you are human"})
	_, res := probePost(t, []string{"chatgpt_browser"}, "")
	r := res["chatgpt_browser"]
	if r.Status != probeFail || !strings.Contains(r.Summary, "Cloudflare") || r.Details["cloudflare_blocked"] != true {
		t.Errorf("应如实记为被拦: %+v", r)
	}
	if !br.closed {
		t.Error("浏览器用完要关闭")
	}
}

func TestProbePortalDetectsAddEntry(t *testing.T) {
	_, _ = probeEnv(t, true, paybrowser.ProbePage{Title: "Billing", URL: probePortal, Text: "Payment methods\nAdd payment method\nUpdate plan"})
	_, res := probePost(t, []string{"customer_portal"}, "")
	d := res["customer_portal"].Details
	if res["customer_portal"].Status != probeOK || d["add_payment_entry"] != true || d["plan_change_entry"] != true || d["final_host"] != "billing.stripe.com" {
		t.Errorf("%+v", res["customer_portal"])
	}
}

func TestProbePortalRefusesUnlistedHost(t *testing.T) {
	_, br := probeEnv(t, true, paybrowser.ProbePage{})
	// 门户返回一个白名单外的域名：不能打开
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/backend-api/payments/customer_portal" {
			w.Write([]byte(`{"url":"https://evil.example.net/steal"}`))
			return
		}
		(&probeFake{hasSub: true}).ServeHTTP(w, r)
	}))
	defer srv.Close()
	probeNewAPI = func(string) (probeAPI, error) {
		c, _ := chatgpt.New("")
		c.BaseURL = srv.URL
		return chatgpt.NewProbe(c), nil
	}
	_, res := probePost(t, []string{"customer_portal"}, "")
	if res["customer_portal"].Status != probeFail || len(br.opened) != 0 {
		t.Errorf("白名单外的门户域名不应被打开: %+v opened=%v", res["customer_portal"], br.opened)
	}
}

func TestProbeSecondRequestGets409(t *testing.T) {
	probeEnv(t, true, paybrowser.ProbePage{})
	probeBusy.Store(true)
	defer probeBusy.Store(false)
	w, _ := probePost(t, []string{"account"}, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("状态 %d，应为 409", w.Code)
	}
}

func TestProbeBusyFlagReleasedAfterRun(t *testing.T) {
	probeEnv(t, true, paybrowser.ProbePage{})
	if w, _ := probePost(t, []string{"account"}, ""); w.Code != 200 {
		t.Fatalf("状态 %d", w.Code)
	}
	if probeBusy.Load() {
		t.Fatal("探测结束后必须释放占用")
	}
}

func TestProbeRejectsBadInput(t *testing.T) {
	probeEnv(t, true, paybrowser.ProbePage{})
	for name, body := range map[string]string{
		"空探测项":  `{"session":"x","checks":[]}`,
		"未知探测项": `{"session":` + jsonStr(paySession("a@b.co")) + `,"checks":["subscriptions/cancel"]}`,
		"坏会话":   `{"session":"{\"user\":{}}","checks":["account"]}`,
		"未知地区":  `{"session":` + jsonStr(paySession("a@b.co")) + `,"checks":["account"],"region":"ZZ"}`,
	} {
		if w := payDo(AdminPayProbe, "POST", "/pay/probe", "/pay/probe", body); w.Code != 400 {
			t.Errorf("%s: 状态 %d，应为 400", name, w.Code)
		}
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestProbeRefreshFailureFailsHTTPChecks(t *testing.T) {
	probeEnv(t, true, paybrowser.ProbePage{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403); w.Write([]byte(`{"detail":"no"}`)) }))
	defer srv.Close()
	probeNewAPI = func(string) (probeAPI, error) {
		c, _ := chatgpt.New("")
		c.BaseURL = srv.URL
		return chatgpt.NewProbe(c), nil
	}
	_, res := probePost(t, []string{"account", "credit_balance"}, "")
	for _, k := range []string{"account", "credit_balance"} {
		if res[k].Status != probeFail || !strings.Contains(res[k].Summary, "刷新会话失败") {
			t.Errorf("%s: %+v", k, res[k])
		}
	}
}

func TestProbeScrubber(t *testing.T) {
	sc := newScrubber(chatgpt.Session{AccessToken: "ACCESS-TOKEN-VALUE", SessionToken: "SESSION-TOKEN-VALUE", Email: "Real.Person@Example.com"}, `{"raw":"whole-session-json"}`)
	in := "tok ACCESS-TOKEN-VALUE st SESSION-TOKEN-VALUE mail Real.Person@Example.com other x.y@z.org " +
		"url https://pay.openai.com/c/pay/cs_live_abc?k=1#f jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.sig_part-1 long " + strings.Repeat("a1B2", 12)
	out := sc.str(in)
	for _, bad := range []string{"ACCESS-TOKEN-VALUE", "SESSION-TOKEN-VALUE", "Real.Person@Example.com", "x.y@z.org", "cs_live_abc", "k=1", "eyJhbGci", strings.Repeat("a1B2", 12)} {
		if strings.Contains(out, bad) {
			t.Errorf("没擦干净 %q: %s", bad, out)
		}
	}
	if !strings.Contains(out, "pay.openai.com/c/pay/…") {
		t.Errorf("链接应只留域名和路径前缀: %s", out)
	}
	if probeMaskEmail("real.person@example.com") != "r***@e***.com" || probeMaskID("pm_1ABCDEFGHIJKLMNOPQR") != "pm_…OPQR" {
		t.Errorf("打码格式: %s %s", probeMaskEmail("real.person@example.com"), probeMaskID("pm_1ABCDEFGHIJKLMNOPQR"))
	}
}

func TestProbeShotRouteRejectsTraversal(t *testing.T) {
	probeEnv(t, true, paybrowser.ProbePage{})
	for _, p := range []gin.Params{
		{{Key: "dir", Value: ".."}, {Key: "name", Value: "x.png"}},
		{{Key: "dir", Value: "probe-12345678"}, {Key: "name", Value: "../a.png"}},
		{{Key: "dir", Value: "probe-zzzzzzzz"}, {Key: "name", Value: "a.png"}},
	} {
		if w := payCtxDo(AdminPayProbeShot, p); w.Code != 400 {
			t.Errorf("%v: 状态 %d，应为 400", p, w.Code)
		}
	}
	if w := payCtxDo(AdminPayProbeShot, gin.Params{{Key: "dir", Value: "probe-12345678"}, {Key: "name", Value: "a.png"}}); w.Code != 404 {
		t.Errorf("不存在的截图应 404: %d", w.Code)
	}
	root := probeShotRoot()
	if err := os.MkdirAll(filepath.Join(root, "probe-12345678"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "probe-12345678", "a.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := payCtxDo(AdminPayProbeShot, gin.Params{{Key: "dir", Value: "probe-12345678"}, {Key: "name", Value: "a.png"}}); w.Code != 200 {
		t.Errorf("应返回截图: %d", w.Code)
	}
}
