package payengine

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// 试运行到填表：在真实支付页上走完 Fill，看清字段命中与核对结果，再决定要不要做真实付款。
//
// 走不到点支付，不是靠一个判断，而是四层：
//  1. fillOnly 只拿到 FormBrowser，这个接口没有 Submit，也拿不到卡池
//  2. openPage 一创建浏览器就调用 LockSubmit，之后浏览器对象本身拒绝点击，谁调 Submit 都只会得到 ErrSubmitLocked
//  3. previewFill 不调用 pay，整个流程里没有 Submit 的调用点
//  4. pay 入口拒绝试运行单
//
// 卡是 Stripe 公开的测试卡号，不碰卡池：不选卡、不注资、不取卡号，也就没有租约要释放。

const (
	previewCardNumber = "4242424242424242" // Stripe 公开测试卡号
	previewCVC        = "123"
	previewHolder     = "TEST PREVIEW"
)

// previewCard 返回试运行用的测试卡：有效期取三年后的 12 月，页面的前端校验不会因为过期拦下。
func previewCard(now time.Time) paybrowser.Card {
	return paybrowser.Card{
		Number:   previewCardNumber,
		ExpMonth: "12",
		ExpYear:  fmt.Sprintf("%02d", (now.Year()+3)%100),
		CVC:      previewCVC,
	}
}

// previewFill 是试运行到填表的流程：读完金额之后用测试卡填表，把结果写进时间线，以 previewed 结束。
func (s *Service) previewFill(ctx context.Context, j *job) error {
	if err := s.advance(ctx, j, StateFilling, "试运行：用公开测试卡填写支付表单，不点支付", nil); err != nil {
		return err
	}
	s.notePage(ctx, j, j.browser, "info", "试运行页面") // 第一次跑真实页面时，靠这几行认出页面类型

	card := previewCard(s.now())
	j.noteCardSecrets(card.Number, card.CVC)
	bill := TaxFreeAddress(rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())))
	bill.Name = previewHolder
	bill.Email = j.o.AccountEmail
	j.note("billing_state", bill.State)

	fctx, cancel := context.WithTimeout(ctx, s.t.Fill)
	defer cancel()
	payButton, err := fillOnly(fctx, j.browser, card, bill)
	s.noteFillReport(ctx, j, j.browser, "试运行填表")
	if err != nil {
		s.notePageDiag(ctx, j, j.browser, "试运行填表失败")
		return abort("dry_run_fill_failed", "试运行填表失败：%v", err)
	}
	return s.end(ctx, j, StatePreviewed, "info",
		fmt.Sprintf("试运行到填表完成，应付 %s；支付按钮：%s，未点击", j.o.AmountText, payButton), nil)
}

// fillOnly 填表并读支付按钮的状态。参数故意只有 FormBrowser：这里点不了支付，也碰不到卡池。
func fillOnly(ctx context.Context, b FormBrowser, card paybrowser.Card, bill paybrowser.Billing) (payButton string, err error) {
	if err := b.Fill(ctx, card, bill); err != nil {
		return "", err
	}
	return b.PayButtonState(ctx), nil
}
