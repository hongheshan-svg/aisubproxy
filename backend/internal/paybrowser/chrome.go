// Package paybrowser 用 chromedp 驱动真实 Chrome 打开 ChatGPT 支付页并自动填卡。
// 每个单据一个独立实例：临时用户目录、可选代理，关闭即删除。不访问数据库。
package paybrowser

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// InstallHint 是 systemd 部署找不到或启动不了 Chrome 时给管理员的安装命令。
// 不指向 /opt/cdk-recharge/install.sh：已部署的服务器上它可能不存在或是旧版（在线更新只换程序、不换脚本）。
const InstallHint = "curl -fsSL https://raw.githubusercontent.com/hongheshan-svg/aisubproxy/master/deploy/install.sh | sudo bash -s -- --browser-deps"

// DockerInstallHint 是 Docker 部署的处理方法：容器里没有安装脚本可跑，浏览器依赖在镜像里。
const DockerInstallHint = "Docker 部署的浏览器依赖在镜像里：请用仓库的 docker-compose.yml 重建镜像（docker compose up -d --build），它带有 security_opt；自己 docker run 的请加 --security-opt seccomp=deploy/chrome-seccomp.json"

// installHint 按部署方式给出提示：镜像里设了 CDK_DEPLOY_MODE=docker。
func installHint() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CDK_DEPLOY_MODE")), "docker") {
		return DockerInstallHint
	}
	return InstallHint
}

// stripSandboxAdvice 去掉 Chromium 原文末尾劝人关沙箱的那句话（"If you want to live dangerously ... --no-sandbox"）。
func stripSandboxAdvice(msg string) string {
	if i := strings.Index(msg, "If you want to live dangerously"); i >= 0 {
		return strings.TrimSpace(msg[:i])
	}
	return msg
}

// explainSandboxError 识别 Chrome 沙箱起不来的原始报错，在前面加一句中文说明并保留原文。
// 原始报错（"No usable sandbox! ... install the chromium-sandbox package"）容易误导人去关沙箱，这里明确不能那样做。
func explainSandboxError(msg string) string {
	for _, key := range []string{"No usable sandbox", "Failed to move to new namespace", "setuid sandbox"} {
		if strings.Contains(msg, key) {
			return "Chrome 沙箱无法启动：服务器或容器禁止了用户命名空间。" +
				"Docker 部署请检查容器的 seccomp 配置（使用仓库的 deploy/chrome-seccomp.json）；" +
				"非 Docker 部署请检查 sysctl user.max_user_namespaces（不能为 0）与 kernel.apparmor_restrict_unprivileged_userns（Ubuntu 24.04 上应由 Chrome 安装包自带的 AppArmor 配置放行）。" +
				"不要用 --no-sandbox 绕过：那会让付款页面的脚本直接运行在服务器权限下。原始错误：" + stripSandboxAdvice(msg)
		}
	}
	return msg
}

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

	profilePrefix = "cdk-pay-chrome-" // 临时用户目录的名字前缀
	// staleProfileAge：超过这么久没有变化的用户目录才算崩溃残留。一单用浏览器的时间远短于此。
	staleProfileAge = time.Hour
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

	// 以下状态由填卡、提交等后续步骤维护，受 mu 保护。
	mu          sync.Mutex
	cardTouched bool                 // 已开始往页面键入卡号：此后页面上可能有卡号，禁止整页截图
	filled      bool                 // 已填入卡片与地址
	submitted   bool                 // 已点击支付；之后绝不再次点击
	answered    map[cdp.FrameID]bool // 已填过验证码的 3DS 框，WaitOutcome 不再把它当成新的验证
	beforeClick map[string]bool      // 点击支付前页面上已有的提示文字，WaitOutcome 不把它们当成付款结果
	report      FillReport           // 本次 Fill 的字段命中与核对结果，不含值

	worldMu sync.Mutex
	worlds  map[cdp.FrameID]runtime.ExecutionContextID // 各 frame 里本包专用的隔离执行环境
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
	dir, err := os.MkdirTemp("", profilePrefix+"*")
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
	chromedp.ListenTarget(tabCtx, b.answerDialog)

	// 让页面始终认为自己有焦点：Xvfb 下没有窗口管理器，窗口可能拿不到系统焦点，
	// 那样 document.hasFocus() 为假，键入与焦点事件都不正常。
	if err := b.run(ctx, emulation.SetFocusEmulationEnabled(true)); err != nil {
		b.Close()
		return nil, fmt.Errorf("启动 Chrome 失败: %w", err)
	}
	if auth != nil {
		chromedp.ListenTarget(tabCtx, b.answerProxyAuth(auth))
		if err := b.run(ctx, fetch.Enable().WithHandleAuthRequests(true)); err != nil {
			b.Close()
			return nil, fmt.Errorf("启用代理认证失败: %w", err)
		}
	}
	return b, nil
}

// answerDialog 自动回答页面弹出的 JS 对话框：不关掉它，页面脚本就停在那里，之后的 CDP 调用都不返回。
// alert 只有确定；beforeunload 要接受才能跳转。confirm、prompt 一律取消：接受可能让页面自己再提交一次，
// 引擎的「只点一次」管不到页面发起的请求。取消最多让这单走到确认或人工，不会多扣款。
// 对话框文字来自页面、不受控，日志只记类型。事件回调里不能同步发 CDP 命令，所以在协程中执行。
func (b *Browser) answerDialog(ev any) {
	if ev, ok := ev.(*page.EventJavascriptDialogOpening); ok {
		accept := ev.Type == page.DialogTypeAlert || ev.Type == page.DialogTypeBeforeunload
		log.Printf("[paybrowser] 自动回答页面对话框，类型 %s，接受 %v", ev.Type, accept)
		go b.runQuiet(page.HandleJavaScriptDialog(accept))
	}
}

// CleanStaleProfiles 删除系统临时目录下崩溃残留的 Chrome 用户目录（里面有用户会话 cookie）。
// 只删超过 staleProfileAge 没有变化的目录：同一台机器上别的实例可能正在用同前缀的目录，随时调用都安全。
// ReadDir 不跟随符号链接（链接的 IsDir 为 false，直接跳过），RemoveAll 也不会穿过目录里的链接。失败只打日志。
func CleanStaleProfiles() {
	tmp := os.TempDir()
	entries, err := os.ReadDir(tmp)
	if err != nil {
		log.Printf("[paybrowser] 读取临时目录失败: %v", err)
		return
	}
	cutoff := time.Now().Add(-staleProfileAge)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), profilePrefix) {
			continue
		}
		dir := filepath.Join(tmp, e.Name())
		if profileChangedSince(dir, cutoff) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("[paybrowser] 删除残留的 Chrome 临时目录 %s 失败: %v", e.Name(), err)
		}
	}
}

// profileChangedSince 报告用户目录本身或其第一层内容在 cutoff 之后是否有变化。
// 符号链接不算（改不了时间，也不代表浏览器在跑）；读不出来按有变化算，留到下一轮。
func profileChangedSince(dir string, cutoff time.Time) bool {
	info, err := os.Lstat(dir)
	if err != nil || info.ModTime().After(cutoff) {
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		fi, err := e.Info()
		if err != nil || fi.ModTime().After(cutoff) {
			return true
		}
	}
	return false
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
		return CheckResult{Error: explainSandboxError(err.Error()), InstallHint: installHint()}
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
		return CheckResult{Mode: b.Mode(), Error: explainSandboxError(fmt.Sprintf("Chrome 已启动但无法正常运行页面: %v", err)), InstallHint: installHint()}
	}
	return CheckResult{OK: true, Version: product, Mode: b.Mode()}
}
