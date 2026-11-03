package db

import (
	"context"
	"database/sql"
	"errors"
)

// PayProbeKeep 是免付款探测记录最多保留的条数。
const PayProbeKeep = 50

// PayProbe 是一条免付款探测记录。表里不存会话、完整邮箱、卡数据或任何 token：
// ChecksJSON、ResultsJSON 由调用方脱敏后再写入。
type PayProbe struct {
	ID          int64  `json:"id"`
	CreatedAt   int64  `json:"created_at"`
	Admin       string `json:"admin"`
	Region      string `json:"region"`
	ChecksJSON  string `json:"-"`
	ResultsJSON string `json:"-"`
	ShotDir     string `json:"shot_dir"` // 截图子目录名，没有截图为空
}

// MigratePayProbes 建免付款探测记录表。可重复执行。
func MigratePayProbes() error {
	_, err := DB.Exec(`CREATE TABLE IF NOT EXISTS pay_probes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	created_at INTEGER NOT NULL,
	admin TEXT NOT NULL DEFAULT '',
	region TEXT NOT NULL DEFAULT '',
	checks TEXT NOT NULL DEFAULT '[]',
	results TEXT NOT NULL DEFAULT '[]',
	shot_dir TEXT NOT NULL DEFAULT ''
)`)
	return err
}

// InsertPayProbe 写入一条记录并只保留最近 PayProbeKeep 条，返回新记录 id 和被淘汰记录的截图目录（调用方负责删目录）。
func InsertPayProbe(ctx context.Context, p *PayProbe) (pruned []string, err error) {
	tx, err := DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	res, err := tx.ExecContext(ctx, `INSERT INTO pay_probes (created_at, admin, region, checks, results, shot_dir) VALUES (?,?,?,?,?,?)`,
		p.CreatedAt, p.Admin, p.Region, p.ChecksJSON, p.ResultsJSON, p.ShotDir)
	if err != nil {
		return nil, err
	}
	if p.ID, err = res.LastInsertId(); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT shot_dir FROM pay_probes WHERE id NOT IN (SELECT id FROM pay_probes ORDER BY id DESC LIMIT ?) AND shot_dir != ''`, PayProbeKeep)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d string
		if err = rows.Scan(&d); err != nil {
			rows.Close()
			return nil, err
		}
		pruned = append(pruned, d)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if _, err = tx.ExecContext(ctx, `DELETE FROM pay_probes WHERE id NOT IN (SELECT id FROM pay_probes ORDER BY id DESC LIMIT ?)`, PayProbeKeep); err != nil {
		return nil, err
	}
	return pruned, tx.Commit()
}

// ListPayProbes 按 id 倒序返回最近的记录（最多 PayProbeKeep 条）。
func ListPayProbes(ctx context.Context) ([]PayProbe, error) {
	rows, err := DB.QueryContext(ctx, `SELECT id, created_at, admin, region, checks, results, shot_dir FROM pay_probes ORDER BY id DESC LIMIT ?`, PayProbeKeep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PayProbe
	for rows.Next() {
		var p PayProbe
		if err := rows.Scan(&p.ID, &p.CreatedAt, &p.Admin, &p.Region, &p.ChecksJSON, &p.ResultsJSON, &p.ShotDir); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPayProbe 读一条记录；不存在返回 (nil, nil)。
func GetPayProbe(ctx context.Context, id int64) (*PayProbe, error) {
	var p PayProbe
	err := DB.QueryRowContext(ctx, `SELECT id, created_at, admin, region, checks, results, shot_dir FROM pay_probes WHERE id = ?`, id).
		Scan(&p.ID, &p.CreatedAt, &p.Admin, &p.Region, &p.ChecksJSON, &p.ResultsJSON, &p.ShotDir)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
