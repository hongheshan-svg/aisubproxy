package db

import (
	"database/sql"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

const (
	SettingMarsWallexBaseURL           = "marswallex_base_url"
	SettingMarsWallexDeveloperID       = "marswallex_developer_id"
	SettingMarsWallexAPIToken          = "marswallex_api_token"
	SettingMarsWallexPrivateKey        = "marswallex_private_key"
	SettingMarsWallexPlatformPublicKey = "marswallex_platform_public_key"
)

// SetSecretSetting 加密后保存设置（设置键名作为 AAD）。
func SetSecretSetting(key, plaintext string) error {
	mk, err := secretbox.LoadKey()
	if err != nil {
		return err
	}
	sealed, err := secretbox.Seal(mk, key, plaintext)
	if err != nil {
		return err
	}
	return SetSetting(key, sealed)
}

// GetSecretSetting 读取并解密设置；未设置返回 "", nil。
func GetSecretSetting(key string) (string, error) {
	raw, err := GetSetting(key)
	if err != nil {
		return "", err
	}
	if raw == "" {
		return "", nil
	}
	mk, err := secretbox.LoadKey()
	if err != nil {
		return "", err
	}
	return secretbox.Open(mk, key, raw)
}

// MigrateMarsWallex 创建 MarsWallex 相关表。
func MigrateMarsWallex() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS marswallex_webhook_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	message_id TEXT NOT NULL UNIQUE,
	topic TEXT NOT NULL DEFAULT '',
	payload TEXT NOT NULL,
	received_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	processed_at DATETIME,
	process_error TEXT
)`,
		`CREATE INDEX IF NOT EXISTS idx_mw_webhook_received ON marswallex_webhook_events(received_at)`,
	}
	for _, s := range stmts {
		if _, err := DB.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

type MarsWallexWebhookEvent struct {
	ID           int64
	MessageID    string
	Topic        string
	Payload      string
	ReceivedAt   time.Time
	ProcessedAt  *time.Time
	ProcessError string
	// 处理重试状态
	ProcessAttempts int
	NextAttemptAt   *int64
	ProcessNote     string
}

// InsertMarsWallexWebhookEvent 幂等写入；重复 message_id 返回 (false, nil)。
func InsertMarsWallexWebhookEvent(messageID, topic, payload string) (inserted bool, err error) {
	res, err := DB.Exec(`INSERT OR IGNORE INTO marswallex_webhook_events (message_id, topic, payload) VALUES (?, ?, ?)`,
		messageID, topic, payload)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func ListMarsWallexWebhookEvents(limit int) ([]MarsWallexWebhookEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := DB.Query(`SELECT id, message_id, topic, payload, received_at, processed_at, process_error,
		process_attempts, next_attempt_at, process_note
		FROM marswallex_webhook_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MarsWallexWebhookEvent
	for rows.Next() {
		var e MarsWallexWebhookEvent
		var pa sql.NullTime
		var pe sql.NullString
		var na sql.NullInt64
		if err := rows.Scan(&e.ID, &e.MessageID, &e.Topic, &e.Payload, &e.ReceivedAt, &pa, &pe,
			&e.ProcessAttempts, &na, &e.ProcessNote); err != nil {
			return nil, err
		}
		if pa.Valid {
			t := pa.Time
			e.ProcessedAt = &t
		}
		e.ProcessError = pe.String
		if na.Valid {
			v := na.Int64
			e.NextAttemptAt = &v
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
