# acg-faka「店铺共享」协议调研报告（我们做上游，下游是原版 acg-faka）

调研版本：lizhipay/acg-faka 最新发布版 **3.8.5**（config/app.php:5，发布于 2026-10-28）。
对比版本：**3.1.0**（git 提交 18eea9b）、**3.1.3**（463bf5b）。GitHub 上没有早于 3.4.4 的 tag，旧版是从 git 历史里取的。
源码只在本机下载后只读查看（没有执行任何源码），未入库。2026-11-05 由子代理调研、控制者核对后落盘；第 4 条「币种设置自 3.6.1 起」由控制者按 acg-faka git 历史（`app/Util/SharedCurrency.php` 首次加入的提交属于 3.6.1）补充确认。
下文路径都相对 3.8.5 源码根目录，`文件:行号` 即出处。

---
## 0. 先说几个会影响设计的发现（对旧调研的纠正与补充）

1. **业务错误的格式是 `{"code":0,"msg":"..."}`，HTTP 状态码 200**。原版框架抛 JSONException 时 code 取默认值 0（kernel/Kernel.php:211-213）。下游只检查 `code != 200`，所以我们返回任何非 200 的 code 都行。
2. **HTTP 4xx/5xx 会丢掉 msg**。下游用 Guzzle，默认 http_errors=true（app/Util/Http.php:20-22；Bind/Shared.php:83-94）。非 2xx 会被当成「连接失败, 疑似被对方防火墙拦截」，对终端客户则显示「商品暂时无法购买，请稍后重试」。业务错误必须用 HTTP 200 加 code≠200 返回。
3. **签名原样覆盖收到的所有表单字段**，不要求 app_key 字段存在（详见第 7 条）。
4. 原版上游的 `app_id` 就是上游用户表的主键 id，我们可以改成任意字符串。下游后台限制 app_id 为 `^[A-Za-z0-9._:@-]{1,32}$`，app_key 为 1–64 位且不含空白、控制字符（Admin/Api/Store.php:143、160）。
5. **没有任何定时任务**。核心里没有 cron，价格、库存、上下架的同步只会在特定时机触发（详见第 12 条）。
6. 导入时，下游**总是把本地 delivery_way 写成 1**，忽略上游的 delivery_way（Admin/Api/Store.php:1182）。
7. **没有币种换算协议**。金额只是数字，币种由站长在店铺档案里手填（见第 4 条）。

---
## 1. 下游会调用哪些接口、什么时候调用

地址 `{域名}/shared/{controller}/{method}`，全部 POST、form-urlencoded（Bind/Shared.php:72-106）。type=0 就是原版协议；type=1 是码支付 open-api，type=2 是 SharedStock 插件，这两个不用管。

| 接口 | 触发时机 | 必需？ |
|---|---|---|
| authentication/connect | 后台「新增/编辑店铺」保存时（Admin/Api/Store.php:947）。保存前必须连通，失败就不保存。点「连接」按钮时再调一次（:1001） | **必需** |
| commodity/items | 后台「导入商品」打开商品树时（Store.php:1031） | **必需** |
| commodity/item | ① 导入每个所选商品时（Store.php:1167）。② 买家打开商品详情页且 shared_sync=1 时，由 syncRemoteItem 调用（Shop.php:165-166 → Bind/Shared.php:872）。③ 后台点「同步店铺商品」，逐个商品调用（Store.php:1348） | **必需**，导入和同步全靠它 |
| commodity/stock | 买家下单时检查库存（Bind/Order.php:749 → Bind/Shared.php:604-610）。商品页读库存时也会走（Shop.php:336，结果缓存在 shared_stock，成交后失效） | 缺了会降级 |
| commodity/valuation | 买家下单时询价，结果记为订单成本 rent，并做「不得低于上游进价」检查（Order.php:751、811） | 缺了会降级 |
| commodity/trade | 付款成功、发货时（Bind/Order.php:1150） | **必需** |
| commodity/inventory | 核心下游不会主动调它，只有降级时才用：stock 不存在时取 `count`；valuation 不存在时取 `factory_price` / `category_factory`；没有 `factory_price` 字段时调 `remoteCost`（Bind/Shared.php:157-163、620、844） | 降级路径用到 |
| commodity/draft、draftCard | 仅当商品 draft_status=1（预选卡密）时才调：下单询价 draft（Order.php:238、376），选卡列表 draftCard（User/Api/Index.php:323） | 我们把 draft_status 设为 0 就不会被调 |
| commodity/inventoryState、commodity/query | **3.8.5 核心下游从不调用**（只有上游控制器自己在「套娃」场景里转调，Controller/Shared/Commodity.php:256）。grep 未找到任何调用点（commodity/query 全仓库只有上游控制器定义，没有任何调用点） | 可以不实现，实现了也无妨 |

**降级规则（我们回 404/405 时）**：`postOptional` 只对 404/405 视为「老上游」（Bind/Shared.php:67-70、84-88），其它状态码、超时、连不上一律报错。
- stock：返回 404 就改调 inventory，取其 `count`；同时把该店铺记为 PROTOCOL_LEGACY，写入 shared.protocol 列（:604-622）。
- valuation：返回 404 就走 legacyValuation，用 inventory 的 `factory_price × num` 估算；算不出来返回 0，此时 `$rent==0` 回退本地成本 getCost（:696-698、636-661、Order.php:765）。
- draft：返回 404 就按 `draft_premium=0` 处理（:527-532）。
- 一旦店铺被标成 LEGACY，之后就**不再尝试** stock、valuation、draft 这些新端点。
- 同时返回单商品 item 又被标成 MODERN 时，两种判断会互相覆盖，容易乱。**建议 stock、valuation 都实现，不要依赖 404 降级。**
- 3.1.x 老协议下 item 用 `sharedCode` 参数、返回分类树。3.8.5 会同时发 `code` 和 `sharedCode`，并按响应形状识别（:278-310、352-369），单商品对象才是现代格式。

---
## 2. 下游发送的确切参数

所有请求都先 `array_merge($data, ["app_id"=>…, "app_key"=>…])`，再算 sign 加进去（Bind/Shared.php:74-75）。所以**每个请求都带 app_id、app_key、sign**（3.1.0 起就是如此，3.1.0 的 SharedService.php:71-72）。

| 接口 | 业务参数（出处 Bind/Shared.php） |
|---|---|
| connect | 无 |
| items | 无 |
| item | `code`、`sharedCode`（两个值相同，都是上游商品 code）。:352-356 |
| stock | `code`、`race`（可能为 null，null 时不发该字段）、`sku`（数组，可能为空数组或 null，此时不发）。:606-610 |
| valuation | `code`、`num`、`race`（可能不发）、`sku`、`card_id`。:688-694 |
| trade | `shared_code`、`contact`、`num`、`card_id`、`device`、`password`、`race`、`request_no`、`sku`（空数组则不发），再加每个 widget 的字段，字段名取 widget.name，值是用户填的值。:420-434 |
| inventory | `sharedCode`、`race`（字符串，可能为空串）。:583-586 |
| draft | `code`、`card_id`，可选 `race`、`sku`。:516-522 |
| draftCard | `code`、`sharedCode`、`page`、`limit`、`race`，以及 `equal-/search-draft` 过滤。:449-458 |
| inventoryState | `shared_code`、`card_id`、`num`、`race`。:389-394（核心下游不调用） |

类型说明：所有值都按字符串发出，整数发成 "1"，空字符串仍然发送，但**不参与签名**。`sku` 在 PHP 里是关联数组，form 里表现为 `sku[颜色]=红`；`widget` 在 form 里就是平铺的 `键=值`。

---
## 3. 下游解析的响应结构

通用外壳（Bind/Shared.php:96-105）：
```
{"code":200,"msg":"success","data":...}
```
`json_decode` 失败，或 code 不是 200（宽松比较 `!=`），就报错并展示 `msg`。data 会被强转成数组。

**connect**：`data:{"shopName":str,"balance":数字}`（上游 Authentication.php:23）。下游校验（Store.php:173-189）：shopName 是标量，去标签后 1–128 字符且无控制字符；balance 必须是 `is_numeric`，范围 0..999999999999.99，否则报「远端店铺返回的数据格式不正确」。

**items**：分类树 `data:[ {id,name,sort,icon,status,pid, children:[商品...]} ]`（上游字段白名单 SharedPayload.php:60）。
- 下游强校验（Store.php:252-297）：分类数 ≤200；每个分类必须有标量 `name` 和数组 `children`，否则整体报错；单个商品不合规就**静默跳过**并提示「已跳过 N 条异常商品」。
- 商品只用 `id`（正整数，≤4294967295，且不能重复）、`code`（缺省取 id，1–64 字符，不含空白或控制字符，不能重复）、`name`（去标签后 1–255 字符）。
- 树里不使用价格，所以树里的商品可以只给 id、code、name。上游原版会输出的完整商品字段见 SharedPayload.php:49-57。

**item**：单个商品对象，字段如下（Store.php:743-815 remoteItem；Bind/Shared.php:312-376）。
- `name`：必填，去标签后 1–255。
- `description`：HTML，≤1MB。会过 HTMLPurifier 白名单，`<img>` 的 src 必须是合法 http(s)；**src 为空会报错**，不想要图就去掉整个 img 标签。
- `cover`：空串则本地用 /favicon.ico；非空时必须是 http(s) 地址，会拼在店铺域名下解析，开启「图片本地化」时还会真下载。
- `price`、`user_price`：数字，范围 0..99999999.99，缺省当 0。
- `config`：INI 文本或数组（见下）。
- `widget`：JSON 字符串或数组，缺省 `[]`。
- `contact_type`：0..3，缺省 0。
- `password_status`：0..1。
- `seckill_status`：0..1；若为 1，则 `seckill_start_time`、`seckill_end_time` 必须是 `Y-m-d H:i:s` 且结束时间晚于开始时间。
- `draft_status`：0..1。
- `draft_premium`：金额。
- `inventory_hidden`：0..1。
- `only_user`：0..1。
- `purchase_count`：0..4294967295。
- `minimum`：0..4294967295。
- `stock`：整数，0..2147483647。
- `maximum`：**3.8.5 导入和同步都不读，忽略**。
- `delivery_way`：**导入时忽略**，本地一律写 1（Store.php:1182）。
- `factory_price`：取值见第 4 条。
- `code` 不从响应读，下游用它请求时传的那个 code。
- 整数字段接受 int、数字字符串、整数值的 float，否则报「必须是整数」（Store.php:490-506）。
- 解析不了时的报错信息会展示给站长，例如「远端商品字段 name 不能为空」。

**config 的 INI 格式**（kernel 的 Ini，app/Util/Ini.php:59-92、119-128）。
```
[category]
种类A=10.00
种类B=12.00
[wholesale]
10=9.50
[category_wholesale]
种类A.10=9.00
[sku]
颜色.红=1.00
```
- 节名用 `[...]`，行用 `键=值`，**一行里等号必须恰好一个**，否则抛「配置解析异常」。
- 点号 `.` 表示嵌套，所以**种类名里不能带点**（`1.5月卡` 会被拆成嵌套）。
- 任何以 `]` 结尾的行都被当成节头。
- 行为空字符串、或整行为 "0" 会被 `empty()` 跳过。
- 计价时用到的是 category、wholesale、category_wholesale、sku 四节（Bind/Order.php:100-145）。
- **没有种类、没有批发价、没有 SKU 的普通商品，`config` 给空串就行**。
- `config` 里的 `category_cost`、`sku_cost`、`shared_mapping`、`*_factory` 是下游内部字段，上游不要输出（SharedPayload.php:95-98）。
- 下游同步 config 时，会把上游 `category` 存成本地 `category_cost`、上游 `sku` 存成 `sku_cost`（Bind/Shared.php:901-909）。

**inventory**（上游 Controller/Shared/Commodity.php:379-388）：`{count, delivery_way, draft_status, price, user_price, config(INI文本), factory_price, is_category}`。下游只在降级路径读 `count`、`config.category_factory`、`factory_price`。

**stock**：`data:{"stock":数字}`，下游 `(string)($stock['stock'] ?? "0")`（Bind/Shared.php:614）。

**valuation**：`data:{"price":总价数字,"currency_code":"CNY"}`（上游 :534）。`price` 是**这个数量、这个规格、这个种类下的总成本价**。`currency_code` 若与店铺档案里手填的币种不同，只写错误日志，**不阻断**（Bind/Shared.php:703-707）。解析异常整体被吞成 0。

**inventoryState**：下游只要 code=200 就算有库存，不看 data（:389-396），但核心下游本来也不调。

**draft**：`{"draft_premium":数字}`；**draftCard**：`{list:[{id,draft,draft_premium}],total}`（我们不开预选，可以不实现）。

**trade**：下游只读 `data.secret`（Bind/Shared.php:441）。上游原版 data 是 `{url, amount, tradeNo, secret, leave_message, stock}`（Order.php:1020-1022，Controller/Shared/Commodity.php:410 会去掉 delivery_files）。**其它字段下游都忽略，secret 必须存在**，缺失会成为空串。

**query**：`{secret, widget, status}`，下游不用。

---
## 4. 价格怎么用

**导入时存的价格**（Store.php:1218-1255）：
- `local.price = 上游 price + 加价`；`local.user_price = 上游 user_price + 加价`（user_price 为 0 或空则保持 0，Bind/Shared.php:757-766）。
- `category`、`sku`、`wholesale`、`category_wholesale` 每个价格同样加价。
- 加价有三种模式（Store.php:1122-1136）：0=固定值，1=百分比，2=加价模板。加价留空视为不加价。
- 导入对话框里的价格同步、信息同步、配置同步三个开关默认都是 0（`binaryFlag($map[...] ?? 0)`，Store.php:1139-1141）。**开关没开，商品导入后价格就不会再变**。

**用 price 还是 user_price**：下游把两者**都**存下来，分别加价（本地 price 是游客价，user_price 是登录会员价）。会员价为 0 时，本地回退到零售价（Bind/Shared.php:757-761，Order.php:97）。**上游对下游返回的 price 就是下游的「标价」，user_price 给 0 就行**。

**成本怎么算**：
- 有 category 的商品，每个种类的拿货价来自 item 的 `config.category_factory`（上游 item/inventory 按下游身份现算）。
- 无 category 的商品，成本取 item 的 `factory_price`（3.6.5 起 item 带该字段）；没有就调 inventory 取（Bind/Shared.php:839-854）。
- 下游订单的实际成本 `rent` 取 valuation 的 `price`，**不是**本地 price。
- 重要：下单时 `assertNotBelowUpstreamCost`（Order.php:583-601）会比较「本单实收」与上游 valuation 报价，若实收更低，就拒绝下单并报「商品价格已变动，请刷新页面后重新下单」。**如果我们调价而下游没同步，下游会按旧价卖、被拦。**
- 所以我们的 `price`（零售标价）要 **≥** valuation 报价，下游加价后才不会被拦。

**币种**：协议里没有币种字段。下游有店铺档案字段 `currency`（默认 CNY）和 `currency_rate`（1 上游币 = ? 本站币），由站长手填；所有从上游拉回来的金额都按此系数换算（App/Util/SharedCurrency.php:28-108、Bind/Shared.php:238、314、541）。valuation 额外带 `currency_code` 用于告警。**我们的金额币种需要和代理商约定，建议返回 CNY 数字，并告知他们在店铺档案里填对。**（设计已定：报美元，代理商在店铺档案里设币种 USD 和汇率，见 `docs/superpowers/specs/2026-11-05-acg-shared-design.md`。币种设置 3.6.1 起才有。）

**价格同步时机**：只有三种。(a) shared_sync=1 时买家访问商品详情页；(b) 后台点「同步」；(c) 导入时。**无定时任务**。同步时是否真的覆盖价格由 shared_amount_sync、shared_config_sync 决定（Bind/Shared.php:924-935）。

---
## 5. 付款后调 trade 的流程

- **同步调用**，发生在付款回调的事务里。超时 30 秒（Bind/Shared.php:78；Order.php:1150）。
- 余额支付、0 元单是在下单请求里同步调；网关支付是在回调里调（Order.php:941、967、1376）。
- **失败（code≠200，或超时、网络异常、非 2xx）**：`post()` 抛 JSONException，整个 DB 事务回滚。
  - 订单仍是 `status=0`（未支付），没有发货。
  - 网关支付场景下，回调接口返回错误，由支付网关重发通知。
  - 回调里 `status!=0` 才拒绝，所以**未支付状态的订单会被重新处理，再次调用 trade**，`request_no` 不变（它就是下游订单的 trade_no，18 位纯数字，Order.php:1150、Str.php:152-157）。
  - **下游不会自动退款**，没有「失败」状态，也不会主动通知客户；余额支付场景里整笔下单回滚，扣款一并回滚。
  - 没有专门的重试机制，完全依赖支付网关的重试策略。
- `request_no` 格式：下游订单号，`random_int` 生成的 18 位数字字符串。**从 1.0.9（2023-06-13）起就随 trade 发送**，3.1.0、3.1.3、3.8.5 都有。
- 重复 request_no：原版上游报 `The request ID already exists`（Order.php:865-867）。**这是个陷阱**：如果上游已成功建单但下游没收到响应（超时），下游重试时会一直收到 "already exists"，永远拿不到卡密，订单在下游卡死。
  - **我们的实现应该做成幂等：同一个 request_no 重复请求，直接返回第一次的 secret**，而不是报错。
  - 下游不调用 `query` 接口兜底，所以必须在 trade 里幂等。
- 失败时 msg 是否展示给终端客户：
  - 余额支付路径（客户在下单请求里直接触发）：会看到 msg。非管理员场景下，msg 里的域名、IP、URL 会被替换成 `***`，并截到 200 字符（Bind/Shared.php:99-104、Util/SharedPayload.php:325-365）。
  - 超时、连不上、HTTP 非 2xx：客户只会看到「商品暂时无法购买，请稍后重试」。
  - 网关回调路径：客户看不到，错误只回给网关，站长在日志里看。
  - 站长在后台操作时才保留原文。

---
## 6. num>1 时 secret 的格式

原版上游把多张卡用 `PHP_EOL`（`\n`）拼接，并 `trim` 掉首尾换行（Order.php:1260-1275）。**用 `\n` 分隔**。下游原样存进 `order.secret`，不拆分（Bind/Shared.php:441；Order.php:1150）。前台展示和邮件都是整段文本。

注意：没有 `delivery_files` 之类要求，文本卡密直接放进 secret。

---
## 7. 签名的精确算法

下游和上游共用 `Str::generateSignature`（app/Util/Str.php:130-140，3.1.0 起不变）：
```
unset($data['sign']); ksort($data);
删除值 === '' 的顶层键;
md5(urldecode(http_build_query($data) . "&key=" . $appKey))
```
细节：
- **ksort 只对顶层键排序**；嵌套数组（`sku`）里的键保持收到时的顺序，**不排序**。
- 只删顶层「值恰为空字符串」的键；`0`、`"0"` 保留；嵌套数组里的空串**不删**。
- `http_build_query` 对数组展开成 `sku[颜色]=红`（键和值先 urlencode，再被整体 urldecode 还原）。所以最终等价于：按顶层键字节序排序，每项写成 `键=原始值`，嵌套项写成 `sku[子键]=原始值`，用 `&` 连接，末尾追加 `&key=` + app_key，再取 md5 小写。
- 因为 `urldecode` 把所有编码还原，含 `&`、`=`、`+` 的值会产生歧义，但下游和上游做同样运算，所以我们只要**按收到的值原样拼接**就能对上。
- 签名覆盖的是**服务端实际收到的全部 POST 字段**，包括 `app_id`、`app_key`（如果发了）。上游校验直接用 `unsafePost()` 整个数组重算（SharedValidation.php:40-41），**不要求 app_key 字段存在**；是否带 app_key 都由接收到的内容决定。
- app_id 在下游是字符串（后台限制 1–32 位字符）；原版上游要求它是标量，并 `User::find($appId)`，即它是上游用户主键（SharedValidation.php:31-39）；3.1.0 是 `(int)$_POST['app_id']`。
- 比较：新版用 `hash_equals`，要求 sign 是字符串（SharedValidation.php:44）；3.1.0 是松散 `!=`。
- 错误响应：app_id 不存在→`商户ID不存在`；签名错→`密钥错误`；均为 code=0、HTTP 200。
- 顶层键如果含数字、`.` 或空格，PHP 会改键名（我们用不到，widget 的 name 已被下游限制为 `^[A-Za-z][A-Za-z0-9_]{0,31}$`，Store.php:548）。
- 下游默认 **总是在表单里带 app_key**（明文），所以上游必须走 HTTPS。

---
## 8. race、sku、card_id、widget、device、password、contact 的含义

- `race`：「商品种类」，对应 config 的 `[category]` 里的名称。有 category 的商品下单时必须选。没有种类的商品传空串。
- `sku`：规格，对应 config 的 `[sku]`：`sku[规格名]=选项名`，选项价是**加价**，累加到单价上（Bind/Order.php:138-145）。没有 SKU 就不会发。
- `card_id`：预选卡密的卡 ID。仅 draft_status=1 且 card_id≠0 时有意义；否则是 0。
- `widget`：商品的自定义下单字段（如游戏账号），以 `name=value` 平铺发在 trade 里。我们设 `widget='[]'`，就没有。
- `device`：购买设备类型整数（订单 create_device），我们可以忽略。
- `password`：买家设置的查单密码；只有 password_status=1 时才要求 ≥6 位（Order.php:705）。
- `contact`：买家联系方式。上游是 API 用户，原版会把它丢掉并改成随机串（Order.php:861-863）。我们可以忽略。

**让下游当成普通自动发货商品**：
- 不要出 category、sku；`widget='[]'`；`draft_status=0`；不要 SKU 配置。
- 下游就会发：`race=""`（空串不参与签名）、`card_id=0`、`device`、`password=""`，不带 sku。

---
## 9. 商品字段取值（「自动发货、无需额外输入」）

推荐 item/items 返回的值（3.8.5 下游校验通过且不触发额外流程）：

| 字段 | 值 | 说明 |
|---|---|---|
| id | 正整数（≤4294967295） | 必须有，且不重复 |
| code | 字符串 1–64 位，无空白 | 下游以此请求 item/stock/valuation/trade |
| name | 非空、去标签后 ≤255 | 必填 |
| description | 可为空串或不含 `<img src="">` 的 HTML | 空 src 的 img 会让导入失败 |
| cover | `""` | 下游会用自己的 /favicon.ico；给 URL 的话会被解析，开启图片本地化时会被下载 |
| price | 零售价（大于 0，≥ valuation 报价） | 0 会触发风控拒单（Order.php:926-938） |
| user_price | 0 | 表示会员价=零售价 |
| delivery_way | 0 | 下游忽略；不影响导入 |
| contact_type | 0 | 任意联系方式（匿名买家至少 3 字符） |
| password_status | 0 | 不要求密码 |
| minimum | 0 | 不限 |
| maximum | 0（可省略） | 下游导入不读；本地为 0 即不限 |
| only_user | 0 | 否则游客不能买 |
| purchase_count | 0 | 否则要求登录 |
| draft_status | 0 | 不走预选，不会调 draft/draftCard |
| draft_premium | 0 | |
| inventory_hidden | 0 | |
| seckill_status | 0 | |
| widget | `"[]"` | |
| config | `""` | 无种类、无批发、无 SKU |
| stock | 当前库存，非负整数 | 导入时写入；用 10000000 之类的大数表示不限量 |

注意：
- 下游买家下单时库存检查走 stock 接口，`stock==0` 或 `num > stock` 就报「库存不足」（Order.php:756）。
- 导入时 `stock` 字段是上游给的；之后的实时库存靠 stock 接口。
- 本地商品写 `delivery_way=1`，原版 Bind/Order.php:1149 对对接商品走 `$commodity->shared` 分支，不看 delivery_way，所以不影响发货。

---
## 10. 余额（balance）

connect 返回的 `balance` 只用来**展示和校验格式**：存进 `shared.balance`，后台店铺列表显示（Store.php:965、1007）。校验见第 3 条（数字，0..999999999999.99）。**下单前不检查余额**，也没有任何位置读它来拦单。余额不足的判断完全在上游 trade 里。

原版上游余额不足的 msg：走 `Bill::create(...)` 的扣款逻辑，具体文案在 Bill 类中（本次未展开读，未核实原文）。我们自己返回清晰的 msg 即可，客户不会直接看到（除了余额支付路径）。

---
## 11. 3.1.x 与 3.8.5 的兼容性差异

| 项 | 3.1.0 | 3.1.3 | 3.8.5 |
|---|---|---|---|
| item 入参 | `sharedCode`，返回**分类树**，只含该商品 | `code`，返回单商品 | 同 3.1.3；上游按 `api_status=1` 闸门（3.7.2 起 `dockedCommodity`） |
| items | 整行 toArray，含 `shared_code`、`factory_price` 等内部列 | 同 | 白名单字段 + 按身份算 `config.category_factory`（SharedPayload.php，3.7.2 起） |
| stock、draft | 无 | 有 | 有 |
| valuation | 无 | 无 | 有；`currency_code` 自 3.6.1 起带 |
| trade 的 `sku` | 无 | 有 | 有；3.8.2 起上游加 IP 白名单校验（`IpWhitelist::guardApiTrade`） |
| draft 带 race/sku | 无 | 无 | 3.8.4 起 |
| draftCard 入参 | `sharedCode,page,limit,race` | 新参数 | 两套都发 |
| app_key 字段 | 发送 | 发送 | 发送 |
| request_no | 1.0.9 起 | 有 | 有 |
| 校验 sign | 松散 `!=` | 同 | `hash_equals`（3.3.4 起） |
| 余额支付 | 强制 `pay_id=1` | 同 | 同 |

下游 3.8.5 同时兼容新老上游：通过响应形状判断（树还是单对象）与 404/405 降级。我们做新协议即可。

**3.8.2 起的 `guardApiTrade`**：上游若给下游账号配置了来源 IP 白名单，才限制来源；这属于上游自己的安全措施，我们不用复制，但如果我们做 IP 限制要注意。

---
## 12. 定时任务

**核心里没有任何与上游相关的定时任务**。仅有：买家访问详情页触发的惰性同步、后台手动同步按钮、导入。代码注释提到「自动货源接入（AutoDock）」常驻守护，那是第三方插件，不在核心里，频率不可知。因此：
- 价格变动不会被下游主动发现；下游靠 valuation 在下单时做一次拦截。
- 上下架：没有同步；上游商品下架后，下游商品仍显示，直到买家下单时 stock、valuation 或 trade 返回错误。

---
## 13. 原版上游的错误 msg（供我们对齐）

全部是 code=0、HTTP 200（见第 0 条），`msg` 是中文（经 `lang()` 翻译）：

| 场景 | msg | 出处 |
|---|---|---|
| 重复 request_no | `The request ID already exists` | Bind/Order.php:865-867 |
| 库存不足（trade/stock 检查） | `库存不足` | Order.php:756-757 |
| 库存不足（inventoryState） | `库存不足` | Controller/Shared/Commodity.php:283-285 |
| 商品下架 | `当前商品已停售` | Order.php:643-645；Commodity.php:249-251、306-308 |
| 商品不存在 | `商品不存在` / `商品不存在#0` | Commodity.php:135-147、522 |
| 未开放对接 | `该商品未开放对接` | Commodity.php:149-151 |
| 密钥错误 | `密钥错误` | SharedValidation.php:45 |
| 商户不存在 | `商户ID不存在` | SharedValidation.php:34、38 |
| 价格配置异常（0 元单） | `商品价格配置异常，暂时无法下单，请联系商家` | Order.php:937 |
| 余额不足 | Bill 类内（未核实原文） | — |

下游是否展示给终端客户：见第 5 条，只有余额支付路径会展示，且会脱敏域名和 IP。

---
## 附：对接我们时的建议清单

1. 实现 connect、items、item、stock、valuation、trade 六个接口；inventory 可选（兼容老下游），draft/draftCard/inventoryState/query 可选。
2. 所有业务错误都返回 **HTTP 200 + `{"code":非200,"msg":"…"}`**；只有路径不存在才返回 404。
3. 签名按第 7 条，覆盖所有收到的字段，不要求 app_key。
4. trade 按 request_no 幂等，重复请求返回原 secret。
5. 商品字段按第 9 条取值；金额币种与代理商约定。
6. 种类名不要含点号、等号，不要以 `]` 结尾。
