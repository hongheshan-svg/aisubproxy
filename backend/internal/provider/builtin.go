package provider

import (
	"slices"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/payengine"
)

func init() {
	Register(zovoCard{})
	Register(marsWallex{})
}

// zovoCard 是卡台：上游发码，四条产品线。具体档位以卡台实时下发为准（发码时校验），这里只认产品线。
// 它是兜底上游：谁都不认领的码和令牌交给它，这是接入多家之前的行为。
type zovoCard struct{}

func (zovoCard) Name() Name              { return ZovoCard }
func (zovoCard) DisplayName() string     { return "ZovoCard" }
func (zovoCard) Kind() Kind              { return KindUpstream }
func (zovoCard) Products() []string      { return []string{"gpt", "x", "grok", "claude"} }
func (zovoCard) PlanNote(string) string  { return "" }
func (zovoCard) ClaimsCode(string) bool  { return false }
func (zovoCard) ClaimsToken(string) bool { return false }
func (zovoCard) Fallback() bool          { return true }
func (zovoCard) CodeList() string        { return "cardplatform" }
func (z zovoCard) Supports(product, _ string) bool {
	return slices.Contains(z.Products(), normProduct(product))
}

// marsWallex 供卡·本站付款。能力直接取自付款引擎实际实现的流程（payengine.SupportedPlanKeys：GPT 五档新开通、Codex 点数六档），
// 引擎以后支持升级时这里自动跟着扩展，不另外维护清单。
type marsWallex struct{}

func (marsWallex) Name() Name          { return MarsWallex }
func (marsWallex) DisplayName() string { return "MarsWallex" }
func (marsWallex) Kind() Kind          { return KindCardSupply }
func (marsWallex) Products() []string  { return []string{"gpt"} }
func (marsWallex) CodePrefix() string  { return "MW-" }
func (marsWallex) TokenPrefix() string { return "mwt_" }
func (marsWallex) Supports(product, plan string) bool {
	return normProduct(product) == "gpt" && slices.Contains(payengine.SupportedPlanKeys(), strings.TrimSpace(plan))
}

// PlanNote pro_20x 在 ZovoCard 是「Plus 升级 Pro」，在 MarsWallex 是新开通，同名不同义，必须标出来。
// 点数档只卖给已订阅 Plus 或 Pro 的账号，按账号计费币种付款（设计 2026-11-03 §5.1）。
func (marsWallex) PlanNote(plan string) string {
	plan = strings.TrimSpace(plan)
	switch {
	case plan == "pro_20x":
		return "仅限无订阅账号"
	case payengine.PlanKind(plan) == payengine.KindCredit:
		return "仅限已订阅 Plus 或 Pro 的账号，按账号计费币种付款"
	}
	return ""
}

// Capability 是能力矩阵的一行。Plan 为 "*" 时代表整条产品线。后台的服务商列表和就绪接口用同一个 Capabilities 生成，形状一致。
type Capability struct {
	Product    string          `json:"product"`
	Plan       string          `json:"plan"`
	Comment    string          `json:"comment"`
	Support    map[Name]bool   `json:"support"`
	Notes      map[Name]string `json:"notes"`
	samplePlan string
}

// Capabilities 由各服务商的 Supports 生成，界面不用另写一份。列出全部已注册的服务商。
func Capabilities() []Capability {
	// 点数各档合成一行 credit*，订阅开通逐档列出
	rows := make([]Capability, 0, len(payengine.SubscriptionPlanKeys)+4)
	for _, p := range payengine.SubscriptionPlanKeys {
		rows = append(rows, Capability{Product: "gpt", Plan: p, samplePlan: p})
	}
	rows = append(rows,
		Capability{Product: "gpt", Plan: "credit*", samplePlan: "credit250", Comment: "Codex 点数"},
		Capability{Product: "x", Plan: "*", samplePlan: "premium_monthly", Comment: "X Premium"},
		Capability{Product: "grok", Plan: "*", samplePlan: "premium_monthly", Comment: "Grok"},
		Capability{Product: "claude", Plan: "*", samplePlan: "pro", Comment: "Claude"},
	)
	list := List()
	for i := range rows {
		r := &rows[i]
		r.Support = make(map[Name]bool, len(list))
		r.Notes = map[Name]string{}
		for _, p := range list {
			r.Support[p.Name()] = p.Supports(r.Product, r.samplePlan)
			if note := p.PlanNote(r.samplePlan); note != "" {
				r.Notes[p.Name()] = note
			}
		}
	}
	return rows
}
