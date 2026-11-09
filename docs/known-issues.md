# 已知遗留问题

这份清单收集历次审查和审计里已经发现、但当时决定先不修的问题，供以后统一安排修复。来源包括：MarsWallex 付款核心（PR #2）和 MarsWallex 钱包子项目的执行账本与终审，菜单分组与卡头（PR #14、#15），内嵌结账页与稳定性（PR #21），首页售后查询（PR #22），全系统审计（PR #23，2026-11-04/05），代理商核心（PR #26），异次元对接（PR #28），GitHub 上标了「不修」的 CodeQL 告警，以及项目记忆里记下的上线前待办。整理日期 2026-11-09，对照的代码是 master `2cbfdb6`（v1.5.4）。这些条目在当时都判为不影响资金安全的小问题；少数条目整理时发现其实碰到钱或敏感数据，已在条目开头标【涉及资金】或【涉及敏感数据】，建议优先看。

状态说明：

- **未修**：整理时对照 master 代码看过，问题还在。
- **未逐条核对**：来自审查记录，没有逐条对照代码确认。后面的 PR 可能已经顺手改掉，动手前先看一眼代码。

已经修掉的条目没有列进来。纯过程记录（例如「没有先写失败测试」）也没有列。

---

## 付款核心与内嵌结账页

### 会影响使用的

- 【涉及敏感数据】进程崩溃、被杀或在线更新重载之后，Chrome 用户目录（里面有用户的 ChatGPT 会话 cookie）最长会在 `/tmp` 里留约 2 小时，因为 `paybrowser/chrome.go` 的 `staleProfileAge` 是 1 小时、清理每小时才跑一次；重载时还可能留下没人管的孤儿 Chrome 进程。来源：PR #21 稳定性审计 G2（当时定为以后再做）。状态：未修。建议：用户目录放进以本进程 PID 命名的父目录，启动时发现那个 PID 已经不在就整个删掉；Chrome 用单独的进程组启动，启动时按记录的进程组杀掉上一轮残留。
- 【涉及资金】确认协程 `payengine/settle.go` 的 `verifyDue` 逐张串行检查，卡池维护也是串行的。几张单同时在确认、代理或接口又慢时，后面的单会查得很晚；最坏情况下定论晚于租约到期，卡已经被 `expireLeasesOnce` 释放进回收，`verifyConclude` 释放时只记一条 `ErrLeaseInvalid` 警告。审计当时请资金评审确认影响，记录里没看到结论。来源：PR #21 稳定性审计 G4。状态：未修。建议：每张单的检查设总时限（例如 45 秒），确认协程改成有上限的并行，或者把「释放到期租约」挪到单独的快速循环。
- 发 Telegram 告警时，`notify.SendText` 先同步读库解密配置，再每条消息起一个协程，不合并、不排队；Telegram 回 429 时消息直接丢，没用上 `RetryAfter`。告警一多（例如一个地区的单批量转人工）会拖慢付款和确认协程。来源：PR #21 稳定性审计 G6。状态：未修。建议：一个常驻发送协程加有界队列，相同内容短时间内合并，429 按 `RetryAfter` 重试，配置缓存在内存里、设置变更时刷新。
- 【涉及资金】没有「每天最多付多少单」的上限。MO-06 的现象里提过，修复计划没要求，当时只做了注资、钱包转账、兑换的单笔和单日金额上限加付款熔断，修复报告认为这已经限制了损失。来源：2026-11-03 资金问题修复报告（MO-01、MO-03、MO-06）。状态：未逐条核对。
- 确认阶段每次都新建 tls-client 并刷新会话，没有复用客户端；遇到 Cloudflare 403 会一路拖成待人工。重试退避也只用在主确认窗口，宽限延长段仍是固定间隔。来源：PR #2 Task 8 延后、PR #21 Task 12 裁定。状态：未逐条核对。
- 托管支付页（点数单）上，提示文字的判断排在 3DS 之前：3DS 页里带 `[role="alert"]` 的说明文字会让单据直接按校验错误或被拒处理，不去填验证码。点击前就已显示、点击后不变的校验错误按裁定被忽略，结果是 Unknown，一单要等约 12 分钟才转待人工。来源：PR #2 终审复审「范围外观察」。状态：未逐条核对。
- `paybrowser/page.go` 的 `waitFind` 和等待页面结果时会吞掉 CDP 错误，浏览器崩溃时要等满超时才结束。来源：PR #2 Task 6 延后。状态：未逐条核对。
- `paybrowser/page.go` 的 `shotPage` 持着 `b.mu` 做 CDP 往返，截图慢时会卡住同一浏览器上的其他调用。来源：PR #2 Task 6 延后。状态：未修。
- 3DS 验证框高于视口时，截图裁掉的部分是空白。来源：PR #2 Task 6 延后。状态：未逐条核对。
- 查找元素的求值次数是「选择器数 × frame 数」，真实页面 frame 多时可能偏慢。来源：PR #2 Task 6 延后。状态：未逐条核对。
- `paybrowser/amount.go` 的 `ParseAmount` 只取原文里最后一段数字：用撇号或空格分千位的金额（例如 1'000.00、$1 000.00）会被读成只有最后一段，`$.50` 会被读成 50.00。目前多数情况落在「读不出或超上限而放弃」的安全方向。来源：PR #2 Task 5 延后、PR #21 Task 3 延后。状态：未修。建议：识别撇号和空格分组，小数点开头的金额单独处理，读到多段时报错而不是取最后一段。
- `paybrowser/page.go` 的 `Open` 轮询时吞掉错误，超时说明写死「60 秒」；调用方时限先到时，超时说明里的秒数仍写 `pageLoadTimeout`。来源：PR #2 Task 5 延后、PR #21 Task 3 延后。状态：未修。
- 内嵌结账页进入 5 秒读页阶段后，不再响应调用方主动取消，最多多占浏览器 5 秒。来源：PR #21 Task 3 延后。状态：未逐条核对。
- 内嵌结账页的诊断（`Diagnose`）仍按托管页的金额选择器查，诊断信息里会显示「没找到金额」。只影响排障信息。来源：PR #21 Task 3 延后。状态：未逐条核对。
- 老订单落库的支付链接如果带首尾空白，`Open` 仍拿未去空白的值（校验和类型判定已经统一去空白，不影响安全）。来源：PR #21 Task 6 延后。状态：未逐条核对。
- 支付域名白名单里，空端口拒绝只加在 chatgpt.com 分支，托管页两个域名带空端口（`host:`）仍放行（实际连的仍是 443）。来源：PR #21 Task 6 延后。状态：未逐条核对。
- 最低金额在建会话时从当前配置读、不落库，排队期间管理员改了配置会按新值判。来源：PR #21 Task 7 延后（计划规定的做法）。状态：未逐条核对。
- 付款引擎的 `Evidence` 没有上限；同一张卡允许用多次（`MaxUsesPerCard>1`）时，会混进同卡后续交易的记录。来源：PR #2 Task 8 延后。状态：未逐条核对。
- `payengine` 的 `graceRestored` 在进程内只增不减；恢复多轮失败时，宽限期从首次写入起算。规模可以忽略。来源：PR #2 Task 8 延后。状态：未修。
- 付款单「同一账号在途」冲突靠错误文本里的 `pay_orders.account_email` 判断（`payengine/store.go` 的 `isInflightConflict`），改索引名或驱动报错格式就会失效。来源：PR #2 Task 3 延后。状态：未修。
- `finish` 不校验目标状态是不是终态；空邮箱的在途单会互相冲突（上游 `ParseSession` 已要求邮箱，影响小）。来源：PR #2 Task 3 延后。状态：未逐条核对。
- 地区代理配置只校验请求里带的地区的币种，`Label` 不校验；`validProxy` 不去首尾空白，比 `chatgpt.New` 更严。来源：PR #2 Task 4 延后。状态：未逐条核对。
- 空套餐名补回的是代码里的默认值，不是库里管理员自定义的值。来源：PR #2 Task 9 延后。状态：未逐条核对。
- 兑换页「配置」里把币种改成 JPY 或 CLP 时，`el-input-number` 会按新精度截掉已有小数；`loadConfig` 没有请求序号保护，旧响应晚到可能覆盖正在编辑的内容（窗口很小）。来源：PR #2 Task 10 延后。状态：未逐条核对。
- 【涉及敏感数据】`chatgpt` 客户端把底层错误原样包装返回，错误里如果带代理地址，可能带出代理账号密码；目前靠调用方展示前脱敏。来源：PR #2 Task 2 延后。状态：未逐条核对。建议：在客户端这一层就把代理 URL 里的用户名密码去掉。

### 测试与代码整理

- `chatgpt/client.go` 和 `gptcheck/check.go` 各有一份 `firstString`。来源：PR #2 Task 2 延后。状态：未修。
- `payengine/service.go` 的 `defaultShotDir` 重复了 `db.go` 的库路径规则，而且没有测试。来源：PR #2 Task 7 延后。状态：未修。
- `paySaveConcurrency` 包变量只为测试注入。来源：PR #2 Task 9 延后。状态：未逐条核对。
- `chrome.go` 的 `kind` 字段插在 `submitted` 和 `submitLocked` 之间，排版不整齐；`fillEmbeddedAddress` 和 `fillOne` 各有一份 find 闭包。来源：PR #21 Task 2、Task 4 延后。状态：未逐条核对。
- 点数和订阅两张价格表组件（`PayCreditPlans`、`PaySubscriptionPlans`）的 `rowFloorProblem`、`showAll`、`visibleRows` 和列模板基本是两份。来源：PR #21 Task 8 延后。状态：未逐条核对。
- 内嵌页点击要 4 个 CDP 往返，可以合并成 2 个并加 `elementFromPoint` 命中检查（毫秒级收益）。来源：PR #21 Task 5 延后。状态：未逐条核对。
- 卡池补拉一轮里调三次 `opts.Client()`；长期没有 Webhook 时每 5 秒跑一次 `COUNT(*)`。来源：PR #21 Task 13 延后。状态：未逐条核对。
- 测试缺口：ChatGPT 客户端没测 Refresh 多分片发送、200 但缺 accessToken、5xx；store 测试 `size=101` 上限用例库里只有 25 条，夹取不生效也会过；没测 transition/addEvent 不存在的单号、finish 状态不符不清会话、插入冲突不残留事件；没测重复键和 `ResolvePlan` 的两个错误分支；启动中途取消路径没有测试；`TestLaunchHidesProxyPassword` 没覆盖启动阶段失败；没测 OTPInput 首选路径、多输入框、无提交按钮按回车、跨站 iframe 走代理；`TestOTPRetryAfterFailedSubmitClick` 用固定 `sleep 2s`；没测 Source 为空、`session_unreadable`、`checkout_failed`、`page_failed`；settle 的「fail 再失败只打日志」和停机不打日志两个分支没有专门测试；变异检查存活的缺口（verifyOrder 取消检查、告警先于定论、先终态后释放、`EqualFold` 改成 `==`）。来源：PR #2 Task 2–8 延后。状态：未逐条核对。
- 测试缺口（内嵌页）：`TestCheckoutPageKind` 没覆盖带端口、userinfo、子域的地址；`PayButtonState` 内嵌页 0 个按钮的分支没有测试；「已离开 /checkout/ 后再回来仍不点」的粘滞分支（`page.go` 约 469–471 行）没有测试；`TestRealOrderWithZeroFloorAborts` 末尾 `h.pool.total()==0` 基本恒真；价格表表头测试用 `arrayContaining`，不验证列顺序；只核对到顶层 iframe，仿造页联想只认 input 不认 keydown，缺双份选项的端到端用例，`ClickOption` 缺 3 种情形，变异检查没有日志，末尾补查的宽选择器有误报；`TestRefreshCardAvailableRevokedDisablesNow` 用卡余额为 0 的卡证明不可选，断言可能不是由停用引起；`TestEgressProbeNoProxy` 设的 `HTTPS_PROXY` 会被 `envProxyHost` 读到。来源：PR #21 Task 2、4、5、7、8、10b、13、14 延后。状态：未逐条核对。

---

## 卡服务商与卡池

### 会影响使用的

- 【涉及敏感数据】`marswallex` 的 `redactPAN` 只处理 `marswallex.Error.Message`，错误里的 `Code` 和被包裹的底层 `Err` 没处理，理论上可能带出卡号。来源：PR #21 Task 14 延后。状态：未逐条核对。建议：对整条错误文本做卡号打码，而不只是 Message 字段。
- 【涉及敏感数据】`cardpool/holders.go` 的 `scrubProfile` 不抹中文姓名和证件有效期（规格列出的敏感字段里没有这两项），平台错误原文里带这两项时会原样返回给提交资料的管理员。来源：MarsWallex 钱包子项目终审 Minor 4。状态：未修。
- 自动补卡的缺口检查不看币种（`cardpool/replenish.go`）：产品里只有 EUR 卡时仍把它们算作可用，不会自动补 USD 卡，每单都以 `no_card` 失败（告警会写明缺 USD 卡）。来源：PR #2 终审修复报告、复审。状态：未逐条核对。
- 卡池页的「释放」「查看卡号」说明文字读的是策略页的编辑中数据，没保存的编辑会短暂影响显示的数值。来源：PR #21 Task 14 延后。状态：未逐条核对。
- ZovoCard 页两个「立即同步」不联动，卡头最多晚 30 分钟；卡头面板文案「由服务商接口自动读取」对 ZovoCard 不准确（读的是本地缓存）。来源：PR #14 终审。状态：未逐条核对。
- 卡头同步失败也会更新「上次同步」时间，和失败提示同时出现时容易误读。来源：PR #14 终审。状态：未逐条核对。
- 没配置的服务商每 30 分钟打一条卡头同步失败日志。来源：PR #14 终审。状态：未逐条核对。
- 卡头同步失败时把 `err.Error()` 原样返回浏览器（不含密钥），和 `poolErrorStatus` 的做法不一致（`handler/card_heads.go` 的 `AdminSyncCardHeads`）。来源：PR #14 终审。状态：未修。
- 修改卡头时，字段没有任何变化也写一条审计（`fields=` 为空）。来源：PR #14 终审。状态：未修。建议：`fields` 为空时直接返回、不写审计。
- 页面里还有「卡台接入」「卡台配置」「去卡台页」等旧叫法（`CardSelectionConfig.vue`、`CDKeyManagement.vue`、`lib/provider.ts`、`handler/provider_builtin.go`）。来源：PR #15 终审。状态：未修。
- MarsWallex 账户同步碰到 200 页上限，或者 `count=0`、空列表时，仍会提交并把这次没出现的账户标成 gone（之后往这些账户转账会被拒）；在 R13 之后，第 2 页起返回 `data:null` 也会让 `SyncAccounts` 提交一份不完整的账户列表。来源：MarsWallex 钱包子项目 Task 2 延后、终审复审。状态：未逐条核对。
- 交易补拉时，一页临时返回 `data:null` 会让 cursor 照样前进（R13 接受的取舍）。来源：MarsWallex 钱包子项目终审复审。状态：未逐条核对。
- 还没有 cursor 时，手动补拉（或付款引擎自动触发的补拉）会把 cursor 设成当前时间，跳过首次 7 天回溯，只影响交易表的历史数据，可手动补拉 7 天补回。来源：MarsWallex 钱包子项目 Task 6 延后、PR #2 终审复审 Minor 3。状态：未逐条核对。
- 补拉单段超过 200 页时，多出的数据只记日志，不告警、不标记（单段要上万笔才会触发）；被取消（ctx 结束）的那一轮会覆盖 `last_*` 状态；`last_run_at` 的注释写的是结束时间，实际是本轮开始时间。来源：MarsWallex 钱包子项目 Task 6 延后。状态：未逐条核对。
- 新建子账户时，平台没回 `out_account_no` 会用空串覆盖；`account_no` 为空会以空串作主键插入；本地写库失败时不标脏。新建时还写了 `synced_at`，空状态默认成 ACTIVE。来源：MarsWallex 钱包子项目 Task 2 延后。状态：未逐条核对。
- 账户接口出错时同步错误被丢弃；`syncAccountsIfDirty` 可能共享一轮在标脏之前就开始的同步；`syncMu` 不感知 ctx。来源：MarsWallex 钱包子项目 Task 2 延后。状态：未逐条核对。
- 推送先于平台 200 响应到达、又不带 order_no 时，单据详情里没有平台单号；`not_sent` 的钱包单也会把账户标脏。来源：MarsWallex 钱包子项目 Task 3 延后。状态：未逐条核对。
- 卡转入转出明细：本机时钟偏差时 `start` 可能晚于 `end`；每行查一次本地单据。账户流水校验用去空白后的值、发出去的却是没去空白的 `account_no`/`currency`（处理器已经去过空白）。来源：MarsWallex 钱包子项目 Task 4 延后。状态：未逐条核对。
- 已存在的持卡人收到推送时，不会关联遗留的 unknown 提交记录（管理员可以手动放弃）；关联备注里用的商户侧 ID 来源不一致；`truncate` 截出来是 201 个字符；`refreshAfterChange` 的兜底读写不在一个事务里，极少数情况下会重复或漏发告警。来源：MarsWallex 钱包子项目 Task 5 延后。状态：未逐条核对。
- 平台已受理新建持卡人、本地写库失败返回 500 后，新建表单不关闭、提交记录也不刷新。来源：MarsWallex 钱包子项目终审复审。状态：未逐条核对。
- 持卡人启停和放弃接口用的是 `poolFail` 而不是 `holderFail`；子账户启停的审计记的是原始 `req.Status`；修改持卡人时处理器没对 id 去空白（服务层会去）；`serialsWindow` 用 `time.Now()` 而不是服务时钟；一次很长的手动补拉可能被客户端断开截断（数据按段保存，但审计会跳过）。来源：MarsWallex 钱包子项目 Task 7 延后。状态：未逐条核对。
- 钱包页和卡池页的小问题：提交中标志在确认框之后才置位，双击可能在弹框前触发两次（数据库在途索引兜底）；重查和确认失败没有逐行防重；停用确认框没用危险样式；长度检查混用 UTF-16 长度和码点数；表格里的单据状态显示英文原值；流水页不自动选唯一的币种、换账户后旧结果还留着、日期框不适配深色；新建子账户在 10 秒结果未知窗口内可以再点；确认失败等非资金写操作把网络异常当普通失败。来源：MarsWallex 钱包子项目 Task 8 延后。状态：未逐条核对。
- 持卡人表单的小问题：前端「今天」用浏览器本地日期（后端按北京日期，以后端为准）；邮箱正则和 Go 的 `mail.ParseAddress` 不完全一致；`validDate` 把 0–99 年当成 19xx；被拒后不刷新提交记录；修改结果未知时对话框保持打开；修改、启停没有逐行 loading；`beforePhoto` 不检查文件类型（后端按内容嗅探）；不一致表格不显示本地单据状态。来源：MarsWallex 钱包子项目 Task 9 延后。状态：未逐条核对。
- 深色主题下卡详情抽屉看起来半透明，后面的列表按钮透出来，需要人工看一眼。来源：MarsWallex 钱包子项目 Task 10 延后。状态：未逐条核对。

### 测试与代码整理

- `db/card_secrets.go` 的 `GetCardSecret` 自己判断「是不是旧明文」（约 29–32 行），规则和 `legacyPlainCardSecret` 相同但分支不同，没统一。来源：审计 O3 收尾 minor (deferred)。状态：未修。建议：`GetCardSecret` 直接调 `legacyPlainCardSecret`。
- 两个唯一约束判断函数（`isInflightConflict`、`isWalletInflightConflict`）里 `sqlite3.Error` 的前置代码重复；`describeOrder` 在付款方或收款方为空时仍打印「 → 」；服务层的 log 和处理器的审计重复；持卡人关联 SQL 写死了状态字面量；`MarsWallexWallet.vue` 618 行、`CardPool.vue` 约 1370 行。来源：MarsWallex 钱包子项目 Task 1、2、5、8、9 延后。状态：未逐条核对。
- 测试缺口（钱包）：`TestNewOutIDPrefixes` 没断言末 8 位是小写十六进制；`TestConversionWebhookBeforeResponse` 依赖时序、`<-done` 没有超时，CI 上可能卡死；校验类测试只断言 `ErrBadRequest`、没断言原因；缺钱包单定论冲突的专门测试；`TestValidateProfile` 断言弱、有一个用例命名不对；重复放弃没有测试；推送导入失败路径没有隐私测试；补拉的 EXISTS 检查和 upsert 不在同一事务、转换函数可能漂移、`TestBackfillDropsOTP` 的 otp 表断言是空断言；`ReceivingAddresses` 遇到 `data:null` 没有测试。来源：MarsWallex 钱包子项目 Task 1–6 延后。状态：未逐条核对。
- 测试缺口（选卡）：「键有没有出现」的测试只覆盖 6 个选卡字段中的 3 个，`SelectionKeys`/`CopySelection` 拼错键测不出；前端没有测试保证 `CardPool.vue` 保存时用了 `withoutSelection`。来源：PR #15 终审。状态：未逐条核对。

---

## 发码与兑换

- 【涉及资金】代理换码（`/partner/swap`）的共用口令只要求至少 12 位，不查弱口令表，也没有按口令维度的全局失败锁定；分布式猜口令只受 bcrypt 速度和每 IP 每小时 20 次限制，猜中后可以按日上限（默认 20 张）换出新码，新码兑换时由本站付订阅费。来源：审计 F-6（S3 修了限流表和 12 位，「按口令失败锁定」「弱口令表」没做）。状态：未修。建议：保存口令时复用 `db.IsWeakPassword`；口令错误按全局计数，复用 `failLimiter` 做锁定。注意：代理商功能开关不管这个入口。
- 老的短口令（升级前设的、不足 12 位）只有在代理用它成功换过一次码之后，后台才会提示「需要更换」，因为库里存的是哈希，看不出长度。来源：审计 S3 修复报告。状态：未修。
- ZovoCard 换码时，旧码套餐缓存为空就向卡台查，但查到的套餐只在内存里用，不回填本地缓存，下次还要再查。来源：审计 S3 修复报告（E-3）。状态：未逐条核对。
- 代理换码设置读取失败时，页面仍显示「未设置」、上限为空，可能误导管理员重设口令。来源：PR #15 终审。状态：未逐条核对。
- `db/db.go` 的 `legacyUUIDCode` 仍被启动时的 `migrateLegacyCDKCodes` 使用（用纳秒时间加 sha1 生成迁移后的码，只在旧 `cd_keys` 行上跑，已跑过的库不会再命中）。是否删掉这段迁移一直没定。来源：审计 HB 修复报告。状态：未修。

---

## 首页售后查询

### 会影响使用的

- 【涉及敏感数据】本地部分很短的邮箱等于没打码：`handler/card_health.go` 的 `maskEmailForAdmin` 对两个字符以内的本地部分会原样露出（例如 `ab@…` 变成 `ab***@…`），没有 `@` 且不超过 3 个字符的串原样返回。首页售后查询和 `/history` 都用它。来源：PR #22 终审 Minor 5（当时决定沿用旧规则）。状态：未修。建议：本地部分不超过 2 个字符时只留 1 个字符；同时检查后台显示是否受影响。
- 【涉及敏感数据】服务商原文的清洗有几种漏网：卡号紧挨字母或下划线时 ASCII `\b` 不命中；URL 编码的邮箱（`%40` 代替 `@`）；中文顶级域的邮箱；被 `+`、`/` 切成短段的 base64。现在 ZovoCard 的原文只来自旧的 `recharge_tasks` 数据，风险低。来源：PR #22 Task 2 延后、终审 Minor 6。状态：未逐条核对。
- 【涉及敏感数据】`factsFor` 写日志前的清洗只遮 20 位以上的令牌和邮箱，上游错误里如果带短于 20 位的码或令牌，日志里会出现原文（现有适配器没看到这种拼接，属于理论残余）。来源：PR #22 终审复审。状态：未逐条核对。
- 清洗会多打码：20 个以上连续的 `-`/`_` 分隔线和相邻的数字串会被当成敏感串遮掉。来源：PR #22 Task 2 延后。状态：未逐条核对。
- 售后查询公共层没有状态白名单：将来某家服务商返回 `Found=true` 加一个不认得的状态，单条接口会回 404，或把那个不认得的原始状态值直接显示给用户。现有两家服务商层都守住了。来源：PR #22 终审 Minor 13。状态：未逐条核对。建议：公共层把不在列表里的状态按 error 处理。
- 时间列里存了认不出的文字时，驱动给零值时间：开通时间显示成公元 1 年，未用的码被判成已过期（master 上原来就这样）。来源：PR #22 终审复审。状态：未逐条核对。建议：零值时间按「未知」显示，不参与过期判断。生产库的数据核对见「要上线前核对的」。
- `/history` 批量表的「重新提交」按钮只看 `can_resubmit`，`credit_region_mismatch`（点数地区不符）的行仍有按钮，而设计要求这类码让用户联系售卖方换码。来源：PR #22 终审复审。状态：未修。
- `/history` 单条查询找不到时，英文界面仍显示后端的中文提示；单条的旧请求逻辑把所有非 2xx 都当「找不到」。来源：PR #22 Task 7 延后、终审复审。状态：未逐条核对。
- `/history` 顶部统计用 `sm:grid-cols-5`，新增两栏后会换行。来源：PR #22 终审复审。状态：未修。
- `/history`「复制已用邮箱」现在复制出来的是打码后的邮箱，这个按钮基本没用了（CHANGELOG 已写）。来源：PR #22 终审 Minor 8。状态：未逐条核对。建议：去掉这个按钮或改成复制卡密。
- 批量 CSV 里「（原因：…）」写死中文，英文界面导出也是中文。来源：PR #22 Task 7 延后。状态：未逐条核对。
- 本站码换码后读新码时，`openLocalCDKCode` 只回 bool，读库临时出错也显示成「已换码但查不到新码」（`replaced_unknown`）。来源：PR #22 Task 4 延后（设计如此规定）。状态：未逐条核对。
- `unixToTime` 没有显式拦 NaN、Inf（结果仍为 nil，靠实现相关的转换）。来源：PR #22 Task 1 延后。状态：未逐条核对。

### 测试与代码整理

- `localFailureText` 和 `localGuideByErrorCode` 两张表没有同步测试；`zovoSetResult` 和 `setResult` 功能重叠；`local_cdk_sealed_test.go` 约 197 行注释过时；语言包里 `cdkLookup.msg*` 等键可能已经没人用。来源：PR #22 Task 4、5、7 延后。状态：未逐条核对。
- 测试缺口：时间解析缺 +08:00 偏移、2020/2100 两端、负数、毫秒阈值附近的用例；错误和 panic 日志不含邮箱没有断言；缺 `UsedAt==finished_at`、`dirty_order→manual_review` 的用例，无主密钥换码子测试缺 `storedSealed==""` 断言；缺「跨服务商」的换码链测试（设计要求，代码每层重新分派，读下来正确）；一个测试名可以改顺。来源：PR #22 Task 1、2、4、6 延后，终审 Minor 10。状态：未逐条核对。
- 前端 `Date.parse` 对不带时区的串按本地时区解析（后端恒带 `Z`，目前只是提醒）。来源：PR #22 Task 6 延后。状态：未逐条核对。

---

## 后台与登录

- 审计日志页把 `card_heads_sync`、`card_head_add`、`card_head_update`、`card_head_delete` 和 `cardpool.selection` 显示成英文原词，`frontend/src/lib/errors.ts` 的 `AUDIT_LABELS` 缺这几项。来源：PR #14、#15 终审。状态：未修。

---

## 在线更新与部署

- 【涉及敏感数据】在线更新前的整库备份（`handler/selfupdate.go` 的 `backupDatabase`，`VACUUM INTO` 到 `updates/backup-*/cdk_recharge.db`，保留 3 份）没有收紧权限，按进程 umask 落盘，通常是 0644；备份目录在 `handler/update.go` 里按 0755 创建。卡台密钥虽已改成密文，备份里仍是整库的用户邮箱、付款记录等业务数据，同机其他用户能读。来源：审计 A-6 的建议修法（S1 说归 S2，S2 没做）。状态：未修。建议：备份后 `os.Chmod(dst, 0o600)`，备份目录建成 0700。
- 在线更新的启动检查（`--check`）跑满 30 秒超时时，报错文案说「可能是不支持 --check 的版本」，排障时有误导（真实原因可能是库锁等待）。来源：审计 finalcheck 小瑕疵。状态：未修。建议：`ctx.Err()` 单独报「启动检查超时」。
- 在线更新排空在途付款单超时（`payengine.DrainMax`）后照旧重载，下载新版本期间也不停止派发新单。不会重复付款，重启后由恢复逻辑接管，但在途单会被打断。来源：PR #21 终审二 Minor-5。状态：未修。建议：排空超时就中止这次更新并提示，下载期间先停派发。
- 原生部署用的旧 systemd 服务文件没有 `KillMode=mixed`：停服务时 Chrome 子进程会被一起杀掉，在途单被计入熔断。新的 `deploy/install.sh` 和 `deploy/cdk-recharge.service` 已经带上，老站要重跑 `install.sh` 才会更新（CHANGELOG 已写）。来源：PR #21 终审二 Minor-4。状态：未修（代码侧无法改已部署的服务文件）。建议：启动时检测服务文件的 KillMode，不对就打日志并在后台提示。
- 启动日志把数据库路径写死成 `data/cdk_recharge.db`（`db/db.go` 约 111 行），设了 `DB_PATH` 时也这么打。来源：PR #28 人工点检。状态：未修。
- 部署文档没写「同一个数据库只能跑一个服务实例」：两个实例会互相接管已提交的付款单。这个限制只写在设计文档里。来源：PR #2 终审裁定 R50。状态：未修。
- `install.sh` 识别不了符号链接形式的 snap 桩。来源：PR #2 Task 11 延后。状态：未逐条核对。

---

## 代理商

### 会影响使用的

- 管理员新建代理商后重读失败（`handler/agent_admin.go` 的 `adminAgentResponse` 里 `GetAgent` 或 `CountAgentCodes` 出错）会回 500：账号已经建好，但初始密码只在这次响应里，就此丢掉，只能再点「重置密码」。来源：PR #26 Task 6 延后、终审裁定 P33（概率低、有补救，暂不修）。状态：未修。建议：重读失败时仍回 200，带上已知字段和初始密码，并记日志。
- 调账缺幂等键时的报错文案说的是发码（`handler/local_cdk_admin.go` 约 177 行，「同一次发码的重试……」）。前端固定带键，用户一般看不到。来源：PR #26 Task 6 延后、裁定 P33。状态：未修。
- 就绪检查里的 `syncWalletThrottled`（`handler/provider_readiness.go`）持着 `walletSyncMu` 做真实钱包同步，最长 15 秒且不响应取消；上游慢时每 30 秒会有一个请求排队等这么久，其间所有走就绪检查的请求（代理商 `/products`、`/orders`、客户兑换）都在锁上排队。不会扣错钱。来源：PR #26 终审 AG-A、裁定 P31（原有设计，本轮不修）。状态：未修。建议：改成「单飞刷新」，锁只保护时间戳和上次结果，真正的同步放到锁外。
- 返回单个代理商时，`CountAgentCodes` 对全表做一次 `GROUP BY`。性能项。来源：PR #26 终审 AG-A。状态：未修。
- 代理商登录：只给存在的账号写审计，带来微小的计时差异（受每 IP+登录名 15 分钟 5 次限制）；登录成功后，旧的临时凭证在 5 分钟内仍可重放且计数清零（需要先拿到 HttpOnly、限定 Path 的 `agent_pre`，且仍受账号级限流）。来源：PR #26 Task 5 延后，终审 AG-B 定为不修。状态：未逐条核对。建议（如要堵）：`TouchAgentLogin` 时顺带 `session_version+1`，用新版本签正式会话。
- 价格矩阵：价格审计只记改了几格；同一批里重复的格子后者生效。来源：PR #26 Task 6 延后。状态：未逐条核对。
- 管理员读代理商登录名失败时静默降级。来源：PR #26 Task 6 延后。状态：未逐条核对。
- 数据层没有外键，`PutAgentOverrides` 能写入不存在的代理商（当时要求处理函数先确认代理商存在）。来源：PR #26 Task 1 延后。状态：未逐条核对。
- `CreateAgentOrder` 不拒绝调用方传入带 `ExchangedFrom` 的码（目前没有这种调用路径）。来源：PR #26 Task 3 延后。状态：未逐条核对。
- `AgentLedgerSummary` 的三条查询不在同一快照里，只影响显示。来源：PR #26 Task 2 延后。状态：未逐条核对。
- 流水 CSV：导出把截止时间截到当前秒，但「先取时间戳后提交」、`created_at` 早于导出开始的行，仍可能让 OFFSET 翻页错位（极少见）。来源：PR #26 Task 8 延后。状态：未逐条核对。
- `sealLocalCDKCode` 的告警措辞讲的是换码，而且每张码都重读一次密钥；503 busy 有两种措辞。来源：PR #26 Task 7 延后。状态：未逐条核对。
- `PutAgentTierPrices` 每格做两次 `COUNT` 查询（不影响正确性）。来源：PR #26 Task 1 延后。状态：未逐条核对。
- 代理商前端：`loadSetup` 的迟到结果只按 step 判断，快速往返两次才会触发；`fmtUsd` 遇到 NaN 显示 `$0.00`；登录失败会清空密码框；布局卸载后遇到 401 走整页跳转。来源：PR #26 Task 9 延后，终审 AG-B 定为不修。状态：未逐条核对。
- 管理员调账对话框：在途时关掉再打开，请求返回后对话框停在禁用状态，只能放弃或关掉重开（安全方向）；`resetForId` 不重置 `adjBusy`；`fillMatrix` 里 `assemblePricing(null, d)` 调了两次。来源：PR #26 Task 11 延后，终审 AG-B 定为不修。状态：未逐条核对。
- 多标签页：A 标签页点退出时不弹确认，就清掉同一浏览器里 B 标签页的未决记录（退出本来就会清，cookie 共用，B 的会话也会被退出）；两个标签页在同一瞬间对同一笔原样重试时，`save()` 读回和原文不等，会退回内存模式。来源：PR #26 终审复审。状态：未逐条核对。建议：`save()` 读回时按 key 比较。

### 测试与代码整理

- `AgentPriceFor` 只是转调 `agentPriceScan`；测试里有 `_ = unpriced`、`_ = bid` 这类多余代码；`errRetryLedgerRef` 分支在 `BEGIN IMMEDIATE` 下基本走不到，注释应说明这是防御；测试里多余的 import、包装和 `containsString`。来源：PR #26 Task 1、2、6、7 延后。状态：未逐条核对。
- 按代理商限定付款单的子查询没有计数钩子，有人删掉限定条件时测试发现不了，只会变慢。来源：PR #26 终审 AG-A、裁定 P32（暂不补）。状态：未修。
- 测试缺口：数据库约束没有直接测试（用户名 NOCASE、余额 CHECK、订单唯一、流水部分唯一索引）；核对测试只篡改 `balance_cents`，没单独验证第二个等式；汇总测试没有恰好落在 `from` 上的流水；订单读回只比 AgentID 不比订单 ID；`FormatCents(MinInt64)` 只验证不 panic；`loginAllowed`/`loginFailed` 用 `time.Now` 而不是 `agentNow`，测不了 15 分钟解锁；订单详情 SSR 测试只断言标题；`keepPendingOnRateLimit` 的测试很薄；建单时停用 401、加密失败 503、无主密钥重放 503 没有接口测试；等同号锁时请求取消→503 并回收锁项这条分支没有测试；调账审计 detail、启用后重新登录没有测试；测试钩子是没有同步的包级变量，失败分支里 Cleanup 置 nil 可能额外报竞争；测试机若在东八区，挡不住按 `time.Local` 划月的错误。来源：PR #26 Task 1–10 延后。状态：未逐条核对。

---

## 异次元对接

### 会影响使用的

- 签名错误锁定按 IP 计时，会连带拒掉同一 IP 上其他合法商户的请求。来源：PR #28 Task 4 延后。状态：未逐条核对。建议：IP 被锁时只拒未知的 app_id（要改计划规定的测试）。
- `/shared/*` 的限流和锁定没有把 IPv6 按 /64 聚合（公开接口已用 `rateIPKey`）。来源：PR #28 Task 4 延后。状态：未修。
- 每个验签通过的请求都执行一次 `UPDATE` 写最近使用时间，SQLite 要拿写锁。来源：PR #28 Task 4 延后。状态：未逐条核对。建议：先比 `creds.LastUsedAt`，间隔够了再写。
- 店铺名 `shopName` 没有去掉 `<`、`>`，站点名以 `<` 开头时，下游 `strip_tags` 之后为空。来源：PR #28 Task 4 延后。状态：未逐条核对。
- trade 的 ctx 超时被归为「商品未就绪」，失败原因显示「暂时缺货」，实际是超时。来源：PR #28 Task 3 延后（计划要求）。状态：未逐条核对。
- 重放时遇到解不开的码，失败记录和「没扣款的繁忙」看不出区别。来源：PR #28 终审。状态：未逐条核对。建议：失败记录的 message 里带上订单号。
- 被限流的请求每次都写一条 `rate_limited` 失败记录（计划规定，只能刷自己的表）。来源：PR #28 Task 5 延后。状态：未逐条核对。
- 出码后没有检查 `len(codes)==Quantity`（目前靠核心保证码不删除）。来源：PR #28 Task 5 延后。状态：未逐条核对。
- 繁忙（busy）日志里带了 `err.Error()`（不含码和金额）。来源：PR #28 Task 5 延后。状态：未逐条核对。
- 重置或开通对接时，凭证是在动态码校验之前读的；同一账号并发「停用＋重置」可能写回旧的 app_id。来源：PR #28 Task 6 延后。状态：未逐条核对。
- 还没开通时点停用，仍会消耗一次动态码，并写一条 app_id 为空的审计。来源：PR #28 Task 6 延后。状态：未逐条核对。
- 保存白名单时，请求体缺 `ips` 字段等同于清空白名单。来源：PR #28 Task 6 延后。状态：未逐条核对。建议：缺字段按 400 处理，清空要显式传空数组。
- 生成密钥时随机数失败，走的是 `agentDBBusy`，日志写成「读库失败」。来源：PR #28 Task 6 延后。状态：未逐条核对。
- 流水列表每一页都全量调一次 `AgentOrderRefs`。来源：PR #28 Task 6 延后。状态：未逐条核对。
- 管理员开关对接时，写库、写审计之后再读一次库，最后这次读失败会返回 500，但开关其实已经生效。来源：PR #28 Task 7 延后。状态：未逐条核对。
- 对接页成功操作后 `load()` 会覆盖还没保存的白名单和提醒线输入。来源：PR #28 Task 8 延后（2026-11-05 点检时确认仍在）。状态：未逐条核对。
- 动态码弹框没有 Esc 关闭、焦点陷阱和自动聚焦；密钥面板是页内卡片，却写了 `role="dialog"`。来源：PR #28 Task 8 延后。状态：未逐条核对。
- 管理员端对接区：「关闭」「重新允许」两个按钮在请求中同时转圈；首次加载失败没有「重试」入口，加载中也没有 loading 状态。来源：PR #28 Task 9 延后。状态：未逐条核对。
- 对接操作的动态码输错只计限流、不写审计（代理商改密码失败会写 `agent_password_change_failed`）。来源：PR #28 终审。状态：未逐条核对。
- `ParseQuantity("000001")` 接受为 1；签名时把空键（`=1`）当成空名标量参与签名，而 PHP `parse_str` 会丢掉空键（只会造成验签失败，下游不会发空键）；`NewItem` 不去标签，纯空白的商品名不回退到 code（商品名由管理员设置）；白名单读取时不去空白，依赖调用方存规整的值。来源：PR #28 Task 1、2 延后。状态：未逐条核对。

### 测试与代码整理

- `db.AgentOrderProductCodes`（`db/agent_order.go`）已经没有调用方，是死代码。来源：PR #28 Task 6 延后。状态：未修。
- `agent_orders` 和 `local_cdks` 的补列循环几乎一样，db 包里这种循环已有 9 处左右，可抽成 `addColumnIfMissing`；三个单列 upsert 结构雷同；主密钥告警文字从 `agent_order.go` 复制；`MaxQuantity`、`ValidRequestNo`、`ParseQuantity` 放在 `money.go` 归属不贴切；测试里自写的 `strconvAtoi` 多余；测试辅助函数名 `from`、`header`、`appID`、`noTLS` 太通用；`acg_shared_admin_test.go` 约 140 行 `sealed != "" &&` 冗余。来源：PR #28 Task 1、2、4、7 延后，PR #22 Task 3 延后。状态：未逐条核对。
- 测试缺口：reason 非法值被 CHECK 拒绝、他人占用 app_id 时 `SetAgentSharedKey` 报唯一约束错没测；只有 `&` 的请求体、空键、`Money` 负数没测；`TestPlaceAgentOrder_ReadyCtxCancelled` 没确认慢检查被调用过（可能走了提前返回分支）；`coreInput` 的 `ExternalRef` 写死；重放时全部码解不开没有核心测试；items 共用 ctx 与去重、白名单在验签之后、恰好 64 KB 没测；主密钥不可用路径没有单测；secret 的 Host 来源测试写死 `example.com`；处理顺序（限流先于规格、非法 request_no 不占额度）没有测试锁住；白名单「参数错误不消耗动态码」、app_id 撞约束重试没有 handler 测试，db 缺 UPDATE 路径测试；管理员版流水 CSV 断言可能空转；停用测试固定错码 `000000` 有极小概率偶然命中；`TestAdminAgentViews_Source` 只验证了任务 6 的改动；`parseAllowlistInput` 的 null 输入没覆盖；对接页和管理员对接区都没有组件测试；`AgentIntegration` 没加进 `agent.spec.ts` 的 SSR 冒烟列表。来源：PR #28 Task 1–9 延后、终审。状态：未逐条核对。

---

## 前端通用

- `vue-tsc` 在本机 Node v26 下启动就崩溃，`npm run build` 只是 `vite build`、不做类型检查，CI 也不跑类型检查，所以 `.vue` 模板里的类型错误一直没人查。来源：PR #2 终审、PR #21 Task 14、PR #28 Task 10。状态：未逐条核对。建议：固定一个能跑 `vue-tsc` 的 Node 版本，在 CI 里加一步类型检查。
- 项目没有 DOM 测试环境，复制、跳转、弹框这类点击交互只能人工在浏览器里点。来源：PR #22 Task 7、PR #26 裁定 P11、PR #28 Task 8/9。状态：未逐条核对。
- 手机上打开 `/ops/cardpool?tab=policy`，标签栏不会滚到当前标签（下划线位置对，但在可视区外），属于 Element Plus 标签栏的既有表现。来源：PR #15 Task 10。状态：未逐条核对。

---

## 要上线前核对的

这些要用真实环境或真实数据确认，代码里看不出结果。

- money N-2：F-2 修复后「绑定邮箱以卡台核验为准」依赖卡台预检响应带 `email`；卡台不回邮箱时，仍回落到会话 JSON 里用户自填的邮箱。要用真实卡台核对预检响应是否带邮箱，并把测试桩改成同样的形态。如果不带：改为从会话 `accessToken` 的 `profile.email` 取邮箱、与 `user.email` 一致才记，或者卡台不回邮箱时只记令牌不记邮箱；`preflightVerdict` 对字符串形式的 `code` 也按非零拒绝。来源：审计复审 reaudit-money N-2。
- MarsWallex 上线顺序：先配 `APP_MASTER_KEY` 和强 `JWT_SECRET`；给 PH 配代理并点「测试」，确认识别国家是 PH（菲律宾价必须菲律宾出口）；用测试账号跑「免付款探测」；跑一次「试运行到填表」；跑通一笔真实开通且续费关闭成功后，才能发开通码；跑通一笔真实点数付款后，才能发点数码。来源：项目记忆（MarsWallex 进度）。
- 托管支付页（点数单）的选择器和词表还没用真实页面核实：词表外新出现的 `[role="alert"]` 文字（例如 Processing your payment…、3DS 提示、非英文文案）仍按被拒结束等待并关浏览器；`.SubmitButton` 延迟渲染时会回落点到页面上别的 submit 按钮（可能是优惠码的 Apply），白白用掉唯一一次提交。第一笔真实点数付款时要收集真实文案和按钮。来源：PR #2 终审修复 minor (deferred)「任务 1 必查」。
- 内嵌结账页（订阅单）待真实验证：改国家后其余地址字段如果撞上 Payment Element 重绘会被记成缺失、静默跳过，后果是付款被 Stripe 拦、钱留在卡上（终审 M4 已加等待，仍需实页确认）；路径不变但页面变成错误态、只剩一个「重试」提交按钮时的处理；PH 价格 ₱565 是否含税要在 PH 试运行时核对。来源：PR #21 Task 4、5、11 延后。
- 第一次在 VPS 上部署新版时停一次服务，确认在途单不会被提前杀掉（`KillMode=mixed` 本机测不出）。MarsWallex Webhook 本机收不到，也要在 VPS 上验证。来源：PR #21 Task 12 延后、PR #21 执行记录。
- MarsWallex 钱包只读验收：查一个没有交易的时间窗、看一张没有明细的卡、一个没有收款地址的账户，确认平台空结果返回 `data: []` 还是 `null` 或不带 `data`。如果是 null，补拉会在第一个空的 24 小时段失败、cursor 不再前进，空列表页面也会报错，要在计划层面放宽 `*Page` 的解码规则。来源：MarsWallex 钱包子项目终审 Important 3、Task 4 延后。
- Chrome 组件更新（`component_crx_cache` 持续变化）可能仍走代理，没有抓包确认。来源：PR #2 Task 8 延后。
- 售后查询上线前在生产库核对：`SELECT DISTINCT status FROM cd_keys`，以及 `used_at`、`expires_at`、`recharge_tasks.completed_at` 里解析不了的行；有异常先修数据，否则这些码会一直显示「暂时查不到」。同时确认已配 `APP_MASTER_KEY`。来源：PR #22 终审 Minor 12、CHANGELOG。
- 异次元对接上线后要验：用真实异次元下单取码（等 MarsWallex 和付款池就绪）；反向代理要转发 `/shared/`、`X-Forwarded-Proto`、`X-Forwarded-For` 和原始 `Host`，同机反代时 `TRUSTED_PROXIES` 要含 `127.0.0.1`。不传 Host 时买家拿到的发货链接是 `127.0.0.1`；受信反代不转发 `X-Forwarded-For` 时所有发卡站共用一个 IP，一个人就能锁住全部代理商的对接。来源：PR #28 终审、项目记忆。
- Claude 卡密兑换的 Organization ID 按 Grok/X 的方式放进 `credential.session`，格式是推测的，还没用真实 Claude 卡密验证。来源：PR #19、项目记忆。
- `deploy/docker-deploy.sh` 还没在测试机上验证三种情况：新装、已有 `.env`、从旧的克隆目录升级。来源：项目记忆（CI 与发版）。
- `install.sh` 没在 Ubuntu 真机上验证（AppArmor、needrestart、snap），完整的正常安装流程也没有端到端跑过（需要签名发布包）。来源：PR #2 Task 11 延后。

---

## 有意不修的取舍

下面这些是有理由地决定不改，或者是用户可能碰到的已知行为限制。以后要改，先重新评估当时的理由。

- D-10：付款用的 Chrome 关闭了站点隔离（site-per-process），这是为了操作 Stripe iframe 有意为之。来源：审计 D-10。
- C-8：选卡优先级只同步 gpt、claude、grok 三条产品线，不同步 x。没有证据表明卡台有 x 产品线的选卡规则。来源：审计 C-8、裁定 R15。
- E-9：「售后查询对本站码换码链只解一跳」不成立：查询入口会逐层跟换码链，最多 5 层。只补了回归测试。来源：审计 E-9。
- D-3：停机时能证明没点支付的单也留在 submitted，重启后只能走人工；人工定论失败会把码标成 declined、码就作废了。按裁定不改「是否已付款」的判定，只在后台人工定论页加了提示：「如果确认这笔没有扣款，请禁用这张码后重新发码给用户」。来源：审计 D-3、裁定 R9。
- 卡台发码结果不明（超时、断连、5xx、同键处理中等）时，只用同一个幂等键重放一次；仍不明就回 502「结果未知」、不给码，靠操作者用同一个键再点一次。依赖卡台的幂等语义。来源：审计 C-5/E-1 修复取舍。
- `cardplatform/client.go` 约 692 行 `SyncUpstreamFullCodes` 仍用按前缀兜底的 `LookupCardplatformCDKCode`，只用来判断这张码本站是否已有；改成严格按 ID 会把 `upstream_id=0` 的老码重复算成新导入。极端情况下前缀撞车会少算一条「新导入」。来源：裁定 R13、S5 修复报告。
- `cdk_redemption_tokens` 不设保留期、不清理（每行几十字节，旧绑定表也从不清理）。来源：裁定 R19。
- 镜像默认 `TRUSTED_PROXIES` 含整个 `172.16.0.0/12`，没改默认值，只在文档和 Dockerfile 注释里写明：不要单独 `docker run -p 8080:8080` 直接暴露端口，否则客户端能伪造来源 IP 绕过按 IP 的限流。改默认值会让容器网络里其他反代后面的所有客户端被当成同一来源。来源：审计 A-18/F-8。
- 已保存的旧 `http://` 卡台地址，运行时照常使用，只在下次后台保存时被拒（避免升级后卡台立即不可用）。来源：审计 S1 修复报告。
- 批量兑换上限 100 张，恰好用满公开接口每 IP 每小时 300 次变更额度；同一 IP 多人共用时仍可能触顶。批量页验证和提交阶段遇到 429 显示为各条目的错误，没有页面级提示（轮询阶段有提示和退避）。来源：审计 S4 修复报告、用户决定 U2。
- 降级在线更新（目标 ≤1.5.3）照常更新，跳过 `--check`。来源：裁定 R18。
- 支付按钮没点到（确定没点击）时，也走 10 分钟确认、转待人工和 Telegram 告警，并用掉一次卡。方向安全，没有真实页面证据前不动资金状态机。来源：PR #2 终审 M-8、裁定 R47。
- 付款确认的几处极端时序维持现状：补拉等锁不看 ctx（`cardpool/backfill.go` 的 `backfillMu`，不会永远卡住，但超时后会把 `last_error` 写成 context deadline exceeded）；定论时的账号证据可能已过去 30 秒以上；补拉成功不等于窗口完整（窗口上界没有时钟容差，单段 200 页封顶后当作完整）。极端情况下账号刚开通却被定失败，运营可在详情里看到。来源：PR #2 终审复审、裁定 R54。
- 注资金额（美元）够不够付应付金额（本币）无法自动核对，没有汇率，由管理员按试运行价格配置。来源：PR #2 终审、裁定 R49。
- 代理商下单或调账结果不明时，请求号只存在浏览器 localStorage，换浏览器、换电脑就找不回（用户决定，CHANGELOG 已写）。来源：PR #26 用户决定。
- 代理商功能开关只管 `/api/v1/agent/*`（`/status` 除外）、`/shared/*` 和管理员代理商接口，不管老的代理换码 `/partner/swap` 和后台代理换码（用户选的）。来源：PR #29。
- 异次元签名错误按「同一商户 ID + IP 10 次锁 15 分钟、未知商户 ID 按 IP 计」实现，重置密钥或重新开通后，最长 15 分钟内仍会被拒（对接页和 CHANGELOG 有提示）。以后可以改成「签名正确即解锁」。来源：PR #28 终审裁定。
- 异次元同号重发时原样交出第一次的码，即使其中有之后被换码停用的，不改指向新码（规格 7.5「拿回第一次的码」）。下游可能发出已停用的旧码，子项目 5 自动换码时再考虑。来源：PR #28 Task 5 裁定。
- `/shared/*` 只收 HTTPS；本机直连只在请求不带任何转发头（X-Forwarded-Proto、X-Forwarded-For、Forwarded、X-Real-IP）时才豁免。同机反代没配 `TRUSTED_PROXIES` 的部署会被全部拒绝。来源：PR #28 Task 4 裁定。
- 异次元失败记录只保留 30 天，管理员端只看最近 50 条（CHANGELOG 已写）。来源：PR #28 Task 7 裁定、终审。

---

## CodeQL 标了「不修」的告警

这些告警已在 GitHub 上标为不修。以后改到相关代码，CodeQL 可能按新行号再开同类告警，按同一理由标「不修」即可。

| 告警 | 规则 | 理由 |
|---|---|---|
| #5 | `js/clear-text-storage-of-sensitive-data` | 误报，分诊时确认没有干净的改法。 |
| #14、#15、#22、#23、#28 | `go/cookie-secure-not-set` | 设计取舍：`Secure` 按请求是否 HTTPS 动态下发（HTTPS 判断经 `TRUSTED_PROXIES`），这样纯 HTTP 的内网或测试部署也能登录；`clearAuthCookies` 清 cookie 时 `Secure` 固定为 false，HTTPS 下浏览器照样能清掉。 |
| #24 | `go/cookie-httponly-not-set` | 设计取舍，同一类 cookie 下发写法（PR #26 引入），用户同意标不修。 |
| #16 | `go/weak-sensitive-data-hashing` | 旧的 SHA-256 密码哈希只用来核对一次，核对通过后立即升级成 bcrypt，不用它生成新哈希。 |
