package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/gptcheck"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
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

// SessionBillingCheck POST /api/v1/public/billing/check
// 支持：
//  1. cdk_code — 给出本站开通这张码时的记录（见 billingCheckByCDK）。★知道卡密不等于本人，绝不拿存储的会话去调上游★
//  2. token_input / session — 用户当场粘贴 session / accessToken，用这一份查上游
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
	c.JSON(http.StatusOK, gin.H{
		"summary":     res.Summary,
		"invoices":    res.Invoices,
		"auth_source": "session",
	})
}

// billingCDKRecord 是凭卡密查账单对外给出的开通记录，字段只有这些（审计 F-1：知道卡密不等于本人）。
// 不给注资成本、卡、订单号、会话和完整邮箱。时间是 Unix 秒。
type billingCDKRecord struct {
	Email       string `json:"email"`                  // 打码后的账号邮箱
	Plan        string `json:"plan"`                   // 套餐，如 plus、credit250
	PlanKind    string `json:"plan_kind"`              // subscription | credit
	RedeemedAt  *int64 `json:"redeemed_at,omitempty"`  // 开通成功的时间
	AccessUntil *int64 `json:"access_until,omitempty"` // 关续费核实时 ChatGPT 给的权益到期时间
	RenewalOff  bool   `json:"renewal_off"`            // 只有核实关掉续费才为 true
	Amount      string `json:"amount,omitempty"`       // 开通时的应付金额，已按币种小数位格式化，如 "1120.00"
	Currency    string `json:"currency,omitempty"`     // 付款币种，如 PHP
}

// billingCheckByCDK 凭卡密查账单：给出本站开通这张码时的记录，不是 ChatGPT 的实时账单。
// 状态沿用售后查询的推导（factsFor）。只读本站库，不调任何上游，不用存储的会话；要实时账单，请用户粘贴登录信息查。
func billingCheckByCDK(c *gin.Context, cdk string) {
	f, err := factsFor(c.Request.Context(), provider.NormalizeCode(cdk), "")
	if err != nil { // factsFor 已经记过清洗后的日志
		c.JSON(http.StatusInternalServerError, gin.H{"error": billingCDKLookupFailedText, "error_code": "lookup_failed"})
		return
	}
	if !f.Found || f.Status == "unsupported" {
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
	if cur := strings.ToUpper(strings.TrimSpace(f.Currency)); f.AmountMinor > 0 && cur != "" {
		rec.Amount, rec.Currency = payengine.FormatMinor(f.AmountMinor, cur), cur
	}
	c.JSON(http.StatusOK, gin.H{
		"auth_source": "cdk",
		"record":      rec,
		"note":        billingCDKNote,
	})
}
