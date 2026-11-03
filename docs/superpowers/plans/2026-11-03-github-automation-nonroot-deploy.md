# GitHub 自动化补强与非 root 部署 实施计划

> **给执行代理：** 按任务实施，每个任务在自己的 worktree 里完成并提交。设计以 spec 为准，本计划只补充接口约定和任务边界。

**Goal：** 每个 PR 都过全套检查；发版先测试再同时产出原生包和 GHCR 镜像；原生服务和容器都不以 root 运行；Docker 版支持在线升级。

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
| 镜像里的程序与版本号 | `/app/cdk-recharge`、`/app/web`、`/app/VERSION`（root 所有、只读） |
| 容器判断 | 沿用 `inContainer()`（`CDK_DEPLOY_MODE=docker` 等） |
| 防重复接管的环境变量 | `CDK_RUNTIME_HANDOFF=1` |
| 容器运行用户 | `cdk`，uid/gid 10001，`HOME=/tmp` |
| 原生运行用户 | 系统用户 `cdk`，安装目录 `/opt/cdk-recharge` 归 `cdk:cdk` |
| Dockerfile 目标 | `release`（从构建上下文的 `dist/` 复制发布包内容）；最后一个阶段 `runtime`（从源码构建，默认目标） |
| Docker 冒烟测试脚本 | `scripts/docker-smoke.sh <镜像名>`：启动容器、等 `/health`、确认 `id -u` 不是 0、以 10001 带沙箱跑一次 Chrome |
| golangci-lint 配置 | `backend/.golangci.yml`（v2 格式） |

## 任务

### 任务 1：后端——Chrome 的 HOME 与 Docker 在线升级（Go 代码）
- `paybrowser/sandbox.go`：任何身份下都把 Chrome 的 `HOME` 设为它的临时目录，并补测试。
- 新增启动接管（spec §2.3 第 4、5 步）：
  - 写成可测试的独立函数，在 `cmd/server/main.go` 里最先调用（打开数据库之前）。
  - 退回镜像版本的原因记进 `boot.json`，服务启动后据此告警并写审计。
  - 新版本完成启动（数据库打开、开始监听）后，把启动次数清零。
- `handler/update.go`、`selfupdate.go`：
  - 容器内也允许在线升级，写入位置改为 runtime 目录，完成后 `exec` runtime 里的程序并设好 `WEB_DIR`。
  - 命令行回滚在容器内作用于 runtime 目录。
  - 删掉「容器内禁用」的提示，提示文案改成带 `sudo` 的命令。
- 测试覆盖 spec §4 列出的接管各情况，以及容器内升级、回滚的路径选择。

### 任务 2：部署——install.sh、Dockerfile、compose、docker-deploy.sh、冒烟脚本
- `deploy/install.sh`：
  - 创建 `cdk` 用户；systemd 单元的加固项按 spec §2.1 原样写，「不加」的几项一个都不能加；
  - 从 `User=root` 的旧安装自动迁移；
  - 回滚以 `cdk` 身份执行并修正文件所有者；
  - 沙箱自检以 `cdk` 身份执行；新安装不再创建 `cdk-chrome`；
  - 提示文案带 `sudo`。
- `Dockerfile`：
  - 用 `golang:1.27-trixie` 和 `node:24-alpine`，运行阶段用 `debian:trixie-slim`；
  - 设 `release` 和 `runtime` 两个目标，`USER 10001`、`HOME=/tmp`；
  - 版本号用构建参数；加 `org.opencontainers.image.source` 标签。
- `docker-compose.yml`：
  - 默认用 GHCR 镜像；
  - `cap_drop: [ALL]`、`no-new-privileges`、`read_only`、tmpfs `/tmp`，保留 seccomp。
  - 另加 `docker-compose.build.yml`，开发时从源码构建。
- `deploy/docker-deploy.sh` 按 spec §2.2 重写：不克隆源码，迁移旧数据卷的所有者，`pull` 后 `up`。
- `scripts/docker-smoke.sh`：本机用 Docker 实测通过，包括数据卷可写、Chrome 带沙箱能起来。
- 所有 `deploy/*.sh`、`scripts/*.sh` 过 `shellcheck -S warning`，包括修掉 `scripts/push.sh` 的 SC2034。

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
