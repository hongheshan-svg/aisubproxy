package cardpool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

const (
	pathHolder   = "/v1/cardholder/h1"
	pathProducts = "/v1/card/product"
	pathAccounts = "/v1/accounts/page"
)

func insertHolder(t *testing.T, id, status string) {
	t.Helper()
	_, err := db.DB.Exec(`INSERT INTO mw_cardholders (card_holder_id, status, created_at) VALUES (?, ?, ?)`, id, status, testNow().Unix())
	if err != nil {
		t.Fatal(err)
	}
}

const productsJSON = `[{"product_id":"p1","card_label":"VISA","card_country":"US","card_mode":"PREPAID","card_currency":"USD"}]`

func cardJSON(cur, avail, frozen, status string) string {
	return `{"card_id":"c1","card_no":"4111********1234","card_label":"visa","card_country":"us","card_mode":"prepaid","card_currency":"` + cur +
		`","expiry_date":"1228","status":"` + status + `","available_balance":"` + avail + `","freeze_balance":"` + frozen + `"}`
}

func TestImportCardholder(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	mw.On("GET", pathHolder,
		mwtest.OK(`{"card_holder_id":"h1","first_name":"A","last_name":"B","country_code":"US","status":"APPROVED"}`),
		mwtest.OK(`{"card_holder_id":"h1","first_name":"A","last_name":"B","country_code":"US","status":"REJECTED","remark":"资料不全"}`))
	h, err := s.ImportCardholder(ctx, "h1", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != "APPROVED" || h.FirstName != "A" || h.LastName != "B" || h.CountryCode != "US" || h.SyncedAt == nil {
		t.Fatalf("%+v", h)
	}
	h, err = s.ImportCardholder(ctx, "h1", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != "REJECTED" || h.Remark != "资料不全" {
		t.Fatalf("%+v", h)
	}
	list, err := s.ListCardholders(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %+v", err, list)
	}
}

func TestImportCardValidatesProduct(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	mw.On("GET", "/v1/card/detail/c1", mwtest.OK(cardJSON("EUR", "0.00", "0.00", "ACTIVE")), mwtest.OK(cardJSON("usd", "0.00", "0.00", "ACTIVE")))
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	if _, err := s.ImportCard(ctx, "c1", "p1", "", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("want ErrBadRequest, got %v", err)
	}
	if _, err := getCard(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不应写入: %v", err)
	}
	c, err := s.ImportCard(ctx, "c1", "p1", "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if c.PoolState != PoolReleasing || c.Source != "imported" || c.Last4 != "1234" || c.NextActionAt == nil || c.Expiry != "1228" {
		t.Fatalf("%+v", c)
	}
}

func TestImportCardRejectsDuplicateAndCanceled(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	insertCard(t, Card{CardID: "dup", ProductID: "p1"})
	if _, err := s.ImportCard(ctx, "dup", "p1", "", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("重复导入: %v", err)
	}
	mw.On("GET", "/v1/card/detail/c1", mwtest.OK(cardJSON("USD", "0.00", "0.00", "CANCELED")))
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	if _, err := s.ImportCard(ctx, "c1", "p1", "", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("已注销: %v", err)
	}
	if _, err := s.ImportCard(ctx, "c1", "p1", "nohold", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("持卡人未导入: %v", err)
	}
}

func TestApplyCardsValidation(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	insertHolder(t, "h2", "PENDING")
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	for name, f := range map[string]func() ([]Order, error){
		"0张":   func() ([]Order, error) { return s.ApplyCards(ctx, "p1", "h1", 0, "a") },
		"21张":  func() ([]Order, error) { return s.ApplyCards(ctx, "p1", "h1", 21, "a") },
		"未审核":  func() ([]Order, error) { return s.ApplyCards(ctx, "p1", "h2", 1, "a") },
		"未导入":  func() ([]Order, error) { return s.ApplyCards(ctx, "p1", "zz", 1, "a") },
		"无此产品": func() ([]Order, error) { return s.ApplyCards(ctx, "nope", "h1", 1, "a") },
	} {
		if _, err := f(); !errors.Is(err, ErrBadRequest) {
			t.Fatalf("%s: want ErrBadRequest, got %v", name, err)
		}
	}
	if n := len(mw.Requests("POST", pathApply)); n != 0 {
		t.Fatalf("不应发开卡请求, got %d", n)
	}
}

func TestApplyCardsCreatesOrders(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	savePolicy(t, func(p *Policy) { p.OpenAmountCents = 500 })
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.OK(`{"transaction_id":"T1"}`))
	orders, err := s.ApplyCards(ctx, "p1", "h1", 3, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 3 || len(mw.Requests("POST", pathApply)) != 3 {
		t.Fatalf("orders=%d", len(orders))
	}
	names := map[string]bool{}
	for _, r := range mw.Requests("POST", pathApply) {
		names[string(r.Body)] = true
	}
	if len(names) != 3 {
		t.Fatalf("请求体应互不相同（含卡名与单号）: %d", len(names))
	}
	for _, o := range orders {
		if o.Kind != KindApply || o.Currency != "USD" || o.AmountCents != 500 || o.State != StateProcessing {
			t.Fatalf("%+v", o)
		}
	}
}

func TestApplyCardsStopsOnFailure(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	mw.On("GET", pathProducts, mwtest.OK(productsJSON))
	mw.On("POST", pathApply, mwtest.Rejected(400, "PARAM"))
	orders, err := s.ApplyCards(ctx, "p1", "h1", 3, "admin")
	if err != nil || len(orders) != 1 || orders[0].State != StateFailed {
		t.Fatalf("%v %+v", err, orders)
	}
}

func TestDisableEnableRules(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	insertCard(t, Card{CardID: "L", ProductID: "p1", PoolState: PoolLeased})
	insertCard(t, Card{CardID: "A", ProductID: "p1"})
	insertCard(t, Card{CardID: "U", ProductID: "p1", PoolState: PoolUsed})
	if err := s.DisableCard(ctx, "L", "", "admin"); !errors.Is(err, ErrCardBusy) {
		t.Fatalf("leased: %v", err)
	}
	if err := s.DisableCard(ctx, "U", "", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("used: %v", err)
	}
	if err := s.DisableCard(ctx, "A", "", "admin"); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "A"); c.PoolState != PoolDisabled || c.DisabledReason != "管理员停用" {
		t.Fatalf("%+v", c)
	}
	// 启用：余额 1.00 → 拒绝
	mw.On("GET", "/v1/card/detail/A",
		mwtest.OK(`{"card_id":"A","card_no":"4111111111111111","status":"ACTIVE","available_balance":"1.00","freeze_balance":"0.00"}`),
		mwtest.OK(`{"card_id":"A","card_no":"4111111111111111","status":"ACTIVE","available_balance":"0.00","freeze_balance":"0.00"}`))
	err := s.EnableCard(ctx, "A", "admin")
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("有余额: %v", err)
	}
	if c := mustGetCard(t, "A"); c.PoolState != PoolDisabled || c.AvailableCents != 100 {
		t.Fatalf("%+v", c)
	}
	if err := s.EnableCard(ctx, "A", "admin"); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "A"); c.PoolState != PoolAvailable || c.DisabledReason != "" {
		t.Fatalf("%+v", c)
	}
	if err := s.EnableCard(ctx, "A", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("非 disabled: %v", err)
	}
}

func TestSweepCard(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("GET", "/v1/card/detail/c1",
		mwtest.OK(cardJSON("USD", "5.00", "0.00", "ACTIVE")),
		mwtest.OK(cardJSON("USD", "0.00", "0.00", "ACTIVE")))
	mw.On("POST", pathWithdraw, mwtest.OK(`{"status":"APPLY"}`))
	o, err := s.SweepCard(ctx, "c1", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != KindWithdraw || o.AmountCents != 500 || o.Currency != "USD" || o.CardID != "c1" {
		t.Fatalf("%+v", o)
	}
	// 在途单已结束后，余额 0 → 拒绝
	if _, err := db.DB.Exec(`UPDATE mw_card_orders SET state = 'success'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SweepCard(ctx, "c1", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("余额 0: %v", err)
	}
	if n := len(mw.Requests("POST", pathWithdraw)); n != 1 {
		t.Fatalf("withdraw 请求 %d", n)
	}
	insertCard(t, Card{CardID: "r", ProductID: "p1", PoolState: PoolRetired})
	insertCard(t, Card{CardID: "l", ProductID: "p1", PoolState: PoolLeased})
	if _, err := s.SweepCard(ctx, "r", "a"); !errors.Is(err, ErrBadRequest) {
		t.Fatal(err)
	}
	if _, err := s.SweepCard(ctx, "l", "a"); !errors.Is(err, ErrCardBusy) {
		t.Fatal(err)
	}
}

func TestCancelCardUsesStatusOrder(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	mw.On("PUT", pathStatus, mwtest.OK(`{"card_id":"c1","status":"PROCESSING"}`))
	o, err := s.CancelCard(ctx, "c1", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != KindStatus || o.TargetStatus != "CANCELED" {
		t.Fatalf("%+v", o)
	}
	if o.State == StateSuccess {
		// 同步成功的单据已经在 submit 里完成；否则由 finalize 完成
	} else if _, err := s.finalize(ctx, o.OutOrderNo, queryResult{State: StateSuccess}, "test"); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolRetired {
		t.Fatalf("%+v", c)
	}
	if _, err := s.FreezeCard(ctx, "c1", "a"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("退役卡: %v", err)
	}
}

func TestListsAndDetail(t *testing.T) {
	ctx := context.Background()
	s, _, clock, _ := newTestService(t)
	for i, id := range []string{"a", "b", "c"} {
		insertCard(t, Card{CardID: id, ProductID: "p1", CreatedAt: clock.Now().Unix() + int64(i)})
	}
	insertCard(t, Card{CardID: "d", ProductID: "p2", PoolState: PoolUsed})
	cards, total, err := s.ListCards(ctx, CardFilter{ProductID: "p1", PageSize: 2, Page: 0})
	if err != nil || total != 3 || len(cards) != 2 || cards[0].CardID != "c" {
		t.Fatalf("%v %d %+v", err, total, cards)
	}
	cards, total, _ = s.ListCards(ctx, CardFilter{State: PoolUsed})
	if total != 1 || len(cards) != 1 {
		t.Fatalf("%d", total)
	}
	insertOrder(t, Order{Kind: KindWithdraw, CardID: "a", State: StateSuccess})
	insertOrder(t, Order{Kind: KindStatus, CardID: "b"})
	_, err = db.DB.Exec(`INSERT INTO mw_card_transactions (transaction_id, kind, card_id, updated_at, occurred_at) VALUES ('t1','auth','a',1,5)`)
	if err != nil {
		t.Fatal(err)
	}
	if os, n, _ := s.ListOrders(ctx, OrderFilter{CardID: "a"}); n != 1 || len(os) != 1 {
		t.Fatalf("%d", n)
	}
	if ts, n, _ := s.ListTransactions(ctx, TxnFilter{CardID: "a"}); n != 1 || len(ts) != 1 {
		t.Fatalf("%d", n)
	}
	v, err := s.CardDetail(ctx, "a")
	if err != nil || v.Card.CardID != "a" || len(v.Orders) != 1 || len(v.Transactions) != 1 {
		t.Fatalf("%v %+v", err, v)
	}
	if _, err := s.CardDetail(ctx, "zz"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestOverviewCounts(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	// 测试时钟 04:00 UTC = 12:00 北京
	clock.Set(time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC))
	dayStart := beijingDayStart(clock.Now()).Unix()
	savePolicy(t, func(p *Policy) {
		p.ProductTargets = map[string]ProductTarget{"p1": {Target: 5, DailyLimit: 10}, "p0": {Target: 1}}
	})
	insertCard(t, Card{CardID: "a1", ProductID: "p1"})
	insertCard(t, Card{CardID: "a2", ProductID: "p1"})
	insertCard(t, Card{CardID: "l1", ProductID: "p1", PoolState: PoolLeased})
	insertCard(t, Card{CardID: "d1", ProductID: "p2", PoolState: PoolDisabled})
	insertOrder(t, Order{Kind: KindApply, ProductID: "p1", State: StateSuccess, CreatedAt: dayStart})
	insertOrder(t, Order{Kind: KindApply, ProductID: "p1", State: StateFailed, CreatedAt: dayStart + 100})
	insertOrder(t, Order{Kind: KindApply, ProductID: "p1", State: StateSuccess, CreatedAt: dayStart - 1}) // 昨天
	insertOrder(t, Order{Kind: KindWithdraw, CardID: "a1", State: StateNeedsReview})
	for i := 0; i < 2; i++ {
		_, err := db.DB.Exec(`INSERT INTO marswallex_webhook_events (message_id, payload, process_attempts) VALUES (?, '{}', ?)`, "m"+string(rune('a'+i)), 10+i*-5)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := db.DB.Exec(`INSERT INTO mw_product_health (product_id, plan_key, suspended_at, reason) VALUES ('p1','plus',1,'x'), ('p1','pro',NULL,'')`)
	if err != nil {
		t.Fatal(err)
	}

	mw.On("GET", pathAccounts, mwtest.Reply{Body: `{"count":"1","data":[{"account_no":"X","balance_list":[{"currency":"USD","available_balance":"9.00","freeze_balance":"0.00"}]}]}`})
	ov, err := s.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.BalanceError != "" || len(ov.MainBalances) != 1 || ov.MainBalances[0].AvailableBalance != "9.00" {
		t.Fatalf("%+v", ov)
	}
	if len(ov.Products) != 3 || ov.Products[0].ProductID != "p0" || ov.Products[1].ProductID != "p1" || ov.Products[2].ProductID != "p2" {
		t.Fatalf("%+v", ov.Products)
	}
	p1 := ov.Products[1]
	if p1.Available != 2 || p1.Leased != 1 || p1.Target != 5 || p1.DailyLimit != 10 || p1.OpenedToday != 2 {
		t.Fatalf("%+v", p1)
	}
	if ov.Products[2].Disabled != 1 || ov.NeedsReview != 1 || ov.FailedEvents != 1 || ov.SuspendedProducts != 1 {
		t.Fatalf("%+v", ov)
	}

	// 账户接口出错
	s2, mw2, _, _ := newTestService(t)
	mw2.On("GET", pathAccounts, mwtest.Rejected(400, "X"))
	ov, err = s2.Overview(ctx)
	if err != nil || ov.BalanceError == "" {
		t.Fatalf("%v %+v", err, ov)
	}
}
