package payengine

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const (
	settingPlanTable   = "pay_plan_table"
	settingConcurrency = "pay_concurrency"
	settingProxies     = "pay_region_proxies"

	MaxConcurrency = 3
)

// SubscriptionPlanKeys 是订阅开通的套餐键（顺序即展示顺序）。
var SubscriptionPlanKeys = []string{"go", "plus", "pro_5x", "pro_20x", "pro_50x"}

// 单据种类，与 pay_orders.kind 列的取值一致。
const (
	KindSubscription = db.PayKindSubscription // 订阅开通
	KindCredit       = db.PayKindCredit       // Codex 点数
)

// CreditPlanKeys 是 Codex 点数各档的键，数量取自键名。
var CreditPlanKeys = []string{"credit250", "credit500", "credit1000", "credit2500", "credit5000", "credit25000"}

// PlanKeys 是套餐表里的全部键：订阅开通在前、点数各档在后（顺序即展示顺序）。
var PlanKeys = append(append([]string{}, SubscriptionPlanKeys...), CreditPlanKeys...)

// SupportedPlanKeys 返回付款引擎实际实现的全部套餐键（订阅开通五档、点数六档）的副本，供服务商能力清单使用。
func SupportedPlanKeys() []string { return append([]string(nil), PlanKeys...) }

// PlanKind 返回套餐键对应的单据种类：点数档为 credit，其余为 subscription。发码门槛按它分别计算。
func PlanKind(planKey string) string {
	if contains(CreditPlanKeys, strings.TrimSpace(planKey)) {
		return KindCredit
	}
	return KindSubscription
}

// CreditQuantity 返回点数档的点数（取自键名，如 credit250 → 250）；不是点数档返回 0。
func CreditQuantity(planKey string) int {
	key := strings.TrimSpace(planKey)
	if !contains(CreditPlanKeys, key) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(key, "credit"))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

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
// ExpectMinor 是预期应付金额，只用于展示和校验配置（上限不能低于它），付款时以支付页实际读到的金额为准，上限由 CapMinor 把关，下限由 FloorMinor 把关。
type RegionConf struct {
	Enabled     bool   `json:"enabled"`
	Currency    string `json:"currency"`
	CapMinor    int64  `json:"cap_minor"`
	FundCents   int64  `json:"fund_cents"`
	ExpectMinor int64  `json:"expect_minor"`
	// FloorMinor 是最低金额：支付页金额低于它就在注资前放弃，单位与币种同 CapMinor。
	FloorMinor int64 `json:"floor_minor"`
	// 结账页金额正好是 0 时（首月免费等优惠）是否照常开通；nil 表示没保存过，按套餐默认（只有 Plus 允许）。点数套餐一律不允许。
	AllowZero *bool `json:"allow_zero,omitempty"`
}

// ZeroAllowed 返回该地区是否允许 0 元开通；AllowZero 为 nil 时按不允许。
func (rc RegionConf) ZeroAllowed() bool { return rc.AllowZero != nil && *rc.AllowZero }

// zeroAllowedByDefault 返回「允许 0 元开通」的默认值：只有 Plus 允许。
func zeroAllowedByDefault(planKey string) bool { return planKey == "plus" }

// PlanConf 是一个套餐及其各地区配置。Kind 与 CreditQuantity 由套餐键决定，保存时客户端传什么都以键为准。
// 点数档没有 ChatGPT 套餐名和期望套餐类型：建结账会话用 credit_purchase_data，确认阶段不看 plan_type。
type PlanConf struct {
	Key             string                `json:"key"`
	Label           string                `json:"label"`
	Kind            string                `json:"kind"`
	CreditQuantity  int                   `json:"credit_quantity"`
	ChatGPTPlanName string                `json:"chatgpt_plan_name"`
	ExpectPlanType  string                `json:"expect_plan_type"`
	Regions         map[string]RegionConf `json:"regions"`
}

func defaultPlan(key string) PlanConf {
	if q := CreditQuantity(key); q > 0 {
		p := PlanConf{Key: key, Label: fmt.Sprintf("Codex 点数 %d", q), Kind: KindCredit, CreditQuantity: q, Regions: map[string]RegionConf{}}
		fillDefaultRegions(&p)
		return p
	}
	d := planDefaults[key]
	p := PlanConf{Key: key, Label: d.label, Kind: KindSubscription, ChatGPTPlanName: d.name, ExpectPlanType: d.expect, Regions: map[string]RegionConf{}}
	fillDefaultRegions(&p)
	return p
}

// fillDefaultRegions 填各地区的出厂值：US、PH 用内置价格区间，其他地区只有币种。全部未启用、注资为 0。
// 「允许 0 元开通」每个地区都写明确的默认值（见 zeroAllowedByDefault）。
func fillDefaultRegions(p *PlanConf) {
	for _, r := range Regions {
		rc, ok := BuiltinRegionPrice(p.Key, r)
		if !ok {
			rc = RegionConf{Currency: defaultCurrency[r]}
		}
		allow := zeroAllowedByDefault(p.Key)
		rc.AllowZero = &allow
		p.Regions[r] = rc
	}
}

// normalizePlan 把由套餐键决定的字段改回键的取值：种类、点数；点数档不留 ChatGPT 套餐名和期望套餐类型，
// 各地区一律不允许 0 元开通。
func normalizePlan(p *PlanConf) {
	p.Kind, p.CreditQuantity = PlanKind(p.Key), CreditQuantity(p.Key)
	if p.Kind == KindCredit {
		p.ChatGPTPlanName, p.ExpectPlanType = "", ""
		for r, rc := range p.Regions {
			no := false
			rc.AllowZero = &no
			p.Regions[r] = rc
		}
	}
}

// DefaultPlanTable 返回全部未启用的默认套餐表：美国、菲律宾带内置价格区间，其他地区金额为 0。
func DefaultPlanTable() []PlanConf {
	out := make([]PlanConf, 0, len(PlanKeys))
	for _, k := range PlanKeys {
		out = append(out, defaultPlan(k))
	}
	return out
}

// LoadPlanTable 读取套餐表，并与默认表合并：补齐缺项、丢弃未知键。
// 美国、菲律宾已存的预期、最低、上限为 0 时用内置价补上（规则见 fillBuiltin），补的值保存前只在内存里。
// 已存的地区没有「允许 0 元开通」时按默认值补上，返回的每个地区 AllowZero 都不是 nil。
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
					if rc.AllowZero == nil {
						allow := zeroAllowedByDefault(s.Key)
						rc.AllowZero = &allow
					}
					tbl[i].Regions[r] = fillBuiltin(s.Key, r, rc)
				}
			}
			normalizePlan(&tbl[i])
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

// SavePlanTable 校验后保存套餐表。种类、点数由套餐键决定，客户端传来的值不采用。
func SavePlanTable(p []PlanConf) error {
	// 注资金额不能超过单笔上限，否则这个套餐的单子每张都会在注资时被拒
	pol, err := cardpool.LoadPolicy()
	if err != nil {
		return fmt.Errorf("读取卡池策略失败，暂不能保存套餐: %w", err)
	}
	seen := map[string]bool{}
	for _, pc := range p {
		if !contains(PlanKeys, pc.Key) {
			return fmt.Errorf("%w: 未知套餐", ErrBadRequest)
		}
		if seen[pc.Key] {
			return fmt.Errorf("%w: 套餐重复", ErrBadRequest)
		}
		seen[pc.Key] = true
		for r, rc := range pc.Regions {
			if !contains(Regions, r) {
				return fmt.Errorf("%w: 未知地区", ErrBadRequest)
			}
			if rc.CapMinor < 0 || rc.FundCents < 0 || rc.ExpectMinor < 0 || rc.FloorMinor < 0 {
				return fmt.Errorf("%w: %s/%s 金额不能为负", ErrBadRequest, pc.Key, r)
			}
			if rc.ExpectMinor > 0 && rc.CapMinor > 0 && rc.CapMinor < rc.ExpectMinor {
				return fmt.Errorf("%w: %s/%s 金额上限不能低于预期金额", ErrBadRequest, pc.Key, r)
			}
			if rc.FloorMinor > 0 && rc.CapMinor > 0 && rc.FloorMinor > rc.CapMinor {
				return fmt.Errorf("%w: %s/%s 最低金额不能高于金额上限", ErrBadRequest, pc.Key, r)
			}
			if rc.FloorMinor > 0 && rc.ExpectMinor > 0 && rc.FloorMinor > rc.ExpectMinor {
				return fmt.Errorf("%w: %s/%s 最低金额不能高于预期金额", ErrBadRequest, pc.Key, r)
			}
			if rc.FundCents > pol.MaxFundCents {
				return fmt.Errorf("%w: %s/%s 注资金额 %d 分超过单笔上限 %d 分", ErrBadRequest, pc.Key, r, rc.FundCents, pol.MaxFundCents)
			}
			if !currencyRe.MatchString(rc.Currency) {
				return fmt.Errorf("%w: %s/%s 币种须为 3 位大写字母", ErrBadRequest, pc.Key, r)
			}
			// 美元地区的注资金额与预期金额都是美分，注资不足预期就一定会被支付页拒绝（D-5）。
			// 预期留空时读取会补内置价（fillBuiltin），按实际生效的预期金额比较（审计 money N-3）
			if expect := fillBuiltin(pc.Key, r, rc).ExpectMinor; rc.Currency == "USD" && rc.FundCents > 0 && expect > 0 && rc.FundCents < expect {
				return fmt.Errorf("%w: %s/%s 美元注资金额 %d 分不能低于预期金额 %d 分", ErrBadRequest, pc.Key, r, rc.FundCents, expect)
			}
		}
	}
	// 传来的地区没有「允许 0 元开通」（如还开着旧后台页面）时沿用当前生效的值，读不出当前表再按默认值，
	// 免得手动取消的勾被悄悄改回默认。各地区写进新 map，不改调用方传来的表。
	cur := map[string]map[string]RegionConf{}
	if tbl, err := LoadPlanTable(); err == nil {
		for _, pc := range tbl {
			cur[pc.Key] = pc.Regions
		}
	}
	out := make([]PlanConf, len(p))
	for i, pc := range p {
		regions := make(map[string]RegionConf, len(pc.Regions))
		for r, rc := range pc.Regions {
			if rc.AllowZero == nil {
				if c, ok := cur[pc.Key][r]; ok && c.AllowZero != nil {
					rc.AllowZero = c.AllowZero
				} else {
					allow := zeroAllowedByDefault(pc.Key)
					rc.AllowZero = &allow
				}
			}
			regions[r] = rc
		}
		pc.Regions = regions
		normalizePlan(&pc)
		out[i] = pc
	}
	b, err := json.Marshal(out)
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
// 真实付款要求：地区已启用、币种、最低金额、金额上限与注资金额大于 0；订阅开通还要 ChatGPT 套餐名与期望套餐类型齐全。
// 试运行：订阅开通只要求 ChatGPT 套餐名；点数档没有额外要求（读金额、填表都不点支付，金额上限不用于试运行）。
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
		return PlanConf{}, RegionConf{}, unavailable("未知套餐")
	}
	rc, ok := plan.Regions[region]
	if !ok {
		return PlanConf{}, RegionConf{}, unavailable("未知地区")
	}
	credit := plan.Kind == KindCredit
	if !credit && plan.ChatGPTPlanName == "" {
		return PlanConf{}, RegionConf{}, unavailable("套餐 %s 未配置 ChatGPT 套餐名", plan.Label)
	}
	if dryRun {
		return *plan, rc, nil
	}
	if !rc.Enabled {
		return PlanConf{}, RegionConf{}, unavailable("套餐 %s 在地区 %s 未启用", plan.Label, region)
	}
	if !credit && plan.ExpectPlanType == "" {
		return PlanConf{}, RegionConf{}, unavailable("套餐 %s 未配置期望套餐类型", plan.Label)
	}
	if rc.Currency == "" {
		return PlanConf{}, RegionConf{}, unavailable("套餐 %s 在地区 %s 未配置币种", plan.Label, region)
	}
	if rc.FloorMinor <= 0 {
		return PlanConf{}, RegionConf{}, unavailable("套餐 %s 在地区 %s 未配置最低金额", plan.Label, region)
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
	if err != nil || n < 1 || n > MaxConcurrency {
		return 1
	}
	return n
}

// ValidateConcurrency 检查并发数是否在 1~3；保存前可先调用，避免整体保存时一半成功一半失败。
func ValidateConcurrency(n int) error {
	if n < 1 || n > MaxConcurrency {
		return fmt.Errorf("%w: 并发数须在 1 到 %d 之间", ErrBadRequest, MaxConcurrency)
	}
	return nil
}

// SaveConcurrency 保存并发数，范围 1~3。
func SaveConcurrency(n int) error {
	if err := ValidateConcurrency(n); err != nil {
		return err
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

// validProxy 检查代理地址：http、https、socks5、socks5h，socks5 要写端口。
// 带账号密码的 socks5 也可以：付款浏览器经本机中转认证（见 paybrowser/socks_relay.go）。
func validProxy(addr string) bool {
	u, err := url.Parse(addr)
	if err != nil || u.Hostname() == "" {
		return false
	}
	switch u.Scheme {
	case "http", "https":
		return true
	case "socks5", "socks5h":
		return u.Port() != ""
	}
	return false
}

// saveProxiesMu 保护 SaveProxies 的读改写：两名管理员同时保存不同地区时，后写的不能覆盖先写的。
var saveProxiesMu sync.Mutex

// SaveProxies 合并保存地区代理：空串保持不变，"-" 清除，其余须为 http/https/socks5/socks5h 地址（与 chatgpt.New 一致）。
// 错误消息不含代理地址，也不回显输入的键（键可能被误填成带密码的地址）。
func SaveProxies(in map[string]string) error {
	for r, v := range in {
		if !contains(Regions, r) {
			return fmt.Errorf("%w: 未知地区", ErrBadRequest)
		}
		if v != "" && v != "-" && !validProxy(v) {
			return fmt.Errorf("%w: 地区 %s 的代理地址格式无效，须为 http、https、socks5 或 socks5h 地址，socks5 要写端口", ErrBadRequest, r)
		}
	}
	saveProxiesMu.Lock()
	defer saveProxiesMu.Unlock()
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
