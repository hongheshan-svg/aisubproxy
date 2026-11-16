

## 2026-11-16 - GW-LINK 品牌与 CDK
- 增加后台品牌名称、Logo 上传/移除、预览与保存；统一首页、后台、登录页、发票页及 favicon，使用 textContent 渲染自定义名称。
- 使用内置 imagegen 生成 GW-LINK Logo，源图为 public/assets/gw-link-logo.png。提示词：透明背景、深海军蓝与钴蓝、几何互锁 G/W、精确文字 GW-LINK、简洁正方形网站品牌标识。
- 品牌配置持久化到 MySQL，上传图片持久化到 data/branding；鉴权、格式/大小/尺寸限制、内容哈希文件名和 nosniff 响应。
- 新 CDK 前缀 GW，随机部分 20 位（100 bits）；增加公开 CDK 接口共享限流和全站限流，修复不可信转发头可伪造客户端 IP 的问题。
- 修复后台签名密钥可推算的默认值：改为持久化 256 位随机密钥，线上另在环境中配置随机密钥并强制 Compose 传入；旧登录失效需重新登录。
- npm test：52 项通过（9 个文件），包含真实 HTTP 鉴权/上传/限流、旧 KC 卡密兼容、签名密钥重启保持及持久化检查；不触发真实支付。
- 已部署至 https://ccc.gw-link.ai/，app 镜像 kc-gpt-pay2-app:20261116-1505-branding；发布备份 /opt/kc-pay-backups/20261116-1505-branding。保留用户当前账号/密码及业务数据。
- Playwright 隔离界面验证真实文件选择、自动缩图、上传/保存、刷新恢复、移除及名称防注入；线上公开首页（1440px/390px）、登录页和发票页无溢出及 JS 异常。生成源图 public/assets/gw-link-logo.png，经上传流程生成 512px 网站图 public/assets/gw-link-logo-web.png（约 114 KiB）。
- 正常线上后台登录因用户已更改密码而未完成，不重置密码；品牌配置和 Logo 使用已授权 SSH 部署通道保存。

## 2026-08-14 - Task: 修復 GPT 代充 API 協議代理傳參
### What was done
- Session inspect 保留為不使用代理的本機格式／到期檢查。
- 執行代充時從本地代理池取得啟用代理並傳入 `/pay.proxy`；本地池為空時省略欄位，由平台啟用代理池兜底。
- 完整 Session payload 繼續傳入，固定 CDK 冪等鍵不變。
- 重寫 `協議api.md`，對齊 inspect 無代理、Worker 協議有代理的新規格。
- 修正 Vitest CommonJS 測試載入方式。
### Testing
- `node --check gpt-api-client.js`、`node --check server.js` 通過。
- `npm test -- test/gpt-api-client.test.js`：3 passed。
- 線上使用正式 API 設定呼叫 inspect 成功，回 `reason=local_check`。
- 契約驗證確認 `/pay` payload 同時包含完整 Session 與 proxy。
- `docker compose up -d --build app` 成功，app 容器 healthy。
### Notes
- `gpt-api-client.js`：`submitPay` 支援並傳送 proxy。
- `server.js`：取得協議代理並傳入平台，保留平台代理回退。
- `test/gpt-api-client.test.js`：改驗證 proxy 會傳送並修復 Vitest 載入。
- `協議api.md`：更新完整協議與 403 風險說明。
- `progress.md`：追加本輪記錄。
- 回滾方式：還原上述檔案並執行 `docker compose up -d --build app`；回滾會恢復不傳代理的舊行為。


## 2026-08-14 - Task: 補齊 GPT API 套餐與卡片相容性
### What was done
- 新增 `pro_5x → pro5x`、`pro_20x → pro20x` 映射，inspect 與 pay 共用同一平台套餐鍵。
- `GET /plans` 客戶端支援平台 `{gpt, credit}` 回應。
- 卡片有效期嚴格支援 `MMYY`、`MM/YY`、`MM/YYYY`；無效格式明確失敗，不再回退 2030。
### Testing
- Node 語法檢查通過。
- `npm test -- test/gpt-api-client.test.js`：4 passed。
### Notes
- `server.js`：新增套餐鍵映射與有效期解析。
- `gpt-api-client.js`：支援 `raw.gpt` 套餐陣列。
- `test/gpt-api-client.test.js`：增加 plans 回應契約測試。
- `協議api.md`：補充套餐映射及有效期格式。
- `progress.md`：追加本輪記錄。
- 回滾方式：還原上述檔案並重建 app；回滾會使 Pro 套餐重新可能回 `plan_disabled`。


## 2026-08-14 - Task: 修復平台任務 done 誤判激活成功
### What was done
- 查詢 task 時優先讀取 `result.status`，不再把 queue 外層 `done` 當業務成功。
- 移除 `done` 成功終態；`result.ok=false` 強制按失敗處理並顯示內層錯誤。
### Testing
- `npm test -- test/gpt-api-client.test.js`：5 passed。
- 線上容器契約驗證：`done + result.failed → failed`，`done + result.success → success`。
- app 重建部署成功，容器 healthy。
### Notes
- `gpt-api-client.js`：優先解析內層業務狀態。
- `server.js`：移除 done 成功映射並使用 result.ok／error。
- `test/gpt-api-client.test.js`：新增 queue done 與業務終態回歸測試。
- `progress.md`：追加本輪記錄。
- 回滾方式：還原上述檔案並重建 app；回滾會再次把失敗任務誤報成功。

## 2026-11-16 - UI、兑换可靠性与请求效率
### Changes
- 前台改为说明区与三步兑换工作区；手机优先展示兑换操作。统一前台、发票页、登录页和后台基础视觉，增强后台数值对比度、移动导航与键盘访问。
- 抽离前台业务脚本和请求/轮询工具；增加超时、单请求轮询、退避、后台暂停、WebSocket 心跳修正、HTTP 状态兜底及刷新恢复。浏览器仅保存兑换码和任务号。
- 卡密占用和任务创建使用同一数据库事务；数据库写入前占用并发名额，异常路径不再无条件释放他人的卡密。
- 上游超时及重启遗留任务改为待核实；精确匹配终态，避免 unpaid/inactive/unsuccessful 被误报成功；确认成功后的记账异常不再回滚卡密。
- 本地任务返回可捕获的 Promise，成功后数据库异常不会重置成功状态或释放卡密；失败只在最终清理阶段回滚一次。进度写入顺序执行并跳过重复进度，结束前等待写入完成，避免终态被迟到的 running 覆盖。
- 状态查询增加最近任务快照与索引；磁盘指标改为异步读取。前台运行状态默认 15 秒、后台概览 10 秒刷新；后台不重叠拉取，CDK 列表按当前页面刷新。
- 字体改为系统字体，Lucide 固定本地版本并保留许可证；发票查询与登录增加超时及重复点击保护。
### Validation
- `npm test`：37 项通过（5 个测试文件）。
- Node 语法检查、HTML 内联脚本语法检查、`git diff --check` 通过。
- Playwright 模拟 API：验证桌面 1440px、手机 390px / 320px 布局；刷新恢复同一任务；丢失提交响应且 WebSocket 不可用时仍通过查询恢复；只发送一次开通请求；待核实禁止再次提交；Session 清空；后台菜单开关及 Escape。测试页面无 JavaScript 异常。
- 截图位于忽略目录 `scratch/`，数据库和支付接口未连接生产环境。真实 MySQL 事务与真实订阅支付仍需部署环境验收。
### Operations
- 本次修改未部署。服务重启时会将遗留的 running 兑换任务转为待核实；不会自动重放支付。
- 调度仍要求单进程/单实例；多实例需要共享任务租约。回滚代码前应核对待核实订单，避免旧逻辑重新开放结果不明的卡密。

## 2026-11-16 - 部署至现有 VPS Docker
### Deployment
- VPS：15.204.98.191；现有项目目录 `/opt/kc-pay`，Compose 项目 `kc-gpt-pay2`。
- 对外站点：`https://ccc.gw-link.ai/`；后台：`https://ccc.gw-link.ai/admin-login`。
- 发布镜像：`kc-gpt-pay2-app:20261116-1423`；源码暂存 `/opt/kc-pay-releases/20261116-1423`。
- 源码及数据库备份：`/opt/kc-pay-backups/20261116-1423`；回滚镜像 `kc-gpt-pay2-app:rollback-20261116-1423`。
- 合并保留服务器上的代理地区识别、数据库地区字段及后台显示扩展；支付代码使用当前工作区版本，确认 PH 地址池已有可用数据。
- 保留服务器的 `.env`、Caddyfile、Compose 网络与持久化数据；仅增加 app 的发布镜像标签并更新源码。Docker 构建上下文排除 Session、Cookie、截图和本地验收文件。
### Verification
- 合并后的本地测试：37 passed；语法检查及 diff 格式检查通过。
- 使用新版镜像和独立 MySQL 测试库完成真实事务验收：并发相同 CDK 只产生一个任务、插入失败回滚卡密占用、重启恢复保留卡密、释放后可重试、复合索引存在；测试库已清理。
- 源码哈希校验通过；`.env` 与 Caddy 配置字节一致；业务订单数量保持不变。
- 外部 HTTPS 首页/后台登录返回 200；运行状态正常且 maintenance=false；WebSocket ping 返回 pong；无效 CDK 返回 403，未登录后台接口返回 401。
- 真实域名 Playwright：桌面/手机无横向溢出、无 JavaScript 异常，获取 Session 链接存在，首页不再显示或请求账单地区。
- 未创建真实支付订单，未执行扣款测试。上方“未部署”的记录为部署前状态，本条记录为当前上线状态。


## 2026-11-16 - 修复 Logo 放大遮挡
- 在用户已登录 Chrome 的首页与后台复现：缓存中的旧 workspace.css 缺少品牌图片尺寸规则，512px 图片溢出到正文及菜单。线上 CSS 响应允许缓存 4 小时。
- 品牌脚本为图标容器及 img 同时设置固定尺寸、最大尺寸、object-fit 和 overflow 保护；首页 36px、后台 38px、登录/发票页 56px，Logo 预览固定 120px。
- 页面引用 workspace.css、branding.js、admin-branding.js 加版本标识，刷新自动获取新资源。
- 隔离浏览器检查 4 个页面 × 桌面/手机 × 当前/旧 CSS 共 16 种组合，图片均位于容器内；无业务或数据库改动。
- 6 个静态文件已热更新至 VPS，备份 /opt/kc-pay-backups/20261116-1520-logo-size；未重启容器，状态 healthy。
- 用户原 Chrome 标签页普通刷新后，真实首页与已登录后台均恢复正常。修复前后截图保存于 scratch/logo-fix/home-before.png、home-after.png、admin-before.png、admin-after.png。
