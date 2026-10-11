# 支付系统

在 acg-faka 原有的支付插件机制上，新增 4 个支付插件和 1 个公共工具目录，商品下单和余额充值都能用。

| 插件目录 | 后台名称 | 对接对象 | 支付方式（code） |
|---|---|---|---|
| `app/Pay/Yipay` | 易支付 | 彩虹易支付协议（MD5），以及兼容该协议的平台 | `alipay` 支付宝、`wxpay` 微信、`qqpay` QQ钱包、`bank` 云闪付 |
| `app/Pay/AlipayOpen` | 支付宝官方 | 支付宝开放平台，RSA2 公钥模式 | `face` 当面付扫码、`page` 电脑网站、`wap` 手机网站、`web` 按设备自动选 |
| `app/Pay/WechatPayV3` | 微信支付官方 | 微信支付 APIv3 直连商户 | `native` 扫码、`h5` 手机浏览器 H5 |
| `app/Pay/UsdtGateway` | 收U（USDT/USDC） | 自建的 BEpusdt / epusdt / GM Pay 网关 | `usdt.trc20`、`usdt.polygon`、`usdt.bep20` 等链，`cashier` 由用户在收银台自选 |
| `app/Pay/PayKit` | （不是插件） | 公共代码：参数签名、RSA、HTTPS 请求、本站扫码收银台 | — |

实现时参考的项目：
- 支付宝、微信支付的官方文档与 SDK，以及 yansongda/pay
- 独角数卡（assimon/dujiaoka）的易支付和 epusdt 对接代码
- BEpusdt、GM Pay（GMWalletApp/epusdt）的源码与 API 文档。签名算法用 BEpusdt 文档里的官方示例校验过。

## 在后台启用

1. **支付管理 → 支付插件**：在对应插件上点「配置」，填写商户参数。同一个插件可以「新增」多套配置，比如两个商户号。
2. **支付管理 → 支付接口**：点「新增」，依次选择：
   - 支付插件
   - 支付方式（即上表里的 code）
   - 使用哪套配置
   - 是否用于「商品购买」和「余额充值」
   - 显示名称和图标（`/assets/user/images/cash/` 下有 `alipay.png`、`wechat.png`、`usdt.png`）
3. 用一个小额商品实际付一笔，确认能收到回调并自动发货。

**回调地址**由系统自动生成，格式为 `<域名>/user/api/order/callback.<订单号>`，充值是 `/user/api/rechargeNotification/callback.<订单号>`，不用在支付平台后台另外填写。如果站点挂了 CDN，或者支付平台访问不到买家用的那个域名，就在后台基础设置里填「回调域名」。回调地址必须能从公网访问。

## 各插件的参数从哪里来

### 易支付
- **网关地址**：易支付平台的接口根地址，例如 `https://pay.example.com/`。误填成带 `submit.php` 的地址也能识别。
- **商户ID / 商户密钥**：平台分配的 pid 和 MD5 key。
- **下单方式**：
  - 「跳转收银台」用 `submit.php`，兼容性最好。
  - 「接口下单」用 `mapi.php`，拿到二维码后在本站收银台展示。
- GM Pay 新版自带易支付兼容接口（网关地址填到 `/payments/epay/v1/order/create-transaction`），按协议应能用这个插件对接，但没有实测，建议优先用「收U」插件。

### 支付宝官方
- 先在开放平台创建应用，签约要用的产品：当面付 / 电脑网站支付 / 手机网站支付。
- **APPID**：应用的 APPID。
- **应用私钥**：用支付宝密钥工具生成。PEM 格式或单行 base64 都可以。
- **支付宝公钥**：在「接口加签方式」里，把应用公钥上传后，页面显示的那个支付宝公钥。注意不是应用公钥。
- **网关地址**：正式环境留空即可。沙箱填 `https://openapi-sandbox.dl.alipaydev.com/gateway.do`。
- 只支持**公钥模式**，不支持公钥证书模式。

### 微信支付官方（APIv3）
- **商户号 / AppID**：AppID 必须已经在商户平台和该商户号完成关联。
- **APIv3 密钥**：商户平台 → API 安全，32 位。
- **商户证书序列号、商户 API 私钥**：申请 API 证书后得到。私钥就是 `apiclient_key.pem` 的内容。
- **微信支付公钥 + 公钥ID**：商户平台 → API 安全 → 微信支付公钥。老商户也可以填平台证书的内容和它的序列号。
- 需要在商户平台开通 Native 支付或 H5 支付。H5 支付还要登记 H5 支付域名。

### 收U（USDT/USDC）
- 先自己部署收款网关：推荐 [BEpusdt](https://github.com/v03413/BEpusdt)，也可以用 epusdt 或 [GM Pay](https://github.com/GMWalletApp/epusdt)。
- **网关类型**：BEpusdt 和经典 epusdt 选「MD5 签名」；GM Pay 新版选「HMAC-SHA256」，并填 PID。
- **网关地址**：网关的根地址，也可以直接填完整的 `create-transaction` 接口地址。
- **API Token**：
  - BEpusdt：后台 → 系统管理 → API 设置 → 对接令牌。
  - GM Pay：API Key 的 secret_key。
- 支付方式 code 直接作为 BEpusdt 的 `trade_type` 传过去，表示指定收款链；`cashier` 表示让用户在网关收银台自己选。GM Pay 一律由收银台选择。
- 回调里的 `amount` 是人民币金额，会和订单金额逐分核对。

## 安全设计

- **调网关接口时校验 TLS 证书。** acg-faka 自带的 `Base::http()` 关闭了证书校验，这里没有用它。
- **支付宝当面付和微信下单都会校验网关应答的签名**，防止返回的二维码或支付链接被中间人替换。
- **回调验签之外还要核对商户身份**：易支付核对 pid，支付宝核对 app_id，微信核对 mchid 和 appid，GM Pay 核对 pid。别的商户号发来的通知一律不认。
- **金额核对、重复通知、并发锁这三项由系统核心负责**，在插件验签之后执行：金额必须和订单逐分一致；已支付的订单再收到通知直接拒绝；处理回调用的是串行化事务。
- **微信回调验签失败时返回 HTTP 500。** 系统默认拒绝回调时返回 200，而微信 APIv3 只要收到 2xx 就不再重试，配置有误时这笔款就再也收不到通知了。
- **微信回调解密后，只保留需要的字段写回系统**；支付宝的 `TRADE_FINISHED` 状态统一视为支付成功。

## 测试

`tests/pay/MockGateway.php` 是一个支付网关模拟器，`tests/pay/e2e.php` 是端到端测试。模拟器里的签名和加密是独立实现的，不调用插件代码，所以测试能真正交叉验证插件实现对不对。

每种支付方式都会走一遍完整流程：真实下单 → 买家看到的支付页（跳转、表单提交或本站收银台）→ 网关验签 → 异步回调 → 订单变为已付款并自动发货。另外还覆盖这些异常情况：
- 篡改签名的通知
- 签名正确但金额不符的通知
- 重复通知
- 微信验签失败时返回 500
- 支付宝应答验签失败

在 Docker 容器里运行：

```bash
docker cp tests faka-local:/tmp/acg-tests
docker exec faka-local php /tmp/acg-tests/pay/e2e.php              # 跑完自动清理测试数据
docker exec faka-local php /tmp/acg-tests/pay/e2e.php --item=10    # 换一个带小数价格的商品
docker exec faka-local php /tmp/acg-tests/pay/e2e.php --keep       # 保留测试订单，方便到后台查看
docker exec faka-local php /tmp/acg-tests/pay/e2e.php --cleanup    # 清理 --keep 留下的数据
```

测试期间会临时关闭下单验证码，结束后自动恢复。

## Docker 部署注意

官方镜像把 `app/Pay` 软链到数据卷 `/data/pay`，只有容器**第一次启动**时会从镜像里拷贝一份。所以已经在跑的站点要手动把新插件拷进数据卷：

```bash
docker cp app/Pay/. <容器名>:/data/pay/
docker exec <容器名> chown -R www-data:www-data /data/pay
```

## 暂不支持

- 支付宝公钥证书模式
- 微信 JSAPI / 小程序支付（需要获取用户 openid）
- 退款接口
- 易支付 V2（RSA）协议
- 不由网关、而是在站内直接监听链上转账收 U

以上功能都只用模拟网关测过，还没有和真实的支付宝、微信、易支付平台、BEpusdt 联调。上线前请用真实商户号做一笔小额实付验证。
