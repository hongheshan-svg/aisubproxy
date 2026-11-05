# 代理商核心设计（代理商全自动 · 子项目 2）

日期：2026-11-04　分支：`feat/agent-core`（基于 master ed5bece）

本文是「代理商全自动」项目的第 2 个子项目，整体拆分见文末「所属项目」。

## 1. 目标

代理商自己登录后台，用美元余额下单取码、导出、查看自己的码和流水；我们按代理商对账。以后不再靠管理员在后台发码、再线下发 .txt。

成功标准：
- 管理员建号、定价后，代理商不找人工就能完成「下单 → 拿码 → 导出 → 对账」。
- 任何时候，每个代理商的余额都等于他全部流水的累加，余额不会小于 0。
- 同一笔下单无论重试多少次，只扣一次钱、只出一批码。
- 代理商只能看到自己的数据，代理商会话碰不到任何管理员接口。

## 2. 用户已定的决定

来自 2026-11-04 的头脑风暴：

| 事项 | 决定 |
|---|---|
| 开户 | 只能由管理员在后台建号，不开放注册 |
| 币种 | 余额和批发价都用美元 |
| 定价 | 分等级，每级一张价格表；单个代理商可以覆盖某个商品的价格 |
| 第一版能买的码 | 只卖本站码（MarsWallex）；卡台码（ZovoCard）以后再加 |
| 码的保存 | 代理商买到的码加密保存，代理商随时能重新导出 |
| 登录 | 密码 + 强制两步验证（TOTP） |
| 退码 | 不支持。特殊情况由管理员作废码，再手动调账 |
| 整体做法 | 方案 A：代理商做成独立的一块，和管理员完全分开 |
| 页面语言 | 代理商后台第一版只有中文 |

## 3. 范围

**做：**
- 管理员：建号、改资料、停用/启用、重置密码、重置两步验证、手动调账；管理等级、批发商品、等级价格表、单个代理商覆盖价；查看所有代理商的流水和订单；对账总览。
- 代理商：登录（首次改密码、绑定两步验证）、概览、下单、我的码、订单、余额流水、改密码。
- 余额账本：只追加的流水；一个底层函数负责所有余额变动；每天核对一次。
- 为后续子项目留好的接口：按外部单号防重复的入账函数（子项目 4）；按请求号防重复的下单函数、固定的商品编码（子项目 3）；每张码记下属于哪个代理商（子项目 5）。

**不做：**
- 卡台码；
- 退码；
- 子账号；
- 每日下单上限：余额是预付的，本身就是上限；账号被盗由强制两步验证来防；
- 代理商后台的英文界面；
- 自动充值、异次元对接、自动换码，分别是子项目 4、3、5；
- 改造现有的 `/partner/swap` 代理换码页，它照常工作。

## 4. 数据模型

新建 7 张表，现有的 `local_cdks` 加 2 列。按现有约定：
- 金额一律存美分整数（`_cents`）；
- 时间存 Unix 秒（INTEGER）；
- 启动时用 `CREATE TABLE IF NOT EXISTS` 建表，用 `pragma_table_info` 判断后补列，可以重复执行。

### 4.1 `agent_tiers` 等级

| 列 | 说明 |
|---|---|
| `id` | 主键 |
| `name` | 等级名，唯一，例如「普通」「金牌」 |
| `note` | 备注 |
| `created_at`、`updated_at` | |

还有代理商在用的等级不能删除。

### 4.2 `agents` 代理商

| 列 | 说明 |
|---|---|
| `id` | 主键 |
| `username` | 登录名，唯一，不区分大小写；3–32 位，只能用字母、数字、`_`、`-` |
| `display_name` | 显示名 |
| `password_hash` | bcrypt |
| `must_change_password` | 1 表示下次登录必须改密码（建号和重置密码后为 1） |
| `totp_sealed` | 两步验证密钥的密文，空串表示还没绑定。用 `secretbox`（`APP_MASTER_KEY`）加密，AAD 为 `"agent_totp:" + id` |
| `totp_last_step` | 最后一次用过的 TOTP 时间窗编号，防止同一个码用两次 |
| `tier_id` | 所属等级，必填 |
| `balance_cents` | 当前余额，`CHECK (balance_cents >= 0)` |
| `status` | `active` / `disabled` |
| `session_version` | 会话版本号。停用、重置密码、重置两步验证、代理商自己改密码时加 1 |
| `note` | 管理员备注 |
| `last_login_at` | 最后一次完整登录（通过两步验证）的时间 |
| `created_at`、`updated_at` | |

### 4.3 `agent_products` 批发商品

| 列 | 说明 |
|---|---|
| `id` | 主键 |
| `code` | 商品编码，唯一，建好后不能改；2–48 位，只能用小写字母、数字、`-`。新增时默认 `mw-<套餐>-<地区小写>`，例如 `mw-plus-ph`，建好前可以改 |
| `provider` | 服务商，第一版只能是 `marswallex` |
| `product` | 产品线，第一版只能是 `gpt` |
| `plan_key` | 套餐，必须在这家服务商支持的套餐里 |
| `region` | 地区，必须在这家服务商支持的地区里 |
| `name` | 显示名，例如「ChatGPT Plus · 菲律宾」 |
| `enabled` | 上架 1 / 下架 0 |
| `sort` | 排序，小的在前 |
| `created_at`、`updated_at` | |

`(provider, product, plan_key, region)` 唯一。商品不能删除，只能下架，因为订单里引用了它。

### 4.4 `agent_tier_prices` 等级价格表

`(tier_id, product_id)` 为主键，存 `price_cents`，必须大于 0。某个等级没填价的商品，这个等级的代理商看不到，也买不了。

### 4.5 `agent_price_overrides` 单个代理商覆盖价

`(agent_id, product_id)` 为主键，存 `price_cents`，必须大于 0。

**取价顺序**：先看这个代理商的覆盖价，再看他所在等级的价格，两个都没有就不能买。

### 4.6 `agent_orders` 下单记录

| 列 | 说明 |
|---|---|
| `id` | 主键，也是对外的订单号 |
| `agent_id` | |
| `product_id` | |
| `product_code`、`provider`、`product`、`plan_key`、`region`、`product_name` | 下单时的商品快照 |
| `quantity` | 1–200 |
| `unit_price_cents`、`total_cents` | 下单时的价格快照 |
| `request_no` | 请求号，1–64 位，只能用 `A-Za-z0-9_-.:` |
| `note` | 代理商备注，200 字以内 |
| `created_at` | |

`(agent_id, request_no)` 唯一。只有成功的订单才会写进这张表（见 5.1），所以没有状态列。

### 4.7 `agent_ledger` 余额流水

| 列 | 说明 |
|---|---|
| `id` | 主键 |
| `agent_id` | |
| `kind` | `purchase` 下单 / `adjust` 手动调账 / `topup` 充值（子项目 4 用） |
| `amount_cents` | 带正负号，不能为 0 |
| `balance_after_cents` | 这条流水之后的余额 |
| `order_id` | `purchase` 时为订单号，其余为空 |
| `ext_ref` | 外部单号。`adjust` 为 `"admin:" + 幂等键`；`topup` 为充值单号；`purchase` 为空串 |
| `operator` | 操作人：`admin:<用户名>` / `agent:<登录名>` / `system:<来源>` |
| `note` | 原因或备注。`adjust` 必填 |
| `created_at` | |

索引：
- 部分唯一索引 `(kind, ext_ref) WHERE ext_ref <> ''`：同一个外部单号只能入账一次；
- 部分唯一索引 `(order_id) WHERE kind = 'purchase'`：一笔订单只扣一次钱；
- `(agent_id, id)` 普通索引，用于按代理商列流水。

流水只追加，代码里没有任何修改或删除它的路径。

### 4.8 `local_cdks` 加两列

- `agent_id INTEGER NOT NULL DEFAULT 0`：属于哪个代理商，0 表示不属于任何代理商（后台发的码）；
- `agent_order_id INTEGER NOT NULL DEFAULT 0`：来自哪一笔订单。

再加索引 `(agent_id, id)`。

代理商买的码：
- `issued_by = "agent:<登录名>"`；
- `note` 为订单备注；
- `code_sealed` 存完整码的密文，加密方式和售后查询的换码一样（`sealLocalCDKCode`，AAD 绑定 `code_hash`）。

`/partner/swap` 换码时，`ExchangeLocalCDK` 写入的新码继承旧码的 `agent_id` 和 `agent_order_id`。这样代理商在「我的码」里也能看到换出来的新码。

**谁能解密 `code_sealed`**：
- 售后查询：只解密换码链上的新码，和现在一样；
- 代理商导出：只解密属于这个代理商自己的码。

管理员列表、审计、日志都读不到明文。

## 5. 下单和账本

### 5.1 代理商下单

输入：`product_code`、`quantity`（1–200）、`note`（200 字以内）、`request_no`。网页下单时请求号由前端自动生成，内容不变就沿用同一个（复用 `lib/idempotency.ts` 的做法）。

1. **事务之外先检查**，任何一项不满足就直接拒绝：
   - 账号是 `active`；
   - 商品存在并已上架，这个代理商取得到价格；
   - 这个套餐 × 地区现在能出码。检查和后台发码共用同一套 `checkCardSupply`：服务商至少成功过一笔真实付款，并且这个组合已开启；
   - `APP_MASTER_KEY` 可用。
2. **查请求号**：`(agent_id, request_no)` 已经有订单时，转到 5.2。
3. **生成码**：用 `provider.GenerateCode` 在内存里生成 N 张，逐张算哈希、加密。加密失败就整单拒绝。
4. **在一个事务里完成下面几步**（`_txlock=immediate`，即 `BEGIN IMMEDIATE`）：
   1. 插入订单。如果撞上 `(agent_id, request_no)` 唯一约束，回滚，转到 5.2；
   2. 扣余额：`UPDATE agents SET balance_cents = balance_cents - ? WHERE id = ? AND status = 'active' AND balance_cents >= ?`。影响 0 行就回滚，返回「余额不足」，并算出还差多少；
   3. 写一条 `purchase` 流水，金额为负，带上变动后的余额和订单号；
   4. 插入 N 张 `local_cdks`，带上 `agent_id`、`agent_order_id`、`code_sealed`；
   5. 提交。
5. **返回**订单和完整的码，响应头 `Cache-Control: no-store`。
6. **写审计** `agent_order`：商品编码、数量、总价、订单号，不写码。

整个过程里，扣钱、写流水、写订单、写码要么全成，要么全不成。

### 5.2 同一个请求号再次提交

- 商品编码和数量都和原订单一样（备注不参与比较）：原样返回原订单，码从 `code_sealed` 解密取回，不重复扣钱。网络断了或者前端重试都安全。
- 商品编码或数量不一样：返回 409「请求号已用于另一笔订单」。

### 5.3 唯一的余额变动函数

整个系统里，只有一个底层函数能改 `balance_cents`。它在调用方给的事务里做两件事：
- 条件更新余额：增加金额时直接加，扣减时要求余额够；
- 写一条流水，带上变动后的余额。

下单、调账、充值都通过它。

对外的三个入口：

| 入口 | 用途 | 防重复 |
|---|---|---|
| 下单（5.1） | `purchase` | `(agent_id, request_no)`，以及每笔订单只能有一条 `purchase` 流水 |
| 管理员调账 | `adjust`，金额可正可负，原因必填 | 请求头必须带 `Idempotency-Key`，`ext_ref = "admin:" + 键`。同一个键再次提交时，返回第一次的流水 |
| `Credit(ctx, agentID, kind, amountCents, extRef, operator, note)` | 子项目 4 的充值入账，第一版只有测试调用 | `(kind, ext_ref)`。同一个外部单号再次调用，返回第一次的流水，不再加钱 |

扣减后余额不能小于 0。调账扣多了会被拒绝，提示当前余额。

### 5.4 每天核对一次

挂在现有的后台定时任务（`handler/ops_maintenance.go`）上，每天核对一次，逐个代理商检查：
- `balance_cents = SUM(agent_ledger.amount_cents)`；
- `balance_cents` = 最后一条流水的 `balance_after_cents`（没有流水时余额应为 0）。

核对结果存进 `site_settings` 的 `agent_reconcile_last`，内容是 JSON：时间、是否一致、对不上的代理商 ID 列表。对不上时：
- 用 `opsAlertDaily` 发 Telegram 告警，只写代理商 ID 和差额；
- 后台对账总览标红。

系统不自动修正。

### 5.5 其他规则

- 订单里记下单时的单价，以后改价不影响已下的单。
- 停用代理商后，他已经卖出去的码照常能用。要作废的话，管理员在本站码列表里单独禁用，不退款。
- 管理员禁用代理商的码不会退款；需要补偿时手动调账。

## 6. 登录和安全

### 6.1 开户和首次登录

- 管理员建号时填登录名、显示名、等级、备注。系统生成 16 位随机初始密码（crypto/rand，去掉易混字符），只在建号结果里显示一次，同时设 `must_change_password = 1`。
- 代理商第一次登录，必须按顺序做完两件事，之后才发正式会话：
  1. 改密码：至少 12 位，用 `db.IsWeakPassword` 拦截弱口令，不能和旧密码相同；
  2. 绑定两步验证：服务端生成 20 字节随机密钥，在页面上显示二维码和 base32 密钥；代理商用验证器 App 扫码后输一次 6 位码，确认无误才把密钥加密存进 `totp_sealed`。
- 二维码在浏览器本地生成（前端新增依赖 npm `qrcode`，MIT 许可证），密钥不发给任何第三方。

### 6.2 日常登录

1. `POST /api/v1/agent/auth/login {username, password}`。密码对了，就发一个 5 分钟有效的**临时凭证**，并告诉前端下一步做什么：`change_password` / `setup_totp` / `totp`。
2. 前端按要求依次完成下一步。改密码会让 `session_version` 加 1，所以每一步通过后都换发新的临时凭证。最后一步通过后，发**正式会话**，更新 `last_login_at`。

限流：
- 密码：按「IP + 小写登录名」计，15 分钟内错 5 次，锁 15 分钟，和管理员登录一样，但用单独的计数器；
- 动态码：每个临时凭证最多错 5 次，之后这个凭证作废，要重新输密码。

停用的账号直接拒绝登录，提示和密码错误一样，不暴露账号是否存在。

### 6.3 TOTP

- 按 RFC 6238：HMAC-SHA1，30 秒一个时间窗，6 位数字。允许前后各差一个时间窗。
- 验证成功后，把用到的时间窗编号写进 `totp_last_step`。编号不大于它的码一律拒绝，所以同一个码不能用第二次。
- 算法自己实现，不加新的 Go 依赖，用 RFC 6238 附录 B 的测试向量验证。
- 密钥、动态码都不进日志、审计、接口响应。唯一的例外是绑定时显示一次的那个密钥。

### 6.4 会话

**签名密钥**：从 `JWT_SECRET` 派生，`HMAC-SHA256(JWT_SECRET, "aisubproxy/agent-session/v1")`。管理员令牌用的是原始的 `JWT_SECRET`，所以两边的令牌互相验不过，不用指望每个接口都记得判断身份。

**正式会话**
- JWT HS256，`aud = "agent"`，带代理商 ID 和 `session_version`，有效期 12 小时。
- 放在 cookie `agent_token` 里：HttpOnly，`Path=/api/v1/agent`，SameSite=Strict，HTTPS 下加 Secure。

**临时凭证**
- `aud = "agent-pre"`，带代理商 ID、`session_version`、阶段、随机编号，有效期 5 分钟。
- 放在 cookie `agent_pre` 里，`Path=/api/v1/agent/auth`。

**CSRF**
- cookie `agent_csrf`，JS 可读，`Path=/`；
- 代理商接口的写请求必须带 `X-CSRF-Token`，并且和这个 cookie 一致。

**代理商中间件**
- 只认 cookie，不认 Bearer 头；
- 每次请求都查一次库：账号必须是 `active`，`session_version` 必须和令牌里的一致，否则返回 401；
- 把代理商 ID 放进 gin context。代理商接口里的所有查询都只用这个 ID，绝不从请求参数里取代理商 ID。

退出登录时清掉三个 cookie。停用、重置密码、重置两步验证、改密码都会让 `session_version` 加 1，已登录的会话立刻失效。代理商在「账号」页改完密码后，前端回到登录页重新登录。

### 6.5 限流

- 下单：每个代理商每分钟最多 30 次，内存计数。
- 代理商的其他读接口：沿用现有 `LookupGuard` 的做法，按 IP 计。

### 6.6 审计和日志

**管理员操作**写 `admin_audit_logs`，action 依次为：
- `agent_create`、`agent_update`、`agent_disable`、`agent_enable`；
- `agent_reset_password`、`agent_reset_totp`、`agent_adjust`；
- `agent_tier_*`、`agent_product_*`、`agent_price_update`、`agent_override_update`。

**代理商操作**也写 `admin_audit_logs`，username 为 `agent:<登录名>`，action 依次为：
- `agent_login`、`agent_login_failed`；
- `agent_password_change`、`agent_totp_bind`；
- `agent_order`、`agent_codes_export`。

导出只记张数和筛选条件。

**任何地方都不出现**完整的码、密码、初始密码、两步验证密钥、动态码。访问日志现有的查询串遮蔽规则照旧；导出用 POST，码不会出现在 URL 里。

## 7. 接口

### 7.1 代理商（`/api/v1/agent`）

| 方法和路径 | 说明 | 凭证 |
|---|---|---|
| `POST /auth/login` | 输密码，返回下一步 | 无 |
| `POST /auth/password/initial` | 首次或重置后改密码 | 临时凭证 |
| `GET /auth/totp/setup` | 取绑定用的密钥和 otpauth URI，只在还没绑定时可用 | 临时凭证 |
| `POST /auth/totp/confirm` | 绑定并发正式会话 | 临时凭证 |
| `POST /auth/totp` | 输动态码，发正式会话 | 临时凭证 |
| `POST /auth/logout` | 退出 | 任意 |
| `GET /me` | 资料、余额 | 会话 |
| `POST /me/password` | 改密码：当前密码 + 新密码 + 动态码 | 会话 |
| `GET /overview` | 概览统计 | 会话 |
| `GET /products` | 能买的商品、单价、能否出码 | 会话 |
| `POST /orders` | 下单 | 会话 |
| `GET /orders`、`GET /orders/:id` | 订单列表和详情，不含码 | 会话 |
| `POST /orders/:id/export` | 导出这笔订单的完整码 | 会话 |
| `GET /codes` | 我的码，不含完整码；可以按商品、状态、订单、时间筛选，分页 | 会话 |
| `POST /codes/export` | 按勾选的 ID 或筛选条件导出完整码，单次最多 10000 张 | 会话 |
| `GET /ledger` | 流水和期初、期末、收入、支出汇总；分页 | 会话 |
| `GET /ledger/export` | 流水 CSV | 会话 |

### 7.2 管理员（`/api/v1/admin`，沿用现有的管理员中间件）

| 方法和路径 | 说明 |
|---|---|
| `GET /agents`、`POST /agents` | 列表；建号（响应里带一次初始密码） |
| `GET /agents/:id`、`PUT /agents/:id` | 详情；改显示名、等级、备注 |
| `POST /agents/:id/disable`、`/enable` | 停用、启用 |
| `POST /agents/:id/reset-password` | 重置密码，响应里带一次新的临时密码 |
| `POST /agents/:id/reset-totp` | 清掉两步验证，下次登录重新绑定 |
| `POST /agents/:id/adjust` | 调账，`{amount_cents, note}`，必须带 `Idempotency-Key` |
| `GET /agents/:id/ledger`、`/ledger/export`、`/orders` | 这个代理商的流水、流水导出、订单 |
| `GET /agents/:id/overrides`、`PUT /agents/:id/overrides` | 覆盖价 |
| `GET /agent-summary` | 对账总览 |
| `GET /agent-tiers`、`POST`、`PUT /:id`、`DELETE /:id` | 等级 |
| `GET /agent-products`、`POST`、`PUT /:id` | 批发商品（编码不能改） |
| `GET /agent-prices`、`PUT /agent-prices` | 等级 × 商品价格矩阵 |
| `GET /cdks?agent_id=` | 现有的本站码列表，新增按代理商筛选，返回的每行带代理商登录名 |

## 8. 页面

### 8.1 代理商后台 `/agent`

用单独的布局 `AgentLayout.vue`。请求统一走 `lib/agentApi.ts`：带 cookie，写请求自动加 `X-CSRF-Token`，遇到 401 回登录页。

| 路由 | 内容 |
|---|---|
| `/agent/login` | 先输密码，再输动态码。第一次登录按顺序进入改密码、绑定两步验证 |
| `/agent` | 概览：当前余额、本月下单金额、各状态码的张数、最近 5 笔订单。码的状态用 `DeriveLocalCDK` 现算，对应关系：unused → 未使用，reserved → 处理中，consumed → 已开通，failed → 失败，disabled → 已禁用 |
| `/agent/order` | 下单：列出能买的商品（显示名、套餐、地区、单价），暂不可售的标灰。选好商品、数量、备注后弹出确认框，写明总价和下单后的余额。下单结果显示全部码，可以「复制全部」「下载 .txt」 |
| `/agent/codes` | 我的码。每张码显示：前缀、商品、状态和原因、购买时间、订单号、备注。已开通的码再显示打码邮箱和开通时间，取法和首页售后查询的 `cardSupplyFacts` 相同。可以按商品、状态、订单、时间筛选。勾选的码或筛选出来的全部码都能导出 .txt 或 .csv |
| `/agent/orders`、`/agent/orders/:id` | 订单列表和详情，详情页可以重新导出这笔订单的全部码 |
| `/agent/ledger` | 余额流水，可以选时间范围。顶部显示期初余额、期末余额、收入合计、支出合计，可以导出 CSV |
| `/agent/account` | 改密码 |

**导出格式**
- .txt：一行一张码。
- .csv：带 BOM，列为 `code, product_code, plan, region, status, purchased_at, order_id, note`，时间按北京时间。

金额用整数运算格式化成 `$1,234.56`，不经过浮点数。

### 8.2 管理员后台 `/ops`

侧栏新增「代理商」一组。

| 路由 | 内容 |
|---|---|
| `/ops/agents` | 代理商列表：登录名、显示名、等级、余额、状态、最后登录时间、码的张数。可以在这里建号。每行的操作有：编辑、停用/启用、重置密码、重置两步验证、调账。页面顶部是对账总览：全部代理商余额合计、今天和本月的下单合计、最近一次核对的时间和结果，对不上时标红 |
| `/ops/agents/:id` | 资料、覆盖价、流水（可以导出）、订单、这个代理商的码 |
| `/ops/agent-pricing` | 三部分：<br>1. 商品管理：新增商品时，从 MarsWallex 已支持的「套餐 × 地区」里选；编码自动生成，建好前可以改；可以上下架、调整排序。<br>2. 价格矩阵：每行一个商品，每列一个等级，直接在格子里填价，留空表示这个等级不卖。<br>3. 等级管理 |
| 本站码列表（现有） | 加一列「代理商」，并且可以按代理商筛选 |

所有时间都按北京时间显示；「今天」「本月」也按北京时间划分。

## 9. 出错时怎么处理

| 情况 | HTTP 状态和 `error` 编号 | 代理商看到的提示 |
|---|---|---|
| 余额不够 | 409 `insufficient_balance`，带 `shortfall_cents` | 「余额不足，还差 $x.xx」 |
| 商品不存在、已下架，或这个代理商没有价格 | 404 `product_unavailable` | 「该商品暂不可售」 |
| 这个套餐 × 地区现在不能出码 | 409 `product_not_ready` | 「该商品暂时无法出码，请稍后再试」 |
| 没配 `APP_MASTER_KEY` | 503 `maintenance` | 「系统维护中，暂不能下单」。同时用 `opsAlertDaily` 每天告警一次 |
| 请求号已用于另一笔订单 | 409 `request_no_conflict` | 「请求号已用于另一笔订单」 |
| 数据库忙、码的哈希撞上唯一约束 | 503 `busy` | 「系统繁忙，请稍后重试」。用同一个请求号重试是安全的 |
| 下单太频繁 | 429 `rate_limited` | 「操作太频繁，请稍后再试」 |
| 导出时某张码解不开 | 200，这张码的 `code` 为空、`unavailable = true` | 这张码标成「无法取回，请联系管理员」，其余照常导出。同时告警，只写码 ID |
| 会话失效 | 401 | 回到登录页 |

注意：上线后 `APP_MASTER_KEY` 不能更换，否则已加密的码和两步验证密钥都解不开。CHANGELOG 和部署文档要写明。

## 10. 测试

**后端**
- 钱：
  - 并发下单：用临时文件建库，DSN 和生产一样带 `_txlock=immediate`，起多个 goroutine 同时下单，总额超过余额。只有余额够的那几单成功，余额不小于 0，余额等于流水累加。现有的 `dbtest.Open` 是内存库、只开一个连接，测不出并发，所以要单独建库。
  - 同一个请求号提交两次：只扣一次钱，两次拿到的码一样。请求号相同但商品或数量不同：返回 409。
  - 下单时事务里任何一步失败（余额不足、码的哈希冲突），订单、流水、码一条都不留。
  - `Credit` 同一个外部单号调两次：只加一次钱，第二次返回第一次的流水。
  - 调账：同一个幂等键提交两次只记一次；扣减超过余额被拒绝；原因为空被拒绝。
  - 每天核对：故意改坏余额，能检测出来并发出告警。
- 取价：先取覆盖价，再取等级价，两个都没有就不能买；下架的商品不能买。
- 会话：
  - 代理商令牌过不了管理员中间件，管理员令牌过不了代理商中间件；
  - 停用账号、`session_version` 变了以后，旧会话返回 401；
  - 写请求不带 CSRF 头返回 403；
  - 临时凭证不能当正式会话用。
- 越权：代理商 A 拿 B 的订单号、码 ID 去查、去导出，都查不到；流水只返回自己的。
- 两步验证：
  - RFC 6238 附录 B 的 SHA1 测试向量；
  - 前后各一个时间窗内的码通过，再远的拒绝；
  - 同一个码第二次被拒绝；
  - 同一个临时凭证错 5 次后作废。
- 首次登录：没改密码、没绑两步验证之前，拿不到正式会话。
- 敏感信息：下单、导出、建号、重置密码之后，检查审计表和日志输出里不出现完整的码、密码、密钥。
- 换码继承：`/partner/swap` 换出的新码带上旧码的 `agent_id`。

**前端**
- 金额格式化、导出的 .txt / .csv 内容、下单确认框的总价计算，用 vitest。
- 主要页面的渲染，沿用 `home.spec.ts` 的 SSR 渲染测试写法。
- 项目没有 DOM 测试环境，点击类的操作（下单、导出、扫码绑定）在本机临时实例上用假数据人工点一遍。不调真实接口，不动真实资金。

## 11. 升级和部署

- 启动时自动建 7 张新表，给 `local_cdks` 补 `agent_id`、`agent_order_id` 两列和索引，可以重复执行。
- 现有功能只有两处变化：
  - 本站码列表多一列「代理商」，可以按代理商筛选；
  - `/partner/swap` 换出的新码继承旧码的代理商。
- 代理商下单需要 `APP_MASTER_KEY`；上线后不能更换。
- 前端新增依赖 `qrcode`。
- CHANGELOG 写进「未发布」一节。不发版，等用户说了再发。

## 12. 和后续子项目的接口

| 子项目 | 用到本子项目的什么 |
|---|---|
| 3 异次元 `/shared/*` | `agent_products.code` 当商品编码；下单函数按 `request_no` 防重复，重复请求返回第一次的码；余额用于 `authentication/connect`。商户 ID 和密钥另行设计 |
| 4 自动充值 | `Credit(..., kind="topup", extRef=充值单号, ...)`，按外部单号防重复 |
| 5 自动售后 | `local_cdks.agent_id` 区分码属于哪个代理商；自动换出的新码同样加密保存、继承代理商 |
| 第二版 卡台码 | `agent_products.provider` 已经留好；到时要加「上游结果不明时冻结余额」的处理 |

## 所属项目：代理商全自动（2026-11-04 用户确认的拆分）

| 顺序 | 子项目 | 依赖 |
|---|---|---|
| 1 | 首页售后查询（已合并，PR #22） | 无 |
| 2 | 代理商核心（本文） | 无 |
| 3 | 异次元发卡店铺共享协议 `/shared/*` | 2 |
| 4 | 自动充值：易支付（人民币按汇率折成美元）、BEpusdt（USDT 按 1:1），入账前都要复核 | 2 |
| 5 | 自动售后：失败并确认没扣款时自动换码，额度按代理商分开 | 1、2 |
| 第二版 | Dujiao-Next 对接协议；卡台码 | 2 |

调研依据：`docs/research/2026-11-04-faka-upstream-protocols.md`、`docs/research/2026-11-04-agent-topup-payments.md`。
