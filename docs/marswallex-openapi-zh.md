# MarsWallex 卡台 API 文档

> **本项目默认卡台 API 文档。** 内容整理自 MarsWallex 官方 API 文档 <https://marswallex-official.feibeiglobal.com/en/resources/api>，规范版本 `1.0.0`（OpenAPI 3.0.1），抓取日期 2026-10-27。官方原始规范存档在同目录 `marswallex-openapi-zh.json` / `marswallex-openapi-en.json`，以官方最新文档为准。

## 速览

| 项目 | 内容 |
|---|---|
| 生产 Base URL | `https://api.feibeiglobal.com/portal/api`（规范 `servers` 只写了 `https://api.feibeiglobal.com`，但签名示例与官网前端都带 `/portal/api` 前缀，见下文「接入注意」） |
| 测试 Base URL | `https://api.feibeiglobal-test.com/portal/api`（官网前端代码中出现，规范未列出，需向 MarsWallex 确认） |
| 数据格式 | 请求与响应均为 JSON（文件上传为 `multipart/form-data`） |
| 认证 | `Authorization: Basic BASE64(developerId:apiToken)` |
| 签名 | 每个请求带 `F-Signature: t={epoch},v={Base64(SHA256withRSA(payload))}`，响应与 Webhook 同样带 `F-Signature`，用 MarsWallex 公钥验签 |
| 接口数量 | 28 个 API + 10 类 Webhook |

## 接入注意（整理时发现的问题，联调前需向 MarsWallex 确认）

1. **Base URL 前缀**：规范里的路径是 `/v1/...`，`servers` 是 `https://api.feibeiglobal.com`；但签名示例里的 URI 是 `/portal/api/v1/accounts`，官网前端代码里出现的地址也是 `https://api.feibeiglobal.com/portal/api`。实际请求和签名都应使用带 `/portal/api` 前缀的完整路径。
2. **响应签名时间戳单位**：请求签名的 `t` 是秒（示例 `1766123535`），响应签名示例里的 `t` 却是 13 位毫秒（`1766123822742`）。验签时按 `F-Signature` 里实际给出的 `t` 原样拼接即可，但做时间窗口校验时要兼容秒和毫秒。
3. **Webhook 字段命名不一致**：概述里写请求体是 `{topic, messageId, data}` 且 `data` 是 JSON 字符串；各 Webhook 的具体定义里却是 `message_id`，且 `data` 是对象。解析时两种都要兼容（`messageId`/`message_id`；`data` 为字符串时再按 JSON 解析一次）。
4. **非 200 状态不代表失败**：`201`/`202`/`409`/`500` 都表示「结果未知或处理中」，必须通过查询接口或 Webhook 确认最终结果，不能直接判失败后重试下单（会重复扣款/开卡）。
5. **中英文规范说明详略不同**：两版的接口和字段完全一致，但中文版有 15 种字段缺说明（多在卡交易、分页参数），本文已按英文版译成中文补齐。
6. **时间字段单位不统一**：多数 `*_time` 字段标注为 Unix 秒，但转账详情 `TransferDetail` 的 `create_time` / `success_time` 标注为 Unix 毫秒；官方响应签名示例里账户的 `create_time` 又是 ISO 字符串（`2025-12-15T10:55:03.000+00:00`）。解析时按位数区分秒和毫秒，并兼容字符串格式。

7. **创建账户的请求体多包了一层 `data`**：`POST /v1/accounts` 的请求体是 `{"data":{"account_name":"...","out_account_no":"..."}}`，中英文规范都是这样，和其他接口的写法不同，联调时确认。
8. **`PUT /v1/card/status` 的 `account_no` 标成了 integer**：其他接口的账户号都是 string，而账户号是 19 位数字（如 `2000519969952288769`），超出 JavaScript 安全整数范围。建议按字符串传，并向 MarsWallex 确认。
9. **各接口声明的状态码不统一**：申卡、创建账户、账户状态变更列了完整的 200/201/202/400/401/403/404/409/500；卡转入转出、兑换只列了 200/201/400；转账只列了 200/400/404；查询类接口大多只列了 200/400。没列出的状态码不代表不会返回。资金类请求（申卡、卡转入转出、兑换、转账）只有 200 能直接作为结果，其他状态码以及超时、断连，一律先查单确认，再决定是否重试；重试时沿用同一个 `out_order_no`。
10. **交易状态里的 `pending`**：授权交易、结算交易的 `status` 说明写着「终态才会通知」，但枚举里有 `pending`（处理中）。收到 `pending` 不要当作终态。
11. **Webhook topic 命名不统一**：持卡人是 `CARDHOLDER_STATUS_CHANGE`（没有 D），卡和账户是 `CARD_STATUS_CHANGED`、`ACCOUNT_STATUS_CHANGED`。按下文「Webhook 事件一览」逐字匹配。
12. **分页总数是字符串**：分页接口返回的 `count` 是 string；转账接口的 `count` 固定为 null。
13. **3DS OTP 通知的字段全是字符串**：规范原话「字段类型为string字符串」。
14. **转账结果通知只推 API 发起的转账**：规范写明「仅 tradeSource=API 的转账」，在后台手工发起的转账不会推送。
15. **敏感数据**（建议）：`GET /v1/card/sensitive/{card_id}` 返回完整卡号和 CVV2，授权交易和 3DS 通知里带 OTP，不要写进日志，也不要长期明文存储。

## 业务流程

以下按资金链路整理，接口细节见后文各接口说明。

### 1. 资金准备

- `GET /v1/receiving/receiving-address` 获取数币收款地址（链：`ETH`、`TRX`，每个地址支持的币种见 `currencies`），向地址充值。
- 兑换目前只支持「卖出数币、买入法币」：`POST /v1/conversion`（`sell_currency`、`buy_currency`、`sell_amount`）。结果看 `CONVERSION_RESULT` 通知，或查 `GET /v1/conversion/{out_order_no}`（`PROCESSING` → `SUCCESS` / `FAILED`）。
- 余额与流水：`GET /v1/accounts/page`、`GET /v1/accounts/detail/{account_no}`（每个币种的可用余额、冻结余额）；`GET /v1/accounts/serials`（必须带 `account_no`、`currency` 和时间范围）。

### 2. 账户结构（可选）

- 主钱包（`MAIN`）是商户的默认钱包，不能通过接口变更状态。`POST /v1/accounts` 可创建子账户（`SUB_ACCOUNT`）；传 `purpose=SHARED_ACCOUNT` 创建共享账户，共享卡开卡时要传这个共享账户的 `account_no`。
- `POST /v1/transfer` 只能在「主钱包 ↔ 子账户」之间划转，`out_order_no` 是幂等键，重复请求返回已有结果。结果看 `TRANSFER_RESULT` 通知或 `GET /v1/transfer/order`（`PROCESSING` → `SUCCESS` / `FAIL`）。
- `PUT /v1/accounts/{account_no}/status` 停用或启用子账户、共享账户（主钱包不能改），必须填 2~100 字的原因。规范原话：「停用时冻结该钱包关联的发卡，不影响已发行卡的消费」。状态变化推送 `ACCOUNT_STATUS_CHANGED`。
- 账户状态：`INITIALIZING`（初始化中）、`ACTIVE`、`DISABLED`、`FAILED`、`REJECTED`。

### 3. 持卡人（开卡前必须）

1. 上传证件照：`POST /v1/file/upload?biz_type=CARDHOLDER_ID_PHOTO`，单个文件不超过 7MB，签名方式特殊（见「上传文件」）。拿到 `file_id`，一个持卡人最多 2 张。
2. 添加持卡人：`POST /v1/cardholder`。要点：
   - `out_card_holder_id` 在同一商户下不能重复；
   - 英文姓名只能用 ASCII 字符，中文姓名选填；
   - 国家/地区为 `US` 时必须填 `ssn`（格式 `123-45-6789`），其他国家不要传；
   - 证件有效期起始日不能晚于提交日，截止日必须晚于提交日，长期有效填 `9999-12-31`；
   - 居住地址和账单地址都必填。
3. 返回 `card_holder_id`，状态为 `PENDING_REVIEW`（待审核）。审核结果看 `CARDHOLDER_STATUS_CHANGE` 通知，或查 `GET /v1/cardholder/{card_holder_id}`。只有 `APPROVED` 才能开卡；`REJECTED` 时 `remark` 是驳回原因，可用 `PUT /v1/cardholder/{card_holder_id}` 修改资料（规范没说明修改后是否重新进入审核）。
4. `PUT /v1/cardholder/status/{card_holder_id}` 只能在 `APPROVED`（启用）和 `DISABLED`（禁用）之间切换。`SUSPENDED` 是风控停用，不能通过这个接口改。

### 4. 开卡

1. `GET /v1/card/product` 选卡产品：卡组（如 VISA）、发卡国家、卡币种、卡模式（`NORMAL` 储值卡 / `SHARE` 共享卡）、支持的手续费扣收方式。
2. `POST /v2/card` 申卡（注意是 v2，其他接口都是 v1）：
   - `out_order_no` 最长 32 位；
   - `amount` 是开卡时的充值金额，最多 2 位小数；
   - `card_name` 最长 16 位；
   - `expiry_date` 格式 `MMyy`，范围是一个月后到四年内；
   - 共享卡要传共享账户的 `account_no`；
   - `fee_deduct_type` 不传默认 `WALLET`（钱包扣费），传 `CARD_ACCOUNT`（卡扣费）前要确认卡产品支持；
   - 可选 `limit.single_limit` 设单笔限额。
3. 申卡结果看 `CARD_APPLY_RESULT` 通知，或查 `GET /v1/card/apply-order`（`PROCESSING` → `SUCCESS` / `FAILED`），成功后拿到 `card_id`。
4. 卡信息：`GET /v1/card/detail/{card_id}`（余额、状态、限额）；`GET /v1/card/sensitive/{card_id}`（完整卡号、CVV2、有效期）。

### 5. 卡资金

- 卡转入 `POST /v1/card/recharge-transaction`，卡转出 `POST /v1/card/withdraw-transaction`，参数都是 `out_order_no`、`card_id`、`currency`、`amount`。
- 状态：`APPLY`（已申请）→ `SUCCESS` / `FAIL`。结果看 `CARD_TRANSFER_RESULT` 通知，或查 `GET /v1/card/transfer-order`；明细分页查 `GET /v1/card/balance-transactions`。

### 6. 卡状态

- `PUT /v1/card/status`：`ACTIVE`（正常）、`REVOKED`（冻结）、`CANCELED`（注销）。
- 注销时卡内余额转入 `account_no` 指定的钱包，不传则转回卡绑定的默认主账户。卡详情里还有 `INCANCELED` 状态，规范没解释，从字面看是「注销中」。
- 状态变化推送 `CARD_STATUS_CHANGED`。

### 7. 卡交易

- **授权**：`AUTH`（授权）、`AUTH_REVERSAL`（授权撤销，带 `origin_transaction_id` 指向原授权）。状态 `approved` / `failed` / `pending`，拒绝时有 `decline_reason`。推送 `AUTH_EVENT`，分页查 `GET /v1/card/transactions/authorization`。
- **结算**：`SETTLE`（结算）、`REFUND`（退款，带 `origin_transaction_id` 指向原结算）。`auth_transaction_id` 关联授权单，`arn` 用于对账和争议处理。推送 `SETTLE_EVENT`，分页查 `GET /v1/card/transactions/settlement`。
- **3DS**：发生 3DS 验证时推送 `CARD_3DS_OTP`，带 OTP、脱敏卡号、交易金额和商户名。
- **金额**：按卡组织惯例，`transaction_amount` / `transaction_currency` 是商户侧的交易金额，`billing_amount` / `billing_currency` 是从卡上扣的金额（规范只写了「交易金额」「账单金额」）。查询接口返回 `fee_details` 手续费明细（类型有 `smallTransactionFee`、`transactionFee`、`failedFee`、`conversionFee`、`discardFee`、`refundFee`、`crossBorderTransactionFee`、`gatewayFee`），Webhook 里没有这个字段。

## Webhook 事件一览

推送到商户在开发者平台配置的通知地址；规范里的 `/webhook/...` 路径只是文档里的分组，事件类型以请求体里的 `topic` 为准。需要先完成开发者审核并订阅对应事件才会推送。商户要在 3 秒内建立连接、15 秒内返回 HTTP 200，否则视为失败并重试；用 `message_id` 去重。

| topic | 事件 | 触发时机 | 对应查询接口 |
|---|---|---|---|
| `CARD_APPLY_RESULT` | 申卡结果 | 申卡处理完成 | `GET /v1/card/apply-order` |
| `CARD_STATUS_CHANGED` | 卡状态变更 | 激活、冻结、注销 | `GET /v1/card/detail/{card_id}` |
| `CARDHOLDER_STATUS_CHANGE` | 持卡人状态变更 | 审核结果或状态变化 | `GET /v1/cardholder/{card_holder_id}` |
| `ACCOUNT_STATUS_CHANGED` | 账户状态变更 | 子账户或共享账户启用、停用 | `GET /v1/accounts/detail/{account_no}` |
| `AUTH_EVENT` | 授权交易 | 授权通过或拒绝 | `GET /v1/card/transactions/authorization` |
| `SETTLE_EVENT` | 结算交易 | 结算或退款处理完成 | `GET /v1/card/transactions/settlement` |
| `CARD_TRANSFER_RESULT` | 卡转入转出结果 | 卡充值或提现完成 | `GET /v1/card/transfer-order` |
| `CONVERSION_RESULT` | 兑换结果 | 兑换完成或失败 | `GET /v1/conversion/{out_order_no}` |
| `TRANSFER_RESULT` | 转账结果 | API 发起的转账到达终态 | `GET /v1/transfer/order` |
| `CARD_3DS_OTP` | 3DS 验证码 | 卡发生 3DS 验证 | 无 |

## 幂等键汇总

| 场景 | 幂等键 | 长度限制 | 说明 |
|---|---|---|---|
| 申卡 | `out_order_no` | 32 | 重复提交返回 409 |
| 卡转入 / 卡转出 | `out_order_no` | 未标注 | |
| 兑换 | `out_order_no` | 32 | 查询接口也用它 |
| 转账 | `out_order_no` | 64 | 规范明确是幂等键，重复请求返回已有结果 |
| 添加持卡人 | `out_card_holder_id` | 未标注 | 同一商户下不能重复 |
| 创建账户 | `out_account_no` | 64 | 必填，响应里原样返回 |
| Webhook | `message_id` | — | 同一消息可能推送多次 |


## 认证与签名（官方原文）

MarsWallex 企业级支付平台 API。所有请求需包含 Authorization 和 F-Signature 头。
### 认证与安全
MarsWallex API 使用 API 密钥来验证请求。使用以下命令生成 RSA 密钥：
```
# creating rsa private key
openssl genrsa -out rsa_private_key.pem 2048
# convert private key into PKCS#8 format
openssl pkcs8 -topk8 -inform PEM -in rsa_private_key.pem -outform PEM -nocrypt -out private_pkcs8.pem
# get public key from private key (X509 format)
openssl rsa -in rsa_private_key.pem -pubout -out rsa_public_key.pem
```
在所有请求中必须包含 **Authorization** 头，其格式如下：
```
Authorization: Basic {token}
```
令牌值计算方式为 `BASE64(developerId + ":" + apiToken)`，示例：
```
String developerId = "2sfn7zUrD530FFZ9gl5E8Yvex";
String apiToken = "fZjOVab5E7xDfDEyXU75SFaI137s8iL6";
String authorization = "Basic " + Base64.getEncoder().encodeToString(("2sfn7zUrD530FFZ9gl5E8Yvex:fZjOVab5E7xDfDEyXU75SFaI137s8iL6").getBytes());
```
您的 API 密钥具有许多权限，因此请务必妥善保存，不要在公开访问的区域（如 GitHub、客户端代码等）分享您的秘密 API 密钥。所有 API 请求必须通过 HTTPS 进行，通过普通 HTTP 进行的调用将会失败，没有身份验证的 API 请求也将会失败。
#### 防止重放攻击
重放攻击是指攻击者拦截有效的有效载荷及其签名，然后重新进行传输。为了减轻这种攻击，MarsWallex 在请求和响应的 F-Signature 头中包含了一个时间戳。由于此时间戳是签名有效载荷的一部分，因此也会被签名验证，所以攻击者不能在不使签名失效的情况下更改时间戳。如果签名有效但时间戳太旧，调用将被拒绝。
#### 请求签名
F-Signature 头包含时间戳和签名。时间戳以 `t=` 为前缀，每个签名以一个方案为前缀，方案以 `v` 开头。
```
t=1706498637,v=XJkZiVrkxgyhBOdw/9hmvbr+aetZ1YU44nEGuX0XYvFpsrRtiiKxcWzQ4ugbtE6mo/9A5hEvpalSDbeXBU968hslKBxkyfCPM1tm8Ye8gF8/tt4UqjSDGCwLqWAG9A98QFAhU6N1exBpANv7/BZwLmKJcutqhG0jdd6EsgsavI4=
```
请注意，上述示例中为了清晰起见可能添加了换行符，但实际的 F-Signature 头是单行的。MarsWallex 和客户将交换各自的公钥，F-Signature 使用 RSA 私钥进行签名。

**步骤 1：** 确定签名负载。使用以下公式创建 payload 字符串：
`HTTP_METHOD&URI&REQUEST_EPOCH&REQUEST_PAYLOAD[&QUERY_STRING]`
- **HTTP_METHOD**：为特定 API 端点定义的实际方法，如 POST
- **URI**：请求的 URL 路径，例如 /profiles/{profileId}
- **REQUEST_EPOCH**：自 1970/1/1 00:00:00 GMT 以来经过的秒数，与 `t` 的值相同
- **REQUEST_PAYLOAD**：请求体的内容，格式为 JSON；**multipart/form-data（文件上传）请求为空，见下文「文件上传」**
- **QUERY_STRING**：URL 的查询字符串，经过 URL 编码，例如 attr1%3Dvalue1%26attr2%3Dvalue2
  - 案例 1：QUERY_STRING 为空，公式为：`HTTP_METHOD&URI&REQUEST_EPOCH&REQUEST_PAYLOAD`
  - 案例 2：REQUEST_PAYLOAD 为空，公式为：`HTTP_METHOD&URI&REQUEST_EPOCH&&QUERY_STRING`
  - 案例 3：REQUEST_PAYLOAD 和 QUERY_STRING 都为空，公式为：`HTTP_METHOD&URI&REQUEST_EPOCH&`

GET 示例 payload：
```
GET&/portal/api/v1/accounts&1766123535&
```
POST 示例 payload：
```
POST&/portal/api/v1/cardholder&1766123535&{"out_card_holder_id":"CH0001","first_name":"Tom"}
```
**文件上传（multipart/form-data）**

文件上传接口的请求体为 multipart/form-data，文件内容**不参与签名**，REQUEST_PAYLOAD 按空处理，适用「案例 2」的公式：
- 表单中只放 `file` 一个字段；
- 业务参数（如 `biz_type`）必须放在 URL 查询字符串中，随 QUERY_STRING 参与签名，表单中的同名参数不会被读取。

请求示例：
```
POST /portal/api/v1/file/upload?biz_type=CARDHOLDER_ID_PHOTO
Content-Type: multipart/form-data; boundary=----FormBoundary
```
签名 payload：
```
POST&/portal/api/v1/file/upload&1766123535&&biz_type%3DCARDHOLDER_ID_PHOTO
```
**步骤 2：** 准备 F-Signature 字符串：
- 时间戳（自 1970 年 1 月 1 日 00:00:00 GMT 以来的秒数，作为字符串）
- 使用您的 RSA 私钥计算 `BASE64(RSAwithSHA256(STEP_1_RESULT))` 字符串（使用步骤 1 中的结果）
#### 响应消息签名
MarsWallex 的响应头同样包含 F-Signature，该签名由 MarsWallex 的 RSA 私钥签署。您可以使用 MarsWallex 的 RSA 公钥验证签名是否与响应体对应，步骤如下：

**步骤 1：确定签名 payload**

创建一个以 `&` 分隔如下内容的 payload 字符串：
- 响应时间戳：从 F-Signature 中提取 `t` 作为时间戳
- 响应有效负载：响应体的内容，格式为 JSON

payload 示例：
```
1766123822742&{"data":[{"account_name":"Wallet Account","account_no":"2000519969952288769","account_status":"NORMAL","available_balance":"89246.14","create_time":"2025-12-15T10:55:03.000+00:00","currency":"USD"},{"account_name":"Wallet Account","account_no":"2000519969964871682","account_status":"NORMAL","available_balance":"35.00","create_time":"2025-12-15T10:55:03.000+00:00","currency":"USDT"}]}
```
**步骤 2：使用 RSA 验证比较签名**

使用 MarsWallex 公钥将接收到的 F-Signature 与步骤 1 中计算的 payload 进行验证：
```
RSA.verify(F-Signature, '1766123822742&{"data":[...]}', MarsWallex Public Key)
```
### Webhook 通知
#### 概述
Webhook 是一种基于 HTTP 回调的实时通知机制。当 MarsWallex 平台上发生特定业务事件时，系统会主动向商户预先配置的通知地址发送 HTTP POST 请求，将事件数据推送到商户服务器。
#### Webhook 触发机制
MarsWallex 在以下条件同时满足时触发 Webhook 通知：
1. **开发者完成审核**：商户在开发者平台完成 API 接入审核
2. **订阅关系已建立**：商户已订阅该事件类型
#### 数据传输格式
##### 请求方法
所有 Webhook 通知均使用 **HTTP POST** 方法发送，请求体为 JSON 格式。
##### 请求头
| 请求头 | 说明 |
|---|---|
| `Content-Type` | `application/json; charset=utf-8` |
| `F-Signature` | 签名信息，格式为 `t={epoch},v={signature}` |
##### 请求体结构
```json
{
    "topic": "CARD_APPLY_RESULT",
    "messageId": "1234567890",
    "data": "{业务数据JSON字符串}"
}
```
| 字段 | 类型 | 说明 |
|---|---|---|
| `topic` | String | 事件类型标识，对应事件类型表中的 topic 值 |
| `messageId` | String | 事件消息唯一标识，用于消息追踪和幂等校验 |
| `data` | String | 业务数据内容，为 JSON 字符串格式，具体结构由各事件类型决定 |
##### 响应要求
商户服务器收到 Webhook 请求后，应返回 **HTTP 200** 状态码，表示接收成功。任何非 200 状态码或网络异常均视为投递失败，将触发重试机制。
#### Webhook 签名验证
为确保 Webhook 通知的真实性和完整性，MarsWallex 对每一条 Webhook 请求都会进行 RSA 签名。商户在接收到 Webhook 通知后，**必须**验证签名，以确认请求确实来自 MarsWallex 平台且未被篡改。
##### 签名算法
- **签名方式**：`SHA256withRSA`
- **签名密钥**：MarsWallex 平台私钥
- **验签密钥**：MarsWallex 平台公钥（商户需提前获取）
##### 签名头格式
F-Signature 请求头与 API 请求签名格式一致：
```
F-Signature: t={epoch},v={signature}
```
- `t`：时间戳，自 1970/1/1 00:00:00 GMT 以来经过的秒数
- `v`：签名值，为 `Base64(SHA256withRSA(签名原文))` 的结果
##### 验签步骤
**步骤 1：确定签名原文**

签名原文由时间戳和请求体以 `&` 连接而成：
```
{epoch}&{请求体JSON}
```
示例：
```
1706498637&{"topic":"CARD_APPLY_RESULT","messageId":"1234567890","data":"{\"cardId\":\"C123456\",\"status\":\"ACTIVE\"}"}
```
**步骤 2：提取签名信息**

从请求头 `F-Signature` 中提取时间戳 `t` 和签名值 `v`：
```
t=1706498637,v=XJkZiVrkxgyhBOdw/9hmvbr+aetZ1YU44nEGuX0XYvFpsrRtiiKxcWzQ4ugbtE6mo/9A5hEvpalSDbeXBU968hslKBxkyfCPM1tm8Ye8gF8/tt4UqjSDGCwLqWAG9A98QFAhU6N1exBpANv7/BZwLmKJcutqhG0jdd6EsgsavI4=
```
**步骤 3：使用 MarsWallex 公钥验签**

使用步骤 1 中构造的签名原文和步骤 2 中提取的签名值，通过 MarsWallex 平台公钥进行 RSA 验签：
```
RSA.verify(signatureValue, signingPayload, marsWallexPublicKey)
```
其中：
- `signatureValue`：从 F-Signature 中提取的 `v` 值（Base64 解码后的签名字节）
- `signingPayload`：步骤 1 构造的签名原文字符串
- `marsWallexPublicKey`：MarsWallex 平台公钥
##### 防重放攻击
签名原文中包含时间戳，因此攻击者无法在不使签名失效的情况下更改时间戳。建议商户在验签通过后，额外校验时间戳与当前时间的差值，超过合理范围（如 5 分钟）的请求应予以拒绝。
#### 重试策略
当 Webhook 投递失败（商户返回非 200 状态码或网络异常）时，MarsWallex 将自动进行重试。
#### 可靠性相关
1. **幂等处理**：同一条 Webhook 消息可能因网络问题或重试机制被多次推送，商户应基于 `messageId` 进行幂等处理，避免重复处理同一事件。
2. **快速响应**：商户服务器收到 Webhook 请求后，应尽快返回 HTTP 200，将业务逻辑异步处理。如果处理时间过长导致连接超时（连接超时 3 秒，读取超时 15 秒），系统将视为投递失败并触发重试。
3. **时效性**：在极少情况下可能发生异步通知无法正常通知到您服务器的情况，请务必对接主动查单接口。

## 通用约定

### 公共请求头

| 请求头 | 必填 | 说明 |
|---|---|---|
| `Authorization` | 是 | `Basic BASE64(developerId:apiToken)` |
| `F-Signature` | 是 | RSA 签名，格式：t={timestamp},v={signature} |
| `Content-Type` | 是（有请求体时） | `application/json`；文件上传为 `multipart/form-data` |

### HTTP 状态码

| 状态码 | 含义（取各接口最常见说明） |
|---|---|
| `200` | 成功 |
| `201` | 资源已创建 |
| `202` | 收到该状态码说明请求已受理，请稍后查询结果或等待异步通知。 |
| `400` | 请求错误 |
| `401` | 未认证 |
| `403` | 权限不足 |
| `404` | 资源不存在 |
| `409` | 请求重复提交，需轮询结果或等待异步通知 |
| `500` | 服务端异常，不能作为交易结果的依据，请稍后查询结果或等待异步通知 |

### 错误响应体 `ErrorResponse`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `code` | string | 是 | 错误码 |
| `message` | string | 是 | 错误描述 |

## 接口一览

| 分类 | 方法 | 路径 | 说明 |
|---|---|---|---|
| 账户管理 | `GET` | [`/v1/accounts`](#get-v1-accounts) | 查询账户列表（官网隐藏） |
| 账户管理 | `POST` | [`/v1/accounts`](#post-v1-accounts) | 创建账户 |
| 账户管理 | `GET` | [`/v1/accounts/page`](#get-v1-accounts-page) | 分页查询账户 |
| 账户管理 | `GET` | [`/v1/accounts/serials`](#get-v1-accounts-serials) | 查询账户流水 |
| 账户管理 | `GET` | [`/v1/accounts/detail/{account_no}`](#get-v1-accounts-detail-account-no) | 查询账户详情 |
| 账户管理 | `PUT` | [`/v1/accounts/{account_no}/status`](#put-v1-accounts-account-no-status) | 账户状态变更 |
| 卡管理 | `GET` | [`/v1/card/product`](#get-v1-card-product) | 查询卡产品 |
| 卡管理 | `POST` | [`/v2/card`](#post-v2-card) | 申卡 |
| 卡管理 | `GET` | [`/v1/card/apply-order`](#get-v1-card-apply-order) | 查询卡申请单 |
| 卡管理 | `POST` | [`/v1/card/recharge-transaction`](#post-v1-card-recharge-transaction) | 卡转入 |
| 卡管理 | `POST` | [`/v1/card/withdraw-transaction`](#post-v1-card-withdraw-transaction) | 卡转出 |
| 卡管理 | `PUT` | [`/v1/card/status`](#put-v1-card-status) | 更新卡状态 |
| 卡管理 | `GET` | [`/v1/card/transfer-order`](#get-v1-card-transfer-order) | 转入转出结果查询 |
| 卡管理 | `GET` | [`/v1/card/balance-transactions`](#get-v1-card-balance-transactions) | 分页卡转入转出明细 |
| 卡管理 | `GET` | [`/v1/card/sensitive/{card_id}`](#get-v1-card-sensitive-card-id) | 查询卡敏感信息 |
| 卡管理 | `GET` | [`/v1/card/detail/{card_id}`](#get-v1-card-detail-card-id) | 查询卡详情 |
| 持卡人 | `POST` | [`/v1/cardholder`](#post-v1-cardholder) | 添加持卡人 |
| 持卡人 | `GET` | [`/v1/cardholder/{card_holder_id}`](#get-v1-cardholder-card-holder-id) | 查询持卡人 |
| 持卡人 | `PUT` | [`/v1/cardholder/{card_holder_id}`](#put-v1-cardholder-card-holder-id) | 修改持卡人 |
| 持卡人 | `PUT` | [`/v1/cardholder/status/{card_holder_id}`](#put-v1-cardholder-status-card-holder-id) | 修改持卡人状态 |
| 卡交易 | `GET` | [`/v1/card/transactions/authorization`](#get-v1-card-transactions-authorization) | 查询卡授权交易 |
| 卡交易 | `GET` | [`/v1/card/transactions/settlement`](#get-v1-card-transactions-settlement) | 查询卡结算交易 |
| 收款 | `GET` | [`/v1/receiving/receiving-address`](#get-v1-receiving-receiving-address) | 查询数币收款地址 |
| 兑换 | `POST` | [`/v1/conversion`](#post-v1-conversion) | 兑换申请 |
| 兑换 | `GET` | [`/v1/conversion/{out_order_no}`](#get-v1-conversion-out-order-no) | 查询兑换单详情 |
| 转账 | `POST` | [`/v1/transfer`](#post-v1-transfer) | 转账申请 |
| 转账 | `GET` | [`/v1/transfer/order`](#get-v1-transfer-order) | 转账查询 |
| 公共接口 | `POST` | [`/v1/file/upload`](#post-v1-file-upload) | 上传文件 |
| Webhook | `POST` | [`/webhook/card-apply-result`](#post-webhook-card-apply-result) | 申卡结果通知 |
| Webhook | `POST` | [`/webhook/card-status-change`](#post-webhook-card-status-change) | 卡状态变更通知 |
| Webhook | `POST` | [`/webhook/cardholder-status-change`](#post-webhook-cardholder-status-change) | 持卡人状态变更通知 |
| Webhook | `POST` | [`/webhook/account-status-change`](#post-webhook-account-status-change) | 账户状态变更通知 |
| Webhook | `POST` | [`/webhook/auth-transaction`](#post-webhook-auth-transaction) | 授权交易通知 |
| Webhook | `POST` | [`/webhook/settlement-transaction`](#post-webhook-settlement-transaction) | 结算交易通知 |
| Webhook | `POST` | [`/webhook/card-transfer-result`](#post-webhook-card-transfer-result) | 卡转入转出结果通知 |
| Webhook | `POST` | [`/webhook/conversion-result`](#post-webhook-conversion-result) | 兑换结果通知 |
| Webhook | `POST` | [`/webhook/transfer-result`](#post-webhook-transfer-result) | 转账结果通知 |
| Webhook | `POST` | [`/webhook/card-3ds-otp`](#post-webhook-card-3ds-otp) | 卡3DS OTP通知 |

## 账户管理

账户查询与流水

<a id="get-v1-accounts"></a>

### 查询账户列表

`GET /v1/accounts`

> 规范里标了 `x-internal: true`，官网文档页面不显示这个接口，可能不对外开放，使用前先向 MarsWallex 确认。

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object[] | 否 |  |
| `data[].account_no` | string | 是 | 账户号 |
| `data[].account_name` | string | 是 | 账户名称 |
| `data[].account_status` | string | 是 | 账户状态；可选值: `ACTIVE`, `DISABLED`, `INITIALIZING`, `FAILED`, `REJECTED` |
| `data[].create_time` | number | 是 | 创建时间（Unix 秒） |
| `data[].balance_list` | object[] | 否 | 余额列表 |
| `data[].balance_list[].currency` | string | 是 | 币种 (ISO 4217 / USDT) |
| `data[].balance_list[].available_balance` | string | 是 | 可用余额（元） |
| `data[].balance_list[].freeze_balance` | string | 是 | 冻结余额（元） |
| `data[].account_type` | string | 是 | 账户类型：MAIN=主钱包，SUB_ACCOUNT=子账户；可选值: `MAIN`, `SUB_ACCOUNT` |
| `data[].purpose` | string | 否 | 用途：SHARED_ACCOUNT=共享账户，null 或不返回=普通账户；可选值: `SHARED_ACCOUNT`；可为 null |

**其他状态码**：`400` 请求错误

<a id="post-v1-accounts"></a>

### 创建账户

`POST /v1/accounts`

创建账户/共享账户。传 purpose=SHARED_ACCOUNT 创建共享账户。

**请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 是 |  |
| `data.account_name` | string | 是 | 账户别名，不含空格或换行；最大长度: `50`；示例: `SubAccountA` |
| `data.out_account_no` | string | 是 | 商户侧账户号（创建必填，必回显），不含空格或换行；最大长度: `64`；示例: `SUB202607200001` |
| `data.purpose` | string | 否 | 用途：传 SHARED_ACCOUNT 创建共享账户；不传或传 null 创建普通子账户；可选值: `SHARED_ACCOUNT`；可为 null |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 是 |  |
| `data.account_no` | string | 是 | 账户 ID |
| `data.account_name` | string | 是 | 账户别名 |
| `data.out_account_no` | string | 是 | 商户侧账户号 |
| `data.purpose` | string | 否 | 用途：SHARED_ACCOUNT=共享账户，null=普通子账户；可选值: `SHARED_ACCOUNT`；可为 null |
| `data.status` | string | 是 | 账户状态：ACTIVE=启用，DISABLED=停用；可选值: `ACTIVE`, `DISABLED` |
| `data.create_time` | number | 是 | 创建时间（Unix 秒） |

**其他状态码**：`201` 收到该状态码说明资源已创建，请勿重复提交，需轮询交易结果或等待异步通知。；`202` 收到该状态码说明请求已受理，请稍后查询结果或等待异步通知。；`400` 参数错误；`401` 未认证；`403` 权限不足；`404` 资源不存在；`409` 收到该状态码说明请求重复提交，需轮询交易结果或等待异步通知。；`500` 收到该状态码说明服务端有异常，收到该状态码不能作为交易结果的依据，请稍后查询结果或等待异步通知。

<a id="get-v1-accounts-page"></a>

### 分页查询账户

`GET /v1/accounts/page`

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `page_no` | query | integer | 是 | 页码 |
| `page_size` | query | integer | 是 | 分页大小 |
| `account_type` | query | string | 否 | 账户类型：MAIN=主钱包，SUB_ACCOUNT=子账户；可选值: `MAIN`, `SUB_ACCOUNT` |
| `account_no` | query | string | 否 | 账户号 |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `count` | string | 是 | 结果集总数 |
| `data` | object[] | 是 | 结果集 |
| `data[].account_no` | string | 是 | 账户号 |
| `data[].account_name` | string | 是 | 账户名称 |
| `data[].account_status` | string | 是 | 账户状态；可选值: `ACTIVE`, `DISABLED`, `INITIALIZING`, `FAILED`, `REJECTED` |
| `data[].create_time` | number | 是 | 创建时间（Unix 秒） |
| `data[].balance_list` | object[] | 否 | 余额列表 |
| `data[].balance_list[].currency` | string | 是 | 币种 (ISO 4217 / USDT) |
| `data[].balance_list[].available_balance` | string | 是 | 可用余额（元） |
| `data[].balance_list[].freeze_balance` | string | 是 | 冻结余额（元） |
| `data[].account_type` | string | 是 | 账户类型：MAIN=主钱包，SUB_ACCOUNT=子账户；可选值: `MAIN`, `SUB_ACCOUNT` |
| `data[].purpose` | string | 否 | 用途：SHARED_ACCOUNT=共享账户，null 或不返回=普通账户；可选值: `SHARED_ACCOUNT`；可为 null |

**其他状态码**：`400` 请求错误

<a id="get-v1-accounts-serials"></a>

### 查询账户流水

`GET /v1/accounts/serials`

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `page_no` | query | integer | 是 | 页码 |
| `page_size` | query | integer | 是 | 分页大小 |
| `account_no` | query | string | 是 |  |
| `currency` | query | string | 是 |  |
| `start_time` | query | integer | 是 | 开始时间（Unix 秒） |
| `end_time` | query | integer | 是 | 结束时间（Unix 秒） |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `count` | string | 否 | 结果集总数 |
| `data` | object[] | 否 | 流水列表 |
| `data[].serial_id` | string | 是 | 流水号 |
| `data[].currency` | string | 是 | 币种 |
| `data[].amount` | string | 是 | 金额 |
| `data[].flag_dc` | string | 是 | 收支方向：DEBIT=出账，CREDIT=入账，MAIN_TO_SUB_ACCOUNT=主账户转入子账户，SUB_ACCOUNT_TO_MAIN=子账户转入主账户；可选值: `DEBIT`, `CREDIT`, `MAIN_TO_SUB_ACCOUNT`, `SUB_ACCOUNT_TO_MAIN` |
| `data[].transaction_type` | string | 是 | 交易类型：CONSUME=消费，CHARGE=充值，REFUND=退款，EXCHANGE=汇兑，REVERSE=撤销，FEE=手续费，LIMIT_ADJUST=限额调整，TRANSFER=转账；可选值: `CONSUME`, `CHARGE`, `REFUND`, `EXCHANGE`, `REVERSE`, `FEE`, `LIMIT_ADJUST`, `TRANSFER` |
| `data[].create_time` | number | 是 | 动账时间（Unix 秒） |

**其他状态码**：`400` 请求错误

<a id="get-v1-accounts-detail-account-no"></a>

### 查询账户详情

`GET /v1/accounts/detail/{account_no}`

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `account_no` | path | string | 是 |  |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 否 |  |
| `data.account_no` | string | 是 | 账户号 |
| `data.account_name` | string | 是 | 账户名称 |
| `data.account_status` | string | 是 | 账户状态；可选值: `ACTIVE`, `DISABLED`, `INITIALIZING`, `FAILED`, `REJECTED` |
| `data.create_time` | number | 是 | 创建时间（Unix 秒） |
| `data.balance_list` | object[] | 否 | 余额列表 |
| `data.balance_list[].currency` | string | 是 | 币种 (ISO 4217 / USDT) |
| `data.balance_list[].available_balance` | string | 是 | 可用余额（元） |
| `data.balance_list[].freeze_balance` | string | 是 | 冻结余额（元） |
| `data.account_type` | string | 是 | 账户类型：MAIN=主钱包，SUB_ACCOUNT=子账户；可选值: `MAIN`, `SUB_ACCOUNT` |
| `data.purpose` | string | 否 | 用途：SHARED_ACCOUNT=共享账户，null 或不返回=普通账户；可选值: `SHARED_ACCOUNT`；可为 null |

**其他状态码**：`400` 请求错误

<a id="put-v1-accounts-account-no-status"></a>

### 账户状态变更

`PUT /v1/accounts/{account_no}/status`

变更账户/共享账户状态。停用时冻结该钱包关联的发卡，不影响已发行卡的消费。主钱包禁止变更。状态变更后异步推送 ACCOUNT_STATUS_CHANGED Webhook。

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `account_no` | path | string | 是 | 子账户系统ID |

**请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `account_status` | string | 是 | 目标状态：DISABLED=停用，ACTIVE=启用；可选值: `DISABLED`, `ACTIVE` |
| `remark` | string | 是 | 变更原因（必填），2~100字符，不支持 emoji；最大长度: `100`；最小长度: `2` |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 是 |  |
| `data.account_no` | string | 是 | 账户号 |
| `data.out_account_no` | string | 是 | 商户侧账户号 |

**其他状态码**：`201` 收到该状态码说明资源已创建，请勿重复提交，需轮询交易结果或等待异步通知。；`202` 收到该状态码说明请求已受理，请稍后查询结果或等待异步通知。；`400` 参数错误；`401` 未认证；`403` 权限不足；`404` 资源不存在；`409` 收到该状态码说明请求重复提交，需轮询交易结果或等待异步通知。；`500` 收到该状态码说明服务端有异常，收到该状态码不能作为交易结果的依据，请稍后查询结果或等待异步通知。

## 卡管理

卡产品、申卡、转入转出、状态管理

<a id="get-v1-card-product"></a>

### 查询卡产品

`GET /v1/card/product`

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object[] | 否 |  |
| `data[].product_id` | string | 是 | 卡产品ID |
| `data[].product_name` | string | 是 | 卡产品名称 |
| `data[].card_label` | string | 是 | 卡组（如 VISA） |
| `data[].card_country` | string | 是 | 卡所属国家 (ISO 3166-2) |
| `data[].card_mode` | string | 是 | NORMAL=储值卡, SHARE=共享卡；可选值: `NORMAL`, `SHARE` |
| `data[].card_currency` | string | 是 | 卡币种 (ISO 4217) |
| `data[].supported_fee_deduct_types` | string[] | 否 | 卡产品支持的手续费扣收方式：CARD_ACCOUNT=卡扣费，WALLET=钱包扣费 |

**其他状态码**：`400` 请求错误

<a id="post-v2-card"></a>

### 申卡

`POST /v2/card`

使用已创建的持卡人申卡，传入平台持卡人ID `card_holder_id`。持卡人需先通过「添加持卡人」接口创建，且审核通过（`APPROVED`）后方可开卡。

**请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `out_order_no` | string | 是 | 商家订单号；最大长度: `32` |
| `product_id` | string | 是 | 卡产品ID |
| `amount` | string | 是 | 充值金额（最多2位小数） |
| `currency` | string | 是 | 币种 |
| `card_holder_id` | string | 是 | 平台持卡人ID，需先调用添加持卡人接口创建获取 |
| `card_name` | string | 是 | 卡名称；最大长度: `16` |
| `expiry_date` | string | 是 | 过期时间 MMyy（一个月后到四年内） |
| `account_no` | string | 否 | 共享钱包账户号（共享卡需要传） |
| `remark` | string | 否 | 备注（最多255字符）；最大长度: `255` |
| `fee_deduct_type` | string | 否 | 手续费扣收方式：CARD_ACCOUNT=卡扣费，WALLET=钱包扣费。不传或传空默认 WALLET；是否支持 CARD_ACCOUNT 取决于卡产品，取值须在卡产品 supported_fee_deduct_types 列表内；可选值: `CARD_ACCOUNT`, `WALLET` |
| `limit` | object | 否 | 交易限额 |
| `limit.single_limit` | string | 否 | 单笔限额 |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 是 |  |
| `data.out_order_no` | string | 是 | 商家订单号；最大长度: `32` |
| `data.transaction_id` | string | 是 | 平台订单号 |

**其他状态码**：`201` 资源已创建，请勿重复提交，需轮询结果或等待异步通知；`202` 请求已受理，请稍后查询结果或等待异步通知；`400` 参数错误；`401` 未认证；`403` 权限不足；`404` 资源不存在；`409` 请求重复提交，需轮询结果或等待异步通知；`500` 服务端异常，不能作为交易结果的依据，请稍后查询结果或等待异步通知

<a id="get-v1-card-apply-order"></a>

### 查询卡申请单

`GET /v1/card/apply-order`

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `out_order_no` | query | string | 否 | 商家订单号（二选一） |
| `transaction_id` | query | string | 否 | 订单号（二选一，优先） |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 否 |  |
| `data.card_id` | string | 否 |  |
| `data.out_order_no` | string | 否 |  |
| `data.transaction_id` | string | 否 |  |
| `data.status` | string | 否 | 可选值: `SUCCESS`, `FAILED`, `PROCESSING` |
| `data.failure_reason` | string | 否 |  |

**其他状态码**：`400` 请求错误

<a id="post-v1-card-recharge-transaction"></a>

### 卡转入

`POST /v1/card/recharge-transaction`

**请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `out_order_no` | string | 是 |  |
| `card_id` | string | 是 |  |
| `currency` | string | 是 |  |
| `amount` | string | 是 | 金额（元） |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 否 |  |
| `data.out_order_no` | string | 是 | 商家订单号 |
| `data.transaction_id` | string | 是 | 平台订单号 |
| `data.card_id` | string | 是 |  |
| `data.currency` | string | 否 |  |
| `data.amount` | string | 否 | 金额（元） |
| `data.type` | string | 否 | CHARGE=转入, WITHDRAW=转出；可选值: `CHARGE`, `WITHDRAW` |
| `data.status` | string | 是 | 可选值: `APPLY`, `SUCCESS`, `FAIL` |
| `data.create_time` | number | 否 |  |

**其他状态码**：`201` 资源已创建；`400` 请求错误

<a id="post-v1-card-withdraw-transaction"></a>

### 卡转出

`POST /v1/card/withdraw-transaction`

**请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `out_order_no` | string | 是 |  |
| `card_id` | string | 是 |  |
| `currency` | string | 是 |  |
| `amount` | string | 是 | 金额（元） |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 否 |  |
| `data.out_order_no` | string | 是 | 商家订单号 |
| `data.transaction_id` | string | 是 | 平台订单号 |
| `data.card_id` | string | 是 |  |
| `data.currency` | string | 否 |  |
| `data.amount` | string | 否 | 金额（元） |
| `data.type` | string | 否 | CHARGE=转入, WITHDRAW=转出；可选值: `CHARGE`, `WITHDRAW` |
| `data.status` | string | 是 | 可选值: `APPLY`, `SUCCESS`, `FAIL` |
| `data.create_time` | number | 否 |  |

**其他状态码**：`201` 资源已创建；`400` 请求错误

<a id="put-v1-card-status"></a>

### 更新卡状态

`PUT /v1/card/status`

**请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `card_id` | string | 是 |  |
| `card_status` | string | 是 | ACTIVE=正常, REVOKED=冻结, CANCELED=注销；可选值: `ACTIVE`, `REVOKED`, `CANCELED` |
| `remark` | string | 否 |  |
| `account_no` | integer | 否 | 钱包ID，注销卡时（card_status=CANCELED）可选传入，指定余额转入的目标钱包；不传则转入卡绑定的默认主账户 |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 否 |  |
| `data.card_id` | string | 否 |  |
| `data.card_status` | string | 否 |  |

**其他状态码**：`400` 请求错误

<a id="get-v1-card-transfer-order"></a>

### 转入转出结果查询

`GET /v1/card/transfer-order`

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `out_order_no` | query | string | 否 | 商家订单号（二选一） |
| `transaction_id` | query | string | 否 | 平台订单号（二选一，优先） |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 否 |  |
| `data.out_order_no` | string | 是 | 商家订单号 |
| `data.transaction_id` | string | 是 | 平台订单号 |
| `data.card_id` | string | 是 |  |
| `data.currency` | string | 否 |  |
| `data.amount` | string | 否 | 金额（元） |
| `data.type` | string | 否 | CHARGE=转入, WITHDRAW=转出；可选值: `CHARGE`, `WITHDRAW` |
| `data.status` | string | 是 | 可选值: `APPLY`, `SUCCESS`, `FAIL` |
| `data.create_time` | number | 否 |  |

**其他状态码**：`400` 请求错误

<a id="get-v1-card-balance-transactions"></a>

### 分页卡转入转出明细

`GET /v1/card/balance-transactions`

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `page_no` | query | integer | 是 |  |
| `page_size` | query | integer | 是 |  |
| `card_id` | query | string | 是 |  |
| `start_time` | query | number | 是 |  |
| `end_time` | query | number | 是 |  |
| `out_order_no` | query | string | 否 |  |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `count` | string | 否 |  |
| `data` | object[] | 否 |  |
| `data[].out_order_no` | string | 是 | 商家订单号 |
| `data[].transaction_id` | string | 是 | 平台订单号 |
| `data[].card_id` | string | 是 |  |
| `data[].currency` | string | 否 |  |
| `data[].amount` | string | 否 | 金额（元） |
| `data[].type` | string | 否 | CHARGE=转入, WITHDRAW=转出；可选值: `CHARGE`, `WITHDRAW` |
| `data[].status` | string | 是 | 可选值: `APPLY`, `SUCCESS`, `FAIL` |
| `data[].create_time` | number | 否 |  |

**其他状态码**：`400` 请求错误

<a id="get-v1-card-sensitive-card-id"></a>

### 查询卡敏感信息

`GET /v1/card/sensitive/{card_id}`

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `card_id` | path | string | 是 |  |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 否 |  |
| `data.card_id` | string | 是 |  |
| `data.card_no` | string | 是 |  |
| `data.expiry_date` | string | 否 | MMyy |
| `data.cvv2` | string | 否 |  |
| `data.freeze_balance` | string | 否 |  |

**其他状态码**：`400` 请求错误

<a id="get-v1-card-detail-card-id"></a>

### 查询卡详情

`GET /v1/card/detail/{card_id}`

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `card_id` | path | string | 是 |  |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 否 |  |
| `data.card_id` | string | 是 |  |
| `data.card_no` | string | 是 | 卡号 |
| `data.card_name` | string | 是 |  |
| `data.card_type` | string | 否 | 卡类型（虚拟卡） |
| `data.card_mode` | string | 否 |  |
| `data.card_label` | string | 否 |  |
| `data.card_country` | string | 否 |  |
| `data.card_currency` | string | 否 |  |
| `data.expiry_date` | string | 否 | MMyy |
| `data.status` | string | 是 | 可选值: `ACTIVE`, `REVOKED`, `CANCELED`, `INCANCELED` |
| `data.available_balance` | string | 否 |  |
| `data.freeze_balance` | string | 否 |  |
| `data.remark` | string | 否 | 备注 |
| `data.create_time` | number | 否 |  |
| `data.limit` | object | 否 | 交易限额 |
| `data.limit.single_limit` | string | 否 | 单笔限额 |

**其他状态码**：`400` 请求错误

## 持卡人

持卡人添加、修改与查询

<a id="post-v1-cardholder"></a>

### 添加持卡人

`POST /v1/cardholder`

按商户侧持卡人ID（`out_card_holder_id`）添加持卡人，同一商户下不得重复。添加后状态为 `PENDING_REVIEW`，经平台审核通过（`APPROVED`）后方可用于开卡。

**请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `out_card_holder_id` | string | 是 | 商户侧持卡人ID |
| `first_name` | string | 是 | 名（英文，仅 ASCII 字符）；最大长度: `32` |
| `last_name` | string | 是 | 姓（英文，仅 ASCII 字符）；最大长度: `32` |
| `chinese_first_name` | string | 否 | 名（中文），选填；最大长度: `32` |
| `chinese_last_name` | string | 否 | 姓（中文），选填；最大长度: `32` |
| `date_of_birth` | string(date) | 是 | 出生日期，YYYY-MM-DD |
| `zone_number` | string | 是 | 手机国家区号，如 1、86；最大长度: `10` |
| `mobile` | string | 是 | 手机号；最大长度: `50` |
| `email` | string(email) | 是 | 邮箱；最大长度: `100` |
| `country_code` | string | 是 | 国家/地区，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `id_type` | string | 是 | 证件类型：PASSPORT=护照，IDCARD=身份证，OTHER_CERTIFICATE=其他；可选值: `PASSPORT`, `IDCARD`, `OTHER_CERTIFICATE` |
| `id_no` | string | 是 | 证件号；最大长度: `64` |
| `id_period_begin` | string(date) | 是 | 证件有效期起始日，YYYY-MM-DD，不得晚于提交日期，且不得晚于截止日 |
| `id_period_end` | string(date) | 是 | 证件有效期截止日，YYYY-MM-DD，须晚于提交日期；长期有效请填写 9999-12-31 |
| `id_photo_file_ids` | string[] | 是 | 证件照片文件ID（由上传文件接口返回），最多 2 个；最多项数: `2` |
| `residential_address` | object | 是 | 居住地址 |
| `residential_address.country` | string | 是 | 国家，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `residential_address.state` | string | 是 | 省 / 州；最大长度: `64` |
| `residential_address.city` | string | 是 | 城市；最大长度: `64` |
| `residential_address.address` | string | 是 | 详细地址；最大长度: `256` |
| `residential_address.post_code` | string | 是 | 邮编；最大长度: `10` |
| `billing_address` | object | 是 | 账单地址 |
| `billing_address.country` | string | 是 | 国家，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `billing_address.state` | string | 是 | 省 / 州；最大长度: `64` |
| `billing_address.city` | string | 是 | 城市；最大长度: `64` |
| `billing_address.address` | string | 是 | 详细地址；最大长度: `256` |
| `billing_address.post_code` | string | 是 | 邮编；最大长度: `10` |
| `ssn` | string | 否 | 美国社会安全号码，国家/地区为 US 时必填，否则不传；格式: `^\d{3}-\d{2}-\d{4}$` |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 是 |  |
| `data.card_holder_id` | string | 是 | 平台持卡人ID |

**其他状态码**：`400` 参数错误；`401` 未认证；`403` 权限不足；`404` 资源不存在；`409` 请求重复提交，需轮询结果或等待异步通知；`500` 服务端异常，不能作为交易结果的依据，请稍后查询结果或等待异步通知

<a id="get-v1-cardholder-card-holder-id"></a>

### 查询持卡人

`GET /v1/cardholder/{card_holder_id}`

按平台持卡人ID查询持卡人信息及审核状态。

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `card_holder_id` | path | string | 是 | 平台持卡人ID（添加持卡人接口返回） |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 是 |  |
| `data.card_holder_id` | string | 是 | 平台持卡人ID |
| `data.out_card_holder_id` | string | 是 | 商户侧持卡人ID |
| `data.first_name` | string | 是 | 名（英文，仅 ASCII 字符）；最大长度: `32` |
| `data.last_name` | string | 是 | 姓（英文，仅 ASCII 字符）；最大长度: `32` |
| `data.chinese_first_name` | string | 否 | 名（中文），选填；最大长度: `32` |
| `data.chinese_last_name` | string | 否 | 姓（中文），选填；最大长度: `32` |
| `data.date_of_birth` | string(date) | 是 | 出生日期，YYYY-MM-DD |
| `data.zone_number` | string | 是 | 手机国家区号，如 1、86；最大长度: `10` |
| `data.mobile` | string | 是 | 手机号；最大长度: `50` |
| `data.email` | string(email) | 是 | 邮箱；最大长度: `100` |
| `data.country_code` | string | 是 | 国家/地区，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `data.id_type` | string | 是 | 证件类型：PASSPORT=护照，IDCARD=身份证，OTHER_CERTIFICATE=其他；可选值: `PASSPORT`, `IDCARD`, `OTHER_CERTIFICATE` |
| `data.id_no` | string | 是 | 证件号；最大长度: `64` |
| `data.id_period_begin` | string(date) | 是 | 证件有效期起始日，YYYY-MM-DD，不得晚于提交日期，且不得晚于截止日 |
| `data.id_period_end` | string(date) | 是 | 证件有效期截止日，YYYY-MM-DD，须晚于提交日期；长期有效请填写 9999-12-31 |
| `data.id_photo_file_ids` | string[] | 是 | 证件照片文件ID（由上传文件接口返回），最多 2 个；最多项数: `2` |
| `data.residential_address` | object | 是 | 居住地址 |
| `data.residential_address.country` | string | 是 | 国家，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `data.residential_address.state` | string | 是 | 省 / 州；最大长度: `64` |
| `data.residential_address.city` | string | 是 | 城市；最大长度: `64` |
| `data.residential_address.address` | string | 是 | 详细地址；最大长度: `256` |
| `data.residential_address.post_code` | string | 是 | 邮编；最大长度: `10` |
| `data.billing_address` | object | 是 | 账单地址 |
| `data.billing_address.country` | string | 是 | 国家，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `data.billing_address.state` | string | 是 | 省 / 州；最大长度: `64` |
| `data.billing_address.city` | string | 是 | 城市；最大长度: `64` |
| `data.billing_address.address` | string | 是 | 详细地址；最大长度: `256` |
| `data.billing_address.post_code` | string | 是 | 邮编；最大长度: `10` |
| `data.ssn` | string | 否 | 美国社会安全号码，国家/地区为 US 时必填，否则不传；格式: `^\d{3}-\d{2}-\d{4}$` |
| `data.status` | string | 是 | 持卡人状态：PENDING_REVIEW=待审核，APPROVED=已通过（可开卡），REJECTED=已拒绝，SUSPENDED=风控停用，DISABLED=禁用；可选值: `PENDING_REVIEW`, `APPROVED`, `REJECTED`, `SUSPENDED`, `DISABLED` |
| `data.remark` | string | 否 | 备注，驳回时为驳回原因 |

**其他状态码**：`400` 参数错误；`401` 未认证；`403` 权限不足；`404` 资源不存在；`409` 请求重复提交，需轮询结果或等待异步通知；`500` 服务端异常，不能作为交易结果的依据，请稍后查询结果或等待异步通知

<a id="put-v1-cardholder-card-holder-id"></a>

### 修改持卡人

`PUT /v1/cardholder/{card_holder_id}`

按平台持卡人ID修改持卡人信息。

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `card_holder_id` | path | string | 是 | 平台持卡人ID（添加持卡人接口返回） |

**请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `first_name` | string | 是 | 名（英文，仅 ASCII 字符）；最大长度: `32` |
| `last_name` | string | 是 | 姓（英文，仅 ASCII 字符）；最大长度: `32` |
| `chinese_first_name` | string | 否 | 名（中文），选填；最大长度: `32` |
| `chinese_last_name` | string | 否 | 姓（中文），选填；最大长度: `32` |
| `date_of_birth` | string(date) | 是 | 出生日期，YYYY-MM-DD |
| `zone_number` | string | 是 | 手机国家区号，如 1、86；最大长度: `10` |
| `mobile` | string | 是 | 手机号；最大长度: `50` |
| `email` | string(email) | 是 | 邮箱；最大长度: `100` |
| `country_code` | string | 是 | 国家/地区，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `id_type` | string | 是 | 证件类型：PASSPORT=护照，IDCARD=身份证，OTHER_CERTIFICATE=其他；可选值: `PASSPORT`, `IDCARD`, `OTHER_CERTIFICATE` |
| `id_no` | string | 是 | 证件号；最大长度: `64` |
| `id_period_begin` | string(date) | 是 | 证件有效期起始日，YYYY-MM-DD，不得晚于提交日期，且不得晚于截止日 |
| `id_period_end` | string(date) | 是 | 证件有效期截止日，YYYY-MM-DD，须晚于提交日期；长期有效请填写 9999-12-31 |
| `id_photo_file_ids` | string[] | 是 | 证件照片文件ID（由上传文件接口返回），最多 2 个；最多项数: `2` |
| `residential_address` | object | 是 | 居住地址 |
| `residential_address.country` | string | 是 | 国家，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `residential_address.state` | string | 是 | 省 / 州；最大长度: `64` |
| `residential_address.city` | string | 是 | 城市；最大长度: `64` |
| `residential_address.address` | string | 是 | 详细地址；最大长度: `256` |
| `residential_address.post_code` | string | 是 | 邮编；最大长度: `10` |
| `billing_address` | object | 是 | 账单地址 |
| `billing_address.country` | string | 是 | 国家，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `billing_address.state` | string | 是 | 省 / 州；最大长度: `64` |
| `billing_address.city` | string | 是 | 城市；最大长度: `64` |
| `billing_address.address` | string | 是 | 详细地址；最大长度: `256` |
| `billing_address.post_code` | string | 是 | 邮编；最大长度: `10` |
| `ssn` | string | 否 | 美国社会安全号码，国家/地区为 US 时必填，否则不传；格式: `^\d{3}-\d{2}-\d{4}$` |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 是 |  |
| `data.card_holder_id` | string | 是 | 平台持卡人ID |

**其他状态码**：`400` 参数错误；`401` 未认证；`403` 权限不足；`404` 资源不存在；`409` 请求重复提交，需轮询结果或等待异步通知；`500` 服务端异常，不能作为交易结果的依据，请稍后查询结果或等待异步通知

<a id="put-v1-cardholder-status-card-holder-id"></a>

### 修改持卡人状态

`PUT /v1/cardholder/status/{card_holder_id}`

启用或禁用持卡人：仅 `APPROVED`（启用）状态可禁用，仅 `DISABLED`（禁用）状态可启用。

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `card_holder_id` | path | string | 是 | 平台持卡人ID（添加持卡人接口返回） |

**请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `status` | string | 是 | 目标状态：APPROVED=启用，DISABLED=禁用。启用状态才能禁用，禁用状态才能启用；可选值: `APPROVED`, `DISABLED` |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 是 |  |
| `data.card_holder_id` | string | 是 | 平台持卡人ID |
| `data.out_card_holder_id` | string | 是 | 商户侧持卡人ID |
| `data.status` | string | 是 | 持卡人状态：PENDING_REVIEW=待审核，APPROVED=已通过（可开卡），REJECTED=已拒绝，SUSPENDED=风控停用，DISABLED=禁用；可选值: `PENDING_REVIEW`, `APPROVED`, `REJECTED`, `SUSPENDED`, `DISABLED` |

**其他状态码**：`400` 参数错误；`401` 未认证；`403` 权限不足；`404` 资源不存在；`409` 请求重复提交，需轮询结果或等待异步通知；`500` 服务端异常，不能作为交易结果的依据，请稍后查询结果或等待异步通知

## 卡交易

授权交易与结算交易查询

<a id="get-v1-card-transactions-authorization"></a>

### 查询卡授权交易

`GET /v1/card/transactions/authorization`

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `page_no` | query | integer | 是 | 页码 |
| `page_size` | query | integer | 是 | 每页条数 |
| `card_id` | query | string | 否 | 卡ID |
| `transaction_id` | query | string | 否 | 交易单号 |
| `start_time` | query | number | 是 | 开始时间 |
| `end_time` | query | number | 是 | 结束时间 |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `count` | string | 否 | 总条数 |
| `data` | object[] | 否 |  |
| `data[].transaction_id` | string | 是 | 交易流水号 |
| `data[].origin_transaction_id` | string | 否 | 原交易单号（授权撤销时返回） |
| `data[].transaction_type` | string | 是 | 授权交易类型；可选值: `AUTH`, `AUTH_REVERSAL` |
| `data[].card_id` | string | 是 | 卡ID |
| `data[].status` | string | 是 | 交易状态，终态才会通知。approved=成功，failed=失败，pending=处理中；可选值: `approved`, `failed`, `pending` |
| `data[].transaction_amount` | string | 是 | 交易金额 |
| `data[].transaction_currency` | string | 是 | 交易币种 |
| `data[].billing_amount` | string | 是 | 账单金额 |
| `data[].billing_currency` | string | 是 | 账单币种 |
| `data[].auth_code` | string | 否 | 授权码 |
| `data[].merchant` | object | 是 | 商家信息 |
| `data[].merchant.merchant_name` | string | 否 | 商家名称 |
| `data[].merchant.merchant_description` | string | 否 | 商家描述 |
| `data[].merchant.country_code` | string | 否 | 商家国家/地区编码，参考 ISO 3166-2 |
| `data[].merchant.mcc` | string | 否 | 商家MCC |
| `data[].otp` | string | 否 | one-time-password |
| `data[].decline_reason` | string | 否 | 拒绝原因 |
| `data[].transaction_time` | number | 是 | 交易时间（Unix 秒） |
| `data[].fee_details` | object[] | 否 | 手续费明细 |
| `data[].fee_details[].amount` | string | 是 | 手续费金额 |
| `data[].fee_details[].currency` | string | 是 | 手续费币种 |
| `data[].fee_details[].type` | string | 是 | 手续费类型；可选值: `smallTransactionFee`, `transactionFee`, `failedFee`, `conversionFee`, `discardFee`, `refundFee`, `crossBorderTransactionFee`, `gatewayFee` |

**其他状态码**：`400` 请求错误

<a id="get-v1-card-transactions-settlement"></a>

### 查询卡结算交易

`GET /v1/card/transactions/settlement`

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `page_no` | query | integer | 是 | 页码 |
| `page_size` | query | integer | 是 | 每页条数 |
| `card_id` | query | string | 否 | 卡ID |
| `transaction_id` | query | string | 否 | 交易单号 |
| `start_time` | query | number | 是 | 开始时间 |
| `end_time` | query | number | 是 | 结束时间 |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `count` | string | 否 | 总条数 |
| `data` | object[] | 否 |  |
| `data[].transaction_id` | string | 是 | 交易流水号 |
| `data[].transaction_type` | string | 是 | 结算交易类型；可选值: `SETTLE`, `REFUND` |
| `data[].origin_transaction_id` | string | 否 | 原结算交易ID，退款时返回 |
| `data[].auth_transaction_id` | string | 否 | 授权单号 |
| `data[].card_id` | string | 是 | 卡ID |
| `data[].status` | string | 是 | 交易状态，终态才会通知。approved=成功，pending=处理中；可选值: `approved`, `pending` |
| `data[].transaction_amount` | string | 是 | 交易金额 |
| `data[].transaction_currency` | string | 是 | 交易币种 |
| `data[].billing_amount` | string | 是 | 账单金额 |
| `data[].billing_currency` | string | 是 | 账单币种 |
| `data[].auth_code` | string | 否 | 授权码 |
| `data[].arn` | string | 否 | 收单方参考号（Acquirer Reference Number），国际银行卡交易清算过程中由收单行生成的全局唯一交易标识符，用于跨机构交易追踪、对账、争议处理和资金结算 |
| `data[].merchant` | object | 是 | 商家信息 |
| `data[].merchant.merchant_name` | string | 否 | 商家名称 |
| `data[].merchant.merchant_description` | string | 否 | 商家描述 |
| `data[].merchant.country_code` | string | 否 | 商家国家/地区编码，参考 ISO 3166-2 |
| `data[].merchant.mcc` | string | 否 | 商家MCC |
| `data[].transaction_time` | number | 是 | 交易时间（Unix 秒） |
| `data[].fee_details` | object[] | 否 | 手续费明细 |
| `data[].fee_details[].amount` | string | 是 | 手续费金额 |
| `data[].fee_details[].currency` | string | 是 | 手续费币种 |
| `data[].fee_details[].type` | string | 是 | 手续费类型；可选值: `smallTransactionFee`, `transactionFee`, `failedFee`, `conversionFee`, `discardFee`, `refundFee`, `crossBorderTransactionFee`, `gatewayFee` |

**其他状态码**：`400` 请求错误

## 收款

数币收款地址

<a id="get-v1-receiving-receiving-address"></a>

### 查询数币收款地址

`GET /v1/receiving/receiving-address`

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object[] | 否 |  |
| `data[].currencies` | string[] | 是 |  |
| `data[].address` | string | 是 |  |
| `data[].chain_type` | string | 是 | 可选值: `ETH`, `TRX` |

**其他状态码**：`400` 请求错误

## 兑换

法币与数币兑换

<a id="post-v1-conversion"></a>

### 兑换申请

`POST /v1/conversion`

目前仅支持数币兑换法币，即卖出数币买入法币

**请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `out_order_no` | string | 是 | 商户订单号；最大长度: `32` |
| `sell_currency` | string | 是 | 最大长度: `10` |
| `sell_amount` | string | 否 | 卖出金额（元） |
| `buy_currency` | string | 是 | 最大长度: `10` |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 否 |  |
| `data.transaction_id` | string | 否 |  |

**其他状态码**：`201` 资源已创建；`400` 请求错误

<a id="get-v1-conversion-out-order-no"></a>

### 查询兑换单详情

`GET /v1/conversion/{out_order_no}`

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `out_order_no` | path | string | 是 | 商户订单号 |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 否 |  |
| `data.out_order_no` | string | 否 |  |
| `data.user_id` | string | 否 | 用户ID |
| `data.transaction_id` | string | 否 |  |
| `data.sell_currency` | string | 否 |  |
| `data.sell_amount` | string | 否 |  |
| `data.buy_currency` | string | 否 |  |
| `data.buy_amount` | string | 否 |  |
| `data.fee_amount` | string | 否 |  |
| `data.fee_currency` | string | 否 |  |
| `data.rate` | string | 否 |  |
| `data.base_currency` | string | 否 |  |
| `data.status` | string | 否 | 可选值: `PROCESSING`, `SUCCESS`, `FAILED` |
| `data.fail_reason` | string | 否 |  |
| `data.success_time` | number | 否 |  |
| `data.create_time` | number | 否 |  |

**其他状态码**：`400` 请求错误

## 转账

主钱包与子账户间资金划转

<a id="post-v1-transfer"></a>

### 转账申请

`POST /v1/transfer`

发起主钱包与子账户之间的资金划转。付款方和收款方必须是「主钱包 + 子账户」的组合；out_order_no 作为幂等键使用，重复请求会返回已有结果。

**请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `out_order_no` | string | 是 | 商户订单号（幂等键）；最大长度: `64`；示例: `M202607200001` |
| `payer_account_no` | string | 是 | 付款方账户号（钱包ID）；示例: `100001` |
| `payee_account_no` | string | 是 | 收款方账户号（钱包ID）；示例: `100002` |
| `amount` | string | 是 | 转账金额，最多2位小数；格式: `^\d{1,12}(\.\d{1,2})?$`；示例: `100.50` |
| `currency` | string | 是 | 币种；最大长度: `10`；示例: `USD` |
| `remark` | string | 否 | 备注（选填）；最大长度: `200` |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `count` | integer | 否 | 总数（本接口固定为null）；可为 null |
| `data` | object | 否 |  |
| `data.out_order_no` | string | 否 | 商户订单号 |
| `data.order_no` | string | 否 | 平台订单号 |
| `data.status` | string | 否 | 转账状态；可选值: `PROCESSING`, `SUCCESS`, `FAIL` |

**其他状态码**：`400` 请求错误；`404` 账户不存在

<a id="get-v1-transfer-order"></a>

### 转账查询

`GET /v1/transfer/order`

查询转账订单详情，out_order_no 与 order_no 二选一传入

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `out_order_no` | query | string | 否 | 商户订单号（与order_no二选一） |
| `order_no` | query | string | 否 | 平台订单号（与out_order_no二选一） |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `count` | integer | 否 | 可为 null |
| `data` | object | 否 |  |
| `data.out_order_no` | string | 是 | 商户订单号 |
| `data.order_no` | string | 否 | 平台订单号 |
| `data.status` | string | 是 | 转账状态；可选值: `PROCESSING`, `SUCCESS`, `FAIL` |
| `data.amount` | string | 是 | 转账金额 |
| `data.currency` | string | 是 | 币种 |
| `data.payer_account_no` | string | 是 | 付款方账户号（钱包ID） |
| `data.payee_account_no` | string | 是 | 收款方账户号（钱包ID） |
| `data.fail_reason` | string | 否 | 失败原因（状态为FAIL时返回） |
| `data.success_time` | number | 否 | 成功时间（Unix毫秒，状态为SUCCESS时返回） |
| `data.create_time` | number | 是 | 创建时间（Unix毫秒） |

**其他状态码**：`400` 请求错误；`404` 订单不存在

## 公共接口

文件上传等通用能力

<a id="post-v1-file-upload"></a>

### 上传文件

`POST /v1/file/upload`

上传文件并返回文件ID，目前用于持卡人证件照片。

签名说明：请求体为 `multipart/form-data`，文件内容不参与签名，`REQUEST_PAYLOAD` 按空处理；表单中只放 `file` 一个字段，业务参数 `biz_type` 必须放在 URL 查询字符串中随 `QUERY_STRING` 参与签名。

签名 payload 示例：`POST&/portal/api/v1/file/upload&1766123535&&biz_type%3DCARDHOLDER_ID_PHOTO`

**请求参数**

| 参数 | 位置 | 类型 | 必填 | 说明 |
|---|---|---|---|---|
| `biz_type` | query | string | 是 | 业务类型：CARDHOLDER_ID_PHOTO=持卡人证件；可选值: `CARDHOLDER_ID_PHOTO` |

**请求体**（`multipart/form-data`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `file` | string(binary) | 是 | 文件，最大不超过 7MB |

**成功响应（200）**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `data` | object | 是 |  |
| `data.file_id` | string | 是 | 文件ID |

**其他状态码**：`400` 参数错误；`401` 未认证；`403` 权限不足；`404` 资源不存在；`409` 请求重复提交，需轮询结果或等待异步通知；`500` 服务端异常，不能作为交易结果的依据，请稍后查询结果或等待异步通知

## Webhook

异步通知回调

<a id="post-webhook-card-apply-result"></a>

### 申卡结果通知

`POST /webhook/card-apply-result`

申卡处理完成后，MarsWallex 会向商户配置的回调地址发送通知

**通知请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `topic` | string | 是 | 事件主题；可选值: `CARD_APPLY_RESULT`；示例: `CARD_APPLY_RESULT` |
| `message_id` | string | 是 | 唯一消息ID，用于幂等去重；示例: `a1b2c3d4-e5f6-7890-abcd-ef1234567890` |
| `data` | object | 是 | 事件数据 |
| `data.card_id` | string | 否 | 卡ID |
| `data.out_order_no` | string | 否 | 商家订单号 |
| `data.transaction_id` | string | 否 | 平台流水号 |
| `data.status` | string | 否 | 申卡结果；可选值: `SUCCESS`, `FAILED` |
| `data.failure_reason` | string | 否 | 失败原因（status 为 FAILED 时返回） |

**商户响应**：商户返回 200 表示已接收

<a id="post-webhook-card-status-change"></a>

### 卡状态变更通知

`POST /webhook/card-status-change`

卡状态发生变更时触发（如激活、冻结、注销）

**通知请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `topic` | string | 是 | 事件主题；可选值: `CARD_STATUS_CHANGED`；示例: `CARD_STATUS_CHANGED` |
| `message_id` | string | 是 | 唯一消息ID，用于幂等去重；示例: `a1b2c3d4-e5f6-7890-abcd-ef1234567890` |
| `data` | object | 是 | 事件数据 |
| `data.card_id` | string | 否 | 卡ID |
| `data.card_status` | string | 否 | 卡的最新状态；可选值: `ACTIVE`, `REVOKED`, `CANCELED` |

**商户响应**：已接收

<a id="post-webhook-cardholder-status-change"></a>

### 持卡人状态变更通知

`POST /webhook/cardholder-status-change`

持卡人审核结果或状态发生变更时触发

**通知请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `topic` | string | 是 | 事件主题；可选值: `CARDHOLDER_STATUS_CHANGE`；示例: `CARDHOLDER_STATUS_CHANGE` |
| `message_id` | string | 是 | 唯一消息ID，用于幂等去重 |
| `data` | object | 是 | 事件数据 |
| `data.card_holder_id` | string | 是 | 平台持卡人ID |
| `data.out_card_holder_id` | string | 是 | 商户侧持卡人ID |
| `data.status` | string | 是 | 持卡人状态：PENDING_REVIEW=待审核，APPROVED=已通过（可开卡），REJECTED=已拒绝，SUSPENDED=风控停用，DISABLED=禁用；可选值: `PENDING_REVIEW`, `APPROVED`, `REJECTED`, `SUSPENDED`, `DISABLED` |
| `data.remark` | string | 否 | 备注，驳回时为驳回原因 |
| `data.update_time` | integer(int64) | 否 | 变更时间（Unix 秒） |

**商户响应**：已接收

<a id="post-webhook-account-status-change"></a>

### 账户状态变更通知

`POST /webhook/account-status-change`

子账户/共享账户状态发生变更（启用或停用）时触发

**通知请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `topic` | string | 是 | 事件主题；可选值: `ACCOUNT_STATUS_CHANGED`；示例: `ACCOUNT_STATUS_CHANGED` |
| `message_id` | string | 是 | 唯一消息ID，用于幂等去重；示例: `a1b2c3d4-e5f6-7890-abcd-ef1234567890` |
| `data` | object | 是 | 事件数据 |
| `data.account_no` | string | 否 | 账户号（钱包ID） |
| `data.out_account_no` | string | 否 | 商户侧账户号。API 创建的账户为创建时传入的值 |
| `data.account_status` | string | 否 | 账户最新状态；可选值: `ACTIVE`, `DISABLED` |
| `data.account_type` | string | 否 | 账户类型：MAIN=主钱包，SUB_ACCOUNT=子账户；可选值: `MAIN`, `SUB_ACCOUNT` |

**商户响应**：已接收

<a id="post-webhook-auth-transaction"></a>

### 授权交易通知

`POST /webhook/auth-transaction`

卡产生授权交易（通过或拒绝）时触发

**通知请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `topic` | string | 是 | 事件主题；可选值: `AUTH_EVENT`；示例: `AUTH_EVENT` |
| `message_id` | string | 是 | 唯一消息ID，用于幂等去重；示例: `a1b2c3d4-e5f6-7890-abcd-ef1234567890` |
| `data` | object | 是 | 授权交易详情 |
| `data.transaction_id` | string | 是 | 交易单号 |
| `data.origin_transaction_id` | string | 否 | 原交易单号（授权撤销时返回） |
| `data.transaction_type` | string | 是 | 授权交易类型；可选值: `AUTH`, `AUTH_REVERSAL` |
| `data.card_id` | string | 是 | 卡ID |
| `data.status` | string | 是 | 交易状态，终态才会通知。approved=成功，failed=失败，pending=处理中；可选值: `approved`, `failed`, `pending` |
| `data.transaction_amount` | string | 是 | 交易金额 |
| `data.transaction_currency` | string | 是 | 交易币种 |
| `data.billing_amount` | string | 是 | 账单金额 |
| `data.billing_currency` | string | 是 | 账单币种 |
| `data.auth_code` | string | 否 | 授权码 |
| `data.merchant` | object | 是 | 商家信息 |
| `data.merchant.merchant_name` | string | 否 | 商家名称 |
| `data.merchant.merchant_description` | string | 否 | 商家描述 |
| `data.merchant.country_code` | string | 否 | 商家国家/地区编码，参考 ISO 3166-2 |
| `data.merchant.mcc` | string | 否 | 商家MCC |
| `data.otp` | string | 否 | one-time-password |
| `data.decline_reason` | string | 否 | 拒绝原因 |
| `data.transaction_time` | number | 是 | 交易时间（Unix 秒） |

**商户响应**：已接收

<a id="post-webhook-settlement-transaction"></a>

### 结算交易通知

`POST /webhook/settlement-transaction`

卡结算或退款交易处理完成时触发

**通知请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `topic` | string | 是 | 事件主题；可选值: `SETTLE_EVENT`；示例: `SETTLE_EVENT` |
| `message_id` | string | 是 | 唯一消息ID，用于幂等去重；示例: `a1b2c3d4-e5f6-7890-abcd-ef1234567890` |
| `data` | object | 是 | 结算交易详情 |
| `data.transaction_id` | string | 是 | 交易ID |
| `data.transaction_type` | string | 是 | 结算交易类型；可选值: `SETTLE`, `REFUND` |
| `data.origin_transaction_id` | string | 否 | 原结算交易ID，退款时返回 |
| `data.auth_transaction_id` | string | 否 | 授权单号 |
| `data.card_id` | string | 是 | 卡ID |
| `data.status` | string | 是 | 交易状态，终态才会通知。approved=成功，pending=处理中；可选值: `approved`, `pending` |
| `data.transaction_amount` | string | 是 | 交易金额 |
| `data.transaction_currency` | string | 是 | 交易币种 |
| `data.billing_amount` | string | 是 | 账单金额 |
| `data.billing_currency` | string | 是 | 账单币种 |
| `data.auth_code` | string | 否 | 授权码 |
| `data.arn` | string | 否 | 收单方参考号（Acquirer Reference Number），国际银行卡交易清算过程中由收单行生成的全局唯一交易标识符，用于跨机构交易追踪、对账、争议处理和资金结算 |
| `data.merchant` | object | 否 | 商家信息 |
| `data.merchant.merchant_name` | string | 否 | 商家名称 |
| `data.merchant.merchant_description` | string | 否 | 商家描述 |
| `data.merchant.country_code` | string | 否 | 商家国家/地区编码，参考 ISO 3166-2 |
| `data.merchant.mcc` | string | 否 | 商家MCC |
| `data.transaction_time` | number | 是 | 交易时间（Unix 秒） |

**商户响应**：已接收

<a id="post-webhook-card-transfer-result"></a>

### 卡转入转出结果通知

`POST /webhook/card-transfer-result`

卡充值或提现交易完成时触发

**通知请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `topic` | string | 是 | 事件主题；可选值: `CARD_TRANSFER_RESULT`；示例: `CARD_TRANSFER_RESULT` |
| `message_id` | string | 是 | 唯一消息ID，用于幂等去重；示例: `a1b2c3d4-e5f6-7890-abcd-ef1234567890` |
| `data` | object | 是 | 卡转账交易详情 |
| `data.out_order_no` | string | 是 | 商家订单号 |
| `data.transaction_id` | string | 是 | 平台订单号 |
| `data.card_id` | string | 是 |  |
| `data.currency` | string | 否 |  |
| `data.amount` | string | 否 | 金额（元） |
| `data.type` | string | 否 | CHARGE=转入, WITHDRAW=转出；可选值: `CHARGE`, `WITHDRAW` |
| `data.status` | string | 是 | 可选值: `APPLY`, `SUCCESS`, `FAIL` |
| `data.create_time` | number | 否 |  |

**商户响应**：已接收

<a id="post-webhook-conversion-result"></a>

### 兑换结果通知

`POST /webhook/conversion-result`

兑换订单完成或失败时触发

**通知请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `topic` | string | 是 | 事件主题；可选值: `CONVERSION_RESULT`；示例: `CONVERSION_RESULT` |
| `message_id` | string | 是 | 唯一消息ID，用于幂等去重；示例: `a1b2c3d4-e5f6-7890-abcd-ef1234567890` |
| `data` | object | 是 | 兑换订单详情 |
| `data.out_order_no` | string | 否 |  |
| `data.user_id` | string | 否 | 用户ID |
| `data.transaction_id` | string | 否 |  |
| `data.sell_currency` | string | 否 |  |
| `data.sell_amount` | string | 否 |  |
| `data.buy_currency` | string | 否 |  |
| `data.buy_amount` | string | 否 |  |
| `data.fee_amount` | string | 否 |  |
| `data.fee_currency` | string | 否 |  |
| `data.rate` | string | 否 |  |
| `data.base_currency` | string | 否 |  |
| `data.status` | string | 否 | 可选值: `PROCESSING`, `SUCCESS`, `FAILED` |
| `data.fail_reason` | string | 否 |  |
| `data.success_time` | number | 否 |  |
| `data.create_time` | number | 否 |  |

**商户响应**：已接收

<a id="post-webhook-transfer-result"></a>

### 转账结果通知

`POST /webhook/transfer-result`

OpenAPI转账订单到达终态时触发（仅tradeSource=API的转账）

**通知请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `topic` | string | 是 | 事件主题；可选值: `TRANSFER_RESULT`；示例: `TRANSFER_RESULT` |
| `message_id` | string | 是 | 唯一消息ID，用于幂等去重；示例: `msg_1234567890` |
| `data` | object | 是 | 转账订单详情 |
| `data.out_order_no` | string | 是 | 商户订单号 |
| `data.order_no` | string | 否 | 平台订单号 |
| `data.status` | string | 是 | 转账状态；可选值: `PROCESSING`, `SUCCESS`, `FAIL` |
| `data.amount` | string | 是 | 转账金额 |
| `data.currency` | string | 是 | 币种 |
| `data.payer_account_no` | string | 是 | 付款方账户号（钱包ID） |
| `data.payee_account_no` | string | 是 | 收款方账户号（钱包ID） |
| `data.fail_reason` | string | 否 | 失败原因（状态为FAIL时返回） |
| `data.success_time` | number | 否 | 成功时间（Unix毫秒，状态为SUCCESS时返回） |
| `data.create_time` | number | 是 | 创建时间（Unix毫秒） |

**商户响应**：已接收

<a id="post-webhook-card-3ds-otp"></a>

### 卡3DS OTP通知

`POST /webhook/card-3ds-otp`

卡发生3DS验证时，向商户推送OTP验证码

**通知请求体**（`application/json`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `topic` | string | 是 | 事件主题；可选值: `CARD_3DS_OTP`；示例: `CARD_3DS_OTP` |
| `message_id` | string | 是 | 唯一消息ID，用于幂等去重 |
| `data` | object | 是 | 字段类型为string字符串 |
| `data.card_id` | string | 是 | 卡ID |
| `data.card_no` | string | 是 | 卡号脱敏 |
| `data.otp` | string | 是 | 3DS 验证码；最大长度: `32` |
| `data.transaction_currency` | string | 否 | 交易币种 |
| `data.transaction_amount` | string | 否 | 交易金额 |
| `data.merchant_name` | string | 否 | 商户名称 |

**商户响应**：已接收

## 数据模型

各接口引用的公共模型（上文接口里已展开，这里便于查阅）。

### BalanceItem

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `currency` | string | 是 | 币种 (ISO 4217 / USDT) |
| `available_balance` | string | 是 | 可用余额（元） |
| `freeze_balance` | string | 是 | 冻结余额（元） |

### Account

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `account_no` | string | 是 | 账户号 |
| `account_name` | string | 是 | 账户名称 |
| `account_status` | string | 是 | 账户状态；可选值: `ACTIVE`, `DISABLED`, `INITIALIZING`, `FAILED`, `REJECTED` |
| `create_time` | number | 是 | 创建时间（Unix 秒） |
| `balance_list` | object[] | 否 | 余额列表 |
| `balance_list[].currency` | string | 是 | 币种 (ISO 4217 / USDT) |
| `balance_list[].available_balance` | string | 是 | 可用余额（元） |
| `balance_list[].freeze_balance` | string | 是 | 冻结余额（元） |
| `account_type` | string | 是 | 账户类型：MAIN=主钱包，SUB_ACCOUNT=子账户；可选值: `MAIN`, `SUB_ACCOUNT` |
| `purpose` | string | 否 | 用途：SHARED_ACCOUNT=共享账户，null 或不返回=普通账户；可选值: `SHARED_ACCOUNT`；可为 null |

### CardProduct

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `product_id` | string | 是 | 卡产品ID |
| `product_name` | string | 是 | 卡产品名称 |
| `card_label` | string | 是 | 卡组（如 VISA） |
| `card_country` | string | 是 | 卡所属国家 (ISO 3166-2) |
| `card_mode` | string | 是 | NORMAL=储值卡, SHARE=共享卡；可选值: `NORMAL`, `SHARE` |
| `card_currency` | string | 是 | 卡币种 (ISO 4217) |
| `supported_fee_deduct_types` | string[] | 否 | 卡产品支持的手续费扣收方式：CARD_ACCOUNT=卡扣费，WALLET=钱包扣费 |

### CardDetail

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `card_id` | string | 是 |  |
| `card_no` | string | 是 | 卡号 |
| `card_name` | string | 是 |  |
| `card_type` | string | 否 | 卡类型（虚拟卡） |
| `card_mode` | string | 否 |  |
| `card_label` | string | 否 |  |
| `card_country` | string | 否 |  |
| `card_currency` | string | 否 |  |
| `expiry_date` | string | 否 | MMyy |
| `status` | string | 是 | 可选值: `ACTIVE`, `REVOKED`, `CANCELED`, `INCANCELED` |
| `available_balance` | string | 否 |  |
| `freeze_balance` | string | 否 |  |
| `remark` | string | 否 | 备注 |
| `create_time` | number | 否 |  |
| `limit` | object | 否 | 交易限额 |
| `limit.single_limit` | string | 否 | 单笔限额 |

### CardSensitive

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `card_id` | string | 是 |  |
| `card_no` | string | 是 |  |
| `expiry_date` | string | 否 | MMyy |
| `cvv2` | string | 否 |  |
| `freeze_balance` | string | 否 |  |

### CardBalanceTransaction

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `out_order_no` | string | 是 | 商家订单号 |
| `transaction_id` | string | 是 | 平台订单号 |
| `card_id` | string | 是 |  |
| `currency` | string | 否 |  |
| `amount` | string | 否 | 金额（元） |
| `type` | string | 否 | CHARGE=转入, WITHDRAW=转出；可选值: `CHARGE`, `WITHDRAW` |
| `status` | string | 是 | 可选值: `APPLY`, `SUCCESS`, `FAIL` |
| `create_time` | number | 否 |  |

### AuthTransaction

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `transaction_id` | string | 是 | 交易流水号 |
| `origin_transaction_id` | string | 否 | 原交易单号（授权撤销时返回） |
| `transaction_type` | string | 是 | 授权交易类型；可选值: `AUTH`, `AUTH_REVERSAL` |
| `card_id` | string | 是 | 卡ID |
| `status` | string | 是 | 交易状态，终态才会通知。approved=成功，failed=失败，pending=处理中；可选值: `approved`, `failed`, `pending` |
| `transaction_amount` | string | 是 | 交易金额 |
| `transaction_currency` | string | 是 | 交易币种 |
| `billing_amount` | string | 是 | 账单金额 |
| `billing_currency` | string | 是 | 账单币种 |
| `auth_code` | string | 否 | 授权码 |
| `merchant` | object | 是 | 商家信息 |
| `merchant.merchant_name` | string | 否 | 商家名称 |
| `merchant.merchant_description` | string | 否 | 商家描述 |
| `merchant.country_code` | string | 否 | 商家国家/地区编码，参考 ISO 3166-2 |
| `merchant.mcc` | string | 否 | 商家MCC |
| `otp` | string | 否 | one-time-password |
| `decline_reason` | string | 否 | 拒绝原因 |
| `transaction_time` | number | 是 | 交易时间（Unix 秒） |
| `fee_details` | object[] | 否 | 手续费明细 |
| `fee_details[].amount` | string | 是 | 手续费金额 |
| `fee_details[].currency` | string | 是 | 手续费币种 |
| `fee_details[].type` | string | 是 | 手续费类型；可选值: `smallTransactionFee`, `transactionFee`, `failedFee`, `conversionFee`, `discardFee`, `refundFee`, `crossBorderTransactionFee`, `gatewayFee` |

### SettlementTransaction

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `transaction_id` | string | 是 | 交易流水号 |
| `transaction_type` | string | 是 | 结算交易类型；可选值: `SETTLE`, `REFUND` |
| `origin_transaction_id` | string | 否 | 原结算交易ID，退款时返回 |
| `auth_transaction_id` | string | 否 | 授权单号 |
| `card_id` | string | 是 | 卡ID |
| `status` | string | 是 | 交易状态，终态才会通知。approved=成功，pending=处理中；可选值: `approved`, `pending` |
| `transaction_amount` | string | 是 | 交易金额 |
| `transaction_currency` | string | 是 | 交易币种 |
| `billing_amount` | string | 是 | 账单金额 |
| `billing_currency` | string | 是 | 账单币种 |
| `auth_code` | string | 否 | 授权码 |
| `arn` | string | 否 | 收单方参考号（Acquirer Reference Number），国际银行卡交易清算过程中由收单行生成的全局唯一交易标识符，用于跨机构交易追踪、对账、争议处理和资金结算 |
| `merchant` | object | 是 | 商家信息 |
| `merchant.merchant_name` | string | 否 | 商家名称 |
| `merchant.merchant_description` | string | 否 | 商家描述 |
| `merchant.country_code` | string | 否 | 商家国家/地区编码，参考 ISO 3166-2 |
| `merchant.mcc` | string | 否 | 商家MCC |
| `transaction_time` | number | 是 | 交易时间（Unix 秒） |
| `fee_details` | object[] | 否 | 手续费明细 |
| `fee_details[].amount` | string | 是 | 手续费金额 |
| `fee_details[].currency` | string | 是 | 手续费币种 |
| `fee_details[].type` | string | 是 | 手续费类型；可选值: `smallTransactionFee`, `transactionFee`, `failedFee`, `conversionFee`, `discardFee`, `refundFee`, `crossBorderTransactionFee`, `gatewayFee` |

### AuthTransactionWebhook

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `transaction_id` | string | 是 | 交易单号 |
| `origin_transaction_id` | string | 否 | 原交易单号（授权撤销时返回） |
| `transaction_type` | string | 是 | 授权交易类型；可选值: `AUTH`, `AUTH_REVERSAL` |
| `card_id` | string | 是 | 卡ID |
| `status` | string | 是 | 交易状态，终态才会通知。approved=成功，failed=失败，pending=处理中；可选值: `approved`, `failed`, `pending` |
| `transaction_amount` | string | 是 | 交易金额 |
| `transaction_currency` | string | 是 | 交易币种 |
| `billing_amount` | string | 是 | 账单金额 |
| `billing_currency` | string | 是 | 账单币种 |
| `auth_code` | string | 否 | 授权码 |
| `merchant` | object | 是 | 商家信息 |
| `merchant.merchant_name` | string | 否 | 商家名称 |
| `merchant.merchant_description` | string | 否 | 商家描述 |
| `merchant.country_code` | string | 否 | 商家国家/地区编码，参考 ISO 3166-2 |
| `merchant.mcc` | string | 否 | 商家MCC |
| `otp` | string | 否 | one-time-password |
| `decline_reason` | string | 否 | 拒绝原因 |
| `transaction_time` | number | 是 | 交易时间（Unix 秒） |

### SettlementTransactionWebhook

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `transaction_id` | string | 是 | 交易ID |
| `transaction_type` | string | 是 | 结算交易类型；可选值: `SETTLE`, `REFUND` |
| `origin_transaction_id` | string | 否 | 原结算交易ID，退款时返回 |
| `auth_transaction_id` | string | 否 | 授权单号 |
| `card_id` | string | 是 | 卡ID |
| `status` | string | 是 | 交易状态，终态才会通知。approved=成功，pending=处理中；可选值: `approved`, `pending` |
| `transaction_amount` | string | 是 | 交易金额 |
| `transaction_currency` | string | 是 | 交易币种 |
| `billing_amount` | string | 是 | 账单金额 |
| `billing_currency` | string | 是 | 账单币种 |
| `auth_code` | string | 否 | 授权码 |
| `arn` | string | 否 | 收单方参考号（Acquirer Reference Number），国际银行卡交易清算过程中由收单行生成的全局唯一交易标识符，用于跨机构交易追踪、对账、争议处理和资金结算 |
| `merchant` | object | 否 | 商家信息 |
| `merchant.merchant_name` | string | 否 | 商家名称 |
| `merchant.merchant_description` | string | 否 | 商家描述 |
| `merchant.country_code` | string | 否 | 商家国家/地区编码，参考 ISO 3166-2 |
| `merchant.mcc` | string | 否 | 商家MCC |
| `transaction_time` | number | 是 | 交易时间（Unix 秒） |

### ReceivingAddress

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `currencies` | string[] | 是 |  |
| `address` | string | 是 |  |
| `chain_type` | string | 是 | 可选值: `ETH`, `TRX` |

### ConversionDetail

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `out_order_no` | string | 否 |  |
| `user_id` | string | 否 | 用户ID |
| `transaction_id` | string | 否 |  |
| `sell_currency` | string | 否 |  |
| `sell_amount` | string | 否 |  |
| `buy_currency` | string | 否 |  |
| `buy_amount` | string | 否 |  |
| `fee_amount` | string | 否 |  |
| `fee_currency` | string | 否 |  |
| `rate` | string | 否 |  |
| `base_currency` | string | 否 |  |
| `status` | string | 否 | 可选值: `PROCESSING`, `SUCCESS`, `FAILED` |
| `fail_reason` | string | 否 |  |
| `success_time` | number | 否 |  |
| `create_time` | number | 否 |  |

### TransferDetail

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `out_order_no` | string | 是 | 商户订单号 |
| `order_no` | string | 否 | 平台订单号 |
| `status` | string | 是 | 转账状态；可选值: `PROCESSING`, `SUCCESS`, `FAIL` |
| `amount` | string | 是 | 转账金额 |
| `currency` | string | 是 | 币种 |
| `payer_account_no` | string | 是 | 付款方账户号（钱包ID） |
| `payee_account_no` | string | 是 | 收款方账户号（钱包ID） |
| `fail_reason` | string | 否 | 失败原因（状态为FAIL时返回） |
| `success_time` | number | 否 | 成功时间（Unix毫秒，状态为SUCCESS时返回） |
| `create_time` | number | 是 | 创建时间（Unix毫秒） |

### SubAccount

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `account_no` | string | 是 | 账户 ID |
| `account_name` | string | 是 | 账户别名 |
| `out_account_no` | string | 是 | 商户侧账户号 |
| `purpose` | string | 否 | 用途：SHARED_ACCOUNT=共享账户，null=普通子账户；可选值: `SHARED_ACCOUNT`；可为 null |
| `status` | string | 是 | 账户状态：ACTIVE=启用，DISABLED=停用；可选值: `ACTIVE`, `DISABLED` |
| `create_time` | number | 是 | 创建时间（Unix 秒） |

### Address

地址

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `country` | string | 是 | 国家，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `state` | string | 是 | 省 / 州；最大长度: `64` |
| `city` | string | 是 | 城市；最大长度: `64` |
| `address` | string | 是 | 详细地址；最大长度: `256` |
| `post_code` | string | 是 | 邮编；最大长度: `10` |

### IdType

证件类型：PASSPORT=护照，IDCARD=身份证，OTHER_CERTIFICATE=其他

可选值：`PASSPORT`, `IDCARD`, `OTHER_CERTIFICATE`

### CardholderStatus

持卡人状态：PENDING_REVIEW=待审核，APPROVED=已通过（可开卡），REJECTED=已拒绝，SUSPENDED=风控停用，DISABLED=禁用

可选值：`PENDING_REVIEW`, `APPROVED`, `REJECTED`, `SUSPENDED`, `DISABLED`

### CardholderDetail

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `card_holder_id` | string | 是 | 平台持卡人ID |
| `out_card_holder_id` | string | 是 | 商户侧持卡人ID |
| `first_name` | string | 是 | 名（英文，仅 ASCII 字符）；最大长度: `32` |
| `last_name` | string | 是 | 姓（英文，仅 ASCII 字符）；最大长度: `32` |
| `chinese_first_name` | string | 否 | 名（中文），选填；最大长度: `32` |
| `chinese_last_name` | string | 否 | 姓（中文），选填；最大长度: `32` |
| `date_of_birth` | string(date) | 是 | 出生日期，YYYY-MM-DD |
| `zone_number` | string | 是 | 手机国家区号，如 1、86；最大长度: `10` |
| `mobile` | string | 是 | 手机号；最大长度: `50` |
| `email` | string(email) | 是 | 邮箱；最大长度: `100` |
| `country_code` | string | 是 | 国家/地区，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `id_type` | string | 是 | 证件类型：PASSPORT=护照，IDCARD=身份证，OTHER_CERTIFICATE=其他；可选值: `PASSPORT`, `IDCARD`, `OTHER_CERTIFICATE` |
| `id_no` | string | 是 | 证件号；最大长度: `64` |
| `id_period_begin` | string(date) | 是 | 证件有效期起始日，YYYY-MM-DD，不得晚于提交日期，且不得晚于截止日 |
| `id_period_end` | string(date) | 是 | 证件有效期截止日，YYYY-MM-DD，须晚于提交日期；长期有效请填写 9999-12-31 |
| `id_photo_file_ids` | string[] | 是 | 证件照片文件ID（由上传文件接口返回），最多 2 个；最多项数: `2` |
| `residential_address` | object | 是 | 居住地址 |
| `residential_address.country` | string | 是 | 国家，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `residential_address.state` | string | 是 | 省 / 州；最大长度: `64` |
| `residential_address.city` | string | 是 | 城市；最大长度: `64` |
| `residential_address.address` | string | 是 | 详细地址；最大长度: `256` |
| `residential_address.post_code` | string | 是 | 邮编；最大长度: `10` |
| `billing_address` | object | 是 | 账单地址 |
| `billing_address.country` | string | 是 | 国家，ISO 3166-1 alpha-2 两位码；最大长度: `2`；最小长度: `2` |
| `billing_address.state` | string | 是 | 省 / 州；最大长度: `64` |
| `billing_address.city` | string | 是 | 城市；最大长度: `64` |
| `billing_address.address` | string | 是 | 详细地址；最大长度: `256` |
| `billing_address.post_code` | string | 是 | 邮编；最大长度: `10` |
| `ssn` | string | 否 | 美国社会安全号码，国家/地区为 US 时必填，否则不传；格式: `^\d{3}-\d{2}-\d{4}$` |
| `status` | string | 是 | 持卡人状态：PENDING_REVIEW=待审核，APPROVED=已通过（可开卡），REJECTED=已拒绝，SUSPENDED=风控停用，DISABLED=禁用；可选值: `PENDING_REVIEW`, `APPROVED`, `REJECTED`, `SUSPENDED`, `DISABLED` |
| `remark` | string | 否 | 备注，驳回时为驳回原因 |

## 附录：中英文规范字段差异

中英文规范的接口和字段一致，仅说明文字详略不同。
