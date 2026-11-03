package paybrowser_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// 密钥和代理变量不能进 Chrome；这些名字在下面几个测试里共用。
var chromeEnvSecrets = []string{"APP_MASTER_KEY", "JWT_SECRET", "CARD_API_KEY", "HTTP_PROXY", "https_proxy", "ALL_PROXY", "GITHUB_TOKEN"}

func TestChromeEnvKeepsOnlyWhitelist(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin:/bin",
		"APP_MASTER_KEY=x",
		"JWT_SECRET=y",
		"CARD_API_KEY=z",
		"HTTP_PROXY=http://alice:topsecret@10.0.0.1:8080",
		"https_proxy=http://alice:topsecret@10.0.0.1:8080",
		"HOME=/root",
		"TMPDIR=/var/tmp",
		"DISPLAY=:0",
		"TZ=Asia/Shanghai",
		"LANG=zh_CN.UTF-8",
		"LANGUAGE=zh_CN",
		"LC_ALL=C.UTF-8",
		"XDG_RUNTIME_DIR=/run/user/0",
		"FONTCONFIG_PATH=/etc/fonts",
		"PATH=/evil", // 重复的名字只取第一个
		"LCX=no",     // 前缀要连着下划线才算
		"=bad",
		"novalue",
	}
	got := paybrowser.ChromeEnv(environ, "/tmp/cdk-pay-chrome-1", ":99")
	home := "HOME=/root" // 非 Linux 的开发机沿用继承来的 HOME
	if paybrowser.HomeInProfile {
		home = "HOME=/tmp/cdk-pay-chrome-1"
	}
	want := []string{
		"PATH=/usr/bin:/bin",
		"TZ=Asia/Shanghai",
		"LANG=zh_CN.UTF-8",
		"LANGUAGE=zh_CN",
		"LC_ALL=C.UTF-8",
		"XDG_RUNTIME_DIR=/run/user/0",
		"FONTCONFIG_PATH=/etc/fonts",
		home,
		"TMPDIR=/tmp/cdk-pay-chrome-1",
		"DISPLAY=:99",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChromeEnv =\n%q\n期望\n%q", got, want)
	}
	// 无头模式不给 DISPLAY，即使本进程有
	for _, kv := range paybrowser.ChromeEnv(environ, "/tmp/d", "") {
		if strings.HasPrefix(kv, "DISPLAY=") {
			t.Fatalf("无头模式不应带 DISPLAY：%q", kv)
		}
	}
}

// pdeathsig 用反射读 Pdeathsig（只有 Linux 有这个字段）。
func pdeathsig(a *syscall.SysProcAttr) (syscall.Signal, bool) {
	f := reflect.ValueOf(a).Elem().FieldByName("Pdeathsig")
	if !f.IsValid() {
		return 0, false
	}
	return syscall.Signal(f.Int()), true
}

func TestWrapChromeCmdUsesEnvMinusI(t *testing.T) {
	environ := []string{"PATH=/usr/bin", "APP_MASTER_KEY=x", "JWT_SECRET=y", "LANG=C.UTF-8"}
	env := paybrowser.ChromeEnv(environ, "/tmp/cdk-pay-chrome-2", "")
	cred := &syscall.Credential{Uid: 990, Gid: 985, Groups: []uint32{}}

	cmd := exec.Command("/opt/chrome/chrome", "--user-data-dir=/tmp/cdk-pay-chrome-2", "--remote-debugging-port=0", "about:blank")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // 已有的字段要保留
	paybrowser.WrapChromeCmd(cmd, "/usr/bin/env", env, cred)

	if cmd.Err != nil {
		t.Fatalf("cmd.Err = %v", cmd.Err)
	}
	if cmd.Path != "/usr/bin/env" {
		t.Fatalf("cmd.Path = %q，期望 /usr/bin/env", cmd.Path)
	}
	if !reflect.DeepEqual(env[:2], []string{"PATH=/usr/bin", "LANG=C.UTF-8"}) || env[len(env)-1] != "TMPDIR=/tmp/cdk-pay-chrome-2" {
		t.Fatalf("白名单环境不对：%q", env)
	}
	wantArgs := append(append([]string{"env", "-i"}, env...),
		"/opt/chrome/chrome", "--user-data-dir=/tmp/cdk-pay-chrome-2", "--remote-debugging-port=0", "about:blank")
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("cmd.Args =\n%q\n期望\n%q", cmd.Args, wantArgs)
	}
	if !reflect.DeepEqual(cmd.Env, env) {
		t.Fatalf("cmd.Env = %q，期望只有白名单 %q", cmd.Env, env)
	}
	all := strings.Join(append(append([]string{}, cmd.Args...), cmd.Env...), "\n")
	for _, s := range []string{"APP_MASTER_KEY", "JWT_SECRET", "no-sandbox"} {
		if strings.Contains(all, s) {
			t.Fatalf("命令行或环境里不应出现 %s：%q %q", s, cmd.Args, cmd.Env)
		}
	}
	if cmd.SysProcAttr.Credential != cred {
		t.Fatalf("Credential 应保留为 cdk-chrome 的凭据，得到 %+v", cmd.SysProcAttr.Credential)
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Fatal("已有的 SysProcAttr 字段被清掉了")
	}
	if sig, ok := pdeathsig(cmd.SysProcAttr); ok && sig != syscall.SIGKILL {
		t.Fatalf("Pdeathsig = %v，期望 SIGKILL", sig)
	}

	// 非 root：不设 Credential
	cmd = exec.Command("/opt/chrome/chrome", "about:blank")
	paybrowser.WrapChromeCmd(cmd, "/usr/bin/env", env, nil)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential != nil {
		t.Fatalf("非 root 不应设置 Credential：%+v", cmd.SysProcAttr)
	}

	// 路径含 = 时 env 会把它当成变量赋值，必须拒绝
	cmd = exec.Command("/opt/a=b/chrome", "about:blank")
	paybrowser.WrapChromeCmd(cmd, "/usr/bin/env", env, nil)
	if cmd.Err == nil {
		t.Fatal("Chrome 路径含 = 时应报错")
	}
}

// 用假的「Chrome」脚本：它把拿到的环境变量和参数写进文件后退出。Launch 会失败，但能看到子进程实际拿到了什么。
func TestLaunchGivesChromeOnlyWhitelistedEnv(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root 下 Chrome 换成 cdk-chrome 运行，写不进测试的临时目录，跳过")
	}
	dir := t.TempDir()
	envOut := filepath.Join(dir, "env.txt")
	argsOut := filepath.Join(dir, "args.txt")
	fake := filepath.Join(dir, "fake-chrome")
	script := "#!/bin/sh\n/usr/bin/env > '" + envOut + "'\nprintf '%s\\n' \"$@\" > '" + argsOut + "'\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHROME_PATH", fake)
	t.Setenv("CDK_DEPLOY_MODE", "")
	for _, k := range chromeEnvSecrets {
		t.Setenv(k, "secret-"+strings.ToLower(k))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if b, err := paybrowser.Launch(ctx, paybrowser.LaunchOptions{}); err == nil {
		b.Close()
		t.Fatal("假 Chrome 直接退出，Launch 应报错")
	}

	envText := readText(t, envOut)
	vars := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(envText), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			vars[k] = v
		}
	}
	for _, k := range chromeEnvSecrets {
		if _, ok := vars[k]; ok {
			t.Errorf("Chrome 拿到了 %s", k)
		}
	}
	if strings.Contains(envText, "secret-") {
		t.Errorf("Chrome 的环境里出现了密钥的值：\n%s", envText)
	}
	for k := range vars {
		switch {
		case k == "HOME", k == "TMPDIR", k == "DISPLAY", k == "PATH", k == "TZ", k == "LANG", k == "LANGUAGE",
			strings.HasPrefix(k, "LC_"), strings.HasPrefix(k, "XDG_"), strings.HasPrefix(k, "FONTCONFIG_"):
		case k == "PWD", k == "SHLVL", k == "_", k == "OLDPWD":
			// 由运行脚本的 /bin/sh 自己设的
		default:
			t.Errorf("Chrome 拿到了白名单以外的变量 %s", k)
		}
	}
	profile := vars["TMPDIR"]
	if !strings.HasPrefix(filepath.Base(profile), "cdk-pay-chrome-") {
		t.Errorf("TMPDIR 应指向本次的临时用户目录，得到 %q", profile)
	}
	wantHome := os.Getenv("HOME")
	if paybrowser.HomeInProfile {
		wantHome = profile
	}
	if vars["HOME"] != wantHome {
		t.Errorf("HOME = %q，期望 %q", vars["HOME"], wantHome)
	}

	args := readText(t, argsOut)
	if strings.Contains(args, "no-sandbox") {
		t.Errorf("不应带 --no-sandbox：\n%s", args)
	}
	if !strings.Contains(args, "--user-data-dir="+profile+"\n") {
		t.Errorf("参数应原样传给 Chrome，user-data-dir 应是临时用户目录：\n%s", args)
	}
}

// 真实 Chrome：从 /proc 读本次 Chrome 各进程的环境变量和命令行，确认没有密钥。root 下 Chrome 以 cdk-chrome 运行，
// 上面的假脚本测试会跳过，这里补上。沙箱里的子进程可能读不到 environ，至少要读到浏览器主进程。
func TestRealChromeProcessesHaveNoSecrets(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("只有 Linux 能从 /proc 读进程的环境变量")
	}
	needChrome(t)
	for _, k := range chromeEnvSecrets {
		t.Setenv(k, "secret-"+strings.ToLower(k))
	}
	b := launch(t, paybrowser.LaunchOptions{})
	marker := []byte("--user-data-dir=" + paybrowser.ProfileDir(b))

	cmdlines, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	found, checked := 0, 0
	for _, p := range cmdlines {
		cmd, err := os.ReadFile(p)
		if err != nil || !bytes.Contains(cmd, marker) {
			continue
		}
		found++
		if bytes.Contains(cmd, []byte("secret-")) {
			t.Errorf("%s 的命令行里有密钥", p)
		}
		env, err := os.ReadFile(filepath.Join(filepath.Dir(p), "environ"))
		if err != nil {
			continue
		}
		checked++
		for _, kv := range bytes.Split(env, []byte{0}) {
			name, _, _ := strings.Cut(string(kv), "=")
			for _, k := range chromeEnvSecrets {
				if name == k {
					t.Errorf("Chrome 进程 %s 拿到了 %s", filepath.Dir(p), k)
				}
			}
		}
		if bytes.Contains(env, []byte("secret-")) {
			t.Errorf("Chrome 进程 %s 的环境里有密钥的值", filepath.Dir(p))
		}
	}
	switch {
	case found == 0:
		t.Fatal("没有找到本次的 Chrome 进程")
	case checked == 0 && os.Getuid() == 0:
		// Chrome 以 cdk-chrome 运行，跨用户读 environ 要 CAP_SYS_PTRACE，容器默认没有
		t.Skipf("找到 %d 个 Chrome 进程，但 root 读不到它们的环境变量（缺 CAP_SYS_PTRACE），跳过", found)
	case checked == 0:
		t.Fatal("没有读到本次 Chrome 进程的环境变量")
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("假 Chrome 没有运行（%s）: %v", path, err)
	}
	return string(b)
}
