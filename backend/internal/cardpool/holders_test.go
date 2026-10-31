package cardpool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex/mwtest"
)

const (
	pathCreateHolder = "/v1/cardholder"
	pathHolderStatus = "/v1/cardholder/status/h1"
	pathUpload       = "/v1/file/upload"
)

// 测试资料里的敏感值，不应出现在日志、本地库与告警中
const (
	piiIDNo   = "P99887766"
	piiSSN    = "123-45-6789"
	piiDOB    = "1990-05-17"
	piiMobile = "4085550123"
	piiEmail  = "john.doe@example.com"
	piiStreet = "1 Market Street"
)

var piiValues = []string{piiIDNo, piiSSN, piiDOB, piiMobile, piiEmail, piiStreet}

// validUSProfile 返回一份合法的 US 持卡人资料（测试时钟的北京日期为 2026-10-28）。
func validUSProfile() marswallex.CardholderProfile {
	addr := marswallex.Address{Country: "US", State: "CA", City: "San Jose", Address: piiStreet, PostCode: "95113"}
	return marswallex.CardholderProfile{
		FirstName: "John", LastName: "Smith",
		DateOfBirth: piiDOB, ZoneNumber: "1", Mobile: piiMobile, Email: piiEmail,
		CountryCode: "US", IDType: "PASSPORT", IDNo: piiIDNo, IDPeriodBegin: "2020-01-01", IDPeriodEnd: "2030-01-01",
		IDPhotoFileIDs: []string{"file-1"}, ResidentialAddress: addr, BillingAddress: addr, SSN: piiSSN,
	}
}

func holderDetailJSON(id, outID, status, remark string) string {
	return `{"card_holder_id":"` + id + `","out_card_holder_id":"` + outID + `","first_name":"John","last_name":"Smith",` +
		`"country_code":"US","status":"` + status + `","remark":"` + remark + `"}`
}

func insertSubmission(t *testing.T, outID, state string, createdAt int64) {
	t.Helper()
	_, err := db.DB.Exec(`INSERT INTO mw_cardholder_submissions (out_card_holder_id, first_name, last_name, country_code, state,
		created_at, updated_at) VALUES (?, 'John', 'Smith', 'US', ?, ?, ?)`, outID, state, createdAt, createdAt)
	if err != nil {
		t.Fatal(err)
	}
}

func mustGetSubmission(t *testing.T, outID string) *Submission {
	t.Helper()
	sub, err := getSubmission(context.Background(), outID)
	if err != nil {
		t.Fatalf("读取提交记录 %s: %v", outID, err)
	}
	return sub
}

func holderStatusEvent(t *testing.T, id, outID, status, remark string) int64 {
	t.Helper()
	return enqueueEvent(t, marswallex.TopicCardholderStatusChange, `{"card_holder_id":"`+id+`","out_card_holder_id":"`+outID+
		`","status":"`+status+`","remark":"`+remark+`","update_time":1793160000}`)
}

// requestOutID 取新建持卡人请求体里的 out_card_holder_id。
func requestOutID(t *testing.T, r mwtest.Request) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatalf("请求体不是 JSON: %v", err)
	}
	out, _ := body["out_card_holder_id"].(string)
	return out
}

func TestValidateProfile(t *testing.T) {
	today := time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC) // 北京 2026-10-28 12:00
	cases := []struct {
		name string
		bad  string // 违规值，不应出现在错误信息里；为空不检查
		mut  func(p *marswallex.CardholderProfile)
	}{
		{"first_name 非 ASCII", "Jöhn", func(p *marswallex.CardholderProfile) { p.FirstName = "Jöhn" }},
		{"first_name 为空", "", func(p *marswallex.CardholderProfile) { p.FirstName = "  " }},
		{"first_name 超长", strings.Repeat("A", 33), func(p *marswallex.CardholderProfile) { p.FirstName = strings.Repeat("A", 33) }},
		{"last_name 控制字符", "Smi\tth", func(p *marswallex.CardholderProfile) { p.LastName = "Smi\tth" }},
		{"last_name 为空", "", func(p *marswallex.CardholderProfile) { p.LastName = "" }},
		{"chinese_first_name 超长", strings.Repeat("张", 33), func(p *marswallex.CardholderProfile) { p.ChineseFirstName = strings.Repeat("张", 33) }},
		{"chinese_last_name 超长", strings.Repeat("李", 33), func(p *marswallex.CardholderProfile) { p.ChineseLastName = strings.Repeat("李", 33) }},
		{"date_of_birth 格式", "1990/05/17", func(p *marswallex.CardholderProfile) { p.DateOfBirth = "1990/05/17" }},
		{"date_of_birth 无效日期", "1990-02-30", func(p *marswallex.CardholderProfile) { p.DateOfBirth = "1990-02-30" }},
		{"date_of_birth 不早于今天", "2026-10-28", func(p *marswallex.CardholderProfile) { p.DateOfBirth = "2026-10-28" }},
		{"zone_number 非数字", "86a", func(p *marswallex.CardholderProfile) { p.ZoneNumber = "86a" }},
		{"zone_number 超长", "12345678901", func(p *marswallex.CardholderProfile) { p.ZoneNumber = "12345678901" }},
		{"zone_number 为空", "", func(p *marswallex.CardholderProfile) { p.ZoneNumber = "" }},
		{"mobile 为空", "", func(p *marswallex.CardholderProfile) { p.Mobile = "" }},
		{"mobile 超长", strings.Repeat("9", 51), func(p *marswallex.CardholderProfile) { p.Mobile = strings.Repeat("9", 51) }},
		{"email 格式", "john.example.com", func(p *marswallex.CardholderProfile) { p.Email = "john.example.com" }},
		{"email 超长", strings.Repeat("a", 89) + "@example.com", func(p *marswallex.CardholderProfile) { p.Email = strings.Repeat("a", 89) + "@example.com" }},
		{"country_code 三位", "USA", func(p *marswallex.CardholderProfile) { p.CountryCode = "USA" }},
		{"country_code 含数字", "U1", func(p *marswallex.CardholderProfile) { p.CountryCode = "U1" }},
		{"residential country", "ZZZ", func(p *marswallex.CardholderProfile) { p.ResidentialAddress.Country = "ZZZ" }},
		{"billing country", "C1", func(p *marswallex.CardholderProfile) { p.BillingAddress.Country = "C1" }},
		{"id_type", "DRIVER", func(p *marswallex.CardholderProfile) { p.IDType = "DRIVER" }},
		{"id_no 为空", "", func(p *marswallex.CardholderProfile) { p.IDNo = " " }},
		{"id_no 超长", strings.Repeat("X", 65), func(p *marswallex.CardholderProfile) { p.IDNo = strings.Repeat("X", 65) }},
		{"id_period_begin 晚于今天", "2026-10-29", func(p *marswallex.CardholderProfile) { p.IDPeriodBegin = "2026-10-29" }},
		{"id_period_begin 格式", "2020-1-1", func(p *marswallex.CardholderProfile) { p.IDPeriodBegin = "2020-1-1" }},
		{"id_period_begin 晚于截止日", "2026-01-01", func(p *marswallex.CardholderProfile) {
			p.IDPeriodBegin, p.IDPeriodEnd = "2026-01-01", "2025-12-31"
		}},
		{"id_period_end 不晚于今天", "2026-10-28", func(p *marswallex.CardholderProfile) { p.IDPeriodEnd = "2026-10-28" }},
		{"id_period_end 无效日期", "9999-12-32", func(p *marswallex.CardholderProfile) { p.IDPeriodEnd = "9999-12-32" }},
		{"id_photo_file_ids 为空", "", func(p *marswallex.CardholderProfile) { p.IDPhotoFileIDs = nil }},
		{"id_photo_file_ids 超过 2 个", "", func(p *marswallex.CardholderProfile) { p.IDPhotoFileIDs = []string{"f1", "f2", "f3"} }},
		{"id_photo_file_ids 含空 ID", "", func(p *marswallex.CardholderProfile) { p.IDPhotoFileIDs = []string{"f1", " "} }},
		{"residential state 超长", strings.Repeat("S", 65), func(p *marswallex.CardholderProfile) { p.ResidentialAddress.State = strings.Repeat("S", 65) }},
		{"residential city 为空", "", func(p *marswallex.CardholderProfile) { p.ResidentialAddress.City = "" }},
		{"residential address 超长", strings.Repeat("R", 257), func(p *marswallex.CardholderProfile) { p.ResidentialAddress.Address = strings.Repeat("R", 257) }},
		{"residential post_code 超长", "12345678901", func(p *marswallex.CardholderProfile) { p.ResidentialAddress.PostCode = "12345678901" }},
		{"billing state 为空", "", func(p *marswallex.CardholderProfile) { p.BillingAddress.State = "" }},
		{"billing city 超长", strings.Repeat("C", 65), func(p *marswallex.CardholderProfile) { p.BillingAddress.City = strings.Repeat("C", 65) }},
		{"billing address 为空", "", func(p *marswallex.CardholderProfile) { p.BillingAddress.Address = "" }},
		{"billing post_code 为空", "", func(p *marswallex.CardholderProfile) { p.BillingAddress.PostCode = "" }},
		{"US SSN 格式", "123456789", func(p *marswallex.CardholderProfile) { p.SSN = "123456789" }},
		{"US SSN 缺失", "", func(p *marswallex.CardholderProfile) { p.SSN = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := validUSProfile()
			c.mut(&p)
			err := validateProfile(&p, today)
			if !errors.Is(err, ErrBadRequest) {
				t.Fatalf("want ErrBadRequest, got %v", err)
			}
			msg := err.Error()
			if c.bad != "" && strings.Contains(msg, strings.TrimSpace(c.bad)) {
				t.Fatalf("错误信息含字段值: %q", msg)
			}
			for _, v := range piiValues {
				if strings.Contains(msg, v) {
					t.Fatalf("错误信息含敏感资料: %q", msg)
				}
			}
		})
	}

	t.Run("合法 US 资料通过", func(t *testing.T) {
		p := validUSProfile()
		if err := validateProfile(&p, today); err != nil {
			t.Fatal(err)
		}
		if p.SSN != piiSSN {
			t.Fatalf("US 的 SSN 不应被清空: %q", p.SSN)
		}
	})
	t.Run("非 US 带 SSN 通过且清空", func(t *testing.T) {
		p := validUSProfile()
		p.CountryCode, p.ResidentialAddress.Country, p.BillingAddress.Country = "CN", "CN", "HK"
		p.ChineseFirstName, p.ChineseLastName = "约翰", "史密斯"
		if err := validateProfile(&p, today); err != nil {
			t.Fatal(err)
		}
		if p.SSN != "" {
			t.Fatalf("SSN 应被清空: %q", p.SSN)
		}
	})
	t.Run("规范化：国家码转大写、去空白", func(t *testing.T) {
		p := validUSProfile()
		p.CountryCode, p.ResidentialAddress.Country, p.BillingAddress.Country = " us ", "us", "Us"
		p.FirstName, p.IDNo, p.SSN = " John ", " "+piiIDNo+" ", " "+piiSSN
		p.IDPhotoFileIDs = []string{" file-1 ", "file-2"}
		if err := validateProfile(&p, today); err != nil {
			t.Fatal(err)
		}
		if p.CountryCode != "US" || p.ResidentialAddress.Country != "US" || p.BillingAddress.Country != "US" {
			t.Fatalf("国家码: %q %q %q", p.CountryCode, p.ResidentialAddress.Country, p.BillingAddress.Country)
		}
		if p.FirstName != "John" || p.IDNo != piiIDNo || p.SSN != piiSSN || !reflect.DeepEqual(p.IDPhotoFileIDs, []string{"file-1", "file-2"}) {
			t.Fatalf("未去空白: %q %q %q %q", p.FirstName, p.IDNo, p.SSN, p.IDPhotoFileIDs)
		}
	})
	t.Run("今天取北京日期", func(t *testing.T) {
		p := validUSProfile()
		p.IDPeriodBegin = "2026-10-29"
		// UTC 10-28 17:00 = 北京 10-29 01:00
		if err := validateProfile(&p, time.Date(2026, 10, 28, 17, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUploadIDPhotoSniffsContent(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	if _, err := s.UploadIDPhoto(ctx, "a.jpg", []byte("<html><body>not a photo</body></html>")); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("HTML 伪装成 jpg 应被拒: %v", err)
	}
	if _, err := s.UploadIDPhoto(ctx, "a.png", nil); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("空文件应被拒: %v", err)
	}
	pngHead := []byte("\x89PNG\r\n\x1a\n")
	big := make([]byte, maxPhotoBytes+1)
	copy(big, pngHead)
	if _, err := s.UploadIDPhoto(ctx, "big.png", big); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("超过 7MB 应被拒: %v", err)
	}
	if n := len(mw.Requests("POST", pathUpload)); n != 0 {
		t.Fatalf("被拒的文件不应上传，requests=%d", n)
	}

	mw.On("POST", pathUpload, mwtest.OK(`{"file_id":"F123"}`))
	png := append(append([]byte{}, pngHead...), make([]byte, 64)...)
	id, err := s.UploadIDPhoto(ctx, "../../tmp/id.png", png)
	if err != nil {
		t.Fatal(err)
	}
	if id != "F123" {
		t.Fatalf("file_id=%q", id)
	}
	reqs := mw.Requests("POST", pathUpload)
	if len(reqs) != 1 || reqs[0].RawQuery != "biz_type=CARDHOLDER_ID_PHOTO" {
		t.Fatalf("上传请求: %+v", reqs)
	}
	if body := string(reqs[0].Body); !strings.Contains(body, `filename="id.png"`) || strings.Contains(body, "../") {
		t.Fatalf("文件名应只取 base")
	}

	// 恰好 7MB 的 PDF、JPEG 可以上传
	pdf := make([]byte, maxPhotoBytes)
	copy(pdf, "%PDF-1.7\n")
	if _, err := s.UploadIDPhoto(ctx, "id.pdf", pdf); err != nil {
		t.Fatalf("7MB PDF: %v", err)
	}
	if _, err := s.UploadIDPhoto(ctx, "id.jpg", append([]byte("\xff\xd8\xff\xe0"), make([]byte, 64)...)); err != nil {
		t.Fatalf("JPEG: %v", err)
	}
	if n := len(mw.Requests("POST", pathUpload)); n != 3 {
		t.Fatalf("requests=%d", n)
	}
}

func TestCreateCardholderSuccess(t *testing.T) {
	ctx := context.Background()
	s, mw, clock, alerts := newTestService(t)
	savePolicy(t, func(p *Policy) { p.HolderIDs = []string{"h0"} })
	mw.On("POST", pathCreateHolder, mwtest.OK(`{"card_holder_id":"h1"}`))
	sub, err := s.CreateCardholder(ctx, validUSProfile(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	reqs := mw.Requests("POST", pathCreateHolder)
	if len(reqs) != 1 {
		t.Fatalf("requests=%d", len(reqs))
	}
	out := requestOutID(t, reqs[0])
	if !strings.HasPrefix(out, "CH") || len(out) != 22 {
		t.Fatalf("out_card_holder_id=%q", out)
	}
	var body marswallex.CreateCardholderRequest
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.IDNo != piiIDNo || body.SSN != piiSSN || body.FirstName != "John" || len(body.IDPhotoFileIDs) != 1 {
		t.Fatalf("请求体资料不完整: first_name=%q", body.FirstName)
	}
	if sub.OutCardHolderID != out || sub.State != SubmissionLinked || sub.CardHolderID != "h1" || sub.RequestedBy != "admin" ||
		sub.FirstName != "John" || sub.LastName != "Smith" || sub.CountryCode != "US" || sub.CreatedAt != clock.Now().Unix() {
		t.Fatalf("提交记录: %+v", sub)
	}
	h, err := getCardholder(ctx, "h1")
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != "PENDING_REVIEW" || h.OutCardHolderID != out || h.FirstName != "John" || h.LastName != "Smith" || h.CountryCode != "US" {
		t.Fatalf("持卡人: %+v", h)
	}
	pol, err := LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pol.HolderIDs, []string{"h0"}) {
		t.Fatalf("策略 HolderIDs 不应变化: %v", pol.HolderIDs)
	}
	if list, err := s.ListSubmissions(ctx); err != nil || len(list) != 0 {
		t.Fatalf("linked 不应出现在列表: %v %+v", err, list)
	}
	if alerts.Count() != 0 {
		t.Fatalf("alerts: %v", alerts.Texts())
	}
}

// 平台已返回 200 但本地写入失败：错误提示「平台已受理，请勿重复提交」，不带资料值；提交记录保留为 submitted。
func TestCreateCardholderLocalWriteFailure(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	if _, err := db.DB.Exec(`CREATE TRIGGER fail_holder BEFORE INSERT ON mw_cardholders BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	mw.On("POST", pathCreateHolder, mwtest.OK(`{"card_holder_id":"h1"}`))
	sub, err := s.CreateCardholder(ctx, validUSProfile(), "admin")
	if err == nil || sub != nil {
		t.Fatalf("本地写入失败应返回错误: %+v %v", sub, err)
	}
	if !errors.Is(err, ErrHolderAccepted) || !strings.Contains(err.Error(), "平台已受理，请勿重复提交；稍后刷新或在提交记录中查看") {
		t.Fatalf("错误信息 = %q", err.Error())
	}
	for _, v := range piiValues {
		if strings.Contains(err.Error(), v) {
			t.Fatalf("错误信息含资料值 %q: %q", v, err.Error())
		}
	}
	reqs := mw.Requests("POST", pathCreateHolder)
	if len(reqs) != 1 {
		t.Fatalf("requests=%d", len(reqs))
	}
	if got := mustGetSubmission(t, requestOutID(t, reqs[0])); got.State != SubmissionSubmitted || got.CardHolderID != "" {
		t.Fatalf("提交记录: %+v", got)
	}
}

func TestCreateCardholderRejected(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)

	// 校验失败：不发请求、不写提交记录
	bad := validUSProfile()
	bad.SSN = ""
	if _, err := s.CreateCardholder(ctx, bad, "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("want ErrBadRequest, got %v", err)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_cardholder_submissions`); n != 0 {
		t.Fatalf("校验失败不应写提交记录: %d", n)
	}

	mw.On("POST", pathCreateHolder, mwtest.Rejected(400, "PARAM"))
	sub, err := s.CreateCardholder(ctx, validUSProfile(), "admin")
	if err == nil || marswallex.OutcomeOf(err) != marswallex.OutcomeRejected {
		t.Fatalf("want rejected error, got %v", err)
	}
	if sub != nil {
		t.Fatalf("被拒时不应返回提交记录: %+v", sub)
	}
	reqs := mw.Requests("POST", pathCreateHolder)
	if len(reqs) != 1 {
		t.Fatalf("requests=%d", len(reqs))
	}
	got := mustGetSubmission(t, requestOutID(t, reqs[0]))
	if got.State != SubmissionAbandoned || got.LastError == "" || len([]rune(got.LastError)) > 201 || got.CardHolderID != "" {
		t.Fatalf("提交记录: %+v", got)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_cardholders`); n != 0 {
		t.Fatalf("被拒不应写持卡人: %d", n)
	}

	// 未发出同样标 abandoned
	ns := New(Options{Client: func() (*marswallex.Client, error) { return mw.NotSentClient(), nil }, Now: s.opts.Now, Alert: s.opts.Alert})
	if _, err := ns.CreateCardholder(ctx, validUSProfile(), "admin"); marswallex.OutcomeOf(err) != marswallex.OutcomeNotSent {
		t.Fatalf("want not sent, got %v", err)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_cardholder_submissions WHERE state = ? AND last_error <> ''`, SubmissionAbandoned); n != 2 {
		t.Fatalf("abandoned=%d", n)
	}
}

func TestCreateCardholderUnknownThenWebhookLinks(t *testing.T) {
	ctx := context.Background()
	s, mw, _, alerts := newTestService(t)
	mw.On("POST", pathCreateHolder, mwtest.Unknown())
	sub, err := s.CreateCardholder(ctx, validUSProfile(), "admin")
	if err != nil {
		t.Fatalf("结果未知不应返回错误: %v", err)
	}
	if sub.State != SubmissionUnknown || sub.CardHolderID != "" || sub.LastError == "" {
		t.Fatalf("提交记录: %+v", sub)
	}
	out := sub.OutCardHolderID
	if out != requestOutID(t, mw.Requests("POST", pathCreateHolder)[0]) {
		t.Fatalf("提交记录与请求的 out ID 不一致")
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_cardholders`); n != 0 {
		t.Fatalf("未知不应写持卡人: %d", n)
	}
	if list, err := s.ListSubmissions(ctx); err != nil || len(list) != 1 || list[0].OutCardHolderID != out {
		t.Fatalf("列表: %v %+v", err, list)
	}

	mw.On("GET", "/v1/cardholder/h7", mwtest.OK(holderDetailJSON("h7", out, "PENDING_REVIEW", "")))
	id := holderStatusEvent(t, "h7", out, "PENDING_REVIEW", "")
	mustProcessEvents(t, s)
	mustBeProcessed(t, id, "已关联提交记录")
	if n := len(mw.Requests("GET", "/v1/cardholder/h7")); n != 1 {
		t.Fatalf("GET requests=%d", n)
	}
	h, err := getCardholder(ctx, "h7")
	if err != nil {
		t.Fatal(err)
	}
	if h.OutCardHolderID != out || h.Status != "PENDING_REVIEW" || h.FirstName != "John" {
		t.Fatalf("持卡人: %+v", h)
	}
	if got := mustGetSubmission(t, out); got.State != SubmissionLinked || got.CardHolderID != "h7" {
		t.Fatalf("提交记录: %+v", got)
	}
	if list, err := s.ListSubmissions(ctx); err != nil || len(list) != 0 {
		t.Fatalf("列表: %v %+v", err, list)
	}
	if alerts.Count() != 0 {
		t.Fatalf("首次审核中不应告警: %v", alerts.Texts())
	}
}

func TestSubmittedRecordLinksByWebhook(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	out := newOutID("CH", testNow())
	insertSubmission(t, out, SubmissionSubmitted, testNow().Unix()) // 发送途中崩溃留下的记录
	// 平台详情没带 out_card_holder_id 时以推送为准
	mw.On("GET", "/v1/cardholder/h8", mwtest.OK(holderDetailJSON("h8", "", "PENDING_REVIEW", "")))
	id := holderStatusEvent(t, "h8", out, "PENDING_REVIEW", "")
	mustProcessEvents(t, s)
	mustBeProcessed(t, id, "已关联提交记录")
	if got := mustGetSubmission(t, out); got.State != SubmissionLinked || got.CardHolderID != "h8" {
		t.Fatalf("提交记录: %+v", got)
	}
	h, err := getCardholder(ctx, "h8")
	if err != nil {
		t.Fatal(err)
	}
	if h.OutCardHolderID != out {
		t.Fatalf("持卡人: %+v", h)
	}

	// 不匹配任何 submitted / unknown 记录的推送：保持「持卡人未导入」，不调平台
	abandoned := newOutID("CH", testNow())
	insertSubmission(t, abandoned, SubmissionAbandoned, testNow().Unix())
	e1 := holderStatusEvent(t, "h9", abandoned, "APPROVED", "")
	e2 := holderStatusEvent(t, "h10", "CHNOPE", "APPROVED", "")
	e3 := holderStatusEvent(t, "h11", "", "APPROVED", "")
	mustProcessEvents(t, s)
	for _, e := range []int64{e1, e2, e3} {
		mustBeProcessed(t, e, "持卡人未导入")
	}
	if got := mustGetSubmission(t, abandoned); got.State != SubmissionAbandoned {
		t.Fatalf("abandoned 记录不应被关联: %+v", got)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_cardholders`); n != 1 {
		t.Fatalf("holders=%d", n)
	}
}

func TestImportLinksSubmission(t *testing.T) {
	ctx := context.Background()
	s, mw, _, _ := newTestService(t)
	out := newOutID("CH", testNow())
	insertSubmission(t, out, SubmissionUnknown, testNow().Unix())
	mw.On("GET", pathHolder, mwtest.OK(holderDetailJSON("h1", out, "APPROVED", "")))
	h, err := s.ImportCardholder(ctx, "h1", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if h.OutCardHolderID != out || h.Status != "APPROVED" {
		t.Fatalf("持卡人: %+v", h)
	}
	if got := mustGetSubmission(t, out); got.State != SubmissionLinked || got.CardHolderID != "h1" {
		t.Fatalf("提交记录: %+v", got)
	}

	// 已放弃的记录不关联
	ab := newOutID("CH", testNow())
	insertSubmission(t, ab, SubmissionAbandoned, testNow().Unix())
	mw.On("GET", "/v1/cardholder/h2", mwtest.OK(holderDetailJSON("h2", ab, "APPROVED", "")))
	h2, err := s.ImportCardholder(ctx, "h2", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if h2.OutCardHolderID != ab {
		t.Fatalf("持卡人: %+v", h2)
	}
	if got := mustGetSubmission(t, ab); got.State != SubmissionAbandoned || got.CardHolderID != "" {
		t.Fatalf("提交记录: %+v", got)
	}

	// 平台返回的 card_holder_id 与请求不一致：不信任其 out ID
	other := newOutID("CH", testNow())
	insertSubmission(t, other, SubmissionUnknown, testNow().Unix())
	mw.On("GET", "/v1/cardholder/h3", mwtest.OK(holderDetailJSON("hX", other, "APPROVED", "")))
	h3, err := s.ImportCardholder(ctx, "h3", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if h3.OutCardHolderID != "" {
		t.Fatalf("持卡人: %+v", h3)
	}
	if got := mustGetSubmission(t, other); got.State != SubmissionUnknown {
		t.Fatalf("提交记录: %+v", got)
	}

	// 再次导入时平台没带 out ID：不清掉已有的
	mw.On("GET", pathHolder, mwtest.OK(holderDetailJSON("h1", "", "APPROVED", "")))
	if h, err = s.RefreshCardholder(ctx, "h1"); err != nil || h.OutCardHolderID != out {
		t.Fatalf("%v %+v", err, h)
	}
}

func TestAbandonSubmission(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := newTestService(t)
	now := testNow().Unix()
	insertSubmission(t, "CHUNKNOWN", SubmissionUnknown, now-30)
	insertSubmission(t, "CHSUBMITTED", SubmissionSubmitted, now-20)
	insertSubmission(t, "CHLINKED", SubmissionLinked, now-10)
	insertSubmission(t, "CHOLD", SubmissionAbandoned, now-40)

	if err := s.AbandonSubmission(ctx, "CHUNKNOWN", "admin"); err != nil {
		t.Fatal(err)
	}
	if got := mustGetSubmission(t, "CHUNKNOWN"); got.State != SubmissionAbandoned {
		t.Fatalf("%+v", got)
	}
	if err := s.AbandonSubmission(ctx, "CHSUBMITTED", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.AbandonSubmission(ctx, "CHLINKED", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("linked 应拒绝: %v", err)
	}
	if got := mustGetSubmission(t, "CHLINKED"); got.State != SubmissionLinked {
		t.Fatalf("%+v", got)
	}
	if err := s.AbandonSubmission(ctx, "CHNOPE", "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应 ErrNotFound: %v", err)
	}

	list, err := s.ListSubmissions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range list {
		ids = append(ids, x.OutCardHolderID)
	}
	if !reflect.DeepEqual(ids, []string{"CHSUBMITTED", "CHUNKNOWN", "CHOLD"}) {
		t.Fatalf("列表应不含 linked、按创建时间倒序: %v", ids)
	}
}

func TestUpdateCardholder(t *testing.T) {
	ctx := context.Background()
	s, mw, _, alerts := newTestService(t)
	if _, err := s.UpdateCardholder(ctx, "h1", validUSProfile(), "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未导入应 ErrNotFound: %v", err)
	}
	if _, err := s.CardholderProfile(ctx, "h1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未导入应 ErrNotFound: %v", err)
	}
	insertHolder(t, "h1", "APPROVED")

	// 修改前实时读取完整资料
	mw.On("GET", pathHolder,
		mwtest.OK(`{"card_holder_id":"h1","first_name":"John","last_name":"Smith","country_code":"US","id_no":"`+piiIDNo+
			`","id_photo_file_ids":["file-1"],"status":"APPROVED"}`),
		mwtest.OK(holderDetailJSON("h1", "", "PENDING_REVIEW", "")))
	d, err := s.CardholderProfile(ctx, "h1")
	if err != nil {
		t.Fatal(err)
	}
	if d.IDNo != piiIDNo || len(d.IDPhotoFileIDs) != 1 {
		t.Fatalf("资料: id_photo_file_ids=%v", d.IDPhotoFileIDs)
	}

	bad := validUSProfile()
	bad.IDType = "DRIVER"
	if _, err := s.UpdateCardholder(ctx, "h1", bad, "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("want ErrBadRequest, got %v", err)
	}
	if n := len(mw.Requests("PUT", pathHolder)); n != 0 {
		t.Fatalf("校验失败不应提交: %d", n)
	}

	mw.On("PUT", pathHolder, mwtest.OK(`{"card_holder_id":"h1"}`))
	p := validUSProfile()
	p.CountryCode = "us"
	h, err := s.UpdateCardholder(ctx, "h1", p, "admin")
	if err != nil {
		t.Fatal(err)
	}
	reqs := mw.Requests("PUT", pathHolder)
	if len(reqs) != 1 {
		t.Fatalf("PUT requests=%d", len(reqs))
	}
	var body map[string]any
	if err := json.Unmarshal(reqs[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"first_name", "last_name", "date_of_birth", "zone_number", "mobile", "email", "country_code",
		"id_type", "id_no", "id_period_begin", "id_period_end", "id_photo_file_ids", "residential_address", "billing_address", "ssn"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("PUT 体缺少 %s", k)
		}
	}
	if _, ok := body["out_card_holder_id"]; ok {
		t.Fatal("PUT 体不应含 out_card_holder_id")
	}
	if body["country_code"] != "US" {
		t.Fatalf("country_code=%v", body["country_code"])
	}
	if n := len(mw.Requests("GET", pathHolder)); n != 2 {
		t.Fatalf("修改后应 GET 刷新，GET requests=%d", n)
	}
	if h.Status != "PENDING_REVIEW" || h.FirstName != "John" {
		t.Fatalf("持卡人: %+v", h)
	}
	// §7.5：修改后刷新得到 APPROVED → PENDING_REVIEW，告警一次
	if alerts.Count() != 1 || alertsContaining(alerts, "h1") != 1 || alertsContaining(alerts, "PENDING_REVIEW") != 1 {
		t.Fatalf("修改后重新审核应告警一次: %v", alerts.Texts())
	}
	// 随后到达的同状态推送不再告警
	e := holderStatusEvent(t, "h1", "", "PENDING_REVIEW", "")
	mustProcessEvents(t, s)
	mustBeProcessed(t, e, "")
	if alerts.Count() != 1 {
		t.Fatalf("推送不应重复告警: %v", alerts.Texts())
	}
}

// 刷新与导入按 §7.5 告警：已在本地且状态有变化才告警，首次导入不告警。
func TestRefreshCardholderAlerts(t *testing.T) {
	ctx := context.Background()
	s, mw, _, alerts := newTestService(t)
	mw.On("GET", pathHolder, mwtest.OK(holderDetailJSON("h1", "", "APPROVED", "")))
	if _, err := s.ImportCardholder(ctx, "h1", "admin"); err != nil {
		t.Fatal(err)
	}
	if alerts.Count() != 0 {
		t.Fatalf("首次导入不应告警: %v", alerts.Texts())
	}
	// 状态没变：不告警
	if _, err := s.RefreshCardholder(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if alerts.Count() != 0 {
		t.Fatalf("状态未变不应告警: %v", alerts.Texts())
	}

	insertHolder(t, "h2", "PENDING_REVIEW")
	mw.On("GET", "/v1/cardholder/h2", mwtest.OK(holderDetailJSON("h2", "", "APPROVED", "")))
	h, err := s.RefreshCardholder(ctx, "h2")
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != "APPROVED" {
		t.Fatalf("持卡人: %+v", h)
	}
	if got := alerts.Texts(); len(got) != 1 || !strings.Contains(got[0], "MarsWallex 持卡人 h2 已通过审核") {
		t.Fatalf("手动刷新应发通过通知一次: %v", got)
	}
	// 随后到达的同状态推送不再通知
	e := holderStatusEvent(t, "h2", "", "APPROVED", "")
	mustProcessEvents(t, s)
	mustBeProcessed(t, e, "")
	if alerts.Count() != 1 {
		t.Fatalf("推送不应重复通知: %v", alerts.Texts())
	}

	// 重复导入时被驳回：附原因告警
	mw.On("GET", "/v1/cardholder/h2", mwtest.OK(holderDetailJSON("h2", "", "REJECTED", "照片模糊")))
	if _, err := s.ImportCardholder(ctx, "h2", "admin"); err != nil {
		t.Fatal(err)
	}
	if got := alerts.Texts(); len(got) != 2 || !strings.Contains(got[1], "h2") || !strings.Contains(got[1], "照片模糊") {
		t.Fatalf("驳回应告警: %v", got)
	}
}

func TestSetCardholderStatus(t *testing.T) {
	ctx := context.Background()
	s, mw, _, alerts := newTestService(t)
	insertHolder(t, "h1", "APPROVED")
	if _, err := s.SetCardholderStatus(ctx, "h1", "REJECTED", "admin"); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("want ErrBadRequest, got %v", err)
	}
	if _, err := s.SetCardholderStatus(ctx, "h9", "DISABLED", "admin"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未导入应 ErrNotFound: %v", err)
	}
	if n := len(mw.Requests("PUT", pathHolderStatus)); n != 0 {
		t.Fatalf("requests=%d", n)
	}

	mw.On("PUT", pathHolderStatus, mwtest.OK(`{"card_holder_id":"h1","status":"DISABLED"}`))
	mw.On("GET", pathHolder, mwtest.OK(holderDetailJSON("h1", "", "DISABLED", "")))
	h, err := s.SetCardholderStatus(ctx, "h1", "DISABLED", "admin")
	if err != nil {
		t.Fatal(err)
	}
	reqs := mw.Requests("PUT", pathHolderStatus)
	if len(reqs) != 1 || string(reqs[0].Body) != `{"status":"DISABLED"}` {
		t.Fatalf("PUT: %+v", reqs)
	}
	if h.Status != "DISABLED" || len(mw.Requests("GET", pathHolder)) != 1 {
		t.Fatalf("持卡人: %+v", h)
	}
	if got := alerts.Texts(); len(got) != 1 || !strings.Contains(got[0], "h1") || !strings.Contains(got[0], "DISABLED") {
		t.Fatalf("禁用后应按 §7.5 告警一次: %v", got)
	}

	// 接口成功但刷新失败：本地仍写入目标状态，同样按 §7.5 通知
	mw.On("PUT", pathHolderStatus, mwtest.OK(`{"card_holder_id":"h1","status":"APPROVED"}`))
	mw.On("GET", pathHolder, mwtest.Unknown())
	h, err = s.SetCardholderStatus(ctx, "h1", "approved", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != "APPROVED" {
		t.Fatalf("持卡人: %+v", h)
	}
	if got := alerts.Texts(); len(got) != 2 || !strings.Contains(got[1], "MarsWallex 持卡人 h1 已通过审核") {
		t.Fatalf("启用后应发通过通知: %v", got)
	}
}

func TestHolderStatusAlerts(t *testing.T) {
	ctx := context.Background()
	s, mw, _, alerts := newTestService(t)
	cases := []struct {
		name, id, old, status, remark string
		want                          []string // 期望的一条告警里应含的片段；nil 表示不告警
	}{
		{"首次审核中", "hp", "PENDING_REVIEW", "PENDING_REVIEW", "", nil},
		{"通过后重新审核", "ha", "APPROVED", "PENDING_REVIEW", "", []string{"ha", "PENDING_REVIEW"}},
		{"未通过 → 通过", "hn", "PENDING_REVIEW", "APPROVED", "", []string{"MarsWallex 持卡人 hn 已通过审核，可在策略中加入补卡持卡人"}},
		{"已通过重复推送", "hk", "APPROVED", "APPROVED", "", nil},
		{"驳回", "hr", "PENDING_REVIEW", "REJECTED", "证件照模糊", []string{"hr", "REJECTED", "证件照模糊"}},
		{"暂停", "hs", "APPROVED", "SUSPENDED", "", []string{"hs", "SUSPENDED"}},
		{"禁用", "hd", "APPROVED", "DISABLED", "", []string{"hd", "DISABLED"}},
		{"状态未变的重复推送", "hz", "SUSPENDED", "SUSPENDED", "", nil},
	}
	for _, c := range cases {
		insertHolder(t, c.id, c.old)
		before := alerts.Count()
		e := holderStatusEvent(t, c.id, "", c.status, c.remark)
		mustProcessEvents(t, s)
		mustBeProcessed(t, e, "")
		got := alerts.Texts()[before:]
		if c.want == nil {
			if len(got) != 0 {
				t.Fatalf("%s: 不应告警: %v", c.name, got)
			}
		} else {
			if len(got) != 1 {
				t.Fatalf("%s: 应告警一次: %v", c.name, got)
			}
			for _, w := range c.want {
				if !strings.Contains(got[0], w) {
					t.Fatalf("%s: 告警 %q 应含 %q", c.name, got[0], w)
				}
			}
		}
		if h, err := getCardholder(ctx, c.id); err != nil || h.Status != c.status {
			t.Fatalf("%s: %v %+v", c.name, err, h)
		}
	}

	// 通过通知（holderok:）与状态告警（holder:）各自按天去重，互不影响
	insertHolder(t, "hy", "PENDING_REVIEW")
	before := alerts.Count()
	for _, st := range []string{"APPROVED", "SUSPENDED", "APPROVED"} {
		holderStatusEvent(t, "hy", "", st, "")
		mustProcessEvents(t, s)
	}
	if got := alerts.Texts()[before:]; len(got) != 2 || !strings.Contains(got[0], "已通过审核") || !strings.Contains(got[1], "SUSPENDED") {
		t.Fatalf("hy: %v", got)
	}

	// 驳回告警（holderrej:）单独去重：同一天先有过状态告警（holder:），驳回原因照样发出
	insertHolder(t, "hj", "APPROVED")
	before = alerts.Count()
	holderStatusEvent(t, "hj", "", "PENDING_REVIEW", "")
	mustProcessEvents(t, s)
	holderStatusEvent(t, "hj", "", "REJECTED", "地址证明不清晰")
	mustProcessEvents(t, s)
	if got := alerts.Texts()[before:]; len(got) != 2 || !strings.Contains(got[0], "PENDING_REVIEW") ||
		!strings.Contains(got[1], "REJECTED") || !strings.Contains(got[1], "地址证明不清晰") {
		t.Fatalf("hj: %v", got)
	}

	// 推送关联提交记录时同样按表告警：首次推送即 APPROVED → 通知
	out := newOutID("CH", testNow())
	insertSubmission(t, out, SubmissionUnknown, testNow().Unix())
	mw.On("GET", "/v1/cardholder/hx", mwtest.OK(holderDetailJSON("hx", out, "APPROVED", "")))
	before = alerts.Count()
	e := holderStatusEvent(t, "hx", out, "APPROVED", "")
	mustProcessEvents(t, s)
	mustBeProcessed(t, e, "已关联提交记录")
	if got := alerts.Texts()[before:]; len(got) != 1 || !strings.Contains(got[0], "MarsWallex 持卡人 hx 已通过审核") {
		t.Fatalf("hx: %v", got)
	}
}

func TestHolderFlowsKeepPIIOutOfLogs(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	ctx := context.Background()
	s, mw, _, alerts := newTestService(t)
	// 平台错误信息回显了证件号：也不能进日志或本地库
	echo := mwtest.Reply{Status: 400, Body: `{"code":"PARAM","message":"id_no ` + piiIDNo + ` invalid, mobile ` + piiMobile + `"}`}
	mw.On("POST", pathCreateHolder, mwtest.OK(`{"card_holder_id":"h1"}`), echo, mwtest.Unknown())
	if _, err := s.CreateCardholder(ctx, validUSProfile(), "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCardholder(ctx, validUSProfile(), "admin"); marswallex.OutcomeOf(err) != marswallex.OutcomeRejected {
		t.Fatalf("want rejected, got %v", err)
	}
	sub, err := s.CreateCardholder(ctx, validUSProfile(), "admin")
	if err != nil || sub.State != SubmissionUnknown {
		t.Fatalf("%v %+v", err, sub)
	}
	// 修改：平台详情含完整资料
	mw.On("PUT", pathHolder, mwtest.OK(`{"card_holder_id":"h1"}`))
	full, _ := json.Marshal(marswallex.CardholderDetail{CardHolderID: "h1", CardholderProfile: validUSProfile(), Status: "PENDING_REVIEW"})
	mw.On("GET", pathHolder, mwtest.OK(string(full)))
	if _, err := s.UpdateCardholder(ctx, "h1", validUSProfile(), "admin"); err != nil {
		t.Fatal(err)
	}
	// 修改被拒（平台回显资料）
	mw.On("PUT", pathHolder, echo)
	if _, err := s.UpdateCardholder(ctx, "h1", validUSProfile(), "admin"); err == nil {
		t.Fatal("want error")
	}
	// 推送关联未知记录，导入时详情含完整资料
	mw.On("GET", "/v1/cardholder/h2", mwtest.OK(strings.Replace(string(full), `"h1"`, `"h2"`, 1)))
	holderStatusEvent(t, "h2", sub.OutCardHolderID, "PENDING_REVIEW", "")
	mustProcessEvents(t, s)

	logs := buf.String()
	if !strings.Contains(logs, "持卡人") {
		t.Fatalf("应有持卡人操作日志: %q", logs)
	}
	var stored strings.Builder
	for _, q := range []string{
		`SELECT card_holder_id || first_name || last_name || country_code || status || remark || note || out_card_holder_id FROM mw_cardholders`,
		`SELECT out_card_holder_id || first_name || last_name || country_code || state || card_holder_id || last_error || requested_by FROM mw_cardholder_submissions`,
	} {
		rows, err := db.DB.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			stored.WriteString(v + "\n")
		}
		rows.Close()
	}
	if n := countRows(t, `SELECT COUNT(*) FROM mw_cardholder_submissions WHERE state = ? AND last_error <> ''`, SubmissionAbandoned); n != 1 {
		t.Fatalf("被拒的提交记录应记下错误: %d", n)
	}
	for _, v := range piiValues {
		if strings.Contains(logs, v) {
			t.Fatalf("日志含敏感资料 %q", v)
		}
		if strings.Contains(stored.String(), v) {
			t.Fatalf("本地库含敏感资料 %q", v)
		}
		for _, a := range alerts.Texts() {
			if strings.Contains(a, v) {
				t.Fatalf("告警含敏感资料 %q", v)
			}
		}
	}
}
