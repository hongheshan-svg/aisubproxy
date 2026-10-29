# MarsWallex 钱包、持卡人管理与交易补拉设计（子项目 2b）

- 日期：2026-10-29
- 状态：已确认设计，待写实现计划
- 前置：子项目 1 `docs/superpowers/specs/2026-10-27-marswallex-access-layer-design.md`、子项目 2 `docs/superpowers/specs/2026-10-28-marswallex-card-pool-design.md`（均已合并到 master）
- 参考：`docs/marswallex-openapi-zh.md`（2026-10-29 重新下载官方规范比对，内容与仓库存档一致）

## 1. 背景与目标

子项目 1 的客户端已实现 MarsWallex 全部 27 个公开接口，子项目 2 的卡池用到其中 13 个，并处理 10 类 Webhook 中的 7 类。剩余 14 个接口和 3 类 Webhook 在业务层没有入口，日常运营仍需登录 MarsWallex 网页后台。

**本子项目目标**：把剩余接口和 Webhook 全部接入后台，让以下操作不再依赖 MarsWallex 网页后台：

- 查看主钱包与子账户余额、充值收款地址、账户流水
- 新建、启停子账户；主钱包与子账户互转
- 把数币兑换为法币（例如 USDT → USD）
- 新建、修改、启停持卡人（含证件照上传）
- 定时补拉授权与结算交易，补上漏推的 Webhook
- 在卡详情里查看平台侧的转入转出明细，并与本地单据比对

本文取代子项目 2 §2 中「数币充值与兑换仍在 MarsWallex 网页后台操作」和「持卡人在 MarsWallex 网页后台建立和审核」两条：两者改为可在本系统后台完成，原有导入方式保留。

## 2. 已确认的决定

| 事项 | 决定 |
|---|---|
| 子账户的用途 | 只做资金分账：后台可新建普通子账户、启停、与主钱包互转、查流水。**卡池仍只从主钱包开卡和注资**，不做共享账户与共享卡 |
| 兑换 | 只能在后台手动发起，不做自动兑换 |
| 持卡人敏感资料 | 本地不保存证件号、证件照、出生日期、SSN、地址等。证件照直接转发给 MarsWallex，只在内存中经过；修改时从平台实时拉取完整资料 |
| 实现方案 | 扩展子项目 2 的单据引擎：兑换与钱包转账作为 `mw_card_orders` 的两种新单据类型，复用发送初判、查单、Webhook 定论、超时转人工、后台重查与确认失败；不涉及资金的操作直接调用接口并同步本地表 |
| 凭证 | 只有生产凭证：自动化测试全部用模拟服务器（`internal/marswallex/mwtest`），生产验收先只读、再最小金额 |

## 3. 整体结构

| 单元 | 职责 |
|---|---|
| `internal/cardpool/funds.go`（新） | 兑换单、钱包转账单的提交与校验；两类单据的查单映射 |
| `internal/cardpool/wallet.go`（新） | 账户同步、新建子账户、启停子账户、收款地址、账户流水 |
| `internal/cardpool/holders.go`（新） | 证件照转发、新建 / 修改 / 启停持卡人、提交记录 |
| `internal/cardpool/backfill.go`（新） | 交易定时补拉、手动补拉、补拉状态 |
| `internal/cardpool` 既有文件 | `orders.go` / `reconcile.go` 增加两种单据类型；`events.go` 处理 3 类新 Webhook、调整持卡人告警、抽出「未交卡号出现授权」规则；`admin.go` 增加卡转入转出明细；`run.go` 增加补拉循环 |
| `internal/db/cardpool.go` | §4 的新表、新列、新索引（可重复执行的迁移） |
| `internal/handler` + `server/http.go` | §9 的后台接口 |
| 前端 `frontend/src/views/admin/MarsWallexWallet.vue`（新） | 「MarsWallex 钱包」页：账户 / 充值地址 / 兑换 / 转账 / 流水 五个标签 |
| 前端 `CardPool.vue` | 持卡人标签加新建、修改、启停与提交记录；交易标签加补拉状态与按钮；卡详情加转入转出明细 |

MarsWallex 客户端（`internal/marswallex`）已包含全部方法，本子项目只在缺字段时补字段，不改调用与签名逻辑。

### 3.1 不在本子项目范围

共享账户与共享卡；卡池从子账户开卡或注资；自动兑换；本地保存持卡人敏感资料；账户流水导出；收款地址二维码；兑换引擎（子项目 4）；资金对账报表（子项目 5）。

## 4. 数据模型

### 4.1 `mw_card_orders` 新增列与索引

| 列 | 类型 | 用途 |
|---|---|---|
| `buy_currency` | TEXT NOT NULL DEFAULT '' | 兑换的买入币种 |
| `payer_account_no` | TEXT NOT NULL DEFAULT '' | 钱包转账付款方 |
| `payee_account_no` | TEXT NOT NULL DEFAULT '' | 钱包转账收款方 |
| `detail` | TEXT NOT NULL DEFAULT '' | 平台返回的成交结果 JSON（兑换：`buy_amount`、`rate`、`fee_amount`、`fee_currency`、`base_currency`；转账：`order_no`），原文保存，仅展示 |

新增部分唯一索引，保证同一类型同一时刻最多一笔在途：

```sql
CREATE UNIQUE INDEX IF NOT EXISTS uq_mw_orders_wallet_inflight ON mw_card_orders(kind)
  WHERE kind IN ('conversion','wallet_transfer') AND state IN ('pending','processing','unknown','needs_review')
```

### 4.2 `mw_accounts`（新）

| 列 | 说明 |
|---|---|
| `account_no` TEXT PRIMARY KEY | 平台账户号 |
| `out_account_no` | 商户侧账户号；只有本系统新建或推送带回时才有（分页接口不返回） |
| `account_name` | 账户名 |
| `account_type` | `MAIN` / `SUB_ACCOUNT` |
| `purpose` | 平台原值（`SHARED_ACCOUNT` 或空） |
| `status` | `ACTIVE` / `DISABLED` / `INITIALIZING` / `FAILED` / `REJECTED` |
| `balances` | 平台 `balance_list` 原文 JSON（币种、可用、冻结，金额为平台字符串） |
| `gone` INTEGER NOT NULL DEFAULT 0 | 最近一次同步时平台上已不存在 |
| `synced_at`、`created_at` | 时间（Unix 秒） |

### 4.3 `mw_cardholders` 新增列

`out_card_holder_id TEXT NOT NULL DEFAULT ''`：商户侧持卡人 ID（本系统新建或导入时从平台带回）。

### 4.4 `mw_cardholder_submissions`（新）

| 列 | 说明 |
|---|---|
| `out_card_holder_id` TEXT PRIMARY KEY | 本系统生成的商户侧 ID |
| `first_name`、`last_name`、`country_code` | 仅用于列表显示 |
| `state` | `submitted`（已发出未回）/ `unknown`（结果未知）/ `linked`（已关联）/ `abandoned`（已放弃） |
| `card_holder_id` | 关联后的平台 ID |
| `last_error` | 截断到 200 字的错误信息 |
| `requested_by`、`created_at`、`updated_at` | 操作人与时间 |

**不含任何证件信息。**

### 4.5 补拉状态

`site_settings` 键 `mw_txn_backfill_state`，JSON：`cursor`（已完整拉取到的时间点，Unix 秒）、`last_run_at`、`last_added`（上一轮新增条数）、`last_error`。

## 5. 兑换单与钱包转账单

### 5.1 单号与类型

| 类型常量 | 值 | 单号前缀 | 复用列 |
|---|---|---|---|
| `KindConversion` | `conversion` | `MC` | `amount_cents` = 卖出金额，`currency` = 卖出币种 |
| `KindWalletTransfer` | `wallet_transfer` | `MT` | `amount_cents`、`currency`；备注写入 `note` |

单号沿用 `newOutOrderNo`（22 位），满足兑换接口 32 位、转账接口 64 位的上限。

### 5.2 提交前校验（后端为准，前端同样校验）

1. 金额大于 0、最多 2 位小数（USDT 也按 2 位，余额零头留在账户里）。
2. 提交前先同步一次账户（§6.1）；同步失败则拒绝提交并提示。
3. 兑换：卖出与买入币种不同；卖出币种在主钱包余额中存在且可用余额足够。每次都显式传 `sell_amount`（不使用规范未说明的「不传金额」）。兑换接口没有账户参数，按作用于主钱包处理（列入 §10 待确认）。
4. 转账：付款方与收款方一方为主钱包（`MAIN`）、另一方为子账户（`SUB_ACCOUNT`）；两者都在本地表且 `gone = 0`；子账户状态为 `ACTIVE`；付款方该币种可用余额足够。
5. 同类型已有在途单（§4.1 索引冲突）时拒绝，提示先处理上一笔。

### 5.3 发送与初判

- 先写 `pending` 单据，再调用接口；沿用 `insertPending`、`recordFirstSend`、`classifySendErr`。
- 兑换：200 → `processing`，记录 `transaction_id`（平台不返回状态）。
- 转账：200 → 按返回 `status` 映射（`PROCESSING` → `processing`、`SUCCESS` → `success`、`FAIL` → `failed`），记录 `order_no`。
- 其他状态码、超时 → `unknown`；明确被拒 → `failed`；未发出 → `not_sent`。规则同子项目 1 §5.3。

### 5.4 查单与定论

- 兑换：`GET /v1/conversion/{out_order_no}`；`SUCCESS` → success，`FAILED` → failed（原因取 `fail_reason`），`PROCESSING` → processing。
- 转账：`GET /v1/transfer/order?out_order_no=`；`SUCCESS` / `FAIL` / `PROCESSING` 同上映射。
- 查不到、出错、状态不认识都算没有定论，按现有退避继续查。
- 定论时把成交结果写入 `detail`；定论后异步触发一次账户同步。
- 资金单永不重发。`unknown` 自首次发送满 30 分钟转 `needs_review` 并告警；后台「重查」「确认失败」沿用现有接口。卡住告警沿用 `alertStaleProcessing`。
- `describeOrder`、`terminalEffects` 等按类型分支的函数补上两种新类型（`terminalEffects` 对新类型无本地副作用）。

### 5.5 Webhook

- `CONVERSION_RESULT`、`TRANSFER_RESULT`：按 `out_order_no` 找对应类型的单据定论，状态兼容 `FAIL` / `FAILED`；写入 `detail`。
- 找不到单据（在网页后台发起的操作）：标记已处理，备注「非本系统单据」。

### 5.6 审计

发起兑换、转账写审计日志：操作人、类型、金额、币种、账户。

## 6. 账户、充值地址、流水

### 6.1 账户同步

- 分页调用 `GET /v1/accounts/page` 拉取全部账户，逐条 upsert 到 `mw_accounts`；本次没出现的本地账户标 `gone = 1`，不删除。
- 触发时机：打开钱包页、点刷新、兑换或转账提交前与定论后、新建或启停子账户后、收到账户状态推送且本地无该账户时。
- 同一时刻只运行一次同步（进程内互斥），并发调用等待同一结果。

### 6.2 新建子账户

- 管理员只填账户名（不含空格或换行，最多 50 字）；`out_account_no` 由系统生成（前缀 `AC` + UTC 时间 + 随机十六进制，长度不超过 64）；不传 `purpose`。
- 200 → 写入本地表。其他结果 → 返回「结果未知，请稍后刷新确认」，并在 10 秒后自动同步一次。
- 不建单据：重复建出空子账户不涉及资金，可停用；且分页接口不返回 `out_account_no`，无法查单。

### 6.3 启停子账户

- 需填变更原因（2–100 字，不含 emoji）；主钱包不允许（后端拒绝，前端不显示按钮）。
- 前端确认框提示：「停用会冻结该账户下发的卡；卡池的卡都从主钱包开，不受影响。」
- 设置目标状态可重复执行：直接调用接口后同步；结果未知同样提示稍后刷新。审计记录操作人、账户、目标状态、原因。
- `ACCOUNT_STATUS_CHANGED`：按 `account_no` 更新本地状态，推送带 `out_account_no` 时一并写入；本地没有该账户时触发一次同步。不告警。

### 6.4 充值收款地址

实时调用 `GET /v1/receiving/receiving-address`，展示链类型、地址、支持币种和复制按钮；页面醒目提示「只能向对应链转入对应币种，转错链会丢失资金」。不缓存。

### 6.5 账户流水

实时分页调用 `GET /v1/accounts/serials`，不落库。条件：账户（必选）、币种（从该账户余额币种中选，必选）、时间段（默认最近 7 天，后端限制最长 90 天）。展示流水号、收支方向（入账 / 出账 / 主转子 / 子转主）、交易类型（消费、充值、退款、汇兑、撤销、手续费、限额调整、转账）、金额、时间。

## 7. 持卡人管理

### 7.1 证件照转发

- 后台接口接收单个文件，转发到 `POST /v1/file/upload?biz_type=CARDHOLDER_ID_PHOTO`，返回 `file_id`。
- 请求体用 `http.MaxBytesReader` 限制在 7 MB + 表单开销以内；按内容嗅探只接受 JPEG、PNG、PDF；文件只在内存中，不写磁盘、不入库、不写日志。
- 前端在选择文件后立即逐张上传，每个持卡人 1–2 张。

### 7.2 新建

- 表单字段按 `POST /v1/cardholder` 规范；提供「账单地址同居住地址」勾选。
- 校验（前后端一致）：英文姓名仅 ASCII 且不超过 32 位；日期为 `YYYY-MM-DD`；证件有效期起始日不晚于今天且不晚于截止日，截止日晚于今天（长期有效填 `9999-12-31`）；国家代码两位大写字母；国家为 US 时 SSN 必填且符合 `^\d{3}-\d{2}-\d{4}$`，否则不传；邮箱格式；证件照 1–2 个。
- `out_card_holder_id` 由系统生成（前缀 `CH`）。提交前写一条 `submitted` 提交记录。
- 200 → 得到 `card_holder_id`，写入 `mw_cardholders`（仅姓名、国家、状态 `PENDING_REVIEW`、`out_card_holder_id`），提交记录标 `linked`。
- 明确被拒 → 提交记录标 `abandoned` 并记错误，返回错误给前端（管理员修正后重新提交会生成新 ID）。
- 其他结果 → 提交记录标 `unknown`，列表顶部显示；可通过以下方式收敛：
  - 收到 `CARDHOLDER_STATUS_CHANGE` 且推送里的 `out_card_holder_id` 匹配一条 `submitted` / `unknown` 记录 → 调 `GET /v1/cardholder/{id}` 导入并标 `linked`；
  - 管理员从网页后台查到平台 ID 后用现有「导入」功能导入，导入时若平台返回的 `out_card_holder_id` 匹配提交记录则自动标 `linked`；
  - 管理员点「放弃」标 `abandoned`。
- 新建的持卡人**不自动加入**策略的补卡持卡人列表，审核通过后由管理员手动加入。

### 7.3 修改

- 点「修改」时实时调用 `GET /v1/cardholder/{id}`，把完整资料返回前端预填（含原证件照 ID，可保留或重新上传）。
- 提交 `PUT /v1/cardholder/{id}`（全量字段，校验同 §7.2）后，重新导入以同步本地状态。
- 不限制可修改的状态，由平台判断。

### 7.4 启停

`PUT /v1/cardholder/status/{id}`：`APPROVED` ↔ `DISABLED`。前端只在对应状态显示按钮；完成后重新导入。审计记录操作人、持卡人、目标状态。

### 7.5 持卡人状态推送的告警调整

| 新状态 | 处理 |
|---|---|
| `APPROVED`（之前不是 `APPROVED`） | 通知「持卡人 X 已通过审核，可在策略中加入补卡持卡人」 |
| `REJECTED` | 告警，附驳回原因 |
| `PENDING_REVIEW` | 仅当之前为 `APPROVED` 时告警（修改后重新审核，补卡会跳过） |
| `SUSPENDED`、`DISABLED` | 照旧告警 |

### 7.6 隐私

- 返回完整资料、接收完整资料的接口不记录请求体与响应体；平台错误信息截断后才写入提交记录。
- 审计日志只记持卡人 ID（或商户侧 ID）与操作类型。

## 8. 交易补拉与卡转入转出明细

### 8.1 定时补拉

- 卡池后台新增补拉循环，每 15 分钟一轮；`Kick` 机制支持手动触发。客户端不可用时跳过本轮（同现有循环）。
- 窗口：起点 = `cursor − 2 小时`（首次无 `cursor` 时为 `now − 7 天`），终点 = `now`。起点到终点超过 7 天时，本轮只处理起点后的 7 天。
- 窗口按 24 小时切段，每段依次分页拉取 `GET /v1/card/transactions/authorization` 与 `/settlement`（`page_size` 50，取到 `count` 为止）。一段全部成功后 `cursor` 前移到段终点；任一页失败则本轮停止，`cursor` 不再前移，记 `last_error`。
- 写入沿用 `upsertTxn`（按交易号去重，字段以最新一次为准）。拉取结果里的 `otp` 字段丢弃，不入验证码表。
- 更新 `last_run_at`、`last_added`（本轮新插入的条数）。

### 8.2 「未交卡号出现授权」规则

- 把 `onAuthEvent` 中的停卡与告警抽成公共函数，推送与补拉共用；告警去重键不变（`authunused:<card_id>`）。
- 补拉额外要求交易时间晚于该卡 `mw_cards.created_at`，避免把入池前的历史交易判为异常；推送路径保持原行为。

### 8.3 手动补拉

后台按钮可选「最近 1 / 3 / 7 天」：窗口为 `[now − N 天, now]`，同样分段。只有窗口起点不晚于 `cursor` 时，成功后才把 `cursor` 前移，避免跳过中间的空档。

### 8.4 卡转入转出明细

- 卡详情实时调用 `GET /v1/card/balance-transactions`（`card_id`，时间段为入池时间到现在，最长 90 天）。
- 逐条按 `out_order_no` 匹配本地单据：匹配到标「本系统」，否则标「外部」；平台为 `SUCCESS` 而本地单据为 `failed` / `not_sent` / 仍在途时标红「不一致」，提示到「单据」中重查。

## 9. 后台接口与页面

前缀 `/api/v1/admin`，沿用现有管理员鉴权与 `poolRoute` 错误映射。

| 方法与路径 | 用途 |
|---|---|
| `GET /cardpool/wallet/accounts?refresh=1` | 账户列表（`refresh=1` 先同步） |
| `POST /cardpool/wallet/accounts` | 新建子账户 |
| `POST /cardpool/wallet/accounts/:no/status` | 启停子账户 |
| `GET /cardpool/wallet/receiving-addresses` | 收款地址 |
| `GET /cardpool/wallet/serials` | 账户流水 |
| `POST /cardpool/wallet/conversions` | 发起兑换 |
| `POST /cardpool/wallet/transfers` | 发起钱包转账 |
| `POST /cardpool/cardholders/photos` | 证件照转发 |
| `POST /cardpool/cardholders` | 新建持卡人 |
| `GET /cardpool/cardholders/:id/profile` | 实时完整资料（修改用） |
| `PUT /cardpool/cardholders/:id` | 修改持卡人 |
| `POST /cardpool/cardholders/:id/status` | 启停持卡人 |
| `GET /cardpool/cardholder-submissions` | 提交记录（不含 `linked`） |
| `POST /cardpool/cardholder-submissions/:out_id/abandon` | 放弃提交记录 |
| `GET /cardpool/backfill` / `POST /cardpool/backfill` | 补拉状态 / 手动补拉 |
| `GET /cardpool/cards/:id/balance-transactions` | 卡转入转出明细 |

兑换单、转账单列表沿用 `GET /cardpool/orders?kind=conversion|wallet_transfer`；重查、确认失败沿用现有接口。

前端：

- 新页面路由 `marswallex-wallet`（与 `cardpool` 同级），导航加「MarsWallex 钱包」。五个标签：
  - **账户**：主钱包与子账户、各币种可用 / 冻结余额、状态；新建子账户、启停。
  - **充值地址**。
  - **兑换**：发起表单（卖出币种、金额、买入币种，提交前确认框列出全部参数）+ 兑换单列表（含成交结果、重查、确认失败）。
  - **转账**：发起表单（方向：主钱包 → 子账户 / 子账户 → 主钱包，子账户、币种、金额、备注）+ 转账单列表。
  - **流水**。
- 卡池页：持卡人标签加「新建」「修改」「启用 / 停用」、提交记录区；交易标签加补拉状态与「立即补拉」；卡详情加转入转出明细；单据标签的类型筛选加两种新类型。

## 10. 待联调确认项

1. 交易查询、卡转入转出明细的 `start_time` / `end_time` 单位（按秒实现）、单次最大时间跨度、`page_size` 上限。
2. 账户流水单次最大时间跨度（后台先限制 90 天）。
3. 兑换作用于哪个账户（按主钱包处理）、最小金额、USDT 小数位、查询不存在单号时的响应。
4. 新建子账户是否需要审核（状态含 `INITIALIZING` / `REJECTED`）。
5. 新建持卡人进入 `PENDING_REVIEW` 时是否推送状态变更。
6. 子项目 1 §9 与子项目 2 §10 中尚未确认的各项不变。

## 11. 测试与验收

### 11.1 自动化测试（全部离线，用 `mwtest` 模拟服务器与可注入时钟）

| 对象 | 覆盖 |
|---|---|
| 兑换、转账 | 首次发送 200 / 被拒 / 未知 / 超时 / 未发出；查单定论（含 `FAIL` 与 `FAILED`）；Webhook 定论与 `detail` 写入；非本系统单号；同类型在途唯一；金额、币种、账户方向、余额、子账户停用等校验；30 分钟转人工 |
| 账户 | 多页同步与 `gone` 标记；新建成功与结果未知；启停（含主钱包拒绝、原因长度）；`ACCOUNT_STATUS_CHANGED` 更新与触发同步 |
| 持卡人 | 证件照大小、类型限制与转发；新建成功、被拒、未知后由推送关联、由导入关联、放弃；修改；启停；§7.5 告警表逐行；请求体与响应体不进入日志 |
| 补拉 | 分页取全；失败不前移 `cursor`；重叠窗口去重；长时间停机后每轮 7 天分段追赶；手动补拉的 `cursor` 规则；丢弃 `otp`；「未交卡号」规则与入池时间条件 |
| 卡转入转出明细 | 本系统 / 外部 / 不一致标记 |
| 迁移 | 在已有子项目 2 数据的库上重复执行无误 |

### 11.2 验收标准

1. `go test ./...` 通过（已知原有失败 `TestIssueProductKeepsGPTDefault`、`TestXIssueInfersProductWithoutQueryParameter` 除外）；`go vet ./...` 与前端构建、现有前端测试通过。
2. 填入生产凭证后先做只读验收：账户列表与余额、收款地址、流水查询、交易补拉一轮成功（同时确认 §10 第 1、2 项）。
3. 小额验收（逐步确认）：新建一个子账户；主钱包转 1 USD 到子账户再转回；兑换一笔最小金额的 USDT（确认 §10 第 3、4 项）。
4. 新建持卡人涉及真实 KYC 审核，有实际需要时再验收。
