package cardpool

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// 订单结果
const (
	ResultSuccess = "success"
	ResultFailed  = "failed"
	ResultAborted = "aborted"
)

// Outcome 为一次用卡结果。
type Outcome struct{ Result, ErrorCode string }

// normalizeEmail 小写、去空格；空字符串记为 "unknown"。
func normalizeEmail(email string) string {
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "" {
		return "unknown"
	}
	return e
}

// evaluateVerdict 根据失败次数与不同已知邮箱数给出判定。
func evaluateVerdict(failCount, distinctKnownEmails, threshold int) string {
	if failCount < threshold {
		return "need_more"
	}
	switch {
	case distinctKnownEmails >= 2:
		return "suspect"
	case distinctKnownEmails == 1:
		return "email_suspect"
	}
	return "unknown_emails"
}

// recordOutcome 记录一次用卡结果，并评估产品×套餐与单卡的健康度。aborted 不记录。
func (s *Service) recordOutcome(ctx context.Context, c *Card, planKey, orderRef, email string, o Outcome) error {
	if o.Result == ResultAborted {
		return nil
	}
	now := s.now().Unix()
	if _, err := db.DB.ExecContext(ctx,
		`INSERT INTO mw_card_outcomes (card_id, product_id, plan_key, order_ref, email_norm, result, error_code, created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		c.CardID, c.ProductID, planKey, orderRef, normalizeEmail(email), o.Result, o.ErrorCode, now); err != nil {
		return err
	}
	if o.Result != ResultFailed {
		return nil
	}
	p, err := LoadPolicy()
	if err != nil {
		return err
	}
	if err := s.evalProduct(ctx, p, c.ProductID, planKey, now); err != nil {
		return err
	}
	if p.MaxUsesPerCard > 1 {
		return s.evalCard(ctx, p, c.CardID, now)
	}
	return nil
}

// countFailures 统计满足条件的失败次数与不同已知邮箱数。scopeWhere 形如 "product_id=? AND plan_key=?"。
// 起算点：created_at > since，且 id 在最近一次成功之后。
func countFailures(ctx context.Context, scopeWhere string, args []any, since int64) (fails, distinct int, err error) {
	var lastSuccessID sql.NullInt64
	if err = db.DB.QueryRowContext(ctx,
		`SELECT MAX(id) FROM mw_card_outcomes WHERE `+scopeWhere+` AND result = ?`,
		append(append([]any{}, args...), ResultSuccess)...).Scan(&lastSuccessID); err != nil {
		return
	}
	qargs := append(append([]any{}, args...), ResultFailed, since, lastSuccessID.Int64)
	err = db.DB.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT CASE WHEN email_norm <> 'unknown' THEN email_norm END)
		 FROM mw_card_outcomes WHERE `+scopeWhere+` AND result = ? AND created_at > ? AND id > ?`,
		qargs...).Scan(&fails, &distinct)
	return
}

func (s *Service) evalProduct(ctx context.Context, p Policy, productID, planKey string, now int64) error {
	suspended, err := s.productSuspended(ctx, productID, planKey)
	if err != nil {
		return err
	}
	if suspended {
		return nil
	}
	since := now - int64(p.HealthWindowHours)*3600
	var resumedAt sql.NullInt64
	err = db.DB.QueryRowContext(ctx, `SELECT resumed_at FROM mw_product_health WHERE product_id = ? AND plan_key = ?`,
		productID, planKey).Scan(&resumedAt)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if resumedAt.Valid && resumedAt.Int64 > since {
		since = resumedAt.Int64
	}
	fails, distinct, err := countFailures(ctx, "product_id = ? AND plan_key = ?", []any{productID, planKey}, since)
	if err != nil {
		return err
	}
	if evaluateVerdict(fails, distinct, p.ProductFailThreshold) != "suspect" {
		return nil
	}
	reason := fmt.Sprintf("近 %d 小时 %d 次失败，涉及 %d 个邮箱", p.HealthWindowHours, fails, distinct)
	if !p.AutoSuspend {
		s.alert(fmt.Sprintf("health:%s:%s", productID, planKey),
			fmt.Sprintf("卡产品 %s 套餐 %s 疑似异常：%s（自动停用未开启）", productID, planKey, reason))
		return nil
	}
	if _, err := db.DB.ExecContext(ctx,
		`INSERT INTO mw_product_health (product_id, plan_key, suspended_at, reason) VALUES (?,?,?,?)
		 ON CONFLICT(product_id, plan_key) DO UPDATE SET suspended_at=excluded.suspended_at, reason=excluded.reason, resumed_at=NULL, resumed_by=''`,
		productID, planKey, now, reason); err != nil {
		return err
	}
	s.alert(fmt.Sprintf("suspend:%s:%s", productID, planKey),
		fmt.Sprintf("卡产品 %s 套餐 %s 已自动停用：%s", productID, planKey, reason))
	return nil
}

func (s *Service) evalCard(ctx context.Context, p Policy, cardID string, now int64) error {
	since := now - int64(p.HealthWindowHours)*3600
	fails, distinct, err := countFailures(ctx, "card_id = ?", []any{cardID}, since)
	if err != nil {
		return err
	}
	if evaluateVerdict(fails, distinct, p.CardFailThreshold) != "suspect" {
		return nil
	}
	reason := fmt.Sprintf("近 %d 小时 %d 次失败，涉及 %d 个邮箱", p.HealthWindowHours, fails, distinct)
	if !p.AutoSuspend {
		s.alert("health:card:"+cardID, fmt.Sprintf("卡 %s 疑似异常：%s（自动停用未开启）", cardID, reason))
		return nil
	}
	if _, err := db.DB.ExecContext(ctx,
		`UPDATE mw_cards SET retire_reason = '健康度',
		   pool_state = CASE WHEN pool_state IN (?, ?) THEN ? ELSE pool_state END,
		   next_action_at = CASE WHEN pool_state IN (?, ?) THEN ? ELSE next_action_at END,
		   updated_at = ?
		 WHERE card_id = ?`,
		PoolAvailable, PoolReleasing, PoolUsed, PoolAvailable, PoolReleasing, now, now, cardID); err != nil {
		return err
	}
	s.alert("cardhealth:"+cardID, fmt.Sprintf("卡 %s 已因健康度退役：%s", cardID, reason))
	return nil
}

// productSuspended 判断产品×套餐是否处于停用状态。
func (s *Service) productSuspended(ctx context.Context, productID, planKey string) (bool, error) {
	var susp, resumed sql.NullInt64
	err := db.DB.QueryRowContext(ctx, `SELECT suspended_at, resumed_at FROM mw_product_health WHERE product_id = ? AND plan_key = ?`,
		productID, planKey).Scan(&susp, &resumed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return susp.Valid && (!resumed.Valid || resumed.Int64 < susp.Int64), nil
}

// ResumeProduct 恢复被停用的产品×套餐；未停用时返回 ErrBadRequest。
func (s *Service) ResumeProduct(ctx context.Context, productID, planKey, by string) error {
	suspended, err := s.productSuspended(ctx, productID, planKey)
	if err != nil {
		return err
	}
	if !suspended {
		return badRequest("该产品套餐未处于停用状态")
	}
	_, err = db.DB.ExecContext(ctx, `UPDATE mw_product_health SET resumed_at = ?, resumed_by = ? WHERE product_id = ? AND plan_key = ?`,
		s.now().Unix(), by, productID, planKey)
	return err
}

// ProductHealth 为产品×套餐的健康度报告行。
type ProductHealth struct {
	ProductID, PlanKey                         string
	Success24h, Failed24h, Success7d, Failed7d int
	Suspended                                  bool
	SuspendedAt                                *int64
	Reason                                     string
}

// HealthReport 汇总所有出现过的产品×套餐的 24 小时与 7 天成功失败数及停用状态。
func (s *Service) HealthReport(ctx context.Context) ([]ProductHealth, error) {
	now := s.now()
	t24 := now.Add(-24 * time.Hour).Unix()
	t7 := now.Add(-7 * 24 * time.Hour).Unix()
	rows, err := db.DB.QueryContext(ctx,
		`SELECT product_id, plan_key,
		   COALESCE(SUM(CASE WHEN result=? AND created_at>? THEN 1 ELSE 0 END),0),
		   COALESCE(SUM(CASE WHEN result=? AND created_at>? THEN 1 ELSE 0 END),0),
		   COALESCE(SUM(CASE WHEN result=? AND created_at>? THEN 1 ELSE 0 END),0),
		   COALESCE(SUM(CASE WHEN result=? AND created_at>? THEN 1 ELSE 0 END),0)
		 FROM mw_card_outcomes GROUP BY product_id, plan_key`,
		ResultSuccess, t24, ResultFailed, t24, ResultSuccess, t7, ResultFailed, t7)
	if err != nil {
		return nil, err
	}
	byKey := map[[2]string]*ProductHealth{}
	var keys [][2]string
	for rows.Next() {
		var h ProductHealth
		if err := rows.Scan(&h.ProductID, &h.PlanKey, &h.Success24h, &h.Failed24h, &h.Success7d, &h.Failed7d); err != nil {
			rows.Close()
			return nil, err
		}
		k := [2]string{h.ProductID, h.PlanKey}
		byKey[k] = &h
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	hrows, err := db.DB.QueryContext(ctx, `SELECT product_id, plan_key, suspended_at, reason, resumed_at FROM mw_product_health`)
	if err != nil {
		return nil, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var pid, plan, reason string
		var susp, resumed sql.NullInt64
		if err := hrows.Scan(&pid, &plan, &susp, &reason, &resumed); err != nil {
			return nil, err
		}
		k := [2]string{pid, plan}
		h := byKey[k]
		if h == nil {
			h = &ProductHealth{ProductID: pid, PlanKey: plan}
			byKey[k] = h
			keys = append(keys, k)
		}
		if susp.Valid {
			v := susp.Int64
			h.SuspendedAt = &v
			h.Reason = reason
			h.Suspended = !resumed.Valid || resumed.Int64 < susp.Int64
		}
	}
	if err := hrows.Err(); err != nil {
		return nil, err
	}
	out := make([]ProductHealth, 0, len(keys))
	for _, k := range keys {
		out = append(out, *byKey[k])
	}
	sortHealth(out)
	return out, nil
}

func sortHealth(hs []ProductHealth) {
	for i := 1; i < len(hs); i++ {
		for j := i; j > 0 && (hs[j].ProductID < hs[j-1].ProductID ||
			(hs[j].ProductID == hs[j-1].ProductID && hs[j].PlanKey < hs[j-1].PlanKey)); j-- {
			hs[j], hs[j-1] = hs[j-1], hs[j]
		}
	}
}
