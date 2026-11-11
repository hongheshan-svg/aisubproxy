package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// H1-13b：「至少 12 位」按字符数算。4 个汉字是 12 个字节，但只有 4 个字符，不够。
func TestAdminChangePasswordCountsRunes(t *testing.T) {
	dbtest.Open(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/pw", AdminChangePassword)
	post := func(newPw string) int {
		req := httptest.NewRequest(http.MethodPost, "/pw", strings.NewReader(`{"old_password":"old-password-1","new_password":"`+newPw+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if code := post("密码密码"); code != http.StatusBadRequest {
		t.Fatalf("4 个汉字应 400（长度不够），实际 %d", code)
	}
	// 12 个字符的汉字密码过了长度检查（后面因为没登录用户走别的错误，不再是长度的 400）
	if code := post(strings.Repeat("密", 6) + "abcDEF"); code == http.StatusBadRequest {
		t.Fatalf("12 个字符不应因长度被拒，实际 %d", code)
	}
}

func TestSwapPasswordCountsRunes(t *testing.T) {
	dbtest.Open(t)
	if err := validateAgentSwapPassword("密码密码"); err != errPasswordTooShort {
		t.Fatalf("4 个汉字应不够长，实际 %v", err)
	}
	if err := validateAgentSwapPassword("换码口令是这个吧一二三四五"); err != nil {
		t.Fatalf("12 个以上字符应通过：%v", err)
	}
	// 短口令使用时打弱标记按字符数判断
	_ = db.DeleteSetting(agentSwapWeakKey)
	noteAgentSwapPasswordUsed("密码密码密码密码密码密码") // 12 字符，不应标弱
	if agentSwapPasswordWeak() {
		t.Fatal("12 个字符不应标弱")
	}
	noteAgentSwapPasswordUsed("密码密码")
	if !agentSwapPasswordWeak() {
		t.Fatal("4 个字符应标弱")
	}
}

func TestIsWeakPasswordCountsRunes(t *testing.T) {
	if !db.IsWeakPassword("密码密码") {
		t.Fatal("4 个汉字（12 字节）应算太短")
	}
	if db.IsWeakPassword("密码是这个吧一二三四五六") {
		t.Fatal("12 个字符不应算太短")
	}
}

func TestAdminChangePasswordTooLongRejected(t *testing.T) {
	dbtest.Open(t)
	const oldPw = "Old-Password-12345"
	if err := db.CreateAdminUser("chpw_admin", oldPw, "x"); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/pw", func(c *gin.Context) { c.Set("username", "chpw_admin"); AdminChangePassword(c) })
	for _, pw := range []string{strings.Repeat("aB3x", 19), strings.Repeat("强", 25)} {
		req := httptest.NewRequest(http.MethodPost, "/pw", strings.NewReader(`{"old_password":"`+oldPw+`","new_password":"`+pw+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "太长") {
			t.Fatalf("过长应 400 太长：%d %s", w.Code, w.Body.String())
		}
		var hash string
		if err := db.DB.QueryRow(`SELECT password_hash FROM admin_users WHERE username='chpw_admin'`).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		if ok, _ := verifyAdminPassword(hash, oldPw); !ok {
			t.Fatal("被拒绝后原密码必须仍然有效")
		}
	}
}
