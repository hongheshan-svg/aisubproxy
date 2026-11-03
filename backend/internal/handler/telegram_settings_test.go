package handler

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/notify"
)

const tgTestToken = "987654321:AAF-handler_test_token_0123456789abcd"

// tgSetup 建测试库，并让 notify 读后台配置（与服务启动时一样）。
func tgSetup(t *testing.T) {
	t.Helper()
	mwSetup(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	notify.SetSettingsSource(TelegramSettings)
	t.Cleanup(func() { notify.SetSettingsSource(nil) })
}

// tgFake 是本地假 Telegram，返回固定的回复，并记下收到的表单。
func tgFake(t *testing.T, status int, body string) *[]url.Values {
	t.Helper()
	var got []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = append(got, r.PostForm)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(notify.SetAPIBase(srv.URL))
	return &got
}

func auditDetails(t *testing.T) string {
	t.Helper()
	rows, err := db.DB.Query(`SELECT action || ' ' || COALESCE(detail,'') FROM admin_audit_logs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		b.WriteString(s + "\n")
	}
	return b.String()
}

// 保存后：库里的令牌是密文；读接口只给脱敏提示；生效来源是后台配置；审计里没有令牌。
func TestTelegramSaveEncryptsAndMasks(t *testing.T) {
	tgSetup(t)
	w := mwDo(AdminPutTelegram, "PUT", "/x", `{"token":"`+tgTestToken+`","chat_id":"8883908283"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("保存: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), tgTestToken) || strings.Contains(w.Body.String(), "handler_test_token") {
		t.Fatalf("返回里不能有令牌: %s", w.Body.String())
	}
	raw, _ := db.GetSetting(settingTelegramBotToken)
	if raw == "" || strings.Contains(raw, "handler_test_token") {
		t.Fatalf("令牌应加密保存: %q", raw)
	}
	m := mwJSON(t, w)
	if m["token_configured"] != true || m["token_hint"] != "987654321:****abcd" || m["chat_id"] != "8883908283" || m["source"] != notify.SourceSettings {
		t.Fatalf("读接口: %v", m)
	}
	if a := auditDetails(t); !strings.Contains(a, "update_telegram") || strings.Contains(a, "handler_test_token") {
		t.Fatalf("审计: %s", a)
	}

	// 只改 chat id：令牌不变
	w = mwDo(AdminPutTelegram, "PUT", "/x", `{"token":"","chat_id":"-1001234567890"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("只改 chat id: %d %s", w.Code, w.Body.String())
	}
	if c := notify.Current(); c.Token != tgTestToken || c.ChatID != "-1001234567890" {
		t.Fatalf("生效配置: chat=%s token 是否保留=%v", c.ChatID, c.Token == tgTestToken)
	}
}

func TestTelegramSaveValidates(t *testing.T) {
	tgSetup(t)
	cases := []struct{ name, body, want string }{
		{"令牌格式不对", `{"token":"not-a-token","chat_id":"1"}`, "令牌格式不对"},
		{"chat id 格式不对", `{"token":"` + tgTestToken + `","chat_id":"abc def"}`, "chat id 格式不对"},
		{"chat id 为空", `{"token":"` + tgTestToken + `","chat_id":""}`, "chat id 格式不对"},
		{"后台没有令牌时只填 chat id", `{"chat_id":"1"}`, "请同时填写机器人令牌"},
		{"什么都没填", `{}`, "没有要保存的内容"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := mwDo(AdminPutTelegram, "PUT", "/x", c.body, nil)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.want) {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	if raw, _ := db.GetSetting(settingTelegramBotToken); raw != "" {
		t.Fatal("校验失败时不应写入")
	}

	// 没有 APP_MASTER_KEY 不能保存令牌（不能明文落库）
	t.Setenv("APP_MASTER_KEY", "")
	w := mwDo(AdminPutTelegram, "PUT", "/x", `{"token":"`+tgTestToken+`","chat_id":"1"}`, nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "APP_MASTER_KEY") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if raw, _ := db.GetSetting(settingTelegramBotToken); raw != "" {
		t.Fatal("没有主密钥时不应写入令牌")
	}
}

// 清除后台配置后，有环境变量就用环境变量。
func TestTelegramClearFallsBackToEnv(t *testing.T) {
	tgSetup(t)
	if w := mwDo(AdminPutTelegram, "PUT", "/x", `{"token":"`+tgTestToken+`","chat_id":"1"}`, nil); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "111:env")
	t.Setenv("TELEGRAM_CHAT_ID", "222")
	w := mwDo(AdminPutTelegram, "PUT", "/x", `{"clear":true}`, nil)
	m := mwJSON(t, w)
	if w.Code != http.StatusOK || m["token_configured"] != false || m["source"] != notify.SourceEnv || m["effective_chat_id"] != "222" || m["env_configured"] != true {
		t.Fatalf("清除后: %d %v", w.Code, m)
	}
}

// 旧版明文令牌：有主密钥时加密迁移并删除明文；没有主密钥时先保留。
func TestMigrateLegacyTelegramToken(t *testing.T) {
	tgSetup(t)
	_ = db.SetSetting(legacyTelegramTokenKey, tgTestToken)
	_ = db.SetSetting(settingTelegramChatID, "1")

	t.Setenv("APP_MASTER_KEY", "")
	MigrateLegacyTelegramToken()
	if v, _ := db.GetSetting(legacyTelegramTokenKey); v != tgTestToken {
		t.Fatal("没有主密钥时不应删除旧令牌")
	}

	t.Setenv("APP_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	MigrateLegacyTelegramToken()
	if v, _ := db.GetSetting(legacyTelegramTokenKey); v != "" {
		t.Fatal("迁移后应删除明文")
	}
	if c := notify.Current(); c.Source != notify.SourceSettings || c.Token != tgTestToken {
		t.Fatalf("迁移后应生效: source=%s", c.Source)
	}
}

// 测试消息：成功时 200 并写审计；Telegram 拒绝时返回处理方法；任何返回和审计里都没有令牌。
func TestTelegramTestMessage(t *testing.T) {
	tgSetup(t)
	w := mwDo(AdminTestTelegram, "POST", "/x", ``, nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "还没有配置") {
		t.Fatalf("未配置: %d %s", w.Code, w.Body.String())
	}

	if w := mwDo(AdminPutTelegram, "PUT", "/x", `{"token":"`+tgTestToken+`","chat_id":"8883908283"}`, nil); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	got := tgFake(t, http.StatusOK, `{"ok":true}`)
	w = mwDo(AdminTestTelegram, "POST", "/x", ``, nil)
	if w.Code != http.StatusOK || len(*got) != 1 || (*got)[0].Get("chat_id") != "8883908283" || !strings.Contains((*got)[0].Get("text"), "测试消息") {
		t.Fatalf("成功: %d %s %v", w.Code, w.Body.String(), *got)
	}

	tgFake(t, http.StatusForbidden, `{"ok":false,"error_code":403,"description":"Forbidden: bot can't initiate conversation with a user"}`)
	w = mwDo(AdminTestTelegram, "POST", "/x", ``, nil)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "先在 Telegram 里打开这个机器人") {
		t.Fatalf("403: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "handler_test_token") {
		t.Fatalf("返回泄露令牌: %s", w.Body.String())
	}
	if a := auditDetails(t); !strings.Contains(a, "telegram_test 成功") || !strings.Contains(a, "telegram_test 失败") || strings.Contains(a, "handler_test_token") {
		t.Fatalf("审计: %s", a)
	}
}

// 通用设置接口不再收发 Telegram 字段（改由专门的接口加密保存）。
func TestGeneralSettingsNoLongerHandleTelegram(t *testing.T) {
	tgSetup(t)
	w := mwDo(AdminPutSettings, "PUT", "/x", `{"telegram_token":"`+tgTestToken+`","telegram_chat_id":"1"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	for _, k := range []string{legacyTelegramTokenKey, settingTelegramBotToken, settingTelegramChatID} {
		if v, _ := db.GetSetting(k); v != "" {
			t.Fatalf("通用设置接口不应写 %s", k)
		}
	}
	if strings.Contains(w.Body.String(), "telegram") {
		t.Fatalf("通用设置接口不应返回 Telegram 字段: %s", w.Body.String())
	}
}
