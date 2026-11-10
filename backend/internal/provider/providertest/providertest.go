// Package providertest 提供两家假服务商，用来在测试里证明注册表可扩展：
// 供卡型 testcard（码前缀 TC-、令牌前缀 tct_，只做 GPT plus），上游型 testup（认领 TU- 开头的码和 tut_ 开头的令牌，做 GPT 和 X）。
package providertest

import (
	"slices"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// 假服务商的名称。
const (
	TestCard provider.Name = "testcard"
	TestUp   provider.Name = "testup"
)

// Card 是可配置的假供卡型服务商。
type Card struct {
	N           provider.Name
	Display     string
	Code, Token string
	Prods       []string
	Plans       []string // 支持的 GPT 档位
}

func (c Card) Name() provider.Name { return c.N }
func (c Card) DisplayName() string { return c.Display }
func (c Card) Kind() provider.Kind { return provider.KindCardSupply }
func (c Card) Products() []string  { return c.Prods }
func (c Card) CodePrefix() string  { return c.Code }
func (c Card) TokenPrefix() string { return c.Token }
func (c Card) PlanNote(plan string) string {
	if plan == "plus" {
		return "测试提示"
	}
	return ""
}
func (c Card) Supports(product, plan string) bool {
	return slices.Contains(c.Prods, strings.ToLower(strings.TrimSpace(product))) && slices.Contains(c.Plans, strings.TrimSpace(plan))
}

// Up 是可配置的假上游服务商。
type Up struct {
	N                 provider.Name
	Display           string
	CodePfx, TokenPfx string
	Prods             []string
}

func (u Up) Name() provider.Name { return u.N }
func (u Up) DisplayName() string { return u.Display }
func (u Up) Kind() provider.Kind { return provider.KindUpstream }
func (u Up) Products() []string  { return u.Prods }
func (u Up) PlanNote(string) string {
	return ""
}
func (u Up) Supports(product, _ string) bool {
	return slices.Contains(u.Prods, strings.ToLower(strings.TrimSpace(product)))
}
func (u Up) ClaimsCode(code string) bool {
	return u.CodePfx != "" && strings.HasPrefix(code, u.CodePfx)
}
func (u Up) ClaimsToken(token string) bool {
	return u.TokenPfx != "" && strings.HasPrefix(token, u.TokenPfx)
}

// NewTestCard 返回 testcard：TC- / tct_，只做 GPT plus。
func NewTestCard() Card {
	return Card{N: TestCard, Display: "TestCard", Code: "TC-", Token: "tct_", Prods: []string{"gpt"}, Plans: []string{"plus"}}
}

// NewTestUp 返回 testup：认领 TU- 码和 tut_ 令牌，做 GPT 和 X。
func NewTestUp() Up {
	return Up{N: TestUp, Display: "TestUp", CodePfx: "TU-", TokenPfx: "tut_", Prods: []string{"gpt", "x"}}
}

// Register 在注册表里加上 ps，测试结束时恢复原样。
func Register(t testing.TB, ps ...provider.Provider) {
	t.Helper()
	t.Cleanup(provider.TestOnlySnapshot())
	for _, p := range ps {
		provider.Register(p)
	}
}

// RegisterBoth 注册 testcard 和 testup。
func RegisterBoth(t testing.TB) {
	t.Helper()
	Register(t, NewTestCard(), NewTestUp())
}
