package cardpool

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/marswallex"
)

// 持卡人资料（证件号、证件照、出生日期、SSN、手机、邮箱、地址）只经内存转发给平台：
// 不写本地库、不写日志、不进告警；本文件的日志只记操作人、持卡人 ID 与商户侧 ID。

const (
	maxPhotoBytes     = 7 << 20 // 证件照单张上限
	idPhotoBizType    = "CARDHOLDER_ID_PHOTO"
	holderOutIDPrefix = "CH"
	maxHolderErrLen   = 200 // 提交记录 last_error 截断长度
	dateLayout        = "2006-01-02"

	holderApproved      = "APPROVED"
	holderDisabled      = "DISABLED"
	holderPendingReview = "PENDING_REVIEW"
	holderRejected      = "REJECTED"
)

var (
	zoneNumberRe  = regexp.MustCompile(`^[0-9]{1,10}$`)
	countryCodeRe = regexp.MustCompile(`^[A-Z]{2}$`)
	ssnRe         = regexp.MustCompile(`^\d{3}-\d{2}-\d{4}$`)
)

// UploadIDPhoto 把证件照转发到平台并返回 file_id。按内容嗅探只收 JPEG / PNG / PDF，文件名只取 base。
func (s *Service) UploadIDPhoto(ctx context.Context, filename string, content []byte) (string, error) {
	if len(content) == 0 || len(content) > maxPhotoBytes {
		return "", badRequest("证件照不能为空且不能超过 7MB")
	}
	switch http.DetectContentType(content) {
	case "image/jpeg", "image/png", "application/pdf":
	default:
		return "", badRequest("证件照只支持 JPEG、PNG 或 PDF")
	}
	name := filepath.Base(strings.TrimSpace(filename))
	if name == "." || name == string(filepath.Separator) {
		name = "id_photo"
	}
	c, err := s.opts.Client()
	if err != nil {
		return "", err
	}
	res, err := c.UploadFile(ctx, idPhotoBizType, name, content)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(res.FileID) == "" {
		return "", errors.New("平台未返回 file_id")
	}
	return res.FileID, nil
}

// ---------- 资料校验 ----------

func trimAddress(a *marswallex.Address) {
	a.Country = strings.ToUpper(strings.TrimSpace(a.Country))
	a.State = strings.TrimSpace(a.State)
	a.City = strings.TrimSpace(a.City)
	a.Address = strings.TrimSpace(a.Address)
	a.PostCode = strings.TrimSpace(a.PostCode)
}

// normalizeProfile 去首尾空白、国家码转大写、非 US 清空 SSN。证件照 ID 复制后再改，不动调用方的切片。
func normalizeProfile(p *marswallex.CardholderProfile) {
	for _, f := range []*string{&p.FirstName, &p.LastName, &p.ChineseFirstName, &p.ChineseLastName, &p.DateOfBirth,
		&p.ZoneNumber, &p.Mobile, &p.Email, &p.IDType, &p.IDNo, &p.IDPeriodBegin, &p.IDPeriodEnd, &p.SSN} {
		*f = strings.TrimSpace(*f)
	}
	p.CountryCode = strings.ToUpper(strings.TrimSpace(p.CountryCode))
	trimAddress(&p.ResidentialAddress)
	trimAddress(&p.BillingAddress)
	ids := make([]string, len(p.IDPhotoFileIDs))
	for i, id := range p.IDPhotoFileIDs {
		ids[i] = strings.TrimSpace(id)
	}
	p.IDPhotoFileIDs = ids
	if p.CountryCode != "US" {
		p.SSN = ""
	}
}

// asciiName 判断是否 1–32 个可打印 ASCII 字符。
func asciiName(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func runeLen(s string, lo, hi int) bool {
	n := utf8.RuneCountInString(s)
	return n >= lo && n <= hi
}

func validDate(s string) bool {
	if len(s) != len(dateLayout) {
		return false
	}
	_, err := time.Parse(dateLayout, s)
	return err == nil
}

func validEmail(s string) bool {
	if !runeLen(s, 1, 100) {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s // 只收裸地址，不收 "Name <a@b>"
}

func validPhotoIDs(ids []string) bool {
	if len(ids) < 1 || len(ids) > 2 {
		return false
	}
	for _, id := range ids {
		if id == "" {
			return false
		}
	}
	return true
}

type fieldCheck struct {
	ok    bool
	field string
	rule  string
}

func addressChecks(name string, a *marswallex.Address) []fieldCheck {
	return []fieldCheck{
		{countryCodeRe.MatchString(a.Country), name + ".country", "须为两位大写字母"},
		{runeLen(a.State, 1, 64), name + ".state", "须为 1–64 个字"},
		{runeLen(a.City, 1, 64), name + ".city", "须为 1–64 个字"},
		{runeLen(a.Address, 1, 256), name + ".address", "须为 1–256 个字"},
		{runeLen(a.PostCode, 1, 10), name + ".post_code", "须为 1–10 个字"},
	}
}

// validateProfile 规范化并校验持卡人资料，today 取其北京日期。错误信息只含字段名，不含字段值。
func validateProfile(p *marswallex.CardholderProfile, today time.Time) error {
	normalizeProfile(p)
	day := today.In(beijing).Format(dateLayout) // 同格式日期可直接按字符串比较
	checks := []fieldCheck{
		{asciiName(p.FirstName), "first_name", "须为 1–32 个可打印 ASCII 字符"},
		{asciiName(p.LastName), "last_name", "须为 1–32 个可打印 ASCII 字符"},
		{runeLen(p.ChineseFirstName, 0, 32), "chinese_first_name", "不能超过 32 个字"},
		{runeLen(p.ChineseLastName, 0, 32), "chinese_last_name", "不能超过 32 个字"},
		{validDate(p.DateOfBirth) && p.DateOfBirth < day, "date_of_birth", "须为 YYYY-MM-DD 且早于今天"},
		{zoneNumberRe.MatchString(p.ZoneNumber), "zone_number", "须为 1–10 位数字"},
		{runeLen(p.Mobile, 1, 50), "mobile", "须为 1–50 个字"},
		{validEmail(p.Email), "email", "须为不超过 100 个字的邮箱地址"},
		{countryCodeRe.MatchString(p.CountryCode), "country_code", "须为两位大写字母"},
		{p.IDType == "PASSPORT" || p.IDType == "IDCARD" || p.IDType == "OTHER_CERTIFICATE", "id_type", "须为 PASSPORT、IDCARD 或 OTHER_CERTIFICATE"},
		{runeLen(p.IDNo, 1, 64), "id_no", "须为 1–64 个字"},
		{validDate(p.IDPeriodBegin) && p.IDPeriodBegin <= day, "id_period_begin", "须为 YYYY-MM-DD 且不晚于今天"},
		{validDate(p.IDPeriodEnd) && p.IDPeriodEnd > day, "id_period_end", "须为 YYYY-MM-DD 且晚于今天（长期有效填 9999-12-31）"},
		{p.IDPeriodBegin <= p.IDPeriodEnd, "id_period_begin", "不能晚于 id_period_end"},
		{validPhotoIDs(p.IDPhotoFileIDs), "id_photo_file_ids", "须为 1–2 个非空的证件照 ID"},
	}
	checks = append(checks, addressChecks("residential_address", &p.ResidentialAddress)...)
	checks = append(checks, addressChecks("billing_address", &p.BillingAddress)...)
	if p.CountryCode == "US" {
		checks = append(checks, fieldCheck{ssnRe.MatchString(p.SSN), "ssn", "国家为 US 时必填，格式为 ###-##-####"})
	}
	for _, c := range checks {
		if !c.ok {
			return badRequest("持卡人资料字段 " + c.field + " 不合法：" + c.rule)
		}
	}
	return nil
}

// scrubProfile 把文本里出现的敏感资料替换为 ***（平台错误信息可能回显字段值）；不足 4 个字的值不处理。
func scrubProfile(text string, p *marswallex.CardholderProfile) string {
	vals := []string{p.IDNo, p.SSN, p.DateOfBirth, p.Mobile, p.Email}
	for _, a := range []marswallex.Address{p.ResidentialAddress, p.BillingAddress} {
		vals = append(vals, a.Address, a.City, a.State, a.PostCode)
	}
	vals = append(vals, p.IDPhotoFileIDs...)
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) }) // 长的先换，免得被短值拆开
	for _, v := range vals {
		if utf8.RuneCountInString(v) >= 4 {
			text = strings.ReplaceAll(text, v, "***")
		}
	}
	return text
}

// safeErr 返回可写日志的错误摘要：平台错误只取结果分类与错误码，不取 message（可能回显资料）。
func safeErr(err error) string {
	var e *marswallex.Error
	if errors.As(err, &e) {
		return fmt.Sprintf("%s/%s HTTP %d code=%s", e.Outcome, e.Reason, e.HTTPStatus, e.Code)
	}
	return err.Error()
}

// ---------- 新建与提交记录 ----------

// CreateCardholder 新建持卡人：校验 → 写 submitted 提交记录（只含姓名、国家）→ 调平台。
// 成功写入本地持卡人（PENDING_REVIEW）并标 linked；明确被拒或未发出标 abandoned 并返回错误；
// 其他结果标 unknown、返回提交记录与 nil，由推送或导入关联。不改策略的补卡持卡人。
func (s *Service) CreateCardholder(ctx context.Context, p marswallex.CardholderProfile, by string) (*Submission, error) {
	now := s.now()
	if err := validateProfile(&p, now); err != nil {
		return nil, err
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	outID := newOutID(holderOutIDPrefix, now)
	if _, err := db.DB.ExecContext(ctx, `INSERT INTO mw_cardholder_submissions (out_card_holder_id, first_name, last_name,
		country_code, state, requested_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		outID, p.FirstName, p.LastName, p.CountryCode, SubmissionSubmitted, by, now.Unix(), now.Unix()); err != nil {
		return nil, err
	}
	log.Printf("[cardpool] %s 提交新建持卡人 %s", by, outID)

	ref, err := c.CreateCardholder(ctx, marswallex.CreateCardholderRequest{OutCardHolderID: outID, CardholderProfile: p})
	wctx := context.WithoutCancel(ctx) // 请求已发出，结果必须记下
	if err == nil {
		if id := strings.TrimSpace(ref.CardHolderID); id != "" {
			if err := s.saveCreatedHolder(wctx, outID, id, &p); err != nil {
				return nil, err
			}
			log.Printf("[cardpool] 新建持卡人 %s 成功：%s", outID, id)
			return getSubmission(wctx, outID)
		}
		err = errors.New("平台未返回 card_holder_id")
	}
	msg := truncate(scrubProfile(err.Error(), &p), maxHolderErrLen)
	switch marswallex.OutcomeOf(err) {
	case marswallex.OutcomeRejected, marswallex.OutcomeNotSent:
		if uerr := s.setSubmissionState(wctx, outID, SubmissionAbandoned, msg); uerr != nil {
			log.Printf("[cardpool] 更新提交记录 %s 失败: %v", outID, uerr)
		}
		log.Printf("[cardpool] 新建持卡人 %s 被拒或未发出（%s），提交记录已放弃", outID, safeErr(err))
		return nil, err
	}
	if err := s.setSubmissionState(wctx, outID, SubmissionUnknown, msg); err != nil {
		return nil, err
	}
	log.Printf("[cardpool] 新建持卡人 %s 结果未知（%s），等待推送或导入关联", outID, safeErr(err))
	return getSubmission(wctx, outID)
}

// saveCreatedHolder 写入新建成功的持卡人（仅姓名、国家、PENDING_REVIEW、商户侧 ID），提交记录标 linked。
func (s *Service) saveCreatedHolder(ctx context.Context, outID, id string, p *marswallex.CardholderProfile) error {
	now := s.now().Unix()
	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 推送可能先到并已导入：只补商户侧 ID，不覆盖平台状态
	if _, err := tx.ExecContext(ctx, `INSERT INTO mw_cardholders (card_holder_id, first_name, last_name, country_code, status,
		out_card_holder_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(card_holder_id) DO UPDATE SET out_card_holder_id = excluded.out_card_holder_id`,
		id, p.FirstName, p.LastName, p.CountryCode, holderPendingReview, outID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mw_cardholder_submissions SET state = ?, card_holder_id = ?, updated_at = ?
		WHERE out_card_holder_id = ?`, SubmissionLinked, id, now, outID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) setSubmissionState(ctx context.Context, outID, state, lastError string) error {
	_, err := db.DB.ExecContext(ctx, `UPDATE mw_cardholder_submissions SET state = ?, last_error = ?, updated_at = ?
		WHERE out_card_holder_id = ?`, state, lastError, s.now().Unix(), outID)
	return err
}

// linkSubmissionStmt 把 submitted / unknown 提交记录标为 linked。
const linkSubmissionStmt = `UPDATE mw_cardholder_submissions SET state = 'linked', card_holder_id = ?, updated_at = ?
	WHERE out_card_holder_id = ? AND state IN ('submitted', 'unknown')`

// ListSubmissions 列出未关联的提交记录（submitted / unknown / abandoned），按创建时间倒序。
func (s *Service) ListSubmissions(ctx context.Context) ([]Submission, error) {
	rows, err := db.DB.QueryContext(ctx, `SELECT `+submissionCols+` FROM mw_cardholder_submissions WHERE state <> ?
		ORDER BY created_at DESC, out_card_holder_id DESC`, SubmissionLinked)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Submission{}
	for rows.Next() {
		var sub Submission
		if err := sub.scan(rows); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

// AbandonSubmission 放弃 submitted / unknown 提交记录；已放弃的视为成功，已关联的返回 ErrBadRequest。
func (s *Service) AbandonSubmission(ctx context.Context, outID, by string) error {
	outID = strings.TrimSpace(outID)
	res, err := db.DB.ExecContext(ctx, `UPDATE mw_cardholder_submissions SET state = ?, updated_at = ?
		WHERE out_card_holder_id = ? AND state IN (?, ?)`,
		SubmissionAbandoned, s.now().Unix(), outID, SubmissionSubmitted, SubmissionUnknown)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		log.Printf("[cardpool] %s 放弃持卡人提交记录 %s", by, outID)
		return nil
	}
	sub, err := getSubmission(ctx, outID)
	if err != nil {
		return err
	}
	if sub.State == SubmissionAbandoned {
		return nil
	}
	return badRequest("提交记录已关联持卡人，不能放弃")
}

// ---------- 修改与启停 ----------

// CardholderProfile 实时读取已在本地的持卡人的完整资料（供修改时预填），不落地。
func (s *Service) CardholderProfile(ctx context.Context, id string) (*marswallex.CardholderDetail, error) {
	id = strings.TrimSpace(id)
	if _, err := getCardholder(ctx, id); err != nil {
		return nil, err
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	return c.GetCardholder(ctx, id)
}

// UpdateCardholder 全量修改已在本地的持卡人资料，成功后重新导入。
func (s *Service) UpdateCardholder(ctx context.Context, id string, p marswallex.CardholderProfile, by string) (*Cardholder, error) {
	id = strings.TrimSpace(id)
	if _, err := getCardholder(ctx, id); err != nil {
		return nil, err
	}
	if err := validateProfile(&p, s.now()); err != nil {
		return nil, err
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	if _, err := c.UpdateCardholder(ctx, id, p); err != nil {
		return nil, err
	}
	log.Printf("[cardpool] %s 修改持卡人 %s 资料", by, id)
	return s.refreshAfterChange(ctx, id, "")
}

// SetCardholderStatus 启用（APPROVED）或禁用（DISABLED）已在本地的持卡人，成功后重新导入。
func (s *Service) SetCardholderStatus(ctx context.Context, id, status, by string) (*Cardholder, error) {
	id, status = strings.TrimSpace(id), normStatus(status)
	if status != holderApproved && status != holderDisabled {
		return nil, badRequest("持卡人状态只能改为 APPROVED 或 DISABLED")
	}
	if _, err := getCardholder(ctx, id); err != nil {
		return nil, err
	}
	c, err := s.opts.Client()
	if err != nil {
		return nil, err
	}
	if _, err := c.UpdateCardholderStatus(ctx, id, status); err != nil {
		return nil, err
	}
	log.Printf("[cardpool] %s 将持卡人 %s 置为 %s", by, id, status)
	return s.refreshAfterChange(ctx, id, status)
}

// refreshAfterChange 在平台已接受变更后重新导入；失败只记日志，status 非空时直接写入本地，返回本地记录。
func (s *Service) refreshAfterChange(ctx context.Context, id, status string) (*Cardholder, error) {
	h, err := s.RefreshCardholder(ctx, id)
	if err == nil {
		return h, nil
	}
	log.Printf("[cardpool] 持卡人 %s 变更已提交，重新导入失败（%s），可稍后手动刷新", id, safeErr(err))
	wctx := context.WithoutCancel(ctx)
	if status != "" {
		if _, err := db.DB.ExecContext(wctx, `UPDATE mw_cardholders SET status = ? WHERE card_holder_id = ?`, status, id); err != nil {
			return nil, err
		}
	}
	return getCardholder(wctx, id)
}

// ---------- 状态推送 ----------

// linkHolderByEvent 处理本地没有的持卡人的状态推送：out_card_holder_id 匹配 submitted / unknown 提交记录时
// 导入并关联，按 §7.5 告警（之前无状态）；否则备注「持卡人未导入」。
func (s *Service) linkHolderByEvent(ctx context.Context, id, outID string) (string, error) {
	if outID == "" {
		return "持卡人未导入", nil
	}
	sub, err := getSubmission(ctx, outID)
	if errors.Is(err, ErrNotFound) {
		return "持卡人未导入", nil
	}
	if err != nil {
		return "", err
	}
	if sub.State != SubmissionSubmitted && sub.State != SubmissionUnknown {
		return "持卡人未导入", nil
	}
	h, err := s.importCardholder(ctx, id, outID)
	if err != nil {
		// 不带平台 message 进事件错误与日志
		return "", fmt.Errorf("导入持卡人 %s 失败: %s", id, safeErr(err))
	}
	s.holderStatusAlert(id, "", h.Status, h.Remark)
	return "已关联提交记录 " + outID + "，持卡人状态 " + h.Status, nil
}

// holderStatusAlert 按 spec §7.5 处理持卡人状态变化的告警。
func (s *Service) holderStatusAlert(id, old, status, remark string) {
	old, status, remark = normStatus(old), normStatus(status), strings.TrimSpace(remark)
	switch status {
	case holderApproved:
		if old != holderApproved {
			s.alert("holderok:"+id, fmt.Sprintf("MarsWallex 持卡人 %s 已通过审核，可在策略中加入补卡持卡人", id))
		}
	case holderPendingReview:
		if old == holderApproved {
			s.alert("holder:"+id, fmt.Sprintf("MarsWallex 持卡人 %s 由 APPROVED 变为 PENDING_REVIEW（修改后重新审核），自动补卡将跳过该持卡人", id))
		}
	case holderRejected:
		reason := remark
		if reason == "" {
			reason = "平台未提供"
		}
		s.alert("holder:"+id, fmt.Sprintf("MarsWallex 持卡人 %s 审核被驳回（REJECTED），自动补卡将跳过该持卡人。驳回原因：%s", id, truncate(reason, 200)))
	default:
		msg := fmt.Sprintf("MarsWallex 持卡人 %s 状态变为 %s，自动补卡将跳过该持卡人", id, status)
		if remark != "" {
			msg += "。平台备注：" + truncate(remark, 200)
		}
		s.alert("holder:"+id, msg)
	}
}
