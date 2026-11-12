package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// 前端的 index.html（含 SPA 路由回退）必须带 Cache-Control: no-cache：
// 在线升级会把前端目录整个换掉，旧的分包文件随之消失。没有缓存头时浏览器会按 Last-Modified 启发式缓存 HTML，
// 升级后整页跳转可能拿到旧 HTML，旧 HTML 引用的入口脚本已经不在，页面就白屏。
// 带内容哈希的 /assets/ 文件不受影响，照常缓存。
// （直接请求 /index.html 会被 http.ServeFile 301 到 /，所以这里不单独测。）
func TestSPAIndexHTMLNoCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	web := filepath.Join(t.TempDir(), "web")
	if err := os.MkdirAll(filepath.Join(web, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("INDEX"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "assets", "index-abc123.js"), []byte("JSBODY"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEB_DIR", web)
	r := gin.New()
	setupRoutes(r)

	serve := func(method, target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, target, nil))
		return w
	}

	for _, target := range []string{"/", "/ops/cdkeys", "/agent/login", "/history?x=1"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := serve(method, target)
			if w.Code != http.StatusOK {
				t.Fatalf("%s %s 应 200，实际 %d", method, target, w.Code)
			}
			if got := w.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("%s %s 的 Cache-Control 应为 no-cache，实际 %q", method, target, got)
			}
		}
	}

	w := serve(http.MethodGet, "/assets/index-abc123.js")
	if w.Code != http.StatusOK || w.Body.String() != "JSBODY" {
		t.Fatalf("/assets/index-abc123.js 应直出文件，实际 %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got == "no-cache" {
		t.Errorf("带哈希的分包文件不该被标成 no-cache")
	}
}

func newSPAServer(t *testing.T, index string) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	web := filepath.Join(t.TempDir(), "web")
	if err := os.MkdirAll(filepath.Join(web, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEB_DIR", web)
	r := gin.New()
	setupRoutes(r)
	return r, web
}

func doReq(r *gin.Engine, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// 回退的 index.html 带内容 ETag、不带 Last-Modified；If-None-Match 命中回 304 且仍带 Cache-Control。
// 内容变了即使文件时间更早也回 200（不受 If-Modified-Since 影响）。
func TestSPAIndexETag(t *testing.T) {
	r, web := newSPAServer(t, "INDEX-A")
	w := doReq(r, http.MethodGet, "/ops/cdkeys", nil)
	etag := w.Header().Get("ETag")
	if w.Code != http.StatusOK || etag == "" {
		t.Fatalf("应 200 且带 ETag，实际 %d %q", w.Code, etag)
	}
	if w.Header().Get("Last-Modified") != "" {
		t.Errorf("不应发 Last-Modified")
	}
	if w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control 应为 no-cache")
	}

	w = doReq(r, http.MethodGet, "/ops/cdkeys", map[string]string{"If-None-Match": etag})
	if w.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match 命中应 304，实际 %d", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("304 也应带 Cache-Control: no-cache")
	}

	// 内容变了，文件时间还比浏览器缓存的更早
	idx := filepath.Join(web, "index.html")
	if err := os.WriteFile(idx, []byte("INDEX-B"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(idx, old, old); err != nil {
		t.Fatal(err)
	}
	w = doReq(r, http.MethodGet, "/ops/cdkeys", map[string]string{
		"If-None-Match":     etag,
		"If-Modified-Since": time.Now().UTC().Format(http.TimeFormat),
	})
	if w.Code != http.StatusOK || w.Body.String() != "INDEX-B" {
		t.Fatalf("内容变了应 200 新内容，实际 %d %q", w.Code, w.Body.String())
	}
	if w.Header().Get("ETag") == etag {
		t.Errorf("内容变了 ETag 应变")
	}
	// 只带 If-Modified-Since（没有 ETag 的旧缓存）也要 200
	w = doReq(r, http.MethodGet, "/", map[string]string{"If-Modified-Since": time.Now().UTC().Format(http.TimeFormat)})
	if w.Code != http.StatusOK {
		t.Fatalf("只带 If-Modified-Since 应 200，实际 %d", w.Code)
	}
}

// /assets/ 下不存在的文件回 404（不回退成 HTML）；/api、/health 的 404 不带 no-cache。
func TestSPAAssetsMissing404(t *testing.T) {
	r, _ := newSPAServer(t, "INDEX")
	w := doReq(r, http.MethodGet, "/assets/gone-abc.js", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("/assets/ 下不存在的文件应 404，实际 %d", w.Code)
	}
	if w.Header().Get("Cache-Control") == "no-cache" {
		t.Errorf("404 不该带 no-cache")
	}
	for _, target := range []string{"/api/v1/nothing", "/health/x"} {
		w = doReq(r, http.MethodGet, target, nil)
		if w.Code != http.StatusNotFound || w.Header().Get("Cache-Control") != "" {
			t.Errorf("%s 应 404 且不带 Cache-Control，实际 %d %q", target, w.Code, w.Header().Get("Cache-Control"))
		}
	}
}
