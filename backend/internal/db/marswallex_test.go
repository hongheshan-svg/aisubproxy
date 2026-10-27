package db

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

func setupMarsWallexDB(t *testing.T) {
	t.Helper()
	c, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := DB
	DB = c
	t.Cleanup(func() { DB = old; c.Close() })
	if _, err := DB.Exec(`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME)`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateMarsWallex(); err != nil {
		t.Fatal(err)
	}
	if err := MigrateCardPool(); err != nil {
		t.Fatal(err)
	}
}

func testKey(b byte) string {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return base64.StdEncoding.EncodeToString(k)
}

func TestSecretSettingRoundTripAndStoredEncrypted(t *testing.T) {
	setupMarsWallexDB(t)
	t.Setenv("APP_MASTER_KEY", testKey(1))
	if err := SetSecretSetting(SettingMarsWallexAPIToken, "tok"); err != nil {
		t.Fatal(err)
	}
	raw, err := GetSetting(SettingMarsWallexAPIToken)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "v1:") || strings.Contains(raw, "tok") {
		t.Fatalf("raw value not encrypted: %q", raw)
	}
	got, err := GetSecretSetting(SettingMarsWallexAPIToken)
	if err != nil || got != "tok" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = GetSecretSetting(SettingMarsWallexPrivateKey)
	if err != nil || got != "" {
		t.Fatalf("unset: got %q, %v", got, err)
	}
}

func TestSecretSettingWithoutMasterKey(t *testing.T) {
	setupMarsWallexDB(t)
	t.Setenv("APP_MASTER_KEY", "")
	err := SetSecretSetting(SettingMarsWallexAPIToken, "tok")
	if !errors.Is(err, secretbox.ErrNoMasterKey) {
		t.Fatalf("err = %v", err)
	}
	raw, _ := GetSetting(SettingMarsWallexAPIToken)
	if raw != "" {
		t.Fatalf("key stored: %q", raw)
	}
	var n int
	_ = DB.QueryRow(`SELECT COUNT(*) FROM site_settings WHERE key = ?`, SettingMarsWallexAPIToken).Scan(&n)
	if n != 0 {
		t.Fatalf("row count %d", n)
	}
}

func TestSecretSettingWrongMasterKey(t *testing.T) {
	setupMarsWallexDB(t)
	t.Setenv("APP_MASTER_KEY", testKey(1))
	if err := SetSecretSetting(SettingMarsWallexAPIToken, "tok"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_MASTER_KEY", testKey(2))
	_, err := GetSecretSetting(SettingMarsWallexAPIToken)
	if !errors.Is(err, secretbox.ErrDecrypt) {
		t.Fatalf("err = %v", err)
	}
}

func TestInsertWebhookEventDuplicateIsNotError(t *testing.T) {
	setupMarsWallexDB(t)
	ins, err := InsertMarsWallexWebhookEvent("m1", "t", "{}")
	if !ins || err != nil {
		t.Fatalf("first: %v %v", ins, err)
	}
	ins, err = InsertMarsWallexWebhookEvent("m1", "t", "{}")
	if ins || err != nil {
		t.Fatalf("second: %v %v", ins, err)
	}
	var n int
	_ = DB.QueryRow(`SELECT COUNT(*) FROM marswallex_webhook_events`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows = %d", n)
	}
}

func TestListWebhookEventsOrderAndLimit(t *testing.T) {
	setupMarsWallexDB(t)
	for _, id := range []string{"a", "b", "c"} {
		if _, err := InsertMarsWallexWebhookEvent(id, "t", "{}"); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := ListMarsWallexWebhookEvents(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].MessageID != "c" || evs[1].MessageID != "b" {
		t.Fatalf("got %+v", evs)
	}
	if evs[0].ProcessedAt != nil {
		t.Fatal("ProcessedAt should be nil")
	}
}
