package paybrowser

// Sel 集中存放支付页各元素的选择器。每项是按优先级排列的候选列表，依次尝试，
// 查找范围为主页面与各级 iframe（含跨站 iframe），选择器顺序优先于 frame 顺序，只认可见元素。
// 除 NewCardOption、ManualAddressOption 外都是 CSS 选择器。
//
// 真实付款页是哪一种还没有证据，所以同时列两种页面的候选：
//   - Stripe Checkout（托管页，字段在主文档，前缀 shipping 或 billing）
//   - Payment Element（字段在 js.stripe.com 的 iframe 里，id 形如 payment-numberInput、billingAddress-*）
//
// 精确的 id 在前，宽泛的 name（如 number、name）在后：宽泛选择器可能命中优惠码之类的无关输入框。
//
// 已核实（2026-11-03，用与 Launch 相同的 Chrome 参数打开 Stripe 官方测试模式 Checkout 演示页，只读 DOM，未提交）：
//   - 卡字段 #cardNumber、#cardExpiry、#cardCvc，及对应的 name、autocomplete
//   - 地址字段 #shipping*（Name、Country、AddressLine1、Locality、PostalCode、AdministrativeArea），及对应的 name、autocomplete
//   - 手动地址入口文字「Enter address manually」；城市、邮编、州默认 class 为 HiddenInput 且不可见
//   - #email（type=text）、#phoneNumber（type=tel）、#enableStripePass、iframe[name="link-login"] 存在
//   - .SubmitButton、data-testid=hosted-payment-submit-button；四个金额元素（Amount 的全部候选）
//
// 没有核实，真实付款前要用「试运行到填表」或测试账号确认：
//   - #billing*、autocomplete="billing *"：只是推断（同一组件只换前缀），官方演示页是实物商品，只有 shipping
//   - Payment Element 原生页的全部字段（payment-*、billingAddress-*）：取自原型，没有实物转储；演示页是内嵌 Checkout，不是这种页
//   - 手动地址入口的中文、日文文字
//   - 点击之后的一切：Success、DeclineText 的文字容器（只证实 .FieldError 这个 class 存在）、ChallengeFrame、OTPInput、OTPSubmit
//   - Success：演示页没有 #payment-success；真实 Stripe Checkout 成功后多半直接跳走，认不出成功时走确认阶段查账号
//
// ChallengeFrame 只列有依据的 3DS 候选：Stripe 的 stripe-challenge-frame（名字），和地址里带 three-ds 的 iframe。
// 以前还有 src*="challenge" 与 src*="3ds"，前者会命中 Stripe 内嵌的 hCaptcha（HCaptchaInvisible 里有 src 含 challenge 的 iframe，
// 风控把它弄可见时），提示文字之前先判 3DS，于是把人机验证当成 3DS 去白等 5 分钟验证码（PF-5）。两个宽泛候选已去掉。
// 第一笔真实付款看到 3DS 截图后，把真实 iframe 的名字或地址补进来；认不出 3DS 框的后果是走确认阶段，不会重复付款。
var Sel = struct {
	// 卡片字段（Stripe Checkout 在主文档，Payment Element 在 iframe）
	CardNumber, Expiry, CVC []string
	// 账单地址字段；Country、State 可能是 select 也可能是 input
	Name, Country, Line1, City, State, Zip []string
	// Email、Phone 是 Stripe Link 区块的邮箱与手机。手机一律不填；邮箱只有可见、必填且为空时才填账号自己的
	Email, Phone []string
	// LinkSave 是「Save my information with Link」勾选框（它本身透明，要用不要求可见的查找）
	LinkSave []string
	// LinkLoginFrame 是 Link 弹出验证时显示的 iframe
	LinkLoginFrame []string
	// PayButton 是提交支付的按钮（只在主页面找，不进 iframe）
	PayButton []string
	// Amount 是汇总区「今日应付」的总额元素
	Amount []string
	// Success 出现即表示支付成功
	Success []string
	// DeclineText 是页面提示文字所在元素（须有文字才算）。全部命中元素都看，点击前就有的文字不算，
	// 按文字内容判断类别（见 classifyPageError），不按这里的选择器
	DeclineText []string
	// ChallengeFrame 是 3DS 验证框所在的 iframe 元素；验证码框可在它里面再嵌几层
	ChallengeFrame []string
	// OTPInput、OTPSubmit 是 3DS 验证框里的验证码输入框与提交按钮。
	// OTPInput 都找不到时，取验证框内唯一可见的文本或数字输入框；没有提交按钮时按回车
	OTPInput, OTPSubmit []string
	// NewCardOption 是账号已绑卡时「使用新卡」入口的可见文字（按文字匹配，不是 CSS）
	NewCardOption []string
	// ManualAddressOption 是展开手动地址的入口文字（按文字匹配，不是 CSS）
	ManualAddressOption []string
}{
	CardNumber: []string{`#payment-numberInput`, `#cardNumber`, `input[autocomplete="cc-number"]`, `input[name="cardNumber"]`, `input[name="number"]`},
	Expiry:     []string{`#payment-expiryInput`, `#cardExpiry`, `input[autocomplete="cc-exp"]`, `input[name="cardExpiry"]`, `input[name="expiry"]`},
	CVC:        []string{`#payment-cvcInput`, `#cardCvc`, `input[autocomplete="cc-csc"]`, `input[name="cardCvc"]`, `input[name="cvc"]`},

	Name: []string{
		`#billingAddress-nameInput`, `#billingName`, `input[autocomplete="billing name"]`, `input[name="billingName"]`,
		`#shippingName`, `input[autocomplete="shipping name"]`, `input[name="shippingName"]`, `input[name="name"]`,
	},
	Country: []string{
		`#billingAddress-countryInput`, `#billingCountry`, `select[autocomplete="billing country"]`, `select[name="billingCountry"]`,
		`#shippingCountry`, `select[autocomplete="shipping country"]`, `select[name="shippingCountry"]`, `select[name="country"]`,
	},
	Line1: []string{
		`#billingAddress-addressLine1Input`, `#billingAddressLine1`, `input[autocomplete="billing address-line1"]`, `input[name="billingAddressLine1"]`,
		`#shippingAddressLine1`, `input[autocomplete="shipping address-line1"]`, `input[name="shippingAddressLine1"]`, `input[name="addressLine1"]`,
	},
	City: []string{
		`#billingAddress-localityInput`, `#billingLocality`, `input[autocomplete="billing address-level2"]`, `input[name="billingLocality"]`,
		`#shippingLocality`, `input[autocomplete="shipping address-level2"]`, `input[name="shippingLocality"]`, `input[name="locality"]`,
	},
	State: []string{
		`#billingAddress-administrativeAreaInput`, `#billingAdministrativeArea`,
		`select[autocomplete="billing address-level1"]`, `input[autocomplete="billing address-level1"]`,
		`select[name="billingAdministrativeArea"]`, `input[name="billingAdministrativeArea"]`,
		`#shippingAdministrativeArea`, `select[autocomplete="shipping address-level1"]`, `input[autocomplete="shipping address-level1"]`,
		`select[name="shippingAdministrativeArea"]`, `input[name="shippingAdministrativeArea"]`,
		`select[name="administrativeArea"]`, `input[name="administrativeArea"]`,
	},
	Zip: []string{
		`#billingAddress-postalCodeInput`, `#billingPostalCode`, `input[autocomplete="billing postal-code"]`, `input[name="billingPostalCode"]`,
		`#shippingPostalCode`, `input[autocomplete="shipping postal-code"]`, `input[name="shippingPostalCode"]`, `input[name="postalCode"]`,
	},

	Email:          []string{`#email`, `input[autocomplete="email"]`, `input[name="email"]`, `input[type="email"]`},
	Phone:          []string{`#phoneNumber`, `#phone`, `input[autocomplete="tel"]`, `input[type="tel"]`},
	LinkSave:       []string{`#enableStripePass`, `input[name="enableStripePass"]`},
	LinkLoginFrame: []string{`iframe[name="link-login"]`},

	// .SubmitButton 在前：主页面可能有别的 submit 按钮（如优惠码 Apply）排在支付按钮前面
	PayButton: []string{`.SubmitButton`, `[data-testid="hosted-payment-submit-button"]`, `button[type="submit"]`},

	Amount: []string{
		`#OrderDetails-TotalAmount`, `[data-testid="product-summary-total-amount"]`,
		`#ProductSummary-totalAmount`, `.OrderDetails-total .CurrencyAmount`,
	},
	// 不用 .SubmitButton--complete：它可能只表示表单已填完。认不出成功只是多等一会儿，再靠查账号定论；误判成功会立刻关掉浏览器
	Success:        []string{`#payment-success`, `[data-testid="payment-success"]`},
	DeclineText:    []string{`#payment-error`, `[role="alert"]`, `.p-FieldError`, `.FieldError`},
	ChallengeFrame: []string{`iframe[name="stripe-challenge-frame"]`, `iframe[src*="three-ds"]`},
	OTPInput: []string{
		`input[autocomplete="one-time-code"]`, `input[name="challengeDataEntry"]`,
		`input[name*="otp" i]`, `input[id*="otp" i]`,
	},
	OTPSubmit: []string{`button[type="submit"]`, `input[type="submit"]`},

	NewCardOption: []string{
		"Add a new card", "Add new card", "New card", "Add payment method", "Use a different card",
		"Add a card", "新增卡", "添加卡", "使用新卡", "添加支付方式", "新的支付方式", "其他卡", "New payment method",
	},
	// 只有英文这条核实过；Launch 固定 --lang=en-US，其余是页面不跟随浏览器语言时的后备
	ManualAddressOption: []string{"Enter address manually", "手动输入地址", "手動で入力"},
}
