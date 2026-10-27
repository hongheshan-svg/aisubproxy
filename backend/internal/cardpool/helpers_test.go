package cardpool

import (
	"sync"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

// testClock 是可前进、并发安全的测试时钟。
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// alertLog 记录告警文本，并发安全。
type alertLog struct {
	mu    sync.Mutex
	texts []string
}

func (a *alertLog) add(text string) {
	a.mu.Lock()
	a.texts = append(a.texts, text)
	a.mu.Unlock()
}

func (a *alertLog) Texts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.texts...)
}

func (a *alertLog) Count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.texts)
}

func newTestService(t *testing.T) (*Service, *mwtest.Server, *testClock, *alertLog) {
	t.Helper()
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	mw := mwtest.New(t)
	clock := &testClock{t: time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC)}
	log := &alertLog{}
	currentClockMu.Lock()
	currentClock = clock
	currentClockMu.Unlock()
	s := New(Options{
		Client: func() (*marswallex.Client, error) { return mw.Client(), nil },
		Now:    clock.Now,
		Alert:  log.add,
	})
	return s, mw, clock, log
}

// currentClock 指向最近一次 newTestService 创建的时钟，insertCard/insertOrder 用它取"现在"。
var (
	currentClockMu sync.Mutex
	currentClock   *testClock
)

func testNow() time.Time {
	currentClockMu.Lock()
	c := currentClock
	currentClockMu.Unlock()
	if c == nil {
		return time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC)
	}
	return c.Now()
}

// insertCard 插入一张卡，空字段补默认值：Expiry "1028"、PlatformStatus "ACTIVE"、Currency "USD"、
// Source "opened"、PoolState "available"、CreatedAt/UpdatedAt = 测试时钟当前时刻（非零值保留）。
func insertCard(t *testing.T, c Card) Card {
	t.Helper()
	now := testNow().Unix()
	if c.Expiry == "" {
		c.Expiry = "1028"
	}
	if c.PlatformStatus == "" {
		c.PlatformStatus = "ACTIVE"
	}
	if c.Currency == "" {
		c.Currency = "USD"
	}
	if c.Source == "" {
		c.Source = "opened"
	}
	if c.PoolState == "" {
		c.PoolState = PoolAvailable
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = now
	}
	if c.UpdatedAt == 0 {
		c.UpdatedAt = now
	}
	_, err := db.DB.Exec(`INSERT INTO mw_cards (`+cardCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.CardID, c.CardHolderID, c.ProductID, c.CardLabel, c.CardCountry, c.Currency, c.Last4, c.Expiry, c.PlatformStatus,
		c.AvailableCents, c.FrozenCents, c.BalanceSyncedAt, c.Source, c.PoolState, c.LeaseToken, c.LeaseOwner, c.LeaseExpiresAt,
		c.LeaseSensitive, c.UseCount, c.LastSensitiveAt, c.RetireReason, c.DisabledReason, c.FrozenAt, c.CancelAfter, c.NextActionAt,
		c.CreatedAt, c.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// insertOrder 插入一张单据：State 默认 "pending"，CreatedAt/UpdatedAt 默认测试时钟当前时刻，
// OutOrderNo 为空时用 newOutOrderNo 生成。返回填好默认值并带自增 ID 的单据。
func insertOrder(t *testing.T, o Order) Order {
	t.Helper()
	now := testNow()
	if o.State == "" {
		o.State = StatePending
	}
	if o.CreatedAt == 0 {
		o.CreatedAt = now.Unix()
	}
	if o.UpdatedAt == 0 {
		o.UpdatedAt = now.Unix()
	}
	if o.OutOrderNo == "" {
		o.OutOrderNo = newOutOrderNo(o.Kind, now)
	}
	res, err := db.DB.Exec(`INSERT INTO mw_card_orders (`+orderCols[len("id, "):]+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.OutOrderNo, o.Kind, o.CardID, o.CardHolderID, o.ProductID, o.AmountCents, o.Currency, o.TargetStatus,
		o.State, o.PlatformTxnID, o.EverUnknown, o.Attempts, o.NextCheckAt, o.FirstSentAt, o.LastError, o.RequestedBy, o.Note,
		o.AlertedStale, o.CreatedAt, o.UpdatedAt, o.FinishedAt)
	if err != nil {
		t.Fatal(err)
	}
	o.ID, _ = res.LastInsertId()
	return o
}

// savePolicy 在默认策略上应用 mut 后保存。
func savePolicy(t *testing.T, mut func(*Policy)) Policy {
	t.Helper()
	p, err := LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if mut != nil {
		mut(&p)
	}
	if err := SavePolicy(p); err != nil {
		t.Fatal(err)
	}
	return p
}
