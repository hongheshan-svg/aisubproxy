# 首页售后查询 实施计划

> **给执行代理：** 必须配合使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans，逐个任务执行。步骤用勾选框（`- [ ]`）跟踪。

**目标：** 最终用户在首页粘贴一张卡密，就能看到状态、失败原因和自助指引、开通详情（含 ChatGPT 给的真实到期时间）以及换出的新码。查询按服务商分派，与具体服务商无关。顺带修掉上游服务商分支返回完整邮箱的漏洞。

**架构：** 分两层。
- **服务商层**：每家返回统一的 `afterSaleFacts`。
- **公共层**（`backend/internal/handler/aftersale.go`）：唯一一份，负责分派、邮箱打码、清洗服务商原文、跟换码链（最多 5 层）、输出白名单字段。

供卡型服务商共用一套实现。上游发码型服务商各自实现可选能力 `upstreamLookuper`。到期时间在续费核实时从 `GET /subscriptions` 的 `active_until` 记到 `pay_orders.access_until`。

**技术栈：** Go 1.x + gin + SQLite（后端），Vue 3 + vue-i18n + vitest + Tailwind 3（前端）。

**设计文档：** `docs/superpowers/specs/2026-11-04-aftersale-lookup-design.md`

**分支：** 等 `feat/embedded-checkout` 合并后，从最新的 master 新开 `feat/aftersale-lookup`。

## 全局约束

- 接口响应和日志里，不能出现完整邮箱、兑换令牌、会话、卡号。邮箱统一用 `maskEmailForAdmin`（`backend/internal/handler/card_health.go:527`）打码，只在公共层打。
- 不改兑换、付款、注资、换码这些资金逻辑。续费那里只多解析、多保存一个字段 `active_until`。
- `GET /api/v1/lookup/cdk`、`POST /api/v1/lookup/cdk/batch` 的路径、限流（`LookupGuard`）、批量上限（`lookupBatchMax = 100`）不变。现有字段只增不删，含义不变。
- `status` 新增两个取值：`replaced`（已换发新码）、`unsupported`（服务商不支持在线查询）。
- 指引编号是固定列表：`session_invalid`、`already_subscribed`、`plan_unavailable`、`credit_needs_subscription`、`credit_not_personal`、`credit_delinquent`、`credit_region_mismatch`、`retry_later`、`manual_review`、`provider_retry`、`provider_wait`。
- 服务商原文：截断到 200 个字符（按 rune 计）；邮箱、20 位以上的连续 `[A-Za-z0-9_-]` 替换成 `***`。
- 换码链最多跟 5 层（`afterSaleChainMax = 5`）。遇到已经走过的码就停。
- 所有新文案同时进 `frontend/src/i18n/locales/zh.ts` 和 `en.ts`，键要一致。
- 门禁：
  - `cd backend && go build ./... && go vet ./... && golangci-lint run ./...`，要求 0 issues；
  - `gofmt -l internal cmd`，输出为空；
  - `go test -race -timeout 40m $(go list ./... | grep -v /paybrowser)`；
  - `cd ../frontend && npx vitest run && npm run build`。
- 提交说明末尾写 `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`。不用 `git add -A`。

## 评审重点

下面 5 类输入，单看设计文档的测试清单覆盖不全，按出问题的可能性从高到低排。每条都在对应任务里补了测试。

1. **卡密写法不规范**：带空格、小写、零宽字符，换码链里的新码也是这种写法。期望都能查到，也能接着跟链。测试放在 Task 2：`TestAfterSaleChainNormalizesCodes`。
2. **邮箱本地部分是中文、只有 1 个字符、没有 `@`**。期望打码后不出乱码、不泄露原文。测试放在 Task 2：`TestAfterSaleMasksUnicodeEmail`。如果 `maskEmailForAdmin` 是按字节截的，改成按 rune 截。
3. **服务商查询 panic 或超时**。单条查询期望返回 `unknown` 和「暂时查不到，请稍后再试」，不能是 500；批量查询里其他条照常返回。测试放在 Task 2：`TestAfterSaleProviderPanicIsUnknown`、`TestAfterSaleBatchIsolatesPanic`。
4. **`active_until` 格式不对**：毫秒数、字符串形式的数字、1970 年、2200 年。期望毫秒能正确换算，离谱的年份不写入，页面上不会出现 1970。测试放在 Task 1：`TestParseActiveUntil`。
5. **旧前端（浏览器缓存）遇到新状态值 `replaced`、`unsupported`**。期望显示兜底文字，不报错。测试放在 Task 5：`afterSaleView 未知状态兜底`。

---

### Task 1：续费核实时记下真实到期时间

**文件：**
- 改：`backend/internal/chatgpt/billing.go`（`RenewalInfo` 约 :62，`SubscriptionStatus` 约 :70-84）
- 改：`backend/internal/payengine/renewal.go`（`renewalOff` 约 :270，`cancelRenewal` 约 :229，确认成功的分支约 :196）
- 改：`backend/internal/payengine/store.go`（`settableColumns` 约 :29，查询列 约 :38）、`backend/internal/payengine/model.go`（`Order` 结构体）
- 改：`backend/internal/db/pay.go`（:68 那张补列表）
- 测试：`backend/internal/chatgpt/billing_test.go`、`backend/internal/payengine/renewal_test.go`

**接口：**
- 产出：`chatgpt.RenewalInfo.ActiveUntil *time.Time`
- 产出：`payengine.Order.AccessUntil *int64`（Unix 秒）
- 产出：`pay_orders.access_until INTEGER`，可为空
- 产出：`func parseActiveUntil(v any) *time.Time`（chatgpt 包内）

- [ ] **Step 1：写失败的测试**
  - `TestParseActiveUntil`（billing_test.go），表驱动：

    | 输入 | 期望 |
    |---|---|
    | `"2026-12-04T10:00:00Z"` | 2026-12-04 10:00 UTC |
    | `"2026-12-04T10:00:00.123456+00:00"` | 同一时刻 |
    | `float64(1796378400)` | Unix 秒 |
    | `float64(1796378400000)` | 按毫秒换算，结果和上一行相同 |
    | `"1796378400"` | Unix 秒 |
    | `nil`、`""`、`"abc"`、`float64(0)` | nil |
    | 1970 年或 2200 年对应的数值 | nil（只接受 2020–2100 年） |

  - `TestSubscriptionStatusReadsActiveUntil`：假服务器返回 `{"will_renew":false,"active_until":"2026-12-04T10:00:00Z"}`，断言 `ActiveUntil` 正确；去掉这个字段后，`ActiveUntil == nil`。
  - `TestRenewalCancelledStoresAccessUntil`（renewal_test.go）：假 ChatGPT 的 `SubscriptionStatus` 返回 `WillRenew=false` 加 `ActiveUntil`。续费核实成功后，`Order.AccessUntil` 等于这个 Unix 秒。
  - `TestRenewalCancelledWithoutActiveUntil`：`ActiveUntil=nil` 时仍然判为 cancelled，`AccessUntil == nil`。
  - `TestRenewalFallbackAccountKeepsNil`：`/subscriptions` 出错、改走账号接口判为关闭时，`AccessUntil == nil`。

- [ ] **Step 2：确认测试失败**
  - 命令：`cd backend && go test ./internal/chatgpt/ ./internal/payengine/ -run 'ActiveUntil|AccessUntil|RenewalFallbackAccountKeepsNil' -v`
  - 期望：编译失败，报 `ActiveUntil`、`AccessUntil` 未定义。

- [ ] **Step 3：实现**
  - `parseActiveUntil`：
    - string：先试 RFC3339、RFC3339Nano，再试纯数字；
    - float64：大于 1e12 按毫秒处理；
    - 结果不在 2020–2100 年之间的，返回 nil。
  - `SubscriptionStatus` 填上 `ActiveUntil`。
  - `renewalOff` 的返回值改为 `(off bool, why string, until *time.Time)`，只有 `/subscriptions` 那条路径会带出 `until`。调用方 `cancelRenewal` 把它存到 `job` 上（新字段 `accessUntil`）。
  - 确认成功的分支：`j.accessUntil != nil` 时，`set["access_until"] = j.accessUntil.Unix()`。
  - `settableColumns` 加上 `access_until`；`Order` 和 store 的查询列加上 `access_until`。
  - db/pay.go 的补列表加一行 `{"access_until", "access_until INTEGER", ""}`，回填语句为空。

- [ ] **Step 4：确认测试通过**：同 Step 2 的命令，期望 PASS。

- [ ] **Step 5：跑门禁并提交**
  - 门禁里的后端部分全部通过。
  - `git add` 上面列出的文件，然后 `git commit -m "feat(pay): 续费核实时记下 ChatGPT 给的权益到期时间"`。

---

### Task 2：售后查询公共层

**文件：**
- 新建：`backend/internal/handler/aftersale.go`
- 改：`backend/internal/handler/lookup.go`
  - `cdkLookupResult` 加字段；
  - `lookupOneCDK` 改为调用公共层；
  - 现有的 `zovoCardLookup`、`localCDKLookup` 暂时通过 `factsFromLegacy` 接进来。
- 改：`backend/internal/handler/provider_adapter.go`
  - `upstreamLookuper` 改为 `Lookup(ctx context.Context, code, device string) (afterSaleFacts, error)`；
  - `zovoCardAdapter.Lookup`（provider_builtin.go:34）暂时返回 `factsFromLegacy(zovoCardLookup(...)), nil`。
- 视需要改：`backend/internal/handler/card_health.go`（`maskEmailForAdmin` 改为按 rune 截）
- 测试：`backend/internal/handler/aftersale_test.go`

**接口（产出，后面的任务都依赖）：**
```go
type afterSaleFacts struct {
	Found              bool
	Status             string // unused|processing|used|failed|disabled|replaced
	Reusable           bool
	Plan, PlanKind     string // PlanKind: subscription|credit|""
	UsedAt, ExpiresAt  *time.Time
	Renewal            string // off|closing|""
	Guide              string // 固定列表里的编号，或空
	ProviderNote       string // 原文，公共层负责清洗
	AccountEmail       string // 原样给，公共层负责打码
	Replacement        string // 新码的完整码，或空
	ReplacedButUnknown bool
	Message            string // 可选；空时由公共层按状态给默认文字
}
type lookupReplacement struct {
	Code   string `json:"code"`
	Status string `json:"status"`
	Depth  int    `json:"depth"`
}
const afterSaleChainMax = 5
func factsFor(ctx context.Context, code, device string) (afterSaleFacts, error) // 分派，并把 panic 恢复成 error
func lookupAfterSale(ctx context.Context, code, device string) cdkLookupResult   // 公共层入口；lookupOneCDK 直接调它
func factsFromLegacy(r cdkLookupResult) afterSaleFacts                          // 过渡用；Task 3、4 之后删掉
func sanitizeProviderNote(s string) string
```

`cdkLookupResult` 新增的字段：
```go
PlanKind        string             `json:"plan_kind,omitempty"`
ExpiresAt       *string            `json:"expires_at,omitempty"`  // RFC3339 UTC
Renewal         string             `json:"renewal,omitempty"`
Guide           string             `json:"guide,omitempty"`
ProviderNote    string             `json:"provider_note,omitempty"`
Replacement     *lookupReplacement `json:"replacement,omitempty"`
ReplacedUnknown bool               `json:"replaced_unknown,omitempty"`
```

**公共层规则：**
- **分派：**
  - `provider.ForCode` 出错：返回 `unknown`。
  - `provider.AsCardSupply` 成功：调供卡型的共用实现 `cardSupplyFacts(ctx, cs, code)`。这一步先写成 `factsFromLegacy(localCDKLookup(...))`。
  - 上游适配器实现了 `upstreamLookuper`：调它。
  - 都不满足：返回 `Found=true, Status="unsupported"`。
- **打码与清洗：**
  - `AccountEmail` 一律过 `maskEmailForAdmin`。
  - `Notes` 和 `ProviderNote` 都填清洗后的原文。
  - `Guide` 不在固定列表里时，按 `Reusable` 改成 `provider_retry` 或 `provider_wait`。
- **到期时间：** 只有 `Status=="used"` 并且 `ExpiresAt != nil` 时才输出 `expires_at`。
- **默认 `Message`：**
  - `replaced`：「该卡密已换发新码」；
  - `unsupported`：「这张卡密暂不支持在线查询，请联系售卖方」；
  - `factsFor` 出错：`status="unknown"`，`message` 为「暂时查不到，请稍后再试」，服务端只记错误类型。
- **换码链：** 见设计文档 §3.2 第 4 点。
  - 用 `provider.NormalizeCode` 规范化后再去重。
  - 中途某一层出错或找不到，就停在上一层。
  - 最终那张码的状态写进 `replacement.status`，层数写进 `depth`。

- [ ] **Step 1：写失败的测试**（aftersale_test.go）
  - 测试里用 `providertest.Register(t, providertest.NewTestUp())` 注册假上游，用 `withUpstreamAdapter(t, providertest.TestUp, fakeUp{...})` 注册假适配器；`fakeUp` 按码返回预设的 facts。

  | 测试 | 断言 |
  |---|---|
  | `TestAfterSaleMasksEmailFromProvider` | 假上游返回 `alice@example.com`。单条结果的 JSON 里不含 `alice@example.com`，含 `al***@example.com`。批量 `LookupCDKStatusBatch` 同样不含原文 |
  | `TestAfterSaleMasksUnicodeEmail` | 邮箱分别是 `张三丰@x.com`、`a@x.com`、`abcdef`。打码结果都是合法 UTF-8，`张三丰@x.com` 不会原样出现 |
  | `TestAfterSaleSanitizesNote` | 300 个字符，里面夹着 `bob@x.com` 和 `abcdefghijklmnopqrstuvwxyz0123`。结果不超过 200 个 rune，邮箱和长串都变成 `***` |
  | `TestAfterSaleUnsupportedProvider` | 上游适配器没有 `Lookup`。`status=="unsupported"`，`message` 和全局约束里写的一致 |
  | `TestAfterSaleUnknownGuideFallsBack` | 假上游返回 `Guide:"weird"`。`Reusable=true` 时结果为 `provider_retry`，`false` 时为 `provider_wait` |
  | `TestAfterSaleChainFollows` | A 换成 B，B 换成 C，C 是 unused。A 的结果：`status=="replaced"`，`replacement=={C,"unused",2}` |
  | `TestAfterSaleChainNormalizesCodes` | A 的 `Replacement` 是 `" tu-bbb​ "`，B 能查到，`replacement.code=="TU-BBB"` |
  | `TestAfterSaleChainCycleAndCap` | A 换成 B、B 又换回 A，结果停在 B，`depth==1`。7 层的链停在第 5 层，`depth==5` |
  | `TestAfterSaleChainStopsOnError` | A 换成 B，查 B 时出错。`replacement` 为空，或者只带到能确认的那一层，A 自己的 `status=="replaced"` |
  | `TestAfterSaleReplacedUnknown` | `ReplacedButUnknown=true`。`replaced_unknown==true`，不带 `replacement` |
  | `TestAfterSaleExpiresOnlyWhenUsed` | used 加 `ExpiresAt`：输出 RFC3339。unused 加 `ExpiresAt`：不输出 |
  | `TestAfterSaleProviderPanicIsUnknown` | 假上游 panic。单条返回 `unknown`，`message` 为「暂时查不到，请稍后再试」 |
  | `TestAfterSaleBatchIsolatesPanic` | 批量 3 张，中间那张 panic。另外两张照常返回，中间那张是 `unknown` |

- [ ] **Step 2：确认测试失败**
  - 命令：`cd backend && go test ./internal/handler/ -run AfterSale -v`
  - 期望：编译失败，报 `afterSaleFacts` 未定义。

- [ ] **Step 3：实现上面的接口和规则**
  - 改了 `upstreamLookuper` 的签名之后，`zovoCardAdapter` 先用 `factsFromLegacy` 过渡。
  - 现有的 `cdk_session_test.go:TestLookupKeepsEmailAndStatusAfterClear` 会因为邮箱打码而失败。把它的期望改成打码后的值，并在提交说明里写明这是有意的行为变化。

- [ ] **Step 4：确认测试通过**
  - 命令：`go test ./internal/handler/ -v -run 'AfterSale|Lookup'`
  - 期望：PASS。

- [ ] **Step 5：跑后端门禁并提交**
  - `git commit -m "feat(lookup): 售后查询公共层：按服务商分派，统一打码、清洗原文、跟换码链"`

---

### Task 3：供卡型服务商的共用实现

**文件：**
- 改：`backend/internal/db/local_cdk.go`
  - `LocalCDKOrder` 加 `Kind string`、`RenewalState string`、`AccessUntil *int64`；
  - 同步改 `localCDKOrderColumns`（约 :363）和 `collectLocalCDKOrders` 的 Scan。
- 改：`backend/internal/handler/local_cdk.go`
  - 新增 `cardSupplyFacts(ctx context.Context, cs provider.CardSupply, code string) (afterSaleFacts, error)`；
  - 删掉 Task 2 里对 `localCDKLookup` 的过渡调用；
  - 如果 `localCDKLookup` 已经没有其他调用方，一并删掉。
- 测试：`backend/internal/handler/aftersale_cardsupply_test.go`（用 `newLocalEnv`）

**接口：**
- 消费：Task 1 的 `pay_orders.access_until`；Task 2 的 `afterSaleFacts`。
- 产出：`cardSupplyFacts`，所有供卡型服务商共用。

**映射规则：**
- `Plan = row.PlanKey`，`PlanKind = payengine.PlanKind(row.PlanKey)`。
- 各状态：

  | 派生状态 | 结果 |
  |---|---|
  | `LocalCDKConsumed` | `used`；`UsedAt = Latest.FinishedAt`；`ExpiresAt = Latest.AccessUntil`；`Renewal`：`cancelled` → `off`，`pending` → `closing`，其他 → 空 |
  | `LocalCDKReserved` | `processing`；`Reason == needs_review` 时 `Guide = manual_review` |
  | `LocalCDKFailed` | `failed`；`Reusable = false`；`Guide = manual_review` |
  | `LocalCDKDisabled`，`Reason == exchanged` | `replaced`；`ReplacedButUnknown = true`（新码的完整码不入库） |
  | `LocalCDKDisabled`，其他原因 | `disabled` |
  | 其余，`Latest == nil` | `unused` |
  | 其余，`Latest != nil`（干净失败） | `unused`；`Reusable = true`；`Guide` 按 `Latest.ErrorCode` 对照设计文档 §4 的表，对不上的用 `retry_later` |

- `AccountEmail = Latest.AccountEmail`（原样给，由公共层打码）。
- `Message`：沿用 `localPublicState` 的文字。

- [ ] **Step 1：写失败的测试**
  - 表驱动 `TestCardSupplyFactsMapping`：用 `e.issueCode("plus","PH")` 和 `e.insertOrder(...)` 造出每种状态，必要时直接 `UPDATE pay_orders SET renewal_state=?, access_until=?, error_code=?`。逐行断言 `Status`、`Reusable`、`Guide`、`Renewal`、`ExpiresAt`。
  - 错误码对照，每个码一行：
    - `session_invalid`、`already_subscribed`、`plan_unavailable` 原样对上；
    - `credit_no_subscription`、`credit_plan_ineligible` → `credit_needs_subscription`；
    - `credit_not_personal`、`credit_delinquent` 原样对上；
    - `credit_currency_mismatch` → `credit_region_mismatch`；
    - 未知码 → `retry_later`。
  - `TestCardSupplyFactsExchanged`：用 `db.ExchangeLocalCDK` 换码后，查旧码得到 `replaced`，`replaced_unknown=true`，并且响应 JSON 里不出现新码的哈希或前缀以外的内容。
  - `TestCardSupplyFactsGenericProvider`：用 `providertest.NewTestCard()`（前缀 `TC-`）造一张码，它不经过任何专门的适配器，也走这套映射，查询结果正确。

- [ ] **Step 2：确认测试失败**
  - 命令：`go test ./internal/handler/ -run CardSupplyFacts -v`
  - 期望：编译失败，或断言失败。

- [ ] **Step 3：按上面的规则实现。**

- [ ] **Step 4：确认测试通过**：同 Step 2 的命令，期望 PASS；`go test ./internal/db/ -run LocalCDK` 也要 PASS。

- [ ] **Step 5：跑后端门禁并提交**
  - `git commit -m "feat(lookup): 供卡型服务商共用的售后查询：指引编号、续费状态、真实到期时间"`

---

### Task 4：ZovoCard（上游发码型）的实现

**文件：**
- 改：`backend/internal/handler/lookup.go`
  - `zovoCardLookup` 改成 `zovoCardFacts(ctx, code, device string) (afterSaleFacts, error)`；
  - `zovoCardAdapter.Lookup` 改为调用它；
  - 删掉 `factsFromLegacy`。
- 改：`backend/internal/db/agent_exchange.go`，新增 `GetAgentExchangeByOldHash(codeHash string) (AgentExchange, error)`。没有记录时返回 `sql.ErrNoRows`。
- 测试：`backend/internal/handler/aftersale_zovo_test.go`、`backend/internal/db/agent_exchange_test.go`

**映射规则：**
- 邮箱的三个来源（`recharge_tasks`、绑定记录、卡台 Result）照旧取值，原样交给公共层打码。
- 状态沿用现有判断。
- 失败时：`ProviderNote` 填原 notes，`Guide` 按 `Reusable` 取 `provider_retry` 或 `provider_wait`。
- `ExpiresAt = nil`；`PlanKind` 留空。
- 换码：
  - 先用 `db.HashCDKCode(norm)` 查 `agent_cdk_exchanges`，只认 `state=='done'` 的记录。
  - 找到了就用 `db.LookupCardplatformCDKCode(newUpstreamID, newPrefix)` 取完整码：取到 → `Status="replaced"`，`Replacement = 完整码`；取不到 → `ReplacedButUnknown = true`。
  - 记录处于 `pending`、`unknown`、`disabling` 时不算换码，状态照常判断。

- [ ] **Step 1：写失败的测试**
  - `TestGetAgentExchangeByOldHash`：有记录 / 没有记录。
  - `TestZovoFactsEmailMaskedAllSources`：三个来源各造一次完整邮箱（卡台 Result 用 `newCDKUpstream` 里的桩），通过 `lookupOneCDK` 查，结果都是打码的。
  - `TestZovoFactsFailureGuide`：可以重新提交 → `provider_retry`，加上原文；不能 → `provider_wait`。
  - `TestZovoFactsReplacement`：
    - 有 done 记录，库里有新码 → `replacement.code` 是完整新码；
    - 库里没有新码 → `replaced_unknown`；
    - 记录处于 pending → 不算换码。

- [ ] **Step 2：确认测试失败**
  - 命令：`go test ./internal/handler/ ./internal/db/ -run 'Zovo|AgentExchangeByOldHash' -v`
  - 期望：编译失败，或断言失败。

- [ ] **Step 3：按上面的规则实现。**

- [ ] **Step 4：确认测试通过**：同 Step 2 的命令，期望 PASS；`go test ./internal/handler/ -run Lookup` 也要 PASS。

- [ ] **Step 5：跑后端门禁并提交**
  - `git commit -m "feat(lookup): ZovoCard 售后查询改用统一结构，补上换码新码，邮箱一律打码"`

---

### Task 5：前端显示模型和语言包

**文件：**
- 新建：`frontend/src/lib/aftersale.ts`
- 改：`frontend/src/i18n/locales/zh.ts`、`en.ts`，新增顶层键 `aftersale`
- 测试：`frontend/src/lib/__tests__/aftersale.test.ts`、`frontend/src/lib/__tests__/i18n-aftersale-keys.test.ts`

**接口（产出）：**
```ts
export interface LookupResult {
  cdk_code: string; status: string; used: boolean; can_resubmit?: boolean
  account_email?: string; plan?: string; plan_kind?: string; used_at?: string
  expires_at?: string; renewal?: string; guide?: string; provider_note?: string
  notes?: string; message?: string
  replacement?: { code: string; status: string; depth: number }; replaced_unknown?: boolean
}
export type AfterSaleAction = 'redeem' | 'resubmit' | 'copy_replacement' | 'redeem_replacement'
export interface AfterSaleView {
  tone: 'ok' | 'info' | 'warn' | 'err'
  titleKey: string                                  // i18n 键，如 aftersale.state.used
  guideKey?: string                                 // aftersale.guide.<编号>
  note?: string                                     // provider_note 原文（已清洗）
  fields: { labelKey: string; value: string }[]     // 只放有值的字段
  renewalKey?: string                               // aftersale.renewal.off / closing
  replacement?: { code: string; statusKey: string }
  actions: AfterSaleAction[]
}
export function afterSaleView(r: LookupResult): AfterSaleView
```

`afterSaleView` 的规则（照设计文档 §6 的表）：

| 输入 | 输出 |
|---|---|
| `unused`，没有 `guide` | `redeem` |
| `unused` 或 `failed`，`can_resubmit` 并且有 `guide` | 「失败，码已恢复」，`resubmit` |
| `failed`，`!can_resubmit` | 「已转人工 / 请勿重复提交」，没有按钮 |
| `processing` | 没有按钮 |
| `used` | 字段：套餐、开通时间、到期时间（有才放）、邮箱；`plan_kind=='credit'` 时用「点数已到账」，不放到期时间 |
| `replaced` 并且有 `replacement` | `copy_replacement`、`redeem_replacement` |
| `replaced` 并且 `replaced_unknown` | 「已换发新码，请联系售卖方获取」 |
| `unsupported` | 「暂不支持在线查询」 |
| 其他值 | `aftersale.state.unknown`，不抛错 |

时间按北京时间显示，复用 lib 里现有的时间格式化函数，执行时先 grep `fmtTs` 或类似的函数确认。

- [ ] **Step 1：写失败的测试**
  - aftersale.test.ts：每种状态一条用例，`guide` 的 11 个编号逐个产出 `aftersale.guide.<编号>`；点数码不放到期时间；`afterSaleView 未知状态兜底` 输入 `status:'weird'` 不抛错，`titleKey==='aftersale.state.unknown'`。
  - i18n-aftersale-keys.test.ts：把 `zh.aftersale` 和 `en.aftersale` 拍平成键列表，两边相等，而且包含 11 个 `guide.<编号>`。
- [ ] **Step 2：确认测试失败**
  - 命令：`cd frontend && npx vitest run src/lib/__tests__/aftersale.test.ts src/lib/__tests__/i18n-aftersale-keys.test.ts`
  - 期望：FAIL，提示模块不存在。
- [ ] **Step 3：实现 `afterSaleView`，写中英文文案。** 指引文字照设计文档 §4 的表；英文译文要通顺。
- [ ] **Step 4：确认测试通过**：同 Step 2 的命令，期望 PASS。
- [ ] **Step 5：跑 `npx vitest run && npm run build` 并提交**
  - `git commit -m "feat(lookup): 售后查询的显示模型和中英文指引"`

---

### Task 6：首页售后查询区块，`/history` 也接上显示模型

**文件：**
- 新建：`frontend/src/components/AfterSaleLookup.vue`（输入框、查询按钮，结果按 `afterSaleView` 渲染）
- 改：`frontend/src/views/HomeView.vue`：标题下方放 `<AfterSaleLookup />`，加一个「批量查询」链接到 `/history`
- 改：`frontend/src/views/user/CDKStatusView.vue`
  - 单条结果改用 `afterSaleView` 渲染；
  - 类型改为从 `lib/aftersale` 导入 `LookupResult`；
  - CSV 的列不变，邮箱已经由后端打好码。
- 改：`frontend/src/views/__tests__/home.spec.ts`
- 改：`CHANGELOG.md` 的「## 未发布」

**行为：**
- 调用方式：原生 `fetch('/api/v1/lookup/cdk?code=' + encodeURIComponent(code))`。404 当作「找不到」，网络错误显示 `cdkLookup.errNetwork`。
- 按钮：
  - 「去兑换 / 重新兑换」跳到 `/recharge?code=<码>`。执行时先确认 RechargeView 是否认 `code` 查询参数；不认的话，补上读取。
  - 「复制」复用 lib 里现有的复制工具。
- 首页区块要放进现有的 section 结构，不改动别的 section。

- [ ] **Step 1：写失败的测试**
  - home.spec.ts 新增「首页有售后查询区块」：挂载 HomeView，能找到 `data-testid="aftersale-lookup"`。
  - 假 fetch 返回 used 结果时，显示打码邮箱，并显示到期时间。
  - 假 fetch 返回 replaced 结果时，显示新码，并有复制按钮。
- [ ] **Step 2：确认测试失败**
  - 命令：`npx vitest run src/views/__tests__/home.spec.ts`
  - 期望：FAIL。
- [ ] **Step 3：实现组件，接到首页和 `/history`。**
- [ ] **Step 4：确认测试通过**：同 Step 2 的命令，期望 PASS。
- [ ] **Step 5：CHANGELOG**：在「## 未发布」里加「首页售后查询」一段，写清楚三件事：
  - 首页可以直接查卡密；
  - 失败会给出指引，换过码的会显示新码；
  - 查询结果和导出里的邮箱改为一律打码，以前上游服务商分支会返回完整邮箱。
- [ ] **Step 6：跑全部门禁并提交**
  - `git commit -m "feat(home): 首页售后查询，卡密查询页单条结果使用同一显示模型"`

---

## 自查记录

- **设计覆盖：**
  - §3.1 服务商层：Task 2（接口）、Task 3（供卡型）、Task 4（上游型）；
  - §3.2 公共层：Task 2；
  - §4 指引：Task 3、4、5；
  - §5 接口字段：Task 2；
  - §6 前端：Task 5、6；
  - §7 出错处理：Task 2；
  - §8 测试：各任务的 Step 1；
  - §9 上线（迁移、CHANGELOG）：Task 1、6。
- **类型一致：**
  - `afterSaleFacts` 和 `cdkLookupResult` 的新字段在 Task 2 定义，Task 3、4 使用；
  - 前端的 `LookupResult` 与后端 JSON 字段名逐一对过：`expires_at`、`replacement{code,status,depth}`、`replaced_unknown`。
- **已知取舍：**
  - 本站码换码的新码没有完整码，只能显示「请联系售卖方获取」，要等第 5 个子项目解决；
  - 已有的单没有 `access_until`，不显示到期时间。
