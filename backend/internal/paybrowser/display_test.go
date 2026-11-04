package paybrowser

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// fakeXvfb 替换 Xvfb 的启动、存活检测和时钟，测试结束时恢复，并清掉共享状态。
type fakeXvfb struct {
	mu     sync.Mutex
	now    time.Time
	starts int
	ok     bool // 下一次启动是否成功
	alive  bool
	alerts []string
}

func useFakeXvfb(t *testing.T) *fakeXvfb {
	t.Helper()
	f := &fakeXvfb{now: time.Unix(1_800_000_000, 0)}
	oldStart, oldAlive, oldNow := xvfbStart, xvfbAlive, xvfbNow
	xvfbStart = func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.starts++
		f.alive = f.ok
		return f.ok
	}
	xvfbAlive = func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.alive
	}
	xvfbNow = func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.now
	}
	restore := safego.SetAlert(func(text string) {
		f.mu.Lock()
		f.alerts = append(f.alerts, text)
		f.mu.Unlock()
	})
	resetXvfbState()
	t.Cleanup(func() {
		xvfbStart, xvfbAlive, xvfbNow = oldStart, oldAlive, oldNow
		restore()
		resetXvfbState()
	})
	return f
}

func (f *fakeXvfb) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func (f *fakeXvfb) set(fn func(f *fakeXvfb)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

func (f *fakeXvfb) snapshot() (starts int, alerts []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, append([]string(nil), f.alerts...)
}

// G8：Xvfb 启动失败后先用无头模式，5 分钟内不再每次等待启动；过了 5 分钟再试，不再永久退回无头模式。
// 第一次退回无头模式时告警一次；恢复之后再失败，算新的一次，再告警。
func TestXvfbRetriesAfterFailure(t *testing.T) {
	f := useFakeXvfb(t)
	if sharedXvfb() {
		t.Fatal("启动失败应退回无头模式")
	}
	starts, alerts := f.snapshot()
	if starts != 1 || len(alerts) != 1 || !strings.Contains(alerts[0], "付款浏览器改用无头模式，Cloudflare 挑战可能增多") {
		t.Fatalf("应启动 1 次并告警 1 次: starts=%d alerts=%v", starts, alerts)
	}
	f.advance(4*time.Minute + 59*time.Second)
	if sharedXvfb() {
		t.Fatal("5 分钟内应继续用无头模式")
	}
	if starts, _ := f.snapshot(); starts != 1 {
		t.Fatalf("5 分钟内不应再启动: %d", starts)
	}
	f.advance(time.Second)
	if sharedXvfb() {
		t.Fatal("再试仍失败，应继续用无头模式")
	}
	if starts, alerts := f.snapshot(); starts != 2 || len(alerts) != 1 {
		t.Fatalf("过了 5 分钟应再试一次、不重复告警: starts=%d alerts=%v", starts, alerts)
	}
	f.set(func(f *fakeXvfb) { f.ok = true })
	f.advance(5 * time.Minute)
	if !sharedXvfb() {
		t.Fatal("再试成功应改回有界面模式")
	}
	if !sharedXvfb() {
		t.Fatal("已在运行时直接用")
	}
	if starts, _ := f.snapshot(); starts != 3 {
		t.Fatalf("已在运行时不应再启动: %d", starts)
	}
	// Xvfb 后来退出、再启动又失败：新的一次退回，再告警
	f.set(func(f *fakeXvfb) { f.ok, f.alive = false, false })
	if sharedXvfb() {
		t.Fatal("启动失败应退回无头模式")
	}
	if starts, alerts := f.snapshot(); starts != 4 || len(alerts) != 2 {
		t.Fatalf("恢复后再失败应再告警: starts=%d alerts=%v", starts, alerts)
	}
}
