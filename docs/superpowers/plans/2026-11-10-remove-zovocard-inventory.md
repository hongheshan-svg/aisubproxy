> 本文件是 ZovoCard 整删计划的附件：写计划前在临时副本上按规格真删一遍得到的逐文件清单。行号以 master c091c05 为准，rebase 之后以实际代码为准；「原型」「草稿目录」指当时的临时副本，已不保留。

# ZovoCard 整删：逐文件清单

- 工作区：`/Volumes/ExtSSD/projects/aisubproxy/.claude/worktrees/remove-zovocard`，分支 `feat/remove-zovocard`（基于 master c091c05 + 规格提交 dd5d7fb）。
- 依据：`docs/superpowers/specs/2026-11-10-remove-zovocard-design.md`。
- 本清单没有改仓库里任何文件。验证用的是复制到草稿目录的原型（`.../scratchpad/zovo/proto/backend`）：按规格把代码真删了一遍，再用 `go build`、`go vet`、`golangci-lint`（含 `unused`）、`deadcode`（x/tools，装在 `.../scratchpad/zovo/bin/deadcode`）、`go test` 找传递依赖和受影响的测试。下面凡写「原型验证」的都是这么跑出来的。
- 行号都是当前分支上的行号。「路径」均相对 `backend/internal/`（后端）或 `frontend/src/`（前端）。
- 原型验证的总体结论：按规格删完后，生产代码 `go build ./...` 通过；`golangci-lint --tests=false` 相比基线只多出 1 条 unused（`handler/provider_adapter.go` 的 `upstreamExchanger`）；`deadcode` 相比基线多出 52 个无人用的 db 函数（见第 4 节）；测试：约 68 个测试声明（含 helper）在 25 个测试文件里编译不过（原型里被自动剔除后统计）；剔除后 `handler` 包另有 43 个测试运行失败、`provider`/`server` 各 1 个（见各节「测试文件」）。

---

## 1. 挪共用小件（行为不变）

### 1.1 要挪的东西与调用方

| 东西 | 现在在 | 目标 | 调用方（非测试） |
|---|---|---|---|
| `MaxIssueCount = 200` | `cardplatform/client.go:62`（用在 :535-536，随包删） | `provider.MaxIssueCount`（建议放 `provider/provider.go`） | `handler/agent_order.go:342, 355, 356`；`handler/local_cdk_admin.go:215, 216`；另有 `handler/cardplatform_cdk.go:169-170`（整文件处理时消失）；注释 `acgshared/money.go:22` |
| `IsXPremiumCredential` | `cardplatform/x_premium.go:35-50` | `credential.IsXPremiumCredential`（新包 `internal/credential`） | `handler/provider_guard.go:93`（`upstreamAccountEmail`）；`handler/cardplatform_cdk.go:1085`（随文件处理） |
| `IsGrokCredential` | `cardplatform/grok.go:46-56` | `credential.IsGrokCredential` | 同上，`provider_guard.go:93`、`cardplatform_cdk.go:1085` |

### 1.2 规格漏掉的共用件（必须一起挪，否则删包后编译不过）

原型验证：只挪上面三个，`go build` 仍会失败。还要处理：

1. **`IsXPremiumPlan`（`cardplatform/x_premium.go:19-27`）、`IsGrokPlan`（`cardplatform/grok.go:15-17` 和 `grokPlanRe` :14）、`IsClaudePlan`（`cardplatform/claude.go:15-22`，依赖 `claudeBarePlans` :11-14）**：被 `issueProduct`（`handler/cardplatform_cdk.go:103-119`）用来按档位键推断产品线。`issueProduct` 又被**保留的** `CardPlatformIssueCDKs`（`handler/local_cdk_admin.go:132`）和 `localCDKIssue`（`:201`）调用，不能删。三个 `Is*Plan` 都是独立纯函数，只需带上 `grokPlanRe`、`claudeBarePlans` 两个包级变量。建议挪到 `provider` 包（它本来就管产品线），`issueProduct` 本身改到 `local_cdk_admin.go`。
2. **`issueProduct`、`deviceFrom`（`cardplatform_cdk.go:1026-1031`）、`str`、`toString`（`cardplatform_cdk.go:1428-1441`）**：都在要删的 `cardplatform_cdk.go` 里，但保留代码在用。`str` 有 22 处非测试调用（`cdk_dispatch.go:30`、`local_cdk.go:199,348,352,581,583,604` 等），`deviceFrom` 被 `lookup.go:62,104` 用，`issueProduct` 被 `local_cdk_admin.go:132,200` 用。
3. **`maskEmailForAdmin`（`handler/card_health.go:521-532`）、`keepHead`（:534-542）、`adminName`（:544-558）**：在要整文件删的 `card_health.go` 里。`maskEmailForAdmin` 被 `aftersale.go:189` 用；`adminName` 被 `cardpool.go`（十几处）、`agent_admin.go:362`、`local_cdk_admin.go:246` 用。要先挪走。
4. `cardplatform.APIError` 之类只在被删代码里用，不用挪。

### 1.3 测试文件（任务 1）

| 文件 | 动作 |
|---|---|
| `cardplatform/client_issue_test.go` | `TestMaxIssueCountMatchesBatchLimit`（:5-9）挪到 `provider` 包测试；`TestCDKListItemFullCodeText`（:11 起）随包删 |
| `cardplatform/x_premium_test.go` | `TestXCredentialsAreNotInvoiceSessions`（:32-55）拆开：:33-41 凭据部分 → `credential/credential_test.go`；:42-54 档位部分跟 `IsXPremiumPlan` 走；其余 3 个用例（`TestXPremiumCatalogueIsProductScoped`、`TestXPaymentCountries`、`TestXCatalogueCannotFallBackToGPTOnOldServers`）随包删 |
| `cardplatform/grok_test.go` | `TestGrokPlansAndCredentials`（:34-58）：:56-58 `IsGrokCredential` 部分 → credential 测试；`IsGrokPlan` 部分跟函数走；`GrokIssuePlan` 部分（:47-54）随包删 |
| `acgshared/acgshared_test.go` | `TestParseQuantity`（:137-）第 138 行 `cardplatform.MaxIssueCount` 改成 `provider.MaxIssueCount`（已核对 provider 包不 import acgshared，无环）或直接写 200；删 :9 的 import |
| `handler/provider_admin_test.go` | :334 `cardplatform.MaxIssueCount+1` 改 `provider.MaxIssueCount+1`；删 :20 的 import |
| `handler/grok_issue_test.go` | `TestIssueProductKeepsGPTDefault`（:96 调 `issueProduct`）保留，挪后继续通过（原型里因为 Is*Plan 被我桩成 false 而失败，不是真问题） |

---

## 2. 摘服务商

### 2.1 `provider` 包要改的地方

| 位置 | 改法 |
|---|---|
| `provider/builtin.go:9-12` `init()` | 去掉 `Register(zovoCard{})`（:10） |
| `provider/builtin.go:14-33` `zovoCard` 类型与 9 个方法（含 `Fallback`、`CodeList() "cardplatform"`） | 整段删 |
| `provider/builtin.go:46` 注释 | `PlanNote` 注释里「在 ZovoCard 是 Plus 升级 Pro」改成中性说法（行为不动） |
| `provider/provider.go:21-25` 常量 | 删 `ZovoCard Name = "zovocard"`（:23） |
| `provider/provider.go:76-78` | `Upstream` 接口去掉 `Fallback() bool` |
| `provider/provider.go:85-87` | `CodeList` 的注释里「cardplatform 卡台码列表」改掉；`CodeLister` 机制本身保留 |
| `provider/provider.go:192-199` `validate` | 去掉「兜底上游只能有一家」那段（:192-198），保留 `return nil` |
| `provider/provider.go:295-303` | 删 `fallback()` |
| `provider/provider.go:326-340`、`355-376` | `ForCode`/`ForToken` 注释里的第 3 条「兜底」删；`forUpstream` 去掉 `fb` 逻辑，只剩「有认领的返回，否则 `ErrUnrecognized`」 |
| `provider/config.go:72-73` 注释、`:86-99` `LoadConfig` | 删 `fb := fallback()` 与 `fbName`（:87-92）；见 2.3 |
| `provider/config.go:136-160` `factoryConfig` | 只配 MarsWallex：`Default = marswallex`，`ByProduct` 空；删 `fb` 全部逻辑 |
| `provider/providertest/providertest.go:46-47、:50-67、:69` | `Up.IsFallback` 字段与 `Up.Fallback()` 方法删（接口不再要求）；注释 |
| `provider/providertest/providertest.go` | 另外注意：这个包不是 `_test.go` 文件，`golangci-lint` 会检查它 |

### 2.2 `handler` 包要改的地方

| 位置 | 改法 |
|---|---|
| `handler/provider_adapter.go:122` | `upstreamAdapters` 初值改成空 map（`map[provider.Name]upstreamAdapter{}`）；其余上游框架（`upstreamAdapter`、`upstreamAdapterFor`、`CheckProviderAdapters`、`upstreamOrFail` 等）按规格 3.2 保留 |
| `handler/provider_adapter.go:103-106` | `upstreamExchanger` 接口：删掉 `zovoCardAdapter` 后没有任何引用，`unused` 会报；随 `agent_exchange.go:345-354` 一起删（见 3.2） |
| `handler/provider_adapter.go:24` | 注释「如卡台余额」改中性 |
| `handler/provider_builtin.go:15-50` | 整段删：`zovoCardAdapter` 类型与 `Preview/Preflight/Redeem/Result/ResultByCode/Issue/Exchange/GraceRecovery/Lookup/Check` |
| `handler/provider_builtin.go:69-98` | 删 `(zovoCardAdapter) CardHeads`（:69-95）和 `PruneMissingCardHeads`（:97-98） |
| `handler/provider_builtin.go:3-11` | import 里的 `errors`、`db` 仍被 MarsWallex 的 `CardHeads` 用，保留；其余按编译提示 |
| `handler/provider_readiness.go:229-247` | 删 `checkZovoCard`；:10 删 `cardplatform` import |
| `handler/card_heads.go:34-38` | `cardHeadSourceFor` 里查 `upstreamAdapterFor` 的那段保留（框架），不动逻辑 |
| `handler/card_heads.go:88-92` | `cardHeadPruneMissing` 可选接口：现在没有实现者，但 `syncCardHeads` 仍引用它，保留，只改 :89 注释（去掉 ZovoCard / `db.PruneCardProductsExcept`） |
| `handler/provider_config_admin.go:70、:129` | 注释里的 `"zovocard"` 示例改成别的字符串 |

### 2.3 码没人认领时现在走哪个分支

- 注册表层：`provider.ForCode`/`ForToken`（`provider/provider.go:331-353`）→ `forUpstream`（:355-376）→ 没有非兜底上游认领、也没有兜底 → 返回 `ErrUnrecognized`。这条分支现在就有测试（`provider/registry_test.go:128` `TestForCodeWithoutFallback`）。
- 公开兑换（预览/预检/兑换/查结果/按码查结果/宽限期恢复）：`routeCode` / `routeToken`（`handler/provider_adapter.go:287-304`）→ **HTTP 404，`{"error":"无法识别的卡密"}`（`unrecognizedCodeText`，:248）**，没有 `provider`、`code` 之类字段。原型里 `ZC-` 开头的码实测就是这个响应。
- `routeBody`（预检、兑换，:310-346）：令牌和码都没给时（:312-314）以前交给兜底上游「按现有方式报错」，现在也是 404 `无法识别的卡密`（行为小变化）。
- 代理换码 `PublicAgentCDKExchange`（`handler/agent_exchange.go:336-339`）：口令校验通过之后才到 `routeCode`，所以口令失败计数、限流不受影响；认不出的码同样 404 `无法识别的卡密`，不占换码额度（占位在更后面，本站码才会走到）。
- 售后查询 `factsFor`（`handler/aftersale.go:137-140`）：`ForCode` 出错直接 `afterSaleFacts{}, nil`，即 `Found=false` → 单条接口 404 `{"error":"未找到该卡密记录","status":"unknown",...}`，批量里是 `status: unknown`。换码链 `followAfterSaleChain`（:246-268）遇到认不出的码会断链，不报错。
- 发码 `CardPlatformIssueCDKs`（`handler/local_cdk_admin.go:133-158`）：带了 `provider` 但不存在 → 400 `未知的卡服务商：xxx`（:137）；没带时走配置，见下。

### 2.4 `LoadConfig` 在全局默认存的是 zovocard 时会怎样

代码在 `provider/config.go:74-134`。删掉兜底后：

- 库里 `cdk_default_provider = "zovocard"`：`Get` 不到 → :94-98 走 else，`cfg.Default` 保持初值（现在是空 `""`，以前是兜底 ZovoCard），警告文字是 `全局默认服务商 "zovocard" 已不存在，按 %s 处理，请重新选择`，其中 `%s` 取 `DisplayName(fbName)`，`fbName` 为空 → 原样返回空串 → **文字变成「按  处理」（中间空的）**。「已不存在」提示有，但文案坏了，必须改文案（建议：「全局默认服务商 "zovocard" 已不存在，请到「卡服务商」页重新选择」）。
- `cfg.Default == ""` 的后果：`Config.For`（:55-60）返回 `""`；`ResolveIssue`（:239-252）`Get("")` 失败 → 返回 `fmt.Errorf("%w：没有可用的默认服务商", ErrUnknown)`，不是 `*UnsupportedError`；`CardPlatformIssueCDKs`（`local_cdk_admin.go:152-157`）把它当成读配置失败 → **HTTP 500 `读取发码服务商配置失败`**（日志里才有原因）。没有自动换家、不会多花钱，但文案误导。建议把 `ErrUnknown` 单独映射成 400 + 「请到卡服务商页重新选择默认服务商」。
- `cdk_provider_by_product` 里存了 `{"x":"zovocard"}`：`:122-126` 已有「X Premium 选的服务商 "zovocard" 已不存在，按全局默认处理，请重新选择」提示，不用改。全局默认是 marswallex 时 X/Grok/Claude 因 `providesProduct` 失败，发码被拒（`UnsupportedError`，400）。
- 一个没提示的漏洞：`cdk_default_provider` 为空、`cdk_provider_by_product` 有值（`:83` 只在两者都空时走出厂配置）：以前 `Default` 取兜底 ZovoCard，现在 `Default=""` 且**没有任何警告**（:100-102 直接 `return cfg, nil`）。正常保存（`SaveConfig` 同时写两项）不会出现，手工改库或 `SaveDefault` 单独写才会。建议 `Default==""` 时统一补一条警告。
- `AdminPutProviderConfig`（`provider_config_admin.go`）、`ValidateConfig`（`config.go:188-215`）：保存时拒绝未注册的名字（400 `服务商配置不合法：全局默认服务商 "zovocard" 未注册`），管理员必须重选，这是现有行为。
- 「页面显示已不存在」靠 `providersOverview`（`provider_config_admin.go:40-61`）的 `warnings` 字段；前端 `CardProviders.vue:14` 已有 `registry-warning` 区域渲染它。

### 2.5 测试文件（任务 2）

**provider 包**（原型里这些在编译期就失败，需改写而不是简单删）：

| 文件 | 用例 | 动作 |
|---|---|---|
| `provider/provider_test.go` | `TestDefaultProviderLoadSave`(:13)、`TestParse`(:36)、`TestSupports`(:49)、`TestCapabilitiesFromSupports`(:79)、`TestBuiltinRouting`(:129)、`TestKindText`(:209) | 改：把 `ZovoCard` 换成 `providertest` 的假上游或字符串；删掉「Names() 有两家」「ZovoCard 全档位支持」类断言（:65-69, :88-101, :136-137, :146-147, :213-218）；`TestBuiltinRouting` 改成断言只有 MW- 前缀归 MarsWallex、其余全部 `ErrUnrecognized`（对应规格测试项 1） |
| `provider/provider_test.go` | `TestMarsWallexSupportsEnginePlans`(:112)、`TestGenerateCode`(:152)、`TestMarsWallexTokenStable`(:188)、`TestReadinessCollectsProblems`(:197) | 不动 |
| `provider/registry_test.go` | `TestRegisterRejectsBadProviders`(:26) | 改：删「第二个兜底上游」子用例（:54 用了 `IsFallback`）；:34 的 `dupUp.N = provider.ZovoCard` 改成 `provider.MarsWallex` |
| `provider/registry_test.go` | `TestRegisterRestored`(:79) | 改：:84 `len(Names()) != 2` 改成 1（原型里这条运行失败） |
| `provider/registry_test.go` | `TestForCodeAndToken`(:90) | 改：期望表里原归 ZovoCard 的 `ZC-AAAA`、`TCX-AAAA`、`""`、`zc-tok-1`（:98-112）改成断言 `ErrUnrecognized` |
| `provider/registry_test.go` | `TestForCodeWithoutFallback`(:128) | 并入上一条或改名；它测的就是新行为 |
| `provider/registry_test.go` | `TestConfigResolution`(:145) | 改：出厂配置断言（:155-166）改为 `Default=marswallex`、`ByProduct` 空；:181-194 存 `zovocard` 的用例改成断言警告文案（含新文案）和 `Default==""`；:214-225 同理；新增规格测试项 3、4 |
| `provider/registry_test.go` | `TestValidateAndSaveConfig`(:249)、`TestSelected`(:287) | 改：`zovocard` 全部换成 `providertest` 假上游或字符串 |
| `provider/registry_test.go` | `TestCapabilitiesIncludeRegistered`(:319) | 核对（原型里没报错，但可能依赖行数） |

**handler 包**：

| 文件 | 用例 | 动作 |
|---|---|---|
| `handler/provider_registry_test.go` | `TestIssueDispatchByProvider`、`TestPublicDispatchByProvider`(:205 运行失败)、`TestCrossProviderMutexThreeWay`(:416 运行失败)、`TestAdminProviderReadinessPerProvider`(:480 运行失败) | 改：用 `providertest.NewTestUp()` 代替 ZovoCard 做「上游」角色；`TestIssueDispatchByProvider` 里发码走上游的断言删 |
| `handler/provider_registry_test.go` | `Check`（假适配器方法，:原型里编译期被删）、`TestTestCardPublicFlowCarriesProviderFields`、`TestMarsWallexPublicResponsesCarryProviderFields` | 改：去掉 `provider.ZovoCard` 与 `zc-` 字面量 |
| `handler/provider_config_admin_test.go` | `TestAdminListProviders`(:42 运行失败)、`TestAdminPutProviderConfig`(:104 失败)、`TestAdminProvidersConfigWarnings`(:169 失败) | 改：内置只有一家、示例名换假服务商；新增「存着 zovocard 的默认/产品配置给出已不存在提示」（规格测试项 4） |
| `handler/provider_admin_test.go` | `TestDefaultProviderReadWriteAudited`(:80 失败)、`TestAdminProviderReadiness`(:619 失败)、`TestDefaultProviderOnlyAffectsNewIssues`、`TestMarsWallexSelectedForReplenish`（后两个编译期失败，用了 `provider.ZovoCard`，:111/:1048/:1052） | 改：`zovocard` 换成 `providertest` 的 `testup` |
| `handler/card_heads_test.go` | `TestZovoCardCardHeadsNeedsCache`(:121)、`TestZovoCardCardHeadsPruneDelisted`(:406) | 删（只测 `zovoCardAdapter.CardHeads`/`PruneMissingCardHeads`） |
| `handler/card_heads_test.go` | `TestCardHeadSourceFor`(:156)、`TestCardHeadsAutoProvider`(:269)、`TestCardHeadsAPIRowAnnotatableDespiteUpstreamFields`(:359)、`TestSyncCardHeadsSkipsInvalidProductIDs`(:380) | 改：用 `providertest` 假上游代替 `zovoCardAdapter`（:124,:136,:160,:414-426 用了它） |
| `handler/provider_helpers_test.go`、`handler/pay_test.go`(`TestPaySubmitProvider`) | | `pay_test.go` 里 `TestPaySubmitProvider` 含 `zovocard` 字符串（`pay.spec.ts` 同理），改成别的名字 |
| `server/provider_routes_test.go` | `TestProviderRoutesRegisteredAndRequireAdminAuth`(:34) | 改：:46 `":name"→"zovocard"` 的替换换成 `marswallex`；路由表（:18-33）不变 |
| `server/provider_routes_test.go` | `TestCardPoolOptionsWiresProviderGate`(:59) | 改：编译期失败，用了 `provider.ZovoCard`；换成假服务商，断言「未选用 MarsWallex 时自动补卡暂停」不变 |
| `server/routes_test.go` | `TestAgentExchangeAdminRoutesRequireAdminAuth`(:96-112) | 改：路径换成 `/api/v1/admin/agent-exchanges` 和 `/api/v1/admin/agent-exchanges/1/resolve`（原型里运行失败） |

---

## 3. 后端接口

### 3.1 `server/http.go` 要删的每条路由（行号为现在的行号）

| 行 | 方法 路径 | 处理函数 | 备注 |
|---|---|---|---|
| 277 | GET `/api/v1/public/cdk/plans` | `PublicCDKPlans` | **规格没列**。数据全来自卡台，前端无调用（已 grep），建议随这次删 |
| 304-305 | POST `/api/v1/webhooks/cardplatform` | `CardPlatformWebhook` | 规格项 11 |
| 314-319 | GET `/api/v1/stats/system`（整个 `stats` 路由组） | `GetSystemStats` | 规格项 13；整组一起删 |
| 418 | GET `/api/v1/admin/cardplatform/ping` | `CardPlatformPing` | |
| 419 | GET `/admin/cardplatform/plans` | `CardPlatformPlans` | |
| 420 | GET `/admin/cardplatform/balance` | `CardPlatformBalance` | |
| 421 | GET `/admin/cardplatform/cdks` | `CardPlatformListCDKs` | |
| 422 | GET `/admin/cardplatform/cdks/stored` | `CardPlatformListStoredCDKs` | |
| 423 | POST `/admin/cardplatform/cdks` | `CardPlatformIssueCDKs` | **只删路由，处理函数不能删**：同一个函数还挂在 :436 `POST /admin/cdks/issue`（本站发码，前端 `LocalCdkPanel.vue:315` 在用） |
| 440 | POST `/admin/cardplatform/cdks/sync` | `CardPlatformSyncUpstreamCDKs` | |
| 441 | POST `/admin/cardplatform/cdks/store` | `CardPlatformStoreCDKCodes` | |
| 442 | POST `/admin/cardplatform/cdks/batch-disable` | `CardPlatformBatchDisableCDKs` | |
| 443 | POST `/admin/cardplatform/cdks/batch-enable` | `CardPlatformBatchEnableCDKs` | |
| 444 | POST `/admin/cardplatform/cdks/batch-note` | `CardPlatformBatchSetCDKNote` | |
| 445 | POST `/admin/cardplatform/cdks/batch-clear-note` | `CardPlatformBatchClearCDKNote` | |
| 446 | PUT `/admin/cardplatform/cdks/:id/note` | `CardPlatformSetCDKNote` | |
| 447 | POST `/admin/cardplatform/cdks/:id/disable` | `CardPlatformDisableCDK` | |
| 448 | POST `/admin/cardplatform/cdks/:id/enable` | `CardPlatformEnableCDK` | |
| 451 | GET `/admin/cardplatform/cdk-orders` | `CardPlatformListCDKOrders` | |
| 452 | GET `/admin/cardplatform/cdk-orders/:id` | `CardPlatformGetCDKOrder` | |
| 453 | DELETE `/admin/cardplatform/cards/:id` | `CardPlatformDeleteCard` | |
| 456 | GET `/admin/webhooks/events` | `AdminListWebhooks` | 规格项 11 |
| 459 | GET `/admin/network/egress` | `NetworkEgress` | 前端只有 CardIntegration、CDKeyManagement 调用 |
| 478-483 | GET/PUT `/admin/card-selection/rules`；GET `/admin/card-selection/plan-status`；POST `/admin/card-selection/sync`；GET/PUT `/admin/card-selection/site-policy` | `AdminGetCardSelectionRules`、`AdminPutCardSelectionRules`、`AdminGetPlanStatus`、`AdminSyncPlanStatus`、`AdminGetSiteRedeemPolicy`、`AdminPutSiteRedeemPolicy` | 6 条 |
| 486-490 | GET `/admin/card-health`；GET/PUT `/admin/card-health/policy`；POST `/admin/card-health/unblock`；POST `/admin/card-health/observe` | `AdminListCardHealth`、`AdminGetCardHealthPolicy`、`AdminPutCardHealthPolicy`、`AdminUnblockCard`、`AdminReobserveCardOrder` | 5 条；`observe` 前端没有调用 |
| 22, 91 | （import 与 `plansync.Start(ctx)`） | | 一起删 |

**换码记录接口改路径**（规格 5.3）：
- `server/http.go:449-450` 改成 `admin.GET("/agent-exchanges", handler.AdminListAgentExchanges)`、`admin.POST("/agent-exchanges/:id/resolve", handler.AdminResolveAgentExchange)`。
- `handler/agent_exchange.go:559`、`:571` 两处注释里的旧路径要改。
- 前端 `views/admin/components/AgentExchangePanel.vue:104`（`'/cardplatform/agent-exchanges'`）、`:138`（`'/cardplatform/agent-exchanges/' + row.id + '/resolve'`）。
- 测试 `server/routes_test.go:102-103`。
- 其它：`docs/`、`CHANGELOG.md` 里引用旧路径的地方改前先 `git grep 'cardplatform/agent-exchanges'`。

被删路由之后的 404：这些路径在 `/api` 前缀下，gin 没匹配到路由，未登录也是 404（`NoRoute` 对 `/api` 回 `{"error":"not found"}`，`http.go` 末段）。规格测试项 5 按此写。

### 3.2 handler 文件逐个给动作

| 文件 | 动作 | 说明 |
|---|---|---|
| `handler/cardplatform.go`（97 行） | **整文件删** | `CardPlatformPing`；`detectEgressIP` 只被它和 `network.go` 用 |
| `handler/network.go`（96 行） | **整文件删** | `NetworkEgress`(:16-40) 含 zovocard.com 预设；`detectEgressIP`(:42-85)、`statusErr`(:87-96) 无其他调用（原型验证） |
| `handler/webhook.go`（231 行） | **整文件删** | `CardPlatformWebhook`(:22)、`applyCDKStatusFromWebhook`(:97)、`clearSessionFromWebhook`(:124)、`webhookIdemKey`(:134)、`jsonNumber`(:168)、`AdminListWebhooks`(:174)、`sanitizeWebhookPayload`(:220)。`handler/marswallex_webhook.go` 是另一个文件，不动 |
| `handler/card_pref.go`（194 行） | **整文件删** | 选卡优先级、`SyncOwnerDirectCardRules`、`injectRedeemCardPolicy`、`issuePrefFromSite` 等，只服务卡台 |
| `handler/card_selection.go`（233 行） | **整文件删** | 选卡规则/套餐状态/本站策略 6 个接口，依赖 `plansync` |
| `handler/redeem_policy.go`（94 行） | **整文件删** | `SiteRedeemPolicy` 只被 `card_pref.go`、`card_selection.go` 用（原型验证无其他引用） |
| `handler/card_health.go`（569 行） | **删 :1-519 和 :560-569，保留并挪走 :521-558** | 删：`EvaluateCardFailVerdict`(41)、`loadCardHealthPolicy`(61)、`saveCardHealthPolicy`(74)、`normalizeAccountEmail`(86)、`ObserveCardOrderOutcome`(96)、`resolveEmailForOrder`(239)、`observeFromWebhookPayload`(253)、`observeFromPublicResult`(322)、`AdminGetCardHealthPolicy`(397)、`AdminPutCardHealthPolicy`(406)、`AdminListCardHealth`(424)、`AdminUnblockCard`(451)、`AdminReobserveCardOrder`(474)、`exactCDKCodeByID`(560)，以及 :22 `webhookObservers`。**挪走**：`maskEmailForAdmin`(521)、`keepHead`(534)、`adminName`(544)（见 1.2） |
| `handler/cardplatform_cdk.go`（1600 行） | **删全部，仅挪走 `issueProduct`(103-119)、`deviceFrom`(1026-1031)、`str`(1428-1436)、`toString`(1438-1441)** | 其余函数全是卡台专用：`writeCardErr`(25)、`CardPlatformPlans`(64)、`CardPlatformBalance`(92)、`zovoCardIssueCDKs`(121-310)、`issueReplayTimeout`(312)、`CardPlatformSyncUpstreamCDKs`(316)、`CardPlatformStoreCDKCodes`(354)、`CardPlatformListStoredCDKs`(405)、`refreshStoredCDKMetadata`(518)、`CardPlatformDisableCDK`(562)、`...EnableCDK`(581)、`...BatchDisableCDKs`(601)、`...BatchEnableCDKs`(633)、`CardPlatformListCDKs`(665)、`CardPlatformListCDKOrders`(734)、`CardPlatformDeleteCard`(771)、`CardPlatformGetCDKOrder`(799)、`writeCDKOrderRaw`(819)、`enrichCDKOrderList`(830)、`enrichOneCDKOrder`(898)、`stripOrderCardNumber`(930)、`stripOrderValue`(934)、`cdkLifecycleLabel`(966)、`strAny`(994)、`int64Any`(1005)、`proxyPublicJSON`(1033)、`zovoCardPreview`(1042)、`zovoCardPreflight`(1064)、`preflightVerdict`(1109)、`zovoCardRedeem`(1139)、`zovoCardResult`(1159)、`zovoCardResultByCode`(1187)、`publicResultEmailKeys`(1234)、`maskPublicResultEmails`(1239)、`maskEmailsDeep`(1245)、`writePublicResult`(1273)、`publicResultStatus`(1283)、`isCompletedOrderStatus`(1294)、`clearSessionAfterCompleted`(1303)、`shortTok`(1315)、`docsDefaultNote/Registry/Plans`(1324-1369)、`PublicCDKPlans`(1371)、`extractJSONString`(1443)、`extractJSONNestedString`(1456)、`extractCredentialSession`(1470)、`looksLikeBareAccessToken`(1499)、`CardPlatformSetCDKNote`(1509)、`CardPlatformBatchSetCDKNote`(1538)、`CardPlatformBatchClearCDKNote`(1573)、`setLocalCDKStatuses`(1599)。注意：`CardPlatformIssueCDKs` 不在这个文件，在 `local_cdk_admin.go:118`，要保留 |
| `handler/handlers.go` | 删 `GetSystemStats`(:301-368，含 :299 的 System Stats 标题注释)；删 :15 import | 其余不动 |
| `handler/settings.go` | **删这些部分**：:16-21 `secretSettingKeys`；:56-71 `AdminGetSettings` 里遍历它的循环；:85-87 `CardAPIBase/CardAPIKey/WebhookSecret` 三个字段；:155-203 `AdminPutSettings` 的密钥类与卡台地址段（含 `cardplatform.ValidateBase/NormalizeBase/LoadConfig`、`secretbox.LoadKey` 检查）；:248-273 `writeSiteSettings` 去掉 `setBase/base/secrets` 参数与 `db.SetSettingsWithSecrets`；:10 `cardplatform` 和 `secretbox` import。**保留**：站点品牌/皮肤/主题/主色、`agent_swap_*`（`AdminGetSettings` 的 :73-75、`AdminPutSettings` 的 :204-221）、`defaultSiteSkin = "zovo"`(:27)、`allowedSkins`(:117-120)（规格 3.3 / D4）。`:24` 注释「卡台新版（zovo）」改成中性 |
| `handler/network.go` / `cardplatform.go` 以外的 `maskSecret`(settings.go:236) | 原型验证：仍被其他地方用，保留 | |
| `handler/lookup.go` | 删 :156-162 两个 `errZovo*`；:164-375 `zovoCardFacts`；:377-388 `zovoMarkFailed`；:17 `cardplatform` import 等随编译清理。保留 :51-154（`LookupCDKStatus`、批量、`lookupOneCDK`） | 见 3.3 c |
| `handler/lookup_results.go` | 改 `LookupCDKResultBatch`(:22-107)：删 :66-87 的 `isUpstream`/`KnownRedemptionTokens`/`notFound` 门控与 :85-88 的跳过分支；删 `routesUpstream`(:109-118)；:26-27、:121 注释改。**保留** 每个令牌走 `runPublicResult` 的主流程与排序、并发上限 | 原型验证：改完后 `db.KnownRedemptionTokens` 变成无人用 |
| `handler/grace_recovery.go` | 删 `zovoCardGraceRecovery`(:51-59) 与 :5 import；`PublicCDKGraceRecovery`(:20-49) 保留，剩下 :39-48 的上游分支在没有实现者时回 400「该服务商不支持宽限期处理」，属于框架保留 | |
| `handler/agent_exchange.go` | 见下表 | 动钱，最小改动 |
| `handler/ops_maintenance.go` | 见 3.3 b | |
| `handler/provider_builtin.go` / `provider_readiness.go` / `provider_adapter.go` | 见 2.2 | |
| `handler/provider_guard.go` | :12 import 换成 `credential`，:93 调用换包；:24 注释 | 其余（跨服务商互斥）不动，本站码流程也用 |
| `handler/local_cdk_admin.go` | :14 删 import、:215-216 换 `provider.MaxIssueCount`；:102-105、:112、:153、:197-198 注释里的 ZovoCard/卡台去掉；:118 `CardPlatformIssueCDKs` 的注释里旧路由去掉（可改名 `AdminIssueCDKs`，非必须） | |
| `handler/agent_order.go` | :20 删 import、:342/:355/:356 换 `provider.MaxIssueCount`（代理商下单，动钱，只换名字，数值 200 不变） | |
| `handler/local_cdk.go` | :939 注释「old_upstream_id 指卡台的码 ID……」改写（逻辑不动：`OldID: 0` 必须保留） | |
| `handler/version.go:19` | 规格 3.3，不动 | |
| `handler/aftersale.go`、`cdk_dispatch.go`、`card_heads.go` | 代码不动（框架）；`card_heads.go:89` 注释 | |

**`handler/agent_exchange.go` 逐函数**（行号为现在的行号）：

| 范围 | 函数 | 动作 |
|---|---|---|
| 25-173 | 口令/限流/额度（`swapRateAllow`、`noteSwapPasswordFailure`、`setAgentSwapPassword`、`verifyAgentSwapPassword`、`agentSwapDailyLimit`、`agentSwapPasswordFP` 等） | 不动 |
| 175-195 | `issueDefinitelyFailed` | 删 |
| 197-217 | `finishDisableOldCode` | 删 |
| 219-230 | `reportDisableFailure` | 删 |
| 232-242 | `agentExchangeConflict` | **保留**（`local_cdk.go:894,946` 在用） |
| 244-256 | `agentExchangeUnknown` | 删 |
| 258-298 | `orderLooksUnpaid` | 删 |
| 300-355 | `PublicAgentCDKExchange` | 改：删 :345-354（`upstreamOrFail` 取适配器 + `upstreamExchanger` 断言 + `ex.Exchange`），改成直接回 400「该服务商不支持代理换码」；:336-344 的 `routeCode`、本站码分支 `localCDKExchange` 一个字不动 |
| 357-557 | `zovoCardExchange` | 删 |
| 559-569 | `AdminListAgentExchanges` | **保留**，改注释里的路径 |
| 571-639 | `AdminResolveAgentExchange` | 改：保留（见 3.3 a，必须保留）；删 :620-636 里 `finishDisableOldCode`/`reportDisableFailure`/`old_disabled` 一段；`out` 里的 `old_cdk_id`/`old_disabled` 字段是否还给，要和前端 `AgentExchangePanel.vue:148` 一起定 |
| 641-650 | `agentSwapIssuePrefs` | 删 |
| 652-666 | `upstreamCDKRegion` | 删 |
| 668-680 | `upstreamCDKPlan` | 删 |
| 682-766 | `parseCDKOrderList`、`extractOrderMap`、`extractOrderEvents`、`anyToInt64` | 删（只被 `zovoCardExchange` 用） |
| imports | `context`、`encoding/json`、`net`、`cardplatform` 删 | `strconv`、`errors` 仍被保留代码用 |

### 3.3 三个专项问题

**a) `localCDKExchange`（`handler/local_cdk.go:891-1000`）会不会让换码记录进入「结果不明」或「旧码待禁用」？ 会。规格 4.6 的条件成立，定论表单必须保留。**

证据（代码）：
- 占位：`db.ReserveAgentCDKExchange`（`local_cdk.go:940`，`db/agent_exchange.go:175-214`）插入 `state='pending'`。
- `pending → unknown`：`db.EscalateStaleAgentExchanges`（`db/agent_exchange.go:346-381`，调用点 `handler/ops_maintenance.go:115`）把卡在 pending 超过 10 分钟（`agentExchangeStaleAfter`，`ops_maintenance.go:44`）的占位转 `unknown`。本站码的触发场景：进程在 `ReserveAgentCDKExchange` 之后、`FinishAgentCDKExchange` 之前崩溃或重启；或 `ExchangeLocalCDK` 事务已提交而 `FinishAgentCDKExchange`(:978) 写库失败（代码自己在 :979-980 写了告警「换码已发出新码但写库失败……占位已保留，请到后台定论」）。
- `→ disabling`：`db.FinishAgentCDKExchange`（`db/agent_exchange.go:216-222`，调用点 `local_cdk.go:978`）直接把 pending 置 `disabling`；紧接着 `db.CompleteAgentCDKExchangeDisable`（`local_cdk.go:981`）转 `done`。第二步失败（:981-984 只记日志）或进程在两步之间退出，记录就停在 `disabling`；之后靠 `ops_maintenance.go:77-111 retryAgentExchangeDisables` 的 `OldUpstreamID<=0` 分支补成 `done`。
- 人工定论 `issued`（`db.ResolveAgentExchangeIssued`，`db/agent_exchange.go:392-397`）也会把 unknown 置 `disabling`，同样靠这个分支收尾。
- 现有测试直接证明了这一点：`handler/provider_admin_test.go:486 TestLocalExchangeRecordNotStuckInDisabling`（造一条 `old_upstream_id=0` 的 `disabling` 记录，跑 `retryAgentExchangeDisables`，断言变 `done`）。
- 所以：`AdminResolveAgentExchange`（定论）、`AdminListAgentExchanges`、前端「结果不明」定论表单和「旧码待禁用」说明**都要保留**，只去掉卡台字样、去掉作废卡台旧码一步。`released` 对本站码有实际意义（释放占位让代理重试），`issued` 对本站码要填的「新码 ID」是 `local_cdks.id`。

**b) `ops_maintenance.go` 里重试禁用卡台旧码的任务怎么挂上去的，删掉要动哪些地方**

- 挂载链：`server/http.go:100 handler.StartOpsMaintenance(ctx)` → `ops_maintenance.go:134-` 的 `StartOpsMaintenance`（`safego.Go` + `safego.Loop`，每 `opsMaintenanceEvery`=10 分钟，启动先跑一轮）→ `runOpsMaintenance`（:67-73）按顺序调 `sweepCDKSessionsOnce`、`escalateAgentExchanges`(:71)、`retryAgentExchangeDisables`(:72)、`reconcileAgentBalances`、`pruneSharedFailures`。
- `retryAgentExchangeDisables`（:77-111）里**只有** :88 `cli := cardplatform.NewFromSettings()` 和 :99-110 `finishDisableOldCode(...)`＋`TouchAgentCDKExchange`＋`opsAlertDaily` 是卡台专用；:90-97 的 `OldUpstreamID<=0` 分支（本站码补记账）必须留下。改法：保留函数，删 `cli`、删 :99 之后卡台分支，import 去掉 `cardplatform`、`context`（若无他用）。
- 不能整函数删，也不能把 :72 那行删掉。
- **决策点**：历史 ZovoCard 行（`old_upstream_id>0` 且 `disabling`）删后没人处理，会一直停在「需要处理」。选项：（1）这个循环里把它们也直接转 `done` 并写审计「卡台已移除，无法禁用」；（2）留着不动，只在文案里说明。需用户定。
- `escalateAgentExchanges`（:113-130）：逻辑保留，告警文案「请先到卡台核对是否已发码」(:122) 要改。同时 `CountAgentExchanges(unknown)` 每天一条的提醒（:129）会把历史 ZovoCard 的 unknown 行也算进去，直到人工定论。
- `db.TouchAgentCDKExchange`（`db/agent_exchange.go:231`）删卡台分支后无人调用（原型 `deadcode` 验证），可一起删。
- `ops_maintenance.go` 里 `sweepCDKSessionsOnce` 不动（读的是 `cdk_session_bindings`，见风险 7）。

**c) 售后查询（lookup.go、lookup_results.go）删掉 ZovoCard 部分后，MarsWallex 码的查询路径是否完全不受影响？ 是。**

- 单条/批量：`LookupCDKStatus`(`lookup.go:51`)/`LookupCDKStatusBatch`(:78) → `lookupOneCDK`(:152) → `lookupAfterSale`(`aftersale.go:160`) → `factsFor`(:133)：`provider.ForCode` 命中 `MW-` 前缀 → `provider.AsCardSupply` → `cardSupplyFacts`（`local_cdk.go:799`），只读 `local_cdks`、换码记录、付款单，不碰 `zovoCardFacts`、`cd_keys`、`cardplatform_cdk_codes`、`recharge_tasks`、`cdk_session_bindings`。
- 换码链 `followAfterSaleChain`(`aftersale.go:246-269`)：每层重新 `factsFor`；本站码换码后的新码仍是 `MW-`，走同一条本站路径。
- 批量查结果 `LookupCDKResultBatch`(`lookup_results.go:22`)：对 `MW-` 令牌 `routesUpstream` 本来就返回 false（:109-118，供卡型「只查本库」），所以删掉 :66-87 的上游门控后 MarsWallex 令牌的行为不变。
- 原型验证：删完 ZovoCard 部分后，`aftersale_cardsupply_test.go` 的 4 个用例、`aftersale_test.go` 的 22 个用例、`lookup_test.go`、`lookup_results_s4_test.go`、`TestO3ResultsBatchCardSupplyTokensStayLocal` 全部通过。
- 唯一的行为变化是老 ZovoCard 码：`factsFor` 里 `ForCode` 失败 → `Found=false` → 单条 404 `未找到该卡密记录`。规格第 4 节没单独列这一条（见风险 11）。

### 3.4 测试文件（任务 3）

**整文件删**（原型验证：全部只测 ZovoCard，或删函数后无法编译）：
`handler/aftersale_zovo_test.go`（21 个声明：helper `zovoSeed*`、`zovoSetResult`，用例 `TestZovoFacts*` 共 16 个；见下面的提醒）、`handler/agent_exchange_region_test.go`（`TestAgentSwapIssuePrefsKeepRegion`）、`handler/card_health_test.go`（`TestEvaluateCardFailVerdict`、`TestNormalizeAccountEmail`）、`handler/card_pref_test.go`（`TestCanonicalCardIssuerAliases`、`TestBuildSelectPrioritySkipsOfflineAndDisabled`、`TestFirstUsablePrefSkipsPolicyProductIfOffline`）、`handler/card_selection_audit_hb_test.go`（`TestCardSelectionRulesHideDBErrors`）、`handler/cardplatform_cdk_audit_s7_test.go`（`TestS7StripOrderCardNumberNested`、`TestS7WriteCDKOrderUnparsableIs502`）、`handler/cardplatform_cdk_region_test.go`（`TestCDKListRegionRoundTripAndPageRefresh`）、`handler/cardplatform_cdk_status_audit_hb_test.go`（`TestSetLocalCDKStatusesReportsFailures`、`TestSetLocalCDKStatusesNoFailureIsEmptyNotNil`）、`handler/cdk_prefix_audit_s5_test.go`（`TestS5WebhookCompletedDoesNotClearByPrefix`、`TestS5ExactCDKCodeIgnoresPrefix`）、`handler/issue_idempotency_test.go`（`newIssueRouter` 与 3 个 `TestIssue*`，全是卡台发码幂等；本站发码幂等已有 `TestLocalIssueIdempotency`）、`handler/issue_recovery_audit_ob_test.go`（`obNewIssueRouter` 与 4 个 `TestObIssue*`，全是卡台发码重放）、`handler/x_premium_test.go`（`TestXIssueInfersProductWithoutQueryParameter`，卡台发 X 码；改为规格测试项 2「X/Grok/Claude 没服务商时拒绝发码」后保留，不是纯删）。

**提醒（aftersale_zovo_test.go）**：这个文件里有些断言其实是售后查询公共层的行为（邮箱打码 `TestZovoFactsEmailMaskedAllSources`、严格按 ID 取新码 `...ReplacementIsStrictByUpstreamID`、`...UsedAtIsRFC3339UTC`、`...ActiveButPastExpiryIsExpired`）。删之前确认本站码版本有等价覆盖（`aftersale_cardsupply_test.go` 的 `TestCardSupplyFactsMapping`/`...Exchanged` 覆盖了一部分，`UsedAt` 的 RFC3339 与过期状态是否覆盖要逐条核对）。

**删这些用例**：
- `handler/agent_exchange_money_test.go`：`TestIssueDefinitelyFailedClassification`(:367)；以及依赖假卡台 `exchangeUpstream`（:30-116）的整套（见下「改」）。
- `handler/agent_exchange_audit_o3_test.go`：`TestO3AgentExchangeLocalStatusSyncFailureAlerts`(:33)、`TestO3AdminResolveIssuedLocalStatusSyncFailureAlerts`(:72)、helper `o3FailLocalDisable`/`o3DropFailLocalDisable`（测的是 `finishDisableOldCode` 里本站状态同步失败告警，随函数删）。
- `handler/agent_exchange_disable_test.go`：`TestAdminResolveIssuedDisableFailureStaysDisabling`(:93)（`TestAgentExchangeDisableFailureAlertsAndRetries`(:25) 见「改」）。
- `handler/agent_exchange_s3_test.go`：`TestCDKListNoPrefixFallbackForFullCode`(:159)。
- `handler/cdk_session_test.go`：`TestWebhookCompletedClearsSession`(:391)、`TestCardHealthEmailSurvivesClear`(:452)。
- `handler/lookup_results_audit_o3_test.go`：`TestO3ResultsBatchForwardsBoundTokensOnly`(:55)；`handler/lookup_results_audit_s7_test.go`：`TestS7ResultsBatchForwardsBothTokensOfSameCode`。
- `handler/preflight_binding_audit_ob_test.go`：`obNewPreflightUpstream`、`TestObForgedPreflightRejectedUpstreamDoesNotBind`、`TestObPreflightBindsVerifiedEmailNotClaimed`、`TestObPreflightVerifiedEmailBinds`（卡台预检绑定）。
- `handler/local_cdk_test.go`：`TestCrossProviderInflightBlocked`（用 `ZC-` 码，编译期失败；跨服务商互斥有 `TestCrossProviderMutexThreeWay` 覆盖，改写后沿用）。
- `handler/settings_webhook_audit_s1_test.go`：`TestS1SettingsSecretsEncrypted`、`TestS1SettingsSecretsNeedMasterKey`、`TestS1SettingsCardAPIBase`（原型里运行失败）、`TestS1WebhookEncryptedSecretAndMasking`、`TestS1WebhookIdemUsesEventID`、`TestS1WebhookStoreFailureReturns500`、helper `s1Webhook`、`s1WebhookSig`；保留 `TestS1SettingsBrandLength`、`TestS1SettingsWriteFailureReturns500`、`s1Put`。
- `handler/settings_webhook_audit_s6_test.go`：`TestS6SettingsBaseAndKeyWrittenAtomically`（失败）、`TestS6DuplicateWebhookDoesNotRewriteStatus`、`TestS6OrderListNeverReturnsFullCardNumber`（整个文件基本清空）。
- `handler/aftersale_zovo_test.go` 之外：`handler/public_email_mask_audit_ob_test.go` 的 `TestObResultByCodeMasksEmailAndHasNoInvoiceURL`(:75)、`TestObResultByTokenMasksEmail`(:105) 依赖假卡台（原型运行失败），要改写成本站码版本，不能直接丢（邮箱打码是敏感数据保护）。

**改这些用例**（保留意图，换成本站码 / 假上游 `providertest` / 去掉 zovo 桩）：
- 代理换码（动钱，见风险 8）：`agent_exchange_money_test.go` 的 `TestAgentExchangeConcurrentRequestsIssueOnce`(:152)、`TestAgentExchangeConcurrentOnRealFileDB`(:194)、`TestAgentExchangeDefiniteFailureReleasesAndRetries`(:245)、`TestAgentExchangeUnknownOutcomeKeepsReservation`(:299)、`TestAgentExchangeClientDisconnectDoesNotCancelIssue`(:397)、`TestAgentExchangeDailyLimit`(:428)、`TestAdminResolveAgentExchange`(:555)、`TestAdminResolveAgentExchangeIssuedDisablesOldCode`(:626)、`TestOpsMaintenanceEscalatesStaleExchange`(:512)、`TestAgentSwapDailyLimitSetting`(:459)（后两者不依赖卡台，可能原样过，需核对）；`agent_exchange_disable_test.go` 的 `TestAgentExchangeDisableFailureAlertsAndRetries`(:25)；`agent_exchange_s3_test.go` 的 `TestAgentExchangeEmptyPlanRefused`(:119)、`TestAgentExchangeDetailFailureRefused`(:140)（这两条测卡台套餐/订单详情拿不到时拒绝，随 `zovoCardExchange` 删，不改）、`TestAgentSwapShortPasswordFlagAndWrongCount`(:88)。这些测试的共同点是用 `ZC-` 码 + `newExchangeUpstream` 假卡台；原型里全部以 404 `无法识别的卡密` 失败。**改写目标：** 用 `MW-` 码走 `localCDKExchange`，保住「并发只发一张」「失败释放可重试」「日额度」「定论」「10 分钟升级与重试」这几条断言，**不放宽**。helper `exchangeUpstream`/`newExchangeUpstream`/`seedExchange`（用 `SaveCardplatformCDKCode`）/`issueOK` 随之删或换。`adminExchangeRouter`/`adminExchangeDo`(:538,547) 是其他测试共用的 helper，要保留。
- 售后/会话：`handler/cdk_session_test.go` 里依赖假卡台 `cdkUpstream`（:39-95）的 `TestPreflightStoresCiphertextAndForwardsSession`、`TestPreflightWithoutKeyDoesNotFailRedemption`、`TestResultByCodeWorksAfterSessionCleared`、`TestResultByCodeWorksWithoutKey`、`TestResultByCodeClearsSessionOnlyWhenCompleted`、`TestResultByTokenClearsSessionWhenCompleted`、`TestLookupKeepsEmailAndStatusAfterClear`（原型里全部运行失败）：这些测的是卡台转发与会话加密，随转发删；会话加密与清理本身的覆盖在 `db/cdk_session_test.go`，保留。保留且不依赖卡台的：`TestBillingCheckByCDK*`、`TestBillingCheckPastedSessionStillWorks`、`TestOpsMaintenanceClearsAndAlertsOncePerDay`（后者含 `zc-` 字面量，换成别的码）。
- 公开守卫：`handler/public_guard_test.go` 的 `TestPublicBodyTooLarge`(:59)、`TestPublicRateLimitBeforeBody`(:69)（原型失败：用 `ZC-` 码发请求期望非 404）。改用 `MW-` 码。
- `handler/lookup_results_audit_o3_test.go` 的 `TestO3ResultsBatchUnknownTokensNeverHitUpstream`、`TestO3ResultsBatchCardSupplyTokensStayLocal`：去掉 zovo 桩，保留「不认识的令牌回 404、本站令牌只查本库」断言。
- 本站码测试里的 zovo 桩：`handler/local_cdk_env_test.go` 的 `localZovoStub`(:282-345) 及 `newLocalZovoStub`、`setupLocalEnv`、`localEnv.zovo` 字段；`local_cdk_test.go` 的 `TestLocalCDKEndToEnd`、`TestLocalCDKFundedFailuresLimit`、`TestLocalCDKHardGateAlsoGuardsRedeem`、`TestLocalCDKRoutingIsCaseInsensitive`、`TestRedeemRoutesByCDKProvider`(:881 运行失败)、`TestLocalCDKLookup`、`TestLocalCDKRateLimit`、`TestLocalCDKOnlyHashesStored`；`provider_admin_test.go` 的 `TestReadinessBlocksLocalIssue`、`TestLocalIssueValidation`、`TestLocalIssueIdempotency`、`TestLocalExchangeRecordNotStuckInDisabling`(:486，断言 `e.zovo.count()==0`)、`TestAdminListLocalCDKs`、`TestAdminDisableEnableLocalCDK`、`TestAgentExchangeLocalCDK`。这些现在用假卡台来证明「本站码不会发往上游」。去掉 zovo 桩后这条保证改为「没有任何上游适配器」，断言本身要换成 `providertest` 假上游（它还在注册表里）收不到请求。
- 其它含 `zovo`/`ZC-` 字面量只需换字符串：`pay_test.go` 的 `TestPaySubmitProvider`、`cdk_session_test.go`/`local_cdk_test.go` 的常量。
- `handler/grok_issue_test.go` 的 `TestGrokIssueUsesGrokCatalogueAndPrefixedPlan`(:75 失败)：卡台 Grok 发码，改成规格测试项 2（拒绝发码，400 `MarsWallex 不支持 Grok …`）。
- `handler/aftersale_test.go`/`aftersale_scrub_gaps_test.go` 里的 `卡台` 字样只是测试文本，改成中性词即可，不影响逻辑。
- `handler/selfupdate_audit_o3_test.go` 的 `TestO3SeamlessUpdateAbortsWhenCheckFails`：里面 `card_api_key` 是启动检查的明文密钥用例，保留（密钥迁移保留）。

**新增**（规格第 7 节）：认不出的码 404；X/Grok/Claude 无服务商拒绝发码；`/api/v1/webhooks/cardplatform`、`/api/v1/admin/cardplatform/*`、`/api/v1/stats/system` 404；旧换码路径 404、新路径 401；`/api/v1/public/cdk/plans` 404（若按建议删）。

**不动**：`cardpool/*`、`payengine/*`（只有注释里有 `fallback` 字样，与 ZovoCard 无关）、`paybrowser/sandbox_test.go`（`card_api_key` 是环境变量白名单反例，保留）、`local_cdk_sealed_test.go`、`local_cdk_inspect_test.go`、`local_cdk_enable_audit_hb_test.go`、`lookup_test.go`、`aftersale_cardsupply_test.go`、`agent_exchange_lockout_test.go`、`ops_maintenance_test.go`（原型运行通过）。

---

## 4. 删卡台包和数据库代码

### 4.1 谁还 import `cardplatform`、`plansync`

- `internal/cardplatform` 的 import 方（非测试）：`handler/agent_exchange.go`、`card_pref.go`、`grace_recovery.go`、`handlers.go`、`provider_readiness.go`、`lookup.go`、`cardplatform_cdk.go`、`network.go`、`ops_maintenance.go`、`settings.go`、`local_cdk_admin.go`、`provider_guard.go`、`card_selection.go`、`card_health.go`、`agent_order.go`、`cardplatform.go`，以及 `plansync/sync.go`。测试方：`acgshared/acgshared_test.go`、`handler/agent_exchange_money_test.go`、`agent_exchange_region_test.go`、`card_pref_test.go`、`provider_admin_test.go`。任务 1-3 做完后只剩 `plansync` 一个。
- `internal/plansync` 的 import 方：`server/http.go:22,91`（`plansync.Start`）、`handler/card_selection.go:13`。任务 3 后只剩 `server/http.go`。
- 整包删文件：`cardplatform/` 下 `baseurl.go`、`claude.go`、`client.go`(967 行)、`direct_rules.go`、`grok.go`、`sellable.go`、`x_premium.go` 及对应 7 个测试文件（`baseurl_audit_s1_test.go`、`client_issue_test.go`、`direct_rules_test.go`、`grace_recovery_test.go`、`grok_test.go`、`sellable_test.go`、`x_premium_test.go`，其中三个含要先挪的用例，见 1.3）；`plansync/sync.go`(162 行)。

### 4.2 db 层：第 3 步删完后没人用的函数

用 `deadcode` 对比基线得出（原型验证，括号为现在的行号）。

`db/db.go`：`InsertWebhookEvent`(939)、`ListWebhookEvents`(950)、`BindCDKRedemptionToken`(978)、`recordRedemptionToken`(1005)、`FindCodeByRedemptionToken`(1025)、`KnownRedemptionTokens`(1047，要等 lookup_results 改完才孤立)、`SaveCardplatformCDKCode`(1143)、`SaveCardplatformCDKCodeWithStatus`(1148)、`UpdateCardplatformCDKStatus`(1189)、`GetCardplatformCDKStatus`(1202)、`CountCardplatformCDKCodes`(1215)、`ListCardplatformStoredCDKCodes`(1238，基线已是死的)、`cardplatformStoredWhere`(1242)、`CountCardplatformStoredCDKCodesFilter`(1272)、`ListCardplatformStoredCDKCodesFilter`(1283，基线已死)、`ListCardplatformStoredCDKCodesPage`(1291)、`LookupCardplatformCDKCode`(1341)、`CardplatformCDKCodeByUpstreamID`(1365)、`SetCardplatformCDKNote`(1385)、`ClearCardplatformCDKNotes`(1411)、`BatchSetCardplatformCDKNotes`(1432)、`GetCardplatformCDKNote`(1455)、`MapCardplatformCDKNotes`(1465)、`LookupStoredCDKByCode`(1522)、`GetCardSelectionRules`(1566)、`SetCardSelectionRules`(1595)、`GetPlanStatusCache`(1628)、`GetPlanStatusCacheMap`(1657，基线已死)、`UpsertPlanStatus`(1670)、`UpsertCardProduct`(1707)、`PruneCardProductsExcept`(1746)、`GetCardProducts`(1785)，以及配套类型 `WebhookEvent`、`StoredCDKCode`、`CardSelectionRule`、`PlanStatusCache`、`CardProductCache`（用编译器确认）。

`db/card_health.go`（整文件除建表外全是死代码）：`InsertCardFailEvent`(48)、`GetCardFailStats`(84)、`ListCardFailEvents`(123)、`IsCardBlocked`(154)、`UpsertCardBlock`(167)、`UpdateCardBlockFreeze`(202，基线已死)、`UnblockCard`(215)、`ListCardBlocklist`(230)、`ListActiveBlockedCardIDs`(268)、`DefaultCardHealthPolicy`(297)，及类型 `CardFailEvent`、`CardFailStats`、`CardBlockEntry`、`CardHealthPolicy`。整文件可删。

`db/card_product_direct.go`：`UpsertDirectCardProduct`(8)；整文件可删。

`db/cdk_region.go`：`LookupStoredCDKRegion`(38)、`UpdateCardplatformCDKMetadata`(27)、`UpdateCardplatformCDKRegion`(22)；`migrateCardplatformCDKRegionCol`(10) **保留**（建表迁移，规格 3.2）。

`db/cdk_session.go`：`BindCDKSession`(97，基线已死)、`BindCDKSessionAs`(103)、`GetSessionByCDK`(173，基线已死)、`GetBindingByCDK`(227)、`ClearCDKSessionByCode`(285)、`ClearCDKSessionByToken`(306)、`openStoredSession`(68)。（`CDKBinding` 类型随之。）其余（`SweepCDKSessions`、`HasRecentCDKBindingForEmail`、`GetAccountEmailByRedemptionToken`、加密迁移）保留，见风险 7。

`db/card_secrets.go`：`GetCardSecret`(29)、`SetSettingsWithSecrets`(229)、`SensitiveCardFieldDrop`(75)、`SensitiveCardFieldPAN`(78)。**保留** `MigrateCardSecrets`(45)、`legacyPlainCardSecret`(22)、`SanitizeWebhookPayload`(136)、`MigrateWebhookPayloads(Batched)`(155,161)（见 4.4）。`SettingCardAPIKey`/`SettingWebhookSecret`/`cardSecretKeys`(14-18) 保留（迁移用）。

`db/agent_exchange.go`：`GetAgentExchangeByOldHash`(304，只被 `zovoCardFacts` 用)；`TouchAgentCDKExchange`(231，取决于 3.3 b 的决定)。其余全部保留（本站码换码在用）。

### 4.3 建表语句里只给 ZovoCard 用的表（保留建表，加注释「旧 ZovoCard 数据，代码已不读写，留表以兼容旧库」）

`db/db.go` 的 `CREATE TABLE`：
- `cd_keys`(:123)——只剩迁移 `db.go:1089,1098,1124` 在读写（保留）；
- `cardplatform_cdk_codes`(:137，索引 :147-148)——**注意**：仍被 `SweepCDKSessions`（`db/cdk_session.go:366-370`）读，且 `cdk_session.go:570` 建索引 `idx_cp_cdk_code_norm`；
- `cardplatform_cdk_notes`(:151)；
- `recharge_tasks`(:157)——迁移 `db.go:1127` 与 `db/local_cdk.go:253-270 clearRechargeTaskSessions` 在用；
- `billing_records`(:174)——基线就没有任何代码引用；
- `webhook_events`(:219)——`MigrateWebhookPayloads`(`card_secrets.go:174,207`) 仍在清洗；
- `cdk_redemption_tokens`(:237)——只剩回填迁移（`TestS7MigrateBackfillsTokensRepeatably`）；
- `card_selection_rules`(:260)、`plan_status_cache`(:272)、`card_fail_events`(:281)、`card_blocklist`(:299)、`card_product_cache`(:314)。

**不是** ZovoCard 专用、要保留且不加注释：`agent_cdk_exchanges`（本站码换码在用）、`cdk_session_bindings`（跨服务商互斥 `HasRecentCDKBindingForEmail` 和本站码流程在读，`db/local_cdk.go:667,709`）、`provider_card_heads`（MarsWallex 卡头）。

### 4.4 启动检查

- `db/startup_check.go`：四个函数 `CheckPath`(22)、`OpenReadOnly`(31)、`CheckCardSecretsMigratable`(58)、`IsMissingSchemaErr`(76)，**全部保留**。其中只有 `CheckCardSecretsMigratable` 与卡台有关，它是「旧明文卡台密钥必须能加密迁移」的检查，属密钥迁移，保留。
- `server/startup_check.go`：`startupChecks`(21-58)：
  - :32 默认管理员口令检查 —— 通用，保留；
  - :35-43 「卡台密钥迁移检查」—— 密钥迁移，保留（文字里的「卡台」可改成「旧版密钥」，错误前缀 `卡台密钥迁移:` 有测试 `startup_check_audit_o3_test.go:111,288` 断言，改文案要同步）；
  - :44-55 上游密钥检查 → `upstreamKeysConfigured`（`server/http.go:157-170`）里把 `"card_api_key"` 也算「动钱上游」；这是安全检查（有动钱密钥就强制 JWT_SECRET），不是卡台业务。**建议保留**（见风险 7），只改 :157 注释和 :200 报错文案里的「卡台 API Key」；
  - :56-57 `CheckProviderAdapters` —— 通用，保留（没有上游适配器时恒通过）。
  - 没有任何「卡台业务检查」可删。规格 5.4「启动检查里只为卡台业务存在的检查删掉」实际无可删项，只有文案要改。
- `server/http.go:46-52`：`db.MigrateCardSecrets()`、`db.MigrateWebhookPayloads()` 两处启动迁移**保留**（规格只提了第一个；第二个是 webhook 原文里卡号/CVV 的清洗，同属敏感数据保护，见风险 7）。

### 4.5 「老库带旧表旧数据再启动」现成的测试工具

- `server` 包内：`o3NewDBFile(t, seed func(*sql.DB)) string`（`server/startup_check_audit_o3_test.go:37`）——在临时目录建一个**完整建好表**的库文件，让 `seed` 往任意表（含 `cd_keys`、`cardplatform_cdk_codes`、`webhook_events`、`site_settings` 里的 `card_api_key`/`cdk_default_provider='zovocard'`）塞旧数据，并设好 `DB_PATH`；配套 `o3Env(t)`（:25）、`o3Exec`、`o3Check(mode)`（跑 `Check()`，即只读的 `startupChecks`）、`o3Setting`、`o3FileSum`。用它可以写「旧库 `--check` 通过」；要走真正启动里的 `db.Migrate()` + `MigrateCardSecrets` + `MigrateWebhookPayloads`，再对同一文件开连接调 `db.Migrate()`。
- `db` 包内：`dbtest.Open(t)`（`db/dbtest/dbtest.go`，内存库、跑全部建表）、`dbtest.SetMasterKey(t)`；`openFileDB(t)`（`db/agent_exchange_test.go:23`）建文件库；旧结构 DDL 手写的先例见 `db/provider_migration_test.go:43 TestMigrateProviderColumnsOnLegacyDB`（用 `legacyPayOrdersDDL`/`legacyBindingsDDL` 先建旧表再调 `Migrate`）和 `db/cdk_session_test.go` 的 `TestMigrateUpgradesOldSchema`。
- 没有直接启动整个 `server.New` 的测试；`New`（`http.go:36`）会起卡池和付款引擎，不建议在测试里整体跑。

### 4.6 测试文件（任务 4）

| 文件 | 动作 |
|---|---|
| `cardplatform/*_test.go`（7 个） | 整文件删（先挪 1.3 的 3 个用例） |
| `db/agent_exchange_test.go` | 删 `TestGetAgentExchangeByOldHash`(:363)、`TestCardplatformCDKCodeByUpstreamID`(:387)；其余（`TestReserveAgentExchange*`、`TestEscalateStaleAgentExchanges`、`TestResolveAgentExchangeOnlyFromUnknown`、`TestMigrateAgentExchanges*`）不动，里面的 `zc-` 字面量可保留（只是码字符串） |
| `db/cdk_region_test.go` | 整文件删（`TestCDKRegionMigrationAndCache`、`TestLookupStoredCDKRegionDistinguishesUnknown` 测的都是 `SaveCardplatformCDKCode`/`LookupStoredCDKRegion`）；但迁移 `migrateCardplatformCDKRegionCol` 的幂等性要在别处留一条断言（可并入新的老库升级测试） |
| `db/cdk_session_test.go` | 逐用例核对：用到 `BindCDKSessionAs`/`ClearCDKSessionBy*`/`GetBindingByCDK` 的（`TestCDKSessionNotStoredInPlaintext`、`TestBindCDKSessionRefusesWithoutMasterKey`、`TestBindCDKSessionByTokenIsSealed`、`TestGetBindingSurvivesMissingKey`、`TestPreviewDoesNotExtendSessionTTL`、`TestClearDoesNotOverwriteNewerSession`、`TestClearByCodeAndToken`）随函数删；`TestMigrate*`、`TestSweep*` 保留（`TestSweepClearsConsumedCodes` 用 `SaveCardplatformCDKCode` 造数，函数删后改成直接 `INSERT`，因为 Sweep 仍读该表）。原型里这些测试因函数没真删所以仍通过，真删时才会暴露 |
| `db/cdk_session_audit_h4_test.go`、`db/redemption_tokens_audit_s7_test.go` | 同上：`TestH4BindSessionTokenOnlyRecordsToken` 依赖 `BindCDKSessionAs`；`TestS7RepeatedPreviewKeepsOldToken` 依赖 `BindCDKRedemptionToken`（删）；`TestS7MigrateBackfillsTokensRepeatably`（迁移）保留 |
| `db/local_cdk_test.go` | `TestRecentCDKBindingForEmail` 用了 `BindCDKSessionAs`，改成直接 `INSERT` |
| `db/provider_migration_test.go` | 不动（`ZC-OLD-1`、`zovocard` 是迁移测试的历史数据，规格 3.2 要求迁移保留） |
| `db/card_secrets_audit_s1_test.go` | `TestS1MigrateCardSecrets` 不动（密钥迁移保留） |
| `server/startup_check_audit_o3_test.go`、`startup_check_audit_s7_test.go` | 不动（密钥迁移与 `--check`） |
| 新增 | 老库升级测试（规格测试项 8），用 4.5 的工具 |

---

## 5. 前端

### 5.1 整文件删

`views/admin/CardIntegration.vue`、`CardSelectionConfig.vue`、`OrderReconcile.vue`、`WebhookEvents.vue`。**这 4 个页面没有各自的测试文件**（`views/admin/__tests__/` 下只有 `pay.spec.ts`、`providers.spec.ts`、`site-appearance.spec.ts`），规格 5.5「删 4 个页面及其测试」里的「测试」是指下面第 5.5 小节的断言。

### 5.2 要改的文件逐处

| 文件 | 行 | 动作 |
|---|---|---|
| `router/routes.ts` | 82-85 | 删 4 条路由（`integration`/`CardIntegration`、`card-selection`/`CardSelectionConfig`、`orders`/`OrderReconcile`、`webhooks`/`Webhooks`）；同处加 4 条重定向 `/ops/integration`、`/ops/card-selection`、`/ops/orders`、`/ops/webhooks` → `/ops/providers`（现有写法 `{ path: 'xxx', redirect: \`${OPS_BASE}/yyy\` }`） |
| `router/routes.ts` | 104, 105, 108 | 旧路径兼容里 `billing`、`reconcile` 指向 `${OPS_BASE}/orders`，`cards` 指向 `${OPS_BASE}/integration`：这些目标页面没了，改成直接指向 `${OPS_BASE}/providers`（规格没提） |
| `lib/adminNav.ts` | 2-3 | 只是注释里的例子「ZovoCard 选卡规则」，改成 MarsWallex 的例子；逻辑不动 |
| `layouts/AdminLayout.vue` | 149 | `zovoEnv` 从 import 去掉 |
| `layouts/AdminLayout.vue` | 175-195 | **规格没提**：顶栏环境徽章 `loadEnv` 里 :179 `let zovo = unknown('卡台')`、:181-184 读 `/api/v1/admin/settings` 的 `card_api_base`/`card_api_key_configured`、:193 `envBadges.value = [zovo, mars]`。后端不再返回这两个字段，徽章会永远显示「卡台·未配置/未知」，必须删，只留 MarsWallex 徽章 |
| 菜单项 | （由 routes.ts 生成） | 删路由即菜单消失；分组「ZovoCard」整组消失，侧栏分组由 4 变 3、菜单项由 15 变 11 |
| `lib/adminui.ts` | 57-63 | 删 `zovoEnv`；`EnvBadge` 类型保留给 `marsEnv` |
| `views/admin/CDKeyManagement.vue` | 1587 行里几乎全部 | 规格说「去掉 `codeListKind === 'cardplatform'` 那一类及其请求」，实际是整个文件 90% 以上都在这一类里：模板 :5-15（API 已配置标签、出口 IP、余额、价格来源）、:32（「卡台配置」链接）、:45-334（整个卡台分支，含购买发码表单、`stored`/`upstream` 两种列表、批量启停、备注、同步完整码）；脚本 :373 起 `codeListKind`、:391 `isFullCode` 注释、:407-1480 的 loadMeta/loadList/syncFromCardplatform/issue/备注/批量/导出等全部、:1273 `sharedKeeper('cardplatform-issue')`、:1436-1580 样式（`.fold-head`、`.plan-card-sm`、`.sel-bar`、`.code-cell`、`.note-cell` 等）。保留：页头「代理换码」弹窗、`el-tabs`（卡密/代理换码）、`IssueProviderBar`、`LocalCdkPanel`、`AgentSwapSettings`、`AgentExchangePanel`、`no-code-list` 兜底。基本是重写成 ~120 行 |
| `views/admin/CDKeyManagement.vue` | 请求 | 调用的将被删后端接口见 5.4 |
| `views/admin/AdminDashboard.vue` | 3 | 描述文字「卡台发码、……」改成中性 |
| `views/admin/AdminDashboard.vue` | 5-8 | 删 `statsError`、`counts_partial` 提示（「总数仍以卡台 total 为准」） |
| `views/admin/AdminDashboard.vue` | 10-16, 92-101 | 删 `statCards`（**4 格**，不是规格说的 3 格：CDK 总数、可用 CDK、**已兑换（`consumed_cdks`，同样来自 `/stats/system`）**、兑换订单）及其模板网格。D5 的「3 格」要更正为 4 格，否则「已兑换」会永远显示 0/— |
| `views/admin/AdminDashboard.vue` | 80-90 `stats`/`statsError`/`statsLoading`/`numOrDash` | 删；`numOrDash` 若无他用一并删 |
| `views/admin/AdminDashboard.vue` | 166-186 | `onMounted` 里 :166-185 请求 `/api/v1/stats/system` 的 try/catch 删；:186- 的版本信息请求保留 |
| `views/admin/AdminDashboard.vue` | 其余 | 待办汇总（`loadTodos`，MarsWallex）、快捷入口、版本信息不动 |
| `views/admin/components/AgentExchangePanel.vue` | 8, 58, 62-63, 66, 70, 132, 134-135, 149 | 去「卡台」字样：说明文字、「先到卡台核对」、「卡台没发码（释放，用户可重试）」「卡台已发码（禁用旧码）」「卡台上新码的 ID」、备注提示、确认文案；:104 `'/cardplatform/agent-exchanges'` → `'/agent-exchanges'`；:138 同理 + `/resolve`；:148-149 `old_disabled === false` 的 toast（「系统会每 10 分钟重试，请到卡台手动禁用旧码」）——本站码没有「手动去卡台禁用」，此分支改写或删，要和后端 `old_disabled` 字段一起定 |
| `views/admin/components/AgentSwapSettings.vue` | 28, 94 | 「每换一张都会向卡台买一张新码」对本站码不成立（本站码换码不花钱买卡），文案改掉（规格没提） |
| `views/admin/CardProviders.vue` | 5-9 | 删 :7 的 `ZovoCard 选卡规则` 链接，提示语改成只指向 MarsWallex 选卡规则 |
| `lib/errors.ts` | 26, 30-31 | 删 `CARD_CFG` 与两条 `card_api_key/card_api_base not configured` 规则；`MW_CFG` 保留 |
| `lib/errors.ts` | 62-73 | **保留**（审计动作中文名，规格 3.2）；`AuditLogs.vue:106-109` 颜色同样保留 |
| `lib/provider.ts` | 12-13, 84 | `CodeListKind` 去掉 `'cardplatform'`；`providerCodeList` 只认 `'local'` |
| `lib/provider.ts` | 221-222 | 删 `CHECK_HINTS` 里 `api_key`、`balance` 两项（「卡台 API Key」「卡台余额」） |
| `lib/provider.ts` | 226-229 | `SELECTION_PAGE` 删 `zovocard` 一项 |
| `lib/provider.ts` | 364 | `DISABLING_HINT`「请到卡台手动禁用旧码」改写 |
| `lib/session.ts` | 92-100 | :100 `if (/\/api\/v1\/admin\/cardplatform\//i.test(url)) return false` 删（接口没了）；:92、:96、:99 注释里的卡台去掉。对应测试见 5.5 |
| `lib/secretError.ts` | 1 | 注释「卡台密钥」；检查是否还有使用方（原 CardIntegration/WebhookEvents 用），无人用则整文件删 |
| `theme.ts` | 61-62, 65 | D4：`label: '卡台新版'` → `'曜夜雾金'`，`labelEn: 'ZovoCard'` → `'Night Gold'`，`blurb` 里「与卡台新版一致」改掉；:6、:60 的 `'zovo'` 编号和 :218、:229 的默认值不动；:46、:59 注释的「卡台新版」改中性 |
| `lib/claude.ts:3,7`、`lib/grok.ts:3,9`、`lib/plan.ts:11-12,22,82`、`lib/idempotency.ts:1`、`views/user/BatchRedeemView.vue:932` | 注释 | 只改注释，不改逻辑 |
| `style.css`、`styles/skins.css`、`frontend/index.html:20-23` | | 皮肤编号 `zovo`，规格 3.3 不动；注释里「卡台新版」可改（index.html 的注释也含 zovo，最终 grep 要列入白名单） |

### 5.3 `provider.ts` 以外前端代码对 `zovocard` 字符串的使用

没有逻辑依赖：前端所有服务商相关逻辑都按注册表数据走（`registry`、`code_list`、`effective`）。`SELECTION_PAGE`、`CHECK_HINTS` 是仅有的两处硬编码，见上表。

### 5.4 前端调用了哪些将被删的后端接口（逐个）

- `CardIntegration.vue`：GET `/api/v1/admin/network/egress`(262)；GET/PUT `/api/v1/admin/settings` 的卡台字段(271, 289，`settings` 接口本身保留)；GET `/api/v1/admin/cardplatform/ping`(307)、`/plans`(331)、`/balance`(350)。
- `CardSelectionConfig.vue`：GET/PUT `/admin/card-selection/rules`(473, 521)；GET `/admin/card-selection/plan-status`(486)；POST `/admin/card-selection/sync`(497)；GET/PUT `/admin/card-selection/site-policy`(557, 585)；GET `/admin/card-health`(636)；PUT `/admin/card-health/policy`(656)；POST `/admin/card-health/unblock`(673)。
- `OrderReconcile.vue`：GET `/admin/cardplatform/cdk-orders`(289)、`/cdk-orders/:id`(321)；DELETE `/admin/cardplatform/cards/:id`(345)。
- `WebhookEvents.vue`：GET `/admin/webhooks/events`(119)；PUT `/admin/settings`(142，写 `webhook_secret`)。
- `CDKeyManagement.vue`：GET `/admin/cardplatform/cdks/stored`(658, 693, 1394)；POST `.../cdks/:id/disable`(727)、`/cdks/batch-disable`(753)、`/cdks/:id/enable`(784)、`/cdks/batch-enable`(810)；PUT `.../cdks/:id/note`(869)；POST `.../cdks/batch-note`(910)、`/cdks/batch-clear-note`(943)、`/cdks/store`(1107)、`/cdks/sync`(1130)；GET `.../plans`(1212)、`/balance`(1213)；GET `/admin/network/egress`(1214)；GET `/admin/settings`(1215，读卡台字段)；POST `/admin/cardplatform/cdks`(1311，卡台发码)；GET `/admin/cardplatform/cdks`(1419)。
- `AdminDashboard.vue`：GET `/api/v1/stats/system`(175)。
- `AdminLayout.vue`：GET `/api/v1/admin/settings` 的 `card_api_base`/`card_api_key_configured`(181-183)。
- `AgentExchangePanel.vue`：GET `/admin/cardplatform/agent-exchanges`(104)、POST `.../agent-exchanges/:id/resolve`(138)——后端接口不是删而是改路径。
- 前端没有调用、但后端要删的：`POST /api/v1/webhooks/cardplatform`（卡台自己推）、`GET /admin/card-health/observe`（POST，无页面）、`GET /api/v1/public/cdk/plans`。
- 仍在调用且保留的：`POST /api/v1/public/cdk/recover-subscription`（`composables/useRedeemFlow.ts:740`）、`/public/cdk/{preview,preflight,redeem,result,result-by-code,exchange}`、`/lookup/*`、`/admin/cdks/issue`（`LocalCdkPanel.vue:315`）、`/admin/settings`（`SiteAppearance.vue`、`AgentSwapSettings.vue`）。

### 5.5 前端测试（任务 5）

| 文件 | 动作 |
|---|---|
| `layouts/__tests__/AdminLayout.spec.ts` | 改：路由桩里删 :39-42 四条；:72-74 菜单项数 15→11、分组数 4→3、分组名列表去掉 `'ZovoCard'`；:86-92「同名菜单项用完整页名区分」去掉对 `aria-label="ZovoCard 选卡规则"` 的断言（现在只有一个「选卡规则」，不再重名，保留对 MarsWallex 的 aria-label/面包屑断言）；:128、:129 分组顺序数组和 `gs[3]`→`gs[2]` 下标 |
| `lib/__tests__/adminui.test.ts` | 删 :3 的 `zovoEnv` import 和 :40-51 两个用例（卡台生产/沙盒/自定义、没有 Key 显示未配置）；`marsEnv` 用例保留 |
| `lib/__tests__/errors.test.ts` | 删 :5-10 「卡台未配置 Key」用例（断言 `/ops/integration` 链接） |
| `lib/__tests__/provider.test.ts` | :89 `selectionPage('zovocard')` 断言改成 `undefined`；:83 `swapFailure(502, { error: '卡台不可用' })` 只是字符串，换成中性 |
| `lib/__tests__/session.s6.test.ts`、`session.audit_oa.test.ts` | 删 `/api/v1/admin/cardplatform/*` 的 URL 断言（s6 :5-15 三个用例改写成别的 URL 或删；audit_oa :200-201 删）；:241 的 `/ops/orders?q=…` 只是路径字符串用例，换个路径避免最终 grep |
| `lib/__tests__/registryFixture.ts` | `zovocard` 作为假上游出现在 :1,4,11,13,16-18,23,26,28 与 `卡台余额`：改名成别的假上游（如 `testup`），否则最终 grep 命中 |
| `lib/__tests__/registry.test.ts` | :22,35,43-83,101-139,152,60-63,68-75,87 全是用 `zovocard` 当「MarsWallex 不支持时改用的上游」。逻辑（`pickIssueProvider` 的改选提示）保留，只换名字；其中断言文案「已改用 ZovoCard」(:48, :63, :83, :102, :140) 同步换 |
| `lib/__tests__/credit-renewal.test.ts` | :38 `pickPayProvider(list, 'zovocard')` 换名 |
| `lib/__tests__/claude.test.ts` | :52 用例名含「卡台」，只是描述，换词 |
| `views/admin/__tests__/providers.spec.ts` | :55,61,79,88,105,108,114,115,118 的 `zovocard`/`ZovoCard`/`卡台余额` 换成假上游 |
| `views/admin/__tests__/pay.spec.ts` | :113 `?provider=zovocard` 换名 |
| `components/redeem/__tests__/*.spec.ts`、`i18n/__tests__/locale-keys.test.ts`、`views/__tests__/{banned-words,home,recharge}.spec.ts` | 里面的 `'卡台'` 是「用户界面禁用词」列表，**不要动**，也要进最终 grep 白名单 |
| 新增 | 4 个旧地址重定向到 `/ops/providers`；菜单没有这 4 项；总览没有统计卡；皮肤显示名改了、编号 `zovo` 没改（`siteAppearance.test.ts` 或 `theme` 相关测试） |
| 不动 | `lib/__tests__/features.test.ts`、`pay*.test.ts`、`mw*.test.ts`、`cardheads.test.ts`、`cardpool*.test.ts`、`homeRoute.test.ts` 等其余 |

---

## 6. 文档：规格 5.6 与实际内容核对

| 规格说的 | 实际 | 出入 |
|---|---|---|
| 整篇删 `docs/zovocard-cdk-zh.md`、`zovocard-openapi-zh.md`、`卡台CDK接入准备-2026-07-28.md`、`卡台CDK测补与兑换进度-2026-07-28.md`、`卡台回调配合需求-给负责人-2026-07-28.md` | 5 个文件都存在，内容都是 ZovoCard | 相符 |
| `README.md`「卡台官网」一行 | `README.md:9` 一行 `\| **卡台官网** \| https://zovocard.com \|` | 相符（只此一处） |
| `docs/UI-UPGRADE.md` 删 ZovoCard 相关段落 | 全文 126 行，提到的是**另一个项目** `cardplatform`（被参考的 UI 做法，:3,22,28,39,110,120）和「卡台 Open API / 卡台卡列表」（:9,48,97-98,126），不是 ZovoCard 接入 | 有出入：它是历史 UI 升级计划，几乎每段都提卡台，按「段落」删会删得七零八落。建议要么整篇归入历史记录（加白名单），要么整篇删 |
| `docs/cdk-list-region.md` 删 ZovoCard 相关段落 | 全文只有 13 行，全是「卡台状态列表」「`cardplatform_cdk_codes` 加 `payment_country` 列」 | 有出入：整篇只讲被删功能，应**整篇删**而不是删段落 |
| `docs/x-premium-subscriptions.md` 删段落 | 全文 9 行，核心句「X 目录只读取卡台 `plans?product=x` 返回的已开放套餐」「依赖卡台/ACC 配套版本」 | 有出入：整篇描述卡台 X 目录，建议整篇删或重写（X 码目前没有服务商） |
| `docs/开源自托管进度-2026-07-28.md` 删段落 | 提到卡台的位置：:5,12,41,77,167,357（范围、卖点、API Key 保险箱、对接配置表、历史记录） | 相符，但 :12「我们卖的是……卡台 Open API 对接」是整篇的立论，建议改写而不是只删一行 |
| `docs/marswallex-openapi-zh.md` 只动提到卡台的那两处 | `:1` 标题「MarsWallex 卡台 API 文档」、`:3`「本项目默认卡台 API 文档」。这里的「卡台」是泛指（MarsWallex 自己），不是 ZovoCard | 相符（两处）；但这是 MarsWallex 的「卡台」，最终 grep 的「卡台」会命中，改成「卡服务」 |
| `docs/known-issues.md` 只针对 ZovoCard 的条目标为已随删除 | 整条 ZovoCard 专用的：:60、:93、:213（money N-2，卡台预检邮箱）、:233（C-8 选卡优先级同步）、:236（卡台发码结果不明重放）、:237（`cardplatform/client.go` SyncUpstreamFullCodes）、:240（旧 http:// 卡台地址）；部分涉及的：:65（旧叫法清单里只有 `CDKeyManagement.vue`、`CardSelectionConfig.vue` 等是卡台文件）、:116（`zovoSetResult` 测试 helper）、:61-64 的卡头条目里只有 :60 是 ZovoCard | 相符；注意 :213 的 money N-2 是「上线前核对」栏里的条目（记忆里也列为上线前核对），标记后上线清单会少一条 |
| `docs/推送教本.md`、`scripts/push.sh` 不动 | 属实 | 相符 |
| `CHANGELOG.md`「未发布」写升级注意 | `CHANGELOG.md:9 ## 未发布`、`:11 ### 升级注意` 已存在 | 相符，直接往里加 |
| （规格没列）其它含 zovo/cardplatform/卡台 的非历史文件 | `deploy/docker-deploy.sh:222`（装完提示「设置管理员账号和卡台 API Key」，用户可见）；`.gitleaks.toml:31`（注释「假卡台 API Key」）；`.push.env.example:4-17`（push 身份，同 `push.sh`，应归 3.3 白名单）；`automation/README.md:38,43,45`、`automation/lib/card.js:4`（指的是另一个叫 `cardplatform` 的取卡项目，不是 ZovoCard 接入）；`frontend/index.html:20,23`（皮肤 `zovo`）；`backend/internal/payengine/builtin.go:7`、`payengine/config.go:33`、`acgshared/money.go:22`、`gptcheck/check.go:2` 注释 | 有出入：最终 grep 会命中，需决定改或进白名单 |

---

## 7. 风险与疑点

按重要性排序。每条带证据。

1. **规格漏挪的共用件，不补会编译失败。** `IsXPremiumPlan`/`IsGrokPlan`/`IsClaudePlan`（`cardplatform/x_premium.go:19`、`grok.go:15`、`claude.go:15`）被 `issueProduct`（`handler/cardplatform_cdk.go:103-119`）用，而 `issueProduct` 被保留的发码入口 `CardPlatformIssueCDKs`（`handler/local_cdk_admin.go:132`）和 `localCDKIssue`（`:201`）调用；`str`（22 处）、`toString`、`deviceFrom`（`lookup.go:62,104`）也在要删的 `cardplatform_cdk.go` 里；`maskEmailForAdmin`、`keepHead`、`adminName` 在要删的 `card_health.go:521-558`，`adminName` 被 `cardpool.go`、`agent_admin.go:362`、`local_cdk_admin.go:246` 用。原型验证：只按规格挪三个小件，`go build` 报 `undefined: str`、`issueProduct`、`deviceFrom`。
2. **本站码换码确实会产生 `unknown` 和 `disabling`，规格 4.6 的条件成立。** 证据见 3.3 a：`db/agent_exchange.go:216-222` 的 `FinishAgentCDKExchange` 直接置 `disabling`；`local_cdk.go:978-984` 写库失败的告警文字就是「请到后台定论」；`ops_maintenance.go:90-97` 与 `provider_admin_test.go:486 TestLocalExchangeRecordNotStuckInDisabling` 证明 `disabling` 对本站码是真实状态。因此定论表单、`AdminResolveAgentExchange`、`retryAgentExchangeDisables` 的本地分支都必须保留。
3. **全局默认存着 zovocard 时，`Default` 变成空串，提示文案坏掉，发码返回 500。** `provider/config.go:92,97`：`"…已不存在，按 %s 处理"` 的 `%s` 为空；`ResolveIssue`（`config.go:244-247`）返回 `ErrUnknown`，`CardPlatformIssueCDKs`（`local_cdk_admin.go:152-157`）回 500「读取发码服务商配置失败」。另一条静默路径：`cdk_default_provider` 为空而 `cdk_provider_by_product` 有值时（`config.go:83,100-102`）`Default=""` 且没有任何警告。
4. **代理换码的动钱测试几乎全部建在假卡台上，不能直接删。** `agent_exchange_money_test.go`（并发只发一张 :152/:194、失败释放重试 :245、结果不明保留占位 :299、日额度 :428、定论 :555）、`agent_exchange_disable_test.go:25`、`agent_exchange_s3_test.go`，原型里全部因 404 `无法识别的卡密` 失败。规格 6、7 要求这些限额/占位/并发保证「数值不变、不放宽」。本站码路径上现有的覆盖只有 `TestAgentExchangeLocalCDK`、`TestLocalExchangeRecordNotStuckInDisabling` 等零散几条，需要先核对是否有「本站码并发换码只发一张」「日额度」「结果不明」的等价用例，没有就要改写补上，再删旧的。
5. **历史 ZovoCard 换码行没有人收尾。** `old_upstream_id>0` 的 `disabling`/`unknown` 行，删代码后：`retryAgentExchangeDisables` 只处理 `OldUpstreamID<=0`，其余永远停留；`escalateAgentExchanges`（`ops_maintenance.go:122-129`）的「还有 N 笔结果不明」每天告警会一直响，直到管理员逐条定论。要用户决定：是否在循环里把这类行统一转 `done`（写审计）。
6. **总览是 4 格不是 3 格。** `AdminDashboard.vue:96-100` 的 `statCards` 有 CDK 总数、可用 CDK、**已兑换**、兑换订单，四格全来自 `/api/v1/stats/system`。D5 只说 3 格，要更正，否则「已兑换」格残留。
7. **D1「新代码不再读写」与三处保留项冲突，建议保留这三处。** （a）`SweepCDKSessions`（`db/cdk_session.go:366-370`）仍 `EXISTS (SELECT 1 FROM cardplatform_cdk_codes …)` 来提前清用户会话——这是清敏感会话，删掉会让老绑定的会话多留到 TTL；（b）`server/http.go:50 db.MigrateWebhookPayloads` 清洗 `webhook_events` 里的卡号/CVV，规格只提了 `MigrateCardSecrets`；（c）`upstreamKeysConfigured`（`http.go:160`）把 `card_api_key` 算作动钱密钥，测试 `startup_check_audit_o3_test.go:160` 依赖它。这三处都读旧数据，规格需明确放行。
8. **顶栏「卡台·未配置」环境徽章规格没提。** `AdminLayout.vue:149,176-195` 读 `/admin/settings` 的 `card_api_base`/`card_api_key_configured`，后端删掉这两个字段后会永远显示「卡台·未配置」；`zovoEnv`（`lib/adminui.ts:57-63`）及其测试要一起删。
9. **`GET /api/v1/public/cdk/plans`（`PublicCDKPlans`，`cardplatform_cdk.go:1371`，路由 `http.go:277`）规格没列。** 全部数据来自卡台，前端无调用。不删的话 `cardplatform` 包删不掉；建议删并写进更新说明（公开接口，可能有外部使用者）。
10. **`CDKeyManagement.vue` 被低估。** 规格写「去掉 `codeListKind === 'cardplatform'` 那一类及其请求」，实际 1587 行里只有页头和 3 个子组件挂载点是保留的，基本是重写，且这个页面同时挂着「代理换码」标签（`AgentSwapSettings` + `AgentExchangePanel`）。需要真实页面验证（买码、代理换码两个标签都要点一遍）。
11. **老 ZovoCard 码的用户可见行为变化比规格第 4 节写的多。** 除了「认不出的码」（4.1），还有：售后查询单条 404「未找到该卡密记录」（`aftersale.go:137-140`、`lookup.go:63-72`）；批量兑换/结果页 404；`routeBody` 空请求体由「兜底上游报错」变 404 `无法识别的卡密`（`provider_adapter.go:312-314`）。更新说明要告诉管理员「已发出但没兑换的 ZovoCard 码从此不能兑换、不能查」。
12. **`AdminPutSettings` 静默忽略旧字段。** 旧前端缓存页面提交 `card_api_key`/`webhook_secret`/`card_api_base` 时，`ShouldBindJSON` 不报错、也不保存，返回 200（原型里 `TestS1SettingsSecretsEncrypted` 失败的原因）。规格 6 说"删掉的接口不留兼容空壳"，这里是字段被忽略而不是接口，行为可接受，但 `AdminGetSettings` 不再返回 `card_api_key_configured` 等，旧缓存的 AdminLayout 会显示「未知」。
13. **规格 5.4 说「启动检查里只为卡台业务存在的检查删掉」，实际没有可删项。** `db/startup_check.go` 全部是密钥迁移/只读打开/缺表容忍；`server/startup_check.go` 只有文案含「卡台」。只需要改文案，而 `startup_check_audit_o3_test.go:111,288` 断言了「卡台密钥迁移」「card_api_key」字样，改文案要同步改测试。
14. **前端测试里大量把 `zovocard` 当「假上游」用。** `registryFixture.ts`、`registry.test.ts`、`providers.spec.ts`、`pay.spec.ts:113`、`credit-renewal.test.ts:38`：逻辑不依赖真服务商，但最终全仓库 grep（规格第 7 节）会命中，要整体换名；另有一批 `'卡台'` 是「用户界面禁用词」列表（`credential.spec.ts:31`、`confirm.spec.ts:20`、`progress-result.spec.ts:24`、`locale-keys.test.ts:11`、`banned-words.test.ts:11`、`home.spec.ts:188`、`recharge.spec.ts:52`），必须进白名单，不能改。
15. **最终 grep 的白名单要比规格 3.2/3.3 多。** 额外命中：`deploy/docker-deploy.sh:222`（用户可见提示，应改）、`.gitleaks.toml:31`、`.push.env.example`（建议并入 3.3）、`automation/README.md`+`lib/card.js`（另一个 cardplatform 项目）、`frontend/index.html:20-23`、`payengine/builtin.go:7`、`payengine/config.go:33`、`acgshared/money.go:22`、`gptcheck/check.go:2`、`db/cdk_session.go:561`（`addProviderColumn(...,"zovocard")`，规格 3.2 允许）、`db/provider_migration_test.go`、`docs/marswallex-openapi-zh.md:1,3`（MarsWallex 自己的"卡台"措辞）。
16. **`routes.ts` 旧路径兼容（:104,105,108）指向被删页面。** `billing`/`reconcile` → `/ops/orders`、`cards` → `/ops/integration`；会二次重定向，最好直接改到 `/ops/providers`。
17. **`AdminResolveAgentExchange` 的 `old_disabled` 字段和前端 toast 耦合。** 删掉禁用逻辑后后端不再返回，`AgentExchangePanel.vue:148-149` 的 `old_disabled === false` 分支永远不触发，旧前端缓存页面也不会报错；但要在更新说明里列这个接口路径改动。
18. **文档与规格 5.6 有出入**（见第 6 节表）：`cdk-list-region.md`、`x-premium-subscriptions.md` 整篇只讲被删功能，按「删段落」会留下空壳；`UI-UPGRADE.md` 讲的是另一个 `cardplatform` UI 项目。
19. **`unused` 与框架残留。** 删完 `zovoCardAdapter` 后注册表里没有上游型服务商，但上游框架（`upstreamAdapter`、`holdUpstreamAccount`、`upstreamAccountEmail`、`cardHeadPruneMissing`、`upstreamGraceRecoverer`、`upstreamLookuper`）按规格 3.2 保留。原型 lint：只有 `upstreamExchanger`（`provider_adapter.go:104`）会被 `unused` 报，必须跟着删；其余因为仍被分派代码引用不会报。`deadcode` 也不会把它们列为不可达（经 `upstreamOrFail` 仍可达）。
20. **全量 `go test ./...` 在并发负载下会超时。** 原型第一次跑 `handler` 包超过 8 分钟被我杀掉（同时机器上还有别的会话在跑 `-race`），单独再跑 57 秒完成。执行计划里每个任务的验证步骤最好单包单跑，别 `./...` 一把梭。
