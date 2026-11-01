package paybrowser_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestCheckReportsInstallHintOnFailure(t *testing.T) {
	t.Setenv("CHROME_PATH", filepath.Join(t.TempDir(), "missing-chrome"))
	r := paybrowser.Check(context.Background())
	if r.OK || r.Error == "" || r.InstallHint != paybrowser.InstallHint {
		t.Fatalf("Check() = %+v，期望失败并带安装提示", r)
	}
}

func TestLaunchFailsCleanlyWithBrokenChrome(t *testing.T) {
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
