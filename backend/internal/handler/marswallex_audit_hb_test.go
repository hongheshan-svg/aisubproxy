package handler

import (
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// C-11：非密钥配置（地址、开发者 ID、平台公钥）要一起写入。中途失败时不能只写了一半。
func TestMarsWallexPutConfigPlainFieldsAllOrNothing(t *testing.T) {
	c := mwSetup(t)
	if _, err := c.Exec(`CREATE TRIGGER hb_mw_fail_dev BEFORE INSERT ON site_settings
		WHEN NEW.key = 'marswallex_developer_id' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	w := mwPut(t, `{"base_url":"`+mwProdURL+`","developer_id":"dev-7"}`)
	if w.Code != 500 {
		t.Fatalf("期望 500，实际 %d %s", w.Code, w.Body.String())
	}
	if v, _ := db.GetSetting(db.SettingMarsWallexBaseURL); v != "" {
		t.Fatalf("开发者 ID 写失败后 base_url 已被写入: %q", v)
	}
}

// C-12：保存 API Token 时去掉首尾空白（从网页复制常带换行）。
func TestMarsWallexPutConfigTrimsAPIToken(t *testing.T) {
	mwSetup(t)
	w := mwPut(t, "{\"api_token\":\"  tok-trim-1 \\n\"}")
	if w.Code != 200 {
		t.Fatalf("PUT %d %s", w.Code, w.Body.String())
	}
	got, err := db.GetSecretSetting(db.SettingMarsWallexAPIToken)
	if err != nil {
		t.Fatal(err)
	}
	if got != "tok-trim-1" {
		t.Fatalf("API Token 未去除首尾空白: %q", got)
	}
}
