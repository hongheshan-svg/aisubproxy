# 发布包、一键安装与在线更新（OTA）

## 发布

推送 `v*` 标签后，GitHub Actions（`.github/workflows/release.yml`）会自动完成以下步骤，然后发布 Release：

1. 在 Linux amd64 上编译含 SQLite 的静态程序和前端；
2. 打包 `cdk-bundle-linux-amd64.tgz`（内含 `cdk-recharge` + `web/` + `VERSION`）；
3. 生成 `.sha256`；
4. 用 ed25519 私钥签名，生成 `.sig`。

发版步骤：

1. 在 `CHANGELOG.md` 最上面加一节 `## v1.5.1 - 日期`，按「新增 / 变更 / 升级注意」写这一版的更新说明。
2. 改版本号、提交、打标签并推送：

```bash
echo 1.5.1 > VERSION && git commit -am "release: v1.5.1"
git tag v1.5.1 && git push origin master v1.5.1
```

- 版本号以标签为准，`VERSION` 文件同步修改即可。
- CI 会用 `scripts/release-notes.sh` 取出 CHANGELOG 里与标签同名的那一节，作为 Release 正文（后面附 GitHub 自动生成的提交对比）。管理后台「版本更新」里显示的也是这段说明。
- **CHANGELOG 里没有这一节时，CI 会在编译之前就失败，不会发布。** 推送标签前可以先在本地检查：`bash scripts/release-notes.sh v1.5.1`。

### 签名密钥

- 私钥：仓库 Secret `RELEASE_SIGNING_KEY`（PKCS#8 PEM）。缺少这个 Secret 时，CI 拒绝发布。
- 公钥：`deploy/release-signing.pub.pem`。同一把公钥也内置在程序（`backend/internal/handler/selfupdate.go`）和 `deploy/install.sh` 里。
- 私钥要离线备份。私钥一旦丢失，就签不出已部署实例能接受的包，只能用新公钥重新安装。
- 换密钥时，要同时更新以上三处公钥和 Secret。

## 首次安装（Linux amd64 + systemd）

```bash
curl -fsSL https://raw.githubusercontent.com/hongheshan-svg/aisubproxy/master/deploy/install.sh | sudo bash
```

- 下载最新 Release，依次校验 sha256 和签名，再运行一次新程序的 `--version` 自检。
- 装到 `/opt/cdk-recharge`，生成 `app.env`（随机 `JWT_SECRET` 和 `APP_MASTER_KEY`，权限 600），注册 systemd 服务 `cdk-recharge`。
- 同时安装浏览器依赖（见下文「浏览器依赖」），失败只警告、不中断安装。升级时这一步在停服务之前做，下载和 apt 期间旧服务继续运行；apt 被别的进程占锁时最多等 5 分钟。
- 默认只监听 `127.0.0.1:9080`，需要用 Caddy 或 Nginx 反代 HTTPS。
- 首次登录走安装向导：用 `journalctl -u cdk-recharge | grep 'Setup Token'` 查到令牌后，打开 `/ops/setup`。
- 指定版本安装：`… | sudo bash -s -- v1.5.0`。
- 再次运行脚本就是升级：先停服务、备份，再替换文件，`app.env` 不动。

## 浏览器依赖（MarsWallex 自动付款）

自动付款要在服务器上用真实的 Chrome 打开付款页，需要 Chrome、Xvfb（虚拟显示）和中文字体。

### systemd 部署

升级到含 MarsWallex 兑换页的版本后，**执行一次**下面的命令，再到后台点「浏览器自检」确认通过：

```bash
curl -fsSL https://raw.githubusercontent.com/hongheshan-svg/aisubproxy/master/deploy/install.sh | sudo bash -s -- --browser-deps
```

- 后台「版本更新」只替换程序和前端，**不会**运行安装脚本，所以已部署的服务器必须手动跑这一次。全新安装时，`install.sh` 会自动做这一步。
- 脚本只做浏览器这一件事：不查发布、不下载程序、不动服务。已装的会跳过，可重复执行。
- amd64 直接下载 Google 官方 deb（约 150MB）安装，不改动 apt 源配置；其他架构改装发行版的 `chromium`。xvfb、fonts-liberation、fonts-noto-cjk 缺哪个装哪个。只支持有 `apt-get` 的系统（Debian / Ubuntu），其他系统会打印要装什么，需手动安装。
- 脚本会创建系统用户 `cdk-chrome`。服务以 root 运行时，Chrome 用这个低权限用户启动，**并保留 Chrome 沙箱**，任何情况下都不会关闭沙箱（不要用 `--no-sandbox`）。
- 脚本结尾会以 `cdk-chrome` 身份带沙箱真实启动一次 Chrome 做自检（与 systemd 的 `NoNewPrivileges=true` 同一环境）。失败时退出码非 0，并提示排查方向：`sysctl user.max_user_namespaces` 不能为 0；Ubuntu 24.04 的 `kernel.apparmor_restrict_unprivileged_userns` 由 Chrome 安装包自带的 AppArmor 配置放行。
- 服务器内存建议 ≥ 2GB（并发 1）。
- `deploy/deploy_to_server1.sh`（scp + systemctl 部署）同样不会安装浏览器，用它部署的服务器也要手动跑一次上面的命令。

### Docker 部署

- 镜像已内置 `chromium`、`xvfb`、字体和 `cdk-chrome` 用户，`CHROME_PATH=/usr/bin/chromium`。升级：`git pull` 后 `docker compose up -d --build`（镜像约增大 400MB）。
- Chrome 带沙箱运行需要创建用户命名空间，而 Docker 默认的 seccomp 配置不放行 `clone`、`unshare`，所以 `docker-compose.yml` 给 `cdk` 服务加了 `security_opt: ["seccomp=./deploy/chrome-seccomp.json"]`。自己 `docker run` 的要加 `--security-opt seccomp=deploy/chrome-seccomp.json`。
- `deploy/chrome-seccomp.json` 来自 `moby/profiles` 的 `seccomp/default.json`，末尾追加了一条 `{"names":["clone","unshare"],"action":"SCMP_ACT_ALLOW"}`。代价：容器内进程可以创建用户命名空间，比 Docker 默认配置宽松，但远小于 `seccomp=unconfined`；没有加 `SYS_ADMIN`，也没有关闭 Chrome 沙箱。

## 在线更新

在管理后台「总览 → 版本更新」点一键更新。更新流程：

1. 下载发布包；
2. 校验 sha256 和 ed25519 签名，缺少任一项都会中止；
3. 运行新程序的 `--version`，确认它能在本机执行，且版本号与发布版本一致；
4. 把当前程序、前端、`VERSION` 和数据库快照（`VACUUM INTO`）备份到 `updates/backup-时间戳/`，只保留最新 3 份；
5. 替换文件，然后原地重启进程。

Docker 部署会自动禁用在线更新，因为容器重建后会退回镜像里的版本。Docker 部署要升级，请拉取新代码后重建镜像。

## 回滚

```bash
sudo /opt/cdk-recharge/cdk-recharge rollback && sudo systemctl restart cdk-recharge
```

- 用最新一份备份恢复程序、前端和 `VERSION`，出问题的版本移到 `updates/failed-*/`。
- 数据库不会自动恢复：迁移只加不改，旧版本可以直接使用新库。如果确实需要恢复，备份就在同一个 `backup-*` 目录里。

## 本地构建

发布包必须在 Linux amd64 + gcc 环境下构建。SQLite 驱动依赖 CGO，不能用 `CGO_ENABLED=0`：那样编出来的 ELF 程序在初始化数据库时就会退出。

`scripts/build-linux-backend.sh` 生成含 SQLite 的静态程序，并且拒绝在 macOS 上直接交叉编译。macOS 可以使用仓库 Dockerfile 里的 Linux 构建环境。
