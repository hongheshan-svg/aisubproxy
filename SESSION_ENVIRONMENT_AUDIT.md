v0.0.3 · 2026-11-17 Session 环境与支付页检查

检查对象为用户提供的完整 Session JSON。本文和测试夹具均不保存其中的 Token、邮箱、账户 ID 或设备 ID。

| 项目 | 文件实际提供的信息 | 原实现问题与处理 |
| --- | --- | --- |
| 浏览器 | macOS UA，Chrome 157 | 浏览器池客户端的插件会再次覆盖 UA；现统一禁用冲突覆盖，使用 CDP 同步请求头与 navigator.userAgentData，包括完整版本列表。 |
| 系统版本与架构 | UA 是缩减形式，不能据此确定实际 macOS 版本或 CPU | 原实现写死 macOS 14 / ARM。现优先使用明确采集的 Client Hints；未提供时保持未知。修改 UA 不会替换 Chromium 内核。 |
| 屏幕、窗口、缩放、触摸 | 本文件未提供 | 删除第二次覆盖成 1920×1080 的脚本；若提供 __env 数据则使用，缺失时采用明确的桌面/手机布局默认值。手机不再被强制拉到桌面尺寸。 |
| CPU、内存、显卡、Canvas、字体 | 本文件未提供 | 删除固定的硬件值、Windows Direct3D 显卡描述及 Canvas 像素修改；无法从 Session 还原未采集的设备信息。 |
| 设备 ID | statsigContext.deviceId | Cookie 和 API 头统一选取同一个值，oai-did 保持 JavaScript 可读。已有 Cookie 优先于重复的环境字段。 |
| 账户 ID | account.id，JWT 也有账户信息 | 只有账户信息而没有 statsigContext 时不再丢失账户 ID；真实会话返回另一用户身份时拒绝继续。 |
| 时区 | 国家 US、州 NJ；没有 IANA 时区 | America/New_York 是根据位置推断的值，不是文件记录的真实时区。有 __env.timezone/timezoneId 时优先使用。 |
| 登录凭证 | accessToken 和 4768 字符的 sessionToken | 原有长 Cookie 分块可用；移除伪造 session/CSRF 响应、恢复已撤销 Cookie 的行为，真实校验后使用服务端刷新得到的 token。 |
| 初始页面 | 未涉及 | 之前环境覆盖晚于首次 Cookie 在线校验；现在首次导航前即安装环境配置。 |
| 支付页判定 | 未涉及 | API 调试和网页升级两条路径均等待支付页面内容，只有 URL 不再算成功。 |

IP 和语言沿用现有出口策略。marketingEligibility、adsSegmentId、用户头像等不是需要手工伪造的浏览器环境字段；网页使用服务端真实会话响应。

验证结果：14 个测试文件、129 项测试通过。另以生产相同的浏览器插件组合，在隔离 Chromium 中验证了 Mac、Android、iPhone 三种环境配置；这不等于分别运行三个原生浏览器内核。

使用用户提供的 Session 在 VPS 临时目录实测：Cookie 验证通过、真实登录成功；Plus / PH 的直接 Checkout API 返回 HTTP 400（unusual activity），随后网页升级入口成功打开 Checkout，并完成付款控件就绪验证，识别 Saved/Card 选择器并切换到空白新卡表单，保存了截图。测试在支付页停止，没有提交付款。没有据此验证 Pro 100 / 200 / 500 或其他用户账号。

以上为 v0.0.3 发布前的隔离验证。这次测试不能证明任意 Session 必定成功：过期/撤销凭证、缺失网页登录 Cookie、账号订阅资格、第三方风控和服务异常仍需明确报错。尤其 Pro 500 目前仍是 API 路径，不能推断其在 API 被拒绝时也会成功。

后续实现补充：环境读取按字段合并 `__env → clientEnv → clientEnvironment → environment → statsigContext`，也支持采集数据中的 `navigator`、`viewport` 和 `timezoneId`。空值、格式错误的值不会覆盖其他来源有效的字段；每个任务使用自己 Session 的账户/设备信息与隔离的浏览器 context。接口同时接受 Session JSON 字符串和对象。

Card 现在是 API/网页两种 Checkout 路径、正式任务、调试任务和换卡重试的必经检查。兼容 Saved/Card、多语言 Card 控件和直接显示新卡字段的页面。仅在卡号、有效期、安全码三个输入框都可见且可编辑时返回成功；切换失败或超时则停止后续付款，不会因标签已选中就跳过检查。原来的可选调试开关不再跳过 Card 检查。

可复现检查：`npm test -- --reporter=dot`；浏览器检查：`node scripts/check-session-environment.cjs`；Card 浏览器测试：`node scripts/check-card-entry.cjs`，可用 `PLAYWRIGHT_TEST_EXECUTABLE` 指定已安装的 Chromium。

实现依据：[Playwright BrowserContext 配置](https://playwright.dev/docs/api/class-browser#browser-new-context)、[Chromium Emulation 协议](https://chromedevtools.github.io/devtools-protocol/tot/Emulation/#method-setUserAgentOverride)、[Chrome User-Agent Client Hints](https://developer.chrome.com/docs/privacy-security/user-agent-client-hints)。
