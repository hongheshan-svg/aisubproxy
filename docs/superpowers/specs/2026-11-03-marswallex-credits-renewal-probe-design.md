# MarsWallex 通道：开通后关闭续费、免付款探测、Codex 点数

日期：2026-11-03　状态：已定稿，实施中
依据：调研报告 `docs/research/2026-11-03-chatgpt-upgrade-credits-research.md`（下文简称「调研」）
前置：付款核心设计 `2026-11-01-marswallex-pay-core-design.md`（下文简称「A 设计」）、服务商注册表设计 `2026-11-03-card-provider-registry-design.md`

## 1. 范围

本轮做三件事：

| 部分 | 内容 | 来由 |
|---|---|---|
| 一 | A 期开通成功后，自动关闭续费 | 调研 §3.3、§7.8。用户已选「成功后自动关续费」 |
| 二 | 免付款探测工具 | 调研 §5。升级是否可行、点数和开通的结账页模式，都要先在服务器上用测试账号确认 |
| 三 | MarsWallex 通道支持 Codex 点数 | 调研 §1、§6.1。用户要求 |

本轮**不做**：
- **已有订阅升级**：升级要先把我们的卡绑到用户账号上（调研 §2、§3），绑卡路径是否可行，取决于探测结果（T9、T10）。探测结果出来后再单独设计。
- **20x 续费绑卡、宽限期原订阅处理**。
- **Free、Go 账号买点数**。

## 2. 资金约束（全部保持）

- 这些规则继续保持：
  - A 设计里的全部资金规则；
  - 服务商注册表设计 §2；
  - 熔断；
  - 发码硬门槛；
  - 重兑最多 3 次；
  - 资金上限；
  - 资金单绝不自动重发；
  - 绝不点第二次支付；
  - dry_run 取值语义。
- 本轮新增的调用分成两类，分开处理：
  - **改账号的调用**：关闭续费、建结账会话。它们都不花钱。
  - **付款的调用**：点数付款。它走 A 的同一套「先写 `submitted_at`、只点一次」流程。
- 下列操作**一律不做**：
  - 调用 `/subscriptions/update`、`/subscriptions/renew`、`/payments/payment_method*` 中的写操作（POST、DELETE）；
  - 用 `/payments/checkout` 给已有订阅的账号开通更高档（调研 §7.1）；
  - 扣用户自己的已存卡。
- 用户会话只在内存和 secretbox 密文里存在，不进日志、事件、截图、审计和接口响应。

## 3. 部分一：开通成功后关闭续费

### 3.1 行为

- **触发条件**：同时满足以下三条的订阅开通单（`kind = subscription`）：
  - 真实付款（`dry_run = 0`）；
  - 状态进入 `succeeded`；
  - 系统确认成功，或者人工定论为成功。
- **执行动作**：成功后，系统用该单的会话调用 `POST /backend-api/subscriptions/cancel {account_id}`，取消下期续费。本期权益不受影响。
  - 接口路径的证据：前端代码里有字符串 `safePost("/subscriptions/cancel",{requestBody:{account_id:p},additionalHeaders:{...:p}})`。附加头的名字以前端为准，实现时对照调研保存的前端包确认。
- **核实**：调用后用只读接口确认已经不再续费。优先用 `GET /backend-api/subscriptions?account_id=` 的 `will_renew`；没有这个字段时，用 accounts/check 里等价的字段。核实结果不确定时，按失败处理。
- **失败处理**：关闭续费失败或核实不了，**不影响**单据的成功状态和资金结论。
  - 发告警。
  - 计入「每日待办」。
  - 后台单据详情显示「续费未关闭」，并提供「重试关闭续费」按钮。
- **重试**：关闭续费不花钱，可以自动重试：最多 3 次，间隔 1、3、10 分钟。之后只能人工点重试。
- **会话保留**：关闭续费需要会话，所以单据成功后，会话要等续费处理有了结论（成功、或自动重试用完）再清空。最长不超过 1 小时，到点一律清空。会话清空后，「重试关闭续费」按钮不可用，界面提示「请让用户自行在 ChatGPT 设置里关闭续费」。
- **不执行的情况**：
  - 试运行单（dry_run 为 1 或 2）一律不调用；
  - ZovoCard 通道不涉及。

### 3.2 数据

`pay_orders` 新增以下列，迁移要幂等：
- `renewal_state TEXT NOT NULL DEFAULT ''`，取值为：
  - 空：不适用；
  - `pending`：待处理；
  - `cancelled`：已关闭并核实；
  - `failed`：失败，等人工；
  - `skipped`：会话已清空，无法处理。
- `renewal_attempts INTEGER NOT NULL DEFAULT 0`
- `renewal_checked_at INTEGER`

旧库里已有的 `succeeded` 单，迁移时把 `renewal_state` 置为 `skipped`，因为那些单的会话已经清空。

### 3.3 发码门槛和兑换页

- **发码门槛**：MarsWallex 订阅码的发码硬门槛加一条，原先那笔系统确认成功的真实开通单，必须同时满足 `renewal_state = 'cancelled'`。这样能证明「付款 → 确认 → 关闭续费」整条链路走通了。
- **兑换页**：MarsWallex 订阅码的兑换确认步骤增加一行说明：「本码开通一个月，到期不自动续费。」
- **用户知情**：上面这行说明同时告诉用户，系统会替他关闭续费。

## 4. 部分二：免付款探测

### 4.1 入口

- **后台位置**：MarsWallex 兑换页新增一个标签「免付款探测」。
- **输入**：管理员粘贴测试账号的会话，格式与现有的测试兑换相同，再勾选要跑哪些探测项。
- **接口**：`POST /api/v1/admin/pay/probe`
  - 请求体：`{session, checks: [...], region?}`
  - 返回：`{id, created_at, results: [{key, title, status: ok|fail|skipped, summary, details}]}`
- **记录保存**：结果存入表 `pay_probes`，只保留最近 50 条。表里不存会话、完整邮箱、卡数据或任何 token。
- **历史与导出**：可以查看历史记录，也可以「复制为文本」发给开发。

### 4.2 探测项

所有探测项都不付款，大部分只读。

| key | 对应调研 | 做什么 | 是否改账号 |
|---|---|---|---|
| `account` | — | 调 accounts/check，汇总这些字段：`plan_type`、是否有生效订阅、计费币种、`structure`、是否欠费、订阅来源平台、`will_renew`。邮箱打码显示 | 只读 |
| `subscriptions` | §3.3 | `GET /subscriptions?account_id=`，列出 `will_renew`、`renews_at`、`billing_period`、`is_delinquent` 等字段是否存在及其值 | 只读 |
| `checkout_modes` | §7.8、T1 | 只对**没有订阅**的账号做：分别「不带」和「带 `checkout_ui_mode:"redirect"`」各建一次 Plus 开通结账会话，报告返回的是 `url`（附域名）还是 `custom_checkout_session` | 建会话，不付款 |
| `credit_checkout` | T1、T2 | 只对**有生效订阅**的账号做：分别「带」和「不带」`checkout_ui_mode:"redirect"`，各建一次 250 点的点数结账会话。报告返回形态、域名和币种 | 建会话，不付款 |
| `payment_methods` | T4、T16 | `GET /payments/payment_methods`：已存卡数量、有没有默认卡（id 打码）、备用付款方式的开关状态 | 只读 |
| `upgrade_preview` | T8 | 只对**有生效订阅**的账号做：对更高的每一档调 `GET /subscriptions/update/preview`，报告 `amount_due`、币种、`renewal_date`、`default_payment_method` 是否存在 | 只读 |
| `customer_portal` | T9 | `GET /payments/customer_portal` 拿到门户链接，报告域名。用 paybrowser 同参数的 Chrome 打开门户，截图，并检测页面上有没有「添加付款方式」类入口。**只看，不点任何按钮** | 只读 |
| `chatgpt_browser` | T10 | 用 paybrowser 同参数的 Chrome，注入会话 cookie，打开 `https://chatgpt.com/`。截图，判断是被 Cloudflare 挑战拦截，还是正常渲染 | 只读 |
| `credit_balance` | T6 | 调用调研找到的疑似点数余额接口（如 `/wham/usage`），报告返回结构里有哪些字段 | 只读 |

### 4.3 约束

- **接口白名单**：探测用到的 HTTP 调用只能是下面这些，用一张写死的「方法 + 路径」白名单约束，并有测试断言白名单之外的请求一律拒绝：
  - 上表列出的读接口；
  - 两种 `POST /payments/checkout`。
- **跳过条件**：账号状态不满足某一项的前提时，该项记为 `skipped` 并写明原因。例如有订阅时不建开通会话。
- **浏览器**：用现有 paybrowser 的启动参数，用完就关。截图写进现有截图目录，只截页面，不截输入框内容。每次最多同时运行一个探测。
- **页面安全**：探测打开的页面一律不点击、不填表。
- **遇到 Cloudflare**：如实记为 `fail`，并写明被拦。不尝试绕过。

## 5. 部分三：Codex 点数

### 5.1 档位与能力

- **档位**：`credit250`、`credit500`、`credit1000`、`credit2500`、`credit5000`、`credit25000`，档位键与 ZovoCard 相同，数量从键名里取。
- **能力清单**：MarsWallex 的能力（注册表 §3.1「能力取自实现」）自动包含这些档位。
- **档位提示**：`PlanNote` 为「仅限已订阅 Plus 或 Pro 的账号，按账号计费币种付款」。

### 5.2 状态机差异

在 A 的状态机上做以下改动：
- `pay_orders` 新增列 `kind TEXT NOT NULL DEFAULT 'subscription'`（取值 `subscription` 或 `credit`）和 `credit_quantity INTEGER NOT NULL DEFAULT 0`。
- 与 A 相同的步骤，照 A 执行。

| 步骤 | 点数单的差异 |
|---|---|
| checking | 下列条件都要满足，任一不满足就放弃（不花钱），并给出中文原因：<br>• 有生效订阅，且 `plan_type` 属于 Plus、Pro 各档（取值以 accounts/check 为准，实现时对照前端包确认）<br>• `structure = personal`<br>• 没有欠费<br>• 账号计费币种等于码的地区币种<br>同时记录两个快照：已存卡快照（id 打码后的哈希、尾号）；余额接口可用时，再记点数余额快照 |
| link_created | `POST /payments/checkout`，请求体带 `credit_purchase_data {account_id, quantity, unit:"credit"}`、`entry_point:"codex_purchase_credits_route"`、`checkout_ui_mode:"redirect"`。处理返回结果：<br>• 只接受带 `url` 且域名在现有支付域名白名单里的结果<br>• 返回 `custom_checkout_session` 时放弃，错误码 `custom_checkout_unsupported`，这时还没注资 |
| opening / 读金额 | 金额上限取「点数档 × 币种」配置（见 5.3），没有配置就放弃（不花钱） |
| filling | 照 A 执行。另外，页面上有「保存付款信息」类勾选框时，必须确认它没被勾选。确认不了就放弃，按 A 的未提交处理：已注资的卡照常回收 |
| verifying | **不看 `plan_type`**。按以下顺序判断：<br>• 成功：页面结果为成功，**并且**卡池里有该卡的授权交易，金额与页面金额的差在容差内<br>• 加强条件：余额接口可用时，还要求点数余额比快照增加了 `quantity`<br>• 证据冲突时转 `needs_review` |
| 成功后 | 重读已存卡列表。如果出现了我们这张卡，发告警并在单据上标「我们的卡被保存到了用户账号」。本轮不自动删除卡（调研 T5 确认前不动用户账单设置） |

- 点数单不触发部分一的关闭续费。
- 熔断、重兑、互斥、上限规则与订阅单完全一致。

### 5.3 价格与上限配置

- **配置位置**：现有的套餐配置（MarsWallex 兑换页「套餐」）增加点数各档，按地区配置预期金额和上限。
- **PH 默认值**：取自 ZovoCard 文档，每 250 点 ₱565，按数量线性计算。
  - 预期金额：565 × 数量 / 250 比索。
  - 上限：预期金额 × 1.12。
- **其他地区**：没有默认值，管理员配置后才能发码。
- **注资金额**：上限按汇率折成美元，再加汇率余量。沿用 A 的注资规则，并受现有单笔和单日注资上限约束。

### 5.4 发码门槛与兑换页

- **发码门槛**：点数码单独计算，必须先有一笔真实点数单达到 `succeeded`、`resolved_by` 为空、`kind = credit`，才能发点数码。第一笔用后台测试兑换来做。
- **兑换页**：点数码兑换前要勾选同意，内容包括：
  - 仅限已订阅 Plus 或 Pro 的账号；
  - 按账号计费币种付款，码上的地区必须与之一致；
  - 点数 12 个月过期，不退款。
- **试运行**：两种试运行（读金额、试运行到填表）都支持点数单。

## 6. 测试要求

**部分一（关闭续费）**：
- 成功后调用一次取消接口，并核实结果。
- 核实失败时，单据仍保持 `succeeded`，`renewal_state = failed`，并发出告警。
- 自动重试最多 3 次。
- 会话在续费处理有结论后清空，最长 1 小时。
- 试运行单不调用。
- 旧库迁移后为 `skipped`。
- 发码门槛要求 `cancelled`。

**部分二（免付款探测）**：
- 白名单之外的请求被拒绝，并有测试断言。
- 账号有订阅时，`checkout_modes` 为 `skipped`；没有订阅时，`credit_checkout` 和 `upgrade_preview` 为 `skipped`。
- 结果和存档里不含会话、完整邮箱、token。
- 同一时刻只运行一个探测。

**部分三（Codex 点数）**：
- checking 的每一条拒绝原因都有测试，并且都在注资前结束。
- 返回 `custom_checkout_session` 时放弃，且不注资。
- 「保存付款信息」勾选框核实不了时放弃，并回收卡。
- verifying 只看授权交易和余额，不看 `plan_type`。
- 证据冲突时转人工。
- 点数单不触发关闭续费。
- 点数码的发码门槛独立计算。

**前端**：
- 探测标签页能提交并展示结果。
- 兑换页点数码的同意项未勾选时，不能提交。
- 订阅码显示「到期不自动续费」。
