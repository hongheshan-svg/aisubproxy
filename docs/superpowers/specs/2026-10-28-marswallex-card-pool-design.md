# MarsWallex 卡池与资金设计（子项目 2）

- 日期：2026-10-28
- 状态：已确认设计，待写实现计划
- 前置：子项目 1 `docs/superpowers/specs/2026-10-27-marswallex-access-layer-design.md`（已合并到 master）
- 参考：`docs/marswallex-openapi-zh.md`

## 1. 背景与目标

替代 ZovoCard 的 5 个子项目中的第 2 个（拆分见子项目 1 设计 §1）。子项目 1 已提供 MarsWallex 客户端（结果分为成功 / 被拒 / 未知 / 未发出）、Webhook 验签入库和后台配置。

**本子项目目标**：系统自己持有一个 MarsWallex 卡池，为子项目 4（兑换引擎）提供「选卡 → 注资 → 取卡号与 3DS 验证码 → 回收」的能力。任何资金操作在结果不明时都能自动收敛或交给人工，不会重复扣款，也不会把钱留在卡上。

## 2. 已确认的决定

| 事项 | 决定 |
|---|---|
| 卡的用法 | 预先开好卡池；**每张卡最多使用 N 次**（策略可配置，默认 1，即一卡一用），用满退役。平时卡里余额为 0，按单转入本单金额，用后转出。ZovoCard 是卡复用、余额留卡；这里默认一卡一用，问清开卡费、销卡费后可调大 N 以分摊成本 |
| 续费 | 代充后订阅不自动续费，不考虑续费扣款 |
| 持卡人 | 在 MarsWallex 网页后台建立和审核，我们只按 `card_holder_id` 导入并跟踪状态 |
| 开卡 | 后台可以开卡（手动批量 + 自动补卡），也可以按 `card_id` 导入网页后台开的卡 |
| 补卡 | 自动补到每个卡产品的目标可用数，受每日开卡上限约束，到达上限后告警 |
| 实现方案 | 独立 `internal/cardpool` 包；写操作先落本地单据；结果不明的单据与 Webhook 事件由后台协程收敛 |
| 资金来源 | 只用主钱包（`MAIN`）；手续费按默认方式（`WALLET`）。数币充值与兑换仍在 MarsWallex 网页后台操作 |
| 凭证 | 只有生产凭证（同子项目 1）：自动化测试全部用模拟服务器，生产验收逐步确认、金额最小 |

## 3. 整体结构

| 单元 | 职责 | 依赖 |
|---|---|---|
| `internal/cardpool`（新） | 持卡人导入与状态同步；开卡、导入卡；资金单与改状态单；单据定论与对账；Webhook 事件处理；补卡；选卡与租约；健康度 | `marswallex`、`db`、`secretbox`、`notify` |
| `internal/marswallex/mwtest`（新） | 从子项目 1 的 `mock_test.go` 抽出的可编排模拟服务器与 Webhook 签名工具，只供测试引用 | `marswallex` |
| `internal/db` 新增 | §4 的表、迁移与读写函数 | — |
| `internal/handler` + 路由 | §9 的后台接口；子项目 1 的 Webhook 接收接口入库后通知处理协程 | `cardpool` |
| 前端 `frontend/src/views/admin/CardPool.vue`（新） | `/ops` 下「卡池」页 | 后台接口 |
| `server/http.go` | 启动时调用 `cardpool.Start(ctx)`（写法同 `plansync.Start`） | — |

告警统一走 `notify.SendText`（Telegram）。

### 3.1 提供给子项目 4 的接口

本子项目实现并测试，暂无调用方。

| 函数 | 说明 |
|---|---|
| `Acquire(ctx, AcquireRequest{PlanKey, OrderRef, AccountEmail, ExcludeCardIDs}) (*Lease, error)` | 选一张卡并加租约（§7.1）；`ExcludeCardIDs` 供同一订单换卡重试时排除已试过的卡；无卡返回 `ErrNoCard`（附原因） |
| `Fund(ctx, lease, amountCents) (*Order, error)` | 提交转入单，提交后即返回；币种取卡币种，金额必须大于 0 |
| `WaitOrder(ctx, outOrderNo) (*Order, error)` | 阻塞到单据终态或 ctx 超时 |
| `Sensitive(ctx, lease) (*marswallex.CardSensitive, error)` | 实时查询完整卡号、CVV2、有效期，不落库；调用后本次租约记为已取卡号 |
| `WaitOTP(ctx, cardID, since) (string, error)` | 取该卡在 `since` 之后收到的最新未过期 3DS 验证码，取用后清除密文 |
| `Release(ctx, lease, Outcome{Result, ErrorCode})` | 释放租约并上报结果；`Result` 为 `success` / `failed` / `aborted`（未付款即放弃，不计入健康度）；随后按 §6.4 回收 |

### 3.2 不在本子项目范围

兑换流程本身（子项目 4）；子账户、兑换、转账的业务功能；资金对账报表（子项目 5）；删除 ZovoCard 代码；主密钥轮换。

## 4. 数据模型

金额一律用 int64 存「分」。时间存 UTC，按日统计时用北京时间（`Asia/Shanghai`）。

| 表 | 用途 | 主要字段 |
|---|---|---|
| `mw_cardholders` | 已导入的持卡人 | `card_holder_id`（主键）、英文姓名、国家、平台状态、驳回原因 `remark`、最后同步时间、管理员备注 |
| `mw_cards` | 卡池，卡的唯一记录来源（平台没有卡列表接口） | `card_id`（主键）、`card_holder_id`、`product_id`、卡组、国家、币种、后四位、有效期 `MMyy`、平台状态、可用余额、冻结余额、余额同步时间、来源（`opened` / `imported`）、启用开关、`pool_state`、租约（令牌、持有方 `OrderRef`、到期时间、`lease_sensitive` 本次租约是否取过卡号）、`use_count`（已用次数）、`last_sensitive_at`、`frozen_at`、`cancel_after`、停用原因、入池时间 |
| `mw_card_orders` | 所有写操作的本地单据 | `out_order_no`（唯一）、`kind`（`apply` / `recharge` / `withdraw` / `status`）、`card_id`（开卡单成功前为空）、`card_holder_id`、`product_id`、金额、币种、目标状态、`state`、平台订单号、`ever_unknown`、尝试次数、`next_check_at`、`first_sent_at`、最后错误、发起方（管理员用户名或租约令牌）、备注、创建与定论时间 |
| `mw_card_transactions` | 授权与结算交易 | `transaction_id`（主键，重复推送覆盖更新）、`kind`（`AUTH` / `AUTH_REVERSAL` / `SETTLE` / `REFUND`）、`card_id`、状态、拒绝原因、交易金额与币种、账单金额与币种、商户名、关联交易号、发生时间 |
| `mw_3ds_otps` | 3DS 验证码 | `card_id`、`otp_sealed`（secretbox 加密，AAD 为 `mw_3ds_otp`）、商户名、金额、币种、收到时间、取用时间 |
| `mw_card_outcomes` | 健康度依据 | `card_id`、`product_id`、`plan_key`、`order_ref`、规范化邮箱（小写去空格，空记为 `unknown`）、结果、错误码、时间 |
| `mw_product_health` | 「卡产品 × 套餐」停用状态 | `product_id`、`plan_key`、停用时间、原因、恢复时间、恢复人 |

`marswallex_webhook_events`（子项目 1）用 `ALTER TABLE ADD COLUMN` 增加 `process_attempts`、`next_attempt_at`。

### 4.1 卡池状态

| `pool_state` | 含义 |
|---|---|
| `available` | 可被选中 |
| `leased` | 租用中（子项目 4 的订单，或管理员手动租用） |
| `releasing` | 已释放、还没用满，正在清零，清零后回到 `available` |
| `used` | 已用满次数，等待清零、冻结、注销 |
| `retired` | 已注销 |
| `disabled` | 停用（管理员手动，或平台风控、异常交易触发），不参与选卡 |

流转：`available` →（租用）→ `leased` →（释放或租约到期）→ 本次租约取过卡号则 `use_count + 1`；`use_count` 达到使用次数上限则 `used`，否则 `releasing` → 清零后回 `available`。`used` → 清零、冻结 → 到 `cancel_after` 后注销 → `retired`。管理员可把任何未注销的卡设为 `disabled`，或从 `disabled` 恢复为 `available`（仅当余额为 0、`use_count` 未达上限）。开卡单提交后、平台给出 `card_id` 之前，卡只存在于单据里，不在 `mw_cards`。

### 4.2 策略配置 `mw_cardpool_policy`（`site_settings` 中的 JSON）

| 项 | 默认 |
|---|---|
| 每个套餐的卡产品优先级（含「默认」列表） | 空（未配置时无卡可选） |
| 每个卡产品的目标可用数、每日开卡上限 | 0、0（即不自动补卡） |
| 补卡用的持卡人（可多选，只轮流用 `APPROVED` 的） | 空 |
| 开卡金额 | 0 |
| 卡有效期 | 开卡后 24 个月 |
| 卡名前缀（卡名 = 前缀 + 序号，总长不超过 16） | `POOL` |
| 每张卡最多使用次数（≥ 1） | 1 |
| 租约时长 | 30 分钟 |
| 用后多少天注销（可设为 0） | 7 天 |
| 健康度：时间窗口、产品失败阈值、单卡失败阈值、是否自动停用 | 24 小时、3、2、是 |

## 5. 敏感数据

- 完整卡号与 CVV2 不落库、不打日志、不出现在除 §9「查看卡号」以外的任何接口；后四位从卡详情取。
- 3DS 验证码加密存储；取用后清除密文；收到 30 分钟后无论是否取用也清除。
- 处理 `CARD_3DS_OTP` 事件后，把 `marswallex_webhook_events.payload` 中的验证码改为 `***`。
- 主密钥缺失时验证码无法加密：事件记 `process_error`，原文中的验证码照样抹掉。

## 6. 写操作与对账

### 6.1 单号

`out_order_no` = 两位类型前缀（`MA` 开卡、`MI` 转入、`MO` 转出、`MS` 改状态）+ `yyMMddHHmmss`（UTC）+ 8 位十六进制随机数，共 22 位，满足平台 32 位上限。改状态单的单号只在本地使用。

### 6.2 发送与初判

1. 在一个数据库事务里写入单据，`state = pending`。
2. 调用接口。
3. 按子项目 1 的结果分类更新：

| 结果 | 开卡单 | 转入 / 转出单 | 改状态单 |
|---|---|---|---|
| 成功 | `processing`，记平台订单号 | 按响应状态：`APPLY` → `processing`；`SUCCESS` → `success`；`FAIL` → `failed` | `success` |
| 被拒 | `ever_unknown` 为假 → `failed`；为真 → `unknown` | 同左 | 同左 |
| 未知 | `unknown`，`ever_unknown = 1` | 同左 | 同左 |
| 未发出 | `not_sent`（终态） | 同左 | 同左 |

同一张卡同一时间最多一笔非终态的转入或转出单，否则直接拒绝新单。卡上有 `needs_review` 单据时，不能再发起转入或转出。

### 6.3 定论与对账协程

- Webhook（`CARD_APPLY_RESULT`、`CARD_TRANSFER_RESULT`）和对账协程的查单，调用同一个定论函数。终态不会被覆盖；两边终态冲突时保留先到的结果，并发告警。
- 对账协程每 5 秒一轮，处理 `next_check_at` 已到的 `unknown` 和 `processing` 单据。退避间隔：10 秒、30 秒、1 分钟、2 分钟、5 分钟，之后每 5 分钟一次。
- 查单接口：开卡单查 `GET /v1/card/apply-order`，转入、转出单查 `GET /v1/card/transfer-order`，改状态单查卡详情。
- `unknown` 的单据查单查不到、或查单本身出错时继续查；到 `first_sent_at` 之后满 30 分钟仍无定论，改为 `needs_review`，并发告警。
- `processing` 满 24 小时发告警，之后继续查单。
- **资金单（开卡、转入、转出）任何情况下都不自动重发**：规范没写卡转入、卡转出的单号是否防重，重发可能重复扣款。`needs_review` 由人工到 MarsWallex 后台核对后，在我们后台选择「确认失败」（必须填备注）或「再查一次」。
- 改状态单在结果未知时查卡详情：已经是目标状态就记为成功，否则用同一张单据重发（设置状态可以重复执行）。注销不传 `account_no`，余额回到默认主钱包。

### 6.4 租约与回收

- 租用（`Acquire`，或管理员在后台租用）用条件更新语句抢卡：只有 `pool_state = 'available'` 的卡才能改为 `leased`，并写入租约令牌、持有方和到期时间。
- 取卡号（`Sensitive`，或管理员查看卡号）写入 `lease_sensitive = 1` 和 `last_sensitive_at`。
- 释放（`Release`、管理员释放、租约到期由对账协程代为执行）只改状态和计数：`lease_sensitive` 为真则 `use_count + 1`；`use_count` 达到上限转为 `used`，否则转为 `releasing`；清空租约字段。
- 对账协程每轮推进 `releasing` 和 `used` 的卡，每一步都是 §6.2 的单据：
  - **`releasing`**：刷新余额 → 有可用余额就转出 → 可用余额和冻结余额都为 0 后回到 `available`。有冻结余额就 10 分钟后再查。
  - **`used`**：刷新余额 → 有可用余额就转出 → 冻结（`REVOKED`），写入 `frozen_at` 和 `cancel_after = frozen_at + 用后注销天数` → 到 `cancel_after` 后注销（`CANCELED`）→ `retired`。冻结余额不用等，注销时平台会把余额退回主钱包。
- 某一步的单据失败或进入 `needs_review` 时，卡停在当前状态，不会回到卡池，同时发告警；单据定论后（含人工确认失败），从这一步重新推进。

### 6.5 补卡

- 对账协程每 60 秒检查一次。对每个配置了目标数的卡产品：缺口 = 目标可用数 −（`available` 与 `releasing` 卡数 + 进行中的开卡单数）。进行中的开卡单指状态为 `pending`、`processing`、`unknown`、`needs_review` 的开卡单。
- 缺口大于 0、且今天（北京时间）的开卡单数小于每日上限时，按缺口逐张提交开卡单，持卡人轮流使用。失败的开卡单也计入当天的数量。
- 到达每日上限时，每天只发一次告警。没有 `APPROVED` 的持卡人时，跳过补卡并告警。
- 开卡请求：`amount` 取策略里的开卡金额，`currency` 取卡产品币种，`expiry_date` 取开卡月份往后推有效期月数的 `MMyy`，`fee_deduct_type` 不传。
- 开卡金额大于 0 时，开卡成功后先清零，再置为 `available`。

## 7. 选卡与健康度

### 7.1 选卡

1. 取 `PlanKey` 对应的卡产品优先级；未配置就用「默认」列表。`PlanKey` 先沿用现有的套餐键，子项目 3 建立套餐注册表后再对接。
2. 按优先级逐个看卡产品，跳过 `mw_product_health` 中处于停用状态的「产品 × 套餐」。在一个产品里选卡，条件是：`available`、平台状态 `ACTIVE`、启用、不在 `ExcludeCardIDs` 中、有效期还剩一个月以上、持卡人状态不是 `DISABLED` 或 `SUSPENDED`（导入时未指定持卡人的卡不检查）。排序：先用 `use_count` 大的（尽快用满退役，减少同时在外的卡号），再按入池时间先后。
3. 按 §6.4 抢卡，抢到就返回租约，抢不到换下一张。
4. 全部产品都没卡时，返回 `ErrNoCard`，原因为「无可用卡」或「全部产品已停用」；同时立即触发一次补卡检查，并发告警。

### 7.2 健康度

- 数据来源只有 `Release` 上报的 `success` 和 `failed`。授权被拒的交易在后台展示供排查，不参与判断。
- 判断沿用现有 `EvaluateCardFailVerdict` 的规则：对每个「产品 × 套餐」，统计时间窗口内、最近一次成功之后的失败：
  - 失败次数达到阈值，且涉及 2 个以上不同的已知邮箱：判定为产品问题。开启了自动停用就写入 `mw_product_health`，并发告警。
  - 只涉及 1 个邮箱，或全部是 `unknown`：不处理。
- **单卡**（只在使用次数上限大于 1 时有意义）：同一规则作用于这张卡在窗口内、最近一次成功之后的失败，阈值用「单卡失败阈值」。判定为卡的问题时，卡转为 `disabled`（原因「健康度」）并告警；不在租用中的卡由对账协程按 `used` 的流程清零、冻结、注销。
- 管理员可以手动恢复产品，恢复后从恢复时间重新计数。

## 8. Webhook 事件处理

- 子项目 1 的接收接口入库后，不阻塞地通知处理协程立即处理。处理协程同时每 10 秒扫描一次未处理的事件作为兜底。
- 按 `id` 顺序逐条处理，每条一个数据库事务。成功写 `processed_at`；失败写 `process_error`，按 10 秒、30 秒、1 分钟、2 分钟、5 分钟退避重试。连续 10 次失败后停止重试并发告警，后台可「重新处理」。
- 所有处理都可以重复执行。

| topic | 处理 |
|---|---|
| `CARD_APPLY_RESULT` | 定论开卡单；成功则查卡详情补全信息并入池（§6.5 最后一条） |
| `CARD_TRANSFER_RESULT` | 定论转入或转出单，刷新卡余额；没有对应本地单据的，只刷新余额 |
| `CARD_STATUS_CHANGED` | 更新平台状态。不是我们发起的冻结或注销：`available` 的卡转为 `disabled`；`leased` 的卡告警；`CANCELED` 记为 `retired` |
| `CARDHOLDER_STATUS_CHANGE` | 只更新已导入的持卡人；不再是 `APPROVED` 时告警 |
| `AUTH_EVENT`、`SETTLE_EVENT` | 写入交易表，`pending` 不当作终态；卡不在卡池里也记录。**`available` 且 `use_count = 0` 的卡出现授权（卡号从未交出过）**：卡转为 `disabled` 并告警。`AUTH_EVENT` 原文里的 `otp` 字段同样改为 `***` |
| `CARD_3DS_OTP` | 按 §5 加密入库并抹掉原文里的验证码 |
| `ACCOUNT_STATUS_CHANGED`、`CONVERSION_RESULT`、`TRANSFER_RESULT` | 标记已处理 |
| 未知 topic、子项目 1 存档的无法解析事件 | 标记已处理，备注原因 |

## 9. 后台接口与页面

接口都在 `/api/v1/admin/cardpool/` 下，沿用现有的管理员登录。所有写操作记入 `admin_audit_logs`。

| 分组 | 接口 |
|---|---|
| 概览 | `GET /overview`：主钱包余额；每个卡产品的 `available` / `leased` / `used` 数量与目标数；今日开卡数与上限；待处理事项（`needs_review` 单据、处理失败的 Webhook、停用的产品） |
| 持卡人 | `GET /cardholders`；`POST /cardholders/import`；`POST /cardholders/:id/refresh` |
| 卡产品 | `GET /products`（实时查询平台） |
| 卡 | `GET /cards`（按状态、产品筛选，分页）；`GET /cards/:id`（详情 + 单据 + 交易）；`POST /cards/apply`（手动批量开卡，一次最多 20 张，计入每日上限）；`POST /cards/import`（填 `card_id`、`product_id`，持卡人可选：平台卡详情不返回这两项；卡详情的卡组、国家、币种、模式须与所选产品一致）；`POST /cards/:id/refresh`；`POST /cards/:id/{disable,enable,freeze,unfreeze,cancel,sweep}` |
| 手动用卡 | `POST /cards/:id/lease`（只能租 `available` 的卡，持有方记为 `admin:<用户名>`，时长同策略）；`POST /cards/:id/fund`（给自己租用中的卡转入，填金额）；`POST /cards/:id/release`（释放自己租用中的卡，之后按 §6.4 回收） |
| 查看卡号 | `POST /cards/:id/sensitive`：返回完整卡号、CVV2、有效期。`leased` 的卡记为本次租约已取卡号；其他状态（`used`、`disabled` 等，用于排查）只查看、不改状态；`available` 的卡要先租用才能查看 |
| 查看验证码 | `POST /cards/:id/otp`：返回该卡最近一条未过期的 3DS 验证码 |
| 单据 | `GET /orders`；`POST /orders/:no/recheck`；`POST /orders/:no/confirm-failed`（只有 `needs_review` 的单据能用，必须填备注） |
| 交易 | `GET /transactions`（可按卡筛选） |
| 策略与健康度 | `GET`/`PUT /policy`；`GET /health`（各「产品 × 套餐」最近 24 小时与 7 天的成功、失败数和停用状态）；`POST /health/resume` |
| Webhook | 子项目 1 的列表接口增加处理状态和尝试次数；新增 `POST /api/v1/admin/marswallex/webhook-events/:id/reprocess` |

前端：

- `/ops/cardpool`「卡池」页，6 个标签：概览、卡、持卡人、单据、交易、策略与健康度。
- 「MarsWallex 对接」页的最近 Webhook 列表增加处理状态和「重新处理」按钮。
- 注销、确认失败、查看卡号、查看验证码都要弹窗确认。

## 10. 待联调确认项

1. 开卡金额 `0` 是否接受。
2. 卡转入、卡转出的 `out_order_no` 是否防重（建议直接问 MarsWallex，不靠实测）。
3. `failedFee`（失败交易费）的收费规则。
4. 卡冻结（`REVOKED`）后，结算和退款能否入账。
5. 卡详情里的 `card_no` 是否脱敏（无论是否脱敏，我们都只存后四位）。
6. 开卡费、销卡费（`discardFee` 是否即销卡费）、卡月费：决定使用次数上限 N 设多少。
7. 卡产品的最低开卡金额、最低转入金额（ZovoCard 的产品有 `min_amount`）。

## 11. 测试与验收

### 11.1 自动化测试（全部离线，时间相关逻辑用可注入的时钟）

| 对象 | 覆盖 |
|---|---|
| `mwtest` | 可按接口编排成功、被拒、未知、未发出、延迟、查不到；可生成带签名的 Webhook |
| 单据 | 四种结果落到正确状态；`ever_unknown` 后收到被拒仍为 `unknown`；资金单从不自动重发；改状态单状态不一致时重发；退避节奏；查不到满 30 分钟变 `needs_review`；`processing` 满 24 小时告警；Webhook 与查单冲突保留先到结果并告警 |
| 并发 | 多个协程同时 `Acquire` 不会拿到同一张卡；同一张卡不会同时有两笔资金单 |
| 租约与回收 | 释放和到期按本次是否取过卡号累加 `use_count`，再按是否用满进入 `used` 或 `releasing`；上限为 1 和 3 时各走一遍；`releasing` 有冻结余额时稍后再查；`used` 卡按清零 → 冻结 → 到期注销走完；某一步失败时卡不回池，定论后从该步继续；管理员只能注资、释放自己租用的卡；查看 `available` 的卡号被拒绝 |
| 补卡 | 进行中的开卡单计入可用数；每日上限按北京时间切日；持卡人轮流且只用 `APPROVED`；到达上限每天只告警一次 |
| Webhook 处理 | 每个 topic 的处理；重复处理结果一致；验证码已加密且原文被抹掉；验证码取用后、满 30 分钟后清除；失败退避重试，10 次后停止；`available` 卡出现授权时停用 |
| 选卡与健康度 | 优先级；跳过停用的产品；`ExcludeCardIDs`；同产品先用 `use_count` 大的、再先入池先出；有效期不足、持卡人异常的卡不选；产品与单卡的各种判定结果；时间窗口；成功后重新计数；手动恢复；`use_count > 0` 的可用卡出现授权只记录不停用 |
| 后台接口 | 写操作有审计日志；查看卡号后卡变为 `used`；只有 `needs_review` 能确认失败；卡号与验证码不出现在其他接口的响应里 |

### 11.2 验收标准

1. `go test ./...` 通过（已知原有失败的 `TestIssueProductKeepsGPTDefault`、`TestXIssueInfersProductWithoutQueryParameter` 除外）。
2. `go vet ./...` 与前端构建通过。
3. 生产验收：动钱，金额尽量小，每一步用户确认后再做。同时完成原定的「真卡可行性验证」。
   1. 导入 1 个持卡人（只读）。
   2. 手动开 1 张卡，金额 0（确认 §10 第 1 项）。
   3. 在后台租用这张卡，转入 1 美元后不取卡号直接释放：验证转入、转出的 Webhook 和余额同步，卡清零后回到 `available`。
   4. 再次租用，转入一笔订阅的金额，查看卡号，手动付一笔真实订阅，在页面上取 3DS 验证码。验证授权、结算、3DS 三类 Webhook 都已落库。
   5. 释放这张卡：它会依次清零、冻结，到期后注销。验收时可以把「用后注销天数」临时设为 0，确认余额回到主钱包。
   6. 把一个产品的目标数设为 1、每日上限设为 1，确认系统只自动开 1 张卡就停止。
