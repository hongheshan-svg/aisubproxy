package cardpool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

const (
	pathAuthTxns   = "/v1/card/transactions/authorization"
	pathSettleTxns = "/v1/card/transactions/settlement"
	hourSec        = int64(3600)
	daySec         = 24 * hourSec
)

// txnPage 生成交易查询的顶层分页响应。
func txnPage(count int, items ...string) mwtest.Reply {
	return mwtest.Reply{Body: fmt.Sprintf(`{"count":"%d","data":[%s]}`, count, strings.Join(items, ","))}
}

// authItem 生成一条拉取到的授权交易；extra 原样拼在末尾（以逗号开头）。
func authItem(id, card string, at int64, extra string) string {
	return fmt.Sprintf(`{"transaction_id":"%s","transaction_type":"AUTH","card_id":"%s","status":"APPROVED",`+
		`"transaction_amount":"1.00","transaction_currency":"USD","billing_amount":"1.00","billing_currency":"USD",`+
		`"merchant":{"merchant_name":"SHOP"},"transaction_time":%d%s}`, id, card, at, extra)
}

func settleItem(id, authID, card string, at int64) string {
	return fmt.Sprintf(`{"transaction_id":"%s","transaction_type":"SETTLE","auth_transaction_id":"%s","card_id":"%s","status":"approved",`+
		`"transaction_amount":"1.00","transaction_currency":"USD","billing_amount":"1.00","billing_currency":"USD",`+
		`"merchant":{"merchant_name":"SHOP"},"transaction_time":%d}`, id, authID, card, at)
}

func setBackfillState(t *testing.T, st BackfillState) {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(backfillStateKey, string(b)); err != nil {
		t.Fatal(err)
	}
}

func mustBackfillState(t *testing.T, s *Service) BackfillState {
	t.Helper()
	st, err := s.BackfillStatus(context.Background())
	if err != nil {
		t.Fatalf("BackfillStatus: %v", err)
	}
	return st
}

// qInt 取请求查询串里的整数参数。
func qInt(t *testing.T, r mwtest.Request, key string) int64 {
	t.Helper()
	v, err := url.ParseQuery(r.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(v.Get(key), 10, 64)
	if err != nil {
		t.Fatalf("请求 %s 的 %s = %q: %v", r.RawQuery, key, v.Get(key), err)
	}
	return n
}

func ptrEq(p *int64, v int64) bool { return p != nil && *p == v }

func fmtPtr(p *int64) string {
	if p == nil {
		return "nil"
	}
	return strconv.FormatInt(*p, 10)
}

// 首次（无 cursor）：窗口 [now−7d, now]，按 24 小时切 7 段，每段授权、结算各拉一次。
func TestBackfillFirstRunCovers7Days(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	mw.On("GET", pathAuthTxns, txnPage(0))
	mw.On("GET", pathSettleTxns, txnPage(0))
	if err := s.backfillOnce(context.Background()); err != nil {
		t.Fatalf("backfillOnce: %v", err)
	}
	now := clock.Now().Unix()
	for _, p := range []string{pathAuthTxns, pathSettleTxns} {
		reqs := mw.Requests("GET", p)
		if len(reqs) != 7 {
			t.Fatalf("%s 应请求 7 次，实际 %d", p, len(reqs))
		}
		for i, r := range reqs {
			a := now - 7*daySec + int64(i)*daySec
			if qInt(t, r, "start_time") != a || qInt(t, r, "end_time") != a+daySec ||
				qInt(t, r, "page_no") != 1 || qInt(t, r, "page_size") != 50 {
				t.Fatalf("%s 第 %d 段请求: %s，期望 start_time=%d end_time=%d", p, i+1, r.RawQuery, a, a+daySec)
			}
		}
	}
	st := mustBackfillState(t, s)
	if !ptrEq(st.Cursor, now) || !ptrEq(st.LastRunAt, now) || st.LastError != "" || st.LastAdded != 0 {
		t.Fatalf("状态: cursor=%s last_run_at=%s %+v", fmtPtr(st.Cursor), fmtPtr(st.LastRunAt), st)
	}
}

// 起点 = cursor − 2h；同一交易两轮都返回只写 1 行，第二轮 last_added = 0。
func TestBackfillOverlapAndDedupe(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	ctx := context.Background()
	now := clock.Now().Unix()
	cursor := now - hourSec
	setBackfillState(t, BackfillState{Cursor: &cursor})
	mw.On("GET", pathAuthTxns, txnPage(1, authItem("A1", "cx", now-1800, "")))
	mw.On("GET", pathSettleTxns, txnPage(1, settleItem("S1", "A1", "cx", now-1200)))

	if err := s.backfillOnce(ctx); err != nil {
		t.Fatalf("第一轮: %v", err)
	}
	reqs := mw.Requests("GET", pathAuthTxns)
	if len(reqs) != 1 || qInt(t, reqs[0], "start_time") != cursor-2*hourSec || qInt(t, reqs[0], "end_time") != now {
		t.Fatalf("第一轮授权请求（累计达到 count 即停）: %+v", reqs)
	}
	if st := mustBackfillState(t, s); st.LastAdded != 2 || !ptrEq(st.Cursor, now) {
		t.Fatalf("第一轮状态: cursor=%s %+v", fmtPtr(st.Cursor), st)
	}
	x := mustGetTxn(t, "A1")
	if x.Kind != "AUTH" || x.CardID != "cx" || x.Status != "APPROVED" || x.MerchantName != "SHOP" || !ptrEq(x.OccurredAt, now-1800) {
		t.Fatalf("A1: %+v", x)
	}
	y := mustGetTxn(t, "S1")
	if y.Kind != "SETTLE" || y.AuthTransactionID != "A1" || y.MerchantName != "SHOP" || !ptrEq(y.OccurredAt, now-1200) {
		t.Fatalf("S1: %+v", y)
	}

	clock.Advance(15 * time.Minute)
	if err := s.backfillOnce(ctx); err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	reqs = mw.Requests("GET", pathAuthTxns)
	if len(reqs) != 2 || qInt(t, reqs[1], "start_time") != now-2*hourSec {
		t.Fatalf("第二轮应从上一轮 cursor − 2h 开始: %+v", reqs)
	}
	for _, id := range []string{"A1", "S1"} {
		if n := countRows(t, `SELECT COUNT(*) FROM mw_card_transactions WHERE transaction_id = ?`, id); n != 1 {
			t.Fatalf("%s 应只有 1 行，实际 %d", id, n)
		}
	}
	if st := mustBackfillState(t, s); st.LastAdded != 0 || !ptrEq(st.Cursor, clock.Now().Unix()) {
		t.Fatalf("第二轮状态: cursor=%s %+v", fmtPtr(st.Cursor), st)
	}
}

// 第 2 段授权失败：本轮停止，cursor 停在第 1 段终点并记 last_error；下一轮从该 cursor − 2h 继续。
func TestBackfillFailureKeepsCursor(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	ctx := context.Background()
	now := clock.Now().Unix()
	cursor := now - 3*daySec
	setBackfillState(t, BackfillState{Cursor: &cursor})
	mw.On("GET", pathAuthTxns, txnPage(0), mwtest.Unknown(), txnPage(0))
	mw.On("GET", pathSettleTxns, txnPage(0))

	if err := s.backfillOnce(ctx); err == nil {
		t.Fatal("第 2 段失败，backfillOnce 应返回错误")
	}
	firstEnd := cursor - 2*hourSec + daySec
	st := mustBackfillState(t, s)
	if !ptrEq(st.Cursor, firstEnd) || st.LastError == "" || !ptrEq(st.LastRunAt, now) {
		t.Fatalf("失败后状态: cursor=%s（期望 %d） %+v", fmtPtr(st.Cursor), firstEnd, st)
	}
	if n := len(mw.Requests("GET", pathAuthTxns)); n != 2 {
		t.Fatalf("授权应请求 2 次（第 2 段失败即停），实际 %d", n)
	}
	if n := len(mw.Requests("GET", pathSettleTxns)); n != 1 {
		t.Fatalf("结算只应拉第 1 段，实际 %d 次", n)
	}

	clock.Advance(15 * time.Minute)
	if err := s.backfillOnce(ctx); err != nil {
		t.Fatalf("下一轮: %v", err)
	}
	reqs := mw.Requests("GET", pathAuthTxns)
	if qInt(t, reqs[2], "start_time") != firstEnd-2*hourSec {
		t.Fatalf("下一轮应从 %d 开始: %s", firstEnd-2*hourSec, reqs[2].RawQuery)
	}
	st = mustBackfillState(t, s)
	if !ptrEq(st.Cursor, clock.Now().Unix()) || st.LastError != "" {
		t.Fatalf("恢复后状态: cursor=%s %+v", fmtPtr(st.Cursor), st)
	}
}

// 落后 10 天：本轮只处理起点后的 7 天；再一轮追平到 now。
func TestBackfillCatchUpCapped(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	ctx := context.Background()
	now := clock.Now().Unix()
	cursor := now - 10*daySec
	setBackfillState(t, BackfillState{Cursor: &cursor})
	mw.On("GET", pathAuthTxns, txnPage(0))
	mw.On("GET", pathSettleTxns, txnPage(0))

	if err := s.backfillOnce(ctx); err != nil {
		t.Fatal(err)
	}
	start := cursor - 2*hourSec
	reqs := mw.Requests("GET", pathAuthTxns)
	if len(reqs) != 7 || qInt(t, reqs[0], "start_time") != start || qInt(t, reqs[6], "end_time") != start+7*daySec {
		t.Fatalf("本轮应为 [%d, %d) 共 7 段: %d 次", start, start+7*daySec, len(reqs))
	}
	if st := mustBackfillState(t, s); !ptrEq(st.Cursor, start+7*daySec) {
		t.Fatalf("cursor=%s，期望 %d", fmtPtr(st.Cursor), start+7*daySec)
	}

	if err := s.backfillOnce(ctx); err != nil {
		t.Fatal(err)
	}
	reqs = mw.Requests("GET", pathAuthTxns)[7:]
	if len(reqs) != 4 || qInt(t, reqs[0], "start_time") != start+7*daySec-2*hourSec || qInt(t, reqs[3], "end_time") != now {
		t.Fatalf("第二轮应追平到 now: %d 次", len(reqs))
	}
	if st := mustBackfillState(t, s); !ptrEq(st.Cursor, now) {
		t.Fatalf("cursor=%s，期望 %d", fmtPtr(st.Cursor), now)
	}
}

// 手动补拉：窗口起点晚于 cursor 时不推进 cursor；不晚于时推进；天数只能 1 / 3 / 7。
func TestBackfillManualCursorRule(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	ctx := context.Background()
	now := clock.Now().Unix()
	cursor := now - 2*daySec
	setBackfillState(t, BackfillState{Cursor: &cursor})
	mw.On("GET", pathAuthTxns, txnPage(0))
	mw.On("GET", pathSettleTxns, txnPage(0))

	st, err := s.BackfillRecent(ctx, 1)
	if err != nil {
		t.Fatalf("手动 1 天: %v", err)
	}
	if !ptrEq(st.Cursor, cursor) || !ptrEq(st.LastRunAt, now) || st.LastError != "" {
		t.Fatalf("手动 1 天后 cursor 不应变化: cursor=%s %+v", fmtPtr(st.Cursor), st)
	}
	reqs := mw.Requests("GET", pathAuthTxns)
	if len(reqs) != 1 || qInt(t, reqs[0], "start_time") != now-daySec || qInt(t, reqs[0], "end_time") != now {
		t.Fatalf("手动 1 天的请求: %+v", reqs)
	}
	if got := mustBackfillState(t, s); !ptrEq(got.Cursor, cursor) {
		t.Fatalf("保存的 cursor=%s，期望 %d", fmtPtr(got.Cursor), cursor)
	}

	st, err = s.BackfillRecent(ctx, 3)
	if err != nil {
		t.Fatalf("手动 3 天: %v", err)
	}
	if !ptrEq(st.Cursor, now) {
		t.Fatalf("手动 3 天后 cursor 应为 now: %s", fmtPtr(st.Cursor))
	}
	reqs = mw.Requests("GET", pathAuthTxns)[1:]
	if len(reqs) != 3 || qInt(t, reqs[0], "start_time") != now-3*daySec {
		t.Fatalf("手动 3 天的请求: %+v", reqs)
	}
	if got := mustBackfillState(t, s); !ptrEq(got.Cursor, now) {
		t.Fatalf("保存的 cursor=%s，期望 %d", fmtPtr(got.Cursor), now)
	}

	for _, d := range []int{2, 0, -1, 8} {
		if _, err := s.BackfillRecent(ctx, d); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("days=%d 应为 ErrBadRequest: %v", d, err)
		}
	}
	if n := len(mw.Requests("GET", pathAuthTxns)); n != 4 {
		t.Fatalf("非法天数不应调接口: %d", n)
	}
}

// 拉取到的授权交易带验证码：丢弃，不进验证码表，交易照常写入。
func TestBackfillDropsOTP(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	now := clock.Now().Unix()
	cursor := now - hourSec
	setBackfillState(t, BackfillState{Cursor: &cursor})
	mw.On("GET", pathAuthTxns, txnPage(1, authItem("A1", "cx", now-600, `,"otp":"123456"`)))
	mw.On("GET", pathSettleTxns, txnPage(0))
	if err := s.backfillOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_3ds_otps`); n != 0 {
		t.Fatalf("验证码表应为空，实际 %d 行", n)
	}
	x := mustGetTxn(t, "A1")
	b, _ := json.Marshal(x)
	raw, _ := db.GetSetting(backfillStateKey)
	if strings.Contains(string(b), "123456") || strings.Contains(raw, "123456") {
		t.Fatalf("验证码不应落库: txn=%s state=%s", b, raw)
	}
}

// 补拉同样执行「未交卡号出现授权」规则，但只认入池（created_at）之后的交易。
func TestBackfillUnusedAuthRule(t *testing.T) {
	s, mw, clock, alerts := newTestService(t)
	now := clock.Now().Unix()
	at := now - 1800
	insertCard(t, Card{CardID: "c1", ProductID: "p1", CreatedAt: now - 2*daySec}) // 入池早于交易 → 停用
	insertCard(t, Card{CardID: "c2", ProductID: "p1"})                            // 入池（now）晚于交易 → 不停用
	insertCard(t, Card{CardID: "c3", ProductID: "p1", CreatedAt: now - 2*daySec}) // 交易没有时间 → 不处理
	insertCard(t, Card{CardID: "c4", ProductID: "p1", CreatedAt: at})             // 与交易同一秒 → 不停用
	cursor := now - hourSec
	setBackfillState(t, BackfillState{Cursor: &cursor})
	mw.On("GET", pathAuthTxns, txnPage(4,
		authItem("A1", "c1", at, ""), authItem("A2", "c2", at, ""), authItem("A4", "c4", at, ""),
		`{"transaction_id":"A3","transaction_type":"AUTH","card_id":"c3","status":"APPROVED","transaction_amount":"1.00","transaction_currency":"USD"}`))
	mw.On("GET", pathSettleTxns, txnPage(0))
	if err := s.backfillOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolDisabled || c.DisabledReason != "未交出卡号出现授权" {
		t.Fatalf("c1 应停用: %+v", c)
	}
	for _, id := range []string{"c2", "c3", "c4"} {
		if c := mustGetCard(t, id); c.PoolState != PoolAvailable {
			t.Fatalf("%s 不应停用: %+v", id, c)
		}
	}
	if alerts.Count() != 1 || alertsContaining(alerts, "c1") != 1 || alertsContaining(alerts, "A1") != 1 {
		t.Fatalf("应只对 c1 告警一次: %v", alerts.Texts())
	}
	if x := mustGetTxn(t, "A3"); x.OccurredAt != nil {
		t.Fatalf("A3 没有交易时间: %+v", x)
	}
}

// 平台 count 与实际不符：空页即停；一直满页时每段每个接口最多 200 页。
func TestBackfillStopsOnShortOrEmptyPage(t *testing.T) {
	fullPage := func(now int64) []string {
		items := make([]string, 50)
		for i := range items {
			items[i] = authItem(fmt.Sprintf("A%03d", i), "", now-600, "")
		}
		return items
	}
	t.Run("count 很大但第 2 页为空", func(t *testing.T) {
		s, mw, clock, _ := newTestService(t)
		now := clock.Now().Unix()
		cursor := now - hourSec
		setBackfillState(t, BackfillState{Cursor: &cursor})
		mw.On("GET", pathAuthTxns, txnPage(500, fullPage(now)...), txnPage(500))
		mw.On("GET", pathSettleTxns, txnPage(0))
		if err := s.backfillOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		reqs := mw.Requests("GET", pathAuthTxns)
		if len(reqs) != 2 || qInt(t, reqs[1], "page_no") != 2 {
			t.Fatalf("应在第 2 页停止: %d 次", len(reqs))
		}
		if n := len(mw.Requests("GET", pathSettleTxns)); n != 1 {
			t.Fatalf("结算应照常拉取: %d", n)
		}
		if st := mustBackfillState(t, s); st.LastAdded != 50 || !ptrEq(st.Cursor, now) || st.LastError != "" {
			t.Fatalf("状态: cursor=%s %+v", fmtPtr(st.Cursor), st)
		}
	})
	t.Run("一直满页", func(t *testing.T) {
		s, mw, clock, _ := newTestService(t)
		now := clock.Now().Unix()
		cursor := now - hourSec
		setBackfillState(t, BackfillState{Cursor: &cursor})
		mw.On("GET", pathAuthTxns, txnPage(1000000, fullPage(now)...))
		mw.On("GET", pathSettleTxns, txnPage(0))
		if err := s.backfillOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		reqs := mw.Requests("GET", pathAuthTxns)
		if len(reqs) != 200 || qInt(t, reqs[199], "page_no") != 200 {
			t.Fatalf("应在第 200 页后停止: %d 次", len(reqs))
		}
		if st := mustBackfillState(t, s); !ptrEq(st.Cursor, now) {
			t.Fatalf("cursor=%s", fmtPtr(st.Cursor))
		}
	})
}

// 手动与定时补拉并发：两者串行，都成功，期间读到的 cursor 只增不减。
func TestBackfillConcurrentRunsSerialize(t *testing.T) {
	s, mw, clock, _ := newTestService(t)
	ctx := context.Background()
	mw.On("GET", pathAuthTxns, mwtest.Reply{Body: `{"count":"0","data":[]}`, Hang: 100 * time.Millisecond})
	mw.On("GET", pathSettleTxns, txnPage(0))

	stop := make(chan struct{})
	polled := make(chan []int64)
	go func() {
		var seen []int64
		for {
			if st, err := s.BackfillStatus(ctx); err == nil && st.Cursor != nil {
				seen = append(seen, *st.Cursor)
			}
			select {
			case <-stop:
				polled <- seen
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()

	// 定时补拉（无 cursor，7 段）先开始并已发出第一个请求，再发起手动补拉
	var wg sync.WaitGroup
	var errOnce, errRecent error
	wg.Add(2)
	go func() { defer wg.Done(); errOnce = s.backfillOnce(ctx) }()
	if !waitFor(time.Second, func() bool { return len(mw.Requests("GET", pathAuthTxns)) > 0 }) {
		t.Fatal("定时补拉未开始")
	}
	go func() { defer wg.Done(); _, errRecent = s.BackfillRecent(ctx, 1) }()
	wg.Wait()
	close(stop)
	seen := <-polled

	if errOnce != nil || errRecent != nil {
		t.Fatalf("都应成功: once=%v recent=%v", errOnce, errRecent)
	}
	now := clock.Now().Unix()
	reqs := mw.Requests("GET", pathAuthTxns)
	if len(reqs) != 8 || qInt(t, reqs[7], "start_time") != now-daySec {
		t.Fatalf("手动补拉应等定时补拉 7 段做完再开始: %d 次", len(reqs))
	}
	if len(seen) == 0 {
		t.Fatal("期间没有读到 cursor")
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("cursor 回退: %v", seen)
		}
	}
	if st := mustBackfillState(t, s); !ptrEq(st.Cursor, now) {
		t.Fatalf("最终 cursor=%s", fmtPtr(st.Cursor))
	}
}

// 取不到客户端：本轮跳过，不改补拉状态、不调接口。
func TestBackfillSkipsWhenClientMissing(t *testing.T) {
	_, mw, clock, alerts := newTestService(t)
	cursor := clock.Now().Unix() - daySec
	setBackfillState(t, BackfillState{Cursor: &cursor, LastAdded: 3, LastError: "旧错误"})
	before, err := db.GetSetting(backfillStateKey)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{
		Client: func() (*marswallex.Client, error) { return nil, errors.New("未配置") },
		Now:    clock.Now,
		Alert:  alerts.add,
	})
	s.backfillRound(context.Background(), &loopLog{now: clock.Now, last: map[string]time.Time{}})
	after, err := db.GetSetting(backfillStateKey)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("状态不应变化:\n前 %s\n后 %s", before, after)
	}
	if n := len(mw.Requests("GET", pathAuthTxns)) + len(mw.Requests("GET", pathSettleTxns)); n != 0 {
		t.Fatalf("不应调接口: %d", n)
	}
	if _, err := s.BackfillRecent(context.Background(), 1); err == nil {
		t.Fatal("手动补拉取不到客户端应返回错误")
	}
}

// 推送路径保持原行为：入池时间晚于交易的未交卡号卡仍被停用。
func TestAuthWebhookRuleUnchanged(t *testing.T) {
	s, _, clock, alerts := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1", CreatedAt: clock.Now().Unix() + hourSec})
	id := enqueueEvent(t, marswallex.TopicAuthEvent, authData("A1", "c1", "APPROVED", "")) // 交易时间 = now
	mustProcessEvents(t, s)
	mustBeProcessed(t, id, "已停用")
	if c := mustGetCard(t, "c1"); c.PoolState != PoolDisabled || c.DisabledReason != "未交出卡号出现授权" {
		t.Fatalf("c1 应停用: %+v", c)
	}
	if alerts.Count() != 1 || alertsContaining(alerts, "c1") != 1 {
		t.Fatalf("应告警一次: %v", alerts.Texts())
	}
}
