package handler

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

const mwProdURL = "https://api.feibeiglobal.com/portal/api"

// 测试用密钥只生成一次。
var mwOurPriv, mwPlatPriv *rsa.PrivateKey

func mwKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	if mwOurPriv == nil {
		var err error
		if mwOurPriv, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			t.Fatal(err)
		}
		if mwPlatPriv, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			t.Fatal(err)
		}
	}
	return mwOurPriv, mwPlatPriv
}

func mwPrivPEM(t *testing.T, k *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func mwSetup(t *testing.T) *sql.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("APP_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	c, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	for _, q := range []string{
		`CREATE TABLE site_settings(key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME)`,
		`CREATE TABLE admin_audit_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT, action TEXT NOT NULL, detail TEXT, ip TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
	} {
		if _, err := c.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.MigrateMarsWallex(); err != nil {
		t.Fatal(err)
	}
	return c
}

func mwDo(h gin.HandlerFunc, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := gin.New()
	r.Handle(method, "/x", h)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func mwJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad json %q: %v", w.Body.String(), err)
	}
	return m
}

func mwPut(t *testing.T, body string) *httptest.ResponseRecorder {
	return mwDo(AdminPutMarsWallexConfig, "PUT", "/x", body, nil)
}

func mwCount(t *testing.T, c *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := c.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMarsWallexPutConfigStoresEncryptedAndMasksOnRead(t *testing.T) {
	c := mwSetup(t)
	our, plat := mwKeys(t)
	privPEM := mwPrivPEM(t, our)
	req, _ := json.Marshal(map[string]string{
		"developer_id": "dev-1", "api_token": "tok-SECRET-123", "private_key": privPEM,
		"platform_public_key": marswallex.PublicKeyPEM(&plat.PublicKey), "base_url": mwProdURL,
	})
	w := mwPut(t, string(req))
	if w.Code != 200 {
		t.Fatalf("PUT %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	m := mwJSON(t, w)
	if m["api_token_set"] != true || m["private_key_set"] != true || m["platform_public_key_set"] != true {
		t.Errorf("flags %v", m)
	}
	if strings.Contains(body, "tok-SECRET-123") || strings.Contains(body, "BEGIN PRIVATE KEY") {
		t.Errorf("secret leaked in response: %s", body)
	}
	if m["public_key_fingerprint"] != marswallex.Fingerprint(&our.PublicKey) {
		t.Errorf("fingerprint %v", m["public_key_fingerprint"])
	}
	var raw string
	if err := c.QueryRow(`SELECT value FROM site_settings WHERE key='marswallex_api_token'`).Scan(&raw); err != nil || !strings.HasPrefix(raw, "v1:") {
		t.Errorf("token not encrypted: %q %v", raw, err)
	}
	// GET 同样不泄露
	g := mwDo(AdminGetMarsWallexConfig, "GET", "/x", "", nil)
	if g.Code != 200 || strings.Contains(g.Body.String(), "tok-SECRET-123") || strings.Contains(g.Body.String(), "PRIVATE KEY") {
		t.Errorf("GET %d %s", g.Code, g.Body.String())
	}
	var detail string
	_ = c.QueryRow(`SELECT detail FROM admin_audit_logs WHERE action='update_marswallex_config'`).Scan(&detail)
	if detail == "" || strings.Contains(detail, "tok-SECRET") || strings.Contains(detail, "dev-1") {
		t.Errorf("audit detail %q", detail)
	}
}

func TestMarsWallexPutConfigEmptySecretKeepsOldValue(t *testing.T) {
	mwSetup(t)
	if w := mwPut(t, `{"api_token":"old-token","developer_id":"d1"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := mwPut(t, `{"api_token":"","developer_id":"d2"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if v, _ := db.GetSecretSetting(db.SettingMarsWallexAPIToken); v != "old-token" {
		t.Errorf("token = %q", v)
	}
	if v, _ := db.GetSetting(db.SettingMarsWallexDeveloperID); v != "d2" {
		t.Errorf("developer_id = %q", v)
	}
}

func TestMarsWallexPutConfigRejectsBadPEMAndBadURL(t *testing.T) {
	c := mwSetup(t)
	w := mwPut(t, `{"developer_id":"dx","private_key":"garbage"}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "私钥无法解析") {
		t.Errorf("bad pem: %d %s", w.Code, w.Body.String())
	}
	w = mwPut(t, `{"developer_id":"dx","base_url":"http://example.com"}`)
	if w.Code != 400 {
		t.Errorf("bad url: %d %s", w.Code, w.Body.String())
	}
	if n := mwCount(t, c, `SELECT COUNT(*) FROM site_settings`); n != 0 {
		t.Errorf("wrote %d settings despite validation failure", n)
	}
}

func TestMarsWallexPutConfigWithoutMasterKey(t *testing.T) {
	mwSetup(t)
	t.Setenv("APP_MASTER_KEY", "")
	w := mwPut(t, `{"api_token":"abc"}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "服务器未配置 APP_MASTER_KEY") {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
	g := mwJSON(t, mwDo(AdminGetMarsWallexConfig, "GET", "/x", "", nil))
	if g["master_key_ready"] != false {
		t.Errorf("master_key_ready = %v", g["master_key_ready"])
	}
}

// 模拟 MarsWallex：校验请求签名，用平台私钥签响应。
func mwPlatform(t *testing.T, reply func(w http.ResponseWriter, r *http.Request, sign func(status int, body string))) *httptest.Server {
	t.Helper()
	our, plat := mwKeys(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		epoch, sig, err := marswallex.ParseSignatureHeader(r.Header.Get("F-Signature"))
		if err != nil {
			t.Errorf("bad request signature header: %v", err)
		} else if err := marswallex.Verify(&our.PublicKey, marswallex.SigningPayload(r.Method, r.URL.EscapedPath(), epoch, body, r.URL.RawQuery), sig); err != nil {
			t.Errorf("request signature invalid: %v", err)
		}
		reply(w, r, func(status int, b string) {
			ep := strconv.FormatInt(time.Now().Unix(), 10)
			sg, _ := marswallex.Sign(plat, ep+"&"+b)
			w.Header().Set("F-Signature", marswallex.FormatSignatureHeader(ep, sg))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, b)
		})
	}))
	t.Cleanup(s.Close)
	return s
}

func mwConfigure(t *testing.T, baseURL string) {
	t.Helper()
	our, plat := mwKeys(t)
	req, _ := json.Marshal(map[string]string{
		"developer_id": "dev-123", "api_token": "token-abc", "private_key": mwPrivPEM(t, our),
		"platform_public_key": marswallex.PublicKeyPEM(&plat.PublicKey), "base_url": baseURL,
	})
	if w := mwPut(t, string(req)); w.Code != 200 {
		t.Fatalf("configure: %d %s", w.Code, w.Body.String())
	}
}

func TestMarsWallexTestConnectionSuccess(t *testing.T) {
	mwSetup(t)
	s := mwPlatform(t, func(w http.ResponseWriter, r *http.Request, sign func(int, string)) {
		if r.Method != "GET" || r.URL.Path != "/portal/api/v1/accounts/page" || r.URL.RawQuery != "page_no=1&page_size=10" {
			t.Errorf("unexpected request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		sign(200, `{"count":"1","data":[{"account_no":"2000519969952288769","account_name":"main","account_type":"MAIN","account_status":"ACTIVE","create_time":1766123535,"balance_list":[{"currency":"USD","available_balance":"89246.14","freeze_balance":"0.00"}]}]}`)
	})
	mwConfigure(t, s.URL+"/portal/api")
	w := mwDo(AdminTestMarsWallex, "POST", "/x", "", nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	m := mwJSON(t, w)
	if m["outcome"] != "success" || m["hint"] != "连接成功" {
		t.Fatalf("%v", m)
	}
	acc := m["accounts"].([]any)[0].(map[string]any)
	bal := acc["balances"].([]any)[0].(map[string]any)
	if acc["account_no"] != "2000519969952288769" || bal["available_balance"] != "89246.14" {
		t.Errorf("accounts %v", m["accounts"])
	}
	if strings.Contains(w.Body.String(), "token-abc") {
		t.Error("token leaked")
	}
}

func TestMarsWallexTestConnectionHints(t *testing.T) {
	mwSetup(t)
	mode := ""
	s := mwPlatform(t, func(w http.ResponseWriter, r *http.Request, sign func(int, string)) {
		switch mode {
		case "401":
			sign(401, `{"code":"UNAUTHORIZED","message":"bad token"}`)
		case "404":
			sign(404, `{"code":"NOT_FOUND","message":"nope"}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"count":0,"data":[]}`)
		}
	})
	// 未配置任何凭证
	w := mwDo(AdminTestMarsWallex, "POST", "/x", "", nil)
	m := mwJSON(t, w)
	if w.Code != 200 || m["outcome"] != "not_sent" || !strings.HasPrefix(m["hint"].(string), "配置不完整") {
		t.Errorf("unconfigured: %d %v", w.Code, m)
	}
	mwConfigure(t, s.URL+"/portal/api")
	cases := []struct{ mode, want string }{{"401", "认证或签名被拒"}, {"404", "/portal/api"}, {"nosig", "响应签名验证失败"}}
	for _, c := range cases {
		mode = c.mode
		w := mwDo(AdminTestMarsWallex, "POST", "/x", "", nil)
		m := mwJSON(t, w)
		if w.Code != 200 || !strings.Contains(m["hint"].(string), c.want) {
			t.Errorf("%s: %d %v", c.mode, w.Code, m)
		}
	}
}

func mwSignedWebhook(t *testing.T, body string, at time.Time) map[string]string {
	_, plat := mwKeys(t)
	ep := strconv.FormatInt(at.Unix(), 10)
	sg, err := marswallex.Sign(plat, ep+"&"+body)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"F-Signature": marswallex.FormatSignatureHeader(ep, sg)}
}

func TestMarsWallexWebhookEndpoint(t *testing.T) {
	c := mwSetup(t)
	_, plat := mwKeys(t)
	body := `{"topic":"CARD_APPLY_RESULT","message_id":"m-1","data":{"a":1}}`
	rows := func() int { return mwCount(t, c, `SELECT COUNT(*) FROM marswallex_webhook_events`) }

	// 未配置平台公钥 → 503
	if w := mwDo(MarsWallexWebhook, "POST", "/x", body, mwSignedWebhook(t, body, time.Now())); w.Code != 503 {
		t.Errorf("no key: %d", w.Code)
	}
	if err := db.SetSetting(db.SettingMarsWallexPlatformPublicKey, marswallex.PublicKeyPEM(&plat.PublicKey)); err != nil {
		t.Fatal(err)
	}
	h := mwSignedWebhook(t, body, time.Now())
	if w := mwDo(MarsWallexWebhook, "POST", "/x", body, h); w.Code != 200 || rows() != 1 {
		t.Errorf("first: %d %s rows=%d", w.Code, w.Body.String(), rows())
	}
	if w := mwDo(MarsWallexWebhook, "POST", "/x", body, h); w.Code != 200 || rows() != 1 {
		t.Errorf("dup: %d rows=%d", w.Code, rows())
	}
	body2 := `{"topic":"CARD_APPLY_RESULT","message_id":"m-2","data":{}}`
	bad := map[string]string{"F-Signature": "t=" + strconv.FormatInt(time.Now().Unix(), 10) + ",v=" + base64.StdEncoding.EncodeToString([]byte("nope"))}
	if w := mwDo(MarsWallexWebhook, "POST", "/x", body2, bad); w.Code != 401 || rows() != 1 {
		t.Errorf("bad sig: %d rows=%d", w.Code, rows())
	}
	if w := mwDo(MarsWallexWebhook, "POST", "/x", body2, mwSignedWebhook(t, body2, time.Now().Add(-10*time.Minute))); w.Code != 401 || rows() != 1 {
		t.Errorf("stale: %d rows=%d", w.Code, rows())
	}
	big := strings.Repeat("a", 1<<20+1)
	if w := mwDo(MarsWallexWebhook, "POST", "/x", big, nil); w.Code != 413 {
		t.Errorf("big: %d", w.Code)
	}
	if _, err := c.Exec(`DROP TABLE marswallex_webhook_events`); err != nil {
		t.Fatal(err)
	}
	if w := mwDo(MarsWallexWebhook, "POST", "/x", body2, mwSignedWebhook(t, body2, time.Now())); w.Code != 500 {
		t.Errorf("db error: %d", w.Code)
	}
}

func TestMarsWallexListWebhookEvents(t *testing.T) {
	mwSetup(t)
	for _, id := range []string{"a", "b"} {
		if _, err := db.InsertMarsWallexWebhookEvent(id, "T", `{}`); err != nil {
			t.Fatal(err)
		}
	}
	w := mwDo(AdminListMarsWallexWebhookEvents, "GET", "/x?limit=10", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	evs := mwJSON(t, w)["events"].([]any)
	if len(evs) != 2 {
		t.Fatalf("len %d", len(evs))
	}
	first, second := evs[0].(map[string]any), evs[1].(map[string]any)
	if first["id"].(float64) < second["id"].(float64) || first["processed_at"] != nil || first["message_id"] != "b" {
		t.Errorf("%v %v", first, second)
	}
}
