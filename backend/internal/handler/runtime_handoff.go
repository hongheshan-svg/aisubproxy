package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// Docker 版在线升级的启动接管（spec §2.3 第 4、5 步）。
//
// 容器内在线升级不改镜像，新程序、前端、版本号写到数据卷的 runtime 目录（<数据目录>/runtime）。
// 容器每次启动先跑镜像里的程序，由 RuntimeHandoff 决定要不要换成 runtime 里的程序：
//   - runtime 版本更新：boot.json 的启动次数加 1，再 exec runtime 里的程序，WEB_DIR 指向 runtime/web，
//     并带上 CDK_RUNTIME_HANDOFF=1，防止反复接管；
//   - 镜像版本相同或更新（拉了新镜像）：删掉过期的 runtime，updates/ 里的备份保留；
//   - runtime 连续 3 次没能完成启动：不再接管，退回镜像版本，原因记进 boot.json；
//   - exec 失败（例如数据目录挂成了 noexec）：记日志，继续用镜像版本。
//
// 新版本完成启动（数据库打开、HTTP 开始监听）后，RuntimeBootCompleted 把启动次数清零；
// 退回过镜像版本的，也在那里告警并写审计。

const (
	// runtimeHandoffEnv 为 1 表示本进程已经是接管后的 runtime 程序，不再接管。
	runtimeHandoffEnv = "CDK_RUNTIME_HANDOFF"
	runtimeDirName    = "runtime"
	updatesDirName    = "updates"
	bootFileName      = "boot.json"
	// maxRuntimeBootFailures 是 runtime 连续没能完成启动的次数上限，到了就退回镜像版本。
	maxRuntimeBootFailures = 3
)

// containerRollbackHint 是容器内的命令行回滚方法。
const containerRollbackHint = "docker exec cdk-recharge /app/cdk-recharge rollback && docker restart cdk-recharge"

// dataDir 是数据目录：DB_PATH 所在目录，与 db.Init 同一规则；容器内默认 /app/data。
func dataDir() string {
	p := "../data/cdk_recharge.db"
	if v := strings.TrimSpace(os.Getenv("DB_PATH")); v != "" {
		p = v
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Dir(p)
}

// runtimeDirOf 是在线升级写入新版本的目录。
func runtimeDirOf(data string) string { return filepath.Join(data, runtimeDirName) }

// currentExe 是正在运行的程序文件（解析软链接）。
func currentExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		return p
	}
	return exe
}

// imageVersion 是镜像里程序的版本：编译时写入的版本号，其次是程序旁边的 VERSION（镜像里是 /app/VERSION）。
func imageVersion(exe string) string {
	if v := strings.TrimSpace(BuildVersion); v != "" {
		return strings.TrimPrefix(v, "v")
	}
	if exe != "" {
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "VERSION")); err == nil {
			if v := versionLine(b); v != "" {
				return v
			}
		}
	}
	return resolveLocalVersion()
}

// versionLine 取 VERSION 文件第一行，去掉空白和前缀 v。
func versionLine(b []byte) string {
	line := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
	return strings.TrimPrefix(line, "v")
}

// isWithin 判断 path 是否在 dir 之下（两边都先解析软链接）。
func isWithin(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	if p, err := filepath.EvalSymlinks(dir); err == nil {
		dir = p
	}
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ---- boot.json ----

// bootFallback 记录一次退回镜像版本：原因写给管理员看，Reported 表示已经告警并写过审计。
type bootFallback struct {
	Version      string `json:"version"`       // 没能接管的 runtime 版本
	ImageVersion string `json:"image_version"` // 退回到的镜像版本
	Reason       string `json:"reason"`
	At           string `json:"at"`
	Reported     bool   `json:"reported"`
}

// bootState 是 <runtime>/boot.json 的内容。
type bootState struct {
	Version       string        `json:"version"`    // BootCount 对应的 runtime 版本，版本变了重新计数
	BootCount     int           `json:"boot_count"` // 接管后还没完成启动的次数
	LastAttemptAt string        `json:"last_attempt_at,omitempty"`
	LastOKAt      string        `json:"last_ok_at,omitempty"`
	Fallback      *bootFallback `json:"fallback,omitempty"`
}

// handoffFS 是启动接管用到的文件操作，测试可替换来模拟写入失败。
type handoffFS interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm os.FileMode) error
	Rename(oldpath, newpath string) error
	Remove(name string) error
	RemoveAll(path string) error
	Stat(name string) (os.FileInfo, error)
}

type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
func (osFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}
func (osFS) Rename(oldpath, newpath string) error  { return os.Rename(oldpath, newpath) }
func (osFS) Remove(name string) error              { return os.Remove(name) }
func (osFS) RemoveAll(path string) error           { return os.RemoveAll(path) }
func (osFS) Stat(name string) (os.FileInfo, error) { return os.Stat(name) }

// loadBoot 读 boot.json。文件不存在时 exists 为 false；内容坏了按空状态处理（会被下一次写覆盖）。
func loadBoot(fsys handoffFS, rt string) (st bootState, exists bool, err error) {
	b, err := fsys.ReadFile(filepath.Join(rt, bootFileName))
	if errors.Is(err, fs.ErrNotExist) {
		return bootState{}, false, nil
	}
	if err != nil {
		return bootState{}, false, err
	}
	if json.Unmarshal(b, &st) != nil {
		return bootState{}, true, nil
	}
	return st, true, nil
}

// saveBoot 先写临时文件再改名，断电也不会留下半个 boot.json。
func saveBoot(fsys handoffFS, rt string, st bootState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(rt, bootFileName)
	tmp := p + ".tmp"
	if err := fsys.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := fsys.Rename(tmp, p); err != nil {
		_ = fsys.Remove(tmp)
		return err
	}
	return nil
}

// setFallback 记下退回镜像版本的原因。同一版本、同一原因已经记过的保留 Reported，免得每次重启都告警。
func (st *bootState) setFallback(version, image, reason string, now time.Time) {
	if f := st.Fallback; f != nil && f.Version == version && f.ImageVersion == image && f.Reason == reason {
		return
	}
	st.Fallback = &bootFallback{Version: version, ImageVersion: image, Reason: reason, At: now.UTC().Format(time.RFC3339)}
}

// handoffEnviron 是 exec runtime 程序用的环境变量：WEB_DIR 指向 runtime/web，带上防重复接管的标记。
// CDK_APP_VERSION 换成 runtime 的版本：程序没有编译进版本号时，靠它报出正确的版本（否则会读到工作目录里镜像的 VERSION）。
func handoffEnviron(environ []string, rt, version string) []string {
	drop := []string{"WEB_DIR=", runtimeHandoffEnv + "=", "CDK_APP_VERSION="}
	out := make([]string, 0, len(environ)+3)
next:
	for _, kv := range environ {
		for _, p := range drop {
			if strings.HasPrefix(kv, p) {
				continue next
			}
		}
		out = append(out, kv)
	}
	return append(out,
		"WEB_DIR="+filepath.Join(rt, "web"),
		runtimeHandoffEnv+"=1",
		"CDK_APP_VERSION="+version,
	)
}

// ---- 启动接管 ----

// handoffDeps 是启动接管的外部依赖，测试换成临时目录、假的 exec 和固定的版本号。
type handoffDeps struct {
	FS           handoffFS
	DataDir      string   // DB_PATH 所在目录
	Exe          string   // 正在运行的程序（已解析软链接）
	Args         []string // 命令行参数，原样传给 runtime 程序
	Getenv       func(string) string
	Environ      func() []string
	ImageVersion func() string
	Exec         func(argv0 string, argv, envv []string) error
	Now          func() time.Time
	Logf         func(format string, args ...any)
}

type handoffAction string

const (
	handoffNone       handoffAction = "none"        // 数据卷里没有在线升级的版本
	handoffAlready    handoffAction = "already"     // 本进程已经是 runtime 程序
	handoffPruned     handoffAction = "pruned"      // 镜像版本相同或更新：删掉了过期的 runtime
	handoffSkipped    handoffAction = "skipped"     // runtime 不完整、版本号无效或 boot.json 写不进：不接管
	handoffFellBack   handoffAction = "fallback"    // 连续启动失败：退回镜像版本
	handoffExecFailed handoffAction = "exec_failed" // exec 失败：继续用镜像版本
	handoffExeced     handoffAction = "exec"        // 已 exec（真实环境里 exec 成功不会返回）
)

type handoffResult struct {
	Action         handoffAction
	RuntimeVersion string
	ImageVersion   string
	Err            error
}

// runtimeHandoff 决定是否换成数据卷里的程序运行。任何意外都退回镜像版本继续启动，不让容器起不来。
func runtimeHandoff(d handoffDeps) handoffResult {
	if d.Getenv(runtimeHandoffEnv) == "1" {
		return handoffResult{Action: handoffAlready}
	}
	rt := runtimeDirOf(d.DataDir)
	if isWithin(d.Exe, rt) {
		// 直接运行了 runtime 里的程序（没带标记）：已经是它，不再接管，更不能删自己所在的目录
		return handoffResult{Action: handoffAlready}
	}

	raw, err := d.FS.ReadFile(filepath.Join(rt, "VERSION"))
	if errors.Is(err, fs.ErrNotExist) {
		if _, e := d.FS.Stat(rt); e == nil {
			d.Logf("[runtime] %s 没有 VERSION（在线升级没写完），不接管，用镜像版本运行", rt)
		}
		return handoffResult{Action: handoffNone}
	}
	if err != nil {
		d.Logf("[runtime] 读取 %s 的版本号失败，不接管，用镜像版本运行: %v", rt, err)
		return handoffResult{Action: handoffSkipped, Err: err}
	}
	res := handoffResult{RuntimeVersion: versionLine(raw), ImageVersion: strings.TrimPrefix(strings.TrimSpace(d.ImageVersion()), "v")}
	cmp, ok := compareVersions(res.RuntimeVersion, res.ImageVersion)
	if !ok {
		d.Logf("[runtime] 版本号无法比较（数据卷 %q，镜像 %q），不接管，用镜像版本运行", res.RuntimeVersion, res.ImageVersion)
		res.Action = handoffSkipped
		return res
	}
	if cmp <= 0 {
		// 拉了新镜像：以镜像为准。updates/ 里的备份不在 runtime 下，保留
		if err := d.FS.RemoveAll(rt); err != nil {
			d.Logf("[runtime] 镜像 v%s 不旧于数据卷 v%s，删除过期的 %s 失败: %v", res.ImageVersion, res.RuntimeVersion, rt, err)
			res.Err = err
		} else {
			d.Logf("[runtime] 镜像 v%s 不旧于数据卷 v%s，已删除过期的 %s，用镜像版本运行", res.ImageVersion, res.RuntimeVersion, rt)
		}
		res.Action = handoffPruned
		return res
	}

	bin := filepath.Join(rt, "cdk-recharge")
	if st, err := d.FS.Stat(bin); err != nil || !st.Mode().IsRegular() {
		d.Logf("[runtime] %s 下缺少程序文件，不接管，用镜像版本运行", rt)
		res.Action = handoffSkipped
		return res
	}
	if st, err := d.FS.Stat(filepath.Join(rt, "web")); err != nil || !st.IsDir() {
		d.Logf("[runtime] %s 下缺少前端目录 web，不接管，用镜像版本运行", rt)
		res.Action = handoffSkipped
		return res
	}

	st, _, err := loadBoot(d.FS, rt)
	if err != nil {
		d.Logf("[runtime] 读取 boot.json 失败，不接管，用镜像版本运行: %v", err)
		res.Action, res.Err = handoffSkipped, err
		return res
	}
	if st.Version != res.RuntimeVersion {
		st = bootState{Version: res.RuntimeVersion} // 换了版本，重新计数
	}
	now := d.Now()
	if st.BootCount >= maxRuntimeBootFailures {
		reason := fmt.Sprintf("在线升级的 v%s 连续 %d 次启动失败，已退回镜像版本 v%s", res.RuntimeVersion, st.BootCount, res.ImageVersion)
		st.setFallback(res.RuntimeVersion, res.ImageVersion, reason, now)
		if err := saveBoot(d.FS, rt, st); err != nil {
			d.Logf("[runtime] 写 boot.json 失败: %v", err)
		}
		d.Logf("[runtime] %s", reason)
		res.Action = handoffFellBack
		return res
	}

	// 先记一次启动再 exec：新版本起不来时，计数留在这里，连续失败够次数就不再接管
	before := st
	st.BootCount++
	st.LastAttemptAt = now.UTC().Format(time.RFC3339)
	if err := saveBoot(d.FS, rt, st); err != nil {
		// 记不下启动次数就没有失败保护，宁可不接管
		d.Logf("[runtime] 写 boot.json 失败，不接管，用镜像版本运行: %v", err)
		res.Action, res.Err = handoffSkipped, err
		return res
	}
	argv := []string{bin}
	if len(d.Args) > 1 {
		argv = append(argv, d.Args[1:]...)
	}
	d.Logf("[runtime] 镜像 v%s，数据卷 v%s 更新：接管运行 %s（第 %d 次）", res.ImageVersion, res.RuntimeVersion, bin, st.BootCount)
	if err := d.Exec(bin, argv, handoffEnviron(d.Environ(), rt, res.RuntimeVersion)); err != nil {
		// 程序根本没跑起来，不算一次启动失败；记下原因，起来后告警
		reason := fmt.Sprintf("在线升级的 v%s 无法执行（%v），已退回镜像版本 v%s。数据目录是否挂成了 noexec？", res.RuntimeVersion, err, res.ImageVersion)
		before.setFallback(res.RuntimeVersion, res.ImageVersion, reason, now)
		if err := saveBoot(d.FS, rt, before); err != nil {
			d.Logf("[runtime] 写 boot.json 失败: %v", err)
		}
		d.Logf("[runtime] %s", reason)
		res.Action, res.Err = handoffExecFailed, err
		return res
	}
	res.Action = handoffExeced
	return res
}

// RuntimeHandoff 在容器内、打开数据库之前调用：数据卷里有更新的在线升级版本时 exec 过去（成功则不返回）。
// 原生部署什么也不做。
func RuntimeHandoff() {
	if !inContainer() {
		return
	}
	exe := currentExe()
	runtimeHandoff(handoffDeps{
		FS:           osFS{},
		DataDir:      dataDir(),
		Exe:          exe,
		Args:         os.Args,
		Getenv:       os.Getenv,
		Environ:      os.Environ,
		ImageVersion: func() string { return imageVersion(exe) },
		Exec:         syscall.Exec,
		Now:          time.Now,
		Logf:         log.Printf,
	})
}

// ---- 完成启动 ----

// bootCompletedDeps 是完成启动时的外部依赖。
type bootCompletedDeps struct {
	FS      handoffFS
	DataDir string
	Exe     string
	Getenv  func(string) string
	Now     func() time.Time
	Alert   func(text string)
	Audit   func(action, detail string)
	Logf    func(format string, args ...any)
}

// runtimeBootCompleted：本进程是 runtime 程序时清零启动次数、清掉旧的退回记录；
// 有还没报过的退回记录时告警并写审计。
func runtimeBootCompleted(d bootCompletedDeps) {
	rt := runtimeDirOf(d.DataDir)
	st, exists, err := loadBoot(d.FS, rt)
	if err != nil {
		d.Logf("[runtime] 读取 boot.json 失败: %v", err)
		return
	}
	if !exists {
		return
	}
	if d.Getenv(runtimeHandoffEnv) == "1" || isWithin(d.Exe, rt) {
		st.BootCount = 0
		st.Fallback = nil
		st.LastOKAt = d.Now().UTC().Format(time.RFC3339)
	}
	if f := st.Fallback; f != nil && !f.Reported {
		d.Alert(f.Reason + "。可以在后台「版本更新」重新升级，或拉取更新的镜像；要退回上一个版本：" + containerRollbackHint)
		d.Audit("runtime_fallback", fmt.Sprintf("runtime=v%s image=v%s reason=%s", f.Version, f.ImageVersion, f.Reason))
		f.Reported = true
	}
	if err := saveBoot(d.FS, rt, st); err != nil {
		d.Logf("[runtime] 写 boot.json 失败: %v", err)
	}
}

// RuntimeBootCompleted 在数据库已打开、HTTP 已开始监听后调用。原生部署什么也不做。
func RuntimeBootCompleted() {
	if !inContainer() {
		return
	}
	runtimeBootCompleted(bootCompletedDeps{
		FS:      osFS{},
		DataDir: dataDir(),
		Exe:     currentExe(),
		Getenv:  os.Getenv,
		Now:     time.Now,
		Alert:   opsAlert,
		Audit:   func(action, detail string) { db.WriteAudit("system", action, detail, "") },
		Logf:    log.Printf,
	})
}

// ---- 版本比较 ----

type semVersion struct {
	nums []int
	pre  []string
}

// parseVersion 解析语义化版本：可带前缀 v，主版本号段数不限，可带 -预发布 与 +构建信息（构建信息不参与比较）。
func parseVersion(s string) (semVersion, bool) {
	s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "v"), "V")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core, pre, hasPre := strings.Cut(s, "-")
	if core == "" {
		return semVersion{}, false
	}
	var v semVersion
	for _, p := range strings.Split(core, ".") {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return semVersion{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return semVersion{}, false
		}
		v.nums = append(v.nums, n)
	}
	if hasPre {
		for _, id := range strings.Split(pre, ".") {
			if id == "" {
				return semVersion{}, false
			}
			v.pre = append(v.pre, id)
		}
	}
	return v, true
}

// compareVersions 按语义化版本比较 a 和 b：a<b 返回 -1，相等 0，a>b 返回 1；有一方解析不了时 ok 为 false。
// 缺的段按 0 算（1.6 等于 1.6.0）；带预发布的低于正式版（1.6.0-rc.1 < 1.6.0）。
func compareVersions(a, b string) (cmp int, ok bool) {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	if !okA || !okB {
		return 0, false
	}
	n := len(va.nums)
	if len(vb.nums) > n {
		n = len(vb.nums)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(va.nums) {
			x = va.nums[i]
		}
		if i < len(vb.nums) {
			y = vb.nums[i]
		}
		if x != y {
			return sign(x - y), true
		}
	}
	switch {
	case len(va.pre) == 0 && len(vb.pre) == 0:
		return 0, true
	case len(va.pre) == 0:
		return 1, true
	case len(vb.pre) == 0:
		return -1, true
	}
	for i := 0; i < len(va.pre) && i < len(vb.pre); i++ {
		if c := comparePreID(va.pre[i], vb.pre[i]); c != 0 {
			return c, true
		}
	}
	return sign(len(va.pre) - len(vb.pre)), true
}

// comparePreID 比较一段预发布标识：纯数字按数值，数字低于字母，字母按字典序。
func comparePreID(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		return sign(na - nb)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
