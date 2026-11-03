package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 设置项。
const (
	// SettingDefault 是全局默认发码服务商。和 SettingByProduct 都没保存过时用出厂配置（见 factoryConfig）。
	SettingDefault = "cdk_default_provider"
	// SettingByProduct 是按产品选的服务商，JSON 对象 {产品: 服务商名}。缺失或值为空表示跟随全局默认。
	SettingByProduct = "cdk_provider_by_product"
)

var (
	// ErrUnsupported 选中的服务商不支持这个档位。发码接口返回 400，绝不悄悄换成别家。
	ErrUnsupported = errors.New("服务商不支持该档位")
	// ErrInvalidConfig 保存的服务商配置不合法。
	ErrInvalidConfig = errors.New("服务商配置不合法")
)

// UnsupportedError 带上是哪家、哪个产品档位，Error() 是给操作者看的话。
type UnsupportedError struct {
	Provider      Provider
	Product, Plan string
	// Explicit 为 true 表示操作者在请求里显式选了这家；否则是按配置选出来的，提示里告诉他去哪改。
	Explicit bool
}

func (e *UnsupportedError) Error() string {
	msg := fmt.Sprintf("%s 不支持 %s %s", e.Provider.DisplayName(), ProductLabel(e.Product), e.Plan)
	if !e.Explicit {
		msg += "，请在发码时手动选择服务商，或到「卡服务商」页调整"
	}
	return msg
}

func (e *UnsupportedError) Is(target error) bool { return target == ErrUnsupported }

// Config 是读出来的服务商配置。ByProduct 只含有效的条目；被忽略的条目和原因在 Warnings 里。
type Config struct {
	Default   Name
	ByProduct map[string]Name
	Warnings  []string
}

// For 返回这个产品线实际用哪家：有产品配置用它，没有用全局默认。
func (c Config) For(product string) Name {
	if n, ok := c.ByProduct[normProduct(product)]; ok {
		return n
	}
	return c.Default
}

// Effective 返回每条产品线实际用哪家。
func (c Config) Effective() map[string]Name {
	out := make(map[string]Name, len(products))
	for _, p := range products {
		out[p.Key] = c.For(p.Key)
	}
	return out
}

// LoadConfig 读服务商配置。读库出错返回错误，调用方不能当成任何一家继续发码。
// 两个设置项都没保存过时返回出厂配置（factoryConfig）。
// 库里存的服务商名已经没有注册时：全局默认按兜底上游处理，产品配置按跟随全局默认处理，两种都写进 Warnings。
func LoadConfig() (Config, error) {
	rawDef, err := db.GetSetting(SettingDefault)
	if err != nil {
		return Config{}, err
	}
	rawBy, err := db.GetSetting(SettingByProduct)
	if err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(rawDef) == "" && strings.TrimSpace(rawBy) == "" {
		return factoryConfig(), nil
	}
	cfg := Config{ByProduct: map[string]Name{}, Warnings: []string{}}
	fb := fallback()
	fbName := Name("")
	if fb != nil {
		fbName = fb.Name()
	}
	cfg.Default = fbName
	if s := strings.TrimSpace(rawDef); s != "" {
		if p, ok := Get(Name(s)); ok {
			cfg.Default = p.Name()
		} else {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("全局默认服务商 %q 已不存在，按 %s 处理，请重新选择", s, DisplayName(fbName)))
		}
	}
	if strings.TrimSpace(rawBy) == "" {
		return cfg, nil
	}
	var by map[string]string
	if err := json.Unmarshal([]byte(rawBy), &by); err != nil {
		cfg.Warnings = append(cfg.Warnings, "按产品选择的配置无法解析，全部按全局默认处理，请重新保存")
		return cfg, nil
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, prod := range keys {
		name := strings.TrimSpace(by[prod])
		if name == "" {
			continue
		}
		if !IsProduct(prod) {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("按产品选择里有未知产品 %q，已忽略", prod))
			continue
		}
		p, ok := Get(Name(name))
		if !ok {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s 选的服务商 %q 已不存在，按全局默认处理，请重新选择", ProductLabel(prod), name))
			continue
		}
		if !providesProduct(p, prod) {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s 选的 %s 不做这条产品线，按全局默认处理，请重新选择", ProductLabel(prod), p.DisplayName()))
			continue
		}
		cfg.ByProduct[prod] = p.Name()
	}
	return cfg, nil
}

// factoryConfig 是从没保存过服务商配置时的出厂配置：全局默认 MarsWallex；它不做的产品线（X、Grok、Claude）单独选兜底上游。
// 这几条写在 ByProduct 里，「卡服务商」页上看得见；保存过一次之后完全按保存的来。
// 全局默认不做某条产品线时发码照样拒绝（不自动换家），所以出厂配置要把这几条显式配上。
// MarsWallex 没注册时退回「全部用兜底上游」。
func factoryConfig() Config {
	cfg := Config{ByProduct: map[string]Name{}, Warnings: []string{}}
	fb := fallback()
	mw, ok := Get(MarsWallex)
	if !ok {
		if fb != nil {
			cfg.Default = fb.Name()
		}
		return cfg
	}
	cfg.Default = mw.Name()
	if fb == nil {
		return cfg
	}
	for _, p := range products {
		if !providesProduct(mw, p.Key) && providesProduct(fb, p.Key) {
			cfg.ByProduct[p.Key] = fb.Name()
		}
	}
	return cfg
}

func providesProduct(p Provider, product string) bool {
	for _, k := range p.Products() {
		if k == product {
			return true
		}
	}
	return false
}

// LoadDefault 读全局默认服务商，规则见 LoadConfig。
func LoadDefault() (Name, error) {
	cfg, err := LoadConfig()
	return cfg.Default, err
}

// SaveDefault 只保存全局默认服务商（旧接口用），产品配置不动。
func SaveDefault(n Name) error {
	p, ok := Get(n)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknown, n)
	}
	return db.SetSetting(SettingDefault, string(p.Name()))
}

// ValidateConfig 校验并规范化要保存的配置：默认服务商必须已注册；产品必须已知，服务商必须已注册且做这条产品线。
// 产品的值为空串表示跟随全局默认，不写进结果。错误都包着 ErrInvalidConfig，文字可以直接给操作者看。
func ValidateConfig(def string, by map[string]string) (Name, map[string]Name, error) {
	if strings.TrimSpace(def) == "" {
		return "", nil, fmt.Errorf("%w：请选择全局默认服务商", ErrInvalidConfig)
	}
	p, ok := Get(Name(def))
	if !ok {
		return "", nil, fmt.Errorf("%w：全局默认服务商 %q 未注册", ErrInvalidConfig, def)
	}
	out := map[string]Name{}
	for prod, name := range by {
		key := normProduct(prod)
		if !IsProduct(key) {
			return "", nil, fmt.Errorf("%w：未知产品 %q", ErrInvalidConfig, prod)
		}
		if strings.TrimSpace(name) == "" {
			continue
		}
		q, ok := Get(Name(name))
		if !ok {
			return "", nil, fmt.Errorf("%w：%s 选的服务商 %q 未注册", ErrInvalidConfig, ProductLabel(key), name)
		}
		if !providesProduct(q, key) {
			return "", nil, fmt.Errorf("%w：%s 不做 %s", ErrInvalidConfig, q.DisplayName(), ProductLabel(key))
		}
		out[key] = q.Name()
	}
	return p.Name(), out, nil
}

// SaveConfig 在一个事务里保存全局默认和产品配置。调用前先用 ValidateConfig 校验。
func SaveConfig(def Name, by map[string]Name) error {
	if _, _, err := ValidateConfig(string(def), toStrings(by)); err != nil {
		return err
	}
	raw, err := json.Marshal(toStrings(by))
	if err != nil {
		return err
	}
	return db.SetSettings(map[string]string{SettingDefault: string(def), SettingByProduct: string(raw)})
}

func toStrings(by map[string]Name) map[string]string {
	out := make(map[string]string, len(by))
	for k, v := range by {
		out[k] = string(v)
	}
	return out
}

// ResolveIssue 决定发码请求没带服务商时用哪家：先取该产品的配置，没有就取全局默认。
// 选出的服务商不支持这个档位时返回 *UnsupportedError（errors.Is ErrUnsupported），绝不换成别家；读库出错返回错误。
func ResolveIssue(product, plan string) (Provider, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	p, ok := Get(cfg.For(product))
	if !ok {
		return nil, fmt.Errorf("%w：没有可用的默认服务商", ErrUnknown)
	}
	if !p.Supports(product, plan) {
		return nil, &UnsupportedError{Provider: p, Product: normProduct(product), Plan: strings.TrimSpace(plan)}
	}
	return p, nil
}

// CheckSupports 是发码请求显式带了服务商时的校验，不支持返回 *UnsupportedError。
func CheckSupports(p Provider, product, plan string) error {
	if p.Supports(product, plan) {
		return nil
	}
	return &UnsupportedError{Provider: p, Product: normProduct(product), Plan: strings.TrimSpace(plan), Explicit: true}
}

// Selected 判断某家现在是否被选用：全局默认是它、任一产品配置是它，或它还有没结清的本地码（随时会用到卡）。
// 自动补卡用它决定要不要暂停。
func Selected(ctx context.Context, n Name) (bool, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return false, err
	}
	if cfg.Default == n {
		return true, nil
	}
	for _, v := range cfg.ByProduct {
		if v == n {
			return true, nil
		}
	}
	return db.HasOpenLocalCDKs(ctx, string(n))
}

// MarsWallexSelected 是 Selected 绑定到 MarsWallex，供 MarsWallex 卡池的自动补卡闸使用。
func MarsWallexSelected(ctx context.Context) (bool, error) { return Selected(ctx, MarsWallex) }
