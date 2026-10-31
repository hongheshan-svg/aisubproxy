package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

const (
	cpPathCreateHolder = "/v1/cardholder"
	cpPathHolderH1     = "/v1/cardholder/h1"
	cpPathUpload       = "/v1/file/upload"
)

// 测试资料里的敏感值，不应出现在审计与日志中
const (
	cpPIIIDNo   = "P99887766"
	cpPIISSN    = "123-45-6789"
	cpPIIDOB    = "1990-05-17"
	cpPIIMobile = "4085550123"
	cpPIIEmail  = "john.doe@example.com"
	cpPIIStreet = "1 Market Street"
)

var cpPIIValues = []string{cpPIIIDNo, cpPIISSN, cpPIIDOB, cpPIIMobile, cpPIIEmail, cpPIIStreet}

func cpProfile() marswallex.CardholderProfile {
	addr := marswallex.Address{Country: "US", State: "CA", City: "San Jose", Address: cpPIIStreet, PostCode: "95113"}
	return marswallex.CardholderProfile{
		FirstName: "John", LastName: "Smith",
		DateOfBirth: cpPIIDOB, ZoneNumber: "1", Mobile: cpPIIMobile, Email: cpPIIEmail,
		CountryCode: "US", IDType: "PASSPORT", IDNo: cpPIIIDNo, IDPeriodBegin: "2020-01-01", IDPeriodEnd: "9999-12-31",
		IDPhotoFileIDs: []string{"file-1"}, ResidentialAddress: addr, BillingAddress: addr, SSN: cpPIISSN,
	}
}

func cpProfileJSON(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(cpProfile())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cpHolderDetailJSON(t *testing.T, id, status string) string {
	t.Helper()
	b, err := json.Marshal(marswallex.CardholderDetail{CardHolderID: id, CardholderProfile: cpProfile(), Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cpInsertHolder(t *testing.T, id, status string) {
	t.Helper()
	if _, err := db.DB.Exec(`INSERT INTO mw_cardholders (card_holder_id, first_name, last_name, country_code, status, remark, note, synced_at, created_at)
		VALUES (?, 'John', 'Smith', 'US', ?, '', '', 1, 1)`, id, status); err != nil {
		t.Fatal(err)
	}
}

// cpCaptureLog 把标准日志重定向到缓冲区，测试结束后还原。
func cpCaptureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// cpUpload 以 multipart 上传一个文件（field 为空时只带一个普通字段）。
func cpUpload(t *testing.T, field, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if field == "" {
		if err := w.WriteField("note", "x"); err != nil {
			t.Fatal(err)
		}
	} else {
		fw, err := w.CreateFormFile(field, filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("username", "alice") })
	r.POST("/photos", AdminCardPoolUploadPhoto)
	req := httptest.NewRequest("POST", "/photos", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

var cpPNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)

func TestUploadPhotoTooLarge(t *testing.T) {
	_, mw, _ := cpEnv(t, false)

	big := append(append([]byte{}, cpPNG...), bytes.Repeat([]byte{1}, 8<<20)...)
	w := cpUpload(t, "file", "big.png", big)
	if w.Code != http.StatusRequestEntityTooLarge || mwJSON(t, w)["error"] != "文件超过 7MB" {
		t.Fatalf("8MB: %d %s", w.Code, w.Body.String())
	}
	// 请求体在上限内、文件本身超过 7MB
	over := append(append([]byte{}, cpPNG...), bytes.Repeat([]byte{1}, 7<<20)...)
	if w := cpUpload(t, "file", "over.png", over); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("7MB+: %d %s", w.Code, w.Body.String())
	}
	if w := cpUpload(t, "", "", nil); w.Code != http.StatusBadRequest {
		t.Errorf("missing file: %d %s", w.Code, w.Body.String())
	}
	if w := cpDo(AdminCardPoolUploadPhoto, "POST", "/x", "/x", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("not multipart: %d %s", w.Code, w.Body.String())
	}
	// 扩展名伪装：按内容嗅探拒绝
	if w := cpUpload(t, "file", "a.jpg", []byte("<html><body>x</body></html>")); w.Code != http.StatusBadRequest {
		t.Errorf("html as jpg: %d %s", w.Code, w.Body.String())
	}
	if n := len(mw.Requests("POST", cpPathUpload)); n != 0 {
		t.Fatalf("upload requests=%d", n)
	}

	mw.On("POST", cpPathUpload, mwtest.OK(`{"file_id":"F123"}`))
	w = cpUpload(t, "file", "id.png", cpPNG)
	if w.Code != 200 || mwJSON(t, w)["file_id"] != "F123" {
		t.Fatalf("upload %d %s", w.Code, w.Body.String())
	}
	reqs := mw.Requests("POST", cpPathUpload)
	if len(reqs) != 1 || !bytes.Contains(reqs[0].Body, cpPNG) || !strings.Contains(reqs[0].RawQuery, "biz_type=CARDHOLDER_ID_PHOTO") {
		t.Errorf("forwarded %d %q", len(reqs), reqs[0].RawQuery)
	}
}

func TestCreateCardholderUnknownReturns202(t *testing.T) {
	c, mw, _ := cpEnv(t, false)

	// 校验失败不调平台
	bad := cpProfile()
	bad.IDPhotoFileIDs = nil
	badJSON, _ := json.Marshal(bad)
	if w := cpDo(AdminCardPoolCreateCardholder, "POST", "/x", "/x", string(badJSON)); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid: %d %s", w.Code, w.Body.String())
	}
	if n := len(mw.Requests("POST", cpPathCreateHolder)); n != 0 {
		t.Fatalf("create requests=%d", n)
	}

	mw.On("POST", cpPathCreateHolder, mwtest.Unknown())
	w := cpDo(AdminCardPoolCreateCardholder, "POST", "/x", "/x", cpProfileJSON(t))
	if w.Code != http.StatusAccepted {
		t.Fatalf("unknown: %d %s", w.Code, w.Body.String())
	}
	m := mwJSON(t, w)
	sub, _ := m["submission"].(map[string]any)
	outID, _ := sub["out_card_holder_id"].(string)
	if m["unknown"] != true || sub["state"] != "unknown" || !strings.HasPrefix(outID, "CH") {
		t.Fatalf("body %v", m)
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.holder_create out_card_holder_id="+outID+" card_holder_id=") {
		t.Errorf("audit %q", a)
	}

	// 未知的提交记录出现在列表里，可以放弃
	list := mwJSON(t, cpDo(AdminCardPoolSubmissions, "GET", "/x", "/x", ``))
	items, _ := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["out_card_holder_id"] != outID {
		t.Fatalf("submissions %v", list)
	}
	ab := cpDo(AdminCardPoolAbandonSubmission, "POST", "/s/:out_id/abandon", "/s/"+outID+"/abandon", ``)
	if ab.Code != 200 || mwJSON(t, ab)["ok"] != true {
		t.Fatalf("abandon %d %s", ab.Code, ab.Body.String())
	}
	if n := mwCount(t, c, `SELECT COUNT(*) FROM mw_cardholder_submissions WHERE state='abandoned' AND out_card_holder_id='`+outID+`'`); n != 1 {
		t.Errorf("not abandoned")
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.holder_abandon out_card_holder_id="+outID) {
		t.Errorf("audit %q", a)
	}
	if w := cpDo(AdminCardPoolAbandonSubmission, "POST", "/s/:out_id/abandon", "/s/CHnope/abandon", ``); w.Code != http.StatusNotFound {
		t.Errorf("abandon missing: %d %s", w.Code, w.Body.String())
	}

	// 平台明确成功 → 200，提交记录已关联
	mw.On("POST", cpPathCreateHolder, mwtest.OK(`{"card_holder_id":"h9"}`))
	w = cpDo(AdminCardPoolCreateCardholder, "POST", "/x", "/x", cpProfileJSON(t))
	if w.Code != 200 {
		t.Fatalf("success: %d %s", w.Code, w.Body.String())
	}
	m = mwJSON(t, w)
	sub, _ = m["submission"].(map[string]any)
	if m["unknown"] != nil || sub["state"] != "linked" || sub["card_holder_id"] != "h9" {
		t.Fatalf("body %v", m)
	}
	if a := cpAudit(t, c); !strings.Contains(a, "card_holder_id=h9") {
		t.Errorf("audit %q", a)
	}
}

func TestCardholderStatusEndpoint(t *testing.T) {
	c, mw, _ := cpEnv(t, false)
	cpInsertHolder(t, "h1", "APPROVED")
	if w := cpDo(AdminCardPoolCardholderStatus, "POST", "/h/:id/status", "/h/h1/status", `{"status":"SUSPENDED"}`); w.Code != 400 {
		t.Errorf("bad status: %d %s", w.Code, w.Body.String())
	}
	mw.On("PUT", "/v1/cardholder/status/h1", mwtest.OK(`{"card_holder_id":"h1","status":"DISABLED"}`))
	mw.On("GET", cpPathHolderH1, mwtest.OK(cpHolderDetailJSON(t, "h1", "DISABLED")))
	w := cpDo(AdminCardPoolCardholderStatus, "POST", "/h/:id/status", "/h/h1/status", `{"status":"DISABLED"}`)
	if w.Code != 200 || mwJSON(t, w)["status"] != "DISABLED" {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	if a := cpAudit(t, c); !strings.Contains(a, "cardpool.holder_status card_holder_id=h1 status=DISABLED") {
		t.Errorf("audit %q", a)
	}
}

func TestHolderEndpointsNoPIIInAuditOrLogs(t *testing.T) {
	buf := cpCaptureLog(t)
	c, mw, _ := cpEnv(t, false)
	cpInsertHolder(t, "h1", "APPROVED")
	// 平台错误信息回显了证件号与 SSN
	echo := mwtest.Reply{Status: 400, Body: `{"code":"PARAM","message":"id_no ` + cpPIIIDNo + ` ssn ` + cpPIISSN + ` invalid"}`}

	// 新建：成功、被拒（回显资料）、结果未知
	mw.On("POST", cpPathCreateHolder, mwtest.OK(`{"card_holder_id":"h2"}`), echo, mwtest.Unknown())
	for i, want := range []int{200, 502, 202} {
		if w := cpDo(AdminCardPoolCreateCardholder, "POST", "/x", "/x", cpProfileJSON(t)); w.Code != want {
			t.Fatalf("create #%d: %d %s", i, w.Code, w.Body.String())
		}
	}

	// 修改：成功（重新导入时平台详情含完整资料）、被拒（回显资料）
	mw.On("PUT", cpPathHolderH1, mwtest.OK(`{"card_holder_id":"h1"}`), echo)
	mw.On("GET", cpPathHolderH1, mwtest.OK(cpHolderDetailJSON(t, "h1", "PENDING_REVIEW")))
	w := cpDo(AdminCardPoolUpdateCardholder, "PUT", "/h/:id", "/h/h1", cpProfileJSON(t))
	if w.Code != 200 || mwJSON(t, w)["card_holder_id"] != "h1" {
		t.Fatalf("update %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), cpPIIIDNo) {
		t.Errorf("update response carries profile: %s", w.Body.String())
	}
	if w := cpDo(AdminCardPoolUpdateCardholder, "PUT", "/h/:id", "/h/h1", cpProfileJSON(t)); w.Code != http.StatusBadGateway {
		t.Fatalf("update rejected %d %s", w.Code, w.Body.String())
	}
	if w := cpDo(AdminCardPoolUpdateCardholder, "PUT", "/h/:id", "/h/nope", cpProfileJSON(t)); w.Code != http.StatusNotFound {
		t.Errorf("update missing %d %s", w.Code, w.Body.String())
	}

	// 查资料：响应里有完整资料，但不进审计与日志
	w = cpDo(AdminCardPoolCardholderProfile, "GET", "/h/:id/profile", "/h/h1/profile", ``)
	if w.Code != 200 {
		t.Fatalf("profile %d %s", w.Code, w.Body.String())
	}
	if m := mwJSON(t, w); m["id_no"] != cpPIIIDNo || m["ssn"] != cpPIISSN || m["card_holder_id"] != "h1" {
		t.Errorf("profile body %v", m)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control=%q", w.Header().Get("Cache-Control"))
	}
	mw.On("GET", cpPathHolderH1, echo)
	if w := cpDo(AdminCardPoolCardholderProfile, "GET", "/h/:id/profile", "/h/h1/profile", ``); w.Code != http.StatusBadGateway {
		t.Errorf("profile rejected %d %s", w.Code, w.Body.String())
	}

	audit := cpAudit(t, c)
	// 查资料成功才写审计，内容只有持卡人 ID（被拒的那次不写）
	if n := mwCount(t, c, `SELECT COUNT(*) FROM admin_audit_logs WHERE action='cardpool.holder_profile'`); n != 1 {
		t.Errorf("holder_profile audit rows = %d", n)
	}
	if n := mwCount(t, c, `SELECT COUNT(*) FROM admin_audit_logs WHERE action='cardpool.holder_profile' AND detail='card_holder_id=h1'`); n != 1 {
		t.Errorf("holder_profile audit detail: %q", audit)
	}
	for _, want := range []string{"cardpool.holder_create out_card_holder_id=CH", "card_holder_id=h2", "cardpool.holder_update card_holder_id=h1",
		"cardpool.holder_profile card_holder_id=h1"} {
		if !strings.Contains(audit, want) {
			t.Errorf("audit missing %q: %q", want, audit)
		}
	}
	logs := buf.String()
	for _, v := range cpPIIValues {
		if strings.Contains(audit, v) {
			t.Errorf("audit leaked %q: %q", v, audit)
		}
		if strings.Contains(logs, v) {
			t.Errorf("log leaked %q: %q", v, logs)
		}
	}
}

// holderFail 与 poolFail 映射相同，但内部错误只记方法与路由，不记错误内容。
func TestHolderFailKeepsErrorTextOutOfLogs(t *testing.T) {
	buf := cpCaptureLog(t)
	gin.SetMode(gin.TestMode)
	run := func(err error) *httptest.ResponseRecorder {
		r := gin.New()
		r.PUT("/h/:id", func(c *gin.Context) { holderFail(c, err) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("PUT", "/h/h1", nil))
		return w
	}
	w := run(errors.New("db write failed: id_no " + cpPIIIDNo))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), cpPIIIDNo) {
		t.Fatalf("internal %d %s", w.Code, w.Body.String())
	}
	if logs := buf.String(); strings.Contains(logs, cpPIIIDNo) || !strings.Contains(logs, "cardpool api PUT /h/:id") {
		t.Errorf("log %q", logs)
	}
	// 平台已受理但本地写入失败：提示原样返回（文案不含资料值）
	w = run(fmt.Errorf("%w（持卡人 h1）", cardpool.ErrHolderAccepted))
	if w.Code != http.StatusInternalServerError || !strings.Contains(mwJSON(t, w)["error"].(string), "平台已受理，请勿重复提交") {
		t.Fatalf("accepted %d %s", w.Code, w.Body.String())
	}
	w = run(&marswallex.Error{Outcome: marswallex.OutcomeRejected, Reason: marswallex.ReasonStatus, HTTPStatus: 400, Code: "PARAM"})
	if w.Code != http.StatusBadGateway || mwJSON(t, w)["outcome"] != "rejected" {
		t.Errorf("platform %d %s", w.Code, w.Body.String())
	}
}
