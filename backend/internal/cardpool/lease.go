package cardpool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// 选卡时每批查询的候选卡数；一批都不合格或都没抢到时接着查下一批。
const acquireCandidates = 20

// AcquireRequest 为选卡请求。ExcludeCardIDs 供同一订单换卡重试时排除已试过的卡。
type AcquireRequest struct {
	PlanKey, OrderRef, AccountEmail string
	ExcludeCardIDs                  []string
}

// Lease 为一次租约。Token 是持有凭证；PlanKey、OrderRef、AccountEmail 在 Release 时用于记录结果。
type Lease struct {
	Token, CardID, ProductID, Currency, PlanKey, OrderRef, AccountEmail string
	ExpiresAt                                                           time.Time
}

// adminOwner 为管理员手动租用时的持有方。
func adminOwner(by string) string { return "admin:" + by }

// newLeaseToken 返回 16 字节随机数的十六进制。
func newLeaseToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成租约令牌失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// expiryEnd 把 "MMyy" 解析为卡失效的时刻：有效期所在月的下个月 1 日 00:00 UTC。格式不对返回 false。
func expiryEnd(mmyy string) (time.Time, bool) {
	mmyy = strings.TrimSpace(mmyy)
	if len(mmyy) != 4 {
		return time.Time{}, false
	}
	for i := 0; i < 4; i++ {
		if mmyy[i] < '0' || mmyy[i] > '9' {
			return time.Time{}, false
		}
	}
	month, _ := strconv.Atoi(mmyy[:2])
	year, _ := strconv.Atoi(mmyy[2:])
	if month < 1 || month > 12 {
		return time.Time{}, false
	}
	// time.Date 会把 13 月规整为次年 1 月
	return time.Date(2000+year, time.Month(month)+1, 1, 0, 0, 0, 0, time.UTC), true
}

// kickReplenish 非阻塞地请求一次补卡检查；已有待处理的请求时合并。
func (s *Service) kickReplenish() {
	select {
	case s.kickReplenishCh <- struct{}{}:
	default:
	}
}

// candidate 为选卡候选；useCount、createdAt 与 cardID 一起作为分批查询的游标。
type candidate struct {
	cardID, productID, currency, expiry string
	useCount                            int
	createdAt                           int64
}

// pickCandidates 查询某产品下可选的卡，一批最多 20 张，按 use_count 降序、入池时间升序、card_id 升序。
// after 非空时只取排在它之后的卡（键集分页，不受并发抢卡导致的行变化影响）。
// 有效期在 Go 里另行过滤。查询结果读完即关闭，避免占用连接时再执行抢卡语句。
func pickCandidates(ctx context.Context, productID string, exclude []string, after *candidate) ([]candidate, error) {
	q := `SELECT c.card_id, c.product_id, c.currency, c.expiry, c.use_count, c.created_at FROM mw_cards c
		LEFT JOIN mw_cardholders h ON h.card_holder_id = c.card_holder_id
		WHERE c.product_id = ? AND c.pool_state = ? AND c.platform_status = 'ACTIVE'
		AND (c.card_holder_id = '' OR UPPER(COALESCE(h.status, '')) NOT IN ('DISABLED', 'SUSPENDED'))`
	args := []any{productID, PoolAvailable}
	if len(exclude) > 0 {
		q += ` AND c.card_id NOT IN (` + placeholders(len(exclude)) + `)`
		for _, id := range exclude {
			args = append(args, id)
		}
	}
	if after != nil {
		q += ` AND (c.use_count < ? OR (c.use_count = ? AND (c.created_at > ? OR (c.created_at = ? AND c.card_id > ?))))`
		args = append(args, after.useCount, after.useCount, after.createdAt, after.createdAt, after.cardID)
	}
	q += ` ORDER BY c.use_count DESC, c.created_at ASC, c.card_id ASC LIMIT ?`
	args = append(args, acquireCandidates)
	rows, err := db.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.cardID, &c.productID, &c.currency, &c.expiry, &c.useCount, &c.createdAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// grabCard 用条件更新抢一张 available 的卡；抢到返回 true。
func (s *Service) grabCard(ctx context.Context, cardID, token, owner string, expiresAt int64) (bool, error) {
	res, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET pool_state = ?, lease_token = ?, lease_owner = ?,
		lease_expires_at = ?, lease_sensitive = 0, updated_at = ?
		WHERE card_id = ? AND pool_state = ?`,
		PoolLeased, token, owner, expiresAt, s.now().Unix(), cardID, PoolAvailable)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// Acquire 按 §7.1 选一张卡并加租约。无卡返回 ErrNoCard（原因为「无可用卡」或「全部产品已停用」），
// 同时触发一次补卡检查并告警（key nocard:<PlanKey>，每天一次）。
func (s *Service) Acquire(ctx context.Context, req AcquireRequest) (*Lease, error) {
	pol, err := LoadPolicy()
	if err != nil {
		return nil, err
	}
	products := pol.PlanProducts[req.PlanKey]
	if len(products) == 0 {
		products = pol.PlanProducts["default"]
	}
	token, err := newLeaseToken()
	if err != nil {
		return nil, err
	}
	now := s.now()
	notAfter := now.UTC().AddDate(0, 1, 0)
	expiresAt := now.Add(time.Duration(pol.LeaseMinutes) * time.Minute)

	suspended := 0
	for _, productID := range products {
		susp, err := s.productSuspended(ctx, productID, req.PlanKey)
		if err != nil {
			return nil, err
		}
		if susp {
			suspended++
			continue
		}
		// 分批取候选，直到抢到或该产品没有更多候选：一整批有效期不合格的卡不能挡住后面的卡
		var after *candidate
		for {
			cands, err := pickCandidates(ctx, productID, req.ExcludeCardIDs, after)
			if err != nil {
				return nil, err
			}
			for _, c := range cands {
				end, ok := expiryEnd(c.expiry)
				if !ok || !end.After(notAfter) {
					continue
				}
				got, err := s.grabCard(ctx, c.cardID, token, req.OrderRef, expiresAt.Unix())
				if err != nil {
					return nil, err
				}
				if got {
					return &Lease{
						Token: token, CardID: c.cardID, ProductID: c.productID, Currency: c.currency,
						PlanKey: req.PlanKey, OrderRef: req.OrderRef, AccountEmail: req.AccountEmail,
						ExpiresAt: time.Unix(expiresAt.Unix(), 0).UTC(),
					}, nil
				}
			}
			if len(cands) < acquireCandidates {
				break
			}
			after = &cands[len(cands)-1]
		}
	}

	reason := "无可用卡"
	if len(products) > 0 && suspended == len(products) {
		reason = "全部产品已停用"
	}
	s.kickReplenish()
	s.alert("nocard:"+req.PlanKey, fmt.Sprintf("卡池选卡失败：套餐 %s %s，已触发补卡检查", req.PlanKey, reason))
	return nil, fmt.Errorf("%w: %s", ErrNoCard, reason)
}

// leasedCard 读取租约对应的卡，要求仍在租用中、令牌一致；requireLive 时还要求未过期。否则返回 ErrLeaseInvalid。
func (s *Service) leasedCard(ctx context.Context, l *Lease, requireLive bool) (*Card, error) {
	if l == nil || l.Token == "" || l.CardID == "" {
		return nil, ErrLeaseInvalid
	}
	c, err := getCard(ctx, l.CardID)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrLeaseInvalid
	}
	if err != nil {
		return nil, err
	}
	if c.PoolState != PoolLeased || c.LeaseToken != l.Token {
		return nil, ErrLeaseInvalid
	}
	if requireLive && !leaseLive(c, s.now()) {
		return nil, ErrLeaseInvalid
	}
	return c, nil
}

// leaseLive 判断卡上的租约是否未过期。
func leaseLive(c *Card, now time.Time) bool {
	return c.LeaseExpiresAt != nil && *c.LeaseExpiresAt > now.Unix()
}

// Fund 给租约中的卡提交一笔转入，提交后即返回；币种取卡币种，金额必须大于 0。
func (s *Service) Fund(ctx context.Context, l *Lease, amountCents int64) (*Order, error) {
	c, err := s.leasedCard(ctx, l, true)
	if err != nil {
		return nil, err
	}
	return s.submitTransfer(ctx, KindRecharge, c.CardID, amountCents, c.Currency, "lease:"+l.Token)
}

// markSensitive 在调接口之前记下本次租约已取卡号；只对仍持有该令牌的租用中的卡生效。
func (s *Service) markSensitive(ctx context.Context, cardID, token string, requireLive bool) (bool, error) {
	now := s.now().Unix()
	q := `UPDATE mw_cards SET lease_sensitive = 1, last_sensitive_at = ?, updated_at = ?
		WHERE card_id = ? AND pool_state = ? AND lease_token = ?`
	args := []any{now, now, cardID, PoolLeased, token}
	if requireLive {
		q += ` AND lease_expires_at > ?`
		args = append(args, now)
	}
	res, err := db.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// Sensitive 实时查询完整卡号、CVV2、有效期，不落库、不记日志。先记下本次租约已取卡号再调接口：
// 响应丢失时平台可能已经返回了卡号，宁可多算一次使用。
func (s *Service) Sensitive(ctx context.Context, l *Lease) (*marswallex.CardSensitive, error) {
	c, err := s.leasedCard(ctx, l, true)
	if err != nil {
		return nil, err
	}
	client, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	ok, err := s.markSensitive(ctx, c.CardID, l.Token, true)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrLeaseInvalid
	}
	return client.GetCardSensitive(ctx, c.CardID)
}

// Release 释放租约并上报结果（aborted 不计入健康度），随后按 §6.4 回收。
// 先校验令牌（过期但仍在租用中的租约，持有者仍可释放）；记录结果出错时照样释放卡，再返回该错误。
func (s *Service) Release(ctx context.Context, l *Lease, o Outcome) error {
	switch o.Result {
	case ResultSuccess, ResultFailed, ResultAborted:
	default:
		return badRequest("结果只能是 success、failed 或 aborted")
	}
	c, err := s.leasedCard(ctx, l, false)
	if err != nil {
		return err
	}
	recErr := s.recordOutcome(ctx, c, l.PlanKey, l.OrderRef, l.AccountEmail, o)
	if err := s.releaseCard(ctx, c.CardID, l.Token); err != nil {
		if recErr != nil {
			return errors.Join(err, fmt.Errorf("记录用卡结果失败: %w", recErr))
		}
		return err
	}
	if recErr != nil {
		return fmt.Errorf("卡已释放，但记录用卡结果失败: %w", recErr)
	}
	return nil
}

// releaseCard 在一条语句里释放租约：取过卡号则 use_count + 1；retire_reason 非空或达到使用次数上限转 used，
// 否则转 releasing；清空租约字段，next_action_at = now。只对持有该令牌的租用中的卡生效，否则返回 ErrLeaseInvalid。
func (s *Service) releaseCard(ctx context.Context, cardID, token string) error {
	pol, err := LoadPolicy()
	if err != nil {
		// 策略读不出也要释放：LoadPolicy 出错时返回默认值（上限 1，偏保守）
		log.Printf("[cardpool] 读取卡池策略失败，按默认值释放卡 %s: %v", cardID, err)
	}
	now := s.now().Unix()
	// SQLite 的 SET 表达式一律读取更新前的值
	res, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET
		use_count = use_count + lease_sensitive,
		pool_state = CASE WHEN retire_reason <> '' OR use_count + lease_sensitive >= ? THEN ? ELSE ? END,
		lease_token = '', lease_owner = '', lease_expires_at = NULL, lease_sensitive = 0,
		next_action_at = ?, updated_at = ?
		WHERE card_id = ? AND pool_state = ? AND lease_token = ?`,
		pol.MaxUsesPerCard, PoolUsed, PoolReleasing, now, now, cardID, PoolLeased, token)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLeaseInvalid
	}
	return nil
}

// expireLeasesOnce 代为释放已到期的租约，不记录结果。没有到期时间的租用中的卡视为已到期。
func (s *Service) expireLeasesOnce(ctx context.Context) error {
	rows, err := db.DB.QueryContext(ctx, `SELECT card_id, lease_token FROM mw_cards
		WHERE pool_state = ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?) ORDER BY card_id`,
		PoolLeased, s.now().Unix())
	if err != nil {
		return err
	}
	type expired struct{ cardID, token string }
	var list []expired
	for rows.Next() {
		var e expired
		if err := rows.Scan(&e.cardID, &e.token); err != nil {
			rows.Close()
			return err
		}
		list = append(list, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var firstErr error
	for _, e := range list {
		err := s.releaseCard(ctx, e.cardID, e.token)
		switch {
		case err == nil:
			log.Printf("[cardpool] 卡 %s 租约到期，已释放", e.cardID)
		case errors.Is(err, ErrLeaseInvalid):
			// 持有者已经释放或续上了新租约
		default:
			log.Printf("[cardpool] 释放到期租约 %s 出错: %v", e.cardID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// ---------- 管理员手动用卡 ----------

// AdminLease 管理员租用一张 available 的卡，持有方记为 admin:<by>，时长同策略。
func (s *Service) AdminLease(ctx context.Context, cardID, by string) (*Lease, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return nil, badRequest("缺少管理员身份")
	}
	pol, err := LoadPolicy()
	if err != nil {
		return nil, err
	}
	token, err := newLeaseToken()
	if err != nil {
		return nil, err
	}
	expiresAt := s.now().Add(time.Duration(pol.LeaseMinutes) * time.Minute).Unix()
	got, err := s.grabCard(ctx, cardID, token, adminOwner(by), expiresAt)
	if err != nil {
		return nil, err
	}
	c, err := getCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	if !got {
		return nil, badRequest("只有可用状态的卡才能租用")
	}
	log.Printf("[cardpool] 管理员 %s 租用卡 %s", by, cardID)
	return &Lease{
		Token: token, CardID: c.CardID, ProductID: c.ProductID, Currency: c.Currency,
		OrderRef: adminOwner(by), ExpiresAt: time.Unix(expiresAt, 0).UTC(),
	}, nil
}

// adminLeasedCard 要求卡由 admin:<by> 租用中（requireLive 时还要求未过期），否则返回 ErrLeaseInvalid。
func (s *Service) adminLeasedCard(ctx context.Context, cardID, by string, requireLive bool) (*Card, error) {
	c, err := getCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	by = strings.TrimSpace(by)
	if by == "" || c.PoolState != PoolLeased || c.LeaseOwner != adminOwner(by) || c.LeaseToken == "" {
		return nil, ErrLeaseInvalid
	}
	if requireLive && !leaseLive(c, s.now()) {
		return nil, ErrLeaseInvalid
	}
	return c, nil
}

// AdminFund 给自己租用中的卡转入。
func (s *Service) AdminFund(ctx context.Context, cardID string, amountCents int64, by string) (*Order, error) {
	c, err := s.adminLeasedCard(ctx, cardID, by, true)
	if err != nil {
		return nil, err
	}
	return s.submitTransfer(ctx, KindRecharge, c.CardID, amountCents, c.Currency, strings.TrimSpace(by))
}

// AdminRelease 释放自己租用中的卡（过期但仍在租用中也可以），不记录结果，之后按 §6.4 回收。
func (s *Service) AdminRelease(ctx context.Context, cardID, by string) error {
	c, err := s.adminLeasedCard(ctx, cardID, by, false)
	if err != nil {
		return err
	}
	if err := s.releaseCard(ctx, c.CardID, c.LeaseToken); err != nil {
		return err
	}
	log.Printf("[cardpool] 管理员 %s 释放卡 %s", by, cardID)
	return nil
}

// ViewSensitive 管理员查看完整卡号：available 要先租用；leased（不论持有方）先记为本次租约已取卡号再查询；
// 其他状态只查询、不改状态。返回值不得记日志。
func (s *Service) ViewSensitive(ctx context.Context, cardID, by string) (*marswallex.CardSensitive, error) {
	c, err := getCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	if c.PoolState == PoolAvailable {
		return nil, badRequest("请先租用")
	}
	client, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	if c.PoolState == PoolLeased {
		ok, err := s.markSensitive(ctx, c.CardID, c.LeaseToken, false)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, badRequest("卡状态已变化，请重试")
		}
	}
	log.Printf("[cardpool] 管理员 %s 查看卡 %s 的卡号（池状态 %s）", by, cardID, c.PoolState)
	return client.GetCardSensitive(ctx, c.CardID)
}
