package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
)

// MO-07：发码幂等键由客户端提供，服务端缺头拒绝；兜底找回只认本次请求开始之后创建的码。
type issueFake struct {
	mu        sync.Mutex
	issueKeys []string
	issueFail bool
	list      []map[string]any
}

func (f *issueFake) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.issueKeys...)
}

func newIssueRouter(t *testing.T, f *issueFake) *gin.Engine {
	t.Helper()
	c, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	old := db.DB
	db.DB = c
	t.Cleanup(func() { db.DB = old; c.Close() })
	for _, q := range []string{`CREATE TABLE site_settings(key TEXT PRIMARY KEY,value TEXT,updated_at DATETIME)`, `CREATE TABLE cardplatform_cdk_codes(upstream_id INTEGER,code TEXT UNIQUE NOT NULL,code_prefix TEXT,plan TEXT,fee_amount_minor INTEGER,status TEXT,payment_country TEXT,created_at DATETIME)`} {
		if _, err = c.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/openapi/v1/gpt-direct/plans":
			w.Write([]byte(`{"code":0,"data":{"version":1,"plans":{"plus":{"enabled":true,"currency":"USD"}},"registry":[{"key":"plus","label":"Plus"}]}}`))
		case r.URL.Path == "/openapi/v1/gpt-direct/cdks" && r.Method == http.MethodPost:
			f.mu.Lock()
			f.issueKeys = append(f.issueKeys, r.Header.Get("Idempotency-Key"))
			fail := f.issueFail
			f.mu.Unlock()
			if fail {
				w.WriteHeader(500)
				w.Write([]byte(`{"code":500,"msg":"boom"}`))
				return
			}
			w.Write([]byte(`{"code":0,"data":{"requested":1,"issued":[{"id":1,"code":"ZC-FAKE-TEST-ISSUE-CODE-A","plan":"plus"}]}}`))
		case r.URL.Path == "/openapi/v1/gpt-direct/cdks" && r.Method == http.MethodGet:
			f.mu.Lock()
			list := f.list
			f.mu.Unlock()
			b, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"list": list, "total": len(list)}})
			w.Write(b)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.Close)
	if e := db.SetSetting("card_api_base", s.URL); e != nil {
		t.Fatal(e)
	}
	if e := db.SetSetting("card_api_key", "fixture-key"); e != nil {
		t.Fatal(e)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/issue", CardPlatformIssueCDKs)
	return r
}

func postIssue(r *gin.Engine, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/issue", strings.NewReader(`{"plan":"plus","count":1,"funding_confirmed":true}`))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestIssueWithoutIdempotencyKeyRejected(t *testing.T) {
	f := &issueFake{}
	r := newIssueRouter(t, f)
	for _, key := range []string{"", "   ", strings.Repeat("k", 129)} {
		if w := postIssue(r, key); w.Code != 400 || !strings.Contains(w.Body.String(), "Idempotency-Key") {
			t.Fatalf("缺头或过长应返回 400: key=%q %d %s", key, w.Code, w.Body.String())
		}
	}
	if n := len(f.keys()); n != 0 {
		t.Fatalf("被拒的请求不应到达卡台: %d", n)
	}
}

func TestIssueForwardsSameKeyOnRetry(t *testing.T) {
	f := &issueFake{}
	r := newIssueRouter(t, f)
	for i := 0; i < 2; i++ {
		if w := postIssue(r, "uuid-1234"); w.Code != 200 {
			t.Fatalf("发码失败 %d %s", w.Code, w.Body.String())
		}
	}
	if ks := f.keys(); len(ks) != 2 || ks[0] != "uuid-1234" || ks[1] != "uuid-1234" {
		t.Fatalf("两次请求应把同一个键转给卡台: %v", ks)
	}
}

func TestIssueRecoveryOnlyCountsCodesCreatedAfterRequestStarted(t *testing.T) {
	now := time.Now().UTC()
	f := &issueFake{issueFail: true, list: []map[string]any{
		{"id": 11, "plan": "plus", "code": "ZC-OLD-OTHER-PLACE-CODE-1", "status": "unused", "created_at": now.Add(-time.Hour).Format(time.RFC3339)},
		{"id": 12, "plan": "plus", "code": "ZC-NODATE-OTHER-CODE-22", "status": "unused"},
		{"id": 13, "plan": "plus", "code": "ZC-THIS-REQUEST-CODE-0003", "status": "unused", "created_at": now.Add(time.Minute).Format(time.RFC3339)},
	}}
	r := newIssueRouter(t, f)
	w := postIssue(r, "uuid-recover")
	if w.Code != 200 {
		t.Fatalf("应从卡台找回: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Recovered bool `json:"recovered"`
		Count     int  `json:"count"`
		Issued    []struct {
			ID   int64  `json:"id"`
			Code string `json:"code"`
		} `json:"issued"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Recovered || got.Count != 1 || len(got.Issued) != 1 || got.Issued[0].ID != 13 {
		t.Fatalf("只应找回本次请求开始之后创建的码（id 13）: %s", w.Body.String())
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM cardplatform_cdk_codes`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("其他码不应被入库: n=%d err=%v", n, err)
	}
}

func TestIssueRecoveryWithNothingNewFailsInsteadOfGuessing(t *testing.T) {
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	f := &issueFake{issueFail: true, list: []map[string]any{
		{"id": 11, "plan": "plus", "code": "ZC-OLD-OTHER-PLACE-CODE-1", "status": "unused", "created_at": old},
	}}
	r := newIssueRouter(t, f)
	w := postIssue(r, "uuid-none")
	if w.Code == 200 {
		t.Fatalf("没有本次的码时不应报成功: %s", w.Body.String())
	}
}
