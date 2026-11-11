# 子项目 C 调研：已有订阅升级与 Codex 点数，ChatGPT 实际怎么收钱

- 日期：2026-11-03
- 性质：只读调研。没有登录任何账号，没有用任何会话或 token，没有调用需要登录的接口，没有改仓库代码。
- 证据等级（每条结论都标）：
  - **【官方】**：OpenAI 或 Stripe 官方文档。
  - **【前端】**：ChatGPT 公开静态 JS 包里的字符串，注明文件和片段。
  - **【社区】**：社区帖子、第三方文章，附链接。
  - **【旁证】**：仓库里 ZovoCard 文档和代码的描述。
  - **【推测】**：没有直接证据。

## 0. 证据来源与局限

- **chatgpt.com 页面和静态资源**：curl 直接访问返回 Cloudflare 挑战页（403），没有绕过。
- **获取路径**：
  - 页面 HTML 取自 Wayback Machine 存档 `web.archive.org/web/20261101225650id_/https://chatgpt.com/codex/settings/usage`。
  - 从存档里拿到路由清单名 `manifest-13462013.js`。
  - 再从公开静态 CDN `cdn.oaistatic.com/assets/...` 匿名下载清单和相关分块。
  - `cdn.oaistatic.com` 不需要登录，也没有挑战页。
- **存放位置**：
  - 下载的文件：`/private/tmp/claude-501/-Users-zhengshan-projects-aisubproxy/925aae1a-228d-4187-b245-452298366b9f/scratchpad/c-research/bundle/`。主包在 `js/`，异步分块在 `async/`，分块映射表是 `chunkmap.txt`。
  - 解析脚本：`.../scratchpad/c-research/scripts/`。
  - 这些文件只当数据看，没有执行。
- **前端版本**：路由清单 `version:'13462013'`，Wayback 存档时间为 2026-11-02。
- **帮助中心**：help.openai.com 对 WebFetch 返回 403。帮助中心的内容只能取自搜索引擎摘要，等级按【官方（摘要）】标，原文没有亲眼看到。
- **最大的局限**：前端代码只能说明「网页怎么调用」，不能证明服务器对非常规调用（比如已有订阅的账号直接调 checkout）的反应。很多分支还受 Statsig 实验开关控制，不同账号看到的流程可能不同。这些都列进 §5 的试运行清单。

---

## 1. Codex 点数怎么买

### 1.1 购买入口

| 结论 | 证据 |
|---|---|
| 入口在 ChatGPT 网页的 Settings > Usage（Codex 用量页），也可以在 Codex 应用的 Usage & Billing 买。触发用量上限时也会弹出「添加点数」。 | 【官方（摘要）】help.openai.com/en/articles/12642688（搜索摘要原文："If you hit a usage limit in Codex on Plus or Pro, you'll see an option to add credits. You can also buy credits from Codex Settings > Usage > Credits"）；【社区】https://www.codexusage.dev/credits 、https://pinkcard.cc/en/guides/chatgpt-codex-credits-payment/ |
| 网页里与点数相关的路由有 `/pricing/credits`、`/codex/purchase/:plan`、`/payments/success-credit-purchase`，都由 `790996.ce621a7a29.js` 加载。 | 【前端】`manifest-oai.js` 里的 `'checkout/pricing/credits'`、`'checkout/codex/purchase/:plan'`、`'checkout/payments/success-credit-purchase'` |
| 个人点数购买的入口标识有 `codex_purchase_credits_route`、`codex_credit_purchase_modal`、`codex_inline_credit_purchase_modal`。 | 【前端】`js/418851.3c820e5cc6.js`：`s:function(e){return"credit_purchase_modal"===e?"workspace":"codex_purchase_credits_route"===e||"codex_credit_purchase_modal"===e||"codex_inline_credit_purchase_modal"===e?"personal":"unknown"}` |

### 1.2 后端接口

**和开通订阅是同一个接口** `POST /backend-api/payments/checkout`，只是请求体不带 `plan_name`，改带 `credit_purchase_data`。【前端】

`js/418851.3c820e5cc6.js` 的模块 `am1R`（原样摘录）：

```js
a:{creditQuantityStep:250,minimumChargeMinorUnits:1e3,presetCreditQuantities:[500,1e3,1500,2500,5e3,1e4]},
c:function(e){let{usageRequestId:t,accountId:_,quantity:r,discountOfferId:E,entryPoint:n,checkoutUiMode:a,billingDetails:i,cancelUrl:o}=e;
  return{checkout_ui_mode:null!=E?"custom":a,entry_point:n,
         credit_purchase_data:{account_id:_,usage_request_id:t,quantity:r,unit:"credit",discount_offer_id:E},
         billing_details:i,cancel_url:o}},
d:function(e,t){if("workspace"===e)return"1243009938";switch(t){case"plus":…case"prolite":…case"pro":…case"promax":…default:return null}}
```

**返回值有两种形态**。`js/767086.3dca15bcfe.js` 是工作区点数的代码，个人点数同理：

```js
"tag"in e&&"custom_checkout_session"===e.tag ? 打开 /checkout/${e.processor_entity}/${e.checkout_session_id}
                                             : null!=e.url && 打开 e.url
```

- **`custom_checkout_session`**：ChatGPT 自己的结账页 `chatgpt.com/checkout/{openai_llc|openai_ie}/{cs_…|oaics_…}`。
  - 页面里嵌 Stripe 的 PaymentElement、AddressElement、ExpressCheckoutElement（iframe）。
  - 确认付款走 `POST /payments/checkout/confirm`。
  - 证据：【前端】`async/111951.5243187031.js` 里有 `"/payments/checkout/confirm"`、`createConfirmationToken`，`PaymentElement` 出现 16 次。
- **`url`**：跳转结账（redirect）。早期原型（已从仓库删除）记录的支付链接形态是 `https://pay.openai.com/c/pay/cs_live_...`，即 Stripe 托管的 Checkout 页。【旁证】

**用哪种由请求里的 `checkout_ui_mode` 决定**。【前端】`js/418851…` 的函数 `H`：

```js
if(null!=e.credit_purchase_data)return t.isStandaloneCreditCustomModeEnabled?.()?"custom":"redirect";
```

也就是说，点数默认的 UI 模式取决于实验开关，而且客户端可以显式传 `checkout_ui_mode`。工作区点数的代码在没有优惠时传 `"redirect"`。**服务器是否接受个人点数显式传 `"redirect"`，要试运行确认（§5 T1）。**

**相关的其他接口**（全部【前端】）：

| 接口 | 作用 | 文件 |
|---|---|---|
| `POST /payments/checkout/credits/quote` | 点数报价 | `async/111951…` |
| `GET /payments/checkout/{processor_entity}/{checkout_session_id}?payment_state_source=live` | 查结账会话状态，字段有 `status`、`payment_status`、`credit_payment_state.payment_intent_status` | `async/350075.d6b168336b.js` |
| `POST /payments/credit_purchase/payment_method` | 用已存卡买点数前登记同意，请求体 `consent_version:"credit_purchase_v1"` | `async/350075…` |
| `GET /subscriptions/credits/discount-offer` | 点数优惠 | `js/767086…` |
| `/wham/usage` | 疑似 Codex 用量与余额接口，返回结构未知 | `js/` 字符串 |

### 1.3 付款方式

**结论：一次性付款，走结账会话；可以填新卡，也可以选已存卡，不是直接静默扣已存卡。** 自动充值除外，它在后台扣默认卡。

- **新卡**：自定义结账页上用 Stripe PaymentElement 填新卡。
  - 新卡有一个「保存」勾选框，文案是 `Save payment details to {merchantName} for future purchases.`
  - 该文案的说明原文："Checkbox label in credit and subscription checkout when a customer enters a new card … separate from Stripe Link consent and subscription renewal"。
  - 证据：【前端】`async/111951…`。
- **已存卡**：确认请求体支持 `saved_payment_method`（`saved_payment_method_id`）和 `custom_payment_method_card`（`new_card_payment_method_id`）两种。
  - 证据：【前端】`js/418851…`：`case"saved_payment_method":return{checkout_session_id:e,…,selected_payment_method_type:"card",saved_payment_method_id:t.savedPaymentMethodId}`。
- **页内快捷购买（inline）**：Codex 用量页的快捷购买会读 `inline_credit_saved_card_contract.payment_methods`，默认选一张已存卡，再走结账会话确认。
  - 证据：【前端】`async/111951…`、`async/350075…`。
  - 第三方文章说「The purchase uses your default payment method」，指的就是这种体验。【社区】pinkcard 文章。
- **自动充值（auto top-up）**：余额低于阈值时，后台用默认付款方式按差额扣款。
  - 证据：【官方（摘要）】同上帮助中心文章；【社区】codexusage.dev 原文："OpenAI bills your default payment method for the exact amount needed to reach your target"。

### 1.4 是否要求已有生效订阅

**结论：个人账号基本要求 Plus 或 Pro（prolite / pro / promax）。Free 和 Go 只有灰度用户能买。**

- 【前端】`am1R.d` 只给 `plus/prolite/pro/promax` 返回实验层，其它都返回 `null`。
- 【前端】`async/350075…` 的资格判断：
  - 账号必须是 `personal`。
  - 套餐不满足时，只有 Free / Go 才去查 `/pageConfigs/usage_limits` 的 `show_buy_credits`，或学生活动资格。
  - 都不满足就放弃，并提示 `"The signed-in account does not have permission or eligibility to buy prepaid credits"`。
- 【官方（摘要）】"Free and Go users are prompted to upgrade to Plus instead of adding credits for Codex"。
- 【旁证】ZovoCard：`GPT_CREDIT_REQUIRES_SUBSCRIPTION`，「点数是订阅的加购项，目标账号必须已有生效中的 Plus/Pro 订阅」（`docs/zovocard-openapi-zh.md:1123-1126`）。

### 1.5 地区、币种、金额、税

**币种跟着账号现有订阅的计费币种走，不能随意选地区。**【前端】`async/350075…`：

```js
let L=_??(0,K.c)(P,{country:C,localCurrency:O,useSubscriptionCurrency:!N,…}),
    B=(0,K.b)(_?.currency)??("personal"!==j||N?L.currency:(0,K.b)(P.entitlement.billing_currency??"USD"));
if(null==B)throw Error("Credit pricing currency is unavailable");
```

个人有订阅的账号（N 为假）取 `entitlement.billing_currency`，没有就用 USD。所以本站码上写的「付款地区」必须和用户账号的计费币种一致。不一致时，网页本身就不会按我们选的币种报价。服务器是否接受 `billing_details` 改币种未知（§5 T2）。

**单价**：`GET /checkout_pricing_config/configs/{country_code}` 返回的 `currency_config.amount_per_credit` 和 `minor_unit_exponent`，按国家或币种分别配置。【前端】`async/111951…`：

```js
eA.object({amount_per_credit:eA.number().positive(),minor_unit_exponent:…})
```

**数量规则**（个人，【前端】`am1R.a`）：

- 步长 250 点。
- 最低收费 1000 个最小单位（USD 即 $10）。
- 预设档：500、1000、1500、2500、5000、10000。
- 上限来自动态配置，代码里看不到具体值。
- ZovoCard 卖到 credit25000，说明上限不低于 25000。

**已知价格**：

| 币种 | 价格 | 证据 |
|---|---|---|
| USD | 1000 点 $40（每点 $0.04；250 点正好 $10，和最低收费吻合） | 【官方（社交账号）】https://x.com/OpenAIDevs/status/1983956898786267619（2025 年，可能已变） |
| PHP | 每点 ₱2.26（250 点 ₱565） | 【旁证】`docs/zovocard-openapi-zh.md:1102-1107`、`handler/cardplatform_cdk.go:1258-1278` |

**税**：

- 结账会话里另算，相关接口和字段有 `POST /payments/checkout/taxes`、`amount_due.tax_amount`、`amount_excluding_tax`。【前端】
- 价格配置的 schema 有 `tax: "inclusive"|"exclusive"`。【前端】`async/544560…` 模块 `ra39`。
- ZovoCard 的 50x 数据显示，PH 含税展示价 ₱32,490，免税价 ₱29,008.93，正好差 12% 增值税。所以账单地址（美国免税州）会影响是否含税。点数是否同样如此，要试运行（§5 T3）。【旁证】`docs/zovocard-cdk-zh.md:283`

**有效期与退款**：点数 12 个月过期，一般不退款。【社区】codexusage.dev、pinkcard（帮助中心摘要里没有看到原文）。

---

## 2. 已有订阅升级怎么付

### 2.1 是新建结账会话，还是更新现有订阅扣已存卡

**结论：更新现有订阅，直接扣卡，没有结账页。** 前端调的是：

```
GET  /backend-api/subscriptions/update/preview?account_id=…&updated_plan=chatgptpro
POST /backend-api/subscriptions/update  {account_id, updated_plan, payment_method_id?, setup_intent_id?}
POST /backend-api/subscriptions/update/cancel_pending  {account_id, expected_payment_intent_id, pending_update_invoice_id}
```

证据：【前端】`js/767086.3dca15bcfe.js` 模块 `smwl`（原样摘录）：

```js
g:function(e){let{accountId:t,updatedPlan:n,promoCampaign:r,promoCode:a}=e;return(0,p.a)({queryKey:[...U,t,n,…],
   queryFn:e=>…S.a.safeGet("/subscriptions/update/preview",{…parameters:{query:{account_id:t,...(0,o.l)({updatedPlan:n,…})}},signal:i})…})},
h:function(e){let{accountId:t,updatedPlan:n,promoCampaign:r,promoCode:a,paymentMethodId:i,setupIntentId:l,…}=e;
   return S.a.safePost("/subscriptions/update",{…requestBody:{account_id:t,...(0,o.l)({updatedPlan:n,…}),payment_method_id:i,setup_intent_id:l},…})},
b:function(e){…return S.a.safePost("/subscriptions/update/cancel_pending",{requestBody:{account_id:t,expected_payment_intent_id:n,pending_update_invoice_id:r},…})}
```

参数名映射在 `js/418851…`：

```js
l:function(e){let{updatedPlan:t,promoCode:_,promoCampaign:r}=e;return{updated_plan:_||r?void 0:t,…}}
```

**目标套餐的取值**和 checkout 的 `plan_name` 同一套，比如降级流程写死了 `updatedPlan:"chatgptprolite"`。【前端】`async/117531.3e478e1dd2.js`

**升级确认对话框的文案**（【前端】`async/285918.168fd89f0b.js`、`async/544560.73083431dd.js`）：

- `"Total due today"`、`"Subtotal"`、`"Tax"`。
- `"Adjustment"`，说明是 "Prorated credit for the remainder of your current subscription"。
- `"Billed monthly, starting today"`，说明是 "Billing cycle information in the Pro upgrade dialog"。
- 按钮 `"Pay now"`，说明是 "Button paying for a Pro upgrade using the saved payment method"。
- 应付为 0 时按钮是 `"Confirm"`，说明是 "Button confirming a Pro upgrade with no immediate payment due"。
- 对话框里显示 `Payment method: {card_brand} •••• {last4}`。

**定价页流程**（【前端】`async/117531…`）：

1. 有订阅的账号点升级时，先取 preview。
2. 用 `e?.card ?? l.default_payment_method` 作为要扣的卡。
3. 没有卡时，先弹「添加付款方式」，然后重新 preview。
4. 弹 `kind:"saved_card_upgrade"` 对话框，确认后调 `POST /subscriptions/update`。
5. 成功后提示 `"Successfully upgraded plan"`，并刷新 `accounts/check`。

**`POST /subscriptions/update` 返回的 `status`**（【前端】`async/255055.cc48f6112f.js`）：

| status | 含义 |
|---|---|
| `null` 或 `"complete"` | 成功 |
| `"pending"` | 处理中 |
| `"payment_method_failed"` | 被拒，带 `payment_method_failure_reason`、`pending_update_payment_intent_id`、`pending_update_invoice_id`；前端会调 `cancel_pending` 清理 |
| `"setup_required"` | 需要先绑卡，带 `setup_intent_client_secret` |
| `"requires_action"` | 要 3DS，带 `payment_intent.client_secret`；前端用 `stripe.confirmCardPayment(client_secret)` 在 chatgpt.com 页面里弹 3DS |
| HTTP 409 `"Subscription update already in progress"` | 映射成 `subscription_update_pending` |

**能升级的起点套餐**：`go`、`plus`、`prolite`、`pro`。【前端】`async/451779.4179db8b3a.js`：

```js
if(d||"go"!==c&&"plus"!==c&&"prolite"!==c&&"pro"!==c)return null
```

**旁证**：

- 【社区】Tibor Blaho："Upgrading from ChatGPT Plus to Pro offers a prorated credit for the remainder of your Plus subscription"（https://x.com/btibor91/status/1864711247171617071）。
- 【社区】有人反映从 Plus 被扣 $180（= $200 − $20）升到 Pro，没有经过结账页，说明钱扣在已存卡上（https://community.openai.com/t/charged-180-for-unauthorized-upgrade-to-chatgpt-pro-plan/1128450 ）。
- 【旁证】ZovoCard 把 `pro_20x` 的流程标成 `Flow:"plus_upgrade"`（`handler/cardplatform_cdk.go:1257`），订单有 `plus_paid` 状态（`docs/zovocard-cdk-zh.md:86`）。推断它的做法是：先用新卡结账开 Plus，再用这张已存卡升级到 Pro。
- 【旁证】ZovoCard 说「升级差价由该账号实时账单决定，不能用两个套餐标价相减代替」（`docs/zovocard-cdk-zh.md:280`），对应的就是 preview 接口。

### 2.2 对已有订阅的账号调用现有 checkout 接口会怎样

**没有直接证据，只能推测。**

- 前端对有订阅的个人账号从来不走 `/payments/checkout` 升级，一律走 `subscriptions/update`。
- 服务器的反应只有三种可能：拒绝；返回一个结账会话，付款后形成第二个订阅、重复扣款；返回别的东西。
- **这个方向绝不能真实付款**，只能在试运行时只建会话、不付款，看返回（§5 T7）。现有 `check` 拒绝已有订阅的账号，这个守卫必须保留。

### 2.3 付款前从哪里读升级金额

`GET /subscriptions/update/preview` 返回的 JSON（字段名【前端】`async/285918…`、`js/418851…`）：

| 字段 | 含义 |
|---|---|
| `amount_due.amount` | 今日应付（含税），最小单位 |
| `amount_due.amount_excluding_tax`、`amount_due.tax_amount` | 不含税金额与税额 |
| `currency` | 币种 |
| `positive_line_item_total` | 新套餐金额 |
| `negative_line_item_total` | 原套餐剩余时间的按比例抵扣 |
| `renewal_date` | 下次续费日 |
| `default_payment_method` | `{card_brand, card_last4}`，preview 认为要扣的卡 |
| `applied_balance` | 用到的账户余额（钱包、礼品卡）。`n:function(e){return null!=e&&e.applied_balance<0&&0===e.amount_due.amount}` 表示余额能全额抵扣 |

**注意**：

- Stripe 按秒计算按比例金额，preview 和实际扣款之间可能有小差额。官方建议用 `proration_date` 固定计算时刻，OpenAI 的接口有没有暴露这个参数未知。【官方】https://docs.stripe.com/billing/subscriptions/prorations ，原文："Because Stripe prorates to the second, prorated amounts might change between the time they're previewed and the time the update is made."
- 前端在 preview 币种和价格配置币种不一致时直接放弃（`nC.currencyCode.toUpperCase()!==l.currency.toUpperCase()`）。**升级币种同样锁在现有订阅的币种上**，不能选地区。【前端】`async/117531…`

---

## 3. 扣已保存付款方式的相关问题（主要针对升级）

### 3.1 怎么把付款方式换成我们的卡

ChatGPT 网页现在有原生的账单设置（【前端】`async/144525.1e1581ffbb.js`、`async/93581.5a2cc5d12c.js`）：

| 接口 | 作用 |
|---|---|
| `GET /payments/payment_methods` | 列出已存卡，字段有 `payment_methods[]`、`default_payment_method_id`、`default_payment_methods` |
| `POST /payments/payment_method {account_id}` | 返回 SetupIntent 的 `client_secret`，在 chatgpt.com 页面里用 Stripe PaymentElement（setup 模式）填卡，`confirmSetup` 之后调 `POST /payments/payment_method/{id}/finalize {account_id, setup_intent_id}` |
| `POST /payments/payment_method/confirm` | 另一条 confirmation token 流程。返回 `requires_action` 时用 `stripe.handleNextAction`，即绑卡时就可能要 3DS |
| `POST /payments/payment_method/default {account_id, payment_method_id}` | 设为默认。界面文案："Make this saved payment method the default for subscription payments" |
| `POST /payments/payment_method/backup {account_id, enabled}` | **备用付款方式**。文案："Use another payment method if your default payment method fails" |
| `DELETE /payments/payment_method/{id}` | 删除卡。默认卡、自动充值卡删不掉，报 "Payment method is required by billing" |

**另外两条路**：

- **Stripe 托管的账单门户**：`GET /payments/customer_portal` 返回 `{url}`，前端只在续费失败时的 `portal_redirect` 模式里用。【前端】`async/41046.63f6c51aa9.js`
- **升级时直接指定卡**：升级接口本身支持 `payment_method_id`，不必先设默认卡。升级对话框在实验开关打开时可以「添加新卡」，用返回的 `canonical_payment_method_id` 去升级。【前端】`async/883378.13930613f9.js` 的 `PricingSubscriptionUpdateDialog`，以及 `AddPaymentMethodDialog onSuccess:e=>{let{canonical_payment_method_id:a}=e;W(a)}`

**关键约束：Cloudflare**。子项目 A 的设计（§2「生成支付链接」）记录了「浏览器访问 chatgpt.com 会被 Cloudflare 拦，原型已验证」。

- 绑卡的 PaymentElement 必须在 chatgpt.com 页面里加载（SetupIntent 属于 OpenAI 的 Stripe 账号）。如果 chromedp 的 Chrome 打不开 chatgpt.com，原生绑卡这条路就走不通。
- 那样只剩 Stripe 托管的账单门户一条路（门户在 Stripe 域名下）。它现在对个人账号是否还开放、能不能加卡和设默认，都未知（§5 T9、T10）。

### 3.2 替换以后，下个月续费会扣到我们的卡吗

- **Stripe 选卡的顺序**：订阅的 `default_payment_method` → 订阅的 `default_source` → 客户的 `invoice_settings.default_payment_method` → 客户的 `default_source`。【官方】https://docs.stripe.com/api/subscriptions/object ，原文："This takes precedence over `default_source`. If neither are set, invoices will use the customer's invoice_settings.default_payment_method or default_source."
- **ChatGPT 自己的文案**：「默认卡」用于订阅付款，开了「备用付款方式」时，默认卡失败会改扣其它已存卡。【前端】`async/144525…`
- 结账页上勾选「保存卡」时的提示原文："This card will be available for future purchases. **Subscription renewals may still charge it.**"【前端】`async/534136.64477f2f56.js`
- **`POST /subscriptions/update` 带 `payment_method_id` 会不会顺带把我们的卡设成订阅默认卡，未知。【推测】** 按 Stripe 的常见做法很可能会。要试运行确认（§5 T12）。
- **升级会让账单周期从今天重新开始**（"Billed monthly, starting today"），所以下次续费大约在 30 天后。【前端】

**结论**：只要我们的卡被设成默认卡，或者作为已存卡且开了「备用」，下次续费就可能落到我们的卡上。

### 3.3 我们的卡清零冻结后，续费失败对用户的影响

- **订阅进入欠费状态**：`GET /subscriptions?account_id=` 能看到 `is_delinquent`、`will_renew`、`became_delinquent_timestamp`、`grace_period_end_timestamp`。网页会提示 "Update payment" / "Renew your ChatGPT {planName} plan"，宽限期过后订阅失效。【前端】`async/512155.bd156c6b6a.js`、`async/255055…`、`async/41046…`。这就是 ZovoCard「宽限期原订阅处理」的来源。【旁证】`docs/zovocard-cdk-zh.md:226-232`
- **备用付款方式开着时**：续费会改扣用户自己的其它卡，而且按新的高档价格扣，比如 Pro $200。用户可能在不知情的情况下被扣大额。【推测，依据 §3.2 的文案】
- **对我们自己的影响**：
  - Stripe 的自动重试会对冻结的卡发起多次授权，产生多次拒付。
  - MarsWallex 文档里没找到拒付手续费的规定（`docs/marswallex-openapi-zh.md` 搜不到）。
  - ZovoCard 是「前 3 笔免费，第 4 笔起 $0.30」（`docs/zovocard-openapi-zh.md:32`）。
  - 拒付率也可能影响卡台账户的风控。**要向 MarsWallex 确认。【推测】**
- **顺带发现，子项目 A 也有同样的问题**：用我们的卡在结账页开通新订阅，续费同样会落到我们的卡上。ZovoCard 的做法是「下單成功時上游就會自動嘗試取消一次」自动续费（`docs/zovocard-openapi-zh.md:1362-1364`）。A 的设计里没有这一步，建议一并评估。

---

## 4. 现有流程哪些能复用

| 现有能力（代码位置） | 点数 | 升级 |
|---|---|---|
| 查账号 `chatgpt.Account`（accounts/check） | 能用。但 `Account` 结构要加 `billing_currency`、`structure`、`is_delinquent`、`purchase_origin_platform` 等字段 | 同左，还要加 `scheduled_plan_change`、`trial`、`is_active_subscription_gratis` |
| `CreateCheckout`（`chatgpt/client.go:201`） | 要改：请求体换成 `credit_purchase_data`；传 `checkout_ui_mode:"redirect"`、`entry_point`、`cancel_url`；兼容 `tag:"custom_checkout_session"`（只有会话号、没有 url）的返回 | 不能用。升级不走 checkout |
| 打开页面、读金额、复核金额（`openPage`、`recheckAmount`、`Sel.Amount`） | 如果拿到的是 pay.openai.com 托管页，基本能直接用。如果是 chatgpt.com 自定义结账页：金额和按钮在 ChatGPT 自己的 DOM 里，`Sel.Amount` 不命中，打开页面这一步就会放弃（不花钱），而且可能被 Cloudflare 拦 | 不能用。金额从 preview 接口 JSON 读，不读页面 |
| 填表（`Fill`，含选新卡、取消 Link、手动地址） | 能用。要加：取消 Stripe Checkout 可能出现的「保存支付信息」勾选，并核实它确实没勾上 | 只有「绑卡」那一步可能用到，取决于用哪个 Stripe 表单（门户或 chatgpt.com 的 PaymentElement），选择器要新写 |
| 点支付一次（`submitAndWait` + `LockSubmit` + 先写 `submitted_at`） | 能用 | 不能直接用。提交动作是一次 HTTP 调用 `POST /subscriptions/update`，要新写「先写 `submitted_at`、只调一次、超时算结果不明」的同类保证 |
| 3DS（`answerChallenge`、`ChallengeFrame`） | 能用（托管页同 A） | 要新写：`requires_action` 需要在 chatgpt.com 页面里用 Stripe.js `confirmCardPayment`。浏览器进不了 chatgpt.com 就只能转人工 |
| 选卡、注资、释放（`fundCard`、卡池） | 能用 | 能用。但卡要在账号上挂 30 天以上还是升级完立刻摘掉，要另外设计（§6） |
| 确认阶段 `verifyAccount`（看 `plan_type` 是否等于目标） | 不能用。点数不改套餐。要换成：点数余额增加，或结账会话 `payment_status=paid`，再加该卡的授权交易 | 能用（`plan_type` 变成 `pro` / `promax` 等） |
| 授权交易核对（`verifyAuth`） | 能用 | 能用，再加「授权金额和 preview 的差额在容差以内」 |
| 被拒 / 待人工的定论表 | 能用 | 大体能用。要加 `pending`、`setup_required`、`subscription_update_pending` 等状态的处理，以及调 `cancel_pending` 清理 |

---

## 5. 必须用测试账号试运行的未知项

前面打了「不付款」标记的项，只建会话或只读，不会扣钱。打了「小额真实付款」的项会动钱，要用户逐项确认后再做。

| # | 怎么试 | 观察什么 | 看到什么说明是哪种情况 |
|---|---|---|---|
| T1 | 不付款。在 Plus 测试号上调 `POST /payments/checkout {credit_purchase_data:{account_id,quantity:250,unit:"credit"}, entry_point:"codex_purchase_credits_route", checkout_ui_mode:"redirect"}`，再不带 `checkout_ui_mode` 调一次 | 返回体里有没有 `url`、`tag`、`processor_entity`、会话号前缀 | 有 `url` 且域名是 pay.openai.com：可以复用 A 的托管页流程。只有 `tag:custom_checkout_session`：要支持 chatgpt.com 自定义结账页，先看 T10 |
| T2 | 不付款。用计费币种分别是 USD 和 PHP 的两个账号，带或不带 `billing_details:{country:"PH",currency:"PHP"}` | 结账页显示的币种 | 始终是账号的计费币种：码的地区必须按账号币种定。能改：可以按码的地区收 |
| T3 | 不付款。在 T1 的页面上读金额，填美国免税州地址后再读一次 | 两次金额是否不同、有没有税行 | 不同：地址影响税，复核金额必须保留 |
| T4 | 不付款。T1 的页面 | 有没有已存卡、新卡入口文字、「保存卡」勾选框及默认状态、Link 区块 | 决定 `NewCardOption` 和新的「不保存」选择器怎么写 |
| T5 | 小额真实付款（250 点，约 $10）。付款前后各调一次 `GET /payments/payment_methods` | 我们的卡尾号有没有出现，`default_payment_method_id` 有没有变 | 没出现：点数续费风险可忽略。出现了：必须删卡，删之前要用户同意 |
| T6 | 不付款。付款前后读 `/wham/usage` 等疑似余额接口 | 返回结构里点数余额字段 | 确认阶段用它判断「到账」 |
| T7 | 不付款，只建会话。在 Plus 测试号上调 `POST /payments/checkout {plan_name:"chatgptpro"}` | HTTP 状态码、错误码 | 被拒：现有守卫足够。返回了会话：绝不能付，否则会出现第二个订阅 |
| T8 | 不付款。`GET /subscriptions/update/preview?account_id=…&updated_plan=chatgptpro`，再试 `chatgptprolite`、`chatgptpromax` | 字段是否和 §2.3 一致；`updated_plan` 接受哪些值；`default_payment_method` | 字段齐全：金额上限可以按 preview 设 |
| T9 | 不付款。`GET /payments/customer_portal` | 有没有 `url`、域名、门户里能不能加卡、能不能设默认、会不会显示「更新套餐」 | 能用：绑卡可以走 Stripe 托管页。返回 4xx 或没有加卡功能：只剩 chatgpt.com 原生绑卡 |
| T10 | 不付款。用和 `paybrowser.Launch` 相同参数的 Chrome，注入会话 cookie，打开 `chatgpt.com/` 和账单设置页 | 是否出现 Cloudflare 挑战，能否正常渲染 | 被拦：chatgpt.com 自定义结账页、原生绑卡、升级 3DS 都做不了 |
| T11 | 小额真实付款（可能有 $0 或 $1 验证授权）。绑一张注资过的 MarsWallex 卡 | 绑卡时有没有授权交易、有没有 3DS、绑完是否自动变成默认卡 | 决定绑卡要不要接 3DS，以及要不要事后恢复用户原默认卡 |
| T12 | 小额真实付款（Go→Plus 是最便宜的组合）。`POST /subscriptions/update {updated_plan, payment_method_id:我们的卡}` | 付款前后的 `default_payment_method_id`，订阅的扣款卡（看 preview 的 `default_payment_method`） | 默认卡变成了我们的卡：升级后必须把默认卡改回用户原来的卡，或者关掉续费 |
| T13 | 同 T12 | 实际返回的 `status`；有没有 `requires_action`；卡池授权金额和 preview 的差额 | 决定容差和 3DS 怎么处理 |
| T14 | 小额，可选。故意用余额不足的卡升级 | 返回 `payment_method_failed` 后，调和不调 `cancel_pending` 时，之后 `GET /subscriptions` 和发票的状态 | 不清理会有残留发票被重试：失败后必须调 `cancel_pending` |
| T15 | 同 T12 | `GET /subscriptions` 的 `renews_at`、`billing_period` | 确认账单周期是否重置为 30 天 |
| T16 | 不付款 | 账单设置里「备用付款方式」开关的默认状态 | 默认开着：续费失败会落到用户别的卡上，兑换页要告知 |
| T17 | 问卡台，不涉及 ChatGPT | MarsWallex 对拒付授权是否收费；拒付率阈值 | 收费：续费重试打到冻结卡上会持续产生费用 |
| T18 | 不付款 | tls-client 不带前端那组附加头（前端对 checkout、绑卡、checkout confirm 会附加 `chatgpt_checkout`、`chatgpt_add_card` 等完整性头）时，各接口是否被拒 | 被拒：这些步骤只能放进浏览器里执行 |

---

## 6. MarsWallex 通道设计草图

### 6.0 总体建议

**点数先做，升级后做。** 点数基本能复用 A 的托管结账页流程。升级则要改用户的账单设置，续费归属也有风险，前置未知项太多。

### 6.1 点数（`kind=credit`）

**状态机**：沿用 A 的状态，加 `kind` 列和点数参数（`credit_quantity`）。

| 状态 | 和 A 的差异 |
|---|---|
| `checking` | 必须 `has_active_subscription` 且 `plan_type ∈ {plus, prolite, pro, promax}`；`structure=personal`；`billing_currency` 等于码的地区币种；不欠费。另外记下点数余额快照和已存卡列表快照（尾号与 id），供确认阶段和事后核对 |
| `link_created` | `POST /payments/checkout`，带 `credit_purchase_data`（`quantity` 取码上的档位）、`checkout_ui_mode:"redirect"`。只接受返回 `url` 且域名在白名单（pay.openai.com）里；返回 `custom_checkout_session` 就放弃，错误码 `custom_checkout_unsupported`，这时还没注资 |
| `opening` | 读金额，查币种，按「点数档 × 币种」的上限检查 |
| `funding`、`filling` | 照旧。填表时选新卡，**确保「保存卡」没勾选，核实不了就放弃** |
| `recheck`、`submitted`、`requires_3ds` | 照旧 |
| `verifying` | 判定成功：点数余额比快照增加了约 `quantity`（T6 确认后启用）。参考依据：结账会话 `payment_status=paid`、该卡的授权交易。**不看 `plan_type`** |
| `post_check`（新增） | 重新读已存卡列表。出现我们的卡就告警。用户事先同意了才删除，删除失败转人工 |

**金额上限**：

- 用 `quantity × 该币种的 amount_per_credit` 作为预期值。
- `cap_minor` 按「预期值 × (1 + 税率上限)」配置。
- 注资 `fund_cents` 按 cap 折成美元，再加汇率余量。ZovoCard 用的是 12% 的安全边际（`docs/zovocard-openapi-zh.md:1127-1130`）。

**兑换页要用户明确同意**：

- 点数按账号当前订阅的计费币种计价。
- 点数 12 个月过期，不退款。
- 如果我们的卡意外被保存到账号上，允许我们删除它。

### 6.2 升级（`kind=upgrade`），满足 §5 的 T8–T15 后再实施

**前提**：

- T9（门户能绑卡）或 T10（浏览器能进 chatgpt.com）至少满足一个，否则不做。
- T12 弄清续费归属。

**状态机**：

| # | 状态 | 做什么 | 失败时 |
|---|---|---|---|
| 1 | `checking` | `personal`、有订阅、`plan_type ∈ {go, plus, prolite, pro}` 且目标更高、不欠费、没有 `scheduled_plan_change`、不在试用期、不是赠送、`purchase_origin_platform` 是网页（不是 iOS 或 Android）、计费币种等于码的币种、月付。记下用户原来的默认卡 id 和已存卡列表 | 放弃，没花钱 |
| 2 | `previewing`（新增） | `GET /subscriptions/update/preview`。要求 `amount_due.amount > 0`，币种一致，金额不超过上限。记下 `amount_due`、`tax_amount`、`renewal_date`。`applied_balance` 能全额抵扣时放弃，转人工确认 | 放弃，没花钱 |
| 3 | `funding` | 按 preview 金额加容差注资 | 同 A |
| 4 | `attaching`（新增） | 在 Stripe 托管门户或 chatgpt.com 原生绑卡页填我们的卡（有 3DS 就答一次）。完成后 `GET /payments/payment_methods`，按尾号和有效期找到我们卡的 `payment_method_id`，找不到就放弃 | 放弃，并且必须删掉已绑上的卡 |
| 5 | `previewing` 复核 | 再 preview 一次。金额变化超出容差就放弃 | 放弃，删卡 |
| 6 | `submitted` | 先写 `submitted_at`，再**只调一次** `POST /subscriptions/update {account_id, updated_plan, payment_method_id: 我们的卡}`。**必须显式带我们的卡 id**，绝不让它扣默认卡（默认卡是用户自己的） | 超时或 5xx 都算结果不明，转确认，绝不重发 |
| 7 | `requires_3ds` | 返回 `requires_action` 时，在 chatgpt.com 页面里用 Stripe.js 完成；做不到就转人工 | 转人工 |
| 8 | `verifying` | `plan_type` 变成目标套餐，再核对该卡授权金额和 preview 的差额。返回 `payment_method_failed` 时调 `cancel_pending` 清理 | 同 A 的定论表 |
| 9 | `detaching`（新增） | 按用户在兑换页选的方式处理续费（见下）。执行完再读一次核实 | 失败告警并转人工，这是续费风险点 |

**金额从哪里读、上限怎么设**：

- 只信 preview 接口的 `amount_due.amount`，不读页面。
- 上限 = min（码上配置的「起点套餐 → 目标套餐 × 币种」上限，preview 金额 × (1 + 2%)）。
- 事后用卡的授权金额复核。

**哪些步骤会动用户的账单设置**（兑换页要逐条明确同意，缺一条就不建单）：

1. 在你的账号上添加一张我们的临时付款卡，用它支付升级差价。
2. 升级后账单周期从今天重新开始。原套餐剩余时间按比例抵扣，抵扣额以页面为准。
3. 续费处理，**必须二选一**：
   - **a. 关闭自动续费**：本周期结束后整个订阅失效，不会回到原套餐。
   - **b. 恢复你原来的默认卡**：下个周期起按新套餐价格扣你自己的卡。
4. 我们的卡用完后会被清零并冻结。续费如果落到这张卡上会失败，订阅可能进入宽限期，之后失效。
5. 如果你开着「备用付款方式」，续费失败时可能改扣你其它已存卡。

**怎么防止续费落到我们的卡上**：

- 升级完成后立刻执行用户选的 3a 或 3b。
- 然后用 `DELETE /payments/payment_method/{id}` 删掉我们的卡。默认卡删不掉，所以必须先切走默认卡或关掉续费。
- 最后核实：`default_payment_method_id` 不是我们的卡；已存卡列表里没有我们的卡；如果选的是 3a，`GET /subscriptions` 的 `will_renew=false`。
- 卡池那边，这张卡要等摘除核实完成后才能清零回收。核实失败时不回收，转人工。

**必须转人工的情况**：

- `POST /subscriptions/update` 结果不明（超时、5xx、`pending` 超过时限）。
- `requires_action` 但 3DS 做不了，或者第二次出现验证框。
- 返回 `payment_method_failed`，但卡上有成功授权（证据冲突）。
- 授权金额和 preview 的差额超出容差。
- `plan_type` 没变，但卡上有授权。
- 摘卡、改默认卡、关续费任何一步失败（续费风险）。
- 流程中途检测到账号欠费、出现计划变更、出现第二个订阅。
- 点数单：卡上有授权但余额没增加；我们的卡被保存且删不掉。

---

## 7. 不该做或做不了的部分

1. **不要用 `/payments/checkout` 给已有订阅的账号买更高档。**
   - 前端从不这样做。
   - 服务器可能新建第二个订阅，造成重复扣款（§2.2）。
   - 现有的 `already_subscribed` 守卫必须保留。
2. **不要扣用户自己的已存卡来升级。**
   - 那等于替用户花他自己的钱。
   - 升级请求必须显式带我们的 `payment_method_id`，提交前还要核对 preview 的 `default_payment_method`、我们卡的尾号。
3. **不要在自己的页面里加载 Stripe.js、用 OpenAI 的 publishable key 绑卡或付款。**
   - 这是冒用对方商户身份收卡，容易触发风控，也有合规问题。
4. **如果 Chrome 进不了 chatgpt.com，而 Stripe 账单门户又不可用，升级就做不了。**
   - 这时没有不冒险的绑卡路径。不要为此去绕 Cloudflare。
5. **不能让我们承担续费。**
   - 用户想长期保持新档位，只能由用户自己的卡续费。
   - 我们的卡用后清零冻结，留在账号上只会带来拒付，还可能误扣用户备用卡。
6. **暂不支持的情况**：
   - Free、Go 账号买点数：只有灰度用户能买，资格不稳定。
   - 年付订阅升级：前端里有年付和月付周期的处理，升级行为未知。
   - iOS、Google Play 购买的订阅：网页上大概率不能升级【推测】，直接拒绝。
   - 欠费、宽限期中的账号升级：前端有专门的欠费恢复流程（`/subscriptions/complete_payment/invoice_details`），属于另一条资金路径，不要混进来。
7. **点数只能按账号的计费币种买，不能任选地区。**
   - 码的地区和账号币种不一致时，在检查阶段拒绝（不花钱）。
   - 不要用 `billing_details` 硬改币种去碰运气（T2 确认之前）。
8. **顺带风险（子项目 A）**：
   - A 用我们的卡开通的新订阅，下个月续费同样会落到已冻结的卡上，导致欠费和拒付。
   - 建议参照 ZovoCard，在 A 成功后关闭自动续费，并在兑换页告知，或者至少评估拒付费用。
   - 另外，A 的 `CreateCheckout` 没有传 `checkout_ui_mode`。如果服务器对 Plus、Pro 实验返回自定义结账页（`enabled_custom_checkout_for_plus/pro/go`），A 会因为「未返回支付链接」放弃。这个方向是安全的，但会导致无法下单。建议在 §12 的核实里一并看 T1 的结论。
