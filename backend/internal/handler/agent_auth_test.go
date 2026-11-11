package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
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
	// 重新输密码拿到新凭证，但账号级动态码限流已锁，正确的码也要等 15 分钟
	pre = e.startTOTPStage("alice", pw)
	if w := e.send("POST", agentBase+"/auth/totp", `{"code":"`+good+`"}`, liveCookiesOne(pre), nil); w.Code != 429 {
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
	// 用库里真实的 session_version 签，保证令牌本身有效
	a, _ := db.GetAgent(context.Background(), id)
	tok, _, err := agent.SignSession(id, a.SessionVersion, e.clock)
	if err != nil {
		t.Fatal(err)
	}
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
		if err := rows.Scan(&u, &a, &d); err != nil {
			t.Fatal(err)
		}
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

// wrongCode 返回一个和 good 不同的 6 位码。
func wrongCode(good string) string {
	if good == "000000" {
		return "111111"
	}
	return "000000"
}

// 同一张临时凭证并发发 10 个错码，只有 5 个能走到校验，其余被拒。
func TestAgentTOTPConcurrentWrongCodes(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	e.clock = e.clock.Add(30 * time.Second)
	pre := e.startTOTPStage("alice", pw)
	bad := wrongCode(agent.TOTPCode(secret, agent.TOTPStep(e.clock)))
	var wg sync.WaitGroup
	bodies := make([]string, 10)
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bodies[i] = e.send("POST", agentBase+"/auth/totp", `{"code":"`+bad+`"}`, liveCookiesOne(pre), nil).Body.String()
		}()
	}
	wg.Wait()
	counts := map[string]int{}
	for _, b := range bodies {
		counts[jsonField(t, b, "error")]++
	}
	if counts["totp_invalid"] != 5 {
		t.Fatalf("应恰有 5 个走到校验: %v", counts)
	}
	if counts["totp_invalid"]+counts["pre_expired"]+counts["rate_limited"] != 10 {
		t.Fatalf("其余应被拒: %v", counts)
	}
}

// 两张不同的临时凭证轮流输错，账号级累计 5 次后，正确的码也返回 429。
func TestAgentTOTPAccountLevelLimit(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	e.clock = e.clock.Add(30 * time.Second)
	good := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
	bad := wrongCode(good)
	a, b := e.startTOTPStage("alice", pw), e.startTOTPStage("alice", pw)
	for i := 0; i < 5; i++ {
		pre := a
		if i%2 == 1 {
			pre = b
		}
		w := e.send("POST", agentBase+"/auth/totp", `{"code":"`+bad+`"}`, liveCookiesOne(pre), nil)
		if w.Code != 401 || jsonField(t, w.Body.String(), "error") != "totp_invalid" {
			t.Fatalf("第 %d 次: %d %s", i+1, w.Code, w.Body)
		}
	}
	w := e.send("POST", agentBase+"/auth/totp", `{"code":"`+good+`"}`, liveCookiesOne(b), nil)
	if w.Code != 429 || jsonField(t, w.Body.String(), "error") != "rate_limited" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// 库里的密文坏了：500 internal，不计入动态码失败次数。
func TestAgentTOTPCorruptSealedNotCounted(t *testing.T) {
	e := newAgentEnv(t)
	id, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	e.clock = e.clock.Add(30 * time.Second)
	pre := e.startTOTPStage("alice", pw)
	if _, err := db.DB.Exec(`UPDATE agents SET totp_sealed = 'garbage' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	code := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
	for i := 0; i < 7; i++ {
		w := e.send("POST", agentBase+"/auth/totp", `{"code":"`+code+`"}`, liveCookiesOne(pre), nil)
		if w.Code != 500 || jsonField(t, w.Body.String(), "error") != "internal" {
			t.Fatalf("第 %d 次: %d %s", i+1, w.Code, w.Body)
		}
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs WHERE action = 'agent_login_failed'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("不应记动态码失败: n=%d err=%v", n, err)
	}
}

// UseAgentTOTPStep 返回 false 的分支：两张凭证并发提交同一个正确码，只有一个成功。
func TestAgentTOTPConcurrentSameCode(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	e.clock = e.clock.Add(30 * time.Second)
	code := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
	a, b := e.startTOTPStage("alice", pw), e.startTOTPStage("alice", pw)
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, pre := range []*http.Cookie{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.send("POST", agentBase+"/auth/totp", `{"code":"`+code+`"}`, liveCookiesOne(pre), nil).Code
		}()
	}
	wg.Wait()
	if oneWon := (codes[0] == 200 && codes[1] == 401) || (codes[0] == 401 && codes[1] == 200); !oneWon {
		t.Fatalf("应恰有一个 200: %v", codes)
	}
}

func TestAgentMasterKeyMissingMaintenance(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	e.clock = e.clock.Add(30 * time.Second)
	pre := e.startTOTPStage("alice", pw)
	t.Setenv("APP_MASTER_KEY", "")
	code := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
	w := e.send("POST", agentBase+"/auth/totp", `{"code":"`+code+`"}`, liveCookiesOne(pre), nil)
	if w.Code != 503 || jsonField(t, w.Body.String(), "error") != "maintenance" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// setupStagePre 建一个改过密码、还没绑定两步验证的账号，登录后返回 setup_totp 阶段的凭证。
func (e *agentEnv) setupStagePre(user string) (int64, *http.Cookie) {
	e.t.Helper()
	const pw = "Some-Good-Pass-77"
	hash, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	ctx := context.Background()
	id, err := db.CreateAgent(ctx, db.NewAgent{Username: user, PasswordHash: string(hash), TierID: e.seedTier("T-" + user)}, e.clock)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := db.SetAgentPassword(ctx, id, string(hash), false, e.clock); err != nil {
		e.t.Fatal(err)
	}
	w := e.send("POST", agentBase+"/auth/login", `{"username":"`+user+`","password":"`+pw+`"}`, nil, nil)
	pre := findCookie(liveCookies(w), agentPreCookie)
	if pre == nil || jsonField(e.t, w.Body.String(), "next") != "setup_totp" {
		e.t.Fatalf("%d %s", w.Code, w.Body)
	}
	return id, pre
}

func TestAgentSetupAfterBoundConflict(t *testing.T) {
	e := newAgentEnv(t)
	id, pre := e.setupStagePre("bob")
	w := e.send("GET", agentBase+"/auth/totp/setup", "", liveCookiesOne(pre), nil)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	if err := db.BindAgentTOTP(context.Background(), id, "sealed-by-someone-else", 1, e.clock); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ m, p, b string }{{"GET", "/auth/totp/setup", ""}, {"POST", "/auth/totp/confirm", `{"code":"123456"}`}} {
		w = e.send(c.m, agentBase+c.p, c.b, liveCookiesOne(pre), nil)
		if w.Code != 409 || jsonField(t, w.Body.String(), "error") != "totp_bound" {
			t.Fatalf("%s: %d %s", c.p, w.Code, w.Body)
		}
	}
}

func TestAgentOldPreInvalidAfterInitialPassword(t *testing.T) {
	e := newAgentEnv(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("InitPass-12345"), bcrypt.MinCost)
	if _, err := db.CreateAgent(context.Background(), db.NewAgent{Username: "newbie", PasswordHash: string(hash), TierID: e.seedTier("普通")}, e.clock); err != nil {
		t.Fatal(err)
	}
	w := e.send("POST", agentBase+"/auth/login", `{"username":"newbie","password":"InitPass-12345"}`, nil, nil)
	old := liveCookiesOne(findCookie(liveCookies(w), agentPreCookie))
	if w = e.send("POST", agentBase+"/auth/password/initial", `{"new_password":"BrandNew-Pass-9876"}`, old, nil); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, c := range []struct{ m, p, b string }{
		{"POST", "/auth/password/initial", `{"new_password":"Another-Pass-4321"}`},
		{"GET", "/auth/totp/setup", ""},
	} {
		w = e.send(c.m, agentBase+c.p, c.b, old, nil)
		if w.Code != 401 || jsonField(t, w.Body.String(), "error") != "pre_expired" {
			t.Fatalf("%s: %d %s", c.p, w.Code, w.Body)
		}
	}
}

func TestAgentTOTPStageCannotChangePassword(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, _ := e.seedAgent("alice", e.seedTier("普通"))
	pre := e.startTOTPStage("alice", pw)
	w := e.send("POST", agentBase+"/auth/password/initial", `{"new_password":"Another-Pass-4321"}`, liveCookiesOne(pre), nil)
	if w.Code != 401 || jsonField(t, w.Body.String(), "error") != "pre_expired" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// 不存在的账号：响应一样，但不写审计（防止密码误填进登录名后进审计、也防刷表）。
func TestAgentLoginUnknownUserNoAudit(t *testing.T) {
	e := newAgentEnv(t)
	w := e.send("POST", agentBase+"/auth/login", `{"username":"nobody","password":"Whatever-Pass-1"}`, nil, nil)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestAgentChangePasswordFailureAudited(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	s := e.login("alice", pw, secret)
	e.clock = e.clock.Add(30 * time.Second)
	body := `{"current_password":"wrong-password-1","new_password":"Another-Pass-4321","code":"000000"}`
	if w := e.agentDo(s, "POST", agentBase+"/me/password", body); w.Code != 400 {
		t.Fatalf("%d", w.Code)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM admin_audit_logs WHERE action = 'agent_password_change_failed'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

// 多张凭证、每张并发发多个错码：账号级上限是 5，走到校验的总数不能超过 5，其余 429。
func TestAgentTOTPAccountLevelConcurrent(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	e.clock = e.clock.Add(30 * time.Second)
	bad := wrongCode(agent.TOTPCode(secret, agent.TOTPStep(e.clock)))
	const tokens, per = 8, 5
	pres := make([]*http.Cookie, tokens)
	for i := range pres {
		pres[i] = e.startTOTPStage("alice", pw)
	}
	bodies := make([]string, tokens*per)
	var wg sync.WaitGroup
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bodies[i] = e.send("POST", agentBase+"/auth/totp", `{"code":"`+bad+`"}`, liveCookiesOne(pres[i/per]), nil).Body.String()
		}()
	}
	wg.Wait()
	counts := map[string]int{}
	for _, b := range bodies {
		counts[jsonField(t, b, "error")]++
	}
	if counts["totp_invalid"] != 5 || counts["rate_limited"] != tokens*per-5 {
		t.Fatalf("走到校验的应恰有 5 个，其余 429: %v", counts)
	}
}

// 库里的密文坏了（内部错误）不占账号级名额：之后修好，正确的码仍能登录。
func TestAgentTOTPInternalErrorKeepsAccountQuota(t *testing.T) {
	e := newAgentEnv(t)
	id, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	e.clock = e.clock.Add(30 * time.Second)
	good := agent.TOTPCode(secret, agent.TOTPStep(e.clock))
	var sealed string
	if err := db.DB.QueryRow(`SELECT totp_sealed FROM agents WHERE id = ?`, id).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE agents SET totp_sealed = 'garbage' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	pre := e.startTOTPStage("alice", pw)
	for i := 0; i < 6; i++ {
		if w := e.send("POST", agentBase+"/auth/totp", `{"code":"`+good+`"}`, liveCookiesOne(pre), nil); w.Code != 500 {
			t.Fatalf("第 %d 次: %d %s", i+1, w.Code, w.Body)
		}
	}
	if _, err := db.DB.Exec(`UPDATE agents SET totp_sealed = ? WHERE id = ?`, sealed, id); err != nil {
		t.Fatal(err)
	}
	if w := e.send("POST", agentBase+"/auth/totp", `{"code":"`+good+`"}`, liveCookiesOne(pre), nil); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// 读库失败不是认证失败：返回 503 busy，而不是会把用户踢回登录页的 401。
func TestAgentDBReadErrorIs503(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	s := e.login("alice", pw, secret)
	e.setupStagePre("bob")
	hash, _ := bcrypt.GenerateFromPassword([]byte("InitPass-12345"), bcrypt.MinCost)
	if _, err := db.CreateAgent(context.Background(), db.NewAgent{Username: "newbie", PasswordHash: string(hash), TierID: e.seedTier("T2")}, e.clock); err != nil {
		t.Fatal(err)
	}
	w := e.send("POST", agentBase+"/auth/login", `{"username":"newbie","password":"InitPass-12345"}`, nil, nil)
	cpre := findCookie(liveCookies(w), agentPreCookie)
	// 让只读 password_hash 等敏感字段的查询失败，其余查询不受影响
	if _, err := db.DB.Exec(`ALTER TABLE agents RENAME COLUMN totp_last_step TO totp_last_step_x`); err != nil {
		t.Skip("SQLite 不支持改列名: ", err)
	}
	w = e.send("POST", agentBase+"/auth/password/initial", `{"new_password":"BrandNew-Pass-9876"}`, liveCookiesOne(cpre), nil)
	if w.Code != 503 || jsonField(t, w.Body.String(), "error") != "busy" {
		t.Fatalf("initial: %d %s", w.Code, w.Body)
	}
	body := `{"current_password":"` + pw + `","new_password":"Another-Pass-4321","code":"000000"}`
	w = e.agentDo(s, "POST", agentBase+"/me/password", body)
	if w.Code != 503 || jsonField(t, w.Body.String(), "error") != "busy" {
		t.Fatalf("me/password: %d %s", w.Code, w.Body)
	}
}

// H1-10：退出接口需要登录；退出只清本浏览器的 cookie，不让会话作废（不踢其他设备）。
func TestAgentLogoutRequiresLoginAndKeepsSession(t *testing.T) {
	e := newAgentEnv(t)
	_, pw, secret := e.seedAgent("alice", e.seedTier("普通"))
	if w := e.send("POST", agentBase+"/auth/logout", "", nil, nil); w.Code != 401 {
		t.Fatalf("未登录退出应 401，实际 %d", w.Code)
	}
	s := e.login("alice", pw, secret)
	if w := e.agentDo(s, "POST", agentBase+"/auth/logout", ""); w.Code != 200 {
		t.Fatalf("登录后退出应 200，实际 %d", w.Code)
	}
	// 同一个令牌（代表另一台设备上的登录）仍然有效
	if w := e.agentDo(s, "GET", agentBase+"/me", ""); w.Code != 200 {
		t.Fatalf("退出不应让会话作废，/me 实际 %d", w.Code)
	}
}
