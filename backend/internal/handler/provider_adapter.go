package handler

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// 卡服务商在 handler 层的接入。注册表（provider 包）只管名字、类型、前缀和能力；
// 真正对接一家服务商的代码在这里按类型接上：
//   - 供卡型：兑换、发码、换码、查询都走通用的本地流程（local_cdk*.go），这里只要它的就绪检查。
//   - 上游型：公开兑换的各入口原样交给它的适配器，代理换码、宽限期恢复、卡密查询是可选能力。
//
// 每家注册了的服务商都必须有对应的适配器，启动时由 CheckProviderAdapters 检查。

// providerExtra 是就绪检查附带展示的一项数据，如卡台余额。
type providerExtra struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// providerStatus 是一家服务商就绪检查的完整结果。
type providerStatus struct {
	Readiness provider.Readiness
	Extras    []providerExtra
	Warnings  []string
	Combos    []planCombo // 只有供卡型有
}

func newProviderStatus(n provider.Name) providerStatus {
	return providerStatus{Readiness: provider.NewReadiness(n), Extras: []providerExtra{}, Warnings: []string{}}
}

// json 是就绪接口里一家服务商的内容。供卡型总带 combos（可能为空），上游型没有。
func (s providerStatus) json(kind provider.Kind) gin.H {
	out := gin.H{
		"provider": s.Readiness.Provider,
		"ok":       s.Readiness.OK,
		"problems": nonNilStrings(s.Readiness.Problems),
		"checks":   nonNilChecks(s.Readiness.Checks),
		"extras":   nonNilExtras(s.Extras),
		"warnings": nonNilStrings(s.Warnings),
	}
	if kind == provider.KindCardSupply {
		combos := s.Combos
		if combos == nil {
			combos = []planCombo{}
		}
		out["combos"] = combos
	}
	return out
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilChecks(v []provider.Check) []provider.Check {
	if v == nil {
		return []provider.Check{}
	}
	return v
}

func nonNilExtras(v []providerExtra) []providerExtra {
	if v == nil {
		return []providerExtra{}
	}
	return v
}

// cardSupplyAdapter 是供卡型服务商要接的就绪检查。plan、region 都给时检查这一个组合，否则至少要有一个可用组合；
// public 为 true 表示由公开接口触发，会打外部接口的刷新要限频。查询出错一律按未就绪。
// 发码硬门槛（首笔真实付款）和付款引擎的卡池由 checkCardSupply 按服务商统一检查，适配器不用管。
type cardSupplyAdapter interface {
	Check(ctx context.Context, plan, region string, public bool) providerStatus
}

// upstreamAdapter 是上游型服务商的适配器。请求体已经被分派读过，所以方法收解析好的参数，不是 gin 处理函数。
// 预检、兑换前的跨服务商互斥由分派统一处理，适配器不用管。
type upstreamAdapter interface {
	Preview(c *gin.Context, body map[string]any)
	Preflight(c *gin.Context, body map[string]any)
	Redeem(c *gin.Context, body map[string]any)
	Result(c *gin.Context, token string)
	ResultByCode(c *gin.Context, code string)
	Issue(c *gin.Context, req cdkIssueRequest)
	// Check 是后台卡服务商页的就绪检查。
	Check(ctx context.Context) providerStatus
}

// upstreamExchanger 是可选能力：代理换码。code 是用户输入的完整码，调用前已通过限流和密码校验。
type upstreamExchanger interface {
	Exchange(c *gin.Context, code, ip string)
}

// upstreamGraceRecoverer 是可选能力：宽限期恢复。
type upstreamGraceRecoverer interface {
	GraceRecovery(c *gin.Context, body graceRecoveryRequest)
}

// upstreamLookuper 是可选能力：卡密查询页。没实现时查询结果是「未找到」。
type upstreamLookuper interface {
	Lookup(ctx context.Context, code, device string) cdkLookupResult
}

var (
	adapterMu          sync.RWMutex
	cardSupplyAdapters = map[provider.Name]cardSupplyAdapter{provider.MarsWallex: marsWallexAdapter{}}
	upstreamAdapters   = map[provider.Name]upstreamAdapter{provider.ZovoCard: zovoCardAdapter{}}
)

func cardSupplyAdapterFor(n provider.Name) (cardSupplyAdapter, bool) {
	adapterMu.RLock()
	defer adapterMu.RUnlock()
	a, ok := cardSupplyAdapters[n]
	return a, ok && a != nil
}

func upstreamAdapterFor(n provider.Name) (upstreamAdapter, bool) {
	adapterMu.RLock()
	defer adapterMu.RUnlock()
	a, ok := upstreamAdapters[n]
	return a, ok && a != nil
}

// CheckProviderAdapters 检查每家注册了的服务商都接了对应类型的适配器。漏接时拒绝启动：
// 否则它的码能被分派过来，处理时才发现没人接。
func CheckProviderAdapters() error {
	for _, p := range provider.List() {
		switch p.Kind() {
		case provider.KindCardSupply:
			if _, ok := cardSupplyAdapterFor(p.Name()); !ok {
				return fmt.Errorf("供卡型服务商 %s 没有接入就绪检查", p.Name())
			}
		case provider.KindUpstream:
			if _, ok := upstreamAdapterFor(p.Name()); !ok {
				return fmt.Errorf("上游型服务商 %s 没有接入适配器", p.Name())
			}
		default:
			return fmt.Errorf("服务商 %s 的类型 %q 未知", p.Name(), p.Kind())
		}
	}
	return nil
}

// safeStatus 调一家的检查。检查本身出错（panic）时只把这家标为未就绪，不影响其他服务商，也不让接口 500。
func safeStatus(n provider.Name, check func() providerStatus) (st providerStatus) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[provider] %s 就绪检查出错: %v", n, r)
			st = newProviderStatus(n)
			st.Readiness.Fail("check_error", "就绪检查", "就绪检查本身出错，按未就绪处理")
		}
	}()
	st = check()
	if st.Readiness.Provider == "" {
		st.Readiness.Provider = n
	}
	return st
}

// checkCardSupply 是一家供卡型服务商的就绪检查：适配器自己的检查，加上按服务商计算的通用项。发码、预检、兑换都用它。
func checkCardSupply(ctx context.Context, cs provider.CardSupply, plan, region string, public bool) providerStatus {
	n := cs.Name()
	a, ok := cardSupplyAdapterFor(n)
	if !ok {
		st := newProviderStatus(n)
		st.Readiness.Fail("adapter", "接入", "这家服务商没有接入就绪检查")
		return st
	}
	st := safeStatus(n, func() providerStatus { return a.Check(ctx, plan, region, public) })
	if engine := payEngine; engine != nil && !engine.HasPool(string(n)) {
		st.Readiness.Fail("pay_pool", "付款卡池", "付款引擎没有接入这家的卡池")
	}
	checkFirstRealPayment(ctx, &st, cs, plan)
	return st
}

// checkFirstRealPayment 是发码硬门槛：系统没有自动确认过一笔这家的真实付款，就不能对外承诺这条路能走通。
// 试运行单、人工定论的单、别家的单都不算。订阅开通码和点数码分开算（设计 2026-11-03 §3.3、§5.4）：
//   - 订阅开通码：那笔开通单还要已经关闭续费并核实；
//   - 点数码：要有一笔真实点数单成功。
//
// 没指定档位时（后台总览）按订阅开通码判断就绪，点数码的门槛另外记一条提醒，不影响整体就绪。
func checkFirstRealPayment(ctx context.Context, st *providerStatus, cs provider.CardSupply, plan string) {
	n := cs.Name()
	kind := payengine.KindSubscription
	if strings.TrimSpace(plan) != "" {
		kind = payengine.PlanKind(plan)
	}
	passed, err := db.HasRealSucceededPayOrder(ctx, string(n), kind)
	switch {
	case err != nil:
		log.Printf("[provider] 查询 %s 真实付款记录失败: %v", n, err)
		st.Readiness.Fail("first_real_payment", "首笔真实付款", "查询真实付款记录失败，按未通过处理")
	case !passed && kind == payengine.KindCredit:
		st.Readiness.Fail("first_real_payment", "首笔真实付款", "需要一笔系统自动确认成功的真实点数付款（订阅开通单、试运行单和人工定论的单都不算），请先在后台测试兑换跑通一笔点数单")
	case !passed:
		st.Readiness.Fail("first_real_payment", "首笔真实付款", "需要一笔系统自动确认成功的真实付款，并且开通后已关闭续费（试运行单、人工定论的单、续费没关掉的单都不算），请先在后台测试兑换跑通一笔")
	case kind == payengine.KindCredit:
		st.Readiness.Pass("first_real_payment", "首笔真实付款", "已有系统自动确认成功的真实点数付款")
	default:
		st.Readiness.Pass("first_real_payment", "首笔真实付款", "已有系统自动确认成功、并已关闭续费的真实开通付款")
	}
	if strings.TrimSpace(plan) != "" || !cs.Supports("gpt", payengine.CreditPlanKeys[0]) {
		return
	}
	switch ok, err := db.HasRealSucceededPayOrder(ctx, string(n), payengine.KindCredit); {
	case err != nil:
		log.Printf("[provider] 查询 %s 真实点数付款记录失败: %v", n, err)
		st.Warnings = append(st.Warnings, "查询真实点数付款记录失败，点数码暂按未通过处理")
	case !ok:
		st.Warnings = append(st.Warnings, "点数码还没有首笔系统自动确认成功的真实点数付款，发点数码之前要先在后台测试兑换跑通一笔")
	}
}

// checkProvider 是后台用的就绪检查，任意类型。
func checkProvider(ctx context.Context, p provider.Provider, plan, region string) providerStatus {
	if cs, ok := provider.AsCardSupply(p); ok {
		return checkCardSupply(ctx, cs, plan, region, false)
	}
	a, ok := upstreamAdapterFor(p.Name())
	if !ok {
		st := newProviderStatus(p.Name())
		st.Readiness.Fail("adapter", "接入", "这家服务商没有接入适配器")
		return st
	}
	return safeStatus(p.Name(), func() providerStatus { return a.Check(ctx) })
}

// ---- 公开接口的分派 ----

const unrecognizedCodeText = "无法识别的卡密"

// providerFields 是本地码公开响应里的服务商字段。上游转发的原始 JSON 不加，前端看到没有 provider_kind 时按上游处理。
func providerFields(p provider.Provider) gin.H {
	return gin.H{
		"provider":              string(p.Name()),
		"provider_kind":         string(p.Kind()),
		"provider_display_name": p.DisplayName(),
	}
}

const localProviderKey = "local_cdk_provider"

// withCardSupply 记下这次请求分派到的供卡型服务商，本地流程的每个响应都带上它的服务商字段（见 localReply）。
func withCardSupply(c *gin.Context, cs provider.CardSupply) { c.Set(localProviderKey, cs) }

func requestCardSupply(c *gin.Context) (provider.CardSupply, bool) {
	v, ok := c.Get(localProviderKey)
	if !ok {
		return nil, false
	}
	cs, ok := v.(provider.CardSupply)
	return cs, ok
}

// localReply 写本地流程的响应：分派过的请求一律带上服务商字段，成功和失败都带，前端据此决定用哪套界面。
// 响应自己写了 provider 的以响应为准。
func localReply(c *gin.Context, status int, h gin.H) {
	if cs, ok := requestCardSupply(c); ok {
		for k, v := range providerFields(cs) {
			if _, exists := h[k]; !exists {
				h[k] = v
			}
		}
	}
	c.JSON(status, h)
}

// routeCode 按码分派。没有兜底上游又没人认领时写好 404。
func routeCode(c *gin.Context, code string) (provider.Provider, bool) {
	p, err := provider.ForCode(code)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": unrecognizedCodeText})
		return nil, false
	}
	return p, true
}

// routeToken 按兑换令牌分派，规则同 routeCode。
func routeToken(c *gin.Context, token string) (provider.Provider, bool) {
	p, err := provider.ForToken(token)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": unrecognizedCodeText})
		return nil, false
	}
	return p, true
}

// routeBody 是预检、兑换的分派：令牌和码各自判定。
//   - 有一个归供卡型就走供卡型，这样供卡型的码和令牌绝不会发往上游；两个都归供卡型时必须是同一家。
//   - 都归上游时必须是同一家：一家的码绝不会跟着另一家的令牌发往那一家。
//   - 都没给时交给兜底上游，由它按现有方式报错。
func routeBody(c *gin.Context, body map[string]any) (provider.Provider, bool) {
	tok, code := str(body["redemption_token"]), str(body["code"])
	if tok == "" && code == "" {
		return routeCode(c, "")
	}
	var byTok, byCode provider.Provider
	var ok bool
	if tok != "" {
		if byTok, ok = routeToken(c, tok); !ok {
			return nil, false
		}
	}
	if code != "" {
		if byCode, ok = routeCode(c, code); !ok {
			return nil, false
		}
	}
	switch {
	case byTok == nil:
		return byCode, true
	case byCode == nil, byTok.Name() == byCode.Name():
		return byTok, true
	}
	_, tokCS := provider.AsCardSupply(byTok)
	_, codeCS := provider.AsCardSupply(byCode)
	switch {
	case tokCS && !codeCS:
		return byTok, true
	case codeCS && !tokCS:
		return byCode, true
	case tokCS && codeCS:
		c.JSON(http.StatusNotFound, gin.H{"error": localCDKNotFoundText})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "卡密和兑换令牌不是同一家服务商的，请重新预览卡密"})
	}
	return nil, false
}

// upstreamOrFail 取上游的适配器。注册表里有、适配器没接上只会是编程错误（启动时会检查），这里按暂不可用处理。
func upstreamOrFail(c *gin.Context, p provider.Provider) (upstreamAdapter, bool) {
	a, ok := upstreamAdapterFor(p.Name())
	if !ok {
		log.Printf("[provider] 上游 %s 没有适配器", p.Name())
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "兑换通道暂时不可用，请稍后再试"})
		return nil, false
	}
	return a, true
}
