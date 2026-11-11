package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// 公开批量查询上限：避免一次查太多。
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

// lookupOneCDK 查一张码，交给售后查询公共层（见 aftersale.go）。
func lookupOneCDK(ctx context.Context, code, deviceID string) cdkLookupResult {
	return lookupAfterSale(ctx, code, deviceID)
}
