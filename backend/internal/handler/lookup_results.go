package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// 批量查结果的令牌数上限，和批量兑换页的张数上限（100）对齐。
const lookupResultsMax = 100

// LookupCDKResultBatch POST /api/v1/lookup/cdk/results
// body: { "tokens": ["redemption_token", …] }
// 批量兑换页的轮询用：一次请求查多个令牌的结果，只在 LookupGuard 里算一次查询限流。
// 每个令牌走和 GET /public/cdk/result 同一套分派，返回 { token, status(HTTP 状态码), data(原响应) }，顺序与请求一致。
// 归上游（卡台）的令牌只转发本站有绑定记录的：批量页的令牌都是本站兑换预览时写过绑定的；
// 其余的本地直接回 404，不打上游，免得一次请求 100 个随机令牌放大成 100 次卡台调用（审计 money N-1）。
func LookupCDKResultBatch(c *gin.Context) {
	var req struct {
		Tokens []string `json:"tokens"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式无效"})
		return
	}
	seen := make(map[string]struct{}, len(req.Tokens))
	tokens := make([]string, 0, len(req.Tokens))
	for _, t := range req.Tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		tokens = append(tokens, t)
	}
	if len(tokens) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供至少一个令牌"})
		return
	}
	if len(tokens) > lookupResultsMax {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "一次最多查询 " + strconv.Itoa(lookupResultsMax) + " 个令牌",
			"max":   lookupResultsMax,
		})
		return
	}

	type item struct {
		Token  string          `json:"token"`
		Status int             `json:"status"`
		Data   json.RawMessage `json:"data,omitempty"`
	}
	isUpstream := make([]bool, len(tokens))
	var upstream []string
	for i, tok := range tokens {
		if isUpstream[i] = routesUpstream(tok); isUpstream[i] {
			upstream = append(upstream, tok)
		}
	}
	known, err := db.KnownRedemptionTokens(upstream)
	if err != nil {
		log.Printf("[lookup] 批量查结果读令牌绑定失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "暂时查不到，请稍后再试"})
		return
	}
	notFound, _ := json.Marshal(gin.H{"error": "没有找到这个兑换令牌的记录"})

	results := make([]item, len(tokens))
	sem := make(chan struct{}, lookupBatchWorkers)
	var wg sync.WaitGroup
	for i, tok := range tokens {
		if isUpstream[i] && !known[tok] {
			results[i] = item{Token: tok, Status: http.StatusNotFound, Data: notFound}
			continue
		}
		// 子协程 panic 时（safego 兜住）这一条就是 500，前端当作暂时出错重试
		results[i] = item{Token: tok, Status: http.StatusInternalServerError}
		wg.Add(1)
		safego.Go("批量查询兑换结果", func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			status, body := runPublicResult(c, tok)
			results[i] = item{Token: tok, Status: status}
			if json.Valid(body) {
				results[i].Data = body
			}
		})
	}
	wg.Wait()

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"max": lookupResultsMax, "results": results})
}

// routesUpstream 判断令牌会不会被 PublicCDKResult 转发给上游服务商：供卡型（本站发的码）只查本库，
// 认不出的令牌 PublicCDKResult 自己回 404，这两种都不打上游。
func routesUpstream(token string) bool {
	p, err := provider.ForToken(token)
	if err != nil {
		return false
	}
	_, local := provider.AsCardSupply(p)
	return !local
}

// runPublicResult 在独立的上下文里跑一遍 PublicCDKResult，拿回状态码和响应体。
// 只带过去请求上下文、来源地址和设备头（卡台要用）。
func runPublicResult(parent *gin.Context, token string) (int, []byte) {
	rec := httptest.NewRecorder()
	cc, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "/result?token="+url.QueryEscape(token), nil).WithContext(parent.Request.Context())
	req.RemoteAddr = parent.Request.RemoteAddr
	for _, h := range []string{"User-Agent", "X-Redemption-Device"} {
		if v := parent.GetHeader(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	cc.Request = req
	PublicCDKResult(cc)
	return rec.Code, rec.Body.Bytes()
}
