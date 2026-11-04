package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 审计 O3 组 auth N-2：在线更新换文件之前，先让新程序跑 --check（启动检查，只读）；
// 不通过就中止更新、把原因显示在更新页，免得换上去以后起不来、systemd 崩溃循环。
// 全部用临时目录和假程序（shell 脚本），不访问 GitHub，不碰真实安装目录。

// o3CheckScript 是假的新程序：--version 报 version，--check 执行 check 这段脚本。
func o3CheckScript(version, check string) string {
	return `case "$1" in
--version) echo ` + version + `;;
--check) ` + check + `;;
*) echo "不应以其他参数运行: $1" >&2; exit 9;;
esac`
}

// 版本高于 1.5.3 的新程序：--version 之后跑 --check，带上当前进程实际用的数据库绝对路径。
func TestO3PreflightRunsCheckWithCurrentDBPath(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "check-ran")
	t.Setenv("O3_MARK", mark)
	t.Setenv("DB_PATH", "rel-data/cdk_recharge.db") // 相对路径：要换成绝对路径再交给新程序（新程序的工作目录不同）
	bin := filepath.Join(dir, "cdk-recharge")
	writeFakeBinary(t, bin, o3CheckScript("v1.6.0", `echo "$DB_PATH" > "$O3_MARK"; echo 启动检查通过; exit 0`))

	if err := preflightBinary(bin, "1.6.0"); err != nil {
		t.Fatalf("--check 通过时预检应通过: %v", err)
	}
	b, err := os.ReadFile(mark)
	if err != nil {
		t.Fatalf("应运行 --check: %v", err)
	}
	want, _ := filepath.Abs(db.ResolvePath())
	if got := strings.TrimSpace(string(b)); got != want || !filepath.IsAbs(got) {
		t.Fatalf("--check 拿到的 DB_PATH = %q，应为当前进程的数据库绝对路径 %q", got, want)
	}
}

// --check 不通过：预检失败，错误里带新程序给出的原因。
func TestO3PreflightCheckFailureCarriesReason(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cdk-recharge")
	writeFakeBinary(t, bin, o3CheckScript("1.6.0", `echo "启动检查未通过: 不安全的 JWT_SECRET：release 模式必须设置至少 16 位的随机 JWT_SECRET 环境变量" >&2; exit 1`))
	err := preflightBinary(bin, "1.6.0")
	if err == nil || !strings.Contains(err.Error(), "不安全的 JWT_SECRET") {
		t.Fatalf("--check 失败应中止并带上原因: %v", err)
	}
}

// 1.5.3 及更早的程序没有 --check：它们会把不认识的参数当成普通启动（连真实数据库、跑迁移、启动付款引擎），
// 绝不能对它们运行 --check，只做 --version 自检（与以前一致）。
func TestO3PreflightSkipsCheckForVersionsWithoutIt(t *testing.T) {
	for _, v := range []string{"1.5.3", "1.5.2", "1.4.9"} {
		dir := t.TempDir()
		mark := filepath.Join(dir, "started")
		t.Setenv("O3_MARK", mark)
		bin := filepath.Join(dir, "cdk-recharge")
		writeFakeBinary(t, bin, o3CheckScript(v, `touch "$O3_MARK"; exit 0`))
		if err := preflightBinary(bin, v); err != nil {
			t.Fatalf("v%s 预检应只看 --version 并通过: %v", v, err)
		}
		if _, err := os.Stat(mark); err == nil {
			t.Fatalf("v%s 没有 --check，不能用 --check 运行它", v)
		}
	}
}

// 端到端（原生部署）：新版本 --check 不通过，更新中止，状态里有原因，已安装的文件不动、不重启。
func TestO3SeamlessUpdateAbortsWhenCheckFails(t *testing.T) {
	priv := useTestReleaseKey(t)
	bundle := buildBundle(t, "1.6.0", o3CheckScript("1.6.0", `echo "启动检查未通过: 卡台密钥迁移: 库里有旧版明文的 card_api_key 要加密迁移，需要配置合法的 APP_MASTER_KEY" >&2; exit 1`))
	startFakeGitHub(t, fakeRelease{version: "1.6.0", bundle: bundle, sig: signB64(priv, bundle)})
	root, bin, reexec := setupInstall(t)
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "cdk_recharge.db"))

	runSeamlessUpdate("latest", "admin")

	assertUpdateFailedUnchanged(t, root, bin, "APP_MASTER_KEY", reexec)
	if st := getUpdateState(); !strings.Contains(st.Error, "card_api_key") {
		t.Fatalf("更新页应显示新程序给出的原因: %+v", st)
	}
	if baks, _ := filepath.Glob(filepath.Join(root, "updates", "backup-*")); len(baks) != 0 {
		t.Fatalf("预检失败时还没开始备份、替换: %v", baks)
	}
}

// 端到端（原生部署）：新版本 --check 通过，照常替换并重启。
func TestO3SeamlessUpdateProceedsWhenCheckPasses(t *testing.T) {
	priv := useTestReleaseKey(t)
	bundle := buildBundle(t, "1.6.0", o3CheckScript("1.6.0", `echo 启动检查通过; exit 0`))
	startFakeGitHub(t, fakeRelease{version: "1.6.0", bundle: bundle, sig: signB64(priv, bundle)})
	root, bin, reexec := setupInstall(t)
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "cdk_recharge.db"))

	runSeamlessUpdate("latest", "admin")

	select {
	case p := <-reexec:
		if p != bin {
			t.Fatalf("重启的程序路径 = %q", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("没有触发重启，state=%+v", getUpdateState())
	}
	if strings.TrimSpace(readFile(t, filepath.Join(root, "VERSION"))) != "1.6.0" {
		t.Fatal("版本号没有替换")
	}
}
