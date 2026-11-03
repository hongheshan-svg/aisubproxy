package provider

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

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

// GenerateCode 用供卡型服务商的码前缀生成一张码，如 MW-AAAAA-BBBBB-CCCCC-DDDDD-EEEEE，共 125 位随机。
// 字符表正好 32 个，取随机字节的低 5 位不会有偏差。随机源失败就报错，绝不退化。
func GenerateCode(p CardSupply) (LocalCode, error) {
	raw := make([]byte, codeGroupSize*codeGroups)
	if _, err := rand.Read(raw); err != nil {
		return LocalCode{}, fmt.Errorf("生成本地码失败: %w", err)
	}
	prefix := p.CodePrefix()
	var b strings.Builder
	b.WriteString(prefix)
	for i, v := range raw {
		if i > 0 && i%codeGroupSize == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(codeAlphabet[v&31])
	}
	code := b.String()
	token := TokenFor(p, code)
	return LocalCode{
		Code:      code,
		Prefix:    code[:len(prefix)+codeGroupSize],
		CodeHash:  db.HashCDKCode(code),
		Token:     token,
		TokenHash: HashToken(token),
	}, nil
}

// TokenFor 由完整码算兑换令牌（带服务商的令牌前缀）。同一张码每次预览得到同一个令牌，不用入库；
// 库里只有令牌的哈希，库泄露时推不出令牌，令牌也推不出码。
func TokenFor(p CardSupply, code string) string {
	sum := sha256.Sum256([]byte("local_cdk_token:" + NormalizeCode(code)))
	return p.TokenPrefix() + hex.EncodeToString(sum[:])[:tokenHexLen]
}

// HashToken 是令牌入库和查询用的哈希。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(token))))
	return hex.EncodeToString(sum[:])
}

// Check 是就绪检查的一项。
type Check struct {
	Key string `json:"key"`
	// Title 是这一项的人话标题，如「MarsWallex 凭证」。界面优先用它，没有时才按 key 查表。
	Title  string `json:"title,omitempty"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	// Raw 是底层错误的原文，只给可展开的详情和排查用，不作为页面正文。
	Raw string `json:"raw,omitempty"`
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
func (r *Readiness) Pass(key, title, detail string) {
	r.Checks = append(r.Checks, Check{Key: key, Title: title, OK: true, Detail: detail})
}

// Fail 记一项没通过的检查，整体变为未就绪。
func (r *Readiness) Fail(key, title, detail string) {
	r.FailRaw(key, title, detail, "")
}

// FailRaw 与 Fail 相同，但把底层错误原文放进 Raw：界面正文只用 detail 这句人话。
func (r *Readiness) FailRaw(key, title, detail, raw string) {
	r.OK = false
	r.Problems = append(r.Problems, detail)
	r.Checks = append(r.Checks, Check{Key: key, Title: title, Detail: detail, Raw: raw})
}

// ---- 过渡：只认 MarsWallex 的旧函数，handler 改完分派后删除 ----

// ResolveCode 旧的按码分流：MW- 开头是 MarsWallex，其余是 ZovoCard。
func ResolveCode(code string) Name {
	if cs, ok := CardSupplyForCode(code); ok && cs.Name() == MarsWallex {
		return MarsWallex
	}
	return ZovoCard
}

// ResolveToken 旧的按令牌分流。
func ResolveToken(token string) Name {
	if cs, ok := CardSupplyForToken(token); ok && cs.Name() == MarsWallex {
		return MarsWallex
	}
	return ZovoCard
}

func mustMarsWallex() CardSupply {
	p, _ := Get(MarsWallex)
	cs, _ := AsCardSupply(p)
	return cs
}

// GenerateLocalCode 旧的生成 MarsWallex 码。
func GenerateLocalCode() (LocalCode, error) { return GenerateCode(mustMarsWallex()) }

// TokenForCode 旧的算 MarsWallex 令牌。
func TokenForCode(code string) string { return TokenFor(mustMarsWallex(), code) }

// All 旧的服务商列表。
var All = []Name{ZovoCard, MarsWallex}
