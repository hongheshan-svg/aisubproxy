package paybrowser_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// outcomeSlack 是 WaitOutcome 超过 timeout 之后还允许多花的时间：到点后只差收尾，不应再等页面。
const outcomeSlack = 5 * time.Second

// amountDiv 让 Open 认为支付页已经打开。
const amountDiv = `<div id="OrderDetails-TotalAmount">$20.00</div>`

func addResult(id, text string) string {
	return fmt.Sprintf(`document.body.insertAdjacentHTML('beforeend', '<div id="%s">%s</div>');`, id, text)
}

// laterScript 在页面打开一会儿之后执行 js：那时 Open 已返回，正好落在 WaitOutcome 的等待期间。
func laterScript(js string) string {
	return `<script>setTimeout(function () {` + js + `}, 1000)</script>`
}

func servePage(t *testing.T, html string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, html)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// outcomeResult 是一次 WaitOutcome 的返回值。
type outcomeResult struct {
	out paybrowser.Outcome
	msg string
	err error
}

// boundedWaitOutcome 在协程里调 WaitOutcome，超过 timeout + outcomeSlack 仍不返回就判失败：
// 这些测试要防的正是它挂住，不能让测试跟着挂到 go test 的总超时。
func boundedWaitOutcome(t *testing.T, b *paybrowser.Browser, ctx context.Context, timeout time.Duration) outcomeResult {
	t.Helper()
	done := make(chan outcomeResult, 1)
	go func() {
		out, msg, err := b.WaitOutcome(ctx, timeout)
		done <- outcomeResult{out, msg, err}
	}()
	select {
	case r := <-done:
		return r
	case <-time.After(timeout + outcomeSlack):
		t.Fatalf("WaitOutcome 超过 %v 仍未返回（timeout 只有 %v）", timeout+outcomeSlack, timeout)
		return outcomeResult{}
	}
}

// lockedLog 把标准日志收进缓冲区。这个包的测试不并行，事件回调在别的协程里写日志，所以要加锁。
type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *lockedLog {
	t.Helper()
	buf := &lockedLog{}
	old := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf
}

// R23：对话框不关掉，页面脚本就停在那里，Runtime.evaluate 不会返回。自动处理后要能继续判出页面结果。
// R30：confirm、prompt 一律取消：接受可能让页面自己再提交一次，引擎的「只点一次」管不到。
func TestWaitOutcomeAnswersDialogs(t *testing.T) {
	needChrome(t)
	const secret = "dialog-secret-text"
	success := addResult("payment-success", "Thanks")
	declined := addResult("payment-error", "Your card was declined")
	cases := []struct {
		name, kind string
		main, sub  string // sub 非空时由跨站 iframe 提供，main 里的 __SUB__ 换成它的地址
		want       paybrowser.Outcome
	}{
		{name: "主页面 alert", kind: "alert", want: paybrowser.OutcomeSuccess,
			main: laterScript(`alert("` + secret + `");` + success)},
		// alert 分不出接受还是取消；confirm 接受会走到 success（模拟页面自己再提交一次），取消才走到被拒
		{name: "confirm 要取消而不是接受", kind: "confirm", want: paybrowser.OutcomeDeclined,
			main: laterScript(`if (confirm("` + secret + `")) {` + success + `} else {` + declined + `}`)},
		{name: "prompt 要取消而不是接受", kind: "prompt", want: paybrowser.OutcomeDeclined,
			main: laterScript(`if (prompt("` + secret + `") === null) {` + declined + `} else {` + success + `}`)},
		{name: "跨站 iframe alert", kind: "alert", want: paybrowser.OutcomeDeclined,
			main: `<iframe src="__SUB__" title="Bank"></iframe>`,
			sub:  laterScript(`alert("` + secret + `");` + declined)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logs := captureLog(t)
			html := amountDiv + c.main
			if c.sub != "" {
				sub := servePage(t, c.sub)
				html = strings.ReplaceAll(html, "__SUB__", strings.Replace(sub.URL, "127.0.0.1", "localhost", 1))
			}
			srv := servePage(t, html)
			b := launch(t, paybrowser.LaunchOptions{ShotDir: t.TempDir()})
			if err := b.Open(openCtx(t), srv.URL, paybrowser.PageHosted, nil); err != nil {
				t.Fatalf("Open: %v", err)
			}
			r := boundedWaitOutcome(t, b, context.Background(), 10*time.Second)
			if r.err != nil || r.out != c.want {
				t.Fatalf("WaitOutcome = %+v，期望 %q", r, c.want)
			}
			text := logs.String()
			if !strings.Contains(text, "类型 "+c.kind) {
				t.Fatalf("日志应记对话框类型 %s: %q", c.kind, text)
			}
			if strings.Contains(text, secret) {
				t.Fatalf("日志不应含对话框文字: %q", text)
			}
		})
	}
}

// R23：页面脚本死循环时单次 CDP 调用不会返回。WaitOutcome 要自己守住 timeout，父 ctx 取消仍报取消，之后 Close 也不能挂。
func TestWaitOutcomeBusyPage(t *testing.T) {
	needChrome(t)
	started := make(chan struct{})
	var once sync.Once
	page := amountDiv + laterScript(`fetch('/started').finally(function () { while (true) {} })`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/started" {
			once.Do(func() { close(started) })
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	}))
	t.Cleanup(srv.Close)

	b := launch(t, paybrowser.LaunchOptions{ShotDir: t.TempDir()})
	dir := paybrowser.ProfileDir(b)
	if err := b.Open(openCtx(t), srv.URL, paybrowser.PageHosted, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("页面脚本没有开始执行")
	}
	time.Sleep(300 * time.Millisecond) // 等请求收尾，脚本进入死循环

	r := boundedWaitOutcome(t, b, context.Background(), 2*time.Second)
	if r.err != nil || r.out != paybrowser.OutcomeUnknown || r.msg != "" {
		t.Fatalf("内部超时应返回 unknown 且不报错: %+v", r)
	}

	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(300*time.Millisecond, cancel)
	r = boundedWaitOutcome(t, b, parent, time.Minute-outcomeSlack) // 兜底时限够长，靠取消返回
	if !errors.Is(r.err, context.Canceled) || r.out != paybrowser.OutcomeUnknown {
		t.Fatalf("父 ctx 取消应返回取消错误: %+v", r)
	}

	closed := make(chan struct{})
	go func() {
		b.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(20 * time.Second):
		t.Fatal("页面死循环时 Close 挂住")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Close 后临时目录仍在: %v", err)
	}
}
