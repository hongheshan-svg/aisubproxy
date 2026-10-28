package cardpool

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

func sensitivePath(id string) string { return "/v1/card/sensitive/" + id }

func sensitiveJSON(id string) string {
	return `{"card_id":"` + id + `","card_no":"4111111111111111","cvv2":"123","expiry_date":"1028"}`
}

// setPlans 设置套餐 → 卡产品优先级。
func setPlans(t *testing.T, plans map[string][]string) {
	t.Helper()
	savePolicy(t, func(p *Policy) {
		for k, v := range plans {
			p.PlanProducts[k] = v
		}
	})
}

func suspendProduct(t *testing.T, productID, planKey string) {
	t.Helper()
	if _, err := db.DB.Exec(`INSERT INTO mw_product_health (product_id, plan_key, suspended_at, reason) VALUES (?, ?, ?, 'test')`,
		productID, planKey, testNow().Unix()); err != nil {
		t.Fatal(err)
	}
}

func mustAcquire(t *testing.T, s *Service, req AcquireRequest) *Lease {
	t.Helper()
	l, err := s.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("Acquire(%+v): %v", req, err)
	}
	return l
}

func countOutcomes(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM mw_card_outcomes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func setPoolState(t *testing.T, cardID, state string) {
	t.Helper()
	if _, err := db.DB.Exec(`UPDATE mw_cards SET pool_state = ? WHERE card_id = ?`, state, cardID); err != nil {
		t.Fatal(err)
	}
}

func TestAcquirePriorityAndOrder(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1", "p2"}})
	now := clock.Now().Unix()
	insertCard(t, Card{CardID: "p2-old", ProductID: "p2", CreatedAt: now - 5000})
	insertCard(t, Card{CardID: "p1-u0", ProductID: "p1", CreatedAt: now - 3000})
	insertCard(t, Card{CardID: "p1-u1-new", ProductID: "p1", UseCount: 1, CreatedAt: now - 100})
	insertCard(t, Card{CardID: "p1-u1-old", ProductID: "p1", UseCount: 1, CreatedAt: now - 200, Currency: "EUR"})

	req := AcquireRequest{PlanKey: "gpt", OrderRef: "ord-1", AccountEmail: "a@x.com"}
	l := mustAcquire(t, s, req)
	if l.CardID != "p1-u1-old" || l.ProductID != "p1" || l.Currency != "EUR" || l.PlanKey != "gpt" ||
		l.OrderRef != "ord-1" || l.AccountEmail != "a@x.com" {
		t.Fatalf("第一张应为 p1 中 use_count 大且较早的卡: %+v", l)
	}
	if b, err := hex.DecodeString(l.Token); err != nil || len(b) != 16 {
		t.Fatalf("令牌应为 16 字节十六进制: %q", l.Token)
	}
	if !l.ExpiresAt.Equal(clock.Now().Add(30 * time.Minute)) {
		t.Fatalf("到期时间 %v", l.ExpiresAt)
	}
	c := mustGetCard(t, "p1-u1-old")
	if c.PoolState != PoolLeased || c.LeaseToken != l.Token || c.LeaseOwner != "ord-1" ||
		i64(c.LeaseExpiresAt) != now+1800 || c.LeaseSensitive {
		t.Fatalf("卡应已租出: %+v", c)
	}

	var got []string
	tokens := map[string]bool{l.Token: true}
	for i := 0; i < 3; i++ {
		l := mustAcquire(t, s, req)
		got = append(got, l.CardID)
		if tokens[l.Token] {
			t.Fatalf("令牌重复: %s", l.Token)
		}
		tokens[l.Token] = true
	}
	want := []string{"p1-u1-new", "p1-u0", "p2-old"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("选卡顺序 %v, want %v", got, want)
		}
	}
	if _, err := s.Acquire(context.Background(), req); !errors.Is(err, ErrNoCard) {
		t.Fatalf("卡用完后应为 ErrNoCard: %v", err)
	}
}

func TestAcquireSkipsSuspendedExcludedExpiringAndBadHolder(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1", "p2"}})
	suspendProduct(t, "p1", "gpt")
	insertHolder(t, "h-dis", "DISABLED")
	insertHolder(t, "h-sus", "SUSPENDED")
	insertHolder(t, "h-ok", "APPROVED")
	now := clock.Now().Unix()
	// 不合格的卡入池更早，若过滤失效会先被选中
	insertCard(t, Card{CardID: "p1-good", ProductID: "p1", CreatedAt: now - 9000})
	insertCard(t, Card{CardID: "excluded", ProductID: "p2", CreatedAt: now - 8000})
	insertCard(t, Card{CardID: "expiring", ProductID: "p2", Expiry: "1026", CreatedAt: now - 7000})
	insertCard(t, Card{CardID: "malformed", ProductID: "p2", Expiry: "13/26", CreatedAt: now - 6000})
	insertCard(t, Card{CardID: "holder-disabled", ProductID: "p2", CardHolderID: "h-dis", CreatedAt: now - 5000})
	insertCard(t, Card{CardID: "holder-suspended", ProductID: "p2", CardHolderID: "h-sus", CreatedAt: now - 4000})
	insertCard(t, Card{CardID: "revoked", ProductID: "p2", PlatformStatus: "REVOKED", CreatedAt: now - 3500})
	insertCard(t, Card{CardID: "releasing", ProductID: "p2", PoolState: PoolReleasing, CreatedAt: now - 3200})
	insertCard(t, Card{CardID: "no-holder", ProductID: "p2", CreatedAt: now - 3000})
	insertCard(t, Card{CardID: "holder-ok", ProductID: "p2", CardHolderID: "h-ok", CreatedAt: now - 2000})

	req := AcquireRequest{PlanKey: "gpt", OrderRef: "o", ExcludeCardIDs: []string{"excluded"}}
	if l := mustAcquire(t, s, req); l.CardID != "no-holder" {
		t.Fatalf("应选持卡人为空的卡: %s", l.CardID)
	}
	if l := mustAcquire(t, s, req); l.CardID != "holder-ok" {
		t.Fatalf("应选持卡人正常的卡: %s", l.CardID)
	}
	_, err := s.Acquire(context.Background(), req)
	if !errors.Is(err, ErrNoCard) || !strings.Contains(err.Error(), "无可用卡") {
		t.Fatalf("其余卡都不合格: %v", err)
	}
	for _, id := range []string{"p1-good", "excluded", "expiring", "malformed", "holder-disabled", "holder-suspended", "revoked"} {
		if c := mustGetCard(t, id); c.PoolState != PoolAvailable {
			t.Fatalf("%s 不应被租出: %+v", id, c)
		}
	}
	// 同一张卡在其他套餐下（p1 未停用）可以选中
	setPlans(t, map[string][]string{"claude": {"p1"}})
	if l := mustAcquire(t, s, AcquireRequest{PlanKey: "claude"}); l.CardID != "p1-good" {
		t.Fatalf("p1×claude 未停用: %s", l.CardID)
	}
}

// 排在前面的 21 张卡有效期都不合格（超过一批 20 张），后面那张合格的卡仍要能选到。
func TestAcquireKeepsFetchingPastIneligibleBatch(t *testing.T) {
	s, _, clock, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	now := clock.Now().Unix()
	for i := 0; i < 21; i++ {
		expiry := "1026" // 不足一个月
		if i%2 == 1 {
			expiry = "13/26" // 格式不对
		}
		// bad-19、bad-20 与 good 的 use_count、created_at 相同：第一批正好止于 bad-19，下一批只能靠 card_id 接上
		insertCard(t, Card{CardID: fmt.Sprintf("bad-%02d", i), ProductID: "p1", Expiry: expiry, UseCount: 1, CreatedAt: now - 10000 + int64(min(i, 19))})
	}
	insertCard(t, Card{CardID: "good", ProductID: "p1", UseCount: 1, CreatedAt: now - 10000 + 19})

	// 下一批分别只能经由 created_at 更晚、use_count 更小接上
	insertCard(t, Card{CardID: "later", ProductID: "p1", UseCount: 1, CreatedAt: now - 10000 + 100})
	insertCard(t, Card{CardID: "lower", ProductID: "p1", UseCount: 0, CreatedAt: now - 20000})

	for _, want := range []string{"good", "later", "lower"} {
		if l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt"}); l.CardID != want {
			t.Fatalf("应越过整批不合格的卡选到 %s: %s", want, l.CardID)
		}
	}
	if _, err := s.Acquire(context.Background(), AcquireRequest{PlanKey: "gpt"}); !errors.Is(err, ErrNoCard) {
		t.Fatalf("其余都不合格: %v", err)
	}
}

func TestAcquireUsesDefaultPlan(t *testing.T) {
	s, _, _, _ := newTestService(t)
	setPlans(t, map[string][]string{"default": {"p9"}, "gpt": {"p1"}})
	insertCard(t, Card{CardID: "c-p1", ProductID: "p1"})
	insertCard(t, Card{CardID: "c-p9", ProductID: "p9"})
	insertCard(t, Card{CardID: "c-p5", ProductID: "p5"})

	l := mustAcquire(t, s, AcquireRequest{PlanKey: "claude", OrderRef: "o"})
	if l.CardID != "c-p9" || l.PlanKey != "claude" {
		t.Fatalf("未配置的套餐应使用默认列表: %+v", l)
	}
	if l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt"}); l.CardID != "c-p1" {
		t.Fatalf("已配置的套餐用自己的列表: %+v", l)
	}
	// gpt 已配置：自己的产品没卡时不回落到默认列表
	insertCard(t, Card{CardID: "c-p9b", ProductID: "p9"})
	if _, err := s.Acquire(context.Background(), AcquireRequest{PlanKey: "gpt"}); !errors.Is(err, ErrNoCard) {
		t.Fatalf("gpt 不应回落到默认列表: %v", err)
	}
	if c := mustGetCard(t, "c-p5"); c.PoolState != PoolAvailable {
		t.Fatalf("不在任何列表里的产品不应被选: %+v", c)
	}
}

func TestAcquireNoCardReasons(t *testing.T) {
	ctx := context.Background()
	s, _, clock, al := newTestService(t)

	drainKick := func() bool {
		select {
		case <-s.kickReplenishCh:
			return true
		default:
			return false
		}
	}

	// 未配置任何产品
	_, err := s.Acquire(ctx, AcquireRequest{PlanKey: "gpt"})
	if !errors.Is(err, ErrNoCard) || !strings.Contains(err.Error(), "无可用卡") {
		t.Fatalf("未配置: %v", err)
	}
	if !drainKick() {
		t.Fatal("无卡时应触发补卡")
	}
	if al.Count() != 1 || !strings.Contains(al.Texts()[0], "gpt") {
		t.Fatalf("应告警 1 次: %v", al.Texts())
	}

	// 有产品但没有卡
	setPlans(t, map[string][]string{"gpt": {"p1", "p2"}})
	_, err = s.Acquire(ctx, AcquireRequest{PlanKey: "gpt"})
	if !errors.Is(err, ErrNoCard) || !strings.Contains(err.Error(), "无可用卡") {
		t.Fatalf("没有卡: %v", err)
	}
	if !drainKick() {
		t.Fatal("无卡时应触发补卡")
	}

	// 全部产品停用（产品下有卡也不选）
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	suspendProduct(t, "p1", "gpt")
	suspendProduct(t, "p2", "gpt")
	_, err = s.Acquire(ctx, AcquireRequest{PlanKey: "gpt"})
	if !errors.Is(err, ErrNoCard) || !strings.Contains(err.Error(), "全部产品已停用") {
		t.Fatalf("全部停用: %v", err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolAvailable {
		t.Fatalf("停用产品的卡不应被租出: %+v", c)
	}
	if al.Count() != 1 {
		t.Fatalf("同一天只告警 1 次: %v", al.Texts())
	}
	// 连续触发不会阻塞（通道容量 1）
	for i := 0; i < 3; i++ {
		if _, err := s.Acquire(ctx, AcquireRequest{PlanKey: "gpt"}); !errors.Is(err, ErrNoCard) {
			t.Fatal(err)
		}
	}
	if !drainKick() || drainKick() {
		t.Fatal("补卡信号应合并为一个")
	}
	// 次日再告警
	clock.Advance(24 * time.Hour)
	if _, err := s.Acquire(ctx, AcquireRequest{PlanKey: "gpt"}); !errors.Is(err, ErrNoCard) {
		t.Fatal(err)
	}
	if al.Count() != 2 {
		t.Fatalf("次日应再告警: %v", al.Texts())
	}
}

func TestConcurrentAcquireNeverDoubleLeases(t *testing.T) {
	s, _, _, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5"} {
		insertCard(t, Card{CardID: id, ProductID: "p1"})
	}
	const n = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	leases := make([]*Lease, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			leases[i], errs[i] = s.Acquire(context.Background(), AcquireRequest{PlanKey: "gpt"})
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[string]string{}
	ok, noCard := 0, 0
	for i := 0; i < n; i++ {
		switch {
		case errs[i] == nil:
			ok++
			if prev, dup := seen[leases[i].CardID]; dup {
				t.Fatalf("卡 %s 被租出两次（令牌 %s、%s）", leases[i].CardID, prev, leases[i].Token)
			}
			seen[leases[i].CardID] = leases[i].Token
		case errors.Is(errs[i], ErrNoCard):
			noCard++
		default:
			t.Fatalf("意外错误: %v", errs[i])
		}
	}
	if ok != 5 || noCard != 15 {
		t.Fatalf("成功 %d、无卡 %d", ok, noCard)
	}
	for id, token := range seen {
		if c := mustGetCard(t, id); c.PoolState != PoolLeased || c.LeaseToken != token {
			t.Fatalf("%s: %+v", id, c)
		}
	}
}

func TestFundRequiresValidLease(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt", OrderRef: "o1"})

	bad := *l
	bad.Token = strings.Repeat("0", 32)
	if _, err := s.Fund(ctx, &bad, 100); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("错误令牌: %v", err)
	}
	if _, err := s.Fund(ctx, nil, 100); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("nil 租约: %v", err)
	}
	clock.Advance(30 * time.Minute) // lease_expires_at == now → 已过期
	if _, err := s.Fund(ctx, l, 100); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("过期租约: %v", err)
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 0 || countOrders(t) != 0 {
		t.Fatalf("租约无效时不应发请求或建单: requests=%d orders=%d", n, countOrders(t))
	}

	insertCard(t, Card{CardID: "c2", ProductID: "p1", Currency: "EUR"})
	l2 := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt", OrderRef: "o2"})
	mw.On("POST", pathRecharge, mwtest.OK(`{"status":"APPLY","transaction_id":"t1"}`))
	o, err := s.Fund(ctx, l2, 150)
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != KindRecharge || o.CardID != "c2" || o.AmountCents != 150 || o.Currency != "EUR" ||
		o.RequestedBy != "lease:o2" || o.State != StateProcessing {
		t.Fatalf("%+v", o)
	}
	// 租约令牌是持有凭证，不得出现在后台能看到的单据里（单据列表、卡详情）
	list, _, err := s.ListOrders(ctx, OrderFilter{})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.CardDetail(ctx, "c2")
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{"单据列表": list, "卡详情": detail} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), l2.Token) {
			t.Fatalf("%s 泄露了租约令牌: %s", name, b)
		}
	}
}

func TestSensitiveMarksBeforeCall(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt"})

	mw.On("GET", sensitivePath("c1"), mwtest.Unknown(), mwtest.OK(sensitiveJSON("c1")))
	if _, err := s.Sensitive(ctx, l); err == nil {
		t.Fatal("接口结果未知时应返回错误")
	}
	c := mustGetCard(t, "c1")
	if !c.LeaseSensitive || i64(c.LastSensitiveAt) != clock.Now().Unix() {
		t.Fatalf("调用前应已记为取过卡号: %+v", c)
	}

	clock.Advance(time.Minute)
	sv, err := s.Sensitive(ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	if sv.CardNo != "4111111111111111" || sv.CVV2 != "123" || sv.ExpiryDate != "1028" {
		t.Fatalf("%+v", sv)
	}
	if c := mustGetCard(t, "c1"); i64(c.LastSensitiveAt) != clock.Now().Unix() {
		t.Fatalf("last_sensitive_at 应更新: %+v", c)
	}

	// 过期租约：不标记、不发请求
	insertCard(t, Card{CardID: "c2", ProductID: "p1"})
	l2 := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt"})
	clock.Advance(31 * time.Minute)
	if _, err := s.Sensitive(ctx, l2); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("过期租约: %v", err)
	}
	if c := mustGetCard(t, "c2"); c.LeaseSensitive || c.LastSensitiveAt != nil {
		t.Fatalf("过期租约不应标记: %+v", c)
	}
	if n := len(mw.Requests("GET", sensitivePath("c2"))); n != 0 {
		t.Fatalf("过期租约不应发请求: %d", n)
	}
}

func TestReleaseTransitions(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	abort := Outcome{Result: ResultAborted}

	// 上限 1：取过卡号 → used
	insertCard(t, Card{CardID: "a", ProductID: "p1"})
	l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt"})
	mw.On("GET", sensitivePath("a"), mwtest.OK(sensitiveJSON("a")))
	if _, err := s.Sensitive(ctx, l); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if err := s.Release(ctx, l, abort); err != nil {
		t.Fatal(err)
	}
	c := mustGetCard(t, "a")
	if c.PoolState != PoolUsed || c.UseCount != 1 || c.LeaseToken != "" || c.LeaseOwner != "" ||
		c.LeaseExpiresAt != nil || c.LeaseSensitive || i64(c.NextActionAt) != clock.Now().Unix() {
		t.Fatalf("上限 1、取过卡号: %+v", c)
	}

	// 上限 1：没取过卡号 → releasing
	insertCard(t, Card{CardID: "b", ProductID: "p1"})
	l = mustAcquire(t, s, AcquireRequest{PlanKey: "gpt"})
	if err := s.Release(ctx, l, abort); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "b"); c.PoolState != PoolReleasing || c.UseCount != 0 || i64(c.NextActionAt) != clock.Now().Unix() {
		t.Fatalf("上限 1、没取过卡号: %+v", c)
	}

	// 上限 3：第 1、2 次 → releasing，第 3 次 → used
	savePolicy(t, func(p *Policy) { p.MaxUsesPerCard = 3 })
	insertCard(t, Card{CardID: "c", ProductID: "p1"})
	mw.On("GET", sensitivePath("c"), mwtest.OK(sensitiveJSON("c")))
	for i := 1; i <= 3; i++ {
		if i > 1 {
			setPoolState(t, "c", PoolAvailable) // 模拟回收后回到池中
		}
		l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt"})
		if l.CardID != "c" {
			t.Fatalf("第 %d 次选到 %s", i, l.CardID)
		}
		if _, err := s.Sensitive(ctx, l); err != nil {
			t.Fatal(err)
		}
		if err := s.Release(ctx, l, abort); err != nil {
			t.Fatal(err)
		}
		c := mustGetCard(t, "c")
		want := PoolReleasing
		if i == 3 {
			want = PoolUsed
		}
		if c.PoolState != want || c.UseCount != i || c.LeaseSensitive {
			t.Fatalf("第 %d 次释放: %+v", i, c)
		}
	}

	// retire_reason 非空：use_count 为 0 也转 used
	insertCard(t, Card{CardID: "d", ProductID: "p1", RetireReason: "健康度"})
	l = mustAcquire(t, s, AcquireRequest{PlanKey: "gpt"})
	if err := s.Release(ctx, l, abort); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "d"); c.PoolState != PoolUsed || c.UseCount != 0 {
		t.Fatalf("retire_reason 非空: %+v", c)
	}
}

func TestReleaseRecordsOutcome(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt", OrderRef: "o1", AccountEmail: " A@X.com "})

	if err := s.Release(ctx, l, Outcome{Result: "weird"}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("非法结果: %v", err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolLeased {
		t.Fatalf("非法结果不应释放: %+v", c)
	}

	if err := s.Release(ctx, l, Outcome{Result: ResultFailed, ErrorCode: "declined"}); err != nil {
		t.Fatal(err)
	}
	if n := countOutcomes(t); n != 1 {
		t.Fatalf("outcomes = %d", n)
	}
	var cardID, productID, plan, ref, email, result, code string
	if err := db.DB.QueryRow(`SELECT card_id, product_id, plan_key, order_ref, email_norm, result, error_code FROM mw_card_outcomes`).
		Scan(&cardID, &productID, &plan, &ref, &email, &result, &code); err != nil {
		t.Fatal(err)
	}
	if cardID != "c1" || productID != "p1" || plan != "gpt" || ref != "o1" || email != "a@x.com" ||
		result != ResultFailed || code != "declined" {
		t.Fatalf("outcome: %s %s %s %s %s %s %s", cardID, productID, plan, ref, email, result, code)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolReleasing {
		t.Fatalf("%+v", c)
	}

	insertCard(t, Card{CardID: "c2", ProductID: "p1"})
	l = mustAcquire(t, s, AcquireRequest{PlanKey: "gpt", OrderRef: "o2"})
	if err := s.Release(ctx, l, Outcome{Result: ResultAborted}); err != nil {
		t.Fatal(err)
	}
	if n := countOutcomes(t); n != 1 {
		t.Fatalf("aborted 不应记录: %d", n)
	}
}

func TestReleaseWrongToken(t *testing.T) {
	ctx := context.Background()
	s, _, clock, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	l := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt", OrderRef: "o1"})

	bad := *l
	bad.Token = strings.Repeat("f", 32)
	if err := s.Release(ctx, &bad, Outcome{Result: ResultFailed}); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("错误令牌: %v", err)
	}
	if err := s.Release(ctx, nil, Outcome{Result: ResultFailed}); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("nil 租约: %v", err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolLeased || c.LeaseToken != l.Token {
		t.Fatalf("错误令牌不应释放: %+v", c)
	}
	if n := countOutcomes(t); n != 0 {
		t.Fatalf("令牌无效时不应记录结果: %d", n)
	}

	// 已过期但仍在租用中：持有者可以释放
	clock.Advance(40 * time.Minute)
	if err := s.Release(ctx, l, Outcome{Result: ResultSuccess}); err != nil {
		t.Fatalf("持有者释放过期租约: %v", err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolReleasing {
		t.Fatalf("%+v", c)
	}
	// 再释放一次：已不是租用中
	if err := s.Release(ctx, l, Outcome{Result: ResultSuccess}); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("重复释放: %v", err)
	}
	if n := countOutcomes(t); n != 1 {
		t.Fatalf("outcomes = %d", n)
	}
}

func TestExpireLeases(t *testing.T) {
	ctx := context.Background()
	s, _, clock, _ := newTestService(t)
	setPlans(t, map[string][]string{"gpt": {"p1"}})
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	l1 := mustAcquire(t, s, AcquireRequest{PlanKey: "gpt", OrderRef: "o1"})
	clock.Advance(10 * time.Minute)
	insertCard(t, Card{CardID: "c2", ProductID: "p1"})
	mustAcquire(t, s, AcquireRequest{PlanKey: "gpt", OrderRef: "o2"})

	clock.Advance(20*time.Minute - time.Second)
	if err := s.expireLeasesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolLeased {
		t.Fatalf("未到期不应释放: %+v", c)
	}

	clock.Advance(time.Second) // c1 正好到期
	if err := s.expireLeasesOnce(ctx); err != nil {
		t.Fatal(err)
	}
	c1 := mustGetCard(t, "c1")
	if c1.PoolState != PoolReleasing || c1.LeaseToken != "" || c1.UseCount != 0 || i64(c1.NextActionAt) != clock.Now().Unix() {
		t.Fatalf("到期应释放: %+v", c1)
	}
	if c := mustGetCard(t, "c2"); c.PoolState != PoolLeased {
		t.Fatalf("c2 未到期: %+v", c)
	}
	if n := countOutcomes(t); n != 0 {
		t.Fatalf("到期释放不记录结果: %d", n)
	}
	if err := s.Release(ctx, l1, Outcome{Result: ResultSuccess}); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("到期释放后原租约应无效: %v", err)
	}
}

func TestAdminLeaseFlow(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, _ := newTestService(t)
	insertCard(t, Card{CardID: "c1", ProductID: "p1"})
	insertCard(t, Card{CardID: "used", ProductID: "p1", PoolState: PoolUsed})
	insertCard(t, Card{CardID: "rel", ProductID: "p1", PoolState: PoolReleasing})

	if _, err := s.AdminLease(ctx, "rel", "alice"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("releasing 的卡不能租: %v", err)
	}
	if _, err := s.AdminLease(ctx, "nope", "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的卡: %v", err)
	}
	if _, err := s.ViewSensitive(ctx, "c1", "alice"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("available 的卡要先租用: %v", err)
	}
	if n := len(mw.Requests("GET", sensitivePath("c1"))); n != 0 {
		t.Fatalf("不应查询卡号: %d", n)
	}

	l, err := s.AdminLease(ctx, "c1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	c := mustGetCard(t, "c1")
	if c.PoolState != PoolLeased || c.LeaseOwner != "admin:alice" || c.LeaseToken != l.Token ||
		i64(c.LeaseExpiresAt) != clock.Now().Add(30*time.Minute).Unix() {
		t.Fatalf("%+v", c)
	}
	if _, err := s.AdminLease(ctx, "c1", "bob"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("已租出的卡不能再租: %v", err)
	}

	// 其他管理员不能转入、释放
	if _, err := s.AdminFund(ctx, "c1", 100, "bob"); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("bob 转入: %v", err)
	}
	if err := s.AdminRelease(ctx, "c1", "bob"); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("bob 释放: %v", err)
	}
	if n := len(mw.Requests("POST", pathRecharge)); n != 0 {
		t.Fatalf("不应发转入请求: %d", n)
	}

	mw.On("POST", pathRecharge, mwtest.OK(`{"status":"APPLY"}`))
	o, err := s.AdminFund(ctx, "c1", 100, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if o.Kind != KindRecharge || o.CardID != "c1" || o.AmountCents != 100 || o.Currency != "USD" || o.RequestedBy != "alice" {
		t.Fatalf("%+v", o)
	}

	// leased（不论持有方）查看卡号 → 标记
	mw.On("GET", sensitivePath("c1"), mwtest.OK(sensitiveJSON("c1")))
	sv, err := s.ViewSensitive(ctx, "c1", "bob")
	if err != nil || sv.CardNo != "4111111111111111" {
		t.Fatalf("%+v %v", sv, err)
	}
	if c := mustGetCard(t, "c1"); !c.LeaseSensitive || i64(c.LastSensitiveAt) != clock.Now().Unix() {
		t.Fatalf("leased 查看后应标记: %+v", c)
	}

	// used 的卡只查看、不改状态
	mw.On("GET", sensitivePath("used"), mwtest.OK(sensitiveJSON("used")))
	before := mustGetCard(t, "used")
	if _, err := s.ViewSensitive(ctx, "used", "alice"); err != nil {
		t.Fatal(err)
	}
	if after := mustGetCard(t, "used"); !reflect.DeepEqual(after, before) {
		t.Fatalf("used 的卡不应改变:\n%+v\n%+v", before, after)
	}

	// 本人释放：取过卡号、上限 1 → used
	if err := s.AdminRelease(ctx, "c1", "alice"); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "c1"); c.PoolState != PoolUsed || c.UseCount != 1 || c.LeaseOwner != "" {
		t.Fatalf("AdminRelease 后: %+v", c)
	}
	if n := countOutcomes(t); n != 0 {
		t.Fatalf("管理员释放不记录结果: %d", n)
	}

	// 过期：不能转入，但可以释放
	insertCard(t, Card{CardID: "c2", ProductID: "p1"})
	if _, err := s.AdminLease(ctx, "c2", "alice"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(31 * time.Minute)
	if _, err := s.AdminFund(ctx, "c2", 100, "alice"); !errors.Is(err, ErrLeaseInvalid) {
		t.Fatalf("过期租约转入: %v", err)
	}
	if err := s.AdminRelease(ctx, "c2", "alice"); err != nil {
		t.Fatal(err)
	}
	if c := mustGetCard(t, "c2"); c.PoolState != PoolReleasing || c.UseCount != 0 {
		t.Fatalf("%+v", c)
	}
}

func TestExpiryEnd(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"1026", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), true},
		{"1228", time.Date(2029, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"0130", time.Date(2030, 2, 1, 0, 0, 0, 0, time.UTC), true},
		{"13/26", time.Time{}, false},
		{"1326", time.Time{}, false},
		{"0026", time.Time{}, false},
		{"126", time.Time{}, false},
		{"", time.Time{}, false},
		{"ab26", time.Time{}, false},
	}
	for _, c := range cases {
		got, ok := expiryEnd(c.in)
		if ok != c.ok || (ok && !got.Equal(c.want)) {
			t.Errorf("expiryEnd(%q) = %v %v, want %v %v", c.in, got, ok, c.want, c.ok)
		}
	}
}
