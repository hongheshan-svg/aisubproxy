package payengine

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

var ctx = context.Background()

func newOrder(t *testing.T, email string) *Order {
	t.Helper()
	o := &Order{Source: "admin", PlanKey: "plus", Region: "PH", Currency: "PHP", AccountEmail: email, SessionSealed: "sealed"}
	if err := insertOrder(ctx, o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestNewOrderNoFormat(t *testing.T) {
	now := time.Date(2026, 11, 1, 3, 4, 5, 0, time.FixedZone("x", 8*3600))
	no := newOrderNo(now)
	if !regexp.MustCompile(`^PO\d{12}[0-9a-f]{8}$`).MatchString(no) || len(no) != 22 {
		t.Fatalf("单号格式错误: %s", no)
	}
	if no[2:14] != "261031190405" {
		t.Fatalf("时间部分应为 UTC: %s", no)
	}
}

func TestInsertOrderInflightUnique(t *testing.T) {
	dbtest.Open(t)
	a := newOrder(t, "a@x.com")
	if err := insertOrder(ctx, &Order{OrderNo: "PO-dup", AccountEmail: "a@x.com"}); !errors.Is(err, ErrInflight) {
		t.Fatalf("期望 ErrInflight，得到 %v", err)
	}
	if err := finish(ctx, a.OrderNo, StateQueued, StateFailed, "error", "x", nil); err != nil {
		t.Fatal(err)
	}
	b := newOrder(t, "a@x.com")
	if err := finish(ctx, b.OrderNo, StateQueued, StatePreviewed, "info", "x", nil); err != nil {
		t.Fatal(err)
	}
	newOrder(t, "a@x.com")
	ev, _ := listEvents(ctx, a.OrderNo)
	if len(ev) != 2 || ev[0].State != StateQueued {
		t.Fatalf("事件不符: %+v", ev)
	}
}

func TestTransitionChecksFrom(t *testing.T) {
	dbtest.Open(t)
	o := newOrder(t, "a@x.com")
	if err := transition(ctx, o.OrderNo, StateChecking, StateOpening, "info", "x", nil); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("期望 ErrStateChanged，得到 %v", err)
	}
	if g, _ := getOrder(ctx, o.OrderNo); g.State != StateQueued {
		t.Fatalf("状态不应变化: %s", g.State)
	}
	if err := transition(ctx, o.OrderNo, StateQueued, StateChecking, "info", "检查账号", map[string]any{"account_id": "acc1"}); err != nil {
		t.Fatal(err)
	}
	g, _ := getOrder(ctx, o.OrderNo)
	ev, _ := listEvents(ctx, o.OrderNo)
	if g.State != StateChecking || g.AccountID != "acc1" || len(ev) != 2 || ev[1].State != StateChecking || ev[1].Message != "检查账号" {
		t.Fatalf("迁移结果不符: %+v %+v", g, ev)
	}
	if err := addEvent(ctx, o.OrderNo, "info", "截图", "shot.png"); err != nil {
		t.Fatal(err)
	}
	ev, _ = listEvents(ctx, o.OrderNo)
	if ev[2].State != StateChecking || ev[2].Screenshot != "shot.png" {
		t.Fatalf("addEvent 不符: %+v", ev[2])
	}
}

func TestTransitionRejectsUnknownColumn(t *testing.T) {
	dbtest.Open(t)
	o := newOrder(t, "a@x.com")
	for _, col := range []string{"state; DROP", "state", "session_sealed"} {
		if err := transition(ctx, o.OrderNo, StateQueued, StateChecking, "info", "x", map[string]any{col: "v"}); err == nil {
			t.Fatalf("列 %q 应被拒绝", col)
		}
	}
	if g, _ := getOrder(ctx, o.OrderNo); g.State != StateQueued {
		t.Fatal("状态不应变化")
	}
}

func TestFinishClearsSession(t *testing.T) {
	dbtest.Open(t)
	o := newOrder(t, "a@x.com")
	if g, _ := getOrder(ctx, o.OrderNo); g.SessionSealed != "sealed" {
		t.Fatal("会话应已存入")
	}
	if err := finish(ctx, o.OrderNo, StateQueued, StateFailed, "error", "失败", map[string]any{"failure": FailureNotSubmitted}); err != nil {
		t.Fatal(err)
	}
	g, _ := getOrder(ctx, o.OrderNo)
	if g.SessionSealed != "" || g.FinishedAt == nil || g.Failure != FailureNotSubmitted {
		t.Fatalf("finish 结果不符: %+v", g)
	}
}

func TestListOrdersFilterAndPage(t *testing.T) {
	dbtest.Open(t)
	for i := 0; i < 25; i++ {
		o := newOrder(t, string(rune('a'+i))+"@x.com")
		if i%5 == 0 {
			if err := finish(ctx, o.OrderNo, StateQueued, StateFailed, "error", "x", nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	l, total, err := listOrders(ctx, StateFailed, 1, 0)
	if err != nil || total != 5 || len(l) != 5 || l[0].ID < l[1].ID {
		t.Fatalf("筛选不符 total=%d len=%d err=%v", total, len(l), err)
	}
	l, total, _ = listOrders(ctx, "", 1, 0)
	if total != 25 || len(l) != 20 {
		t.Fatalf("size 0 应为 20: %d %d", total, len(l))
	}
	l, _, _ = listOrders(ctx, "", 1, 101)
	if len(l) != 25 {
		t.Fatalf("size 101 应截为 100: %d", len(l))
	}
	l, _, _ = listOrders(ctx, "", 2, 20)
	if len(l) != 5 {
		t.Fatalf("第二页应 5 条: %d", len(l))
	}
	in, _ := ordersInStates(ctx, StateQueued, StateChecking)
	if len(in) != 20 {
		t.Fatalf("ordersInStates: %d", len(in))
	}
}

// 接口直接序列化 Order：会话密文与租约令牌（卡的持有凭证）都不能出去。
func TestOrderJSONHidesSecrets(t *testing.T) {
	const sealed, token = "sealed-secret-xyz", "lease-secret-xyz"
	b, err := json.Marshal(Order{OrderNo: "PO1", CardID: "c1", CardLast4: "4242", SessionSealed: sealed, LeaseToken: token})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{sealed, token} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("序列化结果不应含 %q: %s", bad, b)
		}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"session_sealed", "SessionSealed", "lease_token", "LeaseToken"} {
		if _, ok := m[key]; ok {
			t.Fatalf("不应有键 %s: %s", key, b)
		}
	}
	if m["order_no"] != "PO1" || m["card_id"] != "c1" || m["card_last4"] != "4242" {
		t.Fatalf("其余字段应保留: %s", b)
	}
}
