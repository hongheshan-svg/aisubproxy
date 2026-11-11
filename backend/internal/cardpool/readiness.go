package cardpool

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 这个文件是给发码前的就绪检查用的只读查询，不改任何状态，也不调平台（SyncAccounts 由调用方决定要不要先做）。

// replenishAllowed 判断这一轮能不能自动补卡。只挡会主动花钱的补卡；对账、回收、Webhook、补拉不走这里。
// 没注入判定函数（测试、旧调用方）时放行；查询出错返回错误，这一轮不开卡。
func (s *Service) replenishAllowed(ctx context.Context, pol Policy) (bool, error) {
	if pol.ReplenishWhenUnselected || s.opts.MarsWallexSelected == nil {
		return true, nil
	}
	selected, err := s.opts.MarsWallexSelected(ctx)
	if err != nil {
		return false, err
	}
	if !selected {
		s.alert("replenish-unselected", "MarsWallex 未被选用且没有未结清的 MarsWallex 码，已暂停自动补卡。需要预热卡池时，在卡池策略里打开 replenish_when_unselected。")
	}
	return selected, nil
}

// SelectableCards 统计该套餐现在能选到多少张指定币种的卡，口径和 Acquire 一致：
// 产品优先级（没配时取 default）、跳过停用的产品、selectableWhere、有效期还剩一个月以上。
// 例外：currency 必填，传空串时返回 0（Acquire 的空币种表示不限，这里不支持）。
func (s *Service) SelectableCards(ctx context.Context, planKey, currency string) (int, error) {
	pol, err := LoadPolicy()
	if err != nil {
		return 0, err
	}
	products := pol.PlanProducts[planKey]
	if len(products) == 0 {
		products = pol.PlanProducts["default"]
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	now := s.now()
	total := 0
	for _, productID := range products {
		suspended, err := s.productSuspended(ctx, productID, planKey)
		if err != nil {
			return 0, err
		}
		if suspended {
			continue
		}
		n, err := countSelectable(ctx, productID, pol.MaxUsesPerCard, currency, func(expiry string) bool { return expiryEligible(expiry, now) })
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func countSelectable(ctx context.Context, productID string, maxUses int, currency string, eligible func(string) bool) (int, error) {
	args := append([]any{productID}, selectableArgs(maxUses)...)
	args = append(args, currency)
	rows, err := db.DB.QueryContext(ctx, `SELECT c.expiry`+selectableFrom+` WHERE c.product_id = ? AND`+selectableWhere+` AND UPPER(c.currency) = ?`, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var expiry string
		if err := rows.Scan(&expiry); err != nil {
			return 0, err
		}
		if eligible(expiry) {
			n++
		}
	}
	return n, rows.Err()
}

// CheckMainWallet 按本地记录检查主钱包某币种的可用余额够不够。要最新余额时调用方先 SyncAccounts。
func (s *Service) CheckMainWallet(ctx context.Context, currency string, cents int64) error {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	a, err := mainAccountFor(ctx, currency)
	if errors.Is(err, ErrNotFound) {
		return badRequest("本地没有主钱包记录，请先同步账户")
	}
	if err != nil {
		return err
	}
	return checkAvailable(a, "主钱包", currency, cents)
}

// dailyFundCount 今天（北京日）已经注资多少笔，口径和 insertPendingFund 一致。
func (s *Service) dailyFundCount(ctx context.Context, now time.Time) (int64, error) {
	day := beijingDayStart(now)
	var n int64
	err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mw_card_orders
		WHERE kind IN (?, ?) AND created_at >= ? AND created_at < ? AND state NOT IN (?, ?)`,
		KindRecharge, KindApply, day.Unix(), day.AddDate(0, 0, 1).Unix(), StateFailed, StateNotSent).Scan(&n)
	return n, err
}

// RemainingDailyFundCount 今天还能注资多少笔。
func (s *Service) RemainingDailyFundCount(ctx context.Context) (int64, error) {
	pol, err := LoadPolicy()
	if err != nil {
		return 0, err
	}
	used, err := s.dailyFundCount(ctx, s.now())
	if err != nil {
		return 0, err
	}
	if left := pol.DailyFundCountCap - used; left > 0 {
		return left, nil
	}
	return 0, nil
}

// RemainingDailyFund 今天还能注资多少分，口径和 insertPendingFund 一致：北京日，明确没动钱的单不算。
func (s *Service) RemainingDailyFund(ctx context.Context) (int64, error) {
	pol, err := LoadPolicy()
	if err != nil {
		return 0, err
	}
	day := beijingDayStart(s.now())
	var used int64
	err = db.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_cents), 0) FROM mw_card_orders
		WHERE kind IN (?, ?) AND created_at >= ? AND created_at < ? AND state NOT IN (?, ?)`,
		KindRecharge, KindApply, day.Unix(), day.AddDate(0, 0, 1).Unix(), StateFailed, StateNotSent).Scan(&used)
	if err != nil {
		return 0, err
	}
	if left := pol.DailyFundCapCents - used; left > 0 {
		return left, nil
	}
	return 0, nil
}
