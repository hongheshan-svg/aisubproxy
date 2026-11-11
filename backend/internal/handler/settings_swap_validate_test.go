package handler

import (
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// H1-03：换码口令不合格时整个请求被拒，其他设置一项都不能存进去。
func TestSettingsWeakSwapPasswordSavesNothing(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if w := s1Put(`{"brand_name":"旧名"}`); w.Code != 200 {
		t.Fatalf("准备失败: %d", w.Code)
	}
	for _, pw := range []string{"short", "aaaaaaaaaaaaaaaa"} {
		w := s1Put(`{"brand_name":"新名","agent_swap_daily_limit":7,"agent_swap_password":"` + pw + `"}`)
		if w.Code != 400 {
			t.Fatalf("%q 应 400: %d", pw, w.Code)
		}
	}
	if v, _ := db.GetSetting("brand_name"); v != "旧名" {
		t.Fatalf("站点名不应被改: %q", v)
	}
	if v, _ := db.GetSetting(agentSwapDailyLimitKey); v == "7" {
		t.Fatal("每日上限不应被改")
	}
}

func TestSettingsSwapPasswordDBErrorIs500NoLeak(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if _, err := db.DB.Exec(`DROP TABLE site_settings`); err != nil {
		t.Fatal(err)
	}
	w := s1Put(`{"agent_swap_password":"a-good-password-9"}`)
	if w.Code != 500 {
		t.Fatalf("写库失败应 500: %d %s", w.Code, w.Body.String())
	}
}

// 审查修复：超过 bcrypt 的 72 字节上限先被拒（400），其他设置不能先存进去。
func TestSettingsSwapPasswordTooLongSavesNothing(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if w := s1Put(`{"brand_name":"旧名"}`); w.Code != 200 {
		t.Fatalf("准备失败: %d", w.Code)
	}
	for name, pw := range map[string]string{
		"76个英文": strings.Repeat("aB3x", 19),
		"25个汉字": strings.Repeat("强", 25),
	} {
		w := s1Put(`{"brand_name":"新名","agent_swap_password":"` + pw + `"}`)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "太长") {
			t.Fatalf("%s 应 400 且提示太长: %d %s", name, w.Code, w.Body.String())
		}
	}
	if v, _ := db.GetSetting("brand_name"); v != "旧名" {
		t.Fatalf("站点名不应被改: %q", v)
	}
}
