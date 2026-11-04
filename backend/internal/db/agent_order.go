package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrAgentOrderExists 同一个代理商下这个请求号已经有订单（(agent_id, request_no) 撞唯一约束）。
var ErrAgentOrderExists = errors.New("该请求号已下过单")

// AgentOrder 是 agent_orders 的一行。商品信息在下单时快照进订单，之后商品改名、下架都不影响历史订单。
type AgentOrder struct {
	ID, AgentID, ProductID                                       int64
	ProductCode, Provider, Product, PlanKey, Region, ProductName string
	Quantity                                                     int
	UnitPriceCents, TotalCents                                   int64
	RequestNo, Note                                              string
	CreatedAt                                                    int64
}

// NewAgentOrder 是下单需要的内容。数量就是 len(Codes)；码已经由调用方生成并加密好，这里只写库。
type NewAgentOrder struct {
	AgentID        int64
	Product        AgentProduct
	UnitPriceCents int64
	RequestNo      string
	Note, Operator string
	Codes          []NewLocalCDK
}

const agentOrderCols = `id, agent_id, product_id, product_code, provider, product, plan_key, region, product_name,
	quantity, unit_price_cents, total_cents, request_no, note, created_at`

func scanAgentOrder(s rowScanner) (AgentOrder, error) {
	var o AgentOrder
	err := s.Scan(&o.ID, &o.AgentID, &o.ProductID, &o.ProductCode, &o.Provider, &o.Product, &o.PlanKey, &o.Region, &o.ProductName,
		&o.Quantity, &o.UnitPriceCents, &o.TotalCents, &o.RequestNo, &o.Note, &o.CreatedAt)
	return o, err
}

// CreateAgentOrder 在一个事务里：插订单 → 扣余额并写流水（流水带订单 ID）→ 逐张写码 → 提交。
// 任何一步失败都整体回滚，不会出现扣了钱没有码，或者有码没扣钱。
// 请求号重复返回 ErrAgentOrderExists；账号停用返回 ErrAgentDisabled（先于余额不足）；余额不够返回 *InsufficientBalanceError。
// 返回订单和按顺序排列的码 ID。
func CreateAgentOrder(ctx context.Context, o NewAgentOrder, now time.Time) (AgentOrder, []int64, error) {
	qty := len(o.Codes)
	if qty == 0 {
		return AgentOrder{}, nil, errors.New("下单缺少码")
	}
	if o.UnitPriceCents <= 0 {
		return AgentOrder{}, nil, errors.New("单价必须大于 0")
	}
	total := o.UnitPriceCents * int64(qty)
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return AgentOrder{}, nil, err
	}
	defer tx.Rollback()

	ord := AgentOrder{AgentID: o.AgentID, ProductID: o.Product.ID, ProductCode: o.Product.Code, Provider: o.Product.Provider,
		Product: o.Product.Product, PlanKey: o.Product.PlanKey, Region: o.Product.Region, ProductName: o.Product.Name,
		Quantity: qty, UnitPriceCents: o.UnitPriceCents, TotalCents: total, RequestNo: o.RequestNo, Note: o.Note, CreatedAt: now.Unix()}
	res, err := tx.ExecContext(ctx, `INSERT INTO agent_orders (agent_id, product_id, product_code, provider, product, plan_key, region, product_name,
		quantity, unit_price_cents, total_cents, request_no, note, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ord.AgentID, ord.ProductID, ord.ProductCode, ord.Provider, ord.Product, ord.PlanKey, ord.Region, ord.ProductName,
		ord.Quantity, ord.UnitPriceCents, ord.TotalCents, ord.RequestNo, ord.Note, ord.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return AgentOrder{}, nil, ErrAgentOrderExists
		}
		return AgentOrder{}, nil, err
	}
	if ord.ID, err = res.LastInsertId(); err != nil {
		return AgentOrder{}, nil, err
	}
	orderID := ord.ID
	if _, err := applyAgentBalance(ctx, tx, LedgerEntry{AgentID: o.AgentID, Kind: LedgerPurchase, AmountCents: -total,
		OrderID: &orderID, Operator: o.Operator}, true, now.Unix()); err != nil {
		return AgentOrder{}, nil, err
	}
	ids := make([]int64, 0, qty)
	for _, c := range o.Codes {
		c.AgentID, c.AgentOrderID = o.AgentID, orderID
		id, err := insertLocalCDK(ctx, tx, c, now.Unix())
		if err != nil {
			return AgentOrder{}, nil, err
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		return AgentOrder{}, nil, err
	}
	return ord, ids, nil
}

// GetAgentOrderByRequestNo 按请求号查这个代理商的订单，第二个返回值表示有没有。
func GetAgentOrderByRequestNo(ctx context.Context, agentID int64, requestNo string) (AgentOrder, bool, error) {
	o, err := scanAgentOrder(DB.QueryRowContext(ctx, `SELECT `+agentOrderCols+` FROM agent_orders WHERE agent_id = ? AND request_no = ?`, agentID, requestNo))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentOrder{}, false, nil
	}
	return o, err == nil, err
}

// GetAgentOrder 取这个代理商的一笔订单；别人的订单按不存在处理，返回 sql.ErrNoRows。
func GetAgentOrder(ctx context.Context, agentID, orderID int64) (AgentOrder, error) {
	return scanAgentOrder(DB.QueryRowContext(ctx, `SELECT `+agentOrderCols+` FROM agent_orders WHERE agent_id = ? AND id = ?`, agentID, orderID))
}

// ListAgentOrders 按 id 降序列出这个代理商的订单，并返回总数。limit、offset 的范围由接口层校验。
func ListAgentOrders(ctx context.Context, agentID int64, limit, offset int) ([]AgentOrder, int, error) {
	var total int
	if err := DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_orders WHERE agent_id = ?`, agentID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := DB.QueryContext(ctx, `SELECT `+agentOrderCols+` FROM agent_orders WHERE agent_id = ? ORDER BY id DESC LIMIT ? OFFSET ?`, agentID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []AgentOrder{}
	for rows.Next() {
		o, err := scanAgentOrder(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, o)
	}
	return out, total, rows.Err()
}

// SealedLocalCDK 是一张码的哈希和完整码密文，给代理商导出时解密用。
type SealedLocalCDK struct {
	ID               int64
	CodeHash, Sealed string
}

// LocalCDKSealedForAgent 取这个代理商名下指定 ID 的码密文，按 id 升序。条件里固定带 agent_id，别人的 ID 不会返回。
func LocalCDKSealedForAgent(ctx context.Context, agentID int64, ids []int64) ([]SealedLocalCDK, error) {
	out := []SealedLocalCDK{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, agentID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := DB.QueryContext(ctx, `SELECT id, code_hash, code_sealed FROM local_cdks WHERE agent_id = ? AND id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s SealedLocalCDK
		if err := rows.Scan(&s.ID, &s.CodeHash, &s.Sealed); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// LocalCDKIDsForAgentOrder 这笔订单当时发出的码 ID，按 id 升序（不含换码换出的新码，数量恒等于订单数量）；订单不属于这个代理商时没有结果。
func LocalCDKIDsForAgentOrder(ctx context.Context, agentID, orderID int64) ([]int64, error) {
	rows, err := DB.QueryContext(ctx, `SELECT id FROM local_cdks WHERE agent_id = ? AND agent_order_id = ? AND exchanged_from IS NULL ORDER BY id`, agentID, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
