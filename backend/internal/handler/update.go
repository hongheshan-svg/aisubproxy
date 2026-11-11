package handler

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// 一键更新状态（进程内；重启后重置为 idle，版本由 VERSION 体现）
type updateState struct {
	Phase      string `json:"phase"` // idle|checking|downloading|verifying|staging|applying|reloading|done|failed
	Message    string `json:"message"`
	Target     string `json:"target,omitempty"`
	Progress   int    `json:"progress"` // 0-100
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	BytesTotal int64  `json:"bytes_total,omitempty"`
	BytesDone  int64  `json:"bytes_done,omitempty"`
}

var (
	updMu      sync.Mutex
	updRunning bool
	updSnap    = updateState{Phase: "idle", Message: "idle", Progress: 0}
)

func setUpdateState(mut func(*updateState)) {
	updMu.Lock()
	defer updMu.Unlock()
	mut(&updSnap)
}

func getUpdateState() updateState {
	updMu.Lock()
	defer updMu.Unlock()
	return updSnap
}

// updateEnabled 判断是否允许一键更新。容器内也允许：新版本写到数据卷的 runtime 目录（见 currentUpdateLayout）。
func updateEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("UPDATE_ENABLED")))
	if v == "0" || v == "false" || v == "no" || v == "off" {
		return false
	}
	// 默认开启；显式 false 才关。开发机可设 UPDATE_ENABLED=0
	return true
}

func installRoot() string {
	if d := strings.TrimSpace(os.Getenv("CDK_INSTALL_DIR")); d != "" {
		return filepath.Clean(d)
	}
	if exe, err := os.Executable(); err == nil {
		if p, err2 := filepath.EvalSymlinks(exe); err2 == nil {
			return filepath.Dir(p)
		}
		return filepath.Dir(exe)
	}
	wd, _ := os.Getwd()
	return wd
}

func webDir() string {
	if d := strings.TrimSpace(os.Getenv("WEB_DIR")); d != "" {
		return filepath.Clean(d)
	}
	return filepath.Join(installRoot(), "web")
}

// updateLayout 是一次在线升级读写的位置。
// 原生：新文件原地替换安装目录里正在用的那份，临时文件和备份在 <安装目录>/updates。
// 容器：镜像目录不动，新程序、前端、版本号写到数据卷的 <数据目录>/runtime，临时文件和备份写到 <数据目录>/updates
// （与 runtime 在同一文件系统上，改名是原子的）；下次启动由 RuntimeHandoff 接管。
type updateLayout struct {
	Container  bool
	UpdDir     string // 临时文件与备份
	CurBin     string // 当前在跑的程序（备份来源）
	CurWeb     string // 当前的前端（备份来源）
	CurVer     string // 当前的版本号文件（备份来源）
	Bin        string // 新程序写到这里
	Web        string // 新前端写到这里
	Ver        string // 新版本号写到这里
	RuntimeDir string // 仅容器：在线升级的 runtime 目录
}

func currentUpdateLayout() updateLayout {
	root := installRoot()
	l := updateLayout{
		UpdDir: filepath.Join(root, updatesDirName),
		CurBin: selfBinaryPath(),
		CurWeb: webDir(),
		CurVer: filepath.Join(root, "VERSION"),
	}
	if !inContainer() {
		l.Bin, l.Web, l.Ver = l.CurBin, l.CurWeb, l.CurVer
		return l
	}
	data := dataDir()
	rt := runtimeDirOf(data)
	l.Container = true
	l.RuntimeDir = rt
	l.UpdDir = filepath.Join(data, updatesDirName)
	l.Bin = filepath.Join(rt, "cdk-recharge")
	l.Web = filepath.Join(rt, "web")
	l.Ver = filepath.Join(rt, "VERSION")
	return l
}

// deployMode 是部署方式：docker 或 native，供「版本更新」页决定显示哪种说明。
func deployMode() string {
	if inContainer() {
		return "docker"
	}
	return "native"
}

// AdminSystemUpdateStatus GET /api/v1/admin/system/update/status
func AdminSystemUpdateStatus(c *gin.Context) {
	st := getUpdateState()
	c.JSON(http.StatusOK, gin.H{
		"enabled":     updateEnabled(),
		"running":     st.Phase != "idle" && st.Phase != "done" && st.Phase != "failed",
		"state":       st,
		"current":     resolveLocalVersion(),
		"install":     installRoot(),
		"goos":        updateGOOS,
		"goarch":      runtime.GOARCH,
		"can_update":  updateEnabled() && updateGOOS == "linux",
		"update_hint": updateHint(),
		"deploy_mode": deployMode(),
	})
}

// AdminSystemUpdate POST /api/v1/admin/system/update
// body: { "target": "1.2.0" | "latest", "confirm": true }
// 在线下载 GitHub Release 预编译包 → 校验 → 热替换 web+二进制 → re-exec（亚秒级重载，不碰 app.env/data）
func AdminSystemUpdate(c *gin.Context) {
	if !updateEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "UPDATE_ENABLED=0，未开启一键更新"})
		return
	}
	if updateGOOS != "linux" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "一键更新目前仅支持 linux 生产部署"})
		return
	}

	var req struct {
		Target  string `json:"target"`
		Confirm bool   `json:"confirm"`
	}
	_ = c.ShouldBindJSON(&req)
	if !req.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请传 confirm=true 确认更新"})
		return
	}
	target := strings.TrimSpace(req.Target)
	if target == "" || strings.EqualFold(target, "latest") {
		target = "latest"
	} else {
		target = strings.TrimPrefix(target, "v")
		// 目标版本会拼进 GitHub API 路径，只接受语义化版本，挡掉 /、..、? 等
		if _, ok := parseVersion(target); !ok || !updateTargetRe.MatchString(target) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "目标版本号格式不对"})
			return
		}
	}

	updMu.Lock()
	if updRunning {
		updMu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "已有更新在进行中", "state": getUpdateState()})
		return
	}
	// 有在途付款单就不更新：替换程序、重载进程会把正在开浏览器、注资、填表的单打断。只查这一次，不自动排空
	if err := checkNoPaymentsInFlight(c.Request.Context()); err != nil {
		updMu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	updRunning = true
	updSnap = updateState{
		Phase:     "checking",
		Message:   "准备检查 GitHub 发布包…",
		Target:    target,
		Progress:  1,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	updMu.Unlock()

	u, _ := c.Get("username")
	username, _ := u.(string)
	db.WriteAudit(username, "system_update_start", "target="+target, c.ClientIP())

	// 异步执行：先立即返回，前端轮询 status；完成文件替换后延迟 re-exec
	go runSeamlessUpdate(target, username)

	c.JSON(http.StatusAccepted, gin.H{
		"ok":      true,
		"message": "更新已启动：在线下载与热替换，完成后自动重载（不中断部署配置/数据）",
		"state":   getUpdateState(),
	})
}

func runSeamlessUpdate(target, username string) {
	defer func() {
		if r := recover(); r != nil {
			// panic 的值可能带敏感信息，只记类型和位置
			failUpdate("内部错误（panic）：" + safego.Describe(r))
		}
		updMu.Lock()
		// reloading 阶段会 re-exec，不在此清 running
		if updSnap.Phase != "reloading" {
			updRunning = false
		}
		updMu.Unlock()
	}()

	setUpdateState(func(s *updateState) {
		s.Phase = "checking"
		s.Message = "查询 GitHub Release…"
		s.Progress = 5
	})

	assetURL, version, shaURL, sigURL, err := resolveReleaseBundle(target)
	if err != nil {
		failUpdate(err.Error())
		return
	}
	setUpdateState(func(s *updateState) {
		s.Target = version
		s.Message = "找到发布包 v" + version + "，开始下载…"
		s.Progress = 10
	})

	l := currentUpdateLayout()
	updDir := l.UpdDir
	_ = os.MkdirAll(updDir, 0o755)
	staging := filepath.Join(updDir, "staging-"+version+"-"+fmt.Sprintf("%d", time.Now().Unix()))
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		failUpdate("创建 staging 失败: " + err.Error())
		return
	}
	defer os.RemoveAll(staging) // 成功 re-exec 前也会清；失败时清理

	tgzPath := filepath.Join(staging, "cdk-bundle.tgz")
	setUpdateState(func(s *updateState) {
		s.Phase = "downloading"
		s.Message = "下载 cdk-bundle…"
		s.Progress = 15
	})
	if err := downloadFile(assetURL, tgzPath, func(done, total int64) {
		setUpdateState(func(s *updateState) {
			s.BytesDone = done
			s.BytesTotal = total
			if total > 0 {
				// 15-55% 给下载
				s.Progress = 15 + int(done*40/total)
			}
			s.Message = fmt.Sprintf("下载中 %s / %s", humanBytes(done), humanBytes(total))
		})
	}); err != nil {
		failUpdate("下载失败: " + err.Error())
		return
	}

	setUpdateState(func(s *updateState) {
		s.Phase = "verifying"
		s.Message = "校验包完整性…"
		s.Progress = 58
	})
	if shaURL == "" {
		failUpdate("缺少 sha256 校验文件，已中止更新（禁止无完整性校验的远程更新）")
		return
	}
	if err := verifySHA256File(tgzPath, shaURL); err != nil {
		failUpdate("sha256 校验失败，已中止更新: " + err.Error())
		return
	}
	if sigURL == "" {
		failUpdate("缺少签名文件 cdk-bundle-linux-amd64.tgz.sig，已中止更新（只安装本项目签名的发布包）")
		return
	}
	if err := verifyBundleSignatureFile(tgzPath, sigURL); err != nil {
		failUpdate("签名校验失败，已中止更新: " + err.Error())
		return
	}

	setUpdateState(func(s *updateState) {
		s.Phase = "staging"
		s.Message = "解压发布包…"
		s.Progress = 62
	})
	extractDir := filepath.Join(staging, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		failUpdate(err.Error())
		return
	}
	if err := extractTarGz(tgzPath, extractDir); err != nil {
		failUpdate("解压失败: " + err.Error())
		return
	}
	binSrc, webSrc, verSrc, err := locateBundleContents(extractDir)
	if err != nil {
		failUpdate(err.Error())
		return
	}
	if err := preflightBinary(binSrc, version); err != nil {
		failUpdate("新程序自检失败，已中止更新: " + err.Error())
		return
	}

	// 开始备份、替换文件之前再查一次在途付款单：下载、校验期间可能有新单开始处理
	if err := checkNoPaymentsInFlight(context.Background()); err != nil {
		failUpdate(err.Error())
		return
	}

	setUpdateState(func(s *updateState) {
		s.Phase = "applying"
		s.Message = "热替换前端静态资源…"
		s.Progress = 75
	})
	// 备份当前在跑的版本（容器里第一次升级时就是镜像里的程序、前端和版本号）
	bak := filepath.Join(updDir, "backup-"+time.Now().Format("20060102-150405"))
	if err := makeBackupDir(bak); err != nil {
		failUpdate("创建备份目录失败，已中止更新: " + err.Error())
		return
	}
	// 备份旧程序、旧前端、旧版本号：任何一项失败都中止（此时现网文件一个都没动），否则回滚会拿到更早的版本
	if err := backupCurrentFiles(l, bak); err != nil {
		_ = os.RemoveAll(bak)
		failUpdate("备份当前版本失败，已中止更新: " + err.Error())
		return
	}
	// 数据库备份失败也中止：没有可回退的数据就不升级
	if err := backupDatabase(filepath.Join(bak, "cdk_recharge.db")); err != nil {
		failUpdate("备份数据库失败，已中止更新: " + err.Error())
		return
	}
	if l.Container {
		if err := os.MkdirAll(l.RuntimeDir, 0o755); err != nil {
			failUpdate("创建 runtime 目录失败: " + err.Error())
			return
		}
	}

	// 前后端要么都换成新版，要么都保持旧版：先备好新二进制和新 web，再换二进制，最后换 web，任何一步失败都回退已换掉的部分
	if err := swapBinaryAndWeb(l, binSrc, webSrc); err != nil {
		failUpdate(err.Error())
		return
	}
	// 写 VERSION：放在程序之后，容器的启动接管以它为准，程序没换成功就不会有新版本号
	if err := writeNewVersionFn(l, verSrc, version); err != nil {
		log.Printf("[update] 写新 VERSION 失败: %v", err)
		// 程序和前端已经是新版而版本号没写成：先从备份还原旧版，还原成功就按「更新失败」收场，不重载
		if rerr := restoreFromBackupFn(l, bak); rerr != nil {
			log.Printf("[update] 还原旧版失败: %v", rerr)
			failUpdate("写入新版本号失败，且还原旧版也失败，请手动回滚并重启服务。" + manualRollbackHint(l, bak))
			return
		}
		failUpdate("写入新版本号失败，已还原为更新前的版本，本次更新未生效: " + err.Error())
		return
	}

	// 容器：这次 exec 就是一次接管，计一次启动（新版本完成启动后清零）；起不来时容器重启，由启动接管继续计数
	var env []string
	if l.Container {
		boot := bootState{Version: version, BootCount: 1, LastAttemptAt: time.Now().UTC().Format(time.RFC3339)}
		if err := saveBoot(osFS{}, l.RuntimeDir, boot); err != nil {
			log.Printf("[update] 写 boot.json 失败（下次启动时重新计数）: %v", err)
		}
		env = handoffEnviron(os.Environ(), l.RuntimeDir, version)
	}

	db.WriteAudit(username, "system_update_applied", "target="+version+" bak="+filepath.Base(bak), "")

	setUpdateState(func(s *updateState) {
		s.Phase = "reloading"
		s.Message = "文件已替换，即将无痕重载进程…"
		s.Progress = 95
		s.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	})

	pruneBackups(updDir, maxUpdateBackups)

	// 延迟 re-exec，让状态接口还能被读一次
	safego.Go("在线更新重载", func() {
		time.Sleep(400 * time.Millisecond)
		// 兜底：前面查过没有在途单，这里再停止派发、等可能刚开始的单进入确认或结束，再 exec
		resume := drainPayments()
		// 优先同进程 re-exec（PID 不变，systemd 无感，亚秒恢复）。容器里 PID 1 也不变，容器不用重启
		if err := reexecFn(l.Bin, env); err != nil {
			resume() // 进程接着跑，恢复派发
			if l.Container {
				// 程序没跑起来，不算一次启动；下次容器启动由启动接管换过去
				_ = saveBoot(osFS{}, l.RuntimeDir, bootState{Version: version})
				failUpdate("重载失败: " + err.Error() + "。新版本已写入数据卷，执行 docker restart cdk-recharge 即可启用；要退回：" + containerRollbackHint)
				return
			}
			// 回退 systemctl restart
			_ = trySystemctlRestart()
			// 若仍失败，标记 failed（进程若还活着）
			failUpdate("重载失败: " + err.Error() + "（已尝试 systemctl restart）")
		}
	})
}

// updateTargetRe 目标版本只允许数字、字母、点和连字符（parseVersion 对预发布段放得比较宽）。
var updateTargetRe = regexp.MustCompile(`^[0-9A-Za-z.\-]+$`)

// 可替换的文件操作，测试用来模拟磁盘满、改名失败。
var (
	updCopyFile = copyFile
	updRename   = os.Rename
)

// swapBinaryAndWeb 把新二进制和新 web 换到位，保证「要么都是新版，要么都是旧版」：
//  1. 先把新二进制写成 bin.new、新 web 写成 web.next（这一步失败，现网文件一个都没动）；
//  2. 保留旧二进制为 bin.old，再把 bin.new 改名覆盖 bin；
//  3. 最后 web -> web.prev、web.next -> web；失败则把 web 和二进制都恢复成旧的。
//
// 成功后清理 bin.old、web.prev。不动 data/app.env。
func swapBinaryAndWeb(l updateLayout, binSrc, webSrc string) error {
	binDst := l.Bin
	binNew, binOld := binDst+".new", binDst+".old"
	wd := l.Web
	webNext, webPrev := wd+".next", wd+".prev"

	cleanup := func() {
		_ = os.Remove(binNew)
		_ = os.RemoveAll(webNext)
	}
	_ = os.Remove(binNew)
	_ = os.Remove(binOld)
	_ = os.RemoveAll(webNext)
	_ = os.RemoveAll(webPrev)

	if err := updCopyFile(binSrc, binNew); err != nil {
		cleanup()
		return errors.New("写入新二进制失败: " + err.Error())
	}
	if err := os.Chmod(binNew, 0o755); err != nil {
		cleanup()
		return errors.New("设置新二进制权限失败: " + err.Error())
	}
	if err := copyDir(webSrc, webNext); err != nil {
		cleanup()
		return errors.New("写入 web.next 失败: " + err.Error())
	}

	// 换二进制：旧的先留一份，回退用
	hadBin := false
	if _, err := os.Stat(binDst); err == nil {
		hadBin = true
		if err := updCopyFile(binDst, binOld); err != nil {
			cleanup()
			_ = os.Remove(binOld)
			return errors.New("备份旧二进制失败: " + err.Error())
		}
	}
	if err := updRename(binNew, binDst); err != nil {
		cleanup()
		_ = os.Remove(binOld)
		return errors.New("替换二进制失败: " + err.Error())
	}
	restoreBin := func() {
		if hadBin {
			if err := updRename(binOld, binDst); err != nil {
				log.Printf("[update] 回退旧二进制失败，旧文件仍在 %s: %v", binOld, err)
			}
		} else {
			_ = os.Remove(binDst)
		}
	}

	// 换 web
	hadWeb := false
	if _, err := os.Stat(wd); err == nil {
		hadWeb = true
		if err := updRename(wd, webPrev); err != nil {
			restoreBin()
			cleanup()
			return errors.New("切换 web 失败，已回退二进制: " + err.Error())
		}
	}
	if err := updRename(webNext, wd); err != nil {
		if hadWeb {
			_ = updRename(webPrev, wd)
		}
		restoreBin()
		cleanup()
		return errors.New("启用新 web 失败，已回退二进制和 web: " + err.Error())
	}
	_ = os.RemoveAll(webPrev)
	_ = os.Remove(binOld)
	return nil
}

// updateGOOS 是判断「只支持 linux」用的系统名；测试可改。
var updateGOOS = runtime.GOOS

// checkNoPaymentsInFlight 有在途付款单（已离开排队、还没进入确认）时返回拒绝更新的提示。确认中、排队的单不挡：重启后能接着跑。
// 查不到时也拒绝：宁可这次不更新，也不打断正在付款的单。
func checkNoPaymentsInFlight(ctx context.Context) error {
	n, err := payengine.CountProcessing(ctx)
	if err != nil {
		log.Printf("[update] 查询在途付款单失败: %v", err)
		return errors.New("查不到付款单状态，稍后再更新")
	}
	if n > 0 {
		return fmt.Errorf("有 %d 张付款单正在处理，稍后再更新", n)
	}
	return nil
}

// drainPayments 在 re-exec 之前停止派发新付款单，等在途单进入确认或结束（最多 payengine.DrainMax）。
// 返回恢复派发的函数：重载失败、进程接着跑时调用。付款引擎没启动时什么也不做。
func drainPayments() (resume func()) {
	engine := payEngine
	if engine == nil {
		return func() {}
	}
	left, resume := engine.Drain(payengine.DrainMax, payengine.DrainLogEvery)
	if left > 0 {
		log.Printf("[update] 还有 %d 张在途付款单没处理完，照旧重载，重启后由恢复逻辑接管", left)
	}
	return resume
}

func failUpdate(msg string) {
	setUpdateState(func(s *updateState) {
		s.Phase = "failed"
		s.Error = msg
		s.Message = msg
		s.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	})
	updMu.Lock()
	updRunning = false
	updMu.Unlock()
}

// reexecBinary 原地换成 bin 运行（进程号不变）。env 为空时沿用当前的环境变量。
func reexecBinary(bin string, env []string) error {
	path := bin
	if path == "" {
		var err error
		path, err = os.Executable()
		if err != nil {
			return err
		}
	}
	if p, err := filepath.EvalSymlinks(path); err == nil {
		path = p
	}
	args := os.Args
	if len(args) == 0 {
		args = []string{path}
	} else {
		args[0] = path
	}
	if env == nil {
		env = os.Environ()
	}
	return syscall.Exec(path, args, env)
}

func trySystemctlRestart() error {
	// 非阻塞：避免卡死在 restart 自己
	cmd := exec.Command("systemctl", "restart", "cdk-recharge")
	return cmd.Start()
}

func resolveReleaseBundle(target string) (assetURL, version, shaURL, sigURL string, err error) {
	repo := githubRepo()
	client := &http.Client{Timeout: 30 * time.Second}
	var apiURL string
	if target == "latest" {
		apiURL = githubAPIBase + "/repos/" + repo + "/releases/latest"
	} else {
		apiURL = githubAPIBase + "/repos/" + repo + "/releases/tags/v" + strings.TrimPrefix(target, "v")
	}
	req, _ := http.NewRequest(http.MethodGet, apiURL, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "cdk-recharge-system-updater")
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		msg := string(raw)
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return "", "", "", "", fmt.Errorf("GitHub release HTTP %d: %s", resp.StatusCode, msg)
	}
	var rel struct {
		TagName string         `json:"tag_name"`
		Assets  []releaseAsset `json:"assets"`
	}
	if err := json.Unmarshal(raw, &rel); err != nil {
		return "", "", "", "", err
	}
	version = strings.TrimPrefix(strings.TrimSpace(rel.TagName), "v")
	if version == "" {
		return "", "", "", "", fmt.Errorf("release 无 tag_name")
	}

	assetURL, shaURL, sigURL, ok := pickReleaseBundle(rel.Assets)
	if !ok {
		return "", "", "", "", fmt.Errorf("release v%s 未找到适用于本机架构的预编译包 "+bundlePrimaryName()+"（需同时带 .sha256 和 .sig）。请先用 git tag 触发 CI 发布，或手动上传 bundle 到 Release", version)
	}
	return assetURL, version, shaURL, sigURL, nil
}

// releaseAsset 是 Release 里的一个附件（名字和下载地址）。
type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// bundleComplete 发布包、.sha256、.sig 三者齐全。一键更新要求齐全：缺 sha256 或缺签名都会在更新时中止。
func bundleComplete(assets []releaseAsset) bool {
	_, sha, sig, ok := pickReleaseBundle(assets)
	return ok && sha != "" && sig != ""
}

// bundlePrimaryName 本机架构对应的发布包文件名（报错提示用）。
func bundlePrimaryName() string {
	if runtime.GOARCH == "arm64" {
		return "cdk-bundle-linux-arm64.tgz"
	}
	return "cdk-bundle-linux-amd64.tgz"
}

// pickReleaseBundle 从 Release 附件里挑本机架构的发布包（不认别的架构），并带上同名的 .sha256 和 .sig（可能为空）。
// 在线更新（resolveReleaseBundle）和版本页的「可一键更新」（fetchGitHubLatest，经 bundleComplete）共用这一套规则。
func pickReleaseBundle(assets []releaseAsset) (assetURL, shaURL, sigURL string, ok bool) {
	prefer := []string{
		"cdk-bundle-linux-amd64.tgz",
		"cdk-bundle-linux-x86_64.tgz",
		"cdk-bundle.tgz",
	}
	if runtime.GOARCH == "arm64" {
		prefer = []string{
			"cdk-bundle-linux-arm64.tgz",
			"cdk-bundle-linux-aarch64.tgz",
			"cdk-bundle.tgz",
		}
	}
	var shaCandidates []string
	for _, a := range assets {
		if strings.HasSuffix(strings.ToLower(a.Name), ".sha256") {
			shaCandidates = append(shaCandidates, a.URL)
		}
	}
	for _, want := range prefer {
		for _, a := range assets {
			if !strings.EqualFold(a.Name, want) {
				continue
			}
			for _, a2 := range assets {
				if strings.EqualFold(a2.Name, want+".sha256") {
					shaURL = a2.URL
				}
				if strings.EqualFold(a2.Name, want+".sig") {
					sigURL = a2.URL
				}
			}
			if shaURL == "" && len(shaCandidates) == 1 {
				shaURL = shaCandidates[0]
			}
			return a.URL, shaURL, sigURL, true
		}
	}
	return "", "", "", false
}

// 下载发布包的时限（G7）：总共最多 10 分钟，60 秒没有收到新数据就取消。测试可缩短。
var (
	downloadTotalTimeout = 10 * time.Minute
	downloadIdleTimeout  = 60 * time.Second
)

// shaFetchTimeout 是下载 sha256 校验文件的总时限（文件很小）。
const shaFetchTimeout = 60 * time.Second

// downloadFile 下载到 dest。总时间超过 downloadTotalTimeout，或 downloadIdleTimeout 内没有收到新数据（含等响应头），就取消并返回错误。
func downloadFile(url, dest string, progress func(done, total int64)) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), downloadTotalTimeout)
	defer cancel()
	var stalled atomic.Bool
	idle := time.AfterFunc(downloadIdleTimeout, func() {
		stalled.Store(true)
		cancel()
	})
	defer idle.Stop()
	defer func() {
		if err == nil {
			return
		}
		switch {
		case stalled.Load():
			err = fmt.Errorf("%v 没有收到新数据，已取消下载", downloadIdleTimeout)
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			err = fmt.Errorf("%v 没有下载完，已取消下载", downloadTotalTimeout)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "cdk-recharge-system-updater")
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, string(b))
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 32*1024)
	for {
		n, er := resp.Body.Read(buf)
		if n > 0 {
			idle.Reset(downloadIdleTimeout)
			if _, ew := f.Write(buf[:n]); ew != nil {
				return ew
			}
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if er == io.EOF {
			break
		}
		if er != nil {
			return er
		}
	}
	return f.Sync()
}

func verifySHA256File(tgzPath, shaURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), shaFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, shaURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "cdk-recharge-system-updater")
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("sha HTTP %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// 格式: <hex>  filename  或纯 hex
	line := strings.TrimSpace(string(raw))
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return fmt.Errorf("empty sha file")
	}
	want := strings.ToLower(fields[0])
	f, err := os.Open(tgzPath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("sha256 mismatch got=%s want=%s", got, want)
	}
	return nil
}

func extractTarGz(tgzPath, dest string) error {
	f, err := os.Open(tgzPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// 防 zip-slip：条目名含 ".." 直接拒绝（发布包里没有这种名字），再做下面的清洗与前缀检查
		if strings.Contains(hdr.Name, "..") {
			return fmt.Errorf("安装包里有非法路径: %s", hdr.Name)
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("非法路径: %s", hdr.Name)
		}
		target := filepath.Join(dest, name)
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) && target != filepath.Clean(dest) {
			return fmt.Errorf("非法解压路径: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)|0o200)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
	return nil
}

func locateBundleContents(extractDir string) (bin, web, versionFile string, err error) {
	// 允许顶层或单层目录包装
	candidates := []string{extractDir}
	entries, _ := os.ReadDir(extractDir)
	for _, e := range entries {
		if e.IsDir() {
			candidates = append(candidates, filepath.Join(extractDir, e.Name()))
		}
	}
	for _, root := range candidates {
		b := filepath.Join(root, "cdk-recharge")
		w := filepath.Join(root, "web")
		v := filepath.Join(root, "VERSION")
		if st, e := os.Stat(b); e == nil && !st.IsDir() {
			if st2, e2 := os.Stat(w); e2 == nil && st2.IsDir() {
				bin = b
				web = w
				if _, e3 := os.Stat(v); e3 == nil {
					versionFile = v
				}
				return bin, web, versionFile, nil
			}
		}
	}
	return "", "", "", fmt.Errorf("包内缺少 cdk-recharge 二进制或 web/ 目录（请使用 CI 产物 cdk-bundle-linux-amd64.tgz）")
}

// backupCurrentFiles 把当前在跑的程序、前端、版本号复制到备份目录 bak；源文件存在却复制失败就返回错误。
func backupCurrentFiles(l updateLayout, bak string) error {
	if _, err := os.Stat(l.CurBin); err == nil {
		if err := copyFile(l.CurBin, filepath.Join(bak, "cdk-recharge")); err != nil {
			return fmt.Errorf("备份当前程序: %w", err)
		}
	}
	if st, err := os.Stat(l.CurWeb); err == nil && st.IsDir() {
		if err := copyDir(l.CurWeb, filepath.Join(bak, "web")); err != nil {
			return fmt.Errorf("备份当前前端: %w", err)
		}
	}
	if _, err := os.Stat(l.CurVer); err == nil {
		if err := copyFile(l.CurVer, filepath.Join(bak, "VERSION")); err != nil {
			return fmt.Errorf("备份当前版本号: %w", err)
		}
	}
	return nil
}

// 测试可替换。
var (
	writeNewVersionFn   = writeNewVersion
	restoreFromBackupFn = restoreFromBackup
)

// restoreFromBackup 把备份目录 bak 里的旧程序和旧前端放回现网位置（先备好再换名，换名失败就保持原样）。
// 备份里没有旧程序时返回错误。版本号文件不用还原：writeNewVersion 先写临时文件再改名，失败时原 VERSION 原封不动。
func restoreFromBackup(l updateLayout, bak string) error {
	binBak := filepath.Join(bak, "cdk-recharge")
	if _, err := os.Stat(binBak); err != nil {
		return fmt.Errorf("备份里没有旧程序: %w", err)
	}
	binTmp := l.Bin + ".restore"
	_ = os.Remove(binTmp)
	if err := updCopyFile(binBak, binTmp); err != nil {
		_ = os.Remove(binTmp)
		return fmt.Errorf("还原程序: %w", err)
	}
	if err := os.Chmod(binTmp, 0o755); err != nil {
		_ = os.Remove(binTmp)
		return fmt.Errorf("还原程序权限: %w", err)
	}
	webBak := filepath.Join(bak, "web")
	var webTmp, webFailed string
	if st, err := os.Stat(webBak); err == nil && st.IsDir() {
		webTmp, webFailed = l.Web+".restore", l.Web+".failed"
		_ = os.RemoveAll(webTmp)
		_ = os.RemoveAll(webFailed)
		if err := copyDir(webBak, webTmp); err != nil {
			_ = os.RemoveAll(webTmp)
			_ = os.Remove(binTmp)
			return fmt.Errorf("还原前端: %w", err)
		}
	}
	if err := updRename(binTmp, l.Bin); err != nil {
		_ = os.Remove(binTmp)
		if webTmp != "" {
			_ = os.RemoveAll(webTmp)
		}
		return fmt.Errorf("还原程序: %w", err)
	}
	if webTmp != "" {
		hadWeb := false
		if _, err := os.Stat(l.Web); err == nil {
			hadWeb = true
			if err := updRename(l.Web, webFailed); err != nil {
				_ = os.RemoveAll(webTmp)
				return fmt.Errorf("还原前端（程序已还原）: %w", err)
			}
		}
		if err := updRename(webTmp, l.Web); err != nil {
			if hadWeb {
				_ = updRename(webFailed, l.Web)
			}
			_ = os.RemoveAll(webTmp)
			return fmt.Errorf("还原前端（程序已还原）: %w", err)
		}
		_ = os.RemoveAll(webFailed)
	}
	return nil
}

// manualRollbackHint 自动还原也失败时给管理员的手动回滚步骤：哪几个文件从哪个备份还原。
func manualRollbackHint(l updateLayout, bak string) string {
	if l.Container {
		// 容器：备份里的 VERSION 来自镜像目录，不是 runtime/VERSION，不能复制回去。
		// 启动接管看 runtime/VERSION：删掉它就不再接管、回到镜像自带的版本；要保留上一次升级的版本，就把它改回升级前的版本号。
		return fmt.Sprintf("手动回滚：停止容器，把备份目录 %s 里的 cdk-recharge 复制回 %s，web 目录复制回 %s；不要复制备份里的 VERSION（那是镜像自带的版本号）。%s 里的版本号如果不是升级前的，请改回升级前的版本号，或者删除它（删除后启动时不再接管，回到镜像自带的版本），再启动容器。",
			bak, l.Bin, l.Web, l.Ver)
	}
	return fmt.Sprintf("手动回滚：停止服务，把备份目录 %s 里的 cdk-recharge 复制回 %s，web 目录复制回 %s，VERSION 复制回 %s（备份里没有 VERSION 就删掉新写的），再启动服务。",
		bak, l.Bin, l.Web, l.Ver)
}

// writeNewVersion 写新版本号：包里带了 VERSION 就用它的内容，否则写 version。
// 先写同目录的临时文件再改名，写到一半失败（磁盘满等）时原 VERSION 原封不动，临时文件删掉。
func writeNewVersion(l updateLayout, verSrc, version string) error {
	data := []byte(version + "\n")
	if verSrc != "" {
		b, err := os.ReadFile(verSrc)
		if err != nil {
			return err
		}
		data = b
	}
	tmp := l.Ver + ".tmp"
	_ = os.Remove(tmp)
	if err := writeVersionFileFn(tmp, data); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := updRename(tmp, l.Ver); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// writeVersionFileFn 写版本号文件（测试可替换，用来模拟写到一半失败）。
var writeVersionFileFn = func(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

func humanBytes(n int64) string {
	if n <= 0 {
		return "—"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
