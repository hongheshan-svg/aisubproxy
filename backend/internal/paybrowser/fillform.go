package paybrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

const (
	revealGap       = 2 * time.Second // 两次点手动地址入口的最短间隔
	linkSkipWait    = 5 * time.Second // Link 验证框出现后，等「Pay without Link」出现的时限
	revealMaxClicks = 2               // 手动地址入口最多点这么多次，防止按钮是开关时来回切换
	phoneSettle     = 3 * time.Second // 取消 Link 后等手机框收起的时限
)

var (
	addressWait       = 15 * time.Second       // 地址字段的等待时限：含点手动地址入口后字段出现、字段自己出现、选地址联想后字段展开
	suggestWait       = 8 * time.Second        // 在街道里键入后等地址联想出现的时限（2026-11-04 实测 4 秒内出现）
	suggestRetypeWait = 3 * time.Second        // 第一次键入街道后等这么久还没有联想，就清空重打一次（2026-11-04 实测联想库第一次键入时才加载）
	suggestCloseWait  = 2 * time.Second        // 填完地址后按 Esc 关地址联想下拉，等它收起的时限
	blurSettle        = 500 * time.Millisecond // 内嵌页填完后让 iframe 里的输入框失焦，留给页面反应（改写格式、重算）的时间
	emailSettle       = time.Second            // 键入邮箱后留给页面反应的时间：可能重绘、重新勾上 Link 或弹 Link 验证
)

const (
	suggestRetryWait = 2 * time.Second // 点了一条联想后等这么久还没展开，就换下一个 frame 里的联想再点一次
	suggestMaxClicks = 2               // 地址联想最多点这么多次，每次换一个 frame
)

// suggestionSels 是地址联想的选项。2026-11-04 实测：联想在单独的下拉 iframe 里，地址 iframe 里也有一份，都是 role=option。
var suggestionSels = []string{`[role="option"]`}

// errSubmitControl 表示要点的目标（按文字找到的入口、地址联想的选项）其实是提交控件。
var errSubmitControl = errors.New("目标是提交控件，拒绝点击")

// submitControlJS 判断元素是不是提交控件或在提交控件里。入口文字可能写在支付按钮上。
const submitControlJS = `function () {
	const c = this.closest('button, input')
	if (c && String(c.type).toLowerCase() === 'submit') return true
	return !!this.closest('.SubmitButton, [data-testid="hosted-payment-submit-button"]')
}`

// clickReveal 点按文字找到的展开入口（新卡、手动地址），绝不点提交控件。
// 这条路径没有 Submit 的「点击前置位」保护：点错就是一次未被记录的付款，之后会被当成「未提交」，造成重复付款。
func clickReveal(ctx context.Context, e *elem) error {
	var isSubmit bool
	if err := callOn(ctx, e.obj, submitControlJS, &isSubmit); err != nil {
		return err
	}
	if isSubmit {
		return errSubmitControl
	}
	return click(ctx, e)
}

// fillState 是一次 Fill 的进度。
type fillState struct {
	done        []field   // 已填的字段，最后统一核对
	revealClick int       // 已点手动地址入口的次数
	revealLast  time.Time // 上次点手动地址入口的时间
}

// Fill 填入卡片与账单地址，全部发生在点支付之前，任何一步失败都报错、不点支付。错误消息只含中文字段名，绝不含值。
//
// 顺序依据真实 Stripe Checkout 的 DOM：改国家会重绘地址字段，所以先选国家；城市、州、邮编可能要点「手动输入」才出现；
// Link 保存默认勾选且要求手机，先取消再填邮箱，免得键入邮箱就触发 Link 验证；手机一律不填；
// 邮箱只有可见、必填且为空时才填账号自己的；键入都可能让页面重绘，所以最后把所有字段重新核对一遍。
// 内嵌页（PageEmbedded）的地址字段按页面决定：DOM 里有的都要填上（折叠的先展开，展不开就放弃），DOM 里没有的才跳过，
// 见 fillEmbeddedAddress。
func (b *Browser) Fill(ctx context.Context, c Card, bl Billing) error {
	if err := c.validate(); err != nil {
		return err
	}
	if err := bl.validate(); err != nil {
		return err
	}
	b.mu.Lock() // 正在整页截图时等它截完，再往页面里键入卡号
	b.cardTouched = true
	b.filled = false
	b.fillMark = fillMark{}
	b.report = FillReport{ManualAddress: "未点击"}
	b.mu.Unlock()

	err := b.do(ctx, func(ctx context.Context) error { return b.fillForm(ctx, c, bl) })
	if err != nil {
		return fmt.Errorf("填卡失败: %w", err)
	}
	b.mu.Lock()
	b.filled = true
	b.mu.Unlock()
	return nil
}

func (b *Browser) fillForm(ctx context.Context, c Card, bl Billing) error {
	if err := b.revealCardForm(ctx); err != nil {
		return err
	}
	st := &fillState{}
	for _, f := range []field{
		{"卡号", Sel.CardNumber, c.Number, kindCard},
		{"有效期", Sel.Expiry, c.ExpMonth + c.ExpYear, kindCard},
		{"CVC", Sel.CVC, c.CVC, kindCard},
	} {
		if err := b.fillOne(ctx, st, f, fieldWait, false); err != nil {
			return err
		}
	}
	fillAddress := b.fillHostedAddress
	if b.pageKind() == PageEmbedded {
		fillAddress = b.fillEmbeddedAddress
	}
	if err := fillAddress(ctx, st, bl); err != nil {
		return err
	}

	if err := b.disableLinkSave(ctx); err != nil {
		return err
	}
	if err := b.fillEmail(ctx, st, bl.Email); err != nil {
		return err
	}
	if err := b.disableLinkSave(ctx); err != nil { // 键入邮箱可能让页面重绘、重新勾上
		return err
	}
	if err := b.checkPhone(ctx); err != nil {
		return err
	}
	if err := b.checkLinkModal(ctx); err != nil {
		return err
	}
	if err := b.acceptTerms(ctx); err != nil {
		return err
	}
	if b.pageKind() == PageEmbedded {
		if err := b.blurFrames(ctx); err != nil {
			return err
		}
	}
	if err := b.recheckFields(ctx, st.done); err != nil {
		return err
	}
	if b.pageKind() == PageEmbedded {
		return b.markFill(ctx) // 记下这一份页面，点支付前核对没换过（设计 §5.8）
	}
	return nil
}

// blurActiveJS 让本 frame 里有焦点的元素失焦，返回失焦了没有。iframe 元素不碰：它里面的 frame 由 blurFrames 逐个处理。
const blurActiveJS = `(() => {
	const a = document.activeElement
	if (!a || a === document.body || a === document.documentElement || a.tagName === 'IFRAME' || !a.blur) return false
	a.blur()
	return true
})()`

// blurFrames 是内嵌页填表的最后一步（在 recheckFields 之前，设计 §5.7）：让各 iframe 里有焦点的输入框失焦，失焦了就等 blurSettle。
// 只调 blur()，不点击、不发按键。combobox 失焦时自动选中高亮项、改写格式、页面重算金额这类副作用，于是都发生在最后核对和
// 引擎复核金额之前，能被它们抓住；不然要等点支付时焦点离开 iframe 才发生，那时已经点下去了。读不出的 frame 跳过。
func (b *Browser) blurFrames(ctx context.Context) error {
	frames, err := b.frames(ctx)
	if err != nil {
		return fmt.Errorf("让输入框失焦失败: %w", err)
	}
	blurred := false
	for _, f := range frames[1:] {
		r, exc, err := runtime.Evaluate(blurActiveJS).WithContextID(f.ctx).WithReturnByValue(true).Do(ctx)
		if err != nil {
			b.forgetWorld(f.id)
			continue
		}
		var ok bool
		if exc == nil && json.Unmarshal(r.Value, &ok) == nil && ok {
			blurred = true
		}
	}
	if !blurred {
		return nil
	}
	return sleep(ctx, blurSettle)
}

// fillHostedAddress 填托管页的账单地址：有值的字段都必填，找不到就报错。
func (b *Browser) fillHostedAddress(ctx context.Context, st *fillState, bl Billing) error {
	// 地址字段里先选国家：改国家会重绘姓名与其余地址字段
	if err := b.fillOne(ctx, st, field{"国家", Sel.Country, bl.Country, kindChoice}, fieldWait, false); err != nil {
		return err
	}

	hideable := []field{
		{"城市", Sel.City, bl.City, kindText},
		{"州/省", Sel.State, bl.State, kindChoice},
		{"邮编", Sel.Zip, bl.Zip, kindText},
	}
	if err := b.revealAddress(ctx, st, hideable); err != nil {
		return err
	}
	for _, f := range []field{
		{"姓名", Sel.Name, bl.Name, kindText},
		{"街道地址", Sel.Line1, bl.Line1, kindText},
	} {
		if err := b.fillOne(ctx, st, f, addressWait, false); err != nil {
			return err
		}
	}
	for _, f := range hideable {
		if err := b.fillOne(ctx, st, f, addressWait, true); err != nil {
			return err
		}
	}
	return nil
}

// fillEmbeddedAddress 填内嵌页的账单地址（设计 §5.7）。地址字段填哪些按页面决定：Payment Element 上可能一个都没有，
// 也可能有一部分折叠着（2026-11-04 实测城市、州、邮编默认折叠）。每个有值的字段分三种情况：
//   - 可见：照常填。
//   - DOM 里有但不可见：折叠起来的字段，一样要填。页面有手动地址入口就点它展开（同托管页）；
//     没有入口时在街道里逐字键入、点一条地址联想展开（expandBySuggestion）。展不开就报错放弃。
//   - DOM 里没有：记进 FillReport.Absent，不报错。最后再查一遍，填表期间又出现了就报错放弃，不静默漏填。
//
// 顺序：国家排第一（改国家会重绘其余地址字段），选完后先试一次点手动地址入口，再填姓名；
// 然后在需要时选地址联想展开，再填街道、城市、州、邮编，用账单地址自己的值覆盖联想填的值；
// 末尾补查之后关掉还开着的地址联想下拉（closeSuggestions）。
// 地址字段要等卡号填完才出现，所以只有第一个要找的字段最多等 addressWait，之后每个只看当下有没有。
// 值为空的字段同 fillOne 一样跳过，不找也不记。
func (b *Browser) fillEmbeddedAddress(ctx context.Context, st *fillState, bl Billing) error {
	wait := addressWait
	var absent []field
	one := func(f field) error {
		if strings.TrimSpace(f.value) == "" {
			return nil
		}
		visible := false
		e, err := b.waitFind(ctx, wait, func(frames []frameRef) *elem {
			if e := b.find(ctx, frames, f.sels, false); e != nil {
				visible = true
				return e
			}
			visible = false
			return b.findAny(ctx, frames, f.sels)
		})
		if err != nil {
			return err
		}
		wait = 0
		switch {
		case e == nil:
			absent = append(absent, f)
			b.updateReport(func(r *FillReport) { r.Absent = append(r.Absent, f.label) })
			return nil
		case visible:
			// 已经出现：交给 fillOne 重新找一次再填，页面恰好重绘时它会等字段回来
			return b.fillOne(ctx, st, f, fieldWait, false)
		}
		// 在 DOM 里但不可见：折叠的必填字段，等它展开
		return b.fillOne(ctx, st, f, addressWait, true)
	}

	if err := one(field{"国家", Sel.Country, bl.Country, kindChoice}); err != nil {
		return err
	}
	hideable := []field{
		{"城市", Sel.City, bl.City, kindText},
		{"州/省", Sel.State, bl.State, kindChoice},
		{"邮编", Sel.Zip, bl.Zip, kindText},
	}
	if err := b.revealAddress(ctx, st, hideable); err != nil {
		return err
	}
	if err := one(field{"姓名", Sel.Name, bl.Name, kindText}); err != nil {
		return err
	}
	folded, err := b.foldedWithoutEntry(ctx, st, hideable)
	if err != nil {
		return err
	}
	if len(folded) > 0 {
		if err := b.expandBySuggestion(ctx, bl.Line1, folded); err != nil {
			return err
		}
	}
	if err := one(field{"街道地址", Sel.Line1, bl.Line1, kindText}); err != nil {
		return err
	}
	for _, f := range hideable {
		if err := one(f); err != nil {
			return err
		}
	}
	if err := b.checkAbsent(ctx, absent); err != nil {
		return err
	}
	return b.closeSuggestions(ctx, bl.Line1)
}

// closeSuggestions 填完内嵌页的地址后关掉还开着的地址联想下拉（设计 §5.7）。2026-11-04 07:23 实测：覆盖街道时的键入触发了联想请求，
// Esc 先于异步结果到达，结果回来后下拉又打开了，盖在城市、州、邮编那一片上，20 秒后还开着。
//   - 开着（suggestionsOpen）才动手；没开着什么都不做，报告也不加字。
//   - 对街道框用 DOM.focus 设焦点（ensureFocus；不用鼠标点：下拉就在街道下方），按一次 Esc（只按 Esc，绝不按回车），
//     最多等 suggestCloseWait 看它收起。
//   - 收起了：报告「手动地址」后面接「；联想下拉已关闭」，再核对一次街道仍是账单街道，不是就报错放弃。
//   - 关不上（含找不到街道框、设不了焦点）：不报错，报告接「；联想下拉没关上」；点支付前的落点检查（payClickPoint）会兜住点击。
func (b *Browser) closeSuggestions(ctx context.Context, line1 string) error {
	note := func(s string) { b.updateReport(func(r *FillReport) { r.ManualAddress = joinNote(r.ManualAddress, s) }) }
	frames, err := b.frames(ctx)
	if err != nil {
		return fmt.Errorf("检查地址联想下拉失败: %w", err)
	}
	street := b.find(ctx, frames, Sel.Line1, false)
	if !b.suggestionsOpen(ctx, frames, street) {
		return nil
	}
	if street == nil {
		note("联想下拉没关上")
		return nil
	}
	if _, err := ensureFocus(ctx, street); err != nil {
		note("联想下拉没关上")
		return nil
	}
	if err := chromedp.KeyEvent(kb.Escape).Do(ctx); err != nil {
		return fmt.Errorf("关地址联想下拉失败: %w", err)
	}
	// poll 没成功的那一轮会释放对象句柄，每轮重新找街道框；成功的那一轮不释放，下面核对就用它
	closed, err := b.poll(ctx, suggestCloseWait, func(frames []frameRef) bool {
		street = b.find(ctx, frames, Sel.Line1, false)
		return !b.suggestionsOpen(ctx, frames, street)
	})
	if err != nil {
		return err
	}
	if !closed {
		note("联想下拉没关上")
		return nil
	}
	note("联想下拉已关闭")
	if strings.TrimSpace(line1) == "" {
		return nil
	}
	if street == nil {
		b.noteFieldStatus("街道地址", "填完后不见了")
		return errors.New("关联想下拉后找不到「街道地址」输入框，已放弃")
	}
	ok, err := fieldHolds(ctx, street, field{"街道地址", Sel.Line1, line1, kindText})
	if err != nil {
		return fmt.Errorf("核对「街道地址」失败: %w", err)
	}
	if !ok {
		b.noteFieldStatus("街道地址", "核对不一致")
		return errors.New("关联想下拉后街道地址被改了，已放弃")
	}
	return nil
}

// suggestionsOpen 判断地址联想下拉是否开着：任一 iframe（不含主页面）里有可见的联想选项，或者街道框 aria-expanded="true"。
// 2026-11-04 实测：下拉在主页面下单独的 iframe 里，地址 iframe 里也有一份选项；街道框是 combobox。street 可为 nil。
func (b *Browser) suggestionsOpen(ctx context.Context, frames []frameRef, street *elem) bool {
	if b.find(ctx, frames[1:], suggestionSels, false) != nil {
		return true
	}
	var expanded bool
	return street != nil &&
		callOn(ctx, street.obj, `function () { return this.getAttribute('aria-expanded') === 'true' }`, &expanded) == nil && expanded
}

// foldedWithoutEntry 返回城市、州/省、邮编里有值、DOM 里有却不可见（折叠着）的字段，前提是页面上没有手动地址入口、也没点过：
// 有入口时由 fillOne 去点（同托管页）；点过入口、字段还在展开途中时也交给 fillOne 等。不需要用地址联想展开时返回空。
func (b *Browser) foldedWithoutEntry(ctx context.Context, st *fillState, hideable []field) ([]field, error) {
	if st.revealClick > 0 {
		return nil, nil
	}
	frames, err := b.frames(ctx)
	if err != nil {
		return nil, err
	}
	var folded []field
	for _, f := range hideable {
		if strings.TrimSpace(f.value) != "" && b.find(ctx, frames, f.sels, false) == nil && b.findAny(ctx, frames, f.sels) != nil {
			folded = append(folded, f)
		}
	}
	if len(folded) > 0 && b.findText(ctx, frames, Sel.ManualAddressOption) != nil {
		return nil, nil
	}
	return folded, nil
}

// expandBySuggestion 用地址联想展开折叠的城市、州、邮编（设计 §5.7）。2026-11-04 实测：内嵌页上没有手动地址入口，
// 按 Tab、点别的输入框都不展开；在街道里逐字键入（可信键盘事件）后几秒内出现 Google 地址联想，点一条才展开。
//   - 街道值有控制字符就不键入：逐字键入会把 \n、\r 当回车发出去。Fill 入口已经校验过（Billing.validate），这里再挡一次。
//   - 选中街道框（focusField，内嵌页上先看落点）、清空后逐字键入。不按 Esc（会关掉联想），也绝不按回车（Stripe 输入框里回车可能触发提交）。
//   - 按 frame 顺序（跳过主页面）每个 frame 取第一个可见选项，用 clickOption 点，点不成就试下一个、下一轮再试。
//     先等 suggestRetypeWait：一个可见选项都没看到，就重新找街道框、清空、再逐字键入一遍（只重打 1 次；
//     2026-11-04 实测联想库在第一次键入时才开始加载，这一次不出联想，重打才出），再等 suggestWait；
//     看到过选项只是没点成，就不重打，接着等满 suggestWait。
//   - 点中后等 folded 全部展开：每个都可见，而且落点检查（landsInOwnFrame）通过，之后点它们才不会落到主页面上。
//     suggestRetryWait 内没展开，换一个没点过的 frame 里的联想再点一次（最多 suggestMaxClicks 次）；等满 addressWait 仍没展开就报错。
//   - 填表报告记下点的是第几个 iframe 里的选项（按 frame 顺序数，主页面不算），重打过街道也记下；街道框是直接设焦点的，记下原因。
//
// 联想会改写街道、城市、州、邮编，由调用方之后用自己的值覆盖。
func (b *Browser) expandBySuggestion(ctx context.Context, line1 string, folded []field) error {
	switch {
	case strings.TrimSpace(line1) == "":
		return errors.New("账单地址没有街道，无法用地址联想展开城市、州、邮编")
	case strings.IndexFunc(line1, unicode.IsControl) >= 0:
		return errors.New("账单街道含控制字符，无法用地址联想展开城市、州、邮编")
	}
	// typeStreet 每次都重新找街道框：poll 没成功的那一轮会释放全部对象句柄，之前找到的已经失效
	typeStreet := func() error {
		street, err := b.waitFind(ctx, fieldWait, func(frames []frameRef) *elem { return b.find(ctx, frames, Sel.Line1, false) })
		if err != nil {
			return err
		}
		if street == nil {
			return errors.New("找不到「街道地址」输入框，无法展开城市、州、邮编")
		}
		direct, err := b.focusField(ctx, street)
		if err != nil {
			return fmt.Errorf("无法选中「街道地址」输入框: %w", err)
		}
		b.noteDirectFocus("街道地址", direct)
		if err := clearInput(ctx, street); err != nil {
			return fmt.Errorf("清空「街道地址」失败: %w", err)
		}
		if err := typeKeys(ctx, line1, slowKeyDelay); err != nil {
			return fmt.Errorf("键入「街道地址」失败: %w", err)
		}
		return nil
	}
	if err := typeStreet(); err != nil {
		return err
	}

	var (
		clicked []cdp.FrameID // 点过选项的 frame
		nos     []string      // 它们是第几个 iframe，只写进报告
		refused error         // 最近一次没点成的原因，只写进错误说明
	)
	pick := func(limit time.Duration) (bool, error) {
		return b.poll(ctx, limit, func(frames []frameRef) bool {
			for i, f := range frames[1:] { // 主页面上的选项 clickOption 一律不点，不必去滚动主页面
				if slices.Contains(clicked, f.id) {
					continue
				}
				opt := b.find(ctx, []frameRef{f}, suggestionSels, false)
				if opt == nil {
					continue
				}
				if refused = b.clickOption(ctx, frames, opt); refused == nil {
					clicked = append(clicked, f.id)
					nos = append(nos, fmt.Sprint(i+1))
					return true
				}
			}
			return false
		})
	}
	expanded := func(limit time.Duration) (bool, error) {
		return b.poll(ctx, limit, func(frames []frameRef) bool {
			for _, f := range folded {
				e := b.find(ctx, frames, f.sels, false)
				if e == nil {
					return false
				}
				if _, _, err := b.landsInOwnFrame(ctx, frames, e); err != nil {
					return false
				}
			}
			return true
		})
	}

	firstWait := min(suggestRetypeWait, suggestWait)
	ok, err := pick(firstWait)
	if err != nil {
		return err
	}
	retypeNote := ""
	if !ok {
		if refused == nil {
			// 一个可见选项都没看到：联想库可能刚开始加载，清空重打一次（只重打这一次）
			retypeNote = "已重打 1 次街道"
			if err := typeStreet(); err != nil {
				return err
			}
			ok, err = pick(suggestWait)
		} else {
			// 看到过选项只是没点成：不重打，接着等满 suggestWait
			ok, err = pick(suggestWait - firstWait)
		}
		if err != nil {
			return err
		}
	}
	if !ok {
		const msg = "地址联想没有出现，无法展开城市、州、邮编"
		b.updateReport(func(r *FillReport) { r.ManualAddress = joinNote("地址联想没有出现", retypeNote) })
		switch {
		case refused != nil && retypeNote != "":
			return fmt.Errorf("%s（%s；看到过选项但没点成：%w）", msg, retypeNote, refused)
		case refused != nil:
			return fmt.Errorf("%s（看到过选项但没点成：%w）", msg, refused)
		case retypeNote != "":
			return fmt.Errorf("%s（%s）", msg, retypeNote)
		}
		return errors.New(msg)
	}
	first := min(suggestRetryWait, addressWait)
	shown, err := expanded(first)
	if err != nil {
		return err
	}
	if !shown && len(clicked) < suggestMaxClicks {
		// 只看当下：第一下点的可能是不起作用的那份（真实页面地址 iframe 里也有一份选项），联想没收起时换一个 frame 再点
		if _, err := pick(0); err != nil {
			return err
		}
		if shown, err = expanded(addressWait - first); err != nil {
			return err
		}
	}
	where := "第 " + strings.Join(nos, "、") + " 个 iframe 的选项"
	if !shown {
		b.updateReport(func(r *FillReport) {
			r.ManualAddress = joinNote("已点地址联想（未展开）："+where, retypeNote)
		})
		return errors.New("选了地址联想后城市、州、邮编仍未展开")
	}
	b.updateReport(func(r *FillReport) { r.ManualAddress = joinNote("已选地址联想："+where, retypeNote) })
	return nil
}

// joinNote 把附注（可为空）用分号接在 s 后面。
func joinNote(s, note string) string {
	if note == "" {
		return s
	}
	return s + "；" + note
}

// checkAbsent 把填表时 DOM 里没有的地址字段再查一遍：页面可能在键入别的字段后才插入它们，这时报错放弃，不静默漏填。
func (b *Browser) checkAbsent(ctx context.Context, absent []field) error {
	if len(absent) == 0 {
		return nil
	}
	frames, err := b.frames(ctx)
	if err != nil {
		return fmt.Errorf("复查地址字段失败: %w", err)
	}
	for _, f := range absent {
		if b.findAny(ctx, frames, f.sels) != nil {
			return fmt.Errorf("地址字段「%s」填表时没找到、之后又出现了，已放弃", f.label)
		}
	}
	return nil
}

// revealCardForm 等卡号框出现。账号已存卡时页面先显示已存卡，点一次「新卡」入口把卡号框调出来。
// 账号邮箱注册过 Stripe Link 时页面先显示「Confirm it’s you」验证码框并藏起卡号框，点一次「Pay without Link」跳过；
// 同框的「Text a code」会给账号主人发验证码，绝不点。入口都用 clickEntry 点：内嵌页上落点不对就不点，算没点成，照原逻辑接着等或报错；
// 报告和错误写明入口找到了但没点，以及原因（refusedNote），不说成「页面上没有」。
func (b *Browser) revealCardForm(ctx context.Context) error {
	newCard, skippedLink, found := false, false, false
	var linkSeen time.Time
	var skipWhy, newCardWhy string // 内嵌页上入口找到了但没点的原因
	_, err := b.poll(ctx, formWait, func(frames []frameRef) bool {
		if b.find(ctx, frames, Sel.CardNumber, false) != nil {
			found = true
			return true
		}
		if !skippedLink && b.find(ctx, frames, Sel.LinkVerification, false) != nil {
			if linkSeen.IsZero() {
				linkSeen = time.Now()
			}
			if opt := b.findText(ctx, frames, Sel.PayWithoutLinkOption); opt != nil {
				err := b.clickEntry(ctx, frames, opt)
				if err == nil {
					skippedLink = true
					b.noteLinkVerify("已点「Pay without Link」")
					return false
				}
				skipWhy = b.refusedNote(err)
			}
			return time.Since(linkSeen) >= linkSkipWait // 验证框挡着，卡号框不会自己出现，不必等满
		}
		if !newCard {
			if opt := b.findText(ctx, frames, Sel.NewCardOption); opt != nil {
				err := b.clickEntry(ctx, frames, opt)
				newCard = err == nil
				newCardWhy = b.refusedNote(err)
			}
		}
		return false
	})
	switch {
	case err != nil:
		return err
	case found:
		return nil
	case !linkSeen.IsZero() && !skippedLink && skipWhy != "":
		b.noteLinkVerify("要求验证身份，「Pay without Link」没点（" + skipWhy + "）")
		//nolint:staticcheck // ST1005：Stripe Link 是产品名（专有名词）
		return fmt.Errorf("Stripe Link 要求验证身份，「Pay without Link」点不了（%s），已放弃", skipWhy)
	case !linkSeen.IsZero() && !skippedLink:
		b.noteLinkVerify("要求验证身份，找不到「Pay without Link」")
		//nolint:staticcheck // ST1005：Stripe Link 是产品名（专有名词）
		return errors.New("Stripe Link 要求验证身份，页面上没有「Pay without Link」，已放弃")
	case skippedLink:
		return errors.New("点了「Pay without Link」后仍找不到「卡号」输入框")
	case newCardWhy != "":
		return fmt.Errorf("找不到「卡号」输入框（「新卡」入口没点：%s）", newCardWhy)
	}
	return errors.New("找不到「卡号」输入框")
}

func (b *Browser) noteLinkVerify(status string) {
	b.updateReport(func(r *FillReport) { r.LinkVerify = status })
}

// fillOne 等字段出现并填写，值为空时跳过（有些国家的地址表单没有州或邮编）。hideable 的字段可能要点手动地址入口才出现。
func (b *Browser) fillOne(ctx context.Context, st *fillState, f field, wait time.Duration, hideable bool) error {
	if strings.TrimSpace(f.value) == "" {
		return nil
	}
	e, err := b.waitFind(ctx, wait, func(frames []frameRef) *elem {
		if e := b.find(ctx, frames, f.sels, false); e != nil {
			return e
		}
		if hideable {
			b.tryReveal(ctx, frames, st)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if e == nil {
		return fmt.Errorf("找不到「%s」输入框", f.label)
	}
	if err := b.fillField(ctx, e, f); err != nil {
		return err
	}
	st.done = append(st.done, f)
	b.noteField(e, f)
	return nil
}

// revealAddress 在填姓名和街道之前先试一次展开地址：第一个有值的可隐藏字段不可见、又有入口时点它。
// 页面没有入口时不算错：字段可能在街道填好后自己出现，由 fillOne 接着等。
func (b *Browser) revealAddress(ctx context.Context, st *fillState, hideable []field) error {
	frames, err := b.frames(ctx)
	if err != nil {
		return err
	}
	for _, f := range hideable {
		if strings.TrimSpace(f.value) == "" {
			continue
		}
		if b.find(ctx, frames, f.sels, false) == nil {
			b.tryReveal(ctx, frames, st)
		}
		return nil
	}
	return nil
}

// tryReveal 点手动地址入口：没有入口、点过太多次、离上次太近都不点。入口是提交控件时绝不点；内嵌页上落点不对也不点（clickEntry），
// 一次都没点成时报告写「没点（原因）」。
func (b *Browser) tryReveal(ctx context.Context, frames []frameRef, st *fillState) {
	if st.revealClick >= revealMaxClicks || (st.revealClick > 0 && time.Since(st.revealLast) < revealGap) {
		return
	}
	opt := b.findText(ctx, frames, Sel.ManualAddressOption)
	if opt == nil {
		return
	}
	if err := b.clickEntry(ctx, frames, opt); err != nil {
		if why := b.refusedNote(err); why != "" && st.revealClick == 0 {
			b.updateReport(func(r *FillReport) { r.ManualAddress = "没点（" + why + "）" })
		}
		return
	}
	st.revealClick++
	st.revealLast = time.Now()
	b.updateReport(func(r *FillReport) { r.ManualAddress = fmt.Sprintf("已点击 %d 次", st.revealClick) })
}

// checkState 是勾选框的状态。
type checkState struct {
	Checked bool `json:"checked"`
	Shown   bool `json:"shown"`
}

// checkStateJS 不看透明度和面积：Stripe 的勾选框本身透明，盖在样式元素上，只有 display 或 visibility 才说明它没显示。
const checkStateJS = `function () {
	return { checked: !!this.checked, shown: !this.checkVisibility || this.checkVisibility({ visibilityProperty: true }) }
}`

// readCheck 读勾选框的状态。
func readCheck(ctx context.Context, e *elem) (checkState, error) {
	var st checkState
	err := callOn(ctx, e.obj, checkStateJS, &st)
	return st, err
}

// setCheck 把勾选框点到 want：先用鼠标点（clickBox：内嵌页上落点不对就不点），不生效再用脚本点，返回最后读到的状态。
// 调用方先确认它当前不是 want，再按返回的状态核对。内嵌页上鼠标没点、改用了脚本点时，how 是「改用脚本点击（原因）」，写进报告。
func (b *Browser) setCheck(ctx context.Context, e *elem, want bool) (st checkState, how string, err error) {
	read := func() (checkState, error) {
		if err := sleep(ctx, settleDelay); err != nil {
			return checkState{}, err
		}
		return readCheck(ctx, e)
	}
	why := b.refusedNote(b.clickBox(ctx, e)) // 点不到（内嵌页上落点不对时不点）也没关系，下面看结果，再用脚本点
	if st, err = read(); err != nil || st.Checked == want {
		return st, "", err
	}
	if why != "" {
		how = "改用脚本点击（" + why + "）"
	}
	if err := callOn(ctx, e.obj, `function () { this.click() }`, nil); err != nil {
		return st, how, err
	}
	st, err = read()
	return st, how, err
}

// disableLinkSave 取消「Save my information with Link」：勾着就要求手机，还可能弹 Link 验证。
// 先用鼠标点（内嵌页上落点不对就不点），不生效再用脚本点，还是勾着就报错放弃。页面没有这个勾选框不算错。
func (b *Browser) disableLinkSave(ctx context.Context) error {
	frames, err := b.frames(ctx)
	if err != nil {
		return fmt.Errorf("读取 Link 保存选项失败: %w", err)
	}
	e := b.findAny(ctx, frames, Sel.LinkSave)
	if e == nil {
		b.noteLink("页面没有")
		return nil
	}
	st, err := readCheck(ctx, e)
	if err != nil {
		return fmt.Errorf("读取 Link 保存选项失败: %w", err)
	}
	switch {
	case !st.Shown:
		b.noteLink("未显示，无需取消")
		return nil
	case !st.Checked:
		b.noteLink("未勾选，无需取消")
		return nil
	}
	st, how, err := b.setCheck(ctx, e, false)
	if err != nil {
		return fmt.Errorf("取消 Link 保存失败: %w", err)
	}
	if st.Checked {
		b.updateReport(func(r *FillReport) { r.LinkSave = joinNote("取消不掉", how) })
		return errors.New("取消不掉「Save my information with Link」")
	}
	b.updateReport(func(r *FillReport) { r.LinkSave = joinNote("已取消", how) })
	return nil
}

// noteLink 记下 Link 的状态。已经取消过的不覆盖：第二次检查时它理应是未勾选。
func (b *Browser) noteLink(status string) {
	b.updateReport(func(r *FillReport) {
		if !strings.HasPrefix(r.LinkSave, "已取消") {
			r.LinkSave = status
		}
	})
}

// inputState 是输入框是否必填、是否为空、能否编辑。
type inputState struct {
	Required bool `json:"required"`
	Empty    bool `json:"empty"`
	Editable bool `json:"editable"`
}

const inputStateJS = `function () {
	return {
		required: !!this.required || this.getAttribute('aria-required') === 'true',
		empty: String(this.value || '').trim() === '',
		editable: !this.disabled && !this.readOnly,
	}
}`

// fillEmail 只有邮箱框可见、必填且为空时才填账号自己的邮箱；其余情况不碰。错误与报告都不含邮箱。
func (b *Browser) fillEmail(ctx context.Context, st *fillState, email string) error {
	frames, err := b.frames(ctx)
	if err != nil {
		return fmt.Errorf("读取邮箱框失败: %w", err)
	}
	e := b.find(ctx, frames, Sel.Email, false)
	if e == nil {
		b.updateReport(func(r *FillReport) { r.Email = "未显示，未填写" })
		return nil
	}
	var s inputState
	if err := callOn(ctx, e.obj, inputStateJS, &s); err != nil {
		return fmt.Errorf("读取邮箱框失败: %w", err)
	}
	switch {
	case !s.Required:
		b.updateReport(func(r *FillReport) { r.Email = "非必填，未填写" })
		return nil
	case !s.Empty:
		b.updateReport(func(r *FillReport) { r.Email = "已有内容，未改动" })
		return nil
	case strings.TrimSpace(email) == "":
		return errors.New("支付页要求填写邮箱，但账号没有邮箱")
	case !s.Editable:
		return errors.New("支付页的邮箱框必填且为空，但不能编辑")
	}
	f := field{"邮箱", Sel.Email, email, kindText}
	if err := b.fillField(ctx, e, f); err != nil {
		return err
	}
	st.done = append(st.done, f)
	b.noteField(e, f)
	b.updateReport(func(r *FillReport) { r.Email = "必填且为空，已填账号邮箱" })
	return sleep(ctx, emailSettle)
}

// checkPhone 手机一律不填：取消 Link 后手机框仍可见、必填且为空，只能放弃。手机框可能收得慢，等一小会儿。
func (b *Browser) checkPhone(ctx context.Context) error {
	status := ""
	ok, err := b.poll(ctx, phoneSettle, func(frames []frameRef) bool {
		e := b.find(ctx, frames, Sel.Phone, false)
		if e == nil {
			status = "未显示"
			return true
		}
		var s inputState
		if callOn(ctx, e.obj, inputStateJS, &s) != nil {
			return false
		}
		if !s.Required || !s.Empty {
			status = "可见但无需填写，未填写"
			return true
		}
		return false
	})
	if err != nil {
		return err
	}
	if !ok {
		b.updateReport(func(r *FillReport) { r.Phone = "仍必填，已放弃" })
		return errors.New("取消 Link 后支付页仍要求手机号，手机号一律不填，已放弃")
	}
	b.updateReport(func(r *FillReport) { r.Phone = status })
	return nil
}

// acceptTerms 勾上「同意条款」：Stripe 要求同意服务条款时才显示这个勾选框，默认不勾，不勾点支付会被拦下。
// 用户确认过由系统自动勾上（2026-11-03）。勾不上就报错放弃，不点支付；页面没有或没显示不算错。只点勾选框本身，不点条款链接。
func (b *Browser) acceptTerms(ctx context.Context) error {
	note := func(s string) { b.updateReport(func(r *FillReport) { r.Terms = s }) }
	frames, err := b.frames(ctx)
	if err != nil {
		return fmt.Errorf("读取「同意条款」失败: %w", err)
	}
	e := b.findAny(ctx, frames, Sel.TermsConsent)
	if e == nil {
		note("页面没有")
		return nil
	}
	st, err := readCheck(ctx, e)
	if err != nil {
		return fmt.Errorf("读取「同意条款」失败: %w", err)
	}
	switch {
	case !st.Shown:
		note("未显示")
		return nil
	case st.Checked:
		note("页面已勾选")
		return nil
	}
	st, how, err := b.setCheck(ctx, e, true)
	if err != nil {
		return fmt.Errorf("勾选「同意条款」失败: %w", err)
	}
	if !st.Checked {
		note(joinNote("勾不上", how))
		return errors.New("勾不上「同意条款」")
	}
	note(joinNote("已勾选", how))
	return nil
}

// checkLinkModal 发现 Link 弹出验证就放弃：要验证码，走不下去，也不能让它挡在支付按钮前面。
func (b *Browser) checkLinkModal(ctx context.Context) error {
	frames, err := b.frames(ctx)
	if err != nil {
		return fmt.Errorf("检查 Link 验证框失败: %w", err)
	}
	if b.find(ctx, frames, Sel.LinkLoginFrame, false) != nil {
		//nolint:staticcheck // ST1005：Stripe Link 是产品名（专有名词），错误文本保持不变
		return errors.New("Stripe Link 弹出了验证框，已放弃")
	}
	// 键入邮箱后才出现的内嵌验证框：此时卡号框已被藏起，不再点「Pay without Link」重来，直接放弃
	if b.find(ctx, frames, Sel.LinkVerification, false) != nil {
		b.noteLinkVerify("填完后又要求验证身份")
		//nolint:staticcheck // ST1005：Stripe Link 是产品名（专有名词）
		return errors.New("Stripe Link 要求验证身份，已放弃")
	}
	return nil
}

// recheckFields 把填过的字段重新核对一遍，字段不见了或值变了都报错：之前的键入可能让页面重绘、清空或隐藏字段。
// 这里要重新找元素：poll 没成功的那一轮会释放全部对象句柄，之前的 elem 可能已经失效。
func (b *Browser) recheckFields(ctx context.Context, done []field) error {
	frames, err := b.frames(ctx)
	if err != nil {
		return fmt.Errorf("核对字段失败: %w", err)
	}
	for _, f := range done {
		e := b.find(ctx, frames, f.sels, false)
		if e == nil {
			b.noteFieldStatus(f.label, "填完后不见了")
			return fmt.Errorf("「%s」填完后不见了", f.label)
		}
		ok, err := fieldHolds(ctx, e, f)
		if err != nil {
			return fmt.Errorf("核对「%s」失败: %w", f.label, err)
		}
		if !ok {
			b.noteFieldStatus(f.label, "核对不一致")
			return fmt.Errorf("「%s」填完后被页面改掉了", f.label)
		}
		b.noteFieldStatus(f.label, "核对一致")
	}
	return nil
}

// selectHoldsJS 判断下拉框当前选中的是不是 want（选项值或文字，不分大小写）。不是下拉框返回 null。
const selectHoldsJS = `function (want) {
	if (this.tagName !== 'SELECT') return null
	want = String(want).trim().toLowerCase()
	const o = this.options[this.selectedIndex]
	return !!o && (o.value.toLowerCase() === want || o.textContent.trim().toLowerCase() === want)
}`

// fieldHolds 判断字段当前的值是否还是填进去的值。
func fieldHolds(ctx context.Context, e *elem, f field) (bool, error) {
	if f.kind == kindChoice {
		var ok *bool
		if err := callOn(ctx, e.obj, selectHoldsJS, &ok, f.value); err != nil {
			return false, err
		}
		if ok != nil {
			return *ok, nil
		}
	}
	got, err := inputValue(ctx, e)
	if err != nil {
		return false, err
	}
	if f.kind == kindCard {
		return digitsOf(got) == f.value, nil
	}
	return normText(got) == normText(f.value), nil
}

// PayButtonState 只读地描述支付按钮当前的状态，绝不点击。试运行用它把按钮状态写进时间线。
// 托管页只读一次；内嵌页见 embeddedPayButtonState。
func (b *Browser) PayButtonState(ctx context.Context) string {
	if b.pageKind() == PageEmbedded {
		return b.embeddedPayButtonState(ctx)
	}
	state := "未找到"
	err := b.do(ctx, func(ctx context.Context) error {
		frames, err := b.frames(ctx)
		if err != nil {
			return err
		}
		e := b.find(ctx, frames[:1], Sel.PayButton, false)
		if e == nil {
			return nil
		}
		var enabled bool
		if err := callOn(ctx, e.obj, payEnabledJS, &enabled); err != nil {
			return err
		}
		state = "存在但不可点击"
		if enabled {
			state = "可点击"
		}
		return nil
	})
	if err != nil {
		return "读取失败"
	}
	return state
}

// embeddedPayButtonState 是内嵌页的 PayButtonState（设计 §5.8），和 Submit 走同一条路，只检查、绝不点击：
//   - waitPayButton 最多等 fieldWait，等到仍在结账页、主页面唯一可见、可用（不 disabled、不 aria-busy）的提交按钮；
//   - readyAfterFocus 把焦点移到主页面（只设焦点）、固定等 payFocusSettle，再最多等 fieldWait 等按钮重新可用；
//   - payClickPoint 查结账页、按钮个数、填表之后页面没换过、可点、焦点、按钮没在动、落点。
//
// 2026-11-04 07:23 实测：地址改了，页面重算，Fill 刚返回时按钮 disabled、aria-busy，1 秒后才可用；只读一次会误报「存在但不可点击」。
// 结果：
//   - 全过：「可点击（填表后等了 X 秒，移焦点后等了 Y 秒）」，X、Y 分别是两段等按钮可用的时间（不含固定的 payFocusSettle）：
//     X 看 Fill 返回后页面重算要多久，Y 看实页会不会在 iframe 失焦时重算；
//   - 最后检查时按钮不可点：「存在但不可点击（最后检查时忙）」——前两段都等到了可用，滚动、等稳那一会儿又忙了；
//   - 最后检查时页面变了：「页面已离开结账页」「不唯一（N 个）」「未找到」「页面在填表之后重新加载过」等（pageChanged.state）；
//   - 落点是别的元素：「可点击，但落点被挡住（落点是 …）」；其余没过落点检查：「可点击，但没过落点检查（原因）」；
//   - 等不到时按最后一次看到的情况报：唯一但不可用「存在但不可点击（等了 N 秒）」（N 是 fieldWait），
//     「不唯一（N 个）」「未找到」「读取失败」措辞不变；看到已离开结账页报「页面已离开结账页」。
func (b *Browser) embeddedPayButtonState(ctx context.Context) string {
	n := payButtonUnreadable
	state := ""
	err := b.do(ctx, func(ctx context.Context) error {
		start := time.Now()
		btn, err := b.waitPayButton(ctx, &n)
		if err != nil || btn == nil {
			return err
		}
		waited := time.Since(start)
		btn, after, err := b.readyAfterFocus(ctx, btn, &n)
		if err != nil || btn == nil {
			return err
		}
		if _, _, err := b.payClickPoint(ctx, btn); err != nil {
			var pc pageChanged
			switch note := payCheckNote(err); {
			case errors.Is(err, errPayNotReady):
				state = "存在但不可点击（最后检查时忙）"
			case errors.As(err, &pc):
				state = pc.state()
			case strings.HasPrefix(note, "被挡住"):
				state = "可点击，但落点" + note
			default:
				state = "可点击，但" + note
			}
			return nil
		}
		state = fmt.Sprintf("可点击（填表后等了 %.1f 秒，移焦点后等了 %.1f 秒）", waited.Seconds(), after.Seconds())
		return nil
	})
	switch {
	case state != "":
		return state
	case err != nil || n == payButtonUnreadable:
		return "读取失败"
	case n == payButtonLeft:
		return leftCheckoutMsg
	case n == 1:
		return fmt.Sprintf("存在但不可点击（等了 %g 秒）", fieldWait.Seconds())
	case n > 1:
		return fmt.Sprintf("不唯一（%d 个）", n)
	}
	return "未找到"
}
