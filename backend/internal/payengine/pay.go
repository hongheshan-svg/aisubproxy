package payengine

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// pay 是读完金额之后的步骤 5–8：选卡注资、填表、记下提交时间后点支付、等页面结果（含 3DS），最后转确认阶段（步骤 9）。
//
// 只点一次支付的保证：Browser.Submit 的调用点只有 submitAndWait 这一处，只能从这里走到；
// 进入它之前必须把 filling → submitted 写入成功（条件更新，同一单只能成功一次），写失败就不点。
// 确认协程与重启恢复都不持有浏览器，走不到这里。
func (s *Service) pay(ctx context.Context, j *job) error {
	if err := s.fundCard(ctx, j); err != nil {
		return err
	}
	if err := s.fillCard(ctx, j); err != nil {
		return err
	}
	at := s.now().Unix()
	if err := s.advance(ctx, j, StateSubmitted, "已记下提交时间，点击支付", map[string]any{"submitted_at": at}); err != nil {
		return err
	}
	j.o.SubmittedAt = &at
	out, text, err := s.submitAndWait(ctx, j)
	if err != nil {
		return err
	}
	return s.beginVerify(ctx, j, out, text, pageOutcomeNote(out))
}

func pageOutcomeNote(out paybrowser.Outcome) string {
	switch out {
	case paybrowser.OutcomeSuccess:
		return "支付页显示成功，确认是否开通"
	case paybrowser.OutcomeDeclined:
		return "支付页显示被拒，等待授权推送后定论"
	}
	return "支付页没有明确结果，确认是否开通"
}

// fundCard 是步骤 5：选卡、记下租约、注资并等注资单定论。资金操作绝不自动重发：Fund 只调一次，结果不明就放弃，由卡池对账收敛。
func (s *Service) fundCard(ctx context.Context, j *job) error {
	if err := s.advance(ctx, j, StateFunding, "选卡注资", nil); err != nil {
		return err
	}
	l, err := s.opts.Pool.Acquire(ctx, cardpool.AcquireRequest{PlanKey: j.o.PlanKey, OrderRef: j.o.OrderNo, AccountEmail: j.o.AccountEmail})
	if errors.Is(err, cardpool.ErrNoCard) {
		return abort("no_card", "选卡失败：%v", err)
	}
	if err != nil {
		return abort("acquire_failed", "选卡失败：%v", err)
	}
	j.lease = l
	j.o.CardID, j.o.LeaseToken = l.CardID, l.Token
	j.note("card_id", l.CardID)
	j.note("lease_token", l.Token)

	// 姓名先于注资确认：缺姓名填不了表，没必要先把钱转进卡里
	info, err := s.opts.Pool.CardInfo(ctx, l.CardID)
	if err != nil {
		return abort("card_info_failed", "读取卡信息失败：%v", err)
	}
	j.holderName = strings.TrimSpace(info.HolderFirstName + " " + info.HolderLastName)
	if j.holderName == "" {
		return abort("card_info_failed", "该卡没有持卡人姓名，无法填写账单信息")
	}
	// 租约在注资之前落库，重启恢复才能按单据字段释放
	if err := s.advance(ctx, j, StateFunding, "已选卡，尾号 "+info.Last4, map[string]any{"card_last4": info.Last4}); err != nil {
		return err
	}

	fctx, cancel := context.WithTimeout(ctx, s.t.Fund)
	defer cancel()
	fo, err := s.opts.Pool.Fund(fctx, l, j.o.FundCents)
	if err != nil {
		return fundAbort(err)
	}
	if err := s.advance(ctx, j, StateFunding, "已提交注资，等待到账", map[string]any{"fund_order_no": fo.OutOrderNo}); err != nil {
		return err
	}
	done, err := s.opts.Pool.WaitOrder(fctx, fo.OutOrderNo)
	if err != nil {
		return fundAbort(err)
	}
	if done.State != cardpool.StateSuccess {
		return abort("fund_failed", "注资未成功（%s）：%s", done.State, done.LastError)
	}
	return nil
}

// fundAbort 把注资出错转成放弃原因：超时记 fund_timeout，其余记 fund_failed。两者都可能已经到账，由卡池回收转回。
func fundAbort(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return abort("fund_timeout", "注资超时，结果不明，已放弃")
	}
	return abort("fund_failed", "注资失败：%v", err)
}

// fillCard 是步骤 6：取卡号、填卡与免税州账单地址。卡号与 CVV 只留在内存，并立刻登记脱敏。
func (s *Service) fillCard(ctx context.Context, j *job) error {
	if err := s.advance(ctx, j, StateFilling, "注资成功，填写支付表单", nil); err != nil {
		return err
	}
	fctx, cancel := context.WithTimeout(ctx, s.t.Fill)
	defer cancel()
	sv, err := s.opts.Pool.Sensitive(fctx, j.lease)
	if err != nil {
		return abort("card_sensitive_failed", "取卡号失败：%v", err)
	}
	j.noteCardSecrets(sv.CardNo, sv.CVV2)
	mm, yy, err := parseExpiry(sv.ExpiryDate)
	if err != nil {
		return abort("card_expiry_invalid", "%v", err)
	}
	bill := TaxFreeAddress(rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())))
	bill.Name = j.holderName
	j.note("billing_state", bill.State)
	card := paybrowser.Card{Number: sv.CardNo, ExpMonth: mm, ExpYear: yy, CVC: sv.CVV2}
	if err := j.browser.Fill(fctx, card, bill); err != nil {
		return abort("fill_failed", "填写支付表单失败：%v", err)
	}
	return nil
}

// submitAndWait 是步骤 7–8：点支付（全程唯一一次）、等页面结果；出现 3DS 框时取验证码填入一次再等结果。
// 第二次出现验证框不再填：等验证码的时间已计入卡池租约预算，交给确认阶段定论。
func (s *Service) submitAndWait(ctx context.Context, j *job) (paybrowser.Outcome, string, error) {
	sctx, cancel := context.WithTimeout(ctx, s.t.SubmitResult)
	defer cancel()
	if err := j.browser.Submit(sctx); err != nil {
		return paybrowser.OutcomeUnknown, "", fmt.Errorf("点击支付时出错：%w", err)
	}
	out, text, err := s.waitOutcome(ctx, j)
	if err != nil {
		return paybrowser.OutcomeUnknown, "", err
	}
	if out != paybrowser.OutcomeChallenge {
		return out, text, nil
	}
	if err := s.answerChallenge(ctx, j); err != nil {
		return paybrowser.OutcomeUnknown, "", err
	}
	out, text, err = s.waitOutcome(ctx, j)
	if err != nil {
		return paybrowser.OutcomeUnknown, "", err
	}
	if out == paybrowser.OutcomeChallenge {
		s.event(ctx, j, "warn", "填写验证码后再次出现验证框，不再填写", "")
		return paybrowser.OutcomeUnknown, "", nil
	}
	return out, text, nil
}

// waitOutcome 等页面结果。外面再包一层时限，不依赖 Browser 实现是否守约：
// 这里挂住时单据停在 submitted、占着名额，没有告警，租约到期后卡被转出，而钱可能已经扣了。
func (s *Service) waitOutcome(ctx context.Context, j *job) (paybrowser.Outcome, string, error) {
	wctx, cancel := context.WithTimeout(ctx, s.t.SubmitResult+s.t.OutcomeMargin)
	defer cancel()
	out, text, err := j.browser.WaitOutcome(wctx, s.t.SubmitResult)
	if err != nil && wctx.Err() != nil && ctx.Err() == nil {
		return out, text, errors.New("等待页面结果超时")
	}
	return out, text, err
}

// answerChallenge 是步骤 8：截 3DS 验证框、等该卡在提交之后收到的验证码、填入并提交。
func (s *Service) answerChallenge(ctx context.Context, j *job) error {
	if err := s.advance(ctx, j, StateRequires3DS, "出现 3DS 验证框，等待验证码", nil); err != nil {
		return err
	}
	shotCtx, cancelShot := context.WithTimeout(ctx, s.t.PageLoad)
	s.shot(shotCtx, j, "challenge", paybrowser.ShotChallenge)
	cancelShot()

	octx, cancel := context.WithTimeout(ctx, s.t.OTP)
	defer cancel()
	otp, err := s.opts.Pool.WaitOTP(octx, j.o.CardID, time.Unix(*j.o.SubmittedAt, 0))
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("等待验证码超时")
	}
	if err != nil {
		return fmt.Errorf("等待验证码失败：%w", err)
	}
	j.secrets = append(j.secrets, otp)
	ectx, cancelEnter := context.WithTimeout(ctx, s.t.SubmitResult)
	defer cancelEnter()
	if err := j.browser.EnterOTP(ectx, otp); err != nil {
		return fmt.Errorf("填写验证码失败：%w", err)
	}
	s.event(ctx, j, "info", "已填写验证码", "")
	return nil
}
