package db

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// 卡台的两项密钥：和 MarsWallex 令牌一样用 APP_MASTER_KEY 加密保存。
const (
	SettingCardAPIKey    = "card_api_key"
	SettingWebhookSecret = "webhook_secret"
)

var cardSecretKeys = []string{SettingCardAPIKey, SettingWebhookSecret}

// GetCardSecret 读卡台密钥（card_api_key / webhook_secret）。
// 以 v1: 开头的按密文解密；其余视为升级前遗留的明文原样返回（启动迁移会把它们加密，迁移失败启动就会报错）。
// 未设置返回 "", nil；解不开返回错误。
func GetCardSecret(key string) (string, error) {
	raw, err := GetSetting(key)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	if !strings.HasPrefix(raw, sealedPrefix) {
		return raw, nil
	}
	return GetSecretSetting(key)
}

// MigrateCardSecrets 把旧版明文保存的卡台密钥加密回写。可重复运行：已是密文的不动。
// 有明文却加密失败（例如没配 APP_MASTER_KEY）时返回错误，调用方应让启动失败，不能静默留着明文。
func MigrateCardSecrets() error {
	for _, key := range cardSecretKeys {
		raw, err := GetSetting(key)
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", key, err)
		}
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(raw, sealedPrefix) {
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

// MigrateWebhookPayloads 对已有 webhook_events.payload 做同样的清洗。可重复运行。
func MigrateWebhookPayloads() error {
	if DB == nil {
		return fmt.Errorf("db not ready")
	}
	rows, err := DB.Query(`SELECT id, payload FROM webhook_events`)
	if err != nil {
		return fmt.Errorf("读取 webhook_events 失败: %w", err)
	}
	type upd struct {
		id int64
		p  string
	}
	var todo []upd
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return err
		}
		if np := SanitizeWebhookPayload(p); np != p {
			todo = append(todo, upd{id, np})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, u := range todo {
		if _, err := DB.Exec(`UPDATE webhook_events SET payload = ? WHERE id = ?`, u.p, u.id); err != nil {
			return fmt.Errorf("清洗 webhook_events(%d) 失败: %w", u.id, err)
		}
	}
	return nil
}
