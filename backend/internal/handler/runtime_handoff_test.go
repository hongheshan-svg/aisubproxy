package handler

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---- 启动接管 ----

type execCall struct {
	argv0 string
	argv  []string
	env   []string
}

// handoffFixture 是一个容器的样子：镜像目录（程序 + VERSION）和数据目录（runtime/、updates/）。
type handoffFixture struct {
	t       *testing.T
	image   string // 镜像目录，相当于 /app
	data    string // 数据目录，相当于 /app/data
	rt      string
	execs   []execCall
	execErr error
	env     map[string]string
	logs    []string
	fs      handoffFS
	now     time.Time
}

func newHandoffFixture(t *testing.T) *handoffFixture {
	f := &handoffFixture{t: t, image: t.TempDir(), data: t.TempDir(), env: map[string]string{}, fs: osFS{},
		now: time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)}
	f.rt = filepath.Join(f.data, "runtime")
	mustWrite(t, filepath.Join(f.image, "cdk-recharge"), "image-bin")
	return f
}

// writeRuntime 放一份完整的在线升级版本。
func (f *handoffFixture) writeRuntime(version string) {
	mustWrite(f.t, filepath.Join(f.rt, "cdk-recharge"), "runtime-bin-"+version)
	mustWrite(f.t, filepath.Join(f.rt, "web", "index.html"), "web-"+version)
	mustWrite(f.t, filepath.Join(f.rt, "VERSION"), version+"\n")
}

func (f *handoffFixture) deps(imageVersion string) handoffDeps {
	return handoffDeps{
		FS:      f.fs,
		DataDir: f.data,
		Exe:     filepath.Join(f.image, "cdk-recharge"),
		Args:    []string{"/app/cdk-recharge", "--flag"},
		Getenv:  func(k string) string { return f.env[k] },
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "WEB_DIR=/app/web", "CDK_APP_VERSION=" + imageVersion, "JWT_SECRET=s"}
		},
		ImageVersion: func() string { return imageVersion },
		Exec: func(argv0 string, argv, envv []string) error {
			f.execs = append(f.execs, execCall{argv0, argv, envv})
			return f.execErr
		},
		Now:  func() time.Time { return f.now },
		Logf: func(format string, args ...any) { f.logs = append(f.logs, format) },
	}
}

func (f *handoffFixture) boot() bootState {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.rt, bootFileName))
	if err != nil {
		f.t.Fatalf("读 boot.json: %v", err)
	}
	var st bootState
	if err := json.Unmarshal(b, &st); err != nil {
		f.t.Fatalf("boot.json 格式不对: %v\n%s", err, b)
	}
	return st
}

func (f *handoffFixture) writeBoot(st bootState) {
	f.t.Helper()
	if err := saveBoot(osFS{}, f.rt, st); err != nil {
		f.t.Fatal(err)
	}
}

type alertRecorder struct {
	alerts []string
	audits []string
}

func (f *handoffFixture) completed(asRuntime bool, rec *alertRecorder) {
	exe := filepath.Join(f.image, "cdk-recharge")
	env := map[string]string{}
	if asRuntime {
		env[runtimeHandoffEnv] = "1"
	}
	runtimeBootCompleted(bootCompletedDeps{
		FS:      f.fs,
		DataDir: f.data,
		Exe:     exe,
		Getenv:  func(k string) string { return env[k] },
		Now:     func() time.Time { return f.now.Add(time.Minute) },
		Alert:   func(s string) { rec.alerts = append(rec.alerts, s) },
		Audit:   func(action, detail string) { rec.audits = append(rec.audits, action+" "+detail) },
		Logf:    func(string, ...any) {},
	})
}

func TestHandoffExecsNewerRuntime(t *testing.T) {
	f := newHandoffFixture(t)
	f.writeRuntime("1.6.1")
	res := runtimeHandoff(f.deps("1.6.0"))
	if res.Action != handoffExeced || len(f.execs) != 1 {
		t.Fatalf("应接管：%+v execs=%v", res, f.execs)
	}
	c := f.execs[0]
	bin := filepath.Join(f.rt, "cdk-recharge")
	if c.argv0 != bin || !reflect.DeepEqual(c.argv, []string{bin, "--flag"}) {
		t.Fatalf("exec %q %q", c.argv0, c.argv)
	}
	want := []string{"PATH=/usr/bin", "JWT_SECRET=s", "WEB_DIR=" + filepath.Join(f.rt, "web"), "CDK_RUNTIME_HANDOFF=1", "CDK_APP_VERSION=1.6.1"}
	if !reflect.DeepEqual(c.env, want) {
		t.Fatalf("exec 的环境变量 =\n%q\n期望\n%q", c.env, want)
	}
	if st := f.boot(); st.Version != "1.6.1" || st.BootCount != 1 || st.LastAttemptAt == "" || st.Fallback != nil {
		t.Fatalf("接管前应把启动次数加 1：%+v", st)
	}
}

func TestHandoffPrunesStaleRuntime(t *testing.T) {
	for _, image := range []string{"1.6.1", "v1.6.1", "1.6.2", "1.7.0"} {
		t.Run(image, func(t *testing.T) {
			f := newHandoffFixture(t)
			f.writeRuntime("1.6.1")
			f.writeBoot(bootState{Version: "1.6.1", BootCount: 2})
			backup := filepath.Join(f.data, "updates", "backup-20261101-000000", "cdk-recharge")
			mustWrite(t, backup, "old")
			res := runtimeHandoff(f.deps(image))
			if res.Action != handoffPruned || len(f.execs) != 0 {
				t.Fatalf("镜像不旧时不应接管：%+v execs=%v", res, f.execs)
			}
			if _, err := os.Stat(f.rt); !os.IsNotExist(err) {
				t.Fatalf("过期的 runtime 应删除：%v", err)
			}
			if readFile(t, backup) != "old" {
				t.Fatal("updates/ 里的备份应保留")
			}
		})
	}
}

func TestHandoffFallsBackAfterThreeFailedBoots(t *testing.T) {
	f := newHandoffFixture(t)
	f.writeRuntime("1.6.1")
	// 连续 3 次接管都没能完成启动（进程起来前就崩了，没人清零）
	for i := 1; i <= maxRuntimeBootFailures; i++ {
		if res := runtimeHandoff(f.deps("1.6.0")); res.Action != handoffExeced {
			t.Fatalf("第 %d 次应接管：%+v", i, res)
		}
		if st := f.boot(); st.BootCount != i {
			t.Fatalf("第 %d 次后启动次数 = %d", i, st.BootCount)
		}
	}
	res := runtimeHandoff(f.deps("1.6.0"))
	if res.Action != handoffFellBack || len(f.execs) != maxRuntimeBootFailures {
		t.Fatalf("第 4 次应退回镜像版本：%+v execs=%d", res, len(f.execs))
	}
	st := f.boot()
	if st.Fallback == nil || st.Fallback.Reported || st.Fallback.Version != "1.6.1" || st.Fallback.ImageVersion != "1.6.0" ||
		!strings.Contains(st.Fallback.Reason, "在线升级的 v1.6.1 连续 3 次启动失败，已退回镜像版本 v1.6.0") {
		t.Fatalf("退回原因应记进 boot.json：%+v", st.Fallback)
	}
	if _, err := os.Stat(filepath.Join(f.rt, "cdk-recharge")); err != nil {
		t.Fatal("退回时不应删除 runtime（留给管理员回滚或排查）")
	}

	// 镜像版本起来后：告警并写审计，只报一次；启动次数不清零，下次重启仍退回
	var rec alertRecorder
	f.completed(false, &rec)
	if len(rec.alerts) != 1 || !strings.Contains(rec.alerts[0], "连续 3 次启动失败") || !strings.Contains(rec.alerts[0], containerRollbackHint) {
		t.Fatalf("应告警一次并给出回滚方法：%q", rec.alerts)
	}
	if len(rec.audits) != 1 || !strings.HasPrefix(rec.audits[0], "runtime_fallback ") {
		t.Fatalf("应写审计：%q", rec.audits)
	}
	if st := f.boot(); st.BootCount != maxRuntimeBootFailures || st.Fallback == nil || !st.Fallback.Reported {
		t.Fatalf("镜像版本起来后不应清零，应标记已报：%+v", st)
	}
	if res := runtimeHandoff(f.deps("1.6.0")); res.Action != handoffFellBack {
		t.Fatalf("重启后仍应退回：%+v", res)
	}
	f.completed(false, &rec)
	if len(rec.alerts) != 1 || len(rec.audits) != 1 {
		t.Fatalf("同一次退回不应重复告警：%q %q", rec.alerts, rec.audits)
	}
}

func TestHandoffBootCompletedResetsCount(t *testing.T) {
	f := newHandoffFixture(t)
	f.writeRuntime("1.6.1")
	if res := runtimeHandoff(f.deps("1.6.0")); res.Action != handoffExeced {
		t.Fatalf("%+v", res)
	}
	var rec alertRecorder
	f.completed(true, &rec) // runtime 程序完成启动
	if st := f.boot(); st.BootCount != 0 || st.LastOKAt == "" || st.Fallback != nil {
		t.Fatalf("完成启动后应清零：%+v", st)
	}
	if len(rec.alerts) != 0 {
		t.Fatalf("正常启动不应告警：%q", rec.alerts)
	}
	// 之后崩一次再起来，仍能接管
	if res := runtimeHandoff(f.deps("1.6.0")); res.Action != handoffExeced || f.boot().BootCount != 1 {
		t.Fatalf("清零后应重新计数：%+v %+v", res, f.boot())
	}
}

func TestHandoffNewVersionRestartsCount(t *testing.T) {
	f := newHandoffFixture(t)
	f.writeRuntime("1.6.2")
	// 上一个在线升级版本 1.6.1 曾连续失败；现在 runtime 已是 1.6.2
	f.writeBoot(bootState{Version: "1.6.1", BootCount: 3, Fallback: &bootFallback{Version: "1.6.1", Reported: true}})
	if res := runtimeHandoff(f.deps("1.6.0")); res.Action != handoffExeced {
		t.Fatalf("换了版本应重新计数并接管：%+v", res)
	}
	if st := f.boot(); st.Version != "1.6.2" || st.BootCount != 1 || st.Fallback != nil {
		t.Fatalf("%+v", st)
	}
}

func TestHandoffExecFailureFallsBackToImage(t *testing.T) {
	f := newHandoffFixture(t)
	f.writeRuntime("1.6.1")
	f.execErr = errors.New("permission denied")
	res := runtimeHandoff(f.deps("1.6.0"))
	if res.Action != handoffExecFailed || res.Err == nil || len(f.execs) != 1 {
		t.Fatalf("exec 失败应退回镜像版本：%+v", res)
	}
	st := f.boot()
	if st.BootCount != 0 {
		t.Fatalf("程序没跑起来不算一次启动失败，启动次数应恢复：%+v", st)
	}
	if st.Fallback == nil || !strings.Contains(st.Fallback.Reason, "permission denied") || !strings.Contains(st.Fallback.Reason, "noexec") {
		t.Fatalf("应记下 exec 失败的原因：%+v", st.Fallback)
	}
	var rec alertRecorder
	f.completed(false, &rec)
	if len(rec.alerts) != 1 || !strings.Contains(rec.alerts[0], "无法执行") {
		t.Fatalf("起来后应告警：%q", rec.alerts)
	}
}

func TestHandoffSkipsWhenAlreadyHandedOff(t *testing.T) {
	f := newHandoffFixture(t)
	f.writeRuntime("1.6.1")
	f.env[runtimeHandoffEnv] = "1"
	if res := runtimeHandoff(f.deps("1.6.0")); res.Action != handoffAlready || len(f.execs) != 0 {
		t.Fatalf("带了标记不应再接管：%+v", res)
	}
	if _, err := os.Stat(filepath.Join(f.rt, bootFileName)); !os.IsNotExist(err) {
		t.Fatal("不应改动 boot.json")
	}

	// 没带标记、但正在运行的就是 runtime 里的程序：同样不接管，更不能删掉自己
	delete(f.env, runtimeHandoffEnv)
	d := f.deps("1.6.1")
	d.Exe = filepath.Join(f.rt, "cdk-recharge")
	if res := runtimeHandoff(d); res.Action != handoffAlready || len(f.execs) != 0 {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(f.rt, "cdk-recharge")); err != nil {
		t.Fatal("不应删除正在运行的 runtime")
	}
}

func TestHandoffSkipsIncompleteRuntime(t *testing.T) {
	t.Run("没有 runtime", func(t *testing.T) {
		f := newHandoffFixture(t)
		if res := runtimeHandoff(f.deps("1.6.0")); res.Action != handoffNone || len(f.execs) != 0 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("没有 VERSION", func(t *testing.T) {
		f := newHandoffFixture(t)
		f.writeRuntime("1.6.1")
		_ = os.Remove(filepath.Join(f.rt, "VERSION"))
		if res := runtimeHandoff(f.deps("1.6.0")); res.Action != handoffNone || len(f.execs) != 0 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("缺程序文件", func(t *testing.T) {
		f := newHandoffFixture(t)
		f.writeRuntime("1.6.1")
		_ = os.Remove(filepath.Join(f.rt, "cdk-recharge"))
		if res := runtimeHandoff(f.deps("1.6.0")); res.Action != handoffSkipped || len(f.execs) != 0 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("缺前端目录", func(t *testing.T) {
		f := newHandoffFixture(t)
		f.writeRuntime("1.6.1")
		_ = os.RemoveAll(filepath.Join(f.rt, "web"))
		if res := runtimeHandoff(f.deps("1.6.0")); res.Action != handoffSkipped || len(f.execs) != 0 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("版本号无效", func(t *testing.T) {
		f := newHandoffFixture(t)
		f.writeRuntime("not-a-version")
		if res := runtimeHandoff(f.deps("1.6.0")); res.Action != handoffSkipped || len(f.execs) != 0 {
			t.Fatalf("%+v", res)
		}
		if _, err := os.Stat(f.rt); err != nil {
			t.Fatal("版本号无法比较时不应删除 runtime")
		}
	})
}

// failWriteFS 让 boot.json 写不进去。
type failWriteFS struct{ osFS }

func (failWriteFS) WriteFile(string, []byte, os.FileMode) error {
	return errors.New("read-only file system")
}

func TestHandoffSkipsWhenBootFileUnwritable(t *testing.T) {
	f := newHandoffFixture(t)
	f.writeRuntime("1.6.1")
	f.fs = failWriteFS{}
	res := runtimeHandoff(f.deps("1.6.0"))
	if res.Action != handoffSkipped || len(f.execs) != 0 {
		t.Fatalf("记不下启动次数时不应接管（没有失败保护）：%+v", res)
	}
}

func TestHandoffCorruptBootFileStartsOver(t *testing.T) {
	f := newHandoffFixture(t)
	f.writeRuntime("1.6.1")
	mustWrite(t, filepath.Join(f.rt, bootFileName), "{not json")
	if res := runtimeHandoff(f.deps("1.6.0")); res.Action != handoffExeced {
		t.Fatalf("%+v", res)
	}
	if st := f.boot(); st.BootCount != 1 || st.Version != "1.6.1" {
		t.Fatalf("%+v", st)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.6.1", "1.6.0", 1},
		{"1.6.0", "1.6.1", -1},
		{"1.10.0", "1.9.9", 1},
		{"v1.6.0", "1.6.0", 0},
		{"1.6", "1.6.0", 0},
		{"1.6.0+build.7", "1.6.0", 0},
		{"1.6.0-rc.1", "1.6.0", -1},
		{"1.6.0", "1.6.0-rc.1", 1},
		{"1.6.0-rc.2", "1.6.0-rc.10", -1},
		{"1.6.0-alpha", "1.6.0-alpha.1", -1},
		{"1.6.0-1", "1.6.0-alpha", -1},
		{"1.6.0-beta", "1.6.0-alpha", 1},
		{"0.0.0-dev", "1.6.0", -1},
	}
	for _, c := range cases {
		got, ok := compareVersions(c.a, c.b)
		if !ok || got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, %v；期望 %d", c.a, c.b, got, ok, c.want)
		}
	}
	for _, bad := range []string{"", "abc", "1..2", "1.6.0-", "1.6.0-rc..1", "1.x.0", "-1.0"} {
		if _, ok := compareVersions(bad, "1.0.0"); ok {
			t.Errorf("%q 应判为无效版本号", bad)
		}
	}
}

// ---- 容器内在线升级与回滚 ----

// setupContainerInstall 准备一个容器：镜像目录里是 v1.5.0，数据目录为空；重启替换成记录调用。
func setupContainerInstall(t *testing.T) (image, data string, calls chan execCall) {
	t.Helper()
	image, _, _ = setupInstall(t) // 镜像目录：程序、web、VERSION 都是 1.5.0
	data = t.TempDir()
	t.Setenv("CDK_DEPLOY_MODE", "docker")
	t.Setenv("DB_PATH", filepath.Join(data, "cdk_recharge.db"))
	calls = make(chan execCall, 1)
	reexecFn = func(p string, env []string) error { calls <- execCall{argv0: p, env: env}; return nil }
	return image, data, calls
}

func TestSeamlessUpdateInContainerWritesRuntime(t *testing.T) {
	priv := useTestReleaseKey(t)
	bundle := buildBundle(t, "1.5.1", `[ "$1" = "--version" ] && echo 1.5.1`)
	startFakeGitHub(t, fakeRelease{version: "1.5.1", bundle: bundle, sig: signB64(priv, bundle)})
	image, data, calls := setupContainerInstall(t)
	rt := filepath.Join(data, "runtime")

	runSeamlessUpdate("latest", "admin")

	var c execCall
	select {
	case c = <-calls:
	case <-time.After(3 * time.Second):
		t.Fatalf("没有触发重启，state=%+v", getUpdateState())
	}
	if c.argv0 != filepath.Join(rt, "cdk-recharge") {
		t.Fatalf("应 exec runtime 里的程序，得到 %q", c.argv0)
	}
	envs := strings.Join(c.env, "\n") + "\n"
	for _, kv := range []string{"WEB_DIR=" + filepath.Join(rt, "web"), "CDK_RUNTIME_HANDOFF=1", "CDK_APP_VERSION=1.5.1", "CDK_DEPLOY_MODE=docker"} {
		if !strings.Contains(envs, kv+"\n") {
			t.Fatalf("exec 的环境变量缺 %s：%q", kv, c.env)
		}
	}
	if strings.Count(envs, "WEB_DIR=") != 1 {
		t.Fatalf("WEB_DIR 只能有一个：%q", c.env)
	}
	// 新版本在 runtime 里，镜像目录不动
	if !strings.Contains(readFile(t, filepath.Join(rt, "cdk-recharge")), "1.5.1") ||
		readFile(t, filepath.Join(rt, "web", "index.html")) != "web-1.5.1" ||
		strings.TrimSpace(readFile(t, filepath.Join(rt, "VERSION"))) != "1.5.1" {
		t.Fatal("runtime 里的程序、前端或版本号不对")
	}
	if !strings.Contains(readFile(t, filepath.Join(image, "cdk-recharge")), "1.5.0") ||
		readFile(t, filepath.Join(image, "web", "index.html")) != "web-1.5.0" ||
		strings.TrimSpace(readFile(t, filepath.Join(image, "VERSION"))) != "1.5.0" {
		t.Fatal("容器内不应改动镜像目录")
	}
	if _, err := os.Stat(filepath.Join(image, "updates")); !os.IsNotExist(err) {
		t.Fatal("临时文件和备份不应写到镜像目录")
	}
	// 备份是镜像里的旧版本，放在数据目录的 updates/；临时目录清掉了
	baks, _ := filepath.Glob(filepath.Join(data, "updates", "backup-*"))
	if len(baks) != 1 {
		t.Fatalf("应有 1 个备份：%v", baks)
	}
	for _, f := range []string{"cdk-recharge", "VERSION", "cdk_recharge.db", filepath.Join("web", "index.html")} {
		if _, err := os.Stat(filepath.Join(baks[0], f)); err != nil {
			t.Fatalf("备份缺少 %s", f)
		}
	}
	if strings.TrimSpace(readFile(t, filepath.Join(baks[0], "VERSION"))) != "1.5.0" || readFile(t, filepath.Join(baks[0], "web", "index.html")) != "web-1.5.0" {
		t.Fatal("备份应是镜像里的旧版本")
	}
	if left, _ := filepath.Glob(filepath.Join(data, "updates", "staging-*")); len(left) != 0 {
		t.Fatalf("临时目录应清掉：%v", left)
	}
	// 这次 exec 计一次启动
	var st bootState
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(rt, bootFileName))), &st); err != nil || st.Version != "1.5.1" || st.BootCount != 1 {
		t.Fatalf("boot.json = %+v %v", st, err)
	}
}

func TestSeamlessUpdateInContainerRejectsBadSignature(t *testing.T) {
	useTestReleaseKey(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	bundle := buildBundle(t, "1.5.1", `echo 1.5.1`)
	startFakeGitHub(t, fakeRelease{version: "1.5.1", bundle: bundle, sig: signB64(other, bundle)})
	_, data, calls := setupContainerInstall(t)
	runSeamlessUpdate("latest", "admin")
	if st := getUpdateState(); st.Phase != "failed" || !strings.Contains(st.Error, "签名") {
		t.Fatalf("签名不对应中止：%+v", st)
	}
	select {
	case <-calls:
		t.Fatal("失败时不应重启")
	default:
	}
	if _, err := os.Stat(filepath.Join(data, "runtime")); !os.IsNotExist(err) {
		t.Fatal("校验失败时不应写 runtime")
	}
}

func TestSeamlessUpdateInContainerExecFailure(t *testing.T) {
	priv := useTestReleaseKey(t)
	bundle := buildBundle(t, "1.5.1", `[ "$1" = "--version" ] && echo 1.5.1`)
	startFakeGitHub(t, fakeRelease{version: "1.5.1", bundle: bundle, sig: signB64(priv, bundle)})
	_, data, _ := setupContainerInstall(t)
	done := make(chan struct{})
	reexecFn = func(string, []string) error { defer close(done); return errors.New("exec format error") }

	runSeamlessUpdate("latest", "admin")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("没有尝试重启")
	}
	deadline := time.Now().Add(2 * time.Second)
	for getUpdateState().Phase != "failed" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	st := getUpdateState()
	if st.Phase != "failed" || !strings.Contains(st.Error, "docker restart cdk-recharge") || !strings.Contains(st.Error, containerRollbackHint) {
		t.Fatalf("应提示用 docker restart 启用，并给出回滚方法：%+v", st)
	}
	var boot bootState
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(data, "runtime", bootFileName))), &boot); err != nil || boot.BootCount != 0 || boot.Version != "1.5.1" {
		t.Fatalf("exec 没成功不算一次启动：%+v %v", boot, err)
	}
}

func TestRollbackRuntimeInContainer(t *testing.T) {
	data := t.TempDir()
	rt := filepath.Join(data, "runtime")
	mustWrite(t, filepath.Join(rt, "cdk-recharge"), "bin-1.5.2")
	mustWrite(t, filepath.Join(rt, "web", "index.html"), "web-1.5.2")
	mustWrite(t, filepath.Join(rt, "VERSION"), "1.5.2\n")
	mustWrite(t, filepath.Join(rt, bootFileName), `{"version":"1.5.2","boot_count":3}`)
	bak := filepath.Join(data, "updates", "backup-20261103-100000")
	mustWrite(t, filepath.Join(bak, "cdk-recharge"), "bin-1.5.1")
	mustWrite(t, filepath.Join(bak, "web", "index.html"), "web-1.5.1")
	mustWrite(t, filepath.Join(bak, "VERSION"), "1.5.1\n")

	var out bytes.Buffer
	if err := rollbackRuntime(data, "1.5.0", &out); err != nil {
		t.Fatalf("rollbackRuntime: %v", err)
	}
	if readFile(t, filepath.Join(rt, "cdk-recharge")) != "bin-1.5.1" || readFile(t, filepath.Join(rt, "web", "index.html")) != "web-1.5.1" ||
		strings.TrimSpace(readFile(t, filepath.Join(rt, "VERSION"))) != "1.5.1" {
		t.Fatal("runtime 应恢复到备份的版本")
	}
	if _, err := os.Stat(filepath.Join(rt, bootFileName)); !os.IsNotExist(err) {
		t.Fatal("回滚后应删掉 boot.json，让恢复的版本重新计数")
	}
	failed, _ := filepath.Glob(filepath.Join(data, "updates", "failed-*", "cdk-recharge"))
	if len(failed) != 1 || readFile(t, failed[0]) != "bin-1.5.2" {
		t.Fatalf("出问题的版本应留存到数据目录的 updates/failed-*：%v", failed)
	}
	if !strings.Contains(out.String(), "docker restart cdk-recharge") || strings.Contains(out.String(), "systemctl") || strings.Contains(out.String(), "注意") {
		t.Fatalf("容器内应提示 docker restart：%s", out.String())
	}

	// 恢复的版本不比镜像新：提示重启后仍用镜像版本，以及怎么换镜像
	data2 := t.TempDir()
	mustWrite(t, filepath.Join(data2, "updates", "backup-20261103-100000", "cdk-recharge"), "bin-1.5.1")
	mustWrite(t, filepath.Join(data2, "updates", "backup-20261103-100000", "VERSION"), "1.5.1\n")
	out.Reset()
	if err := rollbackRuntime(data2, "1.6.0", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "deploy/docker-deploy.sh --version 1.5.1") {
		t.Fatalf("应提示换用旧版本镜像：%s", out.String())
	}
}

func TestRollbackCommandInContainerUsesRuntime(t *testing.T) {
	noContainerMarkers(t)
	data := t.TempDir()
	t.Setenv("CDK_DEPLOY_MODE", "docker")
	t.Setenv("DB_PATH", filepath.Join(data, "cdk_recharge.db"))
	// 安装目录相关的路径指向别处：容器内回滚不应碰它们
	other := t.TempDir()
	t.Setenv("CDK_INSTALL_DIR", other)
	t.Setenv("WEB_DIR", filepath.Join(other, "web"))
	bak := filepath.Join(data, "updates", "backup-20261103-100000")
	mustWrite(t, filepath.Join(bak, "cdk-recharge"), "bin-1.5.1")
	mustWrite(t, filepath.Join(bak, "VERSION"), "1.5.1\n")

	var out bytes.Buffer
	if err := Rollback(&out); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if readFile(t, filepath.Join(data, "runtime", "cdk-recharge")) != "bin-1.5.1" {
		t.Fatal("容器内应恢复到数据目录的 runtime")
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Fatalf("不应改动安装目录：%v", entries)
	}
}

func TestRollbackNativeHintUnchanged(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "updates", "backup-20261103-100000", "cdk-recharge"), "old")
	var out bytes.Buffer
	if err := RollbackLatest(root, filepath.Join(root, "cdk-recharge"), filepath.Join(root, "web"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "请执行：systemctl restart cdk-recharge\n") {
		t.Fatalf("原生部署的提示应保持原样：%s", out.String())
	}
}

func TestRuntimeBootCompletedNoopWithoutBootFile(t *testing.T) {
	f := newHandoffFixture(t)
	var rec alertRecorder
	f.completed(true, &rec)
	if len(rec.alerts) != 0 {
		t.Fatal(rec.alerts)
	}
	if _, err := os.Stat(f.rt); !os.IsNotExist(err) {
		t.Fatal("没有 boot.json 时不应创建 runtime 目录")
	}
}
