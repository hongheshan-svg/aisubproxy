# GitHub 自动化补强、双版本发布与 Docker 在线升级 设计

日期：2026-11-03
分支：`ci/hardening`（基于 origin/master `fe10a7f`）

> 文件名里的 `nonroot` 是早期方案留下的。用户最终决定**保持 root 运行**（见 §1.2、§2），为了让计划和提交里的引用继续有效，文件名没有改。

## 1. 背景

### 1.1 现状问题（2026-11-03 实测）

- **发版不跑测试。** `release.yml` 只在推 `v*` 标签时触发，直接编译、签名、发布。测试不过的代码也能发出去。
- **工具链已停止维护。**
  - 发版、CI、Dockerfile 都用 Go 1.24 编译，而 Go 官方只维护最新两个大版本（现在是 1.26、1.27）。
  - 前端用 Node 20，它已于 2026-04 停止维护。
- **依赖有已知漏洞。**
  - 后端：`x/net`、`x/text` 共 6 个。
  - 前端生产依赖：原有 24 个。小版本升级后还剩 12 个，其中 5 个高危：`vue-i18n` 系列要升大版本；`xlsx` 在 npm 上没有修复版。
- **代码检查不足。**
  - 4 个 Go 文件没有 gofmt。
  - shellcheck 报 1 条警告。
  - golangci-lint 报 62 条，其中 50 条是没检查错误返回值。
- **发布构建平时不验证。** 静态 Linux 构建和 Docker 镜像只在发版或部署时才构建，坏了要到那时才知道。
- **测试耗时接近上限。** `paybrowser` 包测试在 CI 上要 532 秒，本地带竞态检测 523 秒，接近 `go test` 默认的 10 分钟超时。
- **master 没有保护**，CI 不过也能合并。
- **Docker 部署的问题。**
  - 现在是在服务器上克隆源码、现场构建，没有发布好的镜像。
  - 容器里不能在线升级，因为容器一重建，就会退回镜像里的版本。

### 1.2 用户的决定

- CI 补强、安全、发版与仓库规则三批都做。
- master 开分支保护，管理员也受约束；开启 GitHub 密钥推送保护。
- **服务继续以 root 运行**，原生版和 Docker 版都是。Chrome 照旧换成低权限用户 `cdk-chrome`、带沙箱运行。
  - 当天评估过改成非 root，结论是：会失去 Chrome 和密钥之间的用户隔离；要补回来又得加新机制。用户决定保持现状。
- 每次发版同时提供 Docker 版和原生版。
- Docker 版的在线升级参考 sub2api 来做。
- 漏洞扫描只做提醒，不卡合并和发版。

### 1.3 成功标准

1. 每个 PR 都要过全部必过检查才能合并进 master。
2. 推标签发版时，先跑完全部测试和构建，再产出签名的原生包和 GHCR 镜像。
3. 两种部署的运行方式和现在一样：程序以 root 运行，Chrome 以 `cdk-chrome` 身份带沙箱运行。Docker 版可以在后台一键升级。
4. 新增的每项检查在开启那一刻就是绿的：先修问题，再开检查。

## 2. 运行身份与部署

### 2.1 运行身份：保持现状

- **原生版**（`deploy/install.sh`）：运行方式不变，systemd 单元仍是 `User=root`，安装时创建 `cdk-chrome` 用户，沙箱自检以 `cdk-chrome` 身份执行。只修 shellcheck 报的问题。
- **Docker 版**：容器内仍以 root 运行，镜像里照旧创建 `cdk-chrome`，安全选项保持现状（只有 `seccomp=./deploy/chrome-seccomp.json`）。

### 2.1.1 Chrome 只拿白名单里的环境变量（用户已批准）

**问题**：chromedp 启动 Chrome 时，会把程序的全部环境变量（`os.Environ()`）传给 Chrome，其中包括 `APP_MASTER_KEY`、`JWT_SECRET`、`CARD_API_KEY`。虽然 Chrome 换成了 `cdk-chrome` 用户、读不到 `app.env`，进程里却直接带着主密钥，这层隔离就漏了。

**做法**
- 所有启动 Chrome 的地方（付款、浏览器自检、免付款探测），都通过 `env -i` 中转，只传白名单里的变量：`PATH`、`HOME`、`TMPDIR`、`DISPLAY`、`TZ`、`LANG`、`LANGUAGE`、`LC_*`、`XDG_*`、`FONTCONFIG_*`。
- `HOME` 和 `TMPDIR` 指向本次 Chrome 的临时目录。
- 代理通过 `--proxy-server` 参数传入，不经过环境变量。
- 沙箱和降权到 `cdk-chrome` 都保持不变。

**验证**：单元测试断言改写后的命令只剩白名单变量；在本机实测 Chrome 子进程拿不到 `APP_MASTER_KEY`；再跑一遍「浏览器自检」和「试运行到填表」。

### 2.2 Docker 版：改为发布镜像

**镜像（Dockerfile）**
- 构建阶段改用 `golang:1.27-trixie` 和 `node:24-alpine`；运行阶段用 `debian:trixie-slim`，与构建阶段同一个 Debian 版本。
- 分三个阶段：
  - `runtime-base`：运行环境，包括 chromium、xvfb、字体、`cdk-chrome` 用户、环境变量、健康检查；
  - `release`：基于 `runtime-base`，复制发布包里的程序、前端和版本号；
  - `runtime`：最后一个阶段，也是默认目标。基于 `runtime-base`，从源码构建。
- 版本号由构建参数写入。
- 加 `org.opencontainers.image.source` 标签，把镜像关联到仓库。

**docker-compose.yml**
- 默认用发布好的镜像：`image: ghcr.io/hongheshan-svg/aisubproxy:${CDK_VERSION:-latest}`。
- 补上从 `.env` 读取 `APP_MASTER_KEY`。
- 另加 `docker-compose.build.yml`，开发时从源码构建。

**docker-deploy.sh 重写**
- 不再克隆源码，按顺序：
  1. 按指定版本，从 GitHub 下载该标签下的 compose 文件、seccomp 配置和 Caddyfile 模板；
  2. 首次运行生成 `.env`，其中 `JWT_SECRET`、`APP_MASTER_KEY` 都是随机值；已有的 `.env` 不覆盖；
  3. `docker compose pull && docker compose up -d`，等健康检查通过。

### 2.3 Docker 版在线升级（参考 sub2api）

现在容器里的在线升级是禁用的，原因是容器一重建，就会退回镜像里的版本。

**sub2api 的做法（`Wei-Shaw/sub2api`）**
- 镜像里的程序文件归运行用户所有。
- 在线升级时，下载发布包、校验 checksum，用原子改名替换容器里的程序，然后让进程退出，由 Docker 的 `restart: unless-stopped` 重启容器。
- 缺点：替换只发生在容器的可写层。容器一重建（拉新镜像、改 compose、`down/up`），就会悄悄退回镜像版本，而数据库可能已经被新版本迁移过。

**我们的做法：沿用原生版现有的升级流程，只改写入位置，并在启动时接管**

1. **下载和校验**：与原生版完全一样，校验 sha256 和 ed25519 签名，替换前让新程序做 `--version` 自检，并备份数据库。发布包也是同一个 `cdk-bundle-linux-amd64.tgz`。
2. **写入位置**：容器内不写镜像目录，改写数据卷：
   - 程序、前端、版本号写到 `/app/data/runtime/`；
   - 备份和临时文件写到 `/app/data/updates/`，和 runtime 在同一个文件系统上，改名才是原子的。
3. **切换到新版本**：替换完成后，直接 `exec` 数据卷里的新程序。进程号不变，仍然是 PID 1，容器不用重启。
4. **启动时接管**：程序启动时，先比较镜像里的 `/app/VERSION` 和数据卷里的 `/app/data/runtime/VERSION`。
   - 数据卷里的版本更新：`exec` 数据卷里的程序，把 `WEB_DIR` 指向 `/app/data/runtime/web`，并在环境变量里带上 `CDK_RUNTIME_HANDOFF=1`，防止反复接管。
   - 镜像里的版本相同或更新（说明拉了新镜像）：用镜像里的程序，删掉数据卷里过期的 runtime；`updates/` 里的备份保留。
   - `exec` 失败（例如数据目录挂成了 `noexec`）：记日志，继续用镜像版本，不让容器起不来。
5. **启动保护**：
   - 每次接管前，`/app/data/runtime/boot.json` 里的启动次数加 1；新版本完成启动（数据库打开、HTTP 开始监听）后清零。
   - 连续 3 次没能完成启动，就不再接管：退回镜像版本运行，并告警「在线升级的 vX 连续 3 次启动失败，已退回镜像版本 vY」。
   - 数据库兼容性：沿用原生版回滚的前提，即新版本的数据库旧版本可以直接用。
6. **命令行回滚**：先执行 `docker exec cdk-recharge /app/cdk-recharge rollback`，再执行 `docker restart cdk-recharge`。回滚在容器内作用于 `/app/data/runtime`。
7. **「版本更新」页**：容器里也能一键升级。页面同时说明另一种方式：换镜像版本，执行 `CDK_VERSION=X.Y.Z docker compose pull && docker compose up -d`（镜像标签不带 `v`），或重跑 `deploy/docker-deploy.sh --version X.Y.Z`。
8. **开关**：沿用 `UPDATE_ENABLED`，设为 0 就关闭在线升级。

**取舍**：数据卷里的程序能被改写，这一点和原生版相同。下载时已经验过签名；能改数据卷的人本来也能改数据库，所以启动时不再重复验签。

## 3. 发版（`release.yml`）

推送 `v*` 标签时依次执行：

1. **checks**：调用 `ci.yml`（加 `workflow_call`），跑全部测试和构建。
   - **漏洞扫描不在发版必过项里。** 外部一公布新漏洞，扫描就会变红，这与代码改动无关，不能让它卡住付款问题的紧急修复版。
   - 漏洞扫描的结果，靠 PR 上的显示和每周定时任务来提醒。
2. **guard**：标签对应的提交必须在 origin/master 上；`CHANGELOG.md` 必须有同名小节（沿用 `scripts/release-notes.sh`）。
3. **native**：沿用现有的静态构建、打包和 ed25519 签名，工具链换成 Go 1.27.x 和 Node 24。
4. **image**（等 native 完成后再跑）：
   - 用 native 产出的发布包构建镜像，即 Dockerfile 的 `release` 目标。这样镜像里的程序与一键升级下载的发布包是同一份文件。
   - 用 `docker/build-push-action` 推到 `ghcr.io/hongheshan-svg/aisubproxy`。
   - 标签打 `X.Y.Z`、`X.Y`、`latest`，平台只做 `linux/amd64`。
   - 附带构建来源证明和 SBOM。
   - 只有这个 job 有 `packages: write` 权限。
5. **publish**：native 和 image 都成功后，才创建 GitHub Release，正文追加 `docker pull` 命令。

需要手动做一次：第一次推送镜像后，到 GHCR 包设置里把可见性改成公开。否则服务器拉取前要先 `docker login`。

`deploy/github-release.workflow.yml` 是 `release.yml` 的副本。改为在文件头注明「以 `.github/workflows/release.yml` 为准」，内容保持同步。

## 4. CI（`ci.yml`）

触发：开 PR、推送到 master，以及被 `release.yml` 调用（`workflow_call`）。同一分支有新提交时，取消还没跑完的那次。

| job | 内容 |
|---|---|
| `backend` | gofmt 检查、`go build`、`go vet`、golangci-lint、`go test -race`（paybrowser 以外的包） |
| `backend-browser` | `go test -race -timeout 30m ./internal/paybrowser/...`（真实 Chrome，单独跑，和 `backend` 并行） |
| `frontend` | `npm ci`、`npx vitest run`、`npm run build`（Node 24） |
| `release-build` | 跑 `scripts/build-linux-backend.sh`，确认静态链接的发布构建能过 |
| `docker` | 构建镜像但不推送，用 GHA 缓存。然后冒烟测试：<br>1. 启动容器，`/health` 返回正常；<br>2. `/app/data` 可写；<br>3. 以 `cdk-chrome` 身份带沙箱执行 `chromium --headless=new --dump-dom about:blank`，能拿到 HTML。 |
| `lint-meta` | 用 shellcheck 检查 `deploy/*.sh` 和 `scripts/*.sh`，用 actionlint 检查工作流文件 |
| `gitleaks` | 用固定版本的 gitleaks 扫描密钥。PR 只扫本次新增的提交，推送到 master 时扫全部历史。 |

Docker 版在线升级的启动接管（§2.3 第 4、5 步）写成独立的 Go 函数，用单元测试覆盖以下情况：
- 数据卷版本更新 → 接管；
- 镜像版本相同或更新 → 不接管，并清理过期的 runtime；
- 连续 3 次启动失败 → 不接管，并告警；
- `exec` 失败 → 退回镜像版本；
- 带了防重复接管的标记 → 不再接管。

golangci-lint 配置（`backend/.golangci.yml`，v2 格式）：
- 只启用 `govet`、`staticcheck`、`unused`、`ineffassign`，并先修掉现有的 12 条。
- `errcheck` 暂不启用。

Go 版本：工作流用 `1.27.x`。依赖升级如果要求提高 go.mod 里的 `go` 版本，就跟着提高。

## 5. 安全

### 5.1 `security.yml`

触发：开 PR、推送到 master、每周一定时运行。

**定位是提醒，不拦人。**
- 不加入分支保护的必过项，发版时也不调用。
- PR 上出现红叉，就是提醒顺手升级依赖；每周的定时任务失败时，GitHub 会发邮件给仓库管理员。
- `gitleaks` 例外：它只检查本仓库自己的提交，不受外部漏洞库更新影响，所以作为必过项（见 §6），放进 `ci.yml` 而不是 `security.yml`。

| job | 内容 |
|---|---|
| `govulncheck` | 检查后端。先把 `x/net`、`x/text` 升到修复版。 |
| `npm-audit` | `npm audit --omit=dev --audit-level=high --registry=https://registry.npmjs.org` |

### 5.2 CodeQL（`codeql.yml`）

- 扫 Go（autobuild，CGO 可用）和 JavaScript/TypeScript。
- 触发：开 PR、推送到 master、每周定时。

### 5.3 Dependabot（`.github/dependabot.yml`）

- 覆盖 `gomod`（/backend）、`npm`（/frontend）、`github-actions`（/）、`docker`（/，即 Dockerfile 的基础镜像）。
- 每周一次。小版本和补丁升级合成一个 PR，同时打开的 PR 最多 5 个。

### 5.4 前端依赖漏洞处理

- **小版本升级**：合入。
- **删除没用到的 `@lobehub/icons`**：`src/` 里没有任何引用，删掉后它带来的低危漏洞链一起消失。
- **`vue-i18n` 9 → 11**：要求 Node ≥ 22，CI 改用 Node 24 正好满足。代码用的是组合式 API（`legacy: false`），也没有用到 11 版删掉的写法。
- **`xlsx` 换成 SheetJS 官方 CDN 的 0.20.3 安装包**：npm 上只有 0.18.5，没有修复版。
  - 锁文件带完整性校验值。
  - Dependabot 管不到它，新版本要人工跟进。
  - 这是唯一一个不来自 registry.npmjs.org 的依赖。
  - 补了导出后再读回的往返测试。

### 5.5 Actions 固定版本

所有第三方 Action 都固定到提交 SHA，在注释里写上版本号，由 Dependabot 负责更新。这样即使有人把 Action 的标签改指向别的代码，也拿不到只有 `release.yml` 能用的签名私钥（防供应链攻击）。

## 6. 仓库规则与协作

- **PR 模板**（`.github/pull_request_template.md`），检查项包括：
  - 资金安全；
  - 敏感数据不落地、不进日志；
  - 是否更新 `CHANGELOG.md`；
  - 是否影响部署或升级。
- **master 分支保护**：等本分支合并、检查名称确定后再开。
  - 必过检查：
    - CI：`backend`、`backend-browser`、`frontend`、`release-build`、`docker`、`lint-meta`、`gitleaks`；
    - CodeQL：Go 和 JS 两项分析。CodeQL 只对本 PR 新引入的问题报错，不受外部规则更新的连带影响。
  - **不是**必过项：`govulncheck`、`npm-audit`，原因见 §5.1。
  - 合并必须走 PR。只有一位维护者，所以要求的批准数为 0。
  - 对管理员同样生效，禁止强推，禁止删除分支。
  - 不要求 PR 分支先更新到最新 master（`strict=false`）。合并后推送到 master 时会再跑一遍 CI，能兜住问题。
- **密钥扫描和推送保护**：公开仓库免费，用 `gh api` 开启。

## 7. 不做

- 改为非 root 运行（评估过，用户决定保持 root）。
- arm64 镜像。
- 前端类型检查（vue-tsc 要升级）和 eslint（没有配置文件）。
- golangci-lint 的 `errcheck`（50 条）。
- 缩短 paybrowser 测试耗时。另开任务，这次只把它放到单独的 job，并加长超时。
- 用 cosign 给镜像签名。这次已经有构建来源证明。

## 8. 验证

- **CI 检查全部通过**：每批合入前，先在本地跑通对应命令；推送后，PR 上的全部检查也要通过。
- **Docker**：由 CI `docker` job 的冒烟测试覆盖：`/health` 正常、数据卷可写、Chrome 能以 `cdk-chrome` 身份带沙箱启动。
- **Docker 在线升级**：在本机用两个版本号不同的镜像和发布包，手动走一遍：一键升级 → 重建容器后仍是新版本 → 拉更新的镜像后改用镜像版本。
- **原生安装**：运行方式没变，CI 只做 shellcheck。
- **发版流程**：不推正式标签，因为不论在 fork 上还是用临时标签，都会推到正式的 GHCR 和 Release。所以这次只验证 `workflow_call` 的结构，并用 actionlint 检查。第一次真实发版时，人工在 Actions 页面看着它跑完。

## 9. CHANGELOG「升级注意」要写的内容

- Docker 版改为拉取发布好的镜像，不再在服务器上构建。
- Docker 版支持后台一键升级。升级后的程序放在数据卷里，容器重建后仍然保留；之后如果拉了更新的镜像，就以镜像为准。
- 发布包改用 Go 1.27 编译，前端改用 Node 24 构建。
