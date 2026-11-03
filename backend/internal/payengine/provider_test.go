package payengine

import (
	"errors"
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// 第二家供卡型服务商（与注册表测试里的假服务商同名）。
const otherProvider = "testcard"

// clonePool 复制一份行为相同、调用记录独立的假卡池。
func clonePool(src *fakePool) *fakePool {
	return &fakePool{acquire: src.acquire, fund: src.fund, waitOrder: src.waitOrder, sensitive: src.sensitive,
		waitOTP: src.waitOTP, release: src.release, cardInfo: src.cardInfo, listTxns: src.listTxns, backfill: src.backfill}
}

// setOrderProvider 直接改单据的服务商，模拟重启后换了版本、这家服务商被去掉。
func setOrderProvider(t *testing.T, no, p string) {
	t.Helper()
	if _, err := db.DB.Exec(`UPDATE pay_orders SET provider = ? WHERE order_no = ?`, p, no); err != nil {
		t.Fatal(err)
	}
}

// 两家供卡型服务商各有卡池：单据用哪个卡池只看单据的 provider，另一家的卡池一次都不碰。
func TestEnginePoolByOrderProvider(t *testing.T) {
	for _, prov := range []string{testProvider, otherProvider} {
		t.Run(prov, func(t *testing.T) {
			h := newPayHarness(t, nil)
			pools := map[string]*fakePool{testProvider: h.pool, otherProvider: clonePool(h.pool)}
			h.rebuildPools(map[string]Pool{testProvider: pools[testProvider], otherProvider: pools[otherProvider]})
			h.gpt.accountFn = planFrom(3)
			h.br.waitOutcome = outcomes(outcomeStep{out: paybrowser.OutcomeSuccess, text: "Thanks"})
			h.start(t)
			o, err := h.svc.Submit(ctx, Request{Provider: prov, PlanKey: "plus", Region: "PH", Session: testSession, Source: "admin_test", SourceRef: "admin"})
			if err != nil {
				t.Fatal(err)
			}
			g := waitDone(t, o.OrderNo)
			if g.State != StateSucceeded || g.Provider != prov {
				t.Fatalf("应成功并记下服务商: %+v", g)
			}
			used := pools[prov]
			for _, m := range []string{"Acquire", "CardInfo", "Fund", "WaitOrder", "Sensitive", "Release"} {
				if used.count(m) != 1 {
					t.Fatalf("%s 的卡池 %s 应调用 1 次: %d", prov, m, used.count(m))
				}
			}
			for name, p := range pools {
				if name != prov && p.total() != 0 {
					t.Fatalf("别家（%s）的卡池不应被调用: %d 次", name, p.total())
				}
			}
			if ev, err := h.svc.Evidence(ctx, o.OrderNo); err != nil || used.count("ListTransactions") == 0 {
				t.Fatalf("证据应查本家卡池: %v %v", ev, err)
			}
		})
	}
}

// 建单时就要求服务商有卡池：空的、没有卡池的都拒绝，不建单。
func TestSubmitRequiresProviderPool(t *testing.T) {
	h := newHarness(t)
	for prov, want := range map[string]error{"": ErrBadRequest, " ": ErrBadRequest, "gone": ErrProviderUnavailable, otherProvider: ErrProviderUnavailable} {
		_, err := h.svc.Submit(ctx, Request{Provider: prov, PlanKey: "plus", Region: "PH", Session: testSession, Source: "admin_test", DryRun: true})
		if !errors.Is(err, want) {
			t.Fatalf("%q: 期望 %v，得到 %v", prov, want, err)
		}
	}
	if _, total, _ := listOrders(ctx, "", 1, 0); total != 0 {
		t.Fatalf("出错时不应建单: %d", total)
	}
	if !h.svc.HasPool(testProvider) || h.svc.HasPool("gone") || h.svc.HasPool("") {
		t.Fatal("HasPool 判断不对")
	}
}

// 服务商没有卡池、还没取卡：以 provider_unavailable 放弃，不查账号、不开浏览器，任何一家的卡池都不碰。
func TestUnknownProviderBeforeCardAborts(t *testing.T) {
	for _, prov := range []string{"gone", ""} {
		t.Run(prov, func(t *testing.T) {
			h := newPayHarness(t, nil)
			other := clonePool(h.pool)
			h.rebuildPools(map[string]Pool{testProvider: h.pool, otherProvider: other})
			o := presetOrder(t, "buyer@example.com", StateQueued, nil)
			setOrderProvider(t, o.OrderNo, prov)
			h.start(t)
			g := waitDone(t, o.OrderNo)
			assertNotSubmitted(t, g, "provider_unavailable")
			if h.pool.total() != 0 || other.total() != 0 {
				t.Fatalf("不应碰任何卡池: %d %d", h.pool.total(), other.total())
			}
			if h.gpt.count("Account") != 0 || len(h.br.browsers()) != 0 {
				t.Fatal("没有卡池时不应查账号、开浏览器")
			}
		})
	}
}

// 服务商没有卡池、已经取卡或注资：转待人工并告警，绝不交给别家的卡池释放；没取卡的照旧按服务重启放弃。
func TestUnknownProviderAfterCardNeedsReview(t *testing.T) {
	h := newPayHarness(t, nil)
	other := clonePool(h.pool)
	h.rebuildPools(map[string]Pool{testProvider: h.pool, otherProvider: other})
	now := h.clock.Now().Unix()
	funded := presetOrder(t, "funded@example.com", StateFunding, map[string]any{"card_id": "card-f", "lease_token": "tok-f", "fund_order_no": "FUND-F"})
	filling := presetOrder(t, "filling@example.com", StateFilling, map[string]any{"card_id": "card-g", "lease_token": "tok-g"})
	opening := presetOrder(t, "opening@example.com", StateOpening, nil)
	verifying := presetOrder(t, "verifying@example.com", StateVerifying, map[string]any{
		"submitted_at": now - 30, "card_id": "card-v", "lease_token": "tok-v", "page_outcome": "success",
		"verify_until": now + 600, "next_check_at": now,
	})
	for _, o := range []*Order{funded, filling, opening, verifying} {
		setOrderProvider(t, o.OrderNo, "gone")
	}
	if _, err := h.svc.Evidence(ctx, verifying.OrderNo); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("没有卡池时查证据应报错: %v", err)
	}

	h.start(t)
	for _, o := range []*Order{funded, filling, verifying} {
		g := waitDone(t, o.OrderNo)
		if g.State != StateNeedsReview || g.ErrorCode != "provider_unavailable" || !strings.Contains(g.ErrorMessage, `"gone"`) {
			t.Fatalf("已取卡的单应转待人工: %+v", g)
		}
	}
	assertNotSubmitted(t, mustOrder(t, opening.OrderNo), "service_restart")
	if h.pool.total() != 0 || other.total() != 0 {
		t.Fatalf("绝不使用别家的卡池: %d %d", h.pool.total(), other.total())
	}
	if h.gpt.count("Account") != 0 {
		t.Fatal("没有卡池时确认阶段直接转人工，不查账号")
	}
	alerts := strings.Join(h.alerts.Texts(), "\n")
	for _, o := range []*Order{funded, filling, verifying} {
		if !strings.Contains(alerts, o.OrderNo) {
			t.Fatalf("转待人工应告警 %s: %s", o.OrderNo, alerts)
		}
	}
	ev, _ := listEvents(ctx, verifying.OrderNo)
	found := false
	for _, e := range ev {
		found = found || strings.Contains(e.Message, "租约没有释放")
	}
	if !found {
		t.Fatalf("时间线应说明租约没有释放: %+v", ev)
	}
}
