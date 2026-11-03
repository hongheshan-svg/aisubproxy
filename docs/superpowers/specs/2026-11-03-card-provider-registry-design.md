# 卡服务商注册表：服务商可扩展、按产品配置

日期：2026-11-03　状态：已定稿，实施中

## 1. 背景与目标

ZovoCard 和 MarsWallex 都是卡台服务商，以后还会接入更多家。现在的实现只做到一半：后台能选默认发码服务商，码上也记录了是哪家发的，但「只有两家」写死在后端约 35 处、前端约 20 处。

目标：
- 接入一家新服务商，只需要新增它自己的适配实现并注册，不再修改分流、发码、兑换、后台列表这些公共代码。
- 后台**按产品线**配置用哪家（GPT、X、Grok、Claude 各选一家），没有单独配置的产品线使用全局默认。

用户已定的两条：
- 配置粒度是按产品配置，全局默认作为兜底。
- 服务商分两类，两类都要留好接口：
  - **上游发码型**（像 ZovoCard）：对方发码，并由对方完成开通。本站只转发兑换请求。
  - **供卡型**（像 MarsWallex）：本站自己发码；兑换时用这家的虚拟卡，由本站浏览器完成付款。

## 2. 资金约束（全部保持，不得放宽）

- 一张码只属于一家服务商，终身不变。改默认值或改产品配置，只影响之后新发的码。兑换失败时绝不自动换另一家重试。
- 码或单据上的服务商未注册、认不出时，一律拒绝或转人工，绝不退回到别家。
- 供卡型服务商的码（命中它的码前缀）绝不发往任何上游，也不写入明文。
- 供卡型发码硬门槛按服务商分别计算：该服务商至少有一笔 `dry_run = 0`、`state = succeeded`、`resolved_by = ''` 的单，才能发它的码。
- 跨服务商互斥：同一个 ChatGPT 账号同一时刻只能有一家服务商在处理。
- 以下规则全部不变：
  - 熔断（继续全局生效，任何一家的连续失败都会暂停派发）
  - 重兑最多 3 次
  - 各项资金上限
  - 资金单绝不自动重发
  - 绝不点第二次支付
  - dry_run 取值语义（0 = 真实付款，1 = 试运行，2 = 试运行到填表）

## 3. 后端设计

### 3.1 注册表（`internal/provider`）

- `Kind`：`upstream`（上游发码）、`card_supply`（供卡·本站付款）。
- `Provider` 接口，所有服务商都要实现：
  - `Name() Name`
  - `DisplayName() string`
  - `Kind() Kind`
  - `Products() []string`
  - `Supports(product, plan string) bool`
  - `PlanNote(plan string) string`
- 供卡型还要实现：
  - `CodePrefix() string`：MarsWallex 为 `MW-`
  - `TokenPrefix() string`：MarsWallex 为 `mwt_`
- 上游型还要实现：
  - `ClaimsCode(code string) bool`
  - `ClaimsToken(token string) bool`
  - `Fallback() bool`：只有 ZovoCard 为 true
- 注册时校验，违反就让启动直接 panic（这是编程错误）：
  - 名称为小写 `[a-z0-9_]+`，不能重名。
  - 供卡型的码前缀和令牌前缀都不能为空，彼此不能重复，也不能互为前缀。
  - Fallback 最多一家。
- `List()` 按注册顺序返回，这也是界面上的展示顺序。
- 内置注册两家：ZovoCard 和 MarsWallex。
- **能力取自实现**：MarsWallex 的 `Supports` 直接取自付款引擎实际实现的流程。现在是 `payengine.PlanKeys` 五档新开通。子项目 C（升级、Codex 点数）上线后自动扩展，不另外维护清单。

### 3.2 码与令牌归属

- `ForCode(code)` 的判定顺序：
  1. 码前缀命中某个供卡型服务商，就归它（候选）。
  2. 否则交给第一个 `ClaimsCode` 为 true 的非兜底上游。
  3. 再否则交给兜底上游（ZovoCard，保持现有行为）。
  4. 都没有就返回「无法识别的卡密」。
- `ForToken(token)` 同理，按令牌判定。
- 供卡型的结论只是候选，必须由数据库确认：查到的 `local_cdks` 行的 `provider` 必须等于候选服务商。对不上或查不到，都按「码不存在」处理，**绝不转发给上游**。

### 3.3 配置

- 设置项 `cdk_default_provider`：全局默认（沿用）。
- 设置项 `cdk_provider_by_product`：JSON 对象，`{产品: 服务商名}`。值为空或缺失，表示跟随全局默认。
- `ResolveIssue(product, plan)` 先取该产品的配置，没有就取全局默认。
  - 结果服务商不支持这个档位时，返回 `ErrUnsupported`，错误文案为：「<显示名> 不支持 <产品> <档位>，请在发码时手动选择服务商，或到「卡服务商」页调整」。**后端绝不悄悄换成别家。**
  - 读库出错时返回错误，不能当成 ZovoCard 继续发码。
- 库里存的服务商名已经没有注册时：全局默认按兜底上游处理，产品配置按「跟随全局默认」处理。两种情况都在接口的 `warnings` 里说明。
- 保存时校验：
  - 产品必须是已知产品。
  - 服务商必须已注册，并且 `Products()` 包含该产品。
  - 保存后写审计。
- `Selected(ctx, name)`：满足下面任一条就为真，自动补卡用它判断要不要暂停：
  - 全局默认是它；
  - 任一产品配置是它；
  - 它还有没结清的本地码。
- 原来的 `MarsWallexSelected` 改为绑定到 MarsWallex 的 `Selected`。

### 3.4 发码（`POST /api/v1/admin/cdks/issue`，路由不变）

- 请求没带 `provider` 时，用 `ResolveIssue(product, plan)` 决定服务商。
- 显式带了 `provider` 时，它必须已注册并支持这个档位，否则返回 400，不会换成别家。
- 按服务商类型分派：
  - **供卡型**：走通用的本地发码。用该服务商的前缀生成码，写入 `local_cdks.provider = name`；发码硬门槛和就绪检查都按该服务商计算。
  - **上游型**：交给该服务商的发码适配器（ZovoCard 就是现有逻辑）。
- 响应带 `provider`、`provider_display_name`。

### 3.5 公开兑换与其他按码分流的入口

涉及这些入口：
- 预览、预检、兑换、按令牌查结果、按码查结果
- 宽限期恢复
- 代理换码
- 账单查询（lookup）

规则：
- 一律通过 `ForCode` / `ForToken` 分派。供卡型走本地流程，服务商以数据库行为准；上游型交给该服务商的适配器。
- 上游适配器放在 handler 包，接口方法是 gin 处理函数：`Preview`、`Preflight`、`Redeem`、`Result`、`ResultByCode`、`Issue`。代理换码、宽限期恢复是可选能力，用单独的接口表示，没实现就返回「该服务商不支持」。
- ZovoCard 的现有代码原样搬进适配器，**行为不变**。
- 本地码的公开响应在现有 `provider` 之外，新增 `provider_kind` 和 `provider_display_name`。上游转发的原始 JSON 保持原样不动；前端看到没有 `provider_kind` 时，按上游处理。

### 3.6 付款引擎

- `pay_orders` 新增 `provider` 列，`TEXT NOT NULL DEFAULT ''`。
  - 迁移时把旧行补成 `marswallex`（之前只有这一家供卡）。
  - 新单必须写入：码兑换取码行的 `provider`；后台测试兑换取页面参数，默认 MarsWallex。
- 引擎按单据的 `provider` 取对应的 `Pool`。取不到时：
  - 还没取卡、没注资：按 `failed / not_submitted` 结束，错误码 `provider_unavailable`，这时没动钱。
  - 已经取卡或注资：转 `needs_review`。
  - **绝不使用别家的卡池。**
- `Pool` 接口里的 `*marswallex.CardSensitive` 换成卡池包里的中性类型，其余数据结构沿用 cardpool 包的类型。
- 熔断保持全局。发码硬门槛按 `provider` 过滤。
- `cdk_session_bindings` 新增 `provider` 列，旧行补成 `zovocard`。
- 跨服务商互斥的通用规则：该账号在时间窗内，有别家的绑定记录，或有别家的在途、待人工单，就拒绝。

## 4. 接口约定

### `GET /api/v1/admin/providers`

```json
{
  "providers": [
    {"name": "zovocard", "display_name": "ZovoCard", "kind": "upstream", "kind_text": "上游发码",
     "products": ["gpt", "x", "grok", "claude"], "code_list": "cardplatform"},
    {"name": "marswallex", "display_name": "MarsWallex", "kind": "card_supply", "kind_text": "供卡·本站付款",
     "products": ["gpt"], "code_prefix": "MW-", "code_list": "local"}
  ],
  "products": [{"key": "gpt", "label": "ChatGPT"}, {"key": "x", "label": "X Premium"},
               {"key": "grok", "label": "Grok"}, {"key": "claude", "label": "Claude"}],
  "default": "zovocard",
  "by_product": {"gpt": "marswallex"},
  "effective": {"gpt": "marswallex", "x": "zovocard", "grok": "zovocard", "claude": "zovocard"},
  "warnings": [],
  "capabilities": [
    {"product": "gpt", "plan": "pro_20x", "comment": "", "support": {"zovocard": true, "marswallex": true},
     "notes": {"marswallex": "仅限无订阅账号"}}
  ]
}
```

字段说明：
- `code_list` 表示后台用哪种码列表：`cardplatform` 是卡台码列表，`local` 是本地码列表，空字符串表示没有列表。新的上游型服务商默认为空字符串。

### `PUT /api/v1/admin/providers/config`

请求体：`{"default": "zovocard", "by_product": {"gpt": "marswallex", "x": ""}}`。产品的值为空字符串，表示跟随全局默认。

返回：
- 成功：200，内容与 GET 相同，另加 `not_ready: [{"provider": "marswallex", "problems": ["..."]}]`，列出这次被选用但还没就绪的服务商。配置照常保存，只做提示。
- 校验失败：400，带 `error`。

保留的旧接口：`GET/PUT /api/v1/admin/providers/default` 继续可用。

### `GET /api/v1/admin/providers/readiness?plan=&region=`

```json
{
  "default": "zovocard",
  "by_product": {},
  "providers": {
    "zovocard": {"provider": "zovocard", "ok": true, "problems": [], "checks": [],
                 "extras": [{"label": "卡台余额", "value": "123.45 USD"}], "warnings": []},
    "marswallex": {"provider": "marswallex", "ok": false, "problems": ["..."], "checks": [],
                   "extras": [], "warnings": ["..."], "combos": []}
  },
  "capabilities": []
}
```

字段说明：
- `extras`、`warnings`、`combos` 都按服务商分别给出。原来顶层的 `combos` 和 `zovocard` 字段去掉。
- 某家的检查本身出错时，只把这家标为未就绪，不影响其他服务商。
- 前端按这些数据字段渲染，**不按服务商名写死**。

## 5. 前端设计

- `lib/provider.ts`：
  - `ProviderId` 改为 `string`。
  - 名称、类型、码列表方式都取自 `GET /admin/providers`，用一个小的共享状态缓存。
  - 显示名取不到时显示 name 本身。
- 「卡服务商」页：
  - 卡片按接口返回的列表渲染，数量不限。每张卡片显示：类型标签、就绪检查、`extras`、`warnings`、`combos`。
  - 新增「按产品选择服务商」表格：
    - 每行一个产品。
    - 下拉选项是「跟随全局默认」，加上 `products` 包含这个产品的服务商。
    - 选中的服务商在这个产品下有不支持的档位时，用一行小字列出（数据来自 `capabilities`）。
  - 全局默认也在这里设置。点保存后，`not_ready` 用警告展示。
- 卡密管理发码弹窗：
  - 服务商选项来自注册表。
  - 选好产品和档位后，预选 `by_product[产品]`，没有就用 `default`。
  - 预选那家不支持该档位时，自动改选第一家支持的，并显示提示：「GPT 默认用 MarsWallex，它暂不支持 Codex 点数，已改用 ZovoCard」。
  - 请求里总是显式带上 `provider`。
- 码列表按 `code_list` 选组件：`cardplatform` 用现有卡台列表，`local` 用 `LocalCdkPanel`（`provider` 改为属性传入），空字符串显示「这家服务商的码请在它自己的后台管理」。
- 用户兑换页：
  - 「通道标签」显示 `provider_display_name`。
  - 只在 `provider_kind === 'card_supply'` 时启用本站付款相关界面（仅限无订阅账号提示、失败重兑、转人工）。
- 每家服务商自己的对接页保持不变：MarsWallex 对接、钱包、卡池、持卡人、兑换页，以及卡台的环境徽章。

## 6. 这次不做

- 上游码的通用后台管理（列表、同步、禁用）：仍然只有卡台有。
- 卡池改为支持多个发卡方：新的供卡型服务商自带自己的 `Pool` 实现。
- 按服务商分别熔断。
- 真实接入第三家服务商。
- 子项目 C（已有订阅升级、Codex 点数、20x 续费绑卡、宽限期原订阅处理）。这是付款引擎的功能，排在第一笔真实付款验证 A 期之后。做完后，MarsWallex 的能力会按 §3.1 自动扩展。

## 7. 测试要求

后端测试里注册两家假服务商，用来证明可扩展：一家供卡型 `testcard`，前缀 `TC-` 和 `tct_`；一家上游型 `testup`，认领 `TU-` 开头的码。需要覆盖：
- 注册校验：重名、前缀重叠、两家兜底，都应拒绝。
- 配置解析：
  - 产品配置优先；
  - 没配置时用全局默认；
  - 不支持的档位返回 `ErrUnsupported`；
  - 库里存的服务商名已经没有注册时，退回并给出 warning；
  - 读库出错时报错。
- 发码分派：
  - `testcard` 的码前缀是 `TC-`，并写入 `provider = testcard`；
  - 发码硬门槛按服务商分别计算。
- 公开分派：
  - `TC-` 码查不到时，返回「码不存在」，并且没有发出任何上游请求；
  - `TU-` 码只交给 `testup`，不交给 ZovoCard；
  - 其余码交给 ZovoCard；
  - `MW-` 码的行，如果库里 `provider` 是 `testcard`，按「码不存在」处理。
- 付款引擎：
  - 按单据的 `provider` 使用对应的假卡池；
  - 服务商未知时，取卡前按 `provider_unavailable` 结束，注资后转 `needs_review`，并且始终没有调用别家卡池。
- 迁移：
  - 旧 `pay_orders` 和旧 `cdk_session_bindings` 的服务商列被正确补齐；
  - 只有旧设置项的库能正常工作。
- 跨服务商互斥：三家之间两两互斥。
- 前端 vitest：
  - 模拟三家服务商时，卡服务商页和发码弹窗都正确渲染；
  - 预选的服务商不支持档位时，自动改选并显示提示。
