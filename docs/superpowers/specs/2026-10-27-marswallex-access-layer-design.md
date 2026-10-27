# MarsWallex 接入层设计（子项目 1）

- 日期：2026-10-27
- 状态：已确认设计，待写实现计划
- 参考：`docs/marswallex-openapi-zh.md`（MarsWallex 默认卡台 API 文档）

## 1. 背景与目标

本系统目前是 ZovoCard 的「兑换壳」：发码、兑换（由 ZovoCard 用自己的虚拟卡代付 ChatGPT / Grok / X Premium / Claude 订阅）、对账都依赖 ZovoCard。用户决定**完全替代 ZovoCard**，改用 MarsWallex。MarsWallex 只提供虚拟卡与资金能力（开卡、充值、交易、3DS OTP），没有 CDK 和订阅代付，因此替代工作拆成 5 个子项目，依次推进，每个独立走设计 → 计划 → 实现：

| # | 子项目 | 内容 | 依赖 |
|---|---|---|---|
| 1 | **MarsWallex 接入层（本文）** | 客户端、签名验签、全部接口、Webhook 接收、后台配置与连接测试 | 无 |
| 2 | 卡池与资金 | 持卡人、开卡、卡转入转出、冻结注销、交易与 3DS OTP 落库、选卡与健康度规则 | 1 |
| 3 | 自有 CDK 体系 | 本地生成和管理 CDK、套餐注册表与定价、CDK 状态机 | 无 |
| 4 | 兑换引擎 | 预览、预检、选卡注资、生成结账链接、自动付款、3DS、结果与时间线；先做 GPT | 1、2、3 |
| 5 | 对账与下线 | 资金对账、旧码兼容、前端改造、下线 ZovoCard | 全部 |

子项目 1 完成后，先用一张 MarsWallex 真卡做一次订阅付款的可行性验证，再决定是否继续 2–5。

**本子项目目标**：让后续子项目可以安全、可靠地调用 MarsWallex 全部接口并接收 Webhook，且任何调用结果都能明确区分「成功 / 被拒 / 未知 / 未发出」，不会把结果不明的资金请求误判为失败。

## 2. 约束与已确认的决定

| 事项 | 决定 |
|---|---|
| 凭证情况 | 只有**生产环境**凭证，没有测试环境。联调只调只读接口，资金类接口全部用模拟服务器测试 |
| 密钥存储 | apiToken 和我们的 RSA 私钥用主密钥 AES-256-GCM 加密后存数据库 |
| 实现方案 | 新建独立包，与现有 ZovoCard 代码（`internal/cardplatform` 等）完全隔离，期间两者并存；ZovoCard 代码在子项目 5 统一删除 |
| 规范缺陷 | 按规范原样实现，代码注释标明，联调发现不符再改（见 §6） |

## 3. 整体结构

| 单元 | 职责 | 依赖 |
|---|---|---|
| `internal/secretbox` | 用环境变量 `APP_MASTER_KEY` 做 AES-256-GCM 加解密；以配置项名作附加认证数据（AAD），密文不能挪用到别的配置项 | 标准库 |
| `internal/marswallex` | 签名与验签、HTTP 传输、结果分类、27 个接口的类型化方法、金额与时间类型、Webhook 验签与解析。不访问数据库 | 标准库 |
| 配置与 Webhook 存储（`internal/db` 内新增函数） | 读写加解密后的配置；`marswallex_webhook_events` 表 | `db`、`secretbox` |
| `internal/handler` + 路由 | 后台配置、测试连接、Webhook 列表；公开 Webhook 接收 | 以上 |
| 前端 `frontend/src/views/admin/MarsWallexIntegration.vue` | `/ops` 下「MarsWallex 对接」页 | 后台接口 |

数据流：业务代码 → `marswallex.Client` 方法 → 签名、发送、响应验签 → 返回结果与分类。Webhook → 验签 → 按消息 ID 去重入库 → 200；业务处理留给子项目 2。

**不在本子项目范围**：卡、持卡人等业务表；Webhook 业务处理；卡管理界面；删除 ZovoCard 代码；主密钥轮换。

## 4. 配置与密钥存储

### 4.1 配置项（`site_settings`）

| 键 | 存储 | 说明 |
|---|---|---|
| `marswallex_base_url` | 明文 | 预设：生产 `https://api.feibeiglobal.com/portal/api`；测试 `https://api.feibeiglobal-test.com/portal/api`（未经确认） |
| `marswallex_developer_id` | 明文 | Basic 认证用户名 |
| `marswallex_api_token` | 加密 | Basic 认证密码 |
| `marswallex_private_key` | 加密 | 我们的 RSA 私钥 PEM，支持 PKCS#8 与 PKCS#1 |
| `marswallex_platform_public_key` | 明文 | MarsWallex 平台公钥 PEM（PKIX），验响应与 Webhook 签名 |

### 4.2 加密格式与主密钥

- 密文格式：`v1:` + base64(12 字节随机数 ‖ GCM 密文)。AAD = 配置项键名。版本、键名或主密钥不符时解密报错，绝不返回错误明文。
- `APP_MASTER_KEY`：环境变量，base64 编码的 32 字节。缺失或长度不对时：后台拒绝保存密钥类配置；客户端构造失败，调用归为「未发出」。**不提供默认值**。
- `deploy/docker-deploy.sh` 首次部署时生成 `$INSTALL_DIR/.master_key`（权限 600，方式同现有 `.jwt_secret`）并写入 `.env` 的 `APP_MASTER_KEY`；`deploy/app.env.example` 补充说明。
- 主密钥丢失则已存密钥无法解密，需在后台重新录入；文档写明。

### 4.3 后台读写规则

- 密钥字段只写不读：读出时只返回「是否已配置」；提交空字符串表示不修改（与现有 `card_api_key` 一致）。
- 保存私钥前校验可解析，并推导我们的公钥，返回其 SHA-256 指纹与 PEM，供与交给 MarsWallex 的公钥核对。
- 保存平台公钥前校验可解析，返回指纹。
- 每次修改写审计日志（复用 `auditAdmin`），日志不含密钥内容。

## 5. 签名、传输与结果分类

### 5.1 请求签名

- 签名原文：
  - 无查询串：`METHOD&URI&t&BODY`
  - 无请求体：`METHOD&URI&t&&QUERY`
  - 两者皆无：`METHOD&URI&t&`
- `URI` 为请求 URL 的完整路径，含 `/portal/api` 前缀（如 `/portal/api/v1/accounts/page`）。
- `BODY` 为实际发送的 JSON 字节，签名与发送使用同一份字节。
- `QUERY`：客户端按固定顺序拼出查询串（如 `a=1&b=2`，值按 URL 查询规则编码），实际请求发送该查询串，签名时再对整串做一次 URL 编码（`a%3D1%26b%3D2`）。多参数编码细节规范未写清，以「测试连接」的两参数请求实测确认（§9 待确认项 2）。
- 文件上传：请求体 `multipart/form-data` 只含 `file` 字段，不参与签名；`biz_type` 放查询串参与签名（原文形如 `POST&/portal/api/v1/file/upload&t&&biz_type%3DCARDHOLDER_ID_PHOTO`）。
- 算法：SHA256withRSA（RSASSA-PKCS1-v1_5），`t` 为 Unix 秒。
- 请求头：`Authorization: Basic base64(developerId:apiToken)`、`F-Signature: t={t},v={base64签名}`、有请求体时 `Content-Type`。

### 5.2 响应验签

签名原文 `t&响应体原文`，`t` 取响应头 `F-Signature` 中的原值拼接（秒或毫秒都不换算），用平台公钥验证。

### 5.3 结果分类

每次调用返回结果与下列分类之一，业务代码只依据分类决策：

| 分类 | 条件 | 业务含义 |
|---|---|---|
| 成功 `Success` | HTTP 200 且响应验签通过 | 可直接采用结果 |
| 被拒 `Rejected` | HTTP 400/401/403/404 且响应验签通过 | 确定未执行；可修正后用新单号重来 |
| 未知 `Unknown` | 201、202、409、5xx；其他非 200 状态；超时；请求发出后断连；响应无签名或验签失败；响应无法解析 | 可能已执行；必须查单确认，重试沿用原 `out_order_no` |
| 未发出 `NotSent` | 配置缺失或无效、签名失败、DNS 解析失败、连接被拒——能证明请求一个字节都没发出 | 确定未执行 |

拿不准一律归「未知」；不能证明未发出的网络错误也归「未知」。被拒和未知时，结果中带上 HTTP 状态和响应里的 `code`、`message`（如有）。

### 5.4 超时与重试

单次请求超时 30 秒。客户端从不自动重试；是否重试由调用方在查单后决定。

## 6. 接口方法与数据类型

- 除 `GET /v1/accounts` 外的 27 个 API 各一个类型化方法（该接口是官网隐藏的内部接口，不实现，查账户统一用分页查询）；请求、响应各有 Go 结构体，JSON 字段名与规范一致（snake_case）。10 类 Webhook 的 `data` 各有结构体。
- 客户端不做业务判断，只拼请求、验签、分类。
- 规范缺陷按原样实现并注释：

| 问题 | 处理 |
|---|---|
| 创建账户请求体多一层 `data` | 照规范包装；若服务端返回 400 即属「被拒」，安全失败，联调再改 |
| `PUT /v1/card/status` 的 `account_no` 为 integer | 按规范发 int64；其余 `account_no` 一律 string |
| 分页 `count` 为字符串；转账 `count` 为 null | 按字符串解析，另提供转整数方法 |
| 时间字段秒、毫秒、ISO 字符串混用 | 统一时间类型：按位数区分秒与毫秒，兼容 ISO 字符串 |
| Webhook 消息 ID `messageId` / `message_id`；`data` 为字符串或对象 | 两种写法都兼容；字符串先解析为对象 |

- 金额：接口为最多两位小数的字符串（如 `"100.50"`）；内部一律用 int64 分。提供分 ↔ 字符串转换；超过两位小数、负数、科学计数法、空串、非数字一律报错；全程不用浮点数。

## 7. Webhook 接收

- 路由：`POST /api/v1/webhooks/marswallex`（公开）；后台显示完整回调地址供在 MarsWallex 开发者平台配置。
- 处理：
  1. 读取原始请求体，上限 1MB；
  2. 解析 `F-Signature` 的 `t`、`v`，用平台公钥验证 `t&原始请求体`；
  3. 校验 `t` 与当前时间相差不超过 5 分钟（兼容秒与毫秒）；
  4. 第 2、3 步不通过返回 401，不入库；
  5. 解析 `topic`、消息 ID、`data`；
  6. 写入 `marswallex_webhook_events` 后返回 200。
- 重复消息 ID 返回 200，不重复入库；入库失败返回 500 让对方重试，保证不丢消息。
- 新表 `marswallex_webhook_events`：

| 字段 | 说明 |
|---|---|
| `id` | 自增主键 |
| `message_id` | 唯一；缺失时用原始请求体的 SHA-256 十六进制 |
| `topic` | 事件类型 |
| `payload` | 原始请求体 |
| `received_at` | 收到时间 |
| `processed_at` | 可空；本子项目恒为空，子项目 2 处理后写入 |
| `process_error` | 可空；子项目 2 使用 |

不复用 `webhook_events`：该表属 ZovoCard，且没有处理状态字段。

## 8. 后台接口与页面

后台接口均在 `/api/v1/admin/marswallex/` 下，需管理员 JWT：

| 方法与路径 | 作用 |
|---|---|
| `GET /config` | 地址、developerId、密钥是否已配置、我们的公钥指纹与 PEM、平台公钥指纹、主密钥是否就绪、Webhook 回调地址 |
| `PUT /config` | 保存配置；密钥留空不修改；PEM 校验；主密钥未就绪时拒绝保存密钥；写审计日志 |
| `POST /test` | 调 `GET /v1/accounts/page?page_no=1&page_size=10`（只读）；返回分类、HTTP 状态、响应验签结果、各账户各币种余额 |
| `GET /webhook-events?limit=50` | 最近 Webhook：时间、topic、消息 ID、处理状态 |

测试连接失败时按原因给出提示：401/403 → 检查 developerId 与 apiToken；签名被拒 → 确认交给 MarsWallex 的公钥与此处私钥是同一对；响应验签失败 → 检查平台公钥；404 → 检查地址前缀；未发出 → 检查网络与地址。

前端：`/ops` 下新增「MarsWallex 对接」页（布局参照 `CardIntegration.vue`），4 块：环境与地址（两个预设）；凭证（developerId、apiToken、私钥、平台公钥，显示我们的公钥可复制）；测试连接；最近 Webhook。导航增加入口。

## 9. 待联调确认项

1. 地址前缀 `/portal/api` 是否正确（测试连接 404 可判断）。
2. 多参数查询串的签名编码方式（测试连接的两参数请求可判断）。
3. 创建账户请求体是否真的多一层 `data`。
4. `PUT /v1/card/status` 的 `account_no` 实际类型。
5. 测试环境地址是否为 `https://api.feibeiglobal-test.com/portal/api`。

1、2 在本子项目验收时确认；3–5 涉及写操作或额外环境，留到子项目 2 联调时确认。

## 10. 测试与验收

### 10.1 自动化测试（全部离线，密钥在测试中临时生成）

| 对象 | 覆盖 |
|---|---|
| `secretbox` | 往返；篡改密文、换键名、换主密钥、主密钥缺失或长度不对均报错 |
| 签名 | 三种原文格式与文件上传格式与文档示例逐字一致；签名可被对应公钥验证；任一字符篡改即失败 |
| 传输与分类 | `httptest` 模拟 MarsWallex（用我们的公钥验请求签名、用模拟平台私钥签响应）：200 有签名 → 成功；200 无签名 → 未知；400 有签名 → 被拒；400 无签名 → 未知；201/202/409/500 → 未知；超时、中途断连 → 未知；连接被拒、配置缺失 → 未发出；毫秒时间戳响应验签通过 |
| 接口方法 | 每个方法的 HTTP 方法、路径、查询串、请求体与规范一致，含创建账户 `data` 包装与文件上传 multipart |
| 金额与时间 | 合法金额互转；非法输入报错；秒、毫秒、ISO 字符串解析 |
| Webhook | 正确签名入库并 200；错误签名、过期时间戳 401；重复消息 ID 200 且不重复入库；`messageId` 与字符串 `data` 兼容；入库失败 500 |
| 后台接口 | 读出脱敏；留空不修改；PEM 错误拒绝；主密钥缺失拒绝保存密钥 |

### 10.2 验收标准

1. `go test ./...` 通过（已知原有失败的 `TestIssueProductKeepsGPTDefault`、`TestXIssueInfersProductWithoutQueryParameter` 除外，单独说明，不在本子项目修复）。
2. `go vet ./...` 与前端构建通过。
3. 用户用生产凭证在后台「测试连接」得到「成功」并看到账户余额（只读，同时确认 §9 第 1、2 项）。
4. 在 MarsWallex 配置回调地址后至少收到一条 Webhook，验签通过并入库（取决于对方推送时机，可上线后确认）。
