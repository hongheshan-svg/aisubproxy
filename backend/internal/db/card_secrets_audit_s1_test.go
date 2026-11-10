package db_test

import (
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// A-6/C-2：旧明文迁移成密文；可重复跑；没主密钥时报错且不改库。
func TestS1MigrateCardSecrets(t *testing.T) {
	dbtest.Open(t)
	t.Setenv("APP_MASTER_KEY", "")
	_ = db.SetSetting("card_api_key", "sk_plain_123456")
	_ = db.SetSetting("webhook_secret", "whsec_plain")
	if err := db.MigrateCardSecrets(); err == nil {
		t.Fatal("没有主密钥却有明文，必须报错")
	}
	if v, _ := db.GetSetting("card_api_key"); v != "sk_plain_123456" {
		t.Fatalf("失败时不应改库: %q", v)
	}
	dbtest.SetMasterKey(t)
	for i := 0; i < 2; i++ {
		if err := db.MigrateCardSecrets(); err != nil {
			t.Fatalf("第 %d 次迁移: %v", i+1, err)
		}
	}
	for k, want := range map[string]string{"card_api_key": "sk_plain_123456", "webhook_secret": "whsec_plain"} {
		raw, _ := db.GetSetting(k)
		if !strings.HasPrefix(raw, "v1:") || strings.Contains(raw, want) {
			t.Fatalf("%s 应为密文: %q", k, raw)
		}
		got, err := db.GetSecretSetting(k)
		if err != nil || got != want {
			t.Fatalf("%s 读回 %q err=%v", k, got, err)
		}
	}
}

func TestS1MigrateCardSecretsNothingToDoNoKey(t *testing.T) {
	dbtest.Open(t)
	t.Setenv("APP_MASTER_KEY", "")
	if err := db.MigrateCardSecrets(); err != nil {
		t.Fatalf("没有明文时不需要主密钥: %v", err)
	}
}

// C-4：脱敏规则与迁移。
func TestS1SanitizeWebhookPayload(t *testing.T) {
	in := `{"event":"card_operation","card_number":"5378727109708264","cvv":"123","expire":"08/29","nested":{"card":{"cvc":"999","expiry_date":"0829"},"card_number":"4147000000000000"},"auth_id":7}`
	out := db.SanitizeWebhookPayload(in)
	for _, bad := range []string{"5378727109708264", "123", "08/29", "999", "0829", "4147000000000000", "cvv", "expire"} {
		if strings.Contains(out, bad) {
			t.Fatalf("脱敏后仍含 %q: %s", bad, out)
		}
	}
	for _, want := range []string{"537872******8264", "414700******0000", `"auth_id":7`, "card_operation"} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺少 %q: %s", want, out)
		}
	}
	if again := db.SanitizeWebhookPayload(out); again != out {
		t.Fatalf("应幂等: %s -> %s", out, again)
	}
	if got := db.SanitizeWebhookPayload("not json 5378727109708264 x"); strings.Contains(got, "5378727109708264") {
		t.Fatalf("非 JSON 也要处理: %s", got)
	}
}

func TestS1MigrateWebhookPayloads(t *testing.T) {
	dbtest.Open(t)
	insertLegacyWebhook(t, "k1", `{"card_number":"5378727109708264","cvv":"123"}`)
	for i := 0; i < 2; i++ {
		if err := db.MigrateWebhookPayloads(); err != nil {
			t.Fatal(err)
		}
	}
	var payloads []string
	rows, err := db.DB.Query(`SELECT payload FROM webhook_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, p)
	}
	if len(payloads) != 1 || strings.Contains(payloads[0], "5378727109708264") || strings.Contains(payloads[0], "123") {
		t.Fatalf("历史数据未清洗: %+v", payloads)
	}
}
