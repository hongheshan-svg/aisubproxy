package payengine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
)

const (
	shotRetention  = 7 * 24 * time.Hour // 截图保留时长
	shotCleanEvery = 24 * time.Hour     // 截图清理间隔
	// profileCleanEvery：崩溃残留的 Chrome 用户目录里有用户会话 cookie，清得比截图勤。
	profileCleanEvery = time.Hour
	txnPageSize       = 100 // 翻交易时每页条数；卡池的分页上限更小时按实际读到的条数继续翻
	restartMessage    = "服务重启，未提交"

	// authClockSkew 是查授权交易时往 submitted_at 之前多看的时长。transaction_time 是平台时钟（Unix 秒），
	// 与本机 submitted_at 可能有偏差，真实授权可能显示在提交之前。多算一笔只会多一次人工核对，
	// 漏算会把已扣款单误判为被拒，所以宁可放宽窗口。
	authClockSkew = 5 * time.Minute

	// declineBackfillMargin 是选补拉窗口时多留的余量，盖住 authClockSkew 和补拉本身的耗时。
	declineBackfillMargin = time.Hour
)

// declineBackfillDays 选定论失败前补拉的天数（卡池只接受 1、3、7）。窗口要盖住提交时间：服务停机再恢复时单据可能已提交很久，
// 窗口不够就看不到当时的授权。ok 为 false 表示超出卡池能补拉的范围，不能定论。没有提交时间时取最大窗口。
func declineBackfillDays(submittedAt *int64, now time.Time) (days int, ok bool) {
	if submittedAt == nil {
		return 7, true
	}
	age := now.Sub(time.Unix(*submittedAt, 0)) + declineBackfillMargin
	for _, d := range []int{1, 3, 7} {
		if age <= time.Duration(d)*24*time.Hour {
			return d, true
		}
	}
	return 0, false
}

// orderNoName 是单号格式，截图清理只动名字符合它的目录。
var orderNoName = regexp.MustCompile(`^PO\d{12}[0-9a-f]{8}$`)

// isDeclinedOutcome 判断页面结果是否按「被拒」的流程处理：只等 DeclineGrace、走 verifyDeclined、重启后补宽限。
// 校验错误也走这条路：文字是否属实只有真实页面知道，钱有没有扣要看证据，只有最终定论不同（见 concludeDeclined）。
func isDeclinedOutcome(page string) bool {
	return page == string(paybrowser.OutcomeDeclined) || page == string(paybrowser.OutcomeFormRejected)
}

// beginVerify 关闭浏览器，把已提交的单据转入确认阶段（步骤 9）。text 是页面提示或出错说明，记入 error_message；
// 被拒或校验错误时只等 DeclineGrace（让授权推送到达），其余等满 Verify。
func (s *Service) beginVerify(ctx context.Context, j *job, out paybrowser.Outcome, text, note string) error {
	j.closeBrowser()
	now := s.now()
	until := now.Add(s.t.Verify)
	page := string(paybrowser.OutcomeUnknown)
	switch {
	case out == paybrowser.OutcomeSuccess:
		page = string(out)
	case isDeclinedOutcome(string(out)):
		page = string(out)
		until = now.Add(s.t.DeclineGrace)
	}
	set := map[string]any{"page_outcome": page, "verify_until": until.Unix(), "next_check_at": now.Unix()}
	if text != "" {
		text = truncate(j.scrub(text), errorMessageLimit)
		set["error_message"] = text
		j.o.ErrorMessage = text
	}
	return s.advance(ctx, j, StateVerifying, note, set)
}

// verifyLoop 是确认协程：每隔 VerifyTick 扫描一次到期的确认中单据。不占浏览器名额。
func (s *Service) verifyLoop(ctx context.Context) {
	defer s.loops.Done()
	ticker := time.NewTicker(s.t.VerifyTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		s.verifyDue(ctx)
	}
}

// verifyDue 接管无人处理的已提交单，再检查到期的确认中单据。
// 恢复没做完时：不接管（恢复本身会转，同时做会互相抢），被拒单也不定论（宽限期可能还没补上，按过期的 verify_until 会定成 declined）；
// 其余单据定论都是安全方向（成功或转人工），照常检查。
func (s *Service) verifyDue(ctx context.Context) {
	recovered := s.recovered.Load()
	if recovered {
		s.adoptSubmitted(ctx)
	}
	list, err := dueVerifying(ctx, s.now().Unix())
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[payengine] 读取确认中单据失败: %v", err)
		}
		return
	}
	for i := range list {
		if ctx.Err() != nil {
			return
		}
		if !recovered && isDeclinedOutcome(list[i].PageOutcome) {
			continue
		}
		s.verifyOrder(ctx, &list[i])
	}
}

// adoptSubmitted 接管已提交、但没有工作协程在处理的单据：settle 连续转确认失败后它们停在 submitted / requires_3ds，
// 没人再查，可能一直停到下次重启，其间租约到期、余额被转出。靠条件更新保证安全，这里不持有浏览器，走不到点支付。
func (s *Service) adoptSubmitted(ctx context.Context) {
	list, err := ordersInStates(ctx, StateSubmitted, StateRequires3DS)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[payengine] 读取已提交单据失败: %v", err)
		}
		return
	}
	for i := range list {
		if s.isRunning(list[i].OrderNo) {
			continue
		}
		j := &job{o: &list[i]}
		err := s.beginVerify(ctx, j, paybrowser.OutcomeUnknown, "", "转确认曾失败，由确认协程接管，确认是否开通")
		if err != nil && ctx.Err() == nil && !errors.Is(err, ErrStateChanged) {
			log.Printf("[payengine] 单据 %s 接管失败，下一轮再试: %s", j.o.OrderNo, j.scrub(err.Error()))
		}
	}
}

func (s *Service) isRunning(no string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[no]
}

// verifyOrder 检查一张确认中的单据，定论规则见 spec §6。账号已开通即成功；到期前的任何错误都只记事件、等下次检查。
func (s *Service) verifyOrder(ctx context.Context, o *Order) {
	j := &job{o: o}
	opened, accErr := s.verifyAccount(ctx, j)
	if ctx.Err() != nil {
		return // 停机：下次启动接着查
	}
	if opened {
		s.verifyConclude(ctx, j, StateSucceeded, "info", "账号已开通，付款成功",
			cardpool.Outcome{Result: cardpool.ResultSuccess}, map[string]any{"error_code": "", "error_message": ""})
		return
	}
	s.noteVerifyErr(ctx, j, "account", "查询账号失败，等下次检查：", accErr)
	now := s.now().Unix()
	var until int64
	if o.VerifyUntil != nil {
		until = *o.VerifyUntil
	}
	switch {
	case now < until:
		s.verifyReschedule(ctx, j)
	case isDeclinedOutcome(o.PageOutcome):
		s.verifyDeclined(ctx, j, accErr, now >= until+int64(s.t.Verify/time.Second))
	default:
		s.verifyEscalate(ctx, j, "verify_timeout", "确认超时：账号未开通，支付页也没有明确拒绝")
	}
}

// verifyDeclined 定论支付页显示被拒或校验错误、宽限期已过的单据。只有确认了账号未开通、翻完交易没有成功授权也没有处理中的授权，才算失败。
// 证据不全（查询出错、授权还在处理中）绝不定成被拒——把已扣款的单子定成 declined 是最贵的错误：
// 继续等下次检查，等到 exhausted（verify_until 之后再过一个 Verify）仍不全就转人工。
// 本地证据都说可以定论时，先补拉一次交易再看：Webhook 不通时本地交易表只靠 15 分钟一次的定时补拉，宽限期内看不到授权。
func (s *Service) verifyDeclined(ctx context.Context, j *job, accErr error, exhausted bool) {
	approved, pending, err := s.verifyAuth(ctx, j.o)
	if ctx.Err() != nil {
		return
	}
	s.noteVerifyErr(ctx, j, "auth", "查询授权交易失败，等下次检查：", err)
	if err == nil && !approved && !pending && accErr == nil {
		approved, pending, err = s.backfillAndRecheck(ctx, j)
		if ctx.Err() != nil {
			return
		}
	}
	switch {
	case err != nil:
		// 证据不全，走下面的等待或转人工
	case approved:
		s.verifyEscalate(ctx, j, "evidence_conflict", "证据冲突："+pageFailText(j.o.PageOutcome)+"，但该卡有成功授权")
		return
	case !pending && accErr == nil:
		s.concludeDeclined(ctx, j)
		return
	}
	if exhausted {
		s.verifyEscalate(ctx, j, "evidence_unavailable", pageFailText(j.o.PageOutcome)+"，但一直无法确认账号与授权交易")
		return
	}
	s.verifyReschedule(ctx, j)
}

// pageFailText 是转人工时对页面结果的描述。
func pageFailText(pageOutcome string) string {
	if pageOutcome == string(paybrowser.OutcomeFormRejected) {
		return "支付页提示校验未通过"
	}
	return "支付页显示被拒"
}

// backfillAndRecheck 补拉一次交易后重新查授权。补拉出错或超时按证据不全处理，返回错误，由 verifyDeclined 走等待或转人工。
func (s *Service) backfillAndRecheck(ctx context.Context, j *job) (approved, pending bool, err error) {
	days, ok := declineBackfillDays(j.o.SubmittedAt, s.now())
	if !ok {
		err = errors.New("提交时间早于卡池能补拉的范围（7 天），无法确认授权")
		s.noteVerifyErr(ctx, j, "backfill", "补拉交易失败，等下次检查：", err)
		return false, false, err
	}
	bctx, cancel := context.WithTimeout(ctx, s.t.Backfill)
	defer cancel()
	_, err = s.opts.Pool.BackfillRecent(bctx, days)
	if ctx.Err() != nil {
		return false, false, ctx.Err()
	}
	s.noteVerifyErr(ctx, j, "backfill", "补拉交易失败，等下次检查：", err)
	if err != nil {
		return false, false, err
	}
	approved, pending, err = s.verifyAuth(ctx, j.o)
	if ctx.Err() != nil {
		return false, false, ctx.Err()
	}
	s.noteVerifyErr(ctx, j, "auth", "查询授权交易失败，等下次检查：", err)
	return approved, pending, err
}

// concludeDeclined 写失败定论。校验错误多半是配置或表单问题，不是这张卡或这个产品的问题，所以 Release(aborted) 不计入健康度；
// 失败类别仍是 declined：单据已提交，绝不记成 not_submitted。
func (s *Service) concludeDeclined(ctx context.Context, j *job) {
	if j.o.PageOutcome == string(paybrowser.OutcomeFormRejected) {
		msg := "页面校验未通过"
		if j.o.ErrorMessage != "" {
			msg += "：" + j.o.ErrorMessage
		}
		msg = truncate(j.scrub(msg), errorMessageLimit)
		s.verifyConclude(ctx, j, StateFailed, "error", msg, cardpool.Outcome{Result: cardpool.ResultAborted},
			map[string]any{"failure": FailureDeclined, "error_code": "form_rejected", "error_message": msg})
		return
	}
	reason := j.o.ErrorMessage
	if reason == "" {
		reason = "支付页显示被拒"
	}
	s.verifyConclude(ctx, j, StateFailed, "error", "支付被拒："+reason,
		cardpool.Outcome{Result: cardpool.ResultFailed, ErrorCode: truncate(reason, 200)},
		map[string]any{"failure": FailureDeclined, "error_code": "card_declined", "error_message": reason})
}

// noteVerifyErr 把确认错误写进时间线，同一单、同一来源的文本没变就不再写：定时检查每 15 秒一轮，故障期间不能刷满时间线。
// err 为 nil 表示这个来源已恢复，之后再出错算新的故障。
func (s *Service) noteVerifyErr(ctx context.Context, j *job, source, prefix string, err error) {
	no := j.o.OrderNo
	if err == nil {
		s.mu.Lock()
		delete(s.verifyErrs[no], source)
		s.mu.Unlock()
		return
	}
	text := prefix + j.scrub(err.Error())
	s.mu.Lock()
	if s.verifyErrs[no] == nil {
		s.verifyErrs[no] = map[string]string{}
	}
	same := s.verifyErrs[no][source] == text
	s.verifyErrs[no][source] = text
	s.mu.Unlock()
	if !same {
		s.event(ctx, j, "warn", text, "")
	}
}

// verifyAccount 解开单据里的会话（终态才清空）查账号，判断是否已是目标套餐。
// 期望套餐类型直接读套餐表：下单后管理员停用该地区不影响确认。
func (s *Service) verifyAccount(ctx context.Context, j *job) (bool, error) {
	tbl, err := LoadPlanTable()
	if err != nil {
		return false, err
	}
	expect := ""
	for i := range tbl {
		if tbl[i].Key == j.o.PlanKey {
			expect = strings.TrimSpace(tbl[i].ExpectPlanType)
		}
	}
	if expect == "" {
		return false, fmt.Errorf("套餐 %s 未配置期望套餐类型", j.o.PlanKey)
	}
	sess, err := openSession(j.o.SessionSealed)
	if err != nil {
		return false, errors.New("会话密文无法解开")
	}
	j.useSession(sess)
	proxies, err := LoadProxies()
	if err != nil {
		return false, fmt.Errorf("读取地区代理失败：%w", err)
	}
	j.proxy = proxies[j.o.Region]
	gpt, err := s.opts.ChatGPT(j.proxy)
	if err != nil {
		return false, fmt.Errorf("创建 ChatGPT 客户端失败：%w", err)
	}
	if err := gpt.Refresh(ctx, &j.sess); err != nil {
		return false, fmt.Errorf("刷新会话失败：%w", err)
	}
	acc, err := gpt.Account(ctx, j.sess)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(acc.PlanType), expect), nil
}

// authWindowStart 返回一单授权交易窗口的起点（含）：submitted_at 减去 authClockSkew。定论与 Evidence 共用，口径才一致。
func authWindowStart(submittedAt int64) int64 {
	return submittedAt - int64(authClockSkew/time.Second)
}

// verifyAuth 翻完该卡在授权窗口内（见 authWindowStart）的交易，看有没有成功授权、未定论的授权。
// 只看 AUTH 与 SETTLE：approved 算成功，failed 不算，其余任何取值（含空串、未知值）都算未定论。
// 文档枚举 AUTH 为 approved/failed/pending、SETTLE 为 approved/pending，平台未实测，所以默认不放行：
// 把已扣款的单定成 declined 是最贵的错误，多一次人工核对不是。平台状态原样入库，比较时不区分大小写。
func (s *Service) verifyAuth(ctx context.Context, o *Order) (approved, pending bool, err error) {
	if o.CardID == "" {
		return false, false, errors.New("单据没有记录卡")
	}
	var since int64
	if o.SubmittedAt != nil {
		since = authWindowStart(*o.SubmittedAt)
	}
	list, err := s.txnsSince(ctx, o.CardID, since)
	if err != nil {
		return false, false, err
	}
	for _, t := range list {
		if !strings.EqualFold(t.Kind, "AUTH") && !strings.EqualFold(t.Kind, "SETTLE") {
			continue
		}
		switch {
		case strings.EqualFold(t.Status, "approved"):
			approved = true
		case !strings.EqualFold(t.Status, "failed"):
			pending = true
		}
	}
	return approved, pending, nil
}

// txnsSince 返回该卡在 since 之后的交易（新的在前）。交易按时间倒序分页，翻到全部读完或出现早于 since 的记录为止。
// 时间只精确到秒，与 since 同一秒的算在内。
func (s *Service) txnsSince(ctx context.Context, cardID string, since int64) ([]cardpool.Transaction, error) {
	out := []cardpool.Transaction{}
	seen := 0
	for page := 1; ; page++ {
		list, total, err := s.opts.Pool.ListTransactions(ctx, cardpool.TxnFilter{CardID: cardID, Page: page, PageSize: txnPageSize})
		if err != nil {
			return nil, err
		}
		for _, t := range list {
			if txnTime(t) >= since {
				out = append(out, t)
			}
		}
		seen += len(list)
		if len(list) == 0 || seen >= total || txnTime(list[len(list)-1]) < since {
			return out, nil
		}
	}
}

// txnTime 取交易时间；没有发生时间时用更新时间，与卡池的排序口径一致。
func txnTime(t cardpool.Transaction) int64 {
	if t.OccurredAt != nil {
		return *t.OccurredAt
	}
	return t.UpdatedAt
}

func (s *Service) verifyReschedule(ctx context.Context, j *job) {
	if err := setNextCheck(ctx, j.o.OrderNo, s.now().Add(s.t.VerifyEvery).Unix()); err != nil && ctx.Err() == nil {
		log.Printf("[payengine] 单据 %s 更新下次检查时间失败: %v", j.o.OrderNo, err)
	}
}

// verifyEscalate 转待人工：Release(aborted)，告警。
func (s *Service) verifyEscalate(ctx context.Context, j *job, code, reason string) {
	msg := reason
	if j.o.ErrorMessage != "" {
		msg += "；页面或浏览器提示：" + j.o.ErrorMessage
	}
	msg = truncate(j.scrub(msg), errorMessageLimit)
	if s.verifyConclude(ctx, j, StateNeedsReview, "error", msg, cardpool.Outcome{Result: cardpool.ResultAborted},
		map[string]any{"error_code": code, "error_message": msg}) {
		s.opts.Alert(fmt.Sprintf("付款单 %s 转待人工：%s", j.o.OrderNo, msg))
	}
}

// verifyConclude 先按单据字段重建租约并释放，再写终态。写终态失败时单据留在 verifying，下次检查重新定论（再释放得到 ErrLeaseInvalid，只记 warn）。
func (s *Service) verifyConclude(ctx context.Context, j *job, to, level, msg string, oc cardpool.Outcome, set map[string]any) bool {
	s.release(ctx, j, leaseFromOrder(j.o), oc)
	if err := s.end(ctx, j, to, level, msg, set); err != nil {
		if ctx.Err() == nil {
			log.Printf("[payengine] 单据 %s 写入 %s 失败，下次检查重试: %s", j.o.OrderNo, to, j.scrub(err.Error()))
		}
		return false
	}
	s.mu.Lock()
	delete(s.verifyErrs, j.o.OrderNo)
	s.mu.Unlock()
	return true
}

// leaseFromOrder 按单据字段重建租约：重启后或确认阶段没有内存中的 Lease。单据没有租约时返回 nil。
func leaseFromOrder(o *Order) *cardpool.Lease {
	if o.LeaseToken == "" || o.CardID == "" {
		return nil
	}
	return &cardpool.Lease{Token: o.LeaseToken, CardID: o.CardID, PlanKey: o.PlanKey, OrderRef: o.OrderNo, AccountEmail: o.AccountEmail}
}

// recoverOrders 收敛上次运行遗留的单据，必须在第一次派发之前完成：
// 提交之前的中间状态判 failed / not_submitted 并释放租约；submitted、requires_3ds 转 verifying 继续确认，绝不再点支付；
// 页面被拒或校验错误的 verifying 单补上宽限期。queued 留给工作协程，其余 verifying 留给确认协程。有单据没收敛时返回错误，由工作协程重试。
func (s *Service) recoverOrders(ctx context.Context) error {
	stale, err := ordersInStates(ctx, StateChecking, StateLinkCreated, StateOpening, StateFunding, StateFilling)
	if err != nil {
		return err
	}
	var errs []error
	for i := range stale {
		j := &job{o: &stale[i]}
		s.release(ctx, j, leaseFromOrder(j.o), cardpool.Outcome{Result: cardpool.ResultAborted})
		if err := s.fail(ctx, j, &stepFailure{code: "service_restart", msg: restartMessage}); err != nil {
			errs = append(errs, fmt.Errorf("单据 %s: %w", j.o.OrderNo, err))
		}
	}
	submitted, err := ordersInStates(ctx, StateSubmitted, StateRequires3DS)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for i := range submitted {
		j := &job{o: &submitted[i]}
		if err := s.beginVerify(ctx, j, paybrowser.OutcomeUnknown, "", "服务重启，继续确认是否开通"); err != nil {
			errs = append(errs, fmt.Errorf("单据 %s: %w", j.o.OrderNo, err))
		}
	}
	verifying, err := ordersInStates(ctx, StateVerifying)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for i := range verifying {
		if err := s.restoreDeclineGrace(ctx, &verifying[i]); err != nil {
			errs = append(errs, fmt.Errorf("单据 %s: %w", verifying[i].OrderNo, err))
		}
	}
	return errors.Join(errs...)
}

// restoreDeclineGrace 让页面被拒或校验错误的单重新等够 DeclineGrace：停机期间授权推送送不到，
// 按停机前的 verify_until 定论，会把已扣款的单定成 declined。只推后不提前。
// 每次启动每单只补一次：恢复可能因别的单失败而反复重试，不能每轮都写事件、推后 verify_until。
func (s *Service) restoreDeclineGrace(ctx context.Context, o *Order) error {
	if !isDeclinedOutcome(o.PageOutcome) || s.graceRestored[o.OrderNo] {
		return nil
	}
	until := s.now().Add(s.t.DeclineGrace).Unix()
	if o.VerifyUntil == nil || *o.VerifyUntil < until {
		err := transition(ctx, o.OrderNo, StateVerifying, StateVerifying, "info", "服务重启，重新等待授权推送", map[string]any{"verify_until": until})
		if err != nil {
			return err
		}
	}
	if s.graceRestored == nil {
		s.graceRestored = map[string]bool{}
	}
	s.graceRestored[o.OrderNo] = true
	return nil
}

// tryRecover 收敛遗留单据，返回是否已全部完成。没完成时不能派发，由工作协程下一轮再试。
// 停机取消导致的中断不算故障，不告警。
func (s *Service) tryRecover(ctx context.Context) bool {
	err := s.recoverOrders(ctx)
	switch {
	case err == nil:
		if s.recoveryFailed {
			log.Printf("[payengine] 重启恢复已完成，恢复派发")
		}
		s.recovered.Store(true)
		return true
	case ctx.Err() == nil:
		s.reportRecoveryFailure(err)
	}
	return false
}

// reportRecoveryFailure 每次失败都打日志；只在第一次失败时告警：派发被挡住时队列会无声停摆，但重试不必反复提醒。
func (s *Service) reportRecoveryFailure(err error) {
	msg := scrubRecoveryError(err)
	log.Printf("[payengine] 重启恢复未完成，暂不派发新单据: %s", msg)
	if s.recoveryFailed {
		return
	}
	s.recoveryFailed = true
	s.opts.Alert("付款引擎重启恢复失败，新单据暂不派发：" + msg)
}

// scrubRecoveryError 脱敏恢复阶段的错误。这时没有单据上下文，也没有解开过会话，能夹带的只有代理地址，
// 所以按配置的全部地区代理脱敏；读不出代理配置就无法保证脱敏，不带原文，只给读配置的错误。
func scrubRecoveryError(err error) string {
	proxies, perr := LoadProxies()
	if perr != nil {
		return truncate("读取代理配置失败，原错误已省略："+perr.Error(), errorMessageLimit)
	}
	text := err.Error()
	for _, p := range proxies {
		text = (&job{proxy: p}).scrub(text)
	}
	return truncate(text, errorMessageLimit)
}

// cleanLoop 启动时清理一次截图，之后截图每天一次、浏览器残留每小时一次（残留的首次清理在 Start 里同步做）。
func (s *Service) cleanLoop(ctx context.Context) {
	defer s.loops.Done()
	s.cleanShots()
	shots := time.NewTicker(shotCleanEvery)
	defer shots.Stop()
	profiles := time.NewTicker(profileCleanEvery)
	defer profiles.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-shots.C:
			s.cleanShots()
		case <-profiles.C:
			s.cleanProfiles()
		}
	}
}

// cleanProfiles 只在用真实浏览器时清理：注入假浏览器的测试不该碰系统临时目录，
// 否则 go test ./... 并行时可能影响其它包正在跑的 Chrome。
func (s *Service) cleanProfiles() {
	if s.realBrowser {
		paybrowser.CleanStaleProfiles()
	}
}

// cleanShots 删除 ShotDir 下名字符合单号格式、修改时间早于 7 天的目录。
// ReadDir 不跟随符号链接（链接的 IsDir 为 false，直接跳过），RemoveAll 也不会穿过目录里的链接。删除失败只打日志。
func (s *Service) cleanShots() {
	entries, err := os.ReadDir(s.opts.ShotDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("[payengine] 读取截图目录失败: %v", err)
		}
		return
	}
	cutoff := s.now().Add(-shotRetention)
	for _, e := range entries {
		if !e.IsDir() || !orderNoName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			log.Printf("[payengine] 读取截图目录 %s 失败: %v", e.Name(), err)
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.opts.ShotDir, e.Name())); err != nil {
			log.Printf("[payengine] 删除截图目录 %s 失败: %v", e.Name(), err)
		}
	}
}
