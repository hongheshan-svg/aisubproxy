package paybrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// Cookie 是打开支付页前注入的 cookie。Path 固定为 /，并按原型 lib/session.js 设为 Secure、HttpOnly、SameSite=Lax。
type Cookie struct{ Name, Value, Domain string }

// Card 是要填入的卡片。ExpMonth、ExpYear 都是两位数字。
type Card struct{ Number, ExpMonth, ExpYear, CVC string }

// Billing 是账单地址，Country 为两位国家代码。某项为空时不填（有些国家的地址表单没有州或邮编）。
// Email 是账号自己的邮箱，只有页面上的邮箱框可见、必填且为空时才填，绝不写进日志或事件。
type Billing struct{ Name, Line1, City, State, Zip, Country, Email string }

// Outcome 是提交后页面给出的结果。
type Outcome string

const (
	OutcomeSuccess      Outcome = "success"
	OutcomeDeclined     Outcome = "declined"
	OutcomeFormRejected Outcome = "form_rejected" // 页面提示是表单校验错误，这笔付款多半没有发到发卡行
	OutcomeChallenge    Outcome = "challenge"     // 出现 3DS 验证框，需要验证码
	OutcomeUnknown      Outcome = "unknown"       // 等到超时，页面没有给出可识别的结果
	// OutcomeNotice 只用于给提示文字分类：认不出、或者只是过程提示。WaitOutcome 不返回它，记下后继续等
	OutcomeNotice Outcome = "notice"
)

// ShotScope 是截图范围。
type ShotScope int

const (
	ShotPage      ShotScope = iota // 当前可视区域；开始填卡后禁止
	ShotChallenge                  // 只截 3DS 验证框元素
)

var (
	// ErrAlreadySubmitted 表示已经点击过支付；同一页面绝不再次点击。
	ErrAlreadySubmitted = errors.New("已经点击过支付，不能再次提交")
	// ErrShotForbidden 表示页面上可能有卡号，禁止整页截图。
	ErrShotForbidden = errors.New("已开始填卡，禁止整页截图")
	// ErrSubmitLocked 表示调用过 LockSubmit；这个浏览器再也不会点支付。
	ErrSubmitLocked = errors.New("支付已锁定，不能点击")
)

const (
	pollInterval = 250 * time.Millisecond

	formWait     = 30 * time.Second       // 等卡号框出现（含点开「新卡」入口）
	keyDelay     = 35 * time.Millisecond  // 卡号类字段逐字键入的间隔
	slowKeyDelay = 70 * time.Millisecond  // 文本核对不符时逐字重打的间隔
	settleDelay  = 200 * time.Millisecond // 按 Esc 后等地址联想收起再核对
	reflowDelay  = 500 * time.Millisecond // 下拉框改选后（如国家）地址表单会重绘
	msgLimit     = 500                    // 页面提示文字最多保留的字数

	preClickScanWait = 2 * time.Second // 点击前收集已有提示的时限，卡住时当作没有提示，不耽误点击
	noticeLimit      = 20              // 一单最多记这么多条不同的未识别提示，防止页面刷屏
)

var (
	// pageLoadTimeout 是打开支付页、等页面就绪的时限。
	pageLoadTimeout = 60 * time.Second
	// fieldWait 是卡号框出现后，等其余各字段、支付按钮、验证码框的时限。
	fieldWait = 10 * time.Second
	// declineSettle 是判到被拒或校验错误后再看一会儿的时长：文字之后紧跟着 3DS 或成功的，以后者为准。
	declineSettle = 5 * time.Second
)

// 页面提示的分类词，都是小写，按「含有」匹配。真实拒付文字可能出现在任何错误容器里，所以只看文字、不看选择器。
// 这些词都没有在真实拒付页上核实过（设计文档 §12.7），所以宁可少认：认不出只是多等，认错了可能把已扣款的单定成失败。
var (
	// lost、has expired 不能单独用：「Connection lost」「Your session has expired」都不是拒付
	declineWords = []string{
		"declin", "insufficient", "do not honor", "try a different", "incorrect", "card has expired", "expired card",
		"stolen", "reported lost", "lost card", "fraud", "unable to process", "could not be processed",
	}
	// progressWords 是过程提示，要排在校验词前面：「Authentication required」含 required，但它是在等 3DS，不是校验错误
	progressWords = []string{
		"processing", "please wait", "authenticat", "verifying", "verification", "redirect", "confirming", "one moment",
	}
	formErrorWords = []string{"incomplete", "required", "invalid", "not valid", "please enter", "please fill", "in the past"}
)

// amountJS 按 Sel.Amount 的顺序找第一个含数字的元素，返回其文字（空白已归一）；找不到返回空串。
const amountJS = `(sels => {
	for (const s of sels) {
		let el
		try { el = document.querySelector(s) } catch (e) { continue }
		if (!el) continue
		const t = (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim()
		if (/\d/.test(t)) return t
	}
	return ''
})`

// PageKind 是支付页的类型，由引擎按链接域名决定。
type PageKind int

const (
	PageHosted   PageKind = iota // Stripe 托管页
	PageEmbedded                 // chatgpt.com 内嵌结账页
)

// pageKind 返回 Open 时记下的页面类型。
func (b *Browser) pageKind() PageKind {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.kind
}

// Open 先注入 cookie 再打开支付页，等到页面就绪为止，最多 pageLoadTimeout（60 秒）。
// kind 记在 Browser 上，之后各步按页面类型分支。托管页等金额元素出现；
// 内嵌页等卡号框、金额读屏文字和唯一的支付按钮，等不到时认出 Cloudflare 挑战页（见 waitEmbedded）。
func (b *Browser) Open(ctx context.Context, url string, kind PageKind, cookies []Cookie) error {
	b.mu.Lock()
	b.kind = kind
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, pageLoadTimeout)
	defer cancel()

	for _, c := range cookies {
		set := network.SetCookie(c.Name, c.Value).WithDomain(c.Domain).WithPath("/").
			WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteLax)
		if err := b.run(ctx, set); err != nil {
			return fmt.Errorf("注入 cookie %s 失败: %w", c.Name, err)
		}
	}

	var errText string
	err := b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		_, _, errText, _, err = page.Navigate(url).Do(ctx)
		return err
	}))
	if err == nil && errText != "" {
		err = errors.New(errText)
	}
	if err != nil {
		return openErr("打开支付页失败", err)
	}
	if kind == PageEmbedded {
		return b.waitEmbedded(ctx)
	}

	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		if text, _ := b.amountText(ctx); text != "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return openErr("支付页上始终没有出现金额", ctx.Err())
		case <-tick.C:
		}
	}
}

// renderedJS 判断金额是否已真正显示：按 Sel.Amount 找到含数字的金额元素，要求它有尺寸、
// 自身和所有上级都可见且完全不透明、没有被别的元素盖住（在视口内时检查），上级没有正在跑的动画。
const renderedJS = `(sels => {
	let el = null
	for (const s of sels) {
		let c
		try { c = document.querySelector(s) } catch (e) { continue }
		if (c && /\d/.test(c.innerText || c.textContent || '')) { el = c; break }
	}
	if (!el) return false
	const r = el.getBoundingClientRect()
	if (r.width === 0 || r.height === 0) return false
	for (let n = el; n && n.nodeType === 1; n = n.parentElement) {
		const cs = getComputedStyle(n)
		if (cs.display === 'none' || cs.visibility === 'hidden' || parseFloat(cs.opacity) < 0.99) return false
	}
	if (r.bottom > 0 && r.top < innerHeight && r.right > 0 && r.left < innerWidth) {
		const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
		if (!top || !(el.contains(top) || top.contains(el))) return false
	}
	if (document.getAnimations) {
		for (const a of document.getAnimations()) {
			const t = a.effect && a.effect.target
			if (a.playState === 'running' && t && t.contains && t.contains(el)) return false
		}
	}
	return true
})`

func (b *Browser) amountRendered(ctx context.Context) (bool, error) {
	sels, err := json.Marshal(Sel.Amount)
	if err != nil {
		return false, err
	}
	var ok bool
	err = b.run(ctx, chromedp.Evaluate(fmt.Sprintf("%s(%s)", renderedJS, sels), &ok))
	return ok, err
}

// WaitRendered 等支付页真正显示出来：金额可见、所在区域完全不透明、没有被遮挡、淡入动画已结束。
// Stripe 会先把金额放进 DOM、盖着加载骨架再淡入，Open 返回时画面上可能还是骨架，这时截图没有用。
// 最多等 max；等不到返回 false 而不报错，因为这只影响截图，不能因此中断付款。只读页面，不点击、不输入。
// 内嵌页的金额是隐藏的读屏文字，改为等卡号框可见。
func (b *Browser) WaitRendered(ctx context.Context, max time.Duration) bool {
	if b.pageKind() == PageEmbedded {
		return b.waitCardVisible(ctx, max)
	}
	ctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		if ok, _ := b.amountRendered(ctx); ok {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

func openErr(what string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s：页面加载超时（%d 秒）", what, int(pageLoadTimeout/time.Second))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// ReadAmount 读取支付页汇总区的应付总额，返回最小单位金额与页面原文。
// 内嵌页读 Payment amount 读屏文字，返回规范化原文（如 "100.00 USD"，见 normalizeEmbeddedAmount）。
func (b *Browser) ReadAmount(ctx context.Context, currency string) (minor int64, text string, err error) {
	if b.pageKind() == PageEmbedded {
		return b.readEmbeddedAmount(ctx, currency)
	}
	text, err = b.amountText(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("读取金额失败: %w", err)
	}
	if text == "" {
		return 0, "", errors.New("支付页上没有找到金额")
	}
	minor, err = ParseAmount(text, currency)
	if err != nil {
		return 0, text, err
	}
	return minor, text, nil
}

// amountText 在主页面按 Sel.Amount 取一次金额原文。
func (b *Browser) amountText(ctx context.Context) (string, error) {
	sels, err := json.Marshal(Sel.Amount)
	if err != nil {
		return "", err
	}
	var text string
	err = b.run(ctx, chromedp.Evaluate(fmt.Sprintf("%s(%s)", amountJS, sels), &text))
	return text, err
}

// ---- 填卡、提交、3DS 与截图 ----

// fieldKind 决定字段的填法。
type fieldKind int

const (
	kindCard   fieldKind = iota // 卡号类：逐字键入，按数字核对
	kindText                    // 文本：整段输入后按 Esc，核对不符再逐字重打
	kindChoice                  // 下拉框按选项值或文字选择；是输入框时按文本填
)

// field 是一个待填字段。label 是中文字段名，错误消息里只出现它，绝不出现值。
type field struct {
	label string
	sels  []string
	value string
	kind  fieldKind
}

// validate 在碰页面之前检查卡片格式。错误消息不含值。
func (c Card) validate() error {
	month, _ := strconv.Atoi(c.ExpMonth)
	switch {
	case !isDigits(c.Number, 12, 19):
		return errors.New("卡号格式无效")
	case !isDigits(c.ExpMonth, 2, 2) || month < 1 || month > 12:
		return errors.New("有效期月份须为两位数字（01～12）")
	case !isDigits(c.ExpYear, 2, 2):
		return errors.New("有效期年份须为两位数字")
	case !isDigits(c.CVC, 3, 4):
		return errors.New("CVC 格式无效")
	}
	return nil
}

// validate 在碰页面之前检查账单地址：任何字段含控制字符都拒绝。逐字键入会把 \n、\r 当回车发出去，
// 回车在 Stripe 输入框里可能触发提交。错误消息只含中文字段名，不含值。
func (bl Billing) validate() error {
	for _, f := range []struct{ label, value string }{
		{"姓名", bl.Name}, {"街道地址", bl.Line1}, {"城市", bl.City}, {"州/省", bl.State},
		{"邮编", bl.Zip}, {"国家", bl.Country}, {"邮箱", bl.Email},
	} {
		if strings.IndexFunc(f.value, unicode.IsControl) >= 0 {
			return fmt.Errorf("账单地址含控制字符（%s）", f.label)
		}
	}
	return nil
}

func isDigits(s string, lo, hi int) bool {
	if len(s) < lo || len(s) > hi {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// fillField 按字段类型填写并核对。选中输入框用 focusField：内嵌页上点之前先看落点。
// changed 表示这是下拉框，而且值真的改了（chooseOption）：页面会重绘地址表单，调用方给下一个字段留足等待。
func (b *Browser) fillField(ctx context.Context, e *elem, f field) (changed bool, err error) {
	if f.kind == kindChoice {
		var tag string
		if err := callOn(ctx, e.obj, `function () { return this.tagName }`, &tag); err != nil {
			return false, fmt.Errorf("读取「%s」失败: %w", f.label, err)
		}
		if tag == "SELECT" {
			return chooseOption(ctx, e, f)
		}
		f.kind = kindText
	}
	direct, err := b.focusField(ctx, e)
	if err != nil {
		return false, fmt.Errorf("无法选中「%s」输入框: %w", f.label, err)
	}
	b.noteDirectFocus(f.label, direct)
	if f.kind == kindCard {
		return false, typeCard(ctx, e, f)
	}
	return false, typeText(ctx, e, f)
}

// typeCard 清空后逐字键入，再按数字核对（Stripe 会给卡号加空格、给有效期加斜杠）。
func typeCard(ctx context.Context, e *elem, f field) error {
	if err := clearInput(ctx, e); err != nil {
		return fmt.Errorf("清空「%s」失败: %w", f.label, err)
	}
	if err := typeKeys(ctx, f.value, keyDelay); err != nil {
		return fmt.Errorf("键入「%s」失败: %w", f.label, err)
	}
	got, err := inputValue(ctx, e)
	if err != nil {
		return fmt.Errorf("核对「%s」失败: %w", f.label, err)
	}
	if digitsOf(got) != f.value {
		return fmt.Errorf("「%s」填写后核对不一致", f.label)
	}
	return nil
}

// typeText 整段输入后按 Esc 关掉地址联想，再核对；被截断或改写时清空后逐字慢打一遍。
func typeText(ctx context.Context, e *elem, f field) error {
	attempt := func(slow bool) (bool, error) {
		if err := clearInput(ctx, e); err != nil {
			return false, err
		}
		var err error
		if slow {
			err = typeKeys(ctx, f.value, slowKeyDelay)
		} else {
			err = input.InsertText(f.value).Do(ctx)
		}
		if err != nil {
			return false, err
		}
		if err := chromedp.KeyEvent(kb.Escape).Do(ctx); err != nil {
			return false, err
		}
		if err := sleep(ctx, settleDelay); err != nil {
			return false, err
		}
		got, err := inputValue(ctx, e)
		return err == nil && normText(got) == normText(f.value), err
	}
	ok, err := attempt(false)
	if err == nil && !ok {
		ok, err = attempt(true)
	}
	if err != nil {
		return fmt.Errorf("填写「%s」失败: %w", f.label, err)
	}
	if !ok {
		return fmt.Errorf("「%s」填写后核对不一致", f.label)
	}
	return nil
}

// chooseOptionJS 按选项值（不分大小写）或选项文字选中，并触发 input、change 事件。
const chooseOptionJS = `function (want) {
	want = String(want).trim().toLowerCase()
	const opts = Array.from(this.options)
	const o = opts.find(o => o.value.toLowerCase() === want) || opts.find(o => o.textContent.trim().toLowerCase() === want)
	if (!o) return 'missing'
	if (this.value === o.value) return 'same'
	this.value = o.value
	this.dispatchEvent(new Event('input', { bubbles: true }))
	this.dispatchEvent(new Event('change', { bubbles: true }))
	return this.value === o.value ? 'changed' : 'rejected'
}`

// chooseOption 在下拉框里选 f.value。值真的改了时 changed 为 true，并先等 reflowDelay 让页面开始重绘；
// 重绘可能比这更慢（终审 M4），调用方要给下一个字段留足等待（见 fillEmbeddedAddress）。
func chooseOption(ctx context.Context, e *elem, f field) (changed bool, err error) {
	var r string
	if err := callOn(ctx, e.obj, chooseOptionJS, &r, f.value); err != nil {
		return false, fmt.Errorf("选择「%s」失败: %w", f.label, err)
	}
	switch r {
	case "missing":
		return false, fmt.Errorf("「%s」下拉框里没有 %s 这一项", f.label, f.value)
	case "rejected":
		return false, fmt.Errorf("「%s」选择后核对不一致", f.label)
	case "changed":
		return true, sleep(ctx, reflowDelay)
	}
	return false, nil
}

// payEnabledJS 判断支付按钮能不能点。
const payEnabledJS = `function () { return !this.disabled && this.getAttribute('aria-disabled') !== 'true' }`

// LockSubmit 锁定提交，返回此前是否开始过点击。调用之后 Submit 只会返回错误，绝不点击。
//
// 判断和锁定在同一把锁里完成，所以返回 false 严格证明：这个浏览器从未点击支付，以后也不会。
// 只有这种情况才能记「未提交」。标志在点击之前置位，所以返回 true 不代表真的点到了，只代表不能排除，必须走确认。
func (b *Browser) LockSubmit() (clickAttempted bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.submitLocked = true
	return b.submitted
}

// Submit 点击主页面上的支付按钮。必须先 Fill 成功；先记下「已提交」再点击，此后再调用返回 ErrAlreadySubmitted 且不点击。
// 内嵌页（设计 §5.8）只在主页面仍在结账页时点唯一可见、可用的提交按钮（见 embeddedPayButton）：找到后先把焦点移到主页面、
// 等按钮重新可用（readyAfterFocus），点击前一刻再核对一次，最后在同一次脚本里查可点、焦点、落点（payClickPoint），
// 任何一步不过就不点、不置位、返回错误；全部通过才置位，并点刚检查过的坐标。
func (b *Browser) Submit(ctx context.Context) error {
	b.mu.Lock()
	submitted, filled, locked := b.submitted, b.filled, b.submitLocked
	b.mu.Unlock()
	if submitted {
		return ErrAlreadySubmitted
	}
	if locked {
		return ErrSubmitLocked
	}
	if !filled {
		return errors.New("尚未填卡，不能提交")
	}
	embedded := b.pageKind() == PageEmbedded
	return b.do(ctx, func(ctx context.Context) error {
		seen := false
		n := payButtonUnreadable // 内嵌页：最后一次看到的主页面按钮状态（见 embeddedPayButton）
		btn, err := b.waitFind(ctx, fieldWait, func(frames []frameRef) *elem {
			if embedded { // 内嵌页的按钮只能按个数认：仍在结账页、主页面可见的提交按钮正好一个且可用才算找到
				if n == payButtonLeft { // 看到过离开结账页，此后绝不再认按钮，等到超时报错
					return nil
				}
				return b.embeddedPayButton(ctx, frames[0], &n)
			}
			// 只在主页面找：主页面按钮暂时不可见时，绝不能点到某个 iframe 里的 submit 按钮，白白用掉唯一一次提交
			e := b.find(ctx, frames[:1], Sel.PayButton, false)
			if e == nil {
				return nil
			}
			seen = true
			var enabled bool
			if callOn(ctx, e.obj, payEnabledJS, &enabled) != nil || !enabled {
				return nil
			}
			return e
		})
		if err != nil {
			return err
		}
		if embedded && btn == nil {
			return embeddedPayButtonErr(n)
		}
		if embedded {
			// 先把焦点移出 Stripe 的 iframe、等页面反应完，再等按钮重新可用：不然按下鼠标那一刻 iframe 才失焦，
			// 页面要是因此把按钮短暂设成不可用，松开时点击就丢了，而 submitted 已经置位
			if btn, _, err = b.readyAfterFocus(ctx, btn, &n); err != nil {
				return fmt.Errorf("%w，没有点击", err)
			}
			if btn == nil {
				return embeddedPayButtonErr(n)
			}
		}
		if btn == nil {
			if seen {
				return errors.New("支付按钮一直不可点击（可能有字段未通过页面校验）")
			}
			return errors.New("找不到支付按钮")
		}
		before := b.scanBeforeClick(ctx)
		pay := click
		if embedded {
			if b.beforeRecheck != nil {
				if err := b.beforeRecheck(ctx); err != nil {
					return err
				}
			}
			// 点击那一刻再核对一次：scanBeforeClick 最多要 2 秒，这期间页面可能离开结账页、重绘出第二个按钮或换掉按钮。
			// 只认此刻仍在结账页、主页面上唯一可见、可用的那个，点它；否则不置位、不点击，LockSubmit 仍能证明没点
			if btn = b.embeddedPayButton(ctx, btn.frame, &n); btn == nil {
				return embeddedPayButtonErr(n)
			}
			// 最后在同一次脚本里查：按钮可点、主页面焦点不在 iframe 上、中心那一点是按钮自己（没被地址联想下拉、横幅、弹层盖住）。
			// 不过就不点，同样不置位：置位之后只要点了就绝不再点第二次，点到盖着的东西上，这一单就只能走确认、回收。
			// 通过了就点刚检查过的这个坐标，检查和点击之间不再有等待，也不再重新滚动、求中心
			x, y, lerr := b.payClickPoint(ctx, btn)
			if lerr != nil {
				return payCheckErr(lerr)
			}
			pay = func(ctx context.Context, _ *elem) error { return chromedp.MouseClickXY(x, y).Do(ctx) }
		}
		// 先记下已提交再点击：点击中途出错也绝不再点第二次。从这里开始，任何错误都不能当成「没点击」（见 LockSubmit）
		b.mu.Lock()
		if b.submitted {
			b.mu.Unlock()
			return ErrAlreadySubmitted
		}
		if b.submitLocked {
			b.mu.Unlock()
			return ErrSubmitLocked
		}
		b.submitted = true
		b.beforeClick = before
		b.mu.Unlock()
		if b.clickPay != nil {
			pay = b.clickPay
		}
		if err := pay(ctx, btn); err != nil {
			return fmt.Errorf("点击支付按钮失败: %w", err)
		}
		return nil
	})
}

// WaitOutcome 等提交后的页面结果，返回结果与页面提示文字（最多 500 字）。
// 认不出的提示只记下（见 Notices）、继续等。到 timeout 仍无可识别的结果时返回 OutcomeUnknown 和最后一条这样的提示、不报错；
// 父 ctx 取消时返回取消错误。已填过验证码的 3DS 框不再算作新的验证。
// timeout 约束其中所有 CDP 调用：poll 只在两轮之间看时间，而页面脚本死循环时单次调用根本不会返回。
// 内嵌页每轮先读主页面路径，不再以 /checkout/ 开头时立即返回 OutcomeUnknown 和「页面已离开结账页」。
func (b *Browser) WaitOutcome(ctx context.Context, timeout time.Duration) (Outcome, string, error) {
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, msg := OutcomeUnknown, ""
	embedded, left := b.pageKind() == PageEmbedded, false
	err := b.do(wctx, func(c context.Context) error {
		_, err := b.poll(c, timeout, func(frames []frameRef) bool {
			if embedded && b.leftCheckout(c, frames[0]) { // 内嵌页每轮先看主页面还在不在结账页
				left = true
				return true
			}
			out, msg = b.outcome(c, frames)
			return out != OutcomeUnknown
		})
		if err == nil && (out == OutcomeDeclined || out == OutcomeFormRejected) {
			out, msg = b.settleOutcome(c, out, msg)
		}
		return err
	})
	if left { // 离开结账页不新增结果类型：返回 unknown，由引擎转确认阶段查账号定论
		return OutcomeUnknown, leftCheckoutMsg, nil
	}
	switch {
	case out != OutcomeUnknown:
		return out, msg, nil
	case ctx.Err() != nil:
		return OutcomeUnknown, "", fmt.Errorf("等待支付结果失败: %w", ctx.Err())
	case err == nil || wctx.Err() != nil:
		return OutcomeUnknown, b.lastNoticeText(), nil
	}
	return OutcomeUnknown, "", fmt.Errorf("等待支付结果失败: %w", err)
}

// settleOutcome 判到被拒或校验错误后再看 declineSettle：文字之后紧跟着出现 3DS 或成功的，以后者为准，
// 否则会关掉浏览器，3DS 就走不下去了。校验错误之后又出现拒付文字的，按被拒。
func (b *Browser) settleOutcome(ctx context.Context, out Outcome, msg string) (Outcome, string) {
	sctx, cancel := context.WithTimeout(ctx, declineSettle)
	defer cancel()
	_, _ = b.poll(sctx, declineSettle, func(frames []frameRef) bool {
		o, m := b.outcome(sctx, frames)
		switch {
		case o == OutcomeChallenge || o == OutcomeSuccess:
			out, msg = o, m
			return true
		case o == OutcomeDeclined && out == OutcomeFormRejected:
			out, msg = o, m
		}
		return false
	})
	return out, msg
}

// outcome 检查一轮页面：成功优先，其次是未处理过的 3DS 框，最后才看点击后才出现的提示
// （全部看一遍，有一条算被拒就是被拒，其次算校验错误，其余只记下、继续等）。
// 3DS 要先于提示：页面可能同时显示过程提示，提示判成结果就会关掉浏览器，验证走不下去。
func (b *Browser) outcome(ctx context.Context, frames []frameRef) (Outcome, string) {
	if e := b.find(ctx, frames, Sel.Success, false); e != nil {
		return OutcomeSuccess, truncateMessage(innerText(ctx, e))
	}
	if e := b.find(ctx, frames, Sel.ChallengeFrame, false); e != nil {
		if id, err := contentFrame(ctx, e); err == nil && !b.isAnswered(id) {
			return OutcomeChallenge, ""
		}
	}
	b.mu.Lock()
	before := b.beforeClick // 赋值之后不再修改，出锁后只读
	b.mu.Unlock()
	formError := ""
	for _, t := range b.visibleTexts(ctx, frames, Sel.DeclineText) {
		if before[t] {
			continue
		}
		switch classifyPageError(t) {
		case OutcomeDeclined:
			return OutcomeDeclined, truncateMessage(t)
		case OutcomeFormRejected:
			if formError == "" {
				formError = t
			}
		default:
			b.noteNotice(t)
		}
	}
	if formError != "" {
		return OutcomeFormRejected, truncateMessage(formError)
	}
	return OutcomeUnknown, ""
}

// classifyPageError 按文字判断页面提示的类别，不区分大小写：先认被拒，再认过程提示，再认表单校验错误，其余都是认不出（OutcomeNotice）。
// 被拒要先判断：同时含两类词的文字（如拒付原因里提到邮编无效）不能当成校验错误，否则会少算发卡行的拒付。
// 认不出的文字绝不能当结果：判成被拒只等 2 分钟就关浏览器，已扣款的单可能被定成失败，3DS 也走不下去；不认识的语言同理。
func classifyPageError(text string) Outcome {
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, declineWords):
		return OutcomeDeclined
	case containsAny(lower, progressWords):
		return OutcomeNotice
	case containsAny(lower, formErrorWords):
		return OutcomeFormRejected
	}
	return OutcomeNotice
}

// noteNotice 记下一条认不出的提示：脱敏、去重、限量。页面文字可能夹带邮箱、卡号，出了这里就要进时间线。
func (b *Browser) noteNotice(text string) {
	text = truncateMessage(maskPageText(text))
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastNotice = text
	if b.noticeSeen[text] || len(b.noticeSeen) >= noticeLimit {
		return
	}
	if b.noticeSeen == nil {
		b.noticeSeen = map[string]bool{}
	}
	b.noticeSeen[text] = true
	b.notices = append(b.notices, text)
}

func (b *Browser) lastNoticeText() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastNotice
}

// Notices 取走还没取过的未识别提示（已脱敏、去重、限量）。同一条文字一单只给一次。
func (b *Browser) Notices() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.notices
	b.notices = nil
	return out
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// scanBeforeClick 收集点击前各 frame 里已显示的提示文字，它们不是这次付款的结果。
// 有短超时，出错或超时就少收或不收：多算一条提示只会走确认流程，不能因此耽误提交。
// 这里绝不能释放对象句柄：调用方还拿着支付按钮。
func (b *Browser) scanBeforeClick(ctx context.Context) map[string]bool {
	ctx, cancel := context.WithTimeout(ctx, preClickScanWait)
	defer cancel()
	frames, err := b.frames(ctx)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, t := range b.visibleTexts(ctx, frames, Sel.DeclineText) {
		seen[t] = true
	}
	return seen
}

func (b *Browser) isAnswered(id cdp.FrameID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.answered[id]
}

// truncateMessage 去掉首尾空白，最多保留 500 字。
func truncateMessage(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= msgLimit {
		return s
	}
	return string([]rune(s)[:msgLimit])
}

// EnterOTP 在 3DS 验证框（含其中各级 iframe）里填验证码并提交。
// 验证码框优先按 Sel.OTPInput 找（首选 autocomplete="one-time-code"），否则取框内唯一可见的文本或数字输入框。
//
// 内嵌页（设计 §5.8，终审 I1）：验证框不一定是全屏遮罩，验证码框、提交按钮可能在验证框可视区外，按坐标点会点到主页面——
// 可能就是第二次点支付。所以选中验证码框用 focusField（过落点检查才用鼠标点，不过就直接设焦点），提交见 submitOTPEmbedded。
// 托管页不变。
//
// 验证码只接受 4–12 位数字或字母（终审二 Minor-1）：验证码是逐字发键的，夹着的 Tab 会把焦点移出验证码框，
// 之后的空格、回车可能落到别处的按钮上（包括主页面的支付按钮）。先删掉普通空格和横线（otpStrip，短信里常见的分组写法，
// 如「123 456」「12-34-56」），再查格式；格式不对就报错，一个键都不发，错误里不带验证码。只把删过之后的值打进页面。
// Tab、回车、换行、全角空格、不换行空格等不删，照旧拒绝：「1234\n」这种不替人猜。
func (b *Browser) EnterOTP(ctx context.Context, otp string) error {
	otp = otpStrip.Replace(otp)
	if !otpFormat.MatchString(otp) {
		return errors.New("验证码格式不对（删掉空格、横线后只接受 4–12 位数字或字母），没有填写")
	}
	embedded := b.pageKind() == PageEmbedded
	return b.do(ctx, func(ctx context.Context) error {
		var box cdp.FrameID
		var scope []frameRef
		var btn *elem
		in, err := b.waitFind(ctx, fieldWait, func(frames []frameRef) *elem {
			c := b.find(ctx, frames, Sel.ChallengeFrame, false)
			if c == nil {
				return nil
			}
			id, err := contentFrame(ctx, c)
			if err != nil {
				return nil
			}
			box, scope = id, subtree(frames, id)
			e := b.find(ctx, scope, Sel.OTPInput, false)
			if e == nil {
				e = b.onlyTextInput(ctx, scope)
			}
			if e != nil {
				btn = b.otpSubmit(ctx, e, scope)
			}
			return e
		})
		if err != nil {
			return err
		}
		if in == nil {
			return errors.New("在 3DS 验证框里找不到验证码输入框")
		}
		if embedded {
			if _, err := b.focusField(ctx, in); err != nil {
				return fmt.Errorf("无法选中验证码输入框: %w", err)
			}
		} else if err := focus(ctx, in); err != nil {
			return fmt.Errorf("无法选中验证码输入框: %w", err)
		}
		if err := clearInput(ctx, in); err != nil {
			return fmt.Errorf("清空验证码输入框失败: %w", err)
		}
		if err := typeKeys(ctx, otp, keyDelay); err != nil {
			return fmt.Errorf("键入验证码失败: %w", err)
		}

		// 键入前没有提交按钮的，键入后再找一次（有的页面填了验证码才出现）；仍没有就按回车
		if btn == nil {
			btn = b.otpSubmit(ctx, in, scope)
		}
		switch {
		case embedded:
			err = b.submitOTPEmbedded(ctx, box, in, btn)
		case btn == nil:
			if err = focus(ctx, in); err == nil {
				err = chromedp.KeyEvent(kb.Enter).Do(ctx)
			}
		default:
			err = click(ctx, btn)
		}
		if err != nil {
			return fmt.Errorf("提交验证码失败: %w", err)
		}
		// 提交动作成功后才记为已答；失败时不记，WaitOutcome 仍把这个框报为待验证，可以重新填
		b.mu.Lock()
		if b.answered == nil {
			b.answered = map[cdp.FrameID]bool{}
		}
		b.answered[box] = true
		b.mu.Unlock()
		return nil
	})
}

// submitOTPEmbedded 是内嵌页上提交验证码的那一步。box 是 3DS 验证框承载的 frame，in、btn 都在它的子树里找到（btn 可以是 nil）。
//   - 有提交按钮：按钮不能在主页面上、必须在验证框的子树里；过 landsInOwnFrame（主页面上那一点是装着它的 iframe），
//     再用 DOM.getNodeForLocation 看那一点最深处落在哪个 frame，也必须在验证框的子树里，才用鼠标点。
//     这里不做「不是提交控件」检查：验证框里的提交按钮本来就该点；它不在主页面上，点它不会是点支付。
//     任一不过就不点、返回原因（以「，没有点击」结尾），也不改按回车：落点不对时页面的样子没核实过，不赌。
//   - 没有提交按钮：回车只允许发在 3DS 验证框里（设计 §5.8 的唯一例外）——验证码框在验证框的子树里、
//     ensureFocus 确认焦点就在它上面，才按回车；否则不按、返回原因。
func (b *Browser) submitOTPEmbedded(ctx context.Context, box cdp.FrameID, in, btn *elem) error {
	frames, err := b.frames(ctx)
	if err != nil {
		return err
	}
	scope := subtree(frames, box)
	inBox := func(id cdp.FrameID) bool {
		return id != frames[0].id && slices.ContainsFunc(scope, func(f frameRef) bool { return f.id == id })
	}
	if btn == nil {
		if !inBox(in.frame.id) {
			return errors.New("验证码输入框不在验证框里，没有按回车")
		}
		if _, err := ensureFocus(ctx, in); err != nil {
			return fmt.Errorf("焦点不在验证码输入框上（%v），没有按回车", err)
		}
		return chromedp.KeyEvent(kb.Enter).Do(ctx)
	}
	if !inBox(btn.frame.id) {
		return errors.New("验证码提交按钮不在验证框里，没有点击")
	}
	x, y, err := b.landsInOwnFrame(ctx, frames, btn)
	if err != nil {
		return err
	}
	_, hit, _, err := dom.GetNodeForLocation(int64(math.Round(x)), int64(math.Round(y))).
		WithIncludeUserAgentShadowDOM(true).WithIgnorePointerEventsNone(false).Do(ctx)
	if err != nil {
		return fmt.Errorf("读不出点击落点（%v），没有点击", err)
	}
	if !inBox(hit) {
		return errors.New("点击落点不在验证框里，没有点击")
	}
	return chromedp.MouseClickXY(x, y).Do(ctx)
}

// otpSubmit 找验证码的提交按钮：先在验证码框所在的 frame 里找，再在整个验证框里找。
func (b *Browser) otpSubmit(ctx context.Context, in *elem, scope []frameRef) *elem {
	if btn := b.find(ctx, []frameRef{in.frame}, Sel.OTPSubmit, false); btn != nil {
		return btn
	}
	return b.find(ctx, scope, Sel.OTPSubmit, false)
}

// onlyTextInputJS 在本 frame 里找可见、可编辑的文本或数字输入框：恰好一个时返回它，多个返回 'many'，没有返回 null。
const onlyTextInputJS = `((visible) => {
	const list = Array.from(document.querySelectorAll('input')).filter(el => {
		const t = (el.getAttribute('type') || 'text').toLowerCase()
		return ['text', 'number', 'tel'].includes(t) && !el.disabled && !el.readOnly && visible(el)
	})
	return list.length === 1 ? list[0] : list.length > 1 ? 'many' : null
})`

// onlyTextInput 在 scope 各 frame 里合计只有一个可见文本或数字输入框时返回它。
func (b *Browser) onlyTextInput(ctx context.Context, scope []frameRef) *elem {
	var found *elem
	for _, f := range scope {
		r, err := b.eval(ctx, f, fmt.Sprintf("%s(%s)", onlyTextInputJS, visibleJS))
		if err != nil {
			continue
		}
		switch {
		case r.Type == runtime.TypeString:
			return nil // 本 frame 就有多个
		case r.ObjectID != "":
			if found != nil {
				return nil
			}
			found = &elem{frame: f, obj: r.ObjectID}
		}
	}
	return found
}

// otpFormat 是 EnterOTP 接受的验证码格式（删掉空格、横线之后）。Go 的 $ 在非多行模式下只匹配文本末尾，结尾带换行的不会被放过。
var otpFormat = regexp.MustCompile(`^[0-9A-Za-z]{4,12}$`)

// otpStrip 只删普通空格（U+0020）和横线（-）。不用 strings.Fields、unicode.IsSpace 之类：那会把 Tab、换行、全角空格也当空白删掉，
// 这些照旧要拒绝。
var otpStrip = strings.NewReplacer(" ", "", "-", "")

var shotName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// Shot 截图写到 ShotDir，返回文件名（不含目录）。
// ShotPage 截当前可视区域，开始填卡后一律返回 ErrShotForbidden；ShotChallenge 只截 3DS 验证框元素。
func (b *Browser) Shot(ctx context.Context, name string, scope ShotScope) (string, error) {
	name = strings.TrimSuffix(name, ".png")
	if !shotName.MatchString(name) {
		return "", errors.New("截图名只能含字母、数字、点、下划线与连字符，且不能以点开头")
	}
	if b.shotDir == "" {
		return "", errors.New("未设置截图目录")
	}
	var buf []byte
	var err error
	switch scope {
	case ShotPage:
		buf, err = b.shotPage(ctx)
	case ShotChallenge:
		buf, err = b.shotChallenge(ctx)
	default:
		return "", errors.New("未知的截图范围")
	}
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(b.shotDir, 0o700); err != nil {
		return "", fmt.Errorf("创建截图目录失败: %w", err)
	}
	file := name + ".png"
	if err := os.WriteFile(filepath.Join(b.shotDir, file), buf, 0o600); err != nil {
		return "", fmt.Errorf("写截图文件失败: %w", err)
	}
	return file, nil
}

// shotPage 截可视区域。截图期间持有 mu，Fill 要等截完才开始键入卡号。
func (b *Browser) shotPage(ctx context.Context) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cardTouched {
		return nil, ErrShotForbidden
	}
	var buf []byte
	err := b.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		buf, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).WithFromSurface(true).Do(ctx)
		return err
	}))
	if err != nil {
		return nil, fmt.Errorf("截图失败: %w", err)
	}
	return buf, nil
}

// shotChallenge 只截 3DS 验证框所占的区域。
func (b *Browser) shotChallenge(ctx context.Context) ([]byte, error) {
	var buf []byte
	err := b.do(ctx, func(ctx context.Context) error {
		frames, err := b.frames(ctx)
		if err != nil {
			return err
		}
		box := b.find(ctx, frames, Sel.ChallengeFrame, false)
		if box == nil {
			return errors.New("页面上没有 3DS 验证框")
		}
		if err := dom.ScrollIntoViewIfNeeded().WithObjectID(box.obj).Do(ctx); err != nil {
			return err
		}
		x0, y0, x1, y1, err := bounds(ctx, box)
		if err != nil {
			return err
		}
		_, _, _, _, vp, _, err := page.GetLayoutMetrics().Do(ctx)
		if err != nil {
			return err
		}
		clip, err := challengeClip(x0, y0, x1, y1, vp)
		if err != nil {
			return err
		}
		buf, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).WithFromSurface(true).WithClip(clip).Do(ctx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("截取 3DS 验证框失败: %w", err)
	}
	return buf, nil
}

// challengeClip 把验证框相对可视区域的坐标换成截图裁剪区要的页面坐标。浏览器没给可视区域（vp 为 nil）时返回错误：
// 这一步在点过支付之后，空指针 panic 会让整个进程退出；截图失败只记警告。
func challengeClip(x0, y0, x1, y1 float64, vp *page.VisualViewport) (*page.Viewport, error) {
	if vp == nil {
		return nil, errors.New("浏览器没有返回可视区域位置")
	}
	x, y := math.Round(x0+vp.PageX), math.Round(y0+vp.PageY)
	return &page.Viewport{X: x, Y: y, Width: math.Round(x1 + vp.PageX - x), Height: math.Round(y1 + vp.PageY - y), Scale: 1}, nil
}

func digitsOf(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// normText 去掉空白并转小写，用于核对文本字段。
func normText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}
