package handler

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

func TestContrastRatio(t *testing.T) {
	got, err := contrastRatio("#FFFFFF", "#0F6B5F")
	if err != nil || math.Abs(got-6.39) > 0.01 {
		t.Fatalf("白对深青应约 6.39: %v %v", got, err)
	}
	got, err = contrastRatio("#000000", "#FFFFFF")
	if err != nil || math.Abs(got-21) > 0.001 {
		t.Fatalf("黑对白应为 21: %v %v", got, err)
	}
	if _, err := contrastRatio("red", "#FFFFFF"); err == nil {
		t.Fatal("非法色号应报错")
	}
}

func TestValidPrimaryColor(t *testing.T) {
	for _, ok := range []string{"#0F6B5F", "#1E4FA8", "#5B3FA0", "#A3283B", "#2F6B2F", "#36454F"} {
		if err := validPrimaryColor(ok); err != nil {
			t.Fatalf("预设 %s 应通过: %v", ok, err)
		}
	}
	if err := validPrimaryColor("#7FD4C4"); err == nil || !strings.Contains(err.Error(), "太浅") {
		t.Fatalf("浅色应报太浅: %v", err)
	}
	for _, bad := range []string{"0F6B5F", "#0F6B5", "red", ""} {
		if err := validPrimaryColor(bad); err == nil || !strings.Contains(err.Error(), "#RRGGBB") {
			t.Fatalf("%q 应报格式: %v", bad, err)
		}
	}
}

func TestPutPrimaryColor(t *testing.T) {
	dbtest.Open(t)
	if w := s1Put(`{"primary_color":"#1e4fa8"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if v, _ := db.GetSetting("primary_color"); v != "#1E4FA8" {
		t.Fatalf("应存大写: %q", v)
	}
	if got := publicSiteJSON(t)["primary_color"]; got != "#1E4FA8" {
		t.Fatalf("公开接口: %v", got)
	}
	w := s1Put(`{"primary_color":"#7FD4C4","brand_name":"别存"}`)
	if w.Code != 400 {
		t.Fatalf("太浅应 400: %d", w.Code)
	}
	if v, _ := db.GetSetting("primary_color"); v != "#1E4FA8" {
		t.Fatalf("非法值不能落库: %q", v)
	}
	if v, _ := db.GetSetting("brand_name"); v == "别存" {
		t.Fatal("同批其它项也不能落库")
	}
	for _, c := range []string{"#0F6B5F", "#1E4FA8", "#5B3FA0", "#A3283B", "#2F6B2F", "#36454F"} {
		if w := s1Put(`{"primary_color":"` + c + `"}`); w.Code != 200 {
			t.Fatalf("预设 %s 应能保存: %d %s", c, w.Code, w.Body.String())
		}
	}
}

func TestPublicSiteDefaults(t *testing.T) {
	dbtest.Open(t)
	m := publicSiteJSON(t)
	if m["theme_mode"] != "auto" || m["primary_color"] != "#0F6B5F" || m["logo_version"] != "" {
		t.Fatalf("默认值不对: %v", m)
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
