package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

// C-10：数据库出错时，选卡规则接口只返回固定文案，不把原始数据库错误发给前端。
func TestCardSelectionRulesHideDBErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbtest.Open(t)
	if err := db.DB.Close(); err != nil {
		t.Fatal(err)
	}

	run := func(name string, h gin.HandlerFunc, method, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, "/", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h(c)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("%s: 状态码 %d，期望 500", name, w.Code)
		}
		if strings.Contains(w.Body.String(), "closed") || strings.Contains(w.Body.String(), "sql") {
			t.Fatalf("%s: 响应泄露了数据库错误: %s", name, w.Body.String())
		}
	}
	run("读取规则", AdminGetCardSelectionRules, http.MethodGet, "")
	run("保存规则", AdminPutCardSelectionRules, http.MethodPut, `{"rules":[{"plan_key":"plus"}]}`)
	run("站点策略", AdminPutSiteRedeemPolicy, http.MethodPut, `{"enabled":true}`)
}
