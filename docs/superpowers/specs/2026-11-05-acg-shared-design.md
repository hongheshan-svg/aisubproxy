# 异次元店铺共享对接设计（代理商全自动 · 子项目 3）

日期：2026-11-05
前置：子项目 2「代理商核心」（PR #26，已合并）
协议依据：`docs/research/2026-11-05-acg-shared-protocol.md`（acg-faka 3.8.5 源码只读调研）

## 1. 目标

代理商在自己的异次元发卡站（lizhipay/acg-faka）后台「店铺共享 → 新增店铺」里填上本站地址、商户 ID、商户密钥，就能把本站当上游货源：导入商品，终端客户在代理商站下单付款后，异次元自动调本站取码、发货。本站从这个代理商的美元余额里按他的拿货价扣款，订单记在他名下。代理商不用装任何插件，全程不用人工。

成功标准：
- 代理商按本文第 9.3 节的步骤接好后，终端客户在代理商站买到码，码可以直接在本站兑换；
- 代理商余额、订单、码列表和本站记录一致；
- 异次元因超时重发同一笔单时，拿到的是第一次的码，不会重复扣款；
- 取码失败（余额不足等）时，代理商在本站后台能看到，补足余额后网关重发即可自动成功。

## 2. 用户已定的决定

| 事项 | 决定 |
|---|---|
| 整体做法 | 做进现有 Go 服务；只实现异次元 `/shared/*`（原版协议 type 0）。Dujiao-Next 放以后 |
| 商户 ID / 密钥 | 代理商在自己后台自助生成；密钥只在生成时显示一次；生成、重置、停用都要输两步验证码；重置后旧密钥立即失效；管理员可以对单个代理商关闭对接。密钥用 `APP_MASTER_KEY` 加密保存（验签要原文） |
| 订单归属 | 和后台手动下单合在一起：同一个余额、同一张订单表，订单标来源（后台 / 异次元）并记对方订单号 |
| 报价币种 | 美元。代理商在异次元店铺档案里把币种设为 USD、自己填汇率；本站不做换算 |
| 发货内容 | 每张码一行：`兑换码  兑换：https://本站/recharge?code=兑换码` |
| 失败提醒 | 后台记下每笔取码失败，代理商总览显示红条；代理商可设余额提醒线 |
| IP 白名单 | 代理商可选填来源 IP（最多 5 个），默认不限 |

我定的（设计评审时用户已确认）：
- 报给异次元的 `price` 就是代理商的拿货价（美元），加价由代理商在异次元里设；
- 库存：就绪时报 200（单次取码上限 `cardplatform.MaxIssueCount`），未就绪报 0；
- 商品都按普通自动发货：无种类、无规格、无自定义下单字段、不开预选卡密；
- 所有商品放在一个分类「全部商品」里。

## 3. 范围

做：
- `/shared/*` 协议接口：connect、items、item、stock、valuation、inventory、inventoryState、trade；
- 把代理商下单的核心逻辑从处理函数里抽出来，后台下单和异次元下单共用；
- 商户密钥的生成、重置、停用，IP 白名单，余额提醒线；
- 取码失败记录、代理商总览红/黄条；
- 订单来源列、对方订单号；管理员查看和开关对接。

不做：
- draft、draftCard、query 接口（返回 404；我们不开预选卡密，下游核心也不调 query）；
- 商品种类、规格、批发阶梯价、自定义下单字段；
- 汇率换算、按代理商定制商品名或说明；
- 全站一键关闭（按代理商关闭已够用）；
- 真实异次元站的端到端联调（上线后由用户用自己的站做）。

兼容范围：下游 acg-faka **≥ 3.6.1**（3.6.1 起才有店铺币种和汇率设置，见调研第 4 条；由 acg-faka 的 git 历史确认 `app/Util/SharedCurrency.php` 在 3.6.1 加入）。更老的版本协议上也能接通，但会把美元数字当本币卖，页面上明确写「不支持」。

## 4. 协议要点（详见调研文档）

- 地址 `{上游}/shared/{controller}/{method}`，全部 POST、`application/x-www-form-urlencoded`。下游每个请求都带 `app_id`、`app_key`（明文）、`sign`。
- 签名：去掉 `sign`；顶层键按字节序排序；删掉值恰为空字符串的顶层键；嵌套数组（如 `sku[颜色]`）保持收到时的顺序、不删空值；每项写成 `键=原始值`（嵌套项写成 `sku[子键]=原始值`），用 `&` 连接，末尾加 `&key=商户密钥`，取 md5 小写十六进制。签名覆盖收到的全部字段，包括 `app_id`、`app_key`。
- 响应：业务结果一律 HTTP 200；成功 `{"code":200,"msg":"success","data":…}`，失败 `{"code":0,"msg":"…"}`。HTTP 4xx/5xx 会让下游丢掉 msg，只在路径不存在时返回 404（下游对可选接口的 404 有降级逻辑）。
- trade 在下游的付款回调事务里同步调用，超时 30 秒；失败整单回滚、订单保持未支付、不退款，靠支付网关重发回调再次调用，`request_no`（下游 18 位数字订单号）不变。
- 下游下单时用 valuation 报价做「实收不得低于上游报价」检查；价格只在导入、买家打开商品详情（开了同步时）、后台手动同步时更新，没有定时任务。

## 5. 数据模型

### 5.1 `agent_shared_creds` 对接设置（新表）

| 列 | 类型 | 说明 |
|---|---|---|
| agent_id | INTEGER PRIMARY KEY | 代理商 |
| app_id | TEXT UNIQUE，可为 NULL | 商户 ID；未生成或已停用时为 NULL |
| key_sealed | TEXT NOT NULL DEFAULT '' | 商户密钥密文；空串表示没有有效密钥 |
| key_created_at | INTEGER | 当前密钥生成时间 |
| last_used_at | INTEGER | 最近一次验签通过的时间（每分钟最多写一次） |
| admin_disabled | INTEGER NOT NULL DEFAULT 0 | 管理员关闭对接 |
| ip_allowlist | TEXT NOT NULL DEFAULT '' | 逗号分隔的 IP 或 CIDR，最多 5 个；空串不限 |
| low_balance_cents | INTEGER NOT NULL DEFAULT 0 CHECK (≥ 0) | 余额提醒线；0 关闭 |
| created_at / updated_at | INTEGER | |

- 状态：`admin_disabled=1` →「已被管理员关闭」；否则 `key_sealed=''` →「未开通」；否则「已开通」。
- 商户 ID：`ag` + 14 位随机字母数字（共 16 位，满足下游 `^[A-Za-z0-9._:@-]{1,32}$`）。
- 商户密钥：32 位随机字母数字（下游限制 1–64 位、无空白）。用 `secretbox.Seal(key, name, 明文)` 加密，`name` 为 `acg-shared:<agent_id>:<app_id>`，把密文绑定到这一行，换行搬不走。
- 重置密钥：`app_id` 不变，换新密钥。停用对接：`app_id` 置 NULL、`key_sealed` 置空；以后再开通会得到新的 `app_id`。管理员关闭不清密钥，重新允许后原密钥继续可用。
- 行在第一次打开对接页或第一次保存设置时创建；没有行等同「未开通、未关闭」。

### 5.2 `agent_orders` 加两列

| 列 | 说明 |
|---|---|
| source TEXT NOT NULL DEFAULT 'web' CHECK (source IN ('web','acg')) | 来源 |
| external_ref TEXT NOT NULL DEFAULT '' | 对方订单号（异次元的 `request_no`） |

- 异次元单的 `request_no` 存成 `acg:` + 对方 `request_no`，和后台下单的请求号不会撞；继续用现有的 `UNIQUE (agent_id, request_no)` 保证幂等。
- 迁移：`ALTER TABLE … ADD COLUMN`，已有行自动是 `web`、空串。

### 5.3 `agent_shared_failures` 取码失败记录（新表）

| 列 | 说明 |
|---|---|
| id | 主键 |
| agent_id | 代理商 |
| request_no | 对方订单号（原值，不带 `acg:`） |
| product_code / quantity | 请求的商品和数量（参数不合法时按收到的原样截断保存） |
| reason | `insufficient_balance` / `not_ready` / `product_unavailable` / `request_conflict` / `rate_limited` / `busy` / `invalid_request` |
| message | 返回给异次元的 msg |
| attempts | 失败次数 |
| first_at / last_at | |
| resolved_order_id | 这笔对方订单号后来成功时填本站订单 ID；NULL 表示未解决 |

- `UNIQUE (agent_id, request_no)`：同一笔重复失败只更新次数、原因、最后时间。
- 只有验签和身份都通过、且 `request_no` 合法时才记（防止被人刷表）。
- 每天的核对任务顺带删除 `last_at` 超过 30 天的行。

## 6. 下单核心

把 `AgentPlaceOrder` 里从「锁请求号」到「建单扣款」的部分抽成：

```go
type agentOrderInput struct {
    AgentID   int64
    Username  string        // 写 issued_by / operator
    ProductCode string
    Quantity  int
    RequestNo string        // 已带前缀（异次元为 "acg:" + 对方单号）
    Note      string
    ExpectedUnitPriceCents int64 // ≤ 0 表示不比价（异次元）
    Source, ExternalRef string
}
type agentOrderOutcome struct {
    Order    db.AgentOrder
    Codes    []exportedCode // 明文码
    Replayed bool
}
func placeAgentOrder(ctx context.Context, in agentOrderInput) (agentOrderOutcome, error)
```

- 步骤和现在一致：检查主密钥 → 锁同号请求 → 查重放（同号同商品同数量返回原码，否则 `errRequestConflict`）→ 取商品 → 取价（`ExpectedUnitPriceCents > 0` 时比价，不同返回 `*priceChangedError`）→ 检查就绪 → 生成并加密码 → `db.CreateAgentOrder`（一个事务里扣余额、写流水、建单、写码）→ 撞同号唯一约束时回到查重放。
- 错误用具名类型返回：`errAgentOrderNoKey`、`errAgentOrderBusy`、`errProductUnavailable`、`*priceChangedError`、`errProductNotReady`、`*db.InsufficientBalanceError`、`errRequestConflict`、`db.ErrAgentDisabled`。
- 后台的 `AgentPlaceOrder` 只做：限流、绑定参数、调 `placeAgentOrder`、按错误映射成现在的 HTTP 状态码和错误编号、写审计、返回 JSON。**现有代理商下单测试全部保持通过、不改断言。**
- 每代理商下单限流（`agentOrderAllowed`）留在后台处理函数里；异次元另有自己的限流（第 10 节）。

## 7. `/shared/*` 接口

### 7.1 公共流程（按顺序）

1. **只接受 HTTPS**：`auth.RequestIsHTTPS` 为假且对端不是本机回环地址时，返回 `请使用 https 地址`。
2. **按来源 IP 限流**（第 10 节），请求体上限 64 KB，只接受 form-urlencoded。
3. **自己解析原始请求体**：保留字段顺序（Go 标准库解析后嵌套数组的顺序会丢）。同名顶层字段以最后一个为准（和 PHP 一致）。
4. **查 `app_id`**：找不到、或该行没有有效密钥 → `商户ID不存在`。
5. **验签失败锁定**：同一商户 ID + 同一 IP 15 分钟内签名错 10 次，锁 15 分钟，期间直接返回 `密钥错误`，不再算签名。未知商户 ID 也按 IP 计数。
6. **验签**：按第 4 节算法，用 `subtle.ConstantTimeCompare` 比较 → 不对返回 `密钥错误`。
7. **IP 白名单**：设了白名单且来源 IP（`c.ClientIP()`，取决于 `TRUSTED_PROXIES`）不在里面 → `来源 IP 不在白名单`。
8. **状态**：管理员关闭 → `对接已被管理员关闭`；代理商停用 → `代理商账号已停用`；主密钥不可用 → `系统维护中`。
9. 更新 `last_used_at`（每分钟最多一次），进入具体接口。

所有响应带 `Cache-Control: no-store`。日志只记商户 ID、接口名和对方订单号，不记密钥、签名和请求体。

### 7.2 金额

美分整数格式化成两位小数的 JSON 数字（`325` → `3.25`），不经过浮点运算。

### 7.3 各接口

| 接口 | 参数 | data |
|---|---|---|
| `authentication/connect` | 无 | `{"shopName": 站点名 + "（美元报价）", "balance": 余额}`；shopName 截到 128 字符 |
| `commodity/items` | 无 | `[{"id":1,"name":"全部商品","sort":0,"icon":"","status":1,"pid":0,"children":[商品…]}]`，商品为这个代理商当前买得了的（已上架、取得到价格），字段同 item |
| `commodity/item` | `code` 或 `sharedCode` | 单个商品对象，见 7.4 |
| `commodity/stock` | `code`，`race`、`sku` | `{"stock": 就绪 ? 200 : 0}` |
| `commodity/valuation` | `code`、`num`，`race`、`sku`、`card_id` | `{"price": 单价 × num, "currency_code":"USD"}`；`num` 不在 1–200 返回错误 |
| `commodity/inventory` | `sharedCode`、`race` | `{"count", "delivery_way":0, "draft_status":0, "price", "user_price":0, "config":"", "factory_price", "is_category":0}` |
| `commodity/inventoryState` | `shared_code`、`num`、`card_id`、`race` | 就绪且 `num` 在 1–200 返回 code 200（data 为空对象），否则 `库存不足` |
| `commodity/trade` | 见 7.5 | `{"url":"", "amount", "tradeNo", "secret", "leave_message":"", "stock"}` |

- 商品找不到 → `商品不存在`；已下架或这个代理商取不到价 → `当前商品已停售`。
- `race` 非空或带 `sku` → `该商品没有可选规格`。
- 就绪判断复用 `agentProductReady`（30 秒内最多同步一次），stock、valuation、inventory 调用时带 10 秒超时，超时按未就绪。

### 7.4 商品对象字段

`id`（agent_products.id）、`code`（agent_products.code）、`name`、`description`（固定一句 HTML：`<p>购买后获得兑换码，卡密里附有兑换链接，打开即可兑换。</p>`，不含图片）、`cover:""`、`price`（拿货价）、`user_price:0`、`factory_price`（拿货价）、`stock`、`config:""`、`widget:"[]"`、`delivery_way:0`、`contact_type:0`、`password_status:0`、`minimum:0`、`maximum:0`、`only_user:0`、`purchase_count:0`、`draft_status:0`、`draft_premium:0`、`inventory_hidden:0`、`seckill_status:0`、`sort`、`status:1`、`category_id:1`。

### 7.5 `commodity/trade`

- 参数：`shared_code`（商品编码）、`num`（1–200）、`request_no`（必填，1–64 位字母数字）。`contact`、`device`、`password`、`card_id` 和其他自定义字段忽略；`race` 非空或带 `sku` 拒绝。
- 整个调用 20 秒超时（下游等 30 秒）。
- 按商户 ID 限流（第 10 节）。
- 调 `placeAgentOrder`：`RequestNo = "acg:" + request_no`，`ExpectedUnitPriceCents = 0`（按当时的价格扣），`Source = "acg"`，`ExternalRef = request_no`，`Note = ""`。
- 成功（含重放）：
  - `secret`：每张码一行，`码  兑换：https://<本次请求的 Host>/recharge?code=<URL 编码后的码>`，行间 `\n`，首尾不留换行；
  - `tradeNo`：`AO` + 本站订单 ID；`amount`：本单扣的美元；`stock`：200；
  - 如果失败表里有这个对方订单号，把 `resolved_order_id` 填上；
  - 写审计 `agent_order`（detail 带 `source=acg`、对方订单号；重放写 `agent_codes_export replay=true`）。
- 失败映射（都是 HTTP 200、code 0，并写失败记录）：

| 错误 | msg | reason |
|---|---|---|
| 余额不足 | `货源余额不足，请联系商家` | insufficient_balance |
| 未就绪、就绪检查超时 | `商品暂时缺货，请稍后再试` | not_ready |
| 商品不存在、下架、取不到价 | `当前商品已停售` | product_unavailable |
| 同号不同商品或数量 | `请求号冲突` | request_conflict |
| 超过限流 | `请求太频繁，请稍后再试` | rate_limited |
| 参数不合法（`num`、`request_no`、带规格） | 具体原因 | invalid_request（`request_no` 本身不合法时不记） |
| 数据库忙、内部错误、等同号锁超时 | `系统繁忙，请稍后再试` | busy |
| 主密钥不可用 | `系统维护中` | 不记（第 7.1 节第 8 步已拦） |

## 8. 失败提醒

- 代理商总览接口 `GET /api/v1/agent/overview` 增加：
  - `shared_failures_24h`：`{"total": N, "insufficient_balance": M}`，统计最近 24 小时 `last_at` 内、未解决的失败；
  - `low_balance`：`{"threshold_cents": X, "below": true/false}`（阈值为 0 时 `below` 恒为 false）。
- 总览页：`total > 0` 显示红条「最近 24 小时有 N 笔异次元订单取码失败（余额不足 M 笔），客户可能已付款但没收到码 → 查看」，点了去对接页；`below` 为真显示黄条「余额低于提醒线 $X」。

## 9. 代理商端

### 9.1 接口（`/api/v1/agent`，沿用登录中间件和 CSRF）

| 方法 路径 | 说明 |
|---|---|
| `GET /shared` | 状态、商户 ID、密钥生成时间、最后使用时间、IP 白名单、余额提醒线、最低版本 `3.6.1` |
| `POST /shared/key` `{code}` | 生成或重置密钥，要两步验证码；返回 `{app_id, app_key}`，只这一次。管理员已关闭时拒绝 |
| `POST /shared/disable` `{code}` | 停用对接，要两步验证码 |
| `PUT /shared/allowlist` `{code, ips:[…]}` | 保存 IP 白名单，要两步验证码；每项是 IPv4/IPv6 或 CIDR，最多 5 项，去重 |
| `PUT /shared/low-balance` `{low_balance_cents}` | 保存余额提醒线（0–1,000,000 美分），不要验证码 |
| `GET /shared/failures?page=` | 最近 30 天失败记录，按 `last_at` 倒序，分页同其他列表 |

两步验证码沿用改密码接口的做法（`agentTOTPAllowed` 名额 + 校验 + 用过的时间片不能再用）。

### 9.2 订单

- 订单对象（列表、详情）增加 `source`、`external_ref`；列表加「来源」列（后台 / 异次元），详情显示对方订单号。
- 代理商码导出的 .csv（前端生成）和流水导出（后端生成的 CSV）在末尾加「来源」「对方订单号」两列，码导出的 .txt 仍是一行一张码、不变；码列表接口每张码带上所属订单的 `source`、`external_ref`。管理员看代理商订单、流水同样显示来源。

### 9.3 「对接异次元」页（`/agent/integration`，侧栏新入口）

- 未开通：说明作用和条件（acg-faka ≥ 3.6.1、本站走 HTTPS），按钮「生成商户密钥」→ 输验证码 → 显示上游地址（当前浏览器打开的站点地址 `location.origin`）、商户 ID、商户密钥，各带复制按钮，红字提示「密钥只显示这一次，关掉后看不到，丢了只能重置」。
- 已开通：上游地址、商户 ID、密钥生成时间、最后使用时间；按钮「重置密钥」（提示旧密钥立即失效，要马上到异次元里更新）、「停用对接」；IP 白名单编辑；余额提醒线。
- 已被管理员关闭：显示状态，生成和重置按钮不可用。
- 接入步骤（固定文字）：
  1. 异次元后台「店铺共享 → 新增店铺」，填上游地址、商户 ID、商户密钥；
  2. **在店铺档案里把币种设为 USD，并填好汇率（1 美元 = ? 元）**——红字：不设会把 $3.25 当成 ¥3.25 卖，亏本；
  3. 「导入商品」时设加价，建议打开价格同步；
  4. 本站调价后，到异次元点「同步」，否则下单时会被异次元以「商品价格已变动」拦下。
- 最近失败记录表：时间、对方订单号、商品、数量、原因、次数、状态（未解决 / 已解决）。

## 10. 安全与限流

| 项 | 规则 |
|---|---|
| 按来源 IP | 整个 `/shared/*` 每分钟 120 次，超过返回 `请求太频繁，请稍后再试`（code 0） |
| 按商户 ID 取码 | trade 每分钟 60 次 |
| 签名错误 | 同一商户 ID + IP 15 分钟 10 次，锁 15 分钟；未知商户 ID 按 IP 计 |
| HTTPS | 必须；靠 `TRUSTED_PROXIES` 识别反代后的 HTTPS |
| 密钥 | 加密保存，管理员看不到，日志和审计只记商户 ID |
| 重放 | 协议没有时间戳；重放 trade 只会拿回同一批码，不会重复扣款，且需要先破解 HTTPS 才拿得到请求 |

审计：`agent_shared_key_create`、`agent_shared_key_reset`、`agent_shared_disable`、`agent_shared_allowlist`（记条数）、`admin_agent_shared_disable`、`admin_agent_shared_enable`；异次元取码沿用 `agent_order`。

## 11. 管理员端

- 接口：`GET /api/v1/admin/agents/:id/shared`（状态、商户 ID、最后使用时间、IP 白名单、最近 50 条失败），`POST /api/v1/admin/agents/:id/shared/disable`、`/enable`。
- 代理商详情页加「异次元对接」区：上面这些信息只读 + 「关闭对接」「重新允许」按钮。管理员不能查看或重置密钥。
- 代理商订单列表、流水显示来源。

## 12. 出错处理

| 情况 | 行为 |
|---|---|
| 数据库忙、内部错误 | `系统繁忙，请稍后再试`；异次元回滚，等网关重发；失败记录尽力写 |
| 主密钥不可用 | 所有 `/shared/*` 返回 `系统维护中`；对接页不能生成密钥 |
| 密钥解不开（换过主密钥） | 当作没有有效密钥：`商户ID不存在`；对接页提示「密钥已失效，请重置」 |
| panic | gin 恢复成 500，下游显示「商品暂时无法购买」 |
| 下游在我们建单后才超时 | 下游重发同号请求，我们按幂等返回原码 |
| 本站调价而下游没同步 | 涨价：下游下单时被自己的成本检查拦下，客户付不了款，不亏钱；降价：代理商毛利变大 |

## 13. 测试

- `internal/acgshared`（纯函数）：
  - 签名：手写规范串和 md5 期望值，覆盖排序、删空串、保留 `0`、嵌套 `sku[…]` 顺序、含 `&`、`=`、`+`、中文、空格的值；
  - 原始请求体解析：顺序、重复键、非法编码；
  - 金额格式化；商品对象字段满足调研第 3、9 条的下游校验（名字长度、数字范围、整数字段）。
- 处理函数：
  - 每个接口的正常返回；
  - 错误一律 HTTP 200 + code 0：未知商户 ID、签名错及锁定、IP 白名单、HTTP 被拒、管理员关闭、代理商停用、无主密钥、未实现路径 404；
  - trade：成功、重放拿回同一批码、同号冲突、同号并发只建一单、余额不足写失败记录 → 补足余额后同号成功并标已解决、未就绪、20 秒超时；
  - 代理商端接口：生成要验证码、只返回一次、重置后旧密钥验签失败、停用后商户 ID 失效、管理员关闭后生成被拒、白名单格式校验。
- 抽出下单核心后，现有代理商下单测试不改断言全部通过。
- 前端：纯函数单元测试（状态判断、横条文案、白名单解析）；页面交互人工点检。
- 人工点检：本机临时实例 + 测试脚本按异次元算法签名请求，模拟下游走一遍导入、询价、取码、重放、余额不足。不运行下载的 acg-faka 代码。

## 14. 升级注意（写进 CHANGELOG）

- 升级前必须做：第 4 条 `TRUSTED_PROXIES` 补一句——异次元对接也靠它识别 HTTPS，没配对时所有对接请求都会被拒（「请使用 https 地址」）。
- 升级后要确认：给代理商开通对接时，告诉他们 acg-faka 要 ≥ 3.6.1，店铺档案币种设 USD 并填汇率。
- 行为变化（无需操作）：新增 2 张表、`agent_orders` 加两列（自动迁移）；后端开始占用 `/shared/*` 路径；代理商码导出的 .csv 和流水导出多「来源」「对方订单号」两列（.txt 不变）。

## 15. 和后续子项目的关系

- 子项目 4（自动充值）：代理商余额不足时的红条会引导去充值页；失败记录在充值到账后靠网关重发自动解决，不需要额外联动。
- 子项目 5（自动换码）：异次元来的码和后台买的码一样记在代理商名下，换码逻辑不用区分来源。
- Dujiao-Next（`/api/v1/upstream/*`）以后做时复用第 6 节的下单核心和 `source` 列（新增取值）。
