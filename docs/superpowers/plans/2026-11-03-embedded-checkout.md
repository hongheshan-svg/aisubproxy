# 订阅开通支持内嵌结账页 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal：** ChatGPT 只给内嵌结账会话时，付款引擎用浏览器打开 `chatgpt.com/checkout/<processor_entity>/<会话号>` 完成订阅开通；同时所有付款单增加金额下限。

**Architecture：**
- `chatgpt` 包：从建会话的返回里多读出 `tag` 和 `processor_entity`。
- `payengine`：
  - 拼出内嵌页链接；
  - 白名单只放行 chatgpt.com 的 `/checkout/<段>/<段>`；
  - 按域名定出页面类型，传给浏览器。
- `paybrowser` 按页面类型分支。托管页行为不变。内嵌页的做法：
  - 金额读 Stripe iframe 里给读屏软件的 `Payment amount`；
  - 地址字段可有可无；
  - 主页面的提交按钮必须唯一；
  - 页面离开 `/checkout/` 就转确认。
- 金额下限用套餐配置里的 `expect_minor`。

**Tech Stack：** Go（chromedp、gin、SQLite），Vue 3 + Element Plus + vitest。

**Spec：** `docs/superpowers/specs/2026-11-03-embedded-checkout-design.md`

## Global Constraints

- 代码注释、错误说明、时间线文字、提交信息都用简体中文。提交信息末尾加 `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`。
- 托管页（`pay.openai.com`、`checkout.stripe.com`）的行为一律不变，现有测试全部保持通过，不准为了让新代码通过而改旧断言。只有 §Task 2 的 `Open` 签名变化和 §Task 7 的预期金额要求，可以机械地改旧测试的调用和配置。
- 错误说明、事件、日志里不得出现会话号、完整支付链接、卡号、CVC、会话 token、账号邮箱。白名单拒绝时只写域名。
- 内嵌页的全部放弃条件都在注资之前判定：
  - 打不开、Cloudflare 挑战、读不出金额；
  - 币种不符、超上限、低于预期；
  - 支付按钮不唯一。
- 只点一次的机制不变：先写 `submitted_at` 再点、`LockSubmit`、`ErrAlreadySubmitted`。
- 不做任何绕过 Cloudflare 或页面风控的事。
- 测试不发真实网络请求，不付款。浏览器测试用本机仿造页和真实 Chrome，找不到 Chrome 时用 `needChrome` 跳过。
- 不碰 18080 端口和仓库的 `data/` 目录。不用 `git add -A`，`.claude/` 不要提交。不用不带参数的 `git stash`。
- 门禁：`cd backend && go build ./... && go vet ./... && go test ./...`；`cd frontend && npx vitest run && npm run build`。
- 规范化金额原文的格式是「金额里最后一段数字 + 空格 + 大写币种代码」，比如 `100.00 USD`、`1,120.00 PHP`。
- 下限公式：`金额 × 100 < ExpectMinor × 95` 时放弃，错误码 `amount_below_expected`。

## Review Focus

1. **填表后页面重绘**，主页面冒出第二个提交按钮，或按钮被换掉：`Submit` 在点击那一刻重新数按钮，不是 1 个就不点，返回错误，`submitted` 不置位。测试在 Task 5。
2. **账号语言是中文**，主页面拒付提示是中文（「你的银行卡被拒绝」）：不能判成 `declined`，等到超时返回 `OutcomeUnknown`，交给查账号定论。测试在 Task 5。
3. **Stripe 改了读屏文字**，没有 `Currency:`：`Open` 照样成功，`ReadAmount` 报错，引擎沿用现有路径以 `amount_unreadable` 在注资前放弃（`service_test.go` 已覆盖），不能误读成别的币种。测试在 Task 3。
4. **预期金额配高了**（OpenAI 降价后管理员没改）：单据以 `amount_below_expected` 放弃，说明里同时写出页面金额和预期金额，管理员看得懂要改什么。测试在 Task 7。
5. **会话号或 processor_entity 带路径字符**（`/`、`..`、`?`、`#`、大写）：在拼链接时放弃，不取卡，不启动浏览器，说明里没有会话号。测试在 Task 6。

---

### Task 1：建会话返回带上 tag 和 processor_entity

**Files：**
- Modify：`backend/internal/chatgpt/client.go`（`Checkout`、`CreateCheckout`）
- Test：`backend/internal/chatgpt/client_test.go`

**Interfaces：**
- Produces：`type Checkout struct{ SessionID, URL, Tag, ProcessorEntity string }`；常量沿用 `chatgpt.CheckoutTagCustom = "custom_checkout_session"`（在 `billing.go`）。

- [ ] **Step 1：写失败的测试** `TestCreateCheckoutCustomSession`

  假服务器返回：
  ```json
  {"checkout_session_id":"cs_live_abc","tag":"custom_checkout_session","processor_entity":" openai_llc ","checkout_ui_mode":"custom","client_secret":"cs_live_abc_secret_x"}
  ```
  断言：
  ```go
  want := Checkout{SessionID: "cs_live_abc", Tag: "custom_checkout_session", ProcessorEntity: "openai_llc"}
  if got != want { t.Fatalf("got %+v want %+v", got, want) }
  ```
  `ProcessorEntity` 要去掉首尾空白；`URL` 为空，不报错。

- [ ] **Step 2：运行确认失败**
  Run：`cd backend && go test ./internal/chatgpt/ -run TestCreateCheckout -v`
  Expected：编译失败，`Checkout` 没有 `Tag` 字段。

- [ ] **Step 3：实现**
  - `Checkout` 加 `Tag`、`ProcessorEntity` 两个字段。
  - `CreateCheckout` 解析 `tag`、`processor_entity`，去掉首尾空白。
  - 没有 `checkout_session_id` 时照旧报错。

- [ ] **Step 4：运行确认通过**
  Run：`cd backend && go test ./internal/chatgpt/ -v -run 'TestCreateCheckout'`
  Expected：`TestCreateCheckoutRequestShape` 和新测试都 PASS。

- [ ] **Step 5：提交** `feat(chatgpt): 建结账会话返回 tag 与 processor_entity`

---

### Task 2：页面类型贯通浏览器与引擎

**Files：**
- Modify：`backend/internal/paybrowser/page.go`（`PageKind`、`Open`）、`backend/internal/paybrowser/chrome.go`（`Browser` 加 `kind` 字段）
- Modify：`backend/internal/paybrowser/*_test.go` 里所有 `b.Open(` 调用（`page_test.go`、`render_test.go`、`diagnose_test.go`、`dialog_test.go` 等），一律加 `paybrowser.PageHosted`
- Modify：`backend/internal/payengine/service.go`（`FormBrowser.Open`）、`backend/internal/payengine/worker.go`（`openPage`，新增 `checkoutPageKind`）、`backend/internal/payengine/fakes_test.go`（`fakeBrowser.Open` 记下 kind）
- Test：`backend/internal/payengine/checkout_url_test.go`

**Interfaces：**
- Produces：
  ```go
  // paybrowser
  type PageKind int
  const (
      PageHosted PageKind = iota // Stripe 托管页
      PageEmbedded               // chatgpt.com 内嵌结账页
  )
  func (b *Browser) Open(ctx context.Context, url string, kind PageKind, cookies []Cookie) error
  // payengine
  func checkoutPageKind(raw string) paybrowser.PageKind
  ```
  `fakeBrowser` 新增可读的 `openKind paybrowser.PageKind`，供 Task 6 断言。

- [ ] **Step 1：写失败的测试** `TestCheckoutPageKind`

  断言：
  - `https://chatgpt.com/checkout/openai_llc/cs_live_abc` 和 `HTTPS://CHATGPT.COM/checkout/a/b` 返回 `paybrowser.PageEmbedded`；
  - `https://pay.openai.com/c/pay/x`、`https://checkout.stripe.com/c/pay/x`、空串、`::bad` 返回 `paybrowser.PageHosted`。

- [ ] **Step 2：运行确认失败**
  Run：`cd backend && go test ./internal/payengine/ -run TestCheckoutPageKind`
  Expected：编译失败，`checkoutPageKind` 未定义。

- [ ] **Step 3：实现**
  - `checkoutPageKind`：解析失败或主机名不是 `chatgpt.com`（小写比较）时返回 `PageHosted`。
  - `Open` 把 kind 记在 `Browser` 上，本任务里不改变任何行为。
  - `openPage` 改为 `b.Open(pctx, j.o.CheckoutURL, checkoutPageKind(j.o.CheckoutURL), sessionCookies(j.sess))`。
  - 机械更新所有调用方和假浏览器。

- [ ] **Step 4：运行确认通过**
  Run：`cd backend && go build ./... && go vet ./... && go test ./internal/payengine/ ./internal/paybrowser/ ./internal/handler/`
  Expected：全部 PASS（浏览器测试在有 Chrome 的机器上真跑）。

- [ ] **Step 5：提交** `refactor(pay): 打开支付页时显式传页面类型`

---

### Task 3：内嵌页打开、读金额、Cloudflare 识别

**Files：**
- Create：
  - `backend/internal/paybrowser/testdata/checkout-embedded.html`
  - `backend/internal/paybrowser/testdata/card-embedded.html`
  - `backend/internal/paybrowser/testdata/cf-challenge.html`
- Create：`backend/internal/paybrowser/embedded.go`（内嵌页专用的脚本与函数）
- Create：`backend/internal/paybrowser/embedded_test.go`（仿造服务器 `newFakeEmbedded` 与本任务、Task 4、Task 5 的测试）
- Modify：`backend/internal/paybrowser/page.go`（`Open`、`WaitRendered`、`ReadAmount` 按 `b.kind` 分支；`pageLoadTimeout` 由常量改为变量）
- Modify：`backend/internal/paybrowser/export_test.go`（加 `SetPageLoadTimeout(d time.Duration) (restore func())`）

**Interfaces：**
- Consumes：Task 2 的 `PageKind`、`Open` 签名。
- Produces：
  ```go
  var ErrCFChallenge = errors.New("支付页停在 Cloudflare 挑战页")
  ```
  内嵌页的 `ReadAmount` 返回规范化原文（见 Global Constraints）。
  仿造服务器 `newFakeEmbedded(t) *fakeEmbedded`：
  - `url(query string) string`；
  - `cfURL() string`；
  - `gotSession() string`；
  - `payments() []payRecord`（复用 `page_test.go` 的 `payRecord`，内嵌页用到其中的 Number、Expiry、CVC、Country、Zip、SavePayment）；
  - `doneVisits() int32`。

**仿造页约定**（照 spec §2 的真实 DOM）：

- **主服务器**（127.0.0.1）路由：
  - `/checkout/openai_llc/cs_test_embedded`：返回 `checkout-embedded.html`，把 `__CARD_ORIGIN__` 换成卡片服务器地址，并记下会话 cookie。
  - `/checkout/openai_llc/cs_test_cf`：返回 `cf-challenge.html`，标题 `Just a moment...`，HTML 里有 `window._cf_chl_opt`，没有卡片框。
  - `POST /pay`：记录提交。
  - `/done`：计数后返回一页普通 HTML。
- **卡片服务器**（localhost，与主页面跨站）：提供 `card-embedded.html` 和现有的 `acs.html`。
- **`checkout-embedded.html` 的查询参数**：
  - `twobuttons=1`：主页面两个可见的 `button[type=submit]`；
  - `nobutton=1`：一个都没有；
  - `leave=1`：提交成功后跳到 `/done`；
  - `decline=<文字>`：点击后主页面显示带这段文字的 `[role="alert"]`；
  - `challenge=1`：点击后插入 `iframe[name="stripe-challenge-frame"]`，指向 `acs.html`；
  - `rerender=1`：页面加载 1.5 秒后再插入一个可见的提交按钮；
  - 其余参数原样传给卡片 iframe 的地址。
- **`card-embedded.html`**：
  - 卡片字段：`#payment-numberInput`、`#payment-expiryInput`、`#payment-cvcInput`，与 `card.html` 相同。
  - 读屏文字放在视觉隐藏的容器里：`<p>Payment amount: {amount，默认 $100.00}</p><p>Currency: {currency，默认 usd}</p><p>Payment mode: Live mode. Test data not accepted.</p>`。
  - 读屏文字的参数：`nocurrency=1` 去掉第二段；`noamount=1` 去掉整个容器。
  - 保存卡勾选框：`<input type="checkbox" id="payment-savePaymentInput" name="savePayment" class="p-Checkbox-input">`，标签 `Save payment details to OpenAI OpCo, LLC for future purchases`；带 `savepay=1` 时初始勾上。
  - 地址字段：带 `addr=1` 时，卡号框输入满 16 位后才出现 `<select id="payment-countryInput" autocomplete="billing country">`（选项 US、PH）和 `<input id="payment-postalCodeInput" autocomplete="billing postal-code">`。
  - 主页面取字段值用与 `card.html` 相同的 postMessage collect 协议，回传上面全部字段。

- [ ] **Step 1：写失败的测试**（`embedded_test.go`，每个用例开头 `needChrome(t)`）
  ```go
  // TestEmbeddedOpenAndReadAmount
  err := b.Open(ctx, f.url(""), paybrowser.PageEmbedded, cookies)   // nil
  f.gotSession() == "sess-abc123"
  minor, text, err := b.ReadAmount(ctx, "USD")                      // 10000, "100.00 USD", nil
  b.WaitRendered(ctx, 10*time.Second) == true

  // TestEmbeddedReadAmountPHP：url("amount=%E2%82%B11,120.00&currency=php")
  ReadAmount(ctx, "PHP") == 112000, "1,120.00 PHP"
  paybrowser.CurrencyMatches("1,120.00 PHP", "USD") == false

  // TestEmbeddedAmountWithoutCurrency：url("nocurrency=1")
  Open 返回 nil；ReadAmount 返回非 nil 错误，minor == 0

  // TestEmbeddedOpenRequiresSingleButton（SetPageLoadTimeout(3*time.Second)）
  url("twobuttons=1") → err 含 "找到 2 个"，且 !errors.Is(err, paybrowser.ErrCFChallenge)
  url("nobutton=1")   → err 含 "找到 0 个"

  // TestEmbeddedOpenNoAmountText（SetPageLoadTimeout(3s)）：url("noamount=1") → Open 报错

  // TestEmbeddedOpenCloudflare（SetPageLoadTimeout(3s)）：
  errors.Is(b.Open(ctx, f.cfURL(), paybrowser.PageEmbedded, nil), paybrowser.ErrCFChallenge)
  ```

- [ ] **Step 2：运行确认失败**
  Run：`cd backend && go test ./internal/paybrowser/ -run TestEmbedded -v`
  Expected：编译失败或断言失败（`ErrCFChallenge` 未定义 / 金额读不到）。

- [ ] **Step 3：实现**（`embedded.go` 和 `page.go`）
  - **`Open`（内嵌页）**：注入 cookie、导航，沿用现有代码。然后在 `pageLoadTimeout` 内轮询，三样同时成立才返回 nil：
    - 某个 frame 里有可见的 `Sel.CardNumber`；
    - 某个 frame 里有以 `Payment amount:` 开头的文字。查找不要求可见，因为它是读屏文字。
    - 主页面（`frames[:1]`）可见的 `button[type="submit"]` 正好 1 个。
  - **`Open` 超时后**：用 `ProbeRead` 和 `IsCFChallengePage` 判断。
    - 是挑战页：返回 `fmt.Errorf("%w（标题 %q）", ErrCFChallenge, 标题)`。
    - 否则：返回说明哪一样没成立的错误，比如「支付页上的支付按钮不唯一（找到 2 个）」「支付页上没有找到卡号框」「支付页上没有找到 Payment amount」。
  - **`ReadAmount`（内嵌页）**：取以 `Payment amount:` 开头的元素的父元素文字，用 `Payment amount:\s*(.+?)\s+Currency:\s*([A-Za-z]{3})` 解析。
    - 用 `amountRe` 取金额部分最后一段数字，返回 `数字 + " " + 大写代码`，再交给 `ParseAmount`。
    - 匹配不到时报错「读不到 Payment amount 的币种」。
  - **`WaitRendered`（内嵌页）**：等卡号框可见。
  - 托管页的三个函数一行不改，靠 `b.kind` 分支进入。

- [ ] **Step 4：运行确认通过**
  Run：`cd backend && go test ./internal/paybrowser/ -v -run 'TestEmbedded|TestOpenAndReadAmount'`
  Expected：全部 PASS；托管页 `TestOpenAndReadAmount` 仍 PASS。

- [ ] **Step 5：提交** `feat(paybrowser): 内嵌结账页打开、读金额与 Cloudflare 识别`

---

### Task 4：内嵌页填表

**Files：**
- Modify：`backend/internal/paybrowser/fillform.go`（`fillForm`、`PayButtonState`）、`backend/internal/paybrowser/report.go`（`FillReport.Absent`）
- Test：`backend/internal/paybrowser/embedded_test.go`

**Interfaces：**
- Consumes：Task 3 的仿造服务器。
- Produces：
  - `FillReport` 新增 `Absent []string`：内嵌页上没出现的可选地址字段的中文名，按填写顺序。
  - `Lines()` 在 `Absent` 非空时多输出一行 `页面没有的字段：国家、州/省、城市、邮编、姓名、街道地址`，只列实际缺的那些，用顿号连接。

- [ ] **Step 1：写失败的测试**
  ```go
  // TestEmbeddedFillWithoutAddress：Open(url("")) 后
  b.Fill(ctx, testCard, paybrowser.Billing{Name: "A B", Line1: "1 Main St", City: "Portland", State: "OR", Zip: "97201", Country: "US"}) == nil
  r := b.FillReport(); r.Absent 含 "国家"、"邮编"、"姓名"、"街道地址"
  strings.Join(r.Lines(), "\n") 含 "页面没有的字段："
  b.PayButtonState(ctx) == "可点击"

  // TestEmbeddedFillWithAddress：url("addr=1")
  Fill == nil；r.Absent 不含 "国家"、"邮编"；页面上国家选中 US、邮编为 97201（用 collect 或读 iframe 值核对）

  // TestEmbeddedSavePaymentUnchecked：url("savepay=1")
  Fill 后 b.EnsureNoSavePayment(ctx) 返回 nil；随后读到 #payment-savePaymentInput 未勾选

  // TestEmbeddedPayButtonStateNotUnique：url("rerender=1")，等 2 秒后
  b.PayButtonState(ctx) 含 "不唯一"
  ```
  `testCard` 用 `4242424242424242`、12、三年后的两位年份、123。

- [ ] **Step 2：运行确认失败**
  Run：`cd backend && go test ./internal/paybrowser/ -run 'TestEmbeddedFill|TestEmbeddedSavePayment|TestEmbeddedPayButtonState' -v`
  Expected：FAIL，原因是「找不到「国家」输入框」或 `Absent` 字段未定义。

- [ ] **Step 3：实现**
  - **`fillForm`**：当 `b.kind == PageEmbedded` 时：
    1. 先走 `revealCardForm` 和卡号、有效期、CVC 三个必填字段。
    2. 再按国家、州/省、城市、邮编、姓名、街道地址的顺序处理可选字段：第一个字段最多等 `addressWait`，之后每个只看当下有没有。
    3. 出现的照常用 `fillOne` 填写；没出现的记进 `Absent`，不报错。不点手动地址入口。
    4. 之后的 `disableLinkSave`、`fillEmail`、`checkPhone`、`checkLinkModal`、`acceptTerms`、`recheckFields` 与托管页相同，它们在元素缺失时本来就跳过。
  - **`PayButtonState`（内嵌页）**：数主页面可见的 `button[type="submit"]`。
    - 正好 1 个：按现有的「可点击 / 存在但不可点击」报告。
    - 0 个：报「未找到」。
    - 多于 1 个：报 `不唯一（N 个）`。

- [ ] **Step 4：运行确认通过**
  Run：`cd backend && go test ./internal/paybrowser/ -v`
  Expected：全部 PASS，含托管页的全部填表测试。

- [ ] **Step 5：提交** `feat(paybrowser): 内嵌结账页填表，地址字段可选`

---

### Task 5：内嵌页点击与等结果

**Files：**
- Modify：`backend/internal/paybrowser/page.go`（`Submit`、`WaitOutcome`）、`backend/internal/paybrowser/embedded.go`
- Test：`backend/internal/paybrowser/embedded_test.go`

**Interfaces：**
- Consumes：Task 3、Task 4。
- Produces：内嵌页的 `WaitOutcome` 在主页面路径不再以 `/checkout/` 开头时立即返回 `(OutcomeUnknown, "页面已离开结账页", nil)`。

- [ ] **Step 1：写失败的测试**
  ```go
  // TestEmbeddedSubmitOnceAndLeave：url("leave=1")，Fill 后
  b.Submit(ctx) == nil
  out, msg, err := b.WaitOutcome(ctx, 20*time.Second)  // OutcomeUnknown, "页面已离开结账页", nil，耗时 < 10 秒
  len(f.payments()) == 1；f.doneVisits() == 1
  errors.Is(b.Submit(ctx), paybrowser.ErrAlreadySubmitted)；len(f.payments()) 仍为 1

  // TestEmbeddedSubmitRefusesSecondButton：url("rerender=1")，Fill 后等 2 秒
  b.Submit(ctx) 返回错误，含 "不唯一"；b.LockSubmit() == false（证明没点）；len(f.payments()) == 0

  // TestEmbeddedChineseDeclineIsUnknown：url("decline=" + url.QueryEscape("你的银行卡被拒绝"))，Fill、Submit 后
  out, _, err := b.WaitOutcome(ctx, 3*time.Second)  // OutcomeUnknown, err == nil

  // TestEmbeddedChallenge：url("challenge=1")，Fill、Submit 后
  b.WaitOutcome(ctx, 15*time.Second) 返回 OutcomeChallenge
  ```

- [ ] **Step 2：运行确认失败**
  Run：`cd backend && go test ./internal/paybrowser/ -run 'TestEmbeddedSubmit|TestEmbeddedChineseDecline|TestEmbeddedChallenge' -v`
  Expected：`TestEmbeddedSubmitOnceAndLeave` 等满 20 秒才返回；`TestEmbeddedSubmitRefusesSecondButton` 点到了按钮。两者都 FAIL。

- [ ] **Step 3：实现**
  - **`Submit`（内嵌页）**：在 `waitFind` 里改用「主页面可见的 `button[type="submit"]` 正好一个且可用」。
    - 数量不是 1 时，不置位 `submitted`，返回 `支付按钮不唯一（找到 N 个），没有点击`。
    - 其余流程（`scanBeforeClick`、先置位再点）不变。
  - **`WaitOutcome`（内嵌页）**：poll 的每一轮先读主页面 `location.pathname`。
    - 不以 `/checkout/` 开头时结束等待，返回规定的结果。
    - 以 `/checkout/` 开头时，照现有的 `outcome` 判断。

- [ ] **Step 4：运行确认通过**
  Run：`cd backend && go test ./internal/paybrowser/ -v`
  Expected：全部 PASS。

- [ ] **Step 5：提交** `feat(paybrowser): 内嵌结账页只点唯一按钮，离开结账页即转确认`

---

### Task 6：引擎拼内嵌页链接、白名单、Cloudflare、保存卡

**Files：**
- Modify：`backend/internal/payengine/worker.go`（`createLink`、`trustedCheckoutHosts` 相关的 `checkCheckoutURL`、`openPage`）
- Modify：`backend/internal/payengine/pay.go`（`fillCard`）、`backend/internal/payengine/preview.go`（`fillOnly` 参数改名为 `ensureNoSave`）
- Test：`backend/internal/payengine/checkout_url_test.go`；新建 `backend/internal/payengine/embedded_test.go`

**Interfaces：**
- Consumes：Task 1 的 `Checkout.Tag`、`Checkout.ProcessorEntity`；Task 2 的 `checkoutPageKind`、`fakeBrowser.openKind`；Task 3 的 `paybrowser.ErrCFChallenge`。
- Produces：
  ```go
  var (
      processorEntityRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
      checkoutSessionRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)
  )
  func embeddedCheckoutURL(processorEntity, sessionID string) (string, error) // 不合格返回 checkout_failed 的 stepFailure
  ```

- [ ] **Step 1：写失败的测试**
  - **`TestCheckCheckoutURL`**：通过列表加入 `https://chatgpt.com/checkout/openai_llc/cs_live_abc`。
  - **`untrustedCheckoutURLs`**：删掉原来那条 chatgpt.com 链接，并加入：
    - `https://chatgpt.com/checkout/openai_llc`
    - `https://chatgpt.com/checkout/a/b/c`
    - `https://chatgpt.com/backend-api/x/y`
    - `https://chatgpt.com/checkout/openai_llc/cs_live_abc?x=1`
    - `https://chatgpt.com/checkout/openai_llc/cs_live_abc#f`
    - `https://www.chatgpt.com/checkout/openai_llc/cs_live_abc`
    - `https://chatgpt.com.evil.example/checkout/openai_llc/cs_live_abc`
    - `http://chatgpt.com/checkout/openai_llc/cs_live_abc`
    - `https://chatgpt.com:443/checkout/openai_llc/cs_live_abc`
    - `https://chatgpt.com/checkout/OpenAI/cs_live_abc`
    - `https://chatgpt.com/checkout/openai_llc/cs%2Flive`

    现有的泄露检查（说明里不得含 `cs_live_abc`）照样适用。
  - **`TestEmbeddedCheckoutURL`**：
    - `embeddedCheckoutURL("openai_llc","cs_live_abc")` 返回 `https://chatgpt.com/checkout/openai_llc/cs_live_abc`；
    - 对 `("", x)`、`("a/b", x)`、`("..", x)`、`("OpenAI", x)`、`(x, "cs/1")`、`(x, "cs?1")`、`(x, "cs#1")`、`(x, "cs 1")`、`(x, "")` 返回 `checkout_failed`，说明里不含会话号。
  - **`TestEmbeddedSessionOpensChatGPTPage`**（用引擎 harness）：
    - 设 `h.gpt.checkout = chatgpt.Checkout{SessionID: "cs_live_abc", Tag: chatgpt.CheckoutTagCustom, ProcessorEntity: "openai_llc"}`，试运行单。
    - 期望：单据 `previewed`；`o.CheckoutURL == "https://chatgpt.com/checkout/openai_llc/cs_live_abc"`；浏览器的 `openKind == paybrowser.PageEmbedded`。
  - **`TestEmbeddedSessionBadEntityAbortsBeforeFunding`**：
    - `ProcessorEntity: "a/b"`，真实单。
    - 期望：`assertNotSubmitted(t, o, "checkout_failed")`；`h.pool.total() == 0`；没启动浏览器。
  - **`TestOpenCloudflareAbortsBeforeFunding`**：
    - 假浏览器的 `openErr = fmt.Errorf("x: %w", paybrowser.ErrCFChallenge)`，真实单。
    - 期望：错误码 `cf_challenge`；告警一次，内容含「出口 IP」；卡池零调用。
  - **`TestEmbeddedRealOrderEnsuresNoSavePayment`**：
    - 内嵌会话的真实单走到提交。
    - 期望：假浏览器的 `EnsureNoSavePayment` 被调用 1 次。
    - 同样的托管页开通单调用 0 次。
  - **`TestEmbeddedPreviewFillEnsuresNoSavePayment`**：试运行到填表的内嵌单，`EnsureNoSavePayment` 被调用 1 次。

- [ ] **Step 2：运行确认失败**
  Run：`cd backend && go test ./internal/payengine/ -run 'TestCheckCheckoutURL|TestEmbedded|TestOpenCloudflare|TestUntrusted' -v`
  Expected：FAIL（chatgpt.com 链接被拒，`embeddedCheckoutURL` 未定义）。

- [ ] **Step 3：实现**
  - **`createLink`**（订阅分支）：
    - `co.URL` 非空：照旧。
    - `co.URL` 为空且 `co.Tag == chatgpt.CheckoutTagCustom`：用 `embeddedCheckoutURL` 拼链接，再交给 `acceptCheckoutURL`。
    - 两者都不满足：照旧报「创建支付会话未返回支付链接」。
  - **`checkCheckoutURL`**：主机名是 `chatgpt.com` 时，额外要求：
    - `u.RawQuery == ""`、`u.Fragment == ""`；
    - `u.RawPath == ""`，即路径里没有转义字符；
    - 路径按 `/` 切开后正好是 `["", "checkout", entity, session]`，两段分别匹配上面两个正则。
    - 不合格时报 `checkout_url_untrusted`，说明里只写域名。
  - **`openPage`**：`Open` 报错且 `errors.Is(err, paybrowser.ErrCFChallenge)` 时：
    - `s.opts.Alert(付款单 X（地区 Y）打开支付页被 Cloudflare 拦截，请检查该地区代理的出口 IP；换会话没有用。)`；
    - 返回 `abort("cf_challenge", "打开支付页被 Cloudflare 拦截，请检查该地区代理的出口 IP")`。
    - 其余错误照旧记 `page_failed`。
  - **`fillCard`、`previewFill`**：`j.o.Kind == KindCredit || checkoutPageKind(j.o.CheckoutURL) == paybrowser.PageEmbedded` 时调用 `noSavePayment`。确认不了照旧记 `save_payment_unconfirmed`。
  - 更新 `trustedCheckoutHosts` 上方的注释，写明 chatgpt.com 只放行哪种路径。

- [ ] **Step 4：运行确认通过**
  Run：`cd backend && go test ./internal/payengine/ -v 2>&1 | tail -5`
  Expected：`ok`，全部 PASS。

- [ ] **Step 5：提交** `feat(payengine): 订阅开通支持 chatgpt.com 内嵌结账页`

---

### Task 7：金额下限与必填预期金额

**Files：**
- Modify：`backend/internal/payengine/config.go`（`ResolvePlan`、`RegionConf` 注释）
- Modify：`backend/internal/payengine/worker.go`（`job` 加 `expectMinor int64`；`createLink` 记下；`openPage` 判下限）
- Modify（机械补 `ExpectMinor`，值不大于该测试读到的页面金额）：
  - `backend/internal/payengine/service_test.go`、`fund_limit_test.go`、`config_test.go`、`settle_test.go`、`pay_test.go`
  - `backend/internal/handler/pay_test.go`、`local_cdk_env_test.go`、`cardpool_selection_test.go`、`provider_admin_test.go`、`fund_limits_test.go`
  - 以及运行后报「未配置预期金额」的其它测试
- Test：新建 `backend/internal/payengine/amount_floor_test.go`

**Interfaces：**
- Consumes：无。
- Produces：
  - 错误码 `amount_below_expected`；
  - `ResolvePlan` 在非试运行时，`rc.ExpectMinor <= 0` 返回 `unavailable("套餐 %s 在地区 %s 未配置预期金额", plan.Label, region)`；
  - 常量 `expectFloorPct = 95`。

- [ ] **Step 1：写失败的测试**（`amount_floor_test.go`；harness 的套餐配 `ExpectMinor: 10000, CapMinor: 12000`，页面金额由假浏览器给出，币种 USD）
  ```go
  // TestAmountBelowExpectedAbortsBeforeFunding：页面 9499 → 真实单以 amount_below_expected 结束，h.pool.total()==0，
  //   ErrorMessage 含 "94.99" 和 "100.00" 和 "档位"
  // TestAmountAtFloorProceeds：页面 9500 与 10000 → 真实单走到注资（h.pool 有 Acquire 调用）
  // TestDryRunBelowExpectedOnlyWarns：试运行，页面 5000 → previewed，时间线有一条 warn 含 "低于预期"
  // TestDryRunWithoutExpectSkipsFloor：试运行、ExpectMinor 0 → previewed，时间线有 "未配置预期金额，跳过下限检查"
  // TestResolvePlanRequiresExpect（config_test.go）：Enabled、Cap、Fund 齐全但 ExpectMinor 0
  //   → ResolvePlan(.., false) 返回 ErrPlanUnavailable，消息含 "未配置预期金额"；ResolvePlan(.., true) 不报错
  ```

- [ ] **Step 2：运行确认失败**
  Run：`cd backend && go test ./internal/payengine/ -run 'TestAmountBelow|TestAmountAtFloor|TestDryRun.*Expect|TestResolvePlanRequiresExpect' -v`
  Expected：FAIL（9499 照常注资；ResolvePlan 不报错）。

- [ ] **Step 3：实现**
  - **`ResolvePlan`**：在非试运行分支加入上面的检查，放在 `CapMinor` 检查之前。
  - **`createLink`**：把 `ResolvePlan` 返回的 `rc.ExpectMinor` 存进 `j.expectMinor`。订阅、点数两个分支都要存。
  - **`openPage`**：在超上限检查之后加入下限检查：
    - **真实单**：`j.expectMinor > 0 && minor*100 < j.expectMinor*expectFloorPct` 时，返回
      `abort("amount_below_expected", "支付页金额 %s 低于预期 %s %s 的 95%%（可能选错了档位或带了优惠），已放弃，未注资", text, formatMinor(j.expectMinor, cur), cur)`。
    - **试运行**：偏低时只记一条 warn 事件；`j.expectMinor == 0` 时记一条 info「未配置预期金额，跳过下限检查」。
  - 更新 `RegionConf` 注释：预期金额现在也用作付款下限。
  - 运行全量后端测试，按报错给各测试的套餐配置补 `ExpectMinor`。

- [ ] **Step 4：运行确认通过**
  Run：`cd backend && go build ./... && go vet ./... && go test ./... 2>&1 | grep -v '^ok' | head`
  Expected：没有输出，即全部 `ok`。

- [ ] **Step 5：提交** `feat(payengine): 付款金额低于预期 95% 时在注资前放弃，预期金额改为必填`

---

### Task 8：后台界面：订阅预期金额列与新错误码说明

**Files：**
- Modify：`frontend/src/views/admin/MarsWallexPay.vue`
  - 订阅套餐表格在「币种」与「应付金额上限」之间加「预期金额」列，写法照 `PayCreditPlans.vue` 第 25–29 行；
  - 第 63 行「预期金额」的显示去掉 `isCreditPlan(planConf) &&` 这个条件；
  - 真实付款确认框第 558 行的文案加上预期金额。
- Modify：`frontend/src/views/admin/components/PayCreditPlans.vue` 第 6–7 行说明：「只用来核对配置」改为「付款时低于它的 95% 会在注资前放弃」。
- Modify：`frontend/src/lib/payui.ts`：加 `amount_below_expected: '支付页金额低于该套餐预期金额的 95%（可能选错了档位或带了优惠），已放弃（没有注资）'`。
- Test：`frontend/src/lib/__tests__/payui.test.ts`、`frontend/src/views/admin/__tests__/pay.spec.ts`

**Interfaces：**
- Consumes：`frontend/src/lib/pay.ts` 已有的 `FormRow.expect`（订阅行和点数行都有）。

- [ ] **Step 1：写失败的测试**
  - `payui.test.ts`：`payErrorText('amount_below_expected')` 含「95%」和「没有注资」。
  - `pay.spec.ts`：渲染付款配置页后，订阅套餐表格的表头文字含「预期金额」，参照该文件现有的渲染方式。

- [ ] **Step 2：运行确认失败**
  Run：`cd frontend && npx vitest run src/lib/__tests__/payui.test.ts src/views/admin/__tests__/pay.spec.ts`
  Expected：FAIL。

- [ ] **Step 3：实现上面的模板与文案改动**

- [ ] **Step 4：运行确认通过**
  Run：`cd frontend && npx vitest run && npm run build`
  Expected：全部 PASS，构建成功。

- [ ] **Step 5：提交** `feat(admin): 订阅套餐可配预期金额，新增金额偏低的错误说明`

---

### Task 9：文档与全量门禁

**Files：**
- Modify：`CHANGELOG.md`「## 未发布」
  - 「新增」：订阅开通支持 chatgpt.com 内嵌结账页。
  - 「变更」：金额低于预期 95% 时在注资前放弃。
  - 「升级注意」：订阅套餐必须在后台填「预期金额」，没填的套餐不能下单、不能发码。
- Modify：`docs/superpowers/specs/2026-11-03-embedded-checkout-design.md`：状态改为「已定稿，实施中」。

- [ ] **Step 1：写更新日志与文档改动**
- [ ] **Step 2：全量门禁**
  Run：`cd backend && go build ./... && go vet ./... && go test ./... && cd ../frontend && npx vitest run && npm run build`
  Expected：全部通过。
- [ ] **Step 3：提交前查敏感信息**
  Run：`git diff origin/master --stat && git diff origin/master | grep -nE 'cs_live_[A-Za-z0-9]{6,}|eyJ[A-Za-z0-9_-]{20,}|sk_live|pk_live'`
  Expected：第二条命令没有输出。
- [ ] **Step 4：提交** `docs: 记录内嵌结账页与金额下限`

---

### Task 10：本机实测（不付款）

不提交代码，只出结论。用本机临时实例 18090（`$S=/private/tmp/claude-501/-Users-zhengshan-projects-aisubproxy/925aae1a-228d-4187-b245-452298366b9f/scratchpad/local-run`）和免费号会话 `$S/session.json`。

- [ ] **Step 1：用本分支重新编译并重启 18090**
  - 停掉后台任务 b6a6190d5。
  - 执行 `cd backend && go build -o $S/bin/server ./cmd/server`。
  - 用 `$S/env.sh` 重新启动。
- [ ] **Step 2：在后台 API 给 pro_5x 的 US 地区配置参数**
  - 启用；
  - `expect_minor` 10000；
  - `cap_minor` 11000；
  - `fund_cents` 按 MarsWallex 汇率填一个占位值（试运行不注资）。
- [ ] **Step 3：试运行**
  Run：`python3 -I $S/run_dry.py $S pro_5x US`
  Expected：`previewed`，金额原文 `100.00 USD`，开页截图正常。
- [ ] **Step 4：试运行到填表**
  Run：`python3 -I $S/run_dry.py $S pro_5x US fill`
  Expected：`previewed`，看以下几项：
  - 填表报告：卡号、有效期、CVC 都命中，「页面没有的字段」列出了哪些；
  - 保存卡：「未勾选」或「已取消」；
  - 支付按钮：「可点击」；
  - 填表后复读金额一致。
- [ ] **Step 5：清理**
  - 删除 `$S/dry_*.json`，里面有账号邮箱。
  - 把结论（页面上实际出现了哪些字段、有没有 Link 验证）写进设计文档 §8，提交 `docs: 记录内嵌结账页试运行结果`。
  - 真实付款不在本计划内，要用户另行明确同意。
