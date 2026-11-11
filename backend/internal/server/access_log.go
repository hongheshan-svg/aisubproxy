package server

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// 公开接口的 GET 把卡密和兑换令牌放在查询串里（/public/cdk/result?token=、/lookup/cdk?code=），
// gin 默认的访问日志和崩溃日志会原样记录。这里遮掉这些参数，其余和 gin.Default 一样。

// sensitiveQueryParams 是日志里要遮掉的查询参数。
// setup_token：安装接口只认请求头，旧脚本可能仍放在查询串里，日志照样遮掉。
var sensitiveQueryParams = map[string]bool{"code": true, "cdk_code": true, "token": true, "setup_token": true}

// newEngine 等同于 gin.Default，只是访问日志不带卡密和令牌，崩溃日志不带 panic 的值和请求头（见 safeRecovery）。
func newEngine() *gin.Engine {
	engine := gin.New()
	engine.Use(gin.LoggerWithFormatter(accessLogFormatter), safeRecovery())
	return engine
}

// redactQuery 遮掉路径里的敏感查询参数。参数名按解码后的小写比较，防止 %63ode= 这样的写法绕过。
func redactQuery(path string) string {
	base, raw, ok := strings.Cut(path, "?")
	if !ok {
		return path
	}
	parts := strings.Split(raw, "&")
	for i, kv := range parts {
		name, _, _ := strings.Cut(kv, "=")
		decoded, err := url.QueryUnescape(name)
		if err != nil {
			decoded = name
		}
		if sensitiveQueryParams[strings.ToLower(decoded)] {
			parts[i] = name + "=***"
		}
	}
	return base + "?" + strings.Join(parts, "&")
}

// accessLogFormatter 和 gin 默认的格式一样，只是路径过一遍 redactQuery。
func accessLogFormatter(p gin.LogFormatterParams) string {
	var statusColor, methodColor, resetColor string
	if p.IsOutputColor() {
		statusColor, methodColor, resetColor = p.StatusCodeColor(), p.MethodColor(), p.ResetColor()
	}
	latency := p.Latency
	if latency > time.Minute {
		latency = latency.Truncate(time.Second)
	}
	return fmt.Sprintf("[GIN] %v |%s %3d %s| %13v | %15s |%s %-7s %s %#v\n%s",
		p.TimeStamp.Format("2006/01/02 - 15:04:05"),
		statusColor, p.StatusCode, resetColor,
		latency,
		p.ClientIP,
		methodColor, p.Method, resetColor,
		redactQuery(p.Path),
		p.ErrorMessage,
	)
}

// safeRecovery 兜住处理函数的 panic，替换 gin.Recovery 和之前的查询串遮蔽（B1-03）。
// gin 自带的崩溃日志会写 panic 的值，调试模式下还把请求头（Cookie、X-Setup-Token、X-CSRF-Token）整段写出来；
// 这里只写一行：方法、过了 redactQuery 的请求行、panic 的类型和位置（safego.Describe），和后台协程的约定一致。
// Describe 要在 recover 所在的 defer 里直接调用，所以这里不能再往外重新 panic。
func safeRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				if r == http.ErrAbortHandler {
					panic(r) // 主动中止连接的约定：net/http 自己静默处理，不当崩溃记日志、不回 500
				}
				logger := log.New(gin.DefaultErrorWriter, "", log.LstdFlags)
				logger.Printf("[Recovery] panic recovered: %s %s %s", c.Request.Method, redactQuery(c.Request.RequestURI), safego.Describe(r))
				if !c.Writer.Written() {
					c.AbortWithStatus(http.StatusInternalServerError)
				} else {
					c.Abort()
				}
			}
		}()
		c.Next()
	}
}
