# MarsWallex 钱包、持卡人管理与交易补拉 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 MarsWallex 剩余 14 个接口和 3 类 Webhook 接入后台：账户同步与子账户、兑换单与钱包转账单、收款地址与账户流水、持卡人新建 / 修改 / 启停（含证件照）、交易定时补拉、卡转入转出明细，以及新的「MarsWallex 钱包」页。

**Architecture:** 全部业务逻辑放在现有 `internal/cardpool` 包。兑换与钱包转账是 `mw_card_orders` 的两种新单据类型，复用子项目 2 的发送初判、查单、Webhook 定论、超时转人工；不涉及资金的操作直接调接口并同步本地表。`internal/handler` 新增两个文件挂接口，前端新增钱包页并抽出卡池公共函数。

**Tech Stack:** Go 1.24（module `github.com/tuzi/cdk-recharge-system`）、Gin、SQLite（`mattn/go-sqlite3`，支持部分索引）、`internal/marswallex`（含 `mwtest` 模拟服务）；前端 Vue 3 + Element Plus + Vite + Vitest。

**Spec:** `docs/superpowers/specs/2026-10-29-marswallex-wallet-design.md`；接口字段以 `docs/marswallex-openapi-zh.md` 为准；单据引擎见 `docs/superpowers/specs/2026-10-28-marswallex-card-pool-design.md` §6。

## Global Constraints

- ZovoCard 相关代码不改；`internal/marswallex` 只在缺字段时补字段，不改调用、签名与结果分类。不引入新的第三方依赖。
- 资金金额用 int64 分，调用接口用 `marswallex.FormatAmount` / `ParseAmount`。平台返回的余额、流水、成交结果按原字符串保存和展示。
- 时间列一律 `INTEGER` Unix 秒（UTC），取 `Service.now()`；调用平台的 `start_time` / `end_time` 一律传 Unix 秒。
- **持卡人敏感资料**（证件号、证件照、出生日期、SSN、手机、邮箱、地址）不写本地库、不写日志、不进审计；证件照只在内存中经过（Gin 默认 32MB 内存阈值，7MB 文件不会落临时文件）。
- **资金单（含兑换、钱包转账）任何情况下都不自动重发**；兑换、钱包转账各自同一时刻最多一笔在途（含 `needs_review`）。
- 告警一律走 `Options.Alert`，带 key 的告警同一北京自然日只发一次（沿用现有的 `s.alert(key, text)`）。
- 单号与商户侧 ID：两位前缀 + UTC `060102150405` + 8 位小写十六进制，共 22 位。前缀：`MC` 兑换、`MT` 钱包转账、`AC` 子账户、`CH` 持卡人。
- 固定参数：
  - 补拉每 15 分钟一轮；重叠 2 小时；首次窗口 7 天；每轮最多 7 天；按 24 小时分段；`page_size` 50；每段每个接口最多 200 页；
  - 账户同步 `page_size` 50；账户流水默认 7 天、最长 90 天，`page_size` 默认 20、最大 100；卡转入转出明细最长 90 天；
  - 账户名 1–50 字、不含空白字符；子账户启停原因 2–100 字、不含 emoji；转账备注最多 200 字；
  - 证件照单张 ≤ `7 << 20` 字节，只收 JPEG / PNG / PDF（按内容嗅探），每人 1–2 张；上传接口请求体上限 `7<<20 + 64<<10`。
- 已知原有失败的测试 `TestIssueProductKeepsGPTDefault`、`TestXIssueInfersProductWithoutQueryParameter` 不修复，验证时排除并说明。
- 前端文案为简体中文；沿用 Element Plus 与现有 `card` / `text-muted` 等样式类。
- 提交用仓库已配置的身份，提交信息末尾加 `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`。

## Review Focus

1. **账户余额字符串格式异常**（如 `"1,000.00"`、空串）：期望兑换、转账提交被拒并提示「余额格式无法识别」，不下单、不 panic。→ Task 3 `TestFundsRejectUnparsableBalance`。
2. **兑换的推送先于接口响应到达**：期望单据保持推送给出的 `success`，随后回来的 200 不把它改回 `processing`。→ Task 3 `TestConversionWebhookBeforeResponse`。
3. **补拉时平台 `count` 与实际不符**（`count` 很大但某页 `data` 为空，或一直返回满页）：期望该段在空页或 200 页上限处停止，不死循环。→ Task 6 `TestBackfillStopsOnShortOrEmptyPage`。
4. **证件照扩展名伪装**（HTML 内容命名为 `a.jpg`）：期望按内容嗅探拒绝，不转发给平台。→ Task 5 `TestUploadIDPhotoSniffsContent`。
5. **手动补拉与定时补拉同时运行**：期望两者串行，`cursor` 只增不减。→ Task 6 `TestBackfillConcurrentRunsSerialize`。

---

## File Structure

| 文件 | 职责 |
|---|---|
| `backend/internal/db/cardpool.go` | 新列、新表、新索引的迁移 |
| `backend/internal/cardpool/service.go` | 新单据类型常量、`ErrInflight`、Service 新字段 |
| `backend/internal/cardpool/model.go` | `Order` / `Cardholder` 新字段，`Account`、`Submission` 结构与读取 |
| `backend/internal/cardpool/orderno.go` | 新前缀、`newOutID` |
| `backend/internal/cardpool/orders.go` `reconcile.go` | 新类型的写入、描述、查单映射 |
| `backend/internal/cardpool/wallet.go`（新） | 账户同步、子账户新建与启停、收款地址、账户流水 |
| `backend/internal/cardpool/funds.go`（新） | 兑换单、钱包转账单提交与校验 |
| `backend/internal/cardpool/holders.go`（新） | 证件照、资料校验、持卡人新建 / 修改 / 启停、提交记录 |
| `backend/internal/cardpool/backfill.go`（新） | 交易补拉 |
| `backend/internal/cardpool/events.go` | 3 类新 Webhook、持卡人告警调整、「未交卡号出现授权」公共函数 |
| `backend/internal/cardpool/admin.go` | `ImportCardholder` 写商户侧 ID 并关联提交记录；卡转入转出明细 |
| `backend/internal/cardpool/run.go` | 维护循环加「账户同步」，新增补拉循环 |
| `backend/internal/handler/cardpool_wallet.go`（新） | 钱包、补拉、卡明细接口 |
| `backend/internal/handler/cardpool_holders.go`（新） | 持卡人接口 |
| `backend/internal/server/http.go` | 路由 |
| `frontend/src/lib/api.ts` | `FormData` 请求体不强加 JSON 头 |
| `frontend/src/lib/cardpool.ts`（新） | 卡池与钱包页公共函数 |
| `frontend/src/views/admin/MarsWallexWallet.vue`（新） | 钱包页 |
| `frontend/src/views/admin/components/CardholderForm.vue`（新） | 持卡人新建 / 修改表单 |
| `frontend/src/views/admin/CardPool.vue` | 持卡人、交易、卡详情、单据类型 |
| `frontend/src/router/index.ts`、`frontend/src/layouts/AdminLayout.vue` | 路由与导航 |

---

### Task 1: 数据模型与单据类型

**Files:**
- Modify: `backend/internal/db/cardpool.go`、`backend/internal/cardpool/service.go`、`model.go`、`orderno.go`、`orders.go`
- Test: `backend/internal/db/cardpool_test.go`、`backend/internal/cardpool/orderno_test.go`、`orders_test.go`

**Interfaces:**
- Consumes: 子项目 2 的 `MigrateCardPool`、`Order`、`insertPending`、`newOutOrderNo`
- Produces:

```go
const KindConversion = "conversion"; const KindWalletTransfer = "wallet_transfer"
var ErrInflight = errors.New("已有在途单据")
// Order 新增：BuyCurrency `json:"buy_currency"`、PayerAccountNo `json:"payer_account_no"`、
//            PayeeAccountNo `json:"payee_account_no"`、Detail string `json:"detail"`（成交结果 JSON 原文）
// Cardholder 新增：OutCardHolderID string `json:"out_card_holder_id"`
type Account struct {
	AccountNo, OutAccountNo, AccountName, AccountType, Purpose, Status string // json 用 snake_case
	Balances  []marswallex.BalanceItem `json:"balances"` // 库里为 JSON 文本，空为 []
	Gone      bool   `json:"gone"`
	SyncedAt  *int64 `json:"synced_at"`
	CreatedAt int64  `json:"created_at"`
}
const (SubmissionSubmitted = "submitted"; SubmissionUnknown = "unknown"; SubmissionLinked = "linked"; SubmissionAbandoned = "abandoned")
type Submission struct {
	OutCardHolderID, FirstName, LastName, CountryCode, State, CardHolderID, LastError, RequestedBy string
	CreatedAt, UpdatedAt int64
}
func newOutID(prefix string, now time.Time) string // newOutOrderNo 改为调用它
func getAccount(ctx context.Context, accountNo string) (*Account, error) // 不存在 → ErrNotFound
func getSubmission(ctx context.Context, outID string) (*Submission, error)
```

- 迁移（spec §4，全部可重复执行）：`mw_card_orders` 补 `buy_currency`、`payer_account_no`、`payee_account_no`、`detail`；建 `uq_mw_orders_wallet_inflight`（放在补列之后）；建 `mw_accounts`、`mw_cardholder_submissions`；`mw_cardholders` 补 `out_card_holder_id`。
- `insertPending` 同时写入新四列与 `note`；撞上 `uq_mw_orders_wallet_inflight` 时（错误信息含 `mw_card_orders.kind`）返回 `fmt.Errorf("%w: 已有在途的%s，请先处理", ErrInflight, kindNames[kind])`，不发请求。
- `kindNames` 增加「兑换单」「钱包转账单」；`orderPrefix` 增加 `MC`、`MT`；`describeOrder` 对两种新类型输出金额与币种（兑换加「→ 买入币种」，转账加「付款方 → 收款方」）。

- [ ] **Step 1: 写失败测试**

```go
// db/cardpool_test.go
func TestMigrateCardPoolWalletIdempotent(t *testing.T) // 先建子项目 2 的旧表并插一条旧单据 → Migrate 两次无误；四个新列、两张新表、mw_cardholders.out_card_holder_id 存在；旧单据保留且新列为 ''
func TestWalletInflightIndex(t *testing.T)            // 一条 unknown 的 conversion 后再插 pending conversion → 唯一约束失败；插 wallet_transfer 成功；conversion 改为 success 后可再插
// cardpool/orderno_test.go
func TestNewOutIDPrefixes(t *testing.T) // newOutID("MC"/"MT"/"AC"/"CH", now) 长度 22，前缀正确，时间段为 UTC 060102150405
// cardpool/orders_test.go
func TestInsertPendingWalletColumnsRoundTrip(t *testing.T) // 写入带 BuyCurrency/Payer/Payee/Note 的 conversion → getOrder 读回一致，Detail 为 ""
func TestInsertPendingWalletInflightReturnsErrInflight(t *testing.T) // 第二笔 conversion → errors.Is(err, ErrInflight)
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/db/ ./internal/cardpool/ -run 'Wallet|NewOutID|InsertPending' -count=1`
Expected: 编译失败或 FAIL

- [ ] **Step 3: 实现迁移、常量、结构与读写**

`Account.Balances` 读写时用 `encoding/json`，库里为空串或非法 JSON 时读成空切片。

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/db/ ./internal/cardpool/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/db backend/internal/cardpool
git commit -m "feat(cardpool): 钱包单据类型、账户表与持卡人提交记录表"
```

---

### Task 2: 账户同步与子账户

**Files:**
- Create: `backend/internal/cardpool/wallet.go`
- Modify: `service.go`（字段）、`events.go`（`ACCOUNT_STATUS_CHANGED`）、`run.go`（维护循环）
- Test: `backend/internal/cardpool/wallet_test.go`

**Interfaces:**
- Consumes: Task 1 的 `Account`、`getAccount`、`newOutID`
- Produces:

```go
func (s *Service) SyncAccounts(ctx context.Context) error
func (s *Service) ListAccounts(ctx context.Context) ([]Account, error) // gone=0 在前；MAIN 在前；其余按 created_at
func (s *Service) CreateSubAccount(ctx context.Context, name, by string) (*Account, error)
func (s *Service) SetSubAccountStatus(ctx context.Context, accountNo, status, remark, by string) (*Account, error)
func (s *Service) markAccountsDirty()
func (s *Service) syncAccountsIfDirty(ctx context.Context) error
```

- `SyncAccounts`：分页 `ListAccounts(ListAccountsQuery{PageNo, PageSize: 50})`，直到累计条数 ≥ `count` 或某页为空。全部成功后才在一个事务里 upsert 并把本次没出现的标 `gone = 1`（出现的置 0）；中途失败不改本地表。`out_account_no` 不被空值覆盖。
- 并发：`syncMu sync.Mutex` + `syncGen atomic.Uint64`。进入时先读 `gen`，拿到锁后若 `gen` 已变（等待期间别人同步成功）直接返回 nil；成功后 `gen++`。
- `CreateSubAccount`：`name` 去首尾空白后 1–50 字（按 rune）且不含任何空白字符（`unicode.IsSpace`），否则 `ErrBadRequest`。请求 `CreateAccountRequest{AccountName, OutAccountNo: newOutID("AC", now)}`，不传 `Purpose`。200 → 写入本地（`status` 取响应、`account_type = SUB_ACCOUNT`）并返回；出错原样返回平台错误（结果未知由前端在 10 秒后刷新）。
- `SetSubAccountStatus`：`status` 只能 `ACTIVE` / `DISABLED`；本地账户不存在 → `ErrNotFound`；`MAIN` → `ErrBadRequest`；`remark` 去空白后 2–100 字（rune），含 emoji（rune 落在 `U+1F000–U+1FAFF`、`U+2600–U+27BF`、`U+FE0F`）→ `ErrBadRequest`。调用后无论成败都尝试 `SyncAccounts`，返回最新本地账户；接口出错时返回接口错误。
- `ACCOUNT_STATUS_CHANGED`：按 `account_no` 更新 `status`，推送带 `out_account_no` 时写入；本地没有 → `markAccountsDirty()`，备注「账户未同步，已安排同步」。缺 `account_no` → `dataError`。
- `maintainRound`：在对账之后、回收之前加一步 `lg.check("同步账户", s.syncAccountsIfDirty(ctx))`（取不到客户端时跳过）。`syncAccountsIfDirty` 成功后清除标记，失败保留。

- [ ] **Step 1: 写失败测试**

```go
func TestSyncAccountsAllPages(t *testing.T)          // 第 1 页 50 条 count "51"，第 2 页 1 条 → 51 行；再同步时少 1 条 → 该行 gone=1；再次出现 → gone=0；out_account_no 不被清空
func TestSyncAccountsFailureKeepsLocal(t *testing.T) // 第 2 页 Unknown() → 返回错误，本地行数与 gone 不变
func TestSyncAccountsConcurrentShare(t *testing.T)   // 第 1 页 Hang 200ms；A 开始 50ms 后 B 开始 → 都返回 nil，GET /v1/accounts/page 只收到 1 轮请求
func TestCreateSubAccount(t *testing.T)              // ""、"a b"、"a\nb"、51 个字 → ErrBadRequest 且无请求；"Ops" → 请求体 {"data":{"account_name":"Ops","out_account_no":"AC…"}} 无 purpose，本地有该账户；Unknown() → 返回 OutcomeUnknown 错误，本地无新行
func TestSetSubAccountStatus(t *testing.T)           // MAIN → ErrBadRequest；remark "a"、101 字、"停用😀" → ErrBadRequest；正常 → PUT 体 {"account_status":"DISABLED","remark":"…"}，随后同步，本地 status=DISABLED；未知账户 → ErrNotFound
func TestAccountStatusWebhook(t *testing.T)          // 已知账户 → status 与 out_account_no 更新；未知账户 → 备注含「已安排同步」，下一轮 maintainRound 调用了 GET /v1/accounts/page 且标记清除
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/cardpool/ -run 'Account' -count=1`
Expected: 编译失败

- [ ] **Step 3: 实现 `wallet.go`，接入 `events.go` 与 `run.go`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/cardpool/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/cardpool
git commit -m "feat(cardpool): 账户同步、子账户新建与启停、账户状态推送"
```

---

### Task 3: 兑换单与钱包转账单

**Files:**
- Create: `backend/internal/cardpool/funds.go`
- Modify: `orders.go`（`queryResult.Detail`、`finalize` 写 `detail`、定论后标脏）、`reconcile.go`（`queryOrder`）、`events.go`（两类推送）
- Test: `backend/internal/cardpool/funds_test.go`

**Interfaces:**
- Consumes: Task 1 的类型与 `ErrInflight`；Task 2 的 `SyncAccounts`、`getAccount`、`markAccountsDirty`
- Produces:

```go
type ConversionParams struct {
	SellCurrency, BuyCurrency, By string
	SellCents                     int64
}
func (s *Service) SubmitConversion(ctx context.Context, p ConversionParams) (*Order, error)
type WalletTransferParams struct {
	PayerAccountNo, PayeeAccountNo, Currency, Remark, By string
	AmountCents                                         int64
}
func (s *Service) SubmitWalletTransfer(ctx context.Context, p WalletTransferParams) (*Order, error)
// queryResult 新增 Detail string；非空时 finalize 写入 orders.detail
```

- 校验顺序（spec §5.2）：参数本身（金额 > 0；币种去空白转大写、1–10 字；兑换两币种不同；转账备注 ≤ 200 字）→ `SyncAccounts`（失败返回其错误，不建单）→ 账户与余额。全部在建单前完成，失败一律 `ErrBadRequest`（同步失败除外）且不发请求。
- 兑换：卖出币种必须出现在 `MAIN` 账户的余额里；可用余额用 `ParseAmount` 解析，失败 → `ErrBadRequest("余额格式无法识别")`；不足 → `ErrBadRequest`。请求 `ConversionRequest{OutOrderNo, SellCurrency, SellAmount: FormatAmount(SellCents), BuyCurrency}`。200 → `sendVerdict{state: processing, txnID: transaction_id}`。
- 转账：付款方与收款方一个 `MAIN`、一个 `SUB_ACCOUNT`，不同账户，均 `gone = 0`；子账户 `ACTIVE`；付款方该币种可用余额足够。请求 `TransferRequest{…, Remark}`。200 → 按 `status` 映射（`PROCESSING` / `SUCCESS` / `FAIL`，其余按 unknown），`order_no` 写入 `detail` `{"order_no":…}`。
- 查单：兑换 `GetConversion(out)`；转账 `GetTransfer(TransferRef{OutOrderNo})`。`SUCCESS` → success；`FAILED` / `FAIL` → failed（原因取 `fail_reason`，空则「平台返回失败」）；`PROCESSING` → processing；其余无定论。兑换成交结果写入 `detail`：`{"buy_amount","buy_currency","rate","fee_amount","fee_currency","base_currency"}`（只放非空字段）。
- 推送：`CONVERSION_RESULT` → 找 `KindConversion` 单据；`TRANSFER_RESULT` → 找 `KindWalletTransfer`；状态映射与 `detail` 同查单；找不到 → 备注「非本系统单据」。
- 两种新单据进入终态（本次确实完成转换）后调用 `markAccountsDirty()`。

- [ ] **Step 1: 写失败测试**

```go
func TestConversionValidation(t *testing.T)         // SellCents 0；同币种；卖出币种不在主钱包余额；余额不足 → ErrBadRequest，POST /v1/conversion 0 次，无单据
func TestFundsRejectUnparsableBalance(t *testing.T) // 主钱包 USDT 可用余额为 "1,000.00" 或 "" → ErrBadRequest 且信息含「余额格式无法识别」，无单据
func TestFundsSyncFailureRejects(t *testing.T)      // 账户分页回 Unknown() → 返回错误，无单据，无下单请求
func TestConversionSendOutcomes(t *testing.T)       // OK(`{"transaction_id":"CV1"}`) → processing、platform_txn_id CV1，请求体 sell_amount "12.50"；Rejected → failed；Unknown → unknown；NotSentClient → not_sent
func TestWalletTransferValidation(t *testing.T)     // 两个 MAIN；两个 SUB；同一账户；收款方不在本地；子账户 gone；子账户 DISABLED；余额不足；备注 201 字 → ErrBadRequest，POST /v1/transfer 0 次
func TestWalletTransferSendOutcomes(t *testing.T)   // status PROCESSING/SUCCESS/FAIL/"WEIRD" → processing/success/failed/unknown；detail 含 order_no
func TestWalletInflightUnique(t *testing.T)         // 一笔 unknown 兑换后再兑换 → ErrInflight，POST 只 1 次；此时钱包转账仍可提交
func TestWalletOrdersReconcile(t *testing.T)        // 兑换查单 SUCCESS → success 且 detail 含 buy_amount、rate；转账查单 FAIL+fail_reason → failed、last_error 为该原因；查单出错保持；unknown 满 30 分钟 → needs_review
func TestWalletOrdersNeverResent(t *testing.T)      // unknown 兑换单 50 轮对账 → POST /v1/conversion 只 1 次
func TestConversionWebhookBeforeResponse(t *testing.T) // POST /v1/conversion Hang 300ms 后回 200；期间处理 CONVERSION_RESULT SUCCESS → 最终 success
func TestWalletWebhooks(t *testing.T)               // CONVERSION_RESULT FAILED → failed；TRANSFER_RESULT FAIL → failed；未知单号 → 备注「非本系统单据」；终态后账户标脏
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/cardpool/ -run 'Conversion|Funds|WalletTransfer|WalletInflight|WalletOrders|WalletWebhooks' -count=1`
Expected: 编译失败

- [ ] **Step 3: 实现 `funds.go` 并扩展 `orders.go`、`reconcile.go`、`events.go`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/cardpool/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/cardpool
git commit -m "feat(cardpool): 兑换单与钱包转账单，复用单据引擎对账"
```

---

### Task 4: 只读查询：收款地址、账户流水、卡转入转出明细

**Files:**
- Modify: `backend/internal/cardpool/wallet.go`、`admin.go`
- Test: `backend/internal/cardpool/wallet_test.go`、`admin_test.go`

**Interfaces:**
- Consumes: Task 1 的 `getOrder`（子项目 2）、Task 2
- Produces:

```go
func (s *Service) ReceivingAddresses(ctx context.Context) ([]marswallex.ReceivingAddress, error) // nil → 空切片
type SerialsQuery struct {
	AccountNo, Currency string
	Start, End          int64
	Page, PageSize      int
}
func (s *Service) AccountSerials(ctx context.Context, q SerialsQuery) ([]marswallex.AccountSerial, int, error)
type CardTransferRow struct {
	marswallex.CardBalanceTransaction
	Origin     string `json:"origin"`      // "local" / "external"
	LocalState string `json:"local_state"` // 本地单据状态；external 为空
	Mismatch   bool   `json:"mismatch"`
}
func (s *Service) CardTransfers(ctx context.Context, cardID string) ([]CardTransferRow, error)
```

- `AccountSerials`：账户号、币种必填；`End > Start`；`End − Start ≤ 90×86400`；`Page` 默认 1；`PageSize` 默认 20、上限 100。违反 → `ErrBadRequest`。返回 `Count` 转 int。
- `CardTransfers`：卡必须在 `mw_cards`（否则 `ErrNotFound`）；窗口 `[max(card.created_at, now−90天), now]`；分页 50 取全（空页或累计 ≥ count 停止，最多 200 页）。按 `out_order_no` 找本地单据：找到为 `local`；平台 `SUCCESS` 且本地为 `failed` / `not_sent` / `pending` / `processing` / `unknown` / `needs_review` 时 `Mismatch = true`。按 `create_time` 倒序。

- [ ] **Step 1: 写失败测试**

```go
func TestReceivingAddresses(t *testing.T)        // 平台返回 2 条 → 原样；返回 null → 空切片
func TestAccountSerialsValidation(t *testing.T)  // 缺账户、缺币种、End<=Start、跨度 90 天+1 秒 → ErrBadRequest；PageSize 500 → 请求里 page_size=100；查询串含 start_time、end_time 秒值
func TestCardTransfersMarks(t *testing.T)        // 本地 success 单 → local；无本地单 → external；平台 SUCCESS + 本地 failed → mismatch；+ 本地 unknown → mismatch；卡不在池 → ErrNotFound；卡 200 天前入池 → start_time = now−90 天
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/cardpool/ -run 'ReceivingAddresses|AccountSerials|CardTransfers' -count=1`
Expected: 编译失败

- [ ] **Step 3: 实现**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/cardpool/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/cardpool
git commit -m "feat(cardpool): 收款地址、账户流水与卡转入转出明细比对"
```

---

### Task 5: 持卡人新建、修改、启停与证件照

**Files:**
- Create: `backend/internal/cardpool/holders.go`
- Modify: `admin.go`（`ImportCardholder`）、`events.go`（`onCardholderStatusChange`）
- Test: `backend/internal/cardpool/holders_test.go`、`events_test.go`

**Interfaces:**
- Consumes: Task 1 的 `Submission`、`getSubmission`、`newOutID`、`Cardholder.OutCardHolderID`
- Produces:

```go
const maxPhotoBytes = 7 << 20
func (s *Service) UploadIDPhoto(ctx context.Context, filename string, content []byte) (string, error)
func validateProfile(p *marswallex.CardholderProfile, today time.Time) error // 会规范化：去空白、国家码转大写、非 US 清空 SSN
func (s *Service) CreateCardholder(ctx context.Context, p marswallex.CardholderProfile, by string) (*Submission, error)
func (s *Service) CardholderProfile(ctx context.Context, id string) (*marswallex.CardholderDetail, error)
func (s *Service) UpdateCardholder(ctx context.Context, id string, p marswallex.CardholderProfile, by string) (*Cardholder, error)
func (s *Service) SetCardholderStatus(ctx context.Context, id, status, by string) (*Cardholder, error)
func (s *Service) ListSubmissions(ctx context.Context) ([]Submission, error) // 不含 linked，按 created_at 倒序
func (s *Service) AbandonSubmission(ctx context.Context, outID, by string) error
```

- `UploadIDPhoto`：空或 > `maxPhotoBytes` → `ErrBadRequest`；`http.DetectContentType` 结果须为 `image/jpeg`、`image/png`、`application/pdf` 之一；`filename` 只取 `filepath.Base`。调 `UploadFile(ctx, "CARDHOLDER_ID_PHOTO", name, content)`，返回 `file_id`。
- `validateProfile`（`today` 取北京日期）规则逐条：英文名 / 姓 1–32 个可打印 ASCII；中文名 / 姓 ≤ 32 rune；`date_of_birth` 为 `2006-01-02` 且早于今天；`zone_number` 1–10 位数字；`mobile` 1–50；`email` 能被 `net/mail.ParseAddress` 解析且 ≤ 100；`country_code` 与两个地址的 `country` 为两位大写字母；`id_type` ∈ {PASSPORT, IDCARD, OTHER_CERTIFICATE}；`id_no` 1–64；`id_period_begin` ≤ 今天且 ≤ `id_period_end`，`id_period_end` > 今天；`id_photo_file_ids` 1–2 个且非空；地址 `state` / `city` 1–64、`address` 1–256、`post_code` 1–10；国家为 `US` 时 SSN 必须匹配 `^\d{3}-\d{2}-\d{4}$`。违反 → `ErrBadRequest`，信息只含字段名，不含字段值。
- `CreateCardholder`：校验 → 写 `submitted` 提交记录（只含姓名、国家）→ 调 `CreateCardholder`。
  - 200 → 插入 `mw_cardholders`（姓名、国家、`PENDING_REVIEW`、`out_card_holder_id`），提交记录 `linked` 并写 `card_holder_id`；返回提交记录。
  - `OutcomeRejected` / `OutcomeNotSent` → 提交记录 `abandoned`、`last_error` 截断 200 字；返回该错误。
  - 其他错误 → 提交记录 `unknown`；返回提交记录、`nil` 错误。
  - 不改策略的 `HolderIDs`。
- `CardholderProfile`：持卡人须已在本地（否则 `ErrNotFound`），实时 `GetCardholder`。
- `UpdateCardholder`：本地存在 → 校验 → `UpdateCardholder` → `RefreshCardholder`；返回本地持卡人。
- `SetCardholderStatus`：目标只能 `APPROVED` / `DISABLED` → 调接口 → `RefreshCardholder`。
- `ImportCardholder`（改）：写入平台返回的 `out_card_holder_id`；若匹配 `submitted` / `unknown` 提交记录，标 `linked` 并写 `card_holder_id`。
- `onCardholderStatusChange`（改）：先读旧状态。本地没有该持卡人时，`out_card_holder_id` 匹配 `submitted` / `unknown` 提交记录 → `ImportCardholder` 后备注「已关联提交记录」，否则保持「持卡人未导入」。告警按 spec §7.5：

| 新状态 | 行为（key） |
|---|---|
| `APPROVED` 且旧状态不是 `APPROVED` | 通知「MarsWallex 持卡人 X 已通过审核，可在策略中加入补卡持卡人」（`holderok:<id>`） |
| `REJECTED` | 告警，附驳回原因（`holder:<id>`） |
| `PENDING_REVIEW` | 只有旧状态为 `APPROVED` 时告警（`holder:<id>`） |
| `SUSPENDED`、`DISABLED` | 照旧告警（`holder:<id>`） |

- [ ] **Step 1: 写失败测试**

```go
func TestValidateProfile(t *testing.T)           // 表驱动：每条规则一个反例 → ErrBadRequest 且 err.Error() 不含字段值；合法 US 资料通过；非 US 带 SSN → 通过且 SSN 被清空；"us" → "US"
func TestUploadIDPhotoSniffsContent(t *testing.T) // "<html>…" 命名 a.jpg → ErrBadRequest 且无上传请求；7MB+1 字节 → ErrBadRequest；PNG 头 → POST /v1/file/upload?biz_type=CARDHOLDER_ID_PHOTO，返回 file_id
func TestCreateCardholderSuccess(t *testing.T)   // 请求体 out_card_holder_id 以 CH 开头、长 22；mw_cardholders 新行 status PENDING_REVIEW 且有 out_card_holder_id；提交记录 linked；策略 HolderIDs 不变
func TestCreateCardholderRejected(t *testing.T)  // Rejected(400,"PARAM") → 返回错误；提交记录 abandoned、last_error 非空
func TestCreateCardholderUnknownThenWebhookLinks(t *testing.T) // Unknown() → 提交记录 unknown、err nil；CARDHOLDER_STATUS_CHANGE 带该 out ID → GET /v1/cardholder/{id} 后导入，提交记录 linked
func TestSubmittedRecordLinksByWebhook(t *testing.T) // 直接插一条 submitted 记录（模拟发送途中崩溃）→ 推送同样能关联
func TestImportLinksSubmission(t *testing.T)     // 导入时平台返回的 out_card_holder_id 匹配 unknown 记录 → linked
func TestAbandonSubmission(t *testing.T)         // unknown → abandoned；linked → ErrBadRequest；不存在 → ErrNotFound
func TestUpdateCardholder(t *testing.T)          // 未导入 → ErrNotFound；PUT 体为全量资料；之后 GET 刷新，本地 status 随平台
func TestSetCardholderStatus(t *testing.T)       // 目标 "REJECTED" → ErrBadRequest；DISABLED → PUT /v1/cardholder/status/{id} 体 {"status":"DISABLED"}，本地刷新
func TestHolderStatusAlerts(t *testing.T)        // §7.5 表逐行：首次 PENDING_REVIEW 不告警；APPROVED 后变 PENDING_REVIEW 告警；未通过 → 通过发通知；REJECTED 告警含原因；SUSPENDED 告警
func TestHolderFlowsKeepPIIOutOfLogs(t *testing.T) // log.SetOutput 到缓冲：成功、被拒、未知三种新建与一次修改 → 缓冲里不出现证件号、SSN、出生日期、手机号
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/cardpool/ -run 'Profile|IDPhoto|Cardholder|Submission|HolderStatus|HolderFlows' -count=1`
Expected: 编译失败

- [ ] **Step 3: 实现 `holders.go`，修改 `admin.go`、`events.go`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/cardpool/ -count=1 -race`
Expected: PASS（子项目 2 原有的持卡人告警测试若断言「任何非 APPROVED 都告警」，按 §7.5 表更新断言）

- [ ] **Step 5: 提交**

```bash
git add backend/internal/cardpool
git commit -m "feat(cardpool): 持卡人新建、修改、启停与证件照转发，敏感资料不落地"
```

---

### Task 6: 交易补拉

**Files:**
- Create: `backend/internal/cardpool/backfill.go`
- Modify: `events.go`（抽出规则）、`run.go`（新循环）、`service.go`（`backfillMu`）
- Test: `backend/internal/cardpool/backfill_test.go`

**Interfaces:**
- Consumes: 子项目 2 的 `upsertTxn`、`Transaction`、`runLoop`
- Produces:

```go
const backfillStateKey = "mw_txn_backfill_state"
type BackfillState struct {
	Cursor    *int64 `json:"cursor"`
	LastRunAt *int64 `json:"last_run_at"`
	LastAdded int    `json:"last_added"`
	LastError string `json:"last_error"`
}
func (s *Service) BackfillStatus(ctx context.Context) (BackfillState, error)
func (s *Service) BackfillRecent(ctx context.Context, days int) (BackfillState, error) // days 只能 1 / 3 / 7
func (s *Service) backfillOnce(ctx context.Context) error
func (s *Service) disableIfUnusedAuth(ctx context.Context, t Transaction, afterCreatedOnly bool) (bool, error)
```

- `disableIfUnusedAuth`：从 `onAuthEvent` 原样抽出（同一 UPDATE 条件、同一告警文本与 key `authunused:<card_id>`）；`afterCreatedOnly` 为真时 UPDATE 额外要求 `created_at < t.OccurredAt`（`OccurredAt` 为空则不处理）。`onAuthEvent` 以 `false` 调用，行为不变。
- 窗口算法（`backfillOnce`）：
  - `start = cursor − 2h`（无 cursor 时 `now − 7d`），`end = min(now, start + 7d)`；
  - 按 24h 切段 `[a, b)`；每段先授权后结算，每个接口分页 50，停在「`data` 为空」「累计 ≥ `count`」或 200 页；
  - 每条交易：先查是否已存在（计入 `LastAdded` 只算新插入），丢弃 `OTP` 字段后 `upsertTxn`；授权交易再调 `disableIfUnusedAuth(t, true)`；
  - 一段成功 → `cursor = b`（只增不减）并保存状态；失败 → 停止本轮、`LastError` 记错误（截断 200 字）；全部成功 → `LastError = ""`；
  - 每轮结束写 `LastRunAt = now`。
- `BackfillRecent`：窗口 `[now − days×24h, now]`，同样分段；只有 `start ≤ cursor`（或无 cursor）时成功段才推进 `cursor`。
- `backfillMu`：`backfillOnce` 与 `BackfillRecent` 互斥串行。
- `Start` 改为 4 个协程：新增 `go s.runLoop(ctx, backfillInterval, nil, s.backfillRound)`，`backfillRound` 取不到客户端时 `lg.readiness` 后跳过。更新 `Start` 的注释。

- [ ] **Step 1: 写失败测试**

```go
func TestBackfillFirstRunCovers7Days(t *testing.T)      // 无 cursor → 授权、结算各 7 段，首段 start_time = now−7d；cursor = now；last_error 空
func TestBackfillOverlapAndDedupe(t *testing.T)         // cursor = now−1h → start_time = cursor−2h；同一交易两轮都返回 → 只 1 行，第二轮 last_added = 0
func TestBackfillFailureKeepsCursor(t *testing.T)       // 第 2 段授权回 Unknown() → cursor = 第 1 段终点，last_error 非空；下一轮从原 cursor−2h 继续
func TestBackfillCatchUpCapped(t *testing.T)            // cursor = now−10d → 本轮 end = start+7d；再一轮后 cursor = now
func TestBackfillManualCursorRule(t *testing.T)         // cursor = now−2d：手动 1 天 → cursor 不变；手动 3 天 → cursor = now；days=2 → ErrBadRequest
func TestBackfillDropsOTP(t *testing.T)                 // 授权交易带 otp "123456" → mw_3ds_otps 为空，交易表有该交易
func TestBackfillUnusedAuthRule(t *testing.T)           // available、use_count 0、created_at 早于交易的卡 → disabled + 告警；交易早于 created_at → 不停用
func TestBackfillStopsOnShortOrEmptyPage(t *testing.T)  // count "500" 但第 2 页 data 为空 → 该段在第 2 页停止；一直满页 → 第 200 页后停止
func TestBackfillConcurrentRunsSerialize(t *testing.T)  // 授权查询 Hang 100ms；backfillOnce 与 BackfillRecent(1) 并发 → 都返回 nil，期间 cursor 读数单调不减
func TestBackfillSkipsWhenClientMissing(t *testing.T)   // Client 返回错误 → backfillRound 不改状态
func TestAuthWebhookRuleUnchanged(t *testing.T)         // 推送路径：created_at 晚于交易的未交卡号卡仍被停用（afterCreatedOnly=false）
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/cardpool/ -run 'Backfill|AuthWebhookRule' -count=1`
Expected: 编译失败

- [ ] **Step 3: 实现 `backfill.go`，修改 `events.go`、`run.go`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/cardpool/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/cardpool
git commit -m "feat(cardpool): 授权与结算交易定时补拉，补拉同样执行未交卡号停卡规则"
```

---

### Task 7: 后台接口与路由

**Files:**
- Create: `backend/internal/handler/cardpool_wallet.go`、`cardpool_holders.go`
- Modify: `backend/internal/handler/cardpool.go`（`poolErrorBody` 把 `ErrInflight` 映射为 409）、`backend/internal/server/http.go`
- Test: `backend/internal/handler/cardpool_wallet_test.go`、`cardpool_holders_test.go`

**Interfaces:**
- Consumes: Task 2–6 的全部 `Service` 方法；现有 `poolRoute`、`poolFail`、`poolErrorBody`、`poolAudit`、`bindPoolJSON`、`poolList`、`pageParams`
- Produces（路由均在 `/api/v1/admin` 下，处理器名 → 路径）：

| 处理器 | 路由 | 请求 → 响应 |
|---|---|---|
| `AdminCardPoolAccounts` | `GET /cardpool/wallet/accounts` | `refresh=1` 先 `SyncAccounts`（失败则 `poolFail`）→ `{items}` |
| `AdminCardPoolCreateAccount` | `POST /cardpool/wallet/accounts` | `{account_name}` → 账户 |
| `AdminCardPoolAccountStatus` | `POST /cardpool/wallet/accounts/:no/status` | `{status, remark}` → 账户 |
| `AdminCardPoolReceivingAddresses` | `GET /cardpool/wallet/receiving-addresses` | → `{items}` |
| `AdminCardPoolSerials` | `GET /cardpool/wallet/serials` | `account_no, currency, start_time, end_time, page, page_size` → `{items, total}` |
| `AdminCardPoolConversion` | `POST /cardpool/wallet/conversions` | `{sell_currency, sell_amount, buy_currency}` → 单据 |
| `AdminCardPoolWalletTransfer` | `POST /cardpool/wallet/transfers` | `{payer_account_no, payee_account_no, currency, amount, remark}` → 单据 |
| `AdminCardPoolUploadPhoto` | `POST /cardpool/cardholders/photos` | multipart `file` → `{file_id}` |
| `AdminCardPoolCreateCardholder` | `POST /cardpool/cardholders` | 资料 JSON → 200 `{submission}`；结果未知 202 `{submission, unknown: true}` |
| `AdminCardPoolCardholderProfile` | `GET /cardpool/cardholders/:id/profile` | → 平台完整资料 |
| `AdminCardPoolUpdateCardholder` | `PUT /cardpool/cardholders/:id` | 资料 JSON → 持卡人 |
| `AdminCardPoolCardholderStatus` | `POST /cardpool/cardholders/:id/status` | `{status}` → 持卡人 |
| `AdminCardPoolSubmissions` | `GET /cardpool/cardholder-submissions` | → `{items}` |
| `AdminCardPoolAbandonSubmission` | `POST /cardpool/cardholder-submissions/:out_id/abandon` | → `{ok: true}` |
| `AdminCardPoolBackfillStatus` | `GET /cardpool/backfill` | → `BackfillState` |
| `AdminCardPoolBackfill` | `POST /cardpool/backfill` | `{days}` → `BackfillState` |
| `AdminCardPoolCardTransfers` | `GET /cardpool/cards/:id/balance-transactions` | → `{items}` |

- 金额字段为字符串，按 `^\d{1,12}(\.\d{1,2})?$` 校验后用 `marswallex.ParseAmount` 转分，非法 → 400（同 `AdminCardPoolFundCard`）。
- 上传：`c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 7<<20+64<<10)`；超限 → 413 `{"error":"文件超过 7MB"}`；缺 `file` → 400。
- 审计 action（`poolAudit` 前缀 `cardpool.`）与内容，只含 ID、金额、币种、账户、状态：`wallet_create_account account_no= name=`、`wallet_account_status account_no= status= remark=`、`wallet_conversion out_order_no= sell= buy=`、`wallet_transfer out_order_no= payer= payee= amount= currency=`、`holder_create out_card_holder_id= card_holder_id=`、`holder_update card_holder_id=`、`holder_status card_holder_id= status=`、`holder_abandon out_card_holder_id=`、`backfill days=`。
- 持卡人接口不记录请求体与响应体，出错时 `poolFail` 不得把资料拼进日志。

- [ ] **Step 1: 写失败测试**

```go
// cardpool_wallet_test.go
func TestWalletRoutesRequireService(t *testing.T)    // SetCardPool(nil) 时每个新处理器回 503
func TestConversionParsesAmountAndAudits(t *testing.T) // "1.005"、"0"、"abc"、"" → 400 且无单据；"12.50" → SellCents 1250，审计含 cardpool.wallet_conversion
func TestInflightMapsTo409(t *testing.T)             // 第二笔兑换 → 409，error 含「在途」
func TestAccountsRefreshFailure(t *testing.T)        // refresh=1 且分页 Unknown() → 502 outcome "unknown"
func TestSerialsParams(t *testing.T)                 // 缺 currency → 400；正常 → {items,total}
func TestBackfillEndpoint(t *testing.T)              // days 2 → 400；days 1 → 200 且返回状态；审计 cardpool.backfill days=1
// cardpool_holders_test.go
func TestUploadPhotoTooLarge(t *testing.T)           // 8MB 请求体 → 413，平台无上传请求
func TestCreateCardholderUnknownReturns202(t *testing.T)
func TestHolderEndpointsNoPIIInAuditOrLogs(t *testing.T) // 新建、修改、查资料后：audit_logs 全部内容与 log 缓冲里都不含证件号与 SSN
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/handler/ -run 'Wallet|Conversion|Inflight|AccountsRefresh|Serials|BackfillEndpoint|UploadPhoto|CreateCardholder|HolderEndpoints' -count=1`
Expected: 编译失败

- [ ] **Step 3: 实现处理器、错误映射与路由**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/handler/ ./internal/server/ -count=1 -race`
Expected: PASS（排除 Global Constraints 中两个已知失败）

- [ ] **Step 5: 提交**

```bash
git add backend/internal/handler backend/internal/server
git commit -m "feat(handler): 钱包、持卡人、补拉与卡明细后台接口"
```

---

### Task 8: 前端公共库与「MarsWallex 钱包」页

**Files:**
- Modify: `frontend/src/lib/api.ts`、`frontend/src/router/index.ts`、`frontend/src/layouts/AdminLayout.vue`
- Create: `frontend/src/lib/cardpool.ts`、`frontend/src/lib/__tests__/cardpool.test.ts`、`frontend/src/views/admin/MarsWallexWallet.vue`

**Interfaces:**
- Consumes: Task 7 的接口
- Produces（`src/lib/cardpool.ts`）：

```ts
export interface Resp<T = any> { ok: boolean; status: number; data: T }
export async function poolApi<T = any>(path: string, method?: string, body?: unknown): Promise<Resp<T>> // 前缀 /api/v1/admin/cardpool；FormData 原样传，其余 JSON.stringify
export function errText(r: Resp, fallback: string): string
export function isUnknownOutcome(r: Resp): boolean          // r.status === 502 && r.data?.outcome === 'unknown'
export function money(cents?: number | null): string        // 从 CardPool.vue 移入
export function fmtTs(ts?: number | null): string           // 同上
export const ORDER_KINDS: { value: string; label: string }[] // 原四种 + conversion「兑换」、wallet_transfer「钱包转账」
export function kindLabel(k: string): string
export function parseAmountCents(s: string): number | null  // 去空白后匹配 /^\d{1,12}(\.\d{1,2})?$/ 且 > 0 → 分；否则 null
export function hasEmoji(s: string): boolean                // /\p{Extended_Pictographic}/u
```

- `api.ts`：`init.body instanceof FormData` 时不设置 `Content-Type`。
- 路由 `{ path: 'marswallex-wallet', name: 'MarsWallexWallet', … }`；导航在「卡池」后加 `{ path: '/ops/marswallex-wallet', label: 'MarsWallex 钱包', icon: 'Coin' }`。
- 页面五个标签（spec §9）：
  - **账户**：进入页面调 `GET wallet/accounts?refresh=1`；表格列账户名、账户号、类型、状态、各币种可用 / 冻结、`gone` 标灰。新建子账户对话框；返回 `isUnknownOutcome` 时提示「结果未知，10 秒后自动刷新确认」，并 `setTimeout` 10 秒刷新。子账户行「启用 / 停用」：原因 2–100 字且 `!hasEmoji`，停用确认框含 spec §6.3 提示语。
  - **充值地址**：顶部 `el-alert type="warning"`：「只能向对应的链转入对应的币种，转错链会丢失资金」；每条地址带复制按钮（沿用 `lib/clipboard`）。
  - **兑换**：表单（卖出币种从主钱包余额币种中选，默认 `USDT`；金额；买入币种默认 `USD`）；提交前 `dialog` 确认框列出全部参数及「汇率与手续费以平台成交为准」；下方 `GET orders?kind=conversion` 列表，展示 `detail` 中的买入金额、汇率、手续费，`needs_review` 行有「重查」「确认失败」（沿用卡池接口）。
  - **转账**：方向单选（主钱包 → 子账户 / 子账户 → 主钱包）、子账户（`ACTIVE` 且未 `gone`）、币种（付款方余额币种）、金额、备注 ≤ 200；确认框；`kind=wallet_transfer` 列表。
  - **流水**：账户、币种、时间段（默认最近 7 天，选择器禁止超过 90 天），分页表格；方向与类型用中文标签（spec §6.5）。

- [ ] **Step 1: 写失败测试**

```ts
// src/lib/__tests__/cardpool.test.ts
it('parseAmountCents', () => { /* "12.5"→1250, "0.01"→1, " 3 "→300, "0"→null, "1.005"→null, "-1"→null, "abc"→null, "1234567890123"→null */ })
it('kindLabel covers wallet kinds', () => { /* conversion→兑换, wallet_transfer→钱包转账, apply→开卡, x→x */ })
it('isUnknownOutcome', () => { /* {status:502,data:{outcome:'unknown'}}→true; 502 rejected→false; 200→false */ })
it('hasEmoji', () => { /* "停用😀"→true, "停用原因"→false */ })
```

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend && npx vitest run src/lib/__tests__/cardpool.test.ts`
Expected: FAIL（模块不存在）

- [ ] **Step 3: 实现 `cardpool.ts`、`api.ts` 改动、钱包页、路由与导航**

- [ ] **Step 4: 运行确认通过**

Run: `cd frontend && npx vitest run && npm run build`
Expected: 测试全部 PASS，构建成功

- [ ] **Step 5: 提交**

```bash
git add frontend/src
git commit -m "feat(frontend): MarsWallex 钱包页：账户、充值地址、兑换、转账、流水"
```

---

### Task 9: 卡池页：持卡人表单、补拉、卡明细与单据类型

**Files:**
- Create: `frontend/src/views/admin/components/CardholderForm.vue`
- Modify: `frontend/src/views/admin/CardPool.vue`、`frontend/src/lib/cardpool.ts`
- Test: `frontend/src/lib/__tests__/cardpool.test.ts`

**Interfaces:**
- Consumes: Task 8 的 `cardpool.ts`；Task 7 的持卡人、补拉、卡明细接口
- Produces:

```ts
// cardpool.ts 新增
export interface CardholderFormValue { /* 与 marswallex.CardholderProfile 字段同名（snake_case），外加 billing_same: boolean */ }
export function emptyCardholderForm(): CardholderFormValue
export function validateCardholderForm(f: CardholderFormValue, today: string /* YYYY-MM-DD */): string[] // 返回中文错误列表；规则与 Task 5 validateProfile 一致
export function toCardholderPayload(f: CardholderFormValue): Record<string, unknown> // billing_same 时复制居住地址；非 US 去掉 ssn；国家码转大写
```

- `CardholderForm.vue`：props `mode: 'create' | 'edit'`、`initial?: CardholderFormValue`；`emit('submitted', payload)`。证件照用 `el-upload` 的 `http-request` 调 `poolApi('/cardholders/photos', 'POST', formData)`，限制 `accept=".jpg,.jpeg,.png,.pdf"`、单张 7MB、最多 2 张；编辑模式展示已有文件 ID，可删除后重传。国家选 `US` 时才显示 SSN。提交前跑 `validateCardholderForm`，有错误就列出来，不发请求。
- `CardPool.vue`：
  - 改用 `cardpool.ts` 的公共函数，删除本地重复定义；单据类型筛选用新的 `ORDER_KINDS`。
  - **持卡人标签**：「新建持卡人」→ 表单 → `POST /cardholders`。202 时提示「结果未知；审核推送到达或手动导入后会自动关联」。行操作「修改」（先 `GET /cardholders/:id/profile` 预填）、「停用」（`APPROVED` 时）、「启用」（`DISABLED` 时）。列表上方是提交记录区（`GET /cardholder-submissions`），可「放弃」。新建成功后提示「审核通过后需在『策略』中手动加入补卡持卡人」。
  - **交易标签**：显示上次补拉时间、新增条数、错误；「立即补拉」下拉选 1 / 3 / 7 天。
  - **卡详情**：新增「平台转入转出明细」区块，列 `origin`（本系统 / 外部）、类型、金额、状态、时间；`mismatch` 行标红，并提示「与本地单据不一致，请到『单据』中重查」。

- [ ] **Step 1: 写失败测试**

```ts
it('validateCardholderForm', () => { /* 合法 US 表单 → []；缺 SSN（US）、名含中文、出生日期为今天、证件截止日为今天、起始日晚于今天、0 张照片、3 张照片、国家码 "U" → 各自返回非空 */ })
it('toCardholderPayload', () => { /* billing_same → billing_address 等于 residential_address；country "cn" → "CN" 且无 ssn 键；不含 billing_same 键 */ })
```

- [ ] **Step 2: 运行确认失败**

Run: `cd frontend && npx vitest run src/lib/__tests__/cardpool.test.ts`
Expected: FAIL

- [ ] **Step 3: 实现表单组件与卡池页改动**

- [ ] **Step 4: 运行确认通过**

Run: `cd frontend && npx vitest run && npm run build`
Expected: PASS，构建成功

- [ ] **Step 5: 提交**

```bash
git add frontend/src
git commit -m "feat(frontend): 卡池页持卡人新建修改启停、交易补拉与卡转入转出明细"
```

---

### Task 10: 全量验证

**Files:**
- Modify: `docs/superpowers/specs/2026-10-29-marswallex-wallet-design.md`（状态行改为「已实现，待生产验收」）

- [ ] **Step 1: 后端**

Run: `cd backend && go vet ./... && go test ./... -count=1 -race 2>&1 | tail -40`
Expected: 除 `TestIssueProductKeepsGPTDefault`、`TestXIssueInfersProductWithoutQueryParameter` 外全部 PASS

- [ ] **Step 2: 前端**

Run: `cd frontend && npx vitest run && npm run build`
Expected: 全部 PASS，构建成功

- [ ] **Step 3: 本地浏览器冒烟（不连真实平台）**

起本地后端与前端，管理员登录后打开 `/ops/marswallex-wallet` 和 `/ops/cardpool`：五个钱包标签、持卡人新建表单（含照片选择与校验报错）、交易补拉状态、卡详情明细区块都能渲染。未配置凭证时，各页面显示接口错误提示，不白屏、控制台无未捕获异常。

- [ ] **Step 4: 提交**

```bash
git add docs/superpowers/specs/2026-10-29-marswallex-wallet-design.md
git commit -m "docs(spec): 子项目 2b 已实现，待生产验收"
```
