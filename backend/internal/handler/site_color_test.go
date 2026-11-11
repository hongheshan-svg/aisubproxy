package handler

import (
	"encoding/json"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func TestPublicSiteDefaults(t *testing.T) {
	dbtest.Open(t)
	m := publicSiteJSON(t)
	if m["skin"] != "teal" || m["theme_mode"] != "auto" || m["logo_version"] != "" {
		t.Fatalf("默认值不对: %v", m)
	}
	if _, ok := m["primary_color"]; ok {
		t.Fatalf("不应再返回 primary_color: %v", m)
	}
}

func TestAdminGetSettingsNoPrimary(t *testing.T) {
	dbtest.Open(t)
	w := mwDo(AdminGetSettings, "GET", "/x", "", nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["skin"] != "teal" {
		t.Fatalf("默认皮肤应为 teal: %v", m["skin"])
	}
	if _, ok := m["primary_color"]; ok {
		t.Fatalf("不应再返回 primary_color: %v", m)
	}
}

func TestPutSkinTeal(t *testing.T) {
	dbtest.Open(t)
	if w := s1Put(`{"skin":"teal"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if v, _ := db.GetSetting("skin"); v != "teal" {
		t.Fatalf("skin 应存 teal: %q", v)
	}
	if got := publicSiteJSON(t)["skin"]; got != "teal" {
		t.Fatalf("公开接口 skin: %v", got)
	}
	if w := s1Put(`{"skin":"nope"}`); w.Code != 400 {
		t.Fatalf("未知皮肤应 400: %d", w.Code)
	}
}

func TestPutIgnoresPrimaryColor(t *testing.T) {
	dbtest.Open(t)
	_ = db.SetSetting("primary_color", "#1E4FA8")
	// 不合格的颜色也不能让整批失败，且旧值保持不变
	if w := s1Put(`{"primary_color":"#7FD4C4","brand_name":"新站名"}`); w.Code != 200 {
		t.Fatalf("应忽略主色并返回 200: %d %s", w.Code, w.Body.String())
	}
	if v, _ := db.GetSetting("brand_name"); v != "新站名" {
		t.Fatalf("同批其它项应落库: %q", v)
	}
	if v, _ := db.GetSetting("primary_color"); v != "#1E4FA8" {
		t.Fatalf("primary_color 不应被改: %q", v)
	}
	if w := s1Put(`{"primary_color":"#0F6B5F"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if v, _ := db.GetSetting("primary_color"); v != "#1E4FA8" {
		t.Fatalf("primary_color 不应被改: %q", v)
	}
}

func publicSiteJSON(t *testing.T) map[string]any {
	t.Helper()
	w := mwDo(PublicSiteConfig, "GET", "/x", "", nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
