#!/usr/bin/env bash
# 一键安装 / 升级 / 回滚（Linux amd64 + systemd）。
#
#   安装或升级到最新版：curl -fsSL https://raw.githubusercontent.com/hongheshan-svg/aisubproxy/master/deploy/install.sh | sudo bash
#   指定版本：        curl -fsSL …/install.sh | sudo bash -s -- v1.5.0
#   回滚上一个版本：  sudo /opt/cdk-recharge/cdk-recharge rollback && sudo systemctl restart cdk-recharge
#                     （本脚本在本地时也可：sudo bash install.sh rollback）
#
# 每个发布包都会校验 sha256 与 ed25519 签名（公钥内置于本脚本与程序），并先运行一次新程序确认能启动，
# 任何一步失败都不改动已安装的文件。装好后以后的升级在管理后台「版本更新」里一键完成。
#
# 可选环境变量：
#   CDK_GITHUB_REPO   发布仓库（默认 hongheshan-svg/aisubproxy）
#   CDK_INSTALL_DIR   安装目录（默认 /opt/cdk-recharge）
#   CDK_PORT          首次安装时的监听端口（默认 9080，只监听 127.0.0.1，由 Caddy/Nginx 反代 HTTPS）
#   CDK_RELEASE_DIR   离线安装：从本地目录取发布包（目录里放 VERSION、cdk-bundle-linux-amd64.tgz 及 .sha256/.sig）
#   CDK_SKIP_SYSTEMD  设为 1 时不操作 systemd（容器内测试用）
set -euo pipefail

REPO="${CDK_GITHUB_REPO:-hongheshan-svg/aisubproxy}"
INSTALL_DIR="${CDK_INSTALL_DIR:-/opt/cdk-recharge}"
PORT="${CDK_PORT:-9080}"
SERVICE=cdk-recharge
BUNDLE=cdk-bundle-linux-amd64.tgz
SKIP_SYSTEMD="${CDK_SKIP_SYSTEMD:-0}"
TMP_DIR=""

# 发布签名公钥（与 deploy/release-signing.pub.pem、程序内置公钥是同一把）
RELEASE_PUBKEY='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEA+surxd3pW5IwYSjEfHcrfYVVAQmpvBi1GSHskolwV0Q=
-----END PUBLIC KEY-----'

log() { printf '==> %s\n' "$*"; }
die() { printf '错误：%s\n' "$*" >&2; exit 1; }

systemd_on() { [[ "$SKIP_SYSTEMD" != "1" ]]; }

check_env() {
  [[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]] || die "只支持 Linux x86_64（发布包为 linux/amd64）"
  if systemd_on; then
    [[ $EUID -eq 0 ]] || die "请用 root 运行（sudo bash install.sh）"
    command -v systemctl >/dev/null || die "需要 systemd；Docker 部署请用 deploy/docker-deploy.sh"
  fi
  local c
  for c in curl tar openssl sha256sum base64 grep sed awk; do
    command -v "$c" >/dev/null || die "缺少命令 $c，请先安装"
  done
  local major
  major="$(openssl version | awk '{print $2}' | cut -d. -f1)"
  [[ "$major" =~ ^[0-9]+$ && "$major" -ge 3 ]] || die "需要 OpenSSL 3 及以上以校验 ed25519 签名（当前：$(openssl version)）"
}

# fetch_release <tag|latest> <dir>：把发布包、校验文件、签名下载到 dir，输出版本号
fetch_release() {
  local want="$1" dir="$2"
  if [[ -n "${CDK_RELEASE_DIR:-}" ]]; then
    local f
    for f in "$BUNDLE" "$BUNDLE.sha256" "$BUNDLE.sig" VERSION; do
      cp "$CDK_RELEASE_DIR/$f" "$dir/$f" || die "离线目录缺少 $f"
    done
    tr -d ' \n\r' < "$dir/VERSION"
    return
  fi
  local api json tag
  if [[ "$want" == latest ]]; then
    api="https://api.github.com/repos/$REPO/releases/latest"
  else
    api="https://api.github.com/repos/$REPO/releases/tags/v${want#v}"
  fi
  json="$(curl -fsSL -H 'Accept: application/vnd.github+json' "$api")" || die "查询发布失败：$api"
  tag="$(printf '%s' "$json" | grep -o '"tag_name": *"[^"]*"' | head -n1 | sed 's/.*"\([^"]*\)"$/\1/')"
  [[ -n "$tag" ]] || die "发布信息里没有版本号"
  local name url
  for name in "$BUNDLE" "$BUNDLE.sha256" "$BUNDLE.sig"; do
    url="$(printf '%s' "$json" | grep -o "\"browser_download_url\": *\"[^\"]*/$name\"" | head -n1 | sed 's/.*"\(https[^"]*\)"$/\1/')"
    [[ -n "$url" ]] || die "发布 $tag 缺少文件 $name"
    curl -fsSL -o "$dir/$name" "$url" || die "下载 $name 失败"
  done
  printf '%s' "${tag#v}"
}

# verify_and_extract <dir> <version>：sha256 → 签名 → 解压 → 运行新程序自检
verify_and_extract() {
  local dir="$1" ver="$2" want got
  want="$(awk '{print tolower($1)}' "$dir/$BUNDLE.sha256")"
  got="$(sha256sum "$dir/$BUNDLE" | awk '{print $1}')"
  [[ -n "$want" && "$want" == "$got" ]] || die "sha256 不一致，已中止（文件可能损坏）"
  printf '%s\n' "$RELEASE_PUBKEY" > "$dir/pub.pem"
  base64 -d < "$dir/$BUNDLE.sig" > "$dir/bundle.sig.bin" 2>/dev/null || die "签名文件格式不对，已中止"
  openssl pkeyutl -verify -pubin -inkey "$dir/pub.pem" -rawin -in "$dir/$BUNDLE" -sigfile "$dir/bundle.sig.bin" >/dev/null 2>&1 \
    || die "签名校验失败，已中止（不是本项目发布的包）"
  mkdir -p "$dir/x"
  tar -xzf "$dir/$BUNDLE" -C "$dir/x"
  [[ -f "$dir/x/cdk-recharge" && -d "$dir/x/web" ]] || die "发布包内容不完整"
  chmod 755 "$dir/x/cdk-recharge"
  local self
  self="$(cd "$dir/x" && env -u CDK_APP_VERSION ./cdk-recharge --version 2>&1 | head -n1 | tr -d ' \r')" \
    || die "新程序无法在本机运行：$self"
  [[ "${self#v}" == "${ver#v}" ]] || die "新程序自报版本 ${self} 与发布版本 ${ver} 不一致，已中止"
}

write_app_env() {
  local envf="$INSTALL_DIR/app.env"
  [[ -f "$envf" ]] && return
  umask 077
  cat > "$envf" <<EOF
# 由 install.sh 首次安装时生成。APP_MASTER_KEY 丢失后已保存的 MarsWallex 密钥无法解密，请另行备份本文件。
SERVER_HOST=127.0.0.1
SERVER_PORT=$PORT
SERVER_MODE=release
DB_PATH=$INSTALL_DIR/data/cdk_recharge.db
WEB_DIR=$INSTALL_DIR/web
CDK_INSTALL_DIR=$INSTALL_DIR
JWT_SECRET=$(openssl rand -hex 32)
JWT_EXPIRATION_HOURS=24
APP_MASTER_KEY=$(openssl rand -base64 32)
INSTALL_MODE=wizard
SETUP_ALLOW_CIDRS=
TRUSTED_PROXIES=127.0.0.1
UPDATE_ENABLED=1
EOF
  chmod 600 "$envf"
  log "已生成 $envf（含随机 JWT_SECRET 与 APP_MASTER_KEY）"
}

write_unit() {
  cat > "/etc/systemd/system/$SERVICE.service" <<EOF
[Unit]
Description=CDK Recharge System (MarsWallex)
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=-$INSTALL_DIR/app.env
ExecStart=$INSTALL_DIR/cdk-recharge
Restart=on-failure
RestartSec=3
LimitNOFILE=65535
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable "$SERVICE" >/dev/null
}

# backup_current：停服后把当前程序、前端、版本号、数据库复制到 updates/backup-<时间>
backup_current() {
  local bak
  bak="$INSTALL_DIR/updates/backup-$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$bak"
  cp -a "$INSTALL_DIR/cdk-recharge" "$bak/"
  [[ -d "$INSTALL_DIR/web" ]] && cp -a "$INSTALL_DIR/web" "$bak/"
  [[ -f "$INSTALL_DIR/VERSION" ]] && cp -a "$INSTALL_DIR/VERSION" "$bak/"
  local db="$INSTALL_DIR/data/cdk_recharge.db" s
  if [[ -f "$db" ]]; then
    for s in "" -wal -shm; do
      [[ -f "$db$s" ]] && cp -a "$db$s" "$bak/cdk_recharge.db$s"
    done
  fi
  # 只保留最新 3 个备份
  ls -1d "$INSTALL_DIR"/updates/backup-* 2>/dev/null | sort | head -n -3 | xargs -r rm -rf
  log "已备份当前版本到 $bak"
}

install_files() {
  local src="$1"
  local tmpweb="$INSTALL_DIR/web.next"
  rm -rf "$tmpweb"
  cp -a "$src/web" "$tmpweb"
  rm -rf "$INSTALL_DIR/web"
  mv "$tmpweb" "$INSTALL_DIR/web"
  cp "$src/cdk-recharge" "$INSTALL_DIR/cdk-recharge.new"
  chmod 755 "$INSTALL_DIR/cdk-recharge.new"
  mv -f "$INSTALL_DIR/cdk-recharge.new" "$INSTALL_DIR/cdk-recharge"
  cp "$src/VERSION" "$INSTALL_DIR/VERSION"
}

wait_healthy() {
  local _
  for _ in $(seq 1 30); do
    if curl -fsS -o /dev/null "http://127.0.0.1:$(grep -E '^SERVER_PORT=' "$INSTALL_DIR/app.env" | cut -d= -f2)/api/v1"; then
      return 0
    fi
    sleep 1
  done
  return 1
}

do_rollback() {
  [[ -x "$INSTALL_DIR/cdk-recharge" ]] || die "$INSTALL_DIR 下没有已安装的程序"
  CDK_INSTALL_DIR="$INSTALL_DIR" WEB_DIR="$INSTALL_DIR/web" "$INSTALL_DIR/cdk-recharge" rollback
  if systemd_on; then
    systemctl restart "$SERVICE"
    log "已重启 $SERVICE"
  fi
}

main() {
  local arg="${1:-latest}"
  if [[ "$arg" == rollback ]]; then
    do_rollback
    return
  fi
  check_env
  local tmp ver upgrading=0
  TMP_DIR="$(mktemp -d)"
  tmp="$TMP_DIR"
  trap 'rm -rf "${TMP_DIR:-}"' EXIT
  log "查询发布（$REPO，${arg}）…"
  ver="$(fetch_release "$arg" "$tmp")"
  log "校验 v$ver（sha256、签名、自检）…"
  verify_and_extract "$tmp" "$ver"

  mkdir -p "$INSTALL_DIR/data" "$INSTALL_DIR/updates"
  if [[ -f "$INSTALL_DIR/cdk-recharge" ]]; then
    upgrading=1
    if systemd_on; then systemctl stop "$SERVICE" 2>/dev/null || true; fi
    backup_current
  fi
  install_files "$tmp/x"
  write_app_env
  # 留一份安装脚本，方便以后 `bash $INSTALL_DIR/install.sh rollback`
  if [[ -f "${BASH_SOURCE[0]:-}" ]]; then cp -f "${BASH_SOURCE[0]}" "$INSTALL_DIR/install.sh" 2>/dev/null || true; fi

  if systemd_on; then
    write_unit
    systemctl restart "$SERVICE"
    if wait_healthy; then
      log "v$ver 已启动"
    else
      printf '新版本 30 秒内未通过健康检查。查看日志：journalctl -u %s -n 100\n回滚：sudo %s/cdk-recharge rollback && sudo systemctl restart %s\n' "$SERVICE" "$INSTALL_DIR" "$SERVICE" >&2
      exit 1
    fi
  fi

  if [[ $upgrading -eq 1 ]]; then
    log "已升级到 v$ver。"
  else
    cat <<EOF
==> 已安装 v$ver 到 $INSTALL_DIR（只监听 127.0.0.1:$PORT）。
下一步：
  1. 配置 HTTPS 反代，例如 Caddy（/etc/caddy/Caddyfile）：
       your.domain.com {
           reverse_proxy 127.0.0.1:$PORT
       }
  2. 查看首次安装令牌：journalctl -u $SERVICE | grep 'Setup Token'
     然后打开 https://your.domain.com/ops/setup 创建管理员。
  3. 备份 $INSTALL_DIR/app.env（其中 APP_MASTER_KEY 丢失后，已保存的 MarsWallex 密钥无法解密）。
以后在管理后台「版本更新」里一键升级；新版本起不来时：sudo $INSTALL_DIR/cdk-recharge rollback && sudo systemctl restart $SERVICE
EOF
  fi
}

main "$@"
