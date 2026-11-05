package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 前端静态文件托管：请求路径含 ".." 一律 404（见 codeql #7、#8），正常路径不受影响。
func TestCodeqlStaticRejectsDotDotPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	web := filepath.Join(root, "web")
	if err := os.MkdirAll(filepath.Join(web, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	// webDir 外放一个敏感文件，若穿越成功会读到它
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("TOPSECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("INDEX"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(web, "assets", "x.js"), []byte("JSBODY"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEB_DIR", web)
	r := gin.New()
	setupRoutes(r)

	serve := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		r.ServeHTTP(w, req)
		return w
	}

	for _, target := range []string{
		"/assets/../../secret.txt",
		"/../secret.txt",
		"/..%2f..%2fsecret.txt",
		"/assets/..%2f..%2fsecret.txt",
	} {
		w := serve(target)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s 应 404，实际 %d：%s", target, w.Code, w.Body.String())
		}
		if body := w.Body.String(); strings.Contains(body, "TOPSECRET") || strings.Contains(body, "INDEX") {
			t.Errorf("%s 不应返回任何静态内容：%s", target, body)
		}
	}

	// 正常路径：真实文件直出，SPA 路由回退 index.html
	if w := serve("/assets/x.js"); w.Code != http.StatusOK || w.Body.String() != "JSBODY" {
		t.Errorf("/assets/x.js 应直出文件，实际 %d %q", w.Code, w.Body.String())
	}
	if w := serve("/ops/login"); w.Code != http.StatusOK || w.Body.String() != "INDEX" {
		t.Errorf("/ops/login 应回退 index.html，实际 %d %q", w.Code, w.Body.String())
	}
}
