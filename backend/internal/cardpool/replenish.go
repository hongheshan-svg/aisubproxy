package cardpool

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// replenishGap 返回产品的缺口：目标数 − (available/releasing 卡数 + 在途开卡单数)。
func replenishGap(ctx context.Context, productID string, target int) (int, error) {
	var cards, orders int
	if err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mw_cards WHERE product_id = ? AND pool_state IN (?, ?)`,
		productID, PoolAvailable, PoolReleasing).Scan(&cards); err != nil {
		return 0, err
	}
	if err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mw_card_orders WHERE kind = ? AND product_id = ? AND state IN (?, ?, ?, ?)`,
		KindApply, productID, StatePending, StateProcessing, StateUnknown, StateNeedsReview).Scan(&orders); err != nil {
		return 0, err
	}
	return target - cards - orders, nil
}

// todayApplyCount 返回产品自北京今日零点起的开卡单数（任何状态）。
func (s *Service) todayApplyCount(ctx context.Context, productID string) (int, error) {
	var n int
	err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mw_card_orders WHERE kind = ? AND product_id = ? AND created_at >= ?`,
		KindApply, productID, beijingDayStart(s.now()).Unix()).Scan(&n)
	return n, err
}

// approvedHolders 按策略顺序返回已导入且 APPROVED 的持卡人。
func approvedHolders(ctx context.Context, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		h, err := getCardholder(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if normStatus(h.Status) == "APPROVED" {
			out = append(out, id)
		}
	}
	return out, nil
}

// replenishOnce 按产品目标数补卡一轮，受每日开卡上限约束。
func (s *Service) replenishOnce(ctx context.Context) error {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	pol, err := LoadPolicy()
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(pol.ProductTargets))
	for id, t := range pol.ProductTargets {
		if t.Target > 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	var products []marswallex.CardProduct
	fetched := false
	for _, pid := range ids {
		tgt := pol.ProductTargets[pid]
		gap, err := replenishGap(ctx, pid, tgt.Target)
		if err != nil {
			return err
		}
		if gap <= 0 {
			continue
		}
		today, err := s.todayApplyCount(ctx, pid)
		if err != nil {
			return err
		}
		if tgt.DailyLimit <= 0 || today >= tgt.DailyLimit {
			s.alert("limit:"+pid, fmt.Sprintf("自动补卡：产品 %s 缺 %d 张，但今日开卡已达上限（%d/%d）", pid, gap, today, tgt.DailyLimit))
			continue
		}
		if !fetched {
			c, err := s.opts.Client()
			if err != nil {
				return err
			}
			if products, err = c.ListCardProducts(ctx); err != nil {
				return err
			}
			fetched = true
		}
		p := findProduct(products, pid)
		if p == nil {
			s.alert("product:"+pid, fmt.Sprintf("自动补卡：平台上找不到卡产品 %s", pid))
			continue
		}
		holders, err := approvedHolders(ctx, pol.HolderIDs)
		if err != nil {
			return err
		}
		if len(holders) == 0 {
			s.alert("noholder", "自动补卡：没有可用的持卡人（需已导入且为 APPROVED）")
			continue
		}
		n := gap
		if rest := tgt.DailyLimit - today; rest < n {
			n = rest
		}
		for i := 0; i < n; i++ {
			holder := holders[s.rr%len(holders)]
			s.rr++
			o, err := s.submitApply(ctx, applyParams{
				HolderID: holder, ProductID: pid, Currency: p.CardCurrency,
				NamePrefix: pol.CardNamePrefix, Expiry: expiryMMyy(s.now(), pol.ValidityMonths),
				By: "replenish", AmountCents: pol.OpenAmountCents,
			})
			if err != nil {
				s.alert("applyfail:"+pid, fmt.Sprintf("自动补卡：产品 %s 开卡出错：%v", pid, err))
				break
			}
			if o.State == StateFailed || o.State == StateNotSent {
				s.alert("applyfail:"+pid, fmt.Sprintf("自动补卡：产品 %s 开卡失败（%s）：%s", pid, o.State, o.LastError))
				break
			}
		}
	}
	return nil
}
