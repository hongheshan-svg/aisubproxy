# 代理商核心 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 管理员给代理商开户、定价、调账；代理商用美元余额下单取本站码（MarsWallex），随时导出自己的码，按流水对账。

**Architecture:**
- SQL 和事务放在 `internal/db/agent*.go`，沿用现有写法：启动时建表补列，事务开头就拿写锁，用条件 UPDATE 防并发。
- 不碰数据库的纯逻辑放进新包 `internal/agent`：TOTP、会话令牌、登录名和编码校验、生成初始密码、金额文本。
- HTTP 处理放在 `internal/handler/agent_*.go`，由 `RegisterAgentRoutes`、`RegisterAgentAdminRoutes` 挂到 `/api/v1/agent` 和 `/api/v1/admin` 下。
- 前端新开 `/agent` 区域，用单独的布局；管理员后台在 `/ops` 下新加三页。

**Tech Stack:** Go 1.x + gin v1.10 + mattn/go-sqlite3 + golang-jwt/v5；Vue 3 + Element Plus + vitest；新增 npm 依赖 `qrcode`。

**Spec:** `docs/superpowers/specs/2026-11-04-agent-core-design.md`（提交 5fe38a8，用户已审）。

## Global Constraints

- 所有界面文字、错误提示、提交说明都用简体中文。代理商后台只有中文，不加 i18n 键。
- 金额一律用 int64 美分，后端不出现 float；前端格式化用整数运算。
- 时间存 Unix 秒（INTEGER），界面按北京时间显示；「今天」「本月」也按北京时间划分（用 `handler/ops_maintenance.go` 里的 `beijingZone`）。
- 完整的码、密码、初始密码、两步验证密钥、动态码，不进日志、审计、告警、错误信息，也不出现在 URL 里。
- 代理商下单必须有可用的 `APP_MASTER_KEY`。
- 代理商接口只认 cookie；所有代理商数据的查询，代理商 ID 只能取自会话，不能取自请求参数。
- 单次下单 1–200 张（`cardplatform.MaxIssueCount`），备注 ≤ 200 字（`localCDKNoteMax`）；导出单次最多 10000 张。
- 不加新的 Go 依赖。npm 只加 `qrcode`，以及开发依赖 `@types/qrcode`。
- 测试不调用真实业务接口，不动真实资金。不碰 18080 端口和仓库的 `data/` 目录。
- 提交只加自己改的文件：`git add <路径> && git commit -m "<中文说明>" -- <路径>`。不用 `git add -A`，不提交 `.claude/`、`.superpowers/`。提交说明末尾写 `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`。
- 后端门禁：`cd backend && gofmt -l ./internal ./cmd`（应无输出）、`go vet ./...`、`go test ./internal/db/... ./internal/agent/... ./internal/handler/... ./internal/server/...`。
- 前端门禁：`cd frontend && npx vitest run && npm run build`。

## Review Focus

1. **重复的请求号要先于可售检查处理**：第一次下单成功后，商品被下架或改了价，代理商用同一个请求号重试，应该拿回第一次的码，而不是被告知「暂不可售」。设计 5.1 把检查列在查请求号前面，这里裁定查请求号放在「账号启用、主密钥可用」之后、商品检查之前。测试见 Task 7。
2. **展示价和下单时的价不一样**：代理商在确认框里看到 $5.00，提交前管理员改成了 $6.00。正常人会期待不按 $6.00 扣钱。下单请求带 `expected_unit_price_cents`，和当前价不一致就返回 409 `price_changed`「价格已变化，请刷新后重新下单」，不扣钱。测试见 Task 7。
3. **同一个请求号同时提交两次**：例如双击，或者前端超时后马上重试。只能生成一笔订单、扣一次钱，两次请求拿到同一批码。测试见 Task 3（数据层）和 Task 7（接口层）。
4. **登录名大小写**：已有 `alice` 时，建 `Alice` 要提示登录名已被使用；用 `ALICE` 登录，登的就是 `alice`。测试见 Task 2 和 Task 5。
5. **下单参数越界不扣钱**：数量为 0、201、非整数，备注 201 字，请求号含空格或超过 64 位，一律返回 400，余额和流水都不变。测试见 Task 7。

---

### Task 1: 建表和商品价格（数据层）

**Files:**
- Create: `backend/internal/db/agent_catalog.go`
- Create: `backend/internal/db/agent_schema.go`
- Modify: `backend/internal/db/db.go`（`createTables` 的迁移列表里，在 `MigrateLocalCDKs` 之后调用 `MigrateAgents`）
- Test: `backend/internal/db/agent_catalog_test.go`（`package db_test`）

**Interfaces:**
- Produces（`package db`）:
  - `func MigrateAgents() error`
    - 建 7 张表，列和约束完全按设计第 4 节：`agent_tiers`、`agents`、`agent_products`、`agent_tier_prices`、`agent_price_overrides`、`agent_orders`、`agent_ledger`；
    - 建索引：`uq_agent_ledger_ext ON agent_ledger(kind, ext_ref) WHERE ext_ref <> ''`、`uq_agent_ledger_order ON agent_ledger(order_id) WHERE kind = 'purchase'`、`idx_agent_ledger_agent(agent_id, id)`、`idx_local_cdks_agent(agent_id, id)`；
    - 用 `pragma_table_info` 判断后，给 `local_cdks` 补 `agent_id INTEGER NOT NULL DEFAULT 0`、`agent_order_id INTEGER NOT NULL DEFAULT 0`。
    - `agents.username` 用 `UNIQUE COLLATE NOCASE`。
  - 错误：`ErrAgentTierNotFound`、`ErrAgentTierNameTaken`、`ErrAgentTierInUse`、`ErrAgentProductNotFound`、`ErrAgentProductExists`。
  - 等级：
    - `type AgentTier struct { ID int64; Name, Note string; AgentCount int; CreatedAt, UpdatedAt int64 }`
    - `func CreateAgentTier(ctx context.Context, name, note string, now time.Time) (int64, error)`
    - `func UpdateAgentTier(ctx context.Context, id int64, name, note string, now time.Time) error`
    - `func DeleteAgentTier(ctx context.Context, id int64) error`
    - `func ListAgentTiers(ctx context.Context) ([]AgentTier, error)`
  - 商品：
    - `type AgentProduct struct { ID int64; Code, Provider, Product, PlanKey, Region, Name string; Enabled bool; Sort int; CreatedAt, UpdatedAt int64 }`
    - `func CreateAgentProduct(ctx context.Context, p AgentProduct, now time.Time) (int64, error)`
    - `func UpdateAgentProduct(ctx context.Context, id int64, name string, enabled bool, sort int, now time.Time) error`：只改这三项，编码、服务商、套餐、地区不能改。
    - `func ListAgentProducts(ctx context.Context) ([]AgentProduct, error)`：按 sort、id 升序。
    - `func GetAgentProductByCode(ctx context.Context, code string) (AgentProduct, error)`
  - 价格：
    - `type AgentPriceCell struct { TierID, ProductID, PriceCents int64 }`
    - `func ListAgentTierPrices(ctx context.Context) ([]AgentPriceCell, error)`
    - `func PutAgentTierPrices(ctx context.Context, cells []AgentPriceCell, now time.Time) error`：一个事务；`PriceCents > 0` 就写入或覆盖，`== 0` 就删除，`< 0` 报错；等级或商品不存在时报对应的 NotFound。
    - `type AgentOverride struct { ProductID, PriceCents int64 }`
    - `func ListAgentOverrides(ctx context.Context, agentID int64) ([]AgentOverride, error)`
    - `func PutAgentOverrides(ctx context.Context, agentID int64, cells []AgentOverride, now time.Time) error`：语义同上。
    - `func AgentPriceFor(ctx context.Context, agentID, productID int64) (cents int64, ok bool, err error)`：先取覆盖价，再取代理商所在等级的价格。
    - `type PurchasableProduct struct { AgentProduct; PriceCents int64 }`
    - `func ListAgentPurchasable(ctx context.Context, agentID int64) ([]PurchasableProduct, error)`：只列已上架、取得到价格的商品。

- [ ] **Step 1: 写失败的测试**

`agent_catalog_test.go` 用 `dbtest.Open(t)`。下面要建代理商，可以直接用 SQL `INSERT INTO agents`（Task 2 才有 `CreateAgent`）。

```go
func TestMigrateAgentsIdempotent(t *testing.T)            // 连跑两次 db.Migrate() 都不报错；local_cdks 有 agent_id、agent_order_id 两列
func TestAgentTierNameUniqueAndInUse(t *testing.T)        // 同名第二次返回 ErrAgentTierNameTaken；有代理商在用时 DeleteAgentTier 返回 ErrAgentTierInUse
func TestAgentProductUniqueComboAndCode(t *testing.T)     // 同编码、同「服务商+产品线+套餐+地区」各返回 ErrAgentProductExists
func TestAgentProductImmutableFields(t *testing.T)        // UpdateAgentProduct 之后，Code/PlanKey/Region 不变
func TestPutAgentTierPricesUpsertAndDelete(t *testing.T)  // 写 500 → 改 600 → 写 0 删除；写 -1 报错，并且整批回滚
func TestAgentPriceForOverrideFirst(t *testing.T)
func TestListAgentPurchasableSkipsDisabledAndUnpriced(t *testing.T)
```

`TestAgentPriceForOverrideFirst` 的关键断言：

```go
// 等级价 500：取到 500, true
// 再加覆盖价 450：取到 450, true
// 删掉覆盖价、删掉等级价：取到 0, false
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd backend && go test ./internal/db/ -run 'TestMigrateAgents|TestAgentTier|TestAgentProduct|TestPutAgentTierPrices|TestAgentPriceFor|TestListAgentPurchasable' -count=1`
Expected: 编译失败，报 `undefined: db.CreateAgentTier` 等。

- [ ] **Step 3: 实现 `agent_schema.go`（`MigrateAgents`）和 `agent_catalog.go`，并在 `db.go` 的迁移列表里调用**

唯一约束冲突用现有写法判断：`sqlite3.Error.ExtendedCode` 等于 `ErrConstraintUnique` 或 `ErrConstraintPrimaryKey`。

- [ ] **Step 4: 跑测试，确认通过；再跑 db 包全部测试，确认现有测试不受影响**

Run: `cd backend && go test ./internal/db/... -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/db/agent_schema.go backend/internal/db/agent_catalog.go backend/internal/db/agent_catalog_test.go backend/internal/db/db.go
git commit -m "feat(agent): 代理商相关表和商品、等级、价格的数据层" -- backend/internal/db/agent_schema.go backend/internal/db/agent_catalog.go backend/internal/db/agent_catalog_test.go backend/internal/db/db.go
```

---

### Task 2: 代理商账号和余额账本（数据层）

**Files:**
- Create: `backend/internal/db/agent_account.go`
- Create: `backend/internal/db/agent_ledger.go`
- Test: `backend/internal/db/agent_account_test.go`、`backend/internal/db/agent_ledger_test.go`

**Interfaces:**
- Consumes: Task 1 的表和 `AgentTier`。
- Produces（`package db`）:
  - 常量：`AgentActive = "active"`、`AgentDisabled = "disabled"`；`LedgerPurchase = "purchase"`、`LedgerAdjust = "adjust"`、`LedgerTopup = "topup"`。
  - 错误：`ErrAgentNotFound`、`ErrAgentUsernameTaken`、`ErrAgentDisabled`、`ErrInsufficientBalance`、`ErrLedgerRefConflict`（同一个外部单号已经用于另一个代理商或另一个金额）。
  - `type InsufficientBalanceError struct { BalanceCents, NeedCents int64 }`，实现 `Error()`，并且 `Is(target error) bool` 对 `ErrInsufficientBalance` 返回 true。
  - 账号：
    - `type Agent struct { ID int64; Username, DisplayName string; MustChangePassword, TOTPBound bool; TierID int64; TierName string; BalanceCents int64; Status string; SessionVersion int64; Note string; LastLoginAt *int64; CreatedAt, UpdatedAt int64 }`。不含密码哈希和 TOTP 密文。
    - `type AgentSecrets struct { PasswordHash, TOTPSealed string; TOTPLastStep int64 }`
    - `type NewAgent struct { Username, DisplayName, PasswordHash string; TierID int64; Note string }`
    - `func CreateAgent(ctx context.Context, a NewAgent, now time.Time) (int64, error)`：`must_change_password = 1`、`session_version = 1`、余额 0。登录名重复（不区分大小写）时返回 `ErrAgentUsernameTaken`；等级不存在时返回 `ErrAgentTierNotFound`。
    - `func GetAgent(ctx context.Context, id int64) (Agent, error)`
    - `func GetAgentByUsername(ctx context.Context, username string) (Agent, AgentSecrets, error)`：不区分大小写。
    - `func GetAgentSecrets(ctx context.Context, id int64) (AgentSecrets, error)`
    - `func ListAgents(ctx context.Context) ([]Agent, error)`：按 id 降序。
    - `func UpdateAgentProfile(ctx context.Context, id int64, displayName string, tierID int64, note string, now time.Time) error`
    - `func SetAgentStatus(ctx context.Context, id int64, status string, now time.Time) error`：停用时 `session_version + 1`。
    - `func SetAgentPassword(ctx context.Context, id int64, hash string, mustChange bool, now time.Time) (newSV int64, err error)`：`session_version + 1`。
    - `func BindAgentTOTP(ctx context.Context, id int64, sealed string, step int64, now time.Time) error`：只在 `totp_sealed = ''` 时生效，否则报错。
    - `func ResetAgentTOTP(ctx context.Context, id int64, now time.Time) error`：清空密文，`totp_last_step = 0`，`session_version + 1`。
    - `func UseAgentTOTPStep(ctx context.Context, id, step int64) (bool, error)`：一条 `UPDATE … WHERE id = ? AND totp_last_step < ?`，影响 1 行才返回 true。
    - `func TouchAgentLogin(ctx context.Context, id int64, now time.Time) error`
    - `func CountAgentCodes(ctx context.Context) (map[int64]int, error)`：按 `local_cdks.agent_id` 计数，跳过 0。
  - 账本：
    - `type LedgerEntry struct { ID, AgentID int64; Kind string; AmountCents, BalanceAfterCents int64; OrderID *int64; ExtRef, Operator, Note string; CreatedAt int64 }`
    - `func applyAgentBalance(ctx context.Context, tx *sql.Tx, e LedgerEntry, requireActive bool, now int64) (LedgerEntry, error)`：**不导出，全系统唯一改 `balance_cents` 的地方**。
      - 用一条条件 UPDATE：`balance_cents = balance_cents + ?`，条件是 `balance_cents + ? >= 0`；`requireActive` 时再加 `status = 'active'`。
      - 影响 0 行时，在同一事务里读一次代理商：不存在返回 `ErrAgentNotFound`；要求启用但已停用返回 `ErrAgentDisabled`；其余返回 `*InsufficientBalanceError`。
      - 然后读出新余额，插入流水，返回带 ID 的流水。
    - `func AdjustAgentBalance(ctx context.Context, agentID, amountCents int64, idemKey, operator, note string, now time.Time) (LedgerEntry, bool, error)`：
      - 第二个返回值表示这次是重放；
      - `amountCents != 0`，`note` 去空白后不能为空，`idemKey` 不能为空；
      - `ext_ref = "admin:" + idemKey`，kind 为 `adjust`；
      - 停用的代理商也能调账。
    - `func CreditAgent(ctx context.Context, agentID int64, kind string, amountCents int64, extRef, operator, note string, now time.Time) (LedgerEntry, bool, error)`：`amountCents > 0`，`extRef` 不能为空，`kind` 只能是 `topup`。
    - 两个入账函数的重放规则相同：
      - 在事务里先按 `(kind, ext_ref)` 查已有的流水；
      - 已有的流水代理商和金额都一致，就原样返回，第二个返回值为 true；
      - 不一致就返回 `ErrLedgerRefConflict`；
      - 撞上唯一索引（并发）也按这个规则处理。
    - `func ListAgentLedger(ctx context.Context, agentID, from, to int64, limit, offset int) ([]LedgerEntry, int, error)`：`from <= created_at < to`，id 降序，第二个返回值是总条数。
    - `type LedgerSummary struct { OpeningCents, ClosingCents, InCents, OutCents int64 }`
    - `func AgentLedgerSummary(ctx context.Context, agentID, from, to int64) (LedgerSummary, error)`：
      - 期初 = `from` 之前最后一条流水的变动后余额，没有就是 0；
      - 期末 = 范围内最后一条流水的变动后余额，范围内没有流水就等于期初；
      - 收入、支出分别累加正数、负数的绝对值。
    - `type AgentBalanceMismatch struct { AgentID, BalanceCents, LedgerSumCents, LastAfterCents int64 }`
    - `func CheckAgentBalances(ctx context.Context) ([]AgentBalanceMismatch, error)`：按设计 5.4 的两个等式检查。
    - `type AgentTotals struct { BalanceCents, TodayOrderCents, MonthOrderCents int64 }`
    - `func AgentTotalsSince(ctx context.Context, dayStart, monthStart int64) (AgentTotals, error)`：下单合计 = `purchase` 流水金额的绝对值之和。

- [ ] **Step 1: 写失败的测试**

```go
func TestCreateAgentUsernameCaseInsensitive(t *testing.T) // 建 "alice" 后建 "Alice" → ErrAgentUsernameTaken；GetAgentByUsername("ALICE") 取到 alice
func TestSetAgentPasswordBumpsSessionVersion(t *testing.T)
func TestResetAgentTOTPClearsAndBumps(t *testing.T)
func TestUseAgentTOTPStepRejectsReplay(t *testing.T)       // step 100 → true；再用 100 → false；99 → false；101 → true
func TestAdjustAgentBalanceIdempotent(t *testing.T)         // 同一个键两次：余额只加一次，第二次返回 replay=true 且流水 ID 相同
func TestAdjustAgentBalanceRejectsOverdraft(t *testing.T)   // 余额 300 时调 -500 → errors.Is(err, ErrInsufficientBalance)；余额仍是 300，没有流水
func TestAdjustAgentBalanceNeedsNote(t *testing.T)
func TestCreditAgentIdempotentAndConflict(t *testing.T)     // 同一个 extRef 再调 → replay；换个金额 → ErrLedgerRefConflict
func TestCreditAgentConcurrentSameRef(t *testing.T)         // 文件库，20 个 goroutine 用同一个 extRef 入账 1000：最终余额 1000，只有 1 条流水
func TestLedgerSummaryOpeningClosing(t *testing.T)
func TestCheckAgentBalancesDetectsTamper(t *testing.T)      // 正常时为空；直接 SQL 改 balance_cents 后返回 1 条
```

`TestCreditAgentConcurrentSameRef` 用文件库，DSN 和生产一样带 `_txlock=immediate`。在 `agent_ledger_test.go` 里加一个辅助函数：

```go
// openAgentFileDB 文件库 + 25 个连接 + 生产同款 DSN：测并发扣款用。
func openAgentFileDB(t *testing.T) // sql.Open("sqlite3", filepath.Join(t.TempDir(), "agent.db")+"?_journal_mode=WAL&_busy_timeout=10000&_txlock=immediate")
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd backend && go test ./internal/db/ -run 'TestCreateAgent|TestSetAgentPassword|TestResetAgentTOTP|TestUseAgentTOTPStep|TestAdjustAgentBalance|TestCreditAgent|TestLedgerSummary|TestCheckAgentBalances' -count=1`
Expected: 编译失败，报 `undefined: db.CreateAgent` 等。

- [ ] **Step 3: 实现 `agent_account.go` 和 `agent_ledger.go`**

- [ ] **Step 4: 跑测试，确认通过；再加上 `-race` 跑一遍 db 包**

Run: `cd backend && go test -race ./internal/db/... -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/db/agent_account.go backend/internal/db/agent_ledger.go backend/internal/db/agent_account_test.go backend/internal/db/agent_ledger_test.go
git commit -m "feat(agent): 代理商账号和只追加的余额流水，调账与入账按外部单号防重复" -- backend/internal/db/agent_account.go backend/internal/db/agent_ledger.go backend/internal/db/agent_account_test.go backend/internal/db/agent_ledger_test.go
```

---

### Task 3: 下单事务和码的归属（数据层）

**Files:**
- Create: `backend/internal/db/agent_order.go`
- Modify: `backend/internal/db/local_cdk.go`：`LocalCDK`、`NewLocalCDK`、`localCDKColumns`、`scanLocalCDK`、`insertLocalCDK`、`ExchangeLocalCDK`、`LoadLocalCDKRows`
- Test: `backend/internal/db/agent_order_test.go`

**Interfaces:**
- Consumes: Task 2 的 `applyAgentBalance`、`InsufficientBalanceError`、`ErrAgentDisabled`；Task 1 的 `AgentProduct`。
- Produces（`package db`）:
  - `NewLocalCDK` 和 `LocalCDK` 各加两个字段：`AgentID int64`、`AgentOrderID int64`。`localCDKColumns` 和 `scanLocalCDK` 读出这两列。
  - `ExchangeLocalCDK`：在同一个事务里，读旧码的 `agent_id`、`agent_order_id`，覆盖到新码上。调用方不用改。
  - `func LoadLocalCDKRowsByAgent(ctx context.Context, agentID int64) ([]LocalCDKRow, error)`：只返回这个代理商的码，派生规则和 `LoadLocalCDKRows` 相同。把现有函数改成内部的 `loadLocalCDKRows(ctx, where string, args ...any)`，两个函数共用。
  - `type SealedLocalCDK struct { ID int64; CodeHash, Sealed string }`
  - `func LocalCDKSealedForAgent(ctx context.Context, agentID int64, ids []int64) ([]SealedLocalCDK, error)`：条件里固定带 `agent_id = ?`，别人的 ID 直接不返回；按 id 升序。
  - `func LocalCDKIDsForAgentOrder(ctx context.Context, agentID, orderID int64) ([]int64, error)`
  - `ErrAgentOrderExists`：`(agent_id, request_no)` 撞上唯一约束时返回。
  - `type AgentOrder struct { ID, AgentID, ProductID int64; ProductCode, Provider, Product, PlanKey, Region, ProductName string; Quantity int; UnitPriceCents, TotalCents int64; RequestNo, Note string; CreatedAt int64 }`
  - `type NewAgentOrder struct { AgentID int64; Product AgentProduct; UnitPriceCents int64; RequestNo, Note, Operator string; Codes []NewLocalCDK }`：数量就是 `len(Codes)`。
  - `func CreateAgentOrder(ctx context.Context, o NewAgentOrder, now time.Time) (AgentOrder, []int64, error)`：
    - 一个事务里依次：插入订单 → `applyAgentBalance(..., requireActive=true)` 扣 `UnitPriceCents × len(Codes)` → 流水带 `order_id` → 逐张插入码（`AgentID`、`AgentOrderID` 由本函数填）→ 提交；
    - 任何一步失败都整体回滚；
    - 返回订单和按顺序排列的码 ID。
  - `func GetAgentOrderByRequestNo(ctx context.Context, agentID int64, requestNo string) (AgentOrder, bool, error)`
  - `func GetAgentOrder(ctx context.Context, agentID, orderID int64) (AgentOrder, error)`：别人的订单返回 `sql.ErrNoRows`。
  - `func ListAgentOrders(ctx context.Context, agentID int64, limit, offset int) ([]AgentOrder, int, error)`：id 降序。

- [ ] **Step 1: 写失败的测试**

```go
func TestCreateAgentOrderAtomic(t *testing.T)            // 余额 1000，单价 300 × 2：成功；余额 400；1 条 purchase 流水 -600，带 order_id；2 张码 agent_id、agent_order_id 正确
func TestCreateAgentOrderInsufficientLeavesNothing(t *testing.T) // 余额 100，单价 300：errors.As 拿到 *InsufficientBalanceError{BalanceCents:100, NeedCents:300}；订单、流水、码都是 0 行
func TestCreateAgentOrderDisabledAgent(t *testing.T)     // ErrAgentDisabled，什么都不写
func TestCreateAgentOrderDuplicateRequestNo(t *testing.T) // 第二次返回 ErrAgentOrderExists；只扣一次
func TestCreateAgentOrderCodeHashCollisionRollsBack(t *testing.T) // 第二张码的 CodeHash 和库里已有的码相同：返回错误；余额、订单、流水都没变
func TestCreateAgentOrderConcurrentOverdraft(t *testing.T) // 文件库（openAgentFileDB），余额 1000，30 个 goroutine 各下 1 张 100：恰好 10 单成功，余额 0，CheckAgentBalances 为空
func TestCreateAgentOrderConcurrentSameRequestNo(t *testing.T) // 文件库，10 个 goroutine 用同一个请求号：1 个成功，其余 ErrAgentOrderExists；只扣一次
func TestExchangeInheritsAgent(t *testing.T)             // 代理商的码被 ExchangeLocalCDK 换出的新码，agent_id、agent_order_id 和旧码一样
func TestLocalCDKSealedForAgentScoped(t *testing.T)      // 传入 A 的 ID 和 B 的 ID，用 A 的 agentID 查：只返回 A 的
func TestLoadLocalCDKRowsByAgent(t *testing.T)
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd backend && go test ./internal/db/ -run 'TestCreateAgentOrder|TestExchangeInheritsAgent|TestLocalCDKSealedForAgent|TestLoadLocalCDKRowsByAgent' -count=1`
Expected: 编译失败，报 `undefined: db.CreateAgentOrder` 等。

- [ ] **Step 3: 实现 `agent_order.go`，修改 `local_cdk.go`**

- [ ] **Step 4: 跑 db 包和 handler 包的全部测试（本站码相关的现有测试要全过）**

Run: `cd backend && go test -race ./internal/db/... ./internal/handler/... -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/db/agent_order.go backend/internal/db/agent_order_test.go backend/internal/db/local_cdk.go
git commit -m "feat(agent): 下单在一个事务里扣款写流水写码，码记下所属代理商，换码继承归属" -- backend/internal/db/agent_order.go backend/internal/db/agent_order_test.go backend/internal/db/local_cdk.go
```

---

### Task 4: `internal/agent` 纯逻辑包

**Files:**
- Create: `backend/internal/agent/totp.go`、`backend/internal/agent/session.go`、`backend/internal/agent/creds.go`
- Test: `backend/internal/agent/totp_test.go`、`backend/internal/agent/session_test.go`、`backend/internal/agent/creds_test.go`

**Interfaces:**
- Produces（`package agent`）:
  - TOTP：
    - `func NewTOTPSecret() ([]byte, error)`：20 字节随机数。
    - `func EncodeSecret(b []byte) string`：base32，不带填充。
    - `func DecodeSecret(s string) ([]byte, error)`
    - `func TOTPStep(t time.Time) int64`：`t.Unix() / 30`。
    - `func TOTPCode(secret []byte, step int64) string`：RFC 6238，HMAC-SHA1，6 位，左边补 0。
    - `func VerifyTOTP(secret []byte, code string, now time.Time, lastStep int64) (step int64, ok bool)`：依次试 `step-1`、`step`、`step+1`，只接受大于 `lastStep` 的时间窗，比较用 `subtle.ConstantTimeCompare`；`code` 不是 6 位数字直接返回 false。
    - `func OtpauthURI(issuer, account string, secret []byte) string`：格式为 `otpauth://totp/<issuer>:<account>?secret=<base32>&issuer=<issuer>&algorithm=SHA1&digits=6&period=30`，`issuer` 和 `account` 做 URL 转义。
  - 会话：
    - 常量 `SessionTTL = 12 * time.Hour`、`PreTTL = 5 * time.Minute`。
    - 常量 `AudSession = "agent"`、`AudPre = "agent-pre"`。
    - 常量 `StageChangePassword = "change_password"`、`StageSetupTOTP = "setup_totp"`、`StageTOTP = "totp"`。
    - `type Claims struct { AgentID int64 `json:"aid"`; SV int64 `json:"sv"`; Stage string `json:"stg,omitempty"`; Nonce string `json:"n,omitempty"`; jwt.RegisteredClaims }`
    - `func SignSession(agentID, sv int64, now time.Time) (token string, expires time.Time, err error)`
    - `func SignPre(agentID, sv int64, stage string, now time.Time) (token, nonce string, err error)`：nonce 是 16 字节随机数的十六进制。
    - `func ParseSession(token string, now time.Time) (Claims, error)`：只接受 HS256 签名、`aud == AudSession`、没过期。
    - `func ParsePre(token string, now time.Time) (Claims, error)`：只接受 `aud == AudPre`。
    - 签名密钥：`hmac.New(sha256.New, []byte(auth.JWTSecret()))` 对 `"aisubproxy/agent-session/v1"` 求 MAC 的结果。
  - 其他：
    - `func GeneratePassword() (string, error)`：16 位，字符集 `ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789`，用 crypto/rand。
    - `func NormalizeUsername(s string) (string, bool)`：去空白，转小写，必须匹配 `^[a-z0-9_-]{3,32}$`。
    - `func ValidProductCode(s string) bool`：`^[a-z0-9-]{2,48}$`。
    - `func DefaultProductCode(plan, region string) string`：`"mw-" + plan 里的 "_" 换成 "-" + "-" + 小写地区`，例如 `("pro_5x", "PH") → "mw-pro-5x-ph"`。
    - `func ValidRequestNo(s string) bool`：`^[A-Za-z0-9_.:-]{1,64}$`。
    - `func FormatCents(c int64) string`：例如 `-1234 → "-12.34"`、`5 → "0.05"`，CSV 用。

- [ ] **Step 1: 写失败的测试**

```go
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	cases := map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037", 20000000000: "353130"}
	for ts, want := range cases {
		if got := agent.TOTPCode(secret, ts/30); got != want {
			t.Fatalf("T=%d got %s want %s", ts, got, want)
		}
	}
}
func TestVerifyTOTPWindowAndReplay(t *testing.T)   // ±1 个时间窗通过并返回对应的 step；±2 拒绝；lastStep 等于匹配的 step 时拒绝；"12345"、"abcdef" 拒绝
func TestOtpauthURIFormat(t *testing.T)
func TestSessionAudienceSeparated(t *testing.T)    // SignPre 的令牌 ParseSession 失败；SignSession 的令牌 ParsePre 失败
func TestSessionNotSignedWithJWTSecret(t *testing.T) // 用原始 JWT_SECRET 签的 HS256（aud=agent）令牌，ParseSession 失败
func TestSessionExpiry(t *testing.T)               // now + 12h + 1s 解析失败
func TestGeneratePasswordAlphabet(t *testing.T)    // 长度 16，不含 0 O o 1 l I
func TestNormalizeUsername(t *testing.T)           // " Alice " → "alice", true；"ab" → false；"a b c" → false；33 位 → false
func TestDefaultProductCode(t *testing.T)
func TestValidRequestNo(t *testing.T)              // "" false；65 位 false；"a b" false；"order-1:2.x_y" true
func TestFormatCents(t *testing.T)
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd backend && go test ./internal/agent/ -count=1`
Expected: 编译失败（包不存在）。

- [ ] **Step 3: 实现三个文件**

- [ ] **Step 4: 跑测试，确认通过**

Run: `cd backend && go test ./internal/agent/ -count=1 && go vet ./internal/agent/`
Expected: PASS，vet 无输出。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/agent
git commit -m "feat(agent): TOTP、代理商会话令牌（派生密钥、独立受众）、登录名与编码校验" -- backend/internal/agent
```

---

### Task 5: 代理商登录、会话中间件和路由骨架

**Files:**
- Create: `backend/internal/handler/agent_session.go`（cookie、中间件、限流计数、可替换的时钟）
- Create: `backend/internal/handler/agent_auth.go`（登录流程、退出、`/me`、改密码）
- Create: `backend/internal/handler/agent_routes.go`
- Create: `backend/internal/handler/agent_env_test.go`（后续任务共用的测试环境）
- Modify: `backend/internal/server/http.go`：在 `api` 组里调用 `handler.RegisterAgentRoutes(api)`，在 `admin` 组里调用 `handler.RegisterAgentAdminRoutes(admin)`
- Test: `backend/internal/handler/agent_auth_test.go`、`backend/internal/server/agent_isolation_test.go`

**Interfaces:**
- Consumes: Task 2 的账号函数；Task 4 的整个 `agent` 包；现有的 `loginAllowed`、`loginFailed`、`loginSucceeded`、`secretbox`、`db.IsWeakPassword`、`db.WriteAudit`、`isSecureRequest`、`newCSRFToken`。
- Produces（`package handler`）:
  - 常量：`agentTokenCookie = "agent_token"`、`agentPreCookie = "agent_pre"`、`agentCSRFCookie = "agent_csrf"`。
  - `var agentNow = time.Now`：测试可以替换。
  - `func AgentAuthMiddleware() gin.HandlerFunc`：
    - 只读 `agent_token` cookie，忽略 Authorization 头；
    - `agent.ParseSession`；
    - 写请求要求 `X-CSRF-Token` 等于 `agent_csrf` cookie，否则 403；
    - `db.GetAgent`：必须是 `active`，并且 `SessionVersion == claims.SV`，否则 401；
    - 通过后 `c.Set("agent_id", id)`、`c.Set("agent_username", username)`。
  - `func agentID(c *gin.Context) int64`、`func agentUsername(c *gin.Context) string`。
  - `func auditAgent(c *gin.Context, action, detail string)`：username 写成 `"agent:" + 登录名`。
  - `func RegisterAgentRoutes(api *gin.RouterGroup)`：建 `api.Group("/agent", LookupGuard())`，挂设计 7.1 里的认证接口和 `GET /me`、`POST /me/password`。后续任务往这个函数里追加路由。
  - `func RegisterAgentAdminRoutes(admin *gin.RouterGroup)`：本任务先是空函数，Task 6 填内容。
- 登录流程（设计 6.1–6.3）的实现要点：
  - **登录**：
    - 登录名用 `agent.NormalizeUsername` 处理；
    - 限流键为 `"agent:" + ClientIP + "|" + 登录名`；
    - 密码用 `bcrypt.CompareHashAndPassword` 比对；
    - 账号停用和密码错误返回同样的 401「登录名或密码错误」；
    - 成功后签发临时凭证，响应 `{"next": stage}`。
  - **临时凭证**：放在 `agent_pre` cookie 里，`Path=/api/v1/agent/auth`，HttpOnly，SameSite=Strict，有效期 5 分钟。每个接口都要核对 stage 和 `session_version`。
  - **动态码错误计数**：进程内的 map，以 nonce 为键，最多 5 次，5 分钟后清掉。
  - **绑定两步验证**：
    - 待绑定的密钥存在进程内的 map 里，以 nonce 为键；同一个 nonce 重复 GET 返回同一个密钥；
    - 确认时用 `secretbox.Seal`，AAD 为 `"agent_totp:" + 代理商 ID`，加密后写库；
    - 主密钥不可用时返回 503 `maintenance`。
  - **完成登录**：`TouchAgentLogin`，`agent.SignSession`；`agent_token` 的 Path 为 `/api/v1/agent`，`agent_csrf` 的 Path 为 `/`，有效期都是 12 小时；清掉 `agent_pre`。
  - **改密码**：至少 12 位，不能被 `IsWeakPassword` 判为弱口令，不能和旧密码相同；改完 `session_version + 1`，并清掉会话 cookie。
  - **审计**：`agent_login`（完成登录）、`agent_login_failed`（密码或动态码错误）、`agent_password_change`、`agent_totp_bind`，detail 里不写密码、密钥、动态码。
- 测试环境（`agent_env_test.go`，后续任务共用）：
  - `type agentEnv struct { *localEnv; r *gin.Engine; clock time.Time }`
  - `func newAgentEnv(t *testing.T) *agentEnv`：调 `newLocalEnv(t)`，替换 `agentNow`，让它返回 `e.clock`。新建一个 gin.Engine，挂上 `RegisterAgentRoutes(r.Group("/api/v1"))` 和一个假的管理员组：组里先用中间件 `c.Set("username", "admin"); c.Set("is_admin", true)`，再 `RegisterAgentAdminRoutes(...)`，挂在 `/api/v1/admin` 下。
  - `func (e *agentEnv) seedTier(name string) int64`
  - `func (e *agentEnv) seedAgent(username string, tierID int64) (id int64, password string, secret []byte)`：直接写库，密码已改过、两步验证已绑定。
  - `type agentSession struct { cookies []*http.Cookie; csrf string }`
  - `func (e *agentEnv) login(username, password string, secret []byte) *agentSession`：每次先把 `e.clock` 推后 30 秒，再走「密码 → 动态码」两步。
  - `func (e *agentEnv) agentDo(s *agentSession, method, path, body string) *httptest.ResponseRecorder`：写请求自动带 `X-CSRF-Token`。
  - `func (e *agentEnv) adminDo(method, path, body string, headers map[string]string) *httptest.ResponseRecorder`

- [ ] **Step 1: 写失败的测试**

```go
func TestAgentLoginFullFlowFirstTime(t *testing.T) // 新建号（must_change、未绑定）：login → next=change_password；改密码 → next=setup_totp；GET setup 拿 secret；confirm → 200 并设置 agent_token；之后 GET /me 200
func TestAgentLoginCookiesPathAndFlags(t *testing.T) // agent_token：Path=/api/v1/agent、HttpOnly、SameSite=Strict；agent_csrf：Path=/、不是 HttpOnly
func TestAgentLoginUsernameCaseInsensitive(t *testing.T) // "ALICE" 能登录 alice
func TestAgentLoginDisabledSameError(t *testing.T)  // 停用账号和密码错误的响应体完全一样
func TestAgentLoginRateLimited(t *testing.T)        // 错 5 次后第 6 次 429，即使密码正确
func TestAgentPreTokenCannotAccessMe(t *testing.T)  // 只有 agent_pre 时 GET /me 401
func TestAgentPreStageEnforced(t *testing.T)        // stage=change_password 的凭证调 /auth/totp → 401
func TestAgentTOTPReplayRejected(t *testing.T)      // 同一个动态码在同一个时间窗里第二次登录 → 401
func TestAgentTOTPFiveFailuresKillPre(t *testing.T) // 错 5 次后，正确的码也被拒绝，要重新输密码
func TestAgentSessionRevokedOnDisable(t *testing.T) // 登录后管理员停用 → GET /me 401
func TestAgentSessionRevokedOnPasswordChange(t *testing.T)
func TestAgentWriteNeedsCSRF(t *testing.T)          // POST /me/password 不带头 → 403
func TestAgentBearerIgnored(t *testing.T)           // 用 Authorization: Bearer <有效会话令牌>、不带 cookie → 401
func TestAgentAuthNoSecretsInLogsOrAudit(t *testing.T) // 整个首登流程跑完：日志输出和 admin_audit_logs 里不出现密码、base32 密钥、动态码
```

`internal/server/agent_isolation_test.go`：

```go
func TestAgentTokenRejectedByAdminMiddleware(t *testing.T) // agent.SignSession 的令牌放进 auth_token cookie 和 Bearer 头，访问挂了 JWTAuthMiddleware+AdminAuthMiddleware 的路由 → 401
func TestAdminTokenRejectedByAgentMiddleware(t *testing.T) // 用 JWTSecret 签的 CustomClaims{IsAdmin:true} 放进 agent_token → 401
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd backend && go test ./internal/handler/ -run 'TestAgent' -count=1 && go test ./internal/server/ -run 'Token' -count=1`
Expected: 编译失败，报 `undefined: RegisterAgentRoutes` 等。

- [ ] **Step 3: 实现 `agent_session.go`、`agent_auth.go`、`agent_routes.go`、`agent_env_test.go`，修改 `http.go`**

- [ ] **Step 4: 跑测试，确认通过**

Run: `cd backend && go test -race ./internal/handler/ ./internal/server/ -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/handler/agent_session.go backend/internal/handler/agent_auth.go backend/internal/handler/agent_routes.go backend/internal/handler/agent_env_test.go backend/internal/handler/agent_auth_test.go backend/internal/server/agent_isolation_test.go backend/internal/server/http.go
git commit -m "feat(agent): 代理商登录（密码 + 强制 TOTP）、会话与管理员隔离" -- backend/internal/handler/agent_session.go backend/internal/handler/agent_auth.go backend/internal/handler/agent_routes.go backend/internal/handler/agent_env_test.go backend/internal/handler/agent_auth_test.go backend/internal/server/agent_isolation_test.go backend/internal/server/http.go
```

---

### Task 6: 管理员接口（代理商、等级、商品、价格）

**Files:**
- Create: `backend/internal/handler/agent_admin.go`（代理商管理、调账、覆盖价、对账总览）
- Create: `backend/internal/handler/agent_pricing_admin.go`（等级、商品、价格矩阵）
- Modify: `backend/internal/handler/agent_routes.go`（填 `RegisterAgentAdminRoutes`）
- Modify: `backend/internal/handler/local_cdk_admin.go`（`AdminListLocalCDKs`：支持 `agent_id` 查询参数，每行加 `agent_id` 和 `agent_username`）
- Test: `backend/internal/handler/agent_admin_test.go`

**Interfaces:**
- Consumes: Task 1–3 的数据层；Task 4 的 `GeneratePassword`、`NormalizeUsername`、`ValidProductCode`、`DefaultProductCode`；现有的 `issueIdempotencyKey(c)`、`auditAdmin`、`adminName`、`payengine.SupportedPlanKeys()`、`payengine.Regions`、`provider.Supports`。
- Produces：设计 7.2 列出的管理员路由，`/agents/:id/ledger` 和 `/agents/:id/ledger/export` 除外（这两个和代理商自己的流水共用代码，放在 Task 8）。另外加一个：
  - `GET /agent-products/options`：返回 MarsWallex 每个已支持的「套餐 × 地区」组合 `{plan, region, default_code, exists}`。新增商品时从这里选。
  - 裁定：设计 8.2 要求「从 MarsWallex 已支持的套餐 × 地区里选」，前端拿不到这份清单，所以加这个只读接口。
- 响应约定：
  - 建号和重置密码的响应里带 `password`，响应头 `Cache-Control: no-store`；审计里不写密码。
  - 调账：缺 `Idempotency-Key` 时 400，由 `issueIdempotencyKey` 输出；余额不足时 409 `insufficient_balance`，附 `balance_cents`；重放时 200，附 `replayed: true`；同一个键对应的代理商或金额不一样时 409。
  - 商品：`provider` 固定为 `marswallex`，`product` 固定为 `gpt`；套餐和地区必须通过 `provider.Supports` 和 `payengine.Regions` 检查；编码必须通过 `ValidProductCode`。
  - 价格矩阵 `PUT /agent-prices`：请求体为 `{"cells":[{"tier_id","product_id","price_cents"}]}`。
  - 覆盖价 `PUT /agents/:id/overrides`：请求体为 `{"cells":[{"product_id","price_cents"}]}`。
  - 对账总览 `GET /agent-summary`：返回 `AgentTotalsSince` 的三项，以及 `site_settings.agent_reconcile_last` 解析后的结果（没有就是 null）。
  - 全部操作按设计 6.6 的 action 名写审计。

- [ ] **Step 1: 写失败的测试**

```go
func TestAdminCreateAgentReturnsPasswordOnce(t *testing.T) // 200，带 16 位 password，no-store；GET /agents/:id 不带 password；审计 detail 不含该密码
func TestAdminCreateAgentDuplicateUsername(t *testing.T)   // 先建 alice，再建 "Alice" → 409
func TestAdminResetPasswordForcesChange(t *testing.T)      // 重置后旧会话 401；用新密码登录 next=change_password
func TestAdminResetTOTPForcesRebind(t *testing.T)          // 重置后登录 next=setup_totp
func TestAdminAdjustIdempotent(t *testing.T)               // 同一个 Idempotency-Key 两次：余额只变一次，第二次 replayed=true
func TestAdminAdjustRequiresKeyAndNote(t *testing.T)
func TestAdminAdjustOverdraft(t *testing.T)                // 409 insufficient_balance
func TestAdminProductValidation(t *testing.T)              // 不支持的套餐、地区、编码 → 400；重复 → 409；PUT 改不了 code
func TestAdminProductOptions(t *testing.T)                 // 包含 plus/PH，default_code=mw-plus-ph；建好后 exists=true
func TestAdminPriceMatrixAndOverrides(t *testing.T)
func TestAdminDeleteTierInUse(t *testing.T)                // 409
func TestAdminLocalCDKListAgentFilter(t *testing.T)        // ?agent_id=A 只返回 A 的码，每行 agent_username=A 的登录名
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd backend && go test ./internal/handler/ -run 'TestAdmin(CreateAgent|Reset|Adjust|Product|PriceMatrix|DeleteTier|LocalCDKListAgent)' -count=1`
Expected: FAIL（路由不存在，404）。

- [ ] **Step 3: 实现两个处理文件，填 `RegisterAgentAdminRoutes`，修改 `AdminListLocalCDKs`**

- [ ] **Step 4: 跑测试，确认通过**

Run: `cd backend && go test -race ./internal/handler/ -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/handler/agent_admin.go backend/internal/handler/agent_pricing_admin.go backend/internal/handler/agent_admin_test.go backend/internal/handler/agent_routes.go backend/internal/handler/local_cdk_admin.go
git commit -m "feat(agent): 管理员建号、重置、调账、等级与批发价管理，本站码列表按代理商筛选" -- backend/internal/handler/agent_admin.go backend/internal/handler/agent_pricing_admin.go backend/internal/handler/agent_admin_test.go backend/internal/handler/agent_routes.go backend/internal/handler/local_cdk_admin.go
```

---

### Task 7: 代理商下单和订单接口

**Files:**
- Create: `backend/internal/handler/agent_order.go`
- Modify: `backend/internal/handler/agent_routes.go`（加 `GET /products`、`POST /orders`、`GET /orders`、`GET /orders/:id`、`POST /orders/:id/export`，都挂在 `AgentAuthMiddleware` 之后）
- Test: `backend/internal/handler/agent_order_test.go`

**Interfaces:**
- Consumes: Task 3 的 `CreateAgentOrder`、`GetAgentOrderByRequestNo`、`LocalCDKIDsForAgentOrder`、`LocalCDKSealedForAgent`；Task 1 的 `ListAgentPurchasable`、`GetAgentProductByCode`、`AgentPriceFor`；现有的 `checkCardSupply`、`provider.Get`、`provider.AsCardSupply`、`provider.GenerateCode`、`sealLocalCDKCode`、`localCDKCodeAAD`、`secretbox`、`opsAlertDaily`。
- Produces:
  - `type exportedCode struct { ID int64 `json:"id"`; Code string `json:"code"`; Unavailable bool `json:"unavailable,omitempty"` }`
  - `func openAgentCodes(ctx context.Context, agentID int64, ids []int64) []exportedCode`：
    - 只调 `LocalCDKSealedForAgent`；
    - 逐张解密并核对哈希，失败的标 `Unavailable`；
    - 有失败时发一次告警：`opsAlertDaily("agent-code-unseal", …)`，只写码的 ID。
    - Task 8 复用。
  - `func agentOrderAllowed(agentID int64, now time.Time) bool`：每个代理商每分钟 30 次，进程内固定窗口；另有 `resetAgentOrderRate()` 给测试用。
- 下单 `POST /orders` 的请求体：`{"product_code","quantity","note","request_no","expected_unit_price_cents"}`。处理顺序：
  1. 限流，超了返回 429 `rate_limited`。
  2. 校验参数，错了一律返回 400，并且不碰余额：`quantity` 为 1–200 的整数；`note` ≤ 200 字；`request_no` 通过 `ValidRequestNo`；`product_code` 非空。
  3. `secretbox.LoadKey()` 出错时，用 `opsAlertDaily("agent-order-nokey", …)` 告警，返回 503 `maintenance`。
  4. **查请求号**（Review Focus 1）：已有订单时，商品编码和数量都一致就重放：200，`replayed: true`，码由 `openAgentCodes` 解密取回；不一致就返回 409 `request_no_conflict`。
  5. 商品检查：商品存在且已上架、取得到价格，否则 404 `product_unavailable`；价格和 `expected_unit_price_cents` 不一致时返回 409 `price_changed`，附当前价（Review Focus 2）；`checkCardSupply(..., false).Readiness.OK` 为假时返回 409 `product_not_ready`。
  6. 生成码，逐张用 `sealLocalCDKCode` 加密，任何一张得到空串就返回 503 `maintenance`。`IssuedBy` 为 `"agent:" + 登录名`，`Note` 为订单备注，`Product`、`PlanKey`、`Region`、`Provider` 都取自商品。
  7. `CreateAgentOrder`：
     - 返回 `ErrAgentOrderExists` 时，回到第 4 步；
     - 返回 `*InsufficientBalanceError` 时，409 `insufficient_balance`，附 `shortfall_cents`；
     - 返回 `ErrAgentDisabled` 时，401；
     - 其他错误返回 503 `busy`。
  8. 成功：响应 `{order, codes:[exportedCode]}`，响应头 `Cache-Control: no-store`；写审计 `agent_order`，内容为商品编码、数量、总价、订单号。
- 错误响应统一为 `{"error": "<编号>", "message": "<中文提示>"}`，编号和提示文字按设计第 9 节。
- `GET /products`：`ListAgentPurchasable`，每项加 `ready`（`checkCardSupply` 的结果）。
- `POST /orders/:id/export`：订单必须属于当前代理商，否则 404；响应 `{codes}`，no-store；写审计 `agent_codes_export`，内容为 `order=<id> count=<n>`。

- [ ] **Step 1: 写失败的测试**

`newAgentEnv` 之后调 `e.seedRealPayment()`，让 plus/PH 能出码。建商品 `mw-plus-ph`，等级价 500，调账加 1000 美分。

```go
func TestAgentOrderSuccess(t *testing.T)               // 下 2 张：200；codes 两张，以 MW- 开头；余额 0；用 GetLocalCDKByCode 能查到，agent_id 正确；审计里不含码
func TestAgentOrderReplaySameRequestNo(t *testing.T)   // 同一个请求再发：200 replayed=true，码和第一次相同，余额不变
func TestAgentOrderReplayAfterProductDisabled(t *testing.T) // 第一次成功后下架商品、改价，再发同一个请求：仍然 200 replayed，返回同一批码
func TestAgentOrderRequestNoConflict(t *testing.T)     // 同一个请求号、数量改成 1 → 409 request_no_conflict
func TestAgentOrderPriceChanged(t *testing.T)          // expected 500，当前价 600 → 409 price_changed；余额不变
func TestAgentOrderInsufficient(t *testing.T)          // 409，shortfall_cents 正确；没有订单
func TestAgentOrderBoundaries(t *testing.T)            // 数量 0、201、1.5，备注 201 字，请求号 "a b"、65 位 → 都是 400，余额和流水不变
func TestAgentOrderNotReady(t *testing.T)              // 不调 seedRealPayment → 409 product_not_ready
func TestAgentOrderNoMasterKey(t *testing.T)           // t.Setenv("APP_MASTER_KEY", "") → 503 maintenance；产生 1 条告警
func TestAgentOrderUnpricedProduct(t *testing.T)       // 404 product_unavailable
func TestAgentOrderConcurrentSameRequestNo(t *testing.T) // 文件库（参照 TestLocalCDKRedeemOnce 的打开方式，DSN 带 _txlock=immediate）：8 个 goroutine 同时发同一个请求，全部 200，拿到的码完全相同，只扣一次
func TestAgentOrderRateLimit(t *testing.T)             // 第 31 次 → 429
func TestAgentOrderExportScoped(t *testing.T)          // A 导出 B 的订单 → 404；A 导出自己的 → 码和下单时一样
func TestAgentOrderListScoped(t *testing.T)            // GET /orders 只有自己的
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd backend && go test ./internal/handler/ -run 'TestAgentOrder' -count=1`
Expected: FAIL（路由不存在，404）。

- [ ] **Step 3: 实现 `agent_order.go`，注册路由**

- [ ] **Step 4: 跑测试，确认通过**

Run: `cd backend && go test -race ./internal/handler/ -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add backend/internal/handler/agent_order.go backend/internal/handler/agent_order_test.go backend/internal/handler/agent_routes.go
git commit -m "feat(agent): 代理商下单取码，请求号重放先于可售检查，价格变化拒单，订单导出" -- backend/internal/handler/agent_order.go backend/internal/handler/agent_order_test.go backend/internal/handler/agent_routes.go
```

---

### Task 8: 我的码、概览、余额流水、每天核对

**Files:**
- Create: `backend/internal/handler/agent_codes.go`（`GET /codes`、`POST /codes/export`、`GET /overview`）
- Create: `backend/internal/handler/agent_ledger.go`（代理商的 `GET /ledger`、`GET /ledger/export`，和管理员的 `GET /agents/:id/ledger`、`GET /agents/:id/ledger/export` 共用同一套查询和 CSV 代码；管理员这两个路由在本任务注册进 `RegisterAgentAdminRoutes`）
- Create: `backend/internal/handler/agent_reconcile.go`
- Modify: `backend/internal/handler/agent_routes.go`、`backend/internal/handler/ops_maintenance.go`（`runOpsMaintenance` 里加 `reconcileAgentBalances(now)`）
- Test: `backend/internal/handler/agent_codes_test.go`、`backend/internal/handler/agent_reconcile_test.go`

**Interfaces:**
- Consumes: Task 3 的 `LoadLocalCDKRowsByAgent`、`ListAgentOrders`；Task 7 的 `openAgentCodes`；Task 2 的 `ListAgentLedger`、`AgentLedgerSummary`、`CheckAgentBalances`；现有的 `maskEmailForAdmin`、`beijingZone`、`opsAlertDaily`、`db.GetSetting(key string) (string, error)`、`db.SetSetting(key, value string) error`。
- Produces:
  - 码的状态对外用 5 个值：`unused`、`processing`、`used`、`failed`、`disabled`。由 `DeriveLocalCDK` 的结果映射：reserved → processing，consumed → used，其余同名。中文标签由前端负责。
  - `GET /codes` 的查询参数：`product`（商品编码）、`status`（上面 5 个值之一）、`order_id`、`from`、`to`（Unix 秒，`from <= created_at < to`）、`page`（从 1 开始）、`page_size`（默认 50，最大 200）。
    - 每行返回：`{id, code_prefix, product_code, plan, region, status, reason, created_at, order_id, note, email, used_at}`。
    - `email` 和 `used_at` 只在 `status=used` 时有：`email` 为 `maskEmailForAdmin(Latest.AccountEmail)`，`used_at` 为 `Latest.FinishedAt`。
    - `product_code` 从这个代理商的订单里查。
  - `POST /codes/export`：请求体为 `{"ids":[...]}`，或者 `{"filter":{和 GET /codes 一样的筛选项}}`，两者只能二选一。
    - 结果超过 10000 张时返回 400「一次最多导出 10000 张，请缩小筛选范围」。
    - 每项返回 `exportedCode` 加上 `GET /codes` 一行里的字段。
    - 写审计 `agent_codes_export`，记张数和筛选条件，不记码。
  - `GET /overview`：`{balance_cents, month_order_cents, status_counts:{unused,processing,used,failed,disabled}, recent_orders:[最近 5 笔]}`，月份按北京时间。
  - `GET /ledger`：查询参数 `from`、`to`，默认最近 30 天，`to - from` 最多 366 天，超了返回 400；还有 `page`、`page_size`。响应为 `{items, total, summary}`。
  - `GET /ledger/export`：`text/csv; charset=utf-8`，开头带 BOM，`Content-Disposition: attachment; filename="ledger-<登录名>-<北京日期>.csv"`。
    - 列：`时间,类型,金额,变动后余额,订单号,备注`；
    - 类型的中文：下单、调账、充值；
    - 金额用 `agent.FormatCents`。
  - `func reconcileAgentBalances(now time.Time)`：
    - 读 `agent_reconcile_last`，如果其中的北京日期等于今天就跳过；
    - 否则调 `CheckAgentBalances`，把 `{"at":<Unix 秒>,"ok":bool,"mismatches":[{"agent_id","diff_cents"}]}` 写回 `agent_reconcile_last`；
    - 有对不上的就 `opsAlertDaily("agent-reconcile", "代理商余额与流水对不上：…")`，只写代理商 ID 和差额。

- [ ] **Step 1: 写失败的测试**

```go
func TestAgentCodesListScopedAndMapped(t *testing.T)   // 只返回自己的码；用 insertOrder 造一张成功单后，这张码 status=used，email 是打码的，used_at 有值
func TestAgentCodesFilters(t *testing.T)               // 按 product、status、order_id、时间筛选
func TestAgentCodesExportByIDsScoped(t *testing.T)     // ids 里混进 B 的码 → 只返回 A 的；码正确
func TestAgentCodesExportUnavailable(t *testing.T)     // 直接 SQL 把一张码的 code_sealed 改坏 → 这张 unavailable=true、code 为空，其余正常；有 1 条告警，告警里不含码
func TestAgentCodesExportLimit(t *testing.T)           // 把上限常量改小（agentExportMax 设为可在测试里替换的变量）验证 400
func TestAgentOverviewCounts(t *testing.T)
func TestAgentLedgerSummaryAndRange(t *testing.T)      // 期初、期末、收入、支出；范围超过 366 天 → 400
func TestAgentLedgerExportCSV(t *testing.T)            // BOM、表头、类型中文、金额 "-5.00"
func TestAdminAgentLedgerSharesQuery(t *testing.T)     // 管理员 GET /agents/:id/ledger 和代理商自己看到的条目、汇总一致；CSV 也能导出
func TestReconcileRunsOncePerDayAndAlerts(t *testing.T) // 改坏余额 → 1 条告警，agent_reconcile_last.ok=false；同一个北京日再跑 → 不再查（告警数不变）；换到下一天再跑 → 重新核对
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd backend && go test ./internal/handler/ -run 'TestAgentCodes|TestAgentOverview|TestAgentLedger|TestReconcile' -count=1`
Expected: FAIL

- [ ] **Step 3: 实现三个文件，注册路由，接上 `runOpsMaintenance`**

- [ ] **Step 4: 跑后端全部门禁**

Run: `cd backend && gofmt -l ./internal ./cmd && go vet ./... && go test -race ./internal/db/... ./internal/agent/... ./internal/handler/... ./internal/server/... -count=1`
Expected: gofmt 无输出，vet 无输出，测试全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add backend/internal/handler/agent_codes.go backend/internal/handler/agent_ledger.go backend/internal/handler/agent_reconcile.go backend/internal/handler/agent_codes_test.go backend/internal/handler/agent_reconcile_test.go backend/internal/handler/agent_routes.go backend/internal/handler/ops_maintenance.go
git commit -m "feat(agent): 我的码与导出、概览、余额流水与 CSV、每天核对余额和流水" -- backend/internal/handler/agent_codes.go backend/internal/handler/agent_ledger.go backend/internal/handler/agent_reconcile.go backend/internal/handler/agent_codes_test.go backend/internal/handler/agent_reconcile_test.go backend/internal/handler/agent_routes.go backend/internal/handler/ops_maintenance.go
```

---

### Task 9: 前端基础（请求、金额、导出、路由、布局、登录页）

**Files:**
- Modify: `frontend/package.json`、`frontend/package-lock.json`（`npm install qrcode@^1.5.4` 和 `npm install -D @types/qrcode@^1.5.5`）
- Create: `frontend/src/lib/agentMoney.ts`、`frontend/src/lib/agentApi.ts`、`frontend/src/lib/agentExport.ts`
- Create: `frontend/src/layouts/AgentLayout.vue`、`frontend/src/views/agent/AgentLogin.vue`
- Modify: `frontend/src/router/index.ts`（加 `/agent` 路由组和守卫）
- Test: `frontend/src/lib/__tests__/agentMoney.test.ts`、`frontend/src/lib/__tests__/agentApi.test.ts`、`frontend/src/lib/__tests__/agentExport.test.ts`、`frontend/src/views/__tests__/agent.spec.ts`

**Interfaces:**
- Consumes: Task 5–8 的接口和错误编号。
- Produces:
  - `agentMoney.ts`：
    - `fmtUsd(cents: number): string`：用整数运算，`123456 → "$1,234.56"`，`-500 → "-$5.00"`，`0 → "$0.00"`。
    - `parseSignedUsd(input: string): { ok: true; cents: number } | { ok: false; error: string }`：允许负号，不能为 0，最多两位小数。调账用。
  - `agentApi.ts`：
    - `agentApi<T = any>(path: string, method = 'GET', body?: unknown): Promise<{ ok: boolean; status: number; data: T }>`：请求 `/api/v1/agent` + path，`credentials: 'include'`；写请求从 `agent_csrf` cookie 取值放进 `X-CSRF-Token`。
    - 收到 401 且不是 `/auth/*` 接口时，调用 `onAgentUnauthorized()`。默认行为是 `location.assign('/agent/login')`，可以用 `setAgentUnauthorizedHandler(fn)` 替换。
    - `AGENT_ERROR_TEXT: Record<string, string>`：设计第 9 节的错误编号 → 中文提示。
    - `agentErrorText(data: any): string`：先用 `data.message`，没有再查编号表，最后用「操作失败，请稍后再试」。`insufficient_balance` 时拼上「还差 $x.xx」。
  - `agentExport.ts`：
    - `type ExportedCode = { id: number; code: string; unavailable?: boolean; product_code?: string; plan?: string; region?: string; status?: string; created_at?: number; order_id?: number; note?: string }`
    - `codesTxt(items: ExportedCode[]): { text: string; skipped: number }`：一行一张码，跳过 `unavailable` 的并计数，最后一行后面带换行。
    - `codesCsv(items: ExportedCode[]): string`：开头带 `﻿`；表头 `code,product_code,plan,region,status,purchased_at,order_id,note`；`purchased_at` 用 `fmtBeijing`；`unavailable` 的那张，code 列写「无法取回，请联系管理员」；字段按 CSV 规则转义。
    - `downloadText(name: string, text: string, mime: string): void`
  - 路由：
    - `/agent/login` 用 `AgentLogin.vue`；
    - `/agent` 用 `AgentLayout.vue`，`meta.requiresAgent = true`，子路由 `''`、`order`、`codes`、`orders`、`orders/:id`、`ledger`、`account`。Task 10 填页面，本任务先指向占位组件。
    - 守卫：`requiresAgent` 时，每次页面加载用 `GET /api/v1/agent/me` 确认一次，401 就跳 `/agent/login?redirect=`。不读写管理员的 `authStore`。
  - `AgentLogin.vue`：
    - 按后端返回的 `next` 依次显示：密码 → 改密码 → 绑定两步验证 → 动态码。
    - 绑定页用 `qrcode` 的 `toDataURL(otpauth_uri)` 在浏览器本地生成二维码，旁边显示 base32 密钥。
    - 登录成功后跳到 `redirect`，没有就跳 `/agent`。

- [ ] **Step 1: 写失败的测试**

```ts
// agentMoney.test.ts
expect(fmtUsd(123456)).toBe('$1,234.56'); expect(fmtUsd(-500)).toBe('-$5.00'); expect(fmtUsd(5)).toBe('$0.05')
expect(parseSignedUsd('-12.5')).toEqual({ ok: true, cents: -1250 }); expect(parseSignedUsd('0').ok).toBe(false); expect(parseSignedUsd('1.234').ok).toBe(false)
// agentApi.test.ts：用 vi.stubGlobal 替换 fetch 和 document.cookie
// - POST 时带 X-CSRF-Token=agent_csrf 的值，GET 时不带
// - 401（/me）调用替换后的处理函数；401（/auth/login）不调用
// - agentErrorText({error:'insufficient_balance', shortfall_cents: 250}) 包含 '$2.50'
// agentExport.test.ts
// - codesTxt([{id:1,code:'A'},{id:2,code:'',unavailable:true}]) → { text: 'A\n', skipped: 1 }
// - codesCsv 以 '﻿' 开头，表头正确；note 含逗号和引号时正确转义；unavailable 行的 code 列是「无法取回，请联系管理员」
// agent.spec.ts（沿用 home.spec.ts 的 SSR 渲染写法）：AgentLogin 首屏有「登录名」「密码」输入框，没有英文文案键
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd frontend && npx vitest run src/lib/__tests__/agentMoney.test.ts src/lib/__tests__/agentApi.test.ts src/lib/__tests__/agentExport.test.ts src/views/__tests__/agent.spec.ts`
Expected: FAIL（模块不存在）。

- [ ] **Step 3: 装依赖，实现三个 lib 文件、布局、登录页、路由**

- [ ] **Step 4: 跑前端门禁**

Run: `cd frontend && npx vitest run && npm run build`
Expected: 全部测试通过，build 成功。

- [ ] **Step 5: 提交**

```bash
git add frontend/package.json frontend/package-lock.json frontend/src/lib/agentMoney.ts frontend/src/lib/agentApi.ts frontend/src/lib/agentExport.ts frontend/src/layouts/AgentLayout.vue frontend/src/views/agent/AgentLogin.vue frontend/src/router/index.ts frontend/src/lib/__tests__/agentMoney.test.ts frontend/src/lib/__tests__/agentApi.test.ts frontend/src/lib/__tests__/agentExport.test.ts frontend/src/views/__tests__/agent.spec.ts
git commit -m "feat(agent): 代理商前端基础：请求封装、金额与导出、路由守卫、布局和登录页" -- frontend/package.json frontend/package-lock.json frontend/src/lib/agentMoney.ts frontend/src/lib/agentApi.ts frontend/src/lib/agentExport.ts frontend/src/layouts/AgentLayout.vue frontend/src/views/agent/AgentLogin.vue frontend/src/router/index.ts frontend/src/lib/__tests__/agentMoney.test.ts frontend/src/lib/__tests__/agentApi.test.ts frontend/src/lib/__tests__/agentExport.test.ts frontend/src/views/__tests__/agent.spec.ts
```

---

### Task 10: 代理商后台页面

**Files:**
- Create: `frontend/src/lib/agentViews.ts`
- Create: `frontend/src/views/agent/AgentOverview.vue`、`AgentOrder.vue`、`AgentCodes.vue`、`AgentOrders.vue`、`AgentOrderDetail.vue`、`AgentLedger.vue`、`AgentAccount.vue`
- Modify: `frontend/src/router/index.ts`（把占位组件换成真实页面）
- Test: `frontend/src/lib/__tests__/agentViews.test.ts`，以及在 `frontend/src/views/__tests__/agent.spec.ts` 里追加用例

**Interfaces:**
- Consumes: Task 9 的 `agentApi`、`agentErrorText`、`fmtUsd`、`codesTxt`、`codesCsv`、`downloadText`；现有的 `IdempotencyKeeper`（`lib/idempotency.ts`）、`fmtBeijing`、`copyToClipboard`。
- Produces（`agentViews.ts`）:
  - `CODE_STATUS_LABELS = { unused: '未使用', processing: '处理中', used: '已开通', failed: '失败', disabled: '已禁用' }`
  - `LEDGER_KIND_LABELS = { purchase: '下单', adjust: '调账', topup: '充值' }`
  - `orderPreview(unitCents: number, qty: number, balanceCents: number): { totalCents: number; afterCents: number; enough: boolean }`
  - `validQuantity(input: unknown): number | null`：只接受 1–200 的整数。
  - `beijingRange(fromDate: string, toDate: string): { from: number; to: number } | null`：`YYYY-MM-DD` 按北京时间换成 Unix 秒，`to` 取结束那天的次日 0 点；起止颠倒返回 null。
- 页面要求（按设计 8.1）：
  - **下单页**：用 `IdempotencyKeeper.keyFor({product_code, quantity, note})` 作请求号，成功后调 `done()`。提交时带 `expected_unit_price_cents`。确认框写明总价和下单后余额。结果区有「复制全部」和「下载 .txt」。收到 `price_changed` 时提示并刷新商品列表。
  - **我的码**：筛选项为商品、状态、订单号、日期范围。可以勾选导出，也可以按当前筛选全部导出，格式 .txt 或 .csv。导出 .txt 有跳过的码时，提示「有 N 张无法取回，请联系管理员」。
  - **订单详情**：「重新导出这笔订单的码」。
  - **余额流水**：日期范围默认最近 30 天，顶部四个汇总数，「导出 CSV」直接打开 `/api/v1/agent/ledger/export?from=&to=`。
  - **账号**：改密码，填当前密码、新密码、确认新密码、动态码；成功后跳登录页。

- [ ] **Step 1: 写失败的测试**

```ts
expect(orderPreview(500, 3, 1000)).toEqual({ totalCents: 1500, afterCents: -500, enough: false })
expect(validQuantity('3')).toBe(3); expect(validQuantity(0)).toBeNull(); expect(validQuantity(201)).toBeNull(); expect(validQuantity(1.5)).toBeNull()
expect(beijingRange('2026-11-01', '2026-11-04')).toEqual({ from: 1793462400, to: 1793808000 })
expect(beijingRange('2026-11-05', '2026-11-04')).toBeNull()
// agent.spec.ts 追加：每个页面 SSR 渲染不报错，正文里没有 "agent." 之类的文案键
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd frontend && npx vitest run src/lib/__tests__/agentViews.test.ts src/views/__tests__/agent.spec.ts`
Expected: FAIL

- [ ] **Step 3: 实现 `agentViews.ts` 和 7 个页面，更新路由**

- [ ] **Step 4: 跑前端门禁**

Run: `cd frontend && npx vitest run && npm run build`
Expected: 全部通过。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/lib/agentViews.ts frontend/src/lib/__tests__/agentViews.test.ts frontend/src/views/agent frontend/src/views/__tests__/agent.spec.ts frontend/src/router/index.ts
git commit -m "feat(agent): 代理商后台概览、下单、我的码、订单、余额流水、账号页面" -- frontend/src/lib/agentViews.ts frontend/src/lib/__tests__/agentViews.test.ts frontend/src/views/agent frontend/src/views/__tests__/agent.spec.ts frontend/src/router/index.ts
```

---

### Task 11: 管理员后台页面、本站码列表的代理商列、CHANGELOG

**Files:**
- Create: `frontend/src/lib/agentAdmin.ts`
- Create: `frontend/src/views/admin/AgentList.vue`、`AgentDetail.vue`、`AgentPricing.vue`
- Modify: `frontend/src/router/index.ts`：在 `/ops` 下加三条子路由。
  - `agents`：`meta: { title: '代理商', group: '代理商', icon: 'User' }`
  - `agents/:id`：不设 title，不进侧栏
  - `agent-pricing`：`meta: { title: '批发商品与价格', group: '代理商', icon: 'PriceTag' }`
- Modify: `frontend/src/views/admin/components/LocalCdkPanel.vue`：加「代理商」列和按代理商筛选；`AgentDetail.vue` 复用这个组件，或者直接调 `/cdks?agent_id=`。
- Modify: `CHANGELOG.md`：写进「未发布」一节，分新增、变更、升级注意三类。
- Test: `frontend/src/lib/__tests__/agentAdmin.test.ts`、`frontend/src/layouts/__tests__/`（在现有的侧栏测试里追加，或者新建 `adminNav.agent.test.ts`）

**Interfaces:**
- Consumes: Task 6 的管理员接口；现有的 `adminApi`（`lib/provider.ts`）、`IdempotencyKeeper`、`buildNavItems`、`groupNavItems`；Task 9 的 `fmtUsd`、`parseSignedUsd`。
- Produces（`agentAdmin.ts`）:
  - `type PriceCell = { tier_id: number; product_id: number; price_cents: number }`
  - `matrixChanges(original: PriceCell[], edited: Record<string, string>): { cells: PriceCell[]; errors: string[] }`
    - `edited` 的键为 `"<tier_id>:<product_id>"`，值为美元文本；
    - 空串表示删除，提交时 `price_cents = 0`；
    - 金额校验用 `usdToCents`；
    - 只返回和 `original` 不同的格子。
  - `overrideChanges(original: {product_id: number; price_cents: number}[], edited: Record<string, string>)`：同上，键为 `product_id`。
- 页面要求（按设计 8.2）：
  - **建号、重置密码**：新密码在对话框里显示一次，附「复制」按钮，并提示「关闭后无法再次查看」。
  - **调账对话框**：用 `parseSignedUsd`，原因必填；用 `IdempotencyKeeper` 生成键，放进 `Idempotency-Key` 头。
  - **代理商列表顶部的对账总览**：`ok=false` 时标红，并列出对不上的代理商。
- CHANGELOG 必须写到：
  - 新增代理商后台和管理员的三个页面；
  - 本站码列表多一列「代理商」；
  - `/partner/swap` 换出的新码继承代理商；
  - 前端新增依赖 `qrcode`；
  - 代理商下单需要 `APP_MASTER_KEY`，**上线后不能更换**；
  - 新增 7 张表、`local_cdks` 补两列，启动时自动完成。

- [ ] **Step 1: 写失败的测试**

```ts
const orig = [{ tier_id: 1, product_id: 2, price_cents: 500 }]
expect(matrixChanges(orig, { '1:2': '5.00' })).toEqual({ cells: [], errors: [] })
expect(matrixChanges(orig, { '1:2': '6' }).cells).toEqual([{ tier_id: 1, product_id: 2, price_cents: 600 }])
expect(matrixChanges(orig, { '1:2': '' }).cells).toEqual([{ tier_id: 1, product_id: 2, price_cents: 0 }])
expect(matrixChanges([], { '1:3': 'abc' }).errors.length).toBe(1)
// 侧栏：buildNavItems(routes) 含 group '代理商' 的两项；不含 agents/:id
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd frontend && npx vitest run src/lib/__tests__/agentAdmin.test.ts`
Expected: FAIL

- [ ] **Step 3: 实现 `agentAdmin.ts`、三个页面、路由，修改 `LocalCdkPanel.vue`，写 CHANGELOG**

- [ ] **Step 4: 跑前后端全部门禁**

Run: `cd frontend && npx vitest run && npm run build && cd ../backend && go vet ./... && go test ./internal/db/... ./internal/agent/... ./internal/handler/... ./internal/server/... -count=1`
Expected: 全部通过。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/lib/agentAdmin.ts frontend/src/lib/__tests__/agentAdmin.test.ts frontend/src/views/admin/AgentList.vue frontend/src/views/admin/AgentDetail.vue frontend/src/views/admin/AgentPricing.vue frontend/src/views/admin/components/LocalCdkPanel.vue frontend/src/router/index.ts CHANGELOG.md
git commit -m "feat(agent): 管理员代理商列表、详情、批发商品与价格页面，本站码列表加代理商列；CHANGELOG" -- frontend/src/lib/agentAdmin.ts frontend/src/lib/__tests__/agentAdmin.test.ts frontend/src/views/admin/AgentList.vue frontend/src/views/admin/AgentDetail.vue frontend/src/views/admin/AgentPricing.vue frontend/src/views/admin/components/LocalCdkPanel.vue frontend/src/router/index.ts CHANGELOG.md
```

（侧栏测试文件按实际放的位置一并加进提交。）

---

## 完成后（控制者做，不派给实现者）

- 在本机起测试实例，人工点一遍：
  - 进程：编译 `backend/cmd/server`；
  - 环境变量：`SERVER_PORT=19480`、临时库 `DB_PATH`、`WEB_DIR=frontend/dist`、随机生成的 `JWT_SECRET` 和 `APP_MASTER_KEY`、`UPDATE_ENABLED=0`；
  - 用假数据，不调真实接口；
  - 要点的流程：建号 → 首次登录改密码、扫码绑定 → 下单 → 复制、下载 → 我的码导出 → 流水导出 → 管理员调账、停用后代理商被踢出。
- 全分支终审，然后 gitleaks 扫描，推送，开 PR，等 CI，用合并提交合入。不发版。
