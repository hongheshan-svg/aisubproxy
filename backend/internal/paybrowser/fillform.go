package paybrowser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	revealGap       = 2 * time.Second // 两次点手动地址入口的最短间隔
	revealMaxClicks = 2               // 手动地址入口最多点这么多次，防止按钮是开关时来回切换
	phoneSettle     = 3 * time.Second // 取消 Link 后等手机框收起的时限
)

var (
	addressWait = 15 * time.Second // 地址字段的等待时限：含点手动地址入口后字段出现、字段自己出现
	emailSettle = time.Second      // 键入邮箱后留给页面反应的时间：可能重绘、重新勾上 Link 或弹 Link 验证
)

// errSubmitControl 表示按文字找到的入口其实是提交控件。
var errSubmitControl = errors.New("入口是提交控件，不能点")

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
func (b *Browser) Fill(ctx context.Context, c Card, bl Billing) error {
	if err := c.validate(); err != nil {
		return err
	}
	b.mu.Lock() // 正在整页截图时等它截完，再往页面里键入卡号
	b.cardTouched = true
	b.filled = false
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
		{"国家", Sel.Country, bl.Country, kindChoice}, // 地址字段里先选国家：改国家会重绘姓名与其余地址字段
	} {
		if err := b.fillOne(ctx, st, f, fieldWait, false); err != nil {
			return err
		}
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
	return b.recheckFields(ctx, st.done)
}

// revealCardForm 等卡号框出现。账号已存卡时页面先显示已存卡，点一次「新卡」入口把卡号框调出来。
func (b *Browser) revealCardForm(ctx context.Context) error {
	clicked := false
	e, err := b.waitFind(ctx, formWait, func(frames []frameRef) *elem {
		if e := b.find(ctx, frames, Sel.CardNumber, false); e != nil {
			return e
		}
		if !clicked {
			if opt := b.findText(ctx, frames, Sel.NewCardOption); opt != nil && clickReveal(ctx, opt) == nil {
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
	if err := fillField(ctx, e, f); err != nil {
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

// tryReveal 点手动地址入口：没有入口、点过太多次、离上次太近都不点。入口是提交控件时绝不点。
func (b *Browser) tryReveal(ctx context.Context, frames []frameRef, st *fillState) {
	if st.revealClick >= revealMaxClicks || (st.revealClick > 0 && time.Since(st.revealLast) < revealGap) {
		return
	}
	opt := b.findText(ctx, frames, Sel.ManualAddressOption)
	if opt == nil || clickReveal(ctx, opt) != nil {
		return
	}
	st.revealClick++
	st.revealLast = time.Now()
	b.updateReport(func(r *FillReport) { r.ManualAddress = fmt.Sprintf("已点击 %d 次", st.revealClick) })
}

// linkState 是 Link 保存勾选框的状态。
type linkState struct {
	Checked bool `json:"checked"`
	Shown   bool `json:"shown"`
}

// linkStateJS 不看透明度和面积：勾选框本身透明，盖在样式元素上，只有 display 或 visibility 才说明它没显示。
const linkStateJS = `function () {
	return { checked: !!this.checked, shown: !this.checkVisibility || this.checkVisibility({ visibilityProperty: true }) }
}`

// disableLinkSave 取消「Save my information with Link」：勾着就要求手机，还可能弹 Link 验证。
// 先用鼠标点，不生效再用脚本点，还是勾着就报错放弃。页面没有这个勾选框不算错。
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
	var st linkState
	if err := callOn(ctx, e.obj, linkStateJS, &st); err != nil {
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
	read := func() error {
		if err := sleep(ctx, settleDelay); err != nil {
			return err
		}
		return callOn(ctx, e.obj, linkStateJS, &st)
	}
	_ = click(ctx, e) // 点不到也没关系，下面看结果，再用脚本点
	if err := read(); err != nil {
		return fmt.Errorf("读取 Link 保存选项失败: %w", err)
	}
	if st.Checked {
		if err := callOn(ctx, e.obj, `function () { this.click() }`, nil); err != nil {
			return fmt.Errorf("取消 Link 保存失败: %w", err)
		}
		if err := read(); err != nil {
			return fmt.Errorf("读取 Link 保存选项失败: %w", err)
		}
	}
	if st.Checked {
		return errors.New("取消不掉「Save my information with Link」")
	}
	b.updateReport(func(r *FillReport) { r.LinkSave = "已取消" })
	return nil
}

// noteLink 记下 Link 的状态。已经取消过的不覆盖：第二次检查时它理应是未勾选。
func (b *Browser) noteLink(status string) {
	b.updateReport(func(r *FillReport) {
		if r.LinkSave != "已取消" {
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
	if err := fillField(ctx, e, f); err != nil {
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
func (b *Browser) PayButtonState(ctx context.Context) string {
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
