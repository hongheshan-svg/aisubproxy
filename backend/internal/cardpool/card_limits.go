package cardpool

import (
	"context"
	"fmt"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// 卡的单笔、单日、单月限额（API-02）。只提供管理员手动设置和查询，
// 开卡与付款流程不会自动下发限额：真实付款还没验证过，自动设限可能造成拒付。

// normalizeLimit 校验并规范三项限额：空串表示这一项不设；非空须是大于 0 且最多两位小数的金额。
func normalizeLimit(l marswallex.CardLimit) (marswallex.CardLimit, error) {
	var out marswallex.CardLimit
	for _, f := range []struct {
		name string
		in   string
		out  *string
	}{
		{"单笔限额", l.SingleLimit, &out.SingleLimit},
		{"单日限额", l.DailyLimit, &out.DailyLimit},
		{"单月限额", l.MonthlyLimit, &out.MonthlyLimit},
	} {
		v := strings.TrimSpace(f.in)
		if v == "" {
			continue
		}
		cents, err := marswallex.ParseAmount(v)
		if err != nil || cents <= 0 {
			return out, badRequest(f.name + "无效，须为大于 0 且最多两位小数的金额")
		}
		s, err := marswallex.FormatAmount(cents)
		if err != nil {
			return out, badRequest(f.name + "无效")
		}
		*f.out = s
	}
	if out == (marswallex.CardLimit{}) {
		return out, badRequest("至少需要设置一项限额")
	}
	return out, nil
}

// SetCardLimit 修改卡的限额。限额是整体替换：没填的项在平台上会变成「不限制」。
// 租出中的卡不能改：付款进行到一半改限额可能让这一笔被拒。相同内容重复提交结果一样，结果不明时可以重试。
func (s *Service) SetCardLimit(ctx context.Context, cardID string, limit marswallex.CardLimit) (marswallex.CardLimit, error) {
	l, err := normalizeLimit(limit)
	if err != nil {
		return l, err
	}
	card, err := getCard(ctx, cardID)
	if err != nil {
		return l, err
	}
	if card.PoolState == PoolLeased {
		return l, fmt.Errorf("%w: 卡 %s 已租出，付款中不能改限额", ErrCardBusy, cardID)
	}
	c, err := s.opts.Client()
	if err != nil {
		return l, err
	}
	return l, c.UpdateCardLimit(ctx, cardID, l)
}

// CardLimitUsage 查询卡的限额与剩余限额。
func (s *Service) CardLimitUsage(ctx context.Context, cardID string) (*marswallex.CardLimitUsage, error) {
	if _, err := getCard(ctx, cardID); err != nil {
		return nil, err
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	return c.GetCardLimitUsage(ctx, cardID)
}
