# 订阅套餐「允许 0 元开通」实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 订阅套餐每个地区可勾选「允许 0 元开通」（默认只有 Plus 勾上）；勾了的，结账页金额正好是 0 时照常开通，不再以 `amount_below_floor` 放弃。

**Architecture:**
- 套餐表 `RegionConf` 加一个可空布尔字段，读取和保存时补默认值、沿用旧值。
- 付款引擎建会话时取开关，在下限检查里对 0 元放行。
- 关续费核实后，对 0 元单再查一次账号，不是期望套餐就告警。
- 发码就绪门槛只认金额大于 0 的成功单。
- 后台订阅套餐表加勾选框，订单金额栏区分「0 元开通」和「没读到」。

**Tech Stack:** Go（gin、SQLite），Vue 3 + Element Plus，vitest。

**Spec:** `docs/superpowers/specs/2026-11-11-zero-amount-plus-design.md`

## Global Constraints

- 所有代码注释、事件、告警、界面文字用简体中文。新增文字逐字按本计划写，不要自己改写。
- **资金**：
  - 不新增注资路径，不改注资金额（`FundCents`），不新增点击；
  - 不改熔断（`breaker.go`）和确认成功的判据（`verifyAccount` 只比 `plan_type`）；
  - 不在建结账会话时加 `promo_campaign` 或任何优惠参数。
- **敏感数据**：新事件和告警只带单号、套餐类型，不带会话、卡号、邮箱。出错文字先过 `j.scrub`。
- **测试**：用现有的假浏览器、假 ChatGPT 和测试库，不调用真实业务接口，不动真实资金。
- **TDD**：先写测试、看它失败，再实现。
- **提交**：
  - 用明确路径 `git add <文件>`，不用 `git add -A`，不提交 `.claude/`、`.superpowers/`；
  - zsh 里路径列表用数组 `F=(...)` 加 `"${F[@]}"`；
  - 提交信息末尾加 `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`。
- **后端检查**：在 `backend/` 下跑 `gofmt -l .`（应无输出）、`go vet ./...`、`/opt/homebrew/bin/golangci-lint run ./...`（0 issues）。
- **前端检查**：在 `frontend/` 下跑 `npx vitest run` 和 `npm run build`。
- **不改** `backend/internal/paybrowser/`。

## 裁决（计划相对规格的两处调整）

- **规格 4.2 写了 `j.note("zero_amount", true)`，改为不写。** `j.note` 写的是单据表的列，单据表没有这一列。0 元单的标记只在时间线事件里体现。认定方式不变，仍是规格 4.4 的「订阅单、`AmountMinor == 0`、`AmountText != ""`」。
- **规格 4.5 的门槛条件 `amount_minor > 0` 照写。** 测试里造「成功真实单」的辅助函数原来金额都是 0，要一并补上金额（Task 4）。

## Review Focus

1. **旧后台页面保存时不带 `allow_zero`**：应沿用当前生效的值，包括管理员手动取消后的 false，不能悄悄改回默认。在 Task 1 测。
2. **点数套餐被传了 `allow_zero: true`**：应存成 false，点数单 0 元照旧放弃。在 Task 1、Task 2 测。
3. **建会话后管理员改了开关**：按建会话时取到的值。在 Task 2 测：先取值，再改表，再读金额。
4. **复查只对 0 元单做**：金额大于 0 的成功单关续费时，不能多调一次 `Account`，也不能告警。在 Task 3 测。
5. **0 元单成功后的就绪门槛**：只有 0 元成功单时为 false，不能让发码判为就绪。在 Task 4 测。

---

### Task 1: 套餐表的「允许 0 元开通」字段

**Files:**
- Modify: `backend/internal/payengine/config.go`
  - `RegionConf`（约第 85 行）；
  - `defaultPlan` / `fillDefaultRegions`（约第 100–125 行）；
  - `normalizePlan`；
  - `LoadPlanTable`（约第 146 行）；
  - `SavePlanTable`（约第 185 行）。
- Test: `backend/internal/payengine/config_test.go`（追加）

**Interfaces:**
- Produces:
  - `RegionConf.AllowZero *bool`，JSON 名 `allow_zero,omitempty`；
  - `func (rc RegionConf) ZeroAllowed() bool`；
  - `func zeroAllowedByDefault(planKey string) bool`，只有 `"plus"` 为 true。
  - `LoadPlanTable` 返回的每个地区，`AllowZero` 都不是 nil。

- [ ] **Step 1: 写失败的测试** `TestPlanTableAllowZero`，在 `config_test.go` 里用子测试：
  - 「出厂默认」：清空设置后 `LoadPlanTable()`。
    - `plus` 的 5 个地区 `ZeroAllowed()` 都是 true；
    - `go`、`pro_5x`、`pro_20x`、`pro_50x` 都是 false；
    - 所有点数档（`PlanKind(key) == KindCredit`）都是 false；
    - 每个地区的 `AllowZero` 都不是 nil。
  - 「旧表缺字段」：用 `db.SetSetting(settingPlanTable, ...)` 写一份不含 `allow_zero` 的 JSON，读出来后 plus/PH 为 true，pro_20x/PH 为 false。
  - 「保存时缺字段沿用当前值」：
    1. 先保存 plus/US 为 `AllowZero: boolPtr(false)`；
    2. 再保存一份 plus/US 的 `AllowZero` 为 nil 的表；
    3. 读出来 plus/US 仍是 false；
    4. 同样，pro_20x/US 先存 true，再存 nil，读出来仍是 true。
  - 「点数档传 true 存成 false」：保存 `credit250`/PH `AllowZero: boolPtr(true)`，读出来是 false，并且 JSON 里写的是 false。
  - 「ResolvePlan 带出开关」：`ResolvePlan("plus", "PH", false)` 返回的 `rc.ZeroAllowed()` 与表里的一致。

  `boolPtr` 已在 `renewal_test.go` 定义，同包可直接用。保存要用完整表：先 `LoadPlanTable()`，改其中一项，再 `SavePlanTable(tbl)`，和现有的 `saveWith` 一样。

- [ ] **Step 2: 跑测试，确认失败**
  Run: `cd backend && go test -count=1 -run TestPlanTableAllowZero ./internal/payengine/`
  Expected: 编译失败，提示 `AllowZero`、`ZeroAllowed` 未定义。

- [ ] **Step 3: 实现**
  - 在 `config.go` 加字段、方法和 `zeroAllowedByDefault`。字段注释写：「结账页金额正好是 0 时（首月免费等优惠）是否照常开通；nil 表示没保存过，按套餐默认（只有 Plus 允许）。点数套餐一律不允许。」
  - **出厂表**：`fillDefaultRegions` 给每个地区写明确的默认值，内置价那一支也要写。
  - `normalizePlan` 对点数档把每个地区的 `AllowZero` 设成指向 false。注意 `Regions` 是 map，要整项写回。
  - `LoadPlanTable`：合并已保存的地区后，nil 的按 `zeroAllowedByDefault(key)` 补上。
  - `SavePlanTable`：校验通过后、`normalizePlan` 之前，对 nil 的地区用当前表（`LoadPlanTable()`）对应地区的值补上；当前表读不出时用默认值。

- [ ] **Step 4: 跑测试，确认通过**
  Run: `cd backend && go test -count=1 -run 'TestPlanTableAllowZero|TestPlanTable|TestSavePlanTable|TestLoadPlanTable' ./internal/payengine/`
  Expected: PASS

- [ ] **Step 5: 跑整个包，确认没有带坏别的**
  Run: `cd backend && go test -count=1 ./internal/payengine/`
  Expected: PASS

- [ ] **Step 6: 提交**
  `feat(payengine): 套餐表加「允许 0 元开通」，默认只有 Plus 允许`

### Task 2: 下限检查对 0 元放行

**Files:**
- Modify: `backend/internal/payengine/worker.go`
  - `job` 结构体：在 `floorMinor`（约第 217 行）旁边加 `allowZero bool`；
  - `createLink`（约第 573 行）；
  - `openPage` 的下限检查（约第 775–788 行）。
- Test: `backend/internal/payengine/amount_floor_test.go`（追加）

**Interfaces:**
- Consumes: Task 1 的 `RegionConf.ZeroAllowed()`。
- Produces: `job.allowZero bool`，在 `createLink` 里取值：`rc.ZeroAllowed() && plan.Kind == KindSubscription`。

- [ ] **Step 1: 写失败的测试**。夹具 harness 的套餐是 plus/PH，最低 ₱100.00。plus 默认允许 0 元，「不允许」要用 `saveWith` 存 `AllowZero: boolPtr(false)`，其余字段照抄 harness 的 PH 配置。
  - `TestZeroAmountAllowedProceeds`：
    - 条件：`setPage(h, 0, "₱0.00")`，真实单，`waitOutcome` 为成功。
    - 断言：
      - 走到取卡（`h.pool.count("Acquire") > 0`）；
      - 最终 `StateSucceeded`；
      - `ErrorCode` 不是 `amount_below_floor`；
      - 有一条 info 事件，原文是「支付页显示 0 元（首月免费等优惠），该套餐在该地区允许 0 元开通，照常开通」。
  - `TestZeroAmountNotAllowedAborts`：PH 存成不允许，`setPage(h, 0, "₱0.00")`。断言 `assertNotSubmitted(t, o, "amount_below_floor")`、`h.pool.total() == 0`。
  - `TestPartialDiscountStillAborts`：允许 0 元，`setPage(h, 5000, "₱50.00")`。断言 `amount_below_floor`，没碰卡池。
  - `TestZeroAmountProPlanAborts`：
    - 条件：把 harness 的单子改成 `pro_20x`/PH。用 `saveWith` 给 pro_20x/PH 写和 plus/PH 一样的配置，`AllowZero` 不填（nil），即默认不允许；`Submit` 时 `PlanKey: "pro_20x"`。
    - 断言：0 元以 `amount_below_floor` 放弃。
  - `TestZeroAmountCreditAborts`：`newCreditHarness`，`setPage(h, 0, "₱0.00")`。断言 `amount_below_floor`，不碰卡池。
  - `TestDryRunZeroAmountNotes`：试运行，`setPage(h, 0, "₱0.00")`。
    - 允许时：有一条 info「支付页显示 0 元（首月免费等优惠），该套餐在该地区允许 0 元开通；真实付款会照常开通」，没有「低于最低金额」的 warn；
    - 不允许时：仍是原来那条 warn「…真实付款会在此放弃」。
  - `TestZeroAllowedTakenAtCreateLink`：
    - 条件：仿照 `TestRealOrderWithZeroFloorAborts`，不启动工作协程，包内直接调 `createLink`。之后用 `saveWith` 把 plus/PH 改成不允许，再 `setPage(h, 0, "₱0.00")`、设 `j.o.CheckoutURL = testCheckoutURL`，调 `openPage`。
    - 断言：返回 nil（按建会话时的「允许」放行），没有碰卡池（`openPage` 不注资）。
    - 如果 `createLink` 需要的假 ChatGPT 结账返回不够，用 harness 已有的 `h.gpt` 默认值。

- [ ] **Step 2: 跑测试，确认失败**
  Run: `cd backend && go test -count=1 -run 'TestZeroAmount|TestPartialDiscount|TestDryRunZeroAmount|TestZeroAllowedTaken' ./internal/payengine/`
  Expected:
  - 允许 0 元的几条失败（现在以 `amount_below_floor` 放弃）；
  - `TestZeroAllowedTakenAtCreateLink` 编译失败（没有 `allowZero`）；
  - 不允许的几条可能已经通过，这是对的。

- [ ] **Step 3: 实现**
  - `createLink` 里在 `j.floorMinor = rc.FloorMinor` 的同一处给 `j.allowZero` 赋值。点数单也走这一处，按 `plan.Kind` 判断。
  - `openPage` 的下限分支改成：
    - 真实单：先判 `minor == 0 && j.allowZero`，成立就记上面的 info 事件，然后直接 `return nil`；不成立照原来的两支走（`floorMinor <= 0` 回 `plan_unavailable`、`minor < floorMinor` 回 `amount_below_floor`）。
    - 试运行单：`minor == 0 && j.allowZero` 记试运行那条 info，否则照原来的逻辑。
  - 不写 `j.note("zero_amount", …)`（见「裁决」）。

- [ ] **Step 4: 跑测试，确认通过**
  Run: `cd backend && go test -count=1 -run 'TestZeroAmount|TestPartialDiscount|TestDryRunZeroAmount|TestZeroAllowedTaken|TestAmount|TestDryRun|TestCreditAmount|TestRealOrderWithZeroFloor' ./internal/payengine/`
  Expected: PASS

- [ ] **Step 5: 跑整个包**
  Run: `cd backend && go test -count=1 ./internal/payengine/`
  Expected: PASS

- [ ] **Step 6: 提交**
  `feat(payengine): 允许 0 元开通的套餐，结账页 0 元时照常开通`

### Task 3: 0 元单关续费后复查账号

**Files:**
- Modify: `backend/internal/payengine/settle.go`：从 `verifyAccount`（约第 499 行）抽出取期望类型的部分。
- Modify: `backend/internal/payengine/renewal.go`：`cancelRenewal`（约第 237 行）、调用它的续费处理（约第 193–210 行）。
- Modify: `backend/internal/payengine/worker.go`：`job` 加复查结果字段。
- Test: `backend/internal/payengine/renewal_test.go`（追加）

**Interfaces:**
- Consumes: Task 2 让 0 元单能成功。
- Produces:
  - `func expectPlanType(planKey string) (string, error)`：读套餐表，没配置时报错，错误文字和 `verifyAccount` 原来的一样；`verifyAccount` 改为调它。
  - `func (o *Order) zeroAmount() bool`：`o.Kind == KindSubscription && o.AmountMinor == 0 && o.AmountText != ""`。
  - `job` 字段 `zeroCheck zeroRecheck`，类型是 `type zeroRecheck struct{ done bool; planType, expect, err string }`。

- [ ] **Step 1: 写失败的测试**。用 `newRenewalHarness`，`setPage(h, 0, "₱0.00")`，`h.br.waitOutcome` 为成功：
  - `TestZeroAmountRenewalRecheckStillPlus`：
    - 条件：账号一直返回 plus。用 `h.gpt.accountFn = planFrom(2)`，或直接让它一直返回 plus。
    - 断言：
      - 续费到 `RenewalCancelled`；
      - 有一条 info 事件「0 元开通的单关闭续费后，账号仍是 plus」；
      - 不告警（`h.alerts.Texts()` 为空）；
      - 关续费后比金额大于 0 的单多调一次 `Account`。可以记下成功时的 `h.gpt.count("Account")`，再比对续费完成时的值。
  - `TestZeroAmountRenewalRecheckLostPlan`：
    - 条件：`accountFn` 在确认成功时给 plus，在关续费后的复查里给 free（`PlanType: "free"`、`HasActiveSubscription: false`）。按调用次数 n 切换：先看确认阶段要调几次，复查那次起给 free。
    - 断言：
      - 续费仍是 `RenewalCancelled`，单据仍是 `StateSucceeded`；
      - 有一条 warn 事件，含「已不是 plus」；
      - 告警正好 1 条，原文是「付款单 <单号> 以 0 元开通，关闭续费后账号已不是 plus（现在是 free），可能关续费让优惠当场结束了，请人工处理」；
      - 告警和事件都不含会话 token（`testSession`、`at-refreshed`）和邮箱。
  - `TestZeroAmountRenewalRecheckError`：复查那次 `Account` 报错。断言续费 `RenewalCancelled`，有一条 warn 含「核实不了账号套餐」，不告警。
  - `TestRenewalNoRecheckForPaidOrder`：默认页面 ₱1,120.00。断言关续费完成后，没有「0 元开通」字样的事件，不告警，`Account` 调用次数和改动前一样（不多调）。

- [ ] **Step 2: 跑测试，确认失败**
  Run: `cd backend && go test -count=1 -run 'TestZeroAmountRenewal|TestRenewalNoRecheckForPaidOrder' ./internal/payengine/`
  Expected: 复查相关的三条失败（没有事件、没有告警）。`TestRenewalNoRecheckForPaidOrder` 可能已经通过。

- [ ] **Step 3: 实现**
  - **抽函数**：`expectPlanType` 从 `verifyAccount` 抽出来，`verifyAccount` 改为调它，行为不变。
  - **复查**：`cancelRenewal` 里两处返回 `true, ""` 之前（「已经关掉」的重试路径和取消后核实的路径），如果 `j.o.zeroAmount()`，就调 `s.recheckZero(ctx, gpt, j)`。签名是 `func (s *Service) recheckZero(ctx context.Context, gpt ChatGPT, j *job)`，做三件事：
    1. 取 `expectPlanType(j.o.PlanKey)`；
    2. 调 `gpt.Account(ctx, j.sess)`；
    3. 把结果写进 `j.zeroCheck`（`done=true`）；出错时 `err` 先过 `j.scrub`。
  - **处理结果**：调用方在 `moveRenewal(... RenewalCancelled ...)` 成功之后，如果 `j.zeroCheck.done`：

    | 复查结果 | 处理 |
    |---|---|
    | `err != ""` | warn 事件：`fmt.Sprintf("0 元开通的单关闭续费后，核实不了账号套餐：%s", err)` |
    | 套餐类型相同（`strings.EqualFold`，两边先 `TrimSpace`） | info 事件：`fmt.Sprintf("0 元开通的单关闭续费后，账号仍是 %s", expect)` |
    | 套餐类型不同 | warn 事件：`fmt.Sprintf("0 元开通的单关闭续费后，账号已不是 %s（现在是 %s）", expect, planType)`；告警：`fmt.Sprintf("付款单 %s 以 0 元开通，关闭续费后账号已不是 %s（现在是 %s），可能关续费让优惠当场结束了，请人工处理", o.OrderNo, expect, planType)` |

  - **写事件**：用 `s.event(ctx, j, level, msg, "")`（worker.go:362，只用 `j.o.OrderNo`）；`j` 就是传给 `cancelRenewal` 的那个。告警用 `s.opts.Alert`。
  - **测试计数注意**：`renewalOff` 在订阅接口没给 `will_renew` 时自己也会调一次 `Account`。复查测试比较的是「0 元单」和「同样设置下的付费单」的调用次数之差，不要写死绝对次数。

- [ ] **Step 4: 跑测试，确认通过**
  Run: `cd backend && go test -count=1 -run 'TestZeroAmountRenewal|TestRenewal|TestRetryRenewal|TestResolveTriggersRenewal|TestSettle|TestVerify' ./internal/payengine/`
  Expected: PASS

- [ ] **Step 5: 跑整个包（带 race）**
  Run: `cd backend && go test -race -count=1 ./internal/payengine/`
  Expected: PASS

- [ ] **Step 6: 提交**
  `feat(payengine): 0 元开通的单关续费后复查账号，不是期望套餐就告警`

### Task 4: 就绪门槛只认金额大于 0 的成功单

**Files:**
- Modify: `backend/internal/db/local_cdk.go`：`HasRealSucceededPayOrder`（约第 626 行），订阅分支。
- Modify: `backend/internal/db/local_cdk_test.go`：`TestHasRealSucceededPayOrderGate`（约第 296 行）。
- Modify: `backend/internal/handler/local_cdk_env_test.go`：`seedRealPayment`（约第 552 行）。
- Modify: 其他因门槛变化而失败的测试夹具（Step 4 跑全量时找出来）。

**Interfaces:** 无新接口。门槛的 SQL 订阅分支变为 `AND kind = 'subscription' AND renewal_state = 'cancelled' AND amount_minor > 0`。

- [ ] **Step 1: 写失败的测试**。在 `TestHasRealSucceededPayOrderGate` 末尾加：
  1. 新建一张订阅成功单：`renewal_state = 'cancelled'`、`amount_minor = 0`、`amount_text = '₱0.00'`，服务商是 `zerocard`（用 `addProviderOrder`）；
  2. 断言 `HasRealSucceededPayOrder(localCtx, "zerocard", db.PayKindSubscription)` 为 false；
  3. 再把它改成 `amount_minor = 112000, amount_text = '₱1,120.00'`，断言为 true。

  同时把测试里原有「应当算过门槛」的那张订阅单（变量 `sub`）补上 `amount_minor = 112000`。testcard 那张也补上，因为后面断言它过门槛。

- [ ] **Step 2: 跑测试，确认失败**
  Run: `cd backend && go test -count=1 -run TestHasRealSucceededPayOrderGate ./internal/db/`
  Expected: FAIL（0 元单现在也算过门槛）。

- [ ] **Step 3: 实现**：订阅分支加 `AND amount_minor > 0`，注释写「0 元开通（首月免费等优惠）的单不能证明真实扣款走得通」。点数分支不变。

- [ ] **Step 4: 修夹具，跑相关包**
  - `seedRealPayment` 在 `markRenewalCancelled` 之后把这张单设为 `amount_minor = 112000, amount_text = '₱1,120.00'`。
  - Run: `cd backend && go test -count=1 ./internal/db/ ./internal/payengine/` 以及 `go test -count=1 -timeout 30m ./internal/handler/`
  - Expected: PASS。还有别的测试因为门槛失败的，同样给它造的成功订阅单补上金额，不改断言。

- [ ] **Step 5: 提交**
  `fix(db): 发码就绪门槛不认 0 元开通的单`

### Task 5: 后台界面、文档、CHANGELOG

**Files:**
- Modify: `frontend/src/lib/pay.ts`：`RegionConf`、`FormRow`、`planToForm`、`formToPlan`，新增函数 `orderAmountLabel`。
- Modify: `frontend/src/views/admin/components/PaySubscriptionPlans.vue`：在「启用」列（第 12 行）后加一列。
- Modify: `frontend/src/views/admin/MarsWallexPay.vue`：金额栏（第 109 行、第 224 行），改用 `orderAmountLabel`。
- Test:
  - `frontend/src/lib/__tests__/pay.test.ts`（追加）；
  - `frontend/src/views/admin/__tests__/pay.spec.ts`（追加）。
- Modify: `docs/superpowers/specs/2026-11-03-embedded-checkout-design.md`：§5.10 末尾。
- Modify: `CHANGELOG.md`：在第一个 `## v1.5.6` 之前新开 `## 未发布`。

**Interfaces:**
- Consumes: 后端接口返回的 `regions[r].allow_zero`（Task 1 保证一定有值）、订单的 `amount_minor`、`amount_text`、`currency`。
- Produces:
  - `RegionConf.allow_zero?: boolean`；
  - `FormRow.allowZero: boolean`；
  - `export function orderAmountLabel(minor: number, text: string, currency: string): string`：
    - `minor > 0` 时返回 `formatMinor(minor, currency)`；
    - `minor === 0` 且 `text` 不为空时，返回 `formatMinor(0, currency) + '（0 元开通）'`；
    - 其余返回 `'—'`。

- [ ] **Step 1: 写失败的测试**
  - `pay.test.ts`：
    - `planToForm` 读出 `allow_zero: true` 的行，`allowZero` 为 true；缺字段时为 false。
    - `formToPlan`：
      - 订阅套餐写出 `allow_zero` 等于行上的值；
      - 点数套餐（`kind: 'credit'`）写出 `allow_zero: false`，即使行上是 true。
    - `orderAmountLabel`：
      - `(112000, '₱1,120.00', 'PHP')` 等于 `formatMinor(112000, 'PHP')`；
      - `(0, '$0.00', 'USD')` 等于 `formatMinor(0, 'USD') + '（0 元开通）'`；
      - `(0, '', 'USD')` 等于 `'—'`。
  - `pay.spec.ts`：
    - 用已有的 `render(PaySubscriptionPlans, …)` 渲染一个 plus 表单。断言 HTML 含列名「允许 0 元开通」，并含提示「结账页显示 0 元（首月免费等优惠）时照样开通；不勾则放弃这一单，不注资。」
    - 用 `render(PayCreditPlans, …)` 渲染点数表，断言不含「允许 0 元开通」。

- [ ] **Step 2: 跑测试，确认失败**
  Run: `cd frontend && npx vitest run src/lib/__tests__/pay.test.ts src/views/admin/__tests__/pay.spec.ts`
  Expected: 新加的几条 FAIL。

- [ ] **Step 3: 实现**
  - `pay.ts`：按上面的接口改。`formToPlan` 对点数套餐写 false。
  - `PaySubscriptionPlans.vue`：
    - 新列标签「允许 0 元开通」，宽度和「启用」列相近，单元格里是 `el-switch v-model="row.allowZero"`；
    - 表格上方或列头的提示里写提示原文（「结账页显示 0 元（首月免费等优惠）时照样开通；不勾则放弃这一单，不注资。」），用这个组件已有的说明样式。
  - `MarsWallexPay.vue`：两处金额改用 `orderAmountLabel(row.amount_minor, row.amount_text, row.currency)`。详情页的「（上限 …，注资 … 美元）」保留。
  - **设计文档 §5.10 末尾加一段**：「例外（2026-11-11）：订阅套餐在某地区勾了「允许 0 元开通」（默认只有 Plus 勾上）时，支付页金额正好是 0（首月免费等优惠）照常开通，注资照旧；0 以上但低于最低金额的仍放弃。见 `docs/superpowers/specs/2026-11-11-zero-amount-plus-design.md`。」
  - **CHANGELOG**：新开 `## 未发布`，按升级注意结构写：

    ```markdown
    ## 未发布

    ### 升级注意

    > 本版让订阅套餐可以「0 元开通」：结账页显示 0 元（首月免费等优惠）时照样开通。**没有数据库迁移，不用改环境变量。**

    #### 升级后要确认

    - **刷新所有开着的后台页面**，旧页面不认识新的「允许 0 元开通」开关。

    #### 行为变化（无需操作）

    - **Plus 默认允许 0 元开通**：结账页显示 0 元时照样开通，不再以「低于最低金额」放弃；注资照旧按配置注满，卡用完转回。不想要的，到后台「MarsWallex 付款 → 订阅套餐」取消勾选「允许 0 元开通」。Go、Pro 默认不勾；点数套餐没有这个开关。0 以上但低于最低金额的（如半价）照旧放弃。系统不会主动带任何优惠。
    - **0 元开通的单关闭续费后会再查一次账号**：已不是该套餐时告警，请人工处理。
    - **0 元开通的单不算「真实付款跑通」**：发码就绪仍需一笔金额大于 0 的真实付款。
    - **后台订单金额**：0 元开通的单显示「0.00 USD（0 元开通）」，没读到金额的仍显示「—」。
    ```

- [ ] **Step 4: 跑测试和构建**
  Run: `cd frontend && npx vitest run && npm run build`
  Expected: 全部 PASS，构建成功。

- [ ] **Step 5: 提交**
  `feat(admin): 订阅套餐表加「允许 0 元开通」开关，订单金额区分 0 元开通`

### 收尾：全量门禁

- [ ] Run（`backend/`）：
  - `gofmt -l .`（无输出）、`go vet ./...`、`/opt/homebrew/bin/golangci-lint run ./...`（0 issues）；
  - `go test -race -count=1 $(go list ./... | grep -v '/internal/handler$' | grep -v '/internal/paybrowser')`；
  - `go test -race -count=1 -timeout 30m ./internal/handler/`；
  - `/Users/zhengshan/go/bin/govulncheck ./...`。
- [ ] Run（`frontend/`）：`npx vitest run`、`npm run build`。
- [ ] Run（仓库根）：`/Users/zhengshan/go/bin/gitleaks git --log-opts="origin/master..HEAD" --redact --no-banner .`
- [ ] paybrowser 没改，本地不跑，留给 CI。
