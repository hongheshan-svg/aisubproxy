// Package provider 定义卡服务商：哪家发码、哪家兑换。
//
// 服务商在发码时定死、写在码上，后台只选「默认发码服务商」。切换默认值只影响之后新发的码；
// 一张码只归一家，绝不自动换另一家重试。
package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
)

// Name 是服务商名。
type Name string

const (
	ZovoCard   Name = "zovocard"
	MarsWallex Name = "marswallex"
)

// SettingDefault 是默认发码服务商在 site_settings 里的键。
const SettingDefault = "cdk_default_provider"

// 码和令牌的前缀。分流只认这些前缀，其余一律按现状交给 ZovoCard。
const (
	localCodePrefix  = "MW-"
	localTokenPrefix = "mwt_"
)

// All 是全部服务商，顺序即展示顺序。
var All = []Name{ZovoCard, MarsWallex}

// ErrUnknown 服务商名不在枚举内。
var ErrUnknown = errors.New("未知的卡服务商")

// Parse 只接受两个枚举值（不区分大小写）。
func Parse(s string) (Name, error) {
	switch n := Name(strings.ToLower(strings.TrimSpace(s))); n {
	case ZovoCard, MarsWallex:
		return n, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknown, s)
}

// LoadDefault 读默认发码服务商。没设置过或值不合法都视为 ZovoCard（现状）；读库出错返回错误，调用方不能当成 ZovoCard 继续发码。
func LoadDefault() (Name, error) {
	v, err := db.GetSetting(SettingDefault)
	if err != nil {
		return "", err
	}
	if n, perr := Parse(v); perr == nil {
		return n, nil
	}
	return ZovoCard, nil
}

// SaveDefault 保存默认发码服务商。
func SaveDefault(n Name) error {
	if _, err := Parse(string(n)); err != nil {
		return err
	}
	return db.SetSetting(SettingDefault, string(n))
}

// MarsWallexSelected 判断 MarsWallex 现在是否被选用：默认发码服务商是它，或还有没结清的 MarsWallex 码（随时会用到卡）。
// 自动补卡用它决定要不要暂停。
func MarsWallexSelected(ctx context.Context) (bool, error) {
	def, err := LoadDefault()
	if err != nil {
		return false, err
	}
	if def == MarsWallex {
		return true, nil
	}
	return db.HasOpenLocalCDKs(ctx, string(MarsWallex))
}

// Supports 判断某服务商能否发、兑某产品的某档位。
// ZovoCard 的具体档位以卡台实时下发为准（发码时校验），这里只认产品线；MarsWallex 只做 GPT 五档的新开通。
func Supports(n Name, product, plan string) bool {
	product = strings.ToLower(strings.TrimSpace(product))
	switch n {
	case ZovoCard:
		switch product {
		case "gpt", "x", "grok", "claude":
			return true
		}
	case MarsWallex:
		return product == "gpt" && slices.Contains(payengine.PlanKeys, strings.TrimSpace(plan))
	}
	return false
}

// PlanNote 是该服务商下这个档位需要提示给操作者和用户的话。
// pro_20x 在 ZovoCard 是「Plus 升级 Pro」，在 MarsWallex 是新开通，同名不同义，必须标出来。
func PlanNote(n Name, plan string) string {
	if n == MarsWallex && strings.TrimSpace(plan) == "pro_20x" {
		return "仅限无订阅账号"
	}
	return ""
}

// Capability 是能力矩阵的一行。Plan 为 "*" 时代表整条产品线。
type Capability struct {
	Product    string          `json:"product"`
	Plan       string          `json:"plan"`
	Support    map[Name]bool   `json:"support"`
	Notes      map[Name]string `json:"notes,omitempty"`
	Comment    string          `json:"comment,omitempty"`
	samplePlan string
}

// Capabilities 由 Supports 生成，界面不用另写一份。
func Capabilities() []Capability {
	rows := make([]Capability, 0, len(payengine.PlanKeys)+4)
	for _, p := range payengine.PlanKeys {
		rows = append(rows, Capability{Product: "gpt", Plan: p, samplePlan: p})
	}
	rows = append(rows,
		Capability{Product: "gpt", Plan: "credit*", samplePlan: "credit250", Comment: "Codex 点数"},
		Capability{Product: "x", Plan: "*", samplePlan: "premium_monthly", Comment: "X Premium"},
		Capability{Product: "grok", Plan: "*", samplePlan: "premium_monthly", Comment: "Grok"},
		Capability{Product: "claude", Plan: "*", samplePlan: "pro", Comment: "Claude"},
	)
	for i := range rows {
		r := &rows[i]
		r.Support = make(map[Name]bool, len(All))
		for _, n := range All {
			r.Support[n] = Supports(n, r.Product, r.samplePlan)
			if note := PlanNote(n, r.samplePlan); note != "" {
				if r.Notes == nil {
					r.Notes = map[Name]string{}
				}
				r.Notes[n] = note
			}
		}
	}
	return rows
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

// ResolveCode 按码判定服务商：只看码本身，MW- 开头是 MarsWallex，其余是 ZovoCard。
func ResolveCode(code string) Name {
	if strings.HasPrefix(NormalizeCode(code), localCodePrefix) {
		return MarsWallex
	}
	return ZovoCard
}

// ResolveToken 按兑换令牌判定服务商。
func ResolveToken(token string) Name {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(token)), localTokenPrefix) {
		return MarsWallex
	}
	return ZovoCard
}

// LocalCode 是一张新生成的本地码。Code 和 Token 不入库，只在发码响应里给一次。
type LocalCode struct {
	Code      string
	Prefix    string
	CodeHash  string
	Token     string
	TokenHash string
}

const (
	codeAlphabet  = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // 32 个字符，去掉易混的 I O 0 1
	codeGroupSize = 5
	codeGroups    = 5
	tokenHexLen   = 32
)

// GenerateLocalCode 生成一张码：MW-AAAAA-BBBBB-CCCCC-DDDDD-EEEEE，共 125 位随机。
// 字符表正好 32 个，取随机字节的低 5 位不会有偏差。随机源失败就报错，绝不退化。
func GenerateLocalCode() (LocalCode, error) {
	raw := make([]byte, codeGroupSize*codeGroups)
	if _, err := rand.Read(raw); err != nil {
		return LocalCode{}, fmt.Errorf("生成本地码失败: %w", err)
	}
	var b strings.Builder
	b.WriteString(localCodePrefix)
	for i, v := range raw {
		if i > 0 && i%codeGroupSize == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(codeAlphabet[v&31])
	}
	code := b.String()
	token := TokenForCode(code)
	return LocalCode{
		Code:      code,
		Prefix:    code[:len(localCodePrefix)+codeGroupSize],
		CodeHash:  db.HashCDKCode(code),
		Token:     token,
		TokenHash: HashToken(token),
	}, nil
}

// TokenForCode 由完整码算兑换令牌。同一张码每次预览得到同一个令牌，不用入库；
// 库里只有令牌的哈希，库泄露时推不出令牌，令牌也推不出码。
func TokenForCode(code string) string {
	sum := sha256.Sum256([]byte("local_cdk_token:" + NormalizeCode(code)))
	return localTokenPrefix + hex.EncodeToString(sum[:])[:tokenHexLen]
}

// HashToken 是令牌入库和查询用的哈希。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(token))))
	return hex.EncodeToString(sum[:])
}

// Check 是就绪检查的一项。
type Check struct {
	Key    string `json:"key"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Readiness 是就绪检查的结果。Problems 为空即就绪。
type Readiness struct {
	Provider Name     `json:"provider"`
	OK       bool     `json:"ok"`
	Problems []string `json:"problems"`
	Checks   []Check  `json:"checks"`
}

// NewReadiness 返回一个还没有检查项的结果。
func NewReadiness(n Name) Readiness {
	return Readiness{Provider: n, OK: true, Problems: []string{}, Checks: []Check{}}
}

// Pass 记一项通过的检查。
func (r *Readiness) Pass(key, detail string) {
	r.Checks = append(r.Checks, Check{Key: key, OK: true, Detail: detail})
}

// Fail 记一项没通过的检查，整体变为未就绪。
func (r *Readiness) Fail(key, detail string) {
	r.OK = false
	r.Problems = append(r.Problems, detail)
	r.Checks = append(r.Checks, Check{Key: key, Detail: detail})
}
