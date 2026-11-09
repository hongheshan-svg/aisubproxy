# 用户端兑换改版设计（子项目 A）

日期：2026-11-09。状态：五段设计已在对话中逐段批准，本文待用户审阅。

**2026-11-09 变更（用户决定）**：我们没有 ZovoCard 卡服务。本子项目不再为 ZovoCard 做任何事：§6.9 取消；登录信息只有一套严格检查；不提供「改用邮箱和密码」；开通进度只按一张表对应。X、Grok、Claude 三类码的输入界面保留，以后接别的服务商。把 ZovoCard 从代码里整个删掉另开子项目，在 A 之后做（见文末）。

**2026-11-09 变更 2（用户决定）**：提交卡密和登录信息后自动开通，不再让买家多点一次「确认开通」。第 ② 屏的按钮改为「核对账号并开通」，点数码须知挪到第 ② 屏勾选；账号核对通过就直接提交开通，进入第 ④ 屏。只有「不能用」「要先恢复订阅」「请自己确认」三种情况停在第 ③ 屏。详见 §6.3「下一步」和 §6.4.1。

本文是「界面改版」项目的第 1 个子项目，整体拆分见文末「所属项目」。

**写规格时按代码事实做的调整**（对话里批准的五段设计之外，请审阅时重点看）：
1. 开通中的进度由四行改为三行（核对账号、付款、确认开通）。付款引擎的进度里没有「关闭自动续费」这一步，它在开通后才做，状态只能从售后查询拿到，所以放到第 ⑤ 屏显示（§6.5、§6.6）。
2. 后台的亮暗模式本来就支持「跟随系统」，只是默认是暗色。本文只把默认改为跟随系统（§3.4）。
3. （已取消，见文首变更）顺带修 ZovoCard 码扣款前失败一直显示「处理中」的问题。
4. MarsWallex 码的登录信息，前端检查改成和后端一样严格：现在前端放行的几种格式，提交后会被后端拒绝（§6.3.1）。
5. MarsWallex 预检的新字段名跟现有前端对齐，用 `current_plan`、`subscription_has_active`（§5.4）。

## 1. 目标

买家拿到卡密后，不用懂技术、不用问人，就能自己开通。要解决用户点名的四个难点：

1. **不会拿登录信息（Session）**：按买家的设备给对应教程，粘贴后当场告诉他贴得对不对。
2. **不知道成没成**：开通中逐行打勾；关掉页面后，回首页粘贴同一张卡密就能看到结果。
3. **账号不符合条件**：付款前就告诉买家这个账号行不行、为什么、怎么办。
4. **不知道进哪个入口**：首页只有一个输入框，用没用过的码都从这里进。

做成的标准：
- 一个从没用过的买家，只看页面上的字，能走完五屏。
- 页面上不出现系统内部用语（见 §6.7）。
- 手机宽 375px、电脑宽 1280px，亮色、暗色，四种组合都好看、好用。

## 2. 范围

**做：**
- 首页 `/`：只有一个「粘贴卡密」框，按卡密状态分流（§6.1）。
- 兑换页 `/recharge`：按五屏重做（§6.2–§6.6）。`?cdk=`、`?code=` 两种带码链接照常可用。
- `/lookup`、`/history`：地址保留，换新外观，避免旧链接失效。
- 批量兑换 `/batch`、账单查询 `/billing`：换新外观、去内部用语，流程不改。
- 代理换码 `/partner/swap`：只跟着换外观。
- 后端：MarsWallex 码预检时用买家的 Session 只读查一次账号（§5）。
- 后台「站点外观」：新增主色和 Logo，亮暗模式加「跟随系统」（§4）。
- 中文、英文两套文案同步改。

**不做：**
- 后台各页面的改版（子项目 B、C、D）。后台在 B 之前保持现在的皮肤。
- 下单、付款、确认开通、关闭续费等后端流程，一行不动。付款时引擎照旧再查一次账号。
- 批量兑换页的流程重做。
- 新的服务商、新的产品。
- ZovoCard 相关的任何改动（整个删掉另开子项目）。

## 3. 外观

### 3.1 配色

| 名称 | 亮色 | 暗色 | 用途 |
|---|---|---|---|
| bg | #EEF5F3 | #0C1A18 | 页面背景 |
| surface | #FFFFFF | #132522 | 卡片、输入框底 |
| ink | #0E2A26 | #E6F1EE | 正文、标题 |
| muted | #4A6560 | #9DB5B0 | 次要文字、未到的步骤 |
| line | #DCE5E3 | #22403B | 分隔线 |
| primary | #0F6B5F（后台可改） | 由 primary 推出，默认 #3FB8A5 | 按钮、输入框边、打勾 |
| on-primary | #FFFFFF | #0C1A18 | 按钮上的字 |
| warn-bg / warn-ink | #FBF3E4 / #7A5410 | #2E2410 / #F0C878 | 条件提醒 |
| error-bg / error-ink | #FDECEA / #B42318 | #331614 / #F4A39B | 出错 |

已核对的对比度（WCAG 正文要求 4.5:1）：亮色 ink 13.78、muted 5.72、primary 字在 bg 上 5.77、warn 6.14、error 5.75、白字在 #0F6B5F 上 6.39；暗色 ink 15.45、muted 8.22、ink 在 surface 上 13.82、primary 在 surface 上 6.55、#0C1A18 字在 #3FB8A5 上 7.32、warn 9.62、error 8.34。后台改了主色后，暗色主色按 §4.2 推出，同样保证不低于 4.5。

进度条里「还没到的步骤」用 muted 色，不用更浅的灰（#7F948F 只有 2.91，不够）。状态靠图形区分：做完的打勾、当前的加粗并显示转圈、没到的空心圆。

### 3.2 字体

- 中文和正文：`-apple-system, "PingFang SC", "Microsoft YaHei", "Noto Sans SC", sans-serif`。
- 卡密、邮箱、数字：`"SF Mono", "JetBrains Mono", Menlo, Consolas, monospace`。
- 用户端不加载任何 Google 字体。删掉 `index.html` 里两条 Google 预连接。后台皮肤要用 Google 字体的，仍由 `theme.ts` 按需插入，只在后台页面发生。

### 3.3 尺寸与布局

- 字号：大标题 28px（窄屏 24px），按钮 18px，正文 15–16px，说明 13–14px。
- 按钮和输入框至少 48px 高；圆角 14px；输入框边 3px 主色。
- 一屏一件事，内容左对齐。电脑上是居中的一栏，最宽 520px。
- 键盘操作时焦点框清楚可见（主色 3px 外框）。
- 系统设了「减少动态效果」时，打勾和转圈不做动画。
- 动效只有一处：完成一步时那个勾出现。

### 3.4 亮暗模式

- 默认跟随系统。页头有切换按钮，买家选过就记在本机，沿用现有的 `localStorage` 键 `theme-mode`（读写都包 try/catch，取不到按站点设置）。优先级：买家本机的选择 > 后台 `theme_mode` > 跟随系统。
- 后台的 `theme_mode` 本来就接受 `light`、`dark`、`auto`（`settings.go` 校验），没设过时的默认值由 `dark` 改为 `auto`，前端 `theme.ts` 的默认值同步改。

## 4. 后台「站点外观」

### 4.1 设置项

| 设置项 | 取值 | 默认 | 说明 |
|---|---|---|---|
| `brand_name`、`brand_sub` | 文本 | 不变 | 站名、副标题，照旧 |
| `primary_color` | `#RRGGBB` | `#0F6B5F` | 新增。6 个预设：深青 #0F6B5F、海蓝 #1E4FA8、墨紫 #5B3FA0、枣红 #A3283B、松绿 #2F6B2F、石墨 #36454F（白字对比度 6.39–9.9），也可以填色号 |
| Logo | 图片 | 无 | 新增。没有时页头只显示站名 |
| `theme_mode` | `auto` / `light` / `dark` | `auto`（原为 `dark`） | 取值不变，只改默认 |
| `skin` | 现有皮肤白名单 | 不变 | 只影响后台，设置页上改名为「后台皮肤」；子项目 B 做完后删除 |

### 4.2 主色校验

- 前端和后端都校验：必须是 `#RRGGBB`，白字在它上面的对比度不低于 4.5:1。不够就不让保存，提示「这个颜色太浅，按钮上的白字看不清，请选深一点的颜色」。
- 暗色下的主色由前端推出：保持色相，逐步调亮，直到 #0C1A18 字在它上面的对比度不低于 4.5:1。
- 浅色调（选中底、勾的背景）由前端按主色混白推出，不另存设置。

### 4.3 Logo

- 只收 PNG、JPEG、WebP，按文件头判断类型，不信扩展名和请求头；不超过 256 KB；不收 SVG（SVG 里能藏脚本）。
- 存进设置表，和其他设置一起备份。
- 接口：
  - `POST /api/v1/admin/settings/logo`：上传，multipart，字段名 `file`，写审计日志。
  - `DELETE /api/v1/admin/settings/logo`：删除，写审计日志。
  - `GET /api/v1/public/site/logo`：公开读取。返回按文件头判定的 `Content-Type`，带 `X-Content-Type-Options: nosniff`、`ETag`、`Cache-Control: public, max-age=300`。没有 Logo 返回 404。
- `GET /api/v1/public/site` 新增 `primary_color` 和 `logo_version`（Logo 内容的短哈希，没有 Logo 时为空串）。前端用 `logo_version` 拼图片地址，换图后浏览器不会继续用旧缓存。

## 5. MarsWallex 码预检时查账号

### 5.1 现状

MarsWallex（供卡型）码的预检只解析 Session、检查码能不能用、检查跨服务商冲突，然后发预检票。代码在 `backend/internal/handler/local_cdk.go` 的 `localCDKPreflight`，注释写明「这里没查账号，当前套餐要等引擎 check 才知道」。账号不符合条件，要到付款引擎的 `check` 步骤才失败。失败时没建结账会话、没取卡、没注资，码还能再用。

### 5.2 新流程

预检在发票之前加一步「查账号」，顺序和付款引擎的 `check` 一样，全部只读：

1. 用 `chatgpt.ParseSession` 解析买家粘贴的原文。
2. `ResolvePlan(planKey, region, false)` 取这张码地区的币种。
3. `LoadProxies()` 取这张码地区的代理，用引擎同一个客户端工厂（`Options.ChatGPT`）建 ChatGPT 客户端。
4. `Refresh`：用 sessionToken 换 accessToken。只读，不改买家的 sessionToken。
5. `Account`：查默认账号的套餐。
6. 点数码在账号接口没返回欠费状态时，再调一次 `SubscriptionStatus`。
7. 用 §5.3 的共用规则判断。

入口放在付款引擎：新增 `func (s *Service) InspectAccount(ctx context.Context, r InspectRequest) (AccountVerdict, error)`，在新文件 `backend/internal/payengine/inspect.go`。`InspectRequest` 含 `PlanKey`、`Region`、`Session`（原文）。处理函数通过现有的全局 `payEngine` 调用它；`payEngine` 为 nil 时按「查不了」处理。

整次查询最多等 20 秒，超时按「查不了」处理。

### 5.3 判断规则只写一份

把付款引擎 `check` 和 `checkCredit` 里的判断抽成两个纯函数，放在 `backend/internal/payengine/eligibility.go`：

- `subscriptionEligibility(acc chatgpt.Account) *stepFailure`：已有有效订阅 → `already_subscribed`。
- `creditEligibility(acc chatgpt.Account, delinquent *bool, currency string) *stepFailure`：账号 ID 为空、没有生效订阅、套餐不是 Plus/Pro、不是个人账号、欠费状态未知、欠费、计费币种与码地区不一致 → 现有的各个放弃码，原文照搬。

`check`、`checkCredit` 改为调用这两个函数，放弃码和说明文字不变，付款引擎原有测试全部照过。预检的 `InspectAccount` 也调用它们。两边不会一边放行、一边拦下。

### 5.4 结果与返回字段

`AccountVerdict` 有四种结论：

| 结论 | 条件 | 预检返回 |
|---|---|---|
| 符合 `eligible` | 规则通过 | 200，发预检票；带 `account_checked: true`、`current_plan`（当前套餐，如 `free`、`plus`、`pro`）、`subscription_has_active` |
| 不符合 `ineligible` | 规则不通过 | 200，**不发预检票**；带 `account_checked: true`、`current_plan`、`subscription_has_active`、`blocking_code`（引擎放弃码）、`blocking_reason`（给买家看的中文原因和办法） |
| 登录信息无效 `session_invalid` | `ParseSession` 失败，或 `Refresh`、`Account` 返回未授权 | 400，`error_code: "session_invalid"`，提示「登录信息已失效，请回到 ChatGPT 刷新那个页面，再重新复制」 |
| 查不了 `unknown` | 套餐配置读不到、代理没配、网络出错、ChatGPT 限流、被 Cloudflare 拦截、超时、超过次数上限、引擎没启动 | 200，照常发预检票；带 `account_checked: false` |

字段名跟现有前端读取逻辑对齐（`applyAccountFromPreflight` 读 `current_plan`、`subscription_has_active`、`blocking_reason`），不用 `plan`、`has_active_subscription`：前端会把预检里的 `plan` 当成当前套餐，而 `has_active_subscription` 前端不读。§6.4 是 `blocking_code` 到买家文案的对照表，前端按 `blocking_code` 显示中英文；后端的 `blocking_reason` 是没有对照时的兜底。

`ineligible` 时不发预检票，所以兑换接口自然拒绝；现有前端看到 `blocking_reason` 也会禁用兑换按钮。

### 5.5 防滥用与缓存

- 每张码每小时最多查 10 次（按码的数据库 ID 计数，放在内存，进程重启清零）。超过按「查不了」处理，不拦买家。
- 同一张码加同一个账号（按 Session 里的邮箱）的结论缓存 5 分钟。买家来回切换页面不会重复打 ChatGPT。
- 只缓存 `eligible` 和 `ineligible`，`unknown` 和 `session_invalid` 不缓存。
- 公开接口原有的每 IP 每小时限流照旧。

### 5.6 日志与隐私

- 日志只记：码的数据库 ID、脱敏邮箱（`al***@gmail.com`）、当前套餐、结论、放弃码；查不了时记错误类别（限流、Cloudflare、超时、代理）。
- Session 原文、sessionToken、accessToken 不写日志、不进缓存（缓存键用码 ID 和邮箱的 SHA-256）、不出现在返回内容里。
- 被 Cloudflare 拦截只写日志，不发 Telegram 告警（预检次数多，避免刷屏）。付款时被拦截仍按原样告警。

### 5.7 付款时

不变。兑换提交后，付款引擎照旧走 `check`，再查一次账号。预检的结论只用于提前告诉买家，不作为付款的依据。

## 6. 页面与流程

可视化草图：`.superpowers/brainstorm/15568-1794206310/content/flow.html`（五屏，以 ChatGPT Plus 码为例；本地忽略目录，不进仓库）。

### 6.1 首页 `/`

- 页头：Logo（有就显示）、站名、语言切换、亮暗切换。
- 大标题「把卡密粘贴到这里」，下面一句「没用过的卡密会进入兑换；用过的会直接显示结果。」，一个等宽字输入框和「继续」按钮。输入框下是白色说明卡「卡密在哪？在你购买的店铺订单或发货消息里。」
- 点「继续」（或在输入框里按回车）：去掉首尾空白，调现有的售后查询 `GET /api/v1/lookup/cdk`（`aftersaleClient.ts`），按 `afterSaleView`（`aftersale.ts`）的结果分流：

| 售后查询结果 | 去向 |
|---|---|
| `unused`，不可重新提交 | 跳到 `/recharge?code=<码>`，进入第 ② 屏 |
| `unused` 或 `failed`，可重新提交 | 首页显示结果卡（沿用 failed_restored 标题和指引），按钮「换个账号再试」跳 `/recharge?code=` |
| `processing` | 跳到 `/recharge?code=`。兑换页预览发现已兑换，走现有的 `tryResumeByCode` 直接显示第 ④ 屏 |
| `used`、`replaced`、不可重新提交的 `failed`、`unsupported`、`disabled`、`expired`、`unknown` | 首页显示结果卡，标题、字段、指引、按钮全部沿用 `afterSaleView`，只换外观和文案 |

- 结果卡下面有「换一张卡密」，清空输入框。
- 首页现有的「售后查询」区块并入这个输入框，不再单独出现。
- 页面底部保留现有的常见问题，换新外观；页脚三个链接：常见问题（跳到页内）、批量兑换、账单查询。

### 6.2 兑换页总则

- 顶部三格打勾进度：第一格在认出码之前叫「核对卡密」，认出后改成套餐简称加「码」（如「Plus 码」「Codex 点数码」，名字用现有 `planLabel`）；第二格「确认账号」；第三格「开通」，完成后改为「已开通」。
- 带码进入（`?code=` 或 `?cdk=`）时自动调预览，直接到第 ② 屏；没带码时，第 ① 屏就是首页那个输入框，点「继续」后调预览 `POST /api/v1/public/cdk/preview`。
- 预览报码不能用（已用、已停用等）时，显示和首页一样的结果卡。

### 6.3 第 ② 屏：粘贴登录信息

**条件提醒**（提醒色）：只在预览返回了条件字段时显示。
- `requires_no_subscription` 为真：「这张码只能用在没有订阅的账号上。账号已经是 Plus 或 Pro 的，请换一个账号。」
- `requires_subscription` 为真：「这张码只能用在已订阅 Plus 或 Pro 的个人账号上。」

**ChatGPT 码的设备教程**：三个标签「iPhone」「安卓」「电脑」，默认按设备选中，可以手动切换。

| 设备 | 第 1 步 | 第 2 步 | 第 3 步 |
|---|---|---|---|
| iPhone | 用 Safari 登录 chatgpt.com，已登录可跳过 | 点「打开登录信息页」 | 页面上是一大段文字：长按 → 全选 → 拷贝，回到这里粘贴 |
| 安卓 | 用 Chrome 登录 chatgpt.com，已登录可跳过 | 点「打开登录信息页」 | 长按 → 全选 → 复制，回到这里粘贴 |
| 电脑 | 用浏览器登录 chatgpt.com，已登录可跳过 | 点「打开登录信息页」 | 按 Ctrl+A 全选（Mac 按 ⌘+A），再按 Ctrl+C 复制（Mac 按 ⌘+C）。火狐浏览器会显示成表格，先点上方「原始数据」再复制 |

- 「打开登录信息页」在新窗口打开 `https://chatgpt.com/api/auth/session`。
- 教程下方一句安全提醒，意思沿用 `SessionHelp.vue`：登录信息相当于账号钥匙，只粘贴到你信任的兑换页面。

**粘贴框**：粘贴或输入后，立刻在浏览器里检查（规则见 §6.3.1），不发到服务器。框下显示结果：通过时「✓ 认出账号 al***@gmail.com」（认不出邮箱时显示「✓ 格式正确」），不通过时显示对应的提示。

**邮箱和密码**：不提供。这是 ZovoCard 才有的方式（见文首变更）。

**其他产品**：
- Claude：先说明怎么找 Organization ID，再填两次 Organization ID，最后勾选「目标账号目前是免费账号」。校验沿用 `claude.ts`，只换外观和文案，没有设备教程。
- Grok：说明和规范化沿用 `grok.ts`，只换外观和文案。
- X：只需要 auth_token。中英文语言包里还写着 ct0、billing_email，要改正。

**下一步**（2026-11-09 变更 2 后）：
- 按钮文字「核对账号并开通」。本地检查通过后才能点；点数码另外要勾选点数兑换须知（条目同 §6.4，勾选框「我已阅读并同意以上须知」）。
- 按钮上方一句：「点下面按钮后先核对账号，没问题就直接开始付款开通，不能撤回。」
- 点了之后调预检，按钮变成「正在核对账号…」并锁住，最多等到预检返回（MarsWallex 查账号最多 20 秒，见 §5.2）。预检结论为「可以开通」时，立即自动提交开通，按钮变成「正在提交…」，提交被受理后进入第 ④ 屏；其余结论进入第 ③ 屏。规则见 §6.4.1。

#### 6.3.1 登录信息的本地检查

新函数替换 `RechargeView` 里 `extractSession` 的判断部分。ChatGPT 码一律用严格规则，和后端 `parseLocalSession` 一致，避免前端放行、后端拒绝。批量页的 `batch-session.ts` 不改。

| 粘贴的内容 | 结果 | 提示 |
|---|---|---|
| 空 | 不能下一步 | 无 |
| `{}` 或没有任何字段 | 拒绝 | 「页面上什么都没有，说明这个浏览器还没登录 ChatGPT。请先登录，再打开登录信息页。」 |
| 以 `eyJ` 开头的三段式字符串 | 拒绝 | 「这只是其中一段，不是完整的登录信息。请按上面的步骤复制整页内容。」 |
| 不是 JSON | 拒绝 | 「复制的不是登录信息。请打开登录信息页，全选后复制整页内容。」 |
| JSON 但没有 `sessionToken`、`session_token` | 拒绝 | 「只复制了一部分。请全选整页内容再复制。」 |
| JSON 有令牌但没有 `user.email` | 拒绝 | 「没认出账号邮箱，请重新复制整页内容。」 |
| `expires` 早于现在 | 拒绝 | 「这份登录信息已过期。请刷新登录信息页，再重新复制。」 |
| 其余 | 接受 | 「✓ 认出账号 al***@gmail.com」 |

邮箱脱敏沿用后端规则：保留前两个字符和完整域名，中间用 `***`。`expires` 解析不了时不当作过期。

### 6.4 第 ③ 屏：核对账号

| 状态 | 什么时候 | 标题 | 内容 | 按钮 |
|---|---|---|---|---|
| 可以开通 | 拿到账号信息且没有阻塞项 | 「这个账号可以开通」 | 账号（脱敏）、现在（当前套餐）、将开通（套餐名；MarsWallex 订阅码加「1 个月」，判断沿用 `mwredeem.ts` 的 `ONE_MONTH_TEXT`）、自动续费（订阅码写「开通后帮你关掉」；点数码这一行换成「点数：N」） | 主按钮「确认开通 <套餐>」，次按钮「换一个账号」 |
| 不能用 | 有 `blocking_code` 或 `blocking_reason`，或当前套餐已满足 | 「这个账号不能用这张码」 | 原因和办法（见下表） | 只有「换一个账号」 |
| 要先恢复订阅 | `subscription_recovery_required`，或欠费 | 「这个账号要先恢复订阅」 | 沿用现有 `grace.*` 文案，换成买家能懂的话 | 「恢复订阅」或「重新核对」，沿用 `recover-subscription` 流程 |
| 请自己确认 | MarsWallex 返回 `account_checked: false`，或预检没返回账号信息 | 「请确认这个账号」 | 账号（脱敏）、这张码的条件、一个勾选框：订阅码是「我确认这个账号现在没有订阅」，点数码是「我确认这个账号已订阅 Plus 或 Pro」 | 勾选后才能点「确认开通 <套餐>」 |

- 点数码另外显示点数兑换须知，勾选同意后才能点开通。须知条目沿用 `mwredeem.ts` 的 `CREDIT_TERMS`，改成中英文语言包。
- 开通按钮上方一句：「点下面按钮后就开始付款开通，不能撤回。」
- 「换一个账号」回到第 ② 屏，清空登录信息，卡密保留。

`blocking_code` 对应的买家文案（MarsWallex）：

| blocking_code | 中文 |
|---|---|
| `already_subscribed` | 这个账号已经有订阅（{当前套餐}）。这张码只能用在没有订阅的账号上，请换一个账号。 |
| `credit_no_subscription` | 这个账号没有订阅。点数只能加到已订阅 Plus 或 Pro 的账号上，请换一个账号。 |
| `credit_plan_ineligible` | 这个账号的套餐是 {当前套餐}，点数只能加到 Plus 或 Pro 账号上，请换一个账号。 |
| `credit_not_personal` | 这个账号不是个人账号，点数只能加到个人账号上，请换一个账号。 |
| `credit_delinquent` | 这个账号有欠费。请先在 ChatGPT 里处理欠费，或换一个账号。 |
| `credit_currency_mismatch` | 这个账号按 {账号币种} 付款，这张码是 {码币种} 地区的，两者要一致。请换一个账号，或联系卖家换码。 |
| `credit_account_unknown`、`credit_delinquency_unknown` | 暂时没法确认这个账号能不能加点数。请稍后再试，或换一个账号。 |
| 其他 | 显示后端的 `blocking_reason` |

Grok、Claude、X 的 `blocking_reason` 由服务商给出，原样显示。

#### 6.4.1 自动开通（2026-11-09 变更 2）

- **什么时候自动提交**：只在同一个页面里、买家在第 ② 屏点了「核对账号并开通」、这次预检的结论是「可以开通」时，自动提交开通一次。点数码带上第 ② 屏的须知勾选。
- **什么时候不自动提交**，停在第 ③ 屏，按钮照上表：
  - 预检结论是「不能用」「要先恢复订阅」「请自己确认」；
  - 刷新页面后从缓存恢复到第 ③ 屏；
  - 恢复订阅之后点「重新核对」得到「可以开通」：这时显示「可以开通」状态，买家点「确认开通」。原订阅刚被取消，再付款要买家明确点一次。
- **资金规则不变**：
  - 一次预检最多自动提交一次，提交途中和「不确定是否已提交」时不会再提交；
  - 自动提交没收到回复时，和手动提交一样进入「没收到回复，不确定是否已提交」，只能查结果；
  - 后端付款前照旧再核对一次账号（§5.7）。
- 第 ③ 屏「可以开通」状态保留，用于上面几种不自动提交的情况。

### 6.5 第 ④ 屏：开通中

标题「正在开通 <套餐>」。进度三行：**核对账号**、**付款**、**确认开通**。做完一行打一个勾，当前行转圈，没到的是空心圆。下面一张白卡：「可以关掉这个页面。之后回首页粘贴同一张卡密，就能看到结果。」

按结果接口的 `status` 和 `stage`（各类码共用这一张表）：

| status / stage | 进度 |
|---|---|
| `queued`；`running` 且 stage 为 `queued`、`credential_check` | 第 1 行进行中 |
| `running` 且 stage 为 `checkout`、`card_funding`、`payment` | 第 1 行完成，第 2 行进行中 |
| `running` 且 stage 为 `subscription` | 前 2 行完成，第 3 行进行中 |
| `completed` | 全部完成，进入第 ⑤ 屏 |
| `failed_precharge`、`failed`、`declined`、`review`、`cancelled` | 进入第 ⑤ 屏的失败或人工处理状态 |
| 其他值 | 保持当前行，不报错 |

- 查询频率沿用现在的约 3 秒一次。
- 进入第 ④ 屏满 10 分钟还没结束，白卡加一句「已经等了 10 分钟，还在处理。可以关掉页面，稍后回首页粘贴卡密查看。」页面继续查。
- 现在进度条的倒退问题（`checkout` 排在 `card_funding` 后面）随重做一并消失。

### 6.6 第 ⑤ 屏：结果

**成功**（`completed`）：
- 大勾，标题「已开通 <套餐>」。
- 列出账号（脱敏）。订阅码另列自动续费，取售后查询的 `renewal`：`off` 显示「已关闭」，`closing` 显示「正在关闭，不用你操作」，查不到就不显示这一行。点数码另列「点数：N」。
- 说明「回到 ChatGPT 刷新页面就能用了。看不到的话，退出再重新登录。」
- 主按钮「打开 ChatGPT」。Claude、Grok、X 分别是「打开 Claude」「打开 Grok」「打开 X」，链接 `https://claude.ai`、`https://grok.com`、`https://x.com`。

**失败或转人工**：进入终态失败（含 MarsWallex 的 `review`）时，调售后查询，结果卡和首页用同一个组件、同一套 `afterSaleView` 判断，不另写一套：
- 可重新提交：标题「没开通成功，没有扣款，这张码还能用」，附指引，主按钮「换个账号再试」回到第 ② 屏。MarsWallex 的 `attempts_left` 为 1 时加一句「如果付款后再失败一次，这张码会转人工处理。」（这个次数只算付款后的失败，所以不写成「还剩 N 次」）。
- 转人工或可能已扣款：标题「没开通成功，已转人工处理」，写「请联系卖家，把卡密发给他」，附「复制卡密」按钮。
- 已换新码：沿用新码显示和「用新码兑换」。

### 6.7 用词

| 不再出现 | 换成 |
|---|---|
| 预检 | 核对账号 |
| Session、Session JSON、sessionToken、Access Token | 登录信息 |
| 阶段 | 步骤 |
| 通道、轮询、上游、卡台、review、preflight | 删掉，换成买家能懂的说法 |
| 上游未提供 | 暂时查不到 |
| 兑换（指点了开始付款的那个按钮） | 确认开通 |

自动测试扫描用户端组件和用户端语言包，中文禁用：预检、通道、轮询、阶段、上游、卡台、Session JSON、sessionToken、Access Token；英文禁用：preflight、upstream、polling、stage、channel、session token、access token（不区分大小写）。代码里的变量名、接口字段名不受限制，只查显示给买家的文字。

### 6.8 其他用户端页面

- `/batch`、`/billing`、`/history`、`/lookup`、`/partner/swap`：套用 §3 的外观，去掉 §6.7 的禁用词，流程和接口不变。
- 去掉这些页面顶部的 `RedeemModeTabs` 四格切换，改为页头左侧「返回首页」链接，页脚放和首页一样的三个链接。
- `/lookup`、`/history` 的单张查询结果卡换成 §6.1 的结果卡组件。

### 6.9 顺带修的后端问题（已取消）

没有 ZovoCard 卡服务，本节取消（见文首变更）。下面原文只作记录。

售后查询把 ZovoCard 上游返回的 `failed_precharge` 当成处理中：`backend/internal/handler/lookup.go` 第 349 行的失败分支只认 `failed`、`declined`、`cancelled`，`failed_precharge` 落到第 352 行被判成 `processing`；第 273 行任务表那一支也不认它。第 ⑤ 屏和首页都靠售后查询判断，所以这两处都把 `failed_precharge` 加进失败分支，可不可重提仍由 `zovoMarkFailed` 按码的状态判断。先写失败的测试再改。

## 7. 前端结构

| 文件 | 职责 |
|---|---|
| `src/styles/user-theme.css`（新） | §3.1 的颜色变量。亮色默认；`[data-theme="dark"]` 和 `@media (prefers-color-scheme: dark)`（跟随系统时）给暗色。只挂在用户端根节点上，不影响后台皮肤 |
| `src/lib/brandColor.ts`（新） | `contrastRatio(a: string, b: string): number`、`isUsablePrimary(hex: string): boolean`、`darkPrimaryFrom(hex: string): string`、`tintFrom(hex: string): string` |
| `src/lib/device.ts`（新） | `detectDevice(ua: string, maxTouchPoints: number): 'ios' \| 'android' \| 'desktop'`。iPadOS 报 Macintosh 且有触屏时算 `ios` |
| `src/lib/sessionCheck.ts`（新） | `checkPastedSession(raw: string, opts: { now: number }): SessionCheck`，规则见 §6.3.1（只有严格规则）。`SessionCheck` 为 `{ ok: true; value: string; maskedEmail?: string }` 或 `{ ok: false; problem: 'empty' \| 'not_logged_in' \| 'access_token' \| 'not_json' \| 'no_token' \| 'no_email' \| 'expired' }` |
| `src/lib/redeemProgress.ts`（新） | `progressRows(status: string, stage: string, prev?): { rows: ['done' \| 'current' \| 'todo', …三项]; phase: 'running' \| 'done' \| 'failed' \| 'review' }`，对应关系见 §6.5 |
| `src/composables/useRedeemFlow.ts`（新） | 兑换流程的状态和接口调用：`preview`、`preflight`、`redeem`、`checkResult`、`recoverGrace`、`reset`。保证同一张码同一时间只有一个兑换请求；兑换请求失败后只能走 `checkResult`。进度缓存见下 |
| `src/components/redeem/`（新目录） | `CodeEntry.vue`（第 ① 屏，首页和兑换页共用）、`CredentialStep.vue`（②）、`DeviceGuide.vue`、`SessionPasteBox.vue`、`ConfirmStep.vue`（③）、`ProgressStep.vue`（④）、`ResultStep.vue`（⑤）、`ProgressTicks.vue`（顶部三格）、`CodeStatusCard.vue`（结果卡，逻辑仍用 `afterSaleView`） |
| `src/views/user/RechargeView.vue` | 只负责按 `useRedeemFlow` 的状态切换五屏，目标 300 行以内 |
| `src/views/HomeView.vue` | `CodeEntry` + `CodeStatusCard` + 常见问题 |
| `src/components/UserPageHeader.vue` | Logo、站名、语言切换、亮暗切换；去掉皮肤选择 |
| `src/views/admin/SiteAppearance.vue` | 加主色（6 个预设 + 色号输入 + 实时对比度提示）、Logo 上传和删除；皮肤选择改名「后台皮肤」 |
| `src/i18n/locales/zh.ts`、`en.ts` | 新文案放在 `redeemUi` 下；点数兑换须知移进语言包；改正 X 的说明 |

**进度缓存**：键改为 `cdk_redeem_progress_v2`（`sessionStorage`），启动时删掉旧键 `cdk_redeem_progress_v1`。
- 存：当前屏、卡密、兑换令牌、预检票、预览摘要、账号摘要（只含脱敏邮箱、当前套餐、各个判断标记）、结果的 status 和 stage、保存时间。
- 不存：登录信息原文、邮箱密码、Claude 的 Organization ID、Grok 登录态、须知勾选、最近付款和支付方式。
- 超过 7 天丢弃，沿用现在的规则。

**不再在用户端使用**：`RedeemModeTabs.vue`、`SkinPicker.vue`（后台仍用）、`index.html` 里的 Google 字体预连接。

## 8. 出错时怎么处理

| 情况 | 买家看到的 | 系统怎么做 |
|---|---|---|
| 网络断了、服务器 5xx | 当前这屏写「没连上，请再试一次」，附「再试一次」按钮 | 已填的卡密、登录信息不清空 |
| 公开接口限流（429） | 「操作太频繁，请过几分钟再试」 | 不自动重试 |
| 预检票过期（兑换返回 400） | 回到第 ② 屏，提示「核对已过期，请重新粘贴登录信息」 | 卡密保留 |
| 兑换被拒但没建单（熔断、服务没准备好、跨服务商冲突） | 停在第 ③ 屏，显示原因 | 沿用现有逻辑，不跳去等一个不存在的单 |
| 点了「确认开通」 | 按钮立刻变成「正在提交…」并锁住 | 一张码同一时间只发一次兑换请求 |
| 点了「确认开通」后网络断了 | 「没收到回复，不确定是否已提交。点下面按钮查一下」，按钮是「查一下结果」 | **绝不自动重新提交兑换**。「查一下结果」只按卡密查进度（`result-by-code`）：查到单就进第 ④ 屏；返回码仍未使用、没有进行中的单，才回到第 ③ 屏，由买家自己再点 |
| 开通中刷新或关掉页面 | 回来后接着显示第 ④ 屏 | 按卡密查进度恢复 |
| 等了很久还没有结果 | 「还在处理，可以关掉页面，稍后回首页粘贴卡密查看」 | 前端继续按原频率查，不改后端 |
| 首页查卡密出错 | 输入框下面写原因：格式不对、查不到这张码、网络问题 | 不跳页 |

所有出错文字都用界面的口吻，写清楚发生了什么、下一步做什么，不道歉、不含糊。

## 9. 测试

先写测试、看它失败，再写代码。全部用假数据，不调真实的 ChatGPT，不付钱。

### 9.1 后端

- `eligibility.go`：开通码、点数码的每条规则做成表格测试，覆盖每个放弃码。
- 付款引擎原有测试（`check`、`checkCredit` 相关）全部原样通过，证明抽函数没有改变行为。
- `InspectAccount`（用假的 ChatGPT 客户端）：
  - 符合；
  - 开通码遇到已订阅账号；
  - 点数码的每种不符合；
  - Session 解析失败、刷新未授权、账号接口未授权 → `session_invalid`；
  - 代理没配、网络错误、429、Cloudflare、超时 → `unknown`；
  - 欠费状态缺失时会调 `SubscriptionStatus`。
- `localCDKPreflight`：
  - 四种结论各自的 HTTP 状态和字段；
  - 不符合时不发预检票，拿旧票也兑换不了；
  - 每码每小时第 11 次按「查不了」处理；
  - 5 分钟内同码同账号第二次不再调 ChatGPT，超过 5 分钟会再调；
  - `unknown` 不缓存；
  - 引擎没启动时按「查不了」处理；
  - 抓取日志输出，断言里面没有 sessionToken、accessToken、完整邮箱。
- 外观设置：
  - 主色格式和对比度校验，6 个预设都能保存，#7FD4C4 这类浅色被拒；
  - Logo 的 PNG、JPEG、WebP 能存；SVG、GIF、改了扩展名的文本、超过 256 KB 都被拒；
  - 公开读取带正确类型头和 nosniff；没有 Logo 时 404；
  - 没设过 `theme_mode` 时，公开站点配置返回 `auto`。

### 9.2 前端（vitest）

- 登录信息检查：§6.3.1 表里每一行各测一遍；`expires` 解析不了不算过期；邮箱脱敏。
- 设备识别：iPhone、iPad、安卓、Windows、macOS 各一条 UA。
- 进度对应：§6.5 表里每个后端状态都映射到正确的行和打勾状态；未知状态落到「处理中」，不报错。
- 首页分流：每种卡密状态进入正确的去向。
- 兑换流程模块：
  - 「确认开通」连点只发一次请求；
  - 兑换请求网络失败后，只调查询接口，不再调兑换接口；
  - 预检票过期回到第 ② 屏。
- 主色推导：6 个预设推出的暗色主色都满足对比度要求。
- 文案：中英文两套的键完全一致；用户端组件里不出现 §6.7 的禁用词（测试扫描源码和语言包）。

### 9.3 视觉检查

用 Playwright 截图，逐张看：首页、五屏、各种结果、出错状态。每张截四种组合：375px 和 1280px 宽，亮色和暗色。看文字是否被截断、按钮是否够大、暗色下是否看得清。

### 9.4 本地门禁

gofmt、go vet、golangci-lint、`go test -race`（paybrowser 单独跑）、govulncheck、vitest、前端构建、gitleaks。

## 10. 上线

### 10.1 发版说明要写的

按「升级注意」三块写在最前面：

**升级前必须做：** 无数据库迁移。新的设置项都有默认值，不用提前配置。

**升级后要确认：**
- 打开后台「站点外观」，确认主色和 Logo 是想要的。以前选过的皮肤只影响后台。
- 刷新所有开着的后台页面，旧页面不认识新的设置项。
- 用手机打开首页，粘贴一张测试码，用一个已有订阅的账号点「核对账号并开通」，应停在第 ③ 屏并说明账号已有订阅、不会扣款。

**行为变化：**
- 用户端统一为新外观，后台选的皮肤不再影响用户端。
- 以前没设过亮暗模式的站点，原来默认暗色，现在跟随买家的系统设置。
- 用户端不再从 Google 加载字体。
- 兑换页顶部的四格切换（单张、批量、查询、账单）去掉，改为页脚链接；开通中的进度改为三行打勾。
- MarsWallex 码粘贴登录信息时，前端检查和后端一致：必须是完整 JSON，带账号邮箱。以前前端放行、提交后才被拒。
- MarsWallex 码在确认页就会告诉买家账号符不符合。每次兑换多 2～3 次 ChatGPT 请求，走这张码所在地区的代理。查不了时退回让买家自己勾选确认。
- 买家填完登录信息点「核对账号并开通」后，账号核对通过就直接开通，不再多点一次「确认开通」。账号不能用、要先恢复订阅、查不了账号时仍停在核对页。

### 10.2 真实环境验证

合并前只用假数据测试。上线前在 VPS 上用一个已有订阅的真实账号点「核对账号并开通」，确认停在第 ③ 屏、说明已有订阅、没有下单。核对通过后会自动付款，所以不能再用「走到第 ③ 屏就停」的办法验证可以开通的账号；要验证真实开通，需用户同意并在场、用一张真实码。这一步要用户同意并在场时再做，Session 不进对话。

## 所属项目：界面改版（2026-11-09 用户确认的拆分）

| 子项目 | 内容 | 状态 |
|---|---|---|
| A | 用户端兑换（本文） | 设计中 |
| B | 后台外壳与概览，删除旧皮肤 | 未开始 |
| C | 常用后台页：CDK 卡密、卡池、自动付款 | 未开始 |
| D | 其余后台配置页 | 未开始 |

另外立项的后端改进：无（「预检时查账号」已并入本文 §5）。

另外立项（2026-11-09 用户决定）：把 ZovoCard 对接从代码里整个删掉，保留 X、Grok、Claude 的界面和数据结构，以后接别的服务商。在 A 之后做，按设计 → 计划的流程走。
