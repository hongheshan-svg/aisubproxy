package payengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

// Codex 点数单（设计 2026-11-03 §5）：在开通单的状态机上改四处。
//   - checking：账号要有生效订阅（Plus 或 Pro 各档）、个人账号、没有欠费、计费币种等于码的地区币种；任一不满足就放弃，不花钱。
//     同时记下已存卡快照与点数余额快照。
//   - link_created：POST /payments/checkout 带 credit_purchase_data；只接受托管支付页（链接再过支付域名白名单），自定义结账页放弃。
//   - filling：照开通单填表，另外确认「保存付款信息」没有勾选，确认不了就放弃（已注资的卡照常回收）。
//   - verifying：不看 plan_type，看页面结果、该卡的成功授权与金额、点数余额；证据冲突转人工。成功后重读已存卡，看我们的卡有没有被存下。
// 点数单不关闭续费；熔断、重兑、在途互斥、金额上限与开通单完全一致。

// 成功后核对「我们的卡有没有被存进用户账号」的结果（card_saved 列）。空表示不适用（开通单、没成功的单）。
const (
	CardSavedNo      = "not_saved" // 没有
	CardSavedYes     = "saved"     // 存进去了：已告警，本轮不自动删除
	CardSavedUnknown = "unknown"   // 读不到已存卡，无法判断
)

// checkCredit 是点数单的账号检查。任一条件不满足就放弃：这时还没建结账会话、没取卡、没注资。
func (s *Service) checkCredit(ctx context.Context, j *job, acc chatgpt.Account) error {
	if f := creditAccountEligibility(acc); f != nil {
		return f
	}
	delinquent := acc.IsDelinquent
	if delinquent == nil {
		info, err := j.gpt.SubscriptionStatus(ctx, j.sess, acc.AccountID)
		if errors.Is(err, chatgpt.ErrBlocked) || errors.Is(err, chatgpt.ErrUnauthorized) {
			return s.chatgptAbort(j, "credit_delinquency_unknown", "查询订阅状态失败", err)
		}
		if err != nil {
			return abort("credit_delinquency_unknown", "查询订阅状态失败，无法确认账号没有欠费：%v", err)
		}
		delinquent = info.IsDelinquent
	}
	if f := creditBillingEligibility(acc, delinquent, j.o.Currency); f != nil {
		return f
	}
	return nil
}

// snapshotCredit 记下付款前的已存卡快照和点数余额快照，随下一次状态写入落库。都是只读调用，取不到只记事件、不放弃：
// 已存卡快照取不到时，成功后只能按尾号判断；余额取不到时，确认阶段不做余额核对（设计 §5.2「余额接口可用时」）。
func (s *Service) snapshotCredit(ctx context.Context, j *job) {
	cards, err := j.gpt.SavedCards(ctx, j.sess, j.o.AccountID)
	if err != nil {
		s.event(ctx, j, "warn", "读取已存卡失败，成功后只能按尾号判断我们的卡有没有被存下："+j.scrub(err.Error()), "")
	} else {
		snap := encodeSavedCards(cards)
		j.note("saved_cards_before", snap)
		j.o.SavedCardsBefore = snap
		s.event(ctx, j, "info", fmt.Sprintf("已记下付款前的已存卡快照：%d 张", len(cards)), "")
	}
	bal, err := j.gpt.CreditBalance(ctx, j.sess, j.o.AccountID)
	switch {
	case err != nil:
		s.event(ctx, j, "warn", "读取点数余额失败，确认阶段不做余额核对："+j.scrub(err.Error()), "")
	case !bal.Available:
		s.event(ctx, j, "info", "点数余额接口不可用（账号没有点数余额或为无限额度），确认阶段不做余额核对", "")
	default:
		j.note("credit_balance_before", bal.Balance)
		j.o.CreditBalanceBefore = bal.Balance
		s.event(ctx, j, "info", "已记下付款前的点数余额："+bal.Balance, "")
	}
}

// savedCardSnap 是已存卡快照里的一张卡：只存 id 的哈希前缀和尾号，不存 id 本身。
type savedCardSnap struct {
	IDHash string `json:"id_hash"`
	Last4  string `json:"last4"`
}

func savedCardHash(id string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(id)))
	return hex.EncodeToString(sum[:8])
}

func encodeSavedCards(cards []chatgpt.SavedCard) string {
	out := make([]savedCardSnap, 0, len(cards))
	for _, c := range cards {
		out = append(out, savedCardSnap{IDHash: savedCardHash(c.ID), Last4: c.Last4})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// decodeSavedCards 解出快照里各卡 id 的哈希；没有快照或快照损坏时 ok 为 false。
func decodeSavedCards(raw string) (hashes map[string]bool, ok bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	var list []savedCardSnap
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, false
	}
	hashes = make(map[string]bool, len(list))
	for _, c := range list {
		hashes[c.IDHash] = true
	}
	return hashes, true
}

// createCreditLink 是点数单的步骤 3：建点数结账会话。只接受托管支付页：返回自定义结账页（custom_checkout_session）或未知类型时放弃，
// 这时还没取卡、没注资。链接照样过支付域名白名单（acceptCheckoutURL）。
func (s *Service) createCreditLink(ctx context.Context, j *job) error {
	if j.o.CreditQuantity <= 0 {
		return abort("plan_unavailable", "点数档 %s 没有点数", j.o.PlanKey)
	}
	res, err := j.gpt.CreateCreditCheckout(ctx, j.sess, j.o.AccountID, j.o.CreditQuantity)
	if err != nil {
		return s.chatgptAbort(j, "checkout_failed", "创建点数支付会话失败", err)
	}
	if res.SessionID != "" {
		j.note("checkout_session_id", res.SessionID)
	}
	switch res.Tag {
	case chatgpt.CheckoutTagHosted, "":
	case chatgpt.CheckoutTagCustom:
		return abort("custom_checkout_unsupported", "点数结账返回的是自定义结账页（custom_checkout_session），引擎只支持跳转到托管支付页，已放弃，未注资")
	default:
		return abort("custom_checkout_unsupported", "点数结账返回了未知的结账页类型 %q，已放弃，未注资", truncate(res.Tag, 40))
	}
	return s.acceptCheckoutURL(ctx, j, res.SessionID, res.URL)
}

// noSavePayment 确认支付页上「保存付款信息」没有勾选（点数单和内嵌结账页要确认，见 needsNoSaveCheck）。确认不了返回错误，调用方放弃，不点支付。
func noSavePayment(ctx context.Context, b FormBrowser) error {
	if _, err := b.EnsureNoSavePayment(ctx); err != nil {
		return err
	}
	return nil
}

// creditEvidence 是点数单一次确认拿到的证据。
type creditEvidence struct {
	approved, pending bool   // 授权窗口内该卡有成功授权、有未定论的授权
	amountOK          bool   // 有一笔成功授权的金额与支付页金额在容差内
	authMatched       string // 相符的那笔授权的金额与币种（如 "-565.00 PHP"），写进成功依据
	conflict          string // 成功授权的币种或金额与支付页对不上（非空即转人工）
	authErr           error

	balanceNeeded bool // 有付款前的余额快照：成功还要求余额增加了点数
	credited      bool // 余额比快照增加了不少于 credit_quantity
	delta         string
	balErr        error
}

// verifyCredit 确认一张点数单。不看 plan_type，按设计 §5.2 依次判断：
//   - 成功授权的币种或金额与支付页对不上：证据冲突，立即转人工；
//   - 成功：页面结果为成功，并且该卡有金额相符的成功授权；有余额快照时还要求余额增加了点数；
//   - 也算成功（页面认不出）：页面结果是 unknown（不是被拒、校验错误），该卡有金额相符的成功授权，
//     并且余额快照可读、这次也读到了余额、余额 ≥ 快照 + 点数。真实 Stripe 页成功后多半直接跳走，页面认不出成功；
//     这两条都是付款之外独立的证据，同时满足才放宽。快照没取到、这次读不到余额、余额增加不足都不放宽；
//   - 期限未到：下次再查；
//   - 页面显示被拒或校验错误：余额已增加算证据冲突，其余照开通单的被拒流程定论（余额当作账号证据）；
//   - 其余到期：转人工，说明里列出各项证据。
func (s *Service) verifyCredit(ctx context.Context, j *job) {
	o := j.o
	gpt, clientErr := s.verifyClient(ctx, j)
	if ctx.Err() != nil {
		return
	}
	ev := s.creditEvidence(ctx, j, gpt, clientErr)
	if ctx.Err() != nil {
		return
	}
	s.noteVerifyErr(ctx, j, "auth", "查询授权交易失败，等下次检查：", ev.authErr)
	s.noteVerifyErr(ctx, j, "balance", "查询点数余额失败，等下次检查：", ev.balErr)
	now := s.now().Unix()
	var until int64
	if o.VerifyUntil != nil {
		until = *o.VerifyUntil
	}
	pageOK := o.PageOutcome == string(paybrowser.OutcomeSuccess)
	balanceOK := !ev.balanceNeeded || (ev.balErr == nil && ev.credited)
	switch {
	case ev.authErr == nil && ev.conflict != "":
		s.verifyEscalate(ctx, j, "evidence_conflict", "证据冲突："+ev.conflict)
	case pageOK && ev.authErr == nil && ev.amountOK && balanceOK:
		s.creditSucceeded(ctx, j, gpt, clientErr, ev)
	case creditUnknownPageProven(o, ev):
		s.creditSucceeded(ctx, j, gpt, clientErr, ev)
	case now < until:
		s.verifyReschedule(ctx, j)
	case isDeclinedOutcome(o.PageOutcome):
		if ev.balErr == nil && ev.credited {
			s.verifyEscalate(ctx, j, "evidence_conflict", "证据冲突："+pageFailText(o.PageOutcome)+"，但点数余额增加了 "+ev.delta)
			return
		}
		s.verifyDeclined(ctx, j, ev.balErr, now >= until+declineExtra(s.t, o.PageOutcome))
	default:
		code := "verify_timeout"
		if pageOK {
			code = "evidence_conflict" // 页面说成功，证据到期还不全
		}
		s.verifyEscalate(ctx, j, code, "确认超时："+creditEvidenceText(o, ev)+"。请人工核对后定论")
	}
}

// creditUnknownPageProven 是页面认不出结果时的成功条件：只对点数单、只对 unknown（被拒、校验错误、3DS 等都不算），
// 授权相符且余额按快照增加了点数，两条都要有；授权冲突在调用前已经转人工。
func creditUnknownPageProven(o *Order, ev creditEvidence) bool {
	return o.Kind == KindCredit && o.PageOutcome == string(paybrowser.OutcomeUnknown) &&
		ev.authErr == nil && ev.amountOK &&
		ev.balanceNeeded && ev.balErr == nil && ev.credited
}

// creditEvidenceText 把各项证据写成一句话，转人工时给管理员看。
func creditEvidenceText(o *Order, ev creditEvidence) string {
	parts := []string{}
	if o.PageOutcome == string(paybrowser.OutcomeSuccess) {
		parts = append(parts, "支付页显示成功")
	} else {
		parts = append(parts, "支付页没有显示成功")
	}
	switch {
	case ev.authErr != nil:
		parts = append(parts, "授权交易查不到")
	case ev.amountOK:
		parts = append(parts, "该卡有金额相符的成功授权")
	case ev.pending:
		parts = append(parts, "该卡的授权还在处理中")
	default:
		parts = append(parts, "该卡没有成功授权")
	}
	switch {
	case !ev.balanceNeeded:
		parts = append(parts, "没有余额快照，未核对点数余额")
	case ev.balErr != nil:
		parts = append(parts, "点数余额查不到")
	case ev.credited:
		parts = append(parts, "点数余额增加了 "+ev.delta)
	default:
		parts = append(parts, "点数余额没有增加 "+fmt.Sprint(o.CreditQuantity)+"（变化 "+ev.delta+"）")
	}
	return strings.Join(parts, "；")
}

// creditEvidence 查授权交易与点数余额。gpt 为 nil 时 clientErr 是建客户端失败的原因，只影响余额这一项。
func (s *Service) creditEvidence(ctx context.Context, j *job, gpt ChatGPT, clientErr error) creditEvidence {
	var ev creditEvidence
	ev.approved, ev.pending, ev.amountOK, ev.authMatched, ev.conflict, ev.authErr = s.creditAuth(ctx, j.pool, j.o)
	if strings.TrimSpace(j.o.CreditBalanceBefore) == "" {
		return ev
	}
	ev.balanceNeeded = true
	if gpt == nil {
		ev.balErr = clientErr
		return ev
	}
	bal, err := gpt.CreditBalance(ctx, j.sess, j.o.AccountID)
	switch {
	case err != nil:
		ev.balErr = err
	case !bal.Available:
		ev.balErr = errors.New("点数余额接口这次没有返回余额")
	default:
		delta, err := balanceDelta(j.o.CreditBalanceBefore, bal.Balance)
		if err != nil {
			ev.balErr = err
			break
		}
		ev.delta = delta.FloatString(2)
		ev.credited = delta.Cmp(new(big.Rat).SetInt64(int64(j.o.CreditQuantity))) >= 0
	}
	return ev
}

// creditAuth 翻完该卡在授权窗口内的交易（口径同 verifyAuth：只看 AUTH 与 SETTLE，approved 算成功，failed 不算，其余都算未定论），
// 另外核对每一笔成功授权的币种与金额：按支付页币种取交易金额（交易币种对不上时取账单币种），与支付页金额的差在容差内才算相符。
// 任何一笔成功授权对不上都记 conflict：钱的去向和页面说的不一致，只能人工看。matched 是第一笔相符授权的金额与币种。
func (s *Service) creditAuth(ctx context.Context, pool Pool, o *Order) (approved, pending, amountOK bool, matched, conflict string, err error) {
	if o.CardID == "" {
		return false, false, false, "", "", errors.New("单据没有记录卡")
	}
	var since int64
	if o.SubmittedAt != nil {
		since = authWindowStart(*o.SubmittedAt)
	}
	list, err := s.txnsSince(ctx, pool, o.CardID, since)
	if err != nil {
		return false, false, false, "", "", err
	}
	for _, t := range list {
		if !strings.EqualFold(t.Kind, "AUTH") && !strings.EqualFold(t.Kind, "SETTLE") {
			continue
		}
		switch {
		case strings.EqualFold(t.Status, "approved"):
			approved = true
			if why := txnAmountMismatch(t, o); why != "" {
				if conflict == "" {
					conflict = why
				}
			} else if !amountOK {
				amountOK = true
				matched = txnAmountText(t, o)
			}
		case !strings.EqualFold(t.Status, "failed"):
			pending = true
		}
	}
	return approved, pending, amountOK, matched, conflict, nil
}

// txnAmountText 是一笔授权按支付页币种取的金额与币种（取法同 txnAmountMismatch），写进成功依据。
func txnAmountText(t cardpool.Transaction, o *Order) string {
	amount, cur := t.TxnAmount, strings.TrimSpace(t.TxnCurrency)
	if !strings.EqualFold(cur, o.Currency) {
		amount, cur = t.BillingAmount, strings.TrimSpace(t.BillingCurrency)
	}
	return strings.TrimSpace(amount) + " " + strings.ToUpper(cur)
}

// txnAmountMismatch 核对一笔成功授权的币种与金额；相符返回空串。容差为支付页金额的 1%，至少 1 个最小单位。
func txnAmountMismatch(t cardpool.Transaction, o *Order) string {
	amount, cur := t.TxnAmount, strings.TrimSpace(t.TxnCurrency)
	if !strings.EqualFold(cur, o.Currency) {
		amount, cur = t.BillingAmount, strings.TrimSpace(t.BillingCurrency)
	}
	if !strings.EqualFold(cur, o.Currency) {
		return fmt.Sprintf("该卡的成功授权币种（交易 %s、账单 %s）与支付页币种 %s 不符", t.TxnCurrency, t.BillingCurrency, o.Currency)
	}
	if o.AmountMinor <= 0 {
		return "单据没有记录支付页金额，无法核对授权金额"
	}
	minor, ok := decimalMinor(amount, o.Currency)
	if !ok {
		return fmt.Sprintf("该卡的成功授权金额 %q 读不出", truncate(amount, 40))
	}
	diff := minor - o.AmountMinor
	if diff < 0 {
		diff = -diff
	}
	tol := o.AmountMinor / 100
	if tol < 1 {
		tol = 1
	}
	if diff > tol {
		return fmt.Sprintf("该卡的成功授权金额 %s %s 与支付页金额 %s 不符", strings.TrimSpace(amount), o.Currency, o.AmountText)
	}
	return ""
}

var decimalRe = regexp.MustCompile(`^[+-]?\d+(\.\d+)?$`)

// decimalMinor 把交易金额（如 "-565.00"）换成该币种的最小单位，取绝对值、四舍五入；格式不对返回 false。
func decimalMinor(amount, currency string) (int64, bool) {
	r, ok := parseDecimal(amount)
	if !ok {
		return 0, false
	}
	r.Abs(r)
	r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(paybrowser.CurrencyExponent(currency))), nil)))
	// 四舍五入：floor((2n + d) / 2d)，r 已非负
	num := new(big.Int).Mul(r.Num(), big.NewInt(2))
	num.Add(num, r.Denom())
	den := new(big.Int).Mul(r.Denom(), big.NewInt(2))
	v := new(big.Int).Quo(num, den)
	if !v.IsInt64() {
		return 0, false
	}
	return v.Int64(), true
}

func parseDecimal(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if !decimalRe.MatchString(s) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(strings.TrimPrefix(s, "+"))
	return r, ok
}

// balanceDelta 返回点数余额的变化（after − before）。余额是字符串（前端包里是 string），格式不对就报错，不猜。
func balanceDelta(before, after string) (*big.Rat, error) {
	b, ok := parseDecimal(before)
	if !ok {
		return nil, fmt.Errorf("付款前的点数余额 %q 读不出", truncate(before, 40))
	}
	a, ok := parseDecimal(after)
	if !ok {
		return nil, fmt.Errorf("点数余额 %q 读不出", truncate(after, 40))
	}
	return new(big.Rat).Sub(a, b), nil
}

// creditSucceeded 写成功定论。定论之前重读一次已存卡：出现了我们这张卡就标 saved、发告警（本轮不自动删除，调研 T5 确认之前不动用户账单设置）。
// 读不到已存卡不影响成功，只标 unknown。
func (s *Service) creditSucceeded(ctx context.Context, j *job, gpt ChatGPT, clientErr error, ev creditEvidence) {
	saved, note := s.checkSavedCards(ctx, j, gpt, clientErr)
	msg := "点数付款成功：支付页显示成功，该卡有金额相符的成功授权"
	if ev.balanceNeeded {
		msg += "，点数余额增加了 " + ev.delta
	}
	if j.o.PageOutcome != string(paybrowser.OutcomeSuccess) {
		// 页面认不出时靠授权与余额两条独立证据定的成功，依据写清楚，供日后核对
		msg = fmt.Sprintf("点数付款成功（页面未识别；授权 %s 相符；余额 +%s，快照 %s）", ev.authMatched, ev.delta, j.o.CreditBalanceBefore)
	}
	set := map[string]any{"error_code": "", "error_message": "", "card_saved": saved}
	if !s.verifyConclude(ctx, j, StateSucceeded, "info", msg, cardpool.Outcome{Result: cardpool.ResultSuccess}, set) {
		return
	}
	switch saved {
	case CardSavedYes:
		s.event(ctx, j, "error", note, "")
		s.opts.Alert(fmt.Sprintf("付款单 %s（点数）%s", j.o.OrderNo, note))
	case CardSavedUnknown:
		s.event(ctx, j, "warn", note, "")
	default:
		s.event(ctx, j, "info", note, "")
	}
}

// checkSavedCards 成功后重读已存卡，判断我们的卡有没有被存进用户账号。有付款前快照时只看新出现的卡；没有快照时按尾号判断（宁可多报）。
func (s *Service) checkSavedCards(ctx context.Context, j *job, gpt ChatGPT, clientErr error) (string, string) {
	if j.o.CardLast4 == "" {
		return CardSavedUnknown, "单据没有记录卡的尾号，无法核对我们的卡有没有被存进用户账号"
	}
	if gpt == nil {
		return CardSavedUnknown, "成功后读取已存卡失败，无法核对我们的卡有没有被存进用户账号：" + j.scrub(errText(clientErr))
	}
	cards, err := gpt.SavedCards(ctx, j.sess, j.o.AccountID)
	if err != nil {
		return CardSavedUnknown, "成功后读取已存卡失败，无法核对我们的卡有没有被存进用户账号：" + j.scrub(err.Error())
	}
	before, ok := decodeSavedCards(j.o.SavedCardsBefore)
	for _, c := range cards {
		if strings.TrimSpace(c.Last4) != j.o.CardLast4 || (ok && before[savedCardHash(c.ID)]) {
			continue
		}
		how := ""
		if !ok {
			how = "（付款前没有已存卡快照，按尾号判断）"
		}
		return CardSavedYes, fmt.Sprintf("我们的卡（尾号 %s）被保存到了用户账号%s：本系统不自动删除，请联系用户在 ChatGPT 设置里删除这张卡", j.o.CardLast4, how)
	}
	return CardSavedNo, "已核对：我们的卡没有被存进用户账号"
}

func errText(err error) string {
	if err == nil {
		return "原因未知"
	}
	return err.Error()
}
