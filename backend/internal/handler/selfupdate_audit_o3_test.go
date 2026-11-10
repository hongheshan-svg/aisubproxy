package handler

import (
	"database/sql"
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

// 版本高于 1.5.3 的新程序：--version 之后跑 --check。真实库的绝对路径只走专用变量 CDK_CHECK_DB_PATH，
// DB_PATH 被换成打不开的路径（裁定 R17：误收 --check 的老程序打开库那一步就退出）。
func TestO3PreflightRunsCheckWithCurrentDBPath(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "check-ran")
	t.Setenv("O3_MARK", mark)
	t.Setenv("DB_PATH", "rel-data/cdk_recharge.db") // 相对路径：要换成绝对路径再交给新程序（新程序的工作目录不同）
	bin := filepath.Join(dir, "cdk-recharge")
	writeFakeBinary(t, bin, o3CheckScript("v1.6.0", `echo "$`+db.CheckDBPathEnv+`" > "$O3_MARK"; echo "$DB_PATH" >> "$O3_MARK"; echo 启动检查通过; exit 0`))

	if err := preflightBinary(bin, "1.6.0"); err != nil {
		t.Fatalf("--check 通过时预检应通过: %v", err)
	}
	b, err := os.ReadFile(mark)
	if err != nil {
		t.Fatalf("应运行 --check: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	want, _ := filepath.Abs(db.ResolvePath())
	if len(lines) != 2 || lines[0] != want || !filepath.IsAbs(lines[0]) {
		t.Fatalf("--check 拿到的 %s = %q，应为当前进程的数据库绝对路径 %q", db.CheckDBPathEnv, lines, want)
	}
	if lines[1] == want || lines[1] == "rel-data/cdk_recharge.db" {
		t.Fatalf("跑 --check 时 DB_PATH 不能是真实库路径: %q", lines[1])
	}
}

// 裁定 R17：一个自报高于 1.5.3、却不认识 --check 的程序（把 --check 当普通启动：先接管、再按 DB_PATH 建库、
// 再监听 SERVER_PORT）。它拿到的库路径不能是真实库，建库失败就退出；预检判失败、不替换文件，真实库一个字节都不动。
func TestO3PreflightOldBinaryTakingCheckAsStartNeverSeesRealDB(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "data", "cdk_recharge.db")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("real-db-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_PATH", real)
	t.Setenv("SERVER_PORT", "18080")
	t.Setenv("CDK_RUNTIME_HANDOFF", "")
	mark := filepath.Join(dir, "old-start")
	t.Setenv("O3_MARK", mark)
	bin := filepath.Join(dir, "cdk-recharge")
	// 假老程序：只认 --version，其余参数一律当普通启动
	writeFakeBinary(t, bin, `case "$1" in
--version) echo 1.6.0; exit 0;;
esac
{ echo "db=$DB_PATH"; echo "port=$SERVER_PORT"; echo "handoff=$CDK_RUNTIME_HANDOFF"; } > "$O3_MARK"
mkdir -p "$(dirname "$DB_PATH")" 2>/dev/null && echo opened >> "$DB_PATH" 2>/dev/null || {
  echo "Failed to initialize server: unable to open database file" >&2; exit 1; }
echo "Starting server on 0.0.0.0:$SERVER_PORT"
exit 0`)

	err := preflightBinary(bin, "1.6.0")
	if err == nil {
		t.Fatal("不认识 --check 的程序，预检必须失败")
	}
	if !strings.Contains(err.Error(), "--check") {
		t.Fatalf("错误应提示新程序可能不支持 --check: %v", err)
	}
	b, rerr := os.ReadFile(mark)
	if rerr != nil {
		t.Fatalf("假老程序应已运行: %v", rerr)
	}
	got := string(b)
	if strings.Contains(got, "db="+real) || strings.Contains(got, filepath.Dir(real)) {
		t.Fatalf("老程序拿到了真实库路径: %s", got)
	}
	if strings.Contains(got, "port=18080") || !strings.Contains(got, "handoff=1") {
		t.Fatalf("端口应换成不可用的值、启动接管应关掉: %s", got)
	}
	if data, _ := os.ReadFile(real); string(data) != "real-db-bytes" {
		t.Fatalf("真实库被动过: %q", data)
	}
}

// 防线用的库路径：目录建不出来、SQLite 也打不开；用完清理。
func TestO3CheckGuardDBPathUnopenable(t *testing.T) {
	p, cleanup, err := checkGuardDBPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
		t.Fatalf("防线路径的上级目录不应能建出来: %s", p)
	}
	c, err := sql.Open("sqlite3", p+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(); err == nil {
		c.Close()
		t.Fatalf("SQLite 不应能打开防线路径: %s", p)
	}
	c.Close()
	cleanup()
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
		t.Fatalf("用完应清理: %v", err)
	}
}

// 新程序 --check 退出 0 但没打印「启动检查通过」：不认为通过（老程序或别的东西冒充）。
func TestO3PreflightRequiresCheckOKLine(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cdk-recharge")
	writeFakeBinary(t, bin, o3CheckScript("1.6.0", `echo "Starting server"; exit 0`))
	if err := preflightBinary(bin, "1.6.0"); err == nil || !strings.Contains(err.Error(), "--check") {
		t.Fatalf("没有「启动检查通过」不能算通过: %v", err)
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
	bundle := buildBundle(t, "1.6.0", o3CheckScript("1.6.0", `echo "启动检查未通过: 旧版密钥迁移: 库里有旧版明文的 card_api_key 要加密迁移，需要配置合法的 APP_MASTER_KEY" >&2; exit 1`))
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
