package db_test

import (
	"strings"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// B1-10：环境变量自动建管理员时写一条审计并留一条待发告警；告警和审计里没有密码，用户名可以写。
func TestEnvBootstrapAdminWritesAuditAndNotice(t *testing.T) {
	s8Env(t, t.TempDir())
	t.Setenv("ADMIN_PASSWORD", "Str0ng-Bootstrap-Pass-9")
	_ = db.TakeBootstrapNotice() // 清掉别的用例留下的
	if err := s8Open(t); err != nil {
		t.Fatal(err)
	}
	notice := db.TakeBootstrapNotice()
	if !strings.Contains(notice, "s8admin") {
		t.Fatalf("告警应带用户名: %q", notice)
	}
	if again := db.TakeBootstrapNotice(); again != "" {
		t.Fatalf("告警只取一次: %q", again)
	}
	var action, detail string
	err := db.DB.QueryRow(`SELECT action, detail FROM admin_audit_logs WHERE action = 'admin_bootstrap_env'`).Scan(&action, &detail)
	if err != nil {
		t.Fatalf("应写一条建号审计: %v", err)
	}
	if !strings.Contains(detail, "s8admin") {
		t.Fatalf("审计应带用户名: %q", detail)
	}
	for _, s := range []string{notice, detail} {
		if strings.Contains(s, "Str0ng-Bootstrap-Pass-9") {
			t.Fatalf("不能含密码: %q", s)
		}
	}
}
