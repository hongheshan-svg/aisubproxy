// Package paybrowser 用 chromedp 驱动真实 Chrome 打开 ChatGPT 支付页并自动填卡。
// 每个单据一个独立实例：临时用户目录、可选代理，关闭即删除。不访问数据库。
package paybrowser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/chromedp"
)

// InstallHint 是找不到或启动不了 Chrome 时给管理员的安装提示。
const InstallHint = "sudo bash /opt/cdk-recharge/install.sh --browser-deps"

// ErrNoChrome 表示本机找不到 Chrome。
var ErrNoChrome = errors.New("未找到 Chrome")

var chromeCandidates = []string{
	"/usr/bin/google-chrome-stable",
	"/usr/bin/google-chrome",
	"/usr/bin/chromium",
	"/usr/bin/chromium-browser",
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
}

const (
	startGrace   = 30 * time.Second // 调用方放弃后，最多再等启动这么久才强制中止
	closeTimeout = 5 * time.Second
	checkTimeout = 30 * time.Second
)

// FindChrome 返回 Chrome 可执行文件路径。设置了 CHROME_PATH 时只认它（文件不存在直接报错，不再回落），
// 否则依次探测常见安装位置。
func FindChrome() (string, error) {
	if p := strings.TrimSpace(os.Getenv("CHROME_PATH")); p != "" {
		if !isFile(p) {
			return "", fmt.Errorf("%w：CHROME_PATH 指向的文件不存在（%s）", ErrNoChrome, p)
		}
		return p, nil
	}
	for _, p := range chromeCandidates {
		if isFile(p) {
			return p, nil
		}
	}
	return "", ErrNoChrome
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// LaunchOptions 是启动浏览器的参数。
type LaunchOptions struct {
	// ProxyURL 是代理地址（http、https 或 socks5），可带账号密码；空表示直连。
	ProxyURL string
	// ShotDir 是截图目录。
	ShotDir string

	// proxyLoopback 仅供测试：让回环地址也走代理（Chrome 默认对回环地址直连）。
	proxyLoopback bool
}

// Browser 是一个独立的 Chrome 实例，只开一个标签页。
type Browser struct {
	ctx         context.Context // 标签页的 chromedp 上下文，存活到 Close
	cancelTab   context.CancelFunc
	cancelAlloc context.CancelFunc
	dir         string // 临时用户目录（含会话 cookie）
	mode        string
	shotDir     string
	started     bool // Chrome 已成功启动，Close 时可尝试正常关闭
	closeOnce   sync.Once

	// 以下状态由填卡、提交等后续步骤维护。
	mu        sync.Mutex
	filled    bool // 已填入卡片与地址
	submitted bool // 已点击支付；之后绝不再次点击
}

// Launch 启动一个全新的 Chrome：临时用户目录，不带自动化标记，按 o 配置代理。
// ctx 只约束启动过程，浏览器存活到 Close。
func Launch(ctx context.Context, o LaunchOptions) (*Browser, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("启动 Chrome 失败: %w", err)
	}
	proxy, auth, err := parseProxy(o.ProxyURL)
	if err != nil {
		return nil, err
	}
	bin, err := FindChrome()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "cdk-pay-chrome-")
	if err != nil {
		return nil, fmt.Errorf("创建 Chrome 临时目录失败: %w", err)
	}
	sandbox, err := sandboxOptions(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(bin),
		chromedp.UserDataDir(dir),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		// 不让 Chrome 自己的后台请求（更新检查等）占用代理
		chromedp.Flag("disable-background-networking", true),
		// 卡号框来自 js.stripe.com，与支付页跨站。关掉站点隔离，跨站 iframe 才与主页面同进程、
		// 同一个 target，能按 frame 操作（chromedp 不接管独立进程的子 target）。
		chromedp.Flag("disable-features", "site-per-process"),
		chromedp.Flag("disable-site-isolation-trials", true),
		chromedp.Flag("lang", "en-US"),
		chromedp.WindowSize(1366, 900),
	}
	opts = append(opts, sandbox...)
	if proxy != "" {
		opts = append(opts, chromedp.ProxyServer(proxy))
		if o.proxyLoopback {
			opts = append(opts, chromedp.Flag("proxy-bypass-list", "<-loopback>"))
		}
	}
	mode := modeHeadless
	if env, ok := pickDisplay(); ok {
		mode = modeHeadful
		if env != "" {
			opts = append(opts, chromedp.Env(env))
		}
	} else {
		opts = append(opts, chromedp.Flag("headless", "new"))
	}

	// abort 只用于强制中止迟迟不结束的启动；chromedp 返回的两个 cancel 各只能调一次，留给 Close。
	parent, abort := context.WithCancel(context.Background())
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(parent, opts...)
	// chromedp 的内部日志可能带 CDP 事件原文（含 cookie），一律不输出。
	quiet := func(string, ...any) {}
	tabCtx, cancelTab := chromedp.NewContext(allocCtx, chromedp.WithLogf(quiet), chromedp.WithErrorf(quiet))
	b := &Browser{
		ctx: tabCtx, cancelTab: cancelTab, cancelAlloc: func() { cancelAlloc(); abort() },
		dir: dir, mode: mode, shotDir: o.ShotDir,
	}

	// 首次 Run 才真正拉起 Chrome，不能给它挂会取消的上下文（取消会杀掉浏览器），所以另起协程等待。
	started := make(chan error, 1)
	go func() { started <- chromedp.Run(tabCtx) }()
	select {
	case err = <-started:
	case <-ctx.Done():
		go b.closeAfterStart(started, abort)
		return nil, fmt.Errorf("启动 Chrome 失败: %w", ctx.Err())
	}
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("启动 Chrome 失败: %w", err)
	}
	b.started = true

	if auth != nil {
		chromedp.ListenTarget(tabCtx, b.answerProxyAuth(auth))
		if err := b.run(ctx, fetch.Enable().WithHandleAuthRequests(true)); err != nil {
			b.Close()
			return nil, fmt.Errorf("启用代理认证失败: %w", err)
		}
	}
	return b, nil
}

// closeAfterStart 在调用方放弃等待后，等启动结束再关闭浏览器。
// 不在启动中途打断：那样 chromedp 会丢下尚未退出的 Chrome 进程，临时目录可能删不干净。
// 启动迟迟不结束时才强制中止。
func (b *Browser) closeAfterStart(started <-chan error, abort context.CancelFunc) {
	select {
	case err := <-started:
		b.started = err == nil
	case <-time.After(startGrace):
		abort()
		<-started
	}
	b.Close()
}

// Mode 返回运行模式："headful"（有界面，含 Xvfb）或 "headless"。
func (b *Browser) Mode() string { return b.mode }

// Close 结束 Chrome 并删除临时用户目录，可重复调用。
func (b *Browser) Close() {
	b.closeOnce.Do(func() {
		if b.started {
			// 先尝试正常关闭，超时或失败再由下面强制结束。
			// 没启动成功时不能调 chromedp.Cancel：它会取走 chromedp 内部的启动令牌，让 cancelTab 永久阻塞。
			ctx, cancel := context.WithTimeout(b.ctx, closeTimeout)
			_ = chromedp.Cancel(ctx)
			cancel()
		}
		b.cancelTab()
		b.cancelAlloc() // 等 Chrome 进程退出
		_ = os.RemoveAll(b.dir)
	})
}

// run 在标签页上执行动作，受 ctx 约束；ctx 取消只中止这些动作，不影响浏览器。
func (b *Browser) run(ctx context.Context, actions ...chromedp.Action) error {
	rctx, cancel := context.WithCancel(b.ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	err := chromedp.Run(rctx, actions...)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// CheckResult 是浏览器自检结果。
type CheckResult struct {
	OK          bool   `json:"ok"`
	Version     string `json:"version"`
	Mode        string `json:"mode"`
	ElapsedMs   int64  `json:"elapsed_ms"`
	Error       string `json:"error"`
	InstallHint string `json:"install_hint"`
}

// Check 启动一次浏览器并读取版本号，用于确认服务器能跑付款；最多 30 秒。
func Check(ctx context.Context) CheckResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	r := check(ctx)
	r.ElapsedMs = time.Since(start).Milliseconds()
	return r
}

func check(ctx context.Context) CheckResult {
	b, err := Launch(ctx, LaunchOptions{})
	if err != nil {
		return CheckResult{Error: err.Error(), InstallHint: InstallHint}
	}
	defer b.Close()
	var product string
	var sum int
	err = b.run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			_, product, _, _, _, err = browser.GetVersion().Do(cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser))
			return err
		}),
		chromedp.Evaluate(`1 + 1`, &sum),
	)
	if err == nil && sum != 2 {
		err = errors.New("页面脚本结果异常")
	}
	if err != nil {
		return CheckResult{Mode: b.Mode(), Error: fmt.Sprintf("Chrome 已启动但无法正常运行页面: %v", err), InstallHint: InstallHint}
	}
	return CheckResult{OK: true, Version: product, Mode: b.Mode()}
}
