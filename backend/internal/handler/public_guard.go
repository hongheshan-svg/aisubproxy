package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// publicBodyMax 是公开接口请求体的上限。预检令牌最长 128 KB（localPreflightMaxLen），256 KB 足够。
const publicBodyMax = 256 << 10

const (
	publicBodyTooLargeText = "请求太大"
	publicRateLimitedText  = "请求过于频繁，请稍后再试"
)

// PublicGuard 是 /api/v1/public 组的中间件：先按 IP 限流（GET、HEAD 算查询，其余算变更），通过了再读请求体，
// 超过 256 KB 返回 413。
func PublicGuard() gin.HandlerFunc {
	return publicGuard(func(c *gin.Context) string {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			return localRateQuery
		}
		return localRateMutate
	})
}

// LookupGuard 是 /api/v1/lookup 组的中间件：都按查询限流（批量查询也是，批量页会轮询），请求体同样限 256 KB。
// 批量查询先过这里的大小检查，再切分条数（lookupBatchMax 照旧）。
func LookupGuard() gin.HandlerFunc {
	return publicGuard(func(*gin.Context) string { return localRateQuery })
}

// publicGuard 限流在读请求体之前：超额的请求连体都不读。请求体读进内存（最多 publicBodyMax），后面的处理照常从 Body 读。
func publicGuard(kind func(*gin.Context) string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !localRateAllow(kind(c), c.ClientIP()) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": publicRateLimitedText, "error_code": "rate_limited"})
			return
		}
		if c.Request.ContentLength > publicBodyMax {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": publicBodyTooLargeText, "error_code": "body_too_large"})
			return
		}
		if c.Request.Body == nil || c.Request.Body == http.NoBody {
			c.Next()
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, publicBodyMax))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": publicBodyTooLargeText, "error_code": "body_too_large"})
				return
			}
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid request", "error_code": "invalid_request"})
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Next()
	}
}

// localRateSweepEvery 是限流表的清理间隔：每次删掉一小时以前开始的窗口。
const localRateSweepEvery = time.Minute

// StartLocalRateSweep 启动限流表的定时清理，ctx 结束时停止。
func StartLocalRateSweep(ctx context.Context) {
	safego.Go("公开接口限流表清理循环", func() {
		safego.Loop(ctx, "公开接口限流表清理", localRateSweepEvery, localRateSweepEvery, func(context.Context) {
			sweepLocalRate(localRateNow())
		})
	})
}
