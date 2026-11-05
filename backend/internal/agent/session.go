package agent

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/tuzi/cdk-recharge-system/internal/auth"
)

const (
	SessionTTL = 12 * time.Hour
	PreTTL     = 5 * time.Minute

	AudSession = "agent"
	AudPre     = "agent-pre"

	StageChangePassword = "change_password"
	StageSetupTOTP      = "setup_totp"
	StageTOTP           = "totp"
)

// Claims 是代理商令牌的载荷。
type Claims struct {
	AgentID int64  `json:"aid"`
	SV      int64  `json:"sv"`
	Stage   string `json:"stg,omitempty"`
	Nonce   string `json:"n,omitempty"`
	jwt.RegisteredClaims
}

// signingKey 从 JWTSecret 派生代理商专用密钥，不直接用 JWTSecret 签名。
func signingKey() []byte {
	m := hmac.New(sha256.New, []byte(auth.JWTSecret()))
	m.Write([]byte("aisubproxy/agent-session/v1"))
	return m.Sum(nil)
}

func sign(c Claims) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(signingKey())
}

// SignSession 签发完整登录会话令牌。
func SignSession(agentID, sv int64, now time.Time) (string, time.Time, error) {
	exp := now.Add(SessionTTL)
	tok, err := sign(Claims{AgentID: agentID, SV: sv, RegisteredClaims: jwt.RegisteredClaims{
		Audience:  jwt.ClaimStrings{AudSession},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
	}})
	if err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}

// SignPre 签发登录中间阶段令牌（改密码、绑定两步验证、输入动态码）。
func SignPre(agentID, sv int64, stage string, now time.Time) (string, string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	nonce := hex.EncodeToString(b)
	tok, err := sign(Claims{AgentID: agentID, SV: sv, Stage: stage, Nonce: nonce, RegisteredClaims: jwt.RegisteredClaims{
		Audience:  jwt.ClaimStrings{AudPre},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(PreTTL)),
	}})
	if err != nil {
		return "", "", err
	}
	return tok, nonce, nil
}

func parse(token, aud string, now time.Time) (Claims, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return signingKey(), nil },
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithAudience(aud),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil {
		// 不回传库的原始错误，避免带出令牌片段。
		return Claims{}, errors.New("令牌无效或已过期")
	}
	return c, nil
}

// ParseSession 只接受 HS256、aud=agent、未过期的令牌。
func ParseSession(token string, now time.Time) (Claims, error) { return parse(token, AudSession, now) }

// ParsePre 只接受 aud=agent-pre 的令牌。
func ParsePre(token string, now time.Time) (Claims, error) { return parse(token, AudPre, now) }
