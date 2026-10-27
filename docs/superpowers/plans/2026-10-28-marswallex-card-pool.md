# MarsWallex 卡池与资金 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增 MarsWallex 卡池：持卡人导入、开卡与导入卡、资金单与对账、租约与回收、自动补卡、Webhook 业务处理与 3DS 验证码、选卡与健康度、后台管理页，并为子项目 4 提供 `Acquire / Fund / Sensitive / WaitOTP / WaitOrder / Release`。

**Architecture:** 新包 `internal/cardpool` 持有全部业务逻辑与查询语句，表和迁移放在 `internal/db/cardpool.go`。所有写操作先写本地单据 `mw_card_orders` 再调接口，结果不明的单据和 Webhook 事件由 `Service.Start` 启动的后台协程收敛。`internal/handler/cardpool.go` 把服务接到 Gin，前端新增 `/ops/cardpool` 页。

**Tech Stack:** Go 1.24（module `github.com/tuzi/cdk-recharge-system`）、Gin、SQLite 3.44（`mattn/go-sqlite3`，支持部分索引）、子项目 1 的 `internal/marswallex`、`internal/secretbox`；前端 Vue 3 + Element Plus + Vite。

**Spec:** `docs/superpowers/specs/2026-10-28-marswallex-card-pool-design.md`；接口字段以 `docs/marswallex-openapi-zh.md` 为准；子项目 1 的结果分类见 `docs/superpowers/specs/2026-10-27-marswallex-access-layer-design.md` §5.3。

## Global Constraints

- ZovoCard 相关代码不改：`internal/cardplatform`、`handler/card_*.go`、`cardplatform*.go` 及其表。
- `internal/marswallex` 仍只依赖标准库；新增的 `internal/marswallex/mwtest` 只被测试 import。
- 不引入新的第三方依赖。
- 资金金额用 int64 分，调用接口时用 `marswallex.FormatAmount` / `ParseAmount` 转换。交易表里的金额只用于展示，按平台原字符串保存。
- 新表的时间列一律 `INTEGER` Unix 秒（UTC），`NULL` 表示未设置。所有时间都取 `Service.now()`（可注入）。按日统计用北京时间：`time.FixedZone("UTC+8", 8*3600)`，不依赖 tzdata。
- 完整卡号、CVV2、3DS 验证码不写日志、不进审计内容、不出现在 `POST /cards/:id/sensitive` 和 `POST /cards/:id/otp` 以外的任何响应里。
- **资金单（开卡、转入、转出）任何情况下都不自动重发**；只有改状态单允许用同一张单据重发。
- 告警一律走 `Options.Alert`（生产环境是 `notify.SendText`）；带 key 的告警在同一北京自然日只发一次。
- 单号：两位前缀（`MA` 开卡 / `MI` 转入 / `MO` 转出 / `MS` 改状态）+ UTC `060102150405` + 8 位小写十六进制，共 22 位。
- 固定参数：
  - 退避间隔 10s / 30s / 60s / 120s / 300s，之后每次 300s；
  - `unknown` 满 30 分钟转 `needs_review`；`processing` 满 24 小时告警；`pending` 超过 2 分钟转 `unknown`；
  - 对账协程每 5 秒一轮，补卡每 60 秒一次，Webhook 兜底扫描每 10 秒一次，事件连续失败 10 次后停止重试；
  - 回收中遇到冻结余额或某一步失败，都在 10 分钟后再试；验证码保留 30 分钟；手动开卡一次最多 20 张。
- 已知原有失败的测试 `TestIssueProductKeepsGPTDefault`、`TestXIssueInfersProductWithoutQueryParameter` 不修复，验证时排除并说明。
- 提交用仓库已配置的 zovocard 身份，提交信息末尾加 `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`。

## Review Focus

1. **进程在发出资金请求途中崩溃**：单据停在 `pending`，平台可能已经执行了。期望 2 分钟后转 `unknown` 并开始查单，绝不能当成「未发出」。→ Task 4 `TestStalePendingBecomesUnknown`。
2. **Webhook 先于接口响应到达**：转入请求还在路上，`CARD_TRANSFER_RESULT` 已经推送 `SUCCESS`。期望单据保持 `success`，随后返回的 `APPLY` 不会把它改回 `processing`。→ Task 4 `TestWebhookBeforeSendResult`。
3. **取卡号的响应丢了**：`Sensitive` 调接口超时，但平台可能已经把卡号返回给了调用方。期望这张卡照样记为「本次租约已取卡号」。→ Task 7 `TestSensitiveMarksBeforeCall`。
4. **补卡时卡产品已下线或参数被拒**：开卡单被拒。期望这一轮停止对该产品继续开卡，失败计入当天上限，告警每天只发一次，不会每分钟刷屏。→ Task 8 `TestReplenishFailedCountsAndStops`。
5. **导入卡的有效期格式不规范**（如 `13/26`）：期望这张卡不会被选中，也不会让选卡报错。→ Task 7 `TestAcquireSkipsSuspendedExcludedExpiringAndBadHolder`。

---

## File Structure

| 文件 | 职责 |
|---|---|
| `backend/internal/marswallex/webhook.go` | 抽出 `DecodeWebhookPayload`（不验签，解析已存档原文） |
| `backend/internal/marswallex/mwtest/mwtest.go` | 可编排的模拟 MarsWallex 服务、Webhook 签名 |
| `backend/internal/db/cardpool.go` | 卡池表迁移、Webhook 表加列、`Migrate()` |
| `backend/internal/db/dbtest/dbtest.go` | 测试用内存库 |
| `backend/internal/db/marswallex.go` | Webhook 事件结构与列表增加处理字段 |
| `backend/internal/cardpool/service.go` | `Service`、`Options`、时钟、告警去重、错误 |
| `backend/internal/cardpool/model.go` | 常量、`Card` / `Order` / `Cardholder` / `Transaction` 结构与读取 |
| `backend/internal/cardpool/policy.go` | 策略结构、默认值、校验、读写 |
| `backend/internal/cardpool/orders.go` `reconcile.go` | 单据提交、初判、定论、对账、人工处理、等待 |
| `backend/internal/cardpool/admin.go` | 持卡人、导入卡、刷新、手动开卡、停用启用、冻结注销、清零、列表、概览 |
| `backend/internal/cardpool/health.go` | 结果记录、产品与单卡健康度、恢复、报表 |
| `backend/internal/cardpool/lease.go` `recycle.go` | 选卡与租约、注资、取卡号、释放、到期回收、回收推进 |
| `backend/internal/cardpool/replenish.go` | 自动补卡 |
| `backend/internal/cardpool/events.go` `otp.go` `run.go` | Webhook 事件处理、验证码、后台协程 |
| `backend/internal/handler/cardpool.go` | 后台接口 |
| `backend/internal/handler/marswallex.go` `marswallex_webhook.go` | 导出客户端加载函数；入库后通知；重新处理 |
| `backend/internal/server/http.go` | 路由、创建并启动服务 |
| `frontend/src/views/admin/CardPool.vue` | 卡池页 |
| `frontend/src/views/admin/MarsWallexIntegration.vue` | Webhook 列表加处理状态与重新处理 |
| `frontend/src/router/index.ts` `layouts/AdminLayout.vue` `element.ts` | 路由、导航、图标 |

---

### Task 1: 模拟服务 mwtest 与存档 Webhook 解析

**Files:**
- Create: `backend/internal/marswallex/mwtest/mwtest.go`
- Test: `backend/internal/marswallex/mwtest/mwtest_test.go`
- Modify: `backend/internal/marswallex/webhook.go`（把 `ParseWebhook` 中验签之后的解析部分抽成 `DecodeWebhookPayload`，`ParseWebhook` 改为调用它）
- Test: `backend/internal/marswallex/webhook_test.go`（追加）

**Interfaces:**
- Consumes: `marswallex.NewClient`、`Config`、`Sign`、`Verify`、`SigningPayload`、`FormatSignatureHeader`、`ParseSignatureHeader`、`PublicKeyPEM`
- Produces:
  - `func DecodeWebhookPayload(raw []byte) (*WebhookEvent, error)`：不验签；错误包装 `ErrWebhookPayload`；`message_id` / `messageId` / sha256 缺省 ID 与字符串形式的 `data` 的处理与原来一致
  - 包 `mwtest`：

```go
type Reply struct {
	Status   int           // 0 视为 200
	Body     string        // 原样作为响应体；空视为 `{"data":{}}`
	Unsigned bool          // 不加 F-Signature
	Hang     time.Duration // 先等待再回复
	Drop     bool          // 劫持连接后直接关闭
}
type Request struct{ Method, Path, RawQuery string; Body []byte }
func OK(data string) Reply                    // 200，Body = `{"data":` + data + `}`
func Rejected(status int, code string) Reply  // 已签名，Body = `{"code":"<code>","message":"rejected"}`
func Unknown() Reply                          // 已签名的 500
type Server struct{ /* ... */ }
func New(t testing.TB) *Server                // 两对 RSA 密钥按包缓存；httptest 服务在 t.Cleanup 时关闭
func (s *Server) Client() *marswallex.Client  // BaseURL = URL + "/portal/api"，HTTPClient.Timeout = 2s
func (s *Server) NotSentClient() *marswallex.Client // 指向已关闭的本地端口，调用结果为「未发出」
func (s *Server) On(method, path string, replies ...Reply) // path 不含 /portal/api；队列依次消费，只剩最后一个时重复使用
func (s *Server) Requests(method, path string) []Request
func (s *Server) SignWebhook(body []byte, at time.Time) string // 返回 F-Signature 头（秒级 epoch）
func (s *Server) PlatformPublicKeyPEM() string
```

  - 服务端照子项目 1 `mock_test.go` 的方式校验请求的 Authorization 与签名，校验失败或请求未编排时 `t.Errorf`。未编排的请求回复未签名的 404。并发安全（加互斥锁）。

- [ ] **Step 1: 写失败测试**

```go
// webhook_test.go
func TestDecodeWebhookPayloadMatchesParse(t *testing.T) {
	// 三种原文：message_id + 对象 data；messageId + 字符串 data；无 ID → sha256
	// DecodeWebhookPayload 与 ParseWebhook(合法签名) 得到相同的 Topic、MessageID、Data
	// 非法 JSON → errors.Is(err, ErrWebhookPayload)
}
// mwtest_test.go
func TestOKReplyIsSuccess(t *testing.T)        // On(GET,/v1/card/detail/c1, OK(`{"card_id":"c1","card_no":"****1234","card_name":"x","status":"ACTIVE"}`)) → GetCard 成功，Status=="ACTIVE"，Requests 1 条
func TestRejectedReplyIsRejected(t *testing.T) // Rejected(404,"ORDER_NOT_FOUND") → OutcomeOf == OutcomeRejected
func TestUnknownAndUnsignedAreUnknown(t *testing.T) // Unknown()、Reply{Unsigned:true} → OutcomeUnknown
func TestDropIsUnknown(t *testing.T)           // Reply{Drop:true} → OutcomeUnknown
func TestNotSentClient(t *testing.T)           // NotSentClient().GetCard → OutcomeNotSent
func TestQueueRepeatsLast(t *testing.T)        // On(..., OK(a), OK(b)) → 三次调用依次得到 a, b, b
func TestSignWebhookVerifies(t *testing.T)     // ParseWebhook(SignWebhook(body,now), body, ParsePublicKey(PlatformPublicKeyPEM()), now) 成功
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/marswallex/...`
Expected: 编译失败（`DecodeWebhookPayload`、`mwtest` 未定义）

- [ ] **Step 3: 实现 `DecodeWebhookPayload` 与 `mwtest`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/marswallex/... -count=1`
Expected: PASS（原有测试全部仍通过）

- [ ] **Step 5: 提交**

```bash
git add backend/internal/marswallex
git commit -m "test(marswallex): 可编排模拟服务 mwtest，抽出存档 Webhook 解析"
```

---

### Task 2: 卡池表与迁移

**Files:**
- Create: `backend/internal/db/cardpool.go`
- Create: `backend/internal/db/dbtest/dbtest.go`
- Test: `backend/internal/db/cardpool_test.go`
- Modify: `backend/internal/db/db.go`（`createTables` 中 `MigrateMarsWallex()` 之后调用 `MigrateCardPool()`）
- Modify: `backend/internal/db/marswallex.go`（事件结构加字段；列表查询带出新列）

**Interfaces:**
- Produces:
  - `func Migrate() error`：导出的 `createTables()`
  - `func MigrateCardPool() error`：可重复执行；执行下列 DDL。`marswallex_webhook_events` 用 `pragma_table_info` 判断后再 `ALTER TABLE ADD COLUMN`：`process_attempts INTEGER NOT NULL DEFAULT 0`、`next_attempt_at INTEGER`、`process_note TEXT NOT NULL DEFAULT ''`

```sql
CREATE TABLE IF NOT EXISTS mw_cardholders (
	card_holder_id TEXT PRIMARY KEY,
	first_name TEXT NOT NULL DEFAULT '', last_name TEXT NOT NULL DEFAULT '',
	country_code TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT '',
	remark TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '',
	synced_at INTEGER, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS mw_cards (
	card_id TEXT PRIMARY KEY,
	card_holder_id TEXT NOT NULL DEFAULT '', product_id TEXT NOT NULL,
	card_label TEXT NOT NULL DEFAULT '', card_country TEXT NOT NULL DEFAULT '',
	currency TEXT NOT NULL DEFAULT '', last4 TEXT NOT NULL DEFAULT '', expiry TEXT NOT NULL DEFAULT '',
	platform_status TEXT NOT NULL DEFAULT '',
	available_cents INTEGER NOT NULL DEFAULT 0, frozen_cents INTEGER NOT NULL DEFAULT 0, balance_synced_at INTEGER,
	source TEXT NOT NULL, pool_state TEXT NOT NULL,
	lease_token TEXT NOT NULL DEFAULT '', lease_owner TEXT NOT NULL DEFAULT '',
	lease_expires_at INTEGER, lease_sensitive INTEGER NOT NULL DEFAULT 0,
	use_count INTEGER NOT NULL DEFAULT 0, last_sensitive_at INTEGER,
	retire_reason TEXT NOT NULL DEFAULT '', disabled_reason TEXT NOT NULL DEFAULT '',
	frozen_at INTEGER, cancel_after INTEGER, next_action_at INTEGER,
	created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mw_cards_pick ON mw_cards(product_id, pool_state);
CREATE TABLE IF NOT EXISTS mw_card_orders (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	out_order_no TEXT NOT NULL UNIQUE, kind TEXT NOT NULL,
	card_id TEXT NOT NULL DEFAULT '', card_holder_id TEXT NOT NULL DEFAULT '', product_id TEXT NOT NULL DEFAULT '',
	amount_cents INTEGER NOT NULL DEFAULT 0, currency TEXT NOT NULL DEFAULT '', target_status TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL, platform_txn_id TEXT NOT NULL DEFAULT '',
	ever_unknown INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0,
	next_check_at INTEGER, first_sent_at INTEGER, last_error TEXT NOT NULL DEFAULT '',
	requested_by TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '', alerted_stale INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, finished_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_mw_orders_due ON mw_card_orders(state, next_check_at);
CREATE INDEX IF NOT EXISTS idx_mw_orders_card ON mw_card_orders(card_id, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS uq_mw_orders_card_inflight ON mw_card_orders(card_id)
	WHERE kind IN ('recharge','withdraw') AND state IN ('pending','processing','unknown','needs_review');
CREATE TABLE IF NOT EXISTS mw_card_transactions (
	transaction_id TEXT PRIMARY KEY, kind TEXT NOT NULL, card_id TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT '', decline_reason TEXT NOT NULL DEFAULT '',
	txn_amount TEXT NOT NULL DEFAULT '', txn_currency TEXT NOT NULL DEFAULT '',
	billing_amount TEXT NOT NULL DEFAULT '', billing_currency TEXT NOT NULL DEFAULT '',
	merchant_name TEXT NOT NULL DEFAULT '', origin_transaction_id TEXT NOT NULL DEFAULT '',
	auth_transaction_id TEXT NOT NULL DEFAULT '', occurred_at INTEGER, updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mw_txn_card ON mw_card_transactions(card_id, occurred_at);
CREATE TABLE IF NOT EXISTS mw_3ds_otps (
	id INTEGER PRIMARY KEY AUTOINCREMENT, card_id TEXT NOT NULL,
	otp_sealed TEXT NOT NULL DEFAULT '', merchant_name TEXT NOT NULL DEFAULT '',
	amount TEXT NOT NULL DEFAULT '', currency TEXT NOT NULL DEFAULT '',
	received_at INTEGER NOT NULL, consumed_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_mw_otp_card ON mw_3ds_otps(card_id, received_at);
CREATE TABLE IF NOT EXISTS mw_card_outcomes (
	id INTEGER PRIMARY KEY AUTOINCREMENT, card_id TEXT NOT NULL, product_id TEXT NOT NULL,
	plan_key TEXT NOT NULL, order_ref TEXT NOT NULL DEFAULT '', email_norm TEXT NOT NULL,
	result TEXT NOT NULL, error_code TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mw_outcome_prod ON mw_card_outcomes(product_id, plan_key, created_at);
CREATE INDEX IF NOT EXISTS idx_mw_outcome_card ON mw_card_outcomes(card_id, created_at);
CREATE TABLE IF NOT EXISTS mw_product_health (
	product_id TEXT NOT NULL, plan_key TEXT NOT NULL,
	suspended_at INTEGER, reason TEXT NOT NULL DEFAULT '',
	resumed_at INTEGER, resumed_by TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (product_id, plan_key)
);
```

  - `MarsWallexWebhookEvent` 增加 `ProcessAttempts int`、`NextAttemptAt *int64`、`ProcessNote string`；`ListMarsWallexWebhookEvents` 带出这三列（仍然不查 payload）
  - 包 `dbtest`：`func Open(t testing.TB)`：内存库、`SetMaxOpenConns(1)`、替换 `db.DB`、`t.Cleanup` 时还原、调用 `db.Migrate()`；`func SetMasterKey(t *testing.T)`：用固定的 32 字节 base64 设置 `APP_MASTER_KEY`

- [ ] **Step 1: 写失败测试**

```go
func TestMigrateCardPoolIdempotent(t *testing.T)       // dbtest.Open；再调两次 MigrateCardPool 都返回 nil；sqlite_master 里有 7 张新表
func TestWebhookEventColumnsAdded(t *testing.T)        // 先只建子项目 1 的旧表并插一行 → MigrateCardPool → 该行 process_attempts=0、process_note=''
func TestInflightTransferUniquePerCard(t *testing.T) {
	// c1 有一条 processing 的 recharge → 再插 c1 的 pending withdraw 报约束错误
	// c2 可以插；c1 的那条改为 success 后，c1 可以再插；c1 的 status 单不受限制
	// c1 的 needs_review 转入单同样占位
}
func TestListWebhookEventsIncludesProcessFields(t *testing.T)
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/db/ -run 'CardPool|WebhookEventColumns|Inflight|ProcessFields'`
Expected: 编译失败

- [ ] **Step 3: 实现迁移、`Migrate`、`dbtest`，扩展事件结构**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/db/... -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/db
git commit -m "feat(db): 卡池表、单据在途唯一约束与 Webhook 处理字段"
```

---

### Task 3: cardpool 骨架：服务、模型、策略、单号

**Files:**
- Create: `backend/internal/cardpool/service.go`、`model.go`、`policy.go`、`orderno.go`
- Create: `backend/internal/cardpool/helpers_test.go`（测试工具，后续任务共用）
- Test: `backend/internal/cardpool/policy_test.go`、`orderno_test.go`、`service_test.go`

**Interfaces:**
- Consumes: Task 1 `mwtest`；Task 2 `dbtest.Open`、表
- Produces:

```go
type Options struct {
	Client func() (*marswallex.Client, error) // 必填；生产环境为 handler.MarsWallexClient
	Now    func() time.Time                   // nil → time.Now
	Alert  func(text string)                  // nil → log.Printf
}
func New(opts Options) *Service
// Service 的内部字段由 New 一次建好，后续任务直接使用：
//   applyMu sync.Mutex（手动开卡与自动补卡串行）、rr int（持卡人轮转计数，受 applyMu 保护）、
//   kickEvents、kickReplenishCh chan struct{}（容量 1）、
//   eventHandler func(context.Context, *marswallex.WebhookEvent) (string, error)（为 nil 时 Task 9 用 handleEvent）
func (s *Service) now() time.Time
func (s *Service) alert(key, text string) // key 非空时，同一 key 在同一北京自然日只发一次
func beijingDayStart(t time.Time) time.Time
var (ErrNotFound, ErrNoCard, ErrLeaseInvalid, ErrCardBusy, ErrBadRequest error)
func badRequest(msg string) error // errors.Is(err, ErrBadRequest)，Error() 为 msg
// 常量
PoolAvailable="available" PoolLeased="leased" PoolReleasing="releasing" PoolUsed="used" PoolRetired="retired" PoolDisabled="disabled"
KindApply="apply" KindRecharge="recharge" KindWithdraw="withdraw" KindStatus="status"
StatePending="pending" StateProcessing="processing" StateUnknown="unknown" StateNeedsReview="needs_review"
StateSuccess="success" StateFailed="failed" StateNotSent="not_sent"
func IsTerminal(state string) bool // success / failed / not_sent
// 模型：字段与 Task 2 的列一一对应，时间列用 *int64 或 int64
type Cardholder struct{ ... }
type Card struct{ ... }
type Order struct{ ... }
type Transaction struct{ ... }
func getCard(ctx context.Context, cardID string) (*Card, error)      // 无记录 → ErrNotFound
func getOrder(ctx context.Context, outOrderNo string) (*Order, error) // 无记录 → ErrNotFound
func getCardholder(ctx context.Context, id string) (*Cardholder, error)
// 策略
const PolicySettingKey = "mw_cardpool_policy"
type ProductTarget struct{ Target int `json:"target"`; DailyLimit int `json:"daily_limit"` }
type Policy struct {
	PlanProducts         map[string][]string      `json:"plan_products"` // 键 "default" 为兜底
	ProductTargets       map[string]ProductTarget `json:"product_targets"`
	HolderIDs            []string                 `json:"holder_ids"`
	OpenAmountCents      int64                    `json:"open_amount_cents"`
	ValidityMonths       int                      `json:"validity_months"`
	CardNamePrefix       string                   `json:"card_name_prefix"`
	MaxUsesPerCard       int                      `json:"max_uses_per_card"`
	LeaseMinutes         int                      `json:"lease_minutes"`
	CancelAfterDays      int                      `json:"cancel_after_days"`
	HealthWindowHours    int                      `json:"health_window_hours"`
	ProductFailThreshold int                      `json:"product_fail_threshold"`
	CardFailThreshold    int                      `json:"card_fail_threshold"`
	AutoSuspend          bool                     `json:"auto_suspend"`
}
func DefaultPolicy() Policy // 0 / 24 / "POOL" / 1 / 30 / 7 / 24 / 3 / 2 / true；map 与切片为空但非 nil
func (p Policy) Validate() error // 不合法 → badRequest，消息写明哪一项
func LoadPolicy() (Policy, error) // 未设置 → DefaultPolicy；已设置 → 先取默认值再 Unmarshal（缺的字段沿用默认）
func SavePolicy(p Policy) error  // 先 Validate，失败则不写入
func newOutOrderNo(kind string, now time.Time) string
```

  - 校验范围：`OpenAmountCents ≥ 0`；`ValidityMonths` 2–47；`CardNamePrefix` 匹配 `^[A-Za-z0-9]{1,10}$`；`MaxUsesPerCard` 1–100；`LeaseMinutes` 5–240；`CancelAfterDays` 0–90；`HealthWindowHours` 1–720；两个阈值 1–100；每个产品的 `Target`、`DailyLimit` 都在 0–500。
  - `helpers_test.go` 提供：`newTestService(t) (*Service, *mwtest.Server, *testClock, *alertLog)`，其中调用 `dbtest.Open` 和 `dbtest.SetMasterKey`，`testClock` 默认 `2026-10-28T04:00:00Z`、有 `Advance(d)`，`alertLog` 记录告警文本；还有 `insertCard(t, Card)`、`insertOrder(t, Order)`、`savePolicy(t, func(*Policy))`。

- [ ] **Step 1: 写失败测试**

```go
func TestDefaultPolicyValid(t *testing.T)
func TestPolicyValidateRejects(t *testing.T) // 表驱动：前缀 "TOO-LONG-PREFIX"、有效期 1 和 48、使用次数 0、开卡金额 -1、注销天数 -1、目标数 501 → 都返回 ErrBadRequest
func TestLoadPolicyFillsDefaults(t *testing.T) // 存 `{"max_uses_per_card":3}` → MaxUsesPerCard==3、LeaseMinutes==30、AutoSuspend==true
func TestSavePolicyRejectsInvalidAndKeepsOld(t *testing.T)
func TestOutOrderNoFormat(t *testing.T) // newOutOrderNo(KindRecharge, now) 匹配 ^MI\d{12}[0-9a-f]{8}$、长度 22；1000 次互不重复；四种前缀分别为 MA/MI/MO/MS
func TestAlertDedupePerBeijingDay(t *testing.T) // 时钟 15:59Z（北京 23:59）alert("k") 两次 → 1 条；前进 2 分钟 → 2 条；key 为空每次都发
func TestGetCardNotFound(t *testing.T)          // errors.Is(err, ErrNotFound)
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/cardpool/`
Expected: 编译失败

- [ ] **Step 3: 实现 `service.go`、`model.go`、`policy.go`、`orderno.go`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/cardpool/ -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/cardpool
git commit -m "feat(cardpool): 服务骨架、模型、策略与单号"
```

---

### Task 4: 单据提交、定论与对账

**Files:**
- Create: `backend/internal/cardpool/orders.go`、`reconcile.go`
- Test: `backend/internal/cardpool/orders_test.go`、`reconcile_test.go`

**Interfaces:**
- Consumes: Task 3 全部
- Produces:

```go
type applyParams struct {
	HolderID, ProductID, Currency, NamePrefix, Expiry, By string
	AmountCents int64
}
func (s *Service) submitApply(ctx context.Context, p applyParams) (*Order, error)
func (s *Service) submitTransfer(ctx context.Context, kind, cardID string, amountCents int64, currency, by string) (*Order, error)
func (s *Service) submitStatus(ctx context.Context, cardID, target, by string) (*Order, error)
type queryResult struct{ State, CardID, TxnID, Reason string } // State: processing / success / failed；空表示没有定论
func (s *Service) finalize(ctx context.Context, outOrderNo string, r queryResult, source string) (*Order, error)
func (s *Service) reconcileOrdersOnce(ctx context.Context) error
func (s *Service) Recheck(ctx context.Context, outOrderNo string) (*Order, error)
func (s *Service) ConfirmFailed(ctx context.Context, outOrderNo, note, by string) (*Order, error)
func (s *Service) WaitOrder(ctx context.Context, outOrderNo string) (*Order, error)
func expiryMMyy(now time.Time, months int) string // now.UTC().AddDate(0, months, 0).Format("0106")
func backoff(attempt int) time.Duration           // 0→10s,1→30s,2→60s,3→120s,≥4→300s
```

- **发送流程**：
  1. `INSERT` 写入 `pending` 单据。转入、转出撞上 `uq_mw_orders_card_inflight` 时返回 `ErrCardBusy`，不发请求。
  2. 开卡单的卡名 = `NamePrefix + fmt.Sprintf("%06d", id%1000000)`，`id` 取刚插入的行。
  3. 调用接口，按 spec §6.2 的表初判。所有发送结果的状态更新都带条件 `WHERE state = 'pending'`：Webhook 已经先定论时，丢弃发送结果。如果发送结果是终态、且与已有终态冲突，告警（key `conflict:<单号>`）。
  4. 除 `not_sent` 外，都写入 `first_sent_at = now`、`attempts = 1`；`unknown` 和 `processing` 的 `next_check_at = now + backoff(0)`。
  5. 转入、转出的成功响应状态不是 `APPLY` / `SUCCESS` / `FAIL` 时，按 `unknown` 处理。
- **金额与币种**：`amountCents ≤ 0` 的转入、转出返回 `ErrBadRequest`；币种由调用方传卡币种。
- **`finalize`**：
  - 终态不改写；`processing` 不会覆盖终态。
  - 开卡成功但 `CardID` 为空时，按 `processing` 处理。
  - 副作用：
    - 开卡成功：`INSERT OR IGNORE` 一张卡（`source = opened`、`pool_state = releasing`、`next_action_at = now`，产品、持卡人、币种取自单据）；
    - 转入、转出到达终态：卡的 `next_action_at = now`；
    - 改状态成功：更新 `platform_status`。目标是 `CANCELED` 时 → `retired`；目标是 `REVOKED`、卡为 `used` 且 `frozen_at` 为空时，写入 `frozen_at = now`、`cancel_after = now + CancelAfterDays × 86400`。
- **`reconcileOrdersOnce`**：
  1. `pending` 且 `created_at ≤ now − 120` → `unknown`，`ever_unknown = 1`，`first_sent_at = COALESCE(first_sent_at, created_at)`，`next_check_at = now`。
  2. 到期的 `unknown` 和 `processing` 单据（每轮最多 50 条）逐条查单：
     - 开卡单查 `GetCardApplyOrder(OrderRef{OutOrderNo})`；
     - 转入、转出查 `GetCardTransferOrder(OrderRef{OutOrderNo})`；
     - 改状态单查 `GetCard`：状态已是目标就算成功，否则在单据为 `unknown` 时用同一单据重发 `UpdateCardStatus`，并按 §6.2 初判。
     - 查单出错或查不到，都算没有定论。
     - 每查一次 `attempts++`，`next_check_at = now + backoff(attempts-1)`。
  3. `unknown` 且 `now − first_sent_at ≥ 1800` → `needs_review`，告警（key `review:<单号>`）。
  4. `processing` 且 `now − first_sent_at ≥ 86400`、`alerted_stale = 0` → 告警，`alerted_stale = 1`。
- **人工处理**：
  - `Recheck` 只接受 `unknown` / `processing` / `needs_review`，立即查一次，有定论就 `finalize`。
  - `ConfirmFailed` 只接受 `needs_review`，备注不能为空，否则返回 `ErrBadRequest`；成功后改为 `failed`，写入备注，卡的 `next_action_at = now`。
- **`WaitOrder`**：每 500ms 查一次，直到状态为 `success` / `failed` / `not_sent` / `needs_review`，或 ctx 结束（返回 `ctx.Err()`）。

- [ ] **Step 1: 写失败测试**

```go
// orders_test.go
func TestApplyOutcomes(t *testing.T) // OK(`{"out_order_no":"x","transaction_id":"T1"}`) → processing、platform_txn_id=="T1"；Rejected(400,"PARAM") → failed；Unknown() → unknown、ever_unknown=1、next_check_at=now+10；NotSentClient → not_sent、first_sent_at 为空
func TestApplyRequestBody(t *testing.T) // 第一张单：card_name=="POOL000001"；now=2026-10-28、24 个月 → expiry_date=="1028"；amount=="0.00"；不传 fee_deduct_type
func TestTransferStatusMapping(t *testing.T) // 响应 status 为 APPLY/SUCCESS/FAIL/"WEIRD" → processing/success/failed/unknown
func TestTransferBusyCard(t *testing.T)     // c1 已有 processing 转入 → submitTransfer(withdraw,c1) 返回 ErrCardBusy，mwtest 没收到转出请求
func TestTransferRejectsNonPositiveAmount(t *testing.T)
func TestFinalizeNeverOverwritesTerminal(t *testing.T) // 先 success 后 failed → 仍为 success，1 条告警
func TestWebhookBeforeSendResult(t *testing.T) // 转入回复 Hang 300ms 再回 APPLY；另一协程等单据出现后 finalize(success,"webhook") → 最终仍为 success
func TestApplySuccessCreatesReleasingCard(t *testing.T) // finalize(开卡单, success, CardID "c9") → mw_cards 有 c9：releasing、opened，产品与持卡人来自单据
func TestApplySuccessWithoutCardIDStaysProcessing(t *testing.T)
func TestStatusSuccessSideEffects(t *testing.T) // CANCELED → retired；used 卡 REVOKED → frozen_at=now、cancel_after=now+7d
// reconcile_test.go
func TestStalePendingBecomesUnknown(t *testing.T) // 3 分钟前创建的 pending 转入 → 一轮后为 unknown、ever_unknown=1，并已查单一次
func TestReconcileBackoffSchedule(t *testing.T)   // 查单一直回 Rejected(404,"NOT_FOUND")：每次到期查一次，next_check_at 依次 +10s、+30s、+60s、+120s、+300s、+300s
func TestUnknownToNeedsReviewAfter30Min(t *testing.T) // 满 1800s → needs_review、1 条告警；之后的轮次不再查它
func TestQueryErrorAlsoEscalates(t *testing.T)  // 查单一直回 Unknown() → 同样在 30 分钟后变 needs_review
func TestProcessingAlertAfter24h(t *testing.T)  // 查单一直回 APPLY：24h 时告警 1 次、仍为 processing；再过 1h 不再告警
func TestFundsOrdersNeverResent(t *testing.T)   // unknown 的转入单经过 50 轮对账 → POST /v1/card/recharge-transaction 只收到 1 次
func TestStatusOrderResendOnMismatch(t *testing.T) // 目标 REVOKED：GetCard 回 ACTIVE → 重发 PUT（共 2 次）；下一轮 GetCard 回 REVOKED → success
func TestRejectedAfterUnknownStaysUnknown(t *testing.T) // 改状态单重发时被拒，且 ever_unknown=1 → 仍为 unknown
func TestRecheckAndConfirmFailed(t *testing.T)  // needs_review + 查单 SUCCESS → Recheck 后为 success；ConfirmFailed 备注为空 → ErrBadRequest；填了备注 → failed 且记录备注；对 processing 单 ConfirmFailed → ErrBadRequest
func TestWaitOrder(t *testing.T)                // 另一协程 200ms 后 finalize → 返回 success；needs_review 立即返回；ctx 超时返回 ctx.Err()
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/cardpool/ -run 'Apply|Transfer|Finalize|Webhook|Status|Stale|Reconcile|Unknown|Query|Processing|Funds|Rejected|Recheck|WaitOrder'`
Expected: 编译失败

- [ ] **Step 3: 实现 `orders.go`、`reconcile.go`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/cardpool/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/cardpool
git commit -m "feat(cardpool): 资金单提交、定论与对账，未知单 30 分钟转人工"
```

---

### Task 5: 持卡人与卡的后台操作、列表与概览

**Files:**
- Create: `backend/internal/cardpool/admin.go`
- Test: `backend/internal/cardpool/admin_test.go`

**Interfaces:**
- Consumes: Task 3、Task 4（`submitApply`、`submitTransfer`、`submitStatus`、`expiryMMyy`）
- Produces:

```go
func (s *Service) ImportCardholder(ctx context.Context, id, by string) (*Cardholder, error) // GetCardholder → 写入；已存在则更新
func (s *Service) RefreshCardholder(ctx context.Context, id string) (*Cardholder, error)
func (s *Service) ListCardholders(ctx context.Context) ([]Cardholder, error)
func (s *Service) ListProducts(ctx context.Context) ([]marswallex.CardProduct, error)
func (s *Service) ImportCard(ctx context.Context, cardID, productID, holderID, by string) (*Card, error)
func (s *Service) RefreshCard(ctx context.Context, cardID string) (*Card, error)
func (s *Service) ApplyCards(ctx context.Context, productID, holderID string, count int, by string) ([]Order, error)
func (s *Service) DisableCard(ctx context.Context, cardID, reason, by string) error
func (s *Service) EnableCard(ctx context.Context, cardID, by string) error
func (s *Service) FreezeCard(ctx context.Context, cardID, by string) (*Order, error)   // 目标 REVOKED
func (s *Service) UnfreezeCard(ctx context.Context, cardID, by string) (*Order, error) // 目标 ACTIVE
func (s *Service) CancelCard(ctx context.Context, cardID, by string) (*Order, error)   // 目标 CANCELED
func (s *Service) SweepCard(ctx context.Context, cardID, by string) (*Order, error)
type CardFilter struct{ State, ProductID string; Page, PageSize int }        // PageSize 默认 20、上限 100；按 created_at 倒序
type OrderFilter struct{ State, Kind, CardID string; Page, PageSize int }
type TxnFilter struct{ CardID string; Page, PageSize int }
func (s *Service) ListCards(ctx context.Context, f CardFilter) ([]Card, int, error)
func (s *Service) ListOrders(ctx context.Context, f OrderFilter) ([]Order, int, error)
func (s *Service) ListTransactions(ctx context.Context, f TxnFilter) ([]Transaction, int, error)
type CardDetailView struct{ Card Card; Orders []Order; Transactions []Transaction } // 各取最近 50 条
func (s *Service) CardDetail(ctx context.Context, cardID string) (*CardDetailView, error)
type ProductStock struct{ ProductID string; Available, Leased, Releasing, Used, Disabled, Target, DailyLimit, OpenedToday int }
type Overview struct {
	MainBalances []marswallex.BalanceItem; BalanceError string
	Products []ProductStock
	NeedsReview, FailedEvents, SuspendedProducts int
}
func (s *Service) Overview(ctx context.Context) (*Overview, error)
```

- **规则**：
  - `ImportCard`：用 `GetCard` + `ListCardProducts` 校验。产品必须存在，卡的卡组、国家、币种、模式与产品一致（不区分大小写），否则返回 `badRequest("卡与产品不一致")`；持卡人不为空时必须已导入；卡已在池中、或平台状态是 `CANCELED` → `ErrBadRequest`。写入时 `source = imported`、`pool_state = releasing`、`next_action_at = now`。
  - `RefreshCard`：更新卡组、国家、币种、有效期、平台状态、余额和 `balance_synced_at`。`last4` 取 `card_no` 中数字的最后 4 位，少于 4 位则留空。
  - `ApplyCards`：数量须在 1–20，持卡人须已导入且为 `APPROVED`，产品须存在，币种取产品币种。开卡金额、有效期、卡名前缀取策略。用 `s.applyMu` 与补卡串行；开卡单计入当天数量，但手动开卡不受每日上限限制。
  - `DisableCard`：只接受 `available`，`leased` 返回 `ErrCardBusy`，其他状态返回 `ErrBadRequest`；写入 `disabled_reason`。
  - `EnableCard`：只接受 `disabled`。先 `RefreshCard`，要求可用余额和冻结余额都为 0、`use_count` 小于上限、平台状态 `ACTIVE`，不满足就返回 `ErrBadRequest` 并说明原因。
  - 冻结、解冻、注销、清零：`leased` 返回 `ErrCardBusy`；`retired` 返回 `ErrBadRequest`。
  - `SweepCard`：先 `RefreshCard`，可用余额为 0 时返回 `badRequest("无可用余额")`，否则提交一笔等于可用余额的转出单。
  - `Overview`：
    - 主钱包余额用 `ListAccounts(ListAccountsQuery{PageNo:1, PageSize:20, AccountType:"MAIN"})`，取第一个账户的 `BalanceList`；出错时写入 `BalanceError`，不让整个概览失败。
    - `OpenedToday` 是该产品自北京时间零点起的开卡单数。
    - `FailedEvents` 是未处理且 `process_attempts ≥ 10` 的事件数；`SuspendedProducts` 是当前停用的「产品 × 套餐」数。
  - 所有写操作返回的错误都不含卡号。

- [ ] **Step 1: 写失败测试**

```go
func TestImportCardholder(t *testing.T)            // 平台回 APPROVED → 字段已写入；再导入时平台回 REJECTED，remark 也更新
func TestImportCardValidatesProduct(t *testing.T)  // 币种不一致 → ErrBadRequest 且没有写入；一致时 → releasing、imported、last4 为 "4111********1234" 的最后 4 位 "1234"
func TestImportCardRejectsDuplicateAndCanceled(t *testing.T)
func TestApplyCardsValidation(t *testing.T)        // 数量 0 和 21、持卡人非 APPROVED、产品不存在 → ErrBadRequest，且没有发请求
func TestApplyCardsCreatesOrders(t *testing.T)     // 数量 3 → 3 条开卡单、3 次 POST /v2/card、卡名互不相同
func TestDisableEnableRules(t *testing.T)          // leased → ErrCardBusy；available → disabled；余额 1.00 时启用 → ErrBadRequest；余额为 0 → available
func TestSweepCard(t *testing.T)                   // 可用余额 5.00 → 一条 500 分的转出单；余额 0 → ErrBadRequest
func TestCancelCardUsesStatusOrder(t *testing.T)   // 目标为 CANCELED 的改状态单；finalize 成功后卡为 retired
func TestOverviewCounts(t *testing.T)              // 各状态卡数、今日开卡数（北京时间切日）、needs_review 数；账户接口出错时 BalanceError 非空
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/cardpool/ -run 'Import|ApplyCards|DisableEnable|Sweep|CancelCard|Overview'`
Expected: 编译失败

- [ ] **Step 3: 实现 `admin.go`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/cardpool/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/cardpool
git commit -m "feat(cardpool): 持卡人与卡的后台操作、列表与概览"
```

---

### Task 6: 健康度

**Files:**
- Create: `backend/internal/cardpool/health.go`
- Test: `backend/internal/cardpool/health_test.go`

**Interfaces:**
- Consumes: Task 3
- Produces:

```go
const (ResultSuccess = "success"; ResultFailed = "failed"; ResultAborted = "aborted")
type Outcome struct{ Result, ErrorCode string }
func normalizeEmail(email string) string // 小写、去空格，空字符串记为 "unknown"
func evaluateVerdict(failCount, distinctKnownEmails, threshold int) string
	// failCount < threshold → "need_more"；distinct ≥ 2 → "suspect"；distinct == 1 → "email_suspect"；0 → "unknown_emails"
func (s *Service) recordOutcome(ctx context.Context, c *Card, planKey, orderRef, email string, o Outcome) error
func (s *Service) productSuspended(ctx context.Context, productID, planKey string) (bool, error)
func (s *Service) ResumeProduct(ctx context.Context, productID, planKey, by string) error
type ProductHealth struct {
	ProductID, PlanKey string
	Success24h, Failed24h, Success7d, Failed7d int
	Suspended bool; SuspendedAt *int64; Reason string
}
func (s *Service) HealthReport(ctx context.Context) ([]ProductHealth, error)
```

- **`recordOutcome`**：
  - `aborted` 不写入，直接返回。其他结果写入 `mw_card_outcomes` 后评估：
    - **产品 × 套餐**：起算点 = max(`now − HealthWindowHours`，该范围最近一次成功的时间，`resumed_at`)，统计起算点之后的失败次数和不同已知邮箱数（不含 `unknown`），阈值用 `ProductFailThreshold`。
    - **单卡**：只在 `MaxUsesPerCard > 1` 时评估。起算点 = max(窗口起点，这张卡最近一次成功的时间)，阈值用 `CardFailThreshold`。
  - 判定为 `suspect` 时：
    - `AutoSuspend` 为真：
      - 产品：写入 `mw_product_health`（`suspended_at = now`，`resumed_at = NULL`，原因写明窗口、失败次数、邮箱数），并告警；已经停用的不重复处理。
      - 单卡：写入 `retire_reason = "健康度"`。卡为 `available` 或 `releasing` 时直接转为 `used`，`next_action_at = now`；`leased` 的卡保持不动，等释放时处理。然后告警。
    - `AutoSuspend` 为假：只告警（key `health:<范围>`）。
- **`productSuspended`**：`suspended_at` 不为空，且（`resumed_at` 为空，或 `resumed_at < suspended_at`）。
- **`ResumeProduct`**：写入 `resumed_at = now`、`resumed_by`。不处于停用状态时返回 `ErrBadRequest`。

- [ ] **Step 1: 写失败测试**

```go
func TestEvaluateVerdict(t *testing.T)                 // 表驱动覆盖 4 种结果
func TestProductSuspendsAfterThresholdDistinctEmails(t *testing.T) // 3 次失败，邮箱 a、B@x（规范化后 b@x）、a → 已停用、1 条告警
func TestSameEmailDoesNotSuspend(t *testing.T)         // 3 次失败都是同一邮箱 → 未停用
func TestSuccessResetsCount(t *testing.T)              // 失败、失败、成功、失败 → 未停用
func TestWindowExcludesOld(t *testing.T)               // 25 小时前 2 次失败 + 现在 1 次 → 未停用
func TestResumeResetsCounting(t *testing.T)            // 停用 → 恢复 → 再失败 1 次 → 未停用；对未停用的产品调用恢复 → ErrBadRequest
func TestPerPlanIsolation(t *testing.T)                // gpt 套餐的失败不影响 x 套餐
func TestCardHealthOnlyWhenMaxUsesAboveOne(t *testing.T) // 上限 1：同一张卡 2 次失败、不同邮箱 → 卡不变；上限 3：retire_reason=="健康度"，available 转为 used
func TestCardHealthLeasedCardWaitsForRelease(t *testing.T) // leased 的卡只写 retire_reason，状态仍为 leased
func TestAutoSuspendOffOnlyAlerts(t *testing.T)
func TestAbortedNotRecorded(t *testing.T)
func TestHealthReport(t *testing.T)                    // 24h 和 7d 的成功、失败计数，以及停用状态
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/cardpool/ -run 'Verdict|Suspend|Health|Resume|PerPlan|Aborted|Success'`
Expected: 编译失败

- [ ] **Step 3: 实现 `health.go`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/cardpool/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/cardpool
git commit -m "feat(cardpool): 按产品×套餐与单卡的健康度"
```

---

### Task 7: 选卡、租约与回收

**Files:**
- Create: `backend/internal/cardpool/lease.go`、`recycle.go`
- Test: `backend/internal/cardpool/lease_test.go`、`recycle_test.go`

**Interfaces:**
- Consumes: Task 4（`submitTransfer`、`submitStatus`、`finalize`）、Task 5（`RefreshCard`）、Task 6（`recordOutcome`、`productSuspended`）
- Produces:

```go
type AcquireRequest struct{ PlanKey, OrderRef, AccountEmail string; ExcludeCardIDs []string }
type Lease struct {
	Token, CardID, ProductID, Currency, PlanKey, OrderRef, AccountEmail string
	ExpiresAt time.Time
}
func (s *Service) Acquire(ctx context.Context, req AcquireRequest) (*Lease, error)
func (s *Service) Fund(ctx context.Context, l *Lease, amountCents int64) (*Order, error)
func (s *Service) Sensitive(ctx context.Context, l *Lease) (*marswallex.CardSensitive, error)
func (s *Service) Release(ctx context.Context, l *Lease, o Outcome) error
func (s *Service) AdminLease(ctx context.Context, cardID, by string) (*Lease, error)
func (s *Service) AdminFund(ctx context.Context, cardID string, amountCents int64, by string) (*Order, error)
func (s *Service) AdminRelease(ctx context.Context, cardID, by string) error
func (s *Service) ViewSensitive(ctx context.Context, cardID, by string) (*marswallex.CardSensitive, error)
func (s *Service) releaseCard(ctx context.Context, cardID, token string) error
func (s *Service) expireLeasesOnce(ctx context.Context) error
func (s *Service) advanceCardsOnce(ctx context.Context) error
func (s *Service) kickReplenish() // 非阻塞写入 Task 3 建好的 kickReplenishCh；Task 9 的 Start 消费
func expiryEnd(mmyy string) (time.Time, bool) // "MMyy" → 下个月 1 日 00:00 UTC；格式不对 → false
```

- **`Acquire`**：
  1. 取 `PlanProducts[PlanKey]`，没有就取 `PlanProducts["default"]`。
  2. 逐个产品处理，跳过 `productSuspended` 的。在该产品下查询候选卡（最多 20 张），条件为：
     - `pool_state = 'available'`，`platform_status = 'ACTIVE'`，不在排除列表中；
     - `LEFT JOIN mw_cardholders`：持卡人为空，或其状态不是 `DISABLED` / `SUSPENDED`。
  3. 排序为 `use_count DESC, created_at ASC`。在 Go 里过滤掉 `expiryEnd` 失败的卡，以及 `expiryEnd ≤ now.AddDate(0,1,0)` 的卡。
  4. 逐张执行 `UPDATE ... SET pool_state='leased', lease_token=?, lease_owner=?, lease_expires_at=?, lease_sensitive=0 WHERE card_id=? AND pool_state='available'`，`RowsAffected == 1` 就算抢到。令牌是 16 字节随机数的十六进制。
  5. 都没抢到时返回 `fmt.Errorf("%w: 无可用卡", ErrNoCard)`；如果所有产品都被停用，返回 `fmt.Errorf("%w: 全部产品已停用", ErrNoCard)`。同时调用 `kickReplenish()`，告警（key `nocard:<PlanKey>`）。
- **租约有效**的条件：令牌一致、`pool_state = leased`、`lease_expires_at > now`；否则返回 `ErrLeaseInvalid`。
- **`Fund`**：租约有效时调用 `submitTransfer(recharge, 卡币种, by = "lease:"+token)`。
- **`Sensitive`**：租约有效时，**先**写入 `lease_sensitive = 1`、`last_sensitive_at = now`，**再**调 `GetCardSensitive`。
- **`Release`**：校验令牌，然后依次执行 `recordOutcome` → `releaseCard`。
- **`releaseCard`**：带条件 `lease_token = ?`，在一条语句里完成：
  - `use_count += lease_sensitive`；
  - `retire_reason` 非空、或 `use_count ≥ MaxUsesPerCard` → `used`，否则 `releasing`；
  - 清空租约字段，`next_action_at = now`。
- **`expireLeasesOnce`**：对 `leased` 且 `lease_expires_at ≤ now` 的卡调用 `releaseCard`，不记录结果。
- **管理员手动用卡**：
  - 持有方为 `"admin:"+by`。`AdminLease` 只接受 `available` 的卡，失败返回 `ErrBadRequest`。
  - `AdminFund` 和 `AdminRelease` 要求卡处于租用中、且持有方是本人，否则返回 `ErrLeaseInvalid`。
  - `ViewSensitive`：`available` → `badRequest("请先租用")`；`leased`（不论持有方）→ 先标记本次租约已取卡号，再查询；其他状态 → 只查询，不改状态。
- **`advanceCardsOnce`**：处理到期的 `releasing` / `used` 卡（`next_action_at` 为空或已到），以及 `frozen_at` 不为空、`cancel_after ≤ now` 的 `used` 卡，每轮最多 50 张。
  - 这张卡有非终态单据（含 `needs_review`）时跳过。
  - 同类型的最近一笔单据是 `failed` / `not_sent`，且 `finished_at > now − 600` 时：设 `next_action_at = finished_at + 600`，告警（key `recycle:<卡>`），然后跳过。
  - `releasing`：
    1. `RefreshCard`；出错则 `next_action_at = now + 60`。
    2. 平台状态 `CANCELED` → `retired`；`REVOKED` → `disabled`（原因「平台冻结」），并告警。
    3. 可用余额大于 0 → 提交转出单，`next_action_at = NULL`（由单据定论来推动下一步）。
    4. 冻结余额大于 0 → `next_action_at = now + 600`。
    5. 否则 → `available`，`next_action_at = NULL`。
  - `used`：
    - `frozen_at` 为空：`RefreshCard` → 可用余额大于 0 就转出；否则，平台状态是 `REVOKED` 时直接写入 `frozen_at` 和 `cancel_after`，不是就提交改状态单（目标 `REVOKED`）。
    - `frozen_at` 已有：`now ≥ cancel_after` 就提交改状态单（目标 `CANCELED`），否则 `next_action_at = cancel_after`。

- [ ] **Step 1: 写失败测试**

```go
// lease_test.go
func TestAcquirePriorityAndOrder(t *testing.T) // gpt: [p1,p2]；p2 的卡较早、p1 的卡较新 → 选 p1；p1 内 use_count 为 1 的优先于 0 的，use_count 相同时较早入池的优先
func TestAcquireSkipsSuspendedExcludedExpiringAndBadHolder(t *testing.T) // p1 停用 → 选 p2；排除列表中的卡跳过；有效期 "1026"（now=2026-10-28）跳过；"13/26" 跳过；持卡人 DISABLED 跳过；持卡人为空的卡可选
func TestAcquireUsesDefaultPlan(t *testing.T)
func TestAcquireNoCardReasons(t *testing.T)    // 没有卡 → errors.Is(err, ErrNoCard) 且错误信息含「无可用卡」；全部停用 → 含「全部产品已停用」；同一天只告警 1 次
func TestConcurrentAcquireNeverDoubleLeases(t *testing.T) // 5 张可用卡、20 个协程并发 → 恰好 5 个成功，卡号互不相同，其余 15 个为 ErrNoCard
func TestFundRequiresValidLease(t *testing.T)  // 租约过期 → ErrLeaseInvalid，且没有发请求
func TestSensitiveMarksBeforeCall(t *testing.T) // 敏感信息接口回 Unknown() → 返回错误，但 lease_sensitive==1
func TestReleaseTransitions(t *testing.T)      // 上限 1：取过卡号 → used、use_count=1；没取过 → releasing、use_count=0；上限 3：第 1、2 次取卡号后释放 → releasing，第 3 次 → used；retire_reason 非空时 use_count=0 也转 used
func TestReleaseRecordsOutcome(t *testing.T)   // Release(failed) → mw_card_outcomes 多一行；aborted 不写
func TestReleaseWrongToken(t *testing.T)       // ErrLeaseInvalid
func TestExpireLeases(t *testing.T)            // 到期 → releasing，且没有写结果
func TestAdminLeaseFlow(t *testing.T)          // AdminLease 只能租 available；用其他管理员身份 AdminFund → ErrLeaseInvalid；查看 available 卡的卡号 → ErrBadRequest；leased 的卡查看后被标记；used 的卡查看后状态不变；AdminRelease 后为 releasing 或 used
// recycle_test.go
func TestReleasingSweepsThenAvailable(t *testing.T)  // 余额 2.00 → 一条转出单；finalize 成功后下一轮 GetCard 回余额 0 → available
func TestReleasingWaitsOnFrozenBalance(t *testing.T) // 冻结余额 1.00 → next_action_at=now+600，仍为 releasing
func TestUsedFlowSweepFreezeCancel(t *testing.T)     // 转出 → REVOKED → frozen_at、cancel_after=+7d → 时间未到不动作 → 时间到提交 CANCELED → retired
func TestCancelAfterZeroDays(t *testing.T)           // CancelAfterDays=0 → 冻结后的下一轮就注销
func TestRecycleFailureThrottlesAndAlerts(t *testing.T) // 转出被拒 → failed；10 分钟内不再发请求、告警 1 次；10 分钟后重试
func TestRecycleSkipsWhenOrderInFlightOrNeedsReview(t *testing.T)
func TestExternalFreezeOnReleasing(t *testing.T)     // GetCard 回 REVOKED → disabled，原因「平台冻结」，告警
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/cardpool/ -run 'Acquire|Fund|Sensitive|Release|Expire|AdminLease|Releasing|Used|Cancel|Recycle|External'`
Expected: 编译失败

- [ ] **Step 3: 实现 `lease.go`、`recycle.go`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/cardpool/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/cardpool
git commit -m "feat(cardpool): 选卡、租约、取卡号与回收（清零、冻结、注销）"
```

---

### Task 8: 自动补卡

**Files:**
- Create: `backend/internal/cardpool/replenish.go`
- Test: `backend/internal/cardpool/replenish_test.go`

**Interfaces:**
- Consumes: Task 4 `submitApply`、`expiryMMyy`；Task 3 策略与 `beijingDayStart`；`s.applyMu`（Task 5）
- Produces: `func (s *Service) replenishOnce(ctx context.Context) error`

- **逻辑**：持有 `s.applyMu`，逐个处理 `ProductTargets` 中 `Target > 0` 的产品：
  1. 缺口 = `Target −`（`available` 与 `releasing` 卡数 + 状态为 `pending` / `processing` / `unknown` / `needs_review` 的开卡单数）。缺口 ≤ 0 就跳过。
  2. 今日数 = 该产品自 `beijingDayStart(now)` 起的开卡单数（任何状态都算）。`DailyLimit == 0` 或今日数已达上限 → 告警（key `limit:<产品>`），跳过。
  3. 产品列表每轮最多请求一次 `ListCardProducts`，只在有缺口时请求。产品不存在 → 告警（key `product:<产品>`），跳过。
  4. 可用持卡人：按 `HolderIDs` 的顺序，取已导入且为 `APPROVED` 的。没有 → 告警（key `noholder`），跳过。
  5. 本轮开卡数 = min(缺口, `DailyLimit −` 今日数)。逐张 `submitApply`，持卡人用 `s.rr` 计数器轮流选（`s.rr` 在各产品之间共享）。
  6. 某张开卡单的结果是 `failed` 或 `not_sent` → 告警（key `applyfail:<产品>`），停止本产品本轮的开卡。

- [ ] **Step 1: 写失败测试**

```go
func TestReplenishFillsGap(t *testing.T)            // 目标 3、1 张 available、1 条 processing 开卡单 → 只发 1 次 POST /v2/card
func TestReplenishDailyLimitBeijing(t *testing.T)   // 上限 2，今天已有 2 条 → 不开卡；时钟设为 15:59Z 仍不开；拨到 16:00Z（北京次日）→ 开卡
func TestReplenishFailedCountsAndStops(t *testing.T) // 回 Rejected(400,"PRODUCT_OFFLINE") → 本轮只有 1 次请求；当天计数为 1；下一轮又失败，当天仍只告警 1 次
func TestReplenishHolderRoundRobinApprovedOnly(t *testing.T) // [h1 APPROVED, h2 REJECTED, h3 APPROVED]、缺口 3 → card_holder_id 依次为 h1、h3、h1
func TestReplenishNoHolderAlerts(t *testing.T)
func TestReplenishProductMissingAlerts(t *testing.T)
func TestReplenishOpenAmountUsesPolicy(t *testing.T) // OpenAmountCents=100 → 请求 amount=="1.00"
func TestReplenishZeroDailyLimitDoesNothing(t *testing.T) // 目标 >0、上限 0 → 不发请求，告警 1 次
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/cardpool/ -run Replenish`
Expected: 编译失败

- [ ] **Step 3: 实现 `replenish.go`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/cardpool/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/cardpool
git commit -m "feat(cardpool): 按产品目标数自动补卡，受每日上限约束"
```

---

### Task 9: Webhook 事件处理、3DS 验证码与后台协程

**Files:**
- Create: `backend/internal/cardpool/events.go`、`otp.go`、`run.go`
- Test: `backend/internal/cardpool/events_test.go`、`otp_test.go`、`run_test.go`

**Interfaces:**
- Consumes: Task 1 `marswallex.DecodeWebhookPayload` 与各事件结构；Task 4 `finalize`；Task 5 `RefreshCard`；Task 7 `kickReplenish`；`secretbox.LoadKey`、`Seal`、`Open`
- Produces:

```go
func (s *Service) processEventsOnce(ctx context.Context) (int, error)
func (s *Service) handleEvent(ctx context.Context, ev *marswallex.WebhookEvent) (note string, err error)
// Service 字段 eventHandler 默认指向 handleEvent，测试可替换
func redactOTP(raw string) (string, bool, error) // data 为对象或 JSON 字符串时都能把 data.otp 改为 "***"；返回是否有改动
func (s *Service) Reprocess(ctx context.Context, eventID int64, by string) error
func (s *Service) WaitOTP(ctx context.Context, cardID string, since time.Time) (string, error)
type OTPView struct{ OTP, MerchantName, Amount, Currency string; ReceivedAt time.Time }
func (s *Service) ViewOTP(ctx context.Context, cardID, by string) (*OTPView, error)
func (s *Service) purgeOTPsOnce(ctx context.Context) error
func (s *Service) Start(ctx context.Context)
func (s *Service) Kick()
const otpAAD = "mw_3ds_otp"
```

- **`processEventsOnce`**：
  - 取 `processed_at IS NULL AND process_attempts < 10 AND (next_attempt_at IS NULL OR next_attempt_at <= now)` 的事件，按 `id` 升序，每轮最多 100 条，逐条用 `DecodeWebhookPayload(payload)` 解析后交给 `eventHandler`。
  - 解析失败（包括子项目 1 存档的无法解析事件）→ 标记已处理，备注「无法解析」。
  - 处理成功 → 写入 `processed_at`、`process_note`，清空 `process_error`。
  - 处理出错 → `process_attempts++`，写入 `process_error = err.Error()`，`next_attempt_at = now + backoff(attempts-1)`；达到 10 次时告警（key `event:<id>`）。
- **`handleEvent`**（某个 topic 的数据解析失败时，标记已处理，备注「数据无法解析」，并告警）：
  - `CARD_APPLY_RESULT`：按 `out_order_no` 找单据，找不到备注「无对应本地单据」；`SUCCESS` → `finalize(success, CardID)`，`FAILED` → `finalize(failed, FailureReason)`，来源记为 `webhook`。
  - `CARD_TRANSFER_RESULT`：有对应单据就按 `SUCCESS` / `FAIL` / `APPLY` 定论；没有对应单据、但卡在池中 → 尽力 `RefreshCard`（失败只写备注）。
  - `CARD_STATUS_CHANGED`：卡不在池中 → 备注。存在同卡同目标的改状态单、且它非终态或在 600 秒内成功过 → 视为我们发起的，非终态就定论为成功。否则是外部变更：
    - `CANCELED` → `retired`，卡原来是 `leased` 时告警；
    - `REVOKED` → `available` 的卡转为 `disabled`（原因「平台冻结」）并告警，`leased` 的卡只告警；
    - 其他状态只更新 `platform_status`。
  - `CARDHOLDER_STATUS_CHANGE`：只更新已导入的持卡人（status、remark、synced_at）；新状态不是 `APPROVED` 时告警（key `holder:<id>`）。
  - `AUTH_EVENT`：按 `transaction_id` upsert 交易，`kind` 取 `TransactionType`。卡为 `available` 且 `use_count = 0` → 转为 `disabled`（原因「未交出卡号出现授权」）并告警。
  - `SETTLE_EVENT`：upsert 交易。
  - `CARD_3DS_OTP`：
    - OTP 为 `***` → 备注「验证码已抹除」，不写入。
    - `LoadKey` 失败 → 抹掉原文中的验证码，标记已处理，写入 `process_error = "主密钥缺失，验证码未保存"`，不重试。
    - 否则把验证码加密写入 `mw_3ds_otps`。
  - `ACCOUNT_STATUS_CHANGED`、`CONVERSION_RESULT`、`TRANSFER_RESULT` → 备注「本子项目不处理」；未知 topic → 备注「未知 topic」。
  - `CARD_3DS_OTP` 和 `AUTH_EVENT` 处理完后，都用 `redactOTP` 改写 `payload`。
- **`Reprocess`**：清空 `processed_at`、`process_error`、`process_note`、`next_attempt_at`，`process_attempts = 0`，然后 `Kick()`。
- **`WaitOTP`**：每秒查一次 `card_id = ? AND received_at ≥ since AND received_at > now−1800 AND otp_sealed != ''`，按 id 倒序取最新一条，解密后清空密文、写入 `consumed_at`；ctx 结束时返回 `ctx.Err()`。
- **`ViewOTP`**：取最新一条未过期且密文不为空的验证码，不清除；没有就返回 `ErrNotFound`。
- **`purgeOTPsOnce`**：清空 `received_at ≤ now − 1800` 的密文。
- **`Start`** 启动三个协程，`ctx` 结束时都退出：
  - 每 5 秒一轮：`reconcileOrdersOnce` → `expireLeasesOnce` → `advanceCardsOnce` → `purgeOTPsOnce`；
  - 每 60 秒一次，或收到补卡通知时：`replenishOnce`；
  - 收到 `Kick()` 时，或每 10 秒：`processEventsOnce`。
  - 每个函数的错误都用 `log.Printf` 记下，不中断循环；`Options.Client` 返回配置错误时，这一轮跳过需要调接口的步骤。
- **`Kick()`**：非阻塞写入容量为 1 的 channel。

- [ ] **Step 1: 写失败测试**

```go
// events_test.go（用 mwtest.SignWebhook 生成原文，经 db.InsertMarsWallexWebhookEvent 入库后调用 processEventsOnce）
func TestApplyResultEventFinalizes(t *testing.T)
func TestTransferResultEvent(t *testing.T)            // 有本地单据 → 定论；没有 → 刷新余额，备注已写入
func TestStatusChangedOursVsExternal(t *testing.T)    // 我们发起的 → 单据成功；外部 REVOKED：available → disabled，leased → 只告警；外部 CANCELED → retired
func TestCardholderStatusChangeEvent(t *testing.T)
func TestAuthOnUnusedAvailableCardDisables(t *testing.T)
func TestAuthOnUsedCardOnlyRecords(t *testing.T)      // use_count=1 的 available 卡 → 只记录，状态不变
func TestTxnUpsertOverwritesPending(t *testing.T)     // 同一 transaction_id 先 pending 后 approved → 1 行，状态为 approved
func TestUnknownTopicAndUnparseable(t *testing.T)
func TestEventRetryBackoffAndStop(t *testing.T)       // eventHandler 换成总是报错的 → next_attempt_at 依次 +10s、+30s…；第 10 次后不再处理，告警 1 次
func TestReprocessIsIdempotent(t *testing.T)          // 转入结果事件处理两次 → 单据仍为 success，没有冲突告警
// otp_test.go
func TestOTPEventStoredEncryptedAndRedacted(t *testing.T) // 对象 data 和字符串 data 两种：otp_sealed 以 "v1:" 开头、不含 "123456"；payload 里有 "***"、没有 "123456"
func TestAuthEventOTPRedacted(t *testing.T)
func TestWaitOTPConsumes(t *testing.T)              // 返回 "123456"；之后密文为空；再次 WaitOTP（ctx 200ms）→ 超时
func TestWaitOTPIgnoresOlderThanSince(t *testing.T)
func TestOTPPurgedAfter30Min(t *testing.T)
func TestViewOTPDoesNotConsumeAndExpires(t *testing.T) // 连看两次都成功；31 分钟后 → ErrNotFound
func TestMasterKeyMissingStillRedacts(t *testing.T)    // 清空 APP_MASTER_KEY → 已处理、process_error 含「主密钥」、payload 已抹除、没有验证码行
func TestReprocessRedactedOTPDoesNotStoreStars(t *testing.T)
// run_test.go
func TestStartProcessesKickedEventPromptly(t *testing.T) // Start(ctx)；入库一条事件后 Kick → 1 秒内已处理
func TestStartStopsOnCancel(t *testing.T)                // cancel 之后 Kick 不阻塞、不 panic
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/cardpool/ -run 'Event|Status|Cardholder|Auth|Txn|Unknown|Reprocess|OTP|Purge|MasterKey|Start'`
Expected: 编译失败

- [ ] **Step 3: 实现 `events.go`、`otp.go`、`run.go`**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go test ./internal/cardpool/ -count=1 -race`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/cardpool
git commit -m "feat(cardpool): Webhook 业务处理、3DS 验证码加密与后台协程"
```

---

### Task 10: 后台接口、路由与启动

**Files:**
- Create: `backend/internal/handler/cardpool.go`
- Test: `backend/internal/handler/cardpool_test.go`
- Modify: `backend/internal/handler/marswallex.go`（新增 `func MarsWallexClient() (*marswallex.Client, error) { return marsWallexClientFromSettings() }`）
- Modify: `backend/internal/handler/marswallex_webhook.go`：
  - 入库且 `inserted` 为真时调用 `cardPool.Kick()`（`cardPool` 不为 nil 时）；
  - 列表响应增加 `process_attempts`、`process_note`；
  - 新增 `AdminReprocessMarsWallexWebhookEvent`。
- Modify: `backend/internal/server/http.go`：注册路由；在 `plansync.Start(ctx)` 之后创建并启动服务。

**Interfaces:**
- Consumes: Task 3–9 的全部导出方法
- Produces:
  - `func SetCardPool(s *cardpool.Service)`，以及包级变量 `cardPool *cardpool.Service`；
  - 在 `admin` 组下注册以下路由：

```
GET  /cardpool/overview                      GET  /cardpool/products
GET  /cardpool/cardholders                   POST /cardpool/cardholders/import          {card_holder_id}
POST /cardpool/cardholders/:id/refresh
GET  /cardpool/cards?state=&product_id=&page=&page_size=
GET  /cardpool/cards/:id
POST /cardpool/cards/apply                   {product_id, card_holder_id, count}
POST /cardpool/cards/import                  {card_id, product_id, card_holder_id}
POST /cardpool/cards/:id/refresh             POST /cardpool/cards/:id/disable           {reason}
POST /cardpool/cards/:id/enable              POST /cardpool/cards/:id/freeze
POST /cardpool/cards/:id/unfreeze            POST /cardpool/cards/:id/cancel
POST /cardpool/cards/:id/sweep               POST /cardpool/cards/:id/lease
POST /cardpool/cards/:id/release             POST /cardpool/cards/:id/fund              {amount:"1.00"}
POST /cardpool/cards/:id/sensitive           POST /cardpool/cards/:id/otp
GET  /cardpool/orders?state=&kind=&card_id=&page=&page_size=
POST /cardpool/orders/:no/recheck            POST /cardpool/orders/:no/confirm-failed   {note}
GET  /cardpool/transactions?card_id=&page=&page_size=
GET  /cardpool/policy                        PUT  /cardpool/policy                      Policy JSON
GET  /cardpool/health                        POST /cardpool/health/resume               {product_id, plan_key}
POST /marswallex/webhook-events/:id/reprocess
```

- **错误映射**：
  - `cardPool` 为 nil → 503「卡池未启动」；
  - `ErrBadRequest` → 400，`ErrNotFound` → 404，`ErrCardBusy` / `ErrLeaseInvalid` / `ErrNoCard` → 409，响应都是 `{"error": err.Error()}`；
  - `*marswallex.Error` → 502，响应为 `{"error": ..., "outcome": ...}`；
  - 客户端配置错误 → 503；其他 → 500。
- **其他约定**：
  - `fund` 的金额用 `marswallex.ParseAmount` 解析，必须大于 0。
  - 所有 POST 和 PUT 调用 `db.WriteAudit(adminName(c), "cardpool.<动作>", 详情, c.ClientIP())`。详情只写卡号 ID（`card_id`）、单号、金额、产品，不写卡号、CVV2、验证码。
  - `sensitive` 和 `otp` 的响应加 `Cache-Control: no-store`。`sensitive` 的响应为 `{card_no, cvv2, expiry_date}`。
- **启动**（`server/http.go`）：

```go
pool := cardpool.New(cardpool.Options{Client: handler.MarsWallexClient, Alert: notify.SendText})
handler.SetCardPool(pool)
pool.Start(ctx)
```

- [ ] **Step 1: 写失败测试**（沿用 `marswallex_test.go` 的 `mwSetup` 写法；服务的 `Client` 指向 `mwtest`；Webhook 相关测试先用 `db.SetSetting(db.SettingMarsWallexPlatformPublicKey, srv.PlatformPublicKeyPEM())` 写入 mwtest 的平台公钥）

```go
func TestCardPoolRoutesRequireService(t *testing.T)  // SetCardPool(nil) → 503
func TestSensitiveEndpointAuditsWithoutSecrets(t *testing.T) // 先 lease，再 sensitive → 200、Cache-Control 为 no-store；审计详情含 card_id，但不含卡号和 CVV2
func TestOtherEndpointsNeverReturnCardNumber(t *testing.T)   // mwtest 卡详情回完整卡号 "4111111111111234" → GET cards 和 GET cards/:id 的响应体里都没有这个串
func TestConfirmFailedRequiresNote(t *testing.T)     // 400
func TestErrorMapping(t *testing.T)                  // 租用中的卡 disable → 409；数量 21 → 400；卡不存在 → 404
func TestFundParsesAmount(t *testing.T)              // "1.005" → 400；"1.00" → 产生一条 100 分的转入单
func TestPolicyPutValidates(t *testing.T)            // 非法前缀 → 400，原策略不变
func TestWebhookKicksService(t *testing.T)           // 服务已 Start；POST 一条带签名的 CARD_STATUS_CHANGED → 1 秒内 processed_at 已写入
func TestReprocessEndpoint(t *testing.T)
```

- [ ] **Step 2: 运行确认失败**

Run: `cd backend && go test ./internal/handler/ -run 'CardPool|Sensitive|OtherEndpoints|ConfirmFailed|ErrorMapping|FundParses|PolicyPut|WebhookKicks|ReprocessEndpoint'`
Expected: 编译失败

- [ ] **Step 3: 实现 handler、导出客户端加载函数、修改 Webhook 接收、注册路由与启动**

- [ ] **Step 4: 运行确认通过**

Run: `cd backend && go vet ./... && go test ./internal/handler/ -count=1 -run 'CardPool|Sensitive|OtherEndpoints|ConfirmFailed|ErrorMapping|FundParses|PolicyPut|WebhookKicks|ReprocessEndpoint|MarsWallex'`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/handler backend/internal/server
git commit -m "feat(cardpool): 后台接口、Webhook 通知与服务启动"
```

---

### Task 11: 前端「卡池」页

**Files:**
- Create: `frontend/src/views/admin/CardPool.vue`
- Modify: `frontend/src/router/index.ts`（在 `marswallex` 之后加 `{ path: 'cardpool', name: 'CardPool', component: () => import('../views/admin/CardPool.vue') }`）
- Modify: `frontend/src/layouts/AdminLayout.vue`（`navItems` 中 MarsWallex 之后加 `{ path: '/ops/cardpool', label: '卡池', icon: 'Box' }`）
- Modify: `frontend/src/element.ts`（导入并注册 `Box` 图标）
- Modify: `frontend/src/views/admin/MarsWallexIntegration.vue`（Webhook 列表）

**Interfaces:**
- Consumes: Task 10 的接口；`authFetch`（`../../lib/api`）、`dialog`（`../../lib/dialog`），写法照 `MarsWallexIntegration.vue`

- **页面结构**：用 `el-tabs` 分 6 个标签。
  - **概览**：主钱包余额；每个产品的库存表（可用 / 租用中 / 清零中 / 已用 / 停用 / 目标 / 今日开卡 / 上限）；待处理事项计数，点击跳到对应标签。
  - **卡**：按状态、产品筛选的分页表，列有 card_id、后四位、产品、状态、平台状态、余额、已用次数、到期。行操作按状态显示：
    - `available`：租用、停用；
    - `leased`（本人租用）：注资、查看卡号、查看验证码、释放；
    - `disabled`：启用；
    - 未注销的卡：刷新、冻结、解冻、清零、注销。
    - 另有「手动开卡」（选产品、持卡人、数量）和「导入卡」（card_id、产品、持卡人可选）。
    - 卡详情抽屉里显示这张卡的单据和交易。
  - **持卡人**：列表、按 ID 导入、刷新。
  - **单据**：按状态、类型筛选；`needs_review` 的行显示「再查一次」和「确认失败」（必须填备注）。
  - **交易**：按卡筛选的分页表。
  - **策略与健康度**：策略表单，字段与 `Policy` 一一对应；每个套餐的产品优先级用产品多选并支持排序；产品目标数用表格编辑。保存失败时显示后端的错误信息。下方是健康度表，带「恢复」按钮。
- **确认与敏感信息**：
  - 注销、确认失败、查看卡号、查看验证码之前，都用 `dialog` 弹窗确认。
  - 卡号和验证码只显示在弹窗里，关闭弹窗即清空变量，不写入 localStorage。
- **金额**：一律显示字符串，例如 `(cents/100).toFixed(2)`；注资输入框按字符串提交。
- **`MarsWallexIntegration.vue` 的 Webhook 表格**：
  - 增加「处理状态」列：已处理 / 失败（已重试 N 次）/ 待处理；
  - 增加「备注 / 错误」列；
  - 失败的、以及已处理但带错误的事件显示「重新处理」按钮。

- [ ] **Step 1: 实现页面、路由、导航和图标**

- [ ] **Step 2: 构建**

Run: `cd frontend && npm run build`
Expected: 构建成功，没有类型错误

- [ ] **Step 3: 提交**

```bash
git add frontend/src
git commit -m "feat(ops): 卡池管理页与 Webhook 处理状态"
```

---

### Task 12: 全量验证

- [ ] **Step 1: 后端**

Run: `cd backend && go vet ./... && go test ./... -count=1 2>&1 | grep -E '^(ok|FAIL|---)'`
Expected: 除 `internal/handler` 中已知的 `TestIssueProductKeepsGPTDefault`、`TestXIssueInfersProductWithoutQueryParameter` 外全部 PASS

- [ ] **Step 2: 竞态检测**

Run: `cd backend && go test ./internal/cardpool/... ./internal/marswallex/... -race -count=1`
Expected: PASS

- [ ] **Step 3: 敏感信息检查**

Run: `cd backend && grep -rnE 'log\.(Print|Printf|Println)\(.*(otp|OTP|CardNo|card_no|CVV|cvv)' internal/cardpool internal/handler/cardpool.go`
Expected: 无输出

- [ ] **Step 4: 前端**

Run: `cd frontend && npm run build`
Expected: 成功

- [ ] **Step 5: 汇报**：列出验收需要用户操作的步骤（spec §11.2 第 3 项），以及待联调确认项（spec §10）。
