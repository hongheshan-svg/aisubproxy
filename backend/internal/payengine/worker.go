package payengine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

const (
	dispatchInterval  = 10 * time.Second // 没有 Kick 时也定期查一次排队单据
	errorMessageLimit = 500              // error_message 最多保留的字数
	sessionDomain     = ".chatgpt.com"
)

// Kick 非阻塞地通知工作协程立即派发一轮；已有待处理的通知时合并。
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Start 同步完成重启恢复后启动后台协程：工作协程（收到 Kick 或每 10 秒按建单顺序取 queued 单据处理，
// 同时处理的单据数不超过 LoadConcurrency()）、确认协程（每 5 秒扫描确认中的单据）、清理协程（截图每天一次，浏览器残留每小时一次）。
// 恢复没做完时不派发、不定论被拒单，工作协程每一轮先重试恢复，首次失败告警一次。ctx 结束时全部停止，处理中的单据随之中断，留待下次启动恢复。
func (s *Service) Start(ctx context.Context) {
	s.cleanProfiles() // 第一次派发之前先扫一遍，之后由清理协程每小时一次
	s.tryRecover(ctx)
	s.loops.Add(4)
	safego.Go("付款派发循环", func() { s.loop(ctx) })
	safego.Go("付款确认循环", func() { s.verifyLoop(ctx) })
	safego.Go("付款清理循环", func() { s.cleanLoop(ctx) })
	safego.Go("关闭续费循环", func() { s.renewalLoop(ctx) })
	s.Kick()
}

// Wait 阻塞到后台协程与全部单据协程退出；调用前须先取消传给 Start 的 ctx。
func (s *Service) Wait() { s.loops.Wait() }

func (s *Service) loop(ctx context.Context) {
	defer s.loops.Done()
	ticker := time.NewTicker(dispatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		// 每一轮单独兜住 panic：一轮出错，下一轮照常派发
		safego.Run("付款派发", func() {
			if !s.recovered.Load() && !s.tryRecover(ctx) {
				return
			}
			s.dispatch(ctx)
		})
	}
}

// dispatch 按 id 顺序为排队单据分配名额，每张单据一个协程。
func (s *Service) dispatch(ctx context.Context) {
	if s.breakerPaused(ctx) {
		return // 熔断：不派发新单；在途单由确认协程照常走完
	}
	limit := LoadConcurrency()
	s.mu.Lock()
	full := len(s.running) >= limit
	s.mu.Unlock()
	if full {
		return
	}
	queued, err := ordersInStates(ctx, StateQueued)
	if err != nil {
		log.Printf("[payengine] 读取排队单据失败: %v", err)
		return
	}
	for i := range queued {
		o := queued[i]
		s.mu.Lock()
		if len(s.running) >= limit {
			s.mu.Unlock()
			return
		}
		if s.running[o.OrderNo] {
			s.mu.Unlock()
			continue
		}
		s.running[o.OrderNo] = true
		s.mu.Unlock()
		s.loops.Add(1)
		safego.Go("付款单处理", func() { s.handle(ctx, &o) })
	}
}

// handle 处理一张单据并归还名额；领取成功的单据处理完后踢醒工作协程，让排队的单据接上。
// 处理中 panic 时由 recoverJob 按结算规则收尾；名额在 defer 里归还，panic 也不会占着不放。
func (s *Service) handle(ctx context.Context, o *Order) {
	defer s.loops.Done()
	j := &job{o: o}
	kick := false
	defer func() {
		if r := recover(); r != nil {
			s.recoverJob(ctx, j, r)
			kick = true
		}
		s.mu.Lock()
		delete(s.running, o.OrderNo)
		s.mu.Unlock()
		if kick {
			s.Kick()
		}
	}()
	kick = s.process(ctx, j)
}

// recoverJob 收尾处理中 panic 的单据（见 settlePanic），最后关浏览器。panic 的值可能带卡号、会话，只记类型和位置。
// 收尾本身再出错只记日志：单据留在中间状态，由确认协程（已提交的）或周期扫描（提交前的，见 sweepStale）接管。
func (s *Service) recoverJob(ctx context.Context, j *job, r any) {
	desc := safego.Describe(r)
	defer closeBrowserQuietly(j)
	defer safego.RecoverNoAlert("付款单 " + j.o.OrderNo + " 内部错误后的收尾")
	log.Printf("[payengine] 单据 %s 在 %s 时内部错误（panic）：%s", j.o.OrderNo, j.o.State, desc)
	if ctx.Err() == nil {
		s.opts.Alert(fmt.Sprintf("付款单 %s 在 %s 时内部错误（panic）：%s。已按规则收尾：写过提交时间或不能证明没点支付的转确认，其余按提交前放弃。", j.o.OrderNo, j.o.State, desc))
	}
	s.settlePanic(ctx, j)
}

// closeBrowserQuietly 关浏览器，关的时候 panic 只记日志。
func closeBrowserQuietly(j *job) {
	defer safego.RecoverNoAlert("关闭付款浏览器")
	j.closeBrowser()
}

// settlePanic 按结算规则收尾 panic 的单据：写过 submitted_at，或者浏览器不能证明没点过支付（LockSubmit），一律转确认；
// 否则按提交前放弃处理，记 internal_error。服务停止时不收尾，单据与租约都留给重启恢复。
func (s *Service) settlePanic(ctx context.Context, j *job) {
	if ctx.Err() != nil {
		log.Printf("[payengine] 单据 %s 在 %s 时服务停止，留待重启后处理", j.o.OrderNo, j.o.State)
		return
	}
	if s.mayHaveClicked(ctx, j) {
		s.toVerify(ctx, j, "内部错误，点击结果不明")
		return
	}
	s.abandon(ctx, j, &stepFailure{code: "internal_error", msg: "内部错误，已放弃（没有点支付）"})
}

// mayHaveClicked 判断 panic 的单据是不是可能已经点过支付：内存或库里有 submitted_at 就算；
// 都没有时，开着浏览器的要 LockSubmit 返回 false 才能证明没点过（试运行单打开页面时就锁定了提交，付款流程也拒绝试运行单）。
// 读库失败时以内存为准：submitted_at 写库成功后立刻记到内存里，两者之间没有别的调用。
func (s *Service) mayHaveClicked(ctx context.Context, j *job) (clicked bool) {
	if j.o.SubmittedAt != nil {
		return true
	}
	if fresh, err := getOrder(ctx, j.o.OrderNo); err == nil && fresh.SubmittedAt != nil {
		j.o.SubmittedAt = fresh.SubmittedAt
		return true
	}
	if j.browser == nil || j.o.DryRun {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			clicked = true // LockSubmit 自己出错：证明不了没点过
		}
	}()
	return j.browser.LockSubmit()
}

// job 是处理中的一张单据及其运行期资源。
type job struct {
	o       *Order
	sess    chatgpt.Session
	proxy   string
	gpt     ChatGPT
	browser Browser
	shots   int            // 已截图张数，用作截图名序号
	pending map[string]any // 随下一次状态写入一并落库的列

	pool       Pool            // 单据所属服务商的卡池，见 jobPool；整张单只用这一个
	lease      *cardpool.Lease // 提交前放弃时要释放的租约；提交之后由确认阶段按单据字段释放
	holderName string          // 持卡人英文名，填表用
	secrets    []string        // 要从文字里抹掉的 CVV、验证码
	cardNo     *regexp.Regexp  // 匹配完整卡号（含空格或连字符分组），要从文字里抹掉

	// keepSession 为 true 时写终态不清空会话，留给续费协程关闭续费，最长保留 SessionKeep（见 renewal.go）
	keepSession bool

	floorMinor int64 // 最低金额，建会话时取自套餐配置；不落库，openPage 判下限用
}

// note 记下一列，随下一次状态写入落库。
func (j *job) note(col string, v any) {
	if j.pending == nil {
		j.pending = map[string]any{}
	}
	j.pending[col] = v
}

func (j *job) merge(set map[string]any) map[string]any {
	out := make(map[string]any, len(j.pending)+len(set))
	for k, v := range j.pending {
		out[k] = v
	}
	for k, v := range set {
		out[k] = v
	}
	return out
}

func (j *job) closeBrowser() {
	if j.browser != nil {
		j.browser.Close()
		j.browser = nil
	}
}

// useSession 记下解开的会话。刷新会覆盖 AccessToken，原 token 单独登记，免得它夹在错误文字里漏过脱敏。
func (j *job) useSession(sess chatgpt.Session) {
	j.sess = sess
	j.secrets = append(j.secrets, sess.AccessToken)
}

// noteCardSecrets 登记完整卡号与 CVV：之后所有经过 scrub 的文字都会抹掉它们。
func (j *job) noteCardSecrets(cardNo, cvv string) {
	if digits := []rune(strings.TrimSpace(cardNo)); len(digits) > 0 {
		parts := make([]string, len(digits))
		for i, r := range digits {
			parts[i] = regexp.QuoteMeta(string(r))
		}
		j.cardNo = regexp.MustCompile(strings.Join(parts, `[\s-]*`))
	}
	j.secrets = append(j.secrets, cvv)
}

// scrub 去掉文字中可能夹带的完整卡号、CVV、验证码、代理地址、代理账号密码（含各种转义形式）、会话 token 与账号邮箱；长串先替换。
// 卡号最先处理：CVV 可能恰好是卡号的一段，先替换 CVV 会让卡号匹配不上。
func (j *job) scrub(s string) string {
	if j.cardNo != nil {
		s = j.cardNo.ReplaceAllString(s, "***")
	}
	secrets := append([]string{j.proxy, j.sess.SessionToken, j.sess.AccessToken}, j.secrets...)
	if j.o != nil { // 恢复阶段脱敏用的 job 没有单据
		secrets = append(secrets, j.o.AccountEmail) // 页面文字可能带账号邮箱，邮箱也可能被填进页面
	}
	if u, err := url.Parse(j.proxy); err == nil && u.User != nil {
		secrets = append(secrets, u.User.String())
		if pw, ok := u.User.Password(); ok {
			secrets = append(secrets, pw, url.QueryEscape(pw), url.PathEscape(pw))
		}
	}
	if i := strings.Index(j.proxy, "://"); i >= 0 {
		if at := strings.LastIndex(j.proxy, "@"); at > i {
			secrets = append(secrets, j.proxy[i+3:at])
		}
	}
	sort.SliceStable(secrets, func(a, b int) bool { return len(secrets[a]) > len(secrets[b]) })
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	return s
}

// stepFailure 表示提交前放弃：单据以 failed / not_submitted 结束。
type stepFailure struct{ code, msg string }

func (f *stepFailure) Error() string { return f.code + ": " + f.msg }

func abort(code, format string, a ...any) error {
	return &stepFailure{code: code, msg: fmt.Sprintf(format, a...)}
}

// notClickedError 表示支付按钮确实没被点击，以后也不会被点：由浏览器点击前置位的标志证明（见 Browser.LockSubmit）。
// 只有 submitAndWait 能造出它。
type notClickedError struct{ err error }

func (e *notClickedError) Error() string { return e.err.Error() }
func (e *notClickedError) Unwrap() error { return e.err }

// chatgptAbort 把 ChatGPT 调用失败转成放弃原因；未授权一律按会话失效处理。
// Cloudflare 挑战不是会话失效：换会话没有用，要查该地区代理的出口 IP，所以单独一个错误码并告警。
func (s *Service) chatgptAbort(j *job, code, what string, err error) error {
	if errors.Is(err, chatgpt.ErrBlocked) {
		s.opts.Alert(fmt.Sprintf("付款单 %s（地区 %s）%s：被 Cloudflare 拦截，请检查该地区代理的出口 IP；换会话没有用。", j.o.OrderNo, j.o.Region, what))
		return abort("cf_challenge", "%s：被 Cloudflare 拦截，请检查该地区代理的出口 IP", what)
	}
	if errors.Is(err, chatgpt.ErrUnauthorized) {
		return abort("session_invalid", "%s：会话已失效，请重新获取会话", what)
	}
	return abort(code, "%s：%v", what, err)
}

// advance 把单据从当前状态推进到 to（非终态），一并写入 pending 与 set。
func (s *Service) advance(ctx context.Context, j *job, to, msg string, set map[string]any) error {
	if err := transition(ctx, j.o.OrderNo, j.o.State, to, "info", msg, j.merge(set)); err != nil {
		return err
	}
	j.o.State, j.pending = to, nil
	return nil
}

// end 先关闭浏览器，再把单据写入终态 to。j.keepSession 为 true 时会话留着（关闭续费用），否则清空。
func (s *Service) end(ctx context.Context, j *job, to, level, msg string, set map[string]any) error {
	j.closeBrowser()
	mode := moveEnd
	if j.keepSession {
		mode = moveEndKeepSession
	}
	if err := move(ctx, j.o.OrderNo, j.o.State, to, level, msg, j.merge(set), mode); err != nil {
		return err
	}
	j.o.State, j.pending = to, nil
	return nil
}

func (s *Service) fail(ctx context.Context, j *job, f *stepFailure) error {
	msg := truncate(j.scrub(f.msg), errorMessageLimit)
	return s.end(ctx, j, StateFailed, "error", msg, map[string]any{
		"failure": FailureNotSubmitted, "error_code": f.code, "error_message": msg,
	})
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// event 追加一条时间线事件，失败只记日志。
func (s *Service) event(ctx context.Context, j *job, level, msg, screenshot string) {
	if err := addEvent(ctx, j.o.OrderNo, level, msg, screenshot); err != nil {
		log.Printf("[payengine] 单据 %s 写时间线失败: %v", j.o.OrderNo, err)
	}
}

// shot 截一张图记入时间线，文件名为「两位序号-步骤」；截图失败只记警告。
func (s *Service) shot(ctx context.Context, j *job, step string, scope paybrowser.ShotScope) {
	j.shots++
	file, err := j.browser.Shot(ctx, fmt.Sprintf("%02d-%s", j.shots, step), scope)
	if err != nil {
		s.event(ctx, j, "warn", "截图失败："+j.scrub(err.Error()), "")
		return
	}
	s.event(ctx, j, "info", "已截图", file)
}

// process 领取并处理一张排队单据；未能领取（状态已变或写库失败）返回 false。
// 浏览器不用 defer 关：panic 时 settlePanic 还要问它有没有点过支付，由 recoverJob 最后关。
func (s *Service) process(ctx context.Context, j *job) bool {
	if err := s.advance(ctx, j, StateChecking, "检查账号", nil); err != nil {
		if !errors.Is(err, ErrStateChanged) && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
			log.Printf("[payengine] 单据 %s 领取失败: %v", j.o.OrderNo, err)
		}
		return false
	}
	s.settle(ctx, j, s.run(ctx, j))
	j.closeBrowser()
	return true
}

// settle 按 run 的结果收尾。一旦单据写入过 submitted_at，任何错误（放弃原因也好、意外错误也好）都转 verifying 交给确认协程：
// 已点支付的单子记成「未提交」，用户会重复付款。唯一的例外是 notClickedError：浏览器能严格证明按钮没被点击。
// 提交之前的错误释放租约并以 failed / not_submitted 结束，其中意外错误（如写库失败）记 internal_error，免得单据卡在中间状态。
// 写库再失败就告警：已提交的单由确认协程接管（adoptSubmitted），提交前的单要等重启恢复。
// 服务停止时不收尾：单据与租约都留给重启恢复。
func (s *Service) settle(ctx context.Context, j *job, err error) {
	var f *stepFailure
	var notClicked *notClickedError
	switch {
	case err == nil:
	case ctx.Err() != nil:
		log.Printf("[payengine] 单据 %s 在 %s 时服务停止，留待重启后处理", j.o.OrderNo, j.o.State)
	case errors.As(err, &notClicked):
		s.abandonNotClicked(ctx, j, notClicked)
	case j.o.SubmittedAt != nil:
		s.toVerify(ctx, j, err.Error())
	case errors.As(err, &f):
		s.abandon(ctx, j, f)
	default:
		msg := j.scrub(err.Error())
		log.Printf("[payengine] 单据 %s 在 %s 时中断: %s", j.o.OrderNo, j.o.State, msg)
		s.abandon(ctx, j, &stepFailure{code: "internal_error", msg: "内部错误：" + msg})
	}
}

// toVerify 把可能已经点过支付的单转确认，页面结果按不明处理；text 是出错说明（这里先脱敏）。
// 转不过去就告警：已提交的单由确认协程接管（adoptSubmitted）。
func (s *Service) toVerify(ctx context.Context, j *job, text string) {
	msg := truncate(j.scrub(text), errorMessageLimit)
	log.Printf("[payengine] 单据 %s 提交后在 %s 时出错，转确认: %s", j.o.OrderNo, j.o.State, msg)
	s.event(ctx, j, "warn", "提交后出错："+msg, "")
	if verr := s.beginVerify(ctx, j, paybrowser.OutcomeUnknown, msg, "页面结果不明，确认是否开通"); verr != nil {
		detail := j.scrub(verr.Error())
		log.Printf("[payengine] 单据 %s 转确认失败，等确认协程接管: %s", j.o.OrderNo, detail)
		if ctx.Err() == nil {
			s.opts.Alert(fmt.Sprintf("付款单 %s 已提交，但转确认连续失败，等确认协程接管：%s", j.o.OrderNo, detail))
		}
	}
}

// abandon 处理提交之前的放弃：关浏览器、Release(aborted)、写 failed / not_submitted。
func (s *Service) abandon(ctx context.Context, j *job, f *stepFailure) {
	j.closeBrowser()
	s.release(ctx, j, j.lease, cardpool.Outcome{Result: cardpool.ResultAborted})
	j.lease = nil
	if err := s.fail(ctx, j, f); err != nil {
		detail := j.scrub(err.Error())
		log.Printf("[payengine] 单据 %s 写入失败结果出错: %s", j.o.OrderNo, detail)
		if ctx.Err() == nil {
			s.opts.Alert(fmt.Sprintf("付款单 %s 放弃时写库失败，单据不收敛，该账号下单会一直报在途：%s", j.o.OrderNo, detail))
		}
	}
}

// abandonNotClicked 处理已记下提交时间、但按钮确实没被点击的单：和提交前放弃一样收尾，并清掉 submitted_at，
// 否则这单看起来像提交过。写库失败时单据还是 submitted，由确认协程接管，那是安全方向。
func (s *Service) abandonNotClicked(ctx context.Context, j *job, e *notClickedError) {
	if j.browser != nil {
		s.notePageDiag(ctx, j, j.browser, "支付按钮没有被点击")
	}
	j.note("submitted_at", nil)
	s.abandon(ctx, j, &stepFailure{code: "submit_not_clicked", msg: "支付按钮没有被点击：" + e.Error()})
}

// jobPool 取单据所属服务商的卡池，取到后记在 job 上。取不到时返回 false，绝不改用别家的卡池。
func (s *Service) jobPool(j *job) (Pool, bool) {
	if j.pool != nil {
		return j.pool, true
	}
	p, ok := s.poolFor(j.o.Provider)
	if ok {
		j.pool = p
	}
	return p, ok
}

// providerUnavailableText 是单据所属服务商没有卡池时的说明。
func providerUnavailableText(provider string) string {
	if provider == "" {
		return "单据没有记录服务商，无法确定用哪家的卡池"
	}
	return fmt.Sprintf("服务商 %q 没有可用的卡池", provider)
}

// release 释放租约并上报用卡结果。租约已失效（到期被卡池收回等）或其它错误都只记 warn 事件，不重试：
// 卡池会自行回收到期租约，单据结果不受影响。单据所属服务商没有卡池时也只记 warn，绝不交给别家的卡池。
func (s *Service) release(ctx context.Context, j *job, l *cardpool.Lease, o cardpool.Outcome) {
	if l == nil {
		return
	}
	pool, ok := s.jobPool(j)
	if !ok {
		s.event(ctx, j, "warn", providerUnavailableText(j.o.Provider)+"，租约没有释放", "")
		return
	}
	err := pool.Release(ctx, l, o)
	switch {
	case err == nil:
	case errors.Is(err, cardpool.ErrLeaseInvalid):
		s.event(ctx, j, "warn", "租约已失效，卡已由卡池收回", "")
	default:
		s.event(ctx, j, "warn", "释放租约失败："+j.scrub(err.Error()), "")
	}
}

// run 依次执行步骤 2–4；试运行在读完金额后以 previewed 结束（试运行到填表多走一步填表，见 previewFill），真实付款单交给 pay。
// 第一步先确认单据所属服务商有卡池：建单时查过，重启后换了版本、这家被去掉时，这里还没取卡、没注资，放弃不动钱。
func (s *Service) run(ctx context.Context, j *job) error {
	if _, ok := s.jobPool(j); !ok {
		return abort("provider_unavailable", "%s，未取卡、未注资", providerUnavailableText(j.o.Provider))
	}
	if err := s.check(ctx, j); err != nil {
		return err
	}
	if err := s.createLink(ctx, j); err != nil {
		return err
	}
	if err := s.openPage(ctx, j); err != nil {
		return err
	}
	if j.o.DryRunFill {
		return s.previewFill(ctx, j)
	}
	if j.o.DryRun {
		return s.end(ctx, j, StatePreviewed, "info", "试运行完成，应付 "+j.o.AmountText, nil)
	}
	return s.pay(ctx, j)
}

// check 是步骤 2：解开会话、刷新 token、查账号。开通单：已有有效订阅则放弃（升级不在本流程）；点数单的条件见 checkCredit。
func (s *Service) check(ctx context.Context, j *job) error {
	sess, err := openSession(j.o.SessionSealed)
	if errors.Is(err, chatgpt.ErrBadSession) {
		return abort("session_invalid", "%v", err)
	}
	if err != nil {
		return abort("session_unreadable", "会话密文无法解开（主密钥缺失或已更换）")
	}
	j.useSession(sess)
	proxies, err := LoadProxies()
	if err != nil {
		return abort("proxy_unavailable", "读取地区代理失败：%v", err)
	}
	j.proxy = proxies[j.o.Region]
	if j.gpt, err = s.opts.ChatGPT(j.proxy); err != nil {
		return abort("chatgpt_failed", "创建 ChatGPT 客户端失败：%v", err)
	}
	if err := j.gpt.Refresh(ctx, &j.sess); err != nil {
		return s.chatgptAbort(j, "chatgpt_failed", "刷新会话失败", err)
	}
	acc, err := j.gpt.Account(ctx, j.sess)
	if err != nil {
		return s.chatgptAbort(j, "chatgpt_failed", "查询账号失败", err)
	}
	j.note("account_id", acc.AccountID)
	j.o.AccountID = acc.AccountID
	if j.o.Kind == KindCredit {
		if err := s.checkCredit(ctx, j, acc); err != nil {
			return err
		}
		s.snapshotCredit(ctx, j)
		return nil
	}
	if acc.HasActiveSubscription {
		return abort("already_subscribed", "账号已有有效订阅（%s），不能直接开通", acc.PlanType)
	}
	return nil
}

// createLink 是步骤 3：按套餐表创建支付会话，记下会话号与支付链接。点数单见 createCreditLink。
// 会话带链接时不管 tag 都用这个链接（托管页）；没有链接且 tag 是 custom_checkout_session 时，用 processor_entity 和会话号拼内嵌结账页链接，
// 拼不出来就在这里放弃。两样都没有时照旧报「未返回支付链接」。链接一律再过支付域名白名单（acceptCheckoutURL）。
func (s *Service) createLink(ctx context.Context, j *job) error {
	plan, rc, err := ResolvePlan(j.o.PlanKey, j.o.Region, j.o.DryRun)
	if errors.Is(err, ErrPlanUnavailable) {
		return abort("plan_unavailable", "%v", err)
	}
	if err != nil {
		return abort("internal_error", "读取套餐配置失败：%v", err)
	}
	j.floorMinor = rc.FloorMinor // 订阅单和点数单共用这一处
	if j.o.Kind == KindCredit {
		return s.createCreditLink(ctx, j)
	}
	co, err := j.gpt.CreateCheckout(ctx, j.sess, plan.ChatGPTPlanName, j.o.Region, j.o.Currency)
	if err != nil {
		return s.chatgptAbort(j, "checkout_failed", "创建支付会话失败", err)
	}
	j.note("checkout_session_id", co.SessionID)
	link := co.URL
	if strings.TrimSpace(link) == "" && strings.TrimSpace(co.Tag) == chatgpt.CheckoutTagCustom {
		if link, err = embeddedCheckoutURL(co.ProcessorEntity, co.SessionID); err != nil {
			return err
		}
	}
	return s.acceptCheckoutURL(ctx, j, co.SessionID, link)
}

// acceptCheckoutURL 先去掉链接首尾空白，再校验并推进到 link_created。链接为空记 checkout_failed；
// 不在支付域名白名单里（含点数单拿到内嵌结账页链接）记 checkout_url_untrusted，链接不落库、不打开。两种情况都还没取卡、没注资。
// 落库的是去掉空白后的链接：之后打开页面、判页面类型、确认「保存付款信息」看到的都是同一个链接。
func (s *Service) acceptCheckoutURL(ctx context.Context, j *job, sessionID, link string) error {
	link = strings.TrimSpace(link)
	if link == "" {
		return abort("checkout_failed", "创建支付会话未返回支付链接")
	}
	if err := s.checkOrderURL(j, link); err != nil {
		return err
	}
	if err := s.advance(ctx, j, StateLinkCreated, "已生成支付链接", map[string]any{"checkout_url": link}); err != nil {
		return err
	}
	j.o.CheckoutSessionID, j.o.CheckoutURL = sessionID, link
	return nil
}

// checkOrderURL 在支付域名白名单（s.checkURL）之上按单据类型收紧：点数单只走托管支付页，chatgpt.com 内嵌结账页一律不认，
// 记 checkout_url_untrusted，说明里只写域名。建链接和打开支付页之前都走这里。
func (s *Service) checkOrderURL(j *job, link string) error {
	if err := s.checkURL(link); err != nil {
		return err
	}
	if j.o.Kind == KindCredit && checkoutPageKind(link) == paybrowser.PageEmbedded {
		return abort("checkout_url_untrusted", "点数单只支持托管支付页，支付链接的域名 %s 不支持，已放弃，未注资", embeddedCheckoutHost)
	}
	return nil
}

// embeddedCheckoutHost 是内嵌结账页的主机名。
const embeddedCheckoutHost = "chatgpt.com"

// 内嵌结账页链接 /checkout/<processor_entity>/<会话号> 两段的格式。拼链接和白名单共用：两段都不可能带「/」「.」「?」「#」、空白或转义字符。
var (
	processorEntityRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	checkoutSessionRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)
)

// embeddedCheckoutURL 用建会话返回的 processor_entity 和会话号拼出内嵌结账页链接 https://chatgpt.com/checkout/<processor_entity>/<会话号>。
// 任一段不合格就返回 checkout_failed 的放弃原因，这时还没取卡、没注资、没启动浏览器。说明里不写会话号，也不写 processor_entity 原文。
func embeddedCheckoutURL(processorEntity, sessionID string) (string, error) {
	switch {
	case processorEntity == "":
		return "", abort("checkout_failed", "内嵌结账会话没有返回 processor_entity，拼不出支付链接，已放弃，未注资")
	case !processorEntityRe.MatchString(processorEntity):
		return "", abort("checkout_failed", "内嵌结账会话的 processor_entity 格式不合格，已放弃，未注资")
	case !checkoutSessionRe.MatchString(sessionID):
		return "", abort("checkout_failed", "内嵌结账会话的会话号格式不合格，已放弃，未注资")
	}
	return "https://" + embeddedCheckoutHost + "/checkout/" + processorEntity + "/" + sessionID, nil
}

// trustedCheckoutHosts 是付款引擎允许打开的支付页域名：OpenAI 的支付域名、Stripe 托管结账页和 chatgpt.com 内嵌结账页。
// 只认这三个主机名本身，子域名、相似域名一律不认。
// pay.openai.com、checkout.stripe.com 不限路径；chatgpt.com 只放行 /checkout/<processor_entity>/<会话号>：
// 两段分别匹配 processorEntityRe、checkoutSessionRe，路径里没有转义字符，不带查询串和 # 片段（连空的「?」「#」也不行），见 embeddedCheckoutLink。
// 这份名单只管付款；免付款探测工具另有一份更宽的名单，只用于报告，两份互不复用。
var trustedCheckoutHosts = map[string]bool{
	"pay.openai.com":      true,
	"checkout.stripe.com": true,
	embeddedCheckoutHost:  true,
}

// checkCheckoutURL 校验支付链接（先去首尾空白）：必须是 https、没有账号密码、没有端口，主机名只含 ASCII 字符、正好在 trustedCheckoutHosts 里；
// 主机名是 chatgpt.com 时另外要求连空端口（「chatgpt.com:」）也没有，并且是标准的内嵌结账页链接（embeddedCheckoutLink）。
// 不通过时返回 checkout_url_untrusted 的放弃原因，说明里只写域名，绝不写完整链接（链接里带会话号）。
func checkCheckoutURL(raw string) error {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return abort("checkout_url_untrusted", "支付链接无法解析出域名，已放弃，未注资")
	}
	// 转小写之前先拒绝非 ASCII 主机名：strings.ToLower 会把 İ（U+0130）折成 i、开尔文符号（U+212A）折成 k，
	// 「pay.openaİ.com」转小写后和白名单一样，浏览器做国际化域名转换后去的却是别的域名。百分号编码的主机名解码后同样在这里拦下
	if !isASCII(u.Hostname()) {
		return abort("checkout_url_untrusted", "支付链接的域名 %+q 含非 ASCII 字符，已放弃，未注资", u.Hostname())
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case u.Scheme != "https":
		return abort("checkout_url_untrusted", "支付链接不是 https（域名 %s），已放弃，未注资", host)
	case u.User != nil:
		return abort("checkout_url_untrusted", "支付链接带账号信息（域名 %s），已放弃，未注资", host)
	case u.Port() != "":
		return abort("checkout_url_untrusted", "支付链接带端口（域名 %s），已放弃，未注资", host)
	case !trustedCheckoutHosts[host]:
		return abort("checkout_url_untrusted", "支付链接的域名 %s 不在支付域名白名单里，已放弃，未注资", host)
	case host == embeddedCheckoutHost && u.Host != u.Hostname():
		return abort("checkout_url_untrusted", "支付链接带端口（域名 %s），已放弃，未注资", host)
	case host == embeddedCheckoutHost && !embeddedCheckoutLink(u, raw):
		return abort("checkout_url_untrusted", "支付链接不是标准的内嵌结账页链接：路径不对，或带了查询串、片段（域名 %s），已放弃，未注资", host)
	}
	return nil
}

// isASCII 判断字符串是否只含 ASCII 字符。
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// embeddedCheckoutLink 判断 chatgpt.com 的链接是不是标准的内嵌结账页：没有查询串和片段（raw 里出现「?」「#」就不算，空的也一样），
// 路径里没有转义字符（RawPath 为空），路径按「/」切开正好是 ["", "checkout", processor_entity, 会话号]，两段都匹配对应的正则。
func embeddedCheckoutLink(u *url.URL, raw string) bool {
	if u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "?#") || u.RawPath != "" {
		return false
	}
	parts := strings.Split(u.Path, "/")
	return len(parts) == 4 && parts[0] == "" && parts[1] == "checkout" &&
		processorEntityRe.MatchString(parts[2]) && checkoutSessionRe.MatchString(parts[3])
}

// openPage 是步骤 4：启动浏览器（走地区代理）、注入会话 cookie、打开支付页、截整页图、读应付金额；
// 打不开（停在 Cloudflare 挑战页记 cf_challenge 并告警）、读不出，或真实付款超过上限、低于最低金额也都在注资前放弃。
// pageRenderWait 是打开支付页后、截图前最多等页面显示完整的时间。Stripe 先放金额再淡入，通常一两秒内完成。
const pageRenderWait = 10 * time.Second

func (s *Service) openPage(ctx context.Context, j *job) error {
	if err := s.advance(ctx, j, StateOpening, "启动浏览器，打开支付页", nil); err != nil {
		return err
	}
	b, err := s.opts.Browser(ctx, j.proxy, filepath.Join(s.opts.ShotDir, j.o.OrderNo))
	if err != nil {
		msg := j.scrub(err.Error())
		s.opts.Alert(fmt.Sprintf("付款单 %s 浏览器无法启动：%s", j.o.OrderNo, msg))
		return abort("browser_failed", "浏览器无法启动：%s", msg)
	}
	j.browser = b
	if j.o.DryRun {
		b.LockSubmit() // 试运行单：浏览器对象本身拒绝点击，不只靠流程分支
	}
	// 建链接时已经校验过；打开之前再拦一次，不让任何别的路径带进来的链接被打开
	if err := s.checkOrderURL(j, j.o.CheckoutURL); err != nil {
		return err
	}

	pctx, cancel := context.WithTimeout(ctx, s.t.PageLoad)
	defer cancel()
	if err := b.Open(pctx, j.o.CheckoutURL, checkoutPageKind(j.o.CheckoutURL), sessionCookies(j.sess)); err != nil {
		s.notePageDiag(ctx, j, b, "打开支付页失败")
		sctx, cancelShot := context.WithTimeout(ctx, diagTimeout)
		defer cancelShot()
		s.shot(sctx, j, "open-failed", paybrowser.ShotPage) // 还没填卡，允许截整页；pctx 可能已经超时
		if errors.Is(err, paybrowser.ErrCFChallenge) {
			// 和建会话被拦（chatgptAbort）一样：换会话没有用，要查该地区代理的出口 IP，所以单独一个错误码并告警
			s.opts.Alert(fmt.Sprintf("付款单 %s（地区 %s）打开支付页被 Cloudflare 拦截，请检查该地区代理的出口 IP；换会话没有用。", j.o.OrderNo, j.o.Region))
			return abort("cf_challenge", "打开支付页被 Cloudflare 拦截，请检查该地区代理的出口 IP")
		}
		return abort("page_failed", "打开支付页失败：%v", err)
	}
	if !b.WaitRendered(pctx, pageRenderWait) {
		s.event(ctx, j, "warn", fmt.Sprintf("支付页 %d 秒内没有显示完整，下面的截图可能是加载中的画面", int(pageRenderWait/time.Second)), "")
	}
	s.shot(pctx, j, "opening", paybrowser.ShotPage)
	minor, text, err := b.ReadAmount(pctx, j.o.Currency)
	if text != "" {
		j.note("amount_text", text)
	}
	if err != nil {
		return abort("amount_unreadable", "读不出应付金额：%v", err)
	}
	if !paybrowser.CurrencyMatches(text, j.o.Currency) {
		return abort("currency_mismatch", "支付页金额 %q 的币种与期望的 %s 不符", text, j.o.Currency)
	}
	j.o.AmountMinor, j.o.AmountText = minor, text
	j.note("amount_minor", minor)
	if !j.o.DryRun && minor > j.o.CapMinor {
		return abort("amount_over_cap", "应付金额 %s 超过上限 %s %s", text, formatMinor(j.o.CapMinor, j.o.Currency), j.o.Currency)
	}
	// 下限：偏低多半是选错了档位或带了优惠。必须在任何注资动作之前判
	if j.o.DryRun {
		if j.floorMinor <= 0 {
			s.event(ctx, j, "info", "未配置最低金额，跳过下限检查", "")
		} else if minor < j.floorMinor {
			s.event(ctx, j, "warn", fmt.Sprintf("支付页金额 %s 低于最低金额 %s %s（可能选错了档位或带了优惠）；真实付款会在此放弃", text, formatMinor(j.floorMinor, j.o.Currency), j.o.Currency), "")
		}
	} else if j.floorMinor <= 0 {
		// 真实单不允许没有最低金额：ResolvePlan 已兜底，这里防以后重构漏赋值时下限被悄悄跳过
		return abort("plan_unavailable", "套餐 %s 在地区 %s 未配置最低金额，已放弃，未注资", j.o.PlanKey, j.o.Region)
	} else if minor < j.floorMinor {
		return abort("amount_below_floor", "支付页金额 %s 低于该套餐在该地区的最低金额 %s %s（可能选错了档位或带了优惠），已放弃，未注资", text, formatMinor(j.floorMinor, j.o.Currency), j.o.Currency)
	}
	return nil
}

// sessionCookies 把会话转成注入 chatgpt.com 的 cookie。
func sessionCookies(sess chatgpt.Session) []paybrowser.Cookie {
	cs := chatgpt.SessionCookies(sess.SessionToken)
	out := make([]paybrowser.Cookie, 0, len(cs))
	for _, c := range cs {
		out = append(out, paybrowser.Cookie{Name: c.Name, Value: c.Value, Domain: sessionDomain})
	}
	return out
}

// formatMinor 按币种小数位把最小单位金额格式化为数字文本，如 120000 PHP → "1200.00"。
func formatMinor(v int64, currency string) string {
	exp := paybrowser.CurrencyExponent(currency)
	if exp == 0 {
		return strconv.FormatInt(v, 10)
	}
	div := int64(math.Pow10(exp))
	return fmt.Sprintf("%d.%0*d", v/div, exp, v%div)
}

// checkoutPageKind 按链接域名决定页面类型：主机名是 chatgpt.com 为内嵌页，其余（含解析失败）按托管页。
// 和 checkCheckoutURL 一样先去首尾空白，否则过得了白名单的「 https://chatgpt.com/…」会被误判成托管页。
func checkoutPageKind(raw string) paybrowser.PageKind {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || strings.ToLower(u.Hostname()) != embeddedCheckoutHost {
		return paybrowser.PageHosted
	}
	return paybrowser.PageEmbedded
}
