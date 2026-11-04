package cardpool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// WebhookQuietWindow：这么久一条 MarsWallex Webhook 都没收到，就认为通知地址可能没配置。
// 3DS 验证码只能通过 Webhook 收到，付款引擎据此提前告警、改写等验证码超时的说明。
const WebhookQuietWindow = 7 * 24 * time.Hour

// LastWebhookAt 返回最近一次收到 MarsWallex Webhook 的时间（marswallex_webhook_events.received_at 的最大值，走 received_at 索引）。
// 一条都没收到时 ok = false。只读。
func LastWebhookAt(ctx context.Context) (at time.Time, ok bool, err error) {
	err = db.DB.QueryRowContext(ctx, `SELECT received_at FROM marswallex_webhook_events ORDER BY received_at DESC LIMIT 1`).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return at, true, nil
}

// LastWebhookAt 同包级 LastWebhookAt，供付款引擎通过卡池接口调用。
func (s *Service) LastWebhookAt(ctx context.Context) (time.Time, bool, error) {
	return LastWebhookAt(ctx)
}

// WebhookQuiet 按 LastWebhookAt 的结果判断最近 7 天是否一条 Webhook 都没收到（从没收到也算）。
func WebhookQuiet(at time.Time, ok bool, now time.Time) bool {
	return !ok || at.Before(now.Add(-WebhookQuietWindow))
}

// checkNoWebhook 是每日检查（维护循环里 MarsWallex 已就绪时每轮调用，告警按北京日去重，key nowebhook）：
// 最近 7 天有卡单据（平台本该推送转入转出结果），却一条 Webhook 都没收到，说明通知地址可能没配置。
// 最近 7 天没有卡单据时不发（新装、没业务时不打扰）。只读。
func (s *Service) checkNoWebhook(ctx context.Context) error {
	at, ok, err := LastWebhookAt(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	if !WebhookQuiet(at, ok, now) {
		return nil
	}
	var n int
	if err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mw_card_orders WHERE created_at >= ?`,
		now.Add(-WebhookQuietWindow).Unix()).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	s.alert("nowebhook", fmt.Sprintf("MarsWallex 最近 7 天有 %d 笔卡单据，但一条 Webhook 都没收到，通知地址可能没配置。"+
		"3DS 验证码只能通过 Webhook 收到，遇到 3DS 验证的付款会失败。通知地址填 https://<你的域名>/api/v1/webhooks/marswallex", n))
	return nil
}
