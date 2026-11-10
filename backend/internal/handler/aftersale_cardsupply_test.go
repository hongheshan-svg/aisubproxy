package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/provider/providertest"
)

// cardSupplyFactsOf 按码找到服务商（供卡型），直接调 cardSupplyFacts。
func cardSupplyFactsOf(t *testing.T, ctx context.Context, code string) (afterSaleFacts, error) {
	t.Helper()
	p, err := provider.ForCode(provider.NormalizeCode(code))
	if err != nil {
		t.Fatalf("码 %q 找不到服务商: %v", code, err)
	}
	cs, ok := provider.AsCardSupply(p)
	if !ok {
		t.Fatalf("%s 不是供卡型", p.Name())
	}
	return cardSupplyFacts(ctx, cs, code)
}

func mustFacts(t *testing.T, code string) afterSaleFacts {
	t.Helper()
	f, err := cardSupplyFactsOf(t, context.Background(), code)
	if err != nil {
		t.Fatalf("不该出错: %v", err)
	}
	return f
}

func setOrderSQL(t *testing.T, no, set string, args ...any) {
	t.Helper()
	if _, err := db.DB.Exec(`UPDATE pay_orders SET `+set+` WHERE order_no = ?`, append(args, no)...); err != nil {
		t.Fatal(err)
	}
}

func TestCardSupplyFactsMapping(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	ref := func(id int64) string { return db.LocalCDKSourceRef(id) }
	until := time.Now().Add(30 * 24 * time.Hour).Unix()

	type want struct {
		status, guide, renewal string
		reusable               bool
		expires, usedAt        bool
	}
	check := func(t *testing.T, f afterSaleFacts, w want) {
		t.Helper()
		if !f.Found || f.Status != w.status || f.Reusable != w.reusable || f.Guide != w.guide || f.Renewal != w.renewal {
			t.Fatalf("得到 found=%v status=%q reusable=%v guide=%q renewal=%q，想要 %+v", f.Found, f.Status, f.Reusable, f.Guide, f.Renewal, w)
		}
		if (f.ExpiresAt != nil) != w.expires || (f.UsedAt != nil) != w.usedAt {
			t.Fatalf("时间不对: expires=%v usedAt=%v，想要 %+v", f.ExpiresAt, f.UsedAt, w)
		}
		if f.ExpiresAt != nil && f.ExpiresAt.Unix() != until {
			t.Fatalf("ExpiresAt 应是 access_until: %v", f.ExpiresAt)
		}
	}

	t.Run("从未兑换 unused", func(t *testing.T) {
		code, _ := e.issueCode("plus", "PH")
		f := mustFacts(t, code)
		check(t, f, want{status: "unused"})
		if f.Plan != "plus" || f.PlanKind != "subscription" || f.Message != "" {
			t.Fatalf("%+v", f)
		}
	})
	for _, tc := range []struct{ renewal, want string }{
		{"cancelled", "off"}, {"pending", "closing"}, {"skipped", ""}, {"", ""}, {"failed", ""},
	} {
		t.Run("已开通 renewal="+tc.renewal, func(t *testing.T) {
			code, id := e.issueCode("plus", "PH")
			no := e.insertOrder("cdk", ref(id), 0, "buyer@example.com", "succeeded", "", true)
			setOrderSQL(t, no, `renewal_state = ?, access_until = ?`, tc.renewal, until)
			f := mustFacts(t, code)
			check(t, f, want{status: "used", renewal: tc.want, expires: true, usedAt: true})
			if f.AccountEmail != "buyer@example.com" || f.Message != "" {
				t.Fatalf("邮箱原样给、成功文字用默认: %+v", f)
			}
		})
	}
	t.Run("已开通但没有到期时间", func(t *testing.T) {
		code, id := e.issueCode("plus", "PH")
		e.insertOrder("cdk", ref(id), 0, "a@example.com", "succeeded", "", true)
		f := mustFacts(t, code)
		if f.Status != "used" || f.ExpiresAt != nil || f.UsedAt == nil {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("点数码的 PlanKind", func(t *testing.T) {
		e.enableCredit()
		e.seedRealCreditPayment()
		code, _ := e.issueCode("credit250", "PH")
		f := mustFacts(t, code)
		if f.Status != "unused" || f.Plan != "credit250" || f.PlanKind != "credit" {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("处理中 processing", func(t *testing.T) {
		code, id := e.issueCode("plus", "PH")
		e.insertOrder("cdk", ref(id), 0, "a@example.com", "queued", "", false)
		f := mustFacts(t, code)
		check(t, f, want{status: "processing"})
		if f.Message != "已提交，正在排队" {
			t.Fatalf("沿用 localPublicState 的文字: %q", f.Message)
		}
	})
	t.Run("待人工 processing + manual_review", func(t *testing.T) {
		code, id := e.issueCode("plus", "PH")
		e.insertOrder("cdk", ref(id), 0, "a@example.com", "needs_review", "", true)
		check(t, mustFacts(t, code), want{status: "processing", guide: "manual_review"})
	})
	t.Run("失败转人工 failed", func(t *testing.T) {
		code, id := e.issueCode("plus", "PH")
		e.insertOrder("cdk", ref(id), 0, "a@example.com", "failed", "declined", true)
		f := mustFacts(t, code)
		check(t, f, want{status: "failed", guide: "manual_review"})
		if f.Message != "支付未成功，已转人工处理，请勿重复提交" {
			t.Fatalf("%q", f.Message)
		}
	})
	t.Run("人工禁用 disabled", func(t *testing.T) {
		code, id := e.issueCode("plus", "PH")
		if _, err := db.DB.Exec(`UPDATE local_cdks SET status = 'disabled' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		f := mustFacts(t, code)
		check(t, f, want{status: "disabled"})
		if f.Message != "卡密已禁用" || f.Replacement != "" || f.ReplacedButUnknown {
			t.Fatalf("%+v", f)
		}
	})

	// 干净失败（注资之前）：unused + 可重提 + 按错误码给指引
	guides := []struct{ errCode, guide string }{
		{"session_invalid", "session_invalid"},
		{"already_subscribed", "already_subscribed"},
		{"plan_unavailable", "plan_unavailable"},
		{"credit_no_subscription", "credit_needs_subscription"},
		{"credit_plan_ineligible", "credit_needs_subscription"},
		{"credit_not_personal", "credit_not_personal"},
		{"credit_delinquent", "credit_delinquent"},
		{"credit_currency_mismatch", "credit_region_mismatch"},
		{"something_new", "retry_later"},
		{"", "retry_later"},
	}
	for _, g := range guides {
		t.Run("干净失败 "+g.errCode, func(t *testing.T) {
			code, id := e.issueCode("plus", "PH")
			no := e.insertOrder("cdk", ref(id), 0, "a@example.com", "failed", "not_submitted", false)
			setOrderSQL(t, no, `error_code = ?`, g.errCode)
			f := mustFacts(t, code)
			check(t, f, want{status: "unused", reusable: true, guide: g.guide})
			if !afterSaleGuides[f.Guide] {
				t.Fatalf("指引编号不在固定列表里: %q", f.Guide)
			}
			if f.Message == "" || f.AccountEmail != "a@example.com" {
				t.Fatalf("干净失败应带原因文字和邮箱: %+v", f)
			}
		})
	}

	t.Run("不认得的码 Found=false 且不报错", func(t *testing.T) {
		f := mustFacts(t, "MW-AAAAA-BBBBB-CCCCC-DDDDD-EEEEE")
		if f.Found {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("库里的服务商对不上 Found=false", func(t *testing.T) {
		code, id := e.issueCode("plus", "PH")
		if _, err := db.DB.Exec(`UPDATE local_cdks SET provider = 'other' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		if f := mustFacts(t, code); f.Found {
			t.Fatalf("%+v", f)
		}
	})
	// 放最后：注资后失败会让付款熔断，之后发不了码
	t.Run("注资后失败次数用完 failed", func(t *testing.T) {
		code, id := e.issueCode("plus", "PH")
		for i := 0; i < db.MaxLocalCDKFundedFailures; i++ {
			e.insertFundedFailure(id, "a@example.com")
		}
		check(t, mustFacts(t, code), want{status: "failed", guide: "manual_review"})
	})
}

// 数据库出错和「没有这张码」要分开：前者返回 error（公共层回「暂时查不到」），不能当成找不到。
// 这里用已取消的 ctx 让 database/sql 报 context.Canceled，换到另一个问题也一样走 error 分支。
func TestCardSupplyFactsDBErrorIsError(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, id := e.issueCode("plus", "PH")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	f, err := cardSupplyFactsOf(t, canceled, code)
	if err == nil || f.Found {
		t.Fatalf("取消的 ctx 查码应返回 error: found=%v err=%v", f.Found, err)
	}

	// 查到码但派生状态的查询失败：删掉 pay_orders 表
	if _, err := db.DB.Exec(`ALTER TABLE pay_orders RENAME TO pay_orders_gone`); err != nil {
		t.Fatal(err)
	}
	f, err = cardSupplyFactsOf(t, context.Background(), code)
	if err == nil || f.Found {
		t.Fatalf("单据查询失败应返回 error: found=%v err=%v id=%d", f.Found, err, id)
	}

	// 经公共层：对外是「暂时查不到」，不是「未找到」
	r := lookupAfterSale(context.Background(), code, "")
	if r.Status != "unknown" || r.Message != afterSaleUnknownText || !r.queryFailed {
		t.Fatalf("%+v", r)
	}
}

func TestCardSupplyFactsExchanged(t *testing.T) {
	assertNoSecrets := func(t *testing.T, r cdkLookupResult, extra ...string) []byte {
		t.Helper()
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range append([]string{"v1:", "code_hash", "code_sealed"}, extra...) {
			if strings.Contains(string(b), bad) {
				t.Fatalf("响应里不该出现 %q: %s", bad, b)
			}
		}
		return b
	}

	t.Run("有密文：走换码接口，查旧码得到新码", func(t *testing.T) {
		e := newSealedExchangeEnv(t)
		code, oldID := exchangeableCode(t, e)
		_, newCode, newID := doSealedExchange(t, e, code)

		f := mustFacts(t, code)
		if !f.Found || f.Status != "replaced" || f.Replacement != newCode || f.ReplacedButUnknown {
			t.Fatalf("%+v (old=%d new=%d)", f, oldID, newID)
		}
		if f.Message != "该卡密已换发新码" {
			t.Fatalf("%q", f.Message)
		}
		r := lookupAfterSale(context.Background(), code, "")
		if r.Status != "replaced" || r.ReplacedUnknown || r.Replacement == nil {
			t.Fatalf("%+v", r)
		}
		if *r.Replacement != (lookupReplacement{Code: newCode, Status: "unused", Depth: 1}) {
			t.Fatalf("replacement=%+v", *r.Replacement)
		}
		assertNoSecrets(t, r, db.HashCDKCode(newCode), db.HashCDKCode(code))
	})

	t.Run("没有密文：旧版本换出的码只知道换过", func(t *testing.T) {
		e := newLocalEnv(t)
		e.seedRealPayment()
		code, oldID := e.issueCode("plus", "PH")
		e.insertFundedFailure(oldID, "a@example.com")
		const newCode = "MW-ZZZZZ-YYYYY-XXXXX-WWWWW-VVVVV"
		_, err := db.ExchangeLocalCDK(context.Background(), oldID, db.NewLocalCDK{
			Provider: "marswallex", CodeHash: db.HashCDKCode(newCode), TokenHash: "tok-nosealed", CodePrefix: newCode[:8],
			Product: "gpt", PlanKey: "plus", Region: "PH", IssuedBy: "agent-swap", ExchangedFrom: &oldID, AttemptsCarried: 1,
		}, time.Now())
		if err != nil {
			t.Fatal(err)
		}

		f := mustFacts(t, code)
		if !f.Found || f.Status != "replaced" || f.Replacement != "" || !f.ReplacedButUnknown {
			t.Fatalf("%+v", f)
		}
		r := lookupAfterSale(context.Background(), code, "")
		if r.Status != "replaced" || !r.ReplacedUnknown || r.Replacement != nil {
			t.Fatalf("%+v", r)
		}
		b := assertNoSecrets(t, r, newCode, db.HashCDKCode(newCode))
		if strings.Contains(string(b), `"replacement"`) {
			t.Fatalf("不该带 replacement: %s", b)
		}
	})

	t.Run("主密钥缺失时换的码", func(t *testing.T) {
		e := newSealedExchangeEnv(t)
		code, _ := exchangeableCode(t, e)
		t.Setenv("APP_MASTER_KEY", "")
		doSealedExchange(t, e, code)
		f := mustFacts(t, code)
		if f.Status != "replaced" || f.Replacement != "" || !f.ReplacedButUnknown {
			t.Fatalf("%+v", f)
		}
	})
}

// testcard 没有专门的适配器查询，也走同一套映射。
func TestCardSupplyFactsGenericProvider(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	e.useTestProviders()
	e.seedProviderRealPayment(providertest.TestCard)
	code, id := e.issueProviderCode(providertest.TestCard, "plus", "PH")
	if !strings.HasPrefix(code, "TC-") {
		t.Fatalf("%q", code)
	}
	f := mustFacts(t, code)
	if !f.Found || f.Status != "unused" || f.Plan != "plus" {
		t.Fatalf("%+v", f)
	}

	until := time.Now().Add(24 * time.Hour).Unix()
	no := e.insertProviderOrder(string(providertest.TestCard), "cdk", db.LocalCDKSourceRef(id), 0, "tc@example.com", "succeeded", "", true)
	setOrderSQL(t, no, `renewal_state = 'cancelled', access_until = ?`, until)
	f = mustFacts(t, code)
	if f.Status != "used" || f.Renewal != "off" || f.ExpiresAt == nil || f.ExpiresAt.Unix() != until || f.UsedAt == nil {
		t.Fatalf("%+v", f)
	}
	// 经公共层分派：不经过任何专门适配器
	r := lookupAfterSale(context.Background(), code, "")
	if r.Status != "used" || r.Renewal != "off" || r.ExpiresAt == nil {
		t.Fatalf("%+v", r)
	}
}

// ---- 售后查询公共层的断言（原来由 ZovoCard 的查询测试顺带覆盖，现在改用本站码证明）----

// 邮箱一律打码：响应里不出现完整邮箱，不论单子是已开通还是在处理中。
func TestCardSupplyLookupMasksEmail(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	const full = "buyer.user@example.com"
	for _, state := range []string{"succeeded", "queued"} {
		t.Run(state, func(t *testing.T) {
			code, id := e.issueCode("plus", "PH")
			e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, full, state, "", state == "succeeded")
			raw, _ := json.Marshal(lookupAfterSale(context.Background(), code, ""))
			if strings.Contains(string(raw), full) {
				t.Fatalf("响应里出现完整邮箱: %s", raw)
			}
			if r := lookupAfterSale(context.Background(), code, ""); r.AccountEmail != "bu***@example.com" {
				t.Fatalf("邮箱应打码: %+v", r)
			}
		})
	}
}

// 换出的新码严格按换码记录里的新码 ID 取：两张新码前缀被改成一样，也不会串到对方。
func TestCardSupplyReplacementIsStrictByID(t *testing.T) {
	e := newSealedExchangeEnv(t)
	codeA, _ := exchangeableCode(t, e)
	_, newA, idA := doSealedExchange(t, e, codeA)
	codeB, _ := exchangeableCode(t, e)
	_, newB, idB := doSealedExchange(t, e, codeB)
	if _, err := db.DB.Exec(`UPDATE local_cdks SET code_prefix = (SELECT code_prefix FROM local_cdks WHERE id = ?) WHERE id = ?`, idA, idB); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ old, want, other string }{{codeA, newA, newB}, {codeB, newB, newA}} {
		r := lookupAfterSale(context.Background(), tc.old, "")
		raw, _ := json.Marshal(r)
		if r.Status != "replaced" || r.Replacement == nil || r.Replacement.Code != tc.want || strings.Contains(string(raw), tc.other) {
			t.Fatalf("应只给出自己的新码: %s", raw)
		}
	}
}

// 已开通的码：UsedAt 是 RFC3339 UTC（以 Z 结尾），与服务器所在时区无关。
func TestCardSupplyUsedAtIsRFC3339UTC(t *testing.T) {
	e := newLocalEnv(t)
	e.seedRealPayment()
	code, id := e.issueCode("plus", "PH")
	no := e.insertOrder("cdk", db.LocalCDKSourceRef(id), 0, "a@example.com", "succeeded", "", true)
	setOrderSQL(t, no, `finished_at = ?`, 1700000000)
	r := lookupAfterSale(context.Background(), code, "")
	if r.Status != "used" || r.UsedAt == nil || *r.UsedAt != "2023-11-14T22:13:20Z" {
		t.Fatalf("%+v", r)
	}
	if _, err := time.Parse(time.RFC3339, *r.UsedAt); err != nil {
		t.Fatal(err)
	}
}

// 过期判定在公共层：facts 给出 expired，就对外显示「卡密已过期」。本站码没有过期时间，所以这一条直接测公共层。
func TestAfterSaleResultExpiredStatus(t *testing.T) {
	r := afterSaleResult("MW-AAAAA-BBBBB", afterSaleFacts{Found: true, Status: "expired"})
	if r.Status != "expired" || r.Used || r.Message != "卡密已过期" {
		t.Fatalf("%+v", r)
	}
	// 只有已开通的码才给到期时间，过期的码不给
	exp := time.Now().Add(-time.Hour)
	if r := afterSaleResult("MW-AAAAA-BBBBB", afterSaleFacts{Found: true, Status: "expired", ExpiresAt: &exp}); r.ExpiresAt != nil {
		t.Fatalf("过期的码不给到期时间: %+v", r)
	}
}
