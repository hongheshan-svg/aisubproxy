package auth

import (
	"os"

	"github.com/golang-jwt/jwt/v5"
)

type CustomClaims struct {
	UserID   int64  `json:"user_id"`
	IsAdmin  bool   `json:"is_admin"`
	Username string `json:"username"`
	// TokenVersion 是签发时管理员表里的令牌版本号；鉴权时与库里当前值比对，不一致即作废（改密码会让库里加一）。
	// 升级前签发的令牌没有这个字段，按 0 算。
	TokenVersion int64 `json:"tv,omitempty"`
	jwt.RegisteredClaims
}

func JWTSecret() string {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		secret = "your-secret-key-change-in-production"
	}
	return secret
}
