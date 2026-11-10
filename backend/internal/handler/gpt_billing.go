package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/gptcheck"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// 凭卡密查账单的对外文字。
const (
	billingCDKNote             = "这是本站开通这张卡密时的记录，不是 ChatGPT 的实时账单；要看实时账单，请改用登录信息查询。"
	billingCDKNotFoundText     = "未找到这张卡密"
	billingCDKNotRedeemedText  = "这张卡密还没开通成功"
	billingCDKReplacedText     = "这张卡密已换发新码，请用新码查询"
	billingCDKLookupFailedText = "查询失败，请稍后再试"
)

// billingSessionCheck 用用户当场粘贴的会话查上游。只能用于粘贴模式，绝不能传存储的会话。
var billingSessionCheck = gptcheck.Check

// billingCardFacts 是凭卡密查账单读本站码的入口，测试里可替换成会出错或 panic 的版本。
var billingCardFacts = cardSupplyFacts

// 登录信息查询对外只给这些字段（2026-11-10 用户决定）。不给金额、币种、账单描述（Stripe 的描述里带单价）、
// 账单链接和 PDF（点开就是金额），也不给计费币种（能看出地区）：代理商进价和最终买家付的钱不一样，显示出来会误导。
var (
	billingSummaryKeys = []string{"plan_type", "subscription_plan", "has_active_subscription", "will_renew", "billing_period", "expires_at", "renews_at", "cancels_at"}
	billingInvoiceKeys = []string{"id", "number", "status", "paid", "created"}
)

// SessionBillingCheck POST /api/v1/public/billing/check
// 支持：
//  1. cdk_code — 给出本站开通这张码时的记录（见 billingCheckByCDK）。★知道卡密不等于本人，绝不拿存储的会话去调上游★
//  2. token_input / session — 用户当场粘贴 session / accessToken，用这一份查上游；摘要和账单只给白名单字段，不给金额
func SessionBillingCheck(c *gin.Context) {
	var req struct {
		TokenInput string `json:"token_input"`
		Session    string `json:"session"`
		CDKCode    string `json:"cdk_code"`
		Code       string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request", "error_code": "invalid_request"})
		return
	}

	raw := strings.TrimSpace(req.TokenInput)
	if raw == "" {
		raw = strings.TrimSpace(req.Session)
	}
	cdk := strings.TrimSpace(req.CDKCode)
	if cdk == "" {
		cdk = strings.TrimSpace(req.Code)
	}

	if raw == "" && cdk != "" {
		billingCheckByCDK(c, cdk)
		return
	}

	if raw == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入卡密，或粘贴 session JSON / accessToken", "error_code": "input_required"})
		return
	}

	// 用户当场粘贴的 session / accessToken
	res, err := billingSessionCheck(raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "error_code": "session_invalid"})
		return
	}
	invoices := make([]map[string]interface{}, 0, len(res.Invoices))
	for _, inv := range res.Invoices {
		invoices = append(invoices, pickKeys(inv, billingInvoiceKeys))
	}
	c.JSON(http.StatusOK, gin.H{
		"summary":     pickKeys(res.Summary, billingSummaryKeys),
		"invoices":    invoices,
		"auth_source": "session",
	})
}

// pickKeys 只取白名单里的字段；原来没有的字段不补。
func pickKeys(m map[string]interface{}, keys []string) map[string]interface{} {
	out := make(map[string]interface{}, len(keys))
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

// billingCDKRecord 是凭卡密查账单对外给出的开通记录，字段只有这些（审计 F-1：知道卡密不等于本人）。
// 不给付款金额和币种（代理商进价和最终买家付的钱不一样，显示出来会误导），也不给注资成本、卡、订单号、会话和完整邮箱。
// 时间是 Unix 秒。
type billingCDKRecord struct {
	Email       string `json:"email"`                  // 打码后的账号邮箱
	Plan        string `json:"plan"`                   // 套餐，如 plus、credit250
	PlanKind    string `json:"plan_kind"`              // subscription | credit
	RedeemedAt  *int64 `json:"redeemed_at,omitempty"`  // 开通成功的时间
	AccessUntil *int64 `json:"access_until,omitempty"` // 关续费核实时 ChatGPT 给的权益到期时间
	RenewalOff  bool   `json:"renewal_off"`            // 只有核实关掉续费才为 true
}

// billingLocalFacts 只查本站供卡型服务商的码，别家的码、认不出的码按找不到（Found=false）。
// 不经 factsFor：以后接了外部上游也不会把码发出去。查询 panic 时恢复成 error，日志只有 safego 记的类型和位置。
func billingLocalFacts(ctx context.Context, norm string) (f afterSaleFacts, err error) {
	defer safego.Recover("凭卡密查账单", func(desc string) {
		f, err = afterSaleFacts{}, errors.New("凭卡密查账单 panic："+desc)
	})
	p, perr := provider.ForCode(norm)
	if perr != nil {
		return afterSaleFacts{}, nil
	}
	cs, ok := provider.AsCardSupply(p)
	if !ok {
		return afterSaleFacts{}, nil
	}
	return billingCardFacts(ctx, cs, norm)
}

// billingCheckByCDK 凭卡密查账单：给出本站开通这张码时的记录，不是 ChatGPT 的实时账单。
// 状态沿用售后查询的推导（cardSupplyFacts），只读本站库，不用存储的会话。要实时账单，请用户粘贴登录信息查。
func billingCheckByCDK(c *gin.Context, cdk string) {
	norm := provider.NormalizeCode(cdk)
	f, err := billingLocalFacts(c.Request.Context(), norm)
	if err != nil {
		// 客户端断开、超时不算查询出错，不记日志
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			log.Printf("[billing] 查询本站码出错：%s", sanitizeForLog(err.Error(), norm))
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": billingCDKLookupFailedText, "error_code": "lookup_failed"})
		return
	}
	if !f.Found {
		c.JSON(http.StatusNotFound, gin.H{"error": billingCDKNotFoundText, "error_code": "cdk_not_found"})
		return
	}
	switch f.Status {
	case "used":
	case "replaced":
		c.JSON(http.StatusNotFound, gin.H{"error": billingCDKReplacedText, "error_code": "cdk_replaced"})
		return
	default:
		c.JSON(http.StatusNotFound, gin.H{"error": billingCDKNotRedeemedText, "error_code": "cdk_not_redeemed", "status": f.Status})
		return
	}

	rec := billingCDKRecord{
		Email:      maskEmailForAdmin(f.AccountEmail),
		Plan:       f.Plan,
		PlanKind:   f.PlanKind,
		RenewalOff: f.Renewal == "off",
	}
	if f.UsedAt != nil {
		v := f.UsedAt.Unix()
		rec.RedeemedAt = &v
	}
	if f.ExpiresAt != nil {
		v := f.ExpiresAt.Unix()
		rec.AccessUntil = &v
	}
	c.JSON(http.StatusOK, gin.H{
		"auth_source": "cdk",
		"record":      rec,
		"note":        billingCDKNote,
	})
}
