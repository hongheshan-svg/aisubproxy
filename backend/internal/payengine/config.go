package payengine

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const (
	settingPlanTable   = "pay_plan_table"
	settingConcurrency = "pay_concurrency"
	settingProxies     = "pay_region_proxies"

	maxConcurrency = 3
)

// PlanKeys 是支持的套餐键（顺序即展示顺序）。
var PlanKeys = []string{"go", "plus", "pro_5x", "pro_20x", "pro_50x"}

// Regions 是支持的地区（顺序即展示顺序）。
var Regions = []string{"PH", "US", "JP", "CL", "EG"}

var defaultCurrency = map[string]string{"PH": "PHP", "US": "USD", "JP": "JPY", "CL": "CLP", "EG": "EGP"}

type planDefault struct{ label, name, expect string }

var planDefaults = map[string]planDefault{
	"go":      {"Go", "", ""},
	"plus":    {"Plus", "chatgptplusplan", "plus"},
	"pro_5x":  {"Pro 5x", "chatgptprolite", ""},
	"pro_20x": {"Pro 20x", "chatgptpro", "pro"},
	"pro_50x": {"Pro 50x", "chatgptpromax", "promax"},
}

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

// RegionConf 是某套餐在某地区的付款参数；金额均为最小单位。
type RegionConf struct {
	Enabled   bool   `json:"enabled"`
	Currency  string `json:"currency"`
	CapMinor  int64  `json:"cap_minor"`
	FundCents int64  `json:"fund_cents"`
}

// PlanConf 是一个套餐及其各地区配置。
type PlanConf struct {
	Key             string                `json:"key"`
	Label           string                `json:"label"`
	ChatGPTPlanName string                `json:"chatgpt_plan_name"`
	ExpectPlanType  string                `json:"expect_plan_type"`
	Regions         map[string]RegionConf `json:"regions"`
}

func defaultPlan(key string) PlanConf {
	d := planDefaults[key]
	p := PlanConf{Key: key, Label: d.label, ChatGPTPlanName: d.name, ExpectPlanType: d.expect, Regions: map[string]RegionConf{}}
	for _, r := range Regions {
		p.Regions[r] = RegionConf{Currency: defaultCurrency[r]}
	}
	return p
}

// DefaultPlanTable 返回全部未启用、金额为 0 的默认套餐表。
func DefaultPlanTable() []PlanConf {
	out := make([]PlanConf, 0, len(PlanKeys))
	for _, k := range PlanKeys {
		out = append(out, defaultPlan(k))
	}
	return out
}

// LoadPlanTable 读取套餐表，并与默认表合并：补齐缺项、丢弃未知键。
func LoadPlanTable() ([]PlanConf, error) {
	raw, err := db.GetSetting(settingPlanTable)
	if err != nil {
		return nil, err
	}
	tbl := DefaultPlanTable()
	if raw == "" {
		return tbl, nil
	}
	var saved []PlanConf
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		return nil, fmt.Errorf("套餐配置已损坏: %w", err)
	}
	for _, s := range saved {
		for i := range tbl {
			if tbl[i].Key != s.Key {
				continue
			}
			tbl[i].Label, tbl[i].ChatGPTPlanName, tbl[i].ExpectPlanType = s.Label, s.ChatGPTPlanName, s.ExpectPlanType
			for _, r := range Regions {
				if rc, ok := s.Regions[r]; ok {
					tbl[i].Regions[r] = rc
				}
			}
		}
	}
	return tbl, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// SavePlanTable 校验后保存套餐表。
func SavePlanTable(p []PlanConf) error {
	seen := map[string]bool{}
	for _, pc := range p {
		if !contains(PlanKeys, pc.Key) {
			return fmt.Errorf("%w: 未知套餐 %q", ErrBadRequest, pc.Key)
		}
		if seen[pc.Key] {
			return fmt.Errorf("%w: 套餐 %s 重复", ErrBadRequest, pc.Key)
		}
		seen[pc.Key] = true
		for r, rc := range pc.Regions {
			if !contains(Regions, r) {
				return fmt.Errorf("%w: 未知地区 %q", ErrBadRequest, r)
			}
			if rc.CapMinor < 0 || rc.FundCents < 0 {
				return fmt.Errorf("%w: %s/%s 金额不能为负", ErrBadRequest, pc.Key, r)
			}
			if !currencyRe.MatchString(rc.Currency) {
				return fmt.Errorf("%w: %s/%s 币种须为 3 位大写字母", ErrBadRequest, pc.Key, r)
			}
		}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return db.SetSetting(settingPlanTable, string(b))
}

type planUnavailableError struct{ msg string }

func (e *planUnavailableError) Error() string        { return e.msg }
func (e *planUnavailableError) Is(target error) bool { return target == ErrPlanUnavailable }

func unavailable(format string, a ...any) error {
	return &planUnavailableError{msg: fmt.Sprintf(format, a...)}
}

// ResolvePlan 取出可用的套餐与地区配置。
// 真实付款要求：地区已启用、币种、金额上限与注资金额大于 0、ChatGPT 套餐名与期望套餐类型齐全；
// 试运行只要求 ChatGPT 套餐名。
func ResolvePlan(planKey, region string, dryRun bool) (PlanConf, RegionConf, error) {
	tbl, err := LoadPlanTable()
	if err != nil {
		return PlanConf{}, RegionConf{}, err
	}
	var plan *PlanConf
	for i := range tbl {
		if tbl[i].Key == planKey {
			plan = &tbl[i]
		}
	}
	if plan == nil {
		return PlanConf{}, RegionConf{}, unavailable("未知套餐 %q", planKey)
	}
	rc, ok := plan.Regions[region]
	if !ok {
		return PlanConf{}, RegionConf{}, unavailable("未知地区 %q", region)
	}
	if plan.ChatGPTPlanName == "" {
		return PlanConf{}, RegionConf{}, unavailable("套餐 %s 未配置 ChatGPT 套餐名", plan.Label)
	}
	if dryRun {
		return *plan, rc, nil
	}
	if !rc.Enabled {
		return PlanConf{}, RegionConf{}, unavailable("套餐 %s 在地区 %s 未启用", plan.Label, region)
	}
	if plan.ExpectPlanType == "" {
		return PlanConf{}, RegionConf{}, unavailable("套餐 %s 未配置期望套餐类型", plan.Label)
	}
	if rc.Currency == "" {
		return PlanConf{}, RegionConf{}, unavailable("套餐 %s 在地区 %s 未配置币种", plan.Label, region)
	}
	if rc.CapMinor <= 0 {
		return PlanConf{}, RegionConf{}, unavailable("套餐 %s 在地区 %s 未配置金额上限", plan.Label, region)
	}
	if rc.FundCents <= 0 {
		return PlanConf{}, RegionConf{}, unavailable("套餐 %s 在地区 %s 未配置注资金额", plan.Label, region)
	}
	return *plan, rc, nil
}

// LoadConcurrency 返回并发数（1~3）；未设置或值非法时为 1。
func LoadConcurrency() int {
	raw, err := db.GetSetting(settingConcurrency)
	if err != nil {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 || n > maxConcurrency {
		return 1
	}
	return n
}

// SaveConcurrency 保存并发数，范围 1~3。
func SaveConcurrency(n int) error {
	if n < 1 || n > maxConcurrency {
		return fmt.Errorf("%w: 并发数须在 1 到 %d 之间", ErrBadRequest, maxConcurrency)
	}
	return db.SetSetting(settingConcurrency, strconv.Itoa(n))
}

// LoadProxies 解密读取地区代理（地区 → 地址）；未配置返回空 map。
func LoadProxies() (map[string]string, error) {
	raw, err := db.GetSecretSetting(settingProxies)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if raw == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("代理配置已损坏")
	}
	return out, nil
}

// ProxyStatus 返回各地区是否已配置代理，不暴露地址。
func ProxyStatus() (map[string]bool, error) {
	px, err := LoadProxies()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(Regions))
	for _, r := range Regions {
		out[r] = px[r] != ""
	}
	return out, nil
}

func validProxy(addr string) bool {
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "http", "https", "socks5":
		return true
	}
	return false
}

// SaveProxies 合并保存地区代理：空串保持不变，"-" 清除，其余须为 http/https/socks5 地址（与 chatgpt.New 一致）。
// 错误消息不含代理地址。
func SaveProxies(in map[string]string) error {
	for r, v := range in {
		if !contains(Regions, r) {
			return fmt.Errorf("%w: 未知地区 %q", ErrBadRequest, r)
		}
		if v != "" && v != "-" && !validProxy(v) {
			return fmt.Errorf("%w: 地区 %s 的代理地址格式无效，须为 http、https 或 socks5 地址", ErrBadRequest, r)
		}
	}
	cur, err := LoadProxies()
	if err != nil {
		return err
	}
	for r, v := range in {
		switch v {
		case "":
		case "-":
			delete(cur, r)
		default:
			cur[r] = v
		}
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	return db.SetSecretSetting(settingProxies, string(b))
}
