package paybrowser_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// fillStripe 打开真实DOM仿造页并填表，返回 Fill 的错误。
func fillStripe(t *testing.T, f *fakeCheckout, query string, bill paybrowser.Billing) (*paybrowser.Browser, error) {
	t.Helper()
	b, ctx := openKind(t, f, stripeCheckout, query, paybrowser.LaunchOptions{})
	return b, b.Fill(ctx, testCard, bill)
}

// wantFillRefused 断言 Fill 报错且错误含 want，错误不泄露卡片与邮箱，页面没被点过支付。
func wantFillRefused(t *testing.T, f *fakeCheckout, b *paybrowser.Browser, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Fill = %v，期望报错并含 %q", err, want)
	}
	for _, secret := range []string{testCard.Number, testCard.CVC, testEmail} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("错误消息泄露了 %q: %v", secret, err)
		}
	}
	// Fill 没成功就不能提交：任何一步失败都按提交前放弃处理
	if err := b.Submit(openCtx(t)); err == nil {
		t.Fatal("Fill 失败后 Submit 应报错")
	}
	time.Sleep(200 * time.Millisecond)
	if n := f.submitClicks.Load(); n != 0 {
		t.Fatalf("Fill 失败时支付按钮被点了 %d 次", n)
	}
	if n := len(f.payments()); n != 0 {
		t.Fatalf("Fill 失败时仿造页收到 %d 次提交", n)
	}
}

// fillAndPay 在真实DOM仿造页上走完 Fill、Submit，等到跳转到成功页，返回 /pay 收到的字段。
func fillAndPay(t *testing.T, f *fakeCheckout, query string) payRecord {
	t.Helper()
	b, ctx := openKind(t, f, stripeCheckout, query, paybrowser.LaunchOptions{})
	fillAndSubmit(t, b, ctx)
	waitPaid(t, f, b, ctx, stripeCheckout)
	return onlyPayment(t, f)
}

// 两种前缀下都要点开手动地址、取消 Link、填必填的邮箱，手机不填。shipping 是原始数据，billing 是推断。
func TestStripeFillBothPrefixes(t *testing.T) {
	for _, prefix := range []string{"shipping", "billing"} {
		t.Run(prefix, func(t *testing.T) {
			needChrome(t)
			f := newFakeCheckout(t)
			got := fillAndPay(t, f, "mode=success&prefix="+prefix)
			want := payRecord{
				Method: "new", Number: testCard.Number, Expiry: "1230", CVC: testCard.CVC, NumberKeys: len(testCard.Number),
				Name: testBilling.Name, Country: testBilling.Country, Line1: testBilling.Line1, City: testBilling.City,
				State: testBilling.State, Zip: testBilling.Zip, Email: testEmail,
			}
			if got != want {
				t.Fatalf("仿造页收到 %+v\n期望 %+v", got, want)
			}
			if n := f.submitClicks.Load(); n != 1 {
				t.Fatalf("支付按钮被点了 %d 次，期望 1 次", n)
			}
		})
	}
}

// 真实数据证实不了字段要不要点按钮才出现（发现报告与原始数据不符），两种情况都要能走通。
func TestManualAddressVariants(t *testing.T) {
	cases := map[string]string{
		"字段过一会儿自己出现":    "autoreveal=1",
		"入口等街道填好才出现":    "latemanual=1",
		"点入口后字段过一会儿才出现": "reveallag=3000",
		"入口第一次点击没反应":    "revealclicks=2",
	}
	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			needChrome(t)
			f := newFakeCheckout(t)
			got := fillAndPay(t, f, "mode=success&"+query)
			if got.City != testBilling.City || got.State != testBilling.State || got.Zip != testBilling.Zip {
				t.Fatalf("仿造页收到 %+v，城市、州、邮编应已填写", got)
			}
		})
	}
}

// 入口文字写在支付按钮上时绝不能点：这条路径没有点击前置位保护，点了就是一次记不下来的付款。
func TestManualAddressNeverClicksSubmit(t *testing.T) {
	needChrome(t)
	t.Cleanup(paybrowser.ShortenFillWaits(2 * time.Second))
	f := newFakeCheckout(t)
	b, err := fillStripe(t, f, "mode=success&submitmanual=1", testBilling)
	wantFillRefused(t, f, b, err, "城市")
}

// 买点数时 Stripe 要求同意服务条款：默认不勾，不勾点支付会被前端拦下，不会扣款。用户已确认自动勾上。
func TestFillTermsConsent(t *testing.T) {
	t.Run("要同意条款就勾上", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, stripeCheckout, "mode=success&terms=1", paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)
		waitPaid(t, f, b, ctx, stripeCheckout)
		if got := onlyPayment(t, f); got.Number != testCard.Number {
			t.Fatalf("卡号不一致")
		}
		if got := b.FillReport().Terms; got != "已勾选" {
			t.Fatalf("报告的同意条款 = %q", got)
		}
		if lines := strings.Join(b.FillReport().Lines(), "\n"); !strings.Contains(lines, "同意条款：已勾选") {
			t.Fatalf("时间线没写同意条款的处理：\n%s", lines)
		}
	})
	t.Run("条款勾不上就放弃", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, err := fillStripe(t, f, "mode=success&terms=stuck", testBilling)
		wantFillRefused(t, f, b, err, "同意条款")
	})
	// 条款勾选框做成不写 type 的 <button role="checkbox">、在支付表单里：点它就是提交支付表单。点之前看出是提交控件，不点、放弃
	t.Run("条款勾选框是提交控件就不点", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, err := fillStripe(t, f, "mode=success&terms=button", testBilling)
		wantFillRefused(t, f, b, err, "勾选「同意条款」失败: 目标是提交控件，拒绝点击")
	})
	t.Run("页面没有条款勾选框照常走", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, stripeCheckout, "mode=success", paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)
		waitPaid(t, f, b, ctx, stripeCheckout)
		if got := b.FillReport().Terms; got != "页面没有" {
			t.Fatalf("报告的同意条款 = %q", got)
		}
	})
}

func TestFillLinkSave(t *testing.T) {
	t.Run("取消不掉就放弃", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, err := fillStripe(t, f, "mode=success&linkstuck=1", testBilling)
		wantFillRefused(t, f, b, err, "Link")
	})
	t.Run("键入邮箱后被重新勾上要再取消", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		got := fillAndPay(t, f, "mode=success&linkrecheck=1")
		if got.LinkSave || got.Phone != "" {
			t.Fatalf("Link = %v，手机是否为空 %v；期望 Link 未勾选、手机为空", got.LinkSave, got.Phone == "")
		}
	})
	t.Run("Link 弹出验证就放弃", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, err := fillStripe(t, f, "mode=success&linkmodal=1", testBilling)
		wantFillRefused(t, f, b, err, "Link")
	})
	t.Run("Link 要验证身份就点 Pay without Link 照常填卡", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, ctx := openKind(t, f, stripeCheckout, "mode=success&linkverify=1", paybrowser.LaunchOptions{})
		fillAndSubmit(t, b, ctx)
		waitPaid(t, f, b, ctx, stripeCheckout)
		if got := onlyPayment(t, f); got.Number != testCard.Number || got.LinkSave {
			t.Fatalf("卡号是否一致 %v，Link 保存 %v；期望卡号一致、不存 Link", got.Number == testCard.Number, got.LinkSave)
		}
		if n := f.linkSMSClicks.Load(); n != 0 {
			t.Fatalf("「Text a code」被点了 %d 次，绝不能点", n)
		}
		if got := b.FillReport().LinkVerify; got != "已点「Pay without Link」" {
			t.Fatalf("报告的 Link 验证 = %q", got)
		}
		if lines := strings.Join(b.FillReport().Lines(), "\n"); !strings.Contains(lines, "Link 验证：已点「Pay without Link」") {
			t.Fatalf("时间线没写 Link 验证的处理：\n%s", lines)
		}
	})
	t.Run("Link 要验证身份但没有 Pay without Link 就放弃", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		start := time.Now()
		b, err := fillStripe(t, f, "mode=success&linkverify=nobutton", testBilling)
		wantFillRefused(t, f, b, err, "Pay without Link")
		if el := time.Since(start); el > 20*time.Second {
			t.Fatalf("验证框已出现就不必等满卡号框的时限，实际等了 %v", el)
		}
		if n := f.linkSMSClicks.Load(); n != 0 {
			t.Fatalf("「Text a code」被点了 %d 次，绝不能点", n)
		}
	})
	t.Run("键入邮箱后 Link 要验证身份就放弃", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, err := fillStripe(t, f, "mode=success&linkverify=onemail", testBilling)
		wantFillRefused(t, f, b, err, "Link 要求验证身份")
		if n := f.linkSMSClicks.Load(); n != 0 {
			t.Fatalf("「Text a code」被点了 %d 次，绝不能点", n)
		}
	})
	t.Run("页面没有 Link 区块照常走", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		got := fillAndPay(t, f, "mode=success&nolink=1")
		if got.Number != testCard.Number {
			t.Fatalf("仿造页收到 %+v", got)
		}
	})
}

// 手机一律不填：取消 Link 后仍必填只能放弃。
func TestFillPhoneStillRequiredRefused(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, err := fillStripe(t, f, "mode=success&phonealways=1", testBilling)
	wantFillRefused(t, f, b, err, "手机")
}

func TestFillEmailRules(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"aria-required 且为空要填", "emailattr=aria", testEmail},
		{"required 属性且为空要填", "emailattr=required", testEmail},
		{"已预填不改", "emailattr=aria&emailvalue=other%40example.com", "other@example.com"},
		{"非必填不填", "emailattr=none", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			needChrome(t)
			f := newFakeCheckout(t)
			if got := fillAndPay(t, f, "mode=success&"+c.query).Email; got != c.want {
				t.Fatalf("邮箱 = %q，期望 %q", got, c.want)
			}
		})
	}
	t.Run("必填但没有邮箱可填就放弃", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		bill := testBilling
		bill.Email = ""
		b, err := fillStripe(t, f, "mode=success", bill)
		wantFillRefused(t, f, b, err, "邮箱")
	})
}

// 键入可能让页面重绘：最后的核对要查得出来，宁可放弃也不带着空字段点支付。
func TestFillFinalRecheck(t *testing.T) {
	t.Run("键入邮箱清空了姓名", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, err := fillStripe(t, f, "mode=success&wipeonemail=1", testBilling)
		wantFillRefused(t, f, b, err, "姓名")
	})
	t.Run("改国家过一会儿才重绘", func(t *testing.T) {
		needChrome(t)
		f := newFakeCheckout(t)
		b, err := fillStripe(t, f, "mode=success&countrylag=1500", testBilling)
		wantFillRefused(t, f, b, err, "姓名")
	})
}

// 报告列出每个字段命中了哪个选择器，全程不含卡号、邮箱和地址。
func TestFillReportHasNoValues(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, err := fillStripe(t, f, "mode=success", testBilling)
	if err != nil {
		t.Fatalf("Fill: %v", err)
	}
	lines := strings.Join(b.FillReport().Lines(), "\n")
	for _, want := range []string{"字段「卡号」：命中 #cardNumber（主页面），核对一致", "字段「城市」：命中 #shippingLocality（主页面），核对一致",
		"手动地址：已点击 1 次", "Link 保存：已取消", "邮箱：必填且为空，已填账号邮箱", "手机：未显示"} {
		if !strings.Contains(lines, want) {
			t.Errorf("报告缺少 %q：\n%s", want, lines)
		}
	}
	for _, secret := range []string{testCard.Number, testCard.CVC, testEmail, testBilling.Name, testBilling.Line1, testBilling.City, testBilling.Zip} {
		if strings.Contains(lines, secret) {
			t.Errorf("报告泄露了 %q：\n%s", secret, lines)
		}
	}
	if st := b.PayButtonState(openCtx(t)); st != "可点击" {
		t.Errorf("支付按钮状态 = %q", st)
	}
	if n := f.submitClicks.Load(); n != 0 {
		t.Errorf("读状态时支付按钮被点了 %d 次", n)
	}
}

// 旧页的卡字段在 iframe 里，报告要看得出来；非必填的邮箱不能填。
func TestFillReportOldPage(t *testing.T) {
	needChrome(t)
	f := newFakeCheckout(t)
	b, ctx := openCheckout(t, f, "mode=success", paybrowser.LaunchOptions{})
	if err := b.Fill(ctx, testCard, testBilling); err != nil {
		t.Fatalf("Fill: %v", err)
	}
	lines := strings.Join(b.FillReport().Lines(), "\n")
	for _, want := range []string{"字段「卡号」：命中 #payment-numberInput（iframe），核对一致", "手动地址：未点击", "Link 保存：页面没有", "邮箱：非必填，未填写", "手机：可见但无需填写，未填写"} {
		if !strings.Contains(lines, want) {
			t.Errorf("报告缺少 %q：\n%s", want, lines)
		}
	}
}
