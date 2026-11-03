package paybrowser_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// needChrome 在本机找不到 Chrome 时跳过测试。
func needChrome(t *testing.T) {
	t.Helper()
	if _, err := paybrowser.FindChrome(); err != nil {
		t.Skipf("未找到 Chrome，跳过：%v", err)
	}
}

// launch 启动一个测试用浏览器，测试结束时关闭。
func launch(t *testing.T, o paybrowser.LaunchOptions) *paybrowser.Browser {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	b, err := paybrowser.Launch(ctx, o)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(b.Close)
	return b
}

func TestFindChromeEnvOverride(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "my-chrome")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHROME_PATH", exe)
	got, err := paybrowser.FindChrome()
	if err != nil || got != exe {
		t.Fatalf("FindChrome() = %q, %v，期望 %q", got, err, exe)
	}

	t.Setenv("CHROME_PATH", filepath.Join(t.TempDir(), "missing-chrome"))
	if got, err := paybrowser.FindChrome(); err == nil {
		t.Fatalf("CHROME_PATH 指向不存在的文件应报错，得到 %q", got)
	}
}

func TestLaunchHidesProxyPassword(t *testing.T) {
	for _, p := range []string{
		"http://alice:topsecret@127.0.0.1:notaport",
		"ftp://alice:topsecret@127.0.0.1:21",
		"socks5://alice:topsecret@127.0.0.1:1080",
	} {
		b, err := paybrowser.Launch(context.Background(), paybrowser.LaunchOptions{ProxyURL: p})
		if err == nil {
			b.Close()
			t.Errorf("代理 %q 应报错", p)
			continue
		}
		if strings.Contains(err.Error(), "topsecret") {
			t.Errorf("错误消息泄露了代理密码: %v", err)
		}
	}
}

func TestCheckReportsVersion(t *testing.T) {
	needChrome(t)
	r := paybrowser.Check(context.Background())
	if !r.OK || r.Version == "" {
		t.Fatalf("Check() = %+v，期望 OK 且有版本号", r)
	}
	if r.Mode != "headful" && r.Mode != "headless" {
		t.Errorf("Mode = %q", r.Mode)
	}
	if r.ElapsedMs <= 0 || r.Error != "" || r.InstallHint != "" {
		t.Errorf("成功时的结果不符: %+v", r)
	}
}

// PF-4：自检打开 chatgpt.com，标题是 Cloudflare 挑战页就报 cf_blocked。这里用本机仿造页，不访问真实站点。
func TestCheckReportsCloudflareBlock(t *testing.T) {
	needChrome(t)
	cases := []struct {
		name, title string
		blocked     bool
	}{
		{"挑战页", "Just a moment...", true},
		{"拦截页", "Attention Required! | Cloudflare", true},
		{"正常页", "ChatGPT", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprintf(w, "<html><head><title>%s</title></head><body>x</body></html>", c.title)
			}))
			defer srv.Close()
			defer paybrowser.SetCFProbeURL(srv.URL)()
			r := paybrowser.Check(context.Background())
			if !r.OK || !r.CFChecked || r.CFBlocked != c.blocked {
				t.Fatalf("Check() = %+v，期望 ok、cf_checked，cf_blocked=%v", r, c.blocked)
			}
		})
	}
}

// 探测连不上时自检本身仍然成功，只是 cf_checked=false，不能把 false 当成「没被拦」。
func TestCheckCloudflareProbeFailureKeepsCheckOK(t *testing.T) {
	needChrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // 地址已无人监听
	defer paybrowser.SetCFProbeURL(url)()
	r := paybrowser.Check(context.Background())
	if !r.OK || r.CFChecked || r.CFBlocked || r.CFNote == "" {
		t.Fatalf("Check() = %+v，期望自检成功、cf_checked=false 且有说明", r)
	}
}

func TestIsCFChallengeTitle(t *testing.T) {
	for title, want := range map[string]bool{
		"Just a moment...": true, " just A MOMENT ": true, "Attention Required! | Cloudflare": true, "请稍候…": true,
		"ChatGPT": false, "": false,
	} {
		if got := paybrowser.IsCFChallengeTitle(title); got != want {
			t.Errorf("%q: got %v want %v", title, got, want)
		}
	}
}

func TestInstallHintByDeployMode(t *testing.T) {
	cases := []struct {
		env  string
		want string
	}{
		{"", paybrowser.InstallHint},
		{"systemd", paybrowser.InstallHint},
		{"docker", paybrowser.DockerInstallHint},
		{"  Docker \n", paybrowser.DockerInstallHint},
		{"DOCKER", paybrowser.DockerInstallHint},
	}
	for _, c := range cases {
		t.Run(c.env, func(t *testing.T) {
			t.Setenv("CDK_DEPLOY_MODE", c.env)
			if got := paybrowser.InstallHintFor(); got != c.want {
				t.Fatalf("installHint() = %q，期望 %q", got, c.want)
			}
		})
	}
	if !strings.Contains(paybrowser.InstallHint, "install.sh | sudo bash -s -- --browser-deps") {
		t.Errorf("InstallHint 应是可直接执行的 curl 命令: %s", paybrowser.InstallHint)
	}
	for _, want := range []string{"docker-deploy.sh", "seccomp"} {
		if !strings.Contains(paybrowser.DockerInstallHint, want) {
			t.Errorf("DockerInstallHint 缺少 %q: %s", want, paybrowser.DockerInstallHint)
		}
	}
}

func TestExplainSandboxError(t *testing.T) {
	stripAdvice := func(s string) string {
		if i := strings.Index(s, "If you want to live dangerously"); i >= 0 {
			return strings.TrimSpace(s[:i])
		}
		return s
	}
	cases := []struct {
		name   string
		in     string
		wantIn bool // 是否应加说明
	}{
		{"沙箱不可用", "chrome failed to start: No usable sandbox! If this is a Debian system, please install the chromium-sandbox package", true},
		{"Chromium 原文带劝人关沙箱的句子", "[1:1:ERROR] No usable sandbox! If this is a Debian system, please install the chromium-sandbox package to solve this problem. If you want to live dangerously and need an immediate workaround, you can try using --no-sandbox.", true},
		{"命名空间", "Failed to move to new namespace: PID namespaces supported", true},
		{"setuid", "The setuid sandbox is not running as root", true},
		{"其他错误", "chrome failed to start: exec format error", false},
		{"空", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := paybrowser.ExplainSandboxError(c.in)
			if !c.wantIn {
				if got != c.in {
					t.Fatalf("不相关的错误不应改动: %q", got)
				}
				return
			}
			if !strings.HasSuffix(got, stripAdvice(c.in)) || !strings.HasPrefix(got, "Chrome 沙箱无法启动") {
				t.Fatalf("应在原文前加中文说明并保留原文: %q", got)
			}
			for _, want := range []string{"用户命名空间", "seccomp", "max_user_namespaces", "apparmor_restrict_unprivileged_userns", "不要"} {
				if !strings.Contains(got, want) {
					t.Errorf("说明缺少 %q: %q", want, got)
				}
			}
			// no-sandbox 只能出现在「不要用」的语境里
			head := strings.TrimSuffix(got, stripAdvice(c.in))
			if strings.Contains(got[len(head):], "no-sandbox") {
				t.Errorf("不得原样带出 Chromium 劝人关沙箱的那句话: %q", got)
			}
			if i := strings.Index(head, "no-sandbox"); i >= 0 && !strings.Contains(head[:i], "不要") {
				t.Errorf("no-sandbox 前没有「不要」: %q", head)
			}
		})
	}
}

func TestCheckUsesDockerHintInDocker(t *testing.T) {
	t.Setenv("CDK_DEPLOY_MODE", "docker")
	t.Setenv("CHROME_PATH", filepath.Join(t.TempDir(), "missing-chrome"))
	r := paybrowser.Check(context.Background())
	if r.OK || r.InstallHint != paybrowser.DockerInstallHint {
		t.Fatalf("Check() = %+v，期望 Docker 提示", r)
	}
}

func TestCheckReportsInstallHintOnFailure(t *testing.T) {
	t.Setenv("CDK_DEPLOY_MODE", "")
	t.Setenv("CHROME_PATH", filepath.Join(t.TempDir(), "missing-chrome"))
	r := paybrowser.Check(context.Background())
	if r.OK || r.Error == "" || r.InstallHint != paybrowser.InstallHint {
		t.Fatalf("Check() = %+v，期望失败并带安装提示", r)
	}
}

func TestLaunchFailsCleanlyWithBrokenChrome(t *testing.T) {
	t.Setenv("CDK_DEPLOY_MODE", "")
	bad := filepath.Join(t.TempDir(), "not-chrome")
	if err := os.WriteFile(bad, []byte("not a program"), 0o644); err != nil { // 没有执行权限，进程起不来
		t.Fatal(err)
	}
	t.Setenv("CHROME_PATH", bad)
	done := make(chan paybrowser.CheckResult, 1)
	go func() { done <- paybrowser.Check(context.Background()) }()
	select {
	case r := <-done:
		if r.OK || r.Error == "" || r.InstallHint != paybrowser.InstallHint {
			t.Fatalf("Check() = %+v，期望失败并带安装提示", r)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Chrome 起不来时 Check 卡住")
	}
}

func TestLaunchHonorsCanceledContext(t *testing.T) {
	needChrome(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		b, err := paybrowser.Launch(ctx, paybrowser.LaunchOptions{})
		if err == nil {
			b.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ctx 已取消时 Launch 应报错")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("ctx 已取消时 Launch 卡住")
	}
}

func TestCloseRemovesProfileDir(t *testing.T) {
	needChrome(t)
	b := launch(t, paybrowser.LaunchOptions{})
	dir := paybrowser.ProfileDir(b)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("临时目录应存在: %v", err)
	}
	b.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Close 后临时目录仍在: %v", err)
	}
	b.Close()
}

func TestChromeIdentity(t *testing.T) {
	t.Setenv("CDK_DEPLOY_MODE", "")
	found := func(name string) (*user.User, error) {
		if name != "cdk-chrome" {
			return nil, user.UnknownUserError(name)
		}
		return &user.User{Username: name, Uid: "990", Gid: "985"}, nil
	}
	missing := func(name string) (*user.User, error) { return nil, user.UnknownUserError(name) }
	rootUID := func(name string) (*user.User, error) { return &user.User{Username: name, Uid: "0", Gid: "0"}, nil }

	cases := []struct {
		name     string
		uid      int
		lookup   func(string) (*user.User, error)
		wantCred *syscall.Credential
		wantErr  bool
	}{
		{"非 root 沿用当前用户", 501, missing, nil, false},
		{"root 且有专用用户", 0, found, &syscall.Credential{Uid: 990, Gid: 985, Groups: []uint32{}}, false},
		{"root 但专用用户不存在", 0, missing, nil, true},
		{"root 且专用用户 uid 为 0", 0, rootUID, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cred, noSandbox, err := paybrowser.ChromeIdentity(c.uid, c.lookup)
			if noSandbox {
				t.Fatal("任何情况下都不应加 --no-sandbox")
			}
			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), paybrowser.InstallHint) || !strings.Contains(err.Error(), "cdk-chrome") {
					t.Fatalf("err = %v，期望报错并含用户名与安装提示", err)
				}
				if cred != nil {
					t.Fatalf("出错时不应返回凭据: %+v", cred)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(cred, c.wantCred) {
				t.Fatalf("cred = %+v，期望 %+v", cred, c.wantCred)
			}
		})
	}
}

func TestSocketAlive(t *testing.T) {
	// macOS 的 unix socket 路径上限约 104 字节，t.TempDir() 太长，改用 /tmp 下的短目录
	dir, err := os.MkdirTemp("/tmp", "pb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	live := filepath.Join(dir, "live")
	ln, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if !paybrowser.SocketAlive(live) {
		t.Error("有进程在监听的 socket 应判为存活")
	}

	// 进程已退出但 socket 文件还在（例如 Xvfb 崩溃）
	stale := filepath.Join(dir, "stale")
	sl, err := net.ListenUnix("unix", &net.UnixAddr{Name: stale, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	sl.SetUnlinkOnClose(false)
	sl.Close()
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("残留的 socket 文件应存在: %v", err)
	}
	if paybrowser.SocketAlive(stale) {
		t.Error("没有进程监听的残留 socket 文件不应判为存活")
	}

	if paybrowser.SocketAlive(filepath.Join(dir, "missing")) {
		t.Error("不存在的路径不应判为存活")
	}
}

// R26、R28：崩溃残留的临时目录里有用户会话 cookie，要删掉；最近还有变化的目录可能是别的实例正在用的，不能动；
// 不能跟随符号链接，也不能动别的东西。
func TestCleanStaleProfiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	stale := filepath.Join(tmp, "cdk-pay-chrome-123")
	if err := os.MkdirAll(filepath.Join(stale, "Default"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "Default", "Cookies"), []byte("session"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(stale, "inner-link")); err != nil { // 目录里的链接
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "cdk-pay-chrome-link") // 名字符合的链接本身
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(tmp, "cdk-pay-chrome-file")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(tmp, "other-dir")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}

	// 把时间拨到两小时前。先改里层再改外层；符号链接的时间改不了，也不算数。
	old := time.Now().Add(-2 * time.Hour)
	age := func(paths ...string) {
		t.Helper()
		for _, p := range paths {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	age(filepath.Join(stale, "Default"), stale)

	// 刚建的目录：另一个实例刚启动的浏览器
	fresh := filepath.Join(tmp, "cdk-pay-chrome-fresh")
	if err := os.Mkdir(fresh, 0o700); err != nil {
		t.Fatal(err)
	}
	// 目录本身很久没变，但第一层内容刚写过：另一个实例正在跑的浏览器
	active := filepath.Join(tmp, "cdk-pay-chrome-active")
	if err := os.Mkdir(active, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "Local State"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	age(active)

	paybrowser.CleanStaleProfiles()

	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("残留目录应删除: %v", err)
	}
	for _, p := range []string{keep, link, plain, other, fresh, active} {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("%s 应保留: %v", p, err)
		}
	}

	// 临时目录读不出来时只打日志，不崩溃
	t.Setenv("TMPDIR", filepath.Join(tmp, "missing"))
	paybrowser.CleanStaleProfiles()
}
