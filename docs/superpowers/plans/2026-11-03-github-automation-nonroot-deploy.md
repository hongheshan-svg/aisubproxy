# GitHub 自动化补强与非 root 部署 实施计划

> **给执行代理：** 按任务实施，每个任务在自己的 worktree 里完成并提交。设计以 spec 为准，本计划只补充接口约定和任务边界。

**Goal：** 每个 PR 都要过全套必过检查；发版先跑测试，再同时产出原生包和 GHCR 镜像；Docker 版支持在线升级；运行身份保持 root 不变。

**Spec：** `docs/superpowers/specs/2026-11-03-github-automation-nonroot-deploy-design.md`

**基线：** 分支 `ci/hardening`（基于 origin/master `fe10a7f`）。

## 全局约束

- 所有文案、注释、提交信息用简体中文；提交末尾带执行模型的 `Co-Authored-By`。
- 完整卡号、CVV、用户会话、代理密码、私钥不进日志、事件、截图、接口返回和审计。
- 不调用真实业务接口，不动真实资金；不碰 18080 端口和仓库的 `data/` 目录。
- 不推送、不开 PR（由主代理统一做）。
- Chrome 绝不以 `--no-sandbox` 运行。
- Go 工具链 1.27.x，Node 24；Docker 构建和运行阶段用同一个 Debian 版本（trixie）。

## 任务之间的接口约定

| 约定 | 值 |
|---|---|
| 容器内数据目录 | `DB_PATH` 所在目录，默认 `/app/data` |
| 在线升级 runtime 目录 | `<数据目录>/runtime/`，内含 `cdk-recharge`、`web/`、`VERSION`、`boot.json` |
| 在线升级临时目录与备份 | `<数据目录>/updates/` |
| 镜像里的程序与版本号 | `/app/cdk-recharge`、`/app/web`、`/app/VERSION` |
| 容器判断 | 沿用 `inContainer()`（`CDK_DEPLOY_MODE=docker` 等） |
| 防重复接管的环境变量 | `CDK_RUNTIME_HANDOFF=1` |
| 运行身份 | 原生和容器都保持 root；Chrome 以 `cdk-chrome` 带沙箱运行（不变） |
| Dockerfile 目标 | `runtime-base`（运行环境）；`release`（从构建上下文的 `dist/` 复制发布包内容）；最后一个阶段 `runtime`（从源码构建，默认目标） |
| Docker 冒烟测试脚本 | `scripts/docker-smoke.sh <镜像名>`：启动容器，等 `/health` 正常，确认 `/app/data` 可写，再以 `cdk-chrome` 身份带沙箱跑一次 Chrome |
| golangci-lint 配置 | `backend/.golangci.yml`（v2 格式） |

> 2026-11-03 调整：用户决定保持 root 运行。原计划里的非 root 改动（`cdk` 用户、systemd 加固、迁移、`USER 10001`、`cap_drop`、`read_only`）全部取消。

## 任务

### 任务 1：后端——Docker 在线升级（Go 代码）
- 新增启动接管（spec §2.3 第 4、5 步）：
  - 写成可测试的独立函数，在 `cmd/server/main.go` 里最先调用（在打开数据库之前）。
  - 退回镜像版本时，把原因写进 `boot.json`；服务启动后据此告警并写审计。
  - 新版本完成启动（数据库已打开、已开始监听）后，把启动次数清零。
- `handler/update.go`、`selfupdate.go`：
  - 容器内也允许在线升级；写入位置改为 runtime 目录；完成后 `exec` runtime 里的程序，并设好 `WEB_DIR`。
  - 命令行回滚在容器内作用于 runtime 目录。
  - 删掉「容器内禁用在线更新」的提示。原生版的提示文案保持原样。
- 测试覆盖 spec §4 列出的各种接管情况，以及容器内升级、回滚时的路径选择。
- `paybrowser/sandbox.go` 的 HOME 改动：不再必需。已经做完的可以保留，root 那条路径的行为不能变。
- **Chrome 启动时不带密钥**（spec §2.1.1，用户已批准）：
  - 所有启动 Chrome 的入口都经过 `env -i` 中转，只传白名单里的环境变量；入口包括付款、浏览器自检、免付款探测。
  - 绝不能让 `APP_MASTER_KEY`、`JWT_SECRET`、`CARD_API_KEY` 等进入 Chrome 进程。
  - 沙箱和降权到 `cdk-chrome` 都保持不变。
  - 单元测试要断言改写后的命令里只剩白名单变量；在本机实测 Chrome 子进程里拿不到 `APP_MASTER_KEY`。

### 任务 2：部署——Dockerfile、compose、docker-deploy.sh、冒烟脚本
- `deploy/install.sh`：运行方式不变，只修 shellcheck 报的问题。
- `Dockerfile`：
  - 构建用 `golang:1.27-trixie` 和 `node:24-alpine`，运行阶段用 `debian:trixie-slim`；
  - 分 `runtime-base`、`release`、`runtime` 三个阶段；
  - 仍以 root 运行，仍创建 `cdk-chrome`；
  - 版本号用构建参数传入；加 `org.opencontainers.image.source` 标签。
- `docker-compose.yml`：
  - 默认用 GHCR 镜像；
  - 补上 `APP_MASTER_KEY`；
  - 安全选项保持现状（只有 seccomp）。
- 另加 `docker-compose.build.yml`，开发时用它从源码构建。
- `deploy/docker-deploy.sh` 按 spec §2.2 重写：不克隆源码，直接 `pull` 后 `up`。
- `scripts/docker-smoke.sh`：在本机用 Docker 实测通过。
- 所有 `deploy/*.sh`、`scripts/*.sh` 都要过 `shellcheck -S warning`，包括修掉 `scripts/push.sh` 的 SC2034。

### 任务 3：后端整洁与依赖漏洞
- gofmt 这 4 个文件：`internal/cardplatform/sellable.go`、`internal/db/card_health.go`、`internal/handler/card_health_test.go`、`internal/handler/network.go`。
- 新增 `backend/.golangci.yml`（v2）：只启用 govet、staticcheck、unused、ineffassign，修掉现有全部问题。
- 把 `golang.org/x/net`、`golang.org/x/text` 升到修复版，`govulncheck ./...` 要无漏洞。
- 不改 `internal/handler/update.go`、`selfupdate.go`、`internal/paybrowser/sandbox.go`、`cmd/server/main.go`（属于任务 1）。这几处如有 lint 问题，写进报告，不要改。

### 任务 4：前端依赖
- 合入已完成的小版本升级（5ffe90a）。
- 删除没有用到的 `@lobehub/icons`。
- `vue-i18n` 升到 11 最新版。
- `xlsx` 改用 SheetJS 官方 CDN 的 0.20.x 最新安装包，锁文件带完整性校验值；补一个 Excel 写入后再读回的往返测试。
- `npm audit --omit=dev --audit-level=high --registry=https://registry.npmjs.org` 通过；vitest 和构建都通过；依赖的 `engines` 与 Node 24 兼容。

### 任务 5：工作流与仓库文件（主代理做）
- 写 `ci.yml`、`security.yml`、`codeql.yml`、`.github/dependabot.yml`、`release.yml`，同步 `deploy/github-release.workflow.yml`，加 `.github/pull_request_template.md`。
- 第三方 Action 固定到提交 SHA；actionlint 检查通过。

### 任务 6：收尾（主代理做）
- 合并各任务分支，写 CHANGELOG，在本地跑全部检查。
- 推送 `ci/hardening`，开 PR，等 CI 通过后合并。
- 开启 master 分支保护（必过项按 spec §6），开启密钥扫描和推送保护。
