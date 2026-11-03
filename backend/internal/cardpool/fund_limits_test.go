package cardpool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

func leaseForFund(t *testing.T, s *Service, cardID string) {
	t.Helper()
	insertCard(t, Card{CardID: cardID, ProductID: "p1"})
	if _, err := s.AdminLease(context.Background(), cardID, "alice"); err != nil {
		t.Fatal(err)
	}
}

func fundOrderCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM mw_card_orders`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 审计 TestAuditAdminFundHasNoUpperBound 反过来：超大金额被拒，平台收不到请求。
func TestAdminFundOverSingleLimitRejected(t *testing.T) {
	ctx := context.Background()
	s, mw, _, alerts := newTestService(t)
	leaseForFund(t, s, "c1")
	cents, err := marswallex.ParseAmount("999999999999.99")
	if err != nil {
		t.Fatal(err)
	}
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	if _, err := s.AdminFund(ctx, "c1", cents, "alice"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("应拒绝: %v", err)
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 0 {
		t.Fatalf("平台不应收到请求: %d", n)
	}
	if n := fundOrderCount(t); n != 0 {
		t.Fatalf("不应留下单据: %d", n)
	}
	if alerts.Count() != 1 || !strings.Contains(alerts.Texts()[0], "alice") {
		t.Fatalf("应告警并写明操作方: %v", alerts.Texts())
	}
}

func TestFundSingleLimitBoundary(t *testing.T) {
	ctx := context.Background()
	s, mw, _, alerts := newTestService(t)
	leaseForFund(t, s, "c1")
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	if _, err := s.AdminFund(ctx, "c1", DefaultMaxFundCents+1, "alice"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("超 1 分应拒绝: %v", err)
	}
	if _, err := s.AdminFund(ctx, "c1", DefaultMaxFundCents, "alice"); err != nil {
		t.Fatalf("正好等于上限应通过: %v", err)
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 1 {
		t.Fatalf("应发出 1 次: %d", n)
	}
	if alerts.Count() != 1 {
		t.Fatalf("告警: %v", alerts.Texts())
	}
}

// 付款单走的 Fund 同样受限；系统自动发起的超限告警每天一条。
func TestLeaseFundOverLimitRejectedAndAlertDeduped(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, alerts := newTestService(t)
	expires := clock.Now().Add(72 * time.Hour).Unix()
	insertCard(t, Card{CardID: "c1", ProductID: "p1", PoolState: PoolLeased, LeaseToken: "tok", LeaseOwner: "PO1", LeaseExpiresAt: &expires})
	lease := &Lease{CardID: "c1", Token: "tok"}
	for i := 0; i < 2; i++ {
		if _, err := s.Fund(ctx, lease, DefaultMaxFundCents+1); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("应拒绝: %v", err)
		}
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 0 {
		t.Fatalf("平台不应收到请求: %d", n)
	}
	if alerts.Count() != 1 {
		t.Fatalf("同一天应只告警一次: %v", alerts.Texts())
	}
	clock.Advance(24 * time.Hour)
	if _, err := s.Fund(ctx, lease, DefaultMaxFundCents+1); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("应拒绝: %v", err)
	}
	if alerts.Count() != 2 {
		t.Fatalf("第二天应再告警: %v", alerts.Texts())
	}
}

// 转出不受上限影响：回收资金不能被卡住。
func TestWithdrawNotLimited(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("POST", pathWithdraw, mwtest.OK(`{"transaction_id":"W1","status":"APPLY"}`))
	if _, err := s.submitTransfer(ctx, KindWithdraw, "c1", DefaultMaxFundCents*10, "USD", "recycle"); err != nil {
		t.Fatalf("转出不应受限: %v", err)
	}
}

func insertFundedOrder(t *testing.T, kind, state string, cents, createdAt int64, card string) {
	t.Helper()
	insertOrder(t, Order{Kind: kind, State: state, AmountCents: cents, Currency: "USD", CardID: card, CreatedAt: createdAt, UpdatedAt: createdAt})
}

// 北京日边界：UTC 16:00 是北京 0 点。前一天的不算，当天的算。
func TestDailyFundCapUsesBeijingDay(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, alerts := newTestService(t)
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	fund := func(card string, cents int64) error {
		_, err := s.submitTransfer(ctx, KindRecharge, card, cents, "USD", "alice")
		return err
	}
	clock.Set(time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC))                              // 北京 10-29 00:00
	insertFundedOrder(t, KindRecharge, StateSuccess, 190000, clock.Now().Unix()-1, "old1") // 北京 10-28 23:59:59
	if err := fund("c1", 50000); err != nil {
		t.Fatalf("前一天的不应计入: %v", err)
	}
	insertFundedOrder(t, KindRecharge, StateSuccess, 150000, clock.Now().Unix(), "today1") // 累计正好 200000
	if err := fund("c2", 1); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("超过当日累计应拒绝: %v", err)
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 1 {
		t.Fatalf("平台只应收到第一笔: %d", n)
	}
	if alerts.Count() != 1 || !strings.Contains(alerts.Texts()[0], "今日注资累计") {
		t.Fatalf("告警: %v", alerts.Texts())
	}
	// 北京 10-29 23:59:59 还是同一天
	clock.Set(time.Date(2026, 10, 29, 15, 59, 59, 0, time.UTC))
	if err := fund("c3", 1); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("同一北京日应继续拒绝: %v", err)
	}
	clock.Set(time.Date(2026, 10, 29, 16, 0, 0, 0, time.UTC)) // 北京 10-30 00:00
	if err := fund("c4", 1); err != nil {
		t.Fatalf("次日应重新计数: %v", err)
	}
}

// 明确没动钱的单不计入；结果不明的计入：钱可能已经转了。转出不计入。
func TestDailyFundCapCountsByState(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	leaseForFund(t, s, "c1")
	now := clock.Now().Unix()
	insertFundedOrder(t, KindRecharge, StateFailed, 200000, now, "x1")
	insertFundedOrder(t, KindRecharge, StateNotSent, 200000, now, "x2")
	insertFundedOrder(t, KindWithdraw, StateSuccess, 200000, now, "x3")
	if _, err := s.AdminFund(ctx, "c1", 1000, "alice"); err != nil {
		t.Fatalf("失败、未发出、转出都不应计入: %v", err)
	}
	if _, err := db.DB.Exec(`UPDATE mw_card_orders SET state = ? WHERE card_id = 'c1'`, StateFailed); err != nil {
		t.Fatal(err)
	}
	for i, st := range []string{StateUnknown, StateProcessing, StateNeedsReview, StateSuccess, StatePending} {
		insertFundedOrder(t, KindRecharge, st, 40000, now, fmt.Sprintf("y%d", i))
	}
	if _, err := s.AdminFund(ctx, "c1", 1, "alice"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("结果不明和进行中的单都应计入: %v", err)
	}
	// 带金额的开卡也计入
	if _, err := db.DB.Exec(`UPDATE mw_card_orders SET state = ? WHERE state != ?`, StateFailed, StateFailed); err != nil {
		t.Fatal(err)
	}
	insertFundedOrder(t, KindApply, StateSuccess, 200000, now, "")
	if _, err := s.AdminFund(ctx, "c1", 1, "alice"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("带金额的开卡应计入累计: %v", err)
	}
}

// 真实文件库、多连接：并发注资不能绕过日限。内存库只有一条连接，测不出这个问题。
func TestDailyFundCapConcurrentOnRealFile(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	c, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(25)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	const n = 20
	for i := 0; i < n; i++ {
		leaseForFund(t, s, fmt.Sprintf("c%d", i))
	}
	var wg sync.WaitGroup
	var ok, rejected, other atomic.Int32
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := s.AdminFund(ctx, fmt.Sprintf("c%d", i), 50000, "alice")
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, ErrBadRequest):
				rejected.Add(1)
			default:
				other.Add(1)
				t.Errorf("意外错误: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if ok.Load() != 4 || rejected.Load() != n-4 || other.Load() != 0 { // 上限 200000，每笔 50000
		t.Fatalf("ok=%d rejected=%d other=%d", ok.Load(), rejected.Load(), other.Load())
	}
	if got := len(mw.Requests("POST", pathRecharge)); got != 4 {
		t.Fatalf("平台应只收到 4 笔: %d", got)
	}
}

func TestApplyAmountCountsAgainstLimits(t *testing.T) {
	ctx := context.Background()
	s, mw, _, alerts := newTestService(t)
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"A1"}`))
	params := applyParams{HolderID: "h1", ProductID: "p1", Currency: "USD", NamePrefix: "POOL", Expiry: "1028", By: "replenish"}

	params.AmountCents = DefaultMaxFundCents + 1
	if _, err := s.submitApply(ctx, params); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("开卡金额超单笔上限应拒绝: %v", err)
	}
	params.AmountCents = 0
	if _, err := s.submitApply(ctx, params); err != nil {
		t.Fatalf("金额为 0 的开卡不受限: %v", err)
	}
	insertFundedOrder(t, KindRecharge, StateSuccess, DefaultDailyFundCapCents, testNow().Unix(), "x")
	params.AmountCents = 1
	if _, err := s.submitApply(ctx, params); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("开卡金额超日限应拒绝: %v", err)
	}
	params.AmountCents = 0
	if _, err := s.submitApply(ctx, params); err != nil {
		t.Fatalf("日限满了也能开 0 元卡: %v", err)
	}
	if n := len(mw.Requests("POST", pathApply)); n != 2 {
		t.Fatalf("平台只应收到 2 次开卡: %d", n)
	}
	if alerts.Count() != 2 {
		t.Fatalf("单笔和日限各一条: %v", alerts.Texts())
	}
}

// 兑换、钱包转账超限：连账户同步都不发，平台收不到任何请求。
func TestWalletOpsOverLimitNeverReachPlatform(t *testing.T) {
	ctx := context.Background()
	s, mw, _, alerts := newTestService(t)
	if _, err := s.SubmitConversion(ctx, ConversionParams{SellCurrency: "USD", BuyCurrency: "HKD", SellCents: DefaultMaxWalletOpCents + 1, By: "alice"}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("兑换应拒绝: %v", err)
	}
	if _, err := s.SubmitWalletTransfer(ctx, WalletTransferParams{PayerAccountNo: "A", PayeeAccountNo: "B", Currency: "USD", AmountCents: DefaultMaxWalletOpCents + 1, By: "alice"}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("转账应拒绝: %v", err)
	}
	if n := len(mw.Requests("GET", pathAccounts)) + len(mw.Requests("POST", pathConversion)) + len(mw.Requests("POST", pathWalletTransfer)); n != 0 {
		t.Fatalf("平台不应收到任何请求: %d", n)
	}
	if n := fundOrderCount(t); n != 0 {
		t.Fatalf("不应留下单据: %d", n)
	}
	if alerts.Count() != 2 {
		t.Fatalf("应各告警一次: %v", alerts.Texts())
	}
}

func TestPolicyLimitDefaults(t *testing.T) {
	p := DefaultPolicy()
	if p.MaxFundCents != 50000 || p.MaxWalletOpCents != 200000 || p.DailyFundCapCents != 200000 {
		t.Fatalf("默认值与决策不符: %+v", p)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyLimitsRejectNonPositive(t *testing.T) {
	newTestService(t)
	cases := map[string]func(*Policy){
		"单笔注资 0":  func(p *Policy) { p.MaxFundCents = 0 },
		"单笔注资负数":  func(p *Policy) { p.MaxFundCents = -1 },
		"钱包 0":    func(p *Policy) { p.MaxWalletOpCents = 0 },
		"钱包负数":    func(p *Policy) { p.MaxWalletOpCents = -1 },
		"日限 0":    func(p *Policy) { p.DailyFundCapCents = 0 },
		"日限负数":    func(p *Policy) { p.DailyFundCapCents = -1 },
		"开卡金额超单笔": func(p *Policy) { p.OpenAmountCents = p.MaxFundCents + 1 },
	}
	for name, f := range cases {
		p := DefaultPolicy()
		f(&p)
		if err := SavePolicy(p); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("%s 应拒绝保存: %v", name, err)
		}
	}
	if v, _ := db.GetSetting(PolicySettingKey); v != "" {
		t.Fatalf("校验失败不应写入: %s", v)
	}
}

// 升级：旧策略没有这些字段时用默认值，不能变成 0（不限）。手改过的存量 0 或负数同样归一。
func TestLoadPolicyFillsLimitsForOldPolicies(t *testing.T) {
	newTestService(t)
	cases := map[string]string{
		"旧策略没有字段":  `{"card_name_prefix":"OLD","open_amount_cents":100}`,
		"存量为 0":    `{"card_name_prefix":"OLD","open_amount_cents":100,"max_fund_cents":0,"max_wallet_op_cents":0,"daily_fund_cap_cents":0}`,
		"存量为负":     `{"card_name_prefix":"OLD","open_amount_cents":100,"max_fund_cents":-5,"max_wallet_op_cents":-5,"daily_fund_cap_cents":-5}`,
		"存量为 null": `{"card_name_prefix":"OLD","open_amount_cents":100,"max_fund_cents":null,"max_wallet_op_cents":null,"daily_fund_cap_cents":null}`,
	}
	for name, raw := range cases {
		if err := db.SetSetting(PolicySettingKey, raw); err != nil {
			t.Fatal(err)
		}
		p, err := LoadPolicy()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.CardNamePrefix != "OLD" || p.OpenAmountCents != 100 {
			t.Fatalf("%s 其它字段不应变: %+v", name, p)
		}
		if p.MaxFundCents != DefaultMaxFundCents || p.MaxWalletOpCents != DefaultMaxWalletOpCents || p.DailyFundCapCents != DefaultDailyFundCapCents {
			t.Fatalf("%s 应用默认上限: %+v", name, p)
		}
	}
}

func TestPolicyLimitsConfigurable(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	leaseForFund(t, s, "c1")
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	p := DefaultPolicy()
	p.MaxFundCents = 700
	if err := SavePolicy(p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminFund(ctx, "c1", 701, "alice"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("应按新上限拒绝: %v", err)
	}
	if _, err := s.AdminFund(ctx, "c1", 700, "alice"); err != nil {
		t.Fatalf("应按新上限通过: %v", err)
	}
}

// 策略读不出来时不能放钱出去。
func TestFundFailsClosedWhenPolicyCorrupt(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	leaseForFund(t, s, "c1")
	mw.On("POST", pathRecharge, mwtest.OK(`{"transaction_id":"T1","status":"APPLY"}`))
	if err := db.SetSetting(PolicySettingKey, `{broken`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminFund(ctx, "c1", 100, "alice"); err == nil {
		t.Fatal("策略损坏应拒绝注资")
	}
	if _, err := s.SubmitConversion(ctx, ConversionParams{SellCurrency: "USD", BuyCurrency: "HKD", SellCents: 100}); err == nil {
		t.Fatal("策略损坏应拒绝兑换")
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 0 {
		t.Fatalf("平台不应收到请求: %d", n)
	}
}
