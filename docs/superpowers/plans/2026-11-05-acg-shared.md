# 异次元店铺共享对接 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让代理商在自己的 acg-faka 站「店铺共享」里填本站地址、商户 ID、商户密钥，就能把本站当上游自动取码，按拿货价扣他的美元余额。

**Architecture:** 新建纯函数包 `internal/acgshared`（表单解析、签名、金额、商品对象），在 handler 里加 `/shared/*` 接口（公共中间件做 HTTPS、限流、验签、白名单、状态检查）。把 `AgentPlaceOrder` 的下单核心抽成 `placeAgentOrder`，后台下单和异次元 trade 共用。对接设置、失败记录各一张新表，`agent_orders` 加来源两列。前端加「对接异次元」页、总览红黄条、来源列，管理员详情页加对接区。

**Tech Stack:** Go 1.x + gin + SQLite（`database/sql`）、Vue 3 + TypeScript + vitest。

**Spec:** `docs/superpowers/specs/2026-11-05-acg-shared-design.md`（协议依据 `docs/research/2026-11-05-acg-shared-protocol.md`）

## Global Constraints

- 回复、注释、提交说明、测试名以外的说明文字用简体中文；提交说明末尾写 `Co-Authored-By: <你自己的模型名> <noreply@anthropic.com>`。
- 提交只用 `git add <具体路径>` 和 `git commit -m … -- <路径>`；不用 `git add -A`，不提交 `.claude/`、`.superpowers/`；不 push。
- **现有代理商测试一律不改**：`backend/internal/handler/*_test.go`、`backend/internal/db/*_test.go` 里已有的文件只许新增测试函数，不许改已有断言（第 3 任务的验收条件）。前端已有测试只在第 8 任务按本计划改 `codesCsv` 表头那一条断言。
- 金额全程整数美分；对外格式化只用 `agent.FormatCents`（后端字符串）、`acgshared.Money`（JSON 数字）、`fmtUsd`（前端），不经过浮点。
- 日志、审计、告警、错误信息里不出现商户密钥、`app_key`、`sign`、请求体、完整兑换码、两步验证码。日志只写商户 ID、接口名、对方订单号。
- `/shared/*` 所有业务结果都是 HTTP 200：成功 `{"code":200,"msg":"success","data":…}`，失败 `{"code":0,"msg":"…"}`；只有没实现的路径 404。每个响应带 `Cache-Control: no-store`。
- 面向用户的中文文案按规格和本计划逐字使用。
- 只用标准库和仓库已有依赖。
- 不运行下载的 acg-faka 代码，不调用任何真实业务接口，不做真实付款。
- 后端门禁：`gofmt -l`、`go vet`、`golangci-lint run`、`go test`（排除 paybrowser）、`-race`（handler、db、acgshared 等）；前端：`npx vitest run && npm run build`。
- gitleaks 会把测试字面量里「含 password 的字符串后跟逗号和下一个字符串」当成密钥，测试里别写这种组合。

## Review Focus

1. **后台下单用 `acg:` 开头的请求号**：后台允许 `:`，代理商手填 `acg:123` 会和异次元订单 `acg:123` 撞同一个唯一约束、被当成重放拿走别人的码。后台下单必须拒绝 `acg:` 前缀（第 3 任务测试 `TestAgentPlaceOrder_RejectsACGPrefix`）。
2. **重放时有码解不开**（主密钥换过、密文被改）：异次元会把空行当卡密发给买家。重放里有任何一张 `Unavailable` 就整笔返回 `系统维护中`，不发半批（第 5 任务 `TestSharedTrade_ReplayWithUnavailableCode`）。
3. **反代没配 `TRUSTED_PROXIES` 却带 `X-Forwarded-Proto: https`**：必须照样拒绝，只有真 TLS、可信反代或本机回环才放行（第 4 任务 `TestShared_Errors` 的三行）。
4. **畸形表单**（`sku[a][b]=1`、`sku[]=1`、`a[=1`、同名既是标量又是数组、坏的百分号编码）：返回 `请求格式不正确`，不 panic，不进验签（第 2 任务 `TestParseForm_Invalid`、第 4 任务 `TestShared_Errors`）。
5. **站点名过长或含控制字符**：异次元要求 shopName 去标签后 1–128 字且无控制字符，否则整个店铺连不上。去掉控制字符、只截品牌部分、保留「（美元报价）」后缀（第 4 任务 `TestShared_ConnectShopNameLimits`）。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `backend/internal/db/agent_schema.go`（改） | 建两张新表、`agent_orders` 补两列 |
| `backend/internal/db/agent_shared.go`（新） | 对接设置、失败记录的读写 |
| `backend/internal/db/agent_order.go`（改） | 订单来源两列、`AgentOrderRefs` |
| `backend/internal/acgshared/*.go`（新） | 纯函数：表单解析、签名、金额、商品对象、发货文本 |
| `backend/internal/handler/agent_order_core.go`（新） | `placeAgentOrder` 下单核心和具名错误 |
| `backend/internal/handler/agent_order.go`（改） | `AgentPlaceOrder` 改为调用核心 |
| `backend/internal/handler/acg_shared.go`（新） | `/shared/*` 路由、公共中间件、只读接口 |
| `backend/internal/handler/acg_shared_trade.go`（新） | trade 和失败记录 |
| `backend/internal/handler/acg_shared_settings.go`（新） | 代理商端 `/api/v1/agent/shared*` |
| `backend/internal/handler/acg_shared_admin.go`（新） | 管理员端对接查看和开关 |
| `backend/internal/server/http.go`（改） | 挂 `/shared` 路由 |
| `frontend/src/lib/agentShared.ts`（新） | 前端纯函数：状态、横条文案、白名单解析、来源标签 |
| `frontend/src/views/agent/AgentIntegration.vue`（新） | 「对接异次元」页 |
| 其余前端页面（改） | 总览横条、来源列、导出列、管理员对接区 |

---

### Task 1: 数据层：两张新表、订单来源列

**Files:**
- Modify: `backend/internal/db/agent_schema.go`（`MigrateAgents`）
- Modify: `backend/internal/db/agent_order.go`
- Create: `backend/internal/db/agent_shared.go`
- Test: `backend/internal/db/agent_shared_test.go`

**Interfaces:**
- Produces（后续任务都按这些名字用）：

```go
const (
    AgentOrderSourceWeb = "web"
    AgentOrderSourceACG = "acg"
)
// AgentOrder、NewAgentOrder 各加两个字段：
//   Source, ExternalRef string   // NewAgentOrder.Source 为空时按 "web" 写入
type AgentOrderRef struct{ ProductCode, Source, ExternalRef string }
func AgentOrderRefs(ctx context.Context, agentID int64) (map[int64]AgentOrderRef, error) // 这个代理商全部订单，键是订单 ID

type AgentSharedCreds struct {
    AgentID         int64
    Exists          bool     // 表里有没有这一行；没有时其余字段为零值
    AppID           string   // NULL 读成 ""
    KeySealed       string
    KeyCreatedAt    int64
    LastUsedAt      int64
    AdminDisabled   bool
    IPAllowlist     []string // 库里逗号分隔；空串读成 nil
    LowBalanceCents int64
    CreatedAt, UpdatedAt int64
}
var (
    ErrAgentSharedNotFound      = errors.New("商户 ID 不存在")
    ErrAgentSharedAdminDisabled = errors.New("对接已被管理员关闭")
)
func GetAgentSharedCreds(ctx context.Context, agentID int64) (AgentSharedCreds, error)          // 没有行：Exists=false、AgentID 填好、err=nil
func GetAgentSharedCredsByAppID(ctx context.Context, appID string) (AgentSharedCreds, error)    // 找不到：ErrAgentSharedNotFound
func SetAgentSharedKey(ctx context.Context, agentID int64, appID, keySealed string, now time.Time) error // 不存在则插入；admin_disabled=1 时不改并返回 ErrAgentSharedAdminDisabled
func DisableAgentShared(ctx context.Context, agentID int64, now time.Time) error               // app_id=NULL、key_sealed=''、key_created_at=0；没有行时什么也不做
func SetAgentSharedAdminDisabled(ctx context.Context, agentID int64, disabled bool, now time.Time) error // 不存在则插入；不动密钥
func SetAgentSharedAllowlist(ctx context.Context, agentID int64, ips []string, now time.Time) error      // 不存在则插入；按给定顺序逗号拼接
func SetAgentSharedLowBalance(ctx context.Context, agentID int64, cents int64, now time.Time) error     // 不存在则插入
func TouchAgentSharedUsed(ctx context.Context, agentID int64, now time.Time) error // 只在 last_used_at <= now-60 秒时更新

type NewSharedFailure struct {
    AgentID     int64
    RequestNo   string // 对方原始单号，不带 acg:
    ProductCode string // 调用方已截到 64 字
    Quantity    int
    Reason, Message string
}
type AgentSharedFailure struct {
    ID, AgentID     int64
    RequestNo, ProductCode string
    Quantity        int
    Reason, Message string
    Attempts        int
    FirstAt, LastAt int64
    ResolvedOrderID *int64
}
func RecordAgentSharedFailure(ctx context.Context, f NewSharedFailure, now time.Time) error // 同 (agent_id, request_no) 已有：attempts+1，更新商品、数量、原因、msg、last_at，resolved_order_id 置 NULL
func ResolveAgentSharedFailure(ctx context.Context, agentID int64, requestNo string, orderID int64) error // 只改 resolved_order_id IS NULL 的行；没有行不报错
func ListAgentSharedFailures(ctx context.Context, agentID, since int64, limit, offset int) ([]AgentSharedFailure, int, error) // last_at >= since，last_at 降序、id 降序
func CountAgentSharedFailures(ctx context.Context, agentID, since int64) (total, insufficient int, err error) // 只数未解决的
func PruneAgentSharedFailures(ctx context.Context, before int64) (int64, error) // 删 last_at < before
```

表结构（`CREATE TABLE IF NOT EXISTS`，放进 `MigrateAgents` 的 `stmts`）：
- `agent_shared_creds`：列和约束逐字按规格 5.1（`agent_id INTEGER PRIMARY KEY`、`app_id TEXT UNIQUE`、`key_sealed TEXT NOT NULL DEFAULT ''`、`key_created_at INTEGER NOT NULL DEFAULT 0`、`last_used_at INTEGER NOT NULL DEFAULT 0`、`admin_disabled INTEGER NOT NULL DEFAULT 0`、`ip_allowlist TEXT NOT NULL DEFAULT ''`、`low_balance_cents INTEGER NOT NULL DEFAULT 0 CHECK (low_balance_cents >= 0)`、`created_at`、`updated_at INTEGER NOT NULL`）。
- `agent_shared_failures`：规格 5.3 的列，`reason TEXT NOT NULL CHECK (reason IN ('insufficient_balance','not_ready','product_unavailable','request_conflict','rate_limited','busy','invalid_request'))`，`UNIQUE (agent_id, request_no)`，另建索引 `idx_agent_shared_failures_last ON agent_shared_failures(last_at)`。
- `agent_orders` 补列沿用 `local_cdks` 补列的 `pragma_table_info` 写法：`source TEXT NOT NULL DEFAULT 'web' CHECK (source IN ('web','acg'))`、`external_ref TEXT NOT NULL DEFAULT ''`。

- [ ] **Step 1: 写失败的测试** `backend/internal/db/agent_shared_test.go`，用 `dbtest.Open(t)`：
  - `TestMigrateAgents_AddsOrderSourceColumns`：`DROP TABLE agent_orders`，按旧结构（规格 5.2 之前、`agent_schema.go` 里现在的 `CREATE TABLE agent_orders`，不含两列）重建并插一行，调 `MigrateAgents()` 两次都不报错；这一行 `source='web'`、`external_ref=''`；再 `INSERT … source='x'` 报错。
  - `TestCreateAgentOrder_Source`：`NewAgentOrder.Source` 为空建单后读回 `Source=="web"`；`Source:"acg", ExternalRef:"202611050000000001"` 读回一致（`GetAgentOrderByRequestNo`、`ListAgentOrders` 都要带）。
  - `TestAgentOrderRefs`：两笔订单（web、acg）→ map 里两项，ProductCode、Source、ExternalRef 正确；别的代理商的订单不在里面。
  - `TestAgentSharedCreds_Lifecycle`：没有行时 `Exists=false`；`SetAgentSharedKey(aid,"agAAAAAAAAAAAAAA","s1",t0)` 后按商户 ID 查得到、`KeyCreatedAt==t0`；同一商户 ID 再设 `"s2"` 后 `KeySealed=="s2"`；`DisableAgentShared` 后按旧商户 ID 查得 `ErrAgentSharedNotFound`、`GetAgentSharedCreds` 的 `AppID==""`、`KeySealed==""`；两个代理商都停用后（两行 app_id 都是 NULL）不报唯一约束错误。
  - `TestAgentSharedCreds_AdminDisabled`：设了密钥后 `SetAgentSharedAdminDisabled(true)` → 按商户 ID 仍查得到、`AdminDisabled==true`、`KeySealed` 不变；此时 `SetAgentSharedKey` 返回 `ErrAgentSharedAdminDisabled` 且密钥不变；`false` 后可以再设。
  - `TestAgentSharedCreds_AllowlistAndLowBalance`：`[]string{"1.2.3.4","10.0.0.0/8"}` 读回同样顺序；`nil` 读回 `nil`；`SetAgentSharedLowBalance(-1)` 报错（CHECK）。
  - `TestTouchAgentSharedUsed_OncePerMinute`：t0 写入；t0+30s 不变；t0+61s 变成 t0+61s。
  - `TestAgentSharedFailures`：同号记两次 → `Attempts==2`、原因是第二次的；`CountAgentSharedFailures(since)` 只数未解决且 `last_at>=since` 的，`insufficient` 只数 `insufficient_balance`；`Resolve` 后不再计数、`ResolvedOrderID` 有值；之后同号再失败 → `ResolvedOrderID==nil`、重新计数；`List` 按 `last_at` 降序并返回总数；`Prune(before)` 只删更早的行并返回删除数。

- [ ] **Step 2: 跑测试确认失败**
  Run: `cd backend && go test ./internal/db/ -run 'MigrateAgents_AddsOrderSource|CreateAgentOrder_Source|AgentOrderRefs|AgentShared|TouchAgentShared' -count=1`
  Expected: 编译失败（`AgentOrderSourceWeb`、`GetAgentSharedCreds` 等未定义）

- [ ] **Step 3: 实现** 上面 Produces 的全部函数和迁移。`agentOrderCols`、`scanAgentOrder`、`CreateAgentOrder` 的 INSERT 补两列；upsert 用 `INSERT … ON CONFLICT(agent_id) DO UPDATE …`，`SetAgentSharedKey` 的 `DO UPDATE` 带 `WHERE admin_disabled = 0`，受影响行数为 0 且行存在时返回 `ErrAgentSharedAdminDisabled`（新插入时 admin_disabled 一定是 0）。

- [ ] **Step 4: 跑测试确认通过**
  Run: `cd backend && go test ./internal/db/ -count=1`
  Expected: PASS（含原有全部 db 测试）

- [ ] **Step 5: 提交**
```bash
git add backend/internal/db/agent_shared.go backend/internal/db/agent_shared_test.go
git commit -m "feat(db): 异次元对接设置与取码失败记录两张表，代理商订单加来源列" -- backend/internal/db/agent_shared.go backend/internal/db/agent_shared_test.go backend/internal/db/agent_schema.go backend/internal/db/agent_order.go
```

---

### Task 2: 纯函数包 `internal/acgshared`

**Files:**
- Create: `backend/internal/acgshared/form.go`、`sign.go`、`money.go`、`item.go`
- Test: `backend/internal/acgshared/acgshared_test.go`

**Interfaces:**
- Consumes: 无（只用标准库）
- Produces：

```go
type SubField struct{ Key, Value string }
type Field struct {
    Key   string
    Value string     // 标量值；Sub != nil 时不用
    Sub   []SubField // 嵌套数组（如 sku[颜色]=红），按收到顺序；同一子键重复时值以最后一个为准、位置保持第一次出现处
}
type Form struct{ Fields []Field } // 顶层字段按第一次出现的顺序；同名顶层标量以最后一个为准
var ErrBadForm = errors.New("请求格式不正确")
func ParseForm(body []byte) (Form, error)
func (f Form) Get(key string) (string, bool) // 只取标量；嵌套或不存在返回 "", false
func (f Form) Has(key string) bool           // 标量或嵌套都算
func Sign(f Form, key string) string         // md5 小写十六进制
func Verify(f Form, key string) bool         // 取标量 sign，与 Sign 结果 subtle.ConstantTimeCompare；sign 缺失或是嵌套返回 false

type Money int64                             // 美分
func (m Money) MarshalJSON() ([]byte, error) // 325 → 3.25，5 → 0.05，0 → 0.00；不经过浮点

const MaxQuantity = 200                      // 与 cardplatform.MaxIssueCount 相同，测试里断言二者相等
func ValidRequestNo(s string) bool           // ^[A-Za-z0-9]{1,64}$
func ParseQuantity(s string) (int, bool)     // 十进制整数且 1..MaxQuantity

const ItemDescription = "<p>购买后获得兑换码，卡密里附有兑换链接，打开即可兑换。</p>"
type ItemInput struct {
    ID         int64
    Code, Name string
    Sort       int
    PriceCents int64
    Stock      int
}
type Item struct { /* JSON 字段见规格 7.4，整数字段用 int */ }
func NewItem(in ItemInput) Item
type Category struct {
    ID       int    `json:"id"`
    Name     string `json:"name"`
    Sort     int    `json:"sort"`
    Icon     string `json:"icon"`
    Status   int    `json:"status"`
    PID      int    `json:"pid"`
    Children []Item `json:"children"`
}
func AllCategory(items []Item) []Category // 恰好一个分类：{1,"全部商品",0,"",1,0,items}；items 为 nil 时 children 输出 []
func SecretText(host string, codes []string) string // 每行 `码  兑换：https://<host>/recharge?code=<url.QueryEscape(码)>`（码和「兑换：」之间两个半角空格），行间 "\n"，首尾无换行
```

解析规则（PHP `parse_str` 的子集）：按 `&` 切分，空段跳过；每段按第一个 `=` 分键值，没有 `=` 时值为空串；键和值都按 PHP `urldecode` 还原（`+` → 空格，`%XX` → 字节），坏的 `%` 编码返回 `ErrBadForm`。键形如 `名[子键]`（名和子键都非空、都不含 `[` `]`）是嵌套；键里其他任何 `[` `]` 用法（`a[]`、`a[b][c]`、`a[`、`a]`）返回 `ErrBadForm`；同一个名既出现标量又出现嵌套返回 `ErrBadForm`。

签名规则（调研第 7 条）：去掉顶层 `sign`；顶层键按字节序排序；删掉值恰为空串的顶层标量（嵌套里的空串保留）；标量写 `键=值`，嵌套逐项写 `名[子键]=值`；用 `&` 连接，末尾加 `&key=<密钥>`，md5 小写。

- [ ] **Step 1: 写失败的测试**（数值逐字照抄）
  - `TestSign_Vectors`，密钥都是 `Key32`：

| 请求体 | 期望 sign |
|---|---|
| `app_id=ag0123456789abcd&app_key=Key32&code=mw-plus-ph&num=2&race=&sign=whatever` | `54437c21b855c09186c62ce29780343e` |
| `shared_code=mw-plus-ph&num=1&card_id=0&password=&race=&app_id=ag0123456789abcd&app_key=Key32` | `d2681f16ecd11d47540603c66bdcd975` |
| `app_id=ag0123456789abcd&app_key=Key32&code=x&sku%5B%E9%A2%9C%E8%89%B2%5D=%E7%BA%A2&sku%5B%E5%B0%BA%E5%AF%B8%5D=&sku%5BA%5D=1` | `c9e1f9e9bb51e70a1e07331b10bd6fd1` |
| `code=a%26b%3Dc%2Bd+%E4%B8%AD%E6%96%87&app_key=Key32&app_id=ag0123456789abcd` | `b99c1131176af2d4c1ba1d46dc1f6e4f` |

    （对应的规范串依次是 `app_id=ag0123456789abcd&app_key=Key32&code=mw-plus-ph&num=2&key=Key32`、`app_id=…&app_key=Key32&card_id=0&num=1&shared_code=mw-plus-ph&key=Key32`、`app_id=…&app_key=Key32&code=x&sku[颜色]=红&sku[尺寸]=&sku[A]=1&key=Key32`、`app_id=…&app_key=Key32&code=a&b=c+d 中文&key=Key32`，写进测试注释。）
  - `TestVerify`：第一行请求体把 `sign=whatever` 换成正确值 → true；换成大写 → false；去掉 sign → false；`sign%5Ba%5D=x` → false。
  - `TestParseForm_Order`：`b=2&a=1&sku%5Bx%5D=1&sku%5By%5D=2&sku%5Bx%5D=3&a=9` → `Fields` 顺序 `b, a, sku`；`Get("a")=="9"`；sku 的 `Sub` 是 `[{x 3} {y 2}]`；`Get("sku")` 返回 false、`Has("sku")` 为 true；`k`（无等号）→ `Get("k")==""` 且存在。
  - `TestParseForm_Invalid`：`%zz=1`、`a=%E4%B`、`a%5B%5D=1`、`a%5Bb%5D%5Bc%5D=1`、`a%5B=1`、`sku=1&sku%5Ba%5D=2` 都返回 `ErrBadForm`；空请求体返回空 Form、无错误。
  - `TestMoney`：`json.Marshal(struct{P Money}{325})` == `{"P":3.25}`；5 → `0.05`；0 → `0.00`；100000 → `1000.00`。
  - `TestValidRequestNo`、`TestParseQuantity`：`202611050000000001` 真；64 位真、65 位假；空、`a-b`、`中` 假。数量 `1`、`200` 真；`0`、`201`、`1.5`、`abc`、`""`、`+1` 假。`MaxQuantity == cardplatform.MaxIssueCount`。
  - `TestNewItem`：`NewItem(ItemInput{ID:7, Code:"mw-plus-ph", Name:"Plus 菲律宾", Sort:3, PriceCents:325, Stock:200})` 序列化后解成 `map[string]any`，逐项断言规格 7.4 的全部字段：`id 7`、`code`、`name`、`description==ItemDescription`、`cover ""`、`price 3.25`、`factory_price 3.25`、`user_price 0`、`stock 200`、`config ""`、`widget "[]"`、`delivery_way/contact_type/password_status/minimum/maximum/only_user/purchase_count/draft_status/draft_premium/inventory_hidden/seckill_status` 都是 0、`sort 3`、`status 1`、`category_id 1`；`Name:""` → name 用 code；300 个汉字的名字 → 255 个字。
  - `TestAllCategory`：`AllCategory(nil)` 序列化为 `[{"id":1,"name":"全部商品","sort":0,"icon":"","status":1,"pid":0,"children":[]}]`。
  - `TestSecretText`：`SecretText("shop.example", []string{"AB+C","XYZ"})` == `"AB+C  兑换：https://shop.example/recharge?code=AB%2BC\nXYZ  兑换：https://shop.example/recharge?code=XYZ"`。

- [ ] **Step 2: 跑测试确认失败**
  Run: `cd backend && go test ./internal/acgshared/ -count=1`
  Expected: 编译失败（包里没有实现）

- [ ] **Step 3: 实现** 上面的全部导出。`Money` 用整数除法和 `%02d` 拼字符串；名字截断按 rune。

- [ ] **Step 4: 跑测试确认通过**
  Run: `cd backend && go test ./internal/acgshared/ -count=1 -race`
  Expected: PASS

- [ ] **Step 5: 提交**
```bash
git add backend/internal/acgshared
git commit -m "feat(acgshared): 异次元共享协议的表单解析、签名、金额和商品对象" -- backend/internal/acgshared
```

---

### Task 3: 抽出下单核心 `placeAgentOrder`

**Files:**
- Create: `backend/internal/handler/agent_order_core.go`
- Modify: `backend/internal/handler/agent_order.go`（`AgentPlaceOrder`、`agentOrderBind`、`agentOrderReplay`、`agentOrderCodes`）
- Test: `backend/internal/handler/agent_order_core_test.go`

**Interfaces:**
- Consumes: Task 1 的 `db.NewAgentOrder.Source/ExternalRef`、`db.AgentOrderSourceWeb`
- Produces：

```go
type agentOrderInput struct {
    AgentID                int64
    Username               string // 写 issued_by 和流水操作人："agent:" + Username
    ProductCode            string
    Quantity               int
    RequestNo              string // 已带前缀
    Note                   string
    ExpectedUnitPriceCents int64  // <= 0 不比价
    Source, ExternalRef    string
}
type agentOrderOutcome struct {
    Order    db.AgentOrder
    Codes    []exportedCode
    Replayed bool
}
func placeAgentOrder(ctx context.Context, in agentOrderInput) (agentOrderOutcome, error)
func loadAgentOrderCodes(ctx context.Context, agentID int64, o db.AgentOrder) ([]exportedCode, error) // agentOrderCodes 的无 gin 版本，带状态和 replaced_by

// 具名错误（handler 用 errors.Is / errors.As 区分）
var (
    errAgentOrderNoKey    = errors.New("主密钥不可用")
    errAgentOrderBusy     = errors.New("数据库忙")
    errAgentOrderSeal     = errors.New("码加密失败")   // 现有行为：503 maintenance，不发主密钥告警
    errAgentOrderInternal = errors.New("生成码失败")   // 现有行为：500 internal
    errProductUnavailable = errors.New("商品不可售")   // 不存在、下架、取不到价
    errProductNotReady    = errors.New("商品暂时无法出码") // 含就绪检查被 ctx 打断
    errRequestConflict    = errors.New("请求号已用于另一笔订单")
)
type agentOrderNoKeyError struct{ Kind string }          // Is(errAgentOrderNoKey) 为真；Kind 来自 secretErrKind
type agentOrderBusyError struct{ Op string; Err error }  // Is(errAgentOrderBusy) 为真；Unwrap 返回 Err
type priceChangedError struct{ CurrentCents int64 }
// 另外原样透传：*db.InsufficientBalanceError、db.ErrAgentDisabled、db.ErrAgentNotFound

var agentReadyCheck = agentProductReady // 测试可替换
func agentProductReadyCtx(ctx context.Context, p db.AgentProduct) (provider.CardSupply, bool) // 另起协程调 agentReadyCheck，ctx 先结束就返回 (nil,false)；结果通道带 1 个缓冲
```

步骤顺序与现在的 `AgentPlaceOrder` 第 3–7 步完全一致（主密钥 → `agentOrderLock` → 查重放 → 商品 → 取价、`ExpectedUnitPriceCents > 0` 时比价 → `agentProductReadyCtx` → `agentOrderPausePoint` → 生成并加密 → `db.CreateAgentOrder` 后立即 `release()` → 撞 `ErrAgentOrderExists` 时调 `agentOrderCollided` 再查重放）。重放时取码出现 `*agentCodesNoKeyError` 转成 `*agentOrderNoKeyError`。审计不在核心里写。

`AgentPlaceOrder` 改成：限流 → `agentOrderBind` → `placeAgentOrder(c.Request.Context(), …Source: db.AgentOrderSourceWeb)` → 按错误写出**和现在完全相同**的状态码、`error` 编号、`message`、附加字段（`current_unit_price_cents`、`shortfall_cents`）→ 重放写 `agent_codes_export`（`order=%d count=%d replay=true`）、新单写 `agent_order`（detail 不变）→ 返回 JSON。`agentOrderBind` 在 `ValidRequestNo` 通过后加一条：`strings.HasPrefix(req.RequestNo, "acg:")` → 400 `invalid_request`「请求号不能以 acg: 开头」。`agentOrderCodes` 改为调用 `loadAgentOrderCodes` 再映射错误。

- [ ] **Step 1: 写失败的测试** `agent_order_core_test.go`，用 `newAgentEnv(t)` + `newOrderFix(t, e, true)`（商品 `mw-plus-ph` 单价 500，代理商 alice 余额 1000）：
  - `TestPlaceAgentOrder_ACGSource`：直接调核心，`RequestNo:"acg:202611050000000001", ExpectedUnitPriceCents:0, Source:"acg", ExternalRef:"202611050000000001", Quantity:1` → `Replayed=false`、1 张码、`Order.Source=="acg"`、`Order.ExternalRef` 一致、余额 500。
  - `TestPlaceAgentOrder_ReplayAndConflict`：同号再调 → `Replayed=true`、码的 ID 和明文与第一次相同、余额不变；同号 `Quantity:2` → `errors.Is(err, errRequestConflict)`。
  - `TestPlaceAgentOrder_PriceCheck`：`ExpectedUnitPriceCents:499` → `errors.As(err, &pc)` 且 `pc.CurrentCents==500`；`0` → 按 500 扣。
  - `TestPlaceAgentOrder_ReadyCtxCancelled`：`agentReadyCheck` 换成睡 1 秒再返回就绪的函数（`t.Cleanup` 换回）；ctx 20ms 超时 → `errors.Is(err, errProductNotReady)`，没建单、余额不变。
  - `TestAgentPlaceOrder_RejectsACGPrefix`：后台接口 `request_no:"acg:1"` → 400、`error=="invalid_request"`、`message=="请求号不能以 acg: 开头"`、没建单。

- [ ] **Step 2: 跑测试确认失败**
  Run: `cd backend && go test ./internal/handler/ -run 'PlaceAgentOrder_|RejectsACGPrefix' -count=1`
  Expected: 编译失败（`placeAgentOrder` 未定义）

- [ ] **Step 3: 实现** 核心和 `AgentPlaceOrder` 改写。

- [ ] **Step 4: 跑测试确认通过，且已有测试未改**
  Run: `cd backend && go test ./internal/handler/ -count=1 -race && git diff --stat <BASE> -- 'backend/internal/handler/*_test.go'`（`<BASE>` 是本任务开始前的 HEAD）
  Expected: 全部 PASS；diff 只列出新文件 `agent_order_core_test.go`

- [ ] **Step 5: 提交**
```bash
git add backend/internal/handler/agent_order_core.go backend/internal/handler/agent_order_core_test.go
git commit -m "refactor(agent): 抽出下单核心 placeAgentOrder，后台下单拒绝 acg: 前缀请求号" -- backend/internal/handler/agent_order_core.go backend/internal/handler/agent_order_core_test.go backend/internal/handler/agent_order.go
```

---

### Task 4: `/shared/*` 公共流程和只读接口

**Files:**
- Create: `backend/internal/handler/acg_shared.go`
- Modify: `backend/internal/server/http.go`（在 `api := r.Group("/api/v1")` 之前加 `handler.RegisterSharedRoutes(&r.RouterGroup)`）
- Modify: `backend/internal/handler/agent_env_test.go` 的 `agentEnvOn`：只**新增**两行 `RegisterSharedRoutes(&r.RouterGroup)` 和 `resetSharedState()`（含 Cleanup），不改其他行
- Test: `backend/internal/handler/acg_shared_test.go`

**Interfaces:**
- Consumes: Task 1 的 creds 函数；Task 2 的 `ParseForm/Verify/Money/NewItem/AllCategory/ParseQuantity`；Task 3 的 `agentProductReadyCtx`
- Produces：

```go
func RegisterSharedRoutes(r *gin.RouterGroup) // g := r.Group("/shared", SharedAuth())；挂 8 个 POST：
// authentication/connect、commodity/items、commodity/item、commodity/stock、commodity/valuation、
// commodity/inventory、commodity/inventoryState、commodity/trade（trade 由 Task 5 实现，这里先挂一个返回 sharedFail(c, sharedMsgBusy) 的占位）
func SharedAuth() gin.HandlerFunc
func sharedOK(c *gin.Context, data any) // 200 {"code":200,"msg":"success","data":data}
func sharedFail(c *gin.Context, msg string) // 200 {"code":0,"msg":msg} 并 Abort
func sharedForm(c *gin.Context) acgshared.Form      // c.Get("acg_form")
func sharedCredsOf(c *gin.Context) db.AgentSharedCreds // c.Get("acg_creds")
func sharedRateAllow(key string, limit int, now time.Time) bool // 1 分钟固定窗口；结构照 localRateAllow（map + container/list，最多 20000 个键，满了淘汰最早的）
func resetSharedState() // 清空限流表、验签失败计数，给测试用
var (
    sharedNow          = func() time.Time { return agentNow() } // 测试替换 agentNow 后跟着 e.clock 走
    sharedReadyTimeout = 10 * time.Second
    sharedSignFails    = newFailLimiter(10, 15*time.Minute, 20000) // 测试替换 .now
)
func sharedReady(ctx context.Context, p db.AgentProduct) bool // 套 sharedReadyTimeout 调 agentProductReadyCtx
func sharedLookupProduct(c *gin.Context, code string) (db.AgentProduct, int64, bool) // 写好「商品不存在」/「当前商品已停售」/「系统繁忙，请稍后再试」，返回商品和这个代理商的单价
func sharedRejectSpec(c *gin.Context) bool // race 非空或带 sku → sharedFail「该商品没有可选规格」，返回 true

const (
    sharedMsgHTTPS     = "请使用 https 地址"
    sharedMsgRate      = "请求太频繁，请稍后再试"
    sharedMsgBadForm   = "请求格式不正确"
    sharedMsgNoApp     = "商户ID不存在"
    sharedMsgBadSign   = "密钥错误"
    sharedMsgIP        = "来源 IP 不在白名单"
    sharedMsgAdminOff  = "对接已被管理员关闭"
    sharedMsgAgentOff  = "代理商账号已停用"
    sharedMsgMaint     = "系统维护中"
    sharedMsgBusy      = "系统繁忙，请稍后再试"
    sharedMsgNoProduct = "商品不存在"
    sharedMsgOffSale   = "当前商品已停售"
    sharedMsgNoSpec    = "该商品没有可选规格"
    sharedMsgNoStock   = "库存不足"
    sharedMsgQty       = "购买数量需为 1–200"
    sharedShopSuffix   = "（美元报价）"
)
```

`SharedAuth` 顺序（规格 7.1）：先设 `Cache-Control: no-store` →
1. `auth.RequestIsHTTPS(c.Request)` 为假，且 `RemoteAddr` 的主机不是回环地址 → `sharedMsgHTTPS`；
2. `sharedRateAllow("ip|"+c.ClientIP(), 120, now)` → 否则 `sharedMsgRate`；`Content-Type` 的媒体类型不是 `application/x-www-form-urlencoded` → `sharedMsgBadForm`；`http.MaxBytesReader` 64 KB，读超了 → `sharedMsgBadForm`；
3. `acgshared.ParseForm` 失败 → `sharedMsgBadForm`；
4. `secretbox.LoadKey()` 失败 → `sharedMsgMaint`（每天告警一次，复用 `agentOrderNoKey` 的告警文字）。规格把主密钥检查放在第 8 步，但验签前要先用主密钥解出商户密钥，所以提到这里；
5. `ipKey := "ip|"+ClientIP`：`sharedSignFails.allowed(ipKey)` 为假 → `sharedMsgBadSign`；`app_id` 不是标量、查不到、`KeySealed==""`、或 `secretbox.Open(key, "acg-shared:<agent_id>:<app_id>", KeySealed)` 失败 → `sharedSignFails.failed(ipKey)`，返回 `sharedMsgNoApp`；
6. `pairKey := app_id+"|"+ClientIP`：`allowed(pairKey)` 为假 → `sharedMsgBadSign`；`acgshared.Verify` 失败 → `failed(pairKey)`，`sharedMsgBadSign`；通过 → `succeeded(pairKey)`；
7. 白名单非空且 `ClientIP` 不在任何一项内（`netip.ParseAddr(...).Unmap()`，项为单个地址则相等，为网段则 `Contains`）→ `sharedMsgIP`；
8. `AdminDisabled` → `sharedMsgAdminOff`；`db.GetAgent` 找不到 → `sharedMsgNoApp`，`Status != db.AgentActive` → `sharedMsgAgentOff`，读库失败 → `sharedMsgBusy`；
9. `db.TouchAgentSharedUsed`（出错只记日志）；`c.Set("agent_id", …)`、`c.Set("agent_username", …)`、`c.Set("acg_creds", creds)`、`c.Set("acg_form", form)`、`c.Set("acg_app_key", 明文密钥)`（只给 Task 5 的日志脱敏测试用来比对，不写日志）→ `c.Next()`。

只读接口（参数名按规格 7.3；读商品码时 `item` 先取 `code` 再取 `sharedCode`）：
- connect：`shopName` = 去掉控制字符的 `settingOr("brand_name","Recharge Portal")`，按 rune 截到 `128-len([]rune(sharedShopSuffix))` 再接后缀；`balance` = `acgshared.Money(max(0, BalanceCents))`。
- items：`db.ListAgentPurchasable` → 整个接口共用一个 `sharedReadyTimeout` 的 ctx，同一「服务商|套餐|地区」只查一次就绪（照 `AgentListProducts`），超时后剩下的按未就绪；`stock` 就绪 200 否则 0；用 `AllCategory`。
- item：单个 `NewItem`；stock/valuation/inventory/inventoryState 先 `sharedRejectSpec`。
- valuation：`num` 用 `acgshared.ParseQuantity`，不合法 → `sharedMsgQty`；`{"price": Money(单价*num), "currency_code":"USD"}`。
- inventory：`{"count": 就绪?200:0, "delivery_way":0, "draft_status":0, "price":Money(单价), "user_price":0, "config":"", "factory_price":Money(单价), "is_category":0}`。
- inventoryState：就绪且数量合法 → `sharedOK(c, gin.H{})`；否则 `sharedMsgNoStock`。

- [ ] **Step 1: 写失败的测试** `acg_shared_test.go`。先写夹具：
  - `newSharedFix(t *testing.T, e *agentEnv, ready bool) *sharedFix`：在 `newOrderFix(t, e, ready)` 之上，用 `secretbox.Seal` 和 `db.SetAgentSharedKey` 给 alice 开通，字段 `appID`、`key`；
  - `(f *sharedFix) post(path string, fields [][2]string, opts ...sharedOpt) *httptest.ResponseRecorder`：按给定顺序 `url.QueryEscape` 编码，自动在末尾补 `app_id`、`app_key`，再按 `acgshared.Sign` 补 `sign`；默认 `req.TLS = &tls.ConnectionState{}`、`RemoteAddr = "203.0.113.7:5000"`、`Content-Type: application/x-www-form-urlencoded`；选项：`noTLS()`、`from(addr)`、`header(k,v)`、`rawBody(s)`、`signWith(key)`、`appID(id)`；
  - `decodeShared(t, w) (code int, msg string, data json.RawMessage)`，同时断言 HTTP 200 和 `Cache-Control: no-store`。
  测试：
  - `TestShared_Connect`：`shopName=="Recharge Portal（美元报价）"`、`balance` 的原始 JSON 是 `10.00`。
  - `TestShared_ConnectShopNameLimits`：`brand_name` 设成 200 个「店」加一个 `\x07` → shopName 恰好 128 个字、以「（美元报价）」结尾、不含控制字符。
  - `TestShared_Items`：一个分类 `id 1`「全部商品」；children 里 `mw-plus-ph` 的 `price` 为 `5.00`、`stock` 为 200；下架商品、没定价的商品不在里面；未就绪夹具 → stock 0。
  - `TestShared_Item`：`code` 和 `sharedCode` 都能取到；不存在 → `商品不存在`；下架 → `当前商品已停售`。
  - `TestShared_StockAndSpec`：就绪 200、未就绪 0；`race=x` 或 `sku[a]=b` → `该商品没有可选规格`。
  - `TestShared_ReadyTimeout`：`sharedReadyTimeout=20ms`，`agentReadyCheck` 睡 1 秒 → stock 0、inventoryState `库存不足`。
  - `TestShared_Valuation`：`num=3` → `price` 原始 JSON `15.00`、`currency_code=="USD"`；`num` 为 `0`、`201`、`abc` → `购买数量需为 1–200`。
  - `TestShared_Inventory`、`TestShared_InventoryState`（`num=201` → `库存不足`）。
  - `TestShared_Errors`（表驱动，都是 HTTP 200 + code 0）：无 TLS 来自 `203.0.113.7` → `请使用 https 地址`；无 TLS、带 `X-Forwarded-Proto: https`、对端不在 `TRUSTED_PROXIES` → `请使用 https 地址`；无 TLS 来自 `127.0.0.1:5000` → code 200；未知 app_id → `商户ID不存在`；签名用错密钥 → `密钥错误`；`Content-Type: application/json` → `请求格式不正确`；请求体 65 KB → `请求格式不正确`；`rawBody("a%5B%5D=1&app_id=…")` → `请求格式不正确`；白名单 `198.51.100.0/24` 时来自 `203.0.113.7` → `来源 IP 不在白名单`、来自 `198.51.100.9` → 200；管理员关闭 → `对接已被管理员关闭`；代理商被停用 → `代理商账号已停用`；主密钥清空 → `系统维护中`；密钥用另一把主密钥加密 → `商户ID不存在`。
  - `TestShared_SignLockout`：同 IP 错签 10 次后第 11 次用对的签名仍是 `密钥错误`；换个 IP 用对的签名 → 200；`sharedSignFails.now` 推后 15 分钟零 1 秒 → 原 IP 恢复。
  - `TestShared_UnknownAppIDLocksIP`：同一 IP 用 10 个不存在的 app_id 后，用正确的 app_id 和签名 → `密钥错误`；另一个 IP 正常。
  - `TestShared_IPRateLimit`：同一 IP 第 121 次 → `请求太频繁，请稍后再试`；`e.clock` 推后 1 分钟恢复。
  - `TestShared_Unimplemented404`：POST `/shared/commodity/draft`、`/shared/commodity/draftCard`、`/shared/commodity/query` → HTTP 404。
  - `TestShared_LastUsed`：第一次成功后 `LastUsedAt == e.clock`；30 秒后再请求不变。
  - `TestShared_LogNoSecrets`：`log.SetOutput` 捕获一次错签和一次成功的 connect，输出不含 `f.key`、不含 sign 值、不含请求体里的 `app_key=`。

- [ ] **Step 2: 跑测试确认失败**
  Run: `cd backend && go test ./internal/handler/ -run 'TestShared_' -count=1`
  Expected: 编译失败（`RegisterSharedRoutes` 未定义）

- [ ] **Step 3: 实现** `acg_shared.go`、`http.go` 挂载、`agentEnvOn` 两行。

- [ ] **Step 4: 跑测试确认通过**
  Run: `cd backend && go test ./internal/handler/ -count=1 -race`
  Expected: PASS（含原有全部测试）

- [ ] **Step 5: 提交**
```bash
git add backend/internal/handler/acg_shared.go backend/internal/handler/acg_shared_test.go
git commit -m "feat(acg): /shared 公共校验（HTTPS、限流、验签锁定、白名单、状态）和只读接口" -- backend/internal/handler/acg_shared.go backend/internal/handler/acg_shared_test.go backend/internal/handler/agent_env_test.go backend/internal/server/http.go
```

---

### Task 5: trade 取码和失败记录

**Files:**
- Create: `backend/internal/handler/acg_shared_trade.go`
- Modify: `backend/internal/handler/acg_shared.go`（把 trade 占位换成 `SharedTrade`）
- Test: `backend/internal/handler/acg_shared_trade_test.go`

**Interfaces:**
- Consumes: Task 3 `placeAgentOrder` 和全部具名错误；Task 1 `RecordAgentSharedFailure`、`ResolveAgentSharedFailure`；Task 2 `ValidRequestNo`、`ParseQuantity`、`SecretText`、`Money`；Task 4 `sharedFail/sharedOK/sharedRateAllow/sharedRejectSpec`
- Produces：

```go
func SharedTrade(c *gin.Context)
var sharedTradeTimeout = 20 * time.Second
func recordSharedFailure(c *gin.Context, requestNo, productCode string, qty int, reason, msg string) // 尽力写，失败只记日志
const (
    sharedMsgReqNo        = "请求号不正确"
    sharedMsgNoBalance    = "货源余额不足，请联系商家"
    sharedMsgOutOfStock   = "商品暂时缺货，请稍后再试"
    sharedMsgReqConflict  = "请求号冲突"
)
```

处理顺序：
1. `request_no` 不是标量或 `!acgshared.ValidRequestNo` → `sharedMsgReqNo`，**不记**失败；
2. `sharedRateAllow("trade|"+app_id, 60, now)` 否则 → `sharedMsgRate`，记 `rate_limited`；
3. `sharedRejectSpec` → 记 `invalid_request`；
4. `num` 用 `ParseQuantity`，不合法 → `sharedMsgQty`，记 `invalid_request`（数量记 0）；
5. `ctx, cancel := context.WithTimeout(c.Request.Context(), sharedTradeTimeout)`；`placeAgentOrder(ctx, agentOrderInput{AgentID, Username, ProductCode: shared_code, Quantity, RequestNo: "acg:"+request_no, ExpectedUnitPriceCents: 0, Source: db.AgentOrderSourceACG, ExternalRef: request_no})`；
6. 错误映射（都记失败，商品码截到 64 字）：`*db.InsufficientBalanceError` → `sharedMsgNoBalance`/`insufficient_balance`；`errProductNotReady` → `sharedMsgOutOfStock`/`not_ready`；`errProductUnavailable` → `sharedMsgOffSale`/`product_unavailable`；`errRequestConflict` → `sharedMsgReqConflict`/`request_conflict`；`errAgentOrderNoKey`、`errAgentOrderSeal` → `sharedMsgMaint`，不记；`db.ErrAgentDisabled`、`db.ErrAgentNotFound` → `sharedMsgAgentOff`，不记；其余（含 `errAgentOrderBusy`、`errAgentOrderInternal`）→ `sharedMsgBusy`/`busy`；
7. 成功或重放：`Codes` 里有任何 `Unavailable` → `sharedMsgMaint`，记 `busy`，不返回码；否则 `ResolveAgentSharedFailure`（出错只记日志）；审计：新单 `agent_order`，detail `source=acg ext=<request_no> product=%s quantity=%d total_cents=%d order=%d`；重放 `agent_codes_export`，detail `order=%d count=%d replay=true source=acg ext=<request_no>`；返回 `sharedOK(c, gin.H{"url":"", "amount": Money(Order.TotalCents), "tradeNo": "AO"+订单ID, "secret": SecretText(c.Request.Host, 码), "leave_message":"", "stock": 200})`。
8. 日志一行：`[acg-shared] app_id=%s trade request_no=%s 结果=%s`（结果写原因或「成功/重放」），不写码和金额以外的内容。

- [ ] **Step 1: 写失败的测试** `acg_shared_trade_test.go`（用 Task 4 的 `newSharedFix`；单号用 `202611050000000001` 起）：
  - `TestSharedTrade_Success`：`num=2` → `tradeNo=="AO"+订单ID`、`amount` 原始 JSON `10.00`、`stock 200`、`url ""`、`leave_message ""`；`secret` 两行，每行匹配 `^(\S+)  兑换：https://example\.com/recharge\?code=(\S+)$` 且两处的码相同（httptest 默认 Host `example.com`）；库里订单 `source=="acg"`、`external_ref` 为单号、`request_no=="acg:"+单号`；余额 0；审计有 `agent_order` 且 detail 含 `source=acg`。
  - `TestSharedTrade_Replay`：同号同参数再发 → `secret` 和 `tradeNo` 与第一次相同、余额不变、只有 1 笔订单、审计多一条含 `replay=true` 的 `agent_codes_export`。
  - `TestSharedTrade_Conflict`：同号 `num=1` → `请求号冲突`；失败表一行 `request_conflict`。
  - `TestSharedTrade_ConcurrentSameRequestNo`：`newSharedFix(t, newAgentEnvFileDB(t), true)`，8 个协程同号同参数并发 → 只 1 笔订单、余额只扣一次、所有成功响应的 `secret` 相同。
  - `TestSharedTrade_InsufficientThenResolved`：余额 100 → `货源余额不足，请联系商家`，失败行 `attempts 1`、`insufficient_balance`；再发 → `attempts 2`；管理员调账 +1000（`adjust` 辅助函数）后再发 → 成功，失败行 `ResolvedOrderID` 等于订单 ID。
  - `TestSharedTrade_NotReady` → `商品暂时缺货，请稍后再试`/`not_ready`；`TestSharedTrade_Unavailable`：下架或不存在的 `shared_code` → `当前商品已停售`/`product_unavailable`。
  - `TestSharedTrade_InvalidParams`：`request_no` 为空、`abc-1`、65 位 → `请求号不正确` 且失败表为空；`num=0` → `购买数量需为 1–200` 且记 `invalid_request`；`race=x` → `该商品没有可选规格` 且记 `invalid_request`。
  - `TestSharedTrade_RateLimit`：未就绪夹具下同一商户 ID 连发 60 个不同单号后，第 61 个 → `请求太频繁，请稍后再试`，失败表这一号是 `rate_limited`。
  - `TestSharedTrade_Timeout`：`sharedTradeTimeout=50ms`，`agentOrderLock` 换成「等 ctx 结束再返回 `nil,false`」→ `系统繁忙，请稍后再试`、记 `busy`、没建单。
  - `TestSharedTrade_ChargesCurrentPrice`：valuation 之后 `f.setPrice(pid, 600)`，trade `num=1` → `amount` 为 `6.00`。
  - `TestSharedTrade_ReplayWithUnavailableCode`：成功一单后把其中一张码的 `code_sealed` 改成 `x`，同号重发 → `系统维护中`、响应里没有 `secret`、失败表记 `busy`。
  - `TestSharedTrade_LogNoSecrets`：捕获日志，一次成功、一次重放、一次余额不足：输出不含任何一张码、不含 `f.key`、不含 sign。

- [ ] **Step 2: 跑测试确认失败**
  Run: `cd backend && go test ./internal/handler/ -run 'TestSharedTrade_' -count=1`
  Expected: FAIL（占位返回「系统繁忙，请稍后再试」）或 `SharedTrade` 未定义

- [ ] **Step 3: 实现** `acg_shared_trade.go`，替换占位。

- [ ] **Step 4: 跑测试确认通过**
  Run: `cd backend && go test ./internal/handler/ -count=1 -race`
  Expected: PASS

- [ ] **Step 5: 提交**
```bash
git add backend/internal/handler/acg_shared_trade.go backend/internal/handler/acg_shared_trade_test.go
git commit -m "feat(acg): trade 取码（幂等重放、20 秒超时、按商户限流）和取码失败记录" -- backend/internal/handler/acg_shared_trade.go backend/internal/handler/acg_shared_trade_test.go backend/internal/handler/acg_shared.go
```

---

### Task 6: 代理商端对接设置、总览提醒、来源字段、流水导出列、失败记录清理

**Files:**
- Create: `backend/internal/handler/acg_shared_settings.go`
- Modify: `backend/internal/handler/agent_routes.go`（`sess` 组加 6 个路由）
- Modify: `backend/internal/handler/agent_codes.go`（`AgentOverview`、`agentCodeEntry`/`loadAgentCodes`/`agentCodeView`）
- Modify: `backend/internal/handler/agent_admin.go`（`agentOrderView` 加 `source`、`external_ref`）
- Modify: `backend/internal/handler/agent_ledger.go`（JSON 行和 CSV 加来源）
- Modify: `backend/internal/handler/ops_maintenance.go`（`runOpsMaintenance` 加清理）
- Test: `backend/internal/handler/acg_shared_settings_test.go`

**Interfaces:**
- Consumes: Task 1 全部 db 函数和 `AgentOrderRefs`；Task 4 的 `/shared` 请求夹具（验证重置、停用的效果）
- Produces（前端第 8、9 任务按这些字段名用）：
  - `GET /api/v1/agent/shared` → `{"status":"inactive"|"active"|"admin_disabled", "app_id":"", "key_created_at":0, "last_used_at":0, "key_invalid":false, "ip_allowlist":[], "low_balance_cents":0, "min_acg_version":"3.6.1"}`；`key_invalid` 为真表示有密文但用当前主密钥解不开（主密钥缺失时也为真）。
  - `POST /api/v1/agent/shared/key` `{"code":"123456"}` → `{"app_id":"ag…","app_key":"…"}`，`Cache-Control: no-store`。
  - `POST /api/v1/agent/shared/disable` `{"code"}` → `{"ok":true}`。
  - `PUT /api/v1/agent/shared/allowlist` `{"code","ips":[…]}` → `{"ip_allowlist":[…]}`。
  - `PUT /api/v1/agent/shared/low-balance` `{"low_balance_cents":N}` → `{"low_balance_cents":N}`。
  - `GET /api/v1/agent/shared/failures?page=&page_size=` → `{"items":[{"id","request_no","product_code","quantity","reason","message","attempts","first_at","last_at","resolved_order_id"}],"total":N}`，范围最近 30 天，分页用 `agentPaging`。
  - `GET /api/v1/agent/overview` 多 `"shared_failures_24h":{"total":N,"insufficient_balance":M}`、`"low_balance":{"threshold_cents":X,"below":bool}`。
  - 订单对象多 `"source"`、`"external_ref"`；码列表和码导出的每项多 `"source"`、`"external_ref"`（码不属于订单时为空串）；流水 JSON 每行多 `"source"`、`"external_ref"`（没有订单时为空串）。
  - 流水 CSV 末尾加两列「来源」「对方订单号」（管理员版在「操作人」之后），来源写「后台」/「异次元」，没有订单留空。
  - `func pruneSharedFailures(now time.Time)`：删 `last_at < now-30天`。规格写「每天的核对任务」，本仓库的运维循环每 10 分钟一轮，挂进 `runOpsMaintenance`（按 `last_at` 有索引，开销可忽略）。
  - 错误编号：`shared_admin_disabled`（403，「对接已被管理员关闭」）、`maintenance`（503，「系统维护中，暂不能生成密钥」）、`invalid_request`（400）、`totp_invalid`（400，沿用 `agentMsgBadTOTP`）。

生成/重置（`POST /shared/key`）：读代理商 → 读 creds → `AdminDisabled` 先拒 → `secretbox.LoadKey` 失败 503 → `agentTOTPAllowed` + `verifyAgentTOTP`（照 `AgentChangePassword`）→ `AppID==""` 时生成 `ag`+14 位、否则沿用 → 生成 32 位密钥（两者都用 `crypto/rand` 均匀取 `[A-Za-z0-9]`）→ `secretbox.Seal(key, "acg-shared:<agent_id>:<app_id>", 明文)` → `db.SetAgentSharedKey`（商户 ID 撞唯一约束时重新生成，最多 3 次）→ `slot.ok()` → 审计 `agent_shared_key_create`（原先没有 app_id）或 `agent_shared_key_reset`，detail 只写 `app_id=<id>`。
停用：TOTP 后 `DisableAgentShared`，审计 `agent_shared_disable`（`app_id=<旧 id>`）。
白名单：先校验再要 TOTP：去空白、去空项、去重（按 `netip` 规范化后的字符串）、每项 `netip.ParseAddr` 或 `netip.ParsePrefix`（存规范化结果，网段先 `Masked()`），超过 5 项 → 400「IP 白名单最多 5 项」，非法 → 400「「<原文>」不是合法的 IP 或网段」；审计 `agent_shared_allowlist`（`count=N`）。
提醒线：0–1000000，否则 400「提醒线需为 0 到 10000 美元」；不要 TOTP，不审计。

- [ ] **Step 1: 写失败的测试** `acg_shared_settings_test.go`。每次要动态码前先 `e.clock = e.clock.Add(30 * time.Second)`，码用 `agent.TOTPCode(secret, agent.TOTPStep(e.clock))`（登录用过的时间片不能再用）。
  - `TestAgentShared_GetInactive`：`status=="inactive"`、`app_id==""`、`min_acg_version=="3.6.1"`。
  - `TestAgentShared_CreateNeedsTOTP`：错码 → 400 `totp_invalid`；对的码 → 200，`app_id` 匹配 `^ag[A-Za-z0-9]{14}$`、`app_key` 匹配 `^[A-Za-z0-9]{32}$`、`Cache-Control: no-store`；之后 `GET /shared` 是 `active` 且响应体不含 app_key；用这对凭据发 `/shared/authentication/connect` 成功；审计 `agent_shared_key_create` 的 detail 含 app_id、不含 app_key。
  - `TestAgentShared_ResetInvalidatesOldKey`：重置后 `app_id` 不变、`app_key` 变；旧密钥签名 → `密钥错误`，新密钥 → 成功；审计 `agent_shared_key_reset`。
  - `TestAgentShared_Disable`：停用后旧 app_id → `商户ID不存在`；再生成得到不同的 app_id。
  - `TestAgentShared_AdminDisabledBlocksCreate`：`db.SetAgentSharedAdminDisabled(true)` 后生成 → 403 `shared_admin_disabled`，没消耗动态码（同一个码随后在管理员重新允许后还能用）。
  - `TestAgentShared_NoMasterKey`：清空主密钥后生成 → 503 `maintenance`；`GET /shared` 的 `key_invalid` 在已开通时为 true。
  - `TestAgentShared_Allowlist`：`["1.2.3.4"," 10.0.0.0/8 ","2001:db8::/32","1.2.3.4",""]` → 保存为 `["1.2.3.4","10.0.0.0/8","2001:db8::/32"]`、审计 `count=3`；6 项 → 400「IP 白名单最多 5 项」；`["abc"]` → 400「「abc」不是合法的 IP 或网段」；错动态码 → 400 `totp_invalid` 且不保存。
  - `TestAgentShared_LowBalance`：500 → 保存；-1、1000001 → 400。
  - `TestAgentShared_Failures`：库里 3 行（其中一行 31 天前）→ 返回 2 行、按 `last_at` 降序、`total==2`。
  - `TestAgentOverview_SharedFields`：24 小时内未解决 3 行（2 行余额不足）、已解决 1 行、25 小时前 1 行 → `total 3`、`insufficient_balance 2`；提醒线 2000、余额 1000 → `below true`；提醒线 0 → `below false`。
  - `TestAgentViews_Source`：一笔后台单、一笔异次元单 → `/orders` 两项的 `source`、`external_ref` 正确；`/codes` 和 `/codes/export` 每张码带对应来源；`/ledger` 的下单行带来源、调账行为空串。
  - `TestAgentLedgerExport_SourceColumns`：表头末尾是 `来源,对方订单号`；异次元单那行是 `异次元,<单号>`、后台单 `后台,`、调账行两列都空；管理员导出表头是 `…,备注,操作人,来源,对方订单号`。
  - `TestPruneSharedFailures`：31 天前、29 天前各一行，`runOpsMaintenance(now)` 后只剩 29 天前那行。

- [ ] **Step 2: 跑测试确认失败**
  Run: `cd backend && go test ./internal/handler/ -run 'AgentShared_|AgentOverview_SharedFields|AgentViews_Source|AgentLedgerExport_SourceColumns|PruneSharedFailures' -count=1`
  Expected: FAIL（路由 404、字段缺失）

- [ ] **Step 3: 实现** 上述接口和字段。来源查找用一次 `db.AgentOrderRefs`（码列表在 `loadAgentCodes` 里替换现在的 `AgentOrderProductCodes` 调用，商品码改从 `AgentOrderRef.ProductCode` 取）。

- [ ] **Step 4: 跑测试确认通过**
  Run: `cd backend && go test ./internal/handler/ -count=1 -race`
  Expected: PASS

- [ ] **Step 5: 提交**
```bash
git add backend/internal/handler/acg_shared_settings.go backend/internal/handler/acg_shared_settings_test.go
git commit -m "feat(agent): 对接设置接口、总览失败与余额提醒、订单与流水来源列" -- backend/internal/handler/acg_shared_settings.go backend/internal/handler/acg_shared_settings_test.go backend/internal/handler/agent_routes.go backend/internal/handler/agent_codes.go backend/internal/handler/agent_admin.go backend/internal/handler/agent_ledger.go backend/internal/handler/ops_maintenance.go
```

---

### Task 7: 管理员端查看和开关对接

**Files:**
- Create: `backend/internal/handler/acg_shared_admin.go`
- Modify: `backend/internal/handler/agent_routes.go`（`RegisterAgentAdminRoutes` 加 3 个路由）
- Test: `backend/internal/handler/acg_shared_admin_test.go`

**Interfaces:**
- Consumes: Task 1 `GetAgentSharedCreds`、`SetAgentSharedAdminDisabled`、`ListAgentSharedFailures`；`adminAgentFromPath`、`auditAdmin`
- Produces：
  - `GET /api/v1/admin/agents/:id/shared` → `{"status","app_id","key_created_at","last_used_at","ip_allowlist","low_balance_cents","failures":[最近 50 条，字段同代理商端]}`，不含密文和密钥。
  - `POST /api/v1/admin/agents/:id/shared/disable`、`/enable` → `{"status":…}`；审计 `admin_agent_shared_disable` / `admin_agent_shared_enable`，detail `id=%d username=%s`。

- [ ] **Step 1: 写失败的测试**
  - `TestAdminAgentShared_GetAndToggle`：开通后 GET → `status=="active"`、有 `app_id`、响应体不含密钥和 `key_sealed`；disable → `admin_disabled`、`/shared` 请求 → `对接已被管理员关闭`；enable → 原密钥再次可用；两条审计都在；不存在的代理商 → 404。
  - `TestAdminAgentShared_Failures`：60 行失败 → 返回 50 行、`last_at` 降序。
  - `TestAdminAgentViews_Source`：管理员订单列表每项带 `source`、`external_ref`；管理员流水 JSON 每行带来源（Task 6 已改 `agentOrderView` 和流水视图，这里只验证管理员路径）。

- [ ] **Step 2: 跑测试确认失败**
  Run: `cd backend && go test ./internal/handler/ -run 'AdminAgentShared_|AdminAgentViews_Source' -count=1`
  Expected: FAIL（404）

- [ ] **Step 3: 实现**

- [ ] **Step 4: 跑测试确认通过**
  Run: `cd backend && go test ./internal/handler/ -count=1 -race`
  Expected: PASS

- [ ] **Step 5: 提交**
```bash
git add backend/internal/handler/acg_shared_admin.go backend/internal/handler/acg_shared_admin_test.go
git commit -m "feat(admin): 代理商详情查看异次元对接、关闭与重新允许" -- backend/internal/handler/acg_shared_admin.go backend/internal/handler/acg_shared_admin_test.go backend/internal/handler/agent_routes.go
```

---

### Task 8: 前端代理商端

**Files:**
- Create: `frontend/src/lib/agentShared.ts`
- Create: `frontend/src/views/agent/AgentIntegration.vue`
- Modify: `frontend/src/router/routes.ts`（`/agent` 子路由加 `{ path: 'integration', name: 'AgentIntegration', … }`）
- Modify: `frontend/src/layouts/AgentLayout.vue`（`nav` 在「流水」后加 `{ to: '/agent/integration', label: '对接异次元' }`）
- Modify: `frontend/src/views/agent/AgentOverview.vue`、`AgentOrders.vue`、`AgentOrderDetail.vue`、`AgentCodes.vue`、`AgentLedger.vue`
- Modify: `frontend/src/lib/agentExport.ts`（`ExportedCode` 加 `source?`、`external_ref?`；`codesCsv` 表头末尾加 `,source,external_ref`，值原样写 `web`/`acg`，与 `status` 列写原始值的做法一致）
- Modify: `frontend/src/lib/__tests__/agentExport.test.ts` 第 21 行表头断言（唯一允许改的已有断言）
- Test: `frontend/src/lib/__tests__/agentShared.test.ts`

**Interfaces:**
- Consumes: Task 6 的接口和字段
- Produces（`agentShared.ts`）：

```ts
export type SharedStatus = 'inactive' | 'active' | 'admin_disabled'
export const SHARED_STATUS_TEXT: Record<SharedStatus, string> // 未开通 / 已开通 / 已被管理员关闭
export const SOURCE_LABELS: Record<string, string>            // { web: '后台', acg: '异次元' }
export function sourceLabel(s?: string | null): string        // 未知或空返回 ''
export const FAILURE_REASON_TEXT: Record<string, string>
// insufficient_balance 余额不足、not_ready 暂时缺货、product_unavailable 商品停售、request_conflict 请求号冲突、
// rate_limited 请求太频繁、busy 系统繁忙、invalid_request 参数不正确
export function failureBanner(f?: { total: number; insufficient_balance: number } | null): string
export function lowBalanceBanner(lb?: { threshold_cents: number; below: boolean } | null): string
export function parseAllowlistInput(text: string): { ips: string[]; error: string } // 按逗号、空白、换行切分，去空、去重；超过 5 项 error「IP 白名单最多 5 项」
export const ACG_SETUP_STEPS: string[] // 规格 9.3 的 4 条接入步骤原文；第 2 条在页面上用红字显示
export const KEY_ONCE_WARNING = '密钥只显示这一次，关掉后看不到，丢了只能重置'
export const RESET_WARNING = '重置后旧密钥立即失效，请马上到异次元里更新'
export const KEY_INVALID_TEXT = '密钥已失效，请重置'
```

`failureBanner` 返回 `最近 24 小时有 ${total} 笔异次元订单取码失败（余额不足 ${m} 笔），客户可能已付款但没收到码`（`total<=0` 返回 `''`，页面在后面接「查看」链接到 `/agent/integration`）；`lowBalanceBanner` 返回 `余额低于提醒线 ${fmtUsd(threshold_cents)}`（`below` 为假返回 `''`）。

页面（`AgentIntegration.vue`）按规格 9.3：三种状态；生成、重置、停用、保存白名单都弹框要 6 位动态码；生成/重置成功后显示上游地址（`location.origin`）、商户 ID、商户密钥，各带复制按钮和 `KEY_ONCE_WARNING` 红字，关掉后清空内存里的密钥；`key_invalid` 时显示 `KEY_INVALID_TEXT`；管理员关闭时生成和重置按钮禁用；4 条步骤；提醒线输入框（美元，用 `agentMoney` 里已有的解析函数转美分）；最近失败表（时间、对方订单号、商品、数量、原因、次数、状态 未解决/已解决），分页。其他页面：总览在余额区上方显示红条、黄条；订单列表加「来源」列；订单详情显示来源和对方订单号，导出 .csv 时把订单的 `source`、`external_ref` 填到每张码上；我的码导出 .csv 自动带两列；流水表加「来源」列。

- [ ] **Step 1: 写失败的测试** `agentShared.test.ts`：
  - `failureBanner({total:3, insufficient_balance:2})` === `'最近 24 小时有 3 笔异次元订单取码失败（余额不足 2 笔），客户可能已付款但没收到码'`；`{total:0,…}`、`undefined` → `''`。
  - `lowBalanceBanner({threshold_cents:200000, below:true})` === `'余额低于提醒线 $2,000.00'`；`below:false` → `''`。
  - `parseAllowlistInput('1.2.3.4, 10.0.0.0/8\n1.2.3.4  ')` → `{ ips: ['1.2.3.4','10.0.0.0/8'], error: '' }`；6 个不同地址 → `error === 'IP 白名单最多 5 项'`；空串 → `{ ips: [], error: '' }`。
  - `sourceLabel('acg') === '异次元'`、`sourceLabel('web') === '后台'`、`sourceLabel('') === ''`、`sourceLabel(undefined) === ''`。
  - `ACG_SETUP_STEPS.length === 4`，第 2 条含 `USD` 和 `汇率`。
  - `agentExport.test.ts`：表头断言改为 `'code,product_code,plan,region,status,purchased_at,order_id,note,source,external_ref'`；新增一例 `{…, source:'acg', external_ref:'202611050000000001'}` 那行以 `,acg,202611050000000001` 结尾。

- [ ] **Step 2: 跑测试确认失败**
  Run: `cd frontend && npx vitest run src/lib/__tests__/agentShared.test.ts src/lib/__tests__/agentExport.test.ts`
  Expected: FAIL（模块不存在、表头不符）

- [ ] **Step 3: 实现** `agentShared.ts`、页面和路由、各页改动。

- [ ] **Step 4: 跑全部前端测试和构建**
  Run: `cd frontend && npx vitest run && npm run build`
  Expected: 全部 PASS，构建成功

- [ ] **Step 5: 提交**
```bash
git add frontend/src/lib/agentShared.ts frontend/src/lib/__tests__/agentShared.test.ts frontend/src/views/agent/AgentIntegration.vue
git commit -m "feat(agent-ui): 对接异次元页、总览失败与余额提醒、订单来源列与导出列" -- frontend/src/lib/agentShared.ts frontend/src/lib/__tests__/agentShared.test.ts frontend/src/views/agent/AgentIntegration.vue frontend/src/router/routes.ts frontend/src/layouts/AgentLayout.vue frontend/src/views/agent frontend/src/lib/agentExport.ts frontend/src/lib/__tests__/agentExport.test.ts
```

---

### Task 9: 前端管理员端

**Files:**
- Modify: `frontend/src/views/admin/AgentDetail.vue`（加「异次元对接」区；订单列表、流水列表加「来源」列）
- Modify: `frontend/src/lib/agentAdmin.ts`（加 `adminSharedView`）
- Test: `frontend/src/lib/__tests__/agentAdmin.shared.test.ts`

**Interfaces:**
- Consumes: Task 7 的接口；Task 8 的 `SHARED_STATUS_TEXT`、`sourceLabel`、`FAILURE_REASON_TEXT`
- Produces：

```ts
export function adminSharedView(d: any): {
  statusText: string        // SHARED_STATUS_TEXT
  canDisable: boolean       // status !== 'admin_disabled'
  canEnable: boolean        // status === 'admin_disabled'
  allowlistText: string     // 空时「不限」，否则用「、」连接
}
```

对接区：状态、商户 ID（没有时「—」）、最后使用时间、IP 白名单、余额提醒线、最近 50 条失败表；按钮「关闭对接」「重新允许」各带确认框（关闭的确认文字：「关闭后这个代理商的异次元站会立即取不到货，确定关闭？」）。管理员看不到、也不能重置密钥。

- [ ] **Step 1: 写失败的测试**：`adminSharedView({status:'active', ip_allowlist:[]})` → `{statusText:'已开通', canDisable:true, canEnable:false, allowlistText:'不限'}`；`{status:'admin_disabled', ip_allowlist:['1.2.3.4','10.0.0.0/8']}` → `canEnable:true`、`allowlistText:'1.2.3.4、10.0.0.0/8'`。

- [ ] **Step 2: 跑测试确认失败**
  Run: `cd frontend && npx vitest run src/lib/__tests__/agentAdmin.shared.test.ts`
  Expected: FAIL（`adminSharedView` 未导出）

- [ ] **Step 3: 实现**

- [ ] **Step 4: 跑全部前端测试和构建**
  Run: `cd frontend && npx vitest run && npm run build`
  Expected: 全部 PASS，构建成功

- [ ] **Step 5: 提交**
```bash
git add frontend/src/lib/__tests__/agentAdmin.shared.test.ts
git commit -m "feat(admin-ui): 代理商详情加异次元对接区和来源列" -- frontend/src/views/admin/AgentDetail.vue frontend/src/lib/agentAdmin.ts frontend/src/lib/__tests__/agentAdmin.shared.test.ts
```

---

### Task 10: CHANGELOG 和全量门禁

**Files:**
- Modify: `CHANGELOG.md`（「未发布」）

**Interfaces:**
- Consumes: 前面全部任务

- [ ] **Step 1: 写 CHANGELOG**（按规格第 14 节，三块的位置沿用现有「升级注意」结构）：
  - 「升级前必须做」第 4 条（`TRUSTED_PROXIES`）末尾补一句：异次元对接也靠它识别 HTTPS，没配对时所有对接请求都会被拒（「请使用 https 地址」）。
  - 「升级后要确认」加一条：给代理商开通对接时，告诉他们 acg-faka 要 ≥ 3.6.1，店铺档案币种设 USD 并填汇率，否则会把美元价当人民币卖。
  - 「行为变化（无需操作）」加：新增 `agent_shared_creds`、`agent_shared_failures` 两张表，`agent_orders` 加 `source`、`external_ref` 两列（自动迁移）；后端开始占用 `/shared/*` 路径；代理商码导出的 .csv 多 `source`、`external_ref` 两列，流水导出多「来源」「对方订单号」两列（.txt 不变）；后台下单的请求号不能再以 `acg:` 开头。
  - 「新增」加一段：代理商可在「对接异次元」页生成商户密钥，把本站接成 acg-faka 的店铺共享上游。

- [ ] **Step 2: 跑全量门禁**
  Run: `bash /private/tmp/claude-501/-Users-zhengshan-projects-aisubproxy/925aae1a-228d-4187-b245-452298366b9f/scratchpad/agent-gates.sh`（gofmt、vet、golangci-lint、go test 排除 paybrowser、-race；把 `./internal/acgshared` 加进 -race 列表），再 `cd frontend && npx vitest run && npm run build`
  Expected: gofmt 无输出、lint 0 问题、全部 PASS

- [ ] **Step 3: 提交**
```bash
git commit -m "docs(changelog): 异次元对接的升级注意和行为变化" -- CHANGELOG.md
```

人工点检（由主控在最后做，不在任何实现任务里）：本机临时实例（临时数据目录、不碰 18080 和仓库 `data/`）+ 草稿区里的签名脚本按调研第 7 条算法发请求，走一遍 connect → items → item → valuation → trade → 同号重放 → 余额不足 → 调账后同号成功；查服务日志不含密钥、sign、码。
