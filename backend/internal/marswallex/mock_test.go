package marswallex

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 测试用密钥只生成一次（RSA 生成较慢），各测试共用。
var (
	testKeysOnce sync.Once
	testOurPriv  *rsa.PrivateKey // 我方（商户）私钥
	testPlatPriv *rsa.PrivateKey // 模拟平台私钥
)

func testKeys(t *testing.T) (ourPriv, platPriv *rsa.PrivateKey) {
	t.Helper()
	testKeysOnce.Do(func() {
		var err error
		if testOurPriv, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if testPlatPriv, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return testOurPriv, testPlatPriv
}

const (
	mockDeveloperID = "dev-123"
	mockAPIToken    = "token-abc"
)

// mockPlatform 模拟 MarsWallex：用我方公钥验请求签名，用平台私钥签响应。
// 字段不加锁：测试在两次调用之间读写 Last/Reply，靠「服务端写响应 → 客户端读到」保证先后。
// Reply 不写响应就断连时（Hijack 等），之后不要再改这个 mock 的字段，换一个新的。
type mockPlatform struct {
	t        *testing.T
	ourPub   *rsa.PublicKey  // 用来验请求签名
	platPriv *rsa.PrivateKey // 用来签响应
	cfg      Config          // 建默认 Client 的配置，测试可复制后改字段再 NewClient
	Server   *httptest.Server
	Last     *http.Request // 最近一次请求（Body 已读入 LastBody）
	LastBody []byte
	Reply    func(w http.ResponseWriter, r *http.Request) // 默认返回 200 + `{"data":{}}`
}

// newMockPlatform 起模拟服务，返回 BaseURL=Server.URL+"/portal/api" 的 Client。
func newMockPlatform(t *testing.T) (*mockPlatform, *Client) {
	t.Helper()
	ourPriv, platPriv := testKeys(t)
	m := &mockPlatform{t: t, ourPub: &ourPriv.PublicKey, platPriv: platPriv}
	m.Reply = func(w http.ResponseWriter, r *http.Request) { m.signedJSON(w, http.StatusOK, `{"data":{}}`) }
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.Server.Close)
	m.cfg = Config{
		BaseURL:           m.Server.URL + "/portal/api",
		DeveloperID:       mockDeveloperID,
		APIToken:          mockAPIToken,
		PrivateKey:        ourPriv,
		PlatformPublicKey: &platPriv.PublicKey,
	}
	c, err := NewClient(m.cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return m, c
}

func (m *mockPlatform) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		m.t.Errorf("mock: 读取请求体失败: %v", err)
	}
	m.Last, m.LastBody = r, body
	m.checkRequest(r, body)
	m.Reply(w, r)
}

// checkRequest 校验 Authorization 与请求签名，失败直接 t.Errorf。
func (m *mockPlatform) checkRequest(r *http.Request, body []byte) {
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(mockDeveloperID+":"+mockAPIToken))
	if got := r.Header.Get("Authorization"); got != wantAuth {
		m.t.Errorf("mock: Authorization = %q, want %q", got, wantAuth)
	}
	epoch, sig, err := ParseSignatureHeader(r.Header.Get("F-Signature"))
	if err != nil {
		m.t.Errorf("mock: F-Signature 无效: %v", err)
		return
	}
	if _, err := EpochTime(epoch); err != nil || len(epoch) >= 13 {
		m.t.Errorf("mock: 请求签名时间戳应为 Unix 秒，got %q", epoch)
	}
	// 文件上传（multipart）的请求体不参与签名，按空处理。
	signed := body
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		signed = nil
	}
	// 签名 URI 用线上实际的转义路径。
	payload := SigningPayload(r.Method, r.URL.EscapedPath(), epoch, signed, r.URL.RawQuery)
	if err := Verify(m.ourPub, payload, sig); err != nil {
		m.t.Errorf("mock: 请求签名验证失败: %v; payload=%q", err, payload)
	}
}

// signedJSON 用秒级 epoch 签名并写出响应。
func (m *mockPlatform) signedJSON(w http.ResponseWriter, status int, body string) {
	m.writeSigned(w, status, strconv.FormatInt(time.Now().Unix(), 10), body)
}

// signedJSONMillis 用毫秒级 epoch 签名并写出响应。
func (m *mockPlatform) signedJSONMillis(w http.ResponseWriter, status int, body string) {
	m.writeSigned(w, status, strconv.FormatInt(time.Now().UnixMilli(), 10), body)
}

func (m *mockPlatform) writeSigned(w http.ResponseWriter, status int, epoch, body string) {
	sig, err := Sign(m.platPriv, epoch+"&"+body)
	if err != nil {
		m.t.Errorf("mock: 响应签名失败: %v", err)
	}
	w.Header().Set("F-Signature", FormatSignatureHeader(epoch, sig))
	plainJSON(w, status, body)
}

// plainJSON 写出不带 F-Signature 的响应。
func plainJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}
