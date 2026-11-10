package db

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// 旧版卡台的两项密钥：和 MarsWallex 令牌一样用 APP_MASTER_KEY 加密保存。
const (
	SettingCardAPIKey    = "card_api_key"
	SettingWebhookSecret = "webhook_secret"
)

var cardSecretKeys = []string{SettingCardAPIKey, SettingWebhookSecret}

// legacyPlainCardSecret 判断卡台密钥的库内值是不是升级前遗留的明文（非空、不是 v1: 密文）。
// 启动迁移（MigrateCardSecrets）和启动检查（CheckCardSecretsMigratable，--check 也用）共用这一条规则。
func legacyPlainCardSecret(raw string) bool {
	return strings.TrimSpace(raw) != "" && !strings.HasPrefix(raw, sealedPrefix)
}

// MigrateCardSecrets 把旧版明文保存的卡台密钥加密回写。可重复运行：已是密文的不动。
// 有明文却加密失败（例如没配 APP_MASTER_KEY）时返回错误，调用方应让启动失败，不能静默留着明文。
func MigrateCardSecrets() error {
	for _, key := range cardSecretKeys {
		raw, err := GetSetting(key)
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", key, err)
		}
		if !legacyPlainCardSecret(raw) {
			continue
		}
		if err := SetSecretSetting(key, strings.TrimSpace(raw)); err != nil {
			return fmt.Errorf("旧版明文的 %s 加密迁移失败（需要配置合法的 APP_MASTER_KEY）: %w", key, err)
		}
	}
	return nil
}

// ---- webhook 原文脱敏 ----

// 整个值都要去掉的字段（CVV、有效期）。
var webhookDropKeys = map[string]bool{
	"cvv": true, "cvv2": true, "cvc": true, "cvc2": true, "security_code": true,
	"expire": true, "expiry": true, "expiry_date": true, "expire_date": true,
	"expiration": true, "expiration_date": true,
	"exp_month": true, "exp_year": true, "expire_month": true, "expire_year": true,
}

var webhookPANKeys = map[string]bool{"card_number": true, "cardnumber": true, "pan": true}

var panDigitsRe = regexp.MustCompile(`\d{13,19}`)

// maskPAN 卡号最多保留前 6 后 4；已含 * 的视为已打码；太短的只留后 4。
func maskPAN(s string) string {
	if strings.Contains(s, "*") {
		return s
	}
	digits := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			digits = append(digits, s[i])
		}
	}
	n := len(digits)
	if n >= 13 {
		return string(digits[:6]) + strings.Repeat("*", n-10) + string(digits[n-4:])
	}
	if n > 4 {
		return "****" + string(digits[n-4:])
	}
	return "****"
}

func sanitizeWebhookValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, val := range t {
			lk := strings.ToLower(k)
			switch {
			case webhookDropKeys[lk]:
				delete(t, k)
			case webhookPANKeys[lk]:
				switch x := val.(type) {
				case string:
					t[k] = maskPAN(x)
				case json.Number:
					t[k] = maskPAN(x.String())
				default:
					t[k] = sanitizeWebhookValue(val)
				}
			default:
				t[k] = sanitizeWebhookValue(val)
			}
		}
		return t
	case []interface{}:
		for i := range t {
			t[i] = sanitizeWebhookValue(t[i])
		}
		return t
	}
	return v
}

// SanitizeWebhookPayload 卡台 webhook 原文入库前脱敏：卡号只留前 6 后 4，CVV、有效期整个去掉。
// 幂等。不是 JSON 的原文，把 13–19 位的连续数字按卡号打码。
func SanitizeWebhookPayload(raw string) string {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil || dec.More() {
		return panDigitsRe.ReplaceAllStringFunc(raw, maskPAN)
	}
	out, err := json.Marshal(sanitizeWebhookValue(v))
	if err != nil {
		return panDigitsRe.ReplaceAllStringFunc(raw, maskPAN)
	}
	return string(out)
}

// webhookMigrateBatch 历史清洗每批处理的行数，每批一个事务。
const webhookMigrateBatch = 500

// MigrateWebhookPayloads 对已有 webhook_events.payload 做同样的清洗。可重复运行。
// 按 id 分批、每批一个事务：不会把全表读进内存；中途失败时已完成的批次保留，下次启动接着清（已干净的行不再改写）。
func MigrateWebhookPayloads() error {
	return MigrateWebhookPayloadsBatched(webhookMigrateBatch, nil)
}

// MigrateWebhookPayloadsBatched 同 MigrateWebhookPayloads，可指定批大小；
// afterBatch 在每批提交后调用（参数是已完成的批数），返回错误则中止（供测试模拟中途失败）。
func MigrateWebhookPayloadsBatched(batchSize int, afterBatch func(done int) error) error {
	if DB == nil {
		return fmt.Errorf("db not ready")
	}
	if batchSize <= 0 {
		batchSize = webhookMigrateBatch
	}
	type upd struct {
		id int64
		p  string
	}
	var after int64
	for batch := 1; ; batch++ {
		rows, err := DB.Query(`SELECT id, payload FROM webhook_events WHERE id > ? ORDER BY id LIMIT ?`, after, batchSize)
		if err != nil {
			return fmt.Errorf("读取 webhook_events 失败: %w", err)
		}
		var todo []upd
		n := 0
		for rows.Next() {
			var id int64
			var p string
			if err := rows.Scan(&id, &p); err != nil {
				rows.Close()
				return err
			}
			n++
			after = id
			if np := SanitizeWebhookPayload(p); np != p {
				todo = append(todo, upd{id, np})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if n == 0 {
			return nil
		}
		if len(todo) > 0 {
			tx, err := DB.Begin()
			if err != nil {
				return err
			}
			for _, u := range todo {
				if _, err := tx.Exec(`UPDATE webhook_events SET payload = ? WHERE id = ?`, u.p, u.id); err != nil {
					tx.Rollback()
					return fmt.Errorf("清洗 webhook_events(%d) 失败: %w", u.id, err)
				}
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("清洗 webhook_events 提交失败: %w", err)
			}
		}
		if afterBatch != nil {
			if err := afterBatch(batch); err != nil {
				return err
			}
		}
		if n < batchSize {
			return nil
		}
	}
}
