package server

import (
	"database/sql"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// 老库带着旧 ZovoCard 的表和数据升级：只读检查通过、再迁移不报错、旧数据一行不少，
// 全局默认服务商指向已不存在的 zovocard 时只给警告。
func TestLegacyZovoCardDBStartsAfterUpgrade(t *testing.T) {
	o3Env(t)
	legacyTables := []string{"cd_keys", "cardplatform_cdk_codes", "webhook_events", "card_blocklist", "agent_cdk_exchanges"}

	path := o3NewDBFile(t, func(c *sql.DB) {
		o3SeedAdmin(t, c, "admin")
		o3Exec(t, c, `INSERT INTO cd_keys (code, plan_type) VALUES ('LEGACY-CD-KEY-1', 'plus')`)
		o3Exec(t, c, `INSERT INTO cardplatform_cdk_codes (upstream_id, code, code_prefix, plan) VALUES (101, 'LEGACY-CP-CODE-1', 'LEGACY', 'plus')`)
		o3Exec(t, c, `INSERT INTO webhook_events (event_type, idem_key, payload) VALUES ('cdk.used', 'legacy-idem-1', '{"ok":true}')`)
		o3Exec(t, c, `INSERT INTO card_blocklist (card_id, reason) VALUES (7, 'legacy')`)
		o3Exec(t, c, `INSERT INTO agent_cdk_exchanges (old_code_hash, old_upstream_id, new_upstream_id, state) VALUES ('legacy-hash-1', 101, 0, 'disabling')`)
		o3Exec(t, c, `INSERT INTO site_settings (key, value) VALUES ('card_api_base', 'https://legacy.example.invalid')`)
		o3Exec(t, c, `INSERT INTO site_settings (key, value) VALUES ('cdk_default_provider', 'zovocard')`)
		// card_api_key 存密文：在这个临时库上用正式的加密函数写入
		old := db.DB
		db.DB = c
		err := db.SetSecretSetting(db.SettingCardAPIKey, "legacy-card-api-key")
		db.DB = old
		if err != nil {
			t.Fatal(err)
		}
	})

	countRows := func(c *sql.DB) map[string]int {
		out := map[string]int{}
		for _, tb := range legacyTables {
			var n int
			if err := c.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&n); err != nil {
				t.Fatalf("数 %s: %v", tb, err)
			}
			out[tb] = n
		}
		return out
	}
	open := func() *sql.DB {
		c, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=10000")
		if err != nil {
			t.Fatal(err)
		}
		c.SetMaxOpenConns(1)
		return c
	}

	c := open()
	before := countRows(c)
	c.Close()
	for _, tb := range legacyTables {
		if before[tb] != 1 {
			t.Fatalf("塞入后 %s 应有 1 行，实际 %d", tb, before[tb])
		}
	}

	// 1. 只读启动检查通过
	if err := o3Check("release"); err != nil {
		t.Fatalf("老库只读检查应通过: %v", err)
	}

	// 2. 对同一文件再跑 Migrate，加上启动里的密钥与 webhook 迁移
	c = open()
	defer c.Close()
	oldDB := db.DB
	db.DB = c
	defer func() { db.DB = oldDB }()
	if err := db.Migrate(); err != nil {
		t.Fatalf("老库再次 Migrate 不应报错: %v", err)
	}
	if err := db.MigrateCardSecrets(); err != nil {
		t.Fatalf("MigrateCardSecrets: %v", err)
	}
	if err := db.MigrateWebhookPayloads(); err != nil {
		t.Fatalf("MigrateWebhookPayloads: %v", err)
	}

	// 3. 旧表行数与塞入时相同
	after := countRows(c)
	for _, tb := range legacyTables {
		if after[tb] != before[tb] {
			t.Fatalf("%s 行数变了: 之前 %d，之后 %d", tb, before[tb], after[tb])
		}
	}

	// 4. 默认服务商指向已不存在的 zovocard：只给警告，不报错
	cfg, err := provider.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig 不应报错: %v", err)
	}
	const want = `全局默认服务商 "zovocard" 已不存在，请到「卡服务商」页重新选择`
	found := false
	for _, w := range cfg.Warnings {
		if w == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("应有警告 %q，实际 %v", want, cfg.Warnings)
	}
}
