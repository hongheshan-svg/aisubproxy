package paybrowser

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
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
		return nil, false, fmt.Errorf("服务以 root 运行，需要系统用户 %s 来启动 Chrome（保留沙箱），但找不到该用户，请先运行：%s", chromeUser, InstallHint)
	}
	id, err1 := strconv.ParseUint(u.Uid, 10, 32)
	gid, err2 := strconv.ParseUint(u.Gid, 10, 32)
	if err1 != nil || err2 != nil {
		return nil, false, fmt.Errorf("系统用户 %s 的 uid 或 gid 无效，请重新运行：%s", chromeUser, InstallHint)
	}
	if id == 0 {
		return nil, false, fmt.Errorf("系统用户 %s 的 uid 不能为 0，请重新运行：%s", chromeUser, InstallHint)
	}
	// Groups 为空：去掉 root 的附加组
	return &syscall.Credential{Uid: uint32(id), Gid: uint32(gid), Groups: []uint32{}}, false, nil
}

// sandboxOptions 按当前进程身份给出启动参数：始终显式设置 no-sandbox（防止 chromedp 在 root 下自动加上）；
// 需要换用户时把临时用户目录交给该用户，并让 Chrome 以该用户运行。
func sandboxOptions(dir string) ([]chromedp.ExecAllocatorOption, error) {
	cred, noSandbox, err := chromeIdentity(os.Getuid(), user.Lookup)
	if err != nil {
		return nil, err
	}
	opts := []chromedp.ExecAllocatorOption{chromedp.Flag("no-sandbox", noSandbox)}
	if cred == nil {
		return opts, nil
	}
	if err := os.Chown(dir, int(cred.Uid), int(cred.Gid)); err != nil {
		return nil, fmt.Errorf("把 Chrome 临时目录交给 %s 失败: %w", chromeUser, err)
	}
	opts = append(opts,
		// 继承来的 HOME 是 root 的家目录，低权限用户写不进去；指到临时用户目录
		chromedp.Env("HOME="+dir),
		chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
			if cmd.SysProcAttr == nil {
				cmd.SysProcAttr = &syscall.SysProcAttr{}
			}
			cmd.SysProcAttr.Credential = cred
			// 设置 ModifyCmdFunc 后 chromedp 不再自动设父进程退出信号，这里补上
			setDeathSignal(cmd.SysProcAttr)
		}),
	)
	return opts, nil
}
