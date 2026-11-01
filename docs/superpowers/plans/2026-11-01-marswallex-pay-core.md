# MarsWallex 付款核心 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 输入「ChatGPT 会话 + 套餐 + 付款地区」，系统用 MarsWallex 卡池的一张卡在服务器真 Chrome 里自动完成 ChatGPT 订阅付款并确认开通；后台提供测试兑换、订单与配置页。

**Architecture:** 三个新包：`internal/chatgpt`（tls-client 调 ChatGPT：会话、账号、创建支付会话）、`internal/paybrowser`（chromedp 驱动 Chrome 填 Stripe 支付页与 3DS）、`internal/payengine`（单据状态机、排队、调卡池、确认开通、对账协程）。后台接口在 `internal/handler/pay.go`，前端新页 `MarsWallexPay.vue`。引擎通过接口依赖卡池、ChatGPT 与浏览器，测试用假实现，另有一个接真实卡池 + `mwtest` 的集成测试。

**Tech Stack:** Go 1.24（Gin、SQLite、`github.com/bogdanfinn/tls-client`、新增 `github.com/chromedp/chromedp`）；Vue 3 + Element Plus + Vitest。

**Spec:** `docs/superpowers/specs/2026-11-01-marswallex-pay-core-design.md`

## Global Constraints

- Go 模块 `github.com/tuzi/cdk-recharge-system`，代码在 `backend/`；唯一新增依赖 `github.com/chromedp/chromedp`（及其传递依赖）。
- 所有用户可见文字、错误消息、日志说明用简体中文。
- 完整卡号、CVV、用户会话、代理密码不得出现在日志、时间线、截图、接口返回、审计；会话只以 `secretbox` 密文（AAD `pay_order_session`）存于 `pay_orders.session_sealed`，终态时清空。
- 点击「支付」之前写入 `submitted_at`；之后同一单据绝不再次点击支付；系统从不自动重付。
- 金额一律 int64 最小单位；时间存 UTC Unix 秒（同 `mw_*` 表）。
- 超时（引擎默认值，测试可缩短）：页面加载 60 秒、填表 2 分钟、提交后等待页面结果 2 分钟、验证码 5 分钟、注资 3 分钟、确认 10 分钟（每 15 秒查一次）、被拒后等待授权推送 2 分钟。
- 套餐键：`go`、`plus`、`pro_5x`、`pro_20x`、`pro_50x`；地区：`PH`、`US`、`JP`、`CL`、`EG`；币种预填 PH→PHP、US→USD、JP→JPY、CL→CLP、EG→EGP；JPY、CLP 无小数，其余两位。
- 免税州：DE / OR / NH / MT / AK，国家 `US`。
- 单号：`PO` + `yyMMddHHmmss`（UTC）+ 8 位小写十六进制，共 22 位，正则 `^PO\d{12}[0-9a-f]{8}$`。
- 测试不访问真实 ChatGPT、MarsWallex 或外网；需要 Chrome 的测试在找不到 Chrome 时 `t.Skip`。
- 已知与本计划无关的既有失败测试：`internal/handler` 的 `TestIssueProductKeepsGPTDefault`、`TestXIssueInfersProductWithoutQueryParameter`，不要修。
- 每个提交信息以 `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` 结尾。

## Review Focus

1. **会话粘贴的各种形态**：用户原样粘贴 `chatgpt.com/api/auth/session` 的 JSON（邮箱在 `user.email`）、前后带空白、用 `session_token` 别名、或缺 sessionToken / 邮箱——应能解析或给出明确中文错误，不能建出一张必然失败的单。→ Task 2 `TestParseSession` 表驱动。
2. **各币种金额原文**：`₱1,120.00`、`PHP 1,120.00`、`US$20.00`、`¥3,000`、`CLP$19.000`（智利用点作千分位）、`E£1,000.00`、带「Total due today」等前缀——解析错一位就会误放行或误拦截。→ Task 5 `TestParseAmount` 表驱动。
3. **卡有效期格式**：`MMyy`、`MM/yy`、`MM/yyyy`、`yyyy-MM`——填错有效期必被拒还白白消耗一张卡。→ Task 7 `TestParseExpiry`。
4. **确认阶段前租约已到期**：卡池已自动回收卡，`Release` 返回 `cardpool.ErrLeaseInvalid`——单据仍须进入终态，不能卡死或反复重试。→ Task 8 `TestReleaseLeaseExpiredTolerated`。
5. **截图接口路径穿越**：`/orders/:no/shots/:name` 收到 `..`、`%2f`、非法单号——必须 400，不能读到数据目录外的文件。→ Task 9 `TestPayShotRejectsTraversal`。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `backend/internal/gptcheck/check.go`（改） | 导出 `ParseSessionToken`、`DefaultAccount`，供 `chatgpt` 复用 |
| `backend/internal/chatgpt/session.go` | `Session`、`ParseSession`、`SessionCookies` |
| `backend/internal/chatgpt/client.go` | `Client`（按代理建 tls-client）、`Refresh`、`Account`、`CreateCheckout`、错误 |
| `backend/internal/chatgpt/live_test.go` | `//go:build live`：读 `CHATGPT_SESSION_FILE` 对真实 ChatGPT 试一次（只建支付会话，不付款） |
| `backend/internal/db/pay.go` | `MigratePay`：`pay_orders`、`pay_order_events`、索引 |
| `backend/internal/payengine/model.go` | 状态、失败类别、`Order`、`Event`、错误变量 |
| `backend/internal/payengine/store.go` | 单号、建单、状态迁移写事件、查询、终态清会话 |
| `backend/internal/payengine/config.go` | 套餐表、并发数、各地区代理 |
| `backend/internal/payengine/billing.go` | 免税州地址、有效期解析 |
| `backend/internal/payengine/service.go` | `Options`、`Service`、`Submit`、`Get`、`List`、`Evidence`、`Resolve` |
| `backend/internal/payengine/worker.go` | 排队与并发、步骤 2–6 |
| `backend/internal/payengine/settle.go` | 步骤 7–9、确认协程、重启恢复、截图清理 |
| `backend/internal/paybrowser/chrome.go` | 找 Chrome、Xvfb、启动与关闭、自检 |
| `backend/internal/paybrowser/page.go` | `Open`、`ReadAmount`、`Fill`、`Submit`、`WaitOutcome`、`EnterOTP`、`Shot` |
| `backend/internal/paybrowser/selectors.go` | 全部页面选择器 |
| `backend/internal/paybrowser/amount.go` | `CurrencyExponent`、`ParseAmount` |
| `backend/internal/paybrowser/testdata/*.html` | 仿造支付页 |
| `backend/internal/cardpool/cardinfo.go` | `CardInfo`：后四位与持卡人英文名 |
| `backend/internal/handler/pay.go` | 后台接口 |
| `backend/internal/server/http.go`（改） | 启动引擎、注册路由 |
| `frontend/src/lib/pay.ts`、`frontend/src/views/admin/MarsWallexPay.vue` | 前端 |
| `deploy/install.sh`、`Dockerfile`、`deploy/app.env.example`、`deploy/README-release.md`（改） | 浏览器依赖 |

---

### Task 1: 实地核实 ChatGPT 支付接口与支付页（控制者与用户一起做，不写产品代码）

**Files:**
- Create: `docs/superpowers/notes/2026-11-01-chatgpt-checkout-probe.md`
- Modify: `docs/superpowers/specs/2026-11-01-marswallex-pay-core-design.md`（§4.1 预填值、§12 标记已核实项）

**Interfaces:**
- Produces: 笔记里的「创建支付会话」请求（方法、路径、请求头名、JSON 体）与响应字段名、支付页地址形态、各字段与金额汇总的实际选择器、各套餐 `plan_name` 与开通后 `plan_type`（能查到的）。Task 2 与 Task 5/6 以笔记为准；笔记与本计划的默认值冲突时以笔记为准，并同步改对应测试。

- [ ] **Step 1: 向用户要测试会话**：请用户把一个**免费** ChatGPT 测试账号的 `https://chatgpt.com/api/auth/session` 完整 JSON 存到 scratchpad 下的 `chatgpt-session.json`（不进仓库）。用户暂时给不了就跳到 Step 5，把本任务标为「延后」，后续任务按本计划默认值做。
- [ ] **Step 2: 抓创建支付会话的请求**：先用 Playwright 浏览器注入 `__Secure-next-auth.session-token` cookie 打开 `https://chatgpt.com/#pricing`，点 Plus 的升级按钮，从网络请求里找到创建支付会话的请求与响应。被 Cloudflare 拦截则请用户在自己的浏览器 DevTools 里点一次升级，右键该请求「Copy as cURL」，删掉 Cookie 与 Authorization 后贴过来。记录地区与币种参数如何传。
- [ ] **Step 3: 看支付页结构**：打开拿到的支付链接（不填卡），用 `browser_snapshot` / `browser_evaluate` 列出主页面与各 iframe 里的输入框 id、name、autocomplete，支付按钮文字，金额汇总元素的选择器与原文；在 PH 地区再生成一次，记下 PHP 金额原文。
- [ ] **Step 4: 写笔记**：按 Interfaces 的 Produces 各项写入笔记，响应示例中的令牌、`cs_live` 编号后半段打码；写明是从哪个出口网络探测的。更新 spec §4.1 预填值与 §12 已核实项。
- [ ] **Step 5: 提交**

```bash
git add docs/superpowers/notes/2026-11-01-chatgpt-checkout-probe.md docs/superpowers/specs/2026-11-01-marswallex-pay-core-design.md
git commit -m "docs(pay): 实地核实 ChatGPT 支付接口与支付页结构"
```

---

### Task 2: `internal/chatgpt` 包

**Files:**
- Modify: `backend/internal/gptcheck/check.go`（`parseSessionToken` → `ParseSessionToken`，`defaultAccount` → `DefaultAccount`，改调用处）
- Create: `backend/internal/chatgpt/session.go`、`backend/internal/chatgpt/client.go`
- Test: `backend/internal/chatgpt/session_test.go`、`backend/internal/chatgpt/client_test.go`、`backend/internal/chatgpt/live_test.go`

**Interfaces:**
- Consumes: `gptcheck.ParseAccessToken`、`gptcheck.ParseSessionToken`、`gptcheck.DefaultAccount`
- Produces:
  - `type Session struct{ AccessToken, SessionToken, Email string }`
  - `func ParseSession(raw string) (Session, error)`：错误均 `errors.Is(err, ErrBadSession)`，消息中文说明缺什么
  - `type Cookie struct{ Name, Value string }`；`func SessionCookies(sessionToken string) []Cookie`（`__Secure-next-auth.session-token`，超过 3933 字节分片为 `.0`、`.1`…）
  - `type Client struct{ BaseURL string /* 默认 https://chatgpt.com */ }`；`func New(proxyURL string) (*Client, error)`（空串直连；只接受 `http`、`https`、`socks5` 协议）
  - `func (c *Client) Refresh(ctx context.Context, s *Session) error`（`GET /api/auth/session` 带会话 cookie，更新 `s.AccessToken`）
  - `type Account struct{ AccountID, Email, PlanType string; HasActiveSubscription bool }`；`func (c *Client) Account(ctx context.Context, s Session) (Account, error)`（`GET /backend-api/accounts/check/v4-2023-04-27`；无有效订阅时 `PlanType = "free"`）
  - `type Checkout struct{ SessionID, URL string }`；`func (c *Client) CreateCheckout(ctx context.Context, s Session, planName, country, currency string) (Checkout, error)`
  - `var ErrBadSession, ErrUnauthorized error`（401/403 映射为 `ErrUnauthorized`）

- [ ] **Step 1: 写失败测试**

```go
func TestParseSession(t *testing.T) // 表驱动：
// 原样 /api/auth/session JSON（accessToken、sessionToken、user.email）→ 三字段齐全，Email 小写去空格
// 前后带空白与换行 → 同上
// 用 session_token / access_token 别名 → 同上
// 缺 sessionToken → errors.Is(err, ErrBadSession) 且 err.Error() 含 "sessionToken"
// 缺邮箱 → ErrBadSession 且含 "邮箱"
// 纯 accessToken 字符串 → ErrBadSession
func TestSessionCookiesChunks(t *testing.T) // 8000 字节 token → 3 个 cookie，名 ...session-token.0/.1/.2，拼接等于原值；短 token → 1 个不带后缀
func TestNewRejectsBadProxy(t *testing.T)   // "ftp://x" 报错；"socks5://u:p@127.0.0.1:1080"、"" 不报错
func TestRefreshUpdatesAccessToken(t *testing.T) // httptest 校验收到的 cookie 值等于 sessionToken，返回 {"accessToken":"new"} → s.AccessToken == "new"
func TestAccountParsesPlan(t *testing.T)    // 返回 accounts.default 结构（account.account_id="acc-1"、plan_type="plus"、entitlement.has_active_subscription=false）→ Account{AccountID:"acc-1", PlanType:"free", HasActiveSubscription:false}
func TestCreateCheckoutRequestShape(t *testing.T) // 校验 POST /backend-api/payments/checkout、Authorization: Bearer <AccessToken>、JSON 体 plan_name=chatgptplusplan、billing_details.country=PH、billing_details.currency=PHP；返回 {"checkout_session_id":"cs_live_x","url":"https://pay.openai.com/c/pay/cs_live_x"} → Checkout{SessionID:"cs_live_x", URL:"https://pay.openai.com/c/pay/cs_live_x"}
func TestUnauthorizedMapsToErr(t *testing.T) // 401 → errors.Is(err, ErrUnauthorized)
```

请求路径与 JSON 体以 Task 1 笔记为准；笔记未出时用上面的默认形态。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/chatgpt/ -run 'TestParseSession|TestSessionCookies|TestNew|TestRefresh|TestAccount|TestCreateCheckout|TestUnauthorized' -v`
Expected: 编译失败（包与符号不存在）

- [ ] **Step 3: 实现**：gptcheck 两个函数改名导出并改调用处；`client.go` 每个 `Client` 自建 tls-client（`profiles.Chrome_131`、`WithForceHttp1`、有代理时 `WithProxyUrl`），请求头同 `gptcheck`（User-Agent、Referer `https://chatgpt.com/`），响应体上限 4MB；`Account` 用 `gptcheck.DefaultAccount` 取默认账号，取值规则同 `gptcheck.Check` 的 summary。
- [ ] **Step 4: 写 `live_test.go`**（`//go:build live`）：`TestLiveCreateCheckout` 读 `CHATGPT_SESSION_FILE`，依次 `Refresh`、`Account`、`CreateCheckout("chatgptplusplan","PH","PHP")`，只打印 `PlanType` 与 `SessionID` 前 12 位；未设置环境变量时 `t.Skip`。
- [ ] **Step 5: 运行通过**

Run: `cd backend && go test ./internal/chatgpt/ ./internal/gptcheck/ ./internal/handler/ -v 2>&1 | tail -20`
Expected: chatgpt、gptcheck 全部 PASS；handler 只有两个已知失败

- [ ] **Step 6: 提交**

```bash
git add backend/internal/gptcheck backend/internal/chatgpt
git commit -m "feat(chatgpt): 会话解析、token 刷新、账号查询与创建支付会话"
```

---

### Task 3: 付款单据表与存储

**Files:**
- Create: `backend/internal/db/pay.go`、`backend/internal/payengine/model.go`、`backend/internal/payengine/store.go`
- Modify: `backend/internal/db/db.go`（`Migrate` 末尾在 `MigrateCardPool` 之后调用 `MigratePay`）
- Test: `backend/internal/db/pay_test.go`、`backend/internal/payengine/store_test.go`

**Interfaces:**
- Produces:
  - `db.MigratePay() error`（可重复执行）。`pay_orders` 列见 spec §5，另加 `page_outcome TEXT NOT NULL DEFAULT ''`（`success` / `declined` / `unknown`，确认阶段定论用）与 `session_sealed`；部分唯一索引 `uq_pay_orders_inflight ON pay_orders(account_email) WHERE state NOT IN ('succeeded','failed','needs_review','previewed')`；`pay_order_events(id, order_no, at, state, level, message, screenshot)` 与 `order_no` 索引。
  - 状态常量 `StateQueued="queued"`、`StateChecking="checking"`、`StateLinkCreated="link_created"`、`StateOpening="opening"`、`StateFunding="funding"`、`StateFilling="filling"`、`StateSubmitted="submitted"`、`StateRequires3DS="requires_3ds"`、`StateVerifying="verifying"`、`StateSucceeded="succeeded"`、`StateFailed="failed"`、`StateNeedsReview="needs_review"`、`StatePreviewed="previewed"`；`func IsTerminal(state string) bool`
  - `FailureNotSubmitted="not_submitted"`、`FailureDeclined="declined"`
  - `type Order struct`（字段与列一一对应，JSON 用列名；`SessionSealed` 带 `json:"-"`）、`type Event struct{ ID int64; OrderNo string; At int64; State, Level, Message, Screenshot string }`
  - 错误：`ErrNotFound`、`ErrInflight`、`ErrStateChanged`、`ErrPlanUnavailable`、`ErrBadSession`、`ErrBadRequest`
  - `func newOrderNo(now time.Time) string`
  - `func insertOrder(ctx context.Context, o *Order) error`（唯一索引冲突 → `ErrInflight`；同时写一条 `queued` 事件）
  - `func transition(ctx context.Context, no, from, to, level, msg string, set map[string]any) error`（仅当当前状态为 `from` 时更新 `state`、`updated_at` 与 `set` 中的列并写事件，否则 `ErrStateChanged`；`set` 的列名白名单校验）
  - `func addEvent(ctx context.Context, no, level, msg, screenshot string) error`（不改状态，事件 `state` 取当前状态）
  - `func finish(ctx context.Context, no, from, to, level, msg string, set map[string]any) error`（同 `transition`，另设 `finished_at` 并清空 `session_sealed`）
  - `func getOrder(ctx context.Context, no string) (*Order, error)`、`func listOrders(ctx context.Context, state string, page, size int) ([]Order, int, error)`（按 id 倒序，size 默认 20、上限 100）、`func listEvents(ctx context.Context, no string) ([]Event, error)`、`func ordersInStates(ctx context.Context, states ...string) ([]Order, error)`

- [ ] **Step 1: 写失败测试**

```go
func TestMigratePayIdempotent(t *testing.T)        // 连续调用两次 MigratePay 无错；pay_orders 有 page_outcome 列
func TestNewOrderNoFormat(t *testing.T)            // 匹配 ^PO\d{12}[0-9a-f]{8}$，长度 22，时间部分为 UTC
func TestInsertOrderInflightUnique(t *testing.T)   // 同邮箱两张未终态 → 第二张 ErrInflight；第一张 finish 到 failed 后再插成功；previewed 也算终态
func TestTransitionChecksFrom(t *testing.T)        // from 不符 → ErrStateChanged 且状态不变；成功时写一条事件，State 为新状态
func TestTransitionRejectsUnknownColumn(t *testing.T) // set 含 "state; DROP" → 报错
func TestFinishClearsSession(t *testing.T)         // finish 后 session_sealed == ""、finished_at 非空
func TestListOrdersFilterAndPage(t *testing.T)     // 按 state 筛选、size 0→20、101→100、id 倒序
```

测试用 `dbtest.Open(t)` 后调 `db.MigratePay()`。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/db/ ./internal/payengine/ -run 'Pay|OrderNo|Insert|Transition|Finish|ListOrders' -v`
Expected: 编译失败

- [ ] **Step 3: 实现** `db/pay.go` 与 `payengine/model.go`、`store.go`（查询语句放在 payengine 包内，写法同 cardpool）。
- [ ] **Step 4: 运行通过**（同 Step 2 命令，全部 PASS）
- [ ] **Step 5: 提交**

```bash
git add backend/internal/db backend/internal/payengine
git commit -m "feat(payengine): 付款单据表、状态迁移与时间线存储"
```

---

### Task 4: 套餐表、并发数与地区代理配置

**Files:**
- Create: `backend/internal/payengine/config.go`
- Test: `backend/internal/payengine/config_test.go`

**Interfaces:**
- Consumes: `db.GetSetting(key string) (string, error)`、`db.SetSetting(key, value string) error`、`db.SetSecretSetting`、`db.GetSecretSetting`
- Produces:
  - `type RegionConf struct{ Enabled bool `json:"enabled"`; Currency string `json:"currency"`; CapMinor int64 `json:"cap_minor"`; FundCents int64 `json:"fund_cents"` }`
  - `type PlanConf struct{ Key string `json:"key"`; Label string `json:"label"`; ChatGPTPlanName string `json:"chatgpt_plan_name"`; ExpectPlanType string `json:"expect_plan_type"`; Regions map[string]RegionConf `json:"regions"` }`
  - `var PlanKeys = []string{"go","plus","pro_5x","pro_20x","pro_50x"}`、`var Regions = []string{"PH","US","JP","CL","EG"}`
  - `func DefaultPlanTable() []PlanConf`、`func LoadPlanTable() ([]PlanConf, error)`、`func SavePlanTable(p []PlanConf) error`（键 `pay_plan_table`）
  - `func ResolvePlan(planKey, region string, dryRun bool) (PlanConf, RegionConf, error)`（不满足 spec §4.1 条件 → `ErrPlanUnavailable`，消息说明缺什么）
  - `func LoadConcurrency() int`、`func SaveConcurrency(n int) error`（键 `pay_concurrency`）
  - `func LoadProxies() (map[string]string, error)`、`func ProxyStatus() (map[string]bool, error)`、`func SaveProxies(in map[string]string) error`（键 `pay_region_proxies`，整体 JSON 加密存储）

- [ ] **Step 1: 写失败测试**

```go
func TestDefaultPlanTable(t *testing.T)
// 顺序 go, plus, pro_5x, pro_20x, pro_50x；每个都有 5 个地区，币种 PHP/USD/JPY/CLP/EGP，全部 Enabled=false、金额 0
// ChatGPTPlanName：plus=chatgptplusplan、pro_5x=chatgptprolite、pro_20x=chatgptpro、pro_50x=chatgptpromax、go=""（Task 1 笔记有新值则以笔记为准）
// ExpectPlanType：plus=plus、pro_20x=pro、pro_50x=promax，go 与 pro_5x 为空
func TestResolvePlanRules(t *testing.T)
// plus/PH 未启用 → ErrPlanUnavailable；启用但 CapMinor=0 → ErrPlanUnavailable 且消息含「金额上限」；
// 启用且金额齐全 → OK；go/PH 金额齐全但 ExpectPlanType 空 → 真实付款 ErrPlanUnavailable，dryRun=true 时因 ChatGPTPlanName 空仍 ErrPlanUnavailable；
// pro_5x/PH 未启用、dryRun=true → OK（试运行只要求套餐名）；未知套餐或地区 → ErrPlanUnavailable
func TestLoadPlanTableFillsMissing(t *testing.T)  // 只存了 plus 的 JSON → 读出 5 个套餐，plus 保留已存值，缺的地区补默认
func TestSavePlanTableValidates(t *testing.T)     // 未知套餐键、未知地区、负金额、币种不是 3 位大写字母 → 报错；合法表存后读回相等
func TestConcurrencyRange(t *testing.T)           // 默认 1；Save(0)/Save(4) 报错；Save(2) 后读回 2；存了非法值时读出 1
func TestProxiesWriteOnly(t *testing.T)
// 无主密钥 → SaveProxies 报错；有主密钥：Save{"PH":"socks5://u:p@h:1080"} → ProxyStatus PH=true、其余 false，LoadProxies PH 原值；
// Save{"PH":""} 保留原值；Save{"PH":"-"} 清除；Save{"PH":"ftp://x"} 报错；未知地区报错；库里存的是密文（不含 "socks5"）
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/payengine/ -run 'PlanTable|ResolvePlan|Concurrency|Proxies' -v`
Expected: 编译失败

- [ ] **Step 3: 实现** `config.go`。读出时与默认表合并：按 `PlanKeys`、`Regions` 补齐缺项，丢弃未知键。
- [ ] **Step 4: 运行通过**（同 Step 2，全部 PASS）
- [ ] **Step 5: 提交**

```bash
git add backend/internal/payengine/config.go backend/internal/payengine/config_test.go
git commit -m "feat(payengine): 套餐表、并发数与地区代理配置"
```

---

### Task 5: `paybrowser` 启动、自检、打开支付页与读金额

**Files:**
- Create: `backend/internal/paybrowser/chrome.go`、`page.go`（本任务只含 `Open`、`ReadAmount`）、`selectors.go`、`amount.go`、`export_test.go`、`testdata/checkout.html`、`testdata/card.html`
- Modify: `backend/go.mod`、`backend/go.sum`（`go get github.com/chromedp/chromedp`）
- Test: `backend/internal/paybrowser/amount_test.go`、`chrome_test.go`、`page_test.go`

**Interfaces:**
- Produces:
  - `func CurrencyExponent(cur string) int`（JPY、CLP → 0，其余 2）
  - `func ParseAmount(text, currency string) (int64, error)`
  - `func FindChrome() (string, error)`：先 `CHROME_PATH`（文件不存在则报错，不回落），再依次 `/usr/bin/google-chrome-stable`、`/usr/bin/google-chrome`、`/usr/bin/chromium`、`/usr/bin/chromium-browser`、`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`；都没有 → `ErrNoChrome`
  - `const InstallHint = "sudo bash /opt/cdk-recharge/install.sh --browser-deps"`（实现时按 install.sh 实际安装位置核对路径）
  - `type LaunchOptions struct{ ProxyURL, ShotDir string }`
  - `func Launch(ctx context.Context, o LaunchOptions) (*Browser, error)`：临时 `--user-data-dir`；参数含 `--no-first-run`、`--no-default-browser-check`、`--disable-dev-shm-usage`、`--disable-blink-features=AutomationControlled`、`--window-size=1366,900`，不带 `--enable-automation`；代理用 `--proxy-server`（不含账号密码），有账号密码时启用 CDP `Fetch` 并在 `Fetch.authRequired` 时应答；显示：有 `DISPLAY` 用之，否则 `Xvfb` 在 PATH 中则启动一次共享的 `Xvfb :99 -screen 0 1366x900x24` 并设 `DISPLAY=:99`，都没有则 `--headless=new`
  - `func (b *Browser) Mode() string`（`"headful"` / `"headless"`）、`func (b *Browser) Close()`（结束 Chrome 并删除临时目录，可重复调用）
  - `type CheckResult struct{ OK bool; Version, Mode string; ElapsedMs int64; Error, InstallHint string }`；`func Check(ctx context.Context) CheckResult`
  - `type Cookie struct{ Name, Value, Domain string }`
  - `func (b *Browser) Open(ctx context.Context, url string, cookies []Cookie) error`（先设 cookie 再导航，等金额元素出现，超时 60 秒）
  - `func (b *Browser) ReadAmount(ctx context.Context, currency string) (minor int64, text string, err error)`
  - `selectors.go`：`var Sel = struct{ CardNumber, Expiry, CVC, Name, Country, Line1, City, State, Zip, PayButton, Amount, Success, DeclineText, ChallengeFrame, OTPInput, OTPSubmit, NewCardOption []string }`，初值取自 `automation/fill-checkout.js` 与 Task 1 笔记
  - `export_test.go`：测试专用开关，让 Launch 对回环地址也走代理（`--proxy-bypass-list=<-loopback>`）

- [ ] **Step 1: 写失败测试**

```go
func TestParseAmount(t *testing.T) // 表驱动（text, currency → minor）：
// "₱1,120.00","PHP"→112000；"PHP 1,120.00","PHP"→112000；"US$20.00","USD"→2000；"$20","USD"→2000；
// "¥3,000","JPY"→3000；"CLP$19.000","CLP"→19000；"$19.000","CLP"→19000；"E£1,000.00","EGP"→100000；
// "Total due today ₱1,120.00","PHP"→112000；""、"免费"、"1,120.00.00" → 报错
func TestCurrencyExponent(t *testing.T) // JPY、CLP→0；PHP、USD、EGP→2；小写也识别
func TestFindChromeEnvOverride(t *testing.T) // CHROME_PATH 指向临时可执行文件 → 返回该路径；指向不存在的文件 → 报错
func TestCheckReportsVersion(t *testing.T)  // 需 Chrome：OK=true、Version 非空、Mode 为 headful 或 headless
func TestCloseRemovesProfileDir(t *testing.T) // 需 Chrome：Close 后临时目录不存在；再 Close 不 panic
func TestOpenAndReadAmount(t *testing.T)    // 需 Chrome：httptest 提供 testdata；服务端校验收到 cookie "__Secure-next-auth.session-token"；ReadAmount("PHP") → 112000, "₱1,120.00"
func TestProxyAuth(t *testing.T)            // 需 Chrome：测试内起一个要求 Proxy-Authorization 的 HTTP 代理，ProxyURL 带账号密码 → Open 成功且代理收到正确凭据
```

仿造页 `checkout.html`：金额元素、地址字段在主页面，卡号 / 有效期 / CVC 在 `card.html` iframe 中，字段 id 与 `Sel` 首选项一致（Task 6 会继续扩充支付按钮行为与 3DS）。

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/paybrowser/ -v`
Expected: 编译失败

- [ ] **Step 3: 实现** `amount.go`：去掉货币符号与字母前缀后取最后一段数字；指数为 0 的币种把 `.` 与 `,` 都当千分位；指数为 2 的币种以最后一个 `.` 为小数点、`,` 为千分位；小数位数不符报错。其余文件按 Interfaces 实现；`Check` 的启动失败时 `InstallHint` 为 `InstallHint`。
- [ ] **Step 4: 运行通过**

Run: `cd backend && go test ./internal/paybrowser/ -v`
Expected: 全部 PASS（本机有 Chrome；无 Chrome 的用例显示 SKIP）

- [ ] **Step 5: 提交**

```bash
git add backend/go.mod backend/go.sum backend/internal/paybrowser
git commit -m "feat(paybrowser): 启动 Chrome、自检、打开支付页与读取金额"
```

---

### Task 6: `paybrowser` 填表、提交、3DS 与截图规则

**Files:**
- Modify: `backend/internal/paybrowser/page.go`、`selectors.go`、`testdata/checkout.html`
- Create: `backend/internal/paybrowser/testdata/acs.html`
- Test: `backend/internal/paybrowser/page_test.go`

**Interfaces:**
- Consumes: Task 5 的 `Browser`、`Sel`
- Produces:
  - `type Card struct{ Number, ExpMonth, ExpYear, CVC string }`（`ExpMonth` 两位、`ExpYear` 两位）
  - `type Billing struct{ Name, Line1, City, State, Zip, Country string }`
  - `func (b *Browser) Fill(ctx context.Context, c Card, bl Billing) error`：页面有已存卡时先点 `NewCardOption`；不填 Stripe Link 的邮箱与手机；任一字段找不到 → 错误消息含中文字段名（如「CVC」）；成功后内部标记已填卡
  - `func (b *Browser) Submit(ctx context.Context) error`：第二次调用返回 `ErrAlreadySubmitted`，不点击
  - `type Outcome string`：`OutcomeSuccess="success"`、`OutcomeDeclined="declined"`、`OutcomeChallenge="challenge"`、`OutcomeUnknown="unknown"`
  - `func (b *Browser) WaitOutcome(ctx context.Context, timeout time.Duration) (Outcome, string, error)`：返回页面提示文字（截断 500 字）；超时返回 `OutcomeUnknown`
  - `func (b *Browser) EnterOTP(ctx context.Context, otp string) error`：在 3DS 框（任意层级 iframe）里填验证码并提交
  - `type ShotScope int`：`ShotPage`、`ShotChallenge`；`func (b *Browser) Shot(ctx context.Context, name string, scope ShotScope) (string, error)`：文件写到 `ShotDir`，返回文件名；已填卡后 `ShotPage` 返回 `ErrShotForbidden`；`ShotChallenge` 只截 3DS 框元素
  - 错误：`ErrAlreadySubmitted`、`ErrShotForbidden`

- [ ] **Step 1: 写失败测试**（全部需 Chrome；仿造页按查询参数 `mode=success|decline|3ds` 决定提交后的表现：success 显示成功标志；decline 显示 "Your card was declined."；3ds 插入 `acs.html` iframe，填 "123456" 提交后成功，其它值显示拒绝）

```go
func TestFillSubmitSuccess(t *testing.T)     // mode=success → WaitOutcome == OutcomeSuccess；仿造页服务端收到的卡号等于输入值
func TestDeclineReturnsMessage(t *testing.T) // mode=decline → OutcomeDeclined，消息含 "declined"
func TestChallengeThenOTP(t *testing.T)      // mode=3ds → OutcomeChallenge；EnterOTP("123456") 后 WaitOutcome → OutcomeSuccess
func TestSubmitTwiceRefused(t *testing.T)    // 第二次 Submit → ErrAlreadySubmitted，仿造页记录的提交次数为 1
func TestFillReportsMissingField(t *testing.T) // 仿造页 ?nocvc=1 去掉 CVC 框 → Fill 报错且含 "CVC"
func TestShotRules(t *testing.T)             // Fill 前 Shot(ShotPage) 生成 png；Fill 后 Shot(ShotPage) → ErrShotForbidden；3ds 模式下 Shot(ShotChallenge) 生成 png
func TestSavedCardChoosesNew(t *testing.T)   // ?saved=1 时页面先显示已存卡，Fill 点「新卡」后填写成功
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/paybrowser/ -run 'Fill|Decline|Challenge|SubmitTwice|Shot|SavedCard' -v`
Expected: 编译失败

- [ ] **Step 3: 实现**：字段查找遍历主页面与所有层级 iframe（chromedp 按 frame 的 execution context 执行），卡号类字段逐字键入（每字 35ms），文本字段填后按 Esc 并核对；3DS 框识别用 `Sel.ChallengeFrame`，验证码框优先 `autocomplete="one-time-code"`，否则取框内唯一可见的文本或数字输入框。
- [ ] **Step 4: 运行通过**

Run: `cd backend && go test ./internal/paybrowser/ -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/paybrowser
git commit -m "feat(paybrowser): 填卡与地址、提交、3DS 验证码与截图规则"
```

---

### Task 7: 引擎骨架、排队与步骤 2–4（含试运行）

**Files:**
- Create: `backend/internal/payengine/service.go`、`worker.go`、`billing.go`、`fakes_test.go`
- Test: `backend/internal/payengine/service_test.go`、`billing_test.go`

**Interfaces:**
- Consumes: Task 2 `chatgpt.*`、Task 3 存储、Task 4 配置、Task 5/6 `paybrowser.*`
- Produces:
  - `type Pool interface { Acquire(context.Context, cardpool.AcquireRequest) (*cardpool.Lease, error); Fund(context.Context, *cardpool.Lease, int64) (*cardpool.Order, error); WaitOrder(context.Context, string) (*cardpool.Order, error); Sensitive(context.Context, *cardpool.Lease) (*marswallex.CardSensitive, error); WaitOTP(context.Context, string, time.Time) (string, error); Release(context.Context, *cardpool.Lease, cardpool.Outcome) error; CardInfo(context.Context, string) (cardpool.CardInfo, error); ListTransactions(context.Context, cardpool.TxnFilter) ([]cardpool.Transaction, int, error) }`
  - `type ChatGPT interface { Refresh(context.Context, *chatgpt.Session) error; Account(context.Context, chatgpt.Session) (chatgpt.Account, error); CreateCheckout(context.Context, chatgpt.Session, string, string, string) (chatgpt.Checkout, error) }`
  - `type Browser interface { Open(context.Context, string, []paybrowser.Cookie) error; ReadAmount(context.Context, string) (int64, string, error); Fill(context.Context, paybrowser.Card, paybrowser.Billing) error; Submit(context.Context) error; WaitOutcome(context.Context, time.Duration) (paybrowser.Outcome, string, error); EnterOTP(context.Context, string) error; Shot(context.Context, string, paybrowser.ShotScope) (string, error); Close() }`
  - `type Timeouts struct{ PageLoad, Fill, SubmitResult, OTP, Fund, Verify, VerifyEvery, DeclineGrace time.Duration }`；`func DefaultTimeouts() Timeouts`（值见 Global Constraints）
  - `type Options struct{ Pool Pool; ChatGPT func(proxyURL string) (ChatGPT, error); Browser func(ctx context.Context, proxyURL, shotDir string) (Browser, error); Now func() time.Time; Alert func(string); ShotDir string; Timeouts *Timeouts }`（`ChatGPT` 为 nil 用 `chatgpt.New`，`Browser` 为 nil 用 `paybrowser.Launch`，`ShotDir` 为空用 `DB_PATH` 所在目录下的 `pay-shots`）
  - `func New(o Options) *Service`、`func (s *Service) Start(ctx context.Context)`、`func (s *Service) Wait()`
  - `type Request struct{ PlanKey, Region, Session, Source, SourceRef string; DryRun bool }`；`func (s *Service) Submit(ctx context.Context, r Request) (*Order, error)`
  - `func (s *Service) Get(ctx context.Context, no string) (*Order, []Event, error)`、`func (s *Service) List(ctx context.Context, state string, page, size int) ([]Order, int, error)`
  - `func TaxFreeAddress(r *rand.Rand) paybrowser.Billing`（`Name` 留空由调用方填；`Country="US"`；州取自 DE / OR / NH / MT / AK，城市与邮编前缀取自 `automation/lib/address.js` 的表）
  - `func parseExpiry(s string) (mm, yy string, err error)`
  - 本任务内真实付款单在 `opening` 读完金额且未超上限后，暂以 `failed` / `not_submitted`、`error_code="pay_not_ready"` 结束（Task 8 替换为步骤 5–9）

- [ ] **Step 1: 写失败测试**（`fakes_test.go` 提供 `fakePool`、`fakeChatGPT`、`fakeBrowser`，记录每个方法的调用次数与参数；`fakeBrowser.Open` 可配置阻塞通道）

```go
func TestSubmitValidates(t *testing.T)
// 套餐未启用 → ErrPlanUnavailable；会话缺 sessionToken → ErrBadSession；同邮箱已有进行中单 → ErrInflight；
// 合法 → 状态 queued，session_sealed 非空且不含原会话中的 accessToken
func TestDryRunPreviewed(t *testing.T)
// 免费账号、金额 112000 → 终态 previewed，amount_minor=112000、amount_text="₱1,120.00"、checkout_session_id 已记，
// fakePool 所有方法调用次数为 0，session_sealed 清空，事件依次含 checking、link_created、opening、previewed
func TestAlreadySubscribedFails(t *testing.T)   // HasActiveSubscription=true → failed / not_submitted，error_code="already_subscribed"，未调 CreateCheckout
func TestUnauthorizedFails(t *testing.T)        // Account 返回 chatgpt.ErrUnauthorized → failed / not_submitted，error_code="session_invalid"
func TestAmountOverCapAborts(t *testing.T)      // 真实付款单，金额 > cap_minor → failed / not_submitted，error_code="amount_over_cap"，fakePool 未被调用，浏览器已 Close
func TestAmountUnreadableAborts(t *testing.T)   // ReadAmount 报错 → error_code="amount_unreadable"
func TestProxyPassedThrough(t *testing.T)       // 配置 PH 代理后，ChatGPT 与 Browser 工厂收到同一个代理地址
func TestBrowserLaunchFailureAlerts(t *testing.T) // Browser 工厂报错 → failed / not_submitted，error_code="browser_failed"，Alert 1 次且文本含「浏览器」
func TestConcurrencyLimit(t *testing.T)         // 并发 1，两张单，第一张 Open 阻塞 → 同时 Open 的最大数为 1；放行后第二张也完成
func TestParseExpiry(t *testing.T)              // "0828"→("08","28")；"08/28"、"08/2028"、"2028-08"→同上；"13/28"、"abc"→报错
func TestTaxFreeAddress(t *testing.T)           // 固定种子，100 次：State ∈ {DE,OR,NH,MT,AK}、Country=="US"、Zip 为 5 位数字、Line1 非空
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/payengine/ -run 'Submit|DryRun|AlreadySubscribed|Unauthorized|AmountOver|AmountUnreadable|Proxy|Concurrency|ParseExpiry|TaxFree' -v`
Expected: 编译失败

- [ ] **Step 3: 实现**：工作协程从库里按 id 取 `queued`，受 `LoadConcurrency()` 限制；`Submit` 写库后踢醒工作协程。每个步骤用 `transition` 推进；失败一律 `finish(..., StateFailed, ...)` 并在 `set` 里写 `failure`、`error_code`、`error_message`。浏览器在 `opening` 开始时创建，任何出口都 `Close`；`opening` 读金额前截一张整页图（`01-opening.png`）。会话 cookie 的 `Domain` 为 `.chatgpt.com`。
- [ ] **Step 4: 运行通过**

Run: `cd backend && go test ./internal/payengine/ -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/payengine
git commit -m "feat(payengine): 引擎骨架、排队并发、预检、生成链接与试运行"
```

---

### Task 8: 步骤 5–9、确认协程、重启恢复、人工定论与截图清理

**Files:**
- Create: `backend/internal/payengine/settle.go`、`backend/internal/cardpool/cardinfo.go`
- Modify: `backend/internal/payengine/worker.go`（去掉 `pay_not_ready`，接步骤 5–9）、`service.go`（`Resolve`、`Evidence`）
- Test: `backend/internal/payengine/settle_test.go`、`backend/internal/payengine/integration_test.go`、`backend/internal/cardpool/cardinfo_test.go`

**Interfaces:**
- Consumes: Task 7 全部；`cardpool.ResultSuccess`、`ResultFailed`、`ResultAborted`、`cardpool.ErrLeaseInvalid`
- Produces:
  - `type CardInfo struct{ Last4, HolderFirstName, HolderLastName string }`；`func (s *cardpool.Service) CardInfo(ctx context.Context, cardID string) (CardInfo, error)`（查 `mw_cards` 关联 `mw_cardholders`；卡不存在 → `cardpool.ErrNotFound`）
  - `func (s *Service) Resolve(ctx context.Context, no, result, note, by string) error`（只允许 `needs_review`，`result` 为 `succeeded` / `failed`，`note` 去空白后非空，否则 `ErrBadRequest`；写 `resolved_by`、`resolve_note`）
  - `func (s *Service) Evidence(ctx context.Context, no string) ([]cardpool.Transaction, error)`（该单卡在 `submitted_at` 之后的交易；未提交返回空）
  - 步骤 5–9 规则见 spec §6；确认协程每 5 秒扫描一次 `verifying` 且 `next_check_at` 已到的单据

- [ ] **Step 1: 写失败测试**

```go
func TestCardInfo(t *testing.T) // cardpool 包内：插入持卡人 JOHN/DOE 与卡 last4=4242 → CardInfo{Last4:"4242", HolderFirstName:"JOHN", HolderLastName:"DOE"}；未知卡 → ErrNotFound
func TestHappyPathSucceeded(t *testing.T)
// WaitOutcome=success，Account 第 3 次返回 plan_type=plus → succeeded；Release(success) 1 次；Submit 1 次；
// 填表收到的 Billing.Name=="JOHN DOE"、Card.ExpMonth/ExpYear 来自 Sensitive 的 ExpiryDate；card_last4、billing_state、submitted_at 已记；session_sealed 清空
func Test3DSFlow(t *testing.T)       // WaitOutcome 先 challenge → WaitOTP 的 since == submitted_at → EnterOTP 收到 "123456" → 再 WaitOutcome success → succeeded；ShotChallenge 被调用
func TestOTPTimeoutNeedsReview(t *testing.T) // WaitOTP 超时 → verifying；账号始终 free、无授权 → 过 Verify 时长后 needs_review，Alert 1 次，Release(aborted)
func TestDeclinedAfterGrace(t *testing.T)    // WaitOutcome=declined "Your card was declined." → 过 DeclineGrace 后 failed / declined，Release(failed) 的 ErrorCode 非空，error_message 含 "declined"
func TestDeclinedButApprovedAuthNeedsReview(t *testing.T) // 页面被拒，但 ListTransactions 有该卡 Kind=AUTH、Status=approved、OccurredAt > submitted_at → needs_review
func TestApprovedAuthBeforeSubmitIgnored(t *testing.T)    // 同上但 OccurredAt < submitted_at → failed / declined
func TestBrowserErrorAfterSubmitVerifies(t *testing.T)    // WaitOutcome 返回错误 → verifying；Account 返回 plus → succeeded；Submit 仅 1 次
func TestFundFailureReleasesAborted(t *testing.T)  // WaitOrder 返回 state=failed 的单 → failed / not_submitted，error_code="fund_failed"，Release(aborted)，未调 Sensitive
func TestFundTimeoutReleasesAborted(t *testing.T)  // WaitOrder 超时 → error_code="fund_timeout"，Release(aborted)
func TestFillFailureReleasesAborted(t *testing.T)  // Fill 报错 → failed / not_submitted，Release(aborted)，Submit 0 次
func TestNoCardFails(t *testing.T)                 // Acquire 返回 cardpool.ErrNoCard → failed / not_submitted，error_code="no_card"
func TestReleaseLeaseExpiredTolerated(t *testing.T) // Release 返回 cardpool.ErrLeaseInvalid → 单据仍到终态，多一条 warn 事件，不重试 Release
func TestRecoveryOnStart(t *testing.T)
// 预置各状态单据后 Start：queued 照常处理；checking/link_created/opening/funding/filling → failed / not_submitted，error_message="服务重启，未提交"，
// 有 lease_token 的按单据字段重建 Lease 调 Release(aborted)；submitted/requires_3ds → verifying；所有单据 Submit 调用 0 次
func TestResolve(t *testing.T)   // needs_review + note → 状态与 resolved_by 更新；note 空白 → ErrBadRequest；对 succeeded 单 → ErrBadRequest
func TestShotCleanup(t *testing.T) // pay-shots 下修改时间早于 7 天的单号目录被删除，新目录保留
func TestEngineWithRealCardPool(t *testing.T)
// 真实 cardpool.Service + mwtest：SQL 插入持卡人与一张 available 卡，cardpool.SavePolicy 配 plus 的卡产品优先级；
// mwtest 对转入、查单、取卡号、转出返回成功；假 ChatGPT 与假浏览器走成功路径 → succeeded，卡转入 fund_cents 后被释放（pool_state 不再是 leased）
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/payengine/ ./internal/cardpool/ -run 'CardInfo|HappyPath|3DS|OTPTimeout|Declined|ApprovedAuth|BrowserError|Fund|FillFailure|NoCard|ReleaseLease|Recovery|Resolve|ShotCleanup|RealCardPool' -v`
Expected: 编译失败或断言失败

- [ ] **Step 3: 实现**：
  - 步骤 5 `Acquire` 的 `PlanKey` 为套餐键、`OrderRef` 为单号；`Fund` 后用带 `Timeouts.Fund` 的 ctx 调 `WaitOrder`。
  - 步骤 7 先 `transition` 到 `submitted`（写 `submitted_at`）再调 `Browser.Submit`。
  - 步骤 8 结束（含出错）立即 `Close` 浏览器，转 `verifying`：`page_outcome` 记 `success` / `declined` / `unknown`；`verify_until` 为 declined 时 now + `DeclineGrace`，否则 now + `Verify`。
  - 确认协程定论规则照 spec §6 表格。成功授权的判定：`Kind == "AUTH"`、`Status == "approved"`、`OccurredAt` 晚于 `submitted_at`。
  - 释放时 `ErrLeaseInvalid` 只记 warn 事件。
  - 截图清理每天跑一次。
- [ ] **Step 4: 运行通过**

Run: `cd backend && go test ./internal/payengine/ ./internal/cardpool/ -v 2>&1 | tail -30`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/payengine backend/internal/cardpool/cardinfo.go backend/internal/cardpool/cardinfo_test.go
git commit -m "feat(payengine): 选卡注资、填表提交、3DS、确认开通、重启恢复与人工定论"
```

---

### Task 9: 后台接口与服务启动

**Files:**
- Create: `backend/internal/handler/pay.go`
- Modify: `backend/internal/server/http.go`（建引擎、`handler.SetPayEngine`、`Start`；在 admin 组注册路由）
- Test: `backend/internal/handler/pay_test.go`、`backend/internal/server/routes_test.go`

**Interfaces:**
- Consumes: Task 7/8 的 `payengine.Service` 方法、Task 4 配置函数、`paybrowser.Check`、`cardpool.LoadPolicy`、`auditAdmin`
- Produces:
  - `func SetPayEngine(s *payengine.Service)`；`payRoute` 包装，引擎为 nil 时 503「付款引擎未启动」
  - 路由（`/api/v1/admin/pay`）：`POST /orders`、`GET /orders`、`GET /orders/:no`（返回 `{order, events, evidence}`）、`POST /orders/:no/resolve`、`GET /orders/:no/shots/:name`、`GET /config`、`PUT /config`、`GET /proxies`、`PUT /proxies`、`POST /browser-check`
  - `GET /config` 返回 `{plans, concurrency, lease_minutes, lease_warning}`，`lease_warning = lease_minutes < 25`
  - 错误映射：`ErrInflight`→409；`ErrPlanUnavailable`、`ErrBadSession`、`ErrBadRequest`→400；`ErrNotFound`→404；其余→500「操作失败」
  - 审计动作：`pay_test_submit`（详情：套餐、地区、dry_run、单号）、`pay_order_resolve`、`pay_config_update`、`pay_proxies_update`（详情只列地区）
  - `server/http.go`：`payengine.New(payengine.Options{Pool: pool, Alert: cardPoolAlert(notify.SendText)})`（`*cardpool.Service` 直接满足 `Pool`），`Start(ctx)`

- [ ] **Step 1: 写失败测试**

```go
func TestPayRoutesRegistered(t *testing.T)      // server 包：上述 10 条路由都在
func TestPayEngineNilReturns503(t *testing.T)
func TestPaySubmitMapsErrors(t *testing.T)      // 同邮箱进行中 → 409；未启用套餐 → 400；坏会话 → 400；成功 → 200 带 order_no
func TestPaySubmitAuditHasNoSession(t *testing.T) // 审计 detail 含 plan、region、order_no，不含会话中的 accessToken 与 sessionToken
func TestPayProxiesWriteOnly(t *testing.T)      // PUT 后 GET 只返回 {"PH":true,...}，响应体不含代理主机名
func TestPayShotRejectsTraversal(t *testing.T)  // no="../x"、name="..%2f..%2fetc"、name="a.png"（不符 ^\d{2}-[a-z0-9_]+\.png$）→ 400；合法文件 → 200 且 Content-Type image/png
func TestPayResolveRequiresNote(t *testing.T)   // note 为空 → 400
func TestPayConfigLeaseWarning(t *testing.T)    // cardpool 策略 lease_minutes=20 → lease_warning=true；30 → false
func TestPayBrowserCheckNoChrome(t *testing.T)  // CHROME_PATH 指向不存在文件 → 200，ok=false，install_hint 含 "--browser-deps"
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/handler/ ./internal/server/ -run 'Pay' -v`
Expected: 编译失败

- [ ] **Step 3: 实现** `pay.go` 与 `http.go` 改动；截图接口用 `filepath.Join(shotDir, no, name)` 前先按正则校验单号与文件名。
- [ ] **Step 4: 运行通过**

Run: `cd backend && go test ./internal/handler/ ./internal/server/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'`
Expected: 只有两个已知失败

- [ ] **Step 5: 提交**

```bash
git add backend/internal/handler/pay.go backend/internal/handler/pay_test.go backend/internal/server
git commit -m "feat(handler): MarsWallex 兑换后台接口与引擎启动"
```

---

### Task 10: 前端「MarsWallex 兑换」页

**Files:**
- Create: `frontend/src/lib/pay.ts`、`frontend/src/lib/__tests__/pay.test.ts`、`frontend/src/views/admin/MarsWallexPay.vue`
- Modify: `frontend/src/router/index.ts`（`/ops/marswallex-pay`，name `MarsWallexPay`）、`frontend/src/layouts/AdminLayout.vue`（导航「MarsWallex 兑换」，排在「MarsWallex 钱包」之后）、`frontend/src/element.ts`（按需注册新用到的组件与图标）

**Interfaces:**
- Consumes: Task 9 接口
- Produces:
  - `payApi<T>(path, method?, body?)`（前缀 `/api/v1/admin/pay`，写法同 `poolApi`）
  - `stateLabel(state: string): string`：queued 已受理、checking 预检中、link_created 已生成链接、opening 打开支付页、funding 备卡中、filling 填表中、submitted 已提交、requires_3ds 等待 3DS、verifying 确认中、succeeded 成功、failed 失败、needs_review 待人工、previewed 试运行完成
  - `failureLabel(f: string): string`：not_submitted「提交前放弃，未扣款」、declined「提交后被拒」
  - `formatMinor(minor: number, currency: string): string`：按币种小数位，千分位逗号，后接币种代码，如 `1,120.00 PHP`、`3,000 JPY`
  - `isTerminal(state: string): boolean`

- [ ] **Step 1: 写失败测试** `pay.test.ts`：13 个状态的 `stateLabel` 与上表一致，未知状态原样返回；`formatMinor(112000,'PHP') === '1,120.00 PHP'`、`formatMinor(3000,'JPY') === '3,000 JPY'`、`formatMinor(19000,'CLP') === '19,000 CLP'`、`formatMinor(0,'USD') === '0.00 USD'`；`isTerminal` 对 succeeded/failed/needs_review/previewed 为 true、其余 false。
- [ ] **Step 2: 运行确认失败**

Run: `cd frontend && npx vitest run src/lib/__tests__/pay.test.ts`
Expected: FAIL（模块不存在）

- [ ] **Step 3: 实现** `pay.ts` 与页面：
  - 「测试兑换」：会话文本框、套餐与地区下拉（来自 `/config`）、「试运行（不选卡、不付款）」开关，显示本次金额上限与注资金额。真实付款前弹 `dialog.confirm('将从卡池取一张卡注资 {fund} 美元并真实付款，应付金额上限 {cap}。确认提交？', {title:'确认付款', okText:'确认付款', danger:true})`。提交成功后切到「订单」并打开该单详情。
  - 「订单」：表格 + 状态筛选 + 分页；详情抽屉显示时间线（截图可点开）、页面提示、授权交易；非终态每 5 秒刷新；待人工的单显示「标记成功 / 标记失败」，备注必填。
  - 「配置」：套餐表按套餐分组编辑，金额输入按币种小数位换算；各地区代理只写（已配置显示「已配置」，留空不改，提供「清除」）；并发数；`lease_warning` 为 true 时显示「卡池租约时长少于 25 分钟，可能在确认阶段前到期」；「浏览器自检」按钮显示版本、模式、耗时，失败时显示安装命令。
- [ ] **Step 4: 运行通过**

Run: `cd frontend && npx vitest run && npm run build`
Expected: 测试全部 PASS，构建成功

- [ ] **Step 5: 提交**

```bash
git add frontend/src
git commit -m "feat(frontend): MarsWallex 兑换页（测试兑换、订单、配置）"
```

---

### Task 11: 部署安装浏览器依赖

**Files:**
- Modify: `deploy/install.sh`、`Dockerfile`、`deploy/app.env.example`、`deploy/README-release.md`

**Interfaces:**
- Produces: `install.sh --browser-deps` 只执行「安装浏览器依赖」；正常安装流程也执行这一步；`paybrowser.InstallHint` 中的命令与此一致

- [ ] **Step 1: 实现 `install_browser_deps`**：已有 `google-chrome-stable` 或 `chromium` 且有 `Xvfb` 时跳过；有 `apt-get` 时安装 `wget gnupg ca-certificates`，把 Google 签名公钥写到 `/etc/apt/keyrings/google-chrome.gpg`，添加 `deb [arch=amd64 signed-by=/etc/apt/keyrings/google-chrome.gpg] https://dl.google.com/linux/chrome/deb/ stable main` 源，再安装 `google-chrome-stable xvfb fonts-liberation fonts-noto-cjk`；没有 `apt-get` 时打印中文提示说明需手动安装，然后跳过、不中断安装。参数解析新增 `--browser-deps`。核对 `install.sh` 实际安装路径，使 `paybrowser.InstallHint` 与之一致（不一致则改 `InstallHint` 常量并同步其测试）。
- [ ] **Step 2: `Dockerfile` 运行阶段**加装 `chromium xvfb fonts-liberation fonts-noto-cjk`，`ENV CHROME_PATH=/usr/bin/chromium`。
- [ ] **Step 3: 文档**：`app.env.example` 增加 `# CHROME_PATH=` 注释说明；`README-release.md` 增加「升级到含 MarsWallex 兑换的版本后，执行一次 `sudo bash …/install.sh --browser-deps`，再到后台点浏览器自检」。
- [ ] **Step 4: 校验**

Run: `bash -n deploy/install.sh && (command -v shellcheck >/dev/null && shellcheck -S warning deploy/install.sh || echo "未装 shellcheck，跳过") && cd backend && go test ./internal/paybrowser/ -run FindChrome -v`
Expected: 语法检查无输出；shellcheck 无 warning 级问题或跳过；测试 PASS

- [ ] **Step 5: 提交**

```bash
git add deploy Dockerfile backend/internal/paybrowser
git commit -m "chore(deploy): 安装 Chrome、Xvfb 与字体，新增 --browser-deps"
```

---

### Task 12: 全量验证与本地冒烟（控制者执行）

**Files:** 无新增（发现问题回到对应任务修）

- [ ] **Step 1: 后端全量**

Run: `cd backend && go vet ./... && go test ./... 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'`
Expected: vet 无输出；只有两个已知失败

- [ ] **Step 2: 前端全量**

Run: `cd frontend && npx vitest run && npm run build`
Expected: 全部 PASS，构建成功

- [ ] **Step 3: 本地冒烟**：用临时数据库起一个一次性服务（同 v1.5.1 冒烟时的环境变量写法），Playwright 打开「MarsWallex 兑换」页：三个标签渲染无控制台错误；保存套餐表与代理后刷新仍在、代理只显示「已配置」；浏览器自检成功并显示版本；未启用套餐提交返回中文错误。若 Task 1 拿到了测试会话：启用 plus/PH 试运行一次，确认走到「试运行完成」并显示真实金额，截图目录里只有填卡前的整页图。结束后停掉服务、删除临时数据。
- [ ] **Step 4: 更新 spec 状态行**为「已实现，待生产验收」，提交

```bash
git add docs/superpowers/specs/2026-11-01-marswallex-pay-core-design.md
git commit -m "docs(spec): 付款核心标记为已实现、待生产验收"
```
