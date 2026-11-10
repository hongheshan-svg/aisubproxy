package handler

import (
	"fmt"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 生产代码已不再写 cdk_session_bindings（旧版绑定只剩历史数据）。测试要造「老库里有绑定」的场景时，
// 直接写一行旧版明文，再跑正式的加密迁移，得到和老库一致的密文行。

// bindCDKSession 造一行绑定：code 为空时按令牌写 "RT:" 占位行；已有同码的行则更新会话和令牌，不改 provider。
func bindCDKSession(provider, code, token, session string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		code = "RT:" + strings.TrimSpace(token)
	}
	if _, err := db.DB.Exec(`INSERT INTO cdk_session_bindings (cdk_code, session_payload, redemption_token, provider, session_bound_at, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(cdk_code) DO UPDATE SET session_payload = excluded.session_payload,
			redemption_token = excluded.redemption_token, session_bound_at = excluded.session_bound_at`,
		code, session, token, provider, time.Now().Unix()); err != nil {
		return fmt.Errorf("写旧版绑定: %w", err)
	}
	return db.MigrateCDKSessions()
}

// clearCDKSessionByCode 订单完成后清空会话但保留令牌和邮箱（行在 bindCDKSession 里已加密）。
func clearCDKSessionByCode(code string) error {
	_, err := db.DB.Exec(`UPDATE cdk_session_bindings SET session_payload = '' WHERE cdk_code = ?`, strings.ToUpper(strings.TrimSpace(code)))
	return err
}
