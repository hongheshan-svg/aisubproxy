package handler

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// useTestReleaseKey 换成测试生成的签名公钥，返回对应私钥。
func useTestReleaseKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	old := releasePublicKeyB64
	releasePublicKeyB64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { releasePublicKeyB64 = old })
	return priv
}

func signB64(priv ed25519.PrivateKey, data []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))
}

func TestDefaultGitHubRepoIsOurs(t *testing.T) {
	t.Setenv("CDK_GITHUB_REPO", "")
	if got := githubRepo(); got != "hongheshan-svg/aisubproxy" {
		t.Fatalf("githubRepo() = %q", got)
	}
}

func TestReleasePublicKeyValid(t *testing.T) {
	pub, err := releasePublicKey()
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("内置公钥无效: %v", err)
	}
}

func TestVerifyBundleSignature(t *testing.T) {
	priv := useTestReleaseKey(t)
	data := []byte("bundle bytes")
	if err := verifyBundleSignature(data, signB64(priv, data)); err != nil {
		t.Fatalf("正确签名应通过: %v", err)
	}
	if err := verifyBundleSignature([]byte("bundle bytez"), signB64(priv, data)); err == nil {
		t.Fatal("内容被改动应失败")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if err := verifyBundleSignature(data, signB64(other, data)); err == nil {
		t.Fatal("别的私钥签名应失败")
	}
	for _, bad := range []string{"", "not-base64!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if err := verifyBundleSignature(data, bad); err == nil {
			t.Fatalf("签名 %q 应失败", bad)
		}
	}
	// 签名文件末尾的换行要能容忍
	if err := verifyBundleSignature(data, signB64(priv, data)+"\n"); err != nil {
		t.Fatalf("带换行的签名应通过: %v", err)
	}
}

func TestUpdateDisabledInContainer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("UPDATE_ENABLED", "1")
	t.Setenv("CDK_DEPLOY_MODE", "docker")
	if updateEnabled() {
		t.Fatal("容器内应禁用在线更新")
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/update", strings.NewReader(`{"confirm":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	AdminSystemUpdate(c)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "Docker") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func writeFakeBinary(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cdk-recharge")
	writeFakeBinary(t, bin, `[ "$1" = "--version" ] && echo "v1.5.1" && exit 0; exit 3`)
	if err := preflightBinary(bin, "1.5.1"); err != nil {
		t.Fatalf("版本一致应通过: %v", err)
	}
	if err := preflightBinary(bin, "1.5.2"); err == nil || !strings.Contains(err.Error(), "1.5.1") {
		t.Fatalf("版本不一致应失败并说明实际版本: %v", err)
	}
	writeFakeBinary(t, bin, "exit 1")
	if err := preflightBinary(bin, "1.5.1"); err == nil {
		t.Fatal("退出码非 0 应失败")
	}
	if err := os.WriteFile(bin, []byte("\x7fELF garbage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := preflightBinary(bin, "1.5.1"); err == nil {
		t.Fatal("无法执行的文件应失败")
	}
}

func TestBackupDatabaseVacuumInto(t *testing.T) {
	dbtest.Open(t)
	if err := db.SetSetting("probe_key", "probe_value"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "backup", "cdk_recharge.db")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := backupDatabase(dst); err != nil {
		t.Fatalf("backupDatabase: %v", err)
	}
	c, err := sql.Open("sqlite3", dst)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var v string
	if err := c.QueryRow(`SELECT value FROM site_settings WHERE key='probe_key'`).Scan(&v); err != nil || v != "probe_value" {
		t.Fatalf("备份库内容不对: %q %v", v, err)
	}
	if err := backupDatabase(dst); err == nil {
		t.Fatal("目标文件已存在时应报错，不覆盖旧备份")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestRollbackLatest(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "cdk-recharge")
	web := filepath.Join(root, "web")
	mustWrite(t, bin, "new-bin")
	mustWrite(t, filepath.Join(web, "index.html"), "new-web")
	mustWrite(t, filepath.Join(root, "VERSION"), "1.5.1\n")
	older := filepath.Join(root, "updates", "backup-20261027-100000")
	mustWrite(t, filepath.Join(older, "cdk-recharge"), "very-old-bin")
	newest := filepath.Join(root, "updates", "backup-20261028-100000")
	mustWrite(t, filepath.Join(newest, "cdk-recharge"), "old-bin")
	mustWrite(t, filepath.Join(newest, "web", "index.html"), "old-web")
	mustWrite(t, filepath.Join(newest, "VERSION"), "1.5.0\n")

	var out bytes.Buffer
	if err := RollbackLatest(root, bin, web, &out); err != nil {
		t.Fatalf("RollbackLatest: %v", err)
	}
	if readFile(t, bin) != "old-bin" || readFile(t, filepath.Join(web, "index.html")) != "old-web" ||
		strings.TrimSpace(readFile(t, filepath.Join(root, "VERSION"))) != "1.5.0" {
		t.Fatal("没有恢复到最新的备份")
	}
	if st, err := os.Stat(bin); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("恢复后的程序应可执行: %v", err)
	}
	failed, _ := filepath.Glob(filepath.Join(root, "updates", "failed-*", "cdk-recharge"))
	if len(failed) != 1 || readFile(t, failed[0]) != "new-bin" {
		t.Fatalf("出问题的版本应留存到 failed-*：%v", failed)
	}
	if !strings.Contains(out.String(), "1.5.0") || !strings.Contains(out.String(), "systemctl restart") {
		t.Fatalf("输出应说明恢复的版本和下一步：%s", out.String())
	}
}

func TestRollbackWithoutBackupFails(t *testing.T) {
	root := t.TempDir()
	if err := RollbackLatest(root, filepath.Join(root, "cdk-recharge"), filepath.Join(root, "web"), &bytes.Buffer{}); err == nil {
		t.Fatal("没有备份时应报错")
	}
}

func TestPruneBackupsKeepsNewest(t *testing.T) {
	upd := t.TempDir()
	for _, n := range []string{"backup-20261001-000000", "backup-20261002-000000", "backup-20261003-000000", "backup-20261004-000000", "failed-20261001-000000"} {
		if err := os.MkdirAll(filepath.Join(upd, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pruneBackups(upd, 3)
	left, _ := filepath.Glob(filepath.Join(upd, "backup-*"))
	if len(left) != 3 || filepath.Base(left[0]) != "backup-20261002-000000" {
		t.Fatalf("应保留最新 3 个备份：%v", left)
	}
	if _, err := os.Stat(filepath.Join(upd, "failed-20261001-000000")); err != nil {
		t.Fatal("不应删除 failed-* 目录")
	}
}

// buildBundle 生成与 CI 同结构的发布包：cdk-recharge + web/ + VERSION。
func buildBundle(t *testing.T, version, binScript string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, body string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), ModTime: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("cdk-recharge", 0o755, "#!/bin/sh\n"+binScript+"\n")
	if err := tw.WriteHeader(&tar.Header{Name: "web/", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: time.Now()}); err != nil {
		t.Fatal(err)
	}
	add("web/index.html", 0o644, "web-"+version)
	add("VERSION", 0o644, version+"\n")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type fakeRelease struct {
	version string
	bundle  []byte
	sig     string // 为空则不提供签名文件
}

func startFakeGitHub(t *testing.T, rel fakeRelease) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(rel.bundle)
	assets := []map[string]any{
		{"name": "cdk-bundle-linux-amd64.tgz", "browser_download_url": srv.URL + "/dl/bundle.tgz", "size": len(rel.bundle)},
		{"name": "cdk-bundle-linux-amd64.tgz.sha256", "browser_download_url": srv.URL + "/dl/bundle.sha256"},
	}
	if rel.sig != "" {
		assets = append(assets, map[string]any{"name": "cdk-bundle-linux-amd64.tgz.sig", "browser_download_url": srv.URL + "/dl/bundle.sig"})
	}
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v" + rel.version, "assets": assets})
	})
	mux.HandleFunc("/dl/bundle.tgz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(rel.bundle) })
	mux.HandleFunc("/dl/bundle.sha256", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  cdk-bundle-linux-amd64.tgz\n"))
	})
	mux.HandleFunc("/dl/bundle.sig", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(rel.sig + "\n")) })
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
}

// setupInstall 准备一个已安装 v1.5.0 的目录，并把重启替换成记录调用。
func setupInstall(t *testing.T) (root, bin string, reexecCalled chan string) {
	t.Helper()
	dbtest.Open(t)
	root = t.TempDir()
	bin = filepath.Join(root, "cdk-recharge")
	mustWrite(t, bin, "#!/bin/sh\necho 1.5.0\n")
	mustWrite(t, filepath.Join(root, "web", "index.html"), "web-1.5.0")
	mustWrite(t, filepath.Join(root, "VERSION"), "1.5.0\n")
	t.Setenv("CDK_INSTALL_DIR", root)
	t.Setenv("WEB_DIR", filepath.Join(root, "web"))
	t.Setenv("CDK_GITHUB_REPO", "o/r")
	t.Setenv("CDK_DEPLOY_MODE", "")
	oldBin, oldExec := selfBinaryPath, reexecFn
	selfBinaryPath = func() string { return bin }
	reexecCalled = make(chan string, 1)
	reexecFn = func(p string) error { reexecCalled <- p; return nil }
	t.Cleanup(func() { selfBinaryPath, reexecFn = oldBin, oldExec })
	updMu.Lock()
	updRunning, updSnap = true, updateState{Phase: "checking"}
	updMu.Unlock()
	t.Cleanup(func() {
		updMu.Lock()
		updRunning, updSnap = false, updateState{Phase: "idle"}
		updMu.Unlock()
	})
	return root, bin, reexecCalled
}

func TestSeamlessUpdateEndToEnd(t *testing.T) {
	priv := useTestReleaseKey(t)
	bundle := buildBundle(t, "1.5.1", `[ "$1" = "--version" ] && echo 1.5.1`)
	startFakeGitHub(t, fakeRelease{version: "1.5.1", bundle: bundle, sig: signB64(priv, bundle)})
	root, bin, reexec := setupInstall(t)
	if err := db.SetSetting("probe_key", "kept"); err != nil {
		t.Fatal(err)
	}

	runSeamlessUpdate("latest", "admin")

	select {
	case p := <-reexec:
		if p != bin {
			t.Fatalf("重启的程序路径 = %q", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("没有触发重启，state=%+v", getUpdateState())
	}
	if !strings.Contains(readFile(t, bin), "1.5.1") || readFile(t, filepath.Join(root, "web", "index.html")) != "web-1.5.1" ||
		strings.TrimSpace(readFile(t, filepath.Join(root, "VERSION"))) != "1.5.1" {
		t.Fatal("程序、前端或版本号没有替换")
	}
	baks, _ := filepath.Glob(filepath.Join(root, "updates", "backup-*"))
	if len(baks) != 1 {
		t.Fatalf("应有 1 个备份：%v", baks)
	}
	for _, f := range []string{"cdk-recharge", "VERSION", "cdk_recharge.db", filepath.Join("web", "index.html")} {
		if _, err := os.Stat(filepath.Join(baks[0], f)); err != nil {
			t.Fatalf("备份缺少 %s", f)
		}
	}
	if strings.TrimSpace(readFile(t, filepath.Join(baks[0], "VERSION"))) != "1.5.0" {
		t.Fatal("备份里的 VERSION 应是旧版本")
	}
}

func assertUpdateFailedUnchanged(t *testing.T, root, bin, wantMsg string, reexec chan string) {
	t.Helper()
	st := getUpdateState()
	if st.Phase != "failed" || !strings.Contains(st.Message+st.Error, wantMsg) {
		t.Fatalf("应失败并提示 %q，state=%+v", wantMsg, st)
	}
	select {
	case <-reexec:
		t.Fatal("失败时不应重启")
	default:
	}
	if !strings.Contains(readFile(t, bin), "1.5.0") || readFile(t, filepath.Join(root, "web", "index.html")) != "web-1.5.0" {
		t.Fatal("失败时不应改动已安装的文件")
	}
}

func TestSeamlessUpdateRejectsBadSignature(t *testing.T) {
	useTestReleaseKey(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	bundle := buildBundle(t, "1.5.1", `echo 1.5.1`)
	startFakeGitHub(t, fakeRelease{version: "1.5.1", bundle: bundle, sig: signB64(other, bundle)})
	root, bin, reexec := setupInstall(t)
	runSeamlessUpdate("latest", "admin")
	assertUpdateFailedUnchanged(t, root, bin, "签名", reexec)
}

func TestSeamlessUpdateRejectsMissingSignature(t *testing.T) {
	useTestReleaseKey(t)
	bundle := buildBundle(t, "1.5.1", `echo 1.5.1`)
	startFakeGitHub(t, fakeRelease{version: "1.5.1", bundle: bundle})
	root, bin, reexec := setupInstall(t)
	runSeamlessUpdate("latest", "admin")
	assertUpdateFailedUnchanged(t, root, bin, "签名", reexec)
}

func TestSeamlessUpdateRejectsBinaryThatFailsPreflight(t *testing.T) {
	priv := useTestReleaseKey(t)
	bundle := buildBundle(t, "1.5.1", `echo 1.4.0`) // 自报版本与发布版本不一致
	startFakeGitHub(t, fakeRelease{version: "1.5.1", bundle: bundle, sig: signB64(priv, bundle)})
	root, bin, reexec := setupInstall(t)
	runSeamlessUpdate("latest", "admin")
	assertUpdateFailedUnchanged(t, root, bin, "自检", reexec)
}
