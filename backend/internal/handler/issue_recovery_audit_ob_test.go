package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/db/dbtest"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// C-5 / E-1：发码结果不明时，「找回」只能凭本次请求的幂等键，不能按时间窗扫卡台列表。
// 假卡台按 Idempotency-Key 记批次：同键只出一次码、重放原样返回首次结果（与卡台文档 §4 一致）。

type obIssueUpstream struct {
	mu       sync.Mutex
	nextID   int64
	batches  map[string][]map[string]any // 幂等键 → 这一键发出的码
	list     []map[string]any            // 卡台库存（GET /gpt-direct/cdks 返回的列表）
	posts    []string                    // 每次 POST 带的幂等键
	lists    int                         // GET 列表的次数
	failOnce map[string]bool             // 这些键第一次出码后回 500（码已生成，响应丢了）
	failAll  map[string]bool             // 这些键每次都回 500（重放也拿不到结果）
	hold     map[string]chan struct{}    // 这些键出码后等通道关闭再回响应（模拟慢响应）
	created  map[string]chan struct{}    // 这些键出码后关闭对应通道
	reject   map[string]bool             // 这些键直接业务拒绝（确定没出码）
}

func newObIssueUpstream() *obIssueUpstream {
	return &obIssueUpstream{
		nextID: 100, batches: map[string][]map[string]any{},
		failOnce: map[string]bool{}, failAll: map[string]bool{}, reject: map[string]bool{},
		hold: map[string]chan struct{}{}, created: map[string]chan struct{}{},
	}
}

func (u *obIssueUpstream) issue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Plan  string `json:"plan"`
		Count int    `json:"count"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	key := r.Header.Get("Idempotency-Key")
	u.mu.Lock()
	u.posts = append(u.posts, key)
	if u.reject[key] {
		u.mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":40001,"msg":"余额不足","error":"insufficient_balance"}`))
		return
	}
	batch, replay := u.batches[key]
	if !replay {
		if body.Count < 1 {
			body.Count = 1
		}
		for i := 0; i < body.Count; i++ {
			u.nextID++
			it := map[string]any{
				"id": u.nextID, "plan": body.Plan, "status": "unused",
				"code":       fmt.Sprintf("ZC%04d-OBTEST-%s-ABCDEFGH", u.nextID, strings.ToUpper(key)),
				"created_at": time.Now().UTC().Format(time.RFC3339),
			}
			batch = append(batch, it)
			u.list = append(u.list, it)
		}
		u.batches[key] = batch
	}
	fail := u.failAll[key] || (!replay && u.failOnce[key])
	created, hold := u.created[key], u.hold[key]
	u.mu.Unlock()
	if !replay && created != nil {
		close(created)
	}
	if !replay && hold != nil {
		<-hold
	}
	if fail {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"code":500,"msg":"gateway lost the response"}`))
		return
	}
	if replay {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	b, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"requested": len(batch), "issued": batch}})
	w.Write(b)
}

func (u *obIssueUpstream) codesOf(key string) map[string]bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := map[string]bool{}
	for _, it := range u.batches[key] {
		out[it["code"].(string)] = true
	}
	return out
}

func (u *obIssueUpstream) stats() ([]string, int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.posts...), u.lists
}

func obNewIssueRouter(t *testing.T, u *obIssueUpstream) *gin.Engine {
	t.Helper()
	dbtest.Open(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/openapi/v1/gpt-direct/plans":
			w.Write([]byte(`{"code":0,"data":{"version":1,"plans":{"plus":{"enabled":true,"currency":"USD"}},"registry":[{"key":"plus","label":"Plus"}]}}`))
		case r.URL.Path == "/openapi/v1/gpt-direct/cdks" && r.Method == http.MethodPost:
			u.issue(w, r)
		case r.URL.Path == "/openapi/v1/gpt-direct/cdks" && r.Method == http.MethodGet:
			u.mu.Lock()
			u.lists++
			list := append([]map[string]any(nil), u.list...)
			u.mu.Unlock()
			b, _ := json.Marshal(map[string]any{"code": 0, "data": map[string]any{"list": list, "total": len(list)}})
			w.Write(b)
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":404,"msg":"not found"}`))
		}
	}))
	t.Cleanup(s.Close)
	for k, v := range map[string]string{"card_api_base": s.URL, "card_api_key": "fixture-key", provider.SettingDefault: string(provider.ZovoCard)} {
		if err := db.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/issue", CardPlatformIssueCDKs)
	return r
}

func obPostIssue(r *gin.Engine, key string, count int) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/issue", strings.NewReader(fmt.Sprintf(`{"plan":"plus","count":%d,"funding_confirmed":true}`, count)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

type obIssueResp struct {
	Error         string `json:"error"`
	Recovered     bool   `json:"recovered"`
	Replayed      bool   `json:"replayed"`
	ResultUnknown bool   `json:"result_unknown"`
	Count         int    `json:"count"`
	Issued        []struct {
		ID   int64  `json:"id"`
		Code string `json:"code"`
	} `json:"issued"`
}

func obDecodeIssue(t *testing.T, w *httptest.ResponseRecorder) obIssueResp {
	t.Helper()
	var got obIssueResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是 JSON: %d %s", w.Code, w.Body.String())
	}
	return got
}

func obStoredCodes(t *testing.T) map[string]bool {
	t.Helper()
	rows, err := db.DB.Query(`SELECT code FROM cardplatform_cdk_codes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out[c] = true
	}
	return out
}

// 两批同档位并发：A 的响应丢了，B 已经在卡台出码但响应还没回来。A 的「找回」绝不能包含 B 的码。
func TestObIssueRecoveryNeverTakesConcurrentBatch(t *testing.T) {
	u := newObIssueUpstream()
	u.failOnce["batch-a"] = true
	aArrived, bCreated, releaseB := make(chan struct{}), make(chan struct{}), make(chan struct{})
	u.created["batch-a"] = aArrived
	u.created["batch-b"] = bCreated
	u.hold["batch-b"] = releaseB
	r := obNewIssueRouter(t, u)

	// A 出码后先等 B 也出码，再回 500：B 的码在 A 开始之后创建、本站还没存
	u.mu.Lock()
	u.hold["batch-a"] = bCreated
	u.mu.Unlock()

	var wa, wb *httptest.ResponseRecorder
	aDone, bDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(aDone); wa = obPostIssue(r, "batch-a", 2) }()
	<-aArrived
	go func() { defer close(bDone); wb = obPostIssue(r, "batch-b", 2) }()
	<-bCreated
	// 等 A 的请求处理完再放 B 的响应：A 收尾时 B 的码一定还没在本站落库
	select {
	case <-aDone:
	case <-time.After(10 * time.Second):
		close(releaseB)
		t.Fatal("A 的请求迟迟没有结束")
	}
	close(releaseB)
	<-bDone

	aCodes, bCodes := u.codesOf("batch-a"), u.codesOf("batch-b")
	ga, gb := obDecodeIssue(t, wa), obDecodeIssue(t, wb)
	for _, it := range ga.Issued {
		if bCodes[it.Code] {
			t.Fatalf("A 的结果里出现了 B 的码（一码两卖）: %s", wa.Body.String())
		}
	}
	if wa.Code != http.StatusOK || ga.Count != len(aCodes) {
		t.Fatalf("A 应凭同一个幂等键拿回自己那批 %d 张: %d %s", len(aCodes), wa.Code, wa.Body.String())
	}
	for _, it := range ga.Issued {
		if !aCodes[it.Code] {
			t.Fatalf("A 的结果里有不属于 A 的码 %s", it.Code)
		}
	}
	if wb.Code != http.StatusOK || gb.Count != len(bCodes) {
		t.Fatalf("B 应拿到自己那批: %d %s", wb.Code, wb.Body.String())
	}
	if _, lists := u.stats(); lists != 0 {
		t.Fatalf("不应按时间窗扫卡台列表找回，GET 列表 %d 次", lists)
	}
}

// 结果不明时只用同一个幂等键重放一次，重放拿到的就是这次的批次，按正常成功返回（前端据此换新键）。
func TestObIssueUnknownResultReplaysSameKey(t *testing.T) {
	u := newObIssueUpstream()
	u.failOnce["key-replay"] = true
	r := obNewIssueRouter(t, u)
	w := obPostIssue(r, "key-replay", 1)
	got := obDecodeIssue(t, w)
	if w.Code != http.StatusOK || got.Count != 1 || got.Recovered || !got.Replayed {
		t.Fatalf("重放应拿回本批并标 replayed: %d %s", w.Code, w.Body.String())
	}
	if posts, lists := u.stats(); len(posts) != 2 || posts[0] != "key-replay" || posts[1] != "key-replay" || lists != 0 {
		t.Fatalf("应只用同一个键重放一次、不扫列表: posts=%v lists=%d", posts, lists)
	}
	if stored := obStoredCodes(t); len(stored) != 1 || !stored[got.Issued[0].Code] {
		t.Fatalf("本批应落库: %v", stored)
	}
}

// 重放也拿不到结果：如实报「结果不明」，不返回任何码、不入库、不扫列表；操作者用同一个键再点一次即可。
func TestObIssueUnknownResultWithoutReplayGivesNoCodes(t *testing.T) {
	u := newObIssueUpstream()
	u.failAll["key-lost"] = true
	r := obNewIssueRouter(t, u)
	// 卡台库存里已有别处刚发的码：旧逻辑会把它们当成本批
	u.mu.Lock()
	u.list = append(u.list, map[string]any{"id": 9, "plan": "plus", "status": "unused",
		"code": "ZC-SOMEONE-ELSE-JUST-NOW-0009", "created_at": time.Now().UTC().Add(time.Minute).Format(time.RFC3339)})
	u.mu.Unlock()
	w := obPostIssue(r, "key-lost", 1)
	got := obDecodeIssue(t, w)
	if w.Code < 500 || !got.ResultUnknown || len(got.Issued) != 0 || got.Recovered {
		t.Fatalf("应报结果不明且不给码: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "ZC-SOMEONE-ELSE") {
		t.Fatalf("响应里出现了别处的码: %s", w.Body.String())
	}
	if posts, lists := u.stats(); len(posts) != 2 || lists != 0 {
		t.Fatalf("应重放一次、不扫列表: posts=%v lists=%d", posts, lists)
	}
	if stored := obStoredCodes(t); len(stored) != 0 {
		t.Fatalf("结果不明时不应入库任何码: %v", stored)
	}
}

// 确定没出码（业务拒绝）就不重放，直接把卡台的错误给操作者。
func TestObIssueDefiniteFailureDoesNotReplay(t *testing.T) {
	u := newObIssueUpstream()
	u.reject["key-reject"] = true
	r := obNewIssueRouter(t, u)
	w := obPostIssue(r, "key-reject", 1)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "余额不足") {
		t.Fatalf("业务拒绝应原样报错: %d %s", w.Code, w.Body.String())
	}
	if posts, lists := u.stats(); len(posts) != 1 || lists != 0 {
		t.Fatalf("确定失败不应重放或扫列表: posts=%v lists=%d", posts, lists)
	}
}
