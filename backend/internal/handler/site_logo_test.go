package handler

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

var (
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	jpegBytes = append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), make([]byte, 32)...)
	webpBytes = append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
)

func logoRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/settings/logo", AdminUploadSiteLogo)
	r.DELETE("/settings/logo", AdminDeleteSiteLogo)
	r.GET("/site/logo", PublicSiteLogo)
	return r
}

func uploadLogo(r *gin.Engine, filename string, content []byte) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", filename)
	fw.Write(content)
	mw.Close()
	req := httptest.NewRequest("POST", "/settings/logo", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func doReq(r *gin.Engine, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestSiteLogoUploadKinds(t *testing.T) {
	dbtest.Open(t)
	r := logoRouter()
	for name, c := range map[string][]byte{"a.png": pngBytes, "a.jpg": jpegBytes, "a.webp": webpBytes} {
		if w := uploadLogo(r, name, c); w.Code != 200 {
			t.Fatalf("%s 应 200: %d %s", name, w.Code, w.Body.String())
		}
	}
	before, _ := db.GetSetting("site_logo")
	bad := map[string][]byte{
		"a.svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"a.gif": []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"),
		"a.png": []byte("this is plain text pretending to be png"),
		"big":   append(append([]byte{}, pngBytes...), make([]byte, siteLogoMaxBytes)...),
	}
	for name, c := range bad {
		w := uploadLogo(r, name, c)
		if w.Code != 400 {
			t.Fatalf("%s 应 400: %d", name, w.Code)
		}
		if name == "big" && !strings.Contains(w.Body.String(), "256 KB") {
			t.Fatalf("超大文案: %s", w.Body.String())
		}
	}
	// 恰好 256KB+1 字节
	exact := append(append([]byte{}, pngBytes...), make([]byte, siteLogoMaxBytes+1-len(pngBytes))...)
	if len(exact) != siteLogoMaxBytes+1 {
		t.Fatal("构造错误")
	}
	if w := uploadLogo(r, "x.png", exact); w.Code != 400 {
		t.Fatalf("256KB+1 应 400: %d", w.Code)
	}
	if after, _ := db.GetSetting("site_logo"); after != before {
		t.Fatal("被拒绝的上传不能改库")
	}
}

func TestSiteLogoPublicHeaders(t *testing.T) {
	dbtest.Open(t)
	r := logoRouter()
	if w := doReq(r, "GET", "/site/logo", nil); w.Code != 404 {
		t.Fatalf("没有 Logo 应 404: %d", w.Code)
	}
	w := uploadLogo(r, "a.png", pngBytes)
	var up map[string]string
	json.Unmarshal(w.Body.Bytes(), &up)
	ver := up["logo_version"]
	if len(ver) != 12 {
		t.Fatalf("版本号应 12 位: %q", ver)
	}
	if publicSiteJSON(t)["logo_version"] != ver {
		t.Fatal("公开配置的 logo_version 应一致")
	}
	g := doReq(r, "GET", "/site/logo", nil)
	if g.Code != 200 || !bytes.Equal(g.Body.Bytes(), pngBytes) {
		t.Fatalf("读取失败: %d", g.Code)
	}
	h := g.Header()
	if h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("ETag") != `"`+ver+`"` || h.Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("响应头不对: %v", h)
	}
	if n := doReq(r, "GET", "/site/logo", map[string]string{"If-None-Match": `"` + ver + `"`}); n.Code != http.StatusNotModified {
		t.Fatalf("应 304: %d", n.Code)
	}
}

func TestSiteLogoDeleteAndAudit(t *testing.T) {
	dbtest.Open(t)
	r := logoRouter()
	uploadLogo(r, "a.png", pngBytes)
	if w := doReq(r, "DELETE", "/settings/logo", nil); w.Code != 200 {
		t.Fatalf("删除: %d", w.Code)
	}
	if w := doReq(r, "GET", "/site/logo", nil); w.Code != 404 {
		t.Fatalf("删除后应 404: %d", w.Code)
	}
	if publicSiteJSON(t)["logo_version"] != "" {
		t.Fatal("删除后 logo_version 应为空")
	}
	rows, err := db.DB.Query(`SELECT action, detail FROM admin_audit_logs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var a, d string
		rows.Scan(&a, &d)
		got = append(got, a+"|"+d)
	}
	if len(got) != 2 || got[0] != "update_site_logo|image/png 40" || got[1] != "delete_site_logo|" {
		t.Fatalf("审计不对: %v", got)
	}
}
