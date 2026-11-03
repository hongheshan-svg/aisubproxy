package cardpool

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// PR-5：MarsWallex 没被选用时，自动补卡暂停并每日告警一次；选用后、或策略显式放开时照常开卡。
func TestReplenishPausedWhenProviderUnselected(t *testing.T) {
	s, mw, clock, log := newTestService(t)
	selected := false
	s.opts.MarsWallexSelected = func(context.Context) (bool, error) { return selected, nil }
	insertHolder(t, "h1", "APPROVED")
	setTargets(t, 2, 10, "h1")
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"T1"}`))
	ctx := context.Background()

	if err := s.replenishOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.replenishOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(mw.Requests("POST", pathApply)); n != 0 {
		t.Fatalf("未选用时不应开卡: %d", n)
	}
	if n := len(mw.Requests("GET", pathProducts)); n != 0 {
		t.Fatalf("未选用时不应调平台: %d", n)
	}
	var orders int
	db.DB.QueryRow(`SELECT COUNT(*) FROM mw_card_orders WHERE kind = ?`, KindApply).Scan(&orders)
	if orders != 0 {
		t.Fatalf("不应有开卡单: %d", orders)
	}
	if log.Count() != 1 {
		t.Fatalf("同一天只告警一次: %d", log.Count())
	}
	if !strings.Contains(log.Texts()[0], "已暂停自动补卡") {
		t.Fatalf("告警: %s", log.Texts()[0])
	}
	// 第二天再告警一次
	clock.Advance(24 * time.Hour)
	if err := s.replenishOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if log.Count() != 2 {
		t.Fatalf("次日应再告警: %d", log.Count())
	}

	// 选用后照常开卡
	selected = true
	if err := s.replenishOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(mw.Requests("POST", pathApply)); n != 2 {
		t.Fatalf("选用后应开卡: %d", n)
	}
}

func TestReplenishWhenUnselectedPolicyAllows(t *testing.T) {
	s, mw, _, log := newTestService(t)
	s.opts.MarsWallexSelected = func(context.Context) (bool, error) {
		t.Error("策略显式放开时不用再判定")
		return false, nil
	}
	insertHolder(t, "h1", "APPROVED")
	setTargets(t, 1, 10, "h1")
	savePolicy(t, func(p *Policy) { p.ReplenishWhenUnselected = true })
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"T1"}`))
	if err := s.replenishOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(mw.Requests("POST", pathApply)); n != 1 {
		t.Fatalf("应开卡: %d", n)
	}
	if log.Count() != 0 {
		t.Fatalf("不应告警: %d", log.Count())
	}
	// 字段能存能取
	p, err := LoadPolicy()
	if err != nil || !p.ReplenishWhenUnselected {
		t.Fatal(p.ReplenishWhenUnselected, err)
	}
}

// 判定查询出错时不开卡（花钱的事不确定就不做），错误往上报。
func TestReplenishGateErrorFailsClosed(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	boom := errors.New("查询失败")
	s.opts.MarsWallexSelected = func(context.Context) (bool, error) { return false, boom }
	insertHolder(t, "h1", "APPROVED")
	setTargets(t, 1, 10, "h1")
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"T1"}`))
	if err := s.replenishOnce(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("应报错: %v", err)
	}
	if n := len(mw.Requests("POST", pathApply)); n != 0 {
		t.Fatalf("不应开卡: %d", n)
	}
}

// 闸只挡补卡：对账循环不看它。
func TestReplenishGateDoesNotBlockReconcile(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	s.opts.MarsWallexSelected = func(context.Context) (bool, error) {
		t.Error("对账不该判定是否选用")
		return false, nil
	}
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	o := insertOrder(t, Order{Kind: KindRecharge, CardID: "c1", ProductID: "p1", State: StateUnknown, AmountCents: 100, Currency: "USD"})
	mw.On("GET", pathTransferQ, mwtest.OK(`{"out_order_no":"`+o.OutOrderNo+`","transaction_id":"T1","card_id":"c1","status":"SUCCESS"}`))
	if err := s.reconcileOrdersOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.DB.QueryRow(`SELECT state FROM mw_card_orders WHERE out_order_no = ?`, o.OutOrderNo).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != StateSuccess {
		t.Fatalf("对账应照常收敛: %s", state)
	}
}

func TestSelectableCardsMatchesAcquire(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	ctx := context.Background()
	count := func(want int) {
		t.Helper()
		n, err := s.SelectableCards(ctx, "plus", "usd")
		if err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Fatalf("可选卡 %d，期望 %d", n, want)
		}
	}
	savePolicy(t, func(p *Policy) { p.PlanProducts = map[string][]string{"plus": {"p1"}, "default": {"p2"}} })
	count(0)
	insertCard(t, Card{CardID: "ok", ProductID: "p1"})
	count(1)
	// 下面这些都选不到
	insertCard(t, Card{CardID: "php", ProductID: "p1", Currency: "PHP"})
	insertCard(t, Card{CardID: "other", ProductID: "p9"})
	insertCard(t, Card{CardID: "default-product", ProductID: "p2"})
	insertCard(t, Card{CardID: "expired", ProductID: "p1", Expiry: "1026"})
	insertCard(t, Card{CardID: "inactive", ProductID: "p1", PlatformStatus: "FROZEN"})
	insertCard(t, Card{CardID: "leased", ProductID: "p1", PoolState: PoolLeased})
	count(1)
	// 和 Acquire 对一遍：真能选到的就是那一张
	l, err := s.Acquire(ctx, AcquireRequest{PlanKey: "plus", OrderRef: "o1", Currency: "USD"})
	if err != nil || l.CardID != "ok" {
		t.Fatalf("%+v %v", l, err)
	}
	count(0)
	if _, err := s.Acquire(ctx, AcquireRequest{PlanKey: "plus", OrderRef: "o2", Currency: "USD"}); !errors.Is(err, ErrNoCard) {
		t.Fatalf("应无卡: %v", err)
	}

	// 产品停用后不算
	insertCard(t, Card{CardID: "ok2", ProductID: "p1"})
	count(1)
	if _, err := db.DB.Exec(`INSERT INTO mw_product_health (product_id, plan_key, suspended_at) VALUES ('p1', 'plus', ?)`, clock.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	count(0)

	// 没配产品时取 default
	n, err := s.SelectableCards(ctx, "go", "USD")
	if err != nil || n != 1 {
		t.Fatalf("default 产品: %d %v", n, err)
	}
}

func TestCheckMainWalletAndDailyBudget(t *testing.T) {
	s, mw, _, _ := newTestService(t)
	ctx := context.Background()
	if err := s.CheckMainWallet(ctx, "USD", 100); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("没有主钱包: %v", err)
	}
	mw.On("GET", pathAccounts, mwtest.Reply{Body: `{"count":"1","data":[{"account_no":"A1","account_type":"MAIN","account_status":"ACTIVE","balance_list":[{"currency":"USD","available_balance":"10.00"}]}]}`})
	if err := s.SyncAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckMainWallet(ctx, "usd", 1000); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckMainWallet(ctx, "USD", 1001); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("余额不足: %v", err)
	}
	if err := s.CheckMainWallet(ctx, "PHP", 1); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("没有该币种: %v", err)
	}

	savePolicy(t, func(p *Policy) { p.DailyFundCapCents = 5000 })
	left, err := s.RemainingDailyFund(ctx)
	if err != nil || left != 5000 {
		t.Fatal(left, err)
	}
	insertOrder(t, Order{Kind: KindRecharge, State: StateSuccess, AmountCents: 1200})
	insertOrder(t, Order{Kind: KindApply, State: StateUnknown, AmountCents: 300}) // 结果不明也算，钱可能已经转了
	insertOrder(t, Order{Kind: KindRecharge, State: StateFailed, AmountCents: 999})
	insertOrder(t, Order{Kind: KindRecharge, State: StateNotSent, AmountCents: 999})
	insertOrder(t, Order{Kind: KindWithdraw, State: StateSuccess, AmountCents: 999})
	left, err = s.RemainingDailyFund(ctx)
	if err != nil || left != 3500 {
		t.Fatal(left, err)
	}
	insertOrder(t, Order{Kind: KindRecharge, State: StateSuccess, AmountCents: 9000})
	left, err = s.RemainingDailyFund(ctx)
	if err != nil || left != 0 {
		t.Fatal(left, err)
	}
}
