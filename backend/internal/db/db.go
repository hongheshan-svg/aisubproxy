package db

import (
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
	"github.com/tuzi/cdk-recharge-system/internal/config"
	"golang.org/x/crypto/bcrypt"
)

var DB *sql.DB

const (
	DefaultAdminUsername = "admin"
	// 历史上内置的默认密码，仅用于在启动时识别“仍在使用弱口令”并告警，不再用于创建账号。
	legacyDefaultPassword = "admin123456"
)

// ResolvePath 是数据库文件的位置：默认 ../data/cdk_recharge.db（相对于工作目录，即 backend 目录）；
// 部署/容器内可用环境变量 DB_PATH 覆盖（如 /app/data/cdk_recharge.db）。启动和 --check 用同一个规则。
func ResolvePath() string {
	if p := strings.TrimSpace(os.Getenv("DB_PATH")); p != "" {
		return p
	}
	return "../data/cdk_recharge.db"
}

// InitialAdminPasswordFileName 是随机初始管理员密码的文件名，放在数据库文件所在目录。
const InitialAdminPasswordFileName = "initial-admin-password.txt"

// InitialAdminPasswordPath 是随机初始管理员密码文件的位置：和数据库文件同一目录。
func InitialAdminPasswordPath() string {
	return filepath.Join(filepath.Dir(ResolvePath()), InitialAdminPasswordFileName)
}

// RemoveInitialAdminPasswordFile 删掉随机初始密码文件（第一次改密成功后调用）。文件不存在不算错；
// 其他错误只记一行日志，不带文件内容。
func RemoveInitialAdminPasswordFile() {
	path := InitialAdminPasswordPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("删除初始管理员密码文件失败（%s）: %v", path, err)
	}
}

// writeInitialAdminPasswordFile 把随机初始密码写进新建的 0600 文件。已有的同名文件或符号链接先删掉再用 O_EXCL 新建：
// 不复用旧文件（否则密码会先以旧权限落盘），也不跟随符号链接（O_EXCL 遇到已存在的路径包括链接直接失败）。
// 错误信息不带密码。
func writeInitialAdminPasswordFile(path, username, password string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	content := fmt.Sprintf("用户名: %s\n初始密码: %s\n登录后台并修改密码后，这个文件会被自动删除；也可以手动删除。\n", username, password)
	_, werr := f.WriteString(content)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	return os.Chmod(path, 0o600)
}

func Init(cfg *config.DatabaseConfig) error {
	dbPath := ResolvePath()
	log.Printf("使用数据库: %s", dbPath)
	// WAL 让读不阻塞写；busy_timeout 让并发写等待锁而不是立刻报 database is locked。
	// 参数写在 DSN 里，对连接池里每一个连接都生效（busy_timeout 是连接级设置）。
	// _txlock=immediate：事务一开始就拿写锁，避免读后升级写时的 SQLITE_BUSY（busy_timeout 管不了这种死锁）。
	// _synchronous=FULL：每次提交都等落盘。NORMAL 在掉电、内核崩溃时可能丢最近几笔已提交的事务，
	// 付款单「已付款」回退成「处理中」后，重启恢复可能再付一次。本站写入量小，FULL 的代价可以接受。
	dsn := dbPath + "?_journal_mode=WAL&_busy_timeout=10000&_synchronous=FULL&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return err
	}

	if err := db.Ping(); err != nil {
		return err
	}
	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		return err
	}
	if !strings.EqualFold(journalMode, "wal") {
		log.Printf("⚠️  SQLite journal_mode 为 %s，未切换到 WAL，并发写入时可能出现 database is locked", journalMode)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)

	DB = db
	log.Println("✓ SQLite 数据库连接成功: data/cdk_recharge.db")

	// 创建表
	if err := createTables(); err != nil {
		return err
	}

	return nil
}

func createTables() error {
	queries := []string{
		// 旧 ZovoCard 数据，代码已不读写，留表以兼容旧库
		`CREATE TABLE IF NOT EXISTS cd_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT UNIQUE NOT NULL,
			plan_type TEXT,
			status TEXT DEFAULT 'active',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			used_at DATETIME,
			expires_at DATETIME,
			description TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cd_keys_code ON cd_keys(code)`,
		`CREATE INDEX IF NOT EXISTS idx_cd_keys_status ON cd_keys(status)`,

		// 卡台 GPT 直充 CDK：发码时落库完整码，列表仅回前缀时用本表补全（admin 本站复制）
		// 旧 ZovoCard 数据，代码已不读写，留表以兼容旧库
		`CREATE TABLE IF NOT EXISTS cardplatform_cdk_codes (
			upstream_id INTEGER,
			code TEXT NOT NULL UNIQUE,
			code_prefix TEXT,
			plan TEXT,
			fee_amount_minor INTEGER DEFAULT 0,
			status TEXT DEFAULT '',
			payment_country TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cp_cdk_upstream ON cardplatform_cdk_codes(upstream_id)`,
		`CREATE INDEX IF NOT EXISTS idx_cp_cdk_prefix ON cardplatform_cdk_codes(code_prefix)`,

		// CDK 备注（本站维护，按卡台上游 id；不依赖完整码是否已落库）
		// 旧 ZovoCard 数据，代码已不读写，留表以兼容旧库
		`CREATE TABLE IF NOT EXISTS cardplatform_cdk_notes (
			upstream_id INTEGER PRIMARY KEY,
			note TEXT NOT NULL DEFAULT '',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// 旧 ZovoCard 数据，代码已不读写，留表以兼容旧库
		`CREATE TABLE IF NOT EXISTS recharge_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT UNIQUE NOT NULL,
			cdk_code TEXT NOT NULL,
			session_json TEXT,
			account_email TEXT,
			task_status TEXT DEFAULT 'pending',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME,
			notes TEXT,
			FOREIGN KEY(cdk_code) REFERENCES cd_keys(code)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_recharge_tasks_cdk ON recharge_tasks(cdk_code)`,
		`CREATE INDEX IF NOT EXISTS idx_recharge_tasks_task_id ON recharge_tasks(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_recharge_tasks_status ON recharge_tasks(task_status)`,

		// 旧版遗留表，代码已不读写，保留不删
		`CREATE TABLE IF NOT EXISTS billing_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_hash TEXT UNIQUE NOT NULL,
			account_email TEXT,
			subscription_status TEXT,
			plan_type TEXT,
			billing_amount REAL,
			currency TEXT DEFAULT 'USD',
			next_billing_date DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_records_session ON billing_records(session_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_billing_records_email ON billing_records(account_email)`,

		`CREATE TABLE IF NOT EXISTS admin_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			display_name TEXT,
			is_active INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			token_version INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_admin_users_username ON admin_users(username)`,

		`CREATE TABLE IF NOT EXISTS admin_audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT,
			action TEXT NOT NULL,
			detail TEXT,
			ip TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_created ON admin_audit_logs(created_at)`,

		// 站点配置 key-value（品牌/皮肤/安装锁/加密密钥元数据等）
		`CREATE TABLE IF NOT EXISTS site_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// 卡台 Webhook 事件（幂等入库；配合轮询双通道）
		// 旧 ZovoCard 数据，代码已不读写，留表以兼容旧库
		`CREATE TABLE IF NOT EXISTS webhook_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event_type TEXT,
			idem_key TEXT UNIQUE NOT NULL,
			payload TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_webhook_created ON webhook_events(created_at)`,

		// 兑换时绑定的 CDK → session，供账单页「凭卡密查询」
		`CREATE TABLE IF NOT EXISTS cdk_session_bindings (
			cdk_code TEXT PRIMARY KEY,
			session_payload TEXT NOT NULL,
			redemption_token TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cdk_bind_token ON cdk_session_bindings(redemption_token)`,
		// 只增不改的令牌表：同一张码重复预览会拿到多个令牌，旧令牌也要认（审计 N-2）。
		// 旧 ZovoCard 数据，代码已不读写，留表以兼容旧库
		`CREATE TABLE IF NOT EXISTS cdk_redemption_tokens (
			token TEXT PRIMARY KEY,
			cdk_code TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// 代理失败换码日志（旧码 → 新码，仅未扣款失败）
		`CREATE TABLE IF NOT EXISTS agent_cdk_exchanges (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			old_code_hash TEXT NOT NULL,
			old_upstream_id INTEGER,
			new_upstream_id INTEGER,
			old_code_prefix TEXT,
			new_code_prefix TEXT,
			plan TEXT,
			order_id INTEGER,
			order_status TEXT,
			ip TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_agent_ex_old_hash ON agent_cdk_exchanges(old_code_hash)`,

		// 自动选卡权重规则（管理员可配置优先级）
		// 旧 ZovoCard 数据，代码已不读写，留表以兼容旧库
		`CREATE TABLE IF NOT EXISTS card_selection_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sort_order INTEGER NOT NULL DEFAULT 0,
			plan_key TEXT NOT NULL,
			display_name TEXT NOT NULL,
			bin_prefix TEXT DEFAULT '',
			channel TEXT DEFAULT '',
			enabled INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// 卡台产品状态缓存（每3分钟后台同步，plan_key 用于逻辑套餐）
		// 旧 ZovoCard 数据，代码已不读写，留表以兼容旧库
		`CREATE TABLE IF NOT EXISTS plan_status_cache (
			plan_key TEXT PRIMARY KEY,
			label TEXT DEFAULT '',
			online INTEGER DEFAULT 1,
			service_fee_usd_minor INTEGER DEFAULT 0,
			synced_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// 卡健康：充值失败事件（按卡 + 邮箱归因）
		// 旧 ZovoCard 数据，代码已不读写，留表以兼容旧库
		`CREATE TABLE IF NOT EXISTS card_fail_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			card_id INTEGER NOT NULL,
			card_last_four TEXT DEFAULT '',
			order_id INTEGER NOT NULL DEFAULT 0,
			cdk_code TEXT DEFAULT '',
			account_email_norm TEXT NOT NULL DEFAULT '',
			email_source TEXT DEFAULT '',
			error_code TEXT DEFAULT '',
			order_status TEXT DEFAULT '',
			verdict TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(order_id, card_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_card_fail_card ON card_fail_events(card_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_card_fail_email ON card_fail_events(account_email_norm)`,

		// 卡健康：本站认定的坏卡（多邮箱失败）
		// 旧 ZovoCard 数据，代码已不读写，留表以兼容旧库
		`CREATE TABLE IF NOT EXISTS card_blocklist (
			card_id INTEGER PRIMARY KEY,
			card_last_four TEXT DEFAULT '',
			reason TEXT NOT NULL DEFAULT '',
			distinct_emails INTEGER NOT NULL DEFAULT 0,
			fail_count INTEGER NOT NULL DEFAULT 0,
			freeze_status TEXT DEFAULT '',
			freeze_error TEXT DEFAULT '',
			blocked_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			unblocked_at DATETIME,
			notes TEXT DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_card_block_active ON card_blocklist(blocked_at) WHERE unblocked_at IS NULL`,

		// 卡台实体产品缓存（/openapi/v1/products 同步，product_code 唯一）
		// 旧 ZovoCard 数据，代码已不读写，留表以兼容旧库
		`CREATE TABLE IF NOT EXISTS card_product_cache (
			product_code TEXT PRIMARY KEY,
			issuer TEXT DEFAULT '',
			bin TEXT DEFAULT '',
			network TEXT DEFAULT '',
			issuing_area TEXT DEFAULT '',
			scene TEXT DEFAULT '',
			card_group TEXT DEFAULT '',
			description TEXT DEFAULT '',
			bin_heads TEXT DEFAULT '',
			enabled INTEGER DEFAULT 1,
			suspended_at TEXT DEFAULT '',
			synced_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}

	for _, query := range queries {
		if _, err := DB.Exec(query); err != nil {
			// 语句都带 IF NOT EXISTS，正常不会失败；失败多半是磁盘满、库文件损坏或表结构不对，
			// 带病启动只会在后面以更难懂的方式出错，这里直接终止（审计 A-16）。
			if strings.Contains(err.Error(), "already exists") {
				log.Printf("创建表时提示已存在，跳过: %v", err)
				continue
			}
			return fmt.Errorf("建表失败（%s）: %w", ddlHead(query), err)
		}
	}

	log.Println("✓ 数据表已准备就绪")
	if err := migrateCDKPlanTypes(); err != nil {
		return err
	}
	if err := backfillRedemptionTokens(); err != nil {
		return err
	}
	if err := migrateLegacyCDKCodes(); err != nil {
		return err
	}
	if err := migrateDefaultCardSelectionRules(); err != nil {
		return err
	}
	if err := migrateCardplatformCDKStatusCol(); err != nil {
		log.Printf("migrateCardplatformCDKStatusCol: %v", err)
	}
	if err := migrateCardplatformCDKRegionCol(); err != nil {
		return fmt.Errorf("migrate cardplatform CDK region: %w", err)
	}
	if err := migrateAdminTokenVersion(); err != nil {
		return fmt.Errorf("migrate admin token version: %w", err)
	}
	if err := ensureDefaultAdmin(); err != nil {
		return err
	}
	if err := MigrateMarsWallex(); err != nil {
		return fmt.Errorf("migrate marswallex: %w", err)
	}
	if err := MigrateCardPool(); err != nil {
		return fmt.Errorf("migrate card pool: %w", err)
	}
	if err := MigratePay(); err != nil {
		return fmt.Errorf("migrate pay: %w", err)
	}
	if err := MigratePayProbes(); err != nil {
		return fmt.Errorf("migrate pay probes: %w", err)
	}
	if err := MigrateCardHeads(); err != nil {
		return fmt.Errorf("migrate card heads: %w", err)
	}
	if err := MigrateAgentExchanges(); err != nil {
		return err
	}
	if err := MigrateCDKSessions(); err != nil {
		return fmt.Errorf("migrate cdk sessions: %w", err)
	}
	if err := MigrateLocalCDKs(); err != nil {
		return fmt.Errorf("migrate local cdks: %w", err)
	}
	if err := MigrateAgents(); err != nil {
		return fmt.Errorf("migrate agents: %w", err)
	}
	return nil
}

// ddlHead 把建表语句压成一行、截前 80 个字符，放进错误信息里指明是哪一条。
func ddlHead(q string) string {
	h := strings.Join(strings.Fields(q), " ")
	if r := []rune(h); len(r) > 80 {
		h = string(r[:80]) + "…"
	}
	return h
}

// Migrate 是 createTables 的导出入口，供测试在内存库上建表。
func Migrate() error { return createTables() }

// migrateCardplatformCDKStatusCol 为已存完整码表补 status（禁用后列表要展示）。
func migrateCardplatformCDKStatusCol() error {
	if DB == nil {
		return nil
	}
	var n int
	err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('cardplatform_cdk_codes') WHERE name='status'`).Scan(&n)
	if err != nil || n > 0 {
		return err
	}
	_, err = DB.Exec(`ALTER TABLE cardplatform_cdk_codes ADD COLUMN status TEXT DEFAULT ''`)
	return err
}

// migrateAdminTokenVersion 给旧库的管理员表补令牌版本号（审计 A-2）。可重复执行：列已在就跳过。
// 版本号签进 JWT，改密码时加一，之前签发的令牌随之作废；老账号从 0 开始，升级前签发的令牌（不带版本号，按 0 算）照常可用到过期。
func migrateAdminTokenVersion() error {
	var n int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('admin_users') WHERE name='token_version'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := DB.Exec(`ALTER TABLE admin_users ADD COLUMN token_version INTEGER NOT NULL DEFAULT 0`)
	return err
}

// AdminTokenState 返回管理员当前的令牌版本号和是否启用，鉴权中间件每个请求查一次。账号不存在返回 sql.ErrNoRows。
func AdminTokenState(id int64) (version int64, active bool, err error) {
	var isActive int
	err = DB.QueryRow(`SELECT token_version, COALESCE(is_active, 0) FROM admin_users WHERE id = ?`, id).Scan(&version, &isActive)
	return version, isActive == 1, err
}

// legacySHA256 是旧的（不安全）哈希算法，仅用于兼容历史数据并在登录时升级为 bcrypt。
func legacySHA256(password string) string {
	hash := sha256.Sum256([]byte(password))
	return hex.EncodeToString(hash[:])
}

// HashAdminPassword 现在使用 bcrypt（带盐、慢哈希）。
// WriteAudit 记录一条管理员操作审计日志（best-effort，失败只记日志不影响主流程）。
func WriteAudit(username, action, detail, ip string) {
	if DB == nil {
		return
	}
	if _, err := DB.Exec(
		`INSERT INTO admin_audit_logs (username, action, detail, ip) VALUES (?, ?, ?, ?)`,
		username, action, detail, ip,
	); err != nil {
		log.Printf("写入审计日志失败: %v", err)
	}
}

func HashAdminPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyAdminPassword 校验密码。返回 (是否正确, 需要写回的新哈希)。
// 若数据库里仍是旧的 SHA-256 哈希且校验通过，会返回一个 bcrypt 新哈希用于升级存储。
func VerifyAdminPassword(stored, plain string) (bool, string) {
	if strings.HasPrefix(stored, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(plain)) == nil, ""
	}
	// 旧的 SHA-256 十六进制哈希：常量时间比较，命中后升级为 bcrypt
	if subtle.ConstantTimeCompare([]byte(legacySHA256(plain)), []byte(stored)) == 1 {
		if upgraded, err := HashAdminPassword(plain); err == nil {
			return true, upgraded
		}
		return true, ""
	}
	return false, ""
}

// SetAdminPassword 更新指定管理员的密码（bcrypt），同时令牌版本号加一：改密前签发的所有令牌立刻失效。
func SetAdminPassword(username, newPlain string) error {
	hash, err := HashAdminPassword(newPlain)
	if err != nil {
		return err
	}
	_, err = DB.Exec(`UPDATE admin_users SET password_hash = ?, token_version = token_version + 1, updated_at = CURRENT_TIMESTAMP WHERE username = ?`, hash, username)
	return err
}

// UpgradeAdminHash 把登录时升级出来的 bcrypt 哈希写回数据库。
func UpgradeAdminHash(username, newHash string) {
	if newHash == "" {
		return
	}
	if _, err := DB.Exec(`UPDATE admin_users SET password_hash = ? WHERE username = ?`, newHash, username); err != nil {
		log.Printf("升级管理员密码哈希失败: %v", err)
	}
}

// randomPassword 生成管理员初始密码（安装令牌用 randomToken）。crypto/rand 读不出来时直接 panic：
// 宁可启动失败，也不能退回一个可预测的口令（Go 1.24 起 crypto/rand 本身也不会返回错误）。
func randomPassword(n int) string {
	p, err := randomStringFrom(rand.Reader, n)
	if err != nil {
		panic(fmt.Sprintf("生成随机口令失败: %v", err))
	}
	return p
}

// randomToken 生成安装令牌这类高熵随机串，与 randomPassword 同一生成方式（同一字母表、同一拒绝采样），
// 单独起名是为了表明这里生成的是一次性令牌，不是用户口令。
func randomToken(n int) string {
	p, err := randomStringFrom(rand.Reader, n)
	if err != nil {
		panic(fmt.Sprintf("生成随机令牌失败: %v", err))
	}
	return p
}

// randomStringFrom 用拒绝采样从字母表里取 n 个字符：只用小于 224（56 的最大整数倍）的字节，
// 每个字符等概率。原来直接取模，256 % 56 = 32，前 32 个字符的概率高出 25%（审计 A-17）。
func randomStringFrom(src io.Reader, n int) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
	const limit = 256 - 256%len(alphabet)
	out := make([]byte, 0, n)
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := io.ReadFull(src, buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) < limit && len(out) < n {
				out = append(out, alphabet[int(b)%len(alphabet)])
			}
		}
	}
	return string(out), nil
}

// InstallMode: wizard（推荐，等 Web 安装）| auto（启动时建管理员，兼容旧行为）
func InstallMode() string {
	m := strings.ToLower(strings.TrimSpace(os.Getenv("INSTALL_MODE")))
	if m == "auto" {
		return "auto"
	}
	// 默认 wizard：开源/生产更安全
	return "wizard"
}

// IsInstalled 任一管理员存在或 install_completed_at 已写，即视为已安装。
func IsInstalled() bool {
	if AdminCount() > 0 {
		return true
	}
	v, _ := GetSetting("install_completed_at")
	return strings.TrimSpace(v) != ""
}

func AdminCount() int {
	if DB == nil {
		return 0
	}
	var n int
	_ = DB.QueryRow(`SELECT COUNT(*) FROM admin_users`).Scan(&n)
	return n
}

func GetSetting(key string) (string, error) {
	var v string
	err := DB.QueryRow(`SELECT value FROM site_settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SettingNonEmpty 该设置项存在且非空。只查有没有，不把值读出来（适合很大的值，如 Logo 的 base64）。
func SettingNonEmpty(key string) bool {
	var one int
	err := DB.QueryRow(`SELECT 1 FROM site_settings WHERE key = ? AND value <> '' LIMIT 1`, key).Scan(&one)
	return err == nil
}

func SetSetting(key, value string) error {
	_, err := DB.Exec(`
		INSERT INTO site_settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP
	`, key, value)
	return err
}

// SetSettings 在一个事务里写多项设置：要么全写上，要么都不变。
func SetSettings(kv map[string]string) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range kv {
		if _, err := tx.Exec(`
		INSERT INTO site_settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP
	`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func DeleteSetting(key string) error {
	_, err := DB.Exec(`DELETE FROM site_settings WHERE key = ?`, key)
	return err
}

// EnsureSetupToken 未安装时准备一次性安装令牌（hash 入库，明文仅首次创建时返回供打日志）。
// 若 env SETUP_BOOTSTRAP_TOKEN 已设，则用其；否则随机生成。已有 hash 时返回空 plain。
func EnsureSetupToken() (plain string, firstTime bool, err error) {
	if IsInstalled() {
		return "", false, nil
	}
	// 已有 hash：不再二次生成（避免每次重启换 token）
	if h, _ := GetSetting("setup_token_hash"); strings.TrimSpace(h) != "" {
		return "", false, nil
	}
	plain = strings.TrimSpace(os.Getenv("SETUP_BOOTSTRAP_TOKEN"))
	if plain == "" {
		plain = randomToken(24)
	}
	sum := sha256.Sum256([]byte(plain))
	if err := SetSetting("setup_token_hash", hex.EncodeToString(sum[:])); err != nil {
		return "", false, err
	}
	return plain, true, nil
}

// VerifySetupToken 常量时间比较安装令牌。
func VerifySetupToken(plain string) bool {
	stored, err := GetSetting("setup_token_hash")
	if err != nil || stored == "" || plain == "" {
		return false
	}
	sum := sha256.Sum256([]byte(plain))
	got := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(stored)) == 1
}

// RollbackAdminBootstrap 撤销一次没走完的安装：删掉刚建的管理员和安装完成标记，
// 并把安装令牌恢复成这次用的那个（CreateAdminUser 已把它删了），管理员不用重启就能用同一个令牌重试（审计 A-11）。
// userID 为 0（读回 id 失败）时按 username 找。只有「库里恰好只有这一个管理员」时才动手：
// 多于一个说明里面有回滚之前就存在的管理员，一个都不删，返回错误。
func RollbackAdminBootstrap(userID int64, username, setupToken string) error {
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM admin_users`).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("库里有 %d 个管理员，不是刚建的唯一一个，拒绝回滚", n)
	}
	if userID == 0 {
		if err := tx.QueryRow(`SELECT id FROM admin_users WHERE username = ?`, username).Scan(&userID); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`DELETE FROM admin_users WHERE id = ?`, userID)
	if err != nil {
		return err
	}
	if k, _ := res.RowsAffected(); k != 1 {
		return fmt.Errorf("没有找到要回滚的管理员 id=%d", userID)
	}
	if _, err := tx.Exec(`DELETE FROM site_settings WHERE key = 'install_completed_at'`); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(setupToken))
	if _, err := tx.Exec(`
		INSERT INTO site_settings (key, value, updated_at) VALUES ('setup_token_hash', ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP
	`, hex.EncodeToString(sum[:])); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkInstalled 写安装完成标记并作废 setup token。
func MarkInstalled(username string) error {
	if err := SetSetting("install_completed_at", time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	_ = DeleteSetting("setup_token_hash")
	WriteAudit(username, "install_bootstrap", "setup completed", "")
	return nil
}

// weakPasswordWords：去掉首尾的数字和符号后仍是这些词的，算弱口令（如 password1234、Admin@2026!!）。
var weakPasswordWords = map[string]bool{
	"password": true, "passw0rd": true, "admin": true, "adminadmin": true, "administrator": true,
	"qwerty": true, "qwertyuiop": true, "changeme": true, "letmein": true, "welcome": true,
	"root": true, "test": true, "default": true,
}

// IsWeakPassword 拦截常见弱口令与历史默认口令：名单原文、常见词加首尾数字或符号、
// 同一字符重复、连续数字（升序或降序，可跨 9→0）。只在设置或修改口令时检查，已保存的口令不受影响。
func IsWeakPassword(p string) bool {
	p = strings.TrimSpace(p)
	if utf8.RuneCountInString(p) < 12 { // 按字符数，和各处「至少 12 位」口径一致
		return true
	}
	lower := strings.ToLower(p)
	weak := []string{
		legacyDefaultPassword, "password", "password123", "admin", "admin123",
		"adminadmin", "12345678", "123456789012", "qwerty123456", "changeme",
		"letmein12345", "welcome12345",
	}
	for _, w := range weak {
		if lower == w {
			return true
		}
	}
	core := strings.TrimFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) })
	if weakPasswordWords[core] || sameRune(lower) || consecutiveDigits(lower) {
		return true
	}
	// 与用户名相同在调用方再判
	return false
}

// sameRune 判断字符串是否由同一个字符重复组成。
func sameRune(s string) bool {
	var first rune
	for i, r := range s {
		if i == 0 {
			first = r
		} else if r != first {
			return false
		}
	}
	return s != ""
}

// consecutiveDigits 判断字符串是否全是数字且逐位加一或逐位减一（9 之后是 0）。
func consecutiveDigits(s string) bool {
	if len(s) < 2 {
		return false
	}
	up, down := true, true
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
		if i == 0 {
			continue
		}
		d := (int(s[i]) - int(s[i-1]) + 10) % 10
		up = up && d == 1
		down = down && d == 9
	}
	return up || down
}

// defaultAdminPlan 是启动时对默认管理员的判定。
type defaultAdminPlan struct {
	username     string
	exists       bool   // 这个用户名的管理员已存在
	existingHash string // exists 时的密码哈希
	create       bool   // 要在启动时建号
	password     string // create 时用的 ADMIN_PASSWORD；为空表示随机生成
}

// planDefaultAdmin 只读：判断启动时要不要建默认管理员、ADMIN_PASSWORD 够不够强。
// ensureDefaultAdmin 按它建号，CheckDefaultAdmin（命令行 --check）只取它的错误：两边是同一套规则。
func planDefaultAdmin() (defaultAdminPlan, error) {
	p := defaultAdminPlan{username: os.Getenv("ADMIN_USERNAME")}
	if strings.TrimSpace(p.username) == "" {
		p.username = DefaultAdminUsername
	}
	var count int
	if err := DB.QueryRow(`SELECT COUNT(*), COALESCE(MAX(password_hash), '') FROM admin_users WHERE username = ?`, p.username).Scan(&count, &p.existingHash); err != nil {
		return p, err
	}
	if count > 0 {
		p.exists = true
		return p, nil
	}
	// wizard：无 ADMIN_PASSWORD 时不自动建号，等 Web bootstrap
	password := strings.TrimSpace(os.Getenv("ADMIN_PASSWORD"))
	if InstallMode() == "wizard" && password == "" {
		return p, nil
	}
	if password != "" && (IsWeakPassword(password) || strings.EqualFold(password, p.username)) {
		// 和安装向导同样的强度要求（审计 A-12）。拒绝启动，错误信息里不带口令本身。
		return p, fmt.Errorf("ADMIN_PASSWORD 太弱：至少 12 位、不能是常见弱口令、不能和用户名相同。请换成强口令，或删掉 ADMIN_PASSWORD 改用安装向导 /ops/setup")
	}
	p.create, p.password = true, password
	return p, nil
}

// CheckDefaultAdmin 只读检查启动时按 ADMIN_PASSWORD 建管理员能不能通过（口令太弱会拒绝启动）。
func CheckDefaultAdmin() error {
	_, err := planDefaultAdmin()
	return err
}

func ensureDefaultAdmin() error {
	// 先确保有安装令牌（wizard 模式、未安装时）
	if !IsInstalled() && InstallMode() == "wizard" {
		if plain, _, err := EnsureSetupToken(); err != nil {
			return err
		} else if plain != "" {
			log.Printf("============================================================")
			log.Printf("✓ 首次安装模式 (INSTALL_MODE=wizard)")
			log.Printf("  打开 /ops/setup 完成管理员创建（仅一次）")
			log.Printf("  Setup Token（仅此一次显示，bootstrap 请求头 X-Setup-Token）: %s", plain)
			log.Printf("============================================================")
		} else {
			log.Printf("ℹ 等待 Web 安装向导：/ops/setup（需正确 X-Setup-Token）")
		}
	}

	plan, err := planDefaultAdmin()
	if err != nil {
		return err
	}
	username := plan.username

	if plan.exists {
		// 已存在：若仍是历史弱口令，则在启动时大声告警，提示尽快改密。
		if ok, _ := VerifyAdminPassword(plan.existingHash, legacyDefaultPassword); ok {
			log.Printf("⚠️  管理员 %s 仍在使用历史默认密码（admin123456），存在严重风险，请立刻登录后台修改密码！", username)
		}
		// 补写 install 标记（从旧库升级）
		if v, _ := GetSetting("install_completed_at"); v == "" {
			_ = SetSetting("install_completed_at", time.Now().UTC().Format(time.RFC3339))
			_ = DeleteSetting("setup_token_hash")
		}
		return nil
	}
	if !plan.create {
		return nil
	}

	// auto 或显式 ADMIN_PASSWORD：启动时建管理员
	password, generated := plan.password, false
	if password == "" {
		password = randomPassword(16)
		generated = true
	}

	hash, err := HashAdminPassword(password)
	if err != nil {
		return err
	}
	// 随机密码不进日志（CodeQL go/clear-text-logging）：先写 0600 文件再建号，写不了就拒绝启动。
	pwPath := InitialAdminPasswordPath()
	if generated {
		if err := writeInitialAdminPasswordFile(pwPath, username, password); err != nil {
			return fmt.Errorf("无法写入初始管理员密码文件 %s: %v。请设置环境变量 ADMIN_PASSWORD 指定初始密码，或改用安装向导 INSTALL_MODE=wizard", pwPath, err)
		}
	}
	if _, err := DB.Exec(`
		INSERT INTO admin_users (username, password_hash, display_name, is_active, updated_at)
		VALUES (?, ?, ?, 1, CURRENT_TIMESTAMP)
	`, username, hash, "System Admin"); err != nil {
		if generated {
			_ = os.Remove(pwPath)
		}
		return err
	}
	_ = MarkInstalled(username)

	if generated {
		log.Printf("已创建管理员账号 %s，初始随机密码已写入 %s（权限 0600），请立即登录修改密码，改密成功后该文件会自动删除", username, pwPath)
	} else {
		log.Printf("✓ 已用 ADMIN_PASSWORD 创建管理员账号: %s", username)
	}
	return nil
}

// CreateAdminUser 事务安全创建管理员（安装向导用）。若已有任意管理员则失败。
func CreateAdminUser(username, password, displayName string) error {
	if DB == nil {
		return fmt.Errorf("db not ready")
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("username required")
	}
	if IsWeakPassword(password) {
		return fmt.Errorf("password too weak")
	}
	tx, err := DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM admin_users`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("already_installed")
	}
	hash, err := HashAdminPassword(password)
	if err != nil {
		return err
	}
	if displayName == "" {
		displayName = "System Admin"
	}
	if _, err := tx.Exec(`
		INSERT INTO admin_users (username, password_hash, display_name, is_active, updated_at)
		VALUES (?, ?, ?, 1, CURRENT_TIMESTAMP)
	`, username, hash, displayName); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO site_settings (key, value, updated_at) VALUES ('install_completed_at', ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP
	`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM site_settings WHERE key = ?`, "setup_token_hash"); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO admin_audit_logs (username, action, detail, ip) VALUES (?, ?, ?, ?)`,
		username, "install_bootstrap", "setup completed", "",
	); err != nil {
		log.Printf("audit install: %v", err)
	}
	return tx.Commit()
}

// RandomPassword 导出给安装向导生成强口令。
func RandomPassword(n int) string {
	if n < 12 {
		n = 16
	}
	return randomPassword(n)
}

func normalizeCDKCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// backfillRedemptionTokens 把绑定表里已有的令牌补进令牌表，可重复执行。
func backfillRedemptionTokens() error {
	_, err := DB.Exec(`INSERT OR IGNORE INTO cdk_redemption_tokens (token, cdk_code)
		SELECT redemption_token, cdk_code FROM cdk_session_bindings
		WHERE redemption_token IS NOT NULL AND redemption_token != ''`)
	if err != nil {
		return fmt.Errorf("补齐兑换令牌表失败: %w", err)
	}
	return nil
}

func migrateCDKPlanTypes() error {
	updates := []struct {
		from string
		to   string
	}{
		{"GPT-Pro", "pro"},
		{"GPT-Plus", "5x"},
		{"Pro", "pro"},
		{"PLUS", "5x"},
	}

	for _, item := range updates {
		if _, err := DB.Exec(`UPDATE cd_keys SET plan_type = ? WHERE plan_type = ?`, item.to, item.from); err != nil {
			return err
		}
	}

	return nil
}

func migrateLegacyCDKCodes() error {
	rows, err := DB.Query(`SELECT id, code, plan_type FROM cd_keys`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type item struct {
		id       int64
		code     string
		planType string
	}

	var items []item
	for rows.Next() {
		var current item
		if err := rows.Scan(&current.id, &current.code, &current.planType); err != nil {
			return err
		}
		if strings.Count(current.code, "-") == 4 && len(current.code) >= 32 {
			continue
		}
		items = append(items, current)
	}

	for _, current := range items {
		newCode := legacyUUIDCode(current.planType, current.id)
		if _, err := DB.Exec(`UPDATE cd_keys SET code = ? WHERE id = ?`, newCode, current.id); err != nil {
			return err
		}
		if _, err := DB.Exec(`UPDATE recharge_tasks SET cdk_code = ? WHERE cdk_code = ?`, newCode, current.code); err != nil {
			return err
		}
	}

	return nil
}

func legacyUUIDCode(planType string, id int64) string {
	seed := fmt.Sprintf("%s-%d-%d", planType, id, time.Now().UnixNano())
	sum := sha1.Sum([]byte(seed))
	hexID := hex.EncodeToString(sum[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexID[0:8], hexID[8:12], hexID[12:16], hexID[16:20], hexID[20:32])
}

// ---- 代理失败换码 ----

func HashCDKCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// ---- 自动选卡权重配置 ----

// migrateDefaultCardSelectionRules 首次启动时写入默认选卡优先级。
// 新装库里它仍会往 card_selection_rules 写默认规则，这是旧迁移原样保留（迁移不改写）；业务代码已不读这张表。
func migrateDefaultCardSelectionRules() error {
	if DB == nil {
		return nil
	}
	var count int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM card_selection_rules`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil // 已有用户配置，不覆盖
	}
	defaults := []struct {
		sortOrder   int
		planKey     string
		displayName string
		binPrefix   string
		channel     string
	}{
		// 只写入美卡；香港卡（PP5450RC/PP5583RC）不进默认优先级
		{1, "P5378OX", "渠道1 · P5378OX · 537872 · 美国通用卡", "537872", "ch1"},
		{2, "PP5259RC", "渠道4 · PP5259RC · 525962/555671/544015 · 美国随机", "525962", "ch4"},
		{3, "USMAB01", "渠道3 · USMAB01 · 555671/525962/544015 · AI订阅通用", "USMAB01", "ch3"},
	}
	for _, d := range defaults {
		if _, err := DB.Exec(`
			INSERT INTO card_selection_rules (sort_order, plan_key, display_name, bin_prefix, channel, enabled)
			VALUES (?, ?, ?, ?, ?, 1)
		`, d.sortOrder, d.planKey, d.displayName, d.binPrefix, d.channel); err != nil {
			log.Printf("migrateDefaultCardSelectionRules: %v", err)
		}
	}
	log.Println("✓ 已写入默认选卡优先级规则")
	return nil
}

func Close() error {
	if DB != nil {
		return DB.Close()
	}
	return nil
}
