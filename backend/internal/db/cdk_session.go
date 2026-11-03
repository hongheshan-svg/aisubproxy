package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// cdk_session_bindings 存「卡密 ↔ redemption_token ↔ 用户 ChatGPT 会话」。
//
// 会话等同于登录态，只以 secretbox 密文保存，订单完成或绑定满 7 天清空；主密钥缺失时拒绝写入，绝不降级成明文。
// 清空的只是 session_payload：redemption_token 要留着给兑换进度查询，账号邮箱单独加密留着给账单查询和卡健康归因，
// 这些路径都不依赖会话本身（兑换时会话由前端直接随预检发给卡台，本站从不回读它去兑换）。
const (
	cdkSessionAAD      = "cdk_session_binding"
	cdkSessionEmailAAD = "cdk_session_binding_email"
	sealedPrefix       = "v1:"

	// settingCDKSessionVacuum 记着「密文已写入，但数据库文件的空闲页里可能还留着旧明文，要 VACUUM 一次」。
	// 单独落库：迁移中途崩溃后，重跑时已经没有明文行了，靠它才知道还欠一次 VACUUM。
	settingCDKSessionVacuum = "cdk_session_vacuum_pending"

	cdkSessionMigrateBatch = 200

	// CDKSessionTTL 是会话的最长保留时间，从用户提交会话时算起。
	CDKSessionTTL = 7 * 24 * time.Hour
)

// ExtractSessionEmail 从 session JSON 提取 user.email；不是 JSON 或没有邮箱时返回空串。
func ExtractSessionEmail(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "{") {
		return ""
	}
	var data map[string]any
	if json.Unmarshal([]byte(s), &data) != nil {
		return ""
	}
	if user, ok := data["user"].(map[string]any); ok {
		if email, ok := user["email"].(string); ok {
			return strings.TrimSpace(email)
		}
	}
	if email, ok := data["email"].(string); ok {
		return strings.TrimSpace(email)
	}
	return ""
}

func isSealedSession(s string) bool { return strings.HasPrefix(s, sealedPrefix) }

// sealSessionEmail 加密会话里的账号邮箱；会话里没有邮箱时返回空串。
func sealSessionEmail(key []byte, sess string) (string, error) {
	email := ExtractSessionEmail(sess)
	if email == "" {
		return "", nil
	}
	return secretbox.Seal(key, cdkSessionEmailAAD, email)
}

// openStoredSession 把库里的值还原成会话原文。未迁移的旧明文原样返回（只在主密钥缺失、迁移被跳过时才会有）。
func openStoredSession(stored string) (string, error) {
	if stored == "" || !isSealedSession(stored) {
		return stored, nil
	}
	key, err := secretbox.LoadKey()
	if err != nil {
		return "", err
	}
	return secretbox.Open(key, cdkSessionAAD, stored)
}

// openStoredEmail 取账号邮箱：优先单独加密的那一列，其次未迁移的旧明文会话。
func openStoredEmail(sealedEmail, stored string) (string, error) {
	if sealedEmail != "" {
		key, err := secretbox.LoadKey()
		if err != nil {
			return "", err
		}
		return secretbox.Open(key, cdkSessionEmailAAD, sealedEmail)
	}
	if stored != "" && !isSealedSession(stored) {
		return ExtractSessionEmail(stored), nil
	}
	return "", nil
}

// BindCDKSession 预检/兑换时写入 session（可按码或 redemption_token 关联）。
// 只写密文；主密钥缺失或不合法时返回错误且不写任何东西。调用方记日志后照常继续：兑换本身不依赖这份副本。
func BindCDKSession(cdkCode, redemptionToken, sessionPayload string) error {
	if DB == nil {
		return fmt.Errorf("db not ready")
	}
	code := normalizeCDKCode(cdkCode)
	tok := strings.TrimSpace(redemptionToken)
	sess := strings.TrimSpace(sessionPayload)
	if sess == "" || (code == "" && tok == "") {
		return nil
	}
	key, err := secretbox.LoadKey()
	if err != nil {
		return fmt.Errorf("拒绝保存会话: %w", err)
	}
	sealed, err := secretbox.Seal(key, cdkSessionAAD, sess)
	if err != nil {
		return fmt.Errorf("加密会话失败: %w", err)
	}
	email, err := sealSessionEmail(key, sess)
	if err != nil {
		return fmt.Errorf("加密邮箱失败: %w", err)
	}
	now := time.Now().Unix()
	// 优先已知码
	if code != "" {
		_, err := DB.Exec(`
			INSERT INTO cdk_session_bindings (cdk_code, session_payload, redemption_token, account_email_sealed, session_bound_at, updated_at)
			VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT(cdk_code) DO UPDATE SET
				session_payload = excluded.session_payload,
				account_email_sealed = excluded.account_email_sealed,
				session_bound_at = excluded.session_bound_at,
				redemption_token = COALESCE(NULLIF(excluded.redemption_token,''), cdk_session_bindings.redemption_token),
				updated_at = CURRENT_TIMESTAMP
		`, code, sealed, tok, email, now)
		return err
	}
	// 仅有 token：更新已绑定行
	res, err := DB.Exec(`
		UPDATE cdk_session_bindings
		SET session_payload = ?, account_email_sealed = ?, session_bound_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE redemption_token = ?
	`, sealed, email, now, tok)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	// 尚无行：用 token 伪作 key 前缀保存（账单页仍建议用完整码）
	_, err = DB.Exec(`
		INSERT INTO cdk_session_bindings (cdk_code, session_payload, redemption_token, account_email_sealed, session_bound_at, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, "RT:"+tok, sealed, tok, email, now)
	return err
}

// GetSessionByCDK 凭卡密取绑定 session 的原文；没有绑定或已清空时返回空串。解不开（主密钥缺失或已更换）时返回错误。
func GetSessionByCDK(cdkCode string) (string, error) {
	if DB == nil {
		return "", fmt.Errorf("db not ready")
	}
	code := normalizeCDKCode(cdkCode)
	if code == "" {
		return "", nil
	}
	var stored string
	err := DB.QueryRow(`
		SELECT session_payload FROM cdk_session_bindings
		WHERE cdk_code = ? AND TRIM(session_payload) != ''
	`, code).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return openStoredSession(stored)
}

// GetAccountEmailByCDK 凭卡密取绑定时记下的账号邮箱（完整、未脱敏）。会话清空后仍可取到。
func GetAccountEmailByCDK(cdkCode string) (string, error) {
	if DB == nil {
		return "", fmt.Errorf("db not ready")
	}
	code := normalizeCDKCode(cdkCode)
	if code == "" {
		return "", nil
	}
	var stored, sealedEmail string
	err := DB.QueryRow(`SELECT session_payload, account_email_sealed FROM cdk_session_bindings WHERE cdk_code = ?`, code).Scan(&stored, &sealedEmail)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return openStoredEmail(sealedEmail, stored)
}

// CDKBinding 本地码 ↔ token ↔ session 绑定（公开兑换进度/账单依赖）。
type CDKBinding struct {
	CDKCode         string
	RedemptionToken string
	SessionPayload  string // 会话原文；已清空或解不开时为空。不要写进日志、响应或审计
	HasSession      bool   // 库里是否还留着会话（不管当前能不能解开）
	AccountEmail    string // 绑定时记下的账号邮箱；会话清空后仍在，解不开时为空
	UpdatedAt       string
}

// GetBindingByCDK 按卡密取绑定（session 可为空：仅 preview 过）。
// 主密钥缺失或解密失败不算错误，只是会话和邮箱为空：兑换进度查询只靠 token，不能因此失败。
func GetBindingByCDK(cdkCode string) (*CDKBinding, error) {
	if DB == nil {
		return nil, fmt.Errorf("db not ready")
	}
	code := normalizeCDKCode(cdkCode)
	if code == "" {
		return nil, nil
	}
	var b CDKBinding
	var stored, sealedEmail string
	err := DB.QueryRow(`
		SELECT cdk_code, COALESCE(redemption_token,''), COALESCE(session_payload,''), account_email_sealed, COALESCE(updated_at,'')
		FROM cdk_session_bindings
		WHERE cdk_code = ?
	`, code).Scan(&b.CDKCode, &b.RedemptionToken, &stored, &sealedEmail, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.HasSession = strings.TrimSpace(stored) != ""
	if s, err := openStoredSession(stored); err == nil {
		b.SessionPayload = s
	}
	if em, err := openStoredEmail(sealedEmail, stored); err == nil {
		b.AccountEmail = em
	}
	return &b, nil
}

// clearCDKSessionRowIf 把该行的会话清空，但只在库里的值仍等于 expected 时生效：读取之后用户提交了新会话的话，新会话不能被清掉。
// 旧明文行要先把邮箱加密保存，所以需要主密钥；没有时返回错误且不改动，等配上密钥再清。密文行的邮箱早已单独保存，不需要密钥。
func clearCDKSessionRowIf(code, expected string) (bool, error) {
	var res sql.Result
	var err error
	if isSealedSession(expected) {
		res, err = DB.Exec(`UPDATE cdk_session_bindings SET session_payload = '' WHERE cdk_code = ? AND session_payload = ?`, code, expected)
	} else {
		key, kerr := secretbox.LoadKey()
		if kerr != nil {
			return false, kerr
		}
		email, serr := sealSessionEmail(key, expected)
		if serr != nil {
			return false, serr
		}
		res, err = DB.Exec(`UPDATE cdk_session_bindings SET session_payload = '', account_email_sealed = ? WHERE cdk_code = ? AND session_payload = ?`,
			email, code, expected)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ClearCDKSessionByCode 订单完成时清空会话。没有这一行或已清空都不算错。
func ClearCDKSessionByCode(cdkCode string) error {
	if DB == nil {
		return fmt.Errorf("db not ready")
	}
	code := normalizeCDKCode(cdkCode)
	if code == "" {
		return nil
	}
	var stored string
	err := DB.QueryRow(`SELECT session_payload FROM cdk_session_bindings WHERE cdk_code = ?`, code).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && stored == "") {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = clearCDKSessionRowIf(code, stored)
	return err
}

// ClearCDKSessionByToken 同 ClearCDKSessionByCode，按 redemption_token 定位（只有 token 的 RT: 占位行也能清）。
func ClearCDKSessionByToken(redemptionToken string) error {
	if DB == nil {
		return fmt.Errorf("db not ready")
	}
	tok := strings.TrimSpace(redemptionToken)
	if tok == "" {
		return nil
	}
	rows, err := DB.Query(`SELECT cdk_code, session_payload FROM cdk_session_bindings WHERE redemption_token = ? AND session_payload != ''`, tok)
	if err != nil {
		return err
	}
	list, err := scanCDKSessionRows(rows)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range list {
		if _, err := clearCDKSessionRowIf(r.code, r.stored); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type cdkSessionRow struct{ code, stored string }

// scanCDKSessionRows 读完并关闭结果集，再做后面的写入（测试库只有一条连接，边读边写会死锁）。
func scanCDKSessionRows(rows *sql.Rows) ([]cdkSessionRow, error) {
	defer rows.Close()
	var out []cdkSessionRow
	for rows.Next() {
		var r cdkSessionRow
		if err := rows.Scan(&r.code, &r.stored); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CDKSessionSweep 是一轮清理的结果。
type CDKSessionSweep struct {
	Cleared       int  // 本轮清空的行数
	Failed        int  // 本轮清空失败的行数，下一轮再试
	Skipped       bool // 有到期的旧明文行因主密钥缺失没有清（邮箱无法保全）
	PlaintextLeft int  // 库里还剩多少行明文会话，大于 0 就该告警
}

// SweepCDKSessions 清空到期的会话：绑定满 CDKSessionTTL，或本站已记录该码兑换完成。
// 兑换完成的主路径是 Webhook 与结果查询里的 ClearCDKSessionBy*，这里兜住漏网的（Webhook 没配、用户不再查询）。
//
// 主密钥缺失时，到期的旧明文行不清：邮箱无法加密保全，清掉就再也找不回来，属于破坏现有数据。
// 这些行留到配上密钥（下次启动先加密）；PlaintextLeft 用来持续告警。
func SweepCDKSessions(now time.Time) (CDKSessionSweep, error) {
	var res CDKSessionSweep
	if DB == nil {
		return res, fmt.Errorf("db not ready")
	}
	rows, err := DB.Query(`
		SELECT b.cdk_code, b.session_payload FROM cdk_session_bindings b
		WHERE TRIM(b.session_payload) != ''
		  AND (COALESCE(b.session_bound_at, 0) <= ?
		       OR EXISTS (SELECT 1 FROM cardplatform_cdk_codes c
		                  WHERE upper(trim(c.code)) = b.cdk_code AND c.status IN ('consumed', 'used', 'redeemed')))
	`, now.Add(-CDKSessionTTL).Unix())
	if err != nil {
		return res, err
	}
	due, err := scanCDKSessionRows(rows)
	if err != nil {
		return res, err
	}
	for _, r := range due {
		cleared, err := clearCDKSessionRowIf(r.code, r.stored)
		switch {
		case errors.Is(err, secretbox.ErrNoMasterKey), errors.Is(err, secretbox.ErrBadMasterKey):
			res.Skipped = true
		case err != nil:
			res.Failed++
			log.Printf("[cdk-session] 清空会话失败: %v", err)
		case cleared:
			res.Cleared++
		}
	}
	if res.PlaintextLeft, err = countPlaintextCDKSessions(); err != nil {
		return res, err
	}
	return res, nil
}

func countPlaintextCDKSessions() (int, error) {
	var n int
	err := DB.QueryRow(`SELECT COUNT(*) FROM cdk_session_bindings
		WHERE TRIM(session_payload) != '' AND substr(session_payload, 1, 3) != ?`, sealedPrefix).Scan(&n)
	return n, err
}

// cdkSessionMigration 是一次加密迁移的结果。
type cdkSessionMigration struct {
	Encrypted     int
	Failed        int
	Skipped       bool // 主密钥缺失或不合法，一行都没动
	PlaintextLeft int
}

// encryptLegacyCDKSessions 把明文行逐行加密。每行一条条件更新，自身是原子的：
// 崩溃或中断后，每一行要么还是完整的明文、要么是完整的密文，重跑只处理剩下的明文行，所以幂等、可中断。
// 条件里带旧值，所以这期间用户提交的新会话不会被覆盖。maxRows 大于 0 时加密这么多行后停下（测试用来模拟中断）。
// 已是 v1: 前缀的行绝不二次加密，哪怕当前密钥解不开（可能是密钥换过，配回原密钥还能读）。
func encryptLegacyCDKSessions(maxRows int) (cdkSessionMigration, error) {
	var res cdkSessionMigration
	left, err := countPlaintextCDKSessions()
	if err != nil {
		return res, err
	}
	res.PlaintextLeft = left
	if left == 0 {
		return res, nil
	}
	key, err := secretbox.LoadKey()
	if err != nil {
		res.Skipped = true
		return res, nil
	}
	if err := SetSetting(settingCDKSessionVacuum, "1"); err != nil {
		return res, err
	}
	var after int64
	for {
		list, last, err := nextPlaintextCDKSessions(after)
		if err != nil {
			return res, err
		}
		if len(list) == 0 {
			break
		}
		after = last
		for _, r := range list {
			if maxRows > 0 && res.Encrypted >= maxRows {
				res.PlaintextLeft, err = countPlaintextCDKSessions()
				return res, err
			}
			if err := encryptCDKSessionRow(key, r); err != nil {
				res.Failed++
				log.Printf("[cdk-session] 加密一行旧会话失败，下次启动重试: %v", err)
				continue
			}
			res.Encrypted++
		}
	}
	res.PlaintextLeft, err = countPlaintextCDKSessions()
	return res, err
}

func nextPlaintextCDKSessions(after int64) ([]cdkSessionRow, int64, error) {
	rows, err := DB.Query(`SELECT rowid, cdk_code, session_payload FROM cdk_session_bindings
		WHERE rowid > ? AND TRIM(session_payload) != '' AND substr(session_payload, 1, 3) != ?
		ORDER BY rowid LIMIT ?`, after, sealedPrefix, cdkSessionMigrateBatch)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var list []cdkSessionRow
	last := after
	for rows.Next() {
		var r cdkSessionRow
		if err := rows.Scan(&last, &r.code, &r.stored); err != nil {
			return nil, 0, err
		}
		list = append(list, r)
	}
	return list, last, rows.Err()
}

func encryptCDKSessionRow(key []byte, r cdkSessionRow) error {
	sealed, err := secretbox.Seal(key, cdkSessionAAD, r.stored)
	if err != nil {
		return err
	}
	// 写之前先解一遍：密文解不回原文就不要动这一行
	if back, err := secretbox.Open(key, cdkSessionAAD, sealed); err != nil || back != r.stored {
		return errors.New("密文校验未通过")
	}
	email, err := sealSessionEmail(key, r.stored)
	if err != nil {
		return err
	}
	_, err = DB.Exec(`UPDATE cdk_session_bindings SET session_payload = ?, account_email_sealed = ?
		WHERE cdk_code = ? AND session_payload = ?`, sealed, email, r.code, r.stored)
	return err
}

// MigrateCDKSessions 补列、把旧明文加密、回收残留明文的空闲页。可重复执行、可中断后重跑。
// 主密钥缺失时跳过加密并告警，不改动任何会话数据，也不影响启动。
func MigrateCDKSessions() error {
	if err := migrateCDKSessionColumns(); err != nil {
		return err
	}
	res, err := encryptLegacyCDKSessions(0)
	if err != nil {
		log.Printf("⚠️  [cdk-session] 旧会话加密未完成，下次启动重试: %v", err)
		return nil
	}
	if res.Skipped {
		log.Printf("⚠️  [cdk-session] APP_MASTER_KEY 缺失或不合法：%d 行旧会话仍是明文，已跳过加密，数据未改动。配置密钥后重启会自动加密", res.PlaintextLeft)
		return nil
	}
	if res.Encrypted > 0 {
		log.Printf("[cdk-session] 已把 %d 行旧会话加密", res.Encrypted)
	}
	if res.PlaintextLeft > 0 {
		log.Printf("⚠️  [cdk-session] 还有 %d 行旧会话未能加密，下次启动重试", res.PlaintextLeft)
		return nil
	}
	reclaimPlaintextPages()
	return nil
}

// reclaimPlaintextPages 明文行全部加密之后 VACUUM 一次：SQLite 改写行时不擦旧内容，明文还留在数据库文件的空闲页里。
// 失败只告警，标记保留，下次启动再试。旧备份里的明文不归这里管（部署说明里提醒）。
func reclaimPlaintextPages() {
	if v, err := GetSetting(settingCDKSessionVacuum); err != nil || v == "" {
		return
	}
	if _, err := DB.Exec(`VACUUM`); err != nil {
		log.Printf("⚠️  [cdk-session] VACUUM 失败，数据库文件的空闲页里可能还有旧会话明文，下次启动重试: %v", err)
		return
	}
	// WAL 模式下内容还在 -wal 文件里；回滚日志模式下这条什么也不做
	if _, err := DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		log.Printf("⚠️  [cdk-session] wal_checkpoint 失败: %v", err)
	}
	if err := DeleteSetting(settingCDKSessionVacuum); err != nil {
		log.Printf("[cdk-session] 清除 VACUUM 标记失败: %v", err)
	}
}

func migrateCDKSessionColumns() error {
	for _, c := range []struct{ name, ddl string }{
		{"account_email_sealed", `ALTER TABLE cdk_session_bindings ADD COLUMN account_email_sealed TEXT NOT NULL DEFAULT ''`},
		{"session_bound_at", `ALTER TABLE cdk_session_bindings ADD COLUMN session_bound_at INTEGER`},
	} {
		var n int
		if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('cdk_session_bindings') WHERE name = ?`, c.name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := DB.Exec(c.ddl); err != nil {
			return err
		}
	}
	// updated_at 是 UTC 的 "YYYY-MM-DD HH:MM:SS"。解析不出来时留 NULL，清理按已到期处理
	if _, err := DB.Exec(`UPDATE cdk_session_bindings SET session_bound_at = CAST(strftime('%s', updated_at) AS INTEGER)
		WHERE session_bound_at IS NULL AND TRIM(session_payload) != ''`); err != nil {
		return err
	}
	// 清理按完整码关联 CDK 状态；同一表达式也让 LookupStoredCDKByCode 不用全表扫描
	_, err := DB.Exec(`CREATE INDEX IF NOT EXISTS idx_cp_cdk_code_norm ON cardplatform_cdk_codes(upper(trim(code)))`)
	return err
}
