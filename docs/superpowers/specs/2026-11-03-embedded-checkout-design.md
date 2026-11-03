# 订阅开通支持 ChatGPT 内嵌结账页

日期：2026-11-03　状态：已定稿，实施中
前置：付款核心设计 `2026-11-01-marswallex-pay-core-design.md`（下文简称「A 设计」）、`2026-11-03-marswallex-credits-renewal-probe-design.md`（下文简称「点数设计」）

## 1. 背景

2026-11-03 实测：给没有订阅的账号建开通结账会话（`POST /backend-api/payments/checkout`，`plan_name` 为 Plus 或 Pro 任一档），ChatGPT 只返回内嵌结账会话：

- `tag` 为 `custom_checkout_session`，`checkout_ui_mode` 为 `custom`，`processor_entity` 为 `openai_llc`；
- 返回里有 `checkout_session_id`（`cs_live_…`）和 `client_secret`，**没有 `url`**；
- 请求里写明 `redirect` 或 `hosted` 也一样被改回 `custom`。

ChatGPT 网页版拿到这种返回后，打开 `https://chatgpt.com/checkout/<processor_entity>/<checkout_session_id>`。

引擎目前只认 Stripe 托管页（`pay.openai.com`、`checkout.stripe.com`），遇到没有 `url` 的返回就以「创建支付会话未返回支付链接」放弃。所以订阅开通现在全部走不通。点数单仍然返回托管页链接，不受影响。

目标：引擎能用浏览器打开这个内嵌结账页并完成付款，和用户自己在网页上开通一样。

## 2. 摸底结果

2026-11-03 在本机做了一次只读摸底：

- 出口在美国；
- 用免费号建 Pro 5x 会话；
- 用与 `paybrowser.Launch` 相同参数的 Chrome 打开内嵌页；
- 不填表、不点击。

看到的情况：

| 项 | 结果 |
|---|---|
| Cloudflare | 无界面、有界面两种模式都在 9–11 秒内打开，没有挑战页。之前免付款探测被拦，打开的是 chatgpt.com 首页，只等了 6 秒。VPS 机房 IP 会不会被拦还不知道 |
| 页面语言 | 跟账号的语言设置走（这个号是中文），不跟浏览器的 `--lang=en-US` |
| 左栏 | 档位按钮 US$100 / 200 / 500（已选中会话对应的 100）、Apple Pay、Card / PayPal 标签（默认 Card） |
| 卡片字段 | 在 `js.stripe.com` 的 iframe（Stripe Payment Element）里：`#payment-numberInput`、`#payment-expiryInput`、`#payment-cvcInput`，与 `Sel` 里已有的首选选择器一致 |
| 地址字段 | 页面上没有。国家应该由建会话时传的 `billing_details` 决定。填卡号后会不会出现国家、邮编，没有验证 |
| 保存卡勾选框 | iframe 里有 `#payment-savePaymentInput`（`name="savePayment"`），标签「Save payment details to OpenAI OpCo, LLC for future purchases」，默认没勾 |
| 金额 | 主页面右栏「今日应付金额 US$100.00」，没有 id 或 data-testid，class 都是排版用的；iframe 里有一段给读屏软件的英文「Payment amount: $100.00 Currency: usd Payment mode: Live mode…」 |
| 支付按钮 | 主页面唯一的 `button[type="submit"]`，文字随语言变（这次是「升级至 Pro」） |
| 条款 | 按钮下方的一段文字，没有勾选框 |
| 其他 iframe | OpenAI 的 sentinel（`chatgpt.com/backend-api/sentinel`）、Stripe 的隐形 hCaptcha、Apple Pay。都由页面自己在浏览器里运行，引擎不碰 |

## 3. 范围

本轮做：订阅开通（Plus、Pro 各档）在 ChatGPT 返回内嵌会话时，用浏览器打开内嵌结账页付款。

本轮**不做**：

- **点数单**：仍是托管页，保持不变；它遇到内嵌会话仍按 `custom_checkout_unsupported` 放弃。
- **已有订阅的升级**。
- **PayPal、Apple Pay**。
- **改建会话的请求体**：比如加 `checkout_ui_mode`、`entry_point`。现在不加也能拿到内嵌会话；以后发现必须加再改。
- **任何绕过 Cloudflare 或页面风控的手段**：不用伪装插件，不解人机验证，不伪造 sentinel 令牌，也不自己用 `client_secret` 拼付款页（替代方案 B、C 已否决，见 §9）。

## 4. 资金与安全约束（全部保持）

A 设计与点数设计的全部资金规则继续有效，包括：

- 注资在打开页面、读到金额之后；
- 资金单绝不自动重发；
- 先写 `submitted_at` 再点支付；
- 绝不点第二次；
- `LockSubmit` 的不点击证明；
- 开始填卡后禁止整页截图；
- 卡号、CVC、会话、邮箱一律脱敏；
- 金额上限；
- 熔断；
- 试运行不点击；
- 开通成功后关闭续费。

内嵌页带来的新约束：

- 内嵌页的所有放弃条件都在**注资之前**判定：打不开、Cloudflare、读不出金额、币种不符、超上限、低于最低金额、支付按钮不唯一。
- 金额下限（§5.10）对所有付款单生效，不只内嵌页。
- chatgpt.com 只在一种路径上放行：`/checkout/<段>/<段>`。chatgpt.com 的其他地址一律不打开。

## 5. 设计

### 5.1 流程

步骤与 A 设计相同，有改动的是建会话、打开页面、填表、等结果这四步：

```
检查账号 → 建结账会话* → 打开支付页、读金额* → 选卡注资 → 填表* → 记 submitted_at、点一次 → 等页面结果* → 查账号定论 → 关闭续费
```

### 5.2 建会话（`internal/chatgpt`）

- `Checkout` 加两个字段：`Tag`、`ProcessorEntity`。`CreateCheckout` 从返回里读出 `tag`、`processor_entity`，去掉首尾空白。
- 请求体、请求头不变。免付款探测与自动付款共用 `planCheckoutBody`，继续保持一致。
- 判定规则：
  - 返回没有 `checkout_session_id` 仍然报错，与现在一样；
  - 有 `url` 时不管 `tag` 是什么，都按托管页处理，与现在一样；
  - 没有 `url` 且 `tag` 为 `custom_checkout_session` 时，交给引擎拼内嵌页链接（§5.3）；
  - 两样都没有时，引擎照旧以 `checkout_failed`「创建支付会话未返回支付链接」放弃。

### 5.3 拼链接与白名单（`internal/payengine`）

**拼链接**（`createLink`）：

- `processor_entity` 必须匹配 `^[a-z0-9_]{1,64}$`；
- 会话号必须匹配 `^[A-Za-z0-9_]{1,255}$`；
- 任一不合格，以 `checkout_failed` 放弃，说明里不写会话号；
- 合格时拼成 `https://chatgpt.com/checkout/<processor_entity>/<会话号>`，照旧经 `acceptCheckoutURL` 校验后写入 `checkout_url`。

**白名单**（`checkCheckoutURL`）：

- 现有规则不变：
  - 必须是 https；
  - 不带账号密码；
  - 不带端口；
  - 主机名精确匹配。
- `pay.openai.com`、`checkout.stripe.com` 仍然不限路径。
- 新增 `chatgpt.com`（只认这个主机名本身，不认子域名），要求同时满足：
  - 路径正好是 `/checkout/<段>/<段>`，每段都匹配上面对应的正则；
  - 不带查询串；
  - 不带 `#` 片段。
- 不合格时以 `checkout_url_untrusted` 放弃，说明里只写域名。
- 免付款探测的 `probePayHosts` 不动。

**页面类型**：

- 新增 `checkoutPageKind(url) paybrowser.PageKind`：主机名是 `chatgpt.com` 返回 `PageEmbedded`，其余返回 `PageHosted`。
- 页面类型不落库，每次都按 `checkout_url` 现算。

### 5.4 浏览器接口（`internal/paybrowser`）

- 新增类型 `PageKind`，取值 `PageHosted`、`PageEmbedded`。
- `Open` 改为 `Open(ctx, url string, kind PageKind, cookies []Cookie) error`。页面类型记在 `Browser` 上，之后的 `WaitRendered`、`ReadAmount`、`Fill`、`PayButtonState`、`Submit`、`WaitOutcome` 都按它分支。
- `payengine.FormBrowser` 的 `Open` 签名随之更改，测试里的假浏览器一并改。
- 托管页的全部行为保持原样。下面各节只描述内嵌页。

### 5.5 内嵌页：打开

**打开**：

- 注入会话 cookie、打开链接，沿用现有逻辑。
- 在 `pageLoadTimeout`（60 秒）内等到下面三样同时成立，`Open` 才返回成功：
  - 各级 frame 里有可见的卡号框（`Sel.CardNumber`）；
  - 能读出 §5.6 的金额文字；
  - 主页面（不进 iframe）里可见的 `button[type="submit"]` 正好一个。
- 托管页不检查支付按钮，保持原样。

**打不开时**：超时后先读一次页面，按 `IsCFChallengePage` 判断是不是 Cloudflare 挑战页。

- **是挑战页**：
  - `Open` 返回包着新哨兵错误 `paybrowser.ErrCFChallenge` 的错误；
  - 引擎用 `errors.Is` 认出它，记 `cf_challenge`，并告警「请检查该地区代理的出口 IP」。
- **不是挑战页**：
  - `Open` 的错误说明写明三样里哪一样没成立，比如「找到 2 个支付按钮」；
  - 引擎照旧记 `page_failed`。
- 两种情况都发生在注资之前。

**截图**：内嵌页的 `WaitRendered` 改为等卡号框可见。

- 最多等引擎传入的 `pageRenderWait`（10 秒）。
- 等不到只记 warn，不中断。

### 5.6 内嵌页：读金额

- **来源**：在各级 frame 里找以 `Payment amount:` 开头的文字。这是 Stripe Payment Element 给读屏软件的说明，视觉上隐藏，查找时不能要求可见。
- **解析**：从所在元素的父元素的文字里，用 `Payment amount:\s*(.+?)\s+Currency:\s*([A-Za-z]{3})` 取出金额原文和币种代码。
  - `ReadAmount` 返回规范化的原文「金额里最后一段数字 + 空格 + 币种代码大写」，比如 `100.00 USD`、`1,120.00 PHP`。原来的货币符号丢掉，所以币种只由 `Currency:` 后的代码决定。
  - 引擎不改：现有的 `ParseAmount` 解析这段规范化原文；现有的 `CurrencyMatches` 比对后缀的币种代码，与单据币种不符就以 `currency_mismatch` 放弃。
  - 读不到 `Currency:` 时按读不出金额处理（`amount_unreadable`）。
- **只用这一处**：主页面右栏的「今日应付」不参与判断，因为它的文字随账号语言变，也没有稳定的 id。它只出现在开页截图里，供人工核对。
- **复读**：填表后、点支付前，用同一方法再读一次（A 设计 MO-04）。金额变了或超上限就放弃，与托管页相同。
- **档位**：档位按钮引擎不碰，档位对错靠金额核对把关（§5.10）：金额低于最低金额（选错了便宜档、被套了优惠）或超过上限，都在注资前放弃。

### 5.7 内嵌页：填表

**必填**：卡号、有效期、CVC，选择器沿用 `Sel.CardNumber`、`Sel.Expiry`、`Sel.CVC`。任一填不上就报错，不点支付。

**地址字段可选**：国家、州、城市、邮编、姓名、街道。

- 卡号填完后，最多等 `addressWait`，看各字段出现没有。
- 出现的就按现有顺序填写并核对（先国家，再其余）；没出现的跳过，填表报告里记「页面没有」。
- 托管页仍要求必填，不变。

**页面上没有的元素一律跳过，不算错**：条款勾选框、Link 保存勾选框（`#enableStripePass`）、邮箱框、手机框、「使用新卡」入口、手动地址入口。

- 这些在内嵌页上都没有。
- 现有的 `revealCardForm` 对 Link 验证框（「Pay without Link」）的处理保留，因为 Payment Element 也可能弹出 Link 验证。

**保存卡**：内嵌页填表后调用 `EnsureNoSavePayment`，确认「Save payment details … for future purchases」没勾，勾着就取消。

- 这是点数单现成的逻辑，按标签文字识别，`#payment-savePaymentInput` 能命中。
- 确认不了就报错，不点支付。
- 现在 `fillOnly` 只对点数单调用它，改为点数单和内嵌页都调用；托管页的开通单不变。

**不碰**：档位按钮、Apple Pay、PayPal 标签、返回按钮。

**最后核对**：全部字段重新核对一遍（`recheckFields`），与托管页相同。

### 5.8 内嵌页：点击与等结果

**点击**：`Submit` 点 §5.5 确认过的那个主页面提交按钮。

- 点之前再确认一次它仍是唯一的、可用的（不是 disabled 或 `aria-disabled`）。
- 只点一次的全部机制（先置位、`LockSubmit`、`ErrAlreadySubmitted`）不变。

**等结果**：`WaitOutcome` 按现有顺序判断：成功元素 → 未处理的 3DS 框 → 点击后才出现的提示文字。新增一条：

- 每轮检查主页面地址。路径不再以 `/checkout/` 开头时，立即返回 `OutcomeUnknown`，说明文字为「页面已离开结账页」。
- 引擎照旧转确认阶段，由查账号定论。
- 不新增结果类型。确认阶段对 `unknown` 的处理不变。

**3DS**：Stripe.js 在当前页面上弹出验证框，沿用现有的 `Sel.ChallengeFrame`、`EnterOTP` 和 MarsWallex 验证码推送。

**提示文字**：

- 拒付等文字可能出现在 Stripe iframe 里（英文），也可能出现在主页面上（随账号语言）。
- 认不出的照旧只记下、继续等，超时后转确认，不会重复付款。
- 中文等语言的拒付词没有核实，本轮不加。

### 5.9 错误码

只新增一个错误码 `amount_below_floor`（§5.10），前端 `payui.ts` 要补上它的说明。其余都沿用前端已有说明的错误码：

| 情况 | 错误码 | 动钱了吗 |
|---|---|---|
| 内嵌会话缺 `processor_entity` 或格式不合格 | `checkout_failed` | 没有 |
| 链接不在白名单（含 chatgpt.com 路径不对、带查询串） | `checkout_url_untrusted` | 没有 |
| 打开时停在 Cloudflare 挑战页 | `cf_challenge`（告警） | 没有 |
| 打不开、支付按钮不唯一 | `page_failed` | 没有 |
| 读不出金额 | `amount_unreadable` | 没有 |
| 币种不符 | `currency_mismatch` | 没有 |
| 超上限 | `amount_over_cap` | 没有 |
| 低于最低金额 | `amount_below_floor` | 没有 |
| 填表失败、保存卡确认不了、复读金额不符 | 与托管页相同 | 已注资，按 A 设计释放租约 |

### 5.10 金额下限

2026-11-03 用户追加的规则：**金额必须在该套餐预期价格附近，偏低也要在注资前放弃。** 同日用户又定：下限单独填一个数（「最低金额」），不按预期金额的固定百分比算。原因是挂代理后各地区币种不同，有的地区价格可能随汇率浮动，固定比例不好用。

- **防的是什么**：
  - 内嵌页上有档位按钮，页面默认选错了更便宜的档；
  - 页面自动套用了优惠（比如首月 $0）。
  - 只拦上限时，这两种情况都会被照常付款，客户拿到的套餐不对，钱却花出去了。
- **新配置项「最低金额」**：`RegionConf` 加 `FloorMinor int64`（JSON `floor_minor`）。
  - 和预期金额、上限一样，按「套餐 × 地区」各填一份，用该地区币种的最小单位，不做汇率换算。
  - 保存配置时校验：不能为负；填了上限时不能高于上限；填了预期金额时不能高于预期金额。
  - 预期金额 `expect_minor` 仍只用于展示和校验配置，不参与付款判断。
- **真实付款单的规则**：
  - `ResolvePlan` 在非试运行时要求 `FloorMinor > 0`，否则以「套餐 X 在地区 Y 未配置最低金额」判为不可用。这与未配上限、未配注资金额的处理相同。下单和建会话时都会拦下。
  - 打开页面读到金额后，若 `金额 < FloorMinor`，以 `amount_below_floor` 放弃。说明写明页面金额和最低金额，并提示「可能选错了档位或带了优惠」。此时还没注资。
  - 上限仍是 `CapMinor`，不改。税只会让金额变高，由上限把关。
  - 填表后复读金额要求和第一次完全相同（MO-04），下限自然也覆盖到。
- **试运行**：金额偏低只在时间线记一条 warn，不放弃。没配最低金额时，记一条「未配置最低金额，跳过下限检查」。
- **适用范围**：托管页、内嵌页、开通单、点数单都适用，按单据的套餐和地区取配置。
- **最低金额从哪里来**：建会话这一步已经调用了 `ResolvePlan`，把它返回的 `FloorMinor` 记在运行期的 job 上。不落库，也不改单据表。
- **出厂值**：订阅套餐全空。点数套餐在 PH 的最低金额默认等于预期金额（每 250 点 ₱565 的标价，不含税；上限是它的 1.12 倍）。
- **怎么填**：
  - 价格固定的地区，最低金额填标价；
  - 价格随汇率浮动的地区，最低金额填低位价，上限填高位价；
  - 注资金额是美元。付非美元金额时，发卡方按汇率扣美元，可能另收手续费，所以注资金额要按高位汇率留出余量。
- **升级注意**：已有安装里保存过的套餐配置都没有最低金额。升级后，所有套餐（订阅和点数）在填好最低金额之前都不可下单，管理员要在后台补上。写进 CHANGELOG 的「升级注意」。

## 6. 测试

**`internal/chatgpt`**：

- `CreateCheckout` 解析内嵌会话返回：有 `tag`、`processor_entity`，没有 `url`。
- 托管返回解析不变。

**`internal/payengine`**：

- `createLink` 拼链接：
  - 正常拼出；
  - `processor_entity` 为空、含 `/`、含 `..`、含大写；会话号含 `/`、`?`、`#`、空格：都以 `checkout_failed` 放弃，而且没取卡。
- `checkCheckoutURL`：
  - chatgpt.com 合法路径通过；
  - 下列地址都拒：`/checkout/a`、`/checkout/a/b/c`、`/backend-api/…`、带查询串、带片段、`www.chatgpt.com`、`chatgpt.com.evil.com`、http、带端口；
  - 托管页两个域名照旧。
- `checkoutPageKind` 按域名分类。
- 打开页面时，假浏览器分别返回 Cloudflare 挑战错误、按钮不唯一错误，单据以对应错误码结束，而且没调用注资。
- 金额下限：
  - 最低金额 10000（US$100）时，页面金额 9999 以 `amount_below_floor` 放弃、没注资；10000 照常往下走；
  - 试运行金额偏低只记 warn，以 `previewed` 结束；
  - 非试运行时没配最低金额，`ResolvePlan` 判为不可用，下单就被拒；
  - 保存配置时，最低金额为负、高于上限、高于预期金额都被拒。

**`internal/paybrowser`**（真实 Chrome，本机仿造页）：

- 仿造页按 §2 的真实 DOM 做：主页面和跨站 iframe（卡号框、`Payment amount` 隐藏文字、保存卡勾选框）。
- 覆盖：
  - 打开成功；
  - 读金额与币种；
  - 币种不符；
  - 没有地址字段时填表成功；
  - 有地址字段时照常填写；
  - 保存卡勾着时被取消；
  - 两个提交按钮时打开失败；
  - 只点一次；
  - 点击后跳到别的路径时 `WaitOutcome` 立即返回「页面已离开结账页」；
  - 3DS 框照常识别；
  - Cloudflare 挑战页（仿造标题和 `_cf_chl_opt`）时报专门的错误。
- 托管页的现有测试全部保留，并且必须通过。

**全量**：`go build ./... && go vet ./... && go test ./...`，前端测试与构建（如果改到前端）。

## 7. 上线验证

按顺序执行，前一步不过不进下一步：

1. 本机临时实例（18090）、免费号 Pro 5x：
   - 先跑试运行（只读金额），确认 `previewed`、金额 US$100.00、截图正常；
   - 再跑「试运行到填表」（公开测试卡，不点支付），看填表报告、按钮状态、复读金额。
2. VPS 上用同样的两步再跑一次，确认机房 IP 不会被 Cloudflare 拦。PH 地区必须先配菲律宾代理。
3. 真实付款：
   - 必须有用户的明确同意，并且先配好 MarsWallex 凭证。
   - 3DS 验证码只能经 MarsWallex 推送到达，本机没有公网地址收不到推送，所以建议在 VPS 上跑。
   - 付款成功、续费关闭后，`first_real_payment` 门槛才算满足。

## 8. 未核实事项

下列问题都要靠 §7 的实测回答。认不出的情况都会走确认阶段，不会重复付款。

- 填完卡号后 Payment Element 会不会要求国家、邮编或姓名。
- 账号邮箱注册过 Stripe Link 时，Payment Element 的验证框长什么样。
- 点支付后页面跳到哪里，有没有可识别的成功元素。
- 拒付、3DS 在内嵌页上的实际样子。
- PHP 等其他币种下 `Payment amount` 的写法。
- PH 等非美元地区的页面价是 OpenAI 定的固定本地价，还是按汇率换算、每天浮动；含不含税。
- 有地址时税额会不会让 `Payment amount` 变化。
- VPS 机房 IP 和各地区代理会不会触发 Cloudflare 挑战。

## 9. 否决的方案

- **B：自建付款页**，用返回里的 `client_secret` 加载 Stripe.js 付款，不经过 chatgpt.com。否决原因：
  - 要用 OpenAI 的 Stripe 公钥在别的来源上运行它的结账；
  - 绕开了 OpenAI 页面自己的风控；
  - 容易被判成异常，连累用户账号；
  - 没有任何稳定性保证。
- **C：纯接口**，由服务器直接调 Stripe 和 ChatGPT 接口确认付款。否决原因：
  - 完整卡号要经我们的服务器发给 Stripe；
  - 3DS 仍要浏览器；
  - 同样绕开了页面风控。
