package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mattn/go-sqlite3"
)

// 代理商的等级、批发商品和价格。价格取法：先看这个代理商的覆盖价，再看他所在等级的价格，都没有就不能买。

var (
	ErrAgentTierNotFound    = errors.New("等级不存在")
	ErrAgentTierNameTaken   = errors.New("等级名已被使用")
	ErrAgentTierInUse       = errors.New("还有代理商在用这个等级，不能删除")
	ErrAgentProductNotFound = errors.New("商品不存在")
	ErrAgentProductExists   = errors.New("商品编码或「服务商+产品线+套餐+地区」已存在")
)

func agentBoolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueViolation(err error) bool {
	var se sqlite3.Error
	return errors.As(err, &se) && (se.ExtendedCode == sqlite3.ErrConstraintUnique || se.ExtendedCode == sqlite3.ErrConstraintPrimaryKey)
}

// ---- 等级 ----

type AgentTier struct {
	ID         int64
	Name, Note string
	AgentCount int
	CreatedAt  int64
	UpdatedAt  int64
}

func CreateAgentTier(ctx context.Context, name, note string, now time.Time) (int64, error) {
	res, err := DB.ExecContext(ctx, `INSERT INTO agent_tiers (name, note, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		name, note, now.Unix(), now.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrAgentTierNameTaken
		}
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateAgentTier(ctx context.Context, id int64, name, note string, now time.Time) error {
	res, err := DB.ExecContext(ctx, `UPDATE agent_tiers SET name = ?, note = ?, updated_at = ? WHERE id = ?`,
		name, note, now.Unix(), id)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAgentTierNameTaken
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAgentTierNotFound
	}
	return nil
}

// DeleteAgentTier 删除等级和它的价格表；还有代理商在用时拒绝。
// 检查和删除在同一个事务里，避免检查之后又有人把代理商挂到这个等级上。
func DeleteAgentTier(ctx context.Context, id int64) error {
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM agent_tiers WHERE id = ? AND NOT EXISTS (SELECT 1 FROM agents WHERE tier_id = ?)`, id, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_tiers WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrAgentTierNotFound
		}
		return ErrAgentTierInUse
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_tier_prices WHERE tier_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func ListAgentTiers(ctx context.Context) ([]AgentTier, error) {
	rows, err := DB.QueryContext(ctx, `SELECT t.id, t.name, t.note, t.created_at, t.updated_at,
		(SELECT COUNT(*) FROM agents a WHERE a.tier_id = t.id) FROM agent_tiers t ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentTier
	for rows.Next() {
		var t AgentTier
		if err := rows.Scan(&t.ID, &t.Name, &t.Note, &t.CreatedAt, &t.UpdatedAt, &t.AgentCount); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---- 商品 ----

type AgentProduct struct {
	ID                                             int64
	Code, Provider, Product, PlanKey, Region, Name string
	Enabled                                        bool
	Sort                                           int
	CreatedAt                                      int64
	UpdatedAt                                      int64
}

const agentProductCols = `id, code, provider, product, plan_key, region, name, enabled, sort, created_at, updated_at`

func scanAgentProduct(s interface{ Scan(...any) error }) (AgentProduct, error) {
	var p AgentProduct
	var enabled int
	err := s.Scan(&p.ID, &p.Code, &p.Provider, &p.Product, &p.PlanKey, &p.Region, &p.Name, &enabled, &p.Sort, &p.CreatedAt, &p.UpdatedAt)
	p.Enabled = enabled != 0
	return p, err
}

func CreateAgentProduct(ctx context.Context, p AgentProduct, now time.Time) (int64, error) {
	res, err := DB.ExecContext(ctx, `INSERT INTO agent_products (code, provider, product, plan_key, region, name, enabled, sort, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Code, p.Provider, p.Product, p.PlanKey, p.Region, p.Name, agentBoolInt(p.Enabled), p.Sort, now.Unix(), now.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrAgentProductExists
		}
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateAgentProduct 只改显示名、上架状态和排序；编码、服务商、产品线、套餐、地区建好后不能改（订单里引用了它们）。
func UpdateAgentProduct(ctx context.Context, id int64, name string, enabled bool, sort int, now time.Time) error {
	res, err := DB.ExecContext(ctx, `UPDATE agent_products SET name = ?, enabled = ?, sort = ?, updated_at = ? WHERE id = ?`,
		name, agentBoolInt(enabled), sort, now.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAgentProductNotFound
	}
	return nil
}

func ListAgentProducts(ctx context.Context) ([]AgentProduct, error) {
	rows, err := DB.QueryContext(ctx, `SELECT `+agentProductCols+` FROM agent_products ORDER BY sort, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentProduct
	for rows.Next() {
		p, err := scanAgentProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func GetAgentProductByCode(ctx context.Context, code string) (AgentProduct, error) {
	p, err := scanAgentProduct(DB.QueryRowContext(ctx, `SELECT `+agentProductCols+` FROM agent_products WHERE code = ?`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentProduct{}, ErrAgentProductNotFound
	}
	return p, err
}

// ---- 价格 ----

type AgentPriceCell struct{ TierID, ProductID, PriceCents int64 }

type AgentOverride struct{ ProductID, PriceCents int64 }

func ListAgentTierPrices(ctx context.Context) ([]AgentPriceCell, error) {
	rows, err := DB.QueryContext(ctx, `SELECT tier_id, product_id, price_cents FROM agent_tier_prices ORDER BY tier_id, product_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentPriceCell
	for rows.Next() {
		var c AgentPriceCell
		if err := rows.Scan(&c.TierID, &c.ProductID, &c.PriceCents); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func rowExists(ctx context.Context, tx *sql.Tx, table string, id int64) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// PutAgentTierPrices 在一个事务里批量写价：大于 0 写入或覆盖，等于 0 删除，小于 0 报错并整批回滚。
func PutAgentTierPrices(ctx context.Context, cells []AgentPriceCell, now time.Time) error {
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range cells {
		if c.PriceCents < 0 {
			return fmt.Errorf("价格不能为负: 等级 %d 商品 %d", c.TierID, c.ProductID)
		}
		if ok, err := rowExists(ctx, tx, "agent_tiers", c.TierID); err != nil {
			return err
		} else if !ok {
			return ErrAgentTierNotFound
		}
		if ok, err := rowExists(ctx, tx, "agent_products", c.ProductID); err != nil {
			return err
		} else if !ok {
			return ErrAgentProductNotFound
		}
		if c.PriceCents == 0 {
			_, err = tx.ExecContext(ctx, `DELETE FROM agent_tier_prices WHERE tier_id = ? AND product_id = ?`, c.TierID, c.ProductID)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO agent_tier_prices (tier_id, product_id, price_cents, updated_at) VALUES (?, ?, ?, ?)
				ON CONFLICT(tier_id, product_id) DO UPDATE SET price_cents = excluded.price_cents, updated_at = excluded.updated_at`,
				c.TierID, c.ProductID, c.PriceCents, now.Unix())
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ListAgentOverrides(ctx context.Context, agentID int64) ([]AgentOverride, error) {
	rows, err := DB.QueryContext(ctx, `SELECT product_id, price_cents FROM agent_price_overrides WHERE agent_id = ? ORDER BY product_id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentOverride
	for rows.Next() {
		var o AgentOverride
		if err := rows.Scan(&o.ProductID, &o.PriceCents); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// PutAgentOverrides 语义同 PutAgentTierPrices：大于 0 写入或覆盖，等于 0 删除，小于 0 报错，整批一个事务。
// 代理商本身是否存在由调用方保证（后台接口先查过）。
func PutAgentOverrides(ctx context.Context, agentID int64, cells []AgentOverride, now time.Time) error {
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range cells {
		if c.PriceCents < 0 {
			return fmt.Errorf("价格不能为负: 商品 %d", c.ProductID)
		}
		if ok, err := rowExists(ctx, tx, "agent_products", c.ProductID); err != nil {
			return err
		} else if !ok {
			return ErrAgentProductNotFound
		}
		if c.PriceCents == 0 {
			_, err = tx.ExecContext(ctx, `DELETE FROM agent_price_overrides WHERE agent_id = ? AND product_id = ?`, agentID, c.ProductID)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO agent_price_overrides (agent_id, product_id, price_cents, updated_at) VALUES (?, ?, ?, ?)
				ON CONFLICT(agent_id, product_id) DO UPDATE SET price_cents = excluded.price_cents, updated_at = excluded.updated_at`,
				agentID, c.ProductID, c.PriceCents, now.Unix())
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AgentPriceFor 取价：先看覆盖价，再看代理商所在等级的价格；都没有返回 0, false。
func AgentPriceFor(ctx context.Context, agentID, productID int64) (cents int64, ok bool, err error) {
	return agentPriceScan(ctx, agentID, productID)
}

func agentPriceScan(ctx context.Context, agentID, productID int64) (int64, bool, error) {
	var v sql.NullInt64
	err := DB.QueryRowContext(ctx, `SELECT COALESCE(
		(SELECT price_cents FROM agent_price_overrides WHERE agent_id = ?1 AND product_id = ?2),
		(SELECT p.price_cents FROM agent_tier_prices p JOIN agents a ON a.tier_id = p.tier_id WHERE a.id = ?1 AND p.product_id = ?2))`,
		agentID, productID).Scan(&v)
	if err != nil {
		return 0, false, err
	}
	if !v.Valid {
		return 0, false, nil
	}
	return v.Int64, true, nil
}

type PurchasableProduct struct {
	AgentProduct
	PriceCents int64
}

// ListAgentPurchasable 列出这个代理商买得了的商品：已上架且取得到价格。按 sort、id 升序。
func ListAgentPurchasable(ctx context.Context, agentID int64) ([]PurchasableProduct, error) {
	rows, err := DB.QueryContext(ctx, `SELECT pr.id, pr.code, pr.provider, pr.product, pr.plan_key, pr.region, pr.name, pr.enabled, pr.sort, pr.created_at, pr.updated_at,
		COALESCE(o.price_cents, t.price_cents)
		FROM agent_products pr
		LEFT JOIN agent_price_overrides o ON o.product_id = pr.id AND o.agent_id = ?1
		LEFT JOIN agents a ON a.id = ?1
		LEFT JOIN agent_tier_prices t ON t.product_id = pr.id AND t.tier_id = a.tier_id
		WHERE pr.enabled = 1 AND COALESCE(o.price_cents, t.price_cents) IS NOT NULL
		ORDER BY pr.sort, pr.id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PurchasableProduct
	for rows.Next() {
		var p PurchasableProduct
		var enabled int
		if err := rows.Scan(&p.ID, &p.Code, &p.Provider, &p.Product, &p.PlanKey, &p.Region, &p.Name, &enabled, &p.Sort, &p.CreatedAt, &p.UpdatedAt, &p.PriceCents); err != nil {
			return nil, err
		}
		p.Enabled = enabled != 0
		out = append(out, p)
	}
	return out, rows.Err()
}
