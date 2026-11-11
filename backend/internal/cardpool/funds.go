package cardpool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

const transferRemarkMax = 200 // 钱包转账备注最多 200 字

// ConversionParams 为一笔兑换的参数（作用于主钱包）。SellCents 为卖出金额（分）。
type ConversionParams struct {
	SellCurrency, BuyCurrency, By string
	SellCents                     int64
}

// WalletTransferParams 为一笔主钱包与子账户之间转账的参数。AmountCents 为金额（分）。
type WalletTransferParams struct {
	PayerAccountNo, PayeeAccountNo, Currency, Remark, By string
	AmountCents                                          int64
}

// normCurrency 去空白转大写，要求 1–10 个字符。
func normCurrency(s string) (string, error) {
	c := strings.ToUpper(strings.TrimSpace(s))
	if n := utf8.RuneCountInString(c); n < 1 || n > 10 {
		return "", badRequest("币种需为 1–10 个字符")
	}
	return c, nil
}

// balanceRe 为平台余额字符串：非负、不带符号与千分位，小数位数不限。
var balanceRe = regexp.MustCompile(`^\d+(\.\d+)?$`)

// parseBalanceCents 把平台余额字符串向下取整到分。下单金额只到 2 位小数，余额可以更多位（零头留在账户里）。
// 空串、符号、千分位、非数字等报错；整数部分沿用 ParseAmount 的上限。
func parseBalanceCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if !balanceRe.MatchString(s) {
		return 0, fmt.Errorf("余额格式非法: %q", s)
	}
	intPart, frac, _ := strings.Cut(s, ".")
	if len(frac) > 2 {
		frac = frac[:2]
	}
	if frac != "" {
		intPart += "." + frac
	}
	return marswallex.ParseAmount(intPart)
}

// checkAvailable 检查账户该币种的可用余额（向下取整到分）不少于 cents。余额格式无法识别时拒绝。
func checkAvailable(a *Account, who, currency string, cents int64) error {
	for _, b := range a.Balances {
		if normStatus(b.Currency) != currency {
			continue
		}
		avail, err := parseBalanceCents(b.AvailableBalance)
		if err != nil {
			return badRequest(fmt.Sprintf("%s %s 的 %s 余额格式无法识别：%q", who, a.AccountNo, currency, b.AvailableBalance))
		}
		if avail < cents {
			amt, _ := marswallex.FormatAmount(cents)
			return badRequest(fmt.Sprintf("%s %s 的 %s 可用余额 %s，不足 %s", who, a.AccountNo, currency, b.AvailableBalance, amt))
		}
		return nil
	}
	return badRequest(fmt.Sprintf("%s %s 没有 %s 余额", who, a.AccountNo, currency))
}

// mainAccountFor 返回兑换要校验的本地未消失主钱包：只有一个时就是它；平台按币种各有一个主钱包时，
// 取持有 currency 的那个，多个都持有时取该币种可用余额最大的（余额格式无法识别的排在后面）；
// 都不持有时返回第一个，由 checkAvailable 报没有该币种余额。没有主钱包时返回 ErrNotFound。
func mainAccountFor(ctx context.Context, currency string) (*Account, error) {
	rows, err := db.DB.QueryContext(ctx, `SELECT `+accountCols+` FROM mw_accounts
		WHERE account_type = ? AND gone = 0 ORDER BY created_at, account_no`, accountTypeMain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var mains []Account
	for rows.Next() {
		var a Account
		if err := a.scan(rows); err != nil {
			return nil, err
		}
		mains = append(mains, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(mains) == 0 {
		return nil, ErrNotFound
	}
	best, bestCents := 0, int64(-2) // -2：不持有；-1：持有但余额格式无法识别
	for i, a := range mains {
		for _, b := range a.Balances {
			if normStatus(b.Currency) != currency {
				continue
			}
			cents, err := parseBalanceCents(b.AvailableBalance)
			if err != nil {
				cents = -1
			}
			if cents > bestCents {
				best, bestCents = i, cents
			}
			break
		}
	}
	return &mains[best], nil
}

// SubmitConversion 提交一笔兑换（卖出 SellCurrency 买入 BuyCurrency，作用于主钱包）。
// 校验参数后先同步账户（失败返回其错误），再查主钱包余额；全部通过才写 pending 单据并发送。
// 已有在途兑换单时返回 ErrInflight。兑换单从不重发，结果未知时只查单。
func (s *Service) SubmitConversion(ctx context.Context, p ConversionParams) (*Order, error) {
	if p.SellCents <= 0 {
		return nil, badRequest("金额必须大于 0")
	}
	sell, err := normCurrency(p.SellCurrency)
	if err != nil {
		return nil, err
	}
	buy, err := normCurrency(p.BuyCurrency)
	if err != nil {
		return nil, err
	}
	if sell == buy {
		return nil, badRequest("卖出与买入币种不能相同")
	}
	amount, err := marswallex.FormatAmount(p.SellCents)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	if err := s.checkWalletLimit("兑换", p.SellCents, p.By); err != nil {
		return nil, err
	}
	if err := s.SyncAccounts(ctx); err != nil {
		return nil, err
	}
	mainAcct, err := mainAccountFor(ctx, sell)
	if errors.Is(err, ErrNotFound) {
		return nil, badRequest("没有主钱包")
	}
	if err != nil {
		return nil, err
	}
	if err := checkAvailable(mainAcct, "主钱包", sell, p.SellCents); err != nil {
		return nil, err
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	o := &Order{Kind: KindConversion, AmountCents: p.SellCents, Currency: sell, BuyCurrency: buy, RequestedBy: p.By}
	if err := s.insertPending(ctx, o); err != nil {
		return nil, err
	}
	if err := s.abortIfCanceled(ctx, o.OutOrderNo); err != nil {
		return nil, err
	}
	res, err := c.CreateConversion(ctx, marswallex.ConversionRequest{
		OutOrderNo: o.OutOrderNo, SellCurrency: sell, SellAmount: amount, BuyCurrency: buy,
	})
	var v sendVerdict
	if err != nil {
		v = classifySendErr(err, false)
	} else {
		// 平台不返回状态：受理即处理中
		v = sendVerdict{state: StateProcessing, txnID: res.TransactionID}
	}
	return s.recordSent(context.WithoutCancel(ctx), o.OutOrderNo, v)
}

// transferAccount 读取转账一方的本地账户：不存在或已从平台消失时拒绝。
func transferAccount(ctx context.Context, who, no string) (*Account, error) {
	a, err := getAccount(ctx, no)
	if errors.Is(err, ErrNotFound) {
		return nil, badRequest(fmt.Sprintf("%s %s 不存在", who, no))
	}
	if err != nil {
		return nil, err
	}
	if a.Gone {
		return nil, badRequest(fmt.Sprintf("%s %s 已不在平台账户列表中", who, no))
	}
	return a, nil
}

// SubmitWalletTransfer 提交一笔主钱包与子账户之间的转账（一方 MAIN、一方 SUB_ACCOUNT）。
// 校验参数后先同步账户（失败返回其错误），再查双方账户与付款方余额；全部通过才写 pending 单据并发送。
// 已有在途钱包转账单时返回 ErrInflight。转账单从不重发，结果未知时只查单。
func (s *Service) SubmitWalletTransfer(ctx context.Context, p WalletTransferParams) (*Order, error) {
	if p.AmountCents <= 0 {
		return nil, badRequest("金额必须大于 0")
	}
	cur, err := normCurrency(p.Currency)
	if err != nil {
		return nil, err
	}
	payerNo, payeeNo := strings.TrimSpace(p.PayerAccountNo), strings.TrimSpace(p.PayeeAccountNo)
	if payerNo == "" || payeeNo == "" {
		return nil, badRequest("付款方与收款方账户不能为空")
	}
	if payerNo == payeeNo {
		return nil, badRequest("付款方与收款方不能是同一账户")
	}
	remark := strings.TrimSpace(p.Remark)
	if utf8.RuneCountInString(remark) > transferRemarkMax {
		return nil, badRequest(fmt.Sprintf("转账备注最多 %d 字", transferRemarkMax))
	}
	amount, err := marswallex.FormatAmount(p.AmountCents)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	if err := s.checkWalletLimit("钱包转账", p.AmountCents, p.By); err != nil {
		return nil, err
	}
	if err := s.SyncAccounts(ctx); err != nil {
		return nil, err
	}
	payer, err := transferAccount(ctx, "付款账户", payerNo)
	if err != nil {
		return nil, err
	}
	payee, err := transferAccount(ctx, "收款账户", payeeNo)
	if err != nil {
		return nil, err
	}
	payerType, payeeType := normStatus(payer.AccountType), normStatus(payee.AccountType)
	sub := payee
	switch {
	case payerType == accountTypeMain && payeeType == accountTypeSub:
	case payerType == accountTypeSub && payeeType == accountTypeMain:
		sub = payer
	default:
		return nil, badRequest("只能在主钱包与子账户之间转账")
	}
	if st := normStatus(sub.Status); st != accountActive {
		return nil, badRequest(fmt.Sprintf("子账户 %s 未启用（状态 %s）", sub.AccountNo, sub.Status))
	}
	if err := checkAvailable(payer, "付款账户", cur, p.AmountCents); err != nil {
		return nil, err
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	o := &Order{Kind: KindWalletTransfer, AmountCents: p.AmountCents, Currency: cur, PayerAccountNo: payerNo,
		PayeeAccountNo: payeeNo, Note: remark, RequestedBy: p.By}
	if err := s.insertPending(ctx, o); err != nil {
		return nil, err
	}
	if err := s.abortIfCanceled(ctx, o.OutOrderNo); err != nil {
		return nil, err
	}
	res, err := c.CreateTransfer(ctx, marswallex.TransferRequest{
		OutOrderNo: o.OutOrderNo, PayerAccountNo: payerNo, PayeeAccountNo: payeeNo,
		Amount: amount, Currency: cur, Remark: remark,
	})
	var v sendVerdict
	if err != nil {
		v = classifySendErr(err, false)
	} else {
		v = walletTransferVerdict(res)
	}
	return s.recordSent(context.WithoutCancel(ctx), o.OutOrderNo, v)
}

// walletTransferVerdict 为钱包转账成功响应的初判：PROCESSING / SUCCESS / FAIL 之外按未知处理；
// 平台单号写入 detail。
func walletTransferVerdict(t *marswallex.TransferResult) sendVerdict {
	v := sendVerdict{detail: transferDetail(t.OrderNo)}
	switch normStatus(t.Status) {
	case "PROCESSING":
		v.state = StateProcessing
	case "SUCCESS":
		v.state = StateSuccess
	case "FAIL":
		v.state, v.lastErr = StateFailed, "平台返回 FAIL"
	default:
		v.state, v.lastErr = StateUnknown, fmt.Sprintf("平台返回未知的转账状态 %q", t.Status)
	}
	return v
}

// transferDetail 返回钱包转账单的 detail（平台单号）；单号为空时返回空串，不覆盖已有的 detail。
func transferDetail(orderNo string) string {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return ""
	}
	b, _ := json.Marshal(struct {
		OrderNo string `json:"order_no"`
	}{orderNo})
	return string(b)
}

// conversionDeal 为写入兑换单 detail 的成交结果，只保留平台给出的非空字段。
type conversionDeal struct {
	BuyAmount    string `json:"buy_amount,omitempty"`
	BuyCurrency  string `json:"buy_currency,omitempty"`
	Rate         string `json:"rate,omitempty"`
	FeeAmount    string `json:"fee_amount,omitempty"`
	FeeCurrency  string `json:"fee_currency,omitempty"`
	BaseCurrency string `json:"base_currency,omitempty"`
}

// conversionDetail 返回兑换单的成交结果 JSON（按平台原字符串）；全部为空时返回空串。
func conversionDetail(d *marswallex.ConversionDetail) string {
	deal := conversionDeal{
		BuyAmount: d.BuyAmount, BuyCurrency: d.BuyCurrency, Rate: d.Rate,
		FeeAmount: d.FeeAmount, FeeCurrency: d.FeeCurrency, BaseCurrency: d.BaseCurrency,
	}
	if deal == (conversionDeal{}) {
		return ""
	}
	b, _ := json.Marshal(deal)
	return string(b)
}
