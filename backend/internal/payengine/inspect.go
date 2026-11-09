package payengine

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
)

// InspectRequest 是一次账号核对的输入。Session 是买家粘贴的原文，核对只读它，不改、不存。
type InspectRequest struct{ PlanKey, Region, Session string }

// VerdictKind 是核对结论的类别。
type VerdictKind string

const (
	VerdictEligible       VerdictKind = "eligible"        // 账号符合，可以兑换
	VerdictIneligible     VerdictKind = "ineligible"      // 账号不符合，BlockingCode 说明原因
	VerdictSessionInvalid VerdictKind = "session_invalid" // 会话无法解析或已失效
	VerdictUnknown        VerdictKind = "unknown"         // 暂时核对不了，Cause 说明原因
)

// AccountVerdict 是核对结论。不含会话原文与 token。
type AccountVerdict struct {
	Kind                  VerdictKind
	Email                 string // ParseSession 得到的邮箱（已是小写）；调用方自己脱敏
	CurrentPlan           string // acc.PlanType 转小写，空时为 "free"
	HasActiveSubscription bool
	BlockingCode          string // 只在 ineligible 时有值，取自判断函数的放弃码
	AccountCurrency       string // 只在 credit_currency_mismatch 时有值（缺省按 USD）
	CodeCurrency          string // 码地区的币种
	Cause                 string // 只在 unknown 时有值：plan、proxy、client、cloudflare、rate_limited、timeout、network、engine；处理函数另会设 limit
}

// inspectTimeout 是整次核对的时限。包级变量，测试里可以临时调小。
var inspectTimeout = 20 * time.Second

func unknownVerdict(cause string) AccountVerdict {
	return AccountVerdict{Kind: VerdictUnknown, Cause: cause}
}

// InspectAccount 只读地核对买家账号能不能兑换：用与付款时同一份判断规则，但不建单、不写库、不改买家的会话原文、不告警。
// error 只在调用方的 ctx 已结束时非 nil；其余失败都体现在结论里。
func (s *Service) InspectAccount(ctx context.Context, r InspectRequest) (AccountVerdict, error) {
	if s == nil {
		return unknownVerdict("engine"), nil
	}
	if err := ctx.Err(); err != nil {
		return AccountVerdict{}, err
	}
	_, rc, err := ResolvePlan(r.PlanKey, r.Region, false)
	if err != nil {
		return unknownVerdict("plan"), nil
	}
	sess, err := chatgpt.ParseSession(r.Session)
	if err != nil {
		return AccountVerdict{Kind: VerdictSessionInvalid}, nil
	}
	proxies, err := LoadProxies()
	if err != nil {
		log.Printf("[payengine] 账号核对读取地区代理失败: %v", err)
		return unknownVerdict("proxy"), nil
	}
	gpt, err := s.opts.ChatGPT(proxies[r.Region])
	if err != nil {
		log.Printf("[payengine] 账号核对创建 ChatGPT 客户端失败")
		return unknownVerdict("client"), nil
	}

	cctx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	// fail 把调用失败归类成结论；调用方自己取消时改为返回 ctx 的错误。
	fail := func(step string, err error) (AccountVerdict, error) {
		if perr := ctx.Err(); perr != nil {
			return AccountVerdict{}, perr
		}
		if errors.Is(err, chatgpt.ErrUnauthorized) {
			return AccountVerdict{Kind: VerdictSessionInvalid, Email: sess.Email}, nil
		}
		cause := inspectCause(cctx, err)
		if cause == "cloudflare" {
			log.Printf("[payengine] 账号核对被 Cloudflare 拦截（地区 %s，步骤 %s）", r.Region, step)
		}
		v := unknownVerdict(cause)
		v.Email = sess.Email
		return v, nil
	}

	if err := gpt.Refresh(cctx, &sess); err != nil { // 只刷新会话副本
		return fail("refresh", err)
	}
	acc, err := gpt.Account(cctx, sess)
	if err != nil {
		return fail("account", err)
	}
	v := AccountVerdict{
		Kind:                  VerdictEligible,
		Email:                 sess.Email,
		CurrentPlan:           strings.ToLower(strings.TrimSpace(acc.PlanType)),
		HasActiveSubscription: acc.HasActiveSubscription,
		CodeCurrency:          rc.Currency,
	}
	if v.CurrentPlan == "" {
		v.CurrentPlan = "free"
	}

	var f *stepFailure
	if PlanKind(r.PlanKey) == KindCredit {
		// 顺序与付款时的 checkCredit 一致
		if f = creditAccountEligibility(acc); f == nil {
			delinquent := acc.IsDelinquent
			if delinquent == nil {
				info, err := gpt.SubscriptionStatus(cctx, sess, acc.AccountID)
				if err != nil {
					return fail("subscription", err)
				}
				delinquent = info.IsDelinquent
			}
			f = creditBillingEligibility(acc, delinquent, rc.Currency)
		}
	} else {
		f = subscriptionEligibility(acc)
	}
	if f != nil {
		v.Kind, v.BlockingCode = VerdictIneligible, f.code
		if f.code == "credit_currency_mismatch" {
			v.AccountCurrency = acc.BillingCurrency
			if v.AccountCurrency == "" {
				v.AccountCurrency = defaultBillingCurrency
			}
		}
	}
	return v, nil
}

// inspectCause 把调用失败归成 unknown 的原因。cctx 是带自设时限的 ctx，调用方取消的情况由调用者先排除。
func inspectCause(cctx context.Context, err error) string {
	var he *chatgpt.HTTPError
	switch {
	case errors.Is(err, chatgpt.ErrBlocked):
		return "cloudflare"
	case errors.As(err, &he) && he.Status == 429:
		return "rate_limited"
	case errors.Is(cctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "network"
	}
}
