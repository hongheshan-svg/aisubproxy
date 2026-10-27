package cardpool

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// PolicySettingKey 是策略在 site_settings 中的键。
const PolicySettingKey = "mw_cardpool_policy"

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
		LeaseMinutes:         30,
		CancelAfterDays:      7,
		HealthWindowHours:    24,
		ProductFailThreshold: 3,
		CardFailThreshold:    2,
		AutoSuspend:          true,
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
	return p, nil
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
