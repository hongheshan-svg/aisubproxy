package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/gptcheck"
)

func accounthubBaseURL() string {
	if u := strings.TrimSpace(os.Getenv("ACCOUNTHUB_BASE_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://localhost:8788"
}

// billingSessionCheck 用用户当场粘贴的会话查上游。只能用于粘贴模式，绝不能传存储的会话。
var billingSessionCheck = gptcheck.Check

type acchubInvoiceResp struct {
	InvoiceURL string                   `json:"invoice_url"`
	Invoices   []map[string]interface{} `json:"invoices"`
	Source     string                   `json:"source"`
	Error      string                   `json:"error"`
}

// queryAccounthubInvoices 通过邮箱调 accounthub 获取账单。
func queryAccounthubInvoices(email string) (*acchubInvoiceResp, error) {
	base := accounthubBaseURL()
	u := fmt.Sprintf("%s/gpt/invoices-by-email?email=%s", base, url.QueryEscape(email))
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("accounthub 请求失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		var errResp struct {
			Error string `json:"error"`
		}
		json.Unmarshal(body, &errResp)
		return nil, fmt.Errorf("accounthub %d: %s", resp.StatusCode, errResp.Error)
	}
	var out acchubInvoiceResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("accounthub 响应解析失败")
	}
	return &out, nil
}

// SessionBillingCheck POST /api/v1/public/billing/check
// 支持：
//  1. cdk_code — 用兑换时记下的账号邮箱经 accounthub 查账单。★知道卡密不等于本人，绝不拿存储的会话去调上游★
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

// billingCheckByCDK 凭卡密查账单：只用绑定时记下的邮箱问 accounthub。查不到邮箱或 accounthub 不可用就如实报错，
// 让用户粘贴 session，不回退到用存储的会话调上游。
func billingCheckByCDK(c *gin.Context, cdk string) {
	email, err := db.GetAccountEmailByCDK(cdk)
	if err != nil {
		log.Printf("[billing] 读取卡密绑定的邮箱失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询绑定失败", "error_code": "lookup_failed"})
		return
	}
	if email == "" {
		c.JSON(http.StatusNotFound, gin.H{
			"error":      "未找到该卡密绑定的账号。请确认兑换时使用了 session 凭证，或改用直接粘贴 session 查询。",
			"error_code": "binding_not_found",
		})
		return
	}
	inv, err := queryAccounthubInvoices(email)
	if err != nil {
		log.Printf("[billing] accounthub 查询失败: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "账单服务暂时不可用，请稍后再试，或改用直接粘贴 session 查询。", "error_code": "billing_unavailable"})
		return
	}
	// 知道卡密不等于本人（审计 F-1）：邮箱打码，账单只给金额、状态、时间这类摘要，不给账单链接和 PDF。
	// 托管账单页不用登录就能打开，上面有账单抬头和邮箱；要链接请用户当场粘贴 session 查询。
	c.JSON(http.StatusOK, gin.H{
		"summary":     map[string]interface{}{"email": maskEmailForAdmin(email)},
		"invoices":    billingInvoiceDigest(inv.Invoices),
		"auth_source": "accounthub",
		"note":        "凭卡密查询只显示账单金额和时间，不提供账单链接；需要账单链接请改用 Session 查询",
	})
}

// billingInvoiceDigestKeys 是凭卡密查账单时每张账单可以给出的字段：金额、币种、状态和时间。
// 其余字段（hosted_invoice_url、invoice_pdf、customer_*、description、lines 等）一律不给。
var billingInvoiceDigestKeys = []string{"id", "number", "status", "paid", "total", "amount_paid", "amount_due", "currency", "created", "period_start", "period_end"}

// billingInvoiceDigest 把 accounthub 的账单列表按白名单裁成摘要，裁完什么都不剩的（例如只有链接）不要；永不返回 nil。
func billingInvoiceDigest(list []map[string]interface{}) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(list))
	for _, inv := range list {
		d := make(map[string]interface{}, len(billingInvoiceDigestKeys))
		for _, k := range billingInvoiceDigestKeys {
			if v, ok := inv[k]; ok {
				d[k] = v
			}
		}
		if len(d) > 0 {
			out = append(out, d)
		}
	}
	return out
}
