# 发布包、一键安装与在线更新（OTA）

## 发布

推送 `v*` 标签后，GitHub Actions（`.github/workflows/release.yml`）会自动完成以下步骤，然后发布 Release：

1. 在 Linux amd64 上编译含 SQLite 的静态程序和前端；
2. 打包 `cdk-bundle-linux-amd64.tgz`（内含 `cdk-recharge` + `web/` + `VERSION`）；
3. 生成 `.sha256`；
4. 用 ed25519 私钥签名，生成 `.sig`。

```bash
echo 1.5.1 > VERSION && git commit -am "release: v1.5.1"
git tag v1.5.1 && git push origin master v1.5.1
```

版本号以标签为准，`VERSION` 文件同步修改即可。

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
- 默认只监听 `127.0.0.1:9080`，需要用 Caddy 或 Nginx 反代 HTTPS。
- 首次登录走安装向导：用 `journalctl -u cdk-recharge | grep 'Setup Token'` 查到令牌后，打开 `/ops/setup`。
- 指定版本安装：`… | sudo bash -s -- v1.5.0`。
- 再次运行脚本就是升级：先停服务、备份，再替换文件，`app.env` 不动。

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
