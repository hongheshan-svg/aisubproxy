package handler

import (
	"errors"
	"net/http"
	"testing"

	"github.com/tuzi/cdk-recharge-system/internal/db"
)

func setupEnv(t *testing.T) {
	t.Setenv("SETUP_BOOTSTRAP_TOKEN", oaHSetupToken)
	t.Setenv("INSTALL_MODE", "")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("SETUP_ALLOW_CIDRS", "")
}

func adminCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM admin_users`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// H1-08：管理员建好之后三处失败都回滚创建，安装令牌保留，修好后能直接重试。
func TestSetupBootstrapRollsBackOnLaterFailures(t *testing.T) {
	for _, step := range []string{"lookup_id", "read_hash", "issue_session"} {
		t.Run(step, func(t *testing.T) {
			setupEnv(t)
			r := oaHEngine(t)
			if _, _, err := db.EnsureSetupToken(); err != nil {
				t.Fatal(err)
			}
			setupStepHook = func(s string) error {
				if s == step {
					return errors.New("injected")
				}
				return nil
			}
			t.Cleanup(func() { setupStepHook = nil })
			hdr := map[string]string{"X-Setup-Token": oaHSetupToken}
			w := oaHPost(r, "/bootstrap", `{"mode":"generate","username":"rb_admin"}`, hdr)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("应 500，实际 %d %s", w.Code, w.Body.String())
			}
			if adminCount(t) != 0 || db.IsInstalled() {
				t.Fatalf("管理员应被回滚、站点应仍是未安装")
			}
			if !db.VerifySetupToken(oaHSetupToken) {
				t.Fatal("安装令牌应保留，可重试")
			}
			setupStepHook = nil
			if w := oaHPost(r, "/bootstrap", `{"mode":"generate","username":"rb_admin"}`, hdr); w.Code != http.StatusOK {
				t.Fatalf("重试应 200，实际 %d %s", w.Code, w.Body.String())
			}
		})
	}
}

// 回滚之前就已存在的管理员不能被删：库里不止一个管理员时拒绝回滚。
func TestRollbackAdminBootstrapKeepsExistingAdmins(t *testing.T) {
	oaHEngine(t)
	for _, u := range []string{"old_admin", "new_admin"} {
		if _, err := db.DB.Exec(`INSERT INTO admin_users (username, password_hash, display_name, is_active) VALUES (?, 'x', 'x', 1)`, u); err != nil {
			t.Fatal(err)
		}
	}
	var id int64
	_ = db.DB.QueryRow(`SELECT id FROM admin_users WHERE username='new_admin'`).Scan(&id)
	if err := db.RollbackAdminBootstrap(id, "new_admin", "tok"); err == nil {
		t.Fatal("有别的管理员时应拒绝回滚")
	}
	if err := db.RollbackAdminBootstrap(0, "new_admin", "tok"); err == nil {
		t.Fatal("按用户名回滚也应拒绝")
	}
	if adminCount(t) != 2 {
		t.Fatal("一个管理员都不能少")
	}
}

// H1-09：用户名只收 ASCII 字母、数字、下划线。
func TestValidAdminUsernameASCIIOnly(t *testing.T) {
	for u, want := range map[string]bool{
		"admin": true, "Ab_9": true, "abc": true,
		"管理员账号": false, "ａｄｍｉｎ": false, "ab": false, "a b c": false, "é_é_é": false,
	} {
		if got := validAdminUsername(u); got != want {
			t.Errorf("%q: got %v want %v", u, got, want)
		}
	}
}
