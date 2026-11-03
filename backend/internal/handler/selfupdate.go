package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 在线更新的安全加固：发布包签名校验、升级前自检、数据库备份、回滚。

// releasePublicKeyB64 为发布签名公钥（ed25519，base64）。私钥只在 GitHub Actions 的
// RELEASE_SIGNING_KEY Secret 中；没有它签不出能被安装的发布包。测试会替换此值。
var releasePublicKeyB64 = "+surxd3pW5IwYSjEfHcrfYVVAQmpvBi1GSHskolwV0Q="

// githubAPIBase 为 GitHub API 地址，测试替换为本地模拟服务。
var githubAPIBase = "https://api.github.com"

// reexecFn 为替换文件后的重启方式，测试替换为记录调用。
var reexecFn = reexecBinary

// selfBinaryPath 返回正在运行的程序文件路径（解析软链接），测试替换为临时目录。
var selfBinaryPath = func() string {
	if exe, err := os.Executable(); err == nil {
		if p, err := filepath.EvalSymlinks(exe); err == nil {
			return p
		}
		return exe
	}
	return filepath.Join(installRoot(), "cdk-recharge")
}

// maxUpdateBackups 为保留的升级备份数，更早的自动删除，避免占满磁盘。
const maxUpdateBackups = 3

// containerMarkers 是 Docker / Podman 在容器里放的标记文件。测试会清空它，免得在容器里跑测试时走错分支。
var containerMarkers = []string{"/.dockerenv", "/run/.containerenv"}

// inContainer 判断是否运行在容器中：显式 CDK_DEPLOY_MODE=docker，或存在 Docker / Podman 的标记文件。
func inContainer() bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CDK_DEPLOY_MODE")), "docker") {
		return true
	}
	for _, p := range containerMarkers {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func releasePublicKey() (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(releasePublicKeyB64))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("内置签名公钥无效")
	}
	return ed25519.PublicKey(raw), nil
}

// verifyBundleSignature 校验发布包的 ed25519 签名（sigB64 为 base64，可带首尾空白）。
func verifyBundleSignature(data []byte, sigB64 string) error {
	pub, err := releasePublicKey()
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("签名文件格式不对")
	}
	if !ed25519.Verify(pub, data, sig) {
		return fmt.Errorf("签名与发布包不匹配")
	}
	return nil
}

// verifyBundleSignatureFile 下载签名文件并校验本地发布包。
func verifyBundleSignatureFile(tgzPath, sigURL string) error {
	req, err := http.NewRequest(http.MethodGet, sigURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "cdk-recharge-system-updater")
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载签名文件 HTTP %d", resp.StatusCode)
	}
	sig, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return err
	}
	data, err := os.ReadFile(tgzPath)
	if err != nil {
		return err
	}
	return verifyBundleSignature(data, string(sig))
}

// preflightBinary 在替换前运行新程序的 --version：确认它能在本机执行，且自报版本与发布版本一致。
// 在新程序所在目录运行，并去掉 CDK_APP_VERSION，避免读到旧版本号。
func preflightBinary(bin, wantVersion string) error {
	if err := os.Chmod(bin, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Dir = filepath.Dir(bin)
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CDK_APP_VERSION=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("新程序无法在本机运行: %v", err)
	}
	got := normalizeVer(strings.TrimSpace(strings.SplitN(out.String(), "\n", 2)[0]))
	if got != normalizeVer(wantVersion) {
		return fmt.Errorf("新程序自报版本 %s，与发布版本 %s 不一致", got, normalizeVer(wantVersion))
	}
	return nil
}

// backupDatabase 用 VACUUM INTO 生成一致的数据库快照；目标文件已存在时报错，不覆盖旧备份。
func backupDatabase(dst string) error {
	if db.DB == nil {
		return errors.New("数据库未初始化")
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("备份文件已存在: %s", dst)
	}
	_, err := db.DB.Exec(`VACUUM INTO ?`, dst)
	return err
}

// pruneBackups 只保留最新的 keep 个 backup-* 目录（目录名含时间戳，按名称排序即按时间排序）。
func pruneBackups(updDir string, keep int) {
	dirs, _ := filepath.Glob(filepath.Join(updDir, "backup-*"))
	sort.Strings(dirs)
	for len(dirs) > keep {
		_ = os.RemoveAll(dirs[0])
		dirs = dirs[1:]
	}
}

// latestBackup 返回最新的、含程序文件的备份目录。
func latestBackup(updDir string) (string, error) {
	dirs, _ := filepath.Glob(filepath.Join(updDir, "backup-*"))
	sort.Strings(dirs)
	for i := len(dirs) - 1; i >= 0; i-- {
		if st, err := os.Stat(filepath.Join(dirs[i], "cdk-recharge")); err == nil && !st.IsDir() {
			return dirs[i], nil
		}
	}
	return "", fmt.Errorf("%s 下没有可用的升级备份", updDir)
}

// rollbackPaths 是命令行回滚读写的位置。
type rollbackPaths struct {
	UpdDir  string // 升级备份所在目录；出问题的版本也留存到这里的 failed-*
	Bin     string // 要恢复的程序
	Web     string // 要恢复的前端目录
	Ver     string // 要恢复的版本号文件
	Restart string // 回滚后让管理员执行的重启命令
}

// RollbackLatest 用最新的升级备份恢复程序、前端文件和版本号；出问题的当前版本移到 updates/failed-*。
// 数据库不自动恢复（迁移只加不改，旧版本可以直接用新库），备份路径会打印出来。
func RollbackLatest(root, binPath, web string, w io.Writer) error {
	_, err := rollbackWith(rollbackPaths{
		UpdDir:  filepath.Join(root, updatesDirName),
		Bin:     binPath,
		Web:     web,
		Ver:     filepath.Join(root, "VERSION"),
		Restart: "systemctl restart cdk-recharge",
	}, w)
	return err
}

// rollbackWith 按 p 执行回滚，返回恢复到的版本号（备份里没有 VERSION 时为「未知」）。
func rollbackWith(p rollbackPaths, w io.Writer) (string, error) {
	bak, err := latestBackup(p.UpdDir)
	if err != nil {
		return "", err
	}
	failed := filepath.Join(p.UpdDir, "failed-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(failed, 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(p.Bin); err == nil {
		if err := copyFile(p.Bin, filepath.Join(failed, "cdk-recharge")); err != nil {
			return "", fmt.Errorf("留存当前程序失败: %w", err)
		}
	}
	if _, err := os.Stat(p.Ver); err == nil {
		_ = copyFile(p.Ver, filepath.Join(failed, "VERSION"))
	}

	// 程序：先写 .rollback 再改名覆盖，运行中的进程仍用旧 inode
	tmp := p.Bin + ".rollback"
	if err := copyFile(filepath.Join(bak, "cdk-recharge"), tmp); err != nil {
		return "", fmt.Errorf("写入备份程序失败: %w", err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p.Bin); err != nil {
		return "", fmt.Errorf("恢复程序失败: %w", err)
	}
	// 前端
	if st, err := os.Stat(filepath.Join(bak, "web")); err == nil && st.IsDir() {
		if _, err := os.Stat(p.Web); err == nil {
			if err := os.Rename(p.Web, filepath.Join(failed, "web")); err != nil {
				return "", fmt.Errorf("移走当前前端文件失败: %w", err)
			}
		}
		if err := copyDir(filepath.Join(bak, "web"), p.Web); err != nil {
			return "", fmt.Errorf("恢复前端文件失败: %w", err)
		}
	}
	// 版本号
	restored := "未知"
	if b, err := os.ReadFile(filepath.Join(bak, "VERSION")); err == nil {
		if err := os.WriteFile(p.Ver, b, 0o644); err != nil {
			return "", err
		}
		restored = strings.TrimSpace(string(b))
	}

	fmt.Fprintf(w, "已回滚到 v%s（备份 %s），出问题的版本留存在 %s\n", restored, bak, failed)
	if _, err := os.Stat(filepath.Join(bak, "cdk_recharge.db")); err == nil {
		fmt.Fprintf(w, "升级前的数据库备份：%s（未自动恢复；新库可直接被旧版本使用）\n", filepath.Join(bak, "cdk_recharge.db"))
	}
	fmt.Fprintln(w, "请执行："+p.Restart)
	return restored, nil
}

// rollbackRuntime 是容器内的回滚：把 <数据目录>/updates 里最新的升级备份恢复到 <数据目录>/runtime，
// 并删掉 boot.json，让恢复后的版本重新计启动次数。image 为镜像里的版本，为空表示不知道。
func rollbackRuntime(data, image string, w io.Writer) error {
	rt := runtimeDirOf(data)
	restored, err := rollbackWith(rollbackPaths{
		UpdDir:  filepath.Join(data, updatesDirName),
		Bin:     filepath.Join(rt, "cdk-recharge"),
		Web:     filepath.Join(rt, "web"),
		Ver:     filepath.Join(rt, "VERSION"),
		Restart: "docker restart cdk-recharge",
	}, w)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(rt, bootFileName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("清除 %s 失败: %w", bootFileName, err)
	}
	if c, ok := compareVersions(restored, image); ok && c <= 0 {
		v := strings.TrimPrefix(restored, "v")
		fmt.Fprintf(w, "注意：恢复的 v%s 不比镜像里的 v%s 新，重启后仍以镜像版本运行，数据卷里的 runtime 会被删掉。"+
			"要换回 v%s，请改用该版本的镜像：deploy/docker-deploy.sh --version %s\n", v, image, v, v)
	}
	return nil
}

// Rollback 用于命令行 `cdk-recharge rollback`。容器内（docker exec 运行镜像里的程序）作用于数据卷的 runtime 目录。
func Rollback(w io.Writer) error {
	if inContainer() {
		data := dataDir()
		exe := currentExe()
		image := ""
		if !isWithin(exe, runtimeDirOf(data)) {
			image = imageVersion(exe)
		}
		return rollbackRuntime(data, image, w)
	}
	return RollbackLatest(installRoot(), selfBinaryPath(), webDir(), w)
}

// LocalVersion 返回本程序的版本号（用于 `cdk-recharge --version`）。
func LocalVersion() string {
	return resolveLocalVersion()
}

// updateHint 说明为什么不能在线更新（为空表示可以）。
func updateHint() string {
	if !updateEnabled() {
		return "已设置 UPDATE_ENABLED=0，一键更新已关闭"
	}
	return ""
}
