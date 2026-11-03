# 后台菜单按服务商整理、卡头与 MarsWallex 选卡规则设计

- 日期：2026-11-03
- 状态：已确认设计（用户 2026-11-03 确认「按之前的开始实行」）
- 参考：`docs/marswallex-openapi-zh.md`、`backend/internal/provider/`、`backend/internal/cardpool/`

## 1. 背景与目标

后台左侧菜单里，通用能力和某一家服务商专有的功能混在一起：

- 「CDK 兑换订单」只查 ZovoCard 卡台的订单，却放在「卡密业务」里。
- 「卡台 ZovoCard」页里混着代理换码的设置，而代理换码对所有服务商都适用。
- MarsWallex 的选卡配置（各套餐的产品优先级、自动停用）藏在「卡池 → 策略与健康度」里。

服务商和每家的卡头（卡产品）都会继续增加。

**目标**：

1. 菜单分成「通用能力」和「每家服务商一组」两类。
2. 每家服务商有自己的卡头列表：有接口的自动读取，没有接口的在后台手动配置；3DS、Apple Pay 这类接口不给的信息可以手动标注。
3. MarsWallex 有单独的「选卡规则」页。

## 2. 已确认的决定

| 事项 | 决定 |
|---|---|
| 菜单结构 | 按服务商分组的一级菜单（不用二级菜单，也不做「一家一页加标签页」） |
| 卡头归属 | 卡头是各服务商专有的，放在各自「选卡规则」页的顶部，用同一个组件 |
| 卡头来源 | 服务商实现「能读取卡头」这个可选能力时自动同步；没实现时，后台手动增删改 |
| 3DS、Apple Pay | 手动标注，三种状态：未标注、支持、不支持；**只用于显示，不参与选卡** |
| MarsWallex 选卡规则 | 把卡池页里现有的选卡配置原样搬出来单独成页，**选卡行为不变** |
| 页面地址 | 现有 `/ops/*` 地址全部不变；只新增 `/ops/marswallex-selection` |

MarsWallex API 核对结果（`docs/marswallex-openapi-en.json`）：

- 卡产品接口（`GET /v1/card/product`）只返回产品 ID、产品名、卡组织、国家、模式、币种和手续费扣除方式。
- 没有卡头字段，卡头只出现在产品名里，例如「美卡-泛场景-512998-充值卡」。
- 没有 3DS、Apple Pay 属性，也没有选卡优先级、分流、条件筛选之类的接口。

所以选卡全部在本系统里完成，卡头从产品名里识别，3DS 和 Apple Pay 只能手动标注。

## 3. 菜单

| 分组 | 菜单项 | 地址 | 说明 |
|---|---|---|---|
| （无） | 总览 | `/ops` | |
| 卡密与付款 | CDK 卡密 | `/ops/cdkeys` | 「代理换码核对」标签页改名「代理换码」，在顶部放代理换码设置（从 ZovoCard 接入配置页搬来） |
| | 自动付款 | `/ops/marswallex-pay` | |
| | 卡服务商 | `/ops/providers` | 每家服务商的卡片上加「选卡规则」入口 |
| ZovoCard | 接入配置 | `/ops/integration` | 去掉代理换码设置 |
| | 选卡规则 | `/ops/card-selection` | 顶部加卡头列表 |
| | 兑换订单 | `/ops/orders` | 原「CDK 兑换订单」 |
| | Webhook | `/ops/webhooks` | |
| MarsWallex | 接入配置 | `/ops/marswallex` | |
| | 钱包 | `/ops/marswallex-wallet` | |
| | 卡池 | `/ops/cardpool` | 「策略与健康度」改名「开卡与补卡策略」，选卡配置移走 |
| | 选卡规则 | `/ops/marswallex-selection` | 新页面 |
| 系统 | 外观与品牌、Telegram 告警、审计日志、版本更新 | 不变 | |

- 菜单仍以路由 meta 为唯一来源。服务商分组里的菜单项用短名（如「选卡规则」）。路由 meta 新增可选的 `fullTitle`（如「ZovoCard 选卡规则」），浏览器标签页标题、手机上的面包屑（手机上不显示分组）用 `fullTitle`。
- 服务商页面内的页头标题统一带上服务商名：「ZovoCard 接入配置」「MarsWallex 卡池」等。

## 4. 卡头

### 4.1 数据

新表 `provider_card_heads`，按「服务商 + 产品 ID」存一条：

| 列 | 说明 |
|---|---|
| `provider`、`product_id` | 主键。产品 ID 是服务商自己的产品标识（MarsWallex 的 `product_id`，ZovoCard 的产品代码） |
| `source` | `api` 自动读取；`manual` 手动添加 |
| `name`、`card_heads`（JSON 数组）、`network`、`country`、`currency` | 产品信息。`api` 来源的由同步写入，`manual` 来源的由管理员填写 |
| `listed` | `api` 来源的卡头，最近一次同步时接口是否还返回它。`manual` 来源的恒为 1 |
| `three_ds`、`apple_pay` | `''` 未标注、`yes` 支持、`no` 不支持 |
| `note` | 备注，最多 200 字 |
| `synced_at`、`updated_at`、`updated_by` | 时间为 Unix 秒 |

同步状态（每家服务商最近一次同步的时间、错误信息）记在内存里，服务重启后清空，不建表。

### 4.2 自动读取

handler 层新增可选能力：

```go
// cardHeadSource 是可选能力：能从服务商读取卡头。没实现的服务商在后台手动维护卡头。
type cardHeadSource interface {
	CardHeads(ctx context.Context) ([]cardHeadInfo, error)
}
```

- **MarsWallex**：调用卡产品接口。卡头取产品名里第一段 6 到 9 位的连续数字，两侧必须不是数字；识别不出时卡头为空，界面显示产品名。例如：
  - 「美卡-泛场景-512998-充值卡」识别为 512998；
  - 「美卡投流AI-537872712-充值卡」识别为 537872712。
- **ZovoCard**：读本地产品缓存 `card_product_cache`（由 plansync 每 3 分钟从卡台同步），卡头用 `bin_heads`，为空时用 `bin`。

**同步规则**：

- 接口返回的产品：插入或更新名称、卡头、卡组织、国家、币种，`listed=1`。**不改** `three_ds`、`apple_pay`、`note`。原来是手动添加的同 ID 记录，改为 `source=api`，标注保留。
- 这家服务商 `source=api` 的记录里，接口没再返回的：`listed=0`，不删除。
  - 例外：ZovoCard 下架的卡段直接删除。卡台下架的卡段 BIN 只能由卡台管理员看到，本站不保留（沿用 2026-10-04 提交 f69bec6 的决定，与产品缓存 `card_product_cache` 的规则一致）。这类服务商在适配器上实现可选能力 `PruneMissingCardHeads`。
- 接口返回的产品 ID 不合法（为空或不符合下面的校验）的条目跳过；跳过后一条不剩时按读取出错处理。
- 读取出错时，表里什么都不改，记下错误，在页面上显示。

**同步时机**：

- 服务启动 1 分钟后同步一次，之后每 30 分钟同步一次；
- 页面上的「立即同步」按钮。

### 4.3 后台接口

`:name` 必须是已注册的服务商，否则返回 404。所有修改都记审计（不含任何密钥）。

| 接口 | 说明 |
|---|---|
| `GET /api/v1/admin/providers/:name/card-heads` | 返回 `{provider, auto, last_sync_at, last_sync_error, items}` |
| `POST /api/v1/admin/providers/:name/card-heads/sync` | 立即同步。不能自动读取的服务商返回 400 |
| `PUT /api/v1/admin/providers/:name/card-heads/:product_id` | 修改标注（`three_ds`、`apple_pay`、`note`）；`manual` 来源的还能改产品信息。`api` 来源的只校验标注，产品信息来自上游，不受下面的长度上限约束 |
| `POST /api/v1/admin/providers/:name/card-heads` | 手动添加。能自动读取的服务商返回 400 |
| `DELETE /api/v1/admin/providers/:name/card-heads/:product_id` | 只能删 `manual` 来源的 |

**校验**：

- `three_ds`、`apple_pay` 只能是 `''`、`yes`、`no`。
- `note` 最多 200 字，`name` 最多 100 字。
- `product_id` 为 1 到 64 位字母、数字或 `_-.`。
- 每个卡头为 4 到 12 位数字，最多 10 个。
- `network`、`country`、`currency` 各最多 16 字。

### 4.4 界面

共用组件 `CardHeadsPanel`（参数：服务商名），放在各家「选卡规则」页顶部：

- 表格列：卡头、名称、卡组织、国家、币种、3DS、Apple Pay、备注、来源。接口已不再返回的，标「接口已不再返回」。
- 能自动读取的服务商：显示最近同步时间、错误信息和「立即同步」按钮。
- 不能自动读取的服务商：显示「添加卡头」按钮，每行可以编辑、删除。
- 每行可以编辑标注。
- 加载完后把列表交给父页面，父页面在选产品的下拉框里显示「卡头 · 3DS 支持」这类说明。

## 5. MarsWallex 选卡规则页

`/ops/marswallex-selection`，从上到下：

1. **卡头列表**（`CardHeadsPanel`）。
2. **说明**：只选美元卡（注资按美元计）；支持 3DS 的卡要 MarsWallex Webhook 配通，系统才能自动填验证码。
3. **各套餐的卡产品优先级**：从卡池页原样搬来。下拉框和列表里显示卡头与标注。
4. **停用规则**：单卡最大使用次数、健康度统计窗口、产品失败阈值、单卡失败阈值、自动暂停。
5. **健康度表和「恢复」按钮**：从卡池页搬来，接口不变（`/cardpool/health`、`/cardpool/health/resume`）。

卡池页「开卡与补卡策略」保留：开卡金额、有效期、卡名前缀、租期、注销等待、开卡持卡人、各产品目标数、资金上限、未选用时仍补卡。

### 5.1 策略分开保存

卡池策略现在是整份保存的。拆成两个页面后，任何一页保存时都可能用旧值覆盖另一页刚改的内容。处理方式：

- 新增 `GET` / `PUT /api/v1/admin/cardpool/selection`：
  - 只读写 6 个选卡字段：`plan_products`、`max_uses_per_card`、`health_window_hours`、`product_fail_threshold`、`card_fail_threshold`、`auto_suspend`；
  - 其他字段取当前值；
  - 校验沿用 `SavePolicy`，审计记 `selection`。
- `PUT /api/v1/admin/cardpool/policy`：请求体里没出现的选卡字段，取当前值；出现了就照常使用。
  - 判断依据是「键有没有出现」，不是「值是不是零」。
  - 这样升级前还开着的旧卡池页整份提交时，行为和以前一样。
- 卡池页提交时不再带这 6 个字段。

## 6. 不改的

- 选卡、补卡、付款、发码、兑换的逻辑。
- 卡池策略的存储格式，以及现有数据表的结构（只新增 `provider_card_heads` 表）。
- 所有现有接口的请求和返回格式。`PUT /cardpool/policy` 只是多了「没带的选卡字段保持当前值」。
- 所有现有页面地址，以及后端、前端里指向它们的链接。

## 7. 测试

**后端**

- 卡头识别：四个真实产品名、没有数字、数字太短或太长、多段数字取第一段。
- 同步：
  - 新增、更新时保留标注；
  - 消失的标为 `listed=0`；
  - 手动记录被接口返回后转成 `api`；
  - 读取出错时什么都不改。
- 接口：
  - 未知服务商返回 404；
  - 能自动读取的服务商不能手动添加，不能自动读取的不能同步；
  - 只能删除手动添加的；
  - 参数校验、审计。
  - 测试里用一个只实现手动维护的假服务商。
- 卡池策略：
  - `PUT /cardpool/selection` 只改 6 个字段；
  - `PUT /cardpool/policy` 没带选卡字段时保持当前值，带了照常生效；
  - 两个接口交替保存时互不覆盖。

**前端**

- 菜单分组和顺序、`fullTitle`。
- 卡头标签文字：三种状态、卡头为空时显示产品名。
- 卡池页提交的内容里不再有选卡字段。

**浏览器**

电脑和手机两种布局下，逐页看一遍菜单、两家的选卡规则页、卡池页和 CDK 卡密的代理换码标签页。

## 8. 交付

分两个 PR，各自跑 CI：

1. **卡头**：数据表、可选能力、两家的读取实现、后台接口、`CardHeadsPanel`，并接入现有的 ZovoCard 选卡规则页。
2. **菜单与 MarsWallex 选卡规则**：
   - 菜单分组、`fullTitle`、页头标题；
   - 代理换码设置搬到 CDK 卡密页；
   - MarsWallex 选卡规则页，以及卡池策略的分开保存；
   - 卡服务商页加入口；
   - CHANGELOG。
