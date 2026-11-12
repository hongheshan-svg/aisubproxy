package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
