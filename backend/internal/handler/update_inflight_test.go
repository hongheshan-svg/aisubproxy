package handler

import (
	"encoding/json"
	"fmt"
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

// asLinux 让一键更新按 linux 生产部署处理（开发机、CI 可能不是 linux）。
func asLinux(t *testing.T) {
	t.Helper()
	old := updateGOOS
	updateGOOS = "linux"
	t.Cleanup(func() { updateGOOS = old })
}

// insertPayOrders 按状态各插一张付款单。
func insertPayOrders(t *testing.T, states ...string) {
	t.Helper()
	now := time.Now().Unix()
	for i, st := range states {
		no := fmt.Sprintf("PO%012d%08x", i+1, i+1)
		if _, err := db.DB.Exec(`INSERT INTO pay_orders (order_no, account_email, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			no, no+"@x.com", st, now, now); err != nil {
			t.Fatal(err)
		}
	}
}

func postUpdate(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/system/update", strings.NewReader(`{"confirm":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	AdminSystemUpdate(c)
	return w
}

func clearUpdRunning(t *testing.T) {
	t.Helper()
	updMu.Lock()
	updRunning, updSnap = false, updateState{Phase: "idle"}
	updMu.Unlock()
}

// S1：有在途付款单（filling 等）时，更新接口直接拒绝，提示里带张数；确认中、排队的单不算。
func TestUpdateRefusedWhilePaymentInFlight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbtest.Open(t)
	noContainerMarkers(t)
	asLinux(t)
	t.Setenv("UPDATE_ENABLED", "")
	clearUpdRunning(t)
	t.Cleanup(func() { clearUpdRunning(t) })
	insertPayOrders(t, "filling", "submitted", "verifying", "queued", "succeeded")

	w := postUpdate(t)
	if w.Code != http.StatusConflict {
		t.Fatalf("有在途单应拒绝: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error != "有 2 张付款单正在处理，稍后再更新" {
		t.Fatalf("提示不符: %q", body.Error)
	}
	updMu.Lock()
	running := updRunning
	updMu.Unlock()
	if running {
		t.Fatal("拒绝时不应占着更新")
	}
}

// S1：只有确认中、排队的单时照常更新（重启后能接着跑）。
func TestUpdateNotBlockedByVerifyingOrQueued(t *testing.T) {
	gin.SetMode(gin.TestMode)
	asLinux(t)
	priv := useTestReleaseKey(t)
	bundle := buildBundle(t, "1.5.1", `[ "$1" = "--version" ] && echo 1.5.1`)
	startFakeGitHub(t, fakeRelease{version: "1.5.1", bundle: bundle, sig: signB64(priv, bundle)})
	_, bin, reexec := setupInstall(t)
	clearUpdRunning(t)
	insertPayOrders(t, "verifying", "queued", "queued")

	if w := postUpdate(t); w.Code != http.StatusAccepted {
		t.Fatalf("只有确认中、排队的单不应拒绝: %d %s", w.Code, w.Body.String())
	}
	select {
	case p := <-reexec:
		if p != bin {
			t.Fatalf("重启的程序路径 = %q", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("没有触发重启，state=%+v", getUpdateState())
	}
}

// S1：接口检查之后、替换文件之前又有了在途单：放弃这次更新，不动已安装的文件，不重启。
func TestSeamlessUpdateRefusedBeforeReplacingFiles(t *testing.T) {
	priv := useTestReleaseKey(t)
	bundle := buildBundle(t, "1.5.1", `[ "$1" = "--version" ] && echo 1.5.1`)
	startFakeGitHub(t, fakeRelease{version: "1.5.1", bundle: bundle, sig: signB64(priv, bundle)})
	root, bin, reexec := setupInstall(t)
	insertPayOrders(t, "opening")

	runSeamlessUpdate("latest", "admin")

	assertUpdateFailedUnchanged(t, root, bin, "有 1 张付款单正在处理，稍后再更新", reexec)
	if baks, _ := filepath.Glob(filepath.Join(root, "updates", "backup-*")); len(baks) != 0 {
		t.Fatalf("拒绝时不应开始备份、替换: %v", baks)
	}
	updMu.Lock()
	running := updRunning
	updMu.Unlock()
	if running {
		t.Fatal("失败后应复位 updRunning")
	}
}

// shortDownloadLimits 缩短下载的空闲时限和总时限。
func shortDownloadLimits(t *testing.T, idle, total time.Duration) {
	t.Helper()
	oldIdle, oldTotal := downloadIdleTimeout, downloadTotalTimeout
	downloadIdleTimeout, downloadTotalTimeout = idle, total
	t.Cleanup(func() { downloadIdleTimeout, downloadTotalTimeout = oldIdle, oldTotal })
}

// G7：下载中途卡住（一段时间没有新数据）就取消，不会一直挂着。
func TestDownloadCancelsWhenStalled(t *testing.T) {
	shortDownloadLimits(t, 150*time.Millisecond, 10*time.Second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		select { // 之后再也不发数据
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	begin := time.Now()
	err := downloadFile(srv.URL, filepath.Join(t.TempDir(), "x.tgz"), nil)
	if err == nil || !strings.Contains(err.Error(), "没有收到新数据") {
		t.Fatalf("卡住时应取消并说明原因: %v", err)
	}
	if el := time.Since(begin); el > 3*time.Second {
		t.Fatalf("应很快取消: %v", el)
	}
}

// G7：一直有数据但总时间超限也取消。
func TestDownloadCancelsAtTotalLimit(t *testing.T) {
	shortDownloadLimits(t, 2*time.Second, 300*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		for {
			if _, err := w.Write([]byte("x")); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(srv.Close)
	begin := time.Now()
	err := downloadFile(srv.URL, filepath.Join(t.TempDir(), "x.tgz"), nil)
	if err == nil || !strings.Contains(err.Error(), "没有下载完") {
		t.Fatalf("超过总时限应取消并说明原因: %v", err)
	}
	if el := time.Since(begin); el > 3*time.Second {
		t.Fatalf("应在总时限附近取消: %v", el)
	}
}

// G7：在线更新下载卡住时以失败结束，updRunning 复位（之后能再点更新），不动已安装的文件。
func TestSeamlessUpdateStalledDownloadResets(t *testing.T) {
	shortDownloadLimits(t, 150*time.Millisecond, 10*time.Second)
	root, bin, reexec := setupInstall(t)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.5.1", "assets": []map[string]any{
			{"name": "cdk-bundle-linux-amd64.tgz", "browser_download_url": srv.URL + "/dl/bundle.tgz", "size": 1000},
			{"name": "cdk-bundle-linux-amd64.tgz.sha256", "browser_download_url": srv.URL + "/dl/bundle.sha256"},
		}})
	})
	mux.HandleFunc("/dl/bundle.tgz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	done := make(chan struct{})
	go func() {
		defer close(done)
		runSeamlessUpdate("latest", "admin")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("下载卡住时更新应结束")
	}
	assertUpdateFailedUnchanged(t, root, bin, "没有收到新数据", reexec)
	updMu.Lock()
	running := updRunning
	updMu.Unlock()
	if running {
		t.Fatal("失败后应复位 updRunning")
	}
	if _, err := os.Stat(filepath.Join(root, "updates")); err == nil {
		if left, _ := filepath.Glob(filepath.Join(root, "updates", "staging-*")); len(left) != 0 {
			t.Fatalf("应清掉临时目录: %v", left)
		}
	}
}
