# CDK 充值系统

卡密兑换与自动付款系统：管理员在后台发放卡密，买家在兑换页粘贴卡密和账号信息，系统通过 MarsWallex 虚拟卡完成订阅付款并开通。单机部署，数据存 SQLite。

## 官方入口

| 类型 | 链接 |
|------|------|
| **官方频道** | [https://t.me/spacex_card_visa](https://t.me/spacex_card_visa) |
| **官方群聊 2 群** | [https://t.me/spacex_card2](https://t.me/spacex_card2) |

## 服务器安装与升级

适用于 Linux x86_64 + systemd（如 Ubuntu 22.04 及以上），需要 root 权限。每个发布包都会经过三道检查，任何一步不通过都不会改动已安装的文件：
- 校验 sha256；
- 校验 ed25519 签名；
- 先运行一次新程序，确认能启动。

**首次安装**

```bash
curl -fsSL https://raw.githubusercontent.com/hongheshan-svg/aisubproxy/master/deploy/install.sh | sudo bash
```

- 装在 `/opt/cdk-recharge`，注册为 systemd 服务 `cdk-recharge`。
- 默认只监听 `127.0.0.1:9080`，需要用 Caddy 或 Nginx 反代 HTTPS。
- 用 `journalctl -u cdk-recharge | grep 'Setup Token'` 查看首次安装令牌，再打开 `https://你的域名/ops/setup` 创建管理员。
- 请备份 `/opt/cdk-recharge/app.env`。里面的 `APP_MASTER_KEY` 丢失后，已保存的 MarsWallex 密钥将无法解密。

**升级**

- 方式一：管理后台 →「总览」→「版本更新」→ 一键更新。
- 方式二：命令行重新运行安装命令。会先备份当前版本，再升级到最新版，`app.env` 和数据都不会动。

```bash
curl -fsSL https://raw.githubusercontent.com/hongheshan-svg/aisubproxy/master/deploy/install.sh | sudo bash
```

升级或安装指定版本：

```bash
curl -fsSL https://raw.githubusercontent.com/hongheshan-svg/aisubproxy/master/deploy/install.sh | sudo bash -s -- v1.5.0
```

**回滚**

新版本起不来时，用这条命令恢复到上一个版本：

```bash
sudo /opt/cdk-recharge/cdk-recharge rollback && sudo systemctl restart cdk-recharge
```

每次升级前，程序、前端和数据库都会备份到 `/opt/cdk-recharge/updates/backup-*`，只保留最新 3 份。

**Docker 部署**

每次发版同时发布 Docker 镜像 `ghcr.io/hongheshan-svg/aisubproxy:X.Y.Z`，版本号不带 `v`。安装和换版本都用同一个脚本：

```bash
curl -fsSL https://raw.githubusercontent.com/hongheshan-svg/aisubproxy/master/deploy/docker-deploy.sh | sudo bash -s -- --domain 你的域名
# 指定版本：… | sudo bash -s -- --version X.Y.Z
```

- 需要 Docker Compose V2（`docker compose` 命令），不支持旧的 `docker-compose`。
- 首次安装令牌只在容器首次启动的日志里打印一次，用 `docker logs cdk-recharge 2>&1 | grep 'Setup Token'` 查看，再打开 `https://你的域名/ops/setup` 创建管理员。
- 部署目录默认是 `/opt/cdk-recharge`。请备份其中的 `.env`：里面的 `APP_MASTER_KEY` 丢失后，已保存的 MarsWallex 密钥将无法解密。
- 后台「版本更新」也能一键升级。新版本保存在数据卷里，重建容器后仍然生效。新版本连续 3 次启动失败时，会自动退回镜像自带的版本。
- 回滚在线升级：`docker exec cdk-recharge /app/cdk-recharge rollback && docker restart cdk-recharge`

发布新版本的方法见 [deploy/README-release.md](deploy/README-release.md)。

## 功能概览

**买家端**（无需注册登录）
- 兑换页 `/recharge`：粘贴卡密和账号登录信息，先核对账号，没问题就直接开通。
- 批量兑换 `/batch`：一次提交多张卡密，逐张显示进度，可导出结果。
- 卡密查询 `/history`：凭卡密查看兑换进度和结果。
- 账单查询 `/billing`：核对账号的订阅和账单状态。

**管理后台**（入口 `/ops`，首次安装走 `/ops/setup` 向导）
- 卡密与付款：发码、码列表和筛选、付款单、自动付款配置（按套餐和地区设最低金额、是否允许 0 元开通等）。
- 卡服务商：目前接入 MarsWallex（凭证加密保存，用 `APP_MASTER_KEY` 加密）；含 Webhook 事件、主钱包、卡池（开卡、回收、冻结、对账）和选卡规则。
- 自动付款：服务器上用真实的 Chrome 打开付款页完成支付（需要 Chrome、Xvfb 和中文字体，Docker 镜像已内置）。
- 代理商：后台开户、等级定价、代理商自己的后台 `/agent`、代理换码页、对接异次元等外部发卡站。默认关闭，到后台「系统 → 功能开关」打开。
- 系统：外观与品牌（12 套皮肤）、Telegram 告警、审计日志、版本更新（一键更新、回滚）。

**安全与运维**
- 管理员用 JWT 登录；反代后面必须设置 `TRUSTED_PROXIES`，否则按 IP 的限流和审计 IP 不可信。
- 发布包有 sha256 和 ed25519 签名两道校验；在线更新前先跑新程序的自检，失败不替换任何文件。
- 系统告警走 Telegram，后台可配置并发测试消息。

## 技术栈

- 后端：Go 1.26+（CI 和镜像用 1.27）、Gin、SQLite（CGO，文件路径由 `DB_PATH` 指定）。付款浏览器自动化在 `backend/internal/paybrowser`，付款引擎在 `backend/internal/payengine`。
- 前端：Vue 3、Vite、TypeScript、Tailwind CSS、Pinia、Element Plus、vue-i18n（中英两套）。
- 部署：systemd 原生部署，或 Docker（`ghcr.io/hongheshan-svg/aisubproxy`，Caddy 反代）。

## 项目结构

```
├── backend/
│   ├── cmd/server/        # 程序入口
│   └── internal/
│       ├── server/        # 路由、中间件、HTTP 服务
│       ├── handler/       # HTTP 处理函数（兑换、后台、安装向导、在线更新等）
│       ├── db/            # SQLite 建表、迁移和查询
│       ├── marswallex/    # MarsWallex 接口客户端、验签、Webhook
│       ├── payengine/     # 付款引擎（排队、状态机、熔断）
│       ├── paybrowser/    # 用 Chrome 完成付款页操作
│       ├── cardpool/      # 卡池
│       ├── agent/         # 代理商
│       ├── provider/      # 卡服务商注册表与套餐
│       ├── notify/        # Telegram 告警
│       └── config/        # 环境变量配置
├── frontend/src/
│   ├── views/             # user/ admin/ agent/ 三套页面
│   ├── router/routes.ts   # 路由表
│   ├── i18n/              # 中英文案
│   └── theme.ts           # 皮肤定义
├── deploy/                # install.sh、docker-deploy.sh、app.env.example、Caddy 模板、签名公钥
├── scripts/               # CI 用的脚本（构建、Docker 冒烟、发版说明）
├── docs/                  # 已知问题清单、MarsWallex 接口规范、调研与设计文档
├── Dockerfile             # runtime（源码构建）与 release（发布包）两个目标
├── docker-compose.yml     # 部署用编排
└── docker-compose.build.yml  # 叠加后从本仓库源码构建
```

## 本地开发

前置：Go（见 `backend/go.mod`）、Node.js 24+。

```bash
# 后端（默认 SQLite、监听 8080；可用环境变量改，见下）
cd backend && go run ./cmd/server

# 前端（另开一个终端，Vite 开发服务器）
cd frontend && npm install && npm run dev
```

- `make dev` 只打印上面两条命令；`make build` 构建前后端；`make docker-up` 用本仓库源码构建并启动 Docker（会生成 `.env`，含 `JWT_SECRET` 和 `APP_MASTER_KEY`）。
- 后端启动后健康检查是 `http://localhost:8080/health`，首次打开 `/ops/setup` 走安装向导，令牌在后端启动日志里（找 `Setup Token`）。
- 开发时没有配置 `APP_MASTER_KEY` 也能起，但后台保存 MarsWallex 凭证会报错；本地可用 `openssl rand -base64 32` 生成一个再导出。
- 数据库文件默认是 `../data/cdk_recharge.db`（相对运行目录，在 `backend/` 下运行就是仓库根的 `data/`），可用 `DB_PATH` 改；本地的 `data/` 目录和 `.env` 已在 `.gitignore`，不要提交。

## 环境变量

完整说明和默认值见 [deploy/app.env.example](deploy/app.env.example)。常用的几项：

| 变量 | 作用 |
|------|------|
| `SERVER_HOST` / `SERVER_PORT` / `SERVER_MODE` | 监听地址、端口（默认 8080）、`debug` 或 `release` |
| `DB_PATH` | SQLite 文件路径 |
| `WEB_DIR` | 前端构建产物目录 |
| `JWT_SECRET` | 管理员登录令牌的签名密钥 |
| `APP_MASTER_KEY` | 加密保存 MarsWallex 等密钥的主密钥（base64 的 32 字节），丢失后已保存的密钥无法解密 |
| `TRUSTED_PROXIES` | 可信反代地址，决定能否信任 `X-Forwarded-For` |
| `INSTALL_MODE` | 安装方式，默认 `wizard`（走安装向导） |
| `UPDATE_ENABLED` | 设 `0` 关闭后台在线更新 |
| `CHROME_PATH` | 自动付款用的浏览器路径，一般不用设 |
| `TELEGRAM_BOT_TOKEN` / `TELEGRAM_CHAT_ID` / `TELEGRAM_PROXY` | 后台没配 Telegram 告警时的后备配置 |

## 测试

```bash
# 后端（internal/paybrowser 整包较慢，只改别处时可排除）
cd backend && go test ./...
cd backend && go test $(go list ./... | grep -v /paybrowser)

# 前端
cd frontend && npx vitest run
```

CI（`.github/workflows/ci.yml`）在开 PR 和推送 master 时跑后端与前端的构建和测试、Docker 冒烟、shellcheck、actionlint 和 gitleaks。master 受保护，只能通过 PR 合并。

## 部署

### Docker 镜像构建
```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml build
```

发版时的镜像由 GitHub Actions 用已签名的发布包构建（Dockerfile 的 `release` 目标），先试跑再推送到 GHCR。

### 生产环境部署
见上文「[服务器安装与升级](#服务器安装与升级)」；发布流程见 [deploy/README-release.md](deploy/README-release.md)。

## 常见问题

**Q: 如何查看日志？**
```bash
docker compose logs -f cdk          # Docker 部署
journalctl -u cdk-recharge -f       # systemd 部署
```

**Q: 如何重置数据库？**（会删除全部数据）
```bash
docker compose down -v              # Docker 部署
```
systemd 部署：停止服务后备份并删除 `/opt/cdk-recharge/data/` 下的数据库文件，再启动服务，重新走安装向导。

**Q: 默认管理员账户是什么？**
没有默认账户。首次访问 `/ops/setup`，凭启动日志里的 Setup Token 创建管理员（令牌的查看方法见上文安装部分）。

**Q: 已知问题在哪看？**
见 [docs/known-issues.md](docs/known-issues.md)。

## 贡献

欢迎提交 Issue 和 Pull Request。

## 许可证

本项目以 MIT 许可证发布，全文见仓库根目录的 [LICENSE](LICENSE)。
