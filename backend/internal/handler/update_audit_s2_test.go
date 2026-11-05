package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestS2IsRemoteNewerIgnoresPrerelease(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        bool
	}{
		{"1.5.2", "1.6.0-rc1", true}, // 比当前新的预发布：高于当前正式版
		{"1.6.0", "1.6.0-rc1", false},
		{"1.6.0-rc1", "1.6.0", true},
		{"1.6.0-rc1", "1.6.0-rc2", true},
		{"1.6.0", "1.6.0", false},
		{"v1.5.2", "1.5.3", true},
		{"1.5.3", "1.5.2", false},
		{"", "1.5.3", false},
		{"1.5.2", "garbage", false},
	}
	for _, tc := range cases {
		if got := isRemoteNewer(tc.cur, tc.latest); got != tc.want {
			t.Errorf("isRemoteNewer(%q, %q) = %v，应为 %v", tc.cur, tc.latest, got, tc.want)
		}
	}
}

func TestS2UpdateRejectsBadTarget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	asLinux(t)
	t.Setenv("UPDATE_ENABLED", "")
	for _, target := range []string{"1.2.3/../../x", "1.2.3?x=1", "../1", "1.2.3#a", "a/b", "1.2.3-rc/1"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/u", strings.NewReader(`{"confirm":true,"target":"`+target+`"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		AdminSystemUpdate(c)
		if w.Code != http.StatusBadRequest {
			t.Errorf("target=%q 应 400: %d %s", target, w.Code, w.Body.String())
		}
	}
}

// s2Layout 在临时目录里摆好「旧版」安装：二进制、web，以及要换上的新包。
func s2Layout(t *testing.T) (l updateLayout, binSrc, webSrc string) {
	t.Helper()
	root := t.TempDir()
	l = updateLayout{Bin: filepath.Join(root, "cdk-recharge"), Web: filepath.Join(root, "web")}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(l.Web, 0o755))
	must(os.WriteFile(l.Bin, []byte("OLD-BIN"), 0o755))
	must(os.WriteFile(filepath.Join(l.Web, "index.html"), []byte("OLD-WEB"), 0o644))
	src := t.TempDir()
	binSrc = filepath.Join(src, "cdk-recharge")
	webSrc = filepath.Join(src, "web")
	must(os.MkdirAll(webSrc, 0o755))
	must(os.WriteFile(binSrc, []byte("NEW-BIN"), 0o755))
	must(os.WriteFile(filepath.Join(webSrc, "index.html"), []byte("NEW-WEB"), 0o644))
	return
}

func s2Read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func s2AssertOld(t *testing.T, l updateLayout) {
	t.Helper()
	if got := s2Read(t, l.Bin); got != "OLD-BIN" {
		t.Errorf("二进制应保持旧版: %q", got)
	}
	if got := s2Read(t, filepath.Join(l.Web, "index.html")); got != "OLD-WEB" {
		t.Errorf("web 应保持旧版: %q", got)
	}
	for _, extra := range []string{l.Bin + ".new", l.Bin + ".old", l.Web + ".next", l.Web + ".prev"} {
		if _, err := os.Stat(extra); err == nil {
			t.Errorf("不应留下临时文件 %s", extra)
		}
	}
}

// A-7：新包都写好、都换上才算成功。
func TestS2SwapBinaryAndWebSuccess(t *testing.T) {
	l, bs, ws := s2Layout(t)
	if err := swapBinaryAndWeb(l, bs, ws); err != nil {
		t.Fatal(err)
	}
	if s2Read(t, l.Bin) != "NEW-BIN" || s2Read(t, filepath.Join(l.Web, "index.html")) != "NEW-WEB" {
		t.Fatal("应都换成新版")
	}
	for _, extra := range []string{l.Bin + ".new", l.Bin + ".old", l.Web + ".next", l.Web + ".prev"} {
		if _, err := os.Stat(extra); err == nil {
			t.Errorf("成功后不应留下 %s", extra)
		}
	}
}

// A-7：写新二进制失败（磁盘满、不同文件系统等）时，web 不能被换成新版。
func TestS2SwapKeepsOldWebWhenBinaryWriteFails(t *testing.T) {
	l, bs, ws := s2Layout(t)
	old := updCopyFile
	updCopyFile = func(src, dst string) error { return errors.New("no space left on device") }
	t.Cleanup(func() { updCopyFile = old })
	if err := swapBinaryAndWeb(l, bs, ws); err == nil {
		t.Fatal("应报错")
	}
	s2AssertOld(t, l)
}

// A-7：二进制已换上、换 web 失败时，二进制要退回旧版。
func TestS2SwapRollsBackBinaryWhenWebSwapFails(t *testing.T) {
	l, bs, ws := s2Layout(t)
	old := updRename
	updRename = func(from, to string) error {
		if from == l.Web+".next" {
			return errors.New("rename failed")
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { updRename = old })
	if err := swapBinaryAndWeb(l, bs, ws); err == nil {
		t.Fatal("应报错")
	}
	s2AssertOld(t, l)
}

// A-7：换二进制本身失败时，web 也保持旧版。
func TestS2SwapKeepsOldWhenBinaryRenameFails(t *testing.T) {
	l, bs, ws := s2Layout(t)
	old := updRename
	updRename = func(from, to string) error {
		if from == l.Bin+".new" {
			return errors.New("rename failed")
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { updRename = old })
	if err := swapBinaryAndWeb(l, bs, ws); err == nil {
		t.Fatal("应报错")
	}
	s2AssertOld(t, l)
}
