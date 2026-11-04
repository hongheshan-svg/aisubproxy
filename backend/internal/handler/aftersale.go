package handler

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// 售后查询分两层：服务商层只回答「这张码现在什么情况」（afterSaleFacts），
// 公共层（本文件）只有一份，所有服务商都经过它：分派、邮箱打码、清洗原文、跟换码链、决定输出哪些字段。

// afterSaleFacts 是服务商层给公共层的查询结果。邮箱、原文都原样给，打码和清洗由公共层统一做。
type afterSaleFacts struct {
	Found              bool
	Status             string // unused|processing|used|failed|disabled|replaced
	Reusable           bool
	Plan, PlanKind     string // PlanKind: subscription|credit|""
	UsedAt, ExpiresAt  *time.Time
	Renewal            string // off|closing|""
	Guide              string // 固定列表里的编号，或空
	ProviderNote       string // 原文，公共层负责清洗
	AccountEmail       string // 原样给，公共层负责打码
	Replacement        string // 新码的完整码，或空
	ReplacedButUnknown bool
	Message            string // 可选；空时由公共层按状态给默认文字
}

// lookupReplacement 是换码链最终那张码：完整码、它自己的状态、经过了几层。
type lookupReplacement struct {
	Code   string `json:"code"`
	Status string `json:"status"`
	Depth  int    `json:"depth"`
}

// afterSaleChainMax 是换码链最多跟几层。
const afterSaleChainMax = 5

const (
	afterSaleNotFoundText    = "未找到该卡密记录"
	afterSaleUnknownText     = "暂时查不到，请稍后再试"
	afterSaleReplacedText    = "该卡密已换发新码"
	afterSaleUnsupportedText = "这张卡密暂不支持在线查询，请联系售卖方"
	afterSaleNoteMaxRunes    = 200
)

// afterSaleGuides 是指引编号的固定列表（设计文档第 4 节），文字在前端语言包里。
var afterSaleGuides = map[string]bool{
	"session_invalid":           true,
	"already_subscribed":        true,
	"plan_unavailable":          true,
	"credit_needs_subscription": true,
	"credit_not_personal":       true,
	"credit_delinquent":         true,
	"credit_region_mismatch":    true,
	"retry_later":               true,
	"manual_review":             true,
	"provider_retry":            true,
	"provider_wait":             true,
}

// afterSaleDefaultMessages 是服务商没给 Message 时按状态给的默认文字。
var afterSaleDefaultMessages = map[string]string{
	"unused":      "卡密未使用",
	"processing":  "卡密兑换处理中",
	"used":        "卡密使用成功",
	"disabled":    "卡密已禁用",
	"expired":     "卡密已过期",
	"replaced":    afterSaleReplacedText,
	"unsupported": afterSaleUnsupportedText,
}

var (
	// 邮箱：本地部分和域名允许非 ASCII 字母，顶级域名只认 ASCII 字母，免得把紧跟在后面的中文也吞掉。
	noteEmailRe = regexp.MustCompile(`[\p{L}\p{N}._%+\-]+@[\p{L}\p{N}\-]+(?:\.[\p{L}\p{N}\-]+)*\.[A-Za-z]{2,}`)
	// 长令牌：20 位以上的连续字母、数字、下划线、横线（JWT、sess_ 这类令牌常被 - 或 _ 切成短段）。
	noteTokenRe = regexp.MustCompile(`[A-Za-z0-9_-]{20,}`)
	// 卡号形状的数字：13～19 位，中间允许单个空格或横线。日期、11 位手机号位数不够，不受影响。
	noteCardRe = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)
)

// sanitizeProviderNote 清洗服务商给的原文：像邮箱、长令牌、卡号的部分换成 ***，再截到 200 个字符。
// 顺序：先邮箱（整个换掉，免得只换掉本地部分的数字、把域名留下），再长令牌（免得卡号规则只吃掉令牌的后半段、留下前缀），
// 最后卡号。先替换再截断，截断不会把邮箱切成认不出的半截漏出去。
func sanitizeProviderNote(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = noteEmailRe.ReplaceAllString(s, "***")
	s = noteTokenRe.ReplaceAllString(s, "***")
	s = noteCardRe.ReplaceAllString(s, "***")
	if r := []rune(s); len(r) > afterSaleNoteMaxRunes {
		s = string(r[:afterSaleNoteMaxRunes])
	}
	return strings.TrimSpace(s)
}

// factsFor 按码找到服务商，调它的查询能力。注册表不认得的码按找不到（Found=false）。
// 服务商查询 panic 时恢复成 error；日志只记服务商名和错误类型（panic 记类型和位置），不记错误内容，里面可能有邮箱、令牌。
func factsFor(ctx context.Context, code, device string) (f afterSaleFacts, err error) {
	defer safego.Recover("售后查询", func(desc string) {
		f, err = afterSaleFacts{}, errors.New("售后查询 panic："+desc)
	})
	p, perr := provider.ForCode(code)
	if perr != nil {
		return afterSaleFacts{}, nil
	}
	defer func() {
		if err != nil {
			log.Printf("[aftersale] 服务商 %s 查询出错：%T", p.Name(), err)
		}
	}()
	if cs, ok := provider.AsCardSupply(p); ok {
		return cardSupplyFacts(ctx, cs, code)
	}
	if a, ok := upstreamAdapterFor(p.Name()); ok {
		if l, ok := a.(upstreamLookuper); ok {
			return l.Lookup(ctx, code, device)
		}
	}
	return afterSaleFacts{Found: true, Status: "unsupported"}, nil
}

// lookupAfterSale 是售后查询的公共层入口：查一张码，跟换码链，打码、清洗后给出对外的结果。
// 服务商查询出错时 status=unknown、message 为「暂时查不到，请稍后再试」，并标上 queryFailed，单条接口据此不回 404。
func lookupAfterSale(ctx context.Context, code, device string) cdkLookupResult {
	norm := provider.NormalizeCode(code)
	f, err := factsFor(ctx, norm, device)
	if err != nil {
		return cdkLookupResult{CDKCode: code, Status: "unknown", Message: afterSaleUnknownText, queryFailed: true}
	}
	if !f.Found {
		msg := sanitizeProviderNote(f.Message)
		if msg == "" {
			msg = afterSaleNotFoundText
		}
		return cdkLookupResult{CDKCode: code, Status: "unknown", Message: msg}
	}
	resp := afterSaleResult(code, f)
	if f.Replacement != "" {
		resp.Replacement = followAfterSaleChain(ctx, norm, f.Replacement, device)
	}
	return resp
}

// afterSaleResult 把一张码的 facts 转成对外的结果（不含换码链）。字段只从 cdkLookupResult 的白名单里出。
func afterSaleResult(code string, f afterSaleFacts) cdkLookupResult {
	status := afterSaleStatus(f)
	note := sanitizeProviderNote(f.ProviderNote)
	resp := cdkLookupResult{
		CDKCode:         code,
		Status:          status,
		Used:            status == "used",
		CanResubmit:     f.Reusable,
		AccountEmail:    maskEmailForAdmin(f.AccountEmail),
		Plan:            f.Plan,
		PlanKind:        f.PlanKind,
		Renewal:         f.Renewal,
		Guide:           f.Guide,
		Notes:           note,
		ProviderNote:    note,
		ReplacedUnknown: f.Replacement == "" && f.ReplacedButUnknown,
		Message:         sanitizeProviderNote(f.Message),
	}
	if resp.Guide != "" && !afterSaleGuides[resp.Guide] {
		if f.Reusable {
			resp.Guide = "provider_retry"
		} else {
			resp.Guide = "provider_wait"
		}
	}
	if f.UsedAt != nil {
		resp.UsedAt = afterSaleTime(*f.UsedAt)
	}
	if status == "used" && f.ExpiresAt != nil {
		resp.ExpiresAt = afterSaleTime(*f.ExpiresAt)
	}
	if resp.Message == "" {
		resp.Message = afterSaleDefaultMessage(status, f.Reusable)
	}
	return resp
}

// afterSaleStatus 是对外的状态：换过码的（不管新码库里有没有）一律是 replaced。
func afterSaleStatus(f afterSaleFacts) string {
	if f.Replacement != "" || f.ReplacedButUnknown {
		return "replaced"
	}
	return f.Status
}

func afterSaleDefaultMessage(status string, reusable bool) string {
	if status == "failed" {
		if reusable {
			return "使用失败，卡密已重新激活，可以重新提交"
		}
		return "使用失败，卡密尚未重新激活，请等待处理后再查"
	}
	if m, ok := afterSaleDefaultMessages[status]; ok {
		return m
	}
	return "卡密状态：" + status
}

func afterSaleTime(t time.Time) *string {
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// followAfterSaleChain 从 start 的新码 next 开始跟换码链（新码可能属于别的服务商，每层都重新分派）。
// 最多跟 afterSaleChainMax 层；遇到走过的码、某一层出错或找不到、请求已取消就停下，返回最后一张能确认的码，一层都确认不了时返回 nil。
func followAfterSaleChain(ctx context.Context, start, next, device string) *lookupReplacement {
	seen := map[string]bool{start: true}
	var last *lookupReplacement
	for depth := 1; depth <= afterSaleChainMax; depth++ {
		if ctx.Err() != nil {
			break
		}
		code := provider.NormalizeCode(next)
		if code == "" || seen[code] {
			break
		}
		seen[code] = true
		f, err := factsFor(ctx, code, device)
		if err != nil || !f.Found {
			break
		}
		last = &lookupReplacement{Code: code, Status: afterSaleStatus(f), Depth: depth}
		if f.Replacement == "" {
			break
		}
		next = f.Replacement
	}
	return last
}
