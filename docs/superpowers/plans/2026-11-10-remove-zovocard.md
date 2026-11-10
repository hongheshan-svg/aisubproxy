# ZovoCard 整删 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal：** 把 ZovoCard 卡服务接入从代码里整个删掉。MarsWallex 的兑换、付款、代理换码、代理商下单、本站卡密、售后查询照常工作，老库升级后照常启动。

**Architecture：** 一个分支分 7 步删。顺序是：先挪共用件；再处理动钱的代理换码；再摘服务商和兑换转发；再删后台接口；然后删卡台客户端包和数据库函数；最后改前端，写文档和更新说明。每步结束时，编译、lint、受影响包的测试都要全过。数据库表和数据一律不动。

**Tech Stack：** Go（gin、SQLite）、Vue 3 + TypeScript（vitest、vue-i18n）。

**Spec：** `docs/superpowers/specs/2026-11-10-remove-zovocard-design.md`。第 11 节「清单核对后的修订」优先于前文。
**逐文件清单（附件）：** `docs/superpowers/plans/2026-11-10-remove-zovocard-inventory.md`，下文写作「清单 §x.y」。行号以 master c091c05 为准，rebase 后以实际代码为准。

## Global Constraints

- 所有输出（提交信息、注释、报告）用简体中文。提交信息末尾加一行：`Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`。
- 提交只用明确路径：`F=(路径...)`、`git add "${F[@]}"`、`git commit -m "..." -- "${F[@]}"`。删文件用 `git rm <路径>`。不用 `git add -A`，不提交 `.claude/`、`.superpowers/`，不用裸 `git stash`。
- 不碰 18080 端口和仓库 `data/` 目录。不调真实业务接口，不做真实付款。不派子代理。
- **只删不加**：不新增付款、发码、注资路径。以下数值不变：`MaxIssueCount = 200`、单日换码上限、各限流额度。
- 数据库：建表语句和已有迁移不删。只给 ZovoCard 用的表，在建表处加一行注释：`// 旧 ZovoCard 数据，代码已不读写，留表以兼容旧库`。不写任何改动或删除旧数据的代码。
- 以下保留，不删（规格 §3.2、§11.7）：
  - 卡台密钥加密迁移 `MigrateCardSecrets` 和启动检查；
  - `MigrateWebhookPayloads`；
  - `SweepCDKSessions` 对 `cardplatform_cdk_codes` 的读取；
  - `upstreamKeysConfigured` 对 `card_api_key` 的判断；
  - 审计动作 `cardplatform_*` 的显示名；
  - 「按产品线选服务商」框架和上游适配器接口（`upstreamExchanger` 除外，它删）。
- 以下不动（规格 §3.3、§11.11）：`scripts/push.sh`、`.push.env.example`、`docs/推送教本.md`、`automation/`、皮肤编号 `zovo`（只改显示名）、历史规格、计划和调研文档、CHANGELOG 已发布的部分。
- 删掉的接口不留兼容空壳，路由不存在就返回 404。
- 测试：
  - 只测 ZovoCard 的测试随代码删除；
  - 测共用路径的测试改写后保留，改写时不放宽断言；
  - 新行为先写测试、看它失败，再改代码。
- 跑测试按包分开跑，不要一次跑 `./...`（清单风险 20）：handler 包单独跑；paybrowser 单独跑（`go test -race -timeout 30m ./internal/paybrowser/...`）。
- 每个任务的门禁（后端）：
  - `cd backend && gofmt -l . && go vet ./... && /opt/homebrew/bin/golangci-lint run ./...`，lint 结果须为 0 issues；
  - 再分别对本任务动过的每个包跑 `go test -race -count=1 <包>`。
- 前端门禁：`cd frontend && npx vitest run && npm run build`。

## Review Focus

1. **本站码代理换码在并发、重复提交、单日上限下只发一张码。** ZovoCard 的资金测试删掉后，这几条保证不能跟着丢。由任务 2 的测试钉住。
2. **全局默认服务商存的是 `zovocard` 的老库。** 「卡服务商」页要显示带名字的提示；后台发码回 400 并写清该做什么，不能回 500。由任务 3 的测试钉住。
3. **带着 ZovoCard 旧表、旧数据、旧设置的老库能升级启动。** 包括只读检查和迁移。由任务 5 的测试钉住。
4. **后台旧书签地址**（4 个被删页面，加上 `billing`、`reconcile`、`cards` 这几个旧别名）一次跳到 `/ops/providers`，不出现空白页或二次跳转。由任务 6 的测试钉住。
5. **「CDK 卡密」页去掉卡台那一类后**，「本站卡密」和「代理换码」两个标签照常渲染和操作。由任务 6 的渲染测试，加真实页面检查钉住。

---

## 开工前

- [ ] 小项目 ②（`feat/billing-agent-i18n`）合并进 master 之后：`git fetch origin && git rebase origin/master`。rebase 后若有冲突，停下来报告，不要自己解决。
- [ ] 跑一遍后端门禁和前端门禁，作为基线。基线不全绿就停下报告。

---

### Task 1：挪出共用件（行为不变）

**Files：**
- Create：`backend/internal/credential/credential.go`、`backend/internal/credential/credential_test.go`
- Create：`backend/internal/provider/plans.go`（`MaxIssueCount` 和三个 `Is*Plan`）、`backend/internal/provider/plans_test.go`
- Create：`backend/internal/handler/request_util.go`（`str`、`toString`、`deviceFrom`）、`backend/internal/handler/admin_util.go`（`maskEmailForAdmin`、`keepHead`、`adminName`）
- Modify：`handler/local_cdk_admin.go`（接收 `issueProduct`）、`handler/provider_guard.go`、`handler/agent_order.go`、`handler/cardplatform_cdk.go`、`handler/card_health.go`、`cardplatform/*.go`、`acgshared/acgshared_test.go`、`handler/provider_admin_test.go`
- 明细：清单 §1.1–§1.3

**Interfaces：**
- Produces：
  - `provider.MaxIssueCount`（untyped const，值 200）；
  - `provider.IsXPremiumPlan(plan string) bool`、`provider.IsGrokPlan(plan string) bool`、`provider.IsClaudePlan(plan string) bool`；
  - `credential.IsXPremiumCredential(s string) bool`、`credential.IsGrokCredential(s string) bool`；
  - handler 包内：`str`、`toString`、`deviceFrom`、`issueProduct`、`maskEmailForAdmin`、`keepHead`、`adminName`。函数名和签名都与现在相同，只换文件。

- [ ] **Step 1：挪测试**
  - 把 `cardplatform/client_issue_test.go` 的 `TestMaxIssueCountMatchesBatchLimit`，以及 `x_premium_test.go`、`grok_test.go`、`claude` 相关测试里断言 `Is*Plan` 的部分，挪到 `provider/plans_test.go`。改成调用 `provider.*`，断言原样不动。
  - 把断言 `IsXPremiumCredential` / `IsGrokCredential` 的部分挪到 `credential/credential_test.go`。
  - 运行 `go test ./internal/provider/ ./internal/credential/`。预期：编译失败（符号未定义）。
- [ ] **Step 2：挪生产代码**
  - 在新位置放函数，函数体原样搬过去（连同 `grokPlanRe`、`claudeBarePlans`）。
  - 旧位置删掉，所有调用方改成新路径。`cardplatform` 包内部仍要用的，改为调用 `provider.*` / `credential.*`。注意 `provider` 不能 import `cardplatform`。
  - `issueProduct` 挪进 `local_cdk_admin.go`；其余 handler 小工具挪进上面两个新文件。
- [ ] **Step 3：门禁**
  - 后端门禁。
  - `go test -race -count=1 ./internal/provider/... ./internal/credential/... ./internal/cardplatform/... ./internal/acgshared/...`，再单独跑 `./internal/handler/`。全过。
- [ ] **Step 4：提交** `refactor: 把 ZovoCard 包里的共用件挪到 provider、credential 和 handler 工具文件`

---

### Task 2：代理换码只留本站码路径（动钱）

**Files：**
- Modify：`handler/agent_exchange.go`、`handler/ops_maintenance.go`、`handler/provider_adapter.go`（删 `upstreamExchanger`）、`handler/provider_builtin.go`（删 `zovoCardAdapter.Exchange` 方法）、`server/http.go`（换码记录路由改路径）、`db/agent_exchange.go`（删 `TouchAgentCDKExchange`）
- Modify 测试：`handler/agent_exchange_money_test.go`、`agent_exchange_disable_test.go`、`agent_exchange_s3_test.go`、`agent_exchange_audit_o3_test.go`、`agent_exchange_region_test.go`（整删）、`provider_admin_test.go`
- 明细：清单 §3.2（agent_exchange.go、ops_maintenance.go 两行）、§3.3 a/b、§3.4「代理换码」

**Interfaces：**
- Consumes：Task 1 的 `adminName`。
- Produces：
  - 换码记录列表：`GET /api/v1/admin/agent-exchanges`（原 `/api/v1/admin/cardplatform/agent-exchanges`，旧路径删）；
  - 定论：`POST /api/v1/admin/agent-exchanges/:id/resolve`（原 `/api/v1/admin/cardplatform/agent-exchanges/:id/resolve`，旧路径删）。

  两个接口的请求体和返回字段，除了删掉 `old_cdk_id`、`old_disabled`，其余不变。任务 6 的前端按这两个路径改。

- [ ] **Step 1：先补本站码路径的资金测试**
  - 用 `MW-` 码走 `localCDKExchange`，在 `agent_exchange_money_test.go` 里新增下面 6 个测试。已有等价用例的（先查 `TestAgentExchangeLocalCDK`、`TestLocalExchangeRecordNotStuckInDisabling` 等）在报告里写明，跳过不重复写。
    1. `TestLocalAgentExchangeConcurrentIssuesOnce`：同一张码并发 20 次，只有 1 次 200，其余 409；`local_cdks` 里只多出 1 张新码。加一个文件库版本：`TestLocalAgentExchangeConcurrentOnRealFileDB`，用 `openFileDB`。
    2. `TestLocalAgentExchangeFailureReleasesAndRetries`：让 `ExchangeLocalCDK` 失败（不符合换码条件），记录被释放；修好条件后同一张码能再换一次。
    3. `TestLocalAgentExchangeUnknownKeepsReservation`：占位后不收尾（模拟进程退出），`EscalateStaleAgentExchanges` 把它转成 `unknown`；之后再换同一张码，回 409、`state=unknown`，不发新码。
    4. `TestLocalAgentExchangeDailyLimit`：单日上限设为 2，第 3 张回 429，`error_code=daily_limit`、`limit=2`。
    5. `TestAdminResolveLocalExchangeReleased`：`unknown` 记录定论 `released` 后，同一张码能重新换。
    6. `TestAdminResolveLocalExchangeIssued`：`unknown` 记录定论 `issued`（`new_cdk_id` 是 `local_cdks.id`），记录转 `disabling`；跑一轮 `retryAgentExchangeDisables` 后转 `done`；返回里没有 `old_disabled` 字段；没有发出任何上游请求。
  - 先在旧代码上跑，确认 1–5 全过（证明本站码路径本来就成立），6 因为还有 `old_disabled` 而失败。
- [ ] **Step 2：写路由测试并看它失败**
  - `TestAgentExchangeAdminRoutesMoved`：
    - 新路径未登录回 401，登录后 200；
    - 旧路径 `/api/v1/admin/cardplatform/agent-exchanges` 回 404。
  - 在 `server` 包里，照现有 `provider_routes_test.go` 的写法。
- [ ] **Step 3：删 ZovoCard 换码流程**
  - 删掉以下函数：`zovoCardExchange`、`issueDefinitelyFailed`、`finishDisableOldCode`、`reportDisableFailure`、`agentExchangeUnknown`（确认只有 ZovoCard 用）、`orderLooksUnpaid`、`agentSwapIssuePrefs`、`upstreamCDKRegion`、`upstreamCDKPlan`、`parseCDKOrderList`、`extractOrderMap`、`extractOrderEvents`，以及其余只为它们服务的函数（以 lint 的 unused 为准）。
  - 删掉 `PublicAgentCDKExchange` 里的上游分派分支（:345-354），没人认领的码由 `routeCode` 回 404。删掉 `upstreamExchanger` 接口和 `zovoCardAdapter.Exchange` 方法。
  - `AdminResolveAgentExchange`：
    - 删掉 `issued` 分支里「去卡台作废旧码」那一段和 `old_cdk_id` / `old_disabled` 字段；
    - 「请填写卡台查到的新码 ID」改成「请填写新码 ID（new_cdk_id）」；
    - 其余判断和状态码不动。
  - `retryAgentExchangeDisables`：只留 `OldUpstreamID<=0` 那一支，删 `cli` 和卡台分支（清单 §3.3 b）。
  - `escalateAgentExchanges`：告警文案里「请先到卡台核对是否已发码」改成「请到后台「代理换码记录」核对后定论」。
  - `db.TouchAgentCDKExchange` 没人用了，删掉。
  - ② 加给 ZovoCard 分支的 `error_code` 随分支一起删。本站码路径的 `error_code` 不动。
- [ ] **Step 4：删 ZovoCard 换码测试**
  - 删掉清单 §3.4 列的、建在假卡台 `exchangeUpstream` 上的用例和 helper。
  - `adminExchangeRouter`、`adminExchangeDo` 保留，路径改成新路由。
  - `TestAgentSwapDailyLimitSetting`、`TestOpsMaintenanceEscalatesStaleExchange`、`TestAgentSwapShortPasswordFlagAndWrongCount` 改用 `MW-` 码，保留。
- [ ] **Step 5：门禁**
  - 后端门禁；
  - 单独跑 `go test -race -count=1 ./internal/handler/`，再跑 `./internal/db/ ./internal/server/`；
  - Step 1、2 的测试全过；
  - `agent_exchange_lockout_test.go`、`ops_maintenance_test.go` 原样通过。
- [ ] **Step 6：提交** `refactor(agent-exchange): 代理换码只留本站码路径，定论不再去卡台作废旧码`

---

### Task 3：摘掉 ZovoCard 服务商和兑换转发

**Files：**
- Modify：`provider/builtin.go`、`provider/provider.go`、`provider/config.go`、`provider/providertest/providertest.go`、`handler/provider_adapter.go`、`handler/provider_builtin.go`、`handler/provider_readiness.go`、`handler/card_heads.go`、`handler/provider_config_admin.go`、`handler/local_cdk_admin.go`、`handler/grace_recovery.go`、`handler/lookup.go`、`handler/lookup_results.go`、`handler/aftersale.go`、`handler/cardplatform_cdk.go`（删转发部分：预览、预检、兑换、查结果、按码查结果）
- Modify 测试：清单 §2.5 全部；§3.4 中「售后/会话」「公开守卫」「本站码测试里的 zovo 桩」「public_email_mask」「lookup_results」几组；整删 `aftersale_zovo_test.go`、`preflight_binding_audit_ob_test.go` 里的卡台用例
- 明细：清单 §2.1–§2.5、§3.2（对应行）、§3.3 c

**Interfaces：**
- Consumes：Task 1 的 `provider.Is*Plan`。
- Produces：
  - `provider.LoadConfig()` 的警告文案（任务 6 的页面原样显示）：
    - 全局默认失效：`全局默认服务商 "<名字>" 已不存在，请到「卡服务商」页重新选择`；
    - 默认为空、按产品线有值：`没有设置全局默认服务商，请到「卡服务商」页选择`。
  - 后台发码遇到默认服务商不可用：HTTP 400，`{"error":"没有可用的默认服务商，请到「卡服务商」页重新选择"}`。

- [ ] **Step 1：写测试，看它失败**
  - `provider` 包：
    - `TestFactoryConfigOnlyMarsWallex`：出厂配置 `Default=marswallex`，`ByProduct` 为空；
    - `TestLoadConfigDefaultGone`：`cdk_default_provider="zovocard"` 时，警告等于上面第一句（含名字），`Default` 为空；
    - `TestLoadConfigDefaultEmptyWithByProduct`：默认为空、按产品线有值时，有上面第二句警告；
    - `TestForCodeUnclaimedIsUnrecognized`：`ZC-` 开头的码在 `ForCode` 返回 `ErrUnrecognized`。
  - `handler` 包：
    - `TestUnclaimedCodeIs404`：`ZC-` 码在预览、兑换、代理换码（口令正确时）都回 404，`error="无法识别的卡密"`，`error_code="code_unrecognized"`；
    - `TestIssueWithGoneDefaultIs400`：默认存 `zovocard` 时后台发码回 400，文案为上面那句；
    - `TestIssueXGrokClaudeRefused`：全局默认 marswallex 时，X / Grok / Claude 发码回 400（`UnsupportedError`），不发任何上游请求。由 `x_premium_test.go`、`grok_issue_test.go` 的卡台用例改写。
- [ ] **Step 2：摘服务商**
  - 按清单 §2.1 改 provider 包：删 `zovoCard`、`ZovoCard` 常量、`Fallback`、`fallback()`、`validate` 里兜底那段；`factoryConfig` 只配 MarsWallex；`providertest.Up` 删 `IsFallback`。
  - `LoadConfig` 换成上面两句警告文案。
  - `local_cdk_admin.go` 把 `ResolveIssue` 返回的 `ErrUnknown` 映射成 400 和上面那句文案。其余错误照旧回 500。
- [ ] **Step 3：删兑换转发**
  - 按清单 §2.2 改 handler：`upstreamAdapters` 置为空 map；删 `zovoCardAdapter` 和它的 `CardHeads`、`PruneMissingCardHeads`；删 `checkZovoCard`。
  - 删 `cardplatform_cdk.go` 里只由 adapter 调用的转发函数，以 lint 的 unused 为准。
  - 删 `zovoCardGraceRecovery`、`zovoCardFacts`、`zovoMarkFailed`；删 `lookup_results.go` 里转发卡台的门控（清单 §3.3 c）。
  - 只改注释的位置照清单改成中性说法。
- [ ] **Step 4：改测试**
  - 本站码测试里的 `localZovoStub`，换成 `providertest` 假上游：用它断言「本站码不会发出任何上游请求」，断言强度不变。
  - `public_email_mask_audit_ob_test.go` 里两条邮箱打码用例改成本站码版本，不能删（敏感数据）。
  - `aftersale_zovo_test.go` 整删前，逐条核对清单 §3.4「提醒」里 4 条公共层断言：邮箱打码、严格按 ID 取新码、`UsedAt` 为 RFC3339 UTC、过期判定。本站码版本没有覆盖的，先在 `aftersale_cardsupply_test.go` 补上。
  - 其余按清单 §2.5、§3.4 处理。测试里需要「不存在的服务商名」时直接写字符串 `"gone"`，不写 zovocard。
- [ ] **Step 5：门禁**
  - 后端门禁；
  - `go test -race -count=1 ./internal/provider/...`、`./internal/server/`；
  - 单独跑 `./internal/handler/`。
- [ ] **Step 6：提交** `refactor(provider): 摘掉 ZovoCard 服务商与兑换转发，认不出的码直接提示`

---

### Task 4：删后台接口和其余后端功能

**Files：**
- Delete：`handler/cardplatform.go`、`handler/webhook.go`、`handler/card_health.go`、`handler/card_pref.go`、`handler/card_selection.go`、`handler/redeem_policy.go`、`handler/cardplatform_cdk.go`（剩余部分；清单 §3.2 说可整删时整删）
- Modify：`handler/handlers.go`（删 `GetSystemStats`）、`handler/settings.go`（删卡台地址和密钥的读、写、校验；皮肤编号不动）、`handler/network.go`（删卡台环境）、`handler/local_cdk_admin.go`（若有剩余卡台引用）、`server/http.go`（删清单 §3.1 列的全部路由，包括 `GET /api/v1/public/cdk/plans`、`GET /api/v1/stats/system`、`POST /api/v1/webhooks/cardplatform`、`/api/v1/admin/cardplatform/*`、`/api/v1/admin/card-health/*`、`/api/v1/admin/card-selection/*`；删 `plansync.Start`）
- Modify 测试：清单 §3.4 的整删清单和 `settings_webhook_audit_s1/s6`、`cdk_session_test.go` 两条、`cdk_prefix_audit_s5`、`issue_*` 等
- 明细：清单 §3.1、§3.2、§3.4

**Interfaces：**
- Produces：上面这些路由都不存在了。`GET /api/v1/admin/settings` 的返回里不再有 `card_api_base`、`card_api_key_configured`、`webhook_secret_configured`（以清单为准）；任务 6 要删掉前端对这些字段的读取。

- [ ] **Step 1：写测试，看它失败**
  - `server` 包新增 `TestZovoCardRoutesGone`。它是表驱动测试，逐条断言以下路由回 404（需要登录的路由带上管理员登录态）：
    - `POST /api/v1/webhooks/cardplatform`
    - `GET /api/v1/admin/cardplatform/ping`
    - `GET /api/v1/admin/cardplatform/cdks/stored`
    - `GET /api/v1/stats/system`
    - `GET /api/v1/public/cdk/plans`
    - `GET /api/v1/admin/card-health`
    - `GET /api/v1/admin/card-selection/site-policy`

    同时断言 `GET /api/v1/admin/providers` 照旧回 200。
- [ ] **Step 2：删处理函数和路由**
  - 按清单 §3.2 删文件和函数，按 §3.1 删路由。
  - `settings.go` 删掉卡台字段的读写。`AdminPutSettings` 收到旧字段时照旧忽略（清单风险 12），不额外报错。
  - 删 `plansync.Start` 的调用。
  - 删 `GetSystemStats`，并删掉它独占的辅助函数。
- [ ] **Step 3：删测试**
  - 删掉清单 §3.4 列为「整文件删」「删这些用例」的测试。
  - `settings_webhook_audit_s1_test.go` 保留 `TestS1SettingsBrandLength`、`TestS1SettingsWriteFailureReturns500`、`s1Put`。
- [ ] **Step 4：门禁**
  - 后端门禁。此时 `cardplatform`、`plansync` 包应该已经没有生产代码在 import（测试除外）；在报告里贴 `git grep -n 'internal/cardplatform"\|internal/plansync"' -- backend` 的输出。
  - 单独跑 `./internal/handler/`，再跑 `./internal/server/`。
- [ ] **Step 5：提交** `refactor: 删除 ZovoCard 后台接口、Webhook、坏卡拉黑、选卡规则和套餐同步`

---

### Task 5：删卡台客户端包和数据库函数，老库升级测试

**Files：**
- Delete：`backend/internal/cardplatform/`（整包）、`backend/internal/plansync/`（整包）
- Modify：`db/*.go`，按清单 §4.2 删没人用的函数；在 §4.3 列的表的建表处加注释
- Modify：`server/startup_check.go`、`server/http.go:157,200`（文案里「卡台」改「旧版」）、`server/startup_check_audit_o3_test.go:111,288`（同步文案）
- Test：`server/legacy_zovocard_db_test.go`（新）

- [ ] **Step 1：写老库升级测试，看它是否通过**
  - 新增 `TestLegacyZovoCardDBStartsAfterUpgrade`，用清单 §4.5 的 `o3NewDBFile` 建一个库，塞入以下旧数据：
    - `cd_keys`、`cardplatform_cdk_codes`、`webhook_events`、`card_blocklist` 各一行；
    - `site_settings` 的 `card_api_base`、`card_api_key`（密文）、`cdk_default_provider="zovocard"`；
    - `agent_cdk_exchanges` 一行 `old_upstream_id>0`、`state="disabling"`。
  - 断言：
    1. `o3Check` 只读检查通过；
    2. 对同一文件再跑 `db.Migrate()` 不报错；
    3. 上面每张表的行数与塞入时相同；
    4. `provider.LoadConfig()` 给出任务 3 的「已不存在」警告。
  - 这条测试在删包前后都应通过：先在当前代码上跑一次，确认通过。
- [ ] **Step 2：删包**
  - 删 `cardplatform`、`plansync` 两个包。
  - 按清单 §4.2 删 db 层没人用的函数及其测试。用 `deadcode` 或 lint 核对：删完后不能再多出无人用的函数。
  - §4.3 的表加注释；建表语句和迁移不改。
- [ ] **Step 3：改启动检查文案**
  - 把「卡台密钥迁移」改成「旧版密钥迁移」，「卡台 API Key」改成「旧版卡台 API Key（card_api_key）」。
  - 同步改测试里的断言。
  - 检查逻辑不动。
- [ ] **Step 4：门禁**
  - 后端门禁；
  - `go test -race -count=1 ./internal/db/... ./internal/server/`；
  - 单独跑 `./internal/handler/`；
  - `govulncheck ./...`。
- [ ] **Step 5：提交** `refactor: 删除卡台客户端包、套餐同步包和只给 ZovoCard 用的数据库函数`

---

### Task 6：前端

**Files：**
- Delete：`views/admin/CardIntegration.vue`、`CardSelectionConfig.vue`、`OrderReconcile.vue`、`WebhookEvents.vue`，以及它们的测试
- Modify：
  - `router/routes.ts`（删 4 条路由；加 4 条重定向到 `/ops/providers`；旧别名 `billing`、`reconcile`、`cards` 直接指向 `/ops/providers`）；
  - `lib/adminNav.ts`、`layouts/AdminLayout.vue`（删菜单项；删顶栏「卡台·未配置」标记）、`lib/adminui.ts`（删 `zovoEnv`）；
  - `views/admin/CDKeyManagement.vue`（删卡台那一类码）、`views/admin/AdminDashboard.vue`（删 4 格和 `/api/v1/stats/system` 请求）；
  - `views/admin/components/AgentExchangePanel.vue`（新路径，去掉「卡台」字样和 `old_disabled` 分支）、`views/admin/components/AgentSwapSettings.vue`；
  - `views/admin/CardProviders.vue`、`lib/provider.ts`、`lib/errors.ts`（删「去配置卡台」映射，审计动作名保留）；
  - `theme.ts`（显示名改为 `曜夜雾金` / `Night Gold`，`blurb` 里的「与卡台新版一致」删掉）；
  - `lib/plan.ts`、`lib/claude.ts`、`lib/grok.ts`、`lib/idempotency.ts`、`lib/secretError.ts`、`views/user/BatchRedeemView.vue` 的注释（改成中性说法）。
- Modify：语言包里只由已删后端分支产生的 `agentSwap.errors.*` 键。用 `git grep -n '"error_code"' backend` 核对后端还在返回哪些码，没人返回的键在 zh、en 两边同时删掉，`billing-agent-i18n.spec.ts` 里写死的码表同步删。
- Modify 测试：清单 §5.5；测试里拿 `zovocard` 当假上游的地方统一换成 `acme`；界面禁用词列表里的「卡台」不动。
- 明细：清单 §5.1–§5.5

- [ ] **Step 1：写测试，看它失败**
  1. 路由测试：`/ops/integration`、`/ops/card-selection`、`/ops/orders`、`/ops/webhooks`、`/ops/billing`、`/ops/reconcile`、`/ops/cards` 都一次重定向到 `/ops/providers`。
  2. 菜单测试：后台菜单里没有「接入配置」（ZovoCard）、「选卡规则」（ZovoCard）、「兑换订单」、「Webhook」，MarsWallex 的几项都在。
  3. `AdminDashboard` 渲染：没有「CDK 总数」「可用 CDK」「已兑换」「兑换订单」4 格，也不发 `/api/v1/stats/system` 请求。
  4. `CDKeyManagement` 渲染：「本站卡密」标签渲染 `LocalCdkPanel`，「代理换码」标签渲染 `AgentSwapSettings` 和 `AgentExchangePanel`；页面上没有「从卡台同步完整码」。
  5. `AgentExchangePanel`：请求 `/agent-exchanges`；界面文字里没有「卡台」。
  6. 皮肤：`SKINS` 里 `id==='zovo'` 那项的 `label` 是 `曜夜雾金`，`labelEn` 是 `Night Gold`。
- [ ] **Step 2：改代码**
  - 按清单 §5.2 逐处改。
  - `CDKeyManagement.vue` 基本是重写：只留页头、本站卡密、代理换码三部分，逻辑照原样搬，不改行为。
- [ ] **Step 3：真实页面检查**
  - 用 vite 开发服务器（端口不要用 18080）配合本地后端，或只用前端加假数据。登录后台后逐一检查：
    - 「CDK 卡密」两个标签都能切换、列表能加载；
    - 「卡服务商」页有提示时能显示；
    - 4 个旧地址会跳转；
    - 顶栏没有「卡台」标记。
  - 截图存 `/Volumes/ExtSSD/projects/aisubproxy/.playwright-mcp/`，在报告里列出截图文件名。
  - 不调真实业务接口，不填真实密钥。
- [ ] **Step 4：门禁** `cd frontend && npx vitest run && npm run build`
- [ ] **Step 5：提交** `refactor(frontend): 删除 ZovoCard 后台页面、总览卡台数字和卡台码类别，默认皮肤改显示名`

---

### Task 7：文档、更新说明和最终搜索

**Files：**
- Delete：
  - `docs/zovocard-cdk-zh.md`
  - `docs/zovocard-openapi-zh.md`
  - `docs/卡台CDK接入准备-2026-07-28.md`
  - `docs/卡台CDK测补与兑换进度-2026-07-28.md`
  - `docs/卡台回调配合需求-给负责人-2026-07-28.md`
  - `docs/cdk-list-region.md`
  - `docs/x-premium-subscriptions.md`
- Modify：
  - `README.md:9`（删「卡台官网」行）；
  - `docs/开源自托管进度-2026-07-28.md`（:12 改写成不提卡台，其余提到卡台的段落删）；
  - `docs/marswallex-openapi-zh.md:1,3`（「卡台」改「卡服务」）；
  - `docs/known-issues.md`（清单 §6 列的 ZovoCard 专用条目标为「已随 ZovoCard 删除（2026-11-10）」，money N-2 条一并标上）；
  - `deploy/docker-deploy.sh:222`（提示里删「卡台 API Key」）；
  - `.gitleaks.toml:31`（注释改成「假 API Key」）；
  - `backend/internal/payengine/builtin.go:7`、`payengine/config.go:33`、`acgshared/money.go:22`、`gptcheck/check.go:2` 的注释；
  - `CHANGELOG.md`「未发布」一节。

- [ ] **Step 1：CHANGELOG**。在「未发布」一节加以下内容，升级注意放在这一节最前：
  - **升级前必须做**：
    - 还在用 ZovoCard 的部署不要升级；
    - 先到后台「代理换码记录」，把 ZovoCard 码的「结果不明」「旧码待禁用」记录处理完。升级后系统不会再去卡台作废旧码。
  - **升级后要确认**：「卡服务商」页没有「已不存在」或「没有设置全局默认服务商」的提示；有的话重新选择并保存。
  - **行为变化**：规格第 4 节 1–7 条，加上 §11.9 的 3 条，写成面向管理员的话。另列删掉的公开接口：`GET /api/v1/public/cdk/plans`、`POST /api/v1/webhooks/cardplatform`、`GET /api/v1/stats/system`。
  - **删除**：规格第 8 节「删除」那一条。
- [ ] **Step 2：改文档**。按上面的 Files 改。历史规格、计划、调研不改。
- [ ] **Step 3：最终搜索**
  - 运行 `git grep -n -i -E 'zovo|cardplatform|卡台' -- . ':!docs/superpowers' ':!docs/research' ':!CHANGELOG.md'`，把输出全文写进报告。
  - 每一处都要能归到下面某一类：
    - 规格 §3.2；
    - 规格 §3.3；
    - 规格 §11.7；
    - 规格 §11.11；
    - 审计动作显示名；
    - 测试里的界面禁用词列表；
    - 测试里用来证明「旧数据还在」的旧表名和设置名。
  - 归不进去的，改掉，或在报告里说明为什么保留。
- [ ] **Step 4：全量门禁**
  - 后端门禁；
  - 按包分开跑全部后端测试（handler 单独跑）；
  - `go test -race -timeout 30m ./internal/paybrowser/...`；
  - `govulncheck ./...`；
  - 前端门禁；
  - `/Users/zhengshan/go/bin/gitleaks git --log-opts="origin/master..HEAD" --redact --no-banner .`。
- [ ] **Step 5：提交** `docs: ZovoCard 删除后的文档、部署提示和更新说明`

---

## 执行安排（规格 §9）

| 任务 | 实现 | 审查 |
|---|---|---|
| 1 挪共用件 | sonnet | sonnet |
| 2 代理换码（动钱） | opus | fable |
| 3 摘服务商与兑换转发 | sonnet | fable（兑换分派与付款相邻） |
| 4 后台接口 | sonnet | sonnet |
| 5 删包和数据库函数 | sonnet | sonnet |
| 6 前端 | sonnet | sonnet |
| 7 文档与最终搜索 | sonnet | sonnet |
| 全分支终审 | — | fable |

fable 不可用时，临时改用 opus，恢复后把还没派出的审查切回 fable。
