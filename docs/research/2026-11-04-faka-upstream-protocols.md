# 发卡网对接上游货源调研（2026-11-04，子代理只读源码；由控制者整理落盘）

> 2026-11-05 更正：异次元部分已按 acg-faka 3.8.5 源码重新核实，细节（错误必须 HTTP 200、trade 失败的回滚与重试、价格与币种、商品字段取值）以 `2026-11-05-acg-shared-protocol.md` 为准。

源码摘录在本目录 src/acg、src/duj、src/dn，文件树为 acg-tree.txt、duj-edaoren-tree.txt、dn-tree.txt。这些都是不可信数据，只读。

## 结论
| 系统 | 原生能否对接上游 | 方式 | 我们怎么兼容 |
|---|---|---|---|
| 异次元 acg-faka（lizhipay/acg-faka，活跃，最新 3.8.5，2026-10-28） | 能，叫「店铺共享」(Shared) | 下游后台「店铺共享 → 新增店铺」，填上游地址、商户 ID（app_id）、商户密钥（app_key） | 实现 `/shared/*` 协议（type 0），代理商不用装任何东西 |
| 独角数卡 dujiaoka（assimon，2026-02-12 宣布停更，仓库源码已清空，最后版本 2.0.6） | 不能，也没有插件机制 | 只有商品级 `api_hook`：付款后单向 POST JSON `{title, order_sn, email, actual_price, order_info, good_id, gd_name}`，不签名，不读返回 | 没有协议可兼容；引导代理商改用异次元或 Dujiao-Next |
| Dujiao-Next（dujiao-next/dujiao-next，Go，GPL-3.0，v1.5.2，2026-11-02） | 能，叫「站点连接 / 采购单」 | `/api/v1/upstream/*`，HMAC-SHA256 头部签名，支持回调 | 可以第二优先实现 |

## 异次元 /shared/* 协议
- 地址 `{上游}/shared/{controller}/{method}`。全部 POST，`application/x-www-form-urlencoded`；超时 30 秒，不跟随重定向。出处 app/Service/Bind/Shared.php:72-92。
- 认证：每个请求带 `app_id`（上游会员 ID）和 `sign`。官方客户端还会把 `app_key` 作为表单字段一起发，而且参与签名。服务端对收到的全部参数重算签名，用 hash_equals 比较。出处 SharedValidation.php:31-46、Bind/Shared.php:74-75。
- 签名（app/Util/Str.php:130-140）：
  1. 去掉 sign；
  2. 按键名 ksort 升序；
  3. 删掉值为空字符串的参数；
  4. `urldecode(http_build_query($data) . "&key=" . $appKey)`；
  5. 整串取 md5，32 位小写。
  - 数组参数（如 sku）按 http_build_query 的规则展开。
  - 没有时间戳，也不防重放。
- 响应：成功 `{"code":200,"msg":"success","data":{...}}`，失败一般是 `{"code":0,"msg":"..."}`。客户端只看 code==200，不看 HTTP 状态码。可选接口如果回 404/405，下游会降级到老协议。
- 接口：

  | 接口 | 作用 | 主要参数 |
  |---|---|---|
  | `authentication/connect` | 返回 shopName、balance | 无 |
  | `commodity/items` | 分类树，只含 api_status=1 且上架的商品 | 无 |
  | `commodity/item` | 单个商品 | 3.1.2 起用 `code`；3.1.1 及以前用 `sharedCode`，返回分类树 |
  | `commodity/inventory` | 库存、价格、config（INI 文本）、拿货价 | `sharedCode`、`race` |
  | `commodity/inventoryState` | 下单前校验数量 | `shared_code`、`card_id`、`num`、`race` |
  | `commodity/trade` | 下单取卡，强制余额支付 | `shared_code`、`contact`、`num`、`card_id`、`device`、`password`、`race`、`request_no`、`sku`，加上 widget 自定义字段 |
  | `commodity/stock` | 实时库存（可选，3.1.2+） | `code`、`race`、`sku` |
  | `commodity/valuation` | 报价（可选） | `code`、`num`、`race`、`sku`、`card_id` |
  | `commodity/draftCard`、`commodity/draft` | 预选卡密（可选） | |
  | `commodity/query` | 按订单号查单 | 字段名是 `tradeNo`；官方客户端不调用 |

  - 最小集：connect、items、item、inventory、inventoryState、trade。建议再做 stock、valuation、query。
- trade 返回 data：`url, amount, tradeNo, secret, leave_message`，下游取 `secret` 作为发货内容。出处 Bind/Order.php:1020、Bind/Shared.php:441。
- 幂等：下游把自己的 trade_no 当 `request_no` 传。异次元原版遇到重复的 request_no 会报错（code=0）。我们实现时，重复请求应回 code=200，并带上第一次的 tradeNo 和 secret。
- 字段白名单（SharedPayload.php:49-60）：
  - 商品：`id, category_id, name, description, cover, price, user_price, status, code, sort, delivery_way, contact_type, password_status, coupon, seckill_*, draft_status, draft_premium, inventory_hidden, only_user, purchase_count, widget, minimum, maximum, config, stock, tags`。
  - 分类：`id, name, sort, icon, status, pid`。
- 坑：
  - config 在 item 里是对象，在 inventory 里是 INI 文本；
  - 商品 code 必须稳定；
  - 余额不足要明确报错；
  - 3.8.2 及以上的下游开了二次验证时，上游要有 IP 白名单（我们可以不做）。
- 文档：https://faka.wiki/zh-cn/guide/shared.html 、https://faka.wiki/zh-cn/dev/api.html 。插件文档 https://faka.wiki/zh-cn/dev/plugin.html （不建议走插件）。

## Dujiao-Next /api/v1/upstream/*
- 接口：
  - `POST /ping`：返回 site_name、protocol_version "1.0"、balance、currency、member_level；
  - `GET /categories`；
  - `GET /products`：page、page_size，可带 updated_after、include_inactive；
  - `GET /products/:id`；
  - `POST /orders`：JSON 含 sku_id、quantity、manual_form_data、downstream_order_no、trace_id、callback_url；
  - `GET /orders/:id`：带 fulfillment{type,status,payload,delivery_data,delivered_at}；
  - `POST /orders/:id/cancel`。
- 请求头：`Dujiao-Next-Api-Key`、`Dujiao-Next-Timestamp`（Unix 秒，偏差不超过 60 秒）、`Dujiao-Next-Signature`。
  - 签名 = `hex(HMAC-SHA256(api_secret, METHOD + "\n" + path(不含 query) + "\n" + timestamp + "\n" + md5hex(body)))`。
- 响应：
  - 成功：200 加 `{"ok":true,...}`。
  - 业务失败：200 加 ok:false，带 error_code、error_message，比如 payment_failed。
  - 鉴权失败：401 或 403，带 error_code。
- 下单返回 ok、order_id、order_no、status、amount、currency。
- 回调：POST 到下游的 `/api/v1/upstream/callback`，签名方式相同。body 含 event、order_id、order_no、downstream_order_no、status、fulfillment、timestamp。
- 幂等：同一凭证下，相同的 downstream_order_no 直接返回已有订单。
- 出处：internal/modules/upstreamapi/transport/http/routes.go、internal/upstream/signer.go、middleware/upstream_auth.go、upstream_order.go、upstream_callback.go。
