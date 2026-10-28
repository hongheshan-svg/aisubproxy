package cardpool

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

const (
	otpAAD          = "mw_3ds_otp" // secretbox 的 name（AAD）
	otpTTLSeconds   = 1800         // 验证码保留 30 分钟
	otpPollInterval = time.Second  // WaitOTP 轮询间隔
	redactedOTP     = "***"
)

// OTPView 为管理员查看的验证码。
type OTPView struct {
	OTP, MerchantName, Amount, Currency string
	ReceivedAt                          time.Time
}

// onOTPEvent 把 3DS 验证码加密写入 mw_3ds_otps。原文里的验证码由 processEventsOnce 在标记已处理时抹掉。
// 已抹除（***）的不写入；主密钥缺失或无效时不重试（抹掉后重试也无法恢复）。验证码不得出现在日志、备注与错误里。
func (s *Service) onOTPEvent(ctx context.Context, ev *marswallex.WebhookEvent) (string, error) {
	var d marswallex.Card3DSOTPData
	if err := decodeEventData(ev, &d); err != nil {
		return "", err
	}
	cardID, otp := strings.TrimSpace(d.CardID), strings.TrimSpace(d.OTP)
	if otp == redactedOTP {
		return "验证码已抹除", nil
	}
	if cardID == "" || otp == "" {
		return "", dataError("缺少 card_id 或 otp")
	}
	key, err := secretbox.LoadKey()
	if err != nil {
		msg := "主密钥缺失，验证码未保存"
		if !errors.Is(err, secretbox.ErrNoMasterKey) {
			msg = "主密钥无效，验证码未保存"
		}
		s.alert("otpkey", "MarsWallex 3DS 验证码无法保存："+msg+"，请检查 APP_MASTER_KEY")
		return "", &finalEventError{msg: msg}
	}
	now := s.now().Unix()
	dup, err := otpStored(ctx, key, cardID, otp, now)
	if err != nil {
		return "", err
	}
	if dup {
		// 重复推送，或上次写入后没来得及标记已处理
		return "验证码已保存（重复推送）", nil
	}
	sealed, err := secretbox.Seal(key, otpAAD, otp)
	if err != nil {
		return "", fmt.Errorf("加密验证码失败: %w", err)
	}
	if _, err := db.DB.ExecContext(ctx, `INSERT INTO mw_3ds_otps (card_id, otp_sealed, merchant_name, amount, currency, received_at)
		VALUES (?, ?, ?, ?, ?, ?)`, cardID, sealed, d.MerchantName, d.TransactionAmount, d.TransactionCurrency, now); err != nil {
		return "", err
	}
	log.Printf("[cardpool] 卡 %s 收到 3DS 验证码，已加密保存", cardID)
	return "验证码已加密保存", nil
}

// otpStored 判断同一张卡未过期、未取用的验证码里是否已有相同的一条。
func otpStored(ctx context.Context, key []byte, cardID, otp string, now int64) (bool, error) {
	rows, err := db.DB.QueryContext(ctx, `SELECT otp_sealed FROM mw_3ds_otps
		WHERE card_id = ? AND otp_sealed != '' AND received_at > ?`, cardID, now-otpTTLSeconds)
	if err != nil {
		return false, err
	}
	var sealed []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return false, err
		}
		sealed = append(sealed, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, v := range sealed {
		if got, err := secretbox.Open(key, otpAAD, v); err == nil && got == otp {
			return true, nil
		}
	}
	return false, nil
}

// WaitOTP 每秒查一次该卡在 since 之后收到、未过期且未取用的最新验证码；取到后清除密文并记下取用时间。
// ctx 结束时返回 ctx.Err()。
func (s *Service) WaitOTP(ctx context.Context, cardID string, since time.Time) (string, error) {
	ticker := time.NewTicker(otpPollInterval)
	defer ticker.Stop()
	for {
		otp, ok, err := s.takeOTP(ctx, cardID, since)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return "", cerr
			}
			return "", err
		}
		if ok {
			return otp, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// takeOTP 取用一条验证码；没有（或刚被别处取走）返回 ok = false。先解密再清除，解密失败不会丢掉密文。
func (s *Service) takeOTP(ctx context.Context, cardID string, since time.Time) (string, bool, error) {
	now := s.now().Unix()
	var id int64
	var sealed string
	err := db.DB.QueryRowContext(ctx, `SELECT id, otp_sealed FROM mw_3ds_otps
		WHERE card_id = ? AND received_at >= ? AND received_at > ? AND otp_sealed != ''
		ORDER BY id DESC LIMIT 1`, cardID, since.Unix(), now-otpTTLSeconds).Scan(&id, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	otp, err := openOTPSealed(sealed)
	if err != nil {
		return "", false, err
	}
	res, err := db.DB.ExecContext(ctx, `UPDATE mw_3ds_otps SET otp_sealed = '', consumed_at = ? WHERE id = ? AND otp_sealed != ''`, now, id)
	if err != nil {
		return "", false, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return "", false, err
	}
	log.Printf("[cardpool] 卡 %s 的 3DS 验证码已取用", cardID)
	return otp, true, nil
}

func openOTPSealed(sealed string) (string, error) {
	key, err := secretbox.LoadKey()
	if err != nil {
		return "", err
	}
	otp, err := secretbox.Open(key, otpAAD, sealed)
	if err != nil {
		return "", fmt.Errorf("解密验证码失败: %w", err)
	}
	return otp, nil
}

// ViewOTP 返回该卡未过期、未取用的最新验证码，不清除（便于管理员重看）；没有返回 ErrNotFound。
func (s *Service) ViewOTP(ctx context.Context, cardID, by string) (*OTPView, error) {
	var v OTPView
	var sealed string
	var received int64
	err := db.DB.QueryRowContext(ctx, `SELECT otp_sealed, merchant_name, amount, currency, received_at FROM mw_3ds_otps
		WHERE card_id = ? AND received_at > ? AND otp_sealed != '' ORDER BY id DESC LIMIT 1`,
		cardID, s.now().Unix()-otpTTLSeconds).Scan(&sealed, &v.MerchantName, &v.Amount, &v.Currency, &received)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if v.OTP, err = openOTPSealed(sealed); err != nil {
		return nil, err
	}
	v.ReceivedAt = time.Unix(received, 0).UTC()
	log.Printf("[cardpool] 管理员 %s 查看卡 %s 的 3DS 验证码", by, cardID)
	return &v, nil
}

// purgeOTPsOnce 清除收到满 30 分钟的验证码密文（不论是否取用）。
func (s *Service) purgeOTPsOnce(ctx context.Context) error {
	res, err := db.DB.ExecContext(ctx, `UPDATE mw_3ds_otps SET otp_sealed = '' WHERE otp_sealed != '' AND received_at <= ?`,
		s.now().Unix()-otpTTLSeconds)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("[cardpool] 已清除 %d 条过期的 3DS 验证码", n)
	}
	return nil
}

// redactOTP 把事件原文 data.otp 改为 "***"：data 为对象或 JSON 字符串都能处理，键名不区分大小写（与解码一致）。
// 返回是否有改动；没有可抹的验证码（缺失、为空、已是 ***）时原样返回。原文不是 JSON 对象时返回错误。
func redactOTP(raw string) (string, bool, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return raw, false, err
	}
	changed := false
	for k, v := range env {
		if !strings.EqualFold(k, "data") {
			continue
		}
		nv, ok, err := redactData(v)
		if err != nil {
			return raw, false, err
		}
		if ok {
			env[k], changed = nv, true
		}
	}
	if !changed {
		return raw, false, nil
	}
	out, err := marshalNoEscape(env)
	if err != nil {
		return raw, false, err
	}
	return string(out), true, nil
}

// redactData 处理 data：对象直接改；JSON 字符串先解开再改，改完仍编码为字符串。
func redactData(d json.RawMessage) (json.RawMessage, bool, error) {
	t := bytes.TrimSpace(d)
	if len(t) == 0 {
		return d, false, nil
	}
	switch t[0] {
	case '{':
		return redactObject(t)
	case '"':
		var str string
		if err := json.Unmarshal(t, &str); err != nil {
			return d, false, err
		}
		inner, ok, err := redactObject([]byte(str))
		if err != nil || !ok {
			return d, false, err
		}
		out, err := marshalNoEscape(string(inner))
		if err != nil {
			return d, false, err
		}
		return out, true, nil
	}
	return d, false, nil
}

// redactObject 把对象里键名为 otp（不区分大小写）且有值的字段改为 "***"；不是对象时不改动。
func redactObject(b []byte) (json.RawMessage, bool, error) {
	t := bytes.TrimSpace(b)
	if len(t) == 0 || t[0] != '{' {
		return b, false, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(t, &obj); err != nil {
		return b, false, err
	}
	changed := false
	for k, v := range obj {
		if !strings.EqualFold(k, "otp") || !otpPresent(v) {
			continue
		}
		obj[k], changed = json.RawMessage(`"`+redactedOTP+`"`), true
	}
	if !changed {
		return b, false, nil
	}
	out, err := marshalNoEscape(obj)
	if err != nil {
		return b, false, err
	}
	return out, true, nil
}

// otpPresent 判断 otp 字段是否带有需要抹掉的值：null、空串、*** 都不算。
func otpPresent(v json.RawMessage) bool {
	t := bytes.TrimSpace(v)
	if len(t) == 0 || string(t) == "null" {
		return false
	}
	var s string
	if json.Unmarshal(t, &s) == nil {
		s = strings.TrimSpace(s)
		return s != "" && s != redactedOTP
	}
	return true // 数字等非字符串值
}

// marshalNoEscape 编码 JSON，不转义 <、>、&，避免改动其他字段的写法。
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
