// Package mwtest 提供可编排的 MarsWallex 模拟服务，只供测试 import。
package mwtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

const (
	developerID = "dev-mwtest"
	apiToken    = "token-mwtest"
	apiPrefix   = "/portal/api"
)

// 两对密钥按包缓存（RSA 生成较慢）。
var (
	keysOnce  sync.Once
	ourPriv   *rsa.PrivateKey // 我方（商户）私钥
	platPriv  *rsa.PrivateKey // 模拟平台私钥
	keysError error
)

func keys(t testing.TB) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	keysOnce.Do(func() {
		if ourPriv, keysError = rsa.GenerateKey(rand.Reader, 2048); keysError != nil {
			return
		}
		platPriv, keysError = rsa.GenerateKey(rand.Reader, 2048)
	})
	if keysError != nil {
		t.Fatalf("mwtest: 生成密钥失败: %v", keysError)
	}
	return ourPriv, platPriv
}

// Reply 描述一次回复。
type Reply struct {
	Status   int           // 0 视为 200
	Body     string        // 原样作为响应体；空视为 `{"data":{}}`
	Unsigned bool          // 不加 F-Signature
	Hang     time.Duration // 先等待再回复
	Drop     bool          // 劫持连接后直接关闭
}

// Request 为服务端收到的一次请求。
type Request struct {
	Method, Path, RawQuery string
	Body                   []byte
}

// OK 为 200 成功回复，Body = {"data":<data>}。
func OK(data string) Reply { return Reply{Body: `{"data":` + data + `}`} }

// Rejected 为已签名的明确拒绝（status 应为 400/401/403/404）。
func Rejected(status int, code string) Reply {
	return Reply{Status: status, Body: `{"code":"` + code + `","message":"rejected"}`}
}

// Unknown 为已签名的 500，客户端归为结果未知。
func Unknown() Reply { return Reply{Status: http.StatusInternalServerError} }

// Server 为模拟平台。并发安全。
type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu    sync.Mutex
	queue map[string][]Reply
	reqs  map[string][]Request
}

// New 起模拟服务，在 t.Cleanup 时关闭。
func New(t testing.TB) *Server {
	t.Helper()
	keys(t)
	s := &Server{t: t, queue: map[string][]Reply{}, reqs: map[string][]Request{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *Server) config(baseURL string) marswallex.Config {
	return marswallex.Config{
		BaseURL:           baseURL + apiPrefix,
		DeveloperID:       developerID,
		APIToken:          apiToken,
		PrivateKey:        ourPriv,
		PlatformPublicKey: &platPriv.PublicKey,
		HTTPClient:        &http.Client{Timeout: 2 * time.Second},
	}
}

func (s *Server) newClient(baseURL string) *marswallex.Client {
	s.t.Helper()
	c, err := marswallex.NewClient(s.config(baseURL))
	if err != nil {
		s.t.Fatalf("mwtest: NewClient: %v", err)
	}
	return c
}

// Client 返回指向本服务的客户端（超时 2 秒）。
func (s *Server) Client() *marswallex.Client {
	s.t.Helper()
	return s.newClient(s.srv.URL)
}

// NotSentClient 返回指向已关闭本地端口的客户端，调用结果为「未发出」。
func (s *Server) NotSentClient() *marswallex.Client {
	s.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.t.Fatalf("mwtest: 监听失败: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return s.newClient("http://" + addr)
}

func key(method, path string) string { return strings.ToUpper(method) + " " + path }

// On 编排 method+path（不含 /portal/api）的回复。队列依次消费，只剩最后一个时重复使用。
func (s *Server) On(method, path string, replies ...Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue[key(method, path)] = append([]Reply(nil), replies...)
}

// Requests 返回 method+path 收到的全部请求。
func (s *Server) Requests(method, path string) []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs[key(method, path)]...)
}

// SignWebhook 用模拟平台私钥签 webhook 原文，返回 F-Signature 头（秒级 epoch）。
func (s *Server) SignWebhook(body []byte, at time.Time) string {
	epoch := strconv.FormatInt(at.Unix(), 10)
	sig, err := marswallex.Sign(platPriv, epoch+"&"+string(body))
	if err != nil {
		s.t.Fatalf("mwtest: webhook 签名失败: %v", err)
	}
	return marswallex.FormatSignatureHeader(epoch, sig)
}

// PlatformPublicKeyPEM 返回模拟平台公钥（用于验 webhook）。
func (s *Server) PlatformPublicKeyPEM() string { return marswallex.PublicKeyPEM(&platPriv.PublicKey) }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.t.Errorf("mwtest: 读取请求体失败: %v", err)
	}
	s.checkRequest(r, body)

	path := strings.TrimPrefix(r.URL.Path, apiPrefix)
	k := key(r.Method, path)
	s.mu.Lock()
	s.reqs[k] = append(s.reqs[k], Request{Method: r.Method, Path: path, RawQuery: r.URL.RawQuery, Body: body})
	q, ok := s.queue[k]
	var rep Reply
	if ok && len(q) > 0 {
		rep = q[0]
		if len(q) > 1 {
			s.queue[k] = q[1:]
		}
	}
	s.mu.Unlock()
	if !ok || len(q) == 0 {
		s.t.Errorf("mwtest: 未编排的请求 %s", k)
		http.Error(w, "not scripted", http.StatusNotFound)
		return
	}

	if rep.Hang > 0 {
		select {
		case <-time.After(rep.Hang):
		case <-r.Context().Done():
			return
		}
	}
	if rep.Drop {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			s.t.Errorf("mwtest: Hijack: %v", err)
			return
		}
		conn.Close()
		return
	}
	status, resp := rep.Status, rep.Body
	if status == 0 {
		status = http.StatusOK
	}
	if resp == "" {
		resp = `{"data":{}}`
	}
	if !rep.Unsigned {
		epoch := strconv.FormatInt(time.Now().Unix(), 10)
		sig, err := marswallex.Sign(platPriv, epoch+"&"+resp)
		if err != nil {
			s.t.Errorf("mwtest: 响应签名失败: %v", err)
		}
		w.Header().Set("F-Signature", marswallex.FormatSignatureHeader(epoch, sig))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, resp)
}

// checkRequest 校验 Authorization 与请求签名，失败 t.Errorf。
func (s *Server) checkRequest(r *http.Request, body []byte) {
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(developerID+":"+apiToken))
	if got := r.Header.Get("Authorization"); got != wantAuth {
		s.t.Errorf("mwtest: Authorization = %q, want %q", got, wantAuth)
	}
	epoch, sig, err := marswallex.ParseSignatureHeader(r.Header.Get("F-Signature"))
	if err != nil {
		s.t.Errorf("mwtest: F-Signature 无效: %v", err)
		return
	}
	if _, err := marswallex.EpochTime(epoch); err != nil || len(epoch) >= 13 {
		s.t.Errorf("mwtest: 请求签名时间戳应为 Unix 秒，got %q", epoch)
	}
	// 文件上传（multipart）的请求体不参与签名，按空处理。
	signed := body
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		signed = nil
	}
	payload := marswallex.SigningPayload(r.Method, r.URL.EscapedPath(), epoch, signed, r.URL.RawQuery)
	if err := marswallex.Verify(&ourPriv.PublicKey, payload, sig); err != nil {
		s.t.Errorf("mwtest: 请求签名验证失败: %v", err)
	}
}
