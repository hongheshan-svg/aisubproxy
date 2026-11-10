package handler

import (
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func s1Put(body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/settings", AdminPutSettings)
	req := httptest.NewRequest("PUT", "/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// A-15/B-3/C-9：按字符数判断，超长 400，不截断。
func TestS1SettingsBrandLength(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	name14 := strings.Repeat("牛", 14) // 42 字节，14 字符，应通过
	if w := s1Put(`{"brand_name":"` + name14 + `"}`); w.Code != 200 {
		t.Fatalf("14 字应通过: %d %s", w.Code, w.Body.String())
	}
	if v, _ := db.GetSetting("brand_name"); v != name14 || !utf8.ValidString(v) {
		t.Fatalf("存的值被截坏: %q", v)
	}
	if w := s1Put(`{"brand_name":"` + strings.Repeat("牛", 41) + `"}`); w.Code != 400 {
		t.Fatalf("41 字应 400: %d", w.Code)
	}
	if w := s1Put(`{"brand_sub":"` + strings.Repeat("a", 81) + `"}`); w.Code != 400 {
		t.Fatalf("副标题 81 应 400: %d", w.Code)
	}
	if v, _ := db.GetSetting("brand_name"); v != name14 {
		t.Fatalf("被拒后原值应保留: %q", v)
	}
}

// C-9：写库失败要返回错误。
func TestS1SettingsWriteFailureReturns500(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if _, err := db.DB.Exec(`DROP TABLE site_settings`); err != nil {
		t.Fatal(err)
	}
	if w := s1Put(`{"brand_name":"x"}`); w.Code != 500 {
		t.Fatalf("写库失败应 500: %d %s", w.Code, w.Body.String())
	}
}
