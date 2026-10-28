package cardpool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

func otpData(cardID, otp string) string {
	return `{"card_id":"` + cardID + `","card_no":"4111********1234","otp":"` + otp +
		`","transaction_currency":"USD","transaction_amount":"9.99","merchant_name":"SHOP"}`
}

type otpRow struct {
	sealed, merchant, amount, currency string
	receivedAt                         int64
	consumedAt                         *int64
}

func otpRows(t *testing.T, cardID string) []otpRow {
	t.Helper()
	rows, err := db.DB.Query(`SELECT otp_sealed, merchant_name, amount, currency, received_at, consumed_at
		FROM mw_3ds_otps WHERE card_id = ? ORDER BY id`, cardID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []otpRow
	for rows.Next() {
		var r otpRow
		if err := rows.Scan(&r.sealed, &r.merchant, &r.amount, &r.currency, &r.receivedAt, &r.consumedAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func openOTP(t *testing.T, sealed string) string {
	t.Helper()
	key, err := secretbox.LoadKey()
	if err != nil {
		t.Fatal(err)
	}
	otp, err := secretbox.Open(key, otpAAD, sealed)
	if err != nil {
		t.Fatal(err)
	}
	return otp
}

// mustBeRedacted 检查原文已抹掉验证码，且仍能解析出 otp = "***"。
func mustBeRedacted(t *testing.T, e eventState, otp string) {
	t.Helper()
	if strings.Contains(e.payload, otp) || !strings.Contains(e.payload, "***") {
		t.Fatalf("原文未抹除验证码: %s", e.payload)
	}
	ev, err := marswallex.DecodeWebhookPayload([]byte(e.payload))
	if err != nil {
		t.Fatalf("抹除后原文无法解析: %v", err)
	}
	var d struct {
		OTP string `json:"otp"`
	}
	if err := ev.DecodeData(&d); err != nil || d.OTP != "***" {
		t.Fatalf("data.otp = %q, err = %v", d.OTP, err)
	}
	if strings.Contains(e.note, otp) || strings.Contains(e.procErr, otp) {
		t.Fatalf("备注或错误里出现验证码: %+v", e)
	}
}

func TestOTPEventStoredEncryptedAndRedacted(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	strData, err := json.Marshal(otpData("c2", "123456"))
	if err != nil {
		t.Fatal(err)
	}
	e1 := enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "123456"))
	e2 := enqueueEvent(t, marswallex.TopicCard3DSOTP, string(strData))
	if n := mustProcessEvents(t, s); n != 2 {
		t.Fatalf("处理了 %d 条，期望 2", n)
	}
	for _, tc := range []struct {
		id   int64
		card string
	}{{e1, "c1"}, {e2, "c2"}} {
		e := mustBeProcessed(t, tc.id, "")
		mustBeRedacted(t, e, "123456")
		rows := otpRows(t, tc.card)
		if len(rows) != 1 {
			t.Fatalf("卡 %s 应有 1 条验证码，实际 %d", tc.card, len(rows))
		}
		r := rows[0]
		if !strings.HasPrefix(r.sealed, "v1:") || strings.Contains(r.sealed, "123456") {
			t.Fatalf("密文: %q", r.sealed)
		}
		if openOTP(t, r.sealed) != "123456" {
			t.Fatal("解密结果不对")
		}
		if r.merchant != "SHOP" || r.amount != "9.99" || r.currency != "USD" || r.receivedAt != clock.Now().Unix() || r.consumedAt != nil {
			t.Fatalf("验证码行: %+v", r)
		}
	}
	// 字符串形式的 data 抹除后仍是字符串
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(getEvent(t, e2).payload), &env); err != nil || !strings.HasPrefix(string(env.Data), `"`) {
		t.Fatalf("data 应仍为字符串: %s %v", env.Data, err)
	}
}

func TestAuthEventOTPRedacted(t *testing.T) {
	s, _, _, _ := newTestService(t)
	id := enqueueEvent(t, marswallex.TopicAuthEvent, authData("A1", "cz", "APPROVED", `,"otp":"123456"`))
	mustProcessEvents(t, s)
	e := mustBeProcessed(t, id, "")
	mustBeRedacted(t, e, "123456")
	mustGetTxn(t, "A1")
	if n := countRows(t, `SELECT COUNT(*) FROM mw_3ds_otps`); n != 0 {
		t.Fatalf("授权推送不写验证码表: %d", n)
	}
}

func TestWaitOTPConsumes(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	since := clock.Now()
	enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "123456"))
	mustProcessEvents(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	otp, err := s.WaitOTP(ctx, "c1", since)
	if err != nil || otp != "123456" {
		t.Fatalf("WaitOTP = %q, %v", otp, err)
	}
	rows := otpRows(t, "c1")
	if len(rows) != 1 || rows[0].sealed != "" || rows[0].consumedAt == nil || *rows[0].consumedAt != clock.Now().Unix() {
		t.Fatalf("取用后应清空密文: %+v", rows)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	if otp, err := s.WaitOTP(ctx2, "c1", since); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("再次 WaitOTP 应超时: %q, %v", otp, err)
	}
}

func TestWaitOTPIgnoresOlderThanSince(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	t0 := clock.Now()
	enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "111111"))
	mustProcessEvents(t, s)

	clock.Advance(10 * time.Second)
	since := clock.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if otp, err := s.WaitOTP(ctx, "c1", since); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("早于 since 的验证码不应取到: %q, %v", otp, err)
	}

	// 等待过程中到达的新验证码能取到
	type result struct {
		otp string
		err error
	}
	done := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		otp, err := s.WaitOTP(ctx, "c1", since)
		done <- result{otp, err}
	}()
	time.Sleep(100 * time.Millisecond)
	enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "222222"))
	mustProcessEvents(t, s)
	select {
	case r := <-done:
		if r.err != nil || r.otp != "222222" {
			t.Fatalf("WaitOTP = %q, %v", r.otp, r.err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("WaitOTP 未取到新验证码")
	}
	rows := otpRows(t, "c1")
	if len(rows) != 2 || rows[0].sealed == "" || rows[1].sealed != "" {
		t.Fatalf("旧验证码不应被取用: %+v", rows)
	}

	// 超过 30 分钟的验证码即使未清除、未取用也不返回
	clock.Advance(31 * time.Minute)
	ctx3, cancel3 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel3()
	if otp, err := s.WaitOTP(ctx3, "c1", t0.Add(-time.Minute)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("过期的验证码不应取到: %q, %v", otp, err)
	}
}

func TestOTPPurgedAfter30Min(t *testing.T) {
	ctx := context.Background()
	s, _, clock, _ := newTestService(t)
	enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "123456"))
	mustProcessEvents(t, s)

	clock.Advance(30*time.Minute - time.Second)
	if err := s.purgeOTPsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if rows := otpRows(t, "c1"); rows[0].sealed == "" {
		t.Fatal("未满 30 分钟不应清除")
	}
	clock.Advance(time.Second)
	if err := s.purgeOTPsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if rows := otpRows(t, "c1"); len(rows) != 1 || rows[0].sealed != "" {
		t.Fatalf("满 30 分钟应清除密文: %+v", rows)
	}
}

func TestViewOTPDoesNotConsumeAndExpires(t *testing.T) {
	ctx := context.Background()
	s, _, clock, _ := newTestService(t)
	t0 := clock.Now()
	enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "123456"))
	mustProcessEvents(t, s)

	for i := 0; i < 2; i++ {
		v, err := s.ViewOTP(ctx, "c1", "admin")
		if err != nil {
			t.Fatalf("第 %d 次查看: %v", i+1, err)
		}
		if v.OTP != "123456" || v.MerchantName != "SHOP" || v.Amount != "9.99" || v.Currency != "USD" || !v.ReceivedAt.Equal(t0) {
			t.Fatalf("第 %d 次查看: %+v", i+1, v)
		}
	}
	if rows := otpRows(t, "c1"); rows[0].sealed == "" || rows[0].consumedAt != nil {
		t.Fatalf("查看不应清除: %+v", rows)
	}
	if _, err := s.ViewOTP(ctx, "c2", "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("没有验证码的卡: %v", err)
	}
	clock.Advance(31 * time.Minute)
	if _, err := s.ViewOTP(ctx, "c1", "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("31 分钟后应 ErrNotFound: %v", err)
	}
}

func TestMasterKeyMissingStillRedacts(t *testing.T) {
	s, _, _, _ := newTestService(t)
	t.Setenv("APP_MASTER_KEY", "")
	id := enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "123456"))
	if n := mustProcessEvents(t, s); n != 1 {
		t.Fatalf("处理了 %d 条", n)
	}
	e := getEvent(t, id)
	if !e.processed || !strings.Contains(e.procErr, "主密钥") || e.attempts != 0 {
		t.Fatalf("应标记已处理并记录主密钥错误: %+v", e)
	}
	mustBeRedacted(t, e, "123456")
	if n := countRows(t, `SELECT COUNT(*) FROM mw_3ds_otps`); n != 0 {
		t.Fatalf("不应写入验证码: %d", n)
	}
	if n := mustProcessEvents(t, s); n != 0 {
		t.Fatalf("不应重试: %d", n)
	}
}

func TestReprocessRedactedOTPDoesNotStoreStars(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newTestService(t)
	id := enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "123456"))
	mustProcessEvents(t, s)
	if err := s.Reprocess(ctx, id, "admin"); err != nil {
		t.Fatal(err)
	}
	mustProcessEvents(t, s)
	mustBeProcessed(t, id, "验证码已抹除")
	rows := otpRows(t, "c1")
	if len(rows) != 1 || openOTP(t, rows[0].sealed) != "123456" {
		t.Fatalf("重新处理不应再写入验证码: %+v", rows)
	}

	// 主密钥缺失时抹掉的验证码，恢复主密钥后重新处理也不会存下 ***
	t.Setenv("APP_MASTER_KEY", "")
	id2 := enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c2", "654321"))
	mustProcessEvents(t, s)
	t.Setenv("APP_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err := s.Reprocess(ctx, id2, "admin"); err != nil {
		t.Fatal(err)
	}
	mustProcessEvents(t, s)
	mustBeProcessed(t, id2, "验证码已抹除")
	if rows := otpRows(t, "c2"); len(rows) != 0 {
		t.Fatalf("不应写入 ***: %+v", rows)
	}
}

func TestRedactOTP(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		changed   bool
	}{
		{"对象", `{"topic":"CARD_3DS_OTP","data":{"card_id":"c1","otp":"123456","merchant_name":"A&B <x>"}}`, true},
		{"字符串", `{"topic":"CARD_3DS_OTP","data":"{\"card_id\":\"c1\",\"otp\":\"123456\"}"}`, true},
		{"大写键名", `{"topic":"AUTH_EVENT","Data":{"OTP":"123456"}}`, true},
		{"数字", `{"topic":"AUTH_EVENT","data":{"otp":123456}}`, true},
		{"已抹除", `{"topic":"CARD_3DS_OTP","data":{"otp":"***"}}`, false},
		{"没有验证码", `{"topic":"AUTH_EVENT", "data":{"card_id":"c1"}}`, false},
		{"空验证码", `{"topic":"AUTH_EVENT","data":{"otp":""}}`, false},
		{"没有 data", `{"topic":"AUTH_EVENT"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, changed, err := redactOTP(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if changed != tc.changed {
				t.Fatalf("changed = %v: %s", changed, out)
			}
			if !changed && out != tc.raw {
				t.Fatalf("没有改动时应原样返回: %s", out)
			}
			if strings.Contains(out, "123456") {
				t.Fatalf("仍含验证码: %s", out)
			}
			if changed && !strings.Contains(out, "A&B <x>") && strings.Contains(tc.raw, "A&B <x>") {
				t.Fatalf("不应转义其他字段: %s", out)
			}
		})
	}
	if _, _, err := redactOTP("not json"); err == nil {
		t.Fatal("非 JSON 应报错")
	}
}

// 验证码的收到时间取 Webhook 事件自己的收到时间，不取处理时间：重试或积压后才处理的旧验证码不能满足较新的 WaitOTP(since)。
func TestOTPReceivedAtIsEventTime(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	t0 := clock.Now()
	id := enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "111111"))
	clock.Advance(5 * time.Minute) // 处理晚于收到
	mustProcessEvents(t, s)
	mustBeProcessed(t, id, "验证码已加密保存")
	rows := otpRows(t, "c1")
	if len(rows) != 1 || rows[0].receivedAt != t0.Unix() {
		t.Fatalf("received_at 应为事件收到时间 %d: %+v", t0.Unix(), rows)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if otp, err := s.WaitOTP(ctx, "c1", t0.Add(time.Minute)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("早于新挑战的验证码不应取到: %q, %v", otp, err)
	}
}

// 事件收到已满 30 分钟才处理：不保存（备注「验证码已过期」），原文照样抹掉。
func TestOTPEventOlderThan30MinNotStored(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	id := enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "123456"))
	clock.Advance(30 * time.Minute)
	mustProcessEvents(t, s)
	e := mustBeProcessed(t, id, "验证码已过期")
	mustBeRedacted(t, e, "123456")
	if rows := otpRows(t, "c1"); len(rows) != 0 {
		t.Fatalf("过期的验证码不应保存: %+v", rows)
	}
}

// 按事件去重：两条不同事件即使验证码相同也各存一条；同一事件处理两次（写入后来不及标记已处理就崩溃）只存一条。
func TestOTPDedupeByEvent(t *testing.T) {
	s, _, _, _ := newTestService(t)
	enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "123456"))
	enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c1", "123456"))
	if n := mustProcessEvents(t, s); n != 2 {
		t.Fatalf("处理了 %d 条", n)
	}
	if rows := otpRows(t, "c1"); len(rows) != 2 {
		t.Fatalf("两条事件应各存一条: %+v", rows)
	}

	id := enqueueEvent(t, marswallex.TopicCard3DSOTP, otpData("c2", "654321"))
	raw := getEvent(t, id).payload
	mustProcessEvents(t, s)
	// 模拟崩溃：验证码已写入，但没来得及标记已处理、抹掉原文
	if _, err := db.DB.Exec(`UPDATE marswallex_webhook_events SET processed_at = NULL, process_note = '', payload = ? WHERE id = ?`, raw, id); err != nil {
		t.Fatal(err)
	}
	mustProcessEvents(t, s)
	e := mustBeProcessed(t, id, "重复")
	mustBeRedacted(t, e, "654321")
	if rows := otpRows(t, "c2"); len(rows) != 1 {
		t.Fatalf("同一事件只应存一条: %+v", rows)
	}
}

// 整条无法解析的事件也尽力抹掉原文里的验证码；抹不掉时仍标记已处理，并在备注里记下抹除失败。
func TestUnparseableEventBestEffortRedact(t *testing.T) {
	s, _, _, _ := newTestService(t)
	e1 := enqueueRaw(t, "raw-1", "", `{"topic":123,"message_id":"raw-1","data":{"card_id":"c1","otp":"123456"}}`)
	e2 := enqueueRaw(t, "raw-2", "", `garbage otp=654321`)
	if n := mustProcessEvents(t, s); n != 2 {
		t.Fatalf("处理了 %d 条", n)
	}
	ev1 := mustBeProcessed(t, e1, "无法解析")
	if strings.Contains(ev1.payload, "123456") || !strings.Contains(ev1.payload, `"otp":"***"`) {
		t.Fatalf("应抹掉验证码: %s", ev1.payload)
	}
	ev2 := mustBeProcessed(t, e2, "无法解析")
	if !strings.Contains(ev2.note, "抹除验证码失败") {
		t.Fatalf("抹除失败应记入备注: %+v", ev2)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_3ds_otps`); n != 0 {
		t.Fatalf("不应写入验证码: %d", n)
	}
}
