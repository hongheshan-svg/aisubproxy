package cardpool

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// insertWebhookAt 入库一条收到时间为 at 的 Webhook 事件（已处理，不进事件循环）。
func insertWebhookAt(t *testing.T, at time.Time) {
	t.Helper()
	msgID := fmt.Sprintf("msg-at-%d", eventSeq.Add(1))
	if ok, err := db.InsertMarsWallexWebhookEvent(msgID, "SOMETHING", `{}`); err != nil || !ok {
		t.Fatalf("入库事件: ok=%v err=%v", ok, err)
	}
	// 与 CURRENT_TIMESTAMP 的写法一致（UTC，精确到秒）
	if _, err := db.DB.Exec(`UPDATE marswallex_webhook_events SET received_at = ?, processed_at = received_at WHERE message_id = ?`,
		at.UTC().Format("2006-01-02 15:04:05"), msgID); err != nil {
		t.Fatal(err)
	}
}

// insertDoneOrder 插入一张已成功的卡单据（终态，对账不会去查平台）。
func insertDoneOrder(t *testing.T, createdAt int64) {
	t.Helper()
	insertOrder(t, Order{Kind: KindRecharge, CardID: "c1", AmountCents: 100, Currency: "USD", State: StateSuccess,
		CreatedAt: createdAt, FinishedAt: &createdAt})
}

func noWebhookText(n int) string {
	return fmt.Sprintf("MarsWallex 最近 7 天有 %d 笔卡单据，但一条 Webhook 都没收到，通知地址可能没配置。"+
		"3DS 验证码只能通过 Webhook 收到，遇到 3DS 验证的付款会失败。通知地址填 https://<你的域名>/api/v1/webhooks/marswallex", n)
}

func TestLastWebhookAt(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	ctx := context.Background()
	if _, ok, err := LastWebhookAt(ctx); err != nil || ok {
		t.Fatalf("没有事件时应返回 ok=false: ok=%v err=%v", ok, err)
	}
	newest := clock.Now().Add(-time.Hour)
	insertWebhookAt(t, clock.Now().Add(-48*time.Hour))
	insertWebhookAt(t, newest)
	insertWebhookAt(t, clock.Now().Add(-3*time.Hour))
	at, ok, err := LastWebhookAt(ctx)
	if err != nil || !ok || !at.Equal(newest) {
		t.Fatalf("应返回最近一次收到的时间 %v: at=%v ok=%v err=%v", newest, at, ok, err)
	}
	if at2, ok2, err2 := s.LastWebhookAt(ctx); err2 != nil || !ok2 || !at2.Equal(newest) {
		t.Fatalf("Service.LastWebhookAt 应与包级函数一致: %v %v %v", at2, ok2, err2)
	}
}

func TestWebhookQuiet(t *testing.T) {
	now := time.Date(2026, 11, 4, 4, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		at    time.Time
		ok    bool
		quiet bool
	}{
		{"从未收到", time.Time{}, false, true},
		{"6 天前", now.Add(-6 * 24 * time.Hour), true, false},
		{"刚好 7 天", now.Add(-7 * 24 * time.Hour), true, false},
		{"8 天前", now.Add(-8 * 24 * time.Hour), true, true},
	} {
		if got := WebhookQuiet(c.at, c.ok, now); got != c.quiet {
			t.Fatalf("%s: WebhookQuiet = %v，期望 %v", c.name, got, c.quiet)
		}
	}
}

// 最近 7 天有卡单据、却一条 Webhook 都没收到：告警（key nowebhook，每天一次）；有 Webhook 或没有单据都不发。
func TestNoWebhookCheck(t *testing.T) {
	day := int64(86400)
	cases := []struct {
		name  string
		setup func(t *testing.T, now int64)
		wantN int // 0 表示不告警
	}{
		{"有单据无 Webhook", func(t *testing.T, now int64) {
			insertDoneOrder(t, now-day)
			insertDoneOrder(t, now-6*day)
			insertDoneOrder(t, now-8*day) // 7 天以前的不算
		}, 2},
		{"有单据、Webhook 已超过 7 天", func(t *testing.T, now int64) {
			insertDoneOrder(t, now-day)
			insertWebhookAt(t, time.Unix(now-8*day, 0))
		}, 1},
		{"有单据、7 天内有 Webhook", func(t *testing.T, now int64) {
			insertDoneOrder(t, now-day)
			insertWebhookAt(t, time.Unix(now-6*day, 0))
		}, 0},
		{"7 天内没有单据", func(t *testing.T, now int64) {
			insertDoneOrder(t, now-8*day)
		}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _, clock, alerts := newTestService(t)
			c.setup(t, clock.Now().Unix())
			if err := s.checkNoWebhook(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := alerts.Texts()
			if c.wantN == 0 {
				if len(got) != 0 {
					t.Fatalf("不应告警: %v", got)
				}
				return
			}
			if len(got) != 1 || got[0] != noWebhookText(c.wantN) {
				t.Fatalf("应告警一次:\n得到 %v\n期望 %q", got, noWebhookText(c.wantN))
			}
		})
	}
}

// 告警按北京日去重：同一天只发一次，第二天照发。走维护循环，MarsWallex 未就绪时不检查。
func TestNoWebhookCheckInMaintainRoundOncePerDay(t *testing.T) {
	s, _, clock, alerts := newTestService(t)
	insertDoneOrder(t, clock.Now().Unix()-3600)
	ready := false
	client := s.opts.Client
	s.opts.Client = func() (*marswallex.Client, error) {
		if !ready {
			return nil, errors.New("未配置")
		}
		return client()
	}
	lg := pollLog(s)
	s.maintainRound(context.Background(), lg)
	if alertsContaining(alerts, "Webhook 都没收到") != 0 {
		t.Fatalf("MarsWallex 未就绪时不应检查: %v", alerts.Texts())
	}
	ready = true
	s.maintainRound(context.Background(), lg)
	clock.Advance(time.Hour)
	s.maintainRound(context.Background(), lg)
	if n := alertsContaining(alerts, "Webhook 都没收到"); n != 1 {
		t.Fatalf("同一天应只发一次，得到 %d: %v", n, alerts.Texts())
	}
	clock.Advance(24 * time.Hour)
	s.maintainRound(context.Background(), lg)
	if n := alertsContaining(alerts, "Webhook 都没收到"); n != 2 {
		t.Fatalf("第二天应再发一次，得到 %d: %v", n, alerts.Texts())
	}
}
