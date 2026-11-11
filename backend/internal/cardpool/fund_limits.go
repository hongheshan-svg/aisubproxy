package cardpool

import (
	"context"
	"fmt"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// centsText 把分格式化成金额文本，只用于提示和告警。
func centsText(cents int64) string {
	s, err := marswallex.FormatAmount(cents)
	if err != nil {
		return fmt.Sprintf("%d 分", cents)
	}
	return s
}

// limitAlert 发超限告警。系统自动发起的（付款单、自动补卡）同一 key 每天一条，免得刷屏；人工操作每次都发。
func (s *Service) limitAlert(by, key, text string) {
	if !strings.HasPrefix(by, "lease:") && by != "replenish" {
		key = ""
	}
	s.alert(key, text)
}

// checkFundLimit 读取策略并检查单笔注资上限（平台还没收到任何请求）。策略读不出来就拒绝：不能在不知道上限时放钱出去。
func (s *Service) checkFundLimit(what string, cents int64, by string) (Policy, error) {
	pol, err := LoadPolicy()
	if err != nil {
		return pol, fmt.Errorf("读取卡池策略失败，已拒绝%s: %w", what, err)
	}
	if cents > pol.MaxFundCents {
		msg := fmt.Sprintf("%s %s 超过单笔上限 %s", what, centsText(cents), centsText(pol.MaxFundCents))
		s.limitAlert(by, "fund-single-limit", fmt.Sprintf("%s，已拒绝，未发往平台（操作方 %s）", msg, by))
		return pol, badRequest(msg)
	}
	return pol, nil
}

// checkWalletLimit 检查钱包转账、兑换的单笔上限。要在同步账户之前调用，超限时平台收不到任何请求。
func (s *Service) checkWalletLimit(what string, cents int64, by string) error {
	pol, err := LoadPolicy()
	if err != nil {
		return fmt.Errorf("读取卡池策略失败，已拒绝%s: %w", what, err)
	}
	if cents > pol.MaxWalletOpCents {
		msg := fmt.Sprintf("%s %s 超过单笔上限 %s", what, centsText(cents), centsText(pol.MaxWalletOpCents))
		s.alert("", fmt.Sprintf("%s，已拒绝，未发往平台（操作方 %s）", msg, by))
		return badRequest(msg)
	}
	return nil
}

// insertPendingFund 写入一张会把钱转到卡上的 pending 单据（转入、带金额的开卡），同一条语句里判断当日累计金额和笔数：
// SQLite 的写语句开始执行就持有写锁，统计和写入之间插不进别的写入，并发绕不过上限。
//
// 累计按北京日统计。明确没动钱的单（failed、not_sent）不算；结果不明的（unknown 等）算：钱可能已经转了。
// 笔数只数带金额的单：不带金额的开卡（insertPending 写入，不受这里检查）不往卡上放钱，不占笔数。
func (s *Service) insertPendingFund(ctx context.Context, o *Order, pol Policy) error {
	now := s.now()
	o.OutOrderNo = newOutOrderNo(o.Kind, now)
	o.State = StatePending
	o.CreatedAt, o.UpdatedAt = now.Unix(), now.Unix()
	day := beijingDayStart(now)
	from, to := day.Unix(), day.AddDate(0, 0, 1).Unix()
	args := append(o.insertArgs(), KindRecharge, KindApply, from, to, StateFailed, StateNotSent, o.AmountCents, pol.DailyFundCapCents,
		KindRecharge, KindApply, from, to, StateFailed, StateNotSent, pol.DailyFundCountCap)
	res, err := db.DB.ExecContext(ctx, `INSERT INTO mw_card_orders (`+orderInsertCols+`)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE (SELECT COALESCE(SUM(amount_cents), 0) FROM mw_card_orders
			WHERE kind IN (?, ?) AND created_at >= ? AND created_at < ? AND state NOT IN (?, ?)) + ? <= ?
		AND (SELECT COUNT(*) FROM mw_card_orders
			WHERE kind IN (?, ?) AND created_at >= ? AND created_at < ? AND state NOT IN (?, ?) AND amount_cents > 0) + 1 <= ?`, args...)
	if err != nil {
		return insertOrderErr(o, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// 被挡下后再数一次今天的笔数，只用来决定提示写哪个上限；拒绝本身已经在上面的语句里原子地定了
		if used, err := s.dailyFundCount(ctx, now); err == nil && used+1 > pol.DailyFundCountCap {
			msg := fmt.Sprintf("今日注资已达 %d 笔上限", pol.DailyFundCountCap)
			s.limitAlert(o.RequestedBy, "fund-daily-count", fmt.Sprintf("%s，已拒绝，未发往平台（操作方 %s）", msg, o.RequestedBy))
			return badRequest(msg)
		}
		msg := fmt.Sprintf("今日注资累计将超过上限 %s（本笔 %s）", centsText(pol.DailyFundCapCents), centsText(o.AmountCents))
		s.limitAlert(o.RequestedBy, "fund-daily-cap", fmt.Sprintf("%s，已拒绝，未发往平台（操作方 %s）", msg, o.RequestedBy))
		return badRequest(msg)
	}
	o.ID, err = res.LastInsertId()
	return err
}
