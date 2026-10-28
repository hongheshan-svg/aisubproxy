package cardpool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mattn/go-sqlite3"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
	detailLimit     = 50
	maxApplyCount   = 20
)

// ---------- 持卡人 ----------

// ImportCardholder 从平台读取持卡人并写入；已存在则更新平台字段（备注 note 保留）。
func (s *Service) ImportCardholder(ctx context.Context, id, by string) (*Cardholder, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, badRequest("持卡人 ID 不能为空")
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	d, err := c.GetCardholder(ctx, id)
	if err != nil {
		return nil, err
	}
	now := s.now().Unix()
	// 以请求的 ID 为准；平台返回的 ID 若不同则不信任
	_, err = db.DB.ExecContext(ctx, `INSERT INTO mw_cardholders (card_holder_id, first_name, last_name, country_code, status, remark, synced_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(card_holder_id) DO UPDATE SET first_name = excluded.first_name, last_name = excluded.last_name,
		country_code = excluded.country_code, status = excluded.status, remark = excluded.remark, synced_at = excluded.synced_at`,
		id, d.FirstName, d.LastName, d.CountryCode, normStatus(d.Status), d.Remark, now, now)
	if err != nil {
		return nil, err
	}
	return getCardholder(ctx, id)
}

// RefreshCardholder 重新同步已导入的持卡人。
func (s *Service) RefreshCardholder(ctx context.Context, id string) (*Cardholder, error) {
	if _, err := getCardholder(ctx, id); err != nil {
		return nil, err
	}
	return s.ImportCardholder(ctx, id, "")
}

// ListCardholders 列出所有已导入的持卡人。
func (s *Service) ListCardholders(ctx context.Context) ([]Cardholder, error) {
	rows, err := db.DB.QueryContext(ctx, `SELECT `+cardholderCols+` FROM mw_cardholders ORDER BY created_at DESC, card_holder_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Cardholder{}
	for rows.Next() {
		var h Cardholder
		if err := h.scan(rows); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ListProducts 返回平台的卡产品。
func (s *Service) ListProducts(ctx context.Context) ([]marswallex.CardProduct, error) {
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	return c.ListCardProducts(ctx)
}

// ---------- 卡：导入与刷新 ----------

// parseBalance 把平台余额字符串转成分；空串按 0，格式错误返回错误。
func parseBalance(s string) (int64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return marswallex.ParseAmount(s)
}

// lastDigits4 取 cardNo 中数字的最后 4 位；少于 4 位返回空。
func lastDigits4(cardNo string) string {
	var digits []byte
	for i := 0; i < len(cardNo); i++ {
		if cardNo[i] >= '0' && cardNo[i] <= '9' {
			digits = append(digits, cardNo[i])
		}
	}
	if len(digits) < 4 {
		return ""
	}
	return string(digits[len(digits)-4:])
}

func findProduct(products []marswallex.CardProduct, id string) *marswallex.CardProduct {
	for i := range products {
		if products[i].ProductID == id {
			return &products[i]
		}
	}
	return nil
}

// ImportCard 导入一张已在平台上的卡：校验产品一致后写入，状态为 releasing，由回收流程清零后入池。
func (s *Service) ImportCard(ctx context.Context, cardID, productID, holderID, by string) (*Card, error) {
	cardID, productID, holderID = strings.TrimSpace(cardID), strings.TrimSpace(productID), strings.TrimSpace(holderID)
	if cardID == "" || productID == "" {
		return nil, badRequest("卡 ID 和卡产品不能为空")
	}
	if _, err := getCard(ctx, cardID); err == nil {
		return nil, badRequest("卡已在池中")
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if holderID != "" {
		if _, err := getCardholder(ctx, holderID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, badRequest("持卡人尚未导入")
			}
			return nil, err
		}
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	d, err := c.GetCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	products, err := c.ListCardProducts(ctx)
	if err != nil {
		return nil, err
	}
	p := findProduct(products, productID)
	if p == nil {
		return nil, badRequest("卡产品不存在")
	}
	eq := strings.EqualFold
	if !eq(strings.TrimSpace(d.CardLabel), strings.TrimSpace(p.CardLabel)) ||
		!eq(strings.TrimSpace(d.CardCountry), strings.TrimSpace(p.CardCountry)) ||
		!eq(strings.TrimSpace(d.CardCurrency), strings.TrimSpace(p.CardCurrency)) ||
		!eq(strings.TrimSpace(d.CardMode), strings.TrimSpace(p.CardMode)) {
		return nil, badRequest("卡与产品不一致")
	}
	status := normStatus(d.Status)
	if status == "CANCELED" {
		return nil, badRequest("卡已在平台注销")
	}
	avail, err := parseBalance(d.AvailableBalance)
	if err != nil {
		return nil, fmt.Errorf("解析可用余额失败: %w", err)
	}
	frozen, err := parseBalance(d.FreezeBalance)
	if err != nil {
		return nil, fmt.Errorf("解析冻结余额失败: %w", err)
	}
	now := s.now().Unix()
	_, err = db.DB.ExecContext(ctx, `INSERT INTO mw_cards (card_id, card_holder_id, product_id, card_label, card_country, currency,
		last4, expiry, platform_status, available_cents, frozen_cents, balance_synced_at, source, pool_state, next_action_at,
		created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'imported', ?, ?, ?, ?)`,
		cardID, holderID, productID, d.CardLabel, d.CardCountry, d.CardCurrency, lastDigits4(d.CardNo), d.ExpiryDate, status,
		avail, frozen, now, PoolReleasing, now, now, now)
	if err != nil {
		var se sqlite3.Error
		if errors.As(err, &se) && se.ExtendedCode == sqlite3.ErrConstraintPrimaryKey {
			return nil, badRequest("卡已在池中")
		}
		return nil, err
	}
	return getCard(ctx, cardID)
}

// RefreshCard 从平台同步卡组、国家、币种、有效期、状态与余额。平台返回的空字段不覆盖已有值。
func (s *Service) RefreshCard(ctx context.Context, cardID string) (*Card, error) {
	if _, err := getCard(ctx, cardID); err != nil {
		return nil, err
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	d, err := c.GetCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	avail, err := parseBalance(d.AvailableBalance)
	if err != nil {
		return nil, fmt.Errorf("解析可用余额失败: %w", err)
	}
	frozen, err := parseBalance(d.FreezeBalance)
	if err != nil {
		return nil, fmt.Errorf("解析冻结余额失败: %w", err)
	}
	now := s.now().Unix()
	_, err = db.DB.ExecContext(ctx, `UPDATE mw_cards SET
		card_label = CASE WHEN ? <> '' THEN ? ELSE card_label END,
		card_country = CASE WHEN ? <> '' THEN ? ELSE card_country END,
		currency = CASE WHEN ? <> '' THEN ? ELSE currency END,
		expiry = CASE WHEN ? <> '' THEN ? ELSE expiry END,
		platform_status = CASE WHEN ? <> '' THEN ? ELSE platform_status END,
		last4 = CASE WHEN ? <> '' THEN ? ELSE last4 END,
		available_cents = ?, frozen_cents = ?, balance_synced_at = ?, updated_at = ?
		WHERE card_id = ?`,
		d.CardLabel, d.CardLabel, d.CardCountry, d.CardCountry, d.CardCurrency, d.CardCurrency,
		d.ExpiryDate, d.ExpiryDate, normStatus(d.Status), normStatus(d.Status),
		lastDigits4(d.CardNo), lastDigits4(d.CardNo),
		avail, frozen, now, now, cardID)
	if err != nil {
		return nil, err
	}
	return getCard(ctx, cardID)
}

// ---------- 卡：开卡 ----------

// ApplyCards 手动开卡 count 张。遇到第一张失败、未发出或结果未知（含待人工核对）的单据，或第一次提交出错就停止，
// 返回已创建的单据（和错误，如有）。与自动补卡共用 applyMu 串行；手动开卡不受每日上限限制，但计入当天数量。
func (s *Service) ApplyCards(ctx context.Context, productID, holderID string, count int, by string) ([]Order, error) {
	productID, holderID = strings.TrimSpace(productID), strings.TrimSpace(holderID)
	if count < 1 || count > maxApplyCount {
		return nil, badRequest(fmt.Sprintf("开卡数量必须在 1 到 %d 之间", maxApplyCount))
	}
	if productID == "" {
		return nil, badRequest("卡产品不能为空")
	}
	h, err := getCardholder(ctx, holderID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, badRequest("持卡人尚未导入")
		}
		return nil, err
	}
	if normStatus(h.Status) != "APPROVED" {
		return nil, badRequest("持卡人未通过审核")
	}
	pol, err := LoadPolicy()
	if err != nil {
		return nil, err
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	products, err := c.ListCardProducts(ctx)
	if err != nil {
		return nil, err
	}
	p := findProduct(products, productID)
	if p == nil {
		return nil, badRequest("卡产品不存在")
	}

	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	out := make([]Order, 0, count)
	for i := 0; i < count; i++ {
		o, err := s.submitApply(ctx, applyParams{
			HolderID: holderID, ProductID: productID, Currency: p.CardCurrency,
			NamePrefix: pol.CardNamePrefix, Expiry: expiryMMyy(s.now(), pol.ValidityMonths),
			By: by, AmountCents: pol.OpenAmountCents,
		})
		if err != nil {
			return out, err
		}
		out = append(out, *o)
		if applyStops(o.State) {
			break
		}
	}
	return out, nil
}

// ---------- 卡：停用 / 启用 / 改状态 / 清零 ----------

// DisableCard 停用一张可用的卡；reason 为空记为"管理员停用"。
func (s *Service) DisableCard(ctx context.Context, cardID, reason, by string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "管理员停用"
	}
	res, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET pool_state = ?, disabled_reason = ?, updated_at = ?
		WHERE card_id = ? AND pool_state = ?`, PoolDisabled, reason, s.now().Unix(), cardID, PoolAvailable)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	card, err := getCard(ctx, cardID)
	if err != nil {
		return err
	}
	if card.PoolState == PoolLeased {
		return fmt.Errorf("%w: 卡 %s 已租出", ErrCardBusy, cardID)
	}
	return badRequest("只有可用状态的卡才能停用")
}

// EnableCard 启用已停用的卡：没有退役原因；先同步平台，要求余额均为 0、未用满次数、平台状态 ACTIVE。
func (s *Service) EnableCard(ctx context.Context, cardID, by string) error {
	card, err := getCard(ctx, cardID)
	if err != nil {
		return err
	}
	if card.PoolState != PoolDisabled {
		return badRequest("只有已停用的卡才能启用")
	}
	if card.RetireReason != "" {
		return badRequest("卡已标记退役（" + card.RetireReason + "），不能启用")
	}
	card, err = s.RefreshCard(ctx, cardID)
	if err != nil {
		return err
	}
	pol, err := LoadPolicy()
	if err != nil {
		return err
	}
	switch {
	case card.AvailableCents != 0 || card.FrozenCents != 0:
		return badRequest("卡上仍有余额，请先清零")
	case card.UseCount >= pol.MaxUsesPerCard:
		return badRequest("卡已达到使用次数上限")
	case card.PlatformStatus != "ACTIVE":
		return badRequest("卡在平台上的状态不是 ACTIVE")
	}
	res, err := db.DB.ExecContext(ctx, `UPDATE mw_cards SET pool_state = ?, disabled_reason = '', updated_at = ?
		WHERE card_id = ? AND pool_state = ?`, PoolAvailable, s.now().Unix(), cardID, PoolDisabled)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return badRequest("卡状态已变化，请重试")
	}
	return nil
}

// guardOperable 检查卡可以做冻结、解冻、注销、清零：租出中返回 ErrCardBusy，已退役返回 ErrBadRequest。
func guardOperable(ctx context.Context, cardID string) (*Card, error) {
	card, err := getCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	switch card.PoolState {
	case PoolLeased:
		return nil, fmt.Errorf("%w: 卡 %s 已租出", ErrCardBusy, cardID)
	case PoolRetired:
		return nil, badRequest("卡已退役")
	}
	return card, nil
}

func (s *Service) changeStatus(ctx context.Context, cardID, target, by string) (*Order, error) {
	if _, err := guardOperable(ctx, cardID); err != nil {
		return nil, err
	}
	return s.submitStatus(ctx, cardID, target, by)
}

// FreezeCard 冻结卡（平台状态 REVOKED）。
func (s *Service) FreezeCard(ctx context.Context, cardID, by string) (*Order, error) {
	return s.changeStatus(ctx, cardID, "REVOKED", by)
}

// UnfreezeCard 解冻卡（平台状态 ACTIVE）。
func (s *Service) UnfreezeCard(ctx context.Context, cardID, by string) (*Order, error) {
	return s.changeStatus(ctx, cardID, "ACTIVE", by)
}

// CancelCard 注销卡（平台状态 CANCELED），成功后卡退役。
func (s *Service) CancelCard(ctx context.Context, cardID, by string) (*Order, error) {
	return s.changeStatus(ctx, cardID, "CANCELED", by)
}

// SweepCard 把卡上的可用余额全部转回主钱包。
func (s *Service) SweepCard(ctx context.Context, cardID, by string) (*Order, error) {
	if _, err := guardOperable(ctx, cardID); err != nil {
		return nil, err
	}
	card, err := s.RefreshCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	if card.AvailableCents <= 0 {
		return nil, badRequest("无可用余额")
	}
	return s.submitTransfer(ctx, KindWithdraw, cardID, card.AvailableCents, card.Currency, by)
}

// ---------- 列表 ----------

// CardFilter 为卡列表过滤条件。
type CardFilter struct {
	State, ProductID string
	Page, PageSize   int
}

// OrderFilter 为单据列表过滤条件。
type OrderFilter struct {
	State, Kind, CardID string
	Page, PageSize      int
}

// TxnFilter 为交易列表过滤条件。
type TxnFilter struct {
	CardID         string
	Page, PageSize int
}

// pageArgs 规整分页参数：Page<1 → 1，PageSize 默认 20、上限 100。
func pageArgs(page, size int) (limit, offset int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	return size, (page - 1) * size
}

// whereBuilder 拼接等值过滤条件；空值忽略。
type whereBuilder struct {
	conds []string
	args  []any
}

func (w *whereBuilder) eq(col, v string) {
	if v != "" {
		w.conds = append(w.conds, col+" = ?")
		w.args = append(w.args, v)
	}
}

func (w *whereBuilder) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

// ListCards 按创建时间倒序列出卡，并返回满足过滤条件的总数。
func (s *Service) ListCards(ctx context.Context, f CardFilter) ([]Card, int, error) {
	var w whereBuilder
	w.eq("pool_state", f.State)
	w.eq("product_id", f.ProductID)
	var total int
	if err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mw_cards`+w.sql(), w.args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit, offset := pageArgs(f.Page, f.PageSize)
	rows, err := db.DB.QueryContext(ctx, `SELECT `+cardCols+` FROM mw_cards`+w.sql()+` ORDER BY created_at DESC, card_id LIMIT ? OFFSET ?`,
		append(w.args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Card{}
	for rows.Next() {
		var c Card
		if err := c.scan(rows); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// ListOrders 按创建时间倒序列出单据，并返回满足过滤条件的总数。
func (s *Service) ListOrders(ctx context.Context, f OrderFilter) ([]Order, int, error) {
	var w whereBuilder
	w.eq("state", f.State)
	w.eq("kind", f.Kind)
	w.eq("card_id", f.CardID)
	var total int
	if err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mw_card_orders`+w.sql(), w.args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit, offset := pageArgs(f.Page, f.PageSize)
	rows, err := db.DB.QueryContext(ctx, `SELECT `+orderCols+` FROM mw_card_orders`+w.sql()+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		append(w.args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	orders, err := scanOrders(rows)
	if err != nil {
		return nil, 0, err
	}
	if orders == nil {
		orders = []Order{}
	}
	return orders, total, nil
}

const txnCols = `transaction_id, kind, card_id, status, decline_reason, txn_amount, txn_currency, billing_amount, billing_currency,
	merchant_name, origin_transaction_id, auth_transaction_id, occurred_at, updated_at`

func (t *Transaction) scan(r rowScanner) error {
	return r.Scan(&t.TransactionID, &t.Kind, &t.CardID, &t.Status, &t.DeclineReason, &t.TxnAmount, &t.TxnCurrency,
		&t.BillingAmount, &t.BillingCurrency, &t.MerchantName, &t.OriginTransactionID, &t.AuthTransactionID,
		&t.OccurredAt, &t.UpdatedAt)
}

func queryTransactions(ctx context.Context, where string, args []any, limit, offset int) ([]Transaction, error) {
	rows, err := db.DB.QueryContext(ctx, `SELECT `+txnCols+` FROM mw_card_transactions`+where+
		` ORDER BY COALESCE(occurred_at, updated_at) DESC, transaction_id LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Transaction{}
	for rows.Next() {
		var t Transaction
		if err := t.scan(rows); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTransactions 按发生时间倒序列出卡交易，并返回满足过滤条件的总数。
func (s *Service) ListTransactions(ctx context.Context, f TxnFilter) ([]Transaction, int, error) {
	var w whereBuilder
	w.eq("card_id", f.CardID)
	var total int
	if err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mw_card_transactions`+w.sql(), w.args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit, offset := pageArgs(f.Page, f.PageSize)
	out, err := queryTransactions(ctx, w.sql(), w.args, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// CardDetailView 为卡详情：卡本身及最近 50 条单据与交易。
type CardDetailView struct {
	Card         Card          `json:"card"`
	Orders       []Order       `json:"orders"`
	Transactions []Transaction `json:"transactions"`
}

// CardDetail 返回卡详情。
func (s *Service) CardDetail(ctx context.Context, cardID string) (*CardDetailView, error) {
	card, err := getCard(ctx, cardID)
	if err != nil {
		return nil, err
	}
	orders, _, err := s.ListOrders(ctx, OrderFilter{CardID: cardID, PageSize: detailLimit})
	if err != nil {
		return nil, err
	}
	txns, err := queryTransactions(ctx, " WHERE card_id = ?", []any{cardID}, detailLimit, 0)
	if err != nil {
		return nil, err
	}
	return &CardDetailView{Card: *card, Orders: orders, Transactions: txns}, nil
}

// ---------- 概览 ----------

// ProductStock 为单个卡产品的库存与补卡进度。
type ProductStock struct {
	ProductID   string `json:"product_id"`
	Available   int    `json:"available"`
	Leased      int    `json:"leased"`
	Releasing   int    `json:"releasing"`
	Used        int    `json:"used"`
	Disabled    int    `json:"disabled"`
	Target      int    `json:"target"`
	DailyLimit  int    `json:"daily_limit"`
	OpenedToday int    `json:"opened_today"`
}

// Overview 为后台概览。
type Overview struct {
	MainBalances      []marswallex.BalanceItem `json:"main_balances"`
	BalanceError      string                   `json:"balance_error"`
	Products          []ProductStock           `json:"products"`
	NeedsReview       int                      `json:"needs_review"`
	FailedEvents      int                      `json:"failed_events"`
	SuspendedProducts int                      `json:"suspended_products"`
}

// Overview 汇总主钱包余额、各产品库存与待处理事项。主钱包余额取不到时只写入 BalanceError。
func (s *Service) Overview(ctx context.Context) (*Overview, error) {
	pol, err := LoadPolicy()
	if err != nil {
		return nil, err
	}
	ov := &Overview{MainBalances: []marswallex.BalanceItem{}}

	if c, err := s.opts.Client(); err != nil {
		ov.BalanceError = err.Error()
	} else if page, err := c.ListAccounts(ctx, marswallex.ListAccountsQuery{PageNo: 1, PageSize: 20, AccountType: "MAIN"}); err != nil {
		ov.BalanceError = err.Error()
	} else if len(page.Data) == 0 {
		ov.BalanceError = "未找到主钱包账户"
	} else if bl := page.Data[0].BalanceList; bl != nil {
		ov.MainBalances = bl
	}

	stocks := map[string]*ProductStock{}
	get := func(id string) *ProductStock {
		if st, ok := stocks[id]; ok {
			return st
		}
		st := &ProductStock{ProductID: id}
		stocks[id] = st
		return st
	}
	for id, t := range pol.ProductTargets {
		st := get(id)
		st.Target, st.DailyLimit = t.Target, t.DailyLimit
	}
	rows, err := db.DB.QueryContext(ctx, `SELECT product_id, pool_state, COUNT(*) FROM mw_cards GROUP BY product_id, pool_state`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, state string
		var n int
		if err := rows.Scan(&id, &state, &n); err != nil {
			rows.Close()
			return nil, err
		}
		st := get(id)
		switch state {
		case PoolAvailable:
			st.Available = n
		case PoolLeased:
			st.Leased = n
		case PoolReleasing:
			st.Releasing = n
		case PoolUsed:
			st.Used = n
		case PoolDisabled:
			st.Disabled = n
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	dayStart := beijingDayStart(s.now()).Unix()
	rows, err = db.DB.QueryContext(ctx, `SELECT product_id, COUNT(*) FROM mw_card_orders WHERE kind = ? AND created_at >= ? GROUP BY product_id`,
		KindApply, dayStart)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, err
		}
		// 今天只有开卡单、还没有卡也没有目标数的产品同样列出
		get(id).OpenedToday = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for _, st := range stocks {
		ov.Products = append(ov.Products, *st)
	}
	sort.Slice(ov.Products, func(i, j int) bool { return ov.Products[i].ProductID < ov.Products[j].ProductID })

	counts := []struct {
		dst *int
		q   string
	}{
		{&ov.NeedsReview, `SELECT COUNT(*) FROM mw_card_orders WHERE state = 'needs_review'`},
		{&ov.FailedEvents, `SELECT COUNT(*) FROM marswallex_webhook_events WHERE processed_at IS NULL AND process_attempts >= 10`},
		{&ov.SuspendedProducts, `SELECT COUNT(*) FROM mw_product_health WHERE suspended_at IS NOT NULL AND (resumed_at IS NULL OR resumed_at < suspended_at)`},
	}
	for _, c := range counts {
		if err := db.DB.QueryRowContext(ctx, c.q).Scan(c.dst); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return ov, nil
}
