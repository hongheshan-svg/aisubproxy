package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// S8：第一次改密码成功后自动删掉随机初始密码文件；文件不存在时改密照样成功。
func TestS8ChangePasswordRemovesInitialPasswordFile(t *testing.T) {
	r := oaRouter(t)
	t.Setenv("DB_PATH", filepath.Join(t.TempDir(), "x.db"))
	oaCreateAdmin(t, "s8_chpwd")
	path := filepath.Join(filepath.Dir(os.Getenv("DB_PATH")), "initial-admin-password.txt")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cs := oaLogin(t, r, "198.51.100.77", "s8_chpwd", oaPassword)
	w := oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/change-password", cookies: cs, header: oaCSRF(cs),
		body: fmt.Sprintf(`{"old_password":%q,"new_password":%q}`, oaPassword, "An0therStr0ngPass")})
	if w.Code != http.StatusOK {
		t.Fatalf("改密应 200，实际 %d：%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("改密成功后文件应被删除：%v", err)
	}

	// 文件不存在时改密照样成功
	cs = oaLogin(t, r, "198.51.100.78", "s8_chpwd", "An0therStr0ngPass")
	w = oaDo(r, oaReq{method: "POST", path: "/api/v1/auth/admin/change-password", cookies: cs, header: oaCSRF(cs),
		body: fmt.Sprintf(`{"old_password":%q,"new_password":%q}`, "An0therStr0ngPass", "Yet4notherStrongPw")})
	if w.Code != http.StatusOK {
		t.Fatalf("文件不存在时改密应 200，实际 %d：%s", w.Code, w.Body.String())
	}
}
