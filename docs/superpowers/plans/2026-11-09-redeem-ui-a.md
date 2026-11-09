# 用户端兑换改版（子项目 A）实施计划

> **给执行代理：** 必须配合子技能 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans，逐个任务执行。步骤用复选框（`- [ ]`）记录进度。

**目标：** 买家拿到卡密后，只看页面上的字就能自己开通：首页一个输入框分流，兑换五屏，MarsWallex 码在付款前就告诉买家账号行不行。

**架构：**
- 后端分三块：
  - 把付款引擎里「账号能不能开通、能不能加点数」的判断抽成纯函数；
  - 新增只读的 `InspectAccount`，MarsWallex 码预检时调用它，结果带缓存和每码限流；
  - 站点设置加主色、Logo，`theme_mode` 默认改为 `auto`。
- 前端分两块：
  - 新增一套只挂在用户端根节点的颜色变量，和几个纯函数模块（登录信息检查、设备识别、进度对应、账号核对状态）；
  - 兑换流程的状态和接口调用集中在 `useRedeemFlow`，各屏组件只负责显示。`RechargeView` 只按状态切屏。

**技术栈：** Go 1.x + gin + SQLite（后端）；Vue 3 + vue-i18n 11 + Tailwind + vitest 2（前端，测试环境没有 DOM，组件测试用 `vue/server-renderer` 的 `renderToString`）。

**规格：** `docs/superpowers/specs/2026-11-09-redeem-ui-a-design.md`（以下简称「规格」）。执行者两份都要读：计划写的是决定，规格写的是要求和文案原文。

## 全局约束

- 后端下单、付款、确认开通、关闭续费的流程一行不动；付款引擎原有测试全部原样通过（规格 §2、§5.3）。
- 判断函数返回 `*stepFailure`。调用方必须写 `if f := xxx(acc); f != nil { return f }`，**不能**直接 `return xxx(acc)`：类型化的 nil 指针放进 `error` 接口后不等于 nil。
- Session 原文、sessionToken、accessToken 不写日志、不进缓存、不出现在返回内容里。缓存键用码 ID 和邮箱的 SHA-256。日志里的邮箱用 `maskEmailForAdmin`（规格 §5.6）。
- 预检查账号：整次最多等 20 秒；每码每小时最多 10 次；`eligible`、`ineligible` 缓存 5 分钟，`unknown`、`session_invalid` 不缓存；被 Cloudflare 拦截只写日志、不告警（规格 §5.2、§5.5、§5.6）。
- 颜色变量值逐字取规格 §3.1 表。字体栈逐字取规格 §3.2。用户端不加载任何 Google 字体。
- 尺寸：
  - 大标题 28px（窄屏 24px），按钮 18px，正文 15–16px，说明 13–14px；
  - 按钮和输入框至少 48px 高，圆角 14px，输入框边 3px 主色；
  - 电脑上居中一栏，最宽 520px，内容左对齐；
  - 焦点框主色 3px；设了「减少动态效果」时打勾和转圈不做动画（规格 §3.3）。
- 主色：必须是 `#RRGGBB`，白字对比度不低于 4.5:1。不够时提示「这个颜色太浅，按钮上的白字看不清，请选深一点的颜色」。6 个预设：深青 #0F6B5F、海蓝 #1E4FA8、墨紫 #5B3FA0、枣红 #A3283B、松绿 #2F6B2F、石墨 #36454F（规格 §4.1、§4.2）。
- Logo：只收 PNG、JPEG、WebP，按文件头判断类型，不超过 256 KB，不收 SVG（规格 §4.3）。
- 本机存储：
  - `localStorage` 键 `theme-mode`，读写都包 try/catch；
  - `sessionStorage` 键 `cdk_redeem_progress_v2`，启动时删掉 `cdk_redeem_progress_v1`，超过 7 天丢弃。不存登录信息原文、邮箱密码、Claude Organization ID、Grok 登录态、须知勾选、最近付款和支付方式（规格 §7）。
- 用户端禁用词（只查显示给买家的文字）：
  - 中文：预检、通道、轮询、阶段、上游、卡台、Session JSON、sessionToken、Access Token；
  - 英文：preflight、upstream、polling、stage、channel、session token、access token，不区分大小写（规格 §6.7）。
- 中文、英文两套语言包键完全一致；新文案放在 `redeemUi` 下（规格 §7）。
- 所有出错文字用界面的口吻，写清楚发生了什么、下一步做什么，不道歉、不含糊（规格 §8）。
- 测试全部用假数据，不调真实 ChatGPT，不付钱（规格 §9）。先写测试、看它失败，再写代码。
- 前端：
  - 第一次跑测试前在 `frontend/` 运行 `npm ci`；
  - 测试命令 `npx vitest run`，构建 `npm run build`；
  - 测试文件放在被测代码同级的 `__tests__/` 下（`src/lib/__tests__/*.test.ts`、`src/views/__tests__/*.spec.ts`）。
- 后端：在 `backend/` 运行 `go test -race ./...`；paybrowser 包单独跑，用 `go test -race -timeout 30m ./internal/paybrowser/...`。
- 仓库规矩：
  - 回复和文案用简体中文；
  - 提交用明确路径 `git add <路径>`，不用 `git add -A`，不提交 `.claude/`、`.superpowers/`；
  - 提交信息末尾加 `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`；
  - 不碰 18080 端口和仓库的 `data/` 目录。

## 计划层面的决定（对照规格的调整）

1. **点数码判断拆成两个纯函数**：`creditAccountEligibility`（账号 ID、订阅、套餐、个人账号）和 `creditBillingEligibility`（欠费、币种）。规格写的是一个 `creditEligibility`，但引擎只在前四条都通过后才调 `SubscriptionStatus`；合成一个会在引擎本来已经停下的地方多打一次 ChatGPT。「规则只写一份」不变。
2. **`InspectAccount` 的 `error` 只在调用方的 ctx 已取消时返回**（买家断开），这时处理函数不再回复。其余结果都放在 `AccountVerdict` 里，查不了的原因只记类别（`Cause`），不带错误原文。
3. **每码限流复用 `localRateAllow`**：新增 kind `inspect`，额度 10，键是 `cdk:<码ID>`（`rateIPKey` 对不是 IP 的串原样返回）。命中缓存时不计数。
4. **币种不一致时预检多返回 `account_currency`、`code_currency`**：规格 §6.4 的文案要用这两个值，§5.4 没列。
5. **默认主色 #0F6B5F 的暗色版直接用 #3FB8A5**（规格 §3.1 定的值），其余颜色按算法推。
6. **`progressRows` 多一个可选参数 `prev`**：认不出的状态保持上一次的行（规格 §6.5「保持当前行」）；没有上一次时按第 1 行进行中。返回值多一个 `reviewing` 标记，对应 ZovoCard `_review` 状态的小字。
7. **新增两个规格没列的纯函数模块**：
   - `src/lib/redeemAccount.ts`：预检结果转账号摘要、第 ③ 屏状态判断、放弃码转文案键；
   - `src/lib/homeRoute.ts`：首页分流。

   另外新增 `src/components/redeem/UserShell.vue`，挂用户端根节点上的颜色变量和亮暗属性。这些逻辑都要能在没有 DOM 的测试环境里测。
8. **「不符合时拿旧票也兑换不了」**按「不符合时返回里没有预检票；兑换时不带票或带伪造的票被拒（400）」来测。预检票本身的签发和校验不改。

## 重点核查（测试没直接覆盖、最容易坑到人的输入）

1. 格式化过的多行 JSON（火狐「原始数据」、手动美化过的）、首尾带空白或换行：严格检查也要认。→ 任务 7 加测试。
2. 很短的邮箱 `a@x.com`、`ab@x.com`：只露 1 个字符，和后端 `maskEmailForAdmin` 一致（PR #36）。→ 任务 7 加测试。
3. 后台填了极深、极亮或没有饱和度的自定义主色（`#000000`、`#36454F`、`#0000FF`）：暗色主色推导要能结束，并满足 4.5:1。→ 任务 6 加测试。
4. 在第 ③ 屏刷新页面：账号摘要能恢复，缓存里没有登录信息原文；点开通用缓存的预检票，票过期就回第 ② 屏。→ 任务 9 加测试。
5. 首页回车和点按钮连着来、或网速慢时连点「继续」：只发一次售后查询。→ 任务 10 加测试。

---

### 任务 1：抽出账号判断规则

**文件：**
- 新建：`backend/internal/payengine/eligibility.go`
- 新建：`backend/internal/payengine/eligibility_test.go`
- 修改：`backend/internal/payengine/worker.go:556-558`（`check` 末尾的开通单判断）
- 修改：`backend/internal/payengine/credit.go:33-84`（`creditPlanTypes`、`defaultBillingCurrency` 移到新文件；`checkCredit` 改为调用新函数）

**接口：**
- 产出（同包，不导出）：
  - `func subscriptionEligibility(acc chatgpt.Account) *stepFailure`
  - `func creditAccountEligibility(acc chatgpt.Account) *stepFailure`
  - `func creditBillingEligibility(acc chatgpt.Account, delinquent *bool, currency string) *stepFailure`
- 放弃码和说明文字逐字照搬 `worker.go:557`、`credit.go:44-82` 现有的 `abort(...)` 调用。`SubscriptionStatus` 的调用和它出错时的处理（`chatgptAbort` / `abort("credit_delinquency_unknown", ...)`）留在 `checkCredit` 里，放在两个函数之间。

- [ ] **第 1 步：写失败的测试** `eligibility_test.go`，三个表格测试：

```go
func TestSubscriptionEligibility(t *testing.T) {
	if f := subscriptionEligibility(chatgpt.Account{PlanType: "free"}); f != nil { t.Fatalf("没有订阅应放行: %v", f) }
	f := subscriptionEligibility(chatgpt.Account{PlanType: "plus", HasActiveSubscription: true})
	if f == nil || f.code != "already_subscribed" || !strings.Contains(f.msg, "plus") { t.Fatalf("%v", f) }
}

func TestCreditAccountEligibility(t *testing.T) {
	ok := creditAccount() // credit_test.go 里现成的：个人 Plus、PHP、没有欠费
	for _, tc := range []struct{ name string; mut func(*chatgpt.Account); code, msgHas string }{
		{"账号ID空", func(a *chatgpt.Account) { a.AccountID = " " }, "credit_account_unknown", "账号 ID"},
		{"没订阅", func(a *chatgpt.Account) { a.HasActiveSubscription = false }, "credit_no_subscription", "生效中的订阅"},
		{"team 套餐", func(a *chatgpt.Account) { a.PlanType = "team" }, "credit_plan_ineligible", `"team"`},
		{"套餐大小写和空白", func(a *chatgpt.Account) { a.PlanType = " PRO " }, "", ""},
		{"没返回账号类型", func(a *chatgpt.Account) { a.Structure = "" }, "credit_not_personal", "没有返回账号类型"},
		{"不是个人账号", func(a *chatgpt.Account) { a.Structure = "workspace" }, "credit_not_personal", "workspace"},
		{"符合", func(*chatgpt.Account) {}, "", ""},
	} { /* 改一份 ok 的副本，断言 code 和 msg 包含 msgHas；code 为空时断言返回 nil */ }
}

func TestCreditBillingEligibility(t *testing.T) {
	// delinquent=nil → credit_delinquency_unknown（msg 含「都没有返回欠费状态」）
	// delinquent=true → credit_delinquent
	// false，BillingCurrency=""，currency="USD" → nil（缺省按 USD）
	// false，BillingCurrency=""，currency="PHP" → credit_currency_mismatch，msg 同时含 "USD" 和 "PHP"
	// false，BillingCurrency="php"，currency="PHP" → nil（不分大小写）
}
```

- [ ] **第 2 步：运行，确认失败**

运行：`cd backend && go test ./internal/payengine/ -run 'Eligibility' -count=1`
预期：编译失败，提示 `undefined: subscriptionEligibility`。

- [ ] **第 3 步：实现三个函数**，然后把 `check`、`checkCredit` 改成调用它们，放弃码和文字不变。

- [ ] **第 4 步：运行新测试和付款引擎全部测试**

运行：`go test -race -count=1 ./internal/payengine/`
预期：PASS（原有测试一个不改）。

- [ ] **第 5 步：提交**

```bash
git add backend/internal/payengine/eligibility.go backend/internal/payengine/eligibility_test.go backend/internal/payengine/worker.go backend/internal/payengine/credit.go
git commit -m "refactor(payengine): 账号能否开通、能否加点数的判断抽成纯函数"
```

---

### 任务 2：付款引擎新增只读的 `InspectAccount`

**文件：**
- 新建：`backend/internal/payengine/inspect.go`
- 新建：`backend/internal/payengine/inspect_test.go`

**接口：**
- 用到：任务 1 的三个判断函数；`ResolvePlan(planKey, region, false)` 的 `RegionConf.Currency`；`LoadProxies()`；`s.opts.ChatGPT`；`chatgpt.ParseSession`、`chatgpt.ErrUnauthorized`、`chatgpt.ErrBlocked`、`*chatgpt.HTTPError`；`PlanKind(planKey) == KindCredit`。
- 产出（导出，任务 3 用）：

```go
type InspectRequest struct{ PlanKey, Region, Session string } // Session 是买家粘贴的原文

type VerdictKind string

const (
	VerdictEligible       VerdictKind = "eligible"
	VerdictIneligible     VerdictKind = "ineligible"
	VerdictSessionInvalid VerdictKind = "session_invalid"
	VerdictUnknown        VerdictKind = "unknown"
)

type AccountVerdict struct {
	Kind                  VerdictKind
	Email                 string // ParseSession 得到的邮箱（已是小写）；调用方自己脱敏
	CurrentPlan           string // acc.PlanType 转小写，空时为 "free"
	HasActiveSubscription bool
	BlockingCode          string // 只在 ineligible 时有值，取自判断函数的放弃码
	AccountCurrency       string // 只在 credit_currency_mismatch 时有值（缺省按 USD）
	CodeCurrency          string // 码地区的币种
	Cause                 string // 只在 unknown 时有值：plan、proxy、client、cloudflare、rate_limited、timeout、network、engine；任务 3 的处理函数另会设 limit
}

const inspectTimeout = 20 * time.Second

func (s *Service) InspectAccount(ctx context.Context, r InspectRequest) (AccountVerdict, error)
```

- 行为：
  - 步骤顺序照规格 §5.2，全部只读：在会话副本上 `Refresh`，不写库，不改买家粘贴的原文。
  - 整次查询用 `context.WithTimeout(ctx, inspectTimeout)`。
  - 解析失败，或 `Refresh`、`Account`、`SubscriptionStatus` 返回 `ErrUnauthorized`：结论 `session_invalid`。
  - 返回 `ErrBlocked`：结论 `unknown`，`Cause` 为 `cloudflare`。只写日志，**不调 `opts.Alert`**。
  - `*HTTPError` 且状态码是 429：`rate_limited`。
  - 自己设的超时（`context.DeadlineExceeded`，且调用方的 ctx 没结束）：`timeout`。
  - 其余错误：`network`。
  - `ResolvePlan` 出错：`plan`；`LoadProxies` 出错：`proxy`；建客户端出错：`client`；`s == nil`：`engine`。
  - 调用方的 ctx 已结束时，返回 `(AccountVerdict{}, ctx.Err())`。

- [ ] **第 1 步：写失败的测试** `inspect_test.go`。用 `newHarness`（plus/PH）和 `newCreditHarness`（credit250/PH），通过 `h.gpt` 控制假账号：

```go
func TestInspectAccountSubscription(t *testing.T)    // free 账号 → eligible，CurrentPlan=="free"；HasActiveSubscription → ineligible + already_subscribed，CurrentPlan=="plus"
func TestInspectAccountCredit(t *testing.T)          // creditAccount() → eligible；任务 1 每条不符合各一行 → ineligible + 对应放弃码；币种不一致时 AccountCurrency=="USD"、CodeCurrency=="PHP"
func TestInspectAccountCreditCallsSubscriptionStatus(t *testing.T) // IsDelinquent=nil 时调一次 SubscriptionStatus（h.gpt 的调用记录里有），返回 IsDelinquent=false → eligible
func TestInspectAccountSessionInvalid(t *testing.T)  // 原文 "{}"、refreshErr=ErrUnauthorized、accountErr=ErrUnauthorized → session_invalid
func TestInspectAccountUnknown(t *testing.T)         // 代理表读不到、建客户端出错、accountErr=ErrBlocked、&chatgpt.HTTPError{Status:429}、普通网络错误、accountCtx 阻塞到 ctx 结束（把 inspectTimeout 改成变量便于测试，或在测试里传已设短超时的 ctx）→ unknown 且 Cause 分别为 proxy/client/cloudflare/rate_limited/network/timeout；ErrBlocked 时 h.alerts 里没有新告警
func TestInspectAccountCallerCanceled(t *testing.T)  // 传入已取消的 ctx → err == context.Canceled
func TestInspectAccountNilService(t *testing.T)      // var s *Service；InspectAccount → unknown、Cause=="engine"、err==nil
func TestInspectAccountReadOnly(t *testing.T)        // 结束后 h.gpt 的调用记录里只有 New、Refresh、Account（点数码可多一次 SubscriptionStatus）；库里没有新订单
```

- [ ] **第 2 步：运行，确认失败**

运行：`go test ./internal/payengine/ -run 'InspectAccount' -count=1`
预期：编译失败，提示 `undefined: InspectRequest`。

- [ ] **第 3 步：实现 `inspect.go`**。可以把错误归类写成一个小函数 `inspectCause(err error) string`。为了测超时，`inspectTimeout` 可以改成包级变量，测试里临时调小。

- [ ] **第 4 步：运行**

运行：`go test -race -count=1 ./internal/payengine/`
预期：PASS。

- [ ] **第 5 步：提交**

```bash
git add backend/internal/payengine/inspect.go backend/internal/payengine/inspect_test.go
git commit -m "feat(payengine): 新增只读的账号核对 InspectAccount，规则与付款时共用"
```

---

### 任务 3：MarsWallex 码预检时核对账号

**文件：**
- 新建：`backend/internal/handler/local_cdk_inspect.go`
- 新建：`backend/internal/handler/local_cdk_inspect_test.go`
- 修改：`backend/internal/handler/local_cdk.go:50-57`（加 `localRateInspect = "inspect"`，额度 10）、`:378-436`（`localCDKPreflight`）

**接口：**
- 用到：任务 2 的 `payEngine.InspectAccount`、`AccountVerdict`；`localRateAllow`；`maskEmailForAdmin`；`sealLocalPreflight`；`localReply`。
- 产出（同包）：
  - `var inspectCache`：5 分钟，最多 5000 条，满了先清过期的，再淘汰最早的。时钟 `var inspectNow = time.Now`，测试可换。
  - `func inspectCacheKey(cdkID int64, email string) string`：`hex(sha256("<id>|<小写邮箱>"))`。
  - `func inspectLocalAccount(ctx context.Context, row db.LocalCDK, raw, email string) (payengine.AccountVerdict, error)`：
    - 先查缓存，命中不计数；
    - 再过 `localRateAllow(localRateInspect, "cdk:"+id)`，超了按 `unknown`（Cause `limit`）；
    - `payEngine == nil` 时按 `unknown`；
    - 只缓存 `eligible` 和 `ineligible`；
    - 每次查询写一行日志。只记码 ID、脱敏邮箱、当前套餐、结论、放弃码、查不了的类别。
  - `func inspectBlockingReason(v payengine.AccountVerdict) string`：中文逐字取规格 §6.4 的放弃码文案表，`{当前套餐}` 用 `CurrentPlan`，币种用 `AccountCurrency`、`CodeCurrency`；表里没有的放弃码用「这个账号暂时不能用这张码，请换一个账号。」。
- 预检的返回（规格 §5.4），在 `crossProviderBlocked` 之后、`sealLocalPreflight` 之前调用 `inspectLocalAccount`：
  - `session_invalid`：400，`{"error_code":"session_invalid","error":"登录信息已失效，请回到 ChatGPT 刷新那个页面，再重新复制"}`。
  - `ineligible`：200，**不发** `preflight_token`。带 `account_checked:true`、`current_plan`、`subscription_has_active`、`blocking_code`、`blocking_reason`；币种不一致时另带 `account_currency`、`code_currency`。原有的 `provider`、`email`、`account_email`、`localKindFields` 照旧。
  - `eligible`：照常发票，另带 `account_checked:true`、`current_plan`、`subscription_has_active`。
  - `unknown`：照常发票，另带 `account_checked:false`。
  - 调用方的 ctx 已取消：直接返回，不回复。
  - 删掉第 422 行「这里没查账号」那条注释，改写成现在的行为。

- [ ] **第 1 步：写失败的测试** `local_cdk_inspect_test.go`。照 `local_cdk_env_test.go` 的做法（`localFakeGPT`、`payengine.New(...)`、`SetPayEngine`）搭环境：

```go
func TestLocalPreflightEligible(t *testing.T)            // 200，有 preflight_token，account_checked==true，current_plan=="free"，subscription_has_active==false
func TestLocalPreflightIneligibleNoTicket(t *testing.T)  // 已订阅账号：200，没有 preflight_token 键，blocking_code=="already_subscribed"，blocking_reason 含「plus」；随后带 preflight_token:"" 调兑换 → 400
func TestLocalPreflightCurrencyMismatch(t *testing.T)    // 点数码，账号 USD、码 PHP：带 account_currency=="USD"、code_currency=="PHP"
func TestLocalPreflightSessionInvalid(t *testing.T)      // Account 返回 ErrUnauthorized：400，error_code=="session_invalid"
func TestLocalPreflightUnknownStillTickets(t *testing.T) // Account 返回 429：200，有 preflight_token，account_checked==false
func TestLocalPreflightEngineMissing(t *testing.T)       // SetPayEngine(nil)：200，有票，account_checked==false
func TestLocalPreflightPerCodeLimit(t *testing.T)        // 换 10 个不同邮箱让缓存不命中，第 11 次不再调 ChatGPT（假客户端的 Account 调用次数停在 10），返回 account_checked==false
func TestLocalPreflightCache(t *testing.T)               // 同码同邮箱 5 分钟内第二次不调 ChatGPT；把 inspectNow 拨到 5 分 1 秒后再调一次；unknown（429）不缓存，第二次仍调
func TestLocalPreflightLogNoSecrets(t *testing.T)        // 抓 log 输出：不含测试用 sessionToken、accessToken、完整邮箱，含脱敏邮箱
```

- [ ] **第 2 步：运行，确认失败**

运行：`go test ./internal/handler/ -run 'LocalPreflight' -count=1`
预期：FAIL，提示缺 `account_checked`；`IneligibleNoTicket` 能看到 `preflight_token`。

- [ ] **第 3 步：实现** `local_cdk_inspect.go`，并接进 `localCDKPreflight`。

- [ ] **第 4 步：运行 handler 包全部测试**

运行：`go test -race -count=1 ./internal/handler/`
预期：PASS。原有预检测试如果断言「返回字段只有这些」，按新字段补上，并在报告里列出改了哪些测试。

- [ ] **第 5 步：提交**

```bash
git add backend/internal/handler/local_cdk_inspect.go backend/internal/handler/local_cdk_inspect_test.go backend/internal/handler/local_cdk.go
git commit -m "feat(handler): MarsWallex 码预检时核对账号，不符合不发预检票"
```

---

### 任务 4：售后查询认出 ZovoCard 的 `failed_precharge`

**文件：**
- 修改：`backend/internal/handler/lookup.go:273`（任务表分支）、`:348`（订单分支）
- 测试：`backend/internal/handler/aftersale_zovo_test.go`

**接口：** 只把 `"failed_precharge"` 加进两处失败分支；可不可重提照旧由 `zovoMarkFailed` 判断（规格 §6.9）。

- [ ] **第 1 步：写失败的测试**。参照同文件里 `StatusMapping`（约 :261）的写法，加两条：

```go
func TestAfterSaleZovoFailedPrechargeOrder(t *testing.T) // 上游订单 status=failed_precharge、码可再用 → facts.Status=="failed"，Reusable==true（不是 processing）
func TestAfterSaleZovoFailedPrechargeTask(t *testing.T)  // 任务表 status=failed_precharge → 同上
```

- [ ] **第 2 步：运行，确认失败**

运行：`go test ./internal/handler/ -run 'FailedPrecharge' -count=1`
预期：FAIL，得到的是 `processing`。

- [ ] **第 3 步：改两处 case。**

- [ ] **第 4 步：运行**

运行：`go test -race -count=1 ./internal/handler/ -run 'AfterSale|Lookup'`
预期：PASS。

- [ ] **第 5 步：提交**

```bash
git add backend/internal/handler/lookup.go backend/internal/handler/aftersale_zovo_test.go
git commit -m "fix(aftersale): ZovoCard 扣款前失败不再一直显示处理中"
```

---

### 任务 5：站点外观后端：主色、Logo、默认跟随系统

**文件：**
- 新建：`backend/internal/handler/site_color.go`、`site_color_test.go`
- 新建：`backend/internal/handler/site_logo.go`、`site_logo_test.go`
- 修改：`backend/internal/handler/settings.go:26-40`（默认值、`PublicSiteConfig`）、`:43-123`（`AdminGetSettings`、`adminSettingsBody`、`AdminPutSettings` 校验）、`:233`（`writeSiteSettings`）
- 修改：`backend/internal/server/http.go:275` 一带（公开路由）、`:465` 一带（管理员路由）
- 原有设置测试：如果有断言 `theme_mode` 默认为 `dark` 的，改成 `auto`

**接口：**
- `site_color.go`：
  - `func contrastRatio(a, b string) (float64, error)`：WCAG 2.x 相对亮度公式，输入 `#RRGGBB`。
  - `func validPrimaryColor(hex string) error`：
    - 格式不对时返回「主色要写成 #RRGGBB 的色号」；
    - 白字对比度不到 4.5 时返回「这个颜色太浅，按钮上的白字看不清，请选深一点的颜色」。
- `settings.go`：
  - `defaultThemeMode = "auto"`；`defaultPrimaryColor = "#0F6B5F"`；设置键 `primary_color`；
  - `adminSettingsBody` 加 `PrimaryColor *string \`json:"primary_color"\``，保存时转大写；
  - `PublicSiteConfig`、`AdminGetSettings` 加 `primary_color` 和 `logo_version`。
- `site_logo.go`：
  - 设置键 `site_logo`（标准 base64）和 `site_logo_type`；`const siteLogoMaxBytes = 256 << 10`；
  - `func siteLogoVersion(b []byte) string`：SHA-256 前 12 位十六进制，没有 Logo 时为空串；
  - `func AdminUploadSiteLogo(c *gin.Context)`：
    - multipart 字段 `file`；`http.MaxBytesReader` 限 `siteLogoMaxBytes + 64<<10`；最多读 `siteLogoMaxBytes+1` 字节，超了 400「Logo 不能超过 256 KB」；
    - `http.DetectContentType` 只认 `image/png`、`image/jpeg`、`image/webp`，其余 400「Logo 只能是 PNG、JPG 或 WebP 图片」；
    - 用完 `MultipartForm.RemoveAll()`；`auditAdmin(c, "update_site_logo", "<类型> <字节数>")`；返回 `{"logo_version": ...}`。参照 `cardpool_holders.go:36-78` 的写法。
  - `func AdminDeleteSiteLogo(c *gin.Context)`：删两个键，`auditAdmin(c, "delete_site_logo", "")`。
  - `func PublicSiteLogo(c *gin.Context)`：
    - 没有 Logo 时 404；
    - 有 Logo 时：`Content-Type` 用存下的类型，`X-Content-Type-Options: nosniff`，`ETag: "<version>"`，`Cache-Control: public, max-age=300`；`If-None-Match` 相同时 304。
- 路由：`public.GET("/site/logo", handler.PublicSiteLogo)`；`admin.POST("/settings/logo", handler.AdminUploadSiteLogo)`；`admin.DELETE("/settings/logo", handler.AdminDeleteSiteLogo)`。

- [ ] **第 1 步：写失败的测试**

```go
func TestContrastRatio(t *testing.T)            // #FFFFFF 对 #0F6B5F ≈ 6.39（误差 0.01）；黑对白 = 21
func TestValidPrimaryColor(t *testing.T)        // 6 个预设都通过；"#7FD4C4" 报「太浅」；"0F6B5F"、"#0F6B5"、"red" 报格式
func TestPutPrimaryColor(t *testing.T)          // PUT primary_color "#1e4fa8" → 存成 "#1E4FA8"，公开接口返回它；"#7FD4C4" → 400 且不落库
func TestPublicSiteDefaults(t *testing.T)       // 空库：theme_mode=="auto"，primary_color=="#0F6B5F"，logo_version==""
func TestSiteLogoUploadKinds(t *testing.T)      // 最小 PNG、JPEG、WebP 文件头 → 200；SVG 文本、GIF89a、扩展名写 .png 的纯文本、256KB+1 字节 → 400，库里不变
func TestSiteLogoPublicHeaders(t *testing.T)    // 上传 PNG 后 GET：Content-Type image/png，nosniff，ETag 与 logo_version 一致，Cache-Control 正确；带 If-None-Match → 304
func TestSiteLogoDeleteAndAudit(t *testing.T)   // 上传、删除各写一条审计（admin_audit_logs）；删除后公开读取 404，logo_version==""
```

- [ ] **第 2 步：运行，确认失败**

运行：`go test ./internal/handler/ -run 'Contrast|PrimaryColor|PublicSiteDefaults|SiteLogo' -count=1`
预期：编译失败（`undefined: contrastRatio`）。

- [ ] **第 3 步：实现**，接上路由。

- [ ] **第 4 步：运行**

运行：`go test -race -count=1 ./internal/handler/ ./internal/server/`
预期：PASS。

- [ ] **第 5 步：提交**

```bash
git add backend/internal/handler/site_color.go backend/internal/handler/site_color_test.go backend/internal/handler/site_logo.go backend/internal/handler/site_logo_test.go backend/internal/handler/settings.go backend/internal/server/http.go
git commit -m "feat(settings): 站点外观加主色和 Logo，亮暗默认跟随系统"
```

（原有设置测试如果改了，把文件一起加进 `git add`。）

---

### 任务 6：用户端颜色变量与主色推导

**文件：**
- 新建：`frontend/src/styles/user-theme.css`
- 新建：`frontend/src/lib/brandColor.ts`、`frontend/src/lib/__tests__/brandColor.test.ts`
- 新建：`frontend/src/components/redeem/UserShell.vue`
- 修改：`frontend/src/theme.ts`：
  - `:215` 默认值 `'dark'` 改为 `'auto'`，读 `theme-mode` 包 try/catch；
  - `setTheme` 写入也包 try/catch；
  - `SiteBrand` 加 `primary?: string; logoVersion?: string`。
- 修改：`frontend/src/main.ts:14-28`：读 `primary_color`、`logo_version` 进 `setSiteBrand`；`localStorage.getItem('theme-mode')` 包 try/catch。
- 修改：`frontend/index.html`：删第 11、12 行两条 Google 预连接；第 23 行一带读 `theme-mode` 的内联脚本，没有记录时按 `auto` 处理。
- 修改：`frontend/src/main.ts` 或入口样式：引入 `user-theme.css`。

**接口：**
- `brandColor.ts`：
  - `contrastRatio(a: string, b: string): number`
  - `isUsablePrimary(hex: string): boolean`：格式是 `#RRGGBB`，且对 `#FFFFFF` 的对比度不低于 4.5。
  - `darkPrimaryFrom(hex: string): string`：
    - 输入 `#0F6B5F`（不分大小写）时直接返回 `#3FB8A5`；
    - 其余保持色相，HSL 亮度每次加 0.02，直到对 `#0C1A18` 的对比度不低于 4.5；亮度到 1 还不够就返回 `#FFFFFF`。
  - `mixHex(a: string, b: string, weightOfA: number): string`
  - `tintFrom(hex: string): string`：`mixHex(hex, '#FFFFFF', 0.12)`。
  - `brandColorVars(hex: string | undefined): Record<'--brand-primary' | '--brand-primary-dark' | '--brand-tint' | '--brand-tint-dark', string>`：
    - 不可用或为空时按 `#0F6B5F`；
    - `--brand-tint-dark` 为 `mixHex(darkPrimary, '#132522', 0.2)`。
- `user-theme.css`：
  - 在 `.user-ui` 上定义 `--u-bg`、`--u-surface`、`--u-ink`、`--u-muted`、`--u-line`、`--u-primary`、`--u-on-primary`、`--u-tint`、`--u-warn-bg`、`--u-warn-ink`、`--u-error-bg`、`--u-error-ink`，值取规格 §3.1。`--u-primary: var(--brand-primary, #0F6B5F)`，`--u-tint` 同理。
  - 暗色有两处：`.user-ui[data-theme="dark"]`，以及 `@media (prefers-color-scheme: dark) { .user-ui[data-theme="auto"] { … } }`。暗色的 `--u-primary` 取 `var(--brand-primary-dark, #3FB8A5)`。
  - 同一文件定义用户端公共类：`.u-page`（居中一栏、最宽 520px、16px 边距）、`.u-title`、`.u-help`、`.u-card`、`.u-input`、`.u-btn-primary`、`.u-btn-secondary`、`.u-link`、`.u-warn`、`.u-error`、`.u-mono`。
  - 尺寸、字体、焦点框、减少动态效果，照全局约束。
- `UserShell.vue`：
  - 根节点 `<div class="user-ui min-h-screen" :data-theme="themeMode" :style="brandColorVars(siteBrand.primary)"><slot /></div>`；
  - 背景用 `var(--u-bg)`，文字用 `var(--u-ink)`。

- [ ] **第 1 步：写失败的测试** `brandColor.test.ts`

```ts
it('对比度', () => { expect(contrastRatio('#FFFFFF', '#0F6B5F')).toBeCloseTo(6.39, 1); expect(contrastRatio('#000000', '#FFFFFF')).toBeCloseTo(21, 0) })
it('预设都可用，浅色不可用', () => {
  for (const c of ['#0F6B5F', '#1E4FA8', '#5B3FA0', '#A3283B', '#2F6B2F', '#36454F']) expect(isUsablePrimary(c)).toBe(true)
  for (const c of ['#7FD4C4', '0F6B5F', '#0F6B5', 'red']) expect(isUsablePrimary(c)).toBe(false)
})
it('暗色主色', () => {
  expect(darkPrimaryFrom('#0f6b5f')).toBe('#3FB8A5')
  for (const c of ['#1E4FA8', '#5B3FA0', '#A3283B', '#2F6B2F', '#36454F', '#000000', '#0000FF'])
    expect(contrastRatio(darkPrimaryFrom(c), '#0C1A18')).toBeGreaterThanOrEqual(4.5) // 重点核查 3：极深、无饱和度也要能结束
})
it('浅色调', () => { expect(tintFrom('#0F6B5F')).toBe('#E2EDEC') })
it('变量兜底', () => { expect(brandColorVars('#7FD4C4')['--brand-primary']).toBe('#0F6B5F'); expect(brandColorVars(undefined)['--brand-primary-dark']).toBe('#3FB8A5') })
```

- [ ] **第 2 步：运行，确认失败**

运行：`cd frontend && npm ci && npx vitest run src/lib/__tests__/brandColor.test.ts`
预期：FAIL，找不到模块。

- [ ] **第 3 步：实现**本任务的全部文件。

- [ ] **第 4 步：运行全部前端测试和构建**

运行：`npx vitest run && npm run build`
预期：全部 PASS，构建成功；`dist/index.html` 里没有 `fonts.googleapis.com`（`grep -c fonts.googleapis dist/index.html` 输出 0）。

- [ ] **第 5 步：提交**

```bash
git add frontend/src/styles/user-theme.css frontend/src/lib/brandColor.ts frontend/src/lib/__tests__/brandColor.test.ts frontend/src/components/redeem/UserShell.vue frontend/src/theme.ts frontend/src/main.ts frontend/index.html
git commit -m "feat(frontend): 用户端颜色变量与主色推导，亮暗默认跟随系统，去掉 Google 字体预连接"
```

---

### 任务 7：登录信息检查、设备识别、进度对应

**文件：**
- 新建：`frontend/src/lib/sessionCheck.ts`、`frontend/src/lib/device.ts`、`frontend/src/lib/redeemProgress.ts`
- 新建：对应的三个测试 `frontend/src/lib/__tests__/sessionCheck.test.ts`、`device.test.ts`、`redeemProgress.test.ts`

**接口：**
- `sessionCheck.ts`：
  - `type SessionProblem = 'empty' | 'not_logged_in' | 'access_token' | 'not_json' | 'no_token' | 'no_email' | 'expired'`
  - `type SessionCheck = { ok: true; value: string; maskedEmail?: string } | { ok: false; problem: SessionProblem }`
  - `checkPastedSession(raw: string, opts: { strict: boolean; now: number }): SessionCheck`：
    - 规则逐行照规格 §6.3.1 的表。`strict` 为 true 对应 MarsWallex，false 对应 ZovoCard。判断顺序：空 → `eyJ` 开头的三段式 → 不是 JSON → `{}` 或没有字段 → 没有令牌 → 没有邮箱 → 过期。
    - `value` 是去掉首尾空白后的原文。
    - 宽松模式认 `token.sessionToken`，以及「五段式」或「长度超过 40」的非 JSON 串（与现有 `RechargeView.extractSession` 一致）。
  - `maskEmail(email: string): string`：本地部分不超过 2 个字符时只留 1 个，否则留前 2 个，后接 `***@完整域名`；与后端 `maskEmailForAdmin` 一致。
- `device.ts`：`detectDevice(ua: string, maxTouchPoints: number): 'ios' | 'android' | 'desktop'`。iPadOS 报 Macintosh 且 `maxTouchPoints > 1` 时算 `ios`。
- `redeemProgress.ts`：
  - `type RowState = 'done' | 'current' | 'todo'`
  - `type ProgressView = { rows: [RowState, RowState, RowState]; phase: 'running' | 'done' | 'failed' | 'review'; reviewing: boolean }`
  - `progressRows(kind: 'card_supply' | 'upstream', status: string, stage: string, prev?: ProgressView['rows']): ProgressView`：
    - 对应关系逐行照规格 §6.5 两张表；
    - 认不出的值：`rows` 取 `prev`，没有就是 `['current','todo','todo']`，`phase: 'running'`；
    - `phase` 为 `failed`、`review` 时，`rows` 照 `prev`。第 ⑤ 屏不显示这些行。

- [ ] **第 1 步：写失败的测试**
  - `sessionCheck.test.ts`：
    - 规格 §6.3.1 表的每一行，`strict: true` 和 `strict: false` 各测一遍，断言 `ok` 和 `problem`；
    - `expires: "不是日期"` 不算过期；`expires` 早于 `now` 算过期；
    - 重点核查 1：首尾带换行和空格、用 `JSON.stringify(obj, null, 2)` 美化过的多行 JSON，严格模式也通过，`maskedEmail === 'bu***@example.com'`；
    - 重点核查 2：`maskEmail('a@x.com') === 'a***@x.com'`，`maskEmail('ab@x.com') === 'a***@x.com'`，`maskEmail('alice@gmail.com') === 'al***@gmail.com'`。
  - `device.test.ts`：五条真实格式的 UA：
    - iPhone Safari → `ios`；
    - iPad 报 Macintosh 且 `maxTouchPoints: 5` → `ios`；
    - Android Chrome → `android`；
    - Windows Chrome → `desktop`；
    - macOS Safari 且 `maxTouchPoints: 0` → `desktop`。
  - `redeemProgress.test.ts`：
    - 规格 §6.5 两张表里每个 status / stage 组合各一条断言；
    - ZovoCard 的 `card_review` → `['done','current','todo']`、`reviewing: true`、`phase: 'running'`；
    - 认不出的 `weird` → 带 `prev` 时等于 `prev`，不带时为 `['current','todo','todo']`；
    - MarsWallex 的 `checkout` 在 `card_funding` 之前、之后都是第 2 行进行中，不倒退。

- [ ] **第 2 步：运行，确认失败**

运行：`npx vitest run src/lib/__tests__/sessionCheck.test.ts src/lib/__tests__/device.test.ts src/lib/__tests__/redeemProgress.test.ts`
预期：FAIL，找不到模块。

- [ ] **第 3 步：实现三个模块。**

- [ ] **第 4 步：运行，确认通过**

运行：同第 2 步。
预期：PASS。

- [ ] **第 5 步：提交**

```bash
git add frontend/src/lib/sessionCheck.ts frontend/src/lib/device.ts frontend/src/lib/redeemProgress.ts frontend/src/lib/__tests__/sessionCheck.test.ts frontend/src/lib/__tests__/device.test.ts frontend/src/lib/__tests__/redeemProgress.test.ts
git commit -m "feat(frontend): 登录信息本地检查、设备识别和开通进度对应"
```

---

### 任务 8：语言包 `redeemUi` 与键一致性测试

**文件：**
- 修改：`frontend/src/i18n/locales/zh.ts`、`frontend/src/i18n/locales/en.ts`
- 新建：`frontend/src/i18n/__tests__/locale-keys.test.ts`

**接口：** 在两个语言包里加顶层键 `redeemUi`，子键如下。中文逐字取规格对应段落，英文按同一意思写。后面的任务只用这些键，需要新键时在本任务的结构下补，并保持两边一致。

| 子键 | 内容来源 |
|---|---|
| `home.*` | 规格 §6.1：大标题、副标题、卡密在哪、继续、换一张卡密、页脚三个链接 |
| `ticks.*` | 规格 §6.2：核对卡密、`{plan} 码`、确认账号、开通、已开通 |
| `cond.*` | 规格 §6.3：两条条件提醒 |
| `guide.ios.*`、`guide.android.*`、`guide.desktop.*`、`guide.open`、`guide.safety` | 规格 §6.3 设备教程表和安全提醒 |
| `paste.*` | 规格 §6.3.1：每个 `SessionProblem` 一条提示，`ok`（「✓ 认出账号 {email}」）、`okNoEmail`（「✓ 格式正确」） |
| `mailbox.*` | 规格 §6.3：「不会复制？改用邮箱和密码」「改回粘贴登录信息」、邮箱、密码 |
| `next.*` | 「下一步」「正在核对账号…」 |
| `confirm.*` | 规格 §6.4：四种状态的标题、各行标签、勾选框文字、「点下面按钮后就开始付款开通，不能撤回。」、「确认开通 {plan}」「换一个账号」「正在提交…」 |
| `blocking.*` | 规格 §6.4 放弃码文案表，每个放弃码一个键，占位符 `{plan}`、`{accountCurrency}`、`{codeCurrency}` |
| `creditTerms.*` | `mwredeem.ts` 的 `CREDIT_TERMS` 三条，以及勾选文字 |
| `progress.*` | 规格 §6.5：标题「正在开通 {plan}」、三行名称、可以关掉页面、等了 10 分钟、正在人工核对 |
| `result.*` | 规格 §6.6：成功标题、续费两种状态、点数、刷新说明、四个打开按钮；失败可重提标题、次数提醒、转人工标题和说明、复制卡密 |
| `errors.*` | 规格 §8：没连上、再试一次、操作太频繁、核对已过期、没收到回复、查一下结果、首页三种出错 |

同时改正 X 的说明，删掉 ct0、billing_email，只要 auth_token（规格 §6.3）。

- [ ] **第 1 步：写失败的测试** `locale-keys.test.ts`

```ts
function keys(o: any, p = ''): string[] { return Object.entries(o).flatMap(([k, v]) => v && typeof v === 'object' ? keys(v, p + k + '.') : [p + k]) }
it('中英文键完全一致', () => { expect(keys(en).sort()).toEqual(keys(zh).sort()) })
it('redeemUi 必备的键都在', () => { for (const k of ['redeemUi.home.title', 'redeemUi.paste.expired', 'redeemUi.blocking.already_subscribed', 'redeemUi.errors.submitUnknown', 'redeemUi.result.manualTitle']) expect(keys(zh)).toContain(k) })
it('X 只要 auth_token', () => { expect(JSON.stringify(zh.xPremium)).not.toMatch(/ct0|billing_email/); expect(JSON.stringify(en.xPremium)).not.toMatch(/ct0|billing_email/) })
```

- [ ] **第 2 步：运行，确认失败**

运行：`npx vitest run src/i18n/__tests__/locale-keys.test.ts`
预期：FAIL。如果现有两套语言包本来就不一致，第一条也会失败：补齐缺的键，并在报告里列出补了哪些。

- [ ] **第 3 步：写语言包。**

- [ ] **第 4 步：运行全部前端测试**

运行：`npx vitest run`
预期：PASS。

- [ ] **第 5 步：提交**

```bash
git add frontend/src/i18n/locales/zh.ts frontend/src/i18n/locales/en.ts frontend/src/i18n/__tests__/locale-keys.test.ts
git commit -m "feat(i18n): 新增兑换改版文案，中英文键保持一致，改正 X 的说明"
```

---

### 任务 9：账号摘要与兑换流程 `useRedeemFlow`

**文件：**
- 新建：`frontend/src/lib/redeemAccount.ts`、`frontend/src/lib/__tests__/redeemAccount.test.ts`
- 新建：`frontend/src/composables/useRedeemFlow.ts`、`frontend/src/composables/__tests__/useRedeemFlow.test.ts`

**接口：**
- `redeemAccount.ts`：
  - `interface AccountSummary { maskedEmail: string; currentPlan: string; subscriptionHasActive: boolean | null; recoveryRequired: boolean; delinquent: boolean; checked: boolean | null }`
    - `checked`：`true` 是查过；`false` 是 MarsWallex 返回查不了；`null` 是对方没返回账号信息。
  - `accountFromPreflight(data: any): AccountSummary`：字段来源照现有 `RechargeView.applyAccountFromPreflight`（:638）和规格 §5.4。邮箱用任务 7 的 `maskEmail` 脱敏后才放进摘要。
  - `type ConfirmState = 'ok' | 'blocked' | 'recover' | 'self_check'`
  - `confirmState(i: { account: AccountSummary | null; blockingCode: string; blockingReason: string; satisfied: boolean }): ConfirmState`：照规格 §6.4 的状态表。判断先后：`recover` → `blocked` → `self_check` → `ok`。`satisfied` 为真，即当前套餐已满足（`planSatisfied`），算 `blocked`。
  - `blockingKey(code: string): string | null`：放弃码对应的 `redeemUi.blocking.*` 键；`credit_account_unknown`、`credit_delinquency_unknown` 共用一个键；表里没有的返回 null。
- `useRedeemFlow.ts`：

```ts
export type Screen = 'code' | 'credential' | 'confirm' | 'progress' | 'result'
export type FlowErrorKind = 'network' | 'rate_limited' | 'ticket_expired' | 'rejected' | 'submit_unknown' | 'invalid'
export interface FlowError { kind: FlowErrorKind; message?: string }
export interface RedeemDeps {
  fetchFn?: typeof fetch
  storage?: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'> | null // 默认 sessionStorage，取不到为 null
  now?: () => number
  every?: (fn: () => void, ms: number) => () => void // 默认 setInterval，返回取消函数
  deviceId?: string
}
export function useRedeemFlow(deps?: RedeemDeps): {
  screen: Ref<Screen>; code: Ref<string>; previewInfo: Ref<any | null>; account: Ref<AccountSummary | null>
  blockingCode: Ref<string>; blockingReason: Ref<string>; extra: Ref<{ accountCurrency?: string; codeCurrency?: string }>
  result: Ref<{ status: string; stage: string; message: string; attemptsLeft?: number }>
  lookup: Ref<LookupResult | null>   // 预览报码不能用、开通到终态后取的售后查询结果
  error: Ref<FlowError | null>; busy: Ref<boolean>; submitState: Ref<'idle' | 'submitting' | 'unknown'>
  progressStartedAt: Ref<number | null>
  preview(code: string): Promise<void>
  preflight(credential: { mode: 'session'; session: string } | { mode: 'mailbox'; email: string; password: string }): Promise<void>
  redeem(opts?: { agreeCreditTerms?: boolean; claudeOrgId?: string; grokSession?: string }): Promise<void>
  checkResult(): Promise<void>
  recoverGrace(): Promise<void>      // 调用前由页面弹二次确认（沿用 grace.confirm 文案），这里不弹框
  changeAccount(): void              // 回到 credential：清预检票、账号摘要、放弃码、出错，卡密和预览保留
  reset(opts?: { keepCode?: boolean }): void
  restore(queryCode: string): boolean
}
```

- 行为：
  - 接口路径、请求体、`X-Redemption-Device` 头照现有 `RechargeView`：`api` 在 :798，`doPreview` 在 :850，`doPreflight` 在 :895，`recoverGraceSubscription` 在 :969，`doRedeem` 在 :998，`startPoll` 在 :1045，`tryResumeByCode` 在 :836。
  - 预览：
    - 报「已兑换」一类时走 `result-by-code`，查到单就进 `progress`；
    - 报码不能用时调 `lookupAftersale`，结果放进 `lookup`，`screen` 留在 `code`。
  - 预检：
    - 400 且 `error_code === 'session_invalid'`：停在 `credential`，`error.kind = 'invalid'`；
    - 拿到结果后进 `confirm`。
  - 兑换：
    - **同步**置 `submitState = 'submitting'`，已在提交中或为 `'unknown'` 时直接返回。
    - `fetch` 抛错（网络断）时，`submitState = 'unknown'`，`error.kind = 'submit_unknown'`，之后只能 `checkResult()`。
    - MarsWallex 被拒且没建单（`redeemRejectedBeforeOrder`）：400 回 `credential` 并清预检票，`error.kind = 'ticket_expired'`；其他状态码留在 `confirm`，`error.kind = 'rejected'`。
    - 其余情况进 `progress` 开始查进度。
  - `checkResult()`：只调 `result-by-code`。查到单就进 `progress`；404 回 `confirm`，`submitState = 'idle'`。**不调兑换接口。**
  - 查进度：
    - 每 3000ms 一次，用 `deps.every`；
    - 到终态（`completed`、`failed_precharge`、`failed`、`declined`、`cancelled`，以及 MarsWallex 的 `review`）就停，调一次 `lookupAftersale` 填 `lookup`，进 `result`；
    - 404 时停止查询，`error.kind = 'rejected'`。
  - 状态码分类：429 → `rate_limited`，不自动重试；5xx 和网络错误 → `network`。已填的内容都不清空。
  - 进度缓存：
    - 写 `cdk_redeem_progress_v2`，字段和不存的内容照全局约束；
    - 第一次创建时删掉 `cdk_redeem_progress_v1`；
    - `restore(queryCode)`：读缓存，超过 7 天丢弃；`queryCode` 不为空且和缓存的码不同，就丢掉缓存、返回 false；停在 `progress` 的接着查进度。

- [ ] **第 1 步：写失败的测试**
  - `redeemAccount.test.ts`：
    - `accountFromPreflight`：MarsWallex 返回 `account_checked:false` 时 `checked === false`；ZovoCard 返回不带账号信息时为 `null`；邮箱已脱敏。
    - `confirmState`：四种状态各一条，加两条先后顺序（既要恢复订阅又有放弃码 → `recover`；有放弃码且没查到账号 → `blocked`）。
    - `blockingKey`：规格表里每个码都有键，未知码返回 null。
  - `useRedeemFlow.test.ts`：用假的 `fetchFn`（按路径返回预设响应，并记录调用）、内存 `storage`、可控的 `every`。

```ts
it('确认开通连点只发一次请求', async () => { /* 走到 confirm；const p1 = flow.redeem(); const p2 = flow.redeem(); await Promise.all([p1, p2]); 断言 /cdk/redeem 只调用 1 次 */ })
it('兑换请求网络失败后只查结果，不再兑换', async () => { /* redeem 时 fetchFn 抛 TypeError → submitState==='unknown'；再调 redeem() 不发请求；checkResult() 只调 result-by-code；404 → screen==='confirm' 且 submitState==='idle' */ })
it('预检票过期回到第 ② 屏', async () => { /* MarsWallex redeem 返回 400 且没建单 → screen==='credential'，error.kind==='ticket_expired'，code 保留 */ })
it('429 不自动重试', async () => { /* preview 返回 429 → error.kind==='rate_limited'，fetchFn 只调 1 次 */ })
it('进度缓存不存登录信息原文', async () => { /* preflight 后读 storage 里的 cdk_redeem_progress_v2：不含 sessionToken 原文、密码、完整邮箱 */ })
it('在第 ③ 屏刷新能恢复，票过期回第 ② 屏', async () => { /* 重点核查 4：新建一个 flow 用同一 storage，restore('') → screen==='confirm'、account.maskedEmail 有值；redeem 返回 400 → screen==='credential' */ })
it('带了别的卡密就丢掉旧进度', () => { /* storage 里存码 A；restore('B') === false，storage 被清掉 */ })
it('换一个账号保留卡密', async () => { /* 走到 confirm；changeAccount() → screen==='credential'，code 和 previewInfo 不变，account 为 null，storage 里没有预检票 */ })
it('删掉 v1 旧缓存', () => { /* storage 预置 cdk_redeem_progress_v1；useRedeemFlow 后它不存在 */ })
it('到终态停止查询并取售后结果', async () => { /* every 回调连续返回 running → completed；之后取消函数被调用，lookup 有值，screen==='result' */ })
```

- [ ] **第 2 步：运行，确认失败**

运行：`npx vitest run src/lib/__tests__/redeemAccount.test.ts src/composables/__tests__/useRedeemFlow.test.ts`
预期：FAIL，找不到模块。

- [ ] **第 3 步：实现两个模块。**

- [ ] **第 4 步：运行，确认通过**

运行：同第 2 步，再跑一遍 `npx vitest run`。
预期：PASS。

- [ ] **第 5 步：提交**

```bash
git add frontend/src/lib/redeemAccount.ts frontend/src/lib/__tests__/redeemAccount.test.ts frontend/src/composables/useRedeemFlow.ts frontend/src/composables/__tests__/useRedeemFlow.test.ts
git commit -m "feat(frontend): 兑换流程集中到 useRedeemFlow，提交失败只查结果、绝不重发"
```

---

### 任务 10：首页、卡密输入、结果卡、顶部进度

**文件：**
- 新建：`frontend/src/lib/homeRoute.ts`、`frontend/src/lib/__tests__/homeRoute.test.ts`
- 新建：`frontend/src/composables/useCodeLookup.ts`、`frontend/src/composables/__tests__/useCodeLookup.test.ts`
- 新建：`frontend/src/components/redeem/CodeEntry.vue`、`CodeStatusCard.vue`、`ProgressTicks.vue`
- 修改：`frontend/src/views/HomeView.vue`（整页重写）
- 修改：`frontend/src/views/__tests__/home.spec.ts`（按新首页改断言）

**接口：**
- `homeRoute(r: LookupResult): { kind: 'goto'; to: string } | { kind: 'card' }`：照规格 §6.1 的分流表，`to` 为 `/recharge?code=<encodeURIComponent(码)>`。
- `useCodeLookup(deps?: { fetchFn?: typeof fetch })`：
  - 返回 `{ busy: Ref<boolean>; result: Ref<LookupResult | null>; error: Ref<string>; submit(raw: string): Promise<void>; clear(): void }`；
  - 内部调 `lookupAftersale`；进行中再调 `submit` 直接返回（重点核查 5）；
  - 出错文字用 `redeemUi.errors.*`。
- `CodeEntry.vue`：
  - props `{ modelValue: string; busy: boolean; error: string }`，emits `update:modelValue`、`submit`；
  - 等宽输入框，回车和按钮都 emit `submit`；
  - 首页和兑换页第 ① 屏共用。
- `CodeStatusCard.vue`：
  - props `{ result: LookupResult; context: 'home' | 'result'; attemptsLeft?: number }`，emits `redeem(code: string)`、`reset()`；
  - 逻辑仍用 `afterSaleView`（标题、字段、指引、按钮）。`context === 'result'` 时按规格 §6.6 换标题：
    - 可重提用「没开通成功，没有扣款，这张码还能用」，`attemptsLeft === 1` 时加次数提醒；
    - 转人工用「没开通成功，已转人工处理」，加「复制卡密」。
  - 底部「换一张卡密」emit `reset`。
- `ProgressTicks.vue`：props `{ codeLabel: string | null; current: 1 | 2 | 3; done: boolean }`，文案用 `redeemUi.ticks.*`（规格 §6.2）。
- `HomeView.vue`：
  - 结构：`UserShell` > `UserPageHeader` > `CodeEntry` > 结果卡（`CodeStatusCard context="home"`）> 常见问题（沿用 `faqs` 内容，换新外观）> 页脚三个链接；
  - 去掉 `AfterSaleLookup`、快速三步、入口卡片、「兑换后怎么查」区块；
  - `homeRoute` 返回 `goto` 时 `router.push`。

- [ ] **第 1 步：写失败的测试**
  - `homeRoute.test.ts`：规格 §6.1 分流表的每一行一条（`unused` 不可重提 → goto；`unused` 可重提 → card；`failed` 可重提 → card；`processing` → goto；`used`、`replaced`、不可重提的 `failed`、`unsupported`、`disabled`、`expired`、`unknown` → card）。码里有空格和 `+` 时 `to` 正确编码。
  - `useCodeLookup.test.ts`：`submit` 连调两次只请求一次；去掉首尾空白再查；网络错误时 `error` 有值、`result` 为 null。
  - `home.spec.ts`（renderToString）：
    - 有输入框和「继续」；
    - 没有旧的入口卡片和快速三步；
    - 有常见问题和页脚三个链接；
    - 原来那行 `not.toContain('Telegram' ...)` 保留。

- [ ] **第 2 步：运行，确认失败**

运行：`npx vitest run src/lib/__tests__/homeRoute.test.ts src/composables/__tests__/useCodeLookup.test.ts src/views/__tests__/home.spec.ts`
预期：FAIL。

- [ ] **第 3 步：实现。**

- [ ] **第 4 步：运行全部前端测试和构建**

运行：`npx vitest run && npm run build`
预期：PASS。

- [ ] **第 5 步：提交**

```bash
git add frontend/src/lib/homeRoute.ts frontend/src/lib/__tests__/homeRoute.test.ts frontend/src/composables/useCodeLookup.ts frontend/src/composables/__tests__/useCodeLookup.test.ts frontend/src/components/redeem/CodeEntry.vue frontend/src/components/redeem/CodeStatusCard.vue frontend/src/components/redeem/ProgressTicks.vue frontend/src/views/HomeView.vue frontend/src/views/__tests__/home.spec.ts
git commit -m "feat(frontend): 首页只留一个卡密输入框，按卡密状态分流"
```

---

### 任务 11：第 ② 屏「粘贴登录信息」

**文件：**
- 新建：`frontend/src/components/redeem/CredentialStep.vue`、`DeviceGuide.vue`、`SessionPasteBox.vue`
- 新建：`frontend/src/components/redeem/__tests__/credential.spec.ts`

**接口：**
- 用到：任务 7 的 `checkPastedSession`、`detectDevice`；`claude.ts` 的 `claudeOrgCheck`、`claudeCredential`；`grok.ts` 的 `isGrokCredential`、`grokCredential`；`x-premium.ts` 的 `xPremiumCredential`；`mwredeem.ts` 的 `isCardSupply`。
- `DeviceGuide.vue`：
  - props `{ initial: 'ios' | 'android' | 'desktop' }`；
  - 三个标签可切换，内容取 `redeemUi.guide.*`（规格 §6.3 表）；
  - 「打开登录信息页」用 `<a href="https://chatgpt.com/api/auth/session" target="_blank" rel="noopener noreferrer">`；
  - 下方显示安全提醒。
- `SessionPasteBox.vue`：
  - props `{ modelValue: string; strict: boolean }`，emits `update:modelValue`、`check(result: SessionCheck)`；
  - 输入或粘贴后立刻在本地检查，框下显示 `redeemUi.paste.*`。
- `CredentialStep.vue`：
  - props `{ preview: any; busy: boolean; error: FlowError | null }`，emits `submit(credential)`。
  - 条件提醒：`requires_no_subscription` 或 `requires_subscription` 为真时显示（规格 §6.3）。
  - ChatGPT 码：`DeviceGuide` + `SessionPasteBox`，MarsWallex 码 `strict`，ZovoCard 码宽松。
  - 只有 ZovoCard 的 ChatGPT 码显示「不会复制？改用邮箱和密码」，条件照 `RechargeView.vue:39`：不是 X、Grok、Claude、MarsWallex。
  - Claude、Grok、X：沿用各自的校验，只换外观和文案。
  - 「下一步」本地检查通过才可点；`busy` 时显示「正在核对账号…」并锁住。

- [ ] **第 1 步：写失败的测试** `credential.spec.ts`（renderToString，照 `views/__tests__/home.spec.ts` 的 i18n 和 mock 写法）：
  - MarsWallex 订阅码的预览：渲染出条件提醒「这张码只能用在没有订阅的账号上」、三个设备标签、`https://chatgpt.com/api/auth/session` 链接；没有「改用邮箱和密码」。
  - ZovoCard 的 ChatGPT 码：有「改用邮箱和密码」。
  - Grok、Claude 码：没有设备教程。
  - `busy: true`：按钮文字为「正在核对账号…」且带 `disabled`。

- [ ] **第 2 步：运行，确认失败**

运行：`npx vitest run src/components/redeem/__tests__/credential.spec.ts`
预期：FAIL。

- [ ] **第 3 步：实现三个组件。**

- [ ] **第 4 步：运行**

运行：`npx vitest run`
预期：PASS。

- [ ] **第 5 步：提交**

```bash
git add frontend/src/components/redeem/CredentialStep.vue frontend/src/components/redeem/DeviceGuide.vue frontend/src/components/redeem/SessionPasteBox.vue frontend/src/components/redeem/__tests__/credential.spec.ts
git commit -m "feat(frontend): 粘贴登录信息一屏：按设备给教程，粘贴后当场检查"
```

---

### 任务 12：第 ③ 屏「核对账号」

**文件：**
- 新建：`frontend/src/components/redeem/ConfirmStep.vue`、`frontend/src/components/redeem/__tests__/confirm.spec.ts`

**接口：**
- 用到：任务 9 的 `AccountSummary`、`confirmState`、`blockingKey`；`plan.ts` 的 `planLabel`、`planSatisfied`；`mwredeem.ts` 的 `isOneMonthCode`、`isCreditCode`；语言包的 `redeemUi.creditTerms.*`。
- props `{ preview: any; account: AccountSummary | null; blockingCode: string; blockingReason: string; extra: { accountCurrency?: string; codeCurrency?: string }; submitState: 'idle' | 'submitting' | 'unknown'; error: FlowError | null }`
- emits `confirm(opts: { agreeCreditTerms: boolean })`、`changeAccount()`、`recover()`、`checkResult()`
- 显示照规格 §6.4：
  - 四种状态各自的标题、内容、按钮；
  - 点数码要勾选须知；「请自己确认」要勾选确认框；
  - 开通按钮上方写「不能撤回」那句；
  - `submitState === 'submitting'` 时按钮显示「正在提交…」并锁住；
  - `'unknown'` 时只显示「没收到回复，不确定是否已提交。点下面按钮查一下」和「查一下结果」（规格 §8）；
  - 放弃码文案：`blockingKey` 有键时用键加占位符，没有时显示 `blockingReason`。

- [ ] **第 1 步：写失败的测试** `confirm.spec.ts`（renderToString）：
  - 可以开通：标题「这个账号可以开通」，有「确认开通 ChatGPT Plus」一类按钮，有「不能撤回」那句；
  - 不能用（`already_subscribed`，套餐 plus）：文案含「这个账号已经有订阅（Plus）」，没有开通按钮；
  - `credit_currency_mismatch`：文案里出现 `USD` 和 `PHP`；
  - 请自己确认（`checked: false`）：有勾选框「我确认这个账号现在没有订阅」，开通按钮带 `disabled`；
  - `submitState: 'unknown'`：只有「查一下结果」，没有开通按钮。

- [ ] **第 2 步：运行，确认失败**

运行：`npx vitest run src/components/redeem/__tests__/confirm.spec.ts`
预期：FAIL。

- [ ] **第 3 步：实现。**

- [ ] **第 4 步：运行**

运行：`npx vitest run`
预期：PASS。

- [ ] **第 5 步：提交**

```bash
git add frontend/src/components/redeem/ConfirmStep.vue frontend/src/components/redeem/__tests__/confirm.spec.ts
git commit -m "feat(frontend): 核对账号一屏：付款前说清这个账号行不行"
```

---

### 任务 13：第 ④⑤ 屏，重写兑换页

**文件：**
- 新建：`frontend/src/components/redeem/ProgressStep.vue`、`ResultStep.vue`、`frontend/src/components/redeem/__tests__/progress-result.spec.ts`
- 修改：`frontend/src/views/user/RechargeView.vue`（整页重写，不超过 300 行）
- 现有测试中针对旧兑换页内部行为的（执行者用 `grep -rl RechargeView frontend/src` 找出），改为针对新模块，并在报告里列出

**接口：**
- `ProgressStep.vue`：
  - props `{ planText: string; view: ProgressView; waitedLong: boolean }`；
  - 三行打勾、当前行转圈、没到的空心圆；
  - 白卡写「可以关掉这个页面…」，`waitedLong` 时加 10 分钟那句，`view.reviewing` 时显示「正在人工核对，不用你操作」（规格 §6.5）。
- `ResultStep.vue`：
  - props `{ planText: string; status: string; kind: 'card_supply' | 'upstream'; product: string; maskedEmail: string; lookup: LookupResult | null; creditQuantity?: number; attemptsLeft?: number }`；
  - emits `retry()`、`reset()`；
  - 成功：照规格 §6.6，续费状态取 `lookup.renewal`，打开按钮按产品选 ChatGPT、Claude、Grok、X；
  - 其余情况：`CodeStatusCard context="result"`。
- `RechargeView.vue`：
  - `UserShell` > `UserPageHeader` > `ProgressTicks` > 按 `useRedeemFlow().screen` 切换五屏；
  - 挂载时调 `restore(query.code || query.cdk)`；返回 false 且带了码时，自动调 `preview(码)`（规格 §6.2）；
  - 第 ① 屏 `lookup` 有值（预览报码不能用）时，在输入框下显示 `CodeStatusCard context="home"`，和首页一样；
  - `ConfirmStep` 的 `changeAccount`、`ResultStep` 的 `retry` 都调 `changeAccount()`；`recover` 先用 `dialog.confirm` 弹 `grace.confirm`，确认后才调 `recoverGrace()`；
  - 进度用 `progressRows`，每次把上一次的 `rows` 传进去；
  - `waitedLong` 用 `progressStartedAt` 和当前时间算，满 10 分钟为真。

- [ ] **第 1 步：写失败的测试** `progress-result.spec.ts`（renderToString）：
  - `ProgressStep`：`rows ['done','current','todo']` 渲染出 1 个勾、1 个转圈、1 个空心圆；`waitedLong` 时出现 10 分钟那句；`reviewing` 时出现人工核对那句。
  - `ResultStep` 成功：
    - ChatGPT 订阅码，`lookup.renewal = 'off'`：有「已关闭」，按钮链接 `https://chatgpt.com`；
    - Claude 码：链接 `https://claude.ai`；
    - 查不到 `renewal`：不显示续费行。
  - `ResultStep` 失败可重提：标题「没开通成功，没有扣款，这张码还能用」；`attemptsLeft: 1` 时有次数提醒。
  - 转人工：有「复制卡密」。

- [ ] **第 2 步：运行，确认失败**

运行：`npx vitest run src/components/redeem/__tests__/progress-result.spec.ts`
预期：FAIL。

- [ ] **第 3 步：实现两个组件，重写 `RechargeView.vue`。**

- [ ] **第 4 步：运行全部前端测试和构建，检查行数**

运行：`npx vitest run && npm run build && wc -l src/views/user/RechargeView.vue`
预期：PASS，构建成功，不超过 300 行。

- [ ] **第 5 步：提交**

```bash
git add frontend/src/components/redeem/ProgressStep.vue frontend/src/components/redeem/ResultStep.vue frontend/src/components/redeem/__tests__/progress-result.spec.ts frontend/src/views/user/RechargeView.vue
git commit -m "feat(frontend): 兑换页按五屏重做，开通中逐行打勾"
```

（第 4 步改过的旧测试文件一起加进 `git add`。）

---

### 任务 14：页头和其余用户端页面，禁用词测试

**文件：**
- 修改：`frontend/src/components/UserPageHeader.vue`：
  - 显示 Logo：`siteBrand.logoVersion` 不为空时用 `<img src="/api/v1/public/site/logo?v=<version>" alt="">`；
  - 显示站名、`LanguageToggle`、`ThemeToggle`；
  - 去掉皮肤选择；
  - 加可选 prop `back?: boolean`，为真时左侧显示「返回首页」。
- 新建：`frontend/src/components/redeem/UserFooter.vue`：常见问题（`/#help`）、批量兑换、账单查询三个链接。
- 修改：`frontend/src/views/user/BatchRedeemView.vue`、`BillingCheckView.vue`、`CDKStatusView.vue`、`AgentSwapView.vue`：
  - 包进 `UserShell`；去掉 `RedeemModeTabs`；页头 `back`；页脚 `UserFooter`；
  - 去掉显示给买家的禁用词；
  - `CDKStatusView` 的单张查询结果改用 `CodeStatusCard context="home"`；
  - 流程和接口不动（规格 §6.8）。
- 新建：`frontend/src/views/__tests__/banned-words.test.ts`

**接口：**
- `banned-words.test.ts`：
  - 用 `readFileSync` 读这些文件的 `<template>` 部分：
    - `src/views/HomeView.vue`
    - `src/views/user/*.vue`
    - `src/components/redeem/*.vue`
    - `src/components/UserPageHeader.vue`
    - `src/components/SessionHelp.vue`
  - 同时读这些语言包子树的全部字符串值：zh 和 en 的 `redeemUi`、`home`、`batch`、`cdkLookup`、`aftersale`、`sessionHelp`、`claude`、`grok`、`xPremium`、`grace`。
  - 断言不出现全局约束里的禁用词：英文不区分大小写；中文 `Session JSON` 一类按原样匹配。
  - 只查模板和文案，不查 `<script>` 里的变量名和接口字段。
- 测试先写，那时会因为旧页面里的禁用词失败；改完页面后通过。

- [ ] **第 1 步：写失败的测试** `banned-words.test.ts`。另在 `home.spec.ts` 里加一条：页头不含皮肤选择（`SkinPicker` 渲染出的标记不出现）。

- [ ] **第 2 步：运行，确认失败**

运行：`npx vitest run src/views/__tests__/banned-words.test.ts`
预期：FAIL，列出命中的文件和词。

- [ ] **第 3 步：改页头、页脚和四个页面，改语言包里命中的文案**（中英两边一起改，键不变）。

- [ ] **第 4 步：运行全部前端测试和构建**

运行：`npx vitest run && npm run build`
预期：PASS。

- [ ] **第 5 步：提交**

```bash
git add frontend/src/components/UserPageHeader.vue frontend/src/components/redeem/UserFooter.vue frontend/src/views/user/BatchRedeemView.vue frontend/src/views/user/BillingCheckView.vue frontend/src/views/user/CDKStatusView.vue frontend/src/views/user/AgentSwapView.vue frontend/src/views/__tests__/banned-words.test.ts frontend/src/views/__tests__/home.spec.ts frontend/src/i18n/locales/zh.ts frontend/src/i18n/locales/en.ts
git commit -m "feat(frontend): 其余用户端页面换新外观，去掉内部用语"
```

---

### 任务 15：后台「站点外观」加主色和 Logo

**文件：**
- 修改：`frontend/src/views/admin/SiteAppearance.vue`
- 新建：`frontend/src/views/admin/__tests__/site-appearance.spec.ts`

**接口：**
- 用到：任务 5 的接口（`GET/PUT /api/v1/admin/settings` 的 `primary_color`、`logo_version`；`POST/DELETE /api/v1/admin/settings/logo`）；任务 6 的 `contrastRatio`、`isUsablePrimary`。
- 主色：
  - 6 个预设色块，加一个 `#RRGGBB` 输入框；
  - 实时显示「白字对比度 x.xx:1」，不可用时显示全局约束里的提示，保存按钮锁住；
  - 保存时随其他字段一起 PUT。
- Logo：
  - 文件输入 `accept="image/png,image/jpeg,image/webp"`；
  - 前端先查大小，超过 256 KB 直接提示，不上传；
  - 用 `FormData` 字段 `file` 调 `authFetch(..., { method: 'POST', body: form })`。执行者先确认 `authFetch` 遇到 `FormData` 时不会自动设 JSON 的 `Content-Type`，会的话在 `lib/api.ts` 里跳过；
  - 有 Logo 时显示预览和「删除 Logo」。
- 皮肤选择的标题改为「后台皮肤」，说明「只影响后台页面」。
- 亮暗单选保留 light、dark、auto 三项，默认选中 auto。

- [ ] **第 1 步：写失败的测试** `site-appearance.spec.ts`（照 `views/admin/__tests__/pay.spec.ts` 的 `authFetch` mock 写法）：
  - 渲染出 6 个预设色号；
  - 有「后台皮肤」；
  - 设置返回 `primary_color: '#7FD4C4'` 时，渲染出「这个颜色太浅」提示，保存按钮带 `disabled`；
  - `logo_version` 不为空时有「删除 Logo」。

- [ ] **第 2 步：运行，确认失败**

运行：`npx vitest run src/views/admin/__tests__/site-appearance.spec.ts`
预期：FAIL。

- [ ] **第 3 步：实现。**

- [ ] **第 4 步：运行全部前端测试和构建**

运行：`npx vitest run && npm run build`
预期：PASS。

- [ ] **第 5 步：提交**

```bash
git add frontend/src/views/admin/SiteAppearance.vue frontend/src/views/admin/__tests__/site-appearance.spec.ts
git commit -m "feat(admin): 站点外观可以选主色、上传 Logo，皮肤只管后台"
```

（改了 `lib/api.ts` 就一起加进 `git add`。）

---

### 任务 16：视觉检查与本地门禁

**文件：**
- 不改产品代码。发现问题时回到对应任务的文件修，单独提交。
- 截图存到 `/Volumes/ExtSSD/projects/aisubproxy/.playwright-mcp/redeem-ui-a/`，不进仓库。

- [ ] **第 1 步：起前端开发服务**

运行：在 `frontend/` 用 `npx vite --port 5199 --strictPort`，后台运行。
预期：`http://localhost:5199` 能打开。不连后端，不用 18080 端口。

- [ ] **第 2 步：用 Playwright 浏览器工具挡住接口，逐张截图**
  - 用 `page.route('**/api/v1/**', ...)` 按场景返回假数据；
  - 场景：首页（空、出错、用过的码结果卡）、第 ② 屏（MarsWallex、ZovoCard、粘贴通过、粘贴出错）、第 ③ 屏四种状态和「没收到回复」、第 ④ 屏（进行中、等了 10 分钟）、第 ⑤ 屏（成功、可重提、转人工）、`/batch`、`/billing`、`/history`、后台「站点外观」；
  - 每个场景截四张：375px、1280px 宽各配亮色、暗色（用 `emulateMedia({ colorScheme })`，并把 `theme-mode` 设为 `auto`）。

- [ ] **第 3 步：逐张看。**
  - 文字有没有被截断，按钮够不够大，暗色下看不看得清，焦点框是否清楚。
  - 有问题就修，修完重截那一张。
  - 在报告里列出看过的张数和修过的问题。

- [ ] **第 4 步：跑全部本地门禁**

运行：
```bash
cd backend && gofmt -l . && go vet ./... && golangci-lint run ./... && go test -race -count=1 $(go list ./... | grep -v /internal/paybrowser) && go test -race -timeout 30m ./internal/paybrowser/... && govulncheck ./...
cd ../frontend && npx vitest run && npm run build
cd .. && gitleaks git --log-opts="origin/master..HEAD" --redact --no-banner .
```
预期：
- gofmt 没有输出；
- golangci-lint 报 `0 issues.`；
- 测试全部 PASS；
- govulncheck 显示代码调用到的漏洞为 0；
- 构建成功；
- gitleaks 报 `no leaks found`。

- [ ] **第 5 步：停掉第 1 步起的开发服务**（只结束自己起的那个 PID）。

---

## 上线时要做的（不在本计划里执行）

- 发版说明照规格 §10.1，按「升级注意」三块写在最前面。
- 真实环境验证照规格 §10.2：要用户同意并在场，用真实账号在 VPS 上走到第 ③ 屏，只核对账号、不点开通，Session 不进对话。
