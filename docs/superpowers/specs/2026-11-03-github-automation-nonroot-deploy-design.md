# GitHub 自动化补强与非 root 部署 设计

日期：2026-11-03
分支：`ci/hardening`（基于 origin/master `fe10a7f`）

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
- **部署依赖 root。**
  - 原生安装的 systemd 服务是 `User=root`。
  - 现有 Docker 部署在服务器上克隆源码现场构建，容器内程序也是 root。

### 1.2 用户的决定

- CI 补强、安全、发版与仓库规则三批都做。
- master 开分支保护，管理员也受约束；开启 GitHub 密钥推送保护。
- 服务不以 root 运行。首次安装（装 Docker 或浏览器依赖、写 systemd）用一次 root 可以接受。
- 每次发版同时提供 Docker 版和原生版。

### 1.3 成功标准

1. 每个 PR 都要过全部检查才能合并进 master。
2. 推标签发版时，先跑完全部检查，再产出签名的原生包和 GHCR 镜像。
3. 原生服务和容器内的程序都不是 root，Chrome 带沙箱运行。
4. 新增的每项检查在开启那一刻就是绿的：先修问题，再开检查。

## 2. 运行身份：不再用 root

程序本身已经支持非 root 运行：不是 root 时，Chrome 沿用当前用户、带沙箱启动（`paybrowser/sandbox.go`）。改动集中在部署侧。以 root 运行时降权到 `cdk-chrome` 启动 Chrome 的代码保留，兼容还没升级的旧安装。

### 2.1 原生版（`deploy/install.sh`）

**运行用户**
- 安装时创建系统用户 `cdk`：不能登录，没有家目录。
- 安装目录 `/opt/cdk-recharge` 整个归 `cdk:cdk` 所有，`app.env` 权限 600。

**systemd 单元**
- 改为 `User=cdk`、`Group=cdk`。
- 加上这些加固项：
  - `NoNewPrivileges=true`
  - `ProtectSystem=strict`
  - `ReadWritePaths=/opt/cdk-recharge`
  - `PrivateTmp=true`
  - `ProtectHome=true`
  - `ProtectKernelTunables=true`
  - `ProtectKernelModules=true`
  - `ProtectControlGroups=true`
  - `CapabilityBoundingSet=`（空）
  - `RestrictSUIDSGID=true`
  - `LockPersonality=true`
- **不加**以下几项，因为会让 Chrome 起不来：
  - `RestrictNamespaces` 和 `PrivateUsers`：Chrome 沙箱要建用户命名空间。
  - `MemoryDenyWriteExecute`：V8 的 JIT 要用。
  - `SystemCallFilter`：Chrome 用到的系统调用太杂。

**Chrome**
- 以 `cdk` 身份带沙箱运行。新安装不再创建 `cdk-chrome` 用户。
- 安装脚本的沙箱自检改为以 `cdk` 身份执行。

**从 root 安装升级**
- 检测到旧单元是 `User=root` 时，按以下顺序处理：先备份，再创建 `cdk` 用户，然后 `chown -R` 安装目录，最后重写单元并重启。
- 升级说明里写清楚：自己写的、以 root 往安装目录写文件的脚本需要调整。

**一键升级**
- 现在是原地重新执行新程序（`syscall.Exec`）。安装目录归 `cdk` 所有，所以不需要 root。
- 原来的兜底是 `systemctl restart`，非 root 执行不了。改为提示「请执行 `sudo systemctl restart cdk-recharge`」。
- 回滚提示里的命令也带上 `sudo`。

**取舍**：程序文件可以被服务用户改写。这是一键升级的前提，接受。

### 2.2 Docker 版

**镜像（Dockerfile）**
- 构建阶段改用 Go 1.27 和 Node 24。
- 运行阶段：
  - 用 `USER 10001`（用户名 `cdk`），不再创建 `cdk-chrome`。
  - `/app/data` 归 10001 所有。
  - `HOME` 指向 `/tmp`，Chrome 要写自己的配置。
- 版本号由构建参数写入。

**docker-compose.yml**
- 默认用发布好的镜像：`image: ghcr.io/hongheshan-svg/aisubproxy:${CDK_VERSION:-latest}`。
- 另加 `docker-compose.build.yml`，开发时从源码构建。
- 安全选项：
  - 保留 `seccomp=./deploy/chrome-seccomp.json`，它放行 Chrome 沙箱需要的 clone、unshare。
  - 增加 `cap_drop: [ALL]`、`no-new-privileges:true`、`read_only: true`。
  - `/tmp` 挂 tmpfs（Chrome 配置目录在这里），数据卷仍挂在 `/app/data`（截图也在这里）。
  - 如果实测 `read_only` 让程序起不来，就去掉这一项，并在文档里写明原因。

**docker-deploy.sh 重写**
- 不再克隆源码：
  1. 按指定版本，从 GitHub 下载该标签下的 compose 文件、seccomp 配置和 Caddyfile 模板。
  2. 首次运行生成 `.env`，其中 `JWT_SECRET`、`APP_MASTER_KEY` 都是随机值。
  3. 执行 `docker compose pull && docker compose up -d`。
- 只有安装 Docker 这一步要 root。之后，`docker` 组里的用户就能升级。
- 旧数据卷里的文件属于 root 时，执行一次 `docker run --rm -u 0 --entrypoint chown <镜像> -R 10001:10001 /app/data`。

**容器里的「版本更新」页**：提示改为 `CDK_VERSION=vX.Y.Z docker compose pull && docker compose up -d`。

## 3. 发版（`release.yml`）

推送 `v*` 标签时依次执行：

1. **checks**：调用 `ci.yml` 和 `security.yml`（两者都加 `workflow_call`），跑全部检查。
2. **guard**：标签对应的提交必须在 origin/master 上；`CHANGELOG.md` 必须有同名小节（沿用 `scripts/release-notes.sh`）。
3. **native**：沿用现有的静态构建、打包和 ed25519 签名，工具链换成 Go 1.27.x 和 Node 24。
4. **image**：
   - 用 `docker/build-push-action` 推到 `ghcr.io/hongheshan-svg/aisubproxy`。
   - 标签打 `X.Y.Z`、`X.Y`、`latest`，平台只做 `linux/amd64`。
   - 附带构建来源证明和 SBOM。
   - 加上 `org.opencontainers.image.source` 标签，把镜像关联到仓库。
   - 只有这个 job 有 `packages: write` 权限。
5. **publish**：native 和 image 都成功后，才创建 GitHub Release。正文追加 `docker pull` 命令。

需要手动做一次：第一次推送镜像后，到 GHCR 包设置里把可见性改成公开。否则服务器拉取前要先 `docker login`。

`deploy/github-release.workflow.yml` 是 `release.yml` 的副本。改成在文件头注明「以 `.github/workflows/release.yml` 为准」，内容同步。

## 4. CI（`ci.yml`）

触发：开 PR、推送到 master，以及被 `release.yml` 调用（`workflow_call`）。新的提交进来时，取消同一分支上还没跑完的那次。

| job | 内容 |
|---|---|
| `backend` | gofmt 检查、`go build`、`go vet`、golangci-lint、`go test -race`（除 paybrowser 以外的包） |
| `backend-browser` | `go test -race -timeout 30m ./internal/paybrowser/...`（真实 Chrome，单独跑，跟 `backend` 并行） |
| `frontend` | `npm ci`、`npx vitest run`、`npm run build`（Node 24） |
| `release-build` | 跑 `scripts/build-linux-backend.sh`，确认静态链接的发布构建能过 |
| `docker` | 构建镜像但不推送，用 GHA 缓存。然后冒烟测试：<br>1. 启动容器，`/health` 返回正常；<br>2. 容器内 `id -u` 不是 0；<br>3. 以 10001 带沙箱执行 `chromium --headless=new --dump-dom about:blank`，能拿到 HTML。 |
| `lint-meta` | 用 shellcheck 检查 `deploy/*.sh` 和 `scripts/*.sh`，用 actionlint 检查工作流文件 |

golangci-lint 配置（`backend/.golangci.yml`，v2 格式）：
- 只启用 `govet`、`staticcheck`、`unused`、`ineffassign`，并先把现有的 12 条修掉。
- `errcheck` 暂不启用。

Go 版本：工作流用 `1.27.x`。依赖升级如果要求提高 go.mod 里的 `go` 版本，就跟着提高。

## 5. 安全

### 5.1 `security.yml`

触发：开 PR、推送到 master、每周一定时运行，以及被 `release.yml` 调用。

| job | 内容 |
|---|---|
| `govulncheck` | 检查后端。先把 `x/net`、`x/text` 升到修复版。 |
| `npm-audit` | `npm audit --omit=dev --audit-level=high --registry=https://registry.npmjs.org` |
| `gitleaks` | 用固定版本的 gitleaks 扫描密钥。PR 只扫新增提交，定时任务扫全部历史。 |

### 5.2 CodeQL（`codeql.yml`）

- 扫 Go（autobuild，CGO 可用）和 JavaScript/TypeScript。
- 触发：开 PR、推送到 master、每周定时。

### 5.3 Dependabot（`.github/dependabot.yml`）

- 覆盖 `gomod`（/backend）、`npm`（/frontend）、`github-actions`（/）、`docker`（/，即 Dockerfile 的基础镜像）。
- 每周一次。小版本和补丁升级合成一个 PR，同时打开的 PR 最多 5 个。

### 5.4 前端依赖漏洞处理

- **小版本升级**：子代理已在 `worktree-agent-a04b524647123084a`（5ffe90a）做好，合入本分支。
- **删除没用到的 `@lobehub/icons`**：`src/` 里没有任何引用，删掉后它带来的低危漏洞链一起消失。
- **`vue-i18n` 9 → 11**：要求 Node ≥ 22，CI 改用 Node 24 正好满足。代码里用的是组合式 API（`legacy: false`），升级时核对以下几处：
  - `createI18n` 的选项；
  - 各语言文案里的消息语法；
  - 有没有用到 11 版删掉的 `v-t` 指令。
- **`xlsx` 换成 SheetJS 官方 CDN 的 0.20.x 安装包**：npm 上只有 0.18.5，没有修复版。
  - 锁文件会带完整性校验值，所以每次装到的都是同一个包。
  - Dependabot 管不到它，要人工跟进新版本。
  - 这是唯一一个不来自 registry.npmjs.org 的依赖。

### 5.5 Actions 固定版本

所有第三方 Action 都固定到提交 SHA，并在注释里写上版本号，由 Dependabot 负责更新。签名私钥只有 `release.yml` 能用，这样可以防止 Action 的标签被人改指向别的代码（供应链攻击）。

## 6. 仓库规则与协作

- **PR 模板**（`.github/pull_request_template.md`），列出以下检查项：
  - 资金安全；
  - 敏感数据不落地、不进日志；
  - 是否更新 `CHANGELOG.md`；
  - 是否影响部署或升级。
- **master 分支保护**：本分支合并、检查名称确定之后再开。
  - 必须通过的检查：
    - CI：`backend`、`backend-browser`、`frontend`、`release-build`、`docker`、`lint-meta`；
    - 安全：`govulncheck`、`npm-audit`、`gitleaks`；
    - CodeQL：Go 和 JS 两项分析。
  - 合并必须走 PR。只有一位维护者，所以要求 0 个批准。
  - 对管理员同样生效，禁止强推，禁止删除分支。
  - 不要求 PR 分支先更新到最新 master（`strict=false`）。合并后推送到 master 时会再跑一遍 CI，能兜住问题。
- **密钥扫描和推送保护**：公开仓库免费，用 `gh api` 开启。

## 7. 不做

- 完全不用 root 的部署：无 root 的 Docker、Podman，或装在用户目录。
- arm64 镜像。
- 前端类型检查（vue-tsc 要升级）和 eslint（没有配置文件）。
- golangci-lint 的 `errcheck`（50 条）。
- 缩短 paybrowser 测试耗时。另开任务，这次只把它放到单独的 job，并加长超时。
- 用 cosign 给镜像签名。这次已经有构建来源证明。

## 8. 验证

- **全部检查在 CI 上通过**：每批合入前，本地先跑通对应命令，推送后 PR 上全部检查也要通过。
- **Docker**：由 CI 里 `docker` job 的冒烟测试覆盖：非 root、`/health` 正常、Chrome 能带沙箱起来。
- **原生安装**：这里是 macOS，systemd 安装验证不了。CI 只做 shellcheck。上线前在一台全新的 Ubuntu 24.04 上手动走一遍，分两条路线：
  1. 全新安装，在后台「浏览器自检」确认 Chrome 能起来；
  2. 先装旧的 root 版，再升级到新版，确认数据都在，服务以 `cdk` 身份运行。

  这两项写进 PR 说明和 CHANGELOG 的「升级注意」。
- **发版流程**：不推正式标签。在 fork 或用临时标签验证会推到正式 GHCR 和 Release，所以这次只验证 `workflow_call` 的结构，并用 actionlint 检查。第一次真实发版时，人工在 Actions 页面看着它跑完。

## 9. CHANGELOG「升级注意」要写的内容

- 原生版升级后服务改以 `cdk` 用户运行，安装目录的所有者跟着改。重启服务要用 `sudo systemctl restart cdk-recharge`。
- Docker 版改为拉取发布好的镜像，不再在服务器上构建；容器内不是 root。旧数据卷由部署脚本自动改所有者。
- 发布包改用 Go 1.27 编译，前端改用 Node 24 构建。
