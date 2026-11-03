package paybrowser

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/chromedp/chromedp"
)

// chromeUser 是服务以 root 运行时启动 Chrome 用的低权限系统用户（由安装脚本创建）。
const chromeUser = "cdk-chrome"

// chromeIdentity 决定以哪个系统用户启动 Chrome，以及是否要加 --no-sandbox。
// 非 root：沿用当前用户。root：Chrome 不允许以 root 带沙箱运行，改用 chromeUser；
// 该用户不存在时报错并给出安装提示，绝不退回为无沙箱。任何情况下 noSandbox 都是 false。
func chromeIdentity(uid int, lookup func(name string) (*user.User, error)) (cred *syscall.Credential, noSandbox bool, err error) {
	if uid != 0 {
		return nil, false, nil
	}
	u, err := lookup(chromeUser)
	if err != nil {
		return nil, false, fmt.Errorf("服务以 root 运行，需要系统用户 %s 来启动 Chrome（保留沙箱），但找不到该用户，处理方法：%s", chromeUser, installHint())
	}
	id, err1 := strconv.ParseUint(u.Uid, 10, 32)
	gid, err2 := strconv.ParseUint(u.Gid, 10, 32)
	if err1 != nil || err2 != nil {
		return nil, false, fmt.Errorf("系统用户 %s 的 uid 或 gid 无效，处理方法：%s", chromeUser, installHint())
	}
	if id == 0 {
		return nil, false, fmt.Errorf("系统用户 %s 的 uid 不能为 0，处理方法：%s", chromeUser, installHint())
	}
	// Groups 为空：去掉 root 的附加组
	return &syscall.Credential{Uid: uint32(id), Gid: uint32(gid), Groups: []uint32{}}, false, nil
}

// Chrome 进程只拿得到这些环境变量；APP_MASTER_KEY、JWT_SECRET、CARD_API_KEY 等密钥，
// 以及 HTTP_PROXY 一类代理变量（代理走 --proxy-server）一律不传。
// 白名单变量会出现在 env 进程的命令行上（ps 可见），所以这里绝不能放任何秘密。
var (
	chromeEnvNames    = []string{"PATH", "TZ", "LANG", "LANGUAGE"}
	chromeEnvPrefixes = []string{"LC_", "XDG_", "FONTCONFIG_"}
)

func chromeEnvAllowed(name string) bool {
	for _, n := range chromeEnvNames {
		if name == n {
			return true
		}
	}
	for _, p := range chromeEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// chromeEnv 由当前进程的环境算出 Chrome 的环境：白名单里的变量原样保留；TMPDIR 指向本次的临时用户目录；
// HOME 在 Linux 上也指向它（继承来的可能不可写，例如换成 cdk-chrome 后 root 的家目录），其他平台沿用继承来的
// （见 homeInProfile）；DISPLAY 只用 display（pickDisplay 的结果），为空就不给。
func chromeEnv(environ []string, dir, display string) []string {
	out := make([]string, 0, len(environ)+3)
	seen := map[string]bool{}
	home := ""
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if name == "HOME" {
			home = value
		}
		if chromeEnvAllowed(name) {
			out = append(out, kv)
		}
	}
	if homeInProfile {
		home = dir
	}
	if home != "" {
		out = append(out, "HOME="+home)
	}
	out = append(out, "TMPDIR="+dir)
	if display != "" {
		out = append(out, "DISPLAY="+display)
	}
	return out
}

// envBinary 找 env 程序：优先 /usr/bin/env，没有再按 PATH 找。
func envBinary() (string, error) {
	if fi, err := os.Stat("/usr/bin/env"); err == nil && !fi.IsDir() {
		return "/usr/bin/env", nil
	}
	p, err := exec.LookPath("env")
	if err != nil {
		return "", fmt.Errorf("找不到 env 程序，无法以干净的环境变量启动 Chrome: %w", err)
	}
	return p, nil
}

// wrapChromeCmd 把 chromedp 要启动的 Chrome 命令改成经 `env -i 白名单变量… Chrome 原参数…` 启动。
// chromedp 会把本进程的全部环境变量（os.Environ()）拼进子进程，ModifyCmdFunc 又先于拼接执行，
// 没法在这里直接清空环境；env -i 先清空再 exec Chrome，进程号不变，chromedp 照常管理它。
// cred 不为空时 env 先以该用户运行再 exec Chrome；已有的 SysProcAttr 字段一律保留。
func wrapChromeCmd(cmd *exec.Cmd, envBin string, env []string, cred *syscall.Credential) {
	chrome := cmd.Path
	if abs, err := filepath.Abs(chrome); err == nil {
		chrome = abs // 绝对路径：env 不会按 PATH 去找，也不会把它当成选项
	}
	if strings.Contains(chrome, "=") {
		// env 会把含 = 的参数当成变量赋值，而不是要执行的程序
		cmd.Err = fmt.Errorf("启动 Chrome 失败：程序路径不能含 '='（%s）", chrome)
		return
	}
	args := make([]string, 0, len(env)+len(cmd.Args)+2)
	args = append(args, "env", "-i")
	args = append(args, env...)
	args = append(args, chrome)
	if len(cmd.Args) > 1 {
		args = append(args, cmd.Args[1:]...)
	}
	cmd.Path = envBin
	cmd.Args = args
	// env 进程自己也只给白名单（chromedp 仍会在前面拼上 os.Environ()，真正把关的是 -i）
	cmd.Env = append([]string(nil), env...)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if cred != nil {
		cmd.SysProcAttr.Credential = cred
	}
	// 设置 ModifyCmdFunc 后 chromedp 不再自动设父进程退出信号，这里补上；env 不是 setuid 程序，exec 后信号设置仍在
	setDeathSignal(cmd.SysProcAttr)
}

// sandboxOptions 按当前进程身份给出启动参数：始终显式设置 no-sandbox=false（防止 chromedp 在 root 下自动加上）；
// 需要换用户时把临时用户目录交给该用户，并让 Chrome 以该用户运行；任何身份下 Chrome 都只拿到白名单环境变量。
// display 是 Chrome 要用的 DISPLAY（无头模式为空）。
func sandboxOptions(dir, display string) ([]chromedp.ExecAllocatorOption, error) {
	cred, noSandbox, err := chromeIdentity(os.Getuid(), user.Lookup)
	if err != nil {
		return nil, err
	}
	envBin, err := envBinary()
	if err != nil {
		return nil, err
	}
	if cred != nil {
		if err := os.Chown(dir, int(cred.Uid), int(cred.Gid)); err != nil {
			return nil, fmt.Errorf("把 Chrome 临时目录交给 %s 失败: %w", chromeUser, err)
		}
	}
	env := chromeEnv(os.Environ(), dir, display)
	return []chromedp.ExecAllocatorOption{
		chromedp.Flag("no-sandbox", noSandbox),
		chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) { wrapChromeCmd(cmd, envBin, env, cred) }),
	}, nil
}
