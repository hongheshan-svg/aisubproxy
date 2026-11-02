# MarsWallex 付款核心设计（子项目 A）

- 日期：2026-11-01
- 状态：已实现，待生产验收（§12 的核实项尚未用测试账号完成，核实前不要真实付款）
- 前置：接入层（`2026-10-27-marswallex-access-layer-design.md`）、卡池与资金（`2026-10-28-marswallex-card-pool-design.md`）、钱包（`2026-10-29-marswallex-wallet-design.md`），均已合并并随 v1.5.1 发布
- 参考：`docs/zovocard-cdk-zh.md`（ZovoCard 的套餐、地区与兑换规则）、`automation/`（支付页填表原型）

## 1. 背景与目标

现在的 CDK 充值由 ZovoCard 一家包办：发码、选卡、生成 ChatGPT 支付链接、付款、3DS、回报结果都在它那边，本系统只转发预览 / 预检 / 兑换 / 查结果。用户决定把**卡片供应商做成可选**：ZovoCard 码照旧走 ZovoCard，同时新增 MarsWallex 供应商。MarsWallex 只提供卡和资金，ZovoCard 后台做的事要本系统自己补齐，拆成三个子项目依次推进：

| # | 子项目 | 内容 |
|---|---|---|
| **A** | **付款核心（本文）** | 用 ChatGPT 会话生成支付链接，服务器上用真 Chrome 自动填卡付款，自动过 3DS，确认开通；后台「测试兑换」 |
| B | 本地 CDK + 供应商路由 | 码记录供应商、后台默认供应商（先设 MarsWallex）、本地发码与管理、兑换入口按码分流、结果页与时间线 |
| C | 扩展流程 | 已有订阅升级、Codex 点数、20x 续费绑卡、宽限期原订阅处理 |

**本子项目目标**：输入「ChatGPT 会话 + 套餐 + 付款地区」，系统自动从 MarsWallex 卡池取一张卡完成订阅付款，并确认账号已开通。付款提交后绝不重复提交；结果不明的单据要么自动定论，要么交给人工，不会重复扣款。

## 2. 已确认的决定

| 事项 | 决定 |
|---|---|
| 真卡验证 | **不做前置手动验证，直接开发**。第一笔真实付款即验证，金额最小、每步用户确认（§11） |
| 套餐范围 | 全部 GPT 套餐分到 A、C 两个子项目：A 做免费账号直接开通 Go / Plus / Pro 5x / Pro 20x / Pro 50x；升级、点数、续费绑卡、宽限期放到 C |
| 付款地区 | PH / US / JP / CL / EG，每个「套餐 × 地区」在套餐表里单独启用 |
| 自动付款方式 | Go 用 chromedp 驱动服务器上的真 Chrome（不用 Node，也不直调 Stripe 接口） |
| 生成支付链接 | Go 后端用 tls-client 调 ChatGPT（浏览器访问 chatgpt.com 会被 Cloudflare 拦，原型已验证此分工） |
| 账单信息 | 姓名用持卡人英文名（本地已有），地址随机生成美国免税州地址（DE / OR / NH / MT / AK）。用户已知悉「与持卡人登记地址不一致可能被拒」的风险 |
| 代理 | 每个付款地区可配一个代理，生成链接与 Chrome 付款共用；不配则直连 |
| 重付 | 系统从不自动重新付款 |

## 3. 整体结构

| 单元 | 职责 | 依赖 |
|---|---|---|
| `internal/chatgpt`（新） | 解析会话、刷新 access token；查账号（套餐类型、是否有订阅、account_id、邮箱）；创建支付会话并返回支付链接。按代理创建 tls-client 客户端；会话解析复用 `gptcheck` | tls-client、`gptcheck` |
| `internal/paybrowser`（新） | chromedp 驱动 Chrome：每单一个无痕实例（临时目录、可选代理）；注入会话 cookie、打开支付页、读应付金额、填卡与地址、提交、识别 3DS 并填验证码、识别成功或被拒、按 §8 规则截图。**不访问数据库**，卡号与验证码通过调用方传入的函数临时取用。所有页面选择器集中在一个文件 | chromedp |
| `internal/payengine`（新） | 付款单据与状态机、排队与并发、串起 chatgpt / cardpool / paybrowser、确认开通、结果不明单据的后台协程、套餐表与代理配置、截图清理 | 以上 + `cardpool`、`db`、`secretbox`、`notify` |
| `internal/db` | §5 的表与迁移 | — |
| `internal/handler` + 路由 | §9 的后台接口 | `payengine` |
| 前端 `frontend/src/views/admin/MarsWallexPay.vue`（新） | `/ops` 下「MarsWallex 兑换」页 | 后台接口 |
| `deploy/install.sh`、`Dockerfile` | 安装 Chrome、Xvfb、字体（§10） | — |
| `server/http.go` | 启动时调用 `payengine.Start(ctx)`（写法同 `cardpool.Start`） | — |

告警走 `notify.SendText`（Telegram）：单据转待人工、浏览器无法启动。

### 3.1 留给子项目 B 的接口

| 函数 | 说明 |
|---|---|
| `Submit(ctx, Request{PlanKey, Region, Session, Source, SourceRef, DryRun}) (*Order, error)` | 校验并建单入队，立即返回。错误：`ErrPlanUnavailable`（套餐 × 地区未启用或金额未配）、`ErrBadSession`（会话无法解析、缺 sessionToken 或邮箱）、`ErrInflight`（同账号已有进行中单据） |
| `Get(ctx, orderNo) (*Order, []Event, error)` | 单据与时间线 |

B 如何得知单据结束（按 `source_ref` 查询或注册结束回调）留到 B 设计。

### 3.2 不在本子项目范围

CDK 与兑换入口（B）；升级、点数、续费绑卡、宽限期（C）；自动重付；账号登录或二次验证；资金对账报表。

## 4. 套餐表与配置

### 4.1 套餐表 `pay_plan_table`（`site_settings` 中的 JSON）

每个套餐：

| 字段 | 说明 |
|---|---|
| `key` | `go` / `plus` / `pro_5x` / `pro_20x` / `pro_50x`（与 ZovoCard 套餐键一致，卡池策略的 `PlanKey` 也用它） |
| `label` | 显示名 |
| `chatgpt_plan_name` | 创建支付会话用的套餐名。预填：plus `chatgptplusplan`、pro_5x `chatgptprolite`、pro_20x `chatgptpro`、pro_50x `chatgptpromax`、go 留空。**全部待用测试账号核实**（§12） |
| `expect_plan_type` | 确认开通时账号应显示的套餐类型。预填：plus `plus`、pro_20x `pro`、pro_50x `promax`，其余留空。待核实 |
| `regions` | 地区 → `{enabled, currency, cap_minor, fund_cents}` |

地区字段：

| 字段 | 说明 |
|---|---|
| `enabled` | 是否可下单 |
| `currency` | 付款币种，预填 PH→PHP、US→USD、JP→JPY、CL→CLP、EG→EGP |
| `cap_minor` | 应付金额上限，按付款币种最小单位（JPY、CLP 无小数，其余两位小数） |
| `fund_cents` | 注资金额，美元分。用后余额由卡池转回主钱包，可适当宽松 |

只有 `enabled`、`chatgpt_plan_name` 非空、`cap_minor > 0`、`fund_cents > 0`、`expect_plan_type` 非空同时满足才可真实付款；试运行只要求 `chatgpt_plan_name` 非空。金额默认留空，由管理员按试运行读到的实际价格填写。

### 4.2 其它配置

| 项 | 存储 | 默认 |
|---|---|---|
| `pay_region_proxies`：地区 → 代理地址（`http://` 或 `socks5://`，可带账号密码） | `secretbox` 加密，AAD 为键名；只写不读，读出只返回各地区是否已配置 | 空（直连） |
| `pay_concurrency`：浏览器并发数 | 明文 | 1（允许 1–3） |

## 5. 数据模型

金额用 int64 最小单位；时间存 UTC。

**`pay_orders`**

| 字段 | 说明 |
|---|---|
| `id`、`order_no` | `order_no` 唯一：`PO` + `yyMMddHHmmss`（UTC）+ 8 位十六进制，共 22 位 |
| `source`、`source_ref` | 本子项目只有 `admin_test`（`source_ref` 为管理员用户名）；B 增加 `cdk` |
| `dry_run` | 1 为试运行 |
| `plan_key`、`region`、`currency` | |
| `cap_minor`、`fund_cents` | 建单时套餐表的快照 |
| `account_email`、`account_id` | 邮箱小写去空格 |
| `state` | §6 |
| `session_sealed` | 会话密文（AAD `pay_order_session`），终态时清空 |
| `checkout_session_id`、`checkout_url` | `cs_live_…` 与支付链接 |
| `amount_minor`、`amount_text` | 支付页读到的应付金额（解析值与原文） |
| `card_id`、`card_last4`、`lease_token`、`fund_order_no` | |
| `billing_state` | 用的是哪个免税州（排查被拒用，不存完整地址） |
| `submitted_at` | 非空即已提交；之后绝不再提交 |
| `failure` | 失败类别：`not_submitted`（提交前放弃，未扣款）/ `declined`（提交后被拒） |
| `error_code`、`error_message` | 原因与页面提示文字（截断 500 字） |
| `verify_until`、`next_check_at` | 确认阶段的截止与下次检查时间 |
| `resolved_by`、`resolve_note` | 人工定论 |
| `created_at`、`updated_at`、`finished_at` | |

部分唯一索引：同一 `account_email` 只能有一张未到终态的单据（冲突映射为 `ErrInflight`）。

**`pay_order_events`**：`id`、`order_no`、`at`、`state`、`level`（`info` / `warn` / `error`）、`message`、`screenshot`（文件名，可空）。

## 6. 付款流程与状态机

| # | 状态 | 做什么 | 失败时 |
|---|---|---|---|
| 1 | `queued` | `Submit` 校验套餐 × 地区；会话须带 sessionToken 和邮箱（纯 accessToken 不收）；会话加密入库；入队 | 直接返回错误，不建单 |
| 2 | `checking` | 刷新 access token，查账号；已有有效订阅则拒绝（升级属 C）；记下 account_id | `failed` / `not_submitted` |
| 3 | `link_created` | 按套餐表创建支付会话，记下 `checkout_session_id` 与链接 | 同上 |
| 4 | `opening` | 占用浏览器名额：启动 Chrome（按地区走代理）→ 注入会话 cookie → 打开支付页 → 读出应付金额与币种。币种不符、金额超过 `cap_minor` 或读不出则放弃。**试运行到此结束**，进入 `previewed` | 同上 |
| 5 | `funding` | `Acquire`（`PlanKey`、`OrderRef = order_no`、邮箱）→ `Fund(fund_cents)` → `WaitOrder` 最多 3 分钟 | 放弃，`Release(aborted)`；注资结果不明的由卡池对账收敛 |
| 6 | `filling` | `Sensitive` 取卡号 → 填卡号、有效期、CVC、持卡人英文名、免税州地址。页面若出现已存卡，选「新卡」；不填 Stripe Link 的邮箱与手机 | 放弃，`Release(aborted)` |
| 7 | `submitted` | 记 `submitted_at` 后点「支付」。之后任何情况都不再点第二次 | — |
| 8 | `requires_3ds` | 出现 3DS 验证框：`WaitOTP(card_id, submitted_at)` 最多 5 分钟 → 填入 → 提交 | 转第 9 步 |
| 9 | `verifying` | 关闭浏览器。每 15 秒查一次账号，最多 10 分钟，套餐类型变为 `expect_plan_type` 即成功。参考依据：支付页显示的成功或拒绝、卡池收到的该卡在 `submitted_at` 之后的授权交易 | 见下 |

终态：`succeeded`、`failed`、`needs_review`、`previewed`（试运行完成）。

确认阶段的定论：

| 情况 | 结果 |
|---|---|
| 账号已是目标套餐 | `succeeded` |
| 支付页明确显示被拒，再等 2 分钟（让授权推送到达）后该卡没有成功授权、账号也未开通 | `failed` / `declined` |
| 10 分钟内既未开通又无明确拒绝（超时、浏览器崩溃、提示不明） | `needs_review`，Telegram 告警 |
| 支付页显示被拒，但该卡有成功授权 | `needs_review`（证据冲突，交人工） |

卡的释放（确认阶段结束后，或提交前放弃时立即）：成功 → `Release(success)`；被拒 → `Release(failed, 拒绝原因)`，计入健康度；提交前放弃或待人工 → `Release(aborted)`，不计入健康度。释放后卡池照常转出余额并回收。卡池租约时长低于 25 分钟时，配置页给出提示（第 5–9 步最长约 22 分钟）。

待人工的单据，管理员看时间线、页面提示、截图和授权交易后标记成功或失败（必须填备注）；标记只改本单结果，不触发任何付款。

服务重启：`queued` 照常继续；`checking`、`link_created`、`opening`、`funding`、`filling` 判 `failed` / `not_submitted`（错误为「服务重启，未提交」），有租约的 `Release(aborted)`；`submitted`、`requires_3ds` 转 `verifying` 继续确认。

并发：工作协程按建单顺序取 `queued` 单据，同时处理的单据数不超过 `pay_concurrency`。

超时常量（不做成配置）：页面加载 60 秒、填表 2 分钟、提交后等待页面结果 2 分钟、验证码 5 分钟、注资 3 分钟、确认 10 分钟（每 15 秒）。

## 7. 浏览器与 ChatGPT 交互

- **Chrome 启动**：`--user-data-dir` 用临时目录，单据结束即删除；不带自动化标记（同原型 `spawnRealChrome` 的做法）；`--disable-dev-shm-usage`。有 DISPLAY 用之；否则若装了 Xvfb，程序启动一个共享 Xvfb 实例并以有界面模式运行；都没有则用 `--headless=new` 并在自检中提示。Chrome 路径自动探测，可用 `CHROME_PATH` 覆盖。
- **代理**：Chrome 用 `--proxy-server`；带账号密码时用 CDP `Fetch.authRequired` 应答。tls-client 用 `WithProxyUrl`。
- **会话 cookie**：按原型 `lib/session.js` 的规则从会话构造 chatgpt.com 的 cookie 注入。
- **选择器**：卡号、有效期、CVC、姓名、国家、地址、城市、州、邮编、支付按钮、成功与拒绝标志、3DS 框与验证码输入框，全部集中在 `paybrowser/selectors.go`，初值取自原型 `fill-checkout.js`。跨主页面与各级 iframe 查找。
- **金额读取**：从支付页汇总区读出总额原文，按币种小数位解析为最小单位；读不出视为失败。
- **ChatGPT 接口**：账号查询沿用 `gptcheck` 用的 `accounts/check` 接口；token 刷新沿用 `gptcheck` 的 sessionToken 刷新；创建支付会话的接口与请求体按原型参数（套餐名、国家、币种）实现，确切形态在第一个实现任务用测试账号核实（§12）。

## 8. 敏感数据

- **用户会话**：加密存储，终态时清空；不打日志，任何接口都不返回。
- **完整卡号、CVV**：只在 `filling` 时存在内存中；不进数据库、日志、时间线、截图。
- **截图**（存 `<数据库所在目录>/pay-shots/<order_no>/`，7 天后删除）：填卡之前可截整页；填卡之后只截 3DS 验证框所在 iframe 元素，不截整页（支付表单里有完整卡号）；被拒或出错时只记录页面提示文字。截图只能通过后台接口查看。
- **Chrome 临时目录**：含用户 cookie，每单结束删除。
- **3DS 验证码**：沿用卡池规则（取用后清除密文）。
- **代理密码**：加密存储，只写不读。
- **审计**：测试兑换、人工定论、配置与代理修改写审计日志，日志不含会话、卡号、代理密码。

## 9. 后台接口与页面

### 9.1 接口（`/api/v1/admin/pay/…`，管理员鉴权）

| 方法与路径 | 用途 |
|---|---|
| `POST /orders` | 测试兑换：`{session, plan_key, region, dry_run}`；同账号进行中返回 409 |
| `GET /orders?state=&page=&page_size=` | 订单列表 |
| `GET /orders/:no` | 单据、时间线、该卡在提交后的授权交易 |
| `POST /orders/:no/resolve` | 人工定论 `{result: succeeded\|failed, note}`，只允许 `needs_review` 状态 |
| `GET /orders/:no/shots/:name` | 查看截图 |
| `GET /config`、`PUT /config` | 套餐表与并发数；返回中附卡池租约时长是否不足 25 分钟 |
| `GET /proxies`、`PUT /proxies` | 各地区代理（只写；空字符串表示不修改，`"-"` 表示清除） |
| `POST /browser-check` | 实际启动一次 Chrome 打开空白页，返回版本、显示模式（有界面 / 无头）与耗时；未安装时返回安装命令 |

### 9.2 页面：`/ops` 下「MarsWallex 兑换」，三个标签页

| 标签 | 内容 |
|---|---|
| 测试兑换 | 粘贴会话，选套餐与地区，勾选「试运行」→ 显示本次金额上限与注资金额 → 真实付款时弹确认框（会动钱）→ 提交后跳到该单详情 |
| 订单 | 列表：单号、套餐、地区、邮箱、状态、应付金额、卡后四位、时间，按状态筛选；详情抽屉：时间线、页面提示、截图、授权交易；待人工的单可「标记成功 / 标记失败」 |
| 配置 | 套餐表编辑、各地区代理、并发数、租约时长提示、「浏览器自检」 |

## 10. 部署

- `deploy/install.sh` 新增「安装浏览器依赖」步骤（Debian / Ubuntu：Google Chrome 官方源的 `google-chrome-stable`、`xvfb`、`fonts-liberation`、`fonts-noto-cjk`），已装则跳过；参数 `--browser-deps` 只执行这一步。
- 后台「版本更新」只替换程序、不运行安装脚本，因此**已部署的服务器升级后须手动执行一次** `--browser-deps`。CHANGELOG 升级注意与「浏览器自检」都给出这条命令。
- `Dockerfile` 运行镜像安装 `chromium`、`xvfb`、字体（约增加 400MB），设置 `CHROME_PATH`。
- 服务器内存建议至少 2GB（并发 1）。

## 11. 测试与验收

### 11.1 自动化测试

| 部分 | 方法 |
|---|---|
| `chatgpt` | httptest 模拟 ChatGPT 的账号查询、token 刷新、创建支付会话（基础地址可注入） |
| `paybrowser` | httptest 提供仿造支付页（卡号字段在 iframe 中、金额汇总、3DS 验证框、成功与被拒两种结果），用真 Chrome 跑；找不到 Chrome 时自动跳过 |
| `payengine` | 假 ChatGPT 客户端与假浏览器（接口注入），加真实卡池与 `mwtest` 模拟服务器。覆盖：顺利付款；要 3DS；被拒；金额超上限放弃（不选卡、不注资）；注资结果不明放弃；提交后浏览器崩溃，确认阶段查到开通 → 成功，查不到 → 待人工；被拒但有成功授权 → 待人工；重启恢复各状态；同账号进行中冲突；终态清空会话；试运行不碰卡池 |
| handler | 路由注册、鉴权、409、只写代理、人工定论只限待人工 |
| 前端 | Vitest 单元测试，加一次 Playwright 浏览器冒烟 |

### 11.2 生产验收（每一步用户确认后再做）

1. 服务器执行 `--browser-deps`，「浏览器自检」通过且为有界面模式。
2. 用测试账号对每个「套餐 × 地区」试运行，核实 §12 各项，按实际价格填套餐表。
3. 第一笔真实付款：最便宜的组合。
4. 逐个放开其余组合。

## 12. 待核实（第一个实现任务用测试账号完成，不花钱）

需要用户提供一个测试用 ChatGPT 账号会话。

1. 创建支付会话的接口地址、请求体、返回字段，支付页地址形态（`pay.openai.com` 或 `chatgpt.com/checkout/…`）。
2. 各套餐的 `chatgpt_plan_name`（Go 尤其未知）与开通后的 `plan_type`（`expect_plan_type`）。
3. 各地区是否需要对应地区的出口 IP 才能拿到该地区价格。
4. 支付页各字段与金额汇总的选择器。
5. MarsWallex 发卡行 3DS 页面结构与成功页标志（只能在第一笔真实付款时看到）。

## 13. 风险

- **MarsWallex 卡能否付 ChatGPT 未经验证**。第一笔真实付款被拒时要逐项排查：账单地址与持卡人登记地址不一致、卡 BIN 所属国、出口 IP 地区、3DS，不能直接断定卡不可用。
- ChatGPT 内部接口与支付页结构无公开文档，随时可能变；选择器集中一处，失败留截图与提示文字。
- Cloudflare 或 Stripe 风控可能拦截自动化浏览器。
- 免税州地址可能导致被拒（用户已知悉并选择）。
- 卡池默认一卡一用，被拒也消耗一张卡。
