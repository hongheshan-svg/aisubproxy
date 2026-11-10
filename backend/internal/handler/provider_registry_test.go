package handler

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/provider/providertest"
)

// 用两家假服务商证明注册表可扩展：供卡型 testcard（TC- / tct_）走通用的本地流程，
// 上游型 testup（认领 TU- / tut_）走它自己的适配器，MarsWallex 不受影响。

// fakeCardCheck 是 testcard 的就绪检查。
type fakeCardCheck struct{ fail string }

func (a fakeCardCheck) Check(_ context.Context, _, _ string, _ bool) providerStatus {
	st := newProviderStatus(providertest.TestCard)
	if a.fail != "" {
		st.Readiness.Fail("fake", "测试检查", a.fail)
	} else {
		st.Readiness.Pass("fake", "测试检查", "就绪")
	}
	st.Extras = append(st.Extras, providerExtra{Label: "测试余额", Value: "1.00"})
	st.Combos = []planCombo{{Plan: "plus", Region: "PH", OK: true, Problems: []string{}}}
	return st
}

// fakeUpstream 是假上游的适配器：记下收到的调用；预检时像真上游一样写一条绑定（服务商为它自己的名字，默认 testup）。
// 没有实现代理换码、宽限期恢复、卡密查询这几项可选能力。
type fakeUpstream struct {
	mu    sync.Mutex
	calls []string
	name  provider.Name // 为空时是 testup
}

func (u *fakeUpstream) nm() provider.Name {
	if u.name != "" {
		return u.name
	}
	return providertest.TestUp
}

func (u *fakeUpstream) hit(what string) {
	u.mu.Lock()
	u.calls = append(u.calls, what)
	u.mu.Unlock()
}

func (u *fakeUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.calls)
}

func (u *fakeUpstream) log() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return strings.Join(u.calls, ",")
}

func (u *fakeUpstream) Preview(c *gin.Context, body map[string]any) {
	u.hit("preview:" + str(body["code"]))
	c.JSON(http.StatusOK, gin.H{"redemption_token": "tut_fake", "from": "testup"})
}

func (u *fakeUpstream) Preflight(c *gin.Context, body map[string]any) {
	u.hit("preflight")
	sess := ""
	if cred, ok := body["credential"].(map[string]any); ok {
		sess = str(cred["session"])
	}
	if err := bindCDKSession(string(u.nm()), str(body["code"]), str(body["redemption_token"]), sess); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "from": "testup"})
}

func (u *fakeUpstream) Redeem(c *gin.Context, _ map[string]any) {
	u.hit("redeem")
	c.JSON(http.StatusOK, gin.H{"status": "queued", "from": "testup"})
}

func (u *fakeUpstream) Result(c *gin.Context, token string) {
	u.hit("result:" + token)
	c.JSON(http.StatusOK, gin.H{"status": "running", "from": "testup"})
}

func (u *fakeUpstream) ResultByCode(c *gin.Context, code string) {
	u.hit("result_by_code:" + code)
	c.JSON(http.StatusOK, gin.H{"status": "running", "from": "testup"})
}

func (u *fakeUpstream) Issue(c *gin.Context, req cdkIssueRequest) {
	u.hit("issue:" + req.Plan)
	c.JSON(http.StatusOK, gin.H{"provider": "testup", "issued": []any{}, "from": "testup"})
}

func (u *fakeUpstream) Check(context.Context) providerStatus {
	st := newProviderStatus(u.nm())
	st.Readiness.Pass("fake", "测试检查", "就绪")
	return st
}

// watchUpstream 注册 testup 假上游并接上记录调用的适配器：本站码和认不出的码都不应让它收到请求。
func (e *localEnv) watchUpstream() *fakeUpstream {
	e.t.Helper()
	if _, ok := provider.Get(providertest.TestUp); !ok { // 同一个测试里建多个环境时只注册一次，适配器每个环境换新的
		providertest.Register(e.t, providertest.NewTestUp())
	}
	up := &fakeUpstream{}
	withUpstreamAdapter(e.t, providertest.TestUp, up)
	return up
}

// useSecondUpstream 再注册一家假上游 testup2（认领 TV- 码和 tvt_ 令牌）：跨服务商的规则要对「两家不同的上游」也成立。
func (e *localEnv) useSecondUpstream() *fakeUpstream {
	e.t.Helper()
	const name provider.Name = "testup2"
	up2 := &fakeUpstream{name: name}
	providertest.Register(e.t, providertest.Up{N: name, Display: "TestUp2", CodePfx: "TV-", TokenPfx: "tvt_", Prods: []string{"gpt", "x"}})
	withUpstreamAdapter(e.t, name, up2)
	return up2
}

// useTestProviders 再注册供卡型的 testcard（testup 环境里已经有）、接上适配器，付款引擎给 testcard 也接一个卡池（借用 MarsWallex 的卡池实现，引擎只认名字）。
func (e *localEnv) useTestProviders() *fakeUpstream {
	e.t.Helper()
	providertest.Register(e.t, providertest.NewTestCard())
	withCardSupplyAdapter(e.t, providertest.TestCard, fakeCardCheck{})
	up := e.up
	e.engine = payengine.New(payengine.Options{
		Pools:    map[string]payengine.Pool{string(provider.MarsWallex): e.pool, string(providertest.TestCard): e.pool},
		ChatGPT:  func(string) (payengine.ChatGPT, error) { return e.gpt, nil },
		Browser:  e.br.New,
		Alert:    e.addAlert,
		ShotDir:  e.t.TempDir(),
		Timeouts: localEnvTimeouts(),
	})
	SetPayEngine(e.engine)
	return up
}

// seedProviderRealPayment 写一笔这家的系统自动确认成功的真实付款单：过这家的发码硬门槛。
func (e *localEnv) seedProviderRealPayment(prov provider.Name) {
	e.t.Helper()
	e.markRenewalCancelled(e.insertProviderOrder(string(prov), "admin_test", "admin", 0, "seed-"+string(prov)+"@example.com", "succeeded", "", true))
}

// issueProviderCode 走后台接口给指定服务商发一张码。
func (e *localEnv) issueProviderCode(prov provider.Name, plan, region string) (string, int64) {
	e.t.Helper()
	w := e.do("POST", "/admin/cdks/issue", localBody(e.t, map[string]any{
		"provider": prov, "plan": plan, "region": region, "count": 1, "funding_confirmed": true,
	}))
	if w.Code != http.StatusOK {
		e.t.Fatalf("发码失败 %d %s", w.Code, w.Body.String())
	}
	it := localJSON(e.t, w)["issued"].([]any)[0].(map[string]any)
	return it["code"].(string), int64(it["id"].(float64))
}

func assertProviderFields(t *testing.T, what string, m map[string]any, name, kind, display string) {
	t.Helper()
	if m["provider"] != name || m["provider_kind"] != kind || m["provider_display_name"] != display {
		t.Fatalf("%s 的服务商字段不对: %v", what, m)
	}
}

func localRowProvider(t *testing.T, id int64) string {
	t.Helper()
	var p string
	if err := db.DB.QueryRow(`SELECT provider FROM local_cdks WHERE id = ?`, id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// 硬门槛反过来也一样：testcard 的真实成功单不能替 MarsWallex 过门槛；试运行单也不算。
func TestIssueGateIsPerProvider(t *testing.T) {
	e := newLocalEnv(t)
	e.useTestProviders()
	e.seedProviderRealPayment(providertest.TestCard)
	e.insertProviderOrder("marswallex", "admin_test", "admin", 1, "dry@example.com", "succeeded", "", true) // 试运行单
	w := e.do("POST", "/admin/cdks/issue", localBody(t, map[string]any{"provider": "marswallex", "plan": "plus", "region": "PH", "count": 1, "funding_confirmed": true}))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "需要一笔系统自动确认成功的真实付款") {
		t.Fatalf("MarsWallex 不能借 testcard 的成功单过门槛: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.DB.Exec(`DELETE FROM pay_orders WHERE provider = 'marswallex'`); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.issueProviderCode(providertest.TestCard, "plus", "PH"); !strings.HasPrefix(code, "TC-") {
		t.Fatalf("testcard 自己过了门槛: %s", code)
	}
}

// 发码分派：testcard 的码用它的前缀、写 provider = testcard；硬门槛按服务商分别计算；不支持的档位明说，绝不换成别家。
func TestIssueDispatchByProvider(t *testing.T) {
	e := newLocalEnv(t)
	up := e.useTestProviders()
	e.seedRealPayment() // 只有 MarsWallex 的真实成功单

	issue := func(path string, body map[string]any) (int, map[string]any) {
		w := e.do("POST", path, localBody(t, body))
		return w.Code, localJSON(t, w)
	}
	tc := map[string]any{"provider": "testcard", "plan": "plus", "region": "PH", "count": 1, "funding_confirmed": true}
	code, m := issue("/admin/cdks/issue", tc)
	if code != http.StatusBadRequest || !strings.Contains(localBody(t, m["problems"]), "需要一笔系统自动确认成功的真实付款") {
		t.Fatalf("别家的成功单不能替 testcard 过硬门槛: %d %v", code, m)
	}

	e.seedProviderRealPayment(providertest.TestCard)
	// 两条发码路由是同一个处理，请求都显式带服务商
	for _, path := range []string{"/admin/cdks/issue", "/admin/cardplatform/cdks"} {
		code, m = issue(path, tc)
		if code != http.StatusOK || m["provider"] != "testcard" || m["provider_display_name"] != "TestCard" {
			t.Fatalf("%s: %d %v", path, code, m)
		}
		it := m["issued"].([]any)[0].(map[string]any)
		full := it["code"].(string)
		if !strings.HasPrefix(full, "TC-") || it["plan_note"] != "测试提示" {
			t.Fatalf("testcard 的码用 TC- 前缀: %v", it)
		}
		if p := localRowProvider(t, int64(it["id"].(float64))); p != "testcard" {
			t.Fatalf("库里应记 testcard: %q", p)
		}
	}
	if up.callCount() != 0 {
		t.Fatalf("发 testcard 的码不应碰上游: %s", up.log())
	}

	// 显式选了不支持的档位：400，不换成别家
	code, m = issue("/admin/cdks/issue", map[string]any{"provider": "testcard", "plan": "pro_5x", "region": "PH", "count": 1, "funding_confirmed": true})
	if code != http.StatusBadRequest || m["error"] != "TestCard 不支持 ChatGPT pro_5x" || m["provider"] != "testcard" {
		t.Fatalf("%d %v", code, m)
	}
	// 没带服务商：按产品配置选出 testcard，不支持就 400 并提示手动选择
	if err := provider.SaveConfig(provider.MarsWallex, map[string]provider.Name{"gpt": providertest.TestCard}); err != nil {
		t.Fatal(err)
	}
	code, m = issue("/admin/cdks/issue", map[string]any{"plan": "pro_5x", "region": "PH", "count": 1, "funding_confirmed": true})
	if code != http.StatusBadRequest || !strings.Contains(m["error"].(string), "请在发码时手动选择服务商") {
		t.Fatalf("%d %v", code, m)
	}
	code, m = issue("/admin/cdks/issue", map[string]any{"plan": "plus", "region": "PH", "count": 1, "funding_confirmed": true})
	if code != http.StatusOK || m["provider"] != "testcard" {
		t.Fatalf("按产品配置应发 testcard: %d %v", code, m)
	}
	// X 没有产品配置，跟随全局默认 MarsWallex（它不做 X，发码被拒，不会换成别家）
	if code, m = issue("/admin/cdks/issue", map[string]any{"plan": "x_premium", "product": "x", "count": 1, "funding_confirmed": true}); code == http.StatusBadRequest && strings.Contains(localBody(t, m), "testcard") {
		t.Fatalf("X 不应选到 testcard: %v", m)
	}

	// 上游型交给它的适配器
	code, m = issue("/admin/cdks/issue", map[string]any{"provider": "testup", "plan": "plus", "count": 1, "funding_confirmed": true})
	if code != http.StatusOK || m["from"] != "testup" || up.log() != "issue:plus" {
		t.Fatalf("testup 发码: %d %v %s", code, m, up.log())
	}
	if code, m = issue("/admin/cdks/issue", map[string]any{"provider": "nope", "plan": "plus", "count": 1, "funding_confirmed": true}); code != http.StatusBadRequest || !strings.Contains(m["error"].(string), "未知的卡服务商") {
		t.Fatalf("%d %v", code, m)
	}
}

// 公开分派：TC- 码查不到时「码不存在」且不发任何上游请求；TU- 只交给 testup；认不出的码 404 且不发往任何上游；
// MW- 码的库行服务商不是 MarsWallex 时按码不存在处理。
func TestPublicDispatchByProvider(t *testing.T) {
	e := newLocalEnv(t)
	up := e.useTestProviders()
	e.seedRealPayment()

	preview := func(code string) (int, map[string]any) {
		w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code}))
		return w.Code, localJSON(t, w)
	}
	if code, m := preview("TC-NOPE1-NOPE2-NOPE3-NOPE4-NOPE5"); code != http.StatusNotFound || m["error"] != localCDKNotFoundText || m["provider"] != nil {
		t.Fatalf("查不到的 TC 码: %d %v", code, m)
	}
	for _, path := range []string{"/public/cdk/result?token=tct_0123456789abcdef0123456789abcdef", "/public/cdk/result-by-code?code=TC-NOPE1-NOPE2"} {
		if w := e.do("GET", path, ""); w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := e.do("POST", "/public/cdk/preflight", localBody(t, map[string]any{"code": "TC-NOPE1-NOPE2", "credential": map[string]any{"mode": "session", "session": localSession("x@example.com")}})); w.Code != http.StatusNotFound {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if up.callCount() != 0 {
		t.Fatalf("TC 码绝不发往上游: %s", up.log())
	}

	if code, m := preview("TU-AAAA-BBBB"); code != http.StatusOK || m["from"] != "testup" || up.log() != "preview:TU-AAAA-BBBB" {
		t.Fatalf("TU 码只交给 testup: %d %v %s", code, m, up.log())
	}
	if w := e.do("GET", "/public/cdk/result?token=tut_abc", ""); w.Code != http.StatusOK || !strings.Contains(up.log(), "result:tut_abc") {
		t.Fatalf("tut 令牌只交给 testup: %d %s", w.Code, up.log())
	}
	if w := e.do("GET", "/public/cdk/result-by-code?code=TU-AAAA-BBBB", ""); w.Code != http.StatusOK || !strings.Contains(up.log(), "result_by_code:TU-AAAA-BBBB") {
		t.Fatalf("TU 码按码查结果只交给 testup: %d %s", w.Code, up.log())
	}
	// 一家的码绝不跟着另一家的令牌发出去；令牌认不出同样不发
	up2 := e.useSecondUpstream()
	if w := e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{"code": "TU-AAAA-BBBB", "redemption_token": "tvt_other"})); w.Code != http.StatusBadRequest || strings.Contains(up.log(), "redeem") || up2.callCount() != 0 {
		t.Fatalf("码和令牌不是同一家: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{"code": "TU-AAAA-BBBB", "redemption_token": "zc-tok-1"})); w.Code != http.StatusNotFound || strings.Contains(up.log(), "redeem") {
		t.Fatalf("令牌认不出应 404 且不发出去: %d %s", w.Code, w.Body.String())
	}

	if code, m := preview("ZC-FAKE-0001"); code != http.StatusNotFound || m["error"] != unrecognizedCodeText {
		t.Fatalf("谁都不认领的码应 404: %d %v", code, m)
	}
	if strings.Contains(up.log(), "ZC-FAKE") {
		t.Fatalf("认不出的码不应交给 testup: %s", up.log())
	}

	// MW- 码的库行被标成 testcard：按码不存在处理，也不交给 testcard 或上游
	mwCode, mwID := e.issueCode("plus", "PH")
	if _, err := db.DB.Exec(`UPDATE local_cdks SET provider = 'testcard' WHERE id = ?`, mwID); err != nil {
		t.Fatal(err)
	}
	zcBefore := e.up.callCount()
	if code, m := preview(mwCode); code != http.StatusNotFound || m["error"] != localCDKNotFoundText {
		t.Fatalf("库行服务商对不上应按码不存在: %d %v", code, m)
	}
	if w := e.do("GET", "/public/cdk/result?token="+mwToken(mwCode), ""); w.Code != http.StatusNotFound {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if m := localJSON(t, e.do("GET", "/lookup/cdk?code="+mwCode, "")); m["status"] != "unknown" {
		t.Fatalf("查询页也按未找到: %v", m)
	}
	if e.up.callCount() != zcBefore {
		t.Fatalf("不应发往上游: %s", e.up.log())
	}
}

// testcard 的码走通用的本地流程：预览、预检、兑换（含重兑）、按令牌和按码查结果、查询页、代理换码、宽限期恢复，
// 每个公开响应都带 provider、provider_kind、provider_display_name；建出的付款单记 testcard。
func TestTestCardPublicFlowCarriesProviderFields(t *testing.T) {
	e := newLocalEnv(t)
	up := e.useTestProviders()
	e.seedProviderRealPayment(providertest.TestCard)
	code, id := e.issueProviderCode(providertest.TestCard, "plus", "PH")
	const name, kind, display = "testcard", "card_supply", "TestCard"

	w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code}))
	m := localJSON(t, w)
	if w.Code != http.StatusOK || !strings.HasPrefix(m["redemption_token"].(string), "tct_") || m["plan_note"] != "测试提示" {
		t.Fatalf("预览: %d %v", w.Code, m)
	}
	assertProviderFields(t, "预览", m, name, kind, display)
	token := m["redemption_token"].(string)

	sess := localSession("tc-buyer@example.com")
	w = e.do("POST", "/public/cdk/preflight", localBody(t, map[string]any{"code": code, "redemption_token": token, "credential": map[string]any{"mode": "session", "session": sess}}))
	m = localJSON(t, w)
	if w.Code != http.StatusOK {
		t.Fatalf("预检: %d %v", w.Code, m)
	}
	assertProviderFields(t, "预检", m, name, kind, display)
	preflight := m["preflight_token"].(string)

	// 没有可兑换的单时按令牌查结果是 404，也带服务商字段
	w = e.result(token)
	if w.Code != http.StatusNotFound {
		t.Fatalf("还没兑换: %d %s", w.Code, w.Body.String())
	}
	assertProviderFields(t, "无记录的结果", localJSON(t, w), name, kind, display)

	w = e.redeem(token, preflight)
	m = localJSON(t, w)
	if w.Code != http.StatusOK || m["redemption_token"] != token {
		t.Fatalf("兑换: %d %v", w.Code, m)
	}
	assertProviderFields(t, "兑换", m, name, kind, display)
	var orderProvider string
	if err := db.DB.QueryRow(`SELECT provider FROM pay_orders WHERE source = 'cdk' AND source_ref = ?`, db.LocalCDKSourceRef(id)).Scan(&orderProvider); err != nil || orderProvider != "testcard" {
		t.Fatalf("付款单应记 testcard: %q %v", orderProvider, err)
	}

	// 码在处理中，重复兑换被拒，拒绝的响应也带服务商字段
	w = e.redeem(token, preflight)
	if w.Code != http.StatusConflict {
		t.Fatalf("重复兑换: %d %s", w.Code, w.Body.String())
	}
	assertProviderFields(t, "重复兑换", localJSON(t, w), name, kind, display)

	w = e.result(token)
	m = localJSON(t, w)
	if w.Code != http.StatusOK || m["status"] != "queued" {
		t.Fatalf("按令牌查结果: %d %v", w.Code, m)
	}
	assertProviderFields(t, "按令牌查结果", m, name, kind, display)
	w = e.do("GET", "/public/cdk/result-by-code?code="+code, "")
	m = localJSON(t, w)
	if w.Code != http.StatusOK || m["redemption_token"] != token {
		t.Fatalf("按码查结果: %d %v", w.Code, m)
	}
	assertProviderFields(t, "按码查结果", m, name, kind, display)
	if m := localJSON(t, e.do("GET", "/lookup/cdk?code="+code, "")); m["status"] != "processing" {
		t.Fatalf("查询页: %v", m)
	}

	// 干净失败后重兑：同一个兑换接口，响应同样带服务商字段
	if _, err := db.DB.Exec(`UPDATE pay_orders SET state = 'failed', failure = 'not_submitted', error_code = 'session_invalid', finished_at = 1 WHERE source = 'cdk' AND source_ref = ?`, db.LocalCDKSourceRef(id)); err != nil {
		t.Fatal(err)
	}
	w = e.do("POST", "/public/cdk/redeem", localBody(t, map[string]any{"code": code, "credential": map[string]any{"mode": "session", "session": sess}}))
	m = localJSON(t, w)
	if w.Code != http.StatusOK {
		t.Fatalf("重兑: %d %v", w.Code, m)
	}
	assertProviderFields(t, "重兑", m, name, kind, display)

	// 宽限期恢复：供卡型不支持，拒绝的响应带服务商字段
	w = e.do("POST", "/public/cdk/recover-subscription", localBody(t, map[string]any{"redemption_token": token, "preflight_token": "x", "confirmed": true}))
	m = localJSON(t, w)
	if w.Code != http.StatusBadRequest || !strings.Contains(m["error"].(string), "暂不支持宽限期") {
		t.Fatalf("宽限期恢复: %d %v", w.Code, m)
	}
	assertProviderFields(t, "宽限期恢复", m, name, kind, display)

	// 代理换码：本地流程，结果带服务商字段（这张码有在途单，不符合换码条件）
	if err := setAgentSwapPassword("swap-pass-123456"); err != nil {
		t.Fatal(err)
	}
	w = e.do("POST", "/public/cdk/exchange", localBody(t, map[string]any{"password": "swap-pass-123456", "code": code}))
	m = localJSON(t, w)
	if w.Code != http.StatusBadRequest || !strings.Contains(m["error"].(string), "不符合换码条件") {
		t.Fatalf("代理换码: %d %v", w.Code, m)
	}
	assertProviderFields(t, "代理换码", m, name, kind, display)

	if up.callCount() != 0 {
		t.Fatalf("testcard 的流程不应碰任何上游: %s", up.log())
	}

	// 上游没实现的可选能力：明说不支持，不转给别家
	w = e.do("POST", "/public/cdk/exchange", localBody(t, map[string]any{"password": "swap-pass-123456", "code": "TU-AAAA-BBBB-CCCC"}))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "该服务商不支持代理换码") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = e.do("POST", "/public/cdk/recover-subscription", localBody(t, map[string]any{"redemption_token": "tut_abc", "preflight_token": "x", "confirmed": true}))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "该服务商不支持宽限期处理") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if m := localJSON(t, e.do("GET", "/lookup/cdk?code=TU-AAAA-BBBB-CCCC", "")); m["status"] != "unsupported" {
		t.Fatalf("没实现查询能力的上游按暂不支持在线查询: %v", m)
	}
	if up.callCount() != 0 {
		t.Fatalf("不应发往上游: %s", up.log())
	}
}

// MarsWallex 的公开响应同样带三项服务商字段。
func TestMarsWallexPublicResponsesCarryProviderFields(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, _ := e.issueCode("plus", "PH")
	w := e.do("POST", "/public/cdk/preview", localBody(t, map[string]any{"code": code}))
	assertProviderFields(t, "预览", localJSON(t, w), "marswallex", "card_supply", "MarsWallex")
	token, preflight := e.preflight(code, localSession("mw-buyer@example.com"))
	w = e.redeem(token, preflight)
	assertProviderFields(t, "兑换", localJSON(t, w), "marswallex", "card_supply", "MarsWallex")
	assertProviderFields(t, "按令牌查结果", localJSON(t, e.result(token)), "marswallex", "card_supply", "MarsWallex")
	assertProviderFields(t, "按码查结果", localJSON(t, e.do("GET", "/public/cdk/result-by-code?code="+code, "")), "marswallex", "card_supply", "MarsWallex")
	// 认不出的码回 404，不带服务商字段
	if m := localJSON(t, e.do("POST", "/public/cdk/preview", `{"code":"ZC-FAKE-0001"}`)); m["provider_kind"] != nil || m["provider"] != nil {
		t.Fatalf("认不出的码不带服务商字段: %v", m)
	}
}

// 跨服务商互斥：testcard、testup、testup2（第二家上游）三家两两互斥。别家的绑定、任意一家的在途或待人工付款单、进程内的账号锁都会拦；
// 自己的绑定不拦。
func TestCrossProviderMutexThreeWay(t *testing.T) {
	e := newLocalEnv(t)
	up := e.useTestProviders()
	e.seedProviderRealPayment(providertest.TestCard)
	up2 := e.useSecondUpstream()
	cred := func(email string) map[string]any {
		return map[string]any{"mode": "session", "session": localSession(email)}
	}
	// 三家的预检。返回状态码；409 必须是跨服务商互斥的拒绝，不是别的原因
	tcCode, _ := e.issueProviderCode(providertest.TestCard, "plus", "PH")
	send := func(body map[string]any) int {
		w := e.do("POST", "/public/cdk/preflight", localBody(t, body))
		if w.Code == http.StatusConflict && localJSON(t, w)["error"] != crossProviderBusyText {
			t.Fatalf("409 不是互斥拒绝: %s", w.Body.String())
		}
		return w.Code
	}
	preflight := map[string]func(email string) int{
		"testcard": func(email string) int {
			return send(map[string]any{"code": tcCode, "credential": cred(email)})
		},
		"testup": func(email string) int {
			return send(map[string]any{"code": "TU-AAAA-" + email, "credential": cred(email)})
		},
		"testup2": func(email string) int {
			return send(map[string]any{"code": "TV-AAAA-" + email, "credential": cred(email)})
		},
	}
	all := func(code int) map[string]int {
		return map[string]int{"testcard": code, "testup": code, "testup2": code}
	}
	expect := func(t *testing.T, email string, want map[string]int) {
		t.Helper()
		for _, prov := range []string{"testcard", "testup", "testup2"} {
			if got := preflight[prov](email); got != want[prov] {
				t.Errorf("%s 走 %s: %d，期望 %d", email, prov, got, want[prov])
			}
		}
	}

	t.Run("没人占用时三家都能预检", func(t *testing.T) {
		// 上游预检会写绑定，所以每家用各自的账号
		for _, prov := range []string{"testcard", "testup", "testup2"} {
			if got := preflight[prov]("free-" + prov + "@example.com"); got != http.StatusOK {
				t.Errorf("%s: %d", prov, got)
			}
		}
	})
	t.Run("testup 的绑定拦住另外两家", func(t *testing.T) {
		const email = "up@example.com"
		if got := preflight["testup"](email); got != http.StatusOK || !strings.Contains(up.log(), "preflight") {
			t.Fatalf("testup 预检: %d %s", got, up.log())
		}
		expect(t, email, map[string]int{"testcard": http.StatusConflict, "testup": http.StatusOK, "testup2": http.StatusConflict})
	})
	t.Run("testup2 的绑定拦住另外两家", func(t *testing.T) {
		const email = "up2@example.com"
		if got := preflight["testup2"](email); got != http.StatusOK {
			t.Fatalf("testup2 预检: %d", got)
		}
		expect(t, email, map[string]int{"testcard": http.StatusConflict, "testup": http.StatusConflict, "testup2": http.StatusOK})
	})
	t.Run("testcard 的在途单拦住所有家", func(t *testing.T) {
		const email = "tc@example.com"
		e.insertProviderOrder("testcard", "cdk", "999", 0, email, "queued", "", false)
		expect(t, email, all(http.StatusConflict))
	})
	t.Run("testcard 的待人工单拦住所有家", func(t *testing.T) {
		const email = "tc-review@example.com"
		e.insertProviderOrder("testcard", "cdk", "998", 0, email, payengine.StateNeedsReview, "", true)
		expect(t, email, all(http.StatusConflict))
	})
	t.Run("进程内账号锁：上游转发期间别家进不来", func(t *testing.T) {
		const email = "hold@example.com"
		release, ok := holdAccountShared(email, providertest.TestUp)
		if !ok {
			t.Fatal("共享锁")
		}
		up2Before := up2.callCount()
		if got := preflight["testup2"](email); got != http.StatusConflict || up2.callCount() != up2Before {
			t.Fatalf("testup 转发期间 testup2 应拦下且不转发: %d", got)
		}
		if got := preflight["testup"](email); got != http.StatusOK {
			t.Fatalf("同一家之间不互斥: %d", got)
		}
		release()
		excl, ok := holdAccountExclusive(email)
		if !ok {
			t.Fatal("独占锁")
		}
		if got := preflight["testup2"](email); got != http.StatusConflict {
			t.Fatalf("供卡型建单期间上游应拦下: %d", got)
		}
		excl()
	})
}

// 就绪接口按注册表列出每一家，新服务商自动出现；供卡型带 combos，上游型不带；某家的检查本身出错只影响这一家。
func TestAdminProviderReadinessPerProvider(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	dumpExample(t, "get_providers_readiness", e.do("GET", "/admin/providers/readiness?plan=plus&region=PH", "").Body.Bytes())

	e.useTestProviders()
	withCardSupplyAdapter(t, providertest.TestCard, panicCheck{})
	m := localJSON(t, e.do("GET", "/admin/providers/readiness", ""))
	providers := m["providers"].(map[string]any)
	if len(providers) != 3 {
		t.Fatalf("三家: %v", providers)
	}
	tc := providers["testcard"].(map[string]any)
	if tc["ok"] != false || !strings.Contains(localBody(t, tc["problems"]), "就绪检查本身出错") || tc["combos"] == nil {
		t.Fatalf("出错的这家标为未就绪: %v", tc)
	}
	if tu := providers["testup"].(map[string]any); tu["ok"] != true || tu["combos"] != nil {
		t.Fatalf("testup: %v", tu)
	}
	if mw := providers["marswallex"].(map[string]any); mw["ok"] != true {
		t.Fatalf("别家不受影响: %v", mw)
	}
}

type panicCheck struct{}

func (panicCheck) Check(_ context.Context, _, _ string, _ bool) providerStatus { panic("boom") }
