package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/gptcheck"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
)

// 免付款探测：用测试账号的会话，在服务器上确认升级、点数、开通结账页的实际行为。
// 全部不付款；HTTP 部分只走 chatgpt.ProbeAllowlist 里的「方法 + 路径」；浏览器部分只打开、等待、截图、读文字，不点击、不填表。
// 结果、存档、日志里不出现会话、token、完整邮箱、完整卡 id、完整结账链接。

// 探测项 key（顺序即执行顺序与展示顺序）。
var probeKeys = []string{
	"account", "subscriptions", "checkout_modes", "credit_checkout", "payment_methods",
	"upgrade_preview", "customer_portal", "chatgpt_browser", "credit_balance",
}

var probeTitles = map[string]string{
	"account":         "账号概况",
	"subscriptions":   "订阅详情",
	"checkout_modes":  "开通结账会话形态",
	"credit_checkout": "点数结账会话形态",
	"payment_methods": "已存付款方式",
	"upgrade_preview": "升级预览",
	"customer_portal": "账单门户",
	"chatgpt_browser": "浏览器打开 ChatGPT",
	"credit_balance":  "点数余额接口",
}

const (
	probeOK      = "ok"
	probeFail    = "fail"
	probeSkipped = "skipped"

	probeTimeout = 4 * time.Minute
)

// probeResult 是一个探测项的结果。Details 只放脱敏后的字段。
type probeResult struct {
	Key     string         `json:"key"`
	Title   string         `json:"title"`
	Status  string         `json:"status"`
	Summary string         `json:"summary"`
	Details map[string]any `json:"details"`
}

// probeAPI 是探测用到的 ChatGPT 能力，实现是 *chatgpt.Probe；测试里换成指向假服务器的实例或假实现。
type probeAPI interface {
	Refresh(ctx context.Context, s *chatgpt.Session) error
	GetJSON(ctx context.Context, s chatgpt.Session, path string, query url.Values, accountID string) (map[string]interface{}, error)
	ModeCheckout(ctx context.Context, s chatgpt.Session, planName, country, currency string, redirect bool) (chatgpt.ProbeCheckout, error)
	CreditCheckout(ctx context.Context, s chatgpt.Session, accountID string, redirect bool) (chatgpt.ProbeCheckout, error)
}

// probeBrowser 是探测用到的浏览器能力：没有任何点击、键入、填表。
type probeBrowser interface {
	ProbeOpen(ctx context.Context, url string, cookies []paybrowser.Cookie, settle time.Duration) error
	ProbeRead(ctx context.Context) (paybrowser.ProbePage, error)
	Shot(ctx context.Context, name string, scope paybrowser.ShotScope) (string, error)
	Close()
}

var (
	probeNewAPI = func(proxyURL string) (probeAPI, error) {
		c, err := chatgpt.New(proxyURL)
		if err != nil {
			return nil, err
		}
		return chatgpt.NewProbe(c), nil
	}
	probeLaunch = func(ctx context.Context, proxyURL, shotDir string) (probeBrowser, error) {
		b, err := paybrowser.Launch(ctx, paybrowser.LaunchOptions{ProxyURL: proxyURL, ShotDir: shotDir})
		if err != nil {
			return nil, err
		}
		return b, nil
	}
	// probeSettle 是页面 load 之后再等的时间，让 Cloudflare 挑战或前端渲染跑完。
	probeSettle = 6 * time.Second

	// probeBusy 保证同一时刻只运行一个探测（含浏览器）。
	probeBusy atomic.Bool
)

// 白名单域名：结账链接、账单门户的域名在这里才算「在白名单内」；浏览器也只打开白名单内的域名。
var probePayHosts = []string{"pay.openai.com", "checkout.stripe.com", "billing.stripe.com", "chatgpt.com"}

func probeHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, h := range probePayHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

var (
	probeShotDirRe  = regexp.MustCompile(`^probe-[0-9a-f]{8}$`)
	probeShotFileRe = regexp.MustCompile(`^[a-z0-9_]+\.png$`)
)

// ---------------------------------------------------------------- 脱敏

var (
	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reURL   = regexp.MustCompile(`https?://[^\s"'<>)\]]+`)
	reJWT   = regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-.]+`)
	reLong  = regexp.MustCompile(`[A-Za-z0-9_\-]{40,}`)
)

// probeMaskEmail 把邮箱打码成 a***@e***.com，只留首字符和顶级域。
func probeMaskEmail(e string) string {
	e = strings.TrimSpace(e)
	at := strings.LastIndex(e, "@")
	if at <= 0 || at == len(e)-1 {
		return "***"
	}
	local, domain := e[:at], e[at+1:]
	dot := strings.LastIndex(domain, ".")
	if dot <= 0 {
		return string([]rune(local)[:1]) + "***@***"
	}
	return string([]rune(local)[:1]) + "***@" + string([]rune(domain)[:1]) + "***" + domain[dot:]
}

// probeMaskID 给卡 id、账号 id 打码：只留前 3 位和后 4 位。
func probeMaskID(id string) string {
	r := []rune(strings.TrimSpace(id))
	if len(r) <= 8 {
		return "***"
	}
	return string(r[:3]) + "…" + string(r[len(r)-4:])
}

// scrubber 把已知的敏感串和形似敏感串的内容从文字里清掉。双保险：各探测项本来就只输出摘要字段，这里兜底。
type scrubber struct{ secrets []string }

func newScrubber(s chatgpt.Session, raw string, extra ...string) *scrubber {
	sc := &scrubber{}
	for _, v := range append([]string{s.AccessToken, s.SessionToken, strings.TrimSpace(raw)}, extra...) {
		if len(strings.TrimSpace(v)) >= 6 {
			sc.secrets = append(sc.secrets, v)
		}
	}
	// 长的先换，免得短串先把长串切碎
	sort.Slice(sc.secrets, func(i, j int) bool { return len(sc.secrets[i]) > len(sc.secrets[j]) })
	if s.Email != "" {
		sc.secrets = append(sc.secrets, s.Email)
	}
	return sc
}

func (sc *scrubber) str(v string) string {
	for _, sec := range sc.secrets {
		if strings.Contains(sec, "@") {
			v = strings.ReplaceAll(v, sec, probeMaskEmail(sec))
		} else {
			v = strings.ReplaceAll(v, sec, "[已隐藏]")
		}
	}
	v = reURL.ReplaceAllStringFunc(v, func(m string) string {
		h, p := chatgpt.ProbeURLParts(m)
		if h == "" {
			return "[链接已隐藏]"
		}
		return h + p
	})
	v = reJWT.ReplaceAllString(v, "[已隐藏]")
	v = reEmail.ReplaceAllStringFunc(v, probeMaskEmail)
	v = reLong.ReplaceAllString(v, "[已隐藏]")
	return v
}

func (sc *scrubber) walk(v any) any {
	switch x := v.(type) {
	case string:
		return sc.str(x)
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = sc.str(s)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = sc.walk(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[sc.str(k)] = sc.walk(e)
		}
		return out
	default:
		return v
	}
}

func (sc *scrubber) result(r probeResult) probeResult {
	r.Summary = sc.str(r.Summary)
	if r.Details == nil {
		r.Details = map[string]any{}
	}
	r.Details, _ = sc.walk(r.Details).(map[string]any)
	return r
}

// ---------------------------------------------------------------- 取值辅助

func probeMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func probeStr(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// probeFind 在多个对象里按键名依次查找，返回第一个出现的值。
func probeFind(maps []map[string]any, keys ...string) (any, bool) {
	for _, k := range keys {
		for _, m := range maps {
			if v, ok := m[k]; ok {
				return v, true
			}
		}
	}
	return nil, false
}

// probeScalar 只让标量值进结果；对象、数组只写类型，避免把整块返回体带出去。
func probeScalar(v any) any {
	switch x := v.(type) {
	case nil, bool, float64:
		return x
	case string:
		r := []rune(x)
		if len(r) > 60 {
			return string(r[:60]) + "…"
		}
		return x
	case map[string]any:
		return "[对象]"
	case []any:
		return fmt.Sprintf("[数组 %d 项]", len(x))
	}
	return "[未知类型]"
}

func probeSortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

// probeFields 按字段列表汇总「有没有、值是什么」。keys 里每项可有多个候选键名。
func probeFields(maps []map[string]any, fields [][]string) map[string]any {
	out := map[string]any{}
	for _, f := range fields {
		v, ok := probeFind(maps, f...)
		if ok {
			out[f[0]] = map[string]any{"present": true, "value": probeScalar(v)}
		} else {
			out[f[0]] = map[string]any{"present": false}
		}
	}
	return out
}

func probeMissing(fields map[string]any) []string {
	var out []string
	for k, v := range fields {
		if m := probeMap(v); m != nil && m["present"] == false {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// probeErr 把调用错误写成人能看的说明。
func probeErr(err error) string {
	switch {
	case errors.Is(err, chatgpt.ErrBlocked):
		return "被 Cloudflare 拦截：" + err.Error()
	case errors.Is(err, chatgpt.ErrUnauthorized):
		return "会话未授权或已失效：" + err.Error()
	}
	return err.Error()
}

// ---------------------------------------------------------------- 运行一次探测

type probeAccount struct {
	id        string
	planType  string
	hasSub    bool
	structure string
	raw       map[string]any // 默认账号的 accounts/check 片段（account、entitlement、last_active_subscription）
}

type probeRun struct {
	ctx       context.Context
	api       probeAPI
	sess      chatgpt.Session
	region    string
	proxy     string
	shotRoot  string // 截图根目录
	shotDir   string // 本次探测的截图子目录名，空表示还没有截图
	sc        *scrubber
	refreshed bool
	refreshEr error
	acctDone  bool
	acct      *probeAccount
	acctErr   error
}

func (r *probeRun) fail(key, format string, a ...any) probeResult {
	return probeResult{Key: key, Title: probeTitles[key], Status: probeFail, Summary: fmt.Sprintf(format, a...)}
}

func (r *probeRun) skip(key, format string, a ...any) probeResult {
	return probeResult{Key: key, Title: probeTitles[key], Status: probeSkipped, Summary: fmt.Sprintf(format, a...)}
}

func (r *probeRun) ok(key, summary string, details map[string]any) probeResult {
	return probeResult{Key: key, Title: probeTitles[key], Status: probeOK, Summary: summary, Details: details}
}

// ensureRefresh 刷新一次 accessToken。会话里自带的 accessToken 可能已过期。
func (r *probeRun) ensureRefresh() error {
	if !r.refreshed {
		r.refreshed = true
		r.refreshEr = r.api.Refresh(r.ctx, &r.sess)
		if r.refreshEr != nil {
			r.refreshEr = fmt.Errorf("刷新会话失败：%s", probeErr(r.refreshEr))
		} else if len(r.sess.AccessToken) >= 6 {
			r.sc.secrets = append(r.sc.secrets, r.sess.AccessToken) // 刷新出的新 token 也要擦
		}
	}
	return r.refreshEr
}

// account 读一次 accounts/check 并缓存。
func (r *probeRun) account() (*probeAccount, error) {
	if r.acctDone {
		return r.acct, r.acctErr
	}
	r.acctDone = true
	if err := r.ensureRefresh(); err != nil {
		r.acctErr = err
		return nil, err
	}
	raw, err := r.api.GetJSON(r.ctx, r.sess, chatgpt.ProbeAccountCheck, nil, "")
	if err != nil {
		r.acctErr = fmt.Errorf("读取账号信息失败：%s", probeErr(err))
		return nil, r.acctErr
	}
	def := gptcheck.DefaultAccount(raw)
	info := probeMap(def["account"])
	ent := probeMap(def["entitlement"])
	a := &probeAccount{raw: def}
	a.id = probeStr(info["account_id"])
	if a.id == "" {
		a.id = probeStr(info["id"])
	}
	a.planType = strings.ToLower(probeStr(info["plan_type"]))
	a.structure = probeStr(info["structure"])
	a.hasSub, _ = ent["has_active_subscription"].(bool)
	if !a.hasSub {
		a.planType = "free"
	}
	r.acct = a
	// 账号 id 不算机密，但输出里只给打码形式
	r.sc.secrets = append(r.sc.secrets, a.id)
	return a, nil
}

func (r *probeRun) accountMaps(a *probeAccount) []map[string]any {
	return []map[string]any{probeMap(a.raw["account"]), probeMap(a.raw["entitlement"]), probeMap(a.raw["last_active_subscription"]), a.raw}
}

func (r *probeRun) runOne(key string) probeResult {
	switch key {
	case "account":
		return r.checkAccount()
	case "subscriptions":
		return r.checkSubscriptions()
	case "checkout_modes":
		return r.checkCheckoutModes()
	case "credit_checkout":
		return r.checkCreditCheckout()
	case "payment_methods":
		return r.checkPaymentMethods()
	case "upgrade_preview":
		return r.checkUpgradePreview()
	case "customer_portal":
		return r.checkCustomerPortal()
	case "chatgpt_browser":
		return r.checkChatGPTBrowser()
	case "credit_balance":
		return r.checkCreditBalance()
	}
	return r.fail(key, "未知探测项")
}

// needAccount 取账号；取不到时返回现成的失败结果。
func (r *probeRun) needAccount(key string) (*probeAccount, *probeResult) {
	a, err := r.account()
	if err != nil {
		res := r.fail(key, "%s", err.Error())
		return nil, &res
	}
	if a.id == "" {
		res := r.fail(key, "账号信息里没有 account_id，无法继续")
		return nil, &res
	}
	return a, nil
}

func (r *probeRun) checkAccount() probeResult {
	a, err := r.account()
	if err != nil {
		return r.fail("account", "%s", err.Error())
	}
	maps := r.accountMaps(a)
	fields := probeFields(maps, [][]string{
		{"plan_type"}, {"has_active_subscription"},
		{"billing_currency", "currency", "billing_currency_code"},
		{"structure"}, {"is_delinquent", "delinquent"},
		{"purchase_origin_platform", "subscription_origin_platform", "origin_platform"},
		{"will_renew"}, {"subscription_plan"}, {"billing_period"},
	})
	d := map[string]any{
		"email":      probeMaskEmail(r.sess.Email),
		"account_id": probeMaskID(a.id),
		"fields":     fields,
	}
	if m := probeMissing(fields); len(m) > 0 {
		d["missing"] = m
	}
	sub := "无生效订阅"
	if a.hasSub {
		sub = "有生效订阅"
	}
	return r.ok("account", fmt.Sprintf("plan_type=%s，%s，structure=%s", a.planType, sub, a.structure), d)
}

func (r *probeRun) checkSubscriptions() probeResult {
	a, bad := r.needAccount("subscriptions")
	if bad != nil {
		return *bad
	}
	data, err := r.api.GetJSON(r.ctx, r.sess, chatgpt.ProbeSubscriptions, url.Values{"account_id": {a.id}}, a.id)
	if err != nil {
		return r.fail("subscriptions", "GET /subscriptions 失败：%s", probeErr(err))
	}
	fields := probeFields([]map[string]any{data}, [][]string{
		{"will_renew"}, {"renews_at"}, {"billing_period"}, {"is_delinquent"},
		{"became_delinquent_timestamp"}, {"grace_period_end_timestamp"}, {"cancels_at"},
		{"plan_type", "plan"}, {"status"}, {"billing_currency", "currency"},
	})
	d := map[string]any{"fields": fields, "top_keys": probeSortedKeys(data)}
	missing := probeMissing(fields)
	d["missing"] = missing
	return r.ok("subscriptions", fmt.Sprintf("返回 %d 个顶层字段；未出现：%s", len(data), joinOrNone(missing)), d)
}

func joinOrNone(s []string) string {
	if len(s) == 0 {
		return "无"
	}
	return strings.Join(s, "、")
}

// probeRegionBilling 返回地区的国家码和币种（取自默认套餐表的 Plus 行），以及 Plus 的 ChatGPT 套餐名。
func probeRegionBilling(region string) (country, currency, planName string) {
	if region == "" {
		region = "US"
	}
	for _, p := range payengine.DefaultPlanTable() {
		if p.Key == "plus" {
			planName = p.ChatGPTPlanName
			currency = p.Regions[region].Currency
		}
	}
	return region, currency, planName
}

// probeShape 把建会话的返回摘要变成可输出的字段。
func probeShape(co chatgpt.ProbeCheckout) map[string]any {
	d := map[string]any{"shape": string(co.Shape), "keys": co.Keys}
	if co.Host != "" {
		d["host"] = co.Host
		d["path_prefix"] = co.PathPrefix
		d["host_whitelisted"] = probeHostAllowed(co.Host)
	}
	if co.Processor != "" {
		d["processor_entity"] = co.Processor
	}
	if co.SessionPfx != "" {
		d["session_prefix"] = co.SessionPfx
	}
	if co.Currency != "" {
		d["currency"] = co.Currency
	}
	return d
}

func probeShapeText(co chatgpt.ProbeCheckout) string {
	switch co.Shape {
	case chatgpt.ShapeURL:
		wl := "不在白名单内"
		if probeHostAllowed(co.Host) {
			wl = "在白名单内"
		}
		return fmt.Sprintf("url（%s，%s）", co.Host, wl)
	case chatgpt.ShapeCustom:
		return "custom_checkout_session"
	}
	return "其他形态"
}

// probeCheckoutPair 分别「不带」「带」redirect 各建一次会话，汇总成结果。
func (r *probeRun) probeCheckoutPair(key string, call func(redirect bool) (chatgpt.ProbeCheckout, error)) probeResult {
	d := map[string]any{}
	var parts []string
	okCount := 0
	for _, v := range []struct {
		name     string
		redirect bool
	}{{"without_redirect", false}, {"with_redirect", true}} {
		label := "不带 checkout_ui_mode"
		if v.redirect {
			label = `带 checkout_ui_mode:"redirect"`
		}
		co, err := call(v.redirect)
		if err != nil {
			d[v.name] = map[string]any{"error": probeErr(err)}
			parts = append(parts, label+"：失败（"+probeErr(err)+"）")
			continue
		}
		okCount++
		d[v.name] = probeShape(co)
		parts = append(parts, label+"：返回 "+probeShapeText(co))
	}
	if okCount == 0 {
		res := r.fail(key, "两次建会话都失败。%s", strings.Join(parts, "；"))
		res.Details = d
		return res
	}
	return r.ok(key, strings.Join(parts, "；"), d)
}

func (r *probeRun) checkCheckoutModes() probeResult {
	a, bad := r.needAccount("checkout_modes")
	if bad != nil {
		return *bad
	}
	if a.hasSub {
		return r.skip("checkout_modes", "账号已有生效订阅（plan_type=%s），不建开通会话，避免产生第二个订阅", a.planType)
	}
	country, currency, planName := probeRegionBilling(r.region)
	if currency == "" || planName == "" {
		return r.fail("checkout_modes", "无法确定地区 %s 的币种或 Plus 套餐名", country)
	}
	res := r.probeCheckoutPair("checkout_modes", func(redirect bool) (chatgpt.ProbeCheckout, error) {
		return r.api.ModeCheckout(r.ctx, r.sess, planName, country, currency, redirect)
	})
	note := ""
	if r.region == "" {
		note = "（未指定地区，按 US/USD 建会话）"
	}
	res.Summary = "Plus " + country + "/" + currency + note + "：" + res.Summary
	return res
}

func (r *probeRun) checkCreditCheckout() probeResult {
	a, bad := r.needAccount("credit_checkout")
	if bad != nil {
		return *bad
	}
	if !a.hasSub {
		return r.skip("credit_checkout", "账号没有生效订阅，点数结账需要先订阅 Plus 或 Pro")
	}
	res := r.probeCheckoutPair("credit_checkout", func(redirect bool) (chatgpt.ProbeCheckout, error) {
		return r.api.CreditCheckout(r.ctx, r.sess, a.id, redirect)
	})
	res.Summary = "250 点：" + res.Summary
	return res
}

func (r *probeRun) checkPaymentMethods() probeResult {
	a, bad := r.needAccount("payment_methods")
	if bad != nil {
		return *bad
	}
	data, err := r.api.GetJSON(r.ctx, r.sess, chatgpt.ProbePaymentMethod, url.Values{"account_id": {a.id}}, a.id)
	if err != nil {
		return r.fail("payment_methods", "GET /payments/payment_methods 失败：%s", probeErr(err))
	}
	count := 0
	if list, ok := data["payment_methods"].([]any); ok {
		count = len(list)
	}
	defID := probeStr(data["default_payment_method_id"])
	d := map[string]any{"count": count, "has_default": defID != "", "top_keys": probeSortedKeys(data)}
	if defID != "" {
		d["default_id"] = probeMaskID(defID)
	}
	// 备用付款方式的开关：字段名未知，收集键名里带 backup 的布尔值（顶层和下一层）
	backup := map[string]any{}
	var scan func(prefix string, m map[string]any, depth int)
	scan = func(prefix string, m map[string]any, depth int) {
		for k, v := range m {
			path := prefix + k
			if b, ok := v.(bool); ok && strings.Contains(strings.ToLower(k), "backup") {
				backup[path] = b
			}
			if sub, ok := v.(map[string]any); ok && depth < 2 {
				scan(path+".", sub, depth+1)
			}
		}
	}
	scan("", data, 0)
	d["backup_switches"] = backup
	bt := "返回里没有带 backup 的开关字段"
	if len(backup) > 0 {
		var kv []string
		for _, k := range probeSortedKeys(backup) {
			kv = append(kv, fmt.Sprintf("%s=%v", k, backup[k]))
		}
		bt = "备用付款方式：" + strings.Join(kv, "，")
	}
	return r.ok("payment_methods", fmt.Sprintf("已存卡 %d 张，%s默认卡；%s", count, map[bool]string{true: "有", false: "没有"}[defID != ""], bt), d)
}

// probeTiers 是升级预览的档位，按从低到高排列；顺序是推断（Plus < Pro 5x < Pro 20x < Pro 50x），实际以接口返回为准。
var probeTiers = []struct{ planType, updated string }{
	{"plus", "chatgptplusplan"}, {"prolite", "chatgptprolite"}, {"pro", "chatgptpro"}, {"promax", "chatgptpromax"},
}

func (r *probeRun) checkUpgradePreview() probeResult {
	a, bad := r.needAccount("upgrade_preview")
	if bad != nil {
		return *bad
	}
	if !a.hasSub {
		return r.skip("upgrade_preview", "账号没有生效订阅，无从升级")
	}
	cur := -1
	for i, t := range probeTiers {
		if t.planType == a.planType {
			cur = i
		}
	}
	if cur == 0 || cur < 0 {
		cur = 0 // 当前是 Plus，或档位不认识：试 Plus 以上的全部档
	}
	if cur >= len(probeTiers)-1 {
		return r.skip("upgrade_preview", "当前 plan_type=%s 已是最高档，没有更高的档可预览", a.planType)
	}
	d := map[string]any{}
	var parts []string
	okCount := 0
	for _, t := range probeTiers[cur+1:] {
		data, err := r.api.GetJSON(r.ctx, r.sess, chatgpt.ProbeUpdatePreview, url.Values{"account_id": {a.id}, "updated_plan": {t.updated}}, a.id)
		if err != nil {
			d[t.updated] = map[string]any{"error": probeErr(err)}
			parts = append(parts, t.updated+"：失败（"+probeErr(err)+"）")
			continue
		}
		okCount++
		due := probeMap(data["amount_due"])
		item := map[string]any{
			"amount_due":                   probeScalar(due["amount"]),
			"currency":                     probeScalar(data["currency"]),
			"renewal_date":                 probeScalar(data["renewal_date"]),
			"has_default_payment_method":   data["default_payment_method"] != nil,
			"has_positive_line_item_total": data["positive_line_item_total"] != nil,
			"has_negative_line_item_total": data["negative_line_item_total"] != nil,
			"has_applied_balance":          data["applied_balance"] != nil,
			"has_amount_due_excluding_tax": due["amount_excluding_tax"] != nil,
			"has_amount_due_tax_amount":    due["tax_amount"] != nil,
			"top_keys":                     probeSortedKeys(data),
		}
		d[t.updated] = item
		parts = append(parts, fmt.Sprintf("%s：应付 %v %v，下次续费 %v，%s默认卡", t.updated, item["amount_due"], item["currency"], item["renewal_date"],
			map[bool]string{true: "有", false: "无"}[item["has_default_payment_method"] == true]))
	}
	if okCount == 0 {
		res := r.fail("upgrade_preview", "各档预览都失败。%s", strings.Join(parts, "；"))
		res.Details = d
		return res
	}
	return r.ok("upgrade_preview", strings.Join(parts, "；"), d)
}

func (r *probeRun) checkCreditBalance() probeResult {
	if err := r.ensureRefresh(); err != nil {
		return r.fail("credit_balance", "%s", err.Error())
	}
	data, err := r.api.GetJSON(r.ctx, r.sess, chatgpt.ProbeUsage, nil, "")
	if err != nil {
		return r.fail("credit_balance", "GET /wham/usage 失败：%s", probeErr(err))
	}
	paths := []string{}
	likely := map[string]any{}
	hint := regexp.MustCompile(`(?i)credit|balance|remaining|limit|used|reset|plan`)
	var walk func(prefix string, m map[string]any, depth int)
	walk = func(prefix string, m map[string]any, depth int) {
		for _, k := range probeSortedKeys(m) {
			v := m[k]
			p := prefix + k
			switch x := v.(type) {
			case map[string]any:
				paths = append(paths, p+" (对象)")
				if depth < 3 {
					walk(p+".", x, depth+1)
				}
			case []any:
				paths = append(paths, fmt.Sprintf("%s (数组 %d)", p, len(x)))
			default:
				paths = append(paths, fmt.Sprintf("%s (%T)", p, x))
				if hint.MatchString(k) {
					likely[p] = probeScalar(v)
				}
			}
		}
	}
	walk("", data, 0)
	if len(paths) > 120 {
		paths = paths[:120]
	}
	return r.ok("credit_balance", fmt.Sprintf("返回 %d 个顶层字段，疑似点数相关的标量 %d 个", len(data), len(likely)),
		map[string]any{"fields": paths, "credit_like": likely})
}

// ---------------------------------------------------------------- 浏览器探测

// browse 启动浏览器打开 pageURL，读页面并截图，用完关闭。截图失败不算探测失败。
func (r *probeRun) browse(shotName, pageURL string, cookies []paybrowser.Cookie) (paybrowser.ProbePage, string, string, error) {
	if r.shotDir == "" {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		r.shotDir = "probe-" + hex.EncodeToString(b)
	}
	dir := filepath.Join(r.shotRoot, r.shotDir)
	br, err := probeLaunch(r.ctx, r.proxy, dir)
	if err != nil {
		return paybrowser.ProbePage{}, "", "", fmt.Errorf("启动浏览器失败：%s", err.Error())
	}
	defer br.Close()
	if err := br.ProbeOpen(r.ctx, pageURL, cookies, probeSettle); err != nil {
		return paybrowser.ProbePage{}, "", "", fmt.Errorf("打开页面失败：%s", err.Error())
	}
	pg, err := br.ProbeRead(r.ctx)
	if err != nil {
		return paybrowser.ProbePage{}, "", "", err
	}
	shot, shotErr := br.Shot(r.ctx, shotName, paybrowser.ShotPage)
	note := ""
	if shotErr != nil {
		note = "截图失败：" + shotErr.Error()
		shot = ""
	} else {
		shot = r.shotDir + "/" + shot
	}
	return pg, shot, note, nil
}

// 账单门户页面上的关键词（小写匹配），只用于报告「页面上有没有这类入口」。
var (
	portalAddWords    = []string{"add payment method", "add a payment method", "add card", "add a card", "添加付款方式", "添加支付方式", "添加银行卡", "添加卡"}
	portalChangeWords = []string{"update plan", "change plan", "cancel plan", "cancel subscription", "更新套餐", "取消套餐", "取消订阅"}
)

func probeMatches(text string, words []string) []string {
	t := strings.ToLower(text)
	var out []string
	for _, w := range words {
		if strings.Contains(t, w) {
			out = append(out, w)
		}
	}
	return out
}

func (r *probeRun) checkCustomerPortal() probeResult {
	a, bad := r.needAccount("customer_portal")
	if bad != nil {
		return *bad
	}
	data, err := r.api.GetJSON(r.ctx, r.sess, chatgpt.ProbeCustomerPortal, url.Values{"account_id": {a.id}}, a.id)
	if err != nil {
		return r.fail("customer_portal", "GET /payments/customer_portal 失败：%s", probeErr(err))
	}
	raw := probeStr(data["url"])
	if raw == "" {
		res := r.fail("customer_portal", "返回里没有 url")
		res.Details = map[string]any{"top_keys": probeSortedKeys(data)}
		return res
	}
	host, prefix := chatgpt.ProbeURLParts(raw)
	d := map[string]any{"portal_host": host, "portal_path_prefix": prefix, "host_whitelisted": probeHostAllowed(host)}
	u, perr := url.Parse(raw)
	if perr != nil || u.Scheme != "https" || !probeHostAllowed(host) {
		res := r.fail("customer_portal", "门户链接的域名 %s 不在白名单内（或不是 https），没有打开", host)
		res.Details = d
		return res
	}
	// 门户链接本身带会话凭据，只交给浏览器，不进任何输出；不注入 chatgpt.com 的 cookie
	r.sc.secrets = append(r.sc.secrets, raw)
	pg, shot, note, err := r.browse("customer_portal", raw, nil)
	if err != nil {
		res := r.fail("customer_portal", "门户链接域名 %s（在白名单内）；%s", host, err.Error())
		res.Details = d
		return res
	}
	fh, fp := chatgpt.ProbeURLParts(pg.URL)
	d["final_host"], d["final_path_prefix"], d["page_title"] = fh, fp, pg.Title
	d["screenshot"], d["screenshot_note"] = shot, note
	if paybrowser.IsCFChallengePage(pg) {
		d["cloudflare_blocked"] = true
		res := r.fail("customer_portal", "门户页被 Cloudflare 挑战拦截（%s），如实记录，不尝试绕过", fh)
		res.Details = d
		return res
	}
	add, change := probeMatches(pg.Text, portalAddWords), probeMatches(pg.Text, portalChangeWords)
	d["add_payment_entry"], d["add_payment_matches"] = len(add) > 0, add
	d["plan_change_entry"], d["plan_change_matches"] = len(change) > 0, change
	d["text_length"] = len([]rune(pg.Text))
	sum := fmt.Sprintf("门户域名 %s（在白名单内），页面已打开，%s「添加付款方式」类入口，%s改套餐或取消入口", fh,
		map[bool]string{true: "有", false: "没有"}[len(add) > 0], map[bool]string{true: "有", false: "没有"}[len(change) > 0])
	return r.ok("customer_portal", sum, d)
}

func (r *probeRun) checkChatGPTBrowser() probeResult {
	var cookies []paybrowser.Cookie
	for _, c := range chatgpt.SessionCookies(r.sess.SessionToken) {
		cookies = append(cookies, paybrowser.Cookie{Name: c.Name, Value: c.Value, Domain: ".chatgpt.com"})
	}
	pg, shot, note, err := r.browse("chatgpt_browser", "https://chatgpt.com/", cookies)
	if err != nil {
		return r.fail("chatgpt_browser", "%s", err.Error())
	}
	fh, fp := chatgpt.ProbeURLParts(pg.URL)
	d := map[string]any{"final_host": fh, "final_path_prefix": fp, "page_title": pg.Title, "text_length": len([]rune(pg.Text)),
		"screenshot": shot, "screenshot_note": note}
	if paybrowser.IsCFChallengePage(pg) {
		d["cloudflare_blocked"] = true
		res := r.fail("chatgpt_browser", "chatgpt.com 被 Cloudflare 挑战拦截（标题：%s），如实记录，不尝试绕过", pg.Title)
		res.Details = d
		return res
	}
	if len(strings.TrimSpace(pg.Text)) == 0 {
		d["cloudflare_blocked"] = false
		res := r.fail("chatgpt_browser", "页面打开了但没有任何可见文字，可能没渲染出来")
		res.Details = d
		return res
	}
	lower := strings.ToLower(pg.Text)
	loggedOut := strings.Contains(lower, "log in") && strings.Contains(lower, "sign up")
	d["cloudflare_blocked"] = false
	d["looks_logged_out"] = loggedOut
	sum := "chatgpt.com 正常渲染，没有 Cloudflare 挑战"
	if loggedOut {
		sum += "；页面像是未登录态（出现 Log in 与 Sign up），会话 cookie 可能没被接受"
	}
	return r.ok("chatgpt_browser", sum, d)
}

// ---------------------------------------------------------------- HTTP

type probeRequest struct {
	Session json.RawMessage `json:"session"`
	Checks  []string        `json:"checks"`
	Region  string          `json:"region"`
}

// probeShotRoot 是截图根目录：引擎在跑就用它的，否则与 payengine 同一规则（数据库目录下的 pay-shots）。
func probeShotRoot() string {
	if payEngine != nil && payEngine.ShotDir() != "" {
		return payEngine.ShotDir()
	}
	dbPath := "../data/cdk_recharge.db"
	if p := strings.TrimSpace(os.Getenv("DB_PATH")); p != "" {
		dbPath = p
	}
	return filepath.Join(filepath.Dir(dbPath), "pay-shots")
}

// AdminPayProbe 运行一次免付款探测。同一时刻只运行一个，第二个请求返回 409。
func AdminPayProbe(c *gin.Context) {
	var req probeRequest
	if !bindPayJSON(c, &req) {
		return
	}
	session := strings.TrimSpace(string(req.Session))
	if strings.HasPrefix(session, `"`) {
		var str string
		if err := json.Unmarshal(req.Session, &str); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求体无效"})
			return
		}
		session = str
	}
	if session == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先粘贴完整的 session JSON"})
		return
	}
	sess, err := chatgpt.ParseSession(session)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "会话无效，请粘贴完整的 session JSON"})
		return
	}
	want := map[string]bool{}
	for _, k := range req.Checks {
		k = strings.TrimSpace(k)
		if probeTitles[k] == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "未知的探测项"})
			return
		}
		want[k] = true
	}
	if len(want) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请至少选一个探测项"})
		return
	}
	region := strings.ToUpper(strings.TrimSpace(req.Region))
	if region != "" {
		known := false
		for _, r := range payengine.Regions {
			known = known || r == region
		}
		if !known {
			c.JSON(http.StatusBadRequest, gin.H{"error": "未知地区"})
			return
		}
	}
	proxy := ""
	if region != "" {
		if px, err := payengine.LoadProxies(); err != nil {
			log.Printf("pay probe: 读取代理配置失败: %v", err)
		} else {
			proxy = px[region]
		}
	}

	if !probeBusy.CompareAndSwap(false, true) {
		c.JSON(http.StatusConflict, gin.H{"error": "已有探测正在运行，请等它结束再试"})
		return
	}
	defer probeBusy.Store(false)

	api, err := probeNewAPI(proxy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "创建 ChatGPT 客户端失败，请检查该地区的代理配置"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), probeTimeout)
	defer cancel()
	run := &probeRun{ctx: ctx, api: api, sess: sess, region: region, proxy: proxy, shotRoot: probeShotRoot(), sc: newScrubber(sess, session)}

	var keys []string
	results := []probeResult{}
	for _, k := range probeKeys {
		if !want[k] {
			continue
		}
		keys = append(keys, k)
		results = append(results, run.sc.result(run.runOne(k)))
	}
	// 只留日志里的次数，不记任何内容
	log.Printf("pay probe: 完成 %d 项（%s）", len(results), adminName(c))

	checksJSON, _ := json.Marshal(keys)
	resultsJSON, _ := json.Marshal(results)
	rec := &db.PayProbe{CreatedAt: time.Now().Unix(), Admin: adminName(c), Region: region, ChecksJSON: string(checksJSON), ResultsJSON: string(resultsJSON), ShotDir: run.shotDir}
	resp := gin.H{"created_at": rec.CreatedAt, "region": region, "results": results}
	pruned, err := db.InsertPayProbe(c.Request.Context(), rec)
	if err != nil {
		log.Printf("pay probe: 保存记录失败: %v", err)
		resp["id"] = 0
		resp["save_error"] = "结果没能保存到历史记录"
	} else {
		resp["id"] = rec.ID
		root := probeShotRoot()
		for _, d := range pruned {
			if probeShotDirRe.MatchString(d) && isPlainDir(filepath.Join(root, d)) {
				_ = os.RemoveAll(filepath.Join(root, d))
			}
		}
	}
	auditAdmin(c, "pay_probe", fmt.Sprintf("checks=%s region=%s", strings.Join(keys, ","), region))
	c.JSON(http.StatusOK, resp)
}

// AdminPayProbeList 返回最近的探测记录（最多 50 条，新的在前）。
func AdminPayProbeList(c *gin.Context) {
	list, err := db.ListPayProbes(c.Request.Context())
	if err != nil {
		log.Printf("pay probe list: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取探测记录失败"})
		return
	}
	out := make([]gin.H, 0, len(list))
	for _, p := range list {
		var checks []string
		var results []probeResult
		_ = json.Unmarshal([]byte(p.ChecksJSON), &checks)
		_ = json.Unmarshal([]byte(p.ResultsJSON), &results)
		out = append(out, gin.H{"id": p.ID, "created_at": p.CreatedAt, "admin": p.Admin, "region": p.Region, "checks": checks, "results": results})
	}
	c.JSON(http.StatusOK, gin.H{"probes": out})
}

// AdminPayProbeShot 返回探测截图。目录名与文件名先按正则校验，再确认是普通文件。
func AdminPayProbeShot(c *gin.Context) {
	dir, name := c.Param("dir"), c.Param("name")
	if !probeShotDirRe.MatchString(dir) || !probeShotFileRe.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "截图参数无效"})
		return
	}
	d := filepath.Join(probeShotRoot(), dir)
	if !isPlainDir(d) || !isPlainFile(filepath.Join(d, name)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "截图不存在"})
		return
	}
	b, err := os.ReadFile(filepath.Join(d, name))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "截图不存在"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "image/png", b)
}

// RegisterPayProbeRoutes 挂载免付款探测的后台接口（g 是已带管理员鉴权的分组）。
func RegisterPayProbeRoutes(g *gin.RouterGroup) {
	g.POST("/pay/probe", AdminPayProbe)
	g.GET("/pay/probes", AdminPayProbeList)
	g.GET("/pay/probe-shots/:dir/:name", AdminPayProbeShot)
}
