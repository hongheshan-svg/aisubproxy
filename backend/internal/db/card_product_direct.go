package db

import "fmt"

// UpsertDirectCardProduct 供 /gpt-direct/card-products 同步使用（C-6）。
// 这个接口只返回 product_code、issuer、bin、label 和可用状态，不返回卡组织、发卡地区、
// 场景、卡组和 BIN 头。冲突时只更新它给出的字段，已有的元数据保留，避免被空值覆盖。
func UpsertDirectCardProduct(p CardProductCache) error {
	if DB == nil {
		return fmt.Errorf("db not ready")
	}
	onlineInt := 0
	if p.Enabled {
		onlineInt = 1
	}
	_, err := DB.Exec(`
		INSERT INTO card_product_cache
			(product_code, issuer, bin, description, enabled, suspended_at, synced_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(product_code) DO UPDATE SET
			issuer = excluded.issuer,
			bin = excluded.bin,
			description = excluded.description,
			enabled = excluded.enabled,
			suspended_at = excluded.suspended_at,
			synced_at = excluded.synced_at
	`, p.ProductCode, p.Issuer, p.BIN, p.Description, onlineInt, p.SuspendedAt)
	return err
}
