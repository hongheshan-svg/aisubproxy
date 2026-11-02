package paybrowser

// Sel 集中存放支付页各元素的选择器。每项是按优先级排列的候选列表，依次尝试，
// 查找范围为主页面与各级 iframe（含跨站 iframe），选择器顺序优先于 frame 顺序，只认可见元素。
// 除 NewCardOption 外都是 CSS 选择器。
//
// 卡号到邮编、支付按钮、新增卡入口的初值取自原型 automation/fill-checkout.js；
// 原型里没有的金额、成功、被拒、3DS 相关项按 Stripe 支付页的常见结构给出，
// 待实地核实后修改（只改这里）。
var Sel = struct {
	// 卡片字段（Stripe Payment Element，位于 iframe 中）
	CardNumber, Expiry, CVC []string
	// 账单地址字段（Stripe Address Element）；Country、State 可能是 select 也可能是 input
	Name, Country, Line1, City, State, Zip []string
	// PayButton 是提交支付的按钮（只在主页面找，不进 iframe）
	PayButton []string
	// Amount 是汇总区「今日应付」的总额元素
	Amount []string
	// Success 出现即表示支付成功
	Success []string
	// DeclineText 是被拒或校验出错时的提示文字所在元素（须有文字才算）。全部命中元素都看，点击前就有的文字不算，
	// 属于被拒还是校验错误按文字内容判断（见 classifyPageError），不按这里的选择器
	DeclineText []string
	// ChallengeFrame 是 3DS 验证框所在的 iframe 元素；验证码框可在它里面再嵌几层
	ChallengeFrame []string
	// OTPInput、OTPSubmit 是 3DS 验证框里的验证码输入框与提交按钮。
	// OTPInput 都找不到时，取验证框内唯一可见的文本或数字输入框；没有提交按钮时按回车
	OTPInput, OTPSubmit []string
	// NewCardOption 是账号已绑卡时「使用新卡」入口的可见文字（按文字匹配，不是 CSS）
	NewCardOption []string
}{
	CardNumber: []string{`#payment-numberInput`, `input[name="number"]`, `input[autocomplete="cc-number"]`},
	Expiry:     []string{`#payment-expiryInput`, `input[name="expiry"]`, `input[autocomplete="cc-exp"]`},
	CVC:        []string{`#payment-cvcInput`, `input[name="cvc"]`, `input[autocomplete="cc-csc"]`},

	Name:    []string{`#billingAddress-nameInput`, `input[name="name"]`, `input[autocomplete="billing name"]`},
	Country: []string{`#billingAddress-countryInput`, `select[name="country"]`, `select[autocomplete="billing country"]`},
	Line1:   []string{`#billingAddress-addressLine1Input`, `input[name="addressLine1"]`, `input[autocomplete="billing address-line1"]`},
	City:    []string{`#billingAddress-localityInput`, `input[name="locality"]`, `input[autocomplete="billing address-level2"]`},
	State: []string{
		`#billingAddress-administrativeAreaInput`, `select[name="administrativeArea"]`, `input[name="administrativeArea"]`,
		`select[autocomplete="billing address-level1"]`, `input[autocomplete="billing address-level1"]`,
	},
	Zip: []string{`#billingAddress-postalCodeInput`, `input[name="postalCode"]`, `input[autocomplete="billing postal-code"]`},

	// .SubmitButton 在前：主页面可能有别的 submit 按钮（如优惠码 Apply）排在支付按钮前面
	PayButton: []string{`.SubmitButton`, `button[type="submit"]`},

	Amount: []string{
		`#OrderDetails-TotalAmount`, `[data-testid="product-summary-total-amount"]`,
		`#ProductSummary-totalAmount`, `.OrderDetails-total .CurrencyAmount`,
	},
	Success:     []string{`#payment-success`, `.SubmitButton--complete`, `[data-testid="payment-success"]`},
	DeclineText: []string{`#payment-error`, `[role="alert"]`, `.p-FieldError`, `.FieldError`},
	ChallengeFrame: []string{
		`iframe[name="stripe-challenge-frame"]`, `iframe[src*="three-ds"]`,
		`iframe[src*="3ds"]`, `iframe[src*="challenge"]`,
	},
	OTPInput: []string{
		`input[autocomplete="one-time-code"]`, `input[name="challengeDataEntry"]`,
		`input[name*="otp" i]`, `input[id*="otp" i]`,
	},
	OTPSubmit: []string{`button[type="submit"]`, `input[type="submit"]`},

	NewCardOption: []string{
		"Add a new card", "Add new card", "New card", "Add payment method", "Use a different card",
		"Add a card", "新增卡", "添加卡", "使用新卡", "添加支付方式", "新的支付方式", "其他卡", "New payment method",
	},
}
