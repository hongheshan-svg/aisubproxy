package db_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

const (
	testSessPlain = `{"sessionToken":"AUDIT-SESSION-TOKEN-PLAINTEXT-12345","user":{"email":"buyer@example.com"}}`
	testCDK       = "ZC-AUDIT-SESSION-CODE-0001"
	otherKey      = "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA=" // 32 字节，与 dbtest.SetMasterKey 不同
)

func storedSession(t *testing.T, code string) string {
	t.Helper()
	var s string
	if err := db.DB.QueryRow(`SELECT session_payload FROM cdk_session_bindings WHERE cdk_code = ?`, code).Scan(&s); err != nil {
		t.Fatalf("读 %s: %v", code, err)
	}
	return s
}

func countBindings(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM cdk_session_bindings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// insertLegacyBinding 直接写一行旧版明文（模拟升级前的库）。
func insertLegacyBinding(t *testing.T, code, sess, tok string) {
	t.Helper()
	if _, err := db.DB.Exec(`INSERT INTO cdk_session_bindings (cdk_code, session_payload, redemption_token, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, code, sess, tok); err != nil {
		t.Fatal(err)
	}
}

func setBoundAt(t *testing.T, code string, at time.Time) {
	t.Helper()
	if _, err := db.DB.Exec(`UPDATE cdk_session_bindings SET session_bound_at = ? WHERE cdk_code = ?`, at.Unix(), code); err != nil {
		t.Fatal(err)
	}
}

// 审计 MO-01 的验证测试反过来：库里不再是明文，但凭卡密仍能解出原文。
func TestCDKSessionNotStoredInPlaintext(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if err := db.BindCDKSession("zovocard", testCDK, "rt-1", testSessPlain); err != nil {
		t.Fatal(err)
	}
	stored := storedSession(t, testCDK)
	if stored == testSessPlain || strings.Contains(stored, "AUDIT-SESSION-TOKEN-PLAINTEXT") || strings.Contains(stored, "buyer@example.com") {
		t.Fatalf("库里不应有明文: %q", stored)
	}
	if !strings.HasPrefix(stored, "v1:") {
		t.Fatalf("应是 secretbox 密文: %q", stored)
	}
	got, err := db.GetSessionByCDK("zc-audit-session-code-0001") // 大小写不敏感
	if err != nil || got != testSessPlain {
		t.Fatalf("凭卡密应能解出原文: %q %v", got, err)
	}
	// 邮箱也不能以明文落库
	var email string
	if err := db.DB.QueryRow(`SELECT account_email_sealed FROM cdk_session_bindings WHERE cdk_code = ?`, testCDK).Scan(&email); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(email, "buyer@example.com") || !strings.HasPrefix(email, "v1:") {
		t.Fatalf("邮箱应加密保存: %q", email)
	}
}

func TestBindCDKSessionRefusesWithoutMasterKey(t *testing.T) {
	for _, key := range []string{"", "not-base64!"} {
		dbtest.Open(t)
		t.Setenv("APP_MASTER_KEY", key)
		err := db.BindCDKSession("zovocard", testCDK, "rt-1", testSessPlain)
		if err == nil {
			t.Fatalf("密钥 %q 时应拒绝写入", key)
		}
		if key == "" && !errors.Is(err, secretbox.ErrNoMasterKey) {
			t.Fatalf("应能识别为缺密钥: %v", err)
		}
		if n := countBindings(t); n != 0 {
			t.Fatalf("拒绝写入时表中不应有行: %d", n)
		}
	}
}

// 只有 token 的路径（RT: 占位行与更新已绑定行）同样加密。
func TestBindCDKSessionByTokenIsSealed(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if err := db.BindCDKSession("zovocard", "", "tok-only", testSessPlain); err != nil {
		t.Fatal(err)
	}
	if s := storedSession(t, "RT:tok-only"); strings.Contains(s, "AUDIT-SESSION") {
		t.Fatalf("RT: 占位行不应是明文: %q", s)
	}
	if err := db.BindCDKRedemptionToken("zovocard", testCDK, "tok-2"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindCDKSession("zovocard", "", "tok-2", testSessPlain); err != nil {
		t.Fatal(err)
	}
	if s := storedSession(t, testCDK); strings.Contains(s, "AUDIT-SESSION") || s == "" {
		t.Fatalf("按 token 更新的行不应是明文: %q", s)
	}
	if got, err := db.GetSessionByCDK(testCDK); err != nil || got != testSessPlain {
		t.Fatalf("应能解出: %q %v", got, err)
	}
}

// 密钥缺失时读取不能把兑换进度查询带崩：token 照常可读，会话为空。
func TestGetBindingSurvivesMissingKey(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if err := db.BindCDKSession("zovocard", testCDK, "rt-1", testSessPlain); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_MASTER_KEY", "")
	b, err := db.GetBindingByCDK(testCDK)
	if err != nil || b == nil {
		t.Fatalf("密钥缺失不应让绑定读取失败: %+v %v", b, err)
	}
	if b.RedemptionToken != "rt-1" || b.SessionPayload != "" || !b.HasSession {
		t.Fatalf("token 应可读、会话应为空、仍标记有会话: %+v", b)
	}
	if _, err := db.GetSessionByCDK(testCDK); err == nil {
		t.Fatal("GetSessionByCDK 解不开时应返回错误")
	}
}

func TestMigrateEncryptsLegacyPlaintext(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	insertLegacyBinding(t, testCDK, testSessPlain, "rt-1")
	insertLegacyBinding(t, "ZC-TOKEN-ONLY-RAW", "eyJraw.access.token", "rt-2")
	insertLegacyBinding(t, "ZC-PREVIEW-ONLY", "", "rt-3")
	if err := db.MigrateCDKSessions(); err != nil {
		t.Fatal(err)
	}
	s1 := storedSession(t, testCDK)
	if strings.Contains(s1, "AUDIT-SESSION") || !strings.HasPrefix(s1, "v1:") {
		t.Fatalf("应已加密: %q", s1)
	}
	if got, err := db.GetSessionByCDK(testCDK); err != nil || got != testSessPlain {
		t.Fatalf("迁移后应能解出原文: %q %v", got, err)
	}
	if got, err := db.GetSessionByCDK("ZC-TOKEN-ONLY-RAW"); err != nil || got != "eyJraw.access.token" {
		t.Fatalf("非 JSON 会话也要能解出: %q %v", got, err)
	}
	if s := storedSession(t, "ZC-PREVIEW-ONLY"); s != "" {
		t.Fatalf("空会话行不应被加密: %q", s)
	}
	if em, err := db.GetAccountEmailByCDK(testCDK); err != nil || em != "buyer@example.com" {
		t.Fatalf("迁移应提取邮箱: %q %v", em, err)
	}
	for code, tok := range map[string]string{testCDK: "rt-1", "ZC-TOKEN-ONLY-RAW": "rt-2", "ZC-PREVIEW-ONLY": "rt-3"} {
		if b, err := db.GetBindingByCDK(code); err != nil || b == nil || b.RedemptionToken != tok {
			t.Fatalf("%s 的 token 不应变化: %+v %v", code, b, err)
		}
	}
	// 幂等：再跑一次，密文原样不动
	if err := db.MigrateCDKSessions(); err != nil {
		t.Fatal(err)
	}
	if s := storedSession(t, testCDK); s != s1 {
		t.Fatalf("重跑不应再次加密: %q -> %q", s1, s)
	}
	if got, err := db.GetSessionByCDK(testCDK); err != nil || got != testSessPlain {
		t.Fatalf("重跑后仍应能解出原文: %q %v", got, err)
	}
}

// 中断后重跑：跑到一半停住（模拟崩溃），已加密的行不动，其余的接着加密，一行不丢。
func TestMigrateResumesAfterInterruption(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	plain := map[string]string{}
	for _, c := range []string{"ZC-A", "ZC-B", "ZC-C", "ZC-D", "ZC-E"} {
		sess := `{"sessionToken":"tok-` + c + `","user":{"email":"` + strings.ToLower(c) + `@example.com"}}`
		plain[c] = sess
		insertLegacyBinding(t, c, sess, "")
	}
	res, err := db.EncryptLegacyCDKSessionsN(2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Encrypted != 2 {
		t.Fatalf("第一轮应加密 2 行: %+v", res)
	}
	sealedFirst := map[string]string{}
	plainLeft := 0
	for c, sess := range plain {
		s := storedSession(t, c)
		if s == sess {
			plainLeft++
			continue
		}
		sealedFirst[c] = s
	}
	if plainLeft != 3 || len(sealedFirst) != 2 {
		t.Fatalf("应 2 行已加密、3 行还是明文: %d %d", len(sealedFirst), plainLeft)
	}
	if err := db.MigrateCDKSessions(); err != nil {
		t.Fatal(err)
	}
	for c, sess := range plain {
		if s := storedSession(t, c); strings.Contains(s, "tok-") {
			t.Fatalf("%s 仍是明文: %q", c, s)
		}
		if want, ok := sealedFirst[c]; ok && storedSession(t, c) != want {
			t.Fatalf("%s 已加密的行不应被改动", c)
		}
		if got, err := db.GetSessionByCDK(c); err != nil || got != sess {
			t.Fatalf("%s 应能解出原文: %q %v", c, got, err)
		}
	}
}

// 密钥缺失或不合法：跳过且绝不改动现有数据；配上密钥后再跑才加密。
func TestMigrateSkipsWithoutKeyAndKeepsData(t *testing.T) {
	for _, bad := range []string{"", "not-base64!"} {
		dbtest.Open(t)
		t.Setenv("APP_MASTER_KEY", bad)
		insertLegacyBinding(t, testCDK, testSessPlain, "rt-1")
		if err := db.MigrateCDKSessions(); err != nil {
			t.Fatalf("缺密钥不应让启动失败: %v", err)
		}
		if s := storedSession(t, testCDK); s != testSessPlain {
			t.Fatalf("缺密钥时不应改动数据: %q", s)
		}
		if em, err := db.GetAccountEmailByCDK(testCDK); err != nil || em != "buyer@example.com" {
			t.Fatalf("未迁移的旧行仍应能读出邮箱: %q %v", em, err)
		}
		dbtest.SetMasterKey(t)
		if err := db.MigrateCDKSessions(); err != nil {
			t.Fatal(err)
		}
		if s := storedSession(t, testCDK); strings.Contains(s, "AUDIT-SESSION") {
			t.Fatalf("配上密钥后应加密: %q", s)
		}
	}
}

// 用别的密钥加密的行解不开：不能二次加密、不能清掉，留给配回正确密钥。
func TestMigrateLeavesForeignCiphertextAlone(t *testing.T) {
	dbtest.Open(t)
	t.Setenv("APP_MASTER_KEY", otherKey)
	if err := db.BindCDKSession("zovocard", testCDK, "rt-1", testSessPlain); err != nil {
		t.Fatal(err)
	}
	before := storedSession(t, testCDK)
	dbtest.SetMasterKey(t)
	if err := db.MigrateCDKSessions(); err != nil {
		t.Fatal(err)
	}
	if s := storedSession(t, testCDK); s != before {
		t.Fatalf("别的密钥加密的行不应被改动: %q", s)
	}
	t.Setenv("APP_MASTER_KEY", otherKey)
	if got, err := db.GetSessionByCDK(testCDK); err != nil || got != testSessPlain {
		t.Fatalf("配回原密钥应能解出: %q %v", got, err)
	}
}

// 旧表结构（没有新列）升级：新列补齐，绑定时间取 updated_at，明文被加密。
func TestMigrateUpgradesOldSchema(t *testing.T) {
	c, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old })
	dbtest.SetMasterKey(t)
	for _, q := range []string{
		`CREATE TABLE cdk_session_bindings (cdk_code TEXT PRIMARY KEY, session_payload TEXT NOT NULL, redemption_token TEXT, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`INSERT INTO cdk_session_bindings VALUES ('ZC-OLD', '` + testSessPlain + `', 'rt', '2026-01-02 03:04:05')`,
		`CREATE TABLE site_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE cardplatform_cdk_codes (upstream_id INTEGER, code TEXT NOT NULL UNIQUE, status TEXT DEFAULT '')`,
	} {
		if _, err := c.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := db.MigrateCDKSessions(); err != nil {
			t.Fatalf("第 %d 次迁移: %v", i+1, err)
		}
	}
	var boundAt int64
	if err := c.QueryRow(`SELECT session_bound_at FROM cdk_session_bindings WHERE cdk_code='ZC-OLD'`).Scan(&boundAt); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Unix(); boundAt != want {
		t.Fatalf("绑定时间应取 updated_at: %d != %d", boundAt, want)
	}
	if s := storedSession(t, "ZC-OLD"); strings.Contains(s, "AUDIT-SESSION") {
		t.Fatalf("应已加密: %q", s)
	}
}

// 数据库文件落盘后不能再搜到明文：删掉的内容会留在空闲页里，所以迁移后要 VACUUM。
func TestMigrateLeavesNoPlaintextInDatabaseFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m.db")
	c, err := sql.Open("sqlite3", p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old })
	dbtest.SetMasterKey(t)
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		insertLegacyBinding(t, fmt.Sprintf("ZC-FILE-%04d", i), testSessPlain, "")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "AUDIT-SESSION-TOKEN-PLAINTEXT") {
		t.Fatal("前提不成立：写入后文件里应能搜到明文")
	}
	if err := db.MigrateCDKSessions(); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "AUDIT-SESSION-TOKEN-PLAINTEXT") {
		t.Fatal("迁移后数据库文件里仍有明文残留")
	}
	if v, _ := db.GetSetting("cdk_session_vacuum_pending"); v != "" {
		t.Fatalf("VACUUM 完成后待办标记应清掉: %q", v)
	}
}

func TestSweepClearsExpiredButKeepsTokenAndEmail(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	now := time.Now()
	if err := db.BindCDKSession("zovocard", "ZC-OLD-ONE", "rt-old", testSessPlain); err != nil {
		t.Fatal(err)
	}
	if err := db.BindCDKSession("zovocard", "ZC-NEW-ONE", "rt-new", testSessPlain); err != nil {
		t.Fatal(err)
	}
	setBoundAt(t, "ZC-OLD-ONE", now.Add(-db.CDKSessionTTL-time.Minute))
	setBoundAt(t, "ZC-NEW-ONE", now.Add(-db.CDKSessionTTL+time.Hour))

	res, err := db.SweepCDKSessions(now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Cleared != 1 || res.Skipped {
		t.Fatalf("应清 1 行: %+v", res)
	}
	if s := storedSession(t, "ZC-OLD-ONE"); s != "" {
		t.Fatalf("过期会话应清空: %q", s)
	}
	if s := storedSession(t, "ZC-NEW-ONE"); s == "" {
		t.Fatal("未过期会话不应清空")
	}
	b, err := db.GetBindingByCDK("ZC-OLD-ONE")
	if err != nil || b == nil || b.RedemptionToken != "rt-old" || b.HasSession || b.SessionPayload != "" {
		t.Fatalf("清空后 token 要在、会话要空: %+v %v", b, err)
	}
	if b.AccountEmail != "buyer@example.com" {
		t.Fatalf("清空后邮箱要在: %+v", b)
	}
	if code, err := db.FindCodeByRedemptionToken("rt-old"); err != nil || code != "ZC-OLD-ONE" {
		t.Fatalf("token 反查不能断: %q %v", code, err)
	}
	if s, err := db.GetSessionByCDK("ZC-OLD-ONE"); err != nil || s != "" {
		t.Fatalf("清空后取会话应为空且不报错: %q %v", s, err)
	}
	// 幂等
	if res, err := db.SweepCDKSessions(now); err != nil || res.Cleared != 0 {
		t.Fatalf("重跑不应再清: %+v %v", res, err)
	}
}

// 兑换完成（本站 CDK 状态已是 consumed）的会话不必等 7 天；大小写不同的码也要对上。
func TestSweepClearsConsumedCodes(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if err := db.SaveCardplatformCDKCodeWithStatus(1, "zc-Done-Code-0001", "zc-Done", "plus", 0, "consumed"); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveCardplatformCDKCodeWithStatus(2, "ZC-RUNNING-CODE-1", "ZC-RUN", "plus", 0, "unused"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"zc-Done-Code-0001", "ZC-RUNNING-CODE-1"} {
		if err := db.BindCDKSession("zovocard", c, "rt-"+c, testSessPlain); err != nil {
			t.Fatal(err)
		}
	}
	res, err := db.SweepCDKSessions(time.Now())
	if err != nil || res.Cleared != 1 {
		t.Fatalf("应清 1 行: %+v %v", res, err)
	}
	if s := storedSession(t, "ZC-DONE-CODE-0001"); s != "" {
		t.Fatalf("已完成的应清空: %q", s)
	}
	if s := storedSession(t, "ZC-RUNNING-CODE-1"); s == "" {
		t.Fatal("兑换中的不应清空")
	}
}

// 公开的 preview 接口会写 token 并更新 updated_at：反复预览不能让会话一直不过期。
func TestPreviewDoesNotExtendSessionTTL(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	now := time.Now()
	if err := db.BindCDKSession("zovocard", testCDK, "rt-1", testSessPlain); err != nil {
		t.Fatal(err)
	}
	setBoundAt(t, testCDK, now.Add(-db.CDKSessionTTL-time.Minute))
	if err := db.BindCDKRedemptionToken("zovocard", testCDK, "rt-2"); err != nil {
		t.Fatal(err)
	}
	if res, err := db.SweepCDKSessions(now); err != nil || res.Cleared != 1 {
		t.Fatalf("预览后仍应按首次绑定时间过期: %+v %v", res, err)
	}
	if b, err := db.GetBindingByCDK(testCDK); err != nil || b.RedemptionToken != "rt-2" {
		t.Fatalf("新 token 应保留: %+v %v", b, err)
	}
	// 用户重新提交会话才算重新计时
	if err := db.BindCDKSession("zovocard", testCDK, "", testSessPlain); err != nil {
		t.Fatal(err)
	}
	if res, err := db.SweepCDKSessions(now); err != nil || res.Cleared != 0 {
		t.Fatalf("新提交的会话不应被清: %+v %v", res, err)
	}
}

// 密钥缺失：清理整轮跳过，绝不清除无法保全邮箱的数据。
func TestSweepSkipsWithoutKey(t *testing.T) {
	dbtest.Open(t)
	t.Setenv("APP_MASTER_KEY", "")
	insertLegacyBinding(t, testCDK, testSessPlain, "rt-1")
	setBoundAt(t, testCDK, time.Now().Add(-30*24*time.Hour))
	res, err := db.SweepCDKSessions(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || res.Cleared != 0 || res.PlaintextLeft != 1 {
		t.Fatalf("应跳过并报告残留明文行数: %+v", res)
	}
	if s := storedSession(t, testCDK); s != testSessPlain {
		t.Fatalf("不应改动数据: %q", s)
	}
}

// 读到旧值之后用户又提交了新会话：条件更新不命中，新会话不能被清掉。
func TestClearDoesNotOverwriteNewerSession(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if err := db.BindCDKSession("zovocard", testCDK, "rt-1", testSessPlain); err != nil {
		t.Fatal(err)
	}
	stale := storedSession(t, testCDK)
	if err := db.BindCDKSession("zovocard", testCDK, "", `{"sessionToken":"newer","user":{"email":"new@example.com"}}`); err != nil {
		t.Fatal(err)
	}
	cleared, err := db.ClearCDKSessionRowIf(testCDK, stale)
	if err != nil {
		t.Fatal(err)
	}
	if cleared {
		t.Fatal("旧值已过时，不应清除")
	}
	if got, err := db.GetSessionByCDK(testCDK); err != nil || !strings.Contains(got, "newer") {
		t.Fatalf("新会话应还在: %q %v", got, err)
	}
	if em, err := db.GetAccountEmailByCDK(testCDK); err != nil || em != "new@example.com" {
		t.Fatalf("邮箱应是新会话的: %q %v", em, err)
	}
}

func TestClearByCodeAndToken(t *testing.T) {
	dbtest.Open(t)
	dbtest.SetMasterKey(t)
	if err := db.BindCDKSession("zovocard", testCDK, "rt-1", testSessPlain); err != nil {
		t.Fatal(err)
	}
	if err := db.BindCDKSession("zovocard", "", "rt-only", testSessPlain); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearCDKSessionByCode(strings.ToLower(testCDK)); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearCDKSessionByToken("rt-only"); err != nil {
		t.Fatal(err)
	}
	if s := storedSession(t, testCDK); s != "" {
		t.Fatalf("按码应清空: %q", s)
	}
	if s := storedSession(t, "RT:rt-only"); s != "" {
		t.Fatalf("按 token 应清空: %q", s)
	}
	if em, err := db.GetAccountEmailByCDK(testCDK); err != nil || em != "buyer@example.com" {
		t.Fatalf("邮箱应保留: %q %v", em, err)
	}
	// 没有这一行、已清空都不算错
	if err := db.ClearCDKSessionByCode("ZC-NOT-THERE"); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearCDKSessionByCode(testCDK); err != nil {
		t.Fatal(err)
	}
}
