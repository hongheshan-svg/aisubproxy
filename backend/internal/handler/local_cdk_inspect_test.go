package handler

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// inspectGPT 在 localFakeGPT 上改写 Account：可以按需返回不同账号、返回错误，并记调用次数。
type inspectGPT struct {
	*localFakeGPT
	mu      sync.Mutex
	acc     chatgpt.Account
	err     error
	account int
}

func (g *inspectGPT) Account(context.Context, chatgpt.Session) (chatgpt.Account, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.account++
	return g.acc, g.err
}

func (g *inspectGPT) calls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.account
}

func (g *inspectGPT) set(acc chatgpt.Account, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.acc, g.err = acc, err
}

// newInspectEnv 在 localEnv 上换一个只用于账号核对的引擎（账号由 inspectGPT 决定），并让缓存时钟可控。
func newInspectEnv(t *testing.T) (*localEnv, *inspectGPT) {
	t.Helper()
	e := newLocalEnv(t)
	e.seedRealPayment()
	g := &inspectGPT{localFakeGPT: e.gpt, acc: chatgpt.Account{AccountID: "acc-1", PlanType: "free"}}
	SetPayEngine(payengine.New(payengine.Options{
		Pools:   map[string]payengine.Pool{string(provider.MarsWallex): e.pool},
		ChatGPT: func(string) (payengine.ChatGPT, error) { return g, nil },
		Alert:   e.addAlert,
	}))
	resetInspectCache()
	t.Cleanup(func() { resetInspectCache(); inspectNow = time.Now })
	return e, g
}

// inspectPreflight 对码和邮箱发一次预检，返回状态码和响应。
func (e *localEnv) inspectPreflight(code, email string) (int, map[string]any) {
	e.t.Helper()
	token := mwToken(code)
	w := e.do("POST", "/public/cdk/preflight", localBody(e.t, map[string]any{
		"code": code, "redemption_token": token, "credential": map[string]any{"mode": "session", "session": localSession(email)},
	}))
	return w.Code, localJSON(e.t, w)
}

func TestLocalPreflightEligible(t *testing.T) {
	e, _ := newInspectEnv(t)
	code, _ := e.issueCode("plus", "PH")
	status, m := e.inspectPreflight(code, "buyer@example.com")
	if status != http.StatusOK {
		t.Fatalf("%d %v", status, m)
	}
	if tok, _ := m["preflight_token"].(string); tok == "" {
		t.Fatalf("应发预检票: %v", m)
	}
	if m["account_checked"] != true || m["current_plan"] != "free" || m["subscription_has_active"] != false {
		t.Fatalf("%v", m)
	}
}

func TestLocalPreflightIneligibleNoTicket(t *testing.T) {
	e, g := newInspectEnv(t)
	g.set(chatgpt.Account{AccountID: "acc-1", PlanType: "plus", HasActiveSubscription: true}, nil)
	code, id := e.issueCode("plus", "PH")
	status, m := e.inspectPreflight(code, "buyer@example.com")
	if status != http.StatusOK {
		t.Fatalf("%d %v", status, m)
	}
	if _, ok := m["preflight_token"]; ok {
		t.Fatalf("不符合的账号不能发预检票: %v", m)
	}
	reason, _ := m["blocking_reason"].(string)
	if m["account_checked"] != true || m["blocking_code"] != "already_subscribed" || !strings.Contains(reason, "plus") ||
		m["current_plan"] != "plus" || m["subscription_has_active"] != true {
		t.Fatalf("%v", m)
	}
	if m["provider"] != "marswallex" || m["email"] != "buyer@example.com" || m["account_email"] != "buyer@example.com" || m["kind"] == nil {
		t.Fatalf("原有字段应保留: %v", m)
	}
	// 空票去兑换：被拒，且没有建单
	w := e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{
		"redemption_token": mwToken(code), "preflight_token": "", "client_request_id": "c1",
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空票兑换应被拒绝(400): %d %s", w.Code, w.Body.String())
	}
	if n := cdkOrderCount(t, id); n != 0 {
		t.Fatalf("不应建单: %d", n)
	}
}

func TestLocalPreflightCurrencyMismatch(t *testing.T) {
	e, g := newInspectEnv(t)
	e.enableCredit()
	e.seedRealCreditPayment()
	no := false
	g.set(chatgpt.Account{AccountID: "acc-1", PlanType: "plus", HasActiveSubscription: true, Structure: "personal",
		BillingCurrency: "USD", IsDelinquent: &no}, nil)
	code, _ := e.issueCode("credit250", "PH")
	status, m := e.inspectPreflight(code, "buyer@example.com")
	if status != http.StatusOK {
		t.Fatalf("%d %v", status, m)
	}
	if _, ok := m["preflight_token"]; ok {
		t.Fatalf("币种不符不能发票: %v", m)
	}
	if m["blocking_code"] != "credit_currency_mismatch" || m["account_currency"] != "USD" || m["code_currency"] != "PHP" {
		t.Fatalf("%v", m)
	}
	reason, _ := m["blocking_reason"].(string)
	if !strings.Contains(reason, "USD") || !strings.Contains(reason, "PHP") {
		t.Fatalf("文案应写出两种币种: %q", reason)
	}
}

func TestLocalPreflightSessionInvalid(t *testing.T) {
	e, g := newInspectEnv(t)
	g.set(chatgpt.Account{}, chatgpt.ErrUnauthorized)
	code, _ := e.issueCode("plus", "PH")
	status, m := e.inspectPreflight(code, "buyer@example.com")
	if status != http.StatusBadRequest || m["error_code"] != "session_invalid" ||
		m["error"] != "登录信息已失效，请回到 ChatGPT 刷新那个页面，再重新复制" {
		t.Fatalf("%d %v", status, m)
	}
	if _, ok := m["preflight_token"]; ok {
		t.Fatalf("%v", m)
	}
}

func TestLocalPreflightUnknownStillTickets(t *testing.T) {
	e, g := newInspectEnv(t)
	g.set(chatgpt.Account{}, &chatgpt.HTTPError{Status: 429})
	code, _ := e.issueCode("plus", "PH")
	status, m := e.inspectPreflight(code, "buyer@example.com")
	if status != http.StatusOK {
		t.Fatalf("%d %v", status, m)
	}
	if tok, _ := m["preflight_token"].(string); tok == "" || m["account_checked"] != false {
		t.Fatalf("%v", m)
	}
	for _, k := range []string{"current_plan", "subscription_has_active", "blocking_code"} {
		if _, ok := m[k]; ok {
			t.Fatalf("查不了时不该有 %s: %v", k, m)
		}
	}
}

// 引擎未启动时公开接口在就绪检查就 503 了，到不了账号核对；这里直接测核对函数本身的兜底。
func TestLocalPreflightEngineMissing(t *testing.T) {
	e, _ := newInspectEnv(t)
	_, id := e.issueCode("plus", "PH")
	row, err := db.GetLocalCDK(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	SetPayEngine(nil)
	v, err := inspectLocalAccount(context.Background(), row, localSession("buyer@example.com"), "buyer@example.com")
	if err != nil || v.Kind != payengine.VerdictUnknown || v.Cause != "engine" {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestLocalPreflightPerCodeLimit(t *testing.T) {
	e, g := newInspectEnv(t)
	code, _ := e.issueCode("plus", "PH")
	for i := 0; i < localRateLimits[localRateInspect]; i++ {
		status, m := e.inspectPreflight(code, "buyer"+string(rune('a'+i))+"@example.com")
		if status != http.StatusOK || m["account_checked"] != true {
			t.Fatalf("第 %d 次: %d %v", i+1, status, m)
		}
	}
	if g.calls() != 10 {
		t.Fatalf("前 10 次应各调一次: %d", g.calls())
	}
	status, m := e.inspectPreflight(code, "extra@example.com")
	if status != http.StatusOK || m["account_checked"] != false {
		t.Fatalf("%d %v", status, m)
	}
	if tok, _ := m["preflight_token"].(string); tok == "" {
		t.Fatalf("超限不拦买家: %v", m)
	}
	if g.calls() != 10 {
		t.Fatalf("超限后不该再调 ChatGPT: %d", g.calls())
	}
}

func TestLocalPreflightCache(t *testing.T) {
	e, g := newInspectEnv(t)
	now := time.Now()
	inspectNow = func() time.Time { return now }
	code, _ := e.issueCode("plus", "PH")

	e.inspectPreflight(code, "Buyer@example.com")
	e.inspectPreflight(code, "buyer@example.com") // 邮箱大小写不同也命中
	if g.calls() != 1 {
		t.Fatalf("5 分钟内第二次应命中缓存: %d", g.calls())
	}
	now = now.Add(5*time.Minute + time.Second)
	e.inspectPreflight(code, "buyer@example.com")
	if g.calls() != 2 {
		t.Fatalf("过期后应重新查询: %d", g.calls())
	}

	// unknown 不缓存
	g.set(chatgpt.Account{}, &chatgpt.HTTPError{Status: 429})
	e.inspectPreflight(code, "other@example.com")
	e.inspectPreflight(code, "other@example.com")
	if g.calls() != 4 {
		t.Fatalf("unknown 不该缓存: %d", g.calls())
	}
}

func TestInspectCacheEviction(t *testing.T) {
	resetInspectCache()
	t.Cleanup(func() { resetInspectCache(); inspectNow = time.Now; inspectCacheMax = 5000 })
	now := time.Now()
	inspectNow = func() time.Time { return now }
	inspectCacheMax = 3
	v := payengine.AccountVerdict{Kind: payengine.VerdictEligible}
	inspectCachePut("a", v)
	now = now.Add(time.Minute)
	inspectCachePut("b", v)
	inspectCachePut("c", v)
	inspectCachePut("d", v) // 满了且没有过期的：淘汰最早的 a
	if _, ok := inspectCacheGet("a"); ok {
		t.Fatal("a 应被淘汰")
	}
	for _, k := range []string{"b", "c", "d"} {
		if _, ok := inspectCacheGet(k); !ok {
			t.Fatalf("%s 应在缓存里", k)
		}
	}
	// a 写入后过了 1 分钟，再过 4 分 30 秒：b、c、d 还没到 5 分钟
	now = now.Add(4*time.Minute + 30*time.Second)
	if _, ok := inspectCacheGet("b"); !ok {
		t.Fatal("b 还没过期")
	}
	if inspectCacheKey(7, "A@B.com") != inspectCacheKey(7, "a@b.com") || inspectCacheKey(7, "a@b.com") == inspectCacheKey(8, "a@b.com") {
		t.Fatal("缓存键应忽略邮箱大小写、区分码")
	}
}

func TestLocalPreflightLogNoSecrets(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	e, g := newInspectEnv(t)
	g.set(chatgpt.Account{AccountID: "acc-1", PlanType: "plus", HasActiveSubscription: true}, nil)
	code, _ := e.issueCode("plus", "PH")
	const email = "alice.longname@gmail.com"
	e.inspectPreflight(code, email)
	g.set(chatgpt.Account{}, &chatgpt.HTTPError{Status: 429})
	e.inspectPreflight(code, "bob.longname@gmail.com")

	text := logs.String()
	for _, s := range []string{paySessionToken, payAccessToken, email, "bob.longname@gmail.com", code} {
		if strings.Contains(text, s) {
			t.Fatalf("日志不该含 %q:\n%s", s, text)
		}
	}
	for _, s := range []string{maskEmailForAdmin(email), "already_subscribed", "rate_limited"} {
		if !strings.Contains(text, s) {
			t.Fatalf("日志应含 %q:\n%s", s, text)
		}
	}
}
