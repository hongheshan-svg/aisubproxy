package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
)

type s5AuditPage struct {
	Logs []struct {
		ID int64 `json:"id"`
	} `json:"logs"`
	HasMore      bool  `json:"has_more"`
	NextBeforeID int64 `json:"next_before_id"`
}

func s5GetAudit(t *testing.T, query string) (int, s5AuditPage) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/a", ListAuditLogs)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/a"+query, nil))
	var p s5AuditPage
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	return w.Code, p
}

// R12：审计日志支持 before_id 游标分页。
func TestS5AuditLogsBeforeIDPaging(t *testing.T) {
	dbtest.Open(t)
	for i := 1; i <= 7; i++ {
		db.WriteAudit("admin", "login", fmt.Sprintf("n=%d", i), "1.2.3.4")
	}
	code, p := s5GetAudit(t, "?limit=3")
	if code != 200 || len(p.Logs) != 3 || !p.HasMore || p.Logs[0].ID != 7 || p.NextBeforeID != 5 {
		t.Fatalf("第一页应为 7,6,5 且还有更多: %d %+v", code, p)
	}
	_, p = s5GetAudit(t, fmt.Sprintf("?limit=3&before_id=%d", p.NextBeforeID))
	if len(p.Logs) != 3 || p.Logs[0].ID != 4 || p.Logs[2].ID != 2 || !p.HasMore || p.NextBeforeID != 2 {
		t.Fatalf("第二页应为 4,3,2: %+v", p)
	}
	_, p = s5GetAudit(t, "?limit=3&before_id=2")
	if len(p.Logs) != 1 || p.Logs[0].ID != 1 || p.HasMore || p.NextBeforeID != 0 {
		t.Fatalf("最后一页只有 1 且没有更多: %+v", p)
	}
	if code, _ := s5GetAudit(t, "?before_id=abc"); code != 400 {
		t.Fatalf("before_id 非法应 400: %d", code)
	}
}
