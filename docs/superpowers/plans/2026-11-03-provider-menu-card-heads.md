# 后台菜单按服务商整理、卡头与 MarsWallex 选卡规则 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal：**

- 每家服务商有自己的卡头列表：有接口的自动读取，没接口的手动维护，可以标注 3DS、Apple Pay。
- 后台菜单分成通用能力和每家服务商一组。
- MarsWallex 有独立的选卡规则页。

**Architecture：**

- 卡头存在新表 `provider_card_heads` 里。
- handler 层新增可选能力 `cardHeadSource`：服务商的适配器实现了它，就自动同步；没实现，就开放手动增删改。
- 前端用共用组件 `CardHeadsPanel`，嵌在各家「选卡规则」页的顶部。
- 菜单仍以路由 meta 为唯一来源，现有页面地址都不变。
- MarsWallex 的选卡配置从卡池策略里拆出单独的读写接口，两个页面保存时互不覆盖。

**Tech Stack：** Go 1.27、gin、SQLite；Vue 3、Element Plus、vitest。

**Spec：** `docs/superpowers/specs/2026-11-03-provider-menu-card-heads-design.md`

## Global Constraints

- 选卡、补卡、付款、发码、兑换的行为不变；卡头和标注只用于显示。
- 现有 `/ops/*` 页面地址全部不变，只新增 `/ops/marswallex-selection`。
- 3DS、Apple Pay 的取值只能是 `''`（未标注）、`yes`、`no`。
- 卡头识别规则：产品名里第一段 6 到 9 位的连续数字，两侧必须不是数字。
- 校验上限：
  - `note` 最多 200 字，`name` 最多 100 字；
  - `product_id` 为 1 到 64 位 `[A-Za-z0-9_.-]`；
  - 卡头每个 4 到 12 位数字，最多 10 个；
  - `network`、`country`、`currency` 各最多 16 字。
- 卡头同步时机：启动 1 分钟后一次，之后每 30 分钟一次，另外有手动「立即同步」。
- 审计里不出现任何密钥。
- 可见文字都用简体中文。提交信息以 `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` 结尾。
- 不碰 18080 端口和仓库的 `data/` 目录。
- 测试不调真实接口：MarsWallex 用 `mwtest`，ZovoCard 用本地产品缓存。

## Review Focus

1. **服务商接口返回空列表，或服务商还没配置**：不能把已有卡头全部标成「接口已不再返回」。返回空列表按出错处理，表不改，页面显示错误。（Task 2 测试）
2. **两个浏览器标签页分别开着卡池页和 MarsWallex 选卡规则页，先后保存**：后保存的一方不能把另一方刚改的字段冲掉。（Task 6 测试）
3. **升级前就开着的旧卡池页提交整份策略**：照旧全部生效。（Task 6 测试）
4. **ZovoCard 产品缓存还是空的**（卡台没配置或还没同步）：按出错处理，不改表。（Task 2 测试）
5. **地址里的服务商名是未注册的，或产品 ID 不合法**：返回 404 或 400，不写库。（Task 3 测试）

## 文件结构

| 文件 | 职责 |
|---|---|
| `backend/internal/db/card_heads.go`（新） | 表结构和读写，同步规则（事务内完成） |
| `backend/internal/handler/card_heads.go`（新） | 可选能力、卡头识别、同步与状态、后台定时同步、后台接口 |
| `backend/internal/handler/provider_builtin.go` | 两家适配器实现 `CardHeads` |
| `backend/internal/handler/cardpool.go` | 选卡字段的读写接口；策略接口改为「没带的选卡字段保持当前值」 |
| `backend/internal/cardpool/policy.go` | `Selection` 类型，以及和 `Policy` 之间的互相转换 |
| `backend/internal/server/http.go` | 路由，启动卡头同步 |
| `frontend/src/lib/cardheads.ts`（新） | 卡头类型和标签文字 |
| `frontend/src/views/admin/components/CardHeadsPanel.vue`（新） | 共用卡头组件 |
| `frontend/src/views/admin/MarsWallexSelection.vue`（新） | MarsWallex 选卡规则页 |
| `frontend/src/views/admin/components/AgentSwapSettings.vue`（新） | 代理换码设置，从 ZovoCard 接入配置页拆出来 |
| `frontend/src/router/index.ts`、`layouts/AdminLayout.vue`、`lib/adminui.ts` | 菜单分组、`fullTitle`、页内标签页名 |

---

## PR 1：卡头

### Task 1: 卡头数据表与读写

**Files：**
- Create: `backend/internal/db/card_heads.go`
- Test: `backend/internal/db/card_heads_test.go`
- Modify: `backend/internal/db/db.go`（在调用 `MigratePayProbes()` 的地方旁边加上 `MigrateCardHeads()`）

**Interfaces：**
- Produces：
  ```go
  const CardHeadSourceAPI, CardHeadSourceManual = "api", "manual"
  var ErrCardHeadNotFound, ErrCardHeadNotManual error
  type CardHead struct {
      Provider, ProductID, Source, Name string; CardHeads []string
      Network, Country, Currency string; Listed bool
      ThreeDS, ApplePay, Note string            // json: three_ds, apple_pay, note
      SyncedAt, UpdatedAt int64; UpdatedBy string // json: snake_case
  }
  type CardHeadProduct struct { ProductID, Name string; CardHeads []string; Network, Country, Currency string }
  func MigrateCardHeads() error
  func ListCardHeads(provider string) ([]CardHead, error)   // 排序：listed DESC, product_id；CardHeads 永不为 nil
  func GetCardHead(provider, productID string) (CardHead, error) // 没有时 ErrCardHeadNotFound
  func SyncCardHeads(provider string, items []CardHeadProduct, now int64) error
  func UpdateCardHeadNotes(provider, productID, threeDS, applePay, note, by string, now int64) error
  func SaveManualCardHead(h CardHead, by string, now int64) error // 已有 api 行时 ErrCardHeadNotManual
  func DeleteManualCardHead(provider, productID string) error    // 不存在 ErrCardHeadNotFound，api 行 ErrCardHeadNotManual
  ```

- [ ] **Step 1：写失败的测试**，用 `dbtest.Open(t)`：
  - `TestSyncCardHeadsKeepsNotes`：
    1. 同步 A、B；
    2. 给 A 标注 `three_ds=yes`、`note=x`；
    3. 再同步，A 的名称改了，B 不在列表里。
    4. 期望：A 名称更新、标注保留、`listed=true`；B `listed=false`，仍在表里。
  - `TestSyncCardHeadsAdoptsManual`：
    1. 手动添加 C；
    2. 同步时返回 C；
    3. 期望：C 的 `source=api`，标注保留。
  - `TestSyncCardHeadsOtherProvider`：同步 p1 不影响 p2 的记录。
  - `TestManualCardHeadRules`：
    - 对 api 行 `SaveManualCardHead` 返回 `ErrCardHeadNotManual`；
    - 删除 api 行返回 `ErrCardHeadNotManual`；
    - 删除不存在的返回 `ErrCardHeadNotFound`。
- [ ] **Step 2：** 运行 `cd backend && go test ./internal/db/ -run CardHead`，期望编译失败。
- [ ] **Step 3：** 实现。
  - 建表语句见 spec §4.1，主键 `(provider, product_id)`，`card_heads` 存 JSON。
  - `SyncCardHeads` 在一个事务里完成：
    1. 逐条 `INSERT … ON CONFLICT DO UPDATE`，只更新产品信息、`source='api'`、`listed=1`、`synced_at`；
    2. 再把这家服务商 `source='api'` 但不在本次列表里的记录设为 `listed=0`。
- [ ] **Step 4：** 再跑一遍，期望通过。
- [ ] **Step 5：提交**：`feat(db): 卡头表 provider_card_heads 与同步规则`

### Task 2: 可选能力、卡头识别、同步

**Files：**
- Create: `backend/internal/handler/card_heads.go`
- Modify: `backend/internal/handler/provider_builtin.go`（两家适配器各加 `CardHeads`）、`backend/internal/server/http.go`（在 `handler.StartOpsMaintenance(ctx)` 旁边加上 `handler.StartCardHeadSync(ctx)`）
- Test: `backend/internal/handler/card_heads_test.go`

**Interfaces：**
- Consumes：Task 1 的 `db.CardHeadProduct`、`db.SyncCardHeads`。
- Produces：
  ```go
  type cardHeadSource interface { CardHeads(ctx context.Context) ([]db.CardHeadProduct, error) }
  func cardHeadSourceFor(n provider.Name) (cardHeadSource, bool) // 依次看 cardSupplyAdapterFor、upstreamAdapterFor 的类型断言
  func parseCardHead(name string) string
  func syncCardHeads(ctx context.Context, n provider.Name, src cardHeadSource) error
  func cardHeadSyncState(n provider.Name) (lastAt int64, lastErr string) // 内存里，带锁
  func StartCardHeadSync(ctx context.Context) // 延迟 1 分钟，之后每 30 分钟同步一次，只同步有 cardHeadSource 的服务商
  ```
- **MarsWallex 的 `CardHeads`**：
  - `cardPool` 为 nil 时返回错误「卡池未启动」。
  - 否则调用 `cardPool.ListProducts`，按字段对应：`ProductName→Name`、`parseCardHead(ProductName)` 非空时作为唯一的卡头、`CardLabel→Network`、`CardCountry→Country`、`CardCurrency→Currency`。
- **ZovoCard 的 `CardHeads`**：
  - 读 `db.GetCardProducts()`，结果为空时返回错误「卡台产品还没同步」。
  - 字段对应：`ProductCode→ProductID`、`Description`（为空时用 `ProductCode`）`→Name`、`BinHeads`（为空时用 `[BIN]`，`BIN` 也为空时用空数组）`→CardHeads`、`Network`、`IssuingArea→Country`、`Currency` 留空。

- [ ] **Step 1：写失败的测试**
  - `TestParseCardHead`，表驱动：

    | 输入 | 期望 |
    |---|---|
    | 美卡-泛场景-512998-充值卡 | 512998 |
    | 美卡-泛场景-428820-充值卡 | 428820 |
    | 美卡-泛场景-537100-充值卡-支持APPLEPAY | 537100 |
    | 美卡投流AI-537872712-充值卡 | 537872712 |
    | 普通卡 | （空） |
    | 卡-12345-x | （空，太短） |
    | 卡-1234567890-x | （空，太长） |
    | A-411111-B-522222 | 411111 |
  - `TestSyncCardHeadsEmptyIsError`：假数据源返回空列表或返回错误时，`syncCardHeads` 报错，已有记录的 `listed` 和标注都不变，`cardHeadSyncState` 里记下错误。
  - `TestMarsWallexCardHeads`：用 `cpEnv` 加上 `mw.On("GET", "/v1/card/product", mwtest.OK(...))`，返回上面四个真实产品名，期望卡头和 `Currency=USD` 都对。
  - `TestZovoCardCardHeadsNeedsCache`：缓存为空时报错；有缓存时字段按上面的对应关系转换。
- [ ] **Step 2：** 运行 `go test ./internal/handler/ -run 'CardHead'`，期望失败。
- [ ] **Step 3：实现。**
  - `parseCardHead` 用正则 `(^|[^0-9])([0-9]{6,9})([^0-9]|$)`，取第一处匹配。
  - `syncCardHeads`：
    1. 调用 `src.CardHeads`；
    2. 出错或返回空列表时，记下错误并返回，不写库；
    3. 成功时调用 `db.SyncCardHeads`，并记下同步时间。
- [ ] **Step 4：** 测试通过，`go vet ./internal/handler/`。
- [ ] **Step 5：提交**：`feat(provider): 服务商可选能力「读取卡头」，MarsWallex、ZovoCard 自动同步`

### Task 3: 卡头后台接口

**Files：**
- Modify: `backend/internal/handler/card_heads.go`、`backend/internal/server/http.go`（admin 组）
- Test: `backend/internal/handler/card_heads_test.go`

**Interfaces：**
- Produces（路由在 `/api/v1/admin` 下）：

  | 接口 | 处理函数 |
  |---|---|
  | `GET /providers/:name/card-heads` | `AdminListCardHeads`，返回 `{provider, auto, last_sync_at, last_sync_error, items}` |
  | `POST /providers/:name/card-heads/sync` | `AdminSyncCardHeads`：成功返回列表，同步失败返回 502 和 `{error}` |
  | `PUT /providers/:name/card-heads/:product_id` | `AdminUpdateCardHead`，请求体 `{three_ds, apple_pay, note, name?, card_heads?, network?, country?, currency?}`；后五项只对 manual 行生效 |
  | `POST /providers/:name/card-heads` | `AdminAddCardHead`，能自动读取的服务商返回 400 |
  | `DELETE /providers/:name/card-heads/:product_id` | `AdminDeleteCardHead`，只能删 manual 行 |

- **审计**动作名：`card_heads_sync`、`card_head_update`、`card_head_add`、`card_head_delete`。内容写服务商、产品 ID 和改了哪些字段。

- [ ] **Step 1：写失败的测试**，用 `cpDo` 发请求，用 `providertest.Register(t, providertest.NewTestUp())` 注册一个只能手动维护的服务商：
  - `TestCardHeadsUnknownProvider`：`GET`、`PUT` 访问 `/providers/nope/card-heads` 返回 404。
  - `TestCardHeadsManualProvider`：
    - 对 TestUp：同步返回 400；
    - 添加 `{product_id:"P1", name:"测试卡", card_heads:["411111"], three_ds:"yes"}` 返回 200；
    - 列表里 `auto=false`，有 P1；
    - 修改 `note` 生效；
    - 删除后列表为空；
    - 审计里有 `card_head_add`。
  - `TestCardHeadsAutoProvider`：
    - 用 `withCardSupplyAdapter` 给 `providertest.TestCard` 接一个同时实现 `Check` 和 `CardHeads` 的假适配器；
    - 手动添加返回 400；
    - 同步后 `auto=true`；
    - 修改标注后再同步，标注还在。
  - `TestCardHeadsValidation`，以下情况都返回 400 且不写库：
    - `three_ds:"maybe"`；
    - `note` 有 201 字；
    - `product_id` 含 `/` 或空格（直接用 `db` 调用验证校验函数）；
    - 卡头 `"12ab"`；
    - 卡头 11 个。
- [ ] **Step 2：** 跑测试，期望失败。
- [ ] **Step 3：** 实现处理函数和校验函数 `validateCardHead(h db.CardHead) error`，在 `http.go` 里注册路由。
- [ ] **Step 4：** 测试通过，再跑 `golangci-lint run ./internal/...`。
- [ ] **Step 5：提交**：`feat(admin): 卡头列表、同步、标注与手动维护接口`

### Task 4: 共用卡头组件，接入 ZovoCard 选卡规则页

**Files：**
- Create: `frontend/src/lib/cardheads.ts`、`frontend/src/lib/__tests__/cardheads.test.ts`、`frontend/src/views/admin/components/CardHeadsPanel.vue`
- Modify: `frontend/src/views/admin/CardSelectionConfig.vue`

**Interfaces：**
- Produces：
  ```ts
  export type TriState = '' | 'yes' | 'no'
  export interface CardHead { provider: string; product_id: string; source: 'api' | 'manual'; name: string; card_heads: string[]
    network: string; country: string; currency: string; listed: boolean; three_ds: TriState; apple_pay: TriState
    note: string; synced_at: number; updated_at: number; updated_by: string }
  export interface CardHeadList { provider: string; auto: boolean; last_sync_at: number; last_sync_error: string; items: CardHead[] }
  export function triLabel(v: TriState): string          // '' 未标注、yes 支持、no 不支持
  export function cardHeadTitle(h: CardHead): string     // 卡头用「/」连接；没有卡头时用 name，再没有用 product_id
  export function cardHeadOptionLabel(h: CardHead): string
  export function cardHeadsByProduct(items: CardHead[]): Record<string, CardHead>
  ```
- **`cardHeadOptionLabel` 的格式**：`${title} · 3DS ${triLabel} · Apple Pay ${triLabel}`。未标注的项不写；`listed` 为 false 时在末尾加「（接口已不再返回）」。
  - 例：`512998 · 3DS 不支持`
  - 例：`537100 · 3DS 支持 · Apple Pay 支持`
- **`CardHeadsPanel`**：
  - props：`{ provider: string }`；emits：`loaded(items: CardHead[])`。
  - 表格列：卡头、名称、卡组织、国家、币种、3DS、Apple Pay、备注、来源；接口已不再返回的行标灰。
  - `auto` 为 true：显示同步时间、错误信息、「立即同步」按钮。为 false：显示「添加卡头」，每行可以编辑、删除，删除前要确认。
  - 每行「编辑标注」，弹窗里两个三态选项加备注。

- [ ] **Step 1：写失败的测试** `cardheads.test.ts`：
  - `triLabel` 三种取值；
  - `cardHeadTitle`：有卡头、多个卡头、没卡头有名称、什么都没有；
  - `cardHeadOptionLabel`：四个真实卡头按用户给的 3DS、Apple Pay 标注后的文字，以及 `listed=false`；
  - `cardHeadsByProduct`。
- [ ] **Step 2：** 运行 `cd frontend && npx vitest run src/lib/__tests__/cardheads.test.ts`，期望失败。
- [ ] **Step 3：** 实现 `cardheads.ts` 和 `CardHeadsPanel.vue`，调用 `authFetch`、`dialog`。
  - `CardSelectionConfig.vue` 顶部放 `<CardHeadsPanel provider="zovocard" @loaded=…>`。
  - 选产品的下拉框和优先级列表里，产品名后面加上 `cardHeadOptionLabel`（按产品代码对应）。
  - 页头标题改成「ZovoCard 选卡规则」，说明里写「只对 ZovoCard 发出的码生效」。
- [ ] **Step 4：** 运行 `npx vitest run`、`npx vue-tsc --noEmit`（或 `npm run build`），期望全部通过。
- [ ] **Step 5：提交**：`feat(ui): 共用卡头组件，ZovoCard 选卡规则页显示卡头与标注`

### Task 5: PR 1 收尾

- [ ] CHANGELOG「未发布 → 新增」写「各服务商的卡头」。
- [ ] 跑全部本地检查：gofmt、`go vet`、golangci-lint、`go test -race`（paybrowser 除外）、前端测试和构建、gitleaks。
- [ ] 在临时数据库上起本地服务，用浏览器看 ZovoCard 选卡规则页：同步、标注、手机布局。
- [ ] 推送，开 PR，等 CI 全部通过后用合并提交合并。

---

## PR 2：菜单与 MarsWallex 选卡规则

### Task 6: 卡池选卡字段单独读写

**Files：**
- Modify: `backend/internal/cardpool/policy.go`、`backend/internal/handler/cardpool.go`、`backend/internal/server/http.go`
- Test: `backend/internal/handler/cardpool_selection_test.go`（新）

**Interfaces：**
- Produces：
  ```go
  // cardpool 包
  type Selection struct {
      PlanProducts map[string][]string `json:"plan_products"`; MaxUsesPerCard int `json:"max_uses_per_card"`
      HealthWindowHours int `json:"health_window_hours"`; ProductFailThreshold int `json:"product_fail_threshold"`
      CardFailThreshold int `json:"card_fail_threshold"`; AutoSuspend bool `json:"auto_suspend"`
  }
  var SelectionKeys = []string{"plan_products", "max_uses_per_card", "health_window_hours", "product_fail_threshold", "card_fail_threshold", "auto_suspend"}
  func (p Policy) Selection() Selection
  func (p *Policy) ApplySelection(s Selection)
  // handler：GET、PUT /api/v1/admin/cardpool/selection → AdminCardPoolGetSelection、AdminCardPoolPutSelection
  ```
- **`AdminCardPoolPutSelection`**：
  1. 读当前策略；
  2. 用请求体覆盖 6 个选卡字段；
  3. 调用 `SavePolicy`，它会做校验；
  4. 审计 `poolAudit(c, "selection", …)`；
  5. 返回保存后的 `Selection`。
- **`AdminCardPoolPutPolicy`**：
  1. 先把请求体读成 `map[string]json.RawMessage`；
  2. 照旧绑定到 `p`；
  3. 对 `SelectionKeys` 里请求体没出现的键，取当前值（`p.ApplySelection` 只覆盖缺的字段，按字段逐个处理）。

- [ ] **Step 1：写失败的测试**：
  - `TestPutSelectionOnlyTouchesSelection`：
    1. 先存一份策略，`card_name_prefix=POOL2`、`max_fund_cents=12345`；
    2. 用 `PUT /selection` 改 `plan_products` 和 `auto_suspend`；
    3. 期望其他字段不变，选卡字段已更新，审计里有 `selection`。
  - `TestPutPolicyKeepsSelectionWhenAbsent`：
    1. 先用 `PUT /selection` 设好 `plan_products`；
    2. 再用 `PUT /policy` 提交一份不含 6 个选卡键的策略，改 `card_name_prefix`；
    3. 期望 `plan_products` 还在，前缀已改。
  - `TestPutPolicyFullBodyStillApplies`：旧前端提交含 `plan_products:{}` 和 `max_uses_per_card:2` 的完整策略，两项都生效。
  - `TestPutSelectionValidates`：`max_uses_per_card:0` 返回 400，策略不变。
- [ ] **Step 2：** 跑测试，期望失败。
- [ ] **Step 3：** 实现，注册路由。
- [ ] **Step 4：** 测试通过；原有的卡池测试（包括 `TestPutPolicyKeepsReplenishWhenUnselected`）全部通过。
- [ ] **Step 5：提交**：`feat(cardpool): 选卡字段单独读写，策略接口不再覆盖没带的选卡字段`

### Task 7: MarsWallex 选卡规则页，卡池页瘦身

**Files：**
- Create: `frontend/src/views/admin/MarsWallexSelection.vue`
- Modify: `frontend/src/views/admin/CardPool.vue`、`frontend/src/lib/cardpool.ts`、`frontend/src/lib/__tests__/cardpool.test.ts`

**Interfaces：**
- Consumes：Task 4 的 `CardHeadsPanel`、`cardHeadOptionLabel`、`cardHeadsByProduct`；Task 6 的 `/cardpool/selection`。
- Produces：`lib/cardpool.ts` 里新增：
  ```ts
  export const SELECTION_KEYS = ['plan_products','max_uses_per_card','health_window_hours','product_fail_threshold','card_fail_threshold','auto_suspend'] as const
  export function withoutSelection<T extends object>(p: T): Omit<T, (typeof SELECTION_KEYS)[number]>
  ```

**页面内容**：

- **`MarsWallexSelection.vue`**，从上到下：
  1. `CardHeadsPanel provider="marswallex"`；
  2. 说明：只选美元卡（注资按美元计）；支持 3DS 的卡要 MarsWallex Webhook 配通，系统才能自动填验证码；
  3. 各套餐的卡产品优先级：从 `CardPool.vue` 第 351 行起那段原样搬来，选项文字改用卡头标签；
  4. 停用规则：五个字段；
  5. 健康度表和「恢复」按钮：从第 402 行起搬来，`loadHealth`、`resumeProduct` 一并搬走。
  - 保存调用 `PUT /cardpool/selection`。
- **`CardPool.vue`**：
  - 删掉上面搬走的段落、对应的脚本，以及不再用到的 `health`、`addProduct` 等；
  - 标签页「策略与健康度」改名「开卡与补卡策略」，`name` 仍为 `policy`，保证旧链接 `?tab=policy` 照样能打开；
  - `savePolicy` 的请求体改为 `withoutSelection({...})`；
  - 原来的位置放一行提示：「选卡规则（各套餐的卡产品优先级、自动停用、健康度）已移到 MarsWallex → 选卡规则」，带链接。

- [ ] **Step 1：写失败的测试**：`withoutSelection` 去掉 6 个键，保留其他键，不改动原对象。
- [ ] **Step 2：** 运行 `npx vitest run src/lib/__tests__/cardpool.test.ts`，期望失败。
- [ ] **Step 3：** 实现上面的页面改动。
- [ ] **Step 4：** `npx vitest run`、`npm run build` 都通过。
- [ ] **Step 5：提交**：`feat(ui): MarsWallex 选卡规则单独成页，卡池页只留开卡与补卡策略`

### Task 8: 菜单分组、fullTitle、文字

**Files：**
- Modify:
  - `frontend/src/router/index.ts`
  - `frontend/src/layouts/AdminLayout.vue`
  - `frontend/src/layouts/__tests__/AdminLayout.spec.ts`
  - `frontend/src/lib/adminui.ts`
  - `frontend/src/lib/provider.ts`
  - `frontend/src/views/admin/CardProviders.vue`
  - 各服务商页的页头标题
  - `backend/internal/handler/provider_readiness.go`、`backend/internal/handler/provider_admin_test.go`
  - `backend/internal/notify/telegram.go`

**菜单定义**：

- 路由 meta 的顺序、分组和标题按 spec §3 的表；新路由：
  ```ts
  { path: 'marswallex-selection', name: 'MarsWallexSelection', meta: { title: '选卡规则', fullTitle: 'MarsWallex 选卡规则', group: 'MarsWallex', icon: 'Operation' } }
  ```
- `fullTitle` 取值：
  - ZovoCard 组：「ZovoCard 接入配置」「ZovoCard 选卡规则」「ZovoCard 兑换订单」「ZovoCard Webhook」；
  - MarsWallex 组：「MarsWallex 接入配置」「MarsWallex 钱包」「MarsWallex 卡池」「MarsWallex 选卡规则」；
  - 通用分组不设。
- `AdminLayout.vue`：
  - `currentMeta` 多返回 `fullTitle`（没设时等于 `title`）；
  - `document.title` 用 `fullTitle`；
  - 手机上面包屑的页面名显示 `fullTitle`：电脑上显示 `title`，手机上显示 `fullTitle`，用 CSS 切换。

**跟着改的文字**：

- `adminui.ts`：`policy` 标签页改为「开卡与补卡策略」，`swaps` 改为「代理换码」。
- 卡服务商页：
  - `CardProviders.vue` 第 7 行改为「去看各家的选卡规则」；
  - `provider.ts` 新增 `SELECTION_PAGE: Record<string, string> = { zovocard: '/ops/card-selection', marswallex: '/ops/marswallex-selection' }`；
  - `ProviderCard` 上有选卡规则页的显示「选卡规则」链接。
- 页头标题：`CardIntegration.vue`「ZovoCard 接入配置」，`OrderReconcile.vue`「ZovoCard 兑换订单」，`WebhookEvents.vue`「ZovoCard Webhook」，`MarsWallexIntegration.vue`「MarsWallex 接入配置」，`MarsWallexWallet.vue`「MarsWallex 钱包」，`CardPool.vue`「MarsWallex 卡池」。
- 后端：
  - `provider_readiness.go` 的「去 MarsWallex 对接页填写」改为「去 MarsWallex 接入配置页填写」，同步改 `provider_admin_test.go:769`；
  - `telegram.go` 的「请到后台「CDK 兑换订单」处理」改为「请到后台「ZovoCard → 兑换订单」处理」。

- [ ] **Step 1：改测试**：`AdminLayout.spec.ts` 用新的路由表：
  - 分组依次为 `['卡密与付款','ZovoCard','MarsWallex','系统']`，共 4 个；
  - MarsWallex 组的菜单项为 `['接入配置','钱包','卡池','选卡规则']`；
  - 面包屑显示分组「MarsWallex」；
  - 设了 `fullTitle` 的页面，`document.title` 以 `fullTitle` 开头。
- [ ] **Step 2：** 运行 `npx vitest run src/layouts`，期望失败。
- [ ] **Step 3：** 按上面的定义改。
- [ ] **Step 4：** 前端测试和构建通过；运行 `go test ./internal/handler/ -run Readiness ./internal/notify/` 通过。
- [ ] **Step 5：提交**：`feat(ui): 后台菜单按通用能力与服务商分组`

### Task 9: 代理换码设置搬到 CDK 卡密页

**Files：**
- Create: `frontend/src/views/admin/components/AgentSwapSettings.vue`
- Modify: `frontend/src/views/admin/CardIntegration.vue`、`frontend/src/views/admin/CDKeyManagement.vue`

**要求**：

- 把 `CardIntegration.vue` 里代理换码的三项（密码、每日上限、链接）连同对应的脚本逻辑搬进组件：
  - 读 `GET /api/v1/admin/settings` 拿 `agent_swap_password_configured`、`agent_swap_daily_limit`；
  - 保存时只 `PUT` 改动过的 `agent_swap_password`、`agent_swap_daily_limit`；
  - 改上限时的二次确认文字原样保留。
- `CDKeyManagement.vue`：「代理换码核对」标签页改名「代理换码」，上面是 `<AgentSwapSettings/>`，下面是原来的 `<AgentExchangePanel/>`。
- `CardIntegration.vue`：删掉这三项和相关的变量；保存时请求体不再带代理换码的键。

- [ ] **Step 1：** 搜索确认 `CardIntegration.vue` 里已经没有 `agent_swap` 字样，`AgentSwapSettings.vue` 里有。
- [ ] **Step 2：** `npx vitest run`、`npm run build` 都通过。
- [ ] **Step 3：** 在浏览器里验证：在 CDK 卡密 → 代理换码里保存密码和上限，刷新后显示「已设置」和新上限；ZovoCard 接入配置页保存 API Key 时不影响代理换码设置。
- [ ] **Step 4：提交**：`refactor(ui): 代理换码设置移到 CDK 卡密页`

### Task 10: PR 2 收尾

- [ ] CHANGELOG「未发布」：
  - 新增：「MarsWallex 选卡规则页」；
  - 变更：「后台菜单按通用能力与服务商分组」「代理换码设置移到 CDK 卡密 → 代理换码」「卡池页的选卡配置移到 MarsWallex → 选卡规则」。
- [ ] 跑全部本地检查，和 Task 5 相同。
- [ ] 浏览器在电脑和手机两种布局下逐页检查：
  - 菜单分组；
  - 两家的选卡规则页；
  - 卡池的开卡与补卡策略页；
  - CDK 卡密的代理换码页；
  - 旧链接 `/ops/cardpool?tab=policy` 能打开。
- [ ] 推送，开 PR，等 CI 全部通过后用合并提交合并。
