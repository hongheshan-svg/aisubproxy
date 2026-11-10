# ZovoCard 整删：设计规格

- 日期：2026-11-10
- 分支 / 工作区：`feat/remove-zovocard`，`.claude/worktrees/remove-zovocard`（从 master c091c05 拉出）
- 状态：设计已逐段批准，待用户审阅本文件

## 1. 背景与目标

本站已经没有 ZovoCard 卡服务（只用 MarsWallex），但代码里还留着整套 ZovoCard 接入：服务商注册、卡台客户端、兑换转发、代理换码、售后查询、套餐同步、坏卡拉黑、后台 4 个页面、Webhook 接收等。用户端改版 A 已经去掉了用户端的 ZovoCard 分支，这次把剩下的全部删掉。

目标：
- 代码里不再有 ZovoCard 的功能和配置入口；
- MarsWallex 的兑换、自动付款、代理换码、代理商下单、售后查询照常工作，行为不变；
- 已部署的库升级后能正常启动，旧数据原样保留；
- X / Grok / Claude 三类码的界面和数据结构保留，以后接别的服务商时用。

## 2. 已定的决定（用户 2026-11-10）

| 编号 | 决定 |
|---|---|
| D1 | 线上库里 ZovoCard 留下的数据（Webhook 事件、它发过的卡密记录、卡台接入设置、坏卡记录、旧绑定上的 `zovocard` 服务商等）**留着不动**：不删表、不删数据，新代码不再读写。以后要清理另做。 |
| D2 | 后台「卡密与付款」里只为 ZovoCard 服务的 4 页（ZovoCard 接入配置、ZovoCard 选卡规则、ZovoCard 兑换订单、ZovoCard Webhook）**整页删除**；收藏的旧地址自动跳到「卡服务商」页。 |
| D3 | **一个分支、一个 PR**，按层分任务删，每步做完都能编译、测试全过。 |
| D4 | 默认界面皮肤「卡台新版 / ZovoCard」**只改显示名**为「曜夜雾金 / Night Gold」，配色和内部编号 `zovo` 不动。 |
| D5 | 后台「总览」里来自卡台的 3 格数字（CDK 总数、可用 CDK、兑换订单）**这次先去掉**，总览放什么数字留给后台改版 B 设计。这次只删不加。 |
| D6 | 第一段（范围与行为变化）、第二段（任务顺序、测试、更新说明）已批准，内容见第 3–8 节。 |

## 3. 范围

### 3.1 删除

按功能列（每项都是 ZovoCard 专用；实现时按第 5 节逐项核对）：

1. **服务商注册**：`provider` 包里的 `zovoCard`、`ZovoCard` 名称常量的业务用法、「兜底服务商」（`Fallback`）机制；出厂配置里给 X / Grok / Claude 配的兜底。
2. **上游适配**：`handler/provider_builtin.go` 的 `zovoCardAdapter` 及其预览、核对、兑换、查结果转发；`upstreamAdapters` 里的 ZovoCard 一项。
3. **卡台客户端包** `backend/internal/cardplatform` 整包（共用小件先挪走，见 5.1）。
4. **代理换码里的 ZovoCard 流程**：`zovoCardExchange`、在卡台查单 / 发码 / 作废旧码、`upstreamCDKRegion`、`upstreamCDKPlan`、`agentSwapIssuePrefs` 等只为它服务的函数；后台「结果不明」定论里「确认已发新码就去卡台作废旧码」一步；`ops_maintenance` 里每 10 分钟重试作废卡台旧码的任务。
5. **售后查询（查询卡密）里的 ZovoCard 部分**：`zovoCardFacts`、`zovoMarkFailed` 及其对 `cd_keys`、`cardplatform_cdk_codes`、`recharge_tasks` 的读取；批量查结果里转发卡台的部分。
6. **宽限期恢复的 ZovoCard 转发**：`zovoCardGraceRecovery`。
7. **卡台卡密管理**：`handler/cardplatform_cdk.go`、`handler/cardplatform.go`（连通测试），后台「CDK 卡密」页里「卡台」那一类码的列表、同步、启用 / 禁用、备注。
8. **选卡规则与本站兑换策略**：`card_pref.go`、`card_selection.go`、`redeem_policy.go` 里推给卡台的选卡优先级与兑换策略。
9. **坏卡拉黑（card health）**：只由 ZovoCard 的 Webhook 和查结果喂数据、界面只在 ZovoCard 选卡规则页，整套删（`card_health.go` 的处理函数与路由）。MarsWallex 自己的卡健康（`mw_product_health`、`mw_card_outcomes`）不受影响。
10. **定时套餐同步** `backend/internal/plansync`（只从卡台拉数据）。
11. **Webhook 接收** `POST /api/v1/webhooks/cardplatform` 和 Webhook 事件后台接口。
12. **卡台接入设置的读写接口**：`settings.go` 里卡台地址、密钥的读取、校验和保存；`network.go` 里的卡台环境（zovocard.com 生产 / 沙盒）。
13. **就绪检查** `checkZovoCard`，以及总览统计接口 `GET /api/v1/stats/system`（数据全部来自卡台，见 D5）。
14. **后台页面**：`CardIntegration.vue`、`CardSelectionConfig.vue`、`OrderReconcile.vue`、`WebhookEvents.vue` 及其路由、菜单项、测试；`AdminDashboard.vue` 里来自卡台的 3 格；`frontend/src/lib/errors.ts` 里「去配置卡台」类的报错映射。
15. **只为 ZovoCard 存在的数据库读写函数**（例如 `CardplatformCDKCodeByUpstreamID`、`GetCardplatformCDKStatus`、`UpdateCardplatformCDKStatus`、`LookupStoredCDKByCode` 里读卡台表的部分），以及它们的测试。

### 3.2 保留

- **所有数据表和数据**（D1）。建表语句也保留，老库和新装的库结构一致，不会因为缺表出错；只给 ZovoCard 用的表在建表处加一行注释「旧 ZovoCard 数据，代码已不读写，留表以兼容旧库」。已有的数据库迁移（如 `cdk_session_bindings` 给旧绑定补 `zovocard` 的 `addProviderColumn`）原样保留，迁移不改写历史。
- **卡台密钥的加密迁移**（`MigrateCardSecrets` 和启动时的检查）：库里存着的旧密钥要继续保持加密，这是敏感数据保护，不是 ZovoCard 功能。读取密钥给业务用的函数删掉。
- **X / Grok / Claude**：兑换界面、前端 `lib/claude.ts`、`lib/grok.ts`、数据结构、产品线定义、「按产品线选服务商」框架、上游这一类服务商的接口定义，全部保留。
- **代理换码记录面板**：后台的换码记录列表保留（本站码的换码记录也在这里看）。
- **审计日志里旧动作的显示名**（`errors.ts`、`AuditLogs.vue` 里 `cardplatform_*` 动作的中文名和颜色）：旧审计记录还要能看懂，保留。
- **MarsWallex 的一切**：兑换、付款引擎、卡池、钱包、选卡规则、代理商下单、本站卡密。

### 3.3 名字带 zovocard 但不属于卡服务的，不动

| 位置 | 说明 |
|---|---|
| `scripts/push.sh` | 校验 git 提交身份（提交者名为 zovocard），和卡服务无关 |
| `backend/internal/handler/version.go` 注释 | 「不要把更新仓库指回上游 zovocard」的提醒 |
| `deploy/app.env.example` 注释 | 同上 |
| `docs/推送教本.md` | 讲 `push.sh` 和提交身份 |
| 皮肤内部编号 `zovo`（`theme.ts`、`style.css`、`skins.css`、`settings.go` 的 `defaultSiteSkin`、允许的皮肤列表） | D4：只改显示名 |
| CHANGELOG、`docs/superpowers/` 下的历史规格和计划、`docs/research/` | 历史记录，保留原样 |

## 4. 行为变化

1. **谁都认不出的码**：以前交给 ZovoCard 兜底，以后直接提示「认不出这张码」（用现有的认不出提示，不新写文案）。
2. **X / Grok / Claude**：「卡服务商」页上这三条产品线没有服务商可选（出厂配置原来是 ZovoCard），这三类码拒绝发码。
3. **保存过「某产品线选 ZovoCard」或全局默认是 ZovoCard 的**：页面显示现有的「这个服务商已不存在，请重新选择」提示，按全局默认处理；全局默认本身失效时按现有规则处理（不自动换家、不多花钱）。实现时核对 `LoadConfig` 对全局默认失效的处理，没有提示的补上提示。
4. **旧的 ZovoCard Webhook 地址**返回 404。
5. **后台少了 4 页**，旧地址跳到「卡服务商」页；「CDK 卡密」页没有「卡台」那一类码；「总览」少了 3 格卡台数字。
6. **代理换码记录面板**没有「结果不明」定论表单和「旧码待禁用」说明：这两种状态只由 ZovoCard 流程产生。实现时先确认本站码换码（`localCDKExchange`）不会产生这两种状态；如果会，保留定论表单（去掉卡台字样和作废卡台旧码一步）并在报告里说明。
7. **皮肤选择**里「卡台新版 / ZovoCard」改叫「曜夜雾金 / Night Gold」。

## 5. 各部分做法

### 5.1 先挪出共用小件（行为不变）

- `cardplatform.MaxIssueCount`（200，单次发码上限）：代理商下单、本站卡密后台在用，挪到 `provider` 包成为 `provider.MaxIssueCount`，数值不变。
- `cardplatform.IsXPremiumCredential`、`IsGrokCredential`：`provider_guard.go` 判断登录信息是不是 X / Grok 会话，挪到新包 `backend/internal/credential`（函数名不变），连同测试一起挪。
- 挪完后 `cardplatform` 包只剩 ZovoCard 专用代码。

### 5.2 摘掉服务商

- `provider/builtin.go` 去掉 `zovoCard`；`Fallback` 机制和 `fallback()` 去掉；`factoryConfig` 只配 MarsWallex，X / Grok / Claude 不配。
- 路由（`routeCode` 等）在没人认领时走现有的「认不出」分支。
- `ZovoCard` 名称常量：没有业务用法后删除；测试里需要一个「已不存在的服务商名」时直接写字符串。

### 5.3 后端接口

按第 3.1 节 2、4–13 项删除处理函数、路由和只服务它们的辅助函数。原则：
- 整个函数只为 ZovoCard 服务 → 删；
- 函数被共用、只有一个 ZovoCard 分支 → 删分支，其余逻辑一行不动；
- 代理换码动钱：只删 ZovoCard 流程，MarsWallex（本站码）流程的判断、顺序、锁、状态码不动，现有 MarsWallex 换码测试原样通过；
- 被删接口的路由在 `server/http.go` 里一起删；`provider_routes_test.go` 等路由测试相应更新；
- 代理换码记录列表接口从 `/api/v1/admin/cardplatform/agent-exchanges` 改到 `/api/v1/admin/agent-exchanges`，前端同步改。

### 5.4 卡台客户端包与数据库代码

- 删 `backend/internal/cardplatform` 整包、`backend/internal/plansync` 整包。
- 数据库层删只为 ZovoCard 存在的读写函数；建表语句保留（见 3.2）。
- 启动检查里只为卡台业务存在的检查删掉；卡台密钥加密迁移保留。

### 5.5 前端

- 删 4 个页面及其测试；`routes.ts` 加 4 条重定向：`/ops/integration`、`/ops/card-selection`、`/ops/orders`、`/ops/webhooks` → `/ops/providers`。
- 菜单（`adminNav.ts`、`AdminLayout.vue`）去掉这 4 项。
- `CDKeyManagement.vue` 去掉 `codeListKind === 'cardplatform'` 那一类及其请求；`lib/provider.ts` 去掉对应的类别。
- `CardProviders.vue` 去掉 ZovoCard 的说明文字。
- `AdminDashboard.vue` 去掉 3 格卡台数字和 `/api/v1/stats/system` 请求，其余部分不变。
- `AgentExchangePanel.vue` 按第 4 节第 6 条处理，去掉「卡台」字样。
- `errors.ts` 去掉「去配置卡台」类映射，保留审计动作显示名。
- `theme.ts` 改显示名（D4）。
- 其余提到「卡台」的前端注释（`plan.ts`、`claude.ts`、`grok.ts` 等）：改成中性说法或删掉注释，不改逻辑。

### 5.6 文档

- 只讲 ZovoCard 的文档整篇删除：`docs/zovocard-cdk-zh.md`、`docs/zovocard-openapi-zh.md`、`docs/卡台CDK接入准备-2026-07-28.md`、`docs/卡台CDK测补与兑换进度-2026-07-28.md`、`docs/卡台回调配合需求-给负责人-2026-07-28.md`。
- 删掉 ZovoCard 相关段落、其余保留：`README.md`（「卡台官网」一行）、`docs/UI-UPGRADE.md`、`docs/cdk-list-region.md`、`docs/x-premium-subscriptions.md`、`docs/开源自托管进度-2026-07-28.md`、`docs/marswallex-openapi-zh.md`（只动提到卡台的那两处说明，接口文档本身不动）。
- `docs/known-issues.md`：只针对 ZovoCard 的条目标为「已随 ZovoCard 删除」。
- `docs/推送教本.md` 讲的是 `push.sh` 和提交身份，归入第 3.3 节，不动。
- CHANGELOG「未发布」一节写升级注意（见第 8 节）。
- 历史规格、计划、调研不改。

## 6. 资金与敏感数据

- 不新增任何付款、发码、注资路径；只删。
- 代理换码、代理商下单、本站卡密发码的限额（含 200 上限、单日换码上限）数值不变。
- 卡台密钥在库里继续加密保存；删掉读取它的业务代码后，不新增任何把它读出、打印、返回的地方。
- 删掉的接口不留「兼容空壳」返回假数据：一律 404（路由不存在）。

## 7. 测试

- 只测 ZovoCard 的测试随代码删除；测试共用路径的用例改写后保留，改写时不放宽断言。
- 新增或改写：
  1. 认不出的码（以前会落到兜底）返回现有的「认不出」结果；
  2. X / Grok / Claude 没有服务商时拒绝发码；
  3. 出厂配置只有 MarsWallex，X / Grok / Claude 为空；
  4. 存着 `zovocard` 的产品线配置、全局默认在「卡服务商」接口里给出「已不存在」提示；
  5. `POST /api/v1/webhooks/cardplatform`、`/api/v1/admin/cardplatform/*`、`/api/v1/stats/system` 返回 404；
  6. 前端 4 个旧地址重定向到 `/ops/providers`；
  7. 菜单里没有这 4 项；总览没有那 3 格；皮肤显示名改了、编号没改；
  8. 老库升级：带着 ZovoCard 旧表和旧数据的库能正常启动（用现有的数据库测试工具建一个含旧表数据的库）。
- MarsWallex 代理换码、代理商下单、本站卡密、售后查询的现有测试原样通过。
- 最后全仓库搜 `zovo`、`cardplatform`、`卡台`（不区分大小写）：除第 3.2、3.3 节列出的位置外不再出现；结果写进报告。
- 门禁：gofmt、go vet、golangci-lint、`go test -race`（paybrowser 单独跑）、govulncheck、vitest、build、gitleaks。

## 8. 更新说明草稿（CHANGELOG「未发布」）

**升级注意**
- 升级前必须做：**还在用 ZovoCard 的部署不要升级**，这一版删除了 ZovoCard 的全部接入（仓库是公开的，可能有别的部署在用）。
- 升级后要确认：后台「卡服务商」页没有「这个服务商已不存在」的提示；有的话重新选择并保存一次。
- 行为变化：第 4 节 1–7 条，写成面向管理员的话。

**删除**
- ZovoCard 卡服务接入、后台 4 页、总览里的卡台数字、坏卡拉黑、定时套餐同步、ZovoCard Webhook 接收。数据库里的旧数据保留不动。

## 9. 执行

- 计划写成 6 个任务（顺序同第二段设计）：挪共用小件 → 摘服务商 → 后端接口 → 删卡台包与数据库代码 → 前端 → 文档与 CHANGELOG。
- 子代理执行：实现用 sonnet；第 3 个任务（含代理换码）实现用 opus、审查用 fable；终审用 fable。
- 每个任务结束：后端编译与测试全过、前端测试与构建全过。

## 10. 与小项目 ② 的先后

小项目 ②（账单页、代理换码页英文界面，分支 `feat/billing-agent-i18n`）也改 `agent_exchange.go`。② 先合并；本分支开工前 rebase 到最新 master，再开始第 1 个任务。

## 11. 清单核对后的修订（2026-11-10）

写计划前，在临时副本上把本规格真删了一遍（编译、lint、deadcode、测试），逐文件清单见 `docs/superpowers/plans/2026-11-10-remove-zovocard-inventory.md`。据此修订如下，以本节为准：

1. **共用件比 5.1 列的多**。还要挪：
   - `IsXPremiumPlan`、`IsGrokPlan`、`IsClaudePlan`（连同 `grokPlanRe`、`claudeBarePlans`）挪到 `provider` 包，保留的发码入口靠它们按档位推断产品线；
   - `issueProduct` 挪到 `handler/local_cdk_admin.go`；
   - `str`、`toString`、`deviceFrom` 挪出 `cardplatform_cdk.go`；
   - `maskEmailForAdmin`、`keepHead`、`adminName` 挪出 `card_health.go`。
2. **第 4 节第 6 条的条件成立**：本站码换码会产生「结果不明」（`unknown`）和「旧码待禁用」（`disabling`）。因此保留以下几样，只去掉卡台字样和「作废卡台旧码」一步：
   - 定论接口 `AdminResolveAgentExchange`；
   - 前端定论表单；
   - `retryAgentExchangeDisables` 里本站码那一支（`OldUpstreamID<=0`）；
   - `escalateAgentExchanges`。

   定论「已发新码」时，本站码要填的新码 ID 是 `local_cdks.id`。
3. **历史 ZovoCard 换码记录**（`old_upstream_id>0`，停在「结果不明」或「旧码待禁用」）：按 D1 不改数据、不自动收尾。
   - 「结果不明」的照常能人工定论；
   - 「旧码待禁用」的会一直停在列表里。
   - 更新说明的「升级前必须做」写明：先把 ZovoCard 码的这两类记录处理完。
4. **代理换码的资金测试**大多建在假卡台上。删之前先确认本站码路径有等价覆盖，没有的先补、看它通过，再删旧的。要覆盖：并发只发一张、失败释放可重试、结果不明保留占位、单日上限、两种定论。
5. **全局默认服务商失效**（存的是 zovocard 或为空）：
   - 「卡服务商」页的提示改成「全局默认服务商 "<名字>" 已不存在，请到「卡服务商」页重新选择」（原来会显示成「按  处理」）；
   - 默认为空、按产品线又有配置时，也给出「没有设置全局默认服务商，请到「卡服务商」页选择」；
   - 后台发码在这种情况下回 400「没有可用的默认服务商，请到「卡服务商」页重新选择」，不再回 500。

   不自动换家。
6. **D5 更正**：总览里来自卡台的是 4 格（CDK 总数、可用 CDK、已兑换、兑换订单），4 格都去掉。
7. **以下几处要读旧数据，作为 D1「不再读写」的例外保留**。它们都是敏感数据或安全保护：
   - `SweepCDKSessions` 借 `cardplatform_cdk_codes` 提前清理用户会话；
   - 启动时的 `MigrateWebhookPayloads`（清洗 Webhook 原文里的卡号、CVV）；
   - `upstreamKeysConfigured` 把 `card_api_key` 算作动钱密钥（有它就强制 JWT_SECRET）。

   启动检查没有可删的项，只把文案里的「卡台」改成「旧版」。
8. **规格没列、也要删的**：
   - 顶栏的「卡台·未配置」环境标记（`AdminLayout.vue` 与 `lib/adminui.ts` 的 `zovoEnv`）；
   - 公开接口 `GET /api/v1/public/cdk/plans`：数据全来自卡台，前端没用，写进更新说明。
9. **用户可见的行为变化补充**（写进更新说明）：
   - 已发出、没兑换的 ZovoCard 码从此不能兑换，也查不到（售后查询回「未找到该卡密记录」）；
   - 预检、兑换请求里既没码也没令牌时，回「无法识别的卡密」；
   - 代理换码记录接口改路径（`/api/v1/admin/agent-exchanges`），定论结果里不再有 `old_disabled`。
10. **文档**：
    - `docs/cdk-list-region.md`、`docs/x-premium-subscriptions.md` 整篇只讲被删功能，整篇删；
    - `docs/UI-UPGRADE.md` 讲的是另一个叫 cardplatform 的 UI 项目，属历史记录，不动；
    - `docs/marswallex-openapi-zh.md` 开头两处「卡台」指 MarsWallex 自己，改成「卡服务」。
11. **最终搜索的白名单**在 3.2、3.3 之外再加：
    - `.push.env.example`（提交身份）；
    - `automation/`（另一个叫 cardplatform 的取卡项目）；
    - `frontend/index.html` 的皮肤编号；
    - `.gitleaks.toml` 注释；
    - `db/cdk_session.go` 的迁移；
    - 前端测试里「卡台」作为界面禁用词的列表；
    - 审计动作 `cardplatform_*` 的显示名；
    - 第 7 条保留的几处。

    `deploy/docker-deploy.sh` 装完后的提示里「卡台 API Key」是给用户看的，要改。
12. **`CDKeyManagement.vue`** 去掉卡台那一类以后基本是重写。这页同时挂着「代理换码」标签，要有渲染测试，并在真实页面上点一遍两个标签。
