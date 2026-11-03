package cardpool

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// PolicySettingKey 是策略在 site_settings 中的键。
const PolicySettingKey = "mw_cardpool_policy"

// 资金上限默认值（单位：分）。上限必须大于 0，不存在「0 表示不限」。
const (
	DefaultMaxFundCents      int64 = 50000  // 单笔注资 500 美元
	DefaultMaxWalletOpCents  int64 = 200000 // 单笔钱包转账、兑换 2000 美元
	DefaultDailyFundCapCents int64 = 200000 // 单日注资累计 2000 美元（北京日）
)

// ProductTarget 为单个卡产品的补卡目标与每日开卡上限。
type ProductTarget struct {
	Target     int `json:"target"`
	DailyLimit int `json:"daily_limit"`
}

// Policy 为卡池策略。
type Policy struct {
	PlanProducts         map[string][]string      `json:"plan_products"` // 键 "default" 为兜底
	ProductTargets       map[string]ProductTarget `json:"product_targets"`
	HolderIDs            []string                 `json:"holder_ids"`
	OpenAmountCents      int64                    `json:"open_amount_cents"`
	ValidityMonths       int                      `json:"validity_months"`
	CardNamePrefix       string                   `json:"card_name_prefix"`
	MaxUsesPerCard       int                      `json:"max_uses_per_card"`
	LeaseMinutes         int                      `json:"lease_minutes"`
	CancelAfterDays      int                      `json:"cancel_after_days"`
	HealthWindowHours    int                      `json:"health_window_hours"`
	ProductFailThreshold int                      `json:"product_fail_threshold"`
	CardFailThreshold    int                      `json:"card_fail_threshold"`
	AutoSuspend          bool                     `json:"auto_suspend"`
	// 资金上限，必须大于 0。金额按币种最小单位比较（系统没有汇率）。
	MaxFundCents      int64 `json:"max_fund_cents"`       // 单笔注资上限
	MaxWalletOpCents  int64 `json:"max_wallet_op_cents"`  // 单笔钱包转账、兑换上限
	DailyFundCapCents int64 `json:"daily_fund_cap_cents"` // 单日注资累计上限（北京日）
	// ReplenishWhenUnselected 为真时，MarsWallex 没被选用也照常自动补卡（用于预热卡池）。默认暂停，免得白花开卡费。
	ReplenishWhenUnselected bool `json:"replenish_when_unselected"`
}

// DefaultPolicy 返回默认策略；map 与切片为空但非 nil。
func DefaultPolicy() Policy {
	return Policy{
		PlanProducts:         map[string][]string{},
		ProductTargets:       map[string]ProductTarget{},
		HolderIDs:            []string{},
		OpenAmountCents:      0,
		ValidityMonths:       24,
		CardNamePrefix:       "POOL",
		MaxUsesPerCard:       1,
		LeaseMinutes:         35, // 付款引擎第 5–9 步严格上限约 33 分钟（payengine），要留够
		CancelAfterDays:      7,
		HealthWindowHours:    24,
		ProductFailThreshold: 3,
		CardFailThreshold:    2,
		AutoSuspend:          true,
		MaxFundCents:         DefaultMaxFundCents,
		MaxWalletOpCents:     DefaultMaxWalletOpCents,
		DailyFundCapCents:    DefaultDailyFundCapCents,
	}
}

var cardNamePrefixRe = regexp.MustCompile(`^[A-Za-z0-9]{1,10}$`)

func inRange(name string, v, lo, hi int) error {
	if v < lo || v > hi {
		return badRequest(fmt.Sprintf("%s 必须在 %d 到 %d 之间", name, lo, hi))
	}
	return nil
}

// Validate 校验各项范围，不合法时返回写明哪一项的 badRequest。
func (p Policy) Validate() error {
	if p.OpenAmountCents < 0 {
		return badRequest("open_amount_cents 不能为负数")
	}
	limits := []struct {
		name string
		v    int64
	}{
		{"max_fund_cents", p.MaxFundCents},
		{"max_wallet_op_cents", p.MaxWalletOpCents},
		{"daily_fund_cap_cents", p.DailyFundCapCents},
	}
	for _, l := range limits {
		if l.v <= 0 {
			return badRequest(l.name + " 必须大于 0")
		}
	}
	if p.OpenAmountCents > p.MaxFundCents {
		return badRequest("open_amount_cents 不能超过 max_fund_cents")
	}
	if !cardNamePrefixRe.MatchString(p.CardNamePrefix) {
		return badRequest("card_name_prefix 只能是 1 到 10 位字母或数字")
	}
	checks := []struct {
		name   string
		v      int
		lo, hi int
	}{
		{"validity_months", p.ValidityMonths, 2, 47},
		{"max_uses_per_card", p.MaxUsesPerCard, 1, 100},
		{"lease_minutes", p.LeaseMinutes, 5, 240},
		{"cancel_after_days", p.CancelAfterDays, 0, 90},
		{"health_window_hours", p.HealthWindowHours, 1, 720},
		{"product_fail_threshold", p.ProductFailThreshold, 1, 100},
		{"card_fail_threshold", p.CardFailThreshold, 1, 100},
	}
	for _, c := range checks {
		if err := inRange(c.name, c.v, c.lo, c.hi); err != nil {
			return err
		}
	}
	for id, t := range p.ProductTargets {
		if err := inRange("product_targets["+id+"].target", t.Target, 0, 500); err != nil {
			return err
		}
		if err := inRange("product_targets["+id+"].daily_limit", t.DailyLimit, 0, 500); err != nil {
			return err
		}
	}
	return nil
}

// LoadPolicy 读取策略；未设置返回默认值，已设置则在默认值上 Unmarshal（缺的字段沿用默认）。
func LoadPolicy() (Policy, error) {
	p := DefaultPolicy()
	raw, err := db.GetSetting(PolicySettingKey)
	if err != nil {
		return p, err
	}
	if raw == "" {
		return p, nil
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return DefaultPolicy(), fmt.Errorf("解析卡池策略失败: %w", err)
	}
	// JSON 里的 null 会把 map/切片置为 nil，这里补回非 nil
	if p.PlanProducts == nil {
		p.PlanProducts = map[string][]string{}
	}
	if p.ProductTargets == nil {
		p.ProductTargets = map[string]ProductTarget{}
	}
	if p.HolderIDs == nil {
		p.HolderIDs = []string{}
	}
	p.normalizeLimits()
	return p, nil
}

// normalizeLimits 把库里不大于 0 的资金上限换成默认值并记日志。旧策略缺这些字段时 Unmarshal 已沿用默认值，
// 这里兜住手改过的存量数据：无论如何不能变成不限。
func (p *Policy) normalizeLimits() {
	limits := []struct {
		name string
		v    *int64
		def  int64
	}{
		{"max_fund_cents", &p.MaxFundCents, DefaultMaxFundCents},
		{"max_wallet_op_cents", &p.MaxWalletOpCents, DefaultMaxWalletOpCents},
		{"daily_fund_cap_cents", &p.DailyFundCapCents, DefaultDailyFundCapCents},
	}
	for _, l := range limits {
		if *l.v <= 0 {
			log.Printf("[cardpool] 策略里的 %s 为 %d，不合法，按默认值 %d 执行", l.name, *l.v, l.def)
			*l.v = l.def
		}
	}
}

// SavePolicy 先校验再保存；校验失败不写入。
func SavePolicy(p Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return db.SetSetting(PolicySettingKey, string(b))
}
