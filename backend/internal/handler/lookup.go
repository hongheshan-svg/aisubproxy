package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// 公开批量查询上限：每条可能回源卡台 Result，避免一次打爆上游。
const lookupBatchMax = 100
const lookupBatchWorkers = 4

// cdkLookupResult 是查询接口每张码的结果。字段是白名单：只增不删，新增字段要想清楚能不能给用户看。
type cdkLookupResult struct {
	CDKCode         string             `json:"cdk_code"`
	Status          string             `json:"status"` // unused | used | failed | disabled | expired | processing | replaced | unsupported | unknown
	Used            bool               `json:"used"`
	CanResubmit     bool               `json:"can_resubmit"`
	AccountEmail    string             `json:"account_email,omitempty"` // 一律打码
	Plan            string             `json:"plan,omitempty"`
	UsedAt          *string            `json:"used_at,omitempty"`
	Notes           string             `json:"notes,omitempty"` // 清洗后的服务商原文，同 provider_note
	Message         string             `json:"message"`
	PlanKind        string             `json:"plan_kind,omitempty"`
	ExpiresAt       *string            `json:"expires_at,omitempty"` // RFC3339 UTC
	Renewal         string             `json:"renewal,omitempty"`
	Guide           string             `json:"guide,omitempty"`
	ProviderNote    string             `json:"provider_note,omitempty"`
	Replacement     *lookupReplacement `json:"replacement,omitempty"`
	ReplacedUnknown bool               `json:"replaced_unknown,omitempty"`

	// queryFailed：服务商查询出错（不是找不到）。单条接口据此回 200 和「暂时查不到」，不回 404。
	queryFailed bool `json:"-"`
}

// LookupCDKStatus GET /api/v1/lookup/cdk?code=  或兼容 ?cdk_code=
func LookupCDKStatus(c *gin.Context) {
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		code = strings.TrimSpace(c.Query("cdk_code"))
	}
	code = strings.TrimSpace(code)
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入卡密"})
		return
	}

	resp := lookupOneCDK(c.Request.Context(), code, deviceFrom(c))
	if resp.Status == "unknown" && !resp.queryFailed {
		c.JSON(http.StatusNotFound, gin.H{
			"error":    "未找到该卡密记录",
			"cdk_code": code,
			"status":   "unknown",
			"used":     false,
			"message":  "未找到该卡密。请确认输入完整卡密。",
		})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// LookupCDKStatusBatch POST /api/v1/lookup/cdk/batch
// body: { "codes": ["SXC-…"], "text": "可选整段粘贴" }
func LookupCDKStatusBatch(c *gin.Context) {
	var req struct {
		Codes []string `json:"codes"`
		Text  string   `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	codes := normalizeLookupCodes(append(append([]string{}, req.Codes...), splitLookupText(req.Text)...))
	if len(codes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入至少一张卡密"})
		return
	}
	if len(codes) > lookupBatchMax {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "一次最多查询 " + strconv.Itoa(lookupBatchMax) + " 张卡密",
			"max":   lookupBatchMax,
		})
		return
	}

	results := make([]cdkLookupResult, len(codes))
	sem := make(chan struct{}, lookupBatchWorkers)
	var wg sync.WaitGroup
	ctx := c.Request.Context()
	device := deviceFrom(c)
	for i, code := range codes {
		// 先放一个「查询出错」的结果：子协程 panic 时（safego 兜住）这一条就是它，不会是空记录
		results[i] = cdkLookupResult{CDKCode: code, Status: "unknown", Message: afterSaleUnknownText, queryFailed: true}
		wg.Add(1)
		safego.Go("批量查询卡密", func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = lookupOneCDK(ctx, code, device)
		})
	}
	wg.Wait()

	c.JSON(http.StatusOK, gin.H{
		"total":   len(results),
		"max":     lookupBatchMax,
		"results": results,
	})
}

func splitLookupText(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.FieldsFunc(raw, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == ';' || r == '，' || r == '；'
	})
}

func normalizeLookupCodes(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		code := strings.ToUpper(strings.TrimSpace(item))
		if len(code) < 4 {
			continue
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out
}

func applyLookupFailure(resp *cdkLookupResult, notes string, reusable bool) {
	resp.Status = "failed"
	resp.Used = false
	resp.CanResubmit = reusable
	note := strings.TrimSpace(notes)
	if note != "" {
		resp.Notes = note
	}
	if reusable {
		resp.Message = "使用失败，卡密已重新激活，可以重新提交"
	} else {
		resp.Message = "使用失败，卡密尚未重新激活，请等待处理后再查"
	}
	if note != "" {
		resp.Message = resp.Message + "：" + note
	}
}

// lookupOneCDK 查一张码，交给售后查询公共层（见 aftersale.go）。
func lookupOneCDK(ctx context.Context, code, deviceID string) cdkLookupResult {
	return lookupAfterSale(ctx, code, deviceID)
}

// errZovoUnknownKeyStatus：码表里的状态不是已知取值。固定文字，不带原始状态值。
var errZovoUnknownKeyStatus = errors.New("码表状态无法识别")

// zovoCardFacts 是 ZovoCard 的售后查询：本站的码表、换码记录、绑定和卡台订单。
//
// 出错和找不到分开：本站库（cd_keys、cardplatform_cdk_codes、recharge_tasks、agent_cdk_exchanges、绑定）读出错
// （不是 sql.ErrNoRows）就返回 error；卡台 Result 失败时，本站库已能确定状态就照旧用本站数据，
// 本站库什么都没有、只能靠卡台回答时才返回 error。三处都没有这张码返回 Found=false。
// 邮箱原样给公共层打码；Message 只在公共层没有默认文字的状态（expired）上给。
func zovoCardFacts(ctx context.Context, code, deviceID string) (afterSaleFacts, error) {
	var f afterSaleFacts // Status 为空表示本站库还没能确定状态
	reusable := false
	redeemOK := false
	norm := provider.NormalizeCode(code)

	var planType, keyStatus string
	var usedAt, expiresAt sql.NullTime
	err := db.DB.QueryRowContext(ctx, `
		SELECT COALESCE(plan_type,''), COALESCE(status,''), used_at, expires_at
		FROM cd_keys WHERE upper(trim(code)) = upper(trim(?))
	`, code).Scan(&planType, &keyStatus, &usedAt, &expiresAt)
	switch {
	case err == nil:
		f.Plan = planType
		switch strings.ToLower(keyStatus) {
		case "used":
			f.Status = "used"
		case "disabled":
			f.Status = "disabled"
		case "expired":
			f.Status, f.Message = "expired", "卡密已过期"
		case "active", "":
			f.Status = "unused"
			reusable = true
		default:
			return afterSaleFacts{}, errZovoUnknownKeyStatus
		}
		if expiresAt.Valid && expiresAt.Time.Before(time.Now()) && f.Status == "unused" {
			f.Status, f.Message = "expired", "卡密已过期"
			reusable = false
		}
		if usedAt.Valid {
			t := usedAt.Time.UTC()
			f.UsedAt = &t
		}
	case !errors.Is(err, sql.ErrNoRows):
		return afterSaleFacts{}, fmt.Errorf("查询码表: %w", err)
	}

	var cpStatus, cpPlan string
	err = db.DB.QueryRowContext(ctx, `
		SELECT COALESCE(status,''), COALESCE(plan,'')
		FROM cardplatform_cdk_codes WHERE upper(trim(code)) = upper(trim(?))
		ORDER BY created_at DESC LIMIT 1
	`, code).Scan(&cpStatus, &cpPlan)
	switch {
	case err == nil:
		if f.Plan == "" {
			f.Plan = cpPlan
		}
		st := strings.ToLower(strings.TrimSpace(cpStatus))
		if st == "" {
			st = "unused"
		}
		switch st {
		case "used", "redeemed", "consumed":
			f.Status, f.Message = "used", ""
			reusable = false
		case "disabled":
			if f.Status != "used" {
				f.Status, f.Message = "disabled", ""
			}
			reusable = false
		case "unused", "active":
			if f.Status == "" {
				f.Status = "unused"
			}
			if f.Status == "unused" {
				reusable = true
			}
		}
	case !errors.Is(err, sql.ErrNoRows):
		return afterSaleFacts{}, fmt.Errorf("查询卡台码: %w", err)
	}

	var taskStatus string
	var accountEmail, taskNotes sql.NullString
	var taskCompleted sql.NullTime
	err = db.DB.QueryRowContext(ctx, `
		SELECT COALESCE(task_status,''), account_email, completed_at, COALESCE(notes,'')
		FROM recharge_tasks
		WHERE upper(trim(cdk_code)) = upper(trim(?))
		ORDER BY created_at DESC LIMIT 1
	`, code).Scan(&taskStatus, &accountEmail, &taskCompleted, &taskNotes)
	switch {
	case err == nil:
		if accountEmail.Valid && strings.TrimSpace(accountEmail.String) != "" {
			f.AccountEmail = strings.TrimSpace(accountEmail.String)
		}
		switch strings.ToLower(taskStatus) {
		case "completed", "success", "done":
			redeemOK = true
			f.Status, f.Message = "used", ""
			reusable = false
			if taskCompleted.Valid {
				t := taskCompleted.Time.UTC()
				f.UsedAt = &t
			}
		case "pending", "submitted", "running", "queued", "processing":
			if !redeemOK && f.Status != "used" {
				f.Status, f.Message = "processing", ""
			}
		case "failed", "declined", "cancelled":
			if !redeemOK {
				zovoMarkFailed(&f, taskNotes.String, reusable)
			}
		}
	case !errors.Is(err, sql.ErrNoRows):
		return afterSaleFacts{}, fmt.Errorf("查询兑换任务: %w", err)
	}

	// 换码：只认 done。pending / unknown / disabling 时新码可能还没发出，或旧码还能用，状态照常判断。
	replaced := false
	ex, err := db.GetAgentExchangeByOldHash(db.HashCDKCode(norm))
	switch {
	case err == nil:
		if ex.State == db.AgentExchangeDone {
			replaced = true
			f.Status, f.Message, f.Reusable, f.Guide, f.ProviderNote = "replaced", "", false, "", ""
			if newCode, ok := db.LookupCardplatformCDKCode(ex.NewUpstreamID, ex.NewCodePrefix); ok {
				f.Replacement = newCode
			} else {
				f.ReplacedButUnknown = true
			}
		}
	case !errors.Is(err, sql.ErrNoRows):
		return afterSaleFacts{}, fmt.Errorf("查询换码记录: %w", err)
	}

	bind, err := db.GetBindingByCDK(code)
	if err != nil {
		return afterSaleFacts{}, fmt.Errorf("查询绑定: %w", err)
	}
	if bind != nil {
		if f.AccountEmail == "" {
			f.AccountEmail = bind.AccountEmail
		}
		if tok := strings.TrimSpace(bind.RedemptionToken); tok != "" && !replaced {
			localKnown := f.Status != ""
			st, raw, rerr := cardplatform.NewFromSettings().Result(ctx, tok, deviceID)
			var payload map[string]any
			if rerr != nil || st < 200 || st >= 300 || len(raw) == 0 || json.Unmarshal(raw, &payload) != nil || payload == nil {
				if !localKnown {
					return afterSaleFacts{}, errZovoRemoteUnavailable
				}
			} else {
				orderStatus := strAny(payload["status"])
				if orderStatus == "" {
					if order, ok := payload["order"].(map[string]any); ok {
						orderStatus = strAny(order["status"])
					}
				}
				email := strAny(payload["account_email"])
				if email == "" {
					if order, ok := payload["order"].(map[string]any); ok {
						email = strAny(order["account_email"])
					}
				}
				if email != "" {
					f.AccountEmail = email
				}
				switch os := strings.ToLower(orderStatus); {
				case os == "completed" || os == "success" || os == "done" || os == "paid":
					clearSessionAfterCompleted(code, tok)
					f.Status, f.Reusable, f.Message = "used", false, ""
				case os == "failed" || os == "declined" || os == "cancelled":
					if !redeemOK {
						zovoMarkFailed(&f, "", reusable)
					}
				case os != "":
					if !redeemOK && f.Status != "used" && f.Status != "failed" {
						f.Status, f.Message = "processing", ""
					}
				}
			}
		}
	}

	if f.AccountEmail != "" && f.Status == "unused" && !f.Reusable {
		f.Status, f.Message = "used", ""
	}
	if f.Status == "" {
		return afterSaleFacts{}, nil
	}
	f.Found = true
	if f.Status == "used" {
		f.Reusable = false
	}
	return f, nil
}

// errZovoRemoteUnavailable：本站库没有这张码的状态、卡台又查不到。固定文字。
var errZovoRemoteUnavailable = errors.New("卡台暂时查不到")

// zovoMarkFailed 记下兑换失败：原文放 ProviderNote（公共层清洗），指引按能否重新提交给。
func zovoMarkFailed(f *afterSaleFacts, notes string, reusable bool) {
	f.Status, f.Reusable, f.Message = "failed", reusable, ""
	if n := strings.TrimSpace(notes); n != "" {
		f.ProviderNote = n
	}
	if reusable {
		f.Guide = "provider_retry"
	} else {
		f.Guide = "provider_wait"
	}
}
