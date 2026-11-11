// Package provider 是卡服务商注册表：有哪些服务商、各是什么类型、能发哪些产品和档位、一张码或令牌归哪家。
//
// 服务商在发码时定死、写在码上，后台按产品线选用哪家（没配的产品线用全局默认）。改配置只影响之后新发的码；
// 一张码只归一家，绝不自动换另一家重试，认不出的码和令牌也绝不退回到别家。
//
// 接入一家新服务商：实现 Provider（再按类型实现 CardSupply 或 Upstream），在 init 里 Register。
// 注册表只管身份和能力；兑换、发码的具体实现在 handler 包的适配器里。
//
// 目前没有上游型服务商（注册表里只有供卡型的 MarsWallex）：Upstream、AsUpstream、forUpstream 等是为以后接外部发码上游留的扩展点。
package provider

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Name 是服务商名：小写字母、数字和下划线。
type Name string

// 内置服务商名。
const (
	MarsWallex Name = "marswallex"
)

// Kind 是服务商类型。
type Kind string

const (
	// KindUpstream 上游发码：对方发码、对方开通，本站只转发兑换请求。
	KindUpstream Kind = "upstream"
	// KindCardSupply 供卡·本站付款：本站自己发码，兑换时用这家的虚拟卡、由本站浏览器付款。
	KindCardSupply Kind = "card_supply"
)

// Text 是给界面看的类型名。
func (k Kind) Text() string {
	switch k {
	case KindUpstream:
		return "上游发码"
	case KindCardSupply:
		return "供卡·本站付款"
	}
	return string(k)
}

// Provider 是所有服务商都要实现的接口。
type Provider interface {
	Name() Name
	DisplayName() string
	Kind() Kind
	// Products 是这家能做的产品线，取值见 Products()。
	Products() []string
	// Supports 判断能否发、兑某产品的某档位。product 不区分大小写。
	Supports(product, plan string) bool
	// PlanNote 是这个档位需要提示给操作者和用户的话，没有返回空串。
	PlanNote(plan string) string
}

// CardSupply 是供卡型服务商另外要实现的接口。码和令牌只靠前缀认领，所以前缀不能为空、不能和别家重叠。
type CardSupply interface {
	Provider
	// CodePrefix 是码前缀，大写，如 MW-。
	CodePrefix() string
	// TokenPrefix 是兑换令牌前缀，小写，如 mwt_。
	TokenPrefix() string
}

// Upstream 是上游型服务商另外要实现的接口。
type Upstream interface {
	Provider
	// ClaimsCode 判断这张码是不是这家的。code 已经规范化（去空白、零宽字符，转大写）。
	ClaimsCode(code string) bool
	// ClaimsToken 判断这个兑换令牌是不是这家的。token 已去掉两端空白。
	ClaimsToken(token string) bool
}

// CodeLister 是可选接口：后台用哪种码列表。不实现时供卡型为 local，上游型为空串（这家的码在它自己的后台管理）。
type CodeLister interface {
	CodeList() string
}

// CodeList 返回后台用哪种码列表：local 本地码列表、空串表示没有列表（这家的码在它自己的后台管理）。
func CodeList(p Provider) string {
	if l, ok := p.(CodeLister); ok {
		return l.CodeList()
	}
	if p.Kind() == KindCardSupply {
		return "local"
	}
	return ""
}

// Product 是一条产品线。
type Product struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

var products = []Product{
	{Key: "gpt", Label: "ChatGPT"},
	{Key: "x", Label: "X Premium"},
	{Key: "grok", Label: "Grok"},
	{Key: "claude", Label: "Claude"},
}

// Products 返回全部产品线，顺序即展示顺序。
func Products() []Product { return append([]Product(nil), products...) }

// IsProduct 判断是不是已知产品线。
func IsProduct(key string) bool {
	for _, p := range products {
		if p.Key == key {
			return true
		}
	}
	return false
}

// ProductLabel 返回产品线的显示名，未知的原样返回。
func ProductLabel(key string) string {
	for _, p := range products {
		if p.Key == key {
			return p.Label
		}
	}
	return key
}

func normProduct(product string) string { return strings.ToLower(strings.TrimSpace(product)) }

// ---- 注册表 ----

var (
	ErrUnknown      = errors.New("未知的卡服务商")
	ErrUnrecognized = errors.New("无法识别的卡密")
)

var namePattern = regexp.MustCompile(`^[a-z0-9_]+$`)

var (
	regMu sync.RWMutex
	reg   []Provider
)

// Register 注册一家服务商。名称不合法、重名、前缀为空或重叠，都是编程错误，直接 panic 让启动失败。
func Register(p Provider) {
	regMu.Lock()
	defer regMu.Unlock()
	if err := validate(reg, p); err != nil {
		panic("provider.Register: " + err.Error())
	}
	reg = append(reg, p)
}

func validate(existing []Provider, p Provider) error {
	if p == nil {
		return errors.New("服务商为 nil")
	}
	n := p.Name()
	if !namePattern.MatchString(string(n)) {
		return fmt.Errorf("服务商名 %q 只能是小写字母、数字和下划线", n)
	}
	for _, e := range existing {
		if e.Name() == n {
			return fmt.Errorf("服务商名 %q 重复", n)
		}
	}
	if strings.TrimSpace(p.DisplayName()) == "" {
		return fmt.Errorf("%s 没有显示名", n)
	}
	for _, prod := range p.Products() {
		if !IsProduct(prod) {
			return fmt.Errorf("%s 的产品线 %q 未知", n, prod)
		}
	}
	switch p.Kind() {
	case KindCardSupply:
		cs, ok := p.(CardSupply)
		if !ok {
			return fmt.Errorf("%s 是供卡型，但没有实现 CardSupply", n)
		}
		return validatePrefixes(existing, cs)
	case KindUpstream:
		if _, ok := p.(Upstream); !ok {
			return fmt.Errorf("%s 是上游型，但没有实现 Upstream", n)
		}
		return nil
	}
	return fmt.Errorf("%s 的类型 %q 未知", n, p.Kind())
}

// validatePrefixes 码前缀和令牌前缀都不能为空、不能重复、不能互为前缀（含和自己的另一个前缀比较，忽略大小写）。
func validatePrefixes(existing []Provider, cs CardSupply) error {
	code, tok := cs.CodePrefix(), cs.TokenPrefix()
	switch {
	case code == "" || tok == "":
		return fmt.Errorf("%s 的码前缀和令牌前缀都不能为空", cs.Name())
	case code != strings.ToUpper(strings.TrimSpace(code)):
		return fmt.Errorf("%s 的码前缀 %q 要大写、不带空白", cs.Name(), code)
	case tok != strings.ToLower(strings.TrimSpace(tok)):
		return fmt.Errorf("%s 的令牌前缀 %q 要小写、不带空白", cs.Name(), tok)
	}
	type owned struct {
		owner  Name
		prefix string
	}
	var taken []owned
	for _, e := range existing {
		if ec, ok := e.(CardSupply); ok && e.Kind() == KindCardSupply {
			taken = append(taken, owned{e.Name(), strings.ToLower(ec.CodePrefix())}, owned{e.Name(), strings.ToLower(ec.TokenPrefix())})
		}
	}
	taken = append(taken, owned{cs.Name(), strings.ToLower(code)})
	mine := strings.ToLower(tok)
	for _, t := range taken {
		if strings.HasPrefix(t.prefix, mine) || strings.HasPrefix(mine, t.prefix) {
			return fmt.Errorf("%s 的前缀 %q 和 %s 的前缀 %q 重叠", cs.Name(), tok, t.owner, t.prefix)
		}
	}
	mine = strings.ToLower(code)
	for _, t := range taken[:len(taken)-1] {
		if strings.HasPrefix(t.prefix, mine) || strings.HasPrefix(mine, t.prefix) {
			return fmt.Errorf("%s 的前缀 %q 和 %s 的前缀 %q 重叠", cs.Name(), code, t.owner, t.prefix)
		}
	}
	return nil
}

// List 按注册顺序返回全部服务商，这也是界面上的展示顺序。
func List() []Provider {
	regMu.RLock()
	defer regMu.RUnlock()
	return append([]Provider(nil), reg...)
}

// Names 按注册顺序返回全部服务商名。
func Names() []Name {
	list := List()
	out := make([]Name, len(list))
	for i, p := range list {
		out[i] = p.Name()
	}
	return out
}

// Get 按名称取服务商（大小写、两端空白不敏感）。
func Get(n Name) (Provider, bool) {
	want := Name(strings.ToLower(strings.TrimSpace(string(n))))
	for _, p := range List() {
		if p.Name() == want {
			return p, true
		}
	}
	return nil, false
}

// Parse 只接受已注册的服务商名（不区分大小写）。
func Parse(s string) (Name, error) {
	if p, ok := Get(Name(s)); ok {
		return p.Name(), nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknown, s)
}

// AsCardSupply 是供卡型时返回它。
func AsCardSupply(p Provider) (CardSupply, bool) {
	if p == nil || p.Kind() != KindCardSupply {
		return nil, false
	}
	cs, ok := p.(CardSupply)
	return cs, ok
}

// AsUpstream 是上游型时返回它。
func AsUpstream(p Provider) (Upstream, bool) {
	if p == nil || p.Kind() != KindUpstream {
		return nil, false
	}
	up, ok := p.(Upstream)
	return up, ok
}

// TestOnlySnapshot 保存当前注册表，返回恢复函数。只给测试用（注册假服务商后在 t.Cleanup 里恢复），业务代码不要调用。
func TestOnlySnapshot() (restore func()) {
	regMu.RLock()
	saved := append([]Provider(nil), reg...)
	regMu.RUnlock()
	return func() {
		regMu.Lock()
		reg = saved
		regMu.Unlock()
	}
}

// TestOnlyReset 清空注册表。只给测试用，调用前必须先用 TestOnlySnapshot 留好恢复函数。
func TestOnlyReset() {
	regMu.Lock()
	reg = nil
	regMu.Unlock()
}

// ---- 码与令牌归属 ----

// ForCode 按码判定服务商：
//  1. 码前缀命中某个供卡型服务商，归它。这只是候选，调用方必须用数据库行的 provider 确认；
//  2. 否则交给第一个认领它的上游；
//  3. 都没有返回 ErrUnrecognized，绝不退回到任何一家。
func ForCode(code string) (Provider, error) {
	norm := NormalizeCode(code)
	list := List()
	for _, p := range list {
		if cs, ok := AsCardSupply(p); ok && strings.HasPrefix(norm, cs.CodePrefix()) {
			return p, nil
		}
	}
	return forUpstream(list, func(up Upstream) bool { return up.ClaimsCode(norm) })
}

// ForToken 按兑换令牌判定服务商，规则同 ForCode。供卡型的令牌前缀不区分大小写。
func ForToken(token string) (Provider, error) {
	tok := strings.TrimSpace(token)
	lower := strings.ToLower(tok)
	list := List()
	for _, p := range list {
		if cs, ok := AsCardSupply(p); ok && strings.HasPrefix(lower, cs.TokenPrefix()) {
			return p, nil
		}
	}
	return forUpstream(list, func(up Upstream) bool { return up.ClaimsToken(tok) })
}

func forUpstream(list []Provider, claims func(Upstream) bool) (Provider, error) {
	for _, p := range list {
		if up, ok := AsUpstream(p); ok && claims(up) {
			return p, nil
		}
	}
	return nil, ErrUnrecognized
}

// CardSupplyForCode 码前缀命中供卡型服务商时返回它（候选，要由数据库行确认）。
func CardSupplyForCode(code string) (CardSupply, bool) {
	p, err := ForCode(code)
	if err != nil {
		return nil, false
	}
	return AsCardSupply(p)
}

// CardSupplyForToken 令牌前缀命中供卡型服务商时返回它（候选，要由数据库行确认）。
func CardSupplyForToken(token string) (CardSupply, bool) {
	p, err := ForToken(token)
	if err != nil {
		return nil, false
	}
	return AsCardSupply(p)
}

// NormalizeCode 去掉两端空白和零宽字符、转大写。码常被从聊天工具复制过来，带着看不见的字符。
func NormalizeCode(code string) string {
	code = strings.TrimFunc(code, func(r rune) bool {
		switch r {
		case ' ', '\t', '\r', '\n', 0x00a0, 0x200b, 0x200c, 0x200d, 0x2060, 0xfeff, 0x3000:
			return true
		}
		return false
	})
	return strings.ToUpper(code)
}

// ---- 能力 ----

// Supports 判断某服务商能否发、兑某产品的某档位。未注册的服务商一律 false。
func Supports(n Name, product, plan string) bool {
	p, ok := Get(n)
	return ok && p.Supports(product, plan)
}

// PlanNote 是该服务商下这个档位的提示，未注册的服务商返回空串。
func PlanNote(n Name, plan string) string {
	if p, ok := Get(n); ok {
		return p.PlanNote(plan)
	}
	return ""
}

// DisplayName 返回服务商的显示名，未注册的原样返回名称。
func DisplayName(n Name) string {
	if p, ok := Get(n); ok {
		return p.DisplayName()
	}
	return string(n)
}
