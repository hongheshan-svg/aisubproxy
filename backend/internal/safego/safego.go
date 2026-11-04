// Package safego 统一兜住后台协程的 panic：recover 之后记日志、发告警，不往外抛，一处 panic 不会让整个进程退出。
//
// panic 的值和堆栈里可能夹带卡号、CVV、会话、代理密码（例如被格式化进 panic 的错误文字），这里没有单据上下文，
// 做不到可靠脱敏，所以日志和告警只记 panic 的类型和发生位置（文件:行），不记 panic 的值。
package safego

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// alertQuiet：同一个任务、同一处 panic 在这段时间内只告警一次，确定性的 panic 每轮都会发生，不能刷屏。
	alertQuiet   = 10 * time.Minute
	dedupeMaxKey = 256
	chainFrames  = 8 // 日志里最多记几层调用位置
)

var (
	alertMu sync.RWMutex
	alertFn func(string) // nil 时只记日志；服务启动时接到 Telegram（见 server.New）

	dedupeMu sync.Mutex
	dedupe   = map[string]time.Time{}
)

// SetAlert 设置告警函数，返回恢复原值的函数。nil 表示只记日志。
func SetAlert(fn func(string)) (restore func()) {
	alertMu.Lock()
	old := alertFn
	alertFn = fn
	alertMu.Unlock()
	return func() {
		alertMu.Lock()
		alertFn = old
		alertMu.Unlock()
	}
}

// Alert 发一条系统告警（没有自己告警通道的后台模块用）。没设告警函数时只记日志；告警函数本身 panic 也不会往外抛。
func Alert(text string) {
	alertMu.RLock()
	fn := alertFn
	alertMu.RUnlock()
	if fn == nil {
		log.Printf("[safego] 告警: %s", text)
		return
	}
	defer RecoverNoAlert("发送告警")
	fn(text)
}

func resetDedupe() {
	dedupeMu.Lock()
	dedupe = map[string]time.Time{}
	dedupeMu.Unlock()
}

// firstInQuiet 判断 key 是不是 alertQuiet 之内第一次出现。
func firstInQuiet(key string) bool {
	now := time.Now()
	dedupeMu.Lock()
	defer dedupeMu.Unlock()
	if t, ok := dedupe[key]; ok && now.Sub(t) < alertQuiet {
		return false
	}
	if len(dedupe) >= dedupeMaxKey {
		dedupe = map[string]time.Time{}
	}
	dedupe[key] = now
	return true
}

// Describe 描述一次 panic：类型和发生位置，例如「runtime.boundsError（位于 internal/payengine/worker.go:123）」。
// 不含 panic 的值。必须在 recover 所在的 defer 调用链里使用，那时 panic 现场还在栈上；不在时位置写「位置未知」。
func Describe(r any) string {
	site, _ := panicSite()
	return fmt.Sprintf("%T（位于 %s）", r, site)
}

// panicSite 从当前栈里找出 panic 发生的位置：runtime.gopanic 之后第一个不在 runtime 包里的调用帧；
// chain 是从那里往外的几层调用位置，只有文件和行号。
func panicSite() (site string, chain []string) {
	pcs := make([]uintptr, 64)
	n := runtime.Callers(1, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	seen := false
	for {
		f, more := frames.Next()
		switch {
		case !seen:
			seen = f.Function == "runtime.gopanic"
		case !strings.HasPrefix(f.Function, "runtime."):
			chain = append(chain, shortPath(f.File)+":"+strconv.Itoa(f.Line))
		}
		if !more || len(chain) >= chainFrames {
			break
		}
	}
	if len(chain) == 0 {
		return "位置未知", nil
	}
	return chain[0], chain
}

// shortPath 去掉构建机器上的目录前缀：模块缓存里的第三方包从 pkg/mod/ 之后算，本项目从 internal/ 或 cmd/ 起算。
func shortPath(file string) string {
	file = strings.ReplaceAll(file, "\\", "/")
	if i := strings.LastIndex(file, "/pkg/mod/"); i >= 0 {
		return file[i+len("/pkg/mod/"):]
	}
	for _, mark := range []string{"/internal/", "/cmd/"} {
		if i := strings.LastIndex(file, mark); i >= 0 {
			return file[i+1:]
		}
	}
	parts := strings.Split(file, "/")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "/")
}

// Recover 必须直接用 defer 调用：defer safego.Recover("任务名", nil)。兜住 panic，记日志并告警（同一处 10 分钟内只告警一次）；
// onPanic 不为 nil 时随后调用，参数是 Describe 的结果。onPanic 自己 panic 也不会往外抛。
func Recover(name string, onPanic func(desc string)) {
	r := recover()
	if r == nil {
		return
	}
	desc := report(name, r)
	if firstInQuiet(name + "|" + desc) {
		Alert(fmt.Sprintf("后台任务「%s」出现内部错误（panic），已兜住，服务继续运行：%s", name, desc))
	}
	if onPanic != nil {
		defer RecoverNoAlert(name + "收尾")
		onPanic(desc)
	}
}

// RecoverNoAlert 同 Recover，但只记日志、不告警：给告警发送自己用，免得告警出错又触发告警。
func RecoverNoAlert(name string) {
	if r := recover(); r != nil {
		report(name, r)
	}
}

// report 记一行日志：任务名、panic 类型、位置和几层调用位置，不含 panic 的值。返回 Describe 同样格式的描述。
func report(name string, r any) string {
	site, chain := panicSite()
	desc := fmt.Sprintf("%T（位于 %s）", r, site)
	log.Printf("[safego] %s 出现内部错误（panic），已兜住：%s；调用位置 %s", name, desc, strings.Join(chain, " ← "))
	return desc
}

// Run 在当前协程执行 fn；fn panic 时兜住（同 Recover），返回 true。循环的「每一轮」、逐项处理时用。
func Run(name string, fn func()) (panicked bool) {
	defer Recover(name, func(string) { panicked = true })
	fn()
	return false
}

// Go 在新协程里执行 fn，panic 时兜住（同 Recover），协程结束，进程继续运行。
func Go(name string, fn func()) {
	go func() {
		defer Recover(name, nil)
		fn()
	}()
}

// Loop 先等 first（0 表示立即执行），之后每隔 every 执行一轮 fn，直到 ctx 结束。每一轮单独兜住 panic：
// 一轮出错，下一轮照常，循环本身不退出。在调用方的协程里运行，通常放在 Go 里。
func Loop(ctx context.Context, name string, first, every time.Duration, fn func(context.Context)) {
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if ctx.Err() != nil {
			return
		}
		Run(name, func() { fn(ctx) })
		t.Reset(every)
	}
}
