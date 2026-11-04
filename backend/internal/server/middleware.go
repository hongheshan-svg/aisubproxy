package server

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/tuzi/cdk-recharge-system/internal/auth"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// allowedOrigins 来自环境变量 CORS_ALLOWED_ORIGINS（逗号分隔），默认仅本地开发端口。
func allowedOrigins() []string {
	if raw := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS")); raw != "" {
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if v := strings.TrimSpace(p); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	return []string{
		"http://localhost:5173", "http://127.0.0.1:5173",
		"http://localhost:5174", "http://127.0.0.1:5174",
	}
}

// SecurityHeadersMiddleware 设置一组基础安全响应头；HSTS 仅在 HTTPS 下下发。
func SecurityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

		// 仅当确实走 HTTPS 时才下发 HSTS：直连 TLS，或可信反代（TRUSTED_PROXIES）标了 X-Forwarded-Proto=https
		if auth.RequestIsHTTPS(c.Request) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

func CORSMiddleware() gin.HandlerFunc {
	allowed := allowedOrigins()
	allowAll := len(allowed) == 1 && allowed[0] == "*"

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		// 按白名单回显 Origin（不再用 "*" 搭配 credentials 的不安全组合）
		if allowAll {
			c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		} else if origin != "" {
			for _, o := range allowed {
				if o == origin {
					c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
					c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
					c.Writer.Header().Add("Vary", "Origin")
					break
				}
			}
		}

		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// 鉴权失败统一带的错误码（审计 B-5）：前端只凭这几个码（或对应的固定文案）判断本站登录失效，
// 其他 401/403 当业务结果原样显示，不踢人。改这里要同步 frontend/src/lib/session.ts。
const (
	authErrUnauthorized  = "unauthorized"
	authErrCSRF          = "csrf_failed"
	authErrAdminRequired = "admin_required"
)

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func JWTAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 优先 Authorization: Bearer（非浏览器/兼容旧前端）；否则读 HttpOnly cookie
		var tokenStr string
		fromCookie := false
		if authHeader := c.GetHeader("Authorization"); authHeader != "" {
			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || parts[0] != "Bearer" {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header", "error_code": authErrUnauthorized})
				c.Abort()
				return
			}
			tokenStr = parts[1]
		} else if ck, err := c.Cookie("auth_token"); err == nil && ck != "" {
			tokenStr = ck
			fromCookie = true
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Missing authorization", "error_code": authErrUnauthorized})
			c.Abort()
			return
		}

		claims := &auth.CustomClaims{}
		token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return []byte(auth.JWTSecret()), nil
		})
		if err != nil || !token.Valid {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token", "error_code": authErrUnauthorized})
			c.Abort()
			return
		}

		// 每个请求都查库核对令牌版本号和账号状态（审计 A-2）：改密码后旧令牌立刻作废，禁用、删除账号立刻生效。
		// SQLite 本地主键查询，代价可以接受。
		version, active, err := db.AdminTokenState(claims.UserID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (!active || version != claims.TokenVersion)) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token", "error_code": authErrUnauthorized})
			c.Abort()
			return
		}
		if err != nil {
			log.Printf("鉴权查询管理员状态失败: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "系统错误"})
			c.Abort()
			return
		}

		// 基于 cookie 认证的写操作必须通过 CSRF 双提交校验（Bearer 头不受 CSRF 影响，跳过）
		if fromCookie && isUnsafeMethod(c.Request.Method) {
			csrfHeader := c.GetHeader("X-CSRF-Token")
			csrfCookie, _ := c.Cookie("csrf_token")
			if csrfHeader == "" || csrfCookie == "" ||
				subtle.ConstantTimeCompare([]byte(csrfHeader), []byte(csrfCookie)) != 1 {
				c.JSON(http.StatusForbidden, gin.H{"error": "CSRF 校验失败", "error_code": authErrCSRF})
				c.Abort()
				return
			}
		}

		c.Set("user_id", claims.UserID)
		c.Set("is_admin", claims.IsAdmin)
		c.Set("username", claims.Username)
		if claims.ExpiresAt != nil {
			c.Set("token_expires_at", claims.ExpiresAt.Time.Format(time.RFC3339))
		}
		c.Next()
	}
}

func AdminAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		isAdmin, ok := c.Get("is_admin")
		if !ok || isAdmin != true {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin access required", "error_code": authErrAdminRequired})
			c.Abort()
			return
		}
		c.Next()
	}
}
