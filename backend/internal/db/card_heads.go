package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// 卡头来源：api 由服务商接口同步，manual 由管理员在后台维护。
const (
	CardHeadSourceAPI    = "api"
	CardHeadSourceManual = "manual"
)

var (
	ErrCardHeadNotFound  = errors.New("卡头不存在")
	ErrCardHeadNotManual = errors.New("这条卡头由服务商接口同步，不能手动修改产品信息或删除")
)

// CardHead 是某家服务商的一个卡产品（卡头）。three_ds、apple_pay 取值 ”（未标注）、yes、no，只用于显示。
type CardHead struct {
	Provider  string   `json:"provider"`
	ProductID string   `json:"product_id"`
	Source    string   `json:"source"`
	Name      string   `json:"name"`
	CardHeads []string `json:"card_heads"`
	Network   string   `json:"network"`
	Country   string   `json:"country"`
	Currency  string   `json:"currency"`
	Listed    bool     `json:"listed"`
	ThreeDS   string   `json:"three_ds"`
	ApplePay  string   `json:"apple_pay"`
	Note      string   `json:"note"`
	SyncedAt  int64    `json:"synced_at"`
	UpdatedAt int64    `json:"updated_at"`
	UpdatedBy string   `json:"updated_by"`
}

// CardHeadProduct 是服务商接口返回的一个产品，同步时写入产品信息部分。
type CardHeadProduct struct {
	ProductID string
	Name      string
	CardHeads []string
	Network   string
	Country   string
	Currency  string
}

func MigrateCardHeads() error {
	_, err := DB.Exec(`CREATE TABLE IF NOT EXISTS provider_card_heads (
	provider TEXT NOT NULL,
	product_id TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT 'manual',
	name TEXT NOT NULL DEFAULT '',
	card_heads TEXT NOT NULL DEFAULT '[]',
	network TEXT NOT NULL DEFAULT '',
	country TEXT NOT NULL DEFAULT '',
	currency TEXT NOT NULL DEFAULT '',
	listed INTEGER NOT NULL DEFAULT 1,
	three_ds TEXT NOT NULL DEFAULT '',
	apple_pay TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	synced_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0,
	updated_by TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (provider, product_id)
)`)
	return err
}

const cardHeadCols = `provider, product_id, source, name, card_heads, network, country, currency, listed, three_ds, apple_pay, note, synced_at, updated_at, updated_by`

type rowScanner interface{ Scan(dest ...any) error }

func scanCardHead(r rowScanner) (CardHead, error) {
	var h CardHead
	var heads string
	var listed int
	if err := r.Scan(&h.Provider, &h.ProductID, &h.Source, &h.Name, &heads, &h.Network, &h.Country, &h.Currency,
		&listed, &h.ThreeDS, &h.ApplePay, &h.Note, &h.SyncedAt, &h.UpdatedAt, &h.UpdatedBy); err != nil {
		return h, err
	}
	h.Listed = listed != 0
	if err := json.Unmarshal([]byte(heads), &h.CardHeads); err != nil || h.CardHeads == nil {
		h.CardHeads = []string{}
	}
	return h, nil
}

func headsJSON(heads []string) string {
	if heads == nil {
		heads = []string{}
	}
	b, _ := json.Marshal(heads)
	return string(b)
}

// ListCardHeads 列出一家服务商的卡头：接口仍返回的在前，再按产品 ID 排序。
func ListCardHeads(provider string) ([]CardHead, error) {
	rows, err := DB.Query(`SELECT `+cardHeadCols+` FROM provider_card_heads WHERE provider = ? ORDER BY listed DESC, product_id`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CardHead{}
	for rows.Next() {
		h, err := scanCardHead(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func GetCardHead(provider, productID string) (CardHead, error) {
	h, err := scanCardHead(DB.QueryRow(`SELECT `+cardHeadCols+` FROM provider_card_heads WHERE provider = ? AND product_id = ?`, provider, productID))
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrCardHeadNotFound
	}
	return h, err
}

// SyncCardHeads 把接口返回的产品写入表里：只改产品信息，不改标注；同 ID 的手动记录转成 api 来源；
// 这家服务商 api 来源但本次没返回的标 listed=0，不删除。全部在一个事务里完成。
func SyncCardHeads(provider string, items []CardHeadProduct, now int64) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // 提交后回滚是空操作
	ids := make([]any, 0, len(items)+1)
	ids = append(ids, provider)
	for _, it := range items {
		if _, err := tx.Exec(`INSERT INTO provider_card_heads (provider, product_id, source, name, card_heads, network, country, currency, listed, synced_at)
VALUES (?, ?, 'api', ?, ?, ?, ?, ?, 1, ?)
ON CONFLICT(provider, product_id) DO UPDATE SET source = 'api', name = excluded.name, card_heads = excluded.card_heads,
	network = excluded.network, country = excluded.country, currency = excluded.currency, listed = 1, synced_at = excluded.synced_at`,
			provider, it.ProductID, it.Name, headsJSON(it.CardHeads), it.Network, it.Country, it.Currency, now); err != nil {
			return fmt.Errorf("写入卡头 %s: %w", it.ProductID, err)
		}
		ids = append(ids, it.ProductID)
	}
	q := `UPDATE provider_card_heads SET listed = 0 WHERE provider = ? AND source = 'api'`
	if len(items) > 0 {
		q += ` AND product_id NOT IN (?` + strings.Repeat(`, ?`, len(items)-1) + `)`
	}
	if _, err := tx.Exec(q, ids...); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateCardHeadNotes 只改标注（3DS、Apple Pay、备注），两种来源都可以。
func UpdateCardHeadNotes(provider, productID, threeDS, applePay, note, by string, now int64) error {
	res, err := DB.Exec(`UPDATE provider_card_heads SET three_ds = ?, apple_pay = ?, note = ?, updated_at = ?, updated_by = ?
WHERE provider = ? AND product_id = ?`, threeDS, applePay, note, now, by, provider, productID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCardHeadNotFound
	}
	return nil
}

// SaveManualCardHead 新增或整条更新一条手动卡头；同 ID 已是 api 来源时拒绝。
func SaveManualCardHead(h CardHead, by string, now int64) error {
	res, err := DB.Exec(`INSERT INTO provider_card_heads (provider, product_id, source, name, card_heads, network, country, currency, listed, three_ds, apple_pay, note, updated_at, updated_by)
VALUES (?, ?, 'manual', ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)
ON CONFLICT(provider, product_id) DO UPDATE SET name = excluded.name, card_heads = excluded.card_heads, network = excluded.network,
	country = excluded.country, currency = excluded.currency, three_ds = excluded.three_ds, apple_pay = excluded.apple_pay,
	note = excluded.note, updated_at = excluded.updated_at, updated_by = excluded.updated_by
WHERE provider_card_heads.source = 'manual'`,
		h.Provider, h.ProductID, h.Name, headsJSON(h.CardHeads), h.Network, h.Country, h.Currency, h.ThreeDS, h.ApplePay, h.Note, now, by)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCardHeadNotManual
	}
	return nil
}

// DeleteManualCardHead 只能删手动添加的卡头。
func DeleteManualCardHead(provider, productID string) error {
	h, err := GetCardHead(provider, productID)
	if err != nil {
		return err
	}
	if h.Source != CardHeadSourceManual {
		return ErrCardHeadNotManual
	}
	_, err = DB.Exec(`DELETE FROM provider_card_heads WHERE provider = ? AND product_id = ? AND source = 'manual'`, provider, productID)
	return err
}
