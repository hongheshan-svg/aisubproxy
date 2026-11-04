# 代理商余额充值：易支付 与 USDT-TRC20 接口核实报告

调研日期：2026-11-04。只读文档和源码，未向任何支付平台或 TronGrid 发请求。
说明：「已核实」指有源码或官方文档原文支持；「推断」是我的判断；「未找到」就是没查到。

## 来源说明（重要）

- 彩虹易支付没有找到官方公开文档站。本报告以 GitHub 上的开源源码镜像 maajiko/epay 为准（它自称彩虹易支付系统，README 写「采用 RSA 公私钥验证」）。
  - 这是第三方镜像，不一定是官方仓库。各家聚合支付二开版本可能不同，上线前必须拿你实际对接的平台文档再对一遍。
  - 仓库页：https://github.com/maajiko/epay
  - README：https://raw.githubusercontent.com/maajiko/epay/master/README.md
- 源码是通过 WebFetch 的摘要模型读取的，不是逐行原文。关键行为我标了「已核实」，但具体代码行请你在实现前自己再确认一遍。
- 辅助来源：Go 社区 SDK https://pkg.go.dev/github.com/AH-dark/epay-cli/pkg/epay 、其源码 https://raw.githubusercontent.com/AH-dark/epay-cli/master/pkg/epay/mapi.go 。

---

## 一、易支付协议

### 1. 发起支付

| 项 | submit.php（页面跳转） | mapi.php（接口） |
|---|---|---|
| 用法 | 浏览器 GET/POST 到该地址，平台跳到收银台 | 服务端 POST 表单，返回 JSON，拿支付链接/二维码 |
| 入口 | 只检查 pid，其余交给 `lib\api\Pay::submit()` | 只检查 pid（POST），缺失返回 `{"code":-4,"msg":"未传入任何参数"}`，其余交给 `Pay::create()` |
| 必填 | pid、out_trade_no、notify_url、return_url、name、money（type 为空会跳到收银台选支付方式，不报错） | 同上，但 return_url 非必填；另需 clientip（必须是合法 IP） |
| 其他可选 | param | device、param |

- 出处（源码）：
  - https://raw.githubusercontent.com/maajiko/epay/master/submit.php
  - https://raw.githubusercontent.com/maajiko/epay/master/mapi.php
  - https://raw.githubusercontent.com/maajiko/epay/master/includes/lib/api/Pay.php
- Go SDK 的 mapi 请求字段：pid、type、out_trade_no、notify_url、return_url、name、money、clientip、device、param、sign_type（写死 MD5）、sign。type 常量为 alipay、wxpay；device 取值 pc/mobile/wechat。出处：上面两个 AH-dark/epay-cli 链接。
- mapi 返回字段（Go SDK 结构体的 JSON 标签）：code、msg、trade_no、payurl、qrcode、urlscheme。**这是第三方 SDK 的定义，源码里我没能直接核实**（Pay::create 的返回字段由各支付插件决定，源码里只看到 `type`+`url` 的跳转形式）。
- 其他校验（已核实，Pay.php）：
  - out_trade_no 只允许 `[a-zA-Z0-9._\-|]`。
  - notify_url 必须是合法 URL。
  - name 会被 HTML 转义，超过 127 字节截断，命中违禁词会拒绝。
  - clientip 用 `FILTER_VALIDATE_IP` 校验。
- **money 格式**：
  - 源码只校验 `>0`、is_numeric、匹配 `/^[0-9.]+$/`，**不限制小数位数**；订单落库用 `sprintf("%.2f")`。
  - 一篇第三方教程写「单位元，最大 2 位小数，字符串，示例 1.00」（来自搜索结果摘要，页面我打不开：https://blog.csdn.net/weixin_71406372/article/details/127603048 ）。
  - 安全做法：自己发 `%.2f` 字符串。

### 2. 签名算法（已核实，Payment.php）

出处：https://raw.githubusercontent.com/maajiko/epay/master/includes/lib/Payment.php

1. 参与签名的参数：除 `sign`、`sign_type` 外的全部参数；**值为数组的跳过，空值跳过**。
   - 空值由自定义函数 `isEmpty()` 判断，该函数定义不在这个文件里，所以字符串 "0" 是否算空我**未核实**。
   - 实现时：money 本身不会是 "0"，但 param 之类可选字段若传 "0" 要小心；稳妥做法是不传。
2. 按参数名 ASCII 升序（`ksort`）。
3. 拼成 `k1=v1&k2=v2`，去掉末尾 `&`。
4. MD5：`md5(拼接串 + 商户KEY)`，**KEY 直接接在后面，没有 `&key=` 前缀**。
5. 输出为 **小写** 十六进制。验签是严格 `===` 比较，没有大小写归一，大写的签名会验不过。
6. `sign_type` 为空时按 MD5 处理；比较区分大小写，只有恰好是 `RSA` 才走 RSA 分支（`rsa` 会落到 MD5）。
7. RSA：SHA256 with RSA（`OPENSSL_ALGO_SHA256`），签名结果 base64。
8. 参数值用的是**原始值**（非 URL 编码后的值）：这一条是推断，源码摘要没明确写，实现时用解码后的值签名。

### 3. 异步通知（已核实，functions.php、cron.php）

出处：
- https://raw.githubusercontent.com/maajiko/epay/master/includes/functions.php
- https://raw.githubusercontent.com/maajiko/epay/master/cron.php

- **方式：GET**。把参数拼到 notify_url 后面（notify_url 里已有 `?` 则用 `&`），平台用 curl GET 请求，超时 5 秒。
  - 例外：平台配置了中转代理（proxy==2）时，是 POST 到代理，再由代理转发，不是直接 POST 给你。
- **参数（MD5 模式）**：pid、trade_no、out_trade_no、type、name、money、trade_status、param（非空才带）、sign、sign_type。
  - 老协议的 sign_type 是签完名之后才加的，不参与签名（本来也被排除）。
  - V2（RSA）模式额外带：api_trade_no、buyer、timestamp。
- **trade_status**：只会是 `TRADE_SUCCESS`（源码里写死；只在支付成功后才通知）。
- **money 格式**：源码是 `(float)money` 再拼 query，没有固定两位小数，可能是 `1`、`1.5` 这种。**所以不能按字符串比对金额，要解析成数值或转成分再比。**（推断：这是由 float 转字符串的行为得出）
- **必须回什么**：响应体里**包含**小写 `success`、`SUCCESS` 或 `Success` 即视为成功（`strpos` 判断，不要求完全相等）；回 `ok` 之类不算。
  - 我们应只回纯文本 `success`。
- **重发策略**：
  - 首次失败后设为待重发，第一步约 1 分钟后。
  - cron 里的代码是：第 2 次 2 分钟，第 3 次 16 分钟，第 4 次 36 分钟，第 5 次 1 小时，之后标记为失败（notify=-1）。
  - 代码注释写的是「1、3、20、60、120 分钟」，与代码不一致。
  - 只重试结束时间在最近一天内的订单，每轮 cron 最多处理 20 单。
  - 另有 notify2 分支会对 notify=-1 的订单立即再试一次。
  - 以上是该镜像的行为，其他二开平台策略不同。
- 同步跳转 return_url 用同样的参数，但失败不重发。**不要用 return_url 入账。**

### 4. 查单接口（已核实，api.php）

出处：https://raw.githubusercontent.com/maajiko/epay/master/api.php

- `api.php?act=order`，参数：
  - 商户方式：`pid`、`key`（商户密钥**明文**）、`trade_no` 或 `out_trade_no` 二选一。
  - 另有一种平台内部用的方式（`sign`=md5(SYS_KEY+trade_no+SYS_KEY)），商户用不了。
- 成功返回 `code=1`，`msg=succ`，字段：trade_no、out_trade_no、api_trade_no、bill_trade_no、type、pid、addtime、endtime、name、money、param、buyer、**status**、payurl。找不到订单返回 `code=-1`、「订单号不存在」。
- **status：1 表示已支付**（这是易支付通用约定；本镜像源码里 processOrder 把状态 0 或 4 改为 1，状态 2 跳到失败页。status 各值的完整含义我没找到文档）。
- 注意：密钥是明文放在 query 里的，必须用 HTTPS，且日志里不能记录完整 URL。建议用 POST。
- 列表接口：`act=orders`（pid、key、limit 默认 10 最大 50、offset、status）。
- 另有 V2 版本的 `query()`（Pay.php 里），返回含 trade_no、out_trade_no、type、pid、money、param、buyer、status、refundmoney；具体 V2 路径**未找到官方文档**。

### 5. V2（RSA）与老版（MD5）

出处：https://raw.githubusercontent.com/maajiko/epay/master/includes/lib/ApiHelper.php 、Payment.php、functions.php

- 已核实：
  - 商户有 `keytype` 字段，`keytype==1` 的商户**只能用 RSA**。
  - V2 请求必须带 `timestamp`，与服务器时间相差超过 300 秒即拒绝。
  - V2 的接口响应也会由平台用 RSA 签名（sign_type 写死 RSA）。
  - V2 通知多带 timestamp、api_trade_no、buyer，RSA 签名时 key 传空。
  - `pay/submit`、`pay/create` 在 V2 路由里不走统一验签，各自处理。
- 文档目录里有「签名规则」（RSA）「统一下单」「订单查询」等章节，但页面没有正文：https://docsearch.algolia.com/mcp/docs/repo/maajiko/epay
- V2 具体路径（如 /api/pay/create）：搜索没有找到参数和示例，**未找到**。
- **现在主流兼容哪个**：**未找到权威统计**。
  - 推断：老 MD5 协议（submit.php、mapi.php、api.php）几乎所有易支付兼容平台都支持；V2/RSA 只有新版彩虹易支付支持。
  - 建议首版只做 MD5，再给 V2 留扩展口。

### 6. 已知的坑与安全做法

1. **通知里没有可信的「金额已核对」保证**：通知只带 money，是平台按 float 拼的；验签通过只说明这些参数没被篡改，不说明金额等于你下单时的金额（例如平台自己改价、你下单时被篡改）。
   - 做法：验签 → 用 out_trade_no 找到**自己库里的订单** → 比对 pid、金额（转成分比）、订单状态 → 调 `api.php?act=order` 查单确认 status=1 且金额一致 → 才入账。入账用数据库事务 + 订单状态条件更新。
2. **重复通知**：重试策略保证至少一次送达，且你回 success 后偶尔仍会重复。入账必须幂等（以 out_trade_no 唯一、状态机只允许 待支付→已支付一次）。
3. **同一个 out_trade_no 再次支付**（已核实，Pay.php）：
   - 10 天内同 pid+out_trade_no：已支付则拒绝；money、name、notify_url、return_url、param 任一变化则拒绝并要求换单号；否则复用原订单号。
   - 超过 10 天不检查，可能出现两条同 out_trade_no 的记录。
   - 做法：每次充值生成新的 out_trade_no，且在我方库里给它唯一索引。
4. **回 success 的判定很宽松**：包含 success 字样就算。我们只需回纯文本 `success`；校验失败时回别的（如 `fail`），否则平台不会重发。
5. **GET 通知**：带参数的 URL 会进访问日志，不要在 URL 里放敏感信息；notify_url 要可公网访问、5 秒内响应，别在通知里做耗时操作。
6. **query 密钥明文**：见第 4 点。
7. **notify_url 白名单**：平台可能开启域名白名单（`pay_domain_forbid`），notify_url 域名要与平台登记一致。
8. **汇率**：汇率在下单时固化到订单（存人民币金额、汇率、折算美元），入账按订单上存的汇率，不要在通知到达时重新取汇率。（我的建议）
9. **用 return_url 入账**：不能，只有 notify 才能入账。

---

## 二、USDT-TRC20 只读查账（TronGrid）

### 1. 查地址收到的 TRC20 转账

出处：https://developers.tron.network/reference/get-trc20-transaction-info-by-account-address

- 接口：`GET https://api.trongrid.io/v1/accounts/{address}/transactions/trc20`，address 支持 Base58 或 Hex。
- 参数：
  - `only_to=true`：只要转入该地址的（默认 false）。
  - `only_from`：只要转出。
  - `contract_address`：按合约过滤。
  - `min_timestamp` / `max_timestamp`：**毫秒**；默认 0 / 当前时间。（同义参数 min_block_timestamp、max_block_timestamp）
  - `limit`：默认 20，**最大 200**。
  - `fingerprint`：翻页游标，取自上一页 `meta.fingerprint`，翻页时其余参数必须保持不变。
  - `order_by`：`block_timestamp,asc` 或 `block_timestamp,desc`（默认 desc）。
  - `only_confirmed=true`：只返回已确认的（不能与 only_unconfirmed 同用）。
- 返回：`data[]` 里每条有 `transaction_id`、`token_info{symbol,address,decimals,name}`、`block_timestamp`（毫秒）、`from`、`to`、`type`（一般 Transfer）、`value`（整数字符串）；外层 `success`、`meta{at,page_size,fingerprint,links.next}`。
- `value` 是最小单位，要除以 `10^decimals`，不是 sun。
- 注意：该文档页的示例响应是空的，上面是页面按 schema 整理，不是实测输出。

### 2. USDT 合约与精度

- 主网合约：`TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t`。
  - 证据：搜索结果里 TRON 官网 USDT 页、Tronscan、Bitquery 均指向该地址：https://tron.network/usdt 、https://dexrabbit.bitquery.io/tron/token/TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t 。
  - 局限：tron.network 页面我抓取时没拿到地址正文，tether.to 官方页**未找到**。上线前请你自己在 Tether 官网和 Tronscan 对一次。
- decimals = 6：TRON 官方 TRC-20 示例里「10 USDT 是 10000000」，但那是 Shasta 测试网合约的示例，页面没有直接写主网 decimals（https://developers.tron.network/docs/trc20-contract-interaction ）；搜索结果另有来源说是 6 位。
  - 建议：金额换算不要写死，读返回里的 `token_info.decimals`，并断言等于 6。

### 3. 怎么判断已确认、不会回滚

出处：https://developers.tron.network/docs/confirmation-semantics 、https://developers.tron.network/reference/gettransactioninfobyid

- 官方文档原文含义：
  - 最新区块的查询（`/wallet/...`）不是终局。
  - `/walletsolidity/...` 读的是已固化（solidified）区块的状态，**「通常比最新区块滞后约 1 分钟」**，做最终决定应以它为准。
  - `/walletsolidity/gettransactionbyid` 有返回 = 交易体已进固化链；`/walletsolidity/gettransactioninfobyid` 有返回 = 固化后的执行回执存在，**但回执存在不等于成功**，回执可能记录失败，要看执行状态。
  - TronGrid 是索引数据，会受分页、限流、索引延迟影响，**关键交易要用 /walletsolidity 再核一次**。
  - 要在本地存已处理的区块号、交易 ID、日志序号，让重试幂等。
- 需要多少个确认：**文档没给固定数字**。19 个超级代表、2/3 投票等共识细节文档页没写，我没有权威来源，不写具体数字。
  - 建议：以「walletsolidity 能查到且 receipt.result 为 SUCCESS」作为入账条件，不自己数确认数。
- 要不要查 `gettransactioninfobyid` 看 `receipt.result`：**要**。
  - 字段：`receipt.result`（成功时 `"SUCCESS"`），顶层 `result`，`blockNumber`，`log[]`，失败时有 `resMessage`。
  - 文档的枚举拼写是 `"SUCESS"`/`"FAILED"`，**以文档为准有拼写差异**，判断时建议同时兼容或看 `receipt.result == "SUCCESS"`。
  - 交易还没上链/没执行时，返回可能是 HTTP 200 但无内容或只有 `Error` 字段，要当成「未确认，稍后再查」。
- `only_confirmed=true` 在文档页里有，但 confirmation-semantics 页**没解释**它的语义，别单独依赖它。

### 4. API Key 与限流

出处：https://developers.tron.network/docs/rate-limits 及搜索到的各版本文档（v4.4.2、v4.7.0）

- 请求头：`TRON-PRO-API-KEY`。
- **当前文档不再给固定数字**：限额随套餐、网络、接口类型、服务策略变化，请看 TronGrid 控制台和价格页（https://www.trongrid.io/price ）。
- 没带 key：被严格限流甚至直接拒绝，可能按 IP 限，没有公布数值。
- 带 key：每个 key 有自己的配额和频率；同账号下的 key 可能共享总额；超频返回 429；日额度用完会被进一步限速；异常高频轮询可能被临时封禁（403/429）。
- 旧版文档的数字（**只作参考，已过时**）：v4.4.2 写每个账号每天 100,000 次，日额度内 15 次/秒，超额约 1 次/秒，超频会暂停 30 秒（https://developers.tron.network/v4.4.2/reference/rate-limits ）；v4.7.0 写带 key 且在额度内 20 qps，超额约 5 qps（https://developers.tron.network/v4.7.0/reference/rate-limits ）。
- 免费额度具体多少：**未找到现行官方数字**。
- 建议：必须配 key；轮询间隔 ≥ 10~30 秒；遇到 429/403 指数退避；用 `min_timestamp` 只查增量。

### 5. 入账识别方案对比

下面是设计分析（我的判断），不是查到的事实。

| | 单一收款地址 + 金额尾数 | 每个代理商独立地址 |
|---|---|---|
| 优点 | 只需一个地址，查账只扫一个地址，轮询量小；不需要私钥派生 | 到账即归属明确，不依赖尾数；少转多转不影响归属 |
| 缺点 | 尾数冲突；订单必须有超时；用户必须精确转账，少转/多转要人工处理；归集不需要 | 需要为每个代理商生成并管理地址；**只读服务器要得到地址就必须从外部提供**（预先离线生成的地址池，或 xpub 派生——TRON 常用 secp256k1，可由公钥派生，但要确认你的方案）；地址多则轮询量大；资金分散，要自己归集（归集要私钥，与「服务器不存私钥」冲突，只能离线操作） |

单地址 + 尾数的注意点：
- **尾数冲突**：同一时刻、同一基础金额下的待支付订单，尾数必须唯一（如 0.0001~0.0099 USDT 的范围）。要在库里对「待支付 + 金额」做唯一约束；尾数用完就拒绝新建订单或提示稍后。
- **超时**：订单有效期建议 30 分钟~1 小时，到期后**金额尾数释放前**仍要继续识别迟到的到账，避免错配到新订单。
- **多转/少转**：
  - 金额不精确匹配就不自动入账，进「待人工处理」。
  - 多转：可按实际到账金额入账或人工退回，须有策略。
  - 少转：不入账，记录等人工。
- **同一笔交易只入账一次**：以 `transaction_id`（建议加 log 序号）做唯一键；`transaction_id + 订单` 幂等。
- **别人也可能向该地址转账**（空投、别的用户误转）：不匹配任何订单的到账只记录，不入账。
- **来源地址**：可记录 from 但不要用作归属依据（交易所转账地址不固定）。

### 6. 已知的坑

1. **假 USDT 合约**：任何人都能发行同名 "USDT" 代币并转给你。必须校验 `token_info.address == TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t`，查询时也传 `contract_address`，入账时再校验一次，不要信 `symbol`/`name`。（依据：TRC20 是合约自报 symbol，任何合约可写；这是通用知识，TronGrid 返回的 token_info 来自合约本身）
2. **TRC10 与 TRC20 混淆**：`/v1/accounts/{address}/transactions/trc20` 只返回 TRC20 事件，TRC10 走别的接口；USDT 在 TRON 上是 TRC20。别拿 TRC10 的 asset_name 当 USDT。
3. **失败交易仍会出现在列表**：交易失败（REVERT/OUT_OF_ENERGY）一般不会产生 Transfer 事件，但文档明确说「回执存在不代表成功」，且 TronGrid 是索引数据。入账前用 `/walletsolidity/gettransactioninfobyid` 校验 `receipt.result == "SUCCESS"`。**TronGrid 是否会列出失败交易，我没有找到明确文档**，按保守做法处理。
4. **时间戳单位是毫秒**，`min_timestamp`、`block_timestamp` 都是毫秒；误传秒会查到一大片历史。
5. **value 是整数字符串**：用大整数或 decimal 处理，不要 float；换算要用 `token_info.decimals`。
6. **地址格式**：返回 Base58（T 开头）；比对时统一格式，别拿 Hex 和 Base58 直接比。
7. **分页**：翻页时 `fingerprint` 以外的参数不能变；用 `order_by=block_timestamp,asc` + `min_timestamp` 做增量最稳。
8. **索引延迟**：TronGrid 可能比链上晚，刚到的转账可能晚几秒到几十秒才出现；不要因为查不到就判定「没付」。
9. **Tether 可冻结地址**（搜索结果提到 Tether 保留黑名单能力）：收到的钱可能被冻结，属业务风险，不属于接口层。

---

## 建议的实现结论（摘要）

- 易支付：先做 MD5 + mapi 或 submit；签名严格按「排除 sign/sign_type/空值 → ASCII 升序 → `k=v&…` → 直接拼 KEY → 小写 md5」；通知用 GET；回纯文本 `success`；入账前验签 + 查我方订单 + 比金额 + `api.php?act=order` 复核；入账幂等。
- USDT：TronGrid + API Key；只认主网 USDT 合约；`only_to=true&contract_address=…&min_timestamp=…&order_by=block_timestamp,asc`；入账前再查 `/walletsolidity/gettransactioninfobyid`；`transaction_id` 唯一约束。

## 未找到 / 待你确认的清单

1. 彩虹易支付官方开发文档站（只找到第三方镜像源码）。
2. 老 MD5 协议 mapi 的返回字段官方说明（Go SDK 给出 code/msg/trade_no/payurl/qrcode/urlscheme，未在源码里直接核实）。
3. V2 具体路径和参数示例。
4. 现在哪种协议更主流（无权威统计）。
5. `isEmpty()` 对字符串 "0" 的处理。
6. TronGrid 现行免费额度和限流数值。
7. 「多少个确认」的固定数字（官方只说 walletsolidity 约滞后 1 分钟）。
8. tether.to 官方对主网合约地址的页面。
9. `only_confirmed` 的精确语义。

---

## 三、可复用的开源实现（补充调研）

调研方式：用 GitHub API 和仓库文档/源码只读查看，未下载运行任何代码。时间以 GitHub 返回为准（今天 2026-11-04）。
BEpusdt 文档与源码：https://github.com/v03413/bepusdt （API：https://github.com/v03413/bepusdt/blob/main/docs/api/api.md ，原理：https://github.com/v03413/bepusdt/blob/main/docs/api/how-it-works.md ，回调：https://github.com/v03413/bepusdt/blob/main/docs/notify/readme.md ，易支付兼容：https://github.com/v03413/bepusdt/blob/main/app/handler/epay/epay.go ）
Epusdt 文档与源码：https://github.com/GMWalletApp/epusdt （API：https://github.com/GMWalletApp/epusdt/blob/master/wiki/API.md ）

### 1. USDT 收款网关对比

| 项 | BEpusdt（v03413/bepusdt） | Epusdt / GMPay（原 assimon/epusdt） |
|---|---|---|
| 语言 | Go（gin、gorm） | Go |
| 许可证 | **GPL-3.0**（GitHub 元数据 + README 徽章） | **GPL-3.0** |
| 最近发版 | v1.24.3，2026-10-21；仓库最近推送 2026-11-01；约 2~3 周一版，**活跃** | v2.0.0，2026-07-23；最近推送 2026-07-29；**在维护但较慢** |
| 归属 | 个人作者持续维护 | **2026 年 3 月转让给他人**，仓库现在是 GMWalletApp/epusdt，assimon/epusdt 会跳转过去。BEpusdt 文档明确说两者已无关联（https://github.com/v03413/bepusdt/blob/main/docs/faq/epusdt.md ）。v2.0.0 起 API 大改，旧版 `/api/v1/order/create-transaction` 已不再注册（见其 API 文档开头） |
| 支持链 | Tron、Ethereum、BSC、Polygon，以及 X-Layer、Solana、Aptos、Arbitrum-One、Base、Plasma、Ton；不止 USDT，还有 USDC、TRX 等（https://github.com/v03413/bepusdt/blob/main/docs/trade-type.md ） | Tron、Ethereum、Solana、BSC、Polygon、Aptos 等（USDT、USDC、TRX 等），另可接 OkPay 托管收银台（其 README） |
| 部署 | **独立服务**：单体二进制，也有 Docker 镜像 `v03413/bepusdt`；内置后台、收银台、SQLite 之类轻依赖。代码在 `app/` 下，不是为被外部引入而设计的库 | 独立服务（二进制/Docker/宝塔，见 wiki），有后台 |
| 链上查询方式 | 全量扫块（Tron 用 gRPC 的 `grpc.trongrid.io:50051`，EVM 用 JSON-RPC），本地匹配订单；Tron 默认等 30 个区块偏移确认（源码 `blockConfirmedOffset: 30`，后台有开关 `BlockOffsetConfirm`） | 未细查 |
| 对外 API | 见下 | 见下 |

#### BEpusdt 原生 API（https://github.com/v03413/bepusdt/blob/main/docs/api/api.md ）
- 下单：`POST /api/v1/order/create-transaction`（JSON）。参数：order_id、notify_url、redirect_url、signature 必填；amount、trade_type（默认 usdt.trc20）、fiat、address、name、timeout（至少 120 秒）、rate 可选。
- 响应：`data` 里有 trade_id、order_id、amount（法币）、actual_amount（加密货币）、status、token（收款地址）、expiration_time、`payment_url`（收银台链接）。
- 另有：取消订单 `cancel-transaction`、收银台订单 `create-order`（用户自选币种）、MQTT 实时推送。
- 签名：非空参数、排除 signature、按 key 的 ASCII 升序、`k=v&…`、**末尾直接接 API Token、MD5、小写**（和易支付 MD5 同款）。
- 回调（POST JSON）字段：trade_id、order_id、**amount（法币金额）**、**actual_amount（实付加密货币数量）**、token（收款地址）、**block_transaction_id（链上交易哈希）**、signature、status（1 等待、2 成功、3 超时）。
- 回调应答：api.md 写返回 `success`（不区分大小写）；notify 文档写 HTTP 200 且响应内容为 `ok`（不区分大小写）；两处文档不一致。源码 `epusdt()` 里只检查了状态码 200。**我们的接收端若只回一个，两种说法对不上，建议回 `success`，并在联调时实测确认。**
- 重试：失败后 2、4、8、16、32、64 分钟……指数增长，最多 10 次（https://github.com/v03413/bepusdt/blob/main/docs/notify/readme.md ）。
- 支付成功回调之外，还会每分钟推「等待支付」（status=1，不重试）和「超时」（status=3）。**入账只认 status=2。**
- 订单两种模式：`amount>0` 为普通模式（地址+金额锁定）；`amount=0` 为**地址独占模式**（只锁地址，任意金额到账都回调，回调里的 amount 和 actual_amount 以实际到账为准），适合充值场景（api.md 开头）。

#### Epusdt / GMPay API（https://github.com/GMWalletApp/epusdt/blob/master/wiki/API.md ）
- 下单：`POST /payments/gmpay/v1/order/create-transaction`。参数：pid、order_id（最长 32）、currency（法币，如 cny、usd）、token（如 usdt）、network（如 tron）、amount（>0.01）、notify_url、redirect_url、name、signature。
- 响应：trade_id、order_id、amount、currency、actual_amount、receive_address、token、status、expiration_time、payment_url。
- 其他：收银台 `/pay/checkout-counter/{trade_id}`、状态 `/pay/check-status/{trade_id}`、配置 `/payments/gmpay/v1/config`。
- 签名：**HMAC-SHA256**（密钥是 secret_key），非空参数、排除 signature、ASCII 升序拼 `k=v&…`，输出 64 位小写十六进制；已不接受 MD5。
- 回调（POST JSON）：pid、trade_id、order_id、amount（法币）、**actual_amount（实到加密货币数量）**、receive_address、token、**block_transaction_id（链上哈希）**、signature、status（只在成功时回调，值 2）。应答 HTTP 200 且响应体为 `ok` 或 `success`；失败按指数退避重试，间隔最大 5 分钟。

#### 其他项目
- 我用 GitHub 搜索 "usdt trc20 payment" 等关键词（Go），**没有搜到比这两个更成熟、更活跃的 Go 项目**。
- 其他相关：GMPay Edge（Cloudflare Workers 版，非 Go，https://github.com/GMWalletApp/gmpay-edge ，在 Epusdt README 里列出）；BEpusdt 的彩虹易支付插件 https://github.com/v03413/Epay-BEpusdt 、PHP SDK、Python SDK（BEpusdt README 列出）。这些我没有逐个核查许可证和维护状态，**未核实**。

### 2. 币种与汇率

#### BEpusdt
- 下单金额可以是 **CNY、USD、EUR、GBP、JPY**，默认 CNY（`fiat` 参数，api.md）。
- **能不能直接指定 USDT 数量**：没有「按 USDT 数量下单」的参数；变通是 `fiat=USD` 且 `rate=1`，此时 USDT 数量 = 金额 ÷ 1 = 金额。
  - `rate` 支持：`7.4` 固定汇率；`~1.02` 上浮 2%（乘）；`+0.3` 加；`-0.2` 减；留空用系统配置（api.md 里的 rate 参数说明）。
  - **推断**：`fiat=USD&rate=1` 能做到 1:1，但我没有实测。
- 汇率来源：默认定时（间隔 3600 秒，可调）从 CoinGecko 抓取，写库；可以对每个「币种×法币」设浮动语法（固定值、乘系数、加减），最终汇率保留 4 位小数；下单时把汇率快照写入订单，支付期间不变（how-it-works.md 第 4 节）。
- **同金额冲突**：同地址同金额的待支付订单会自动把加密货币金额加 1 个最小精度单位（USDT 是 0.000001）并重试，最多 100 次，超过报错（how-it-works.md 2.2）。所以普通模式下 `actual_amount` 可能比 `amount` 多几个微单位。
- **按实际收到的 USDT 数量入账**：**可以**。原生 API 回调里有 `actual_amount`（实付加密货币数量）和 `block_transaction_id`（交易哈希）。独占模式下它们是实际到账值。
  - 但匹配模式有容差：Classic 完全相等；HasPrefix 允许多付小数位；RoundOff 四舍五入（how-it-works.md 2.3）。所以「订单金额」和「实际到账」可能有微小差异，**以回调的 actual_amount 为准入账**更准。
  - 也就是说：对账金额应取 actual_amount，而不是 amount。

#### Epusdt / GMPay
- `currency` 可写 `cny`、`usd`（API 文档）。汇率来源和固定/自动策略：**我没有在 API 文档中找到**（后台可能可配，未核实）。
- 回调里有 `actual_amount`（实到加密货币数量）和 `block_transaction_id`，所以同样可以按实收入账。
- 示例里 100 CNY 对应 actual_amount 14.29，说明有汇率换算。

### 3. 易支付兼容

#### BEpusdt（源码：https://github.com/v03413/bepusdt/blob/main/app/handler/epay/epay.go ，路由 app/router/epay.go ）
- **只有 `submit.php`**（GET 和 POST 都支持）。**没有 `mapi.php`**，也没有 `api.php` 查单（路由文件里只注册了 `/submit.php`）。
- 必填：pid、type、notify_url、return_url、out_trade_no、name、money、sign。
  - **pid 固定为 `1000`**，不是 1000 就报错。
  - `type` 就是 BEpusdt 的交易类型，如 `usdt.trc20`。
  - 额外可选（BEpusdt 自己的扩展）：**fiat、rate、timeout、address**。
- 签名：与易支付完全一致：排除 sign、sign_type 和空值，ASCII 升序，`k=v&…` 后直接接 Token，小写 MD5。传了 fiat、rate 等扩展参数就必须一起参与签名（源码里是把全部非空参数都签了）。
- 金额与币种：`money` 是法币金额，`fiat` 不传默认 CNY，所以默认 `money` 是**人民币**；传 `fiat=USD` 就按美元；传 `rate=1` 就固定 1:1。
- 成功后 302 跳到收银台。失败是 HTTP 200 的纯文本错误信息（如「签名错误」「参数 money 解析错误」），而不是 JSON。
- 还会校验 notify_url、return_url 是否「允许的回调地址」（`IsAllowedCallbackURL`，具体规则**未核实**，可能禁止内网地址，自部署同机回调要注意）。
- **异步通知（易支付格式）**：GET 请求，参数 money、name、out_trade_no、pid、trade_no、trade_status=TRADE_SUCCESS、type、sign（源码 `BuildNotifyParams`，https://github.com/v03413/bepusdt/blob/main/app/model/order.go ）。
  - **没有 `sign_type`，也没有实付 USDT 数量和交易哈希**：易支付格式的通知里，`money` 是下单时的法币金额。
  - 应答：响应体包含 `success` 或 `ok`（小写化后用包含判断），HTTP 状态码必须 200（app/task/notify/notify.go ）。
- 所以：**走易支付格式拿不到 actual_amount 和交易哈希**。要 1:1 入账，只能靠 `fiat=USD&rate=1` 使 `money` ≈ USDT 数量；实际到账与 money 可能差几个微单位，或在容差模式下略有出入。
- 结论：「一套易支付客户端同时接聚合支付（人民币）和 BEpusdt」**基本可行，限于 submit.php 跳转 + GET 通知 + MD5 签名**，但要注意：
  1. BEpusdt 没有 mapi.php 和 api.php 查单，所以「通知后调 api.php?act=order 复核」这一步对 BEpusdt **做不到**（我们前面建议的复核手段不适用）。要复核，只能用 BEpusdt 原生接口。BEpusdt 的原生 API 文档里我没看到查单接口，只有取消订单；**未找到查单接口**。
  2. 每个渠道的币种语义不同（聚合支付 money=人民币，BEpusdt 要自己带 `fiat=USD&rate=1`），客户端需要按渠道配置不同的附加参数，并且每个渠道记录各自的 pid/KEY。
  3. 通知里 money 的小数位格式各不相同，金额比对要用数值。

#### Epusdt / GMPay（https://github.com/GMWalletApp/epusdt/blob/master/wiki/API.md ）
- 路径：`GET/POST /payments/epay/v1/order/create-transaction/submit.php`，同样**只有 submit.php 这一个易支付入口**，没有 mapi.php。
- 参数：pid、money、out_trade_no、notify_url 必填；return_url、name 可选；`type` 仅允许空、`alipay` 或已启用的 `token.network`（如 `usdt.tron`）；可选 token、network、currency（法币，默认取后台 `epay.default_currency`，默认 cny）、sign、sign_type。
- 签名：MD5，排除 sign、sign_type 和空值，ASCII 升序，末尾接 secret_key，小写。
- 通知：GET，参数 pid、trade_no、out_trade_no、type、name、money（示例 `100.0000`）、trade_status=TRADE_SUCCESS、sign、sign_type=MD5。**同样没有实付 USDT 数量和交易哈希。**
- 另一种做法：用原生 GMPay 接口时回调才有 actual_amount 和哈希。

### 4. 易支付 Go 客户端

| 仓库 | 许可证 | 维护 | 评价 |
|---|---|---|---|
| AH-dark/epay-cli（https://pkg.go.dev/github.com/AH-dark/epay-cli/pkg/epay ） | **MIT** | 最近推送 2024-10-23，0 星，已停更 | 有 Submit、MApiSubmit、Verify，但 mapi 的 sign_type 写死 MD5，只含 alipay/wxpay 两种 type；无查单；成熟度低 |
| JeffreyZhu0201/rainbow-pay-sdk-go（https://github.com/JeffreyZhu0201/rainbow-pay-sdk-go ） | **无许可证**（GitHub 显示 null；没有许可证则默认不允许复用） | 最近推送 2025-08-12，5 星 | 不能直接用 |
| zengxiang1/epay-go | 无 | 2026-07 | 是一个完整的 Go+Gin+PG+Redis+Vue3 支付系统示例，不是客户端库 |

- 我另外搜 "epay golang sdk"、"彩虹易支付 go" 等，**没有发现成熟、活跃、许可证清晰的 Go 易支付客户端**。
- **建议自己写，不引入**：
  - 签名只有几十行（排序、拼接、MD5，上文第一节已核实规则）；
  - 现成库停更、星数极少、功能只覆盖 submit/mapi，不覆盖我们要的查单、金额核对、幂等入账；
  - 我们需要兼容 BEpusdt 的扩展参数（fiat、rate）和不同平台的小差异，自己写更可控。
  - 可以参考 AH-dark/epay-cli（MIT，可参考甚至拷贝，保留版权声明）和 BEpusdt 的 epay.go（GPL，只参考思路，见下）。

### 5. 许可证结论

> 这是技术性的理解，不是法律意见，正式使用请法务确认。

- **BEpusdt 和 Epusdt/GMPay 都是 GPL-3.0**（GitHub 元数据）。
  - **不要把它们的代码拷贝或作为库编译进我们的后端**：GPL-3.0 的传染性会要求我们整个程序按 GPL 开放源码（只要分发给他人就触发；只在自己服务器上运行不分发，一般不触发，但商业上风险和合规成本仍高，不建议）。
  - **可以作为独立服务部署**，我们通过 HTTP API（下单、回调）与它交互。这属于不同程序之间的网络通信，一般不被视为同一个作品，不会传染我们的代码。这是推荐路径。
  - 若 BEpusdt 的 GPL 二进制只部署在我们自己的服务器上、不分发给别人，我们自己运行没有额外义务；如果修改后对外提供网络服务，GPL-3.0（不是 AGPL）也不要求公开源码。如果要分发修改版或把它打包随我们产品发给客户，才要按 GPL 提供源码。
  - 自己重写时：只参考 API 文档和协议（接口、签名规则是协议，不是版权保护的表达），不要拷贝它们的源码。
- **MIT（如 AH-dark/epay-cli）**：可以直接引入、修改、闭源使用，保留版权和许可声明即可。但它质量不够，建议只参考。
- **无许可证的仓库（rainbow-pay-sdk-go）**：默认保留所有权利，不能复用。

### 6. 对「代理商充值」设计的建议

1. **人民币易支付**：自己写一个小的易支付客户端（MD5 + submit.php 或 mapi.php + 通知验签 + 查单复核）。
2. **USDT 两条路线，选其一**：
   - A. 自己实现 TronGrid 只读轮询（本报告第二部分）。优点：无外部服务、无 GPL 问题、完全可控；缺点：自己写匹配、尾数、幂等。
   - B. 部署 BEpusdt 作为独立服务（独占地址模式 `amount=0` 适合充值），用**原生 API**下单，回调拿 `actual_amount` + `block_transaction_id` 按实收入账。优点：多链、已有收银台和扫块、活跃维护；缺点：多一个要运维的服务和它自己的数据库/私钥管理——**BEpusdt 的收款地址需要你自己提供，它只监听地址，不碰私钥**（这一点我在 README 和文档里没有直接看到明确声明，属推断，**未核实**）；GPL 隔离部署；它的 TronGrid API Key 也要自己配（rpc-endpoint.md 建议配置）。
   - 若走 B，**不建议用它的易支付兼容接口**：通知里没有实付 USDT 数量和交易哈希，也没有查单接口，复核做不了；用原生 API 更合适。
3. 不管选哪条，**入账都以「实收 USDT 数量」为准，1:1 记美元**；USDT 价格偏离 1 美元的风险（脱锚）是业务风险，需要产品层面决定是否接受。

### 本节未找到 / 未核实
1. BEpusdt 的原生查单接口（文档只有创建、取消、收银台订单、币种列表）。
2. BEpusdt `IsAllowedCallbackURL` 的具体规则（是否禁内网）。
3. BEpusdt 是否完全不接触私钥的明确声明。
4. Epusdt/GMPay 的汇率来源与固定/自动策略。
5. BEpusdt 原生回调应答究竟是 `ok` 还是 `success` 才算成功（两份文档不一致；源码里原生回调只检查 200）。
6. 其他主流开源 USDT 网关（我只在 GitHub 搜索里没看到更主流的，未穷举）。
7. 以上许可证判断为技术理解，非法律意见。
