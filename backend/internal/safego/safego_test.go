package safego

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// captureAlerts 把告警记下来，测试结束时恢复原来的告警函数和去重记录。
func captureAlerts(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	restore := SetAlert(func(text string) {
		mu.Lock()
		got = append(got, text)
		mu.Unlock()
	})
	resetDedupe()
	t.Cleanup(func() {
		restore()
		resetDedupe()
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

const secretInPanic = "4242424242424242 cvv=739"

func panicWithSecret() { panic("卡号 " + secretInPanic) }

func TestRunRecoversAndAlertsWithoutPanicValue(t *testing.T) {
	alerts := captureAlerts(t)
	if !Run("测试任务", panicWithSecret) {
		t.Fatal("panic 应被兜住并报告 true")
	}
	a := alerts()
	if len(a) != 1 || !strings.Contains(a[0], "测试任务") {
		t.Fatalf("应告警 1 次并带任务名: %v", a)
	}
	if strings.Contains(a[0], secretInPanic) || strings.Contains(a[0], "4242") {
		t.Fatalf("告警不应带 panic 的值: %q", a[0])
	}
	// 只记类型和发生位置（文件:行）
	if !strings.Contains(a[0], "string") || !strings.Contains(a[0], "safego_test.go:") {
		t.Fatalf("告警应带 panic 的类型和位置: %q", a[0])
	}
	if Run("测试任务", func() {}) {
		t.Fatal("没 panic 时应返回 false")
	}
}

// elementAt 越界时 panic 发生在 return 这一行。
func elementAt(s []int, i int) int { return s[i] }

func TestDescribeRuntimeErrorPointsAtFaultingLine(t *testing.T) {
	var desc string
	func() {
		defer func() {
			if r := recover(); r != nil {
				desc = Describe(r)
			}
		}()
		_ = elementAt(nil, 3)
	}()
	if !strings.Contains(desc, "safego_test.go:") || !strings.Contains(desc, "runtime.") {
		t.Fatalf("应指向出错的那一行并带类型: %q", desc)
	}
}

func TestGoRecoversInGoroutine(t *testing.T) {
	alerts := captureAlerts(t)
	done := make(chan struct{})
	Go("后台协程", func() {
		defer close(done)
		panicWithSecret()
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("协程没有结束")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(alerts()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("应告警")
		}
		time.Sleep(time.Millisecond)
	}
}

// 循环协程按「每一轮」兜住：一轮 panic，下一轮照常执行，循环本身不退出。
func TestLoopContinuesAfterPanic(t *testing.T) {
	alerts := captureAlerts(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var rounds atomic.Int32
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		Loop(ctx, "测试循环", 0, time.Millisecond, func(context.Context) {
			if rounds.Add(1) <= 2 {
				panicWithSecret()
			}
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for rounds.Load() < 5 {
		if time.Now().After(deadline) {
			t.Fatalf("panic 之后循环应继续: 只跑了 %d 轮", rounds.Load())
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 结束后循环应退出")
	}
	// 同一处 panic 短时间内只告警一次，不刷屏
	if a := alerts(); len(a) != 1 {
		t.Fatalf("同样的 panic 应只告警 1 次: %v", a)
	}
}

// 告警函数自己 panic 也不能往外抛。
func TestAlertPanicDoesNotEscape(t *testing.T) {
	restore := SetAlert(func(string) { panic("告警坏了") })
	defer restore()
	resetDedupe()
	defer resetDedupe()
	if !Run("任务", panicWithSecret) {
		t.Fatal("应报告 panic")
	}
	Alert("直接告警")
}
