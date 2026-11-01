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
	"strconv"
	"strings"
	"time"
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
type Billing struct{ Name, Line1, City, State, Zip, Country string }

// Outcome 是提交后页面给出的结果。
type Outcome string

const (
	OutcomeSuccess   Outcome = "success"
	OutcomeDeclined  Outcome = "declined"
	OutcomeChallenge Outcome = "challenge" // 出现 3DS 验证框，需要验证码
	OutcomeUnknown   Outcome = "unknown"   // 等到超时，页面没有给出可识别的结果
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
)

const (
	pageLoadTimeout = 60 * time.Second
	pollInterval    = 250 * time.Millisecond

	formWait     = 30 * time.Second       // 等卡号框出现（含点开「新卡」入口）
	fieldWait    = 10 * time.Second       // 卡号框出现后，等其余各字段、支付按钮、验证码框
	keyDelay     = 35 * time.Millisecond  // 卡号类字段逐字键入的间隔
	slowKeyDelay = 70 * time.Millisecond  // 文本核对不符时逐字重打的间隔
	settleDelay  = 200 * time.Millisecond // 按 Esc 后等地址联想收起再核对
	reflowDelay  = 500 * time.Millisecond // 下拉框改选后（如国家）地址表单会重绘
	msgLimit     = 500                    // 页面提示文字最多保留的字数
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

// Open 先注入 cookie 再打开支付页，等到金额元素出现为止，最多 60 秒。
func (b *Browser) Open(ctx context.Context, url string, cookies []Cookie) error {
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

func openErr(what string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s：页面加载超时（%d 秒）", what, int(pageLoadTimeout/time.Second))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// ReadAmount 读取支付页汇总区的应付总额，返回最小单位金额与页面原文。
func (b *Browser) ReadAmount(ctx context.Context, currency string) (minor int64, text string, err error) {
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

// Fill 填入卡片与账单地址。页面先显示账号已存的卡时，先点「新卡」入口。
// 不填 Stripe Link 的邮箱与手机（会触发 Link 验证）。任一字段找不到时报错，错误消息只含中文字段名。
func (b *Browser) Fill(ctx context.Context, c Card, bl Billing) error {
	if err := c.validate(); err != nil {
		return err
	}
	b.mu.Lock() // 正在整页截图时等它截完，再往页面里键入卡号
	b.cardTouched = true
	b.filled = false
	b.mu.Unlock()

	fields := []field{
		{"卡号", Sel.CardNumber, c.Number, kindCard},
		{"有效期", Sel.Expiry, c.ExpMonth + c.ExpYear, kindCard},
		{"CVC", Sel.CVC, c.CVC, kindCard},
		{"国家", Sel.Country, bl.Country, kindChoice}, // 地址字段里先选国家：改国家会重绘姓名与其余地址字段
		{"姓名", Sel.Name, bl.Name, kindText},
		{"街道地址", Sel.Line1, bl.Line1, kindText},
		{"城市", Sel.City, bl.City, kindText},
		{"州/省", Sel.State, bl.State, kindChoice},
		{"邮编", Sel.Zip, bl.Zip, kindText},
	}
	err := b.do(ctx, func(ctx context.Context) error {
		if err := b.revealCardForm(ctx); err != nil {
			return err
		}
		for _, f := range fields {
			if strings.TrimSpace(f.value) == "" {
				continue
			}
			e, err := b.waitFind(ctx, fieldWait, func(frames []frameRef) *elem { return b.find(ctx, frames, f.sels, false) })
			if err != nil {
				return err
			}
			if e == nil {
				return fmt.Errorf("找不到「%s」输入框", f.label)
			}
			if err := fillField(ctx, e, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("填卡失败: %w", err)
	}
	b.mu.Lock()
	b.filled = true
	b.mu.Unlock()
	return nil
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

// revealCardForm 等卡号框出现。账号已存卡时页面先显示已存卡，点一次「新卡」入口把卡号框调出来。
func (b *Browser) revealCardForm(ctx context.Context) error {
	clicked := false
	e, err := b.waitFind(ctx, formWait, func(frames []frameRef) *elem {
		if e := b.find(ctx, frames, Sel.CardNumber, false); e != nil {
			return e
		}
		if !clicked {
			if opt := b.findText(ctx, frames, Sel.NewCardOption); opt != nil && click(ctx, opt) == nil {
				clicked = true
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if e == nil {
		return errors.New("找不到「卡号」输入框")
	}
	return nil
}

// fillField 按字段类型填写并核对。
func fillField(ctx context.Context, e *elem, f field) error {
	if f.kind == kindChoice {
		var tag string
		if err := callOn(ctx, e.obj, `function () { return this.tagName }`, &tag); err != nil {
			return fmt.Errorf("读取「%s」失败: %w", f.label, err)
		}
		if tag == "SELECT" {
			return chooseOption(ctx, e, f)
		}
		f.kind = kindText
	}
	if err := focus(ctx, e); err != nil {
		return fmt.Errorf("无法选中「%s」输入框: %w", f.label, err)
	}
	if f.kind == kindCard {
		return typeCard(ctx, e, f)
	}
	return typeText(ctx, e, f)
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

func chooseOption(ctx context.Context, e *elem, f field) error {
	var r string
	if err := callOn(ctx, e.obj, chooseOptionJS, &r, f.value); err != nil {
		return fmt.Errorf("选择「%s」失败: %w", f.label, err)
	}
	switch r {
	case "missing":
		return fmt.Errorf("「%s」下拉框里没有 %s 这一项", f.label, f.value)
	case "rejected":
		return fmt.Errorf("「%s」选择后核对不一致", f.label)
	case "changed":
		return sleep(ctx, reflowDelay)
	}
	return nil
}

// Submit 点击主页面上的支付按钮。必须先 Fill 成功；先记下「已提交」再点击，此后再调用返回 ErrAlreadySubmitted 且不点击。
func (b *Browser) Submit(ctx context.Context) error {
	b.mu.Lock()
	submitted, filled := b.submitted, b.filled
	b.mu.Unlock()
	if submitted {
		return ErrAlreadySubmitted
	}
	if !filled {
		return errors.New("尚未填卡，不能提交")
	}
	return b.do(ctx, func(ctx context.Context) error {
		seen := false
		btn, err := b.waitFind(ctx, fieldWait, func(frames []frameRef) *elem {
			// 只在主页面找：主页面按钮暂时不可见时，绝不能点到某个 iframe 里的 submit 按钮，白白用掉唯一一次提交
			e := b.find(ctx, frames[:1], Sel.PayButton, false)
			if e == nil {
				return nil
			}
			seen = true
			var enabled bool
			if callOn(ctx, e.obj, `function () { return !this.disabled && this.getAttribute('aria-disabled') !== 'true' }`, &enabled) != nil || !enabled {
				return nil
			}
			return e
		})
		if err != nil {
			return err
		}
		if btn == nil {
			if seen {
				return errors.New("支付按钮一直不可点击（可能有字段未通过页面校验）")
			}
			return errors.New("找不到支付按钮")
		}
		// 先记下已提交再点击：点击中途出错也绝不再点第二次
		b.mu.Lock()
		if b.submitted {
			b.mu.Unlock()
			return ErrAlreadySubmitted
		}
		b.submitted = true
		b.mu.Unlock()
		if err := click(ctx, btn); err != nil {
			return fmt.Errorf("点击支付按钮失败: %w", err)
		}
		return nil
	})
}

// WaitOutcome 等提交后的页面结果，返回结果与页面提示文字（最多 500 字）。
// 到 timeout 仍无可识别的结果时返回 OutcomeUnknown。已填过验证码的 3DS 框不再算作新的验证。
func (b *Browser) WaitOutcome(ctx context.Context, timeout time.Duration) (Outcome, string, error) {
	out, msg := OutcomeUnknown, ""
	err := b.do(ctx, func(ctx context.Context) error {
		_, err := b.poll(ctx, timeout, func(frames []frameRef) bool {
			out, msg = b.outcome(ctx, frames)
			return out != OutcomeUnknown
		})
		return err
	})
	if err != nil {
		return OutcomeUnknown, "", fmt.Errorf("等待支付结果失败: %w", err)
	}
	return out, msg, nil
}

// outcome 检查一轮页面：成功优先，其次被拒提示，最后是未处理过的 3DS 框。
func (b *Browser) outcome(ctx context.Context, frames []frameRef) (Outcome, string) {
	if e := b.find(ctx, frames, Sel.Success, false); e != nil {
		return OutcomeSuccess, truncateMessage(innerText(ctx, e))
	}
	if e := b.find(ctx, frames, Sel.DeclineText, true); e != nil {
		return OutcomeDeclined, truncateMessage(innerText(ctx, e))
	}
	if e := b.find(ctx, frames, Sel.ChallengeFrame, false); e != nil {
		if id, err := contentFrame(ctx, e); err == nil && !b.isAnswered(id) {
			return OutcomeChallenge, ""
		}
	}
	return OutcomeUnknown, ""
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
func (b *Browser) EnterOTP(ctx context.Context, otp string) error {
	otp = strings.TrimSpace(otp)
	if otp == "" {
		return errors.New("验证码为空")
	}
	return b.do(ctx, func(ctx context.Context) error {
		var box cdp.FrameID
		var scope []frameRef
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
			if e := b.find(ctx, scope, Sel.OTPInput, false); e != nil {
				return e
			}
			return b.onlyTextInput(ctx, scope)
		})
		if err != nil {
			return err
		}
		if in == nil {
			return errors.New("在 3DS 验证框里找不到验证码输入框")
		}
		if err := focus(ctx, in); err != nil {
			return fmt.Errorf("无法选中验证码输入框: %w", err)
		}
		if err := clearInput(ctx, in); err != nil {
			return fmt.Errorf("清空验证码输入框失败: %w", err)
		}
		if err := typeKeys(ctx, otp, keyDelay); err != nil {
			return fmt.Errorf("键入验证码失败: %w", err)
		}
		b.mu.Lock()
		if b.answered == nil {
			b.answered = map[cdp.FrameID]bool{}
		}
		b.answered[box] = true
		b.mu.Unlock()

		// 提交按钮先在验证码框所在的 frame 里找，再在整个验证框里找；都没有就按回车
		btn := b.find(ctx, []frameRef{in.frame}, Sel.OTPSubmit, false)
		if btn == nil {
			btn = b.find(ctx, scope, Sel.OTPSubmit, false)
		}
		if btn == nil {
			if err := focus(ctx, in); err != nil {
				return fmt.Errorf("提交验证码失败: %w", err)
			}
			return chromedp.KeyEvent(kb.Enter).Do(ctx)
		}
		if err := click(ctx, btn); err != nil {
			return fmt.Errorf("提交验证码失败: %w", err)
		}
		return nil
	})
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
		// 内容四边形是相对可视区域的坐标，截图裁剪区要的是页面坐标
		x, y := math.Round(x0+vp.PageX), math.Round(y0+vp.PageY)
		clip := &page.Viewport{X: x, Y: y, Width: math.Round(x1 + vp.PageX - x), Height: math.Round(y1 + vp.PageY - y), Scale: 1}
		buf, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).WithFromSurface(true).WithClip(clip).Do(ctx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("截取 3DS 验证框失败: %w", err)
	}
	return buf, nil
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
