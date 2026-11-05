package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// F-2：伪造的 session 不能借 ZovoCard 预检写进绑定，也就不能把别人的邮箱在跨服务商互斥里锁住。
// 绑定只在卡台确认预检通过（2xx、envelope code 为 0）时写；卡台给出了它核验过的账号邮箱时，绑定记这个邮箱，不记会话里自填的。

const obVictimEmail = "victim@example.com"

type obPreflightUpstream struct {
	mu     sync.Mutex
	status int
	body   string
	calls  int
}

func (u *obPreflightUpstream) set(status int, body string) {
	u.mu.Lock()
	u.status, u.body = status, body
	u.mu.Unlock()
}

func obNewPreflightUpstream(t *testing.T) *obPreflightUpstream {
	t.Helper()
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	u := &obPreflightUpstream{status: 200, body: `{"code":0,"data":{"preflight_token":"pf-ob"}}`}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/cdk/preflight" {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":404}`))
			return
		}
		u.mu.Lock()
		u.calls++
		st, body := u.status, u.body
		u.mu.Unlock()
		w.WriteHeader(st)
		w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	for k, v := range map[string]string{"card_api_base": s.URL, "card_api_key": "fixture-key", provider.SettingDefault: string(provider.ZovoCard)} {
		if err := db.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

func obForgedPreflightBody(code, email, token string) string {
	sess, _ := json.Marshal(map[string]any{"user": map[string]any{"email": email}, "sessionToken": token})
	b, _ := json.Marshal(map[string]any{
		"code":       code,
		"credential": map[string]any{"mode": "session", "session": string(sess)},
	})
	return string(b)
}

func obBindingCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM cdk_session_bindings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func obVictimLocked(t *testing.T) bool {
	t.Helper()
	locked, err := db.HasRecentCDKBindingForEmail(context.Background(), obVictimEmail, time.Now().Add(-crossProviderWindow), string(provider.MarsWallex))
	if err != nil {
		t.Fatal(err)
	}
	return locked
}

// 审计里的原始攻击：任意码 + 伪造会话，卡台回 404/400（err 为 nil）。修前照样写绑定，受害者 15 分钟内兑换 MarsWallex 码被 409。
func TestObForgedPreflightRejectedUpstreamDoesNotBind(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"卡台 404 码不存在", 404, `{"code":404,"msg":"cdk not found"}`},
		{"卡台 400 会话无效", 400, `{"code":40001,"msg":"session invalid"}`},
		{"HTTP 200 但 envelope 报错", 200, `{"code":40001,"msg":"session invalid"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := obNewPreflightUpstream(t)
			u.set(tc.status, tc.body)
			w := cdkRequest(PublicCDKPreflight, "POST", "/preflight", obForgedPreflightBody("SXC-ANY-FORGED-CODE-0001", obVictimEmail, "x"))
			if w.Code != tc.status {
				t.Fatalf("应透传卡台状态 %d: %d %s", tc.status, w.Code, w.Body.String())
			}
			if n := obBindingCount(t); n != 0 {
				t.Fatalf("卡台没有确认预检通过，不应写绑定: %d 行", n)
			}
			if obVictimLocked(t) {
				t.Fatal("伪造预检不应让受害者邮箱被跨服务商互斥锁住")
			}
		})
	}
}

// 攻击者用自己真实的 sessionToken（卡台核验通过、返回攻击者的邮箱），把会话 JSON 里的 user.email 改成受害者：
// 绑定按卡台核验的邮箱记，锁的是攻击者自己，受害者不受影响。
func TestObPreflightBindsVerifiedEmailNotClaimed(t *testing.T) {
	u := obNewPreflightUpstream(t)
	u.set(200, `{"code":0,"data":{"email":"Attacker@Example.com","preflight_token":"pf-ob"}}`)
	w := cdkRequest(PublicCDKPreflight, "POST", "/preflight", obForgedPreflightBody("SXC-REAL-CODE-OF-ATTACKER-01", obVictimEmail, "real-attacker-token"))
	if w.Code != 200 {
		t.Fatalf("应透传卡台响应: %d %s", w.Code, w.Body.String())
	}
	if obVictimLocked(t) {
		t.Fatal("会话里伪造的邮箱不能让受害者被锁住")
	}
	got, err := db.GetAccountEmailByCDK("SXC-REAL-CODE-OF-ATTACKER-01")
	if err != nil || got != "Attacker@Example.com" {
		t.Fatalf("绑定应记卡台核验的邮箱: %q %v", got, err)
	}
	locked, err := db.HasRecentCDKBindingForEmail(context.Background(), "attacker@example.com", time.Now().Add(-crossProviderWindow), string(provider.MarsWallex))
	if err != nil || !locked {
		t.Fatalf("真正走了 ZovoCard 预检的账号应被跨服务商互斥锁住: %v %v", locked, err)
	}
}

// 正常用户：卡台预检通过、核验的邮箱与会话一致（大小写不敏感），照常写绑定，跨服务商互斥照常生效。
func TestObPreflightVerifiedEmailBinds(t *testing.T) {
	u := obNewPreflightUpstream(t)
	u.set(200, `{"code":0,"msg":"ok","data":{"email":"VICTIM@example.com","preflight_token":"pf-ob"}}`)
	w := cdkRequest(PublicCDKPreflight, "POST", "/preflight", obForgedPreflightBody("SXC-REAL-CODE-OF-VICTIM-001", obVictimEmail, "real-victim-token"))
	if w.Code != 200 {
		t.Fatalf("应透传卡台响应: %d %s", w.Code, w.Body.String())
	}
	if n := obBindingCount(t); n != 1 {
		t.Fatalf("预检通过应写绑定: %d 行", n)
	}
	if !obVictimLocked(t) {
		t.Fatal("本人真实的 ZovoCard 预检应让跨服务商互斥生效")
	}
}
