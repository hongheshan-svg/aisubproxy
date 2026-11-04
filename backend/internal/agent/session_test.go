package agent_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/auth"

	"github.com/golang-jwt/jwt/v5"
)

func TestSessionRoundTrip(t *testing.T) {
	now := time.Now()
	tok, exp, err := agent.SignSession(7, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(agent.SessionTTL)) {
		t.Fatalf("exp=%v", exp)
	}
	c, err := agent.ParseSession(tok, now)
	if err != nil || c.AgentID != 7 || c.SV != 3 {
		t.Fatalf("c=%+v err=%v", c, err)
	}
	pre, nonce, err := agent.SignPre(8, 4, agent.StageTOTP, now)
	if err != nil || len(nonce) != 32 {
		t.Fatalf("nonce=%q err=%v", nonce, err)
	}
	pc, err := agent.ParsePre(pre, now)
	if err != nil || pc.AgentID != 8 || pc.SV != 4 || pc.Stage != agent.StageTOTP || pc.Nonce != nonce {
		t.Fatalf("pc=%+v err=%v", pc, err)
	}
}

func TestSessionAudienceSeparated(t *testing.T) {
	now := time.Now()
	pre, _, _ := agent.SignPre(1, 1, agent.StageTOTP, now)
	if _, err := agent.ParseSession(pre, now); err == nil {
		t.Fatal("pre 令牌不应通过 ParseSession")
	}
	s, _, _ := agent.SignSession(1, 1, now)
	if _, err := agent.ParsePre(s, now); err == nil {
		t.Fatal("会话令牌不应通过 ParsePre")
	}
}

func TestSessionNotSignedWithJWTSecret(t *testing.T) {
	now := time.Now()
	c := agent.Claims{AgentID: 1, SV: 1, RegisteredClaims: jwt.RegisteredClaims{
		Audience:  jwt.ClaimStrings{agent.AudSession},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(auth.JWTSecret()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ParseSession(tok, now); err == nil {
		t.Fatal("用原始 JWT_SECRET 签的令牌必须被拒绝")
	}
}

func TestSessionExpiry(t *testing.T) {
	now := time.Now()
	tok, _, _ := agent.SignSession(1, 1, now)
	if _, err := agent.ParseSession(tok, now.Add(agent.SessionTTL-time.Second)); err != nil {
		t.Fatalf("未过期应通过: %v", err)
	}
	_, err := agent.ParseSession(tok, now.Add(agent.SessionTTL+time.Second))
	if err == nil {
		t.Fatal("过期应失败")
	}
	if strings.Contains(err.Error(), tok) {
		t.Fatal("错误信息不能带令牌原文")
	}
	pre, _, _ := agent.SignPre(1, 1, agent.StageTOTP, now)
	if _, err := agent.ParsePre(pre, now.Add(agent.PreTTL+time.Second)); err == nil {
		t.Fatal("pre 过期应失败")
	}
}

func TestParseGarbageErrorHasNoToken(t *testing.T) {
	tok := "not.a.jwt-secret-token"
	_, err := agent.ParseSession(tok, time.Now())
	if err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("err=%v", err)
	}
}
