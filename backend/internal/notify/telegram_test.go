package notify

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

const testToken = "123456789:AAH-fake_token_for_tests_only_0123456789"

// fakeTelegram 是本地假 Telegram：记录收到的请求，按 reply 依次返回。
type fakeTelegram struct {
	mu      sync.Mutex
	paths   []string
	forms   []url.Values
	replies []fakeReply
	got     chan url.Values
}

type fakeReply struct {
	status int
	body   string
}

func newFakeTelegram(t *testing.T, replies ...fakeReply) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{replies: replies, got: make(chan url.Values, 10)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.forms = append(f.forms, r.PostForm)
		rep := fakeReply{http.StatusOK, `{"ok":true,"result":{}}`}
		if len(f.replies) > 0 {
			rep, f.replies = f.replies[0], f.replies[1:]
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
		f.got <- r.PostForm
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(SetAPIBase(srv.URL))
	return f
}

func (f *fakeTelegram) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

func useSettings(t *testing.T, fn func() (string, string, error)) {
	t.Helper()
	SetSettingsSource(fn)
	t.Cleanup(func() { SetSettingsSource(nil) })
}

// 后台配置（令牌和 chat id 都有）优先；不完整或读失败时用环境变量；都没有则为 none。
func TestCurrentPrefersSettingsThenEnv(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "env-token")
	t.Setenv("TELEGRAM_CHAT_ID", "111")

	useSettings(t, func() (string, string, error) { return testToken, "222", nil })
	if c := Current(); c.Source != SourceSettings || c.ChatID != "222" || c.Token != testToken {
		t.Fatalf("后台配置完整时应优先: %+v", c)
	}
	useSettings(t, func() (string, string, error) { return testToken, "", nil })
	if c := Current(); c.Source != SourceEnv || c.ChatID != "111" {
		t.Fatalf("后台缺 chat id 时应退回环境变量: %+v", c)
	}
	useSettings(t, func() (string, string, error) { return "", "", errors.New("APP_MASTER_KEY 未配置") })
	if c := Current(); c.Source != SourceEnv {
		t.Fatalf("后台配置读不出时应退回环境变量: %+v", c)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	useSettings(t, func() (string, string, error) { return "", "", nil })
	if c := Current(); c.Source != SourceNone || c.Ready() {
		t.Fatalf("都没配时应为 none: %+v", c)
	}
}

// 按 Telegram 的接口格式发送：路径里是令牌，表单带 chat_id、text、parse_mode=HTML。
func TestSendPostsForm(t *testing.T) {
	f := newFakeTelegram(t)
	cfg := Config{Token: testToken, ChatID: "8883908283", Source: SourceSettings}
	if err := Send(context.Background(), cfg, "【付款】单据 PO1 &amp; 重试"); err != nil {
		t.Fatal(err)
	}
	if f.paths[0] != "/bot"+testToken+"/sendMessage" {
		t.Fatalf("路径: %s", f.paths[0])
	}
	form := f.forms[0]
	if form.Get("chat_id") != "8883908283" || form.Get("parse_mode") != "HTML" || form.Get("text") != "【付款】单据 PO1 &amp; 重试" {
		t.Fatalf("表单: %v", form)
	}
}

// Telegram 返回 ok=false：报 APIError，带上错误码和原文。
func TestSendReturnsAPIError(t *testing.T) {
	newFakeTelegram(t, fakeReply{http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`})
	err := Send(context.Background(), Config{Token: testToken, ChatID: "1"}, "x")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 400 || !strings.Contains(apiErr.Description, "chat not found") {
		t.Fatalf("err = %v", err)
	}
}

// 任何错误里都不能出现令牌：网络错误会带上请求 URL（URL 里有令牌），返回的原文也可能回显令牌。
func TestSendErrorsNeverContainToken(t *testing.T) {
	// 网络错误：连一个已经关掉的端口
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	restore := SetAPIBase(deadURL)
	err := Send(context.Background(), Config{Token: testToken, ChatID: "1"}, "x")
	restore()
	if err == nil || strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "fake_token") {
		t.Fatalf("网络错误泄露了令牌: %v", err)
	}

	// 返回内容回显了令牌
	newFakeTelegram(t, fakeReply{http.StatusBadGateway, "upstream error for /bot" + testToken + "/sendMessage"})
	err = Send(context.Background(), Config{Token: testToken, ChatID: "1"}, "x")
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("返回原文泄露了令牌: %v", err)
	}
}

// 异步发送失败时的日志也不含令牌。
func TestSendTextLogsWithoutToken(t *testing.T) {
	var buf bytes.Buffer
	var bufMu sync.Mutex
	log.SetOutput(writerFunc(func(p []byte) (int, error) { bufMu.Lock(); defer bufMu.Unlock(); return buf.Write(p) }))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	f := newFakeTelegram(t, fakeReply{http.StatusUnauthorized, `{"ok":false,"error_code":401,"description":"Unauthorized"}`})
	useSettings(t, func() (string, string, error) { return testToken, "1", nil })
	SendText("x")
	select {
	case <-f.got:
	case <-time.After(5 * time.Second):
		t.Fatal("没有发送")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		bufMu.Lock()
		out := buf.String()
		bufMu.Unlock()
		if strings.Contains(out, "发送失败") {
			if strings.Contains(out, testToken) {
				t.Fatalf("日志泄露了令牌: %s", out)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("没有记录失败日志: %q", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

// 被限流（429）时按 retry_after 等一次再发；要求等太久就不重试。
func TestSendRetriesOnceOnRateLimit(t *testing.T) {
	f := newFakeTelegram(t,
		fakeReply{http.StatusTooManyRequests, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`},
		fakeReply{http.StatusOK, `{"ok":true}`},
	)
	if err := Send(context.Background(), Config{Token: testToken, ChatID: "1"}, "x"); err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if f.calls() != 2 {
		t.Fatalf("应发 2 次，实际 %d", f.calls())
	}

	f2 := newFakeTelegram(t,
		fakeReply{http.StatusTooManyRequests, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":600}}`},
	)
	err := Send(context.Background(), Config{Token: testToken, ChatID: "1"}, "x")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != 429 || f2.calls() != 1 {
		t.Fatalf("要求等太久时不应重试: err=%v calls=%d", err, f2.calls())
	}
}

// 超长消息截断到上限内，且不截断 &amp; 这类实体。
func TestTruncateHTMLKeepsEntities(t *testing.T) {
	if got := truncateHTML("短消息 &amp;", 100); got != "短消息 &amp;" {
		t.Fatalf("短消息不应改动: %q", got)
	}
	long := strings.Repeat("字", 30) + "&amp;" + strings.Repeat("尾", 30)
	for max := 20; max <= 45; max++ {
		got := truncateHTML(long, max)
		if utf8.RuneCountInString(got) > max {
			t.Fatalf("max=%d 超长: %d", max, utf8.RuneCountInString(got))
		}
		body := strings.TrimSuffix(got, "…（已截断）")
		if amp := strings.LastIndexByte(body, '&'); amp >= 0 && !strings.Contains(body[amp:], ";") {
			t.Fatalf("max=%d 截断了实体: %q", max, got)
		}
	}
}

// 设了 TELEGRAM_PROXY 时经这个代理发送；格式不对时报错且不回显原值（里面可能有代理密码）。
func TestSendUsesTelegramProxy(t *testing.T) {
	var via []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 经 HTTP 代理时请求行是完整 URL
		via = append(via, r.URL.String())
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(proxy.Close)
	t.Cleanup(SetAPIBase("http://api.telegram.invalid"))
	t.Setenv(ProxyEnv, proxy.URL)
	if err := Send(context.Background(), Config{Token: testToken, ChatID: "1"}, "x"); err != nil {
		t.Fatal(err)
	}
	if len(via) != 1 || !strings.HasPrefix(via[0], "http://api.telegram.invalid/bot") {
		t.Fatalf("应经代理发送: %v", via)
	}

	t.Setenv(ProxyEnv, "ftp://user:pa55word@host:21")
	err := Send(context.Background(), Config{Token: testToken, ChatID: "1"}, "x")
	if err == nil || !strings.Contains(err.Error(), ProxyEnv) || strings.Contains(err.Error(), "pa55word") {
		t.Fatalf("代理格式不对时应报错且不回显密码: %v", err)
	}
}
