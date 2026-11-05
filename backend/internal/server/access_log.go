package server

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// 公开接口的 GET 把卡密和兑换令牌放在查询串里（/public/cdk/result?token=、/lookup/cdk?code=），
// gin 默认的访问日志和崩溃日志会原样记录。这里遮掉这些参数，其余和 gin.Default 一样。

// sensitiveQueryParams 是日志里要遮掉的查询参数。
// setup_token：安装接口只认请求头，旧脚本可能仍放在查询串里，日志照样遮掉。
var sensitiveQueryParams = map[string]bool{"code": true, "cdk_code": true, "token": true, "setup_token": true}

// newEngine 等同于 gin.Default，只是访问日志和崩溃日志不带卡密和令牌。
func newEngine() *gin.Engine {
	engine := gin.New()
	engine.Use(gin.LoggerWithFormatter(accessLogFormatter), gin.Recovery(), scrubQueryOnPanic())
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

// scrubQueryOnPanic 放在 gin.Recovery 里面：gin 的崩溃日志会打印请求行，崩溃时先把查询串里的敏感参数遮掉再往外抛。
func scrubQueryOnPanic() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				c.Request.RequestURI = redactQuery(c.Request.RequestURI)
				_, c.Request.URL.RawQuery, _ = strings.Cut(redactQuery("?"+c.Request.URL.RawQuery), "?")
				panic(r)
			}
		}()
		c.Next()
	}
}
