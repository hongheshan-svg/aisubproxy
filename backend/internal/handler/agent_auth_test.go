package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/tuzi/cdk-recharge-system/internal/agent"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

const agentBase = "/api/v1/agent"

func jsonField(t *testing.T, body, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("响应不是 JSON: %s", body)
	}
	s, _ := m[key].(string)
	return s
}

func TestAgentLoginFullFlowFirstTime(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	hash, _ := bcrypt.GenerateFromPassword([]byte("InitPass-12345"), bcrypt.MinCost)
	id, err := db.CreateAgent(context.Background(), db.NewAgent{Username: "newbie", DisplayName: "新人", PasswordHash: string(hash), TierID: tier}, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	_ = id
	w := e.send("POST", agentBase+"/auth/login", `{"username":"newbie","password":"InitPass-12345"}`, nil, nil)
	if w.Code != 200 || jsonField(t, w.Body.String(), "next") != "change_password" {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	pre := findCookie(liveCookies(w), agentPreCookie)
	w = e.send("POST", agentBase+"/auth/password/initial", `{"new_password":"BrandNew-Pass-9876"}`, liveCookiesOne(pre), nil)
	if w.Code != 200 || jsonField(t, w.Body.String(), "next") != "setup_totp" {
		t.Fatalf("initial: %d %s", w.Code, w.Body.String())
	}
	pre = findCookie(liveCookies(w), agentPreCookie)
	w = e.send("GET", agentBase+"/auth/totp/setup", "", liveCookiesOne(pre), nil)
	secretStr := jsonField(t, w.Body.String(), "secret")
	if w.Code != 200 || secretStr == "" || !strings.HasPrefix(jsonField(t, w.Body.String(), "otpauth_uri"), "otpauth://") {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	w2 := e.send("GET", agentBase+"/auth/totp/setup", "", liveCookiesOne(pre), nil)
	if jsonField(t, w2.Body.String(), "secret") != secretStr {
		t.Fatal("同一个临时凭证重复取应得到同一个密钥")
	}
	secret, err := agent.DecodeSecret(secretStr)
	if err != nil {
		t.Fatal(err)
	}
	code := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
	w = e.send("POST", agentBase+"/auth/totp/confirm", `{"code":"`+code+`"}`, liveCookiesOne(pre), nil)
	cs := liveCookies(w)
	tok := findCookie(cs, agentTokenCookie)
	if w.Code != 200 || jsonField(t, w.Body.String(), "next") != "done" || tok == nil || findCookie(cs, agentCSRFCookie) == nil {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	w = e.send("GET", agentBase+"/me", "", []*http.Cookie{tok}, nil)
	if w.Code != 200 || jsonField(t, w.Body.String(), "username") != "newbie" {
		t.Fatalf("me: %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{"password_hash", "session_version", "totp"} {
		if strings.Contains(w.Body.String(), bad) {
			t.Fatalf("/me 不应含 %s: %s", bad, w.Body.String())
		}
	}
	a, _ := db.GetAgent(context.Background(), id)
	if a.MustChangePassword || !a.TOTPBound || a.LastLoginAt == nil {
		t.Fatalf("账号状态不对: %+v", a)
	}
}

func liveCookiesOne(c *http.Cookie) []*http.Cookie {
	if c == nil {
		return nil
	}
	return []*http.Cookie{c}
}

func TestAgentLoginCookiesPathAndFlags(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	e.clock = e.clock.Add(30 * time.Second)
	w := e.send("POST", agentBase+"/auth/login", `{"username":"alice","password":"`+pw+`"}`, nil, nil)
	pre := findCookie(liveCookies(w), agentPreCookie)
	if pre == nil || pre.Path != "/api/v1/agent/auth" || !pre.HttpOnly || pre.SameSite != http.SameSiteStrictMode || pre.MaxAge != 300 {
		t.Fatalf("agent_pre 属性不对: %+v", pre)
	}
	code := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
	w = e.send("POST", agentBase+"/auth/totp", `{"code":"`+code+`"}`, liveCookiesOne(pre), nil)
	var tok, csrf, cleared *http.Cookie
	for _, c := range w.Result().Cookies() {
		switch c.Name {
		case agentTokenCookie:
			tok = c
		case agentCSRFCookie:
			csrf = c
		case agentPreCookie:
			cleared = c
		}
	}
	if tok == nil || tok.Path != "/api/v1/agent" || !tok.HttpOnly || tok.SameSite != http.SameSiteStrictMode || tok.MaxAge != 12*3600 {
		t.Fatalf("agent_token 属性不对: %+v", tok)
	}
	if csrf == nil || csrf.Path != "/" || csrf.HttpOnly || csrf.MaxAge != 12*3600 {
		t.Fatalf("agent_csrf 属性不对: %+v", csrf)
	}
	if cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("agent_pre 应被清掉: %+v", cleared)
	}
}

func TestAgentLoginUsernameCaseInsensitive(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	s := e.login("ALICE", pw, secret)
	w := e.agentDo(s, "GET", agentBase+"/me", "")
	if w.Code != 200 || jsonField(t, w.Body.String(), "username") != "alice" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestAgentLoginDisabledSameError(t *testing.T) {
	e := newAgentEnv(t)
	id, pw, _ := e.seedAgent("alice", e.seedTier("普通"))
	wrong := e.send("POST", agentBase+"/auth/login", `{"username":"alice","password":"wrong-password-1"}`, nil, nil)
	ghost := e.send("POST", agentBase+"/auth/login", `{"username":"nobody","password":"wrong-password-1"}`, nil, nil)
	if err := db.SetAgentStatus(context.Background(), id, db.AgentDisabled, e.clock); err != nil {
		t.Fatal(err)
	}
	dis := e.send("POST", agentBase+"/auth/login", `{"username":"alice","password":"`+pw+`"}`, nil, nil)
	if wrong.Code != 401 || dis.Code != 401 || ghost.Code != 401 {
		t.Fatalf("%d %d %d", wrong.Code, dis.Code, ghost.Code)
	}
	if wrong.Body.String() != dis.Body.String() || wrong.Body.String() != ghost.Body.String() {
		t.Fatalf("响应应完全一样:\n%s\n%s\n%s", wrong.Body, dis.Body, ghost.Body)
	}
	if jsonField(t, wrong.Body.String(), "error") != "invalid_credentials" || jsonField(t, wrong.Body.String(), "message") != "登录名或密码错误" {
		t.Fatalf("%s", wrong.Body)
	}
	if len(liveCookies(dis)) != 0 {
		t.Fatal("不应下发 cookie")
	}
}

func TestAgentLoginRateLimited(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, _ := e.seedAgent("alice", e.seedTier("普通"))
	for i := 0; i < 5; i++ {
		w := e.send("POST", agentBase+"/auth/login", `{"username":"alice","password":"wrong-password-1"}`, nil, nil)
		if w.Code != 401 {
			t.Fatalf("第 %d 次: %d", i+1, w.Code)
		}
	}
	w := e.send("POST", agentBase+"/auth/login", `{"username":"alice","password":"`+pw+`"}`, nil, nil)
	if w.Code != 429 || jsonField(t, w.Body.String(), "error") != "rate_limited" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestAgentPreTokenCannotAccessMe(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, _ := e.seedAgent("alice", e.seedTier("普通"))
	w := e.send("POST", agentBase+"/auth/login", `{"username":"alice","password":"`+pw+`"}`, nil, nil)
	pre := findCookie(liveCookies(w), agentPreCookie)
	// 把临时凭证伪装成 agent_token 也不行
	fake := &http.Cookie{Name: agentTokenCookie, Value: pre.Value}
	for _, cs := range [][]*http.Cookie{{pre}, {fake}} {
		w = e.send("GET", agentBase+"/me", "", cs, nil)
		if w.Code != 401 || jsonField(t, w.Body.String(), "error") != "unauthorized" {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
}

func TestAgentPreStageEnforced(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	hash, _ := bcrypt.GenerateFromPassword([]byte("InitPass-12345"), bcrypt.MinCost)
	if _, err := db.CreateAgent(context.Background(), db.NewAgent{Username: "newbie", PasswordHash: string(hash), TierID: tier}, e.clock); err != nil {
		t.Fatal(err)
	}
	w := e.send("POST", agentBase+"/auth/login", `{"username":"newbie","password":"InitPass-12345"}`, nil, nil)
	pre := liveCookiesOne(findCookie(liveCookies(w), agentPreCookie))
	for _, c := range []struct{ m, p, b string }{
		{"POST", "/auth/totp", `{"code":"123456"}`},
		{"POST", "/auth/totp/confirm", `{"code":"123456"}`},
		{"GET", "/auth/totp/setup", ""},
	} {
		w = e.send(c.m, agentBase+c.p, c.b, pre, nil)
		if w.Code != 401 || jsonField(t, w.Body.String(), "error") != "pre_expired" {
			t.Fatalf("%s: %d %s", c.p, w.Code, w.Body)
		}
	}
}

func TestAgentPasswordInitialWeak(t *testing.T) {
	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	hash, _ := bcrypt.GenerateFromPassword([]byte("InitPass-12345"), bcrypt.MinCost)
	if _, err := db.CreateAgent(context.Background(), db.NewAgent{Username: "newbie", PasswordHash: string(hash), TierID: tier}, e.clock); err != nil {
		t.Fatal(err)
	}
	w := e.send("POST", agentBase+"/auth/login", `{"username":"newbie","password":"InitPass-12345"}`, nil, nil)
	pre := liveCookiesOne(findCookie(liveCookies(w), agentPreCookie))
	for _, pw := range []string{"short", "password123", "InitPass-12345"} {
		w = e.send("POST", agentBase+"/auth/password/initial", `{"new_password":"`+pw+`"}`, pre, nil)
		if w.Code != 400 || jsonField(t, w.Body.String(), "error") != "weak_password" || jsonField(t, w.Body.String(), "message") == "" {
			t.Fatalf("%q: %d %s", pw, w.Code, w.Body)
		}
	}
}

// startTOTPStage 走完密码，返回 totp 阶段的临时凭证。
func (e *agentEnv) startTOTPStage(user, pw string) *http.Cookie {
	e.t.Helper()
	w := e.send("POST", agentBase+"/auth/login", `{"username":"`+user+`","password":"`+pw+`"}`, nil, nil)
	pre := findCookie(liveCookies(w), agentPreCookie)
	if pre == nil || jsonField(e.t, w.Body.String(), "next") != "totp" {
		e.t.Fatalf("%d %s", w.Code, w.Body)
	}
	return pre
}

func TestAgentTOTPReplayRejected(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	e.clock = e.clock.Add(30 * time.Second)
	code := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
	pre := e.startTOTPStage("alice", pw)
	if w := e.send("POST", agentBase+"/auth/totp", `{"code":"`+code+`"}`, liveCookiesOne(pre), nil); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	pre = e.startTOTPStage("alice", pw)
	w := e.send("POST", agentBase+"/auth/totp", `{"code":"`+code+`"}`, liveCookiesOne(pre), nil)
	if w.Code != 401 || jsonField(t, w.Body.String(), "error") != "totp_invalid" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestAgentTOTPFiveFailuresKillPre(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	e.clock = e.clock.Add(30 * time.Second)
	pre := e.startTOTPStage("alice", pw)
	good := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
	bad := "000000"
	if bad == good {
		bad = "111111"
	}
	for i := 0; i < 5; i++ {
		w := e.send("POST", agentBase+"/auth/totp", `{"code":"`+bad+`"}`, liveCookiesOne(pre), nil)
		if w.Code != 401 || jsonField(t, w.Body.String(), "error") != "totp_invalid" {
			t.Fatalf("第 %d 次: %d %s", i+1, w.Code, w.Body)
		}
	}
	w := e.send("POST", agentBase+"/auth/totp", `{"code":"`+good+`"}`, liveCookiesOne(pre), nil)
	if w.Code != 401 || jsonField(t, w.Body.String(), "error") != "pre_expired" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// 重新输密码后可以继续
	pre = e.startTOTPStage("alice", pw)
	if w := e.send("POST", agentBase+"/auth/totp", `{"code":"`+good+`"}`, liveCookiesOne(pre), nil); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestAgentSessionRevokedOnDisable(t *testing.T) {
	e := newAgentEnv(t)
	id, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	s := e.login("alice", pw, secret)
	if w := e.agentDo(s, "GET", agentBase+"/me", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err := db.SetAgentStatus(context.Background(), id, db.AgentDisabled, e.clock); err != nil {
		t.Fatal(err)
	}
	if w := e.agentDo(s, "GET", agentBase+"/me", ""); w.Code != 401 {
		t.Fatalf("%d", w.Code)
	}
}

func TestAgentSessionRevokedOnPasswordChange(t *testing.T) {
	e := newAgentEnv(t)
	id, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	s := e.login("alice", pw, secret)
	if _, err := db.SetAgentPassword(context.Background(), id, "x", false, e.clock); err != nil {
		t.Fatal(err)
	}
	if w := e.agentDo(s, "GET", agentBase+"/me", ""); w.Code != 401 {
		t.Fatalf("%d", w.Code)
	}
}

func TestAgentWriteNeedsCSRF(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	s := e.login("alice", pw, secret)
	w := e.send("POST", agentBase+"/me/password", `{}`, s.cookies, nil)
	if w.Code != 403 || jsonField(t, w.Body.String(), "error") != "csrf_invalid" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w = e.send("POST", agentBase+"/me/password", `{}`, s.cookies, map[string]string{"X-CSRF-Token": "wrong"})
	if w.Code != 403 {
		t.Fatalf("%d", w.Code)
	}
}

func TestAgentBearerIgnored(t *testing.T) {
	e := newAgentEnv(t)
	id, _, _ := e.seedAgent("alice", e.seedTier("普通"))
	tok, _, err := agent.SignSession(id, 1, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	// seedAgent 里改过密码，session_version 已不是 1；取真实值保证令牌本身有效
	a, _ := db.GetAgent(context.Background(), id)
	tok, _, _ = agent.SignSession(id, a.SessionVersion, e.clock)
	w := e.send("GET", agentBase+"/me", "", nil, map[string]string{"Authorization": "Bearer " + tok})
	if w.Code != 401 {
		t.Fatalf("%d", w.Code)
	}
	w = e.send("GET", agentBase+"/me", "", []*http.Cookie{{Name: agentTokenCookie, Value: tok}}, nil)
	if w.Code != 200 {
		t.Fatalf("同一令牌走 cookie 应通过: %d", w.Code)
	}
}

func TestAgentChangePasswordInSession(t *testing.T) {
	e := newAgentEnv(t)
	id, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	s := e.login("alice", pw, secret)
	e.clock = e.clock.Add(30 * time.Second)
	code := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
	body := func(cur, nw, c string) string {
		return `{"current_password":"` + cur + `","new_password":"` + nw + `","code":"` + c + `"}`
	}
	if w := e.agentDo(s, "POST", agentBase+"/me/password", body("wrong-password-1", "Another-Pass-4321", code)); w.Code != 400 || jsonField(t, w.Body.String(), "error") != "invalid_credentials" {
		t.Fatalf("错的当前密码: %d %s", w.Code, w.Body)
	}
	if w := e.agentDo(s, "POST", agentBase+"/me/password", body(pw, "short", code)); w.Code != 400 || jsonField(t, w.Body.String(), "error") != "weak_password" {
		t.Fatalf("弱密码: %d %s", w.Code, w.Body)
	}
	if w := e.agentDo(s, "POST", agentBase+"/me/password", body(pw, pw+"x", "000000")); w.Code != 400 || jsonField(t, w.Body.String(), "error") != "totp_invalid" {
		t.Fatalf("错码: %d %s", w.Code, w.Body)
	}
	w := e.agentDo(s, "POST", agentBase+"/me/password", body(pw, "Another-Pass-4321", code))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := e.agentDo(s, "GET", agentBase+"/me", ""); w.Code != 401 {
		t.Fatalf("改完密码旧会话应失效: %d", w.Code)
	}
	_, sec, _ := db.GetAgentByUsername(context.Background(), "alice")
	if bcrypt.CompareHashAndPassword([]byte(sec.PasswordHash), []byte("Another-Pass-4321")) != nil {
		t.Fatal("密码没改")
	}
	_ = id
}

func TestAgentLogoutClearsCookies(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	s := e.login("alice", pw, secret)
	w := e.agentDo(s, "POST", agentBase+"/auth/logout", "")
	n := 0
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			n++
		}
	}
	if w.Code != 200 || n != 3 {
		t.Fatalf("%d 清了 %d 个", w.Code, n)
	}
}

func TestAgentAuthNoSecretsInLogsOrAudit(t *testing.T) {
	var buf bytes.Buffer
	oldOut, oldFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) })

	e := newAgentEnv(t)
	tier := e.seedTier("普通")
	const initPw, newPw = "InitPass-12345", "BrandNew-Pass-9876"
	hash, _ := bcrypt.GenerateFromPassword([]byte(initPw), bcrypt.MinCost)
	if _, err := db.CreateAgent(context.Background(), db.NewAgent{Username: "newbie", PasswordHash: string(hash), TierID: tier}, e.clock); err != nil {
		t.Fatal(err)
	}
	e.send("POST", agentBase+"/auth/login", `{"username":"newbie","password":"wrong-Pass-00000"}`, nil, nil)
	w := e.send("POST", agentBase+"/auth/login", `{"username":"newbie","password":"`+initPw+`"}`, nil, nil)
	pre := findCookie(liveCookies(w), agentPreCookie)
	w = e.send("POST", agentBase+"/auth/password/initial", `{"new_password":"`+newPw+`"}`, liveCookiesOne(pre), nil)
	pre = findCookie(liveCookies(w), agentPreCookie)
	w = e.send("GET", agentBase+"/auth/totp/setup", "", liveCookiesOne(pre), nil)
	secretStr := jsonField(t, w.Body.String(), "secret")
	secret, _ := agent.DecodeSecret(secretStr)
	wrong := "000000"
	if wrong == agent.TOTPCode(secret, agent.TOTPStep(e.clock)) {
		wrong = "111111"
	}
	e.send("POST", agentBase+"/auth/totp/confirm", `{"code":"`+wrong+`"}`, liveCookiesOne(pre), nil)
	code := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
	w = e.send("POST", agentBase+"/auth/totp/confirm", `{"code":"`+code+`"}`, liveCookiesOne(pre), nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	rows, err := db.DB.Query(`SELECT username, action, detail FROM admin_audit_logs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var all strings.Builder
	n := 0
	for rows.Next() {
		var u, a, d string
		rows.Scan(&u, &a, &d)
		all.WriteString(u + "|" + a + "|" + d + "\n")
		n++
	}
	if n < 3 {
		t.Fatalf("审计应有记录: %s", all.String())
	}
	hay := buf.String() + all.String()
	for _, s := range []string{initPw, newPw, "wrong-Pass-00000", secretStr, code, wrong} {
		if strings.Contains(hay, s) {
			t.Fatalf("日志或审计里出现了敏感内容 %q", s)
		}
	}
	for _, want := range []string{"agent:newbie|agent_login_failed", "agent:newbie|agent_password_change", "agent:newbie|agent_totp_bind", "agent:newbie|agent_login"} {
		if !strings.Contains(all.String(), want) {
			t.Fatalf("缺审计 %s:\n%s", want, all.String())
		}
	}
}
