#!/usr/bin/env bash
# 一键安装 / 升级 / 回滚（Linux amd64 + systemd）。
#
#   安装或升级到最新版：curl -fsSL https://raw.githubusercontent.com/hongheshan-svg/aisubproxy/master/deploy/install.sh | sudo bash
#   指定版本：        curl -fsSL …/install.sh | sudo bash -s -- v1.5.0
#   回滚上一个版本：  sudo /opt/cdk-recharge/cdk-recharge rollback && sudo systemctl restart cdk-recharge
#                     （本脚本在本地时也可：sudo bash install.sh rollback）
#   只安装浏览器依赖：curl -fsSL …/install.sh | sudo bash -s -- --browser-deps
#                     （MarsWallex 自动付款需要 Chrome、Xvfb、字体；后台「版本更新」不会装它们，升级后须手动跑一次）
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

# Chrome 候选路径，须与 backend/internal/paybrowser/chrome.go 的 chromeCandidates（Linux 部分）一致
CHROME_CANDIDATES=(/usr/bin/google-chrome-stable /usr/bin/google-chrome /usr/bin/chromium /usr/bin/chromium-browser)
# 服务以 root 运行时，Chrome 改用这个低权限用户启动并保留沙箱
CHROME_USER=cdk-chrome
CHROME_DEB_URL=https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb
BROWSER_DEPS_CMD="curl -fsSL https://raw.githubusercontent.com/hongheshan-svg/aisubproxy/master/deploy/install.sh | sudo bash -s -- --browser-deps"

log() { printf '==> %s\n' "$*"; }
warn() { printf '警告：%s\n' "$*" >&2; }
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
    command -v "$c" >/dev/null || die "缺少命令 ${c}，请先安装"
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
APP_MASTER_KEY=$(openssl rand -base64 32)
INSTALL_MODE=wizard
SETUP_ALLOW_CIDRS=
TRUSTED_PROXIES=127.0.0.1
UPDATE_ENABLED=1
EOF
  chmod 600 "$envf"
  log "已生成 ${envf}（含随机 JWT_SECRET 与 APP_MASTER_KEY）"
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
# 停止时程序先等在途付款单（最多 3 分钟），多留余量再 SIGKILL
TimeoutStopSec=240
# SIGTERM 只发给主进程，不同时杀掉在途单的 Chrome、Xvfb
KillMode=mixed
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

# find_chrome：打印第一个存在的 Chrome 路径；一个都没有返回 1
find_chrome() {
  local c
  for c in "${CHROME_CANDIDATES[@]}"; do
    [[ -f "$c" && -x "$c" ]] || continue
    # Ubuntu 的 /usr/bin/chromium(-browser) 可能只是 snap 过渡桩（脚本里有 /snap/）：不算已有浏览器，
    # 会改装 Google Chrome（Go 端候选列表里 google-chrome 在前，优先选它）
    if [[ "$(head -c 2 "$c" 2>/dev/null </dev/null)" == "#!" ]] && head -c 4096 "$c" 2>/dev/null </dev/null | grep -qa '/snap/'; then
      continue
    fi
    printf '%s' "$c"
    return 0
  done
  return 1
}

# pkg_installed <包名>：dpkg 认为已安装；xvfb 另外认 PATH 里的 Xvfb（不是 deb 装的也算）
pkg_installed() {
  # 只认完全装好的：dpkg -s 对半装（unpacked、half-configured、config-failed）也返回 0
  if dpkg-query -W -f='${Status}' "$1" 2>/dev/null </dev/null | grep -q '^install ok installed$'; then return 0; fi
  [[ "$1" == xvfb ]] && command -v Xvfb >/dev/null 2>&1
}

# apt_run：就地设非交互；NEEDRESTART_MODE=l 只列出需要重启的服务、不自动重启（不动服务器上别的服务）。
# -o DPkg::Lock::Timeout=300：unattended-upgrades、cloud-init 常占着 dpkg 锁，等它最多 5 分钟。
apt_run() {
  DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l apt-get -o DPkg::Lock::Timeout=300 "$@" </dev/null
}

ensure_chrome_user() {
  if id -u "$CHROME_USER" >/dev/null 2>&1 </dev/null; then return 0; fi
  command -v useradd >/dev/null 2>&1 || { warn "缺少 useradd，无法创建系统用户 ${CHROME_USER}"; return 1; }
  useradd --system --no-create-home --shell /usr/sbin/nologin "$CHROME_USER" </dev/null \
    || { warn "创建系统用户 ${CHROME_USER} 失败"; return 1; }
  log "已创建系统用户 ${CHROME_USER}（用来运行 Chrome）"
}

# sandbox_selftest <chrome 路径>：以 cdk-chrome 身份、带沙箱真实启动一次 Chrome。
# --no-new-privs 与 systemd 的 NoNewPrivileges=true 同一环境。失败返回 1；没有 setpriv 时跳过（不算失败）。
sandbox_selftest() {
  local chrome="$1" ver uid gid home out
  ver="$(timeout 15 "$chrome" --version 2>/dev/null </dev/null | head -n1)" || ver=""
  ver="$(printf '%s' "$ver" | sed 's/ *built on.*$//; s/ *$//')"
  if ! command -v setpriv >/dev/null 2>&1 || ! command -v timeout >/dev/null 2>&1; then
    warn "缺少 setpriv 或 timeout，跳过沙箱自检；请在后台「浏览器自检」确认。Chrome：${ver:-$chrome}"
    return 0
  fi
  uid="$(id -u "$CHROME_USER")" || return 1
  gid="$(id -g "$CHROME_USER")" || return 1
  home="$(mktemp -d /tmp/cdk-chrome-selftest.XXXXXX)" || return 1
  chown "$uid:$gid" "$home" && chmod 700 "$home" || { rm -rf "$home"; return 1; }
  if out="$(cd / && HOME="$home" timeout 30 setpriv --no-new-privs --reuid="$uid" --regid="$gid" --clear-groups \
      "$chrome" --headless=new --disable-gpu --no-first-run --user-data-dir="$home/profile" --dump-dom about:blank 2>&1 </dev/null)" \
    && [[ "$out" == *"<html"* ]]; then
    rm -rf "$home"
    log "${ver:-Chrome}：沙箱自检通过（以 ${CHROME_USER} 带沙箱启动成功）"
    return 0
  fi
  rm -rf "$home"
  {
    printf '错误：Chrome 沙箱自检失败，自动付款无法在这台机器上运行。Chrome 输出（节选）：\n'
    # Chrome 原文末尾会劝人 "try using --no-sandbox"，那句话不能照抄出来
    printf '%s\n' "$out" | grep -iE 'sandbox|namespace|fatal|error' | sed 's/ *If you want to live dangerously.*$//' \
      | grep -v -- 'no-sandbox' | head -n 8 | sed 's/^/    /'
    printf '原因通常是服务器或容器禁止了用户命名空间。排查方向：\n'
    printf '  1. sysctl user.max_user_namespaces（不能为 0）；\n'
    printf '  2. Ubuntu 24.04：sysctl kernel.apparmor_restrict_unprivileged_userns，Chrome 安装包自带的 AppArmor 配置应放行它；\n'
    printf '  3. 在容器里：容器的 seccomp 配置要放行 clone、unshare（使用仓库的 deploy/chrome-seccomp.json）。\n'
    printf '不要用 --no-sandbox 绕过：那会让付款页面的脚本直接运行在服务器权限下。\n'
  } >&2
  return 1
}

# install_browser_deps：安装 Chrome、Xvfb、字体，创建 cdk-chrome 用户，并做一次沙箱自检。已装的跳过。
# 注意：本函数常在 if/|| 条件里被调用，那时 set -e 失效，所以每个可能失败的命令都显式 || return 1；
# 且所有外部命令都 </dev/null，否则 `curl … | bash` 时会吃掉 stdin 上还没读到的脚本。
install_browser_deps() {
  [[ $EUID -eq 0 ]] || { warn "安装浏览器依赖需要 root 权限"; return 1; }

  local mem_kb
  mem_kb="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null </dev/null)" || mem_kb=""
  # 标称 2GB 的机器实际显示约 1.9GB，所以阈值取 1.8GB
  if [[ "$mem_kb" =~ ^[0-9]+$ && "$mem_kb" -lt 1800000 ]]; then
    warn "内存约 $((mem_kb / 1024))MB，自动付款建议服务器内存 ≥ 2GB（并发 1）"
  fi

  local chrome="" need_chrome=0 pkgs=() p
  chrome="$(find_chrome)" || need_chrome=1
  for p in xvfb fonts-liberation fonts-noto-cjk; do
    pkg_installed "$p" || pkgs+=("$p")
  done

  if [[ $need_chrome -eq 1 || ${#pkgs[@]} -gt 0 ]]; then
    if ! command -v apt-get >/dev/null 2>&1; then
      {
        printf '这台机器没有 apt-get，无法自动安装浏览器依赖。请手动安装：\n'
        printf '  - Google Chrome 或 Chromium（路径之一：%s）\n' "${CHROME_CANDIDATES[*]}"
        printf '  - Xvfb（有界面模式需要）\n'
        printf '  - 字体：Liberation、Noto CJK\n'
        printf '装好后再运行：%s\n' "$BROWSER_DEPS_CMD"
      } >&2
      ensure_chrome_user >/dev/null 2>&1 || true # 尽力创建，失败不影响上面的提示
      return 1
    fi
    ensure_chrome_user || return 1
    local deb_dir="" deb=""
    if [[ $need_chrome -eq 1 ]]; then
      if [[ "$(uname -m)" == x86_64 ]]; then
        log "下载 Google Chrome 官方安装包（约 150MB）…"
        deb_dir="$(mktemp -d)" || return 1
        chmod 755 "$deb_dir" || { rm -rf "$deb_dir"; return 1; }
        deb="$deb_dir/google-chrome-stable_current_amd64.deb"
        if ! curl -fsSL --connect-timeout 15 --retry 3 --retry-delay 2 --max-time 900 -o "$deb" "$CHROME_DEB_URL" </dev/null; then
          rm -rf "$deb_dir"
          warn "下载 Chrome 失败：$CHROME_DEB_URL"
          return 1
        fi
        chmod 644 "$deb" || { rm -rf "$deb_dir"; return 1; }
      else
        log "$(uname -m) 架构没有 Google Chrome 官方包，改装发行版的 chromium"
        pkgs+=(chromium)
      fi
    fi
    log "安装浏览器依赖（apt-get）…"
    if ! apt_run update; then
      warn "apt-get update 失败，30 秒后重试一次"
      sleep 30
      if ! apt_run update; then
        [[ -z "$deb_dir" ]] || rm -rf "$deb_dir"
        warn "apt-get update 仍然失败"
        return 1
      fi
    fi
    local rc=0
    if [[ -n "$deb" ]]; then
      apt_run install -y --no-install-recommends "$deb" ${pkgs[@]+"${pkgs[@]}"} || rc=$?
    else
      apt_run install -y --no-install-recommends "${pkgs[@]}" || rc=$?
    fi
    [[ -z "$deb_dir" ]] || rm -rf "$deb_dir"
    if [[ $rc -ne 0 ]]; then
      warn "apt-get install 失败（退出码 $rc）"
      return 1
    fi
    chrome="$(find_chrome)" || { warn "安装完成但仍找不到 Chrome"; return 1; }
  else
    ensure_chrome_user || return 1
    log "浏览器依赖已齐全，跳过安装（${chrome}）"
  fi

  sandbox_selftest "$chrome" || return 1
  log "浏览器依赖就绪。请到管理后台点「浏览器自检」确认。"
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
  if [[ "$arg" == --browser-deps ]]; then
    # 只装浏览器依赖：不查发布、不下载程序、不动服务
    [[ $EUID -eq 0 ]] || die "请用 root 运行：${BROWSER_DEPS_CMD}"
    install_browser_deps || die "浏览器依赖未装好，见上面的提示"
    return
  fi
  check_env
  local tmp ver upgrading=0
  TMP_DIR="$(mktemp -d)"
  tmp="$TMP_DIR"
  trap 'rm -rf "${TMP_DIR:-}"' EXIT
  log "查询发布（${REPO}，${arg}）…"
  ver="$(fetch_release "$arg" "$tmp")"
  log "校验 v${ver}（sha256、签名、自检）…"
  verify_and_extract "$tmp" "$ver"

  # 自动付款需要的 Chrome、Xvfb、字体。装不上只警告，不影响程序本身升级；放在停服之前，下载和 apt 期间旧服务继续运行
  if ! install_browser_deps; then
    warn "浏览器依赖未装好，MarsWallex 自动付款暂不可用。解决后单独重跑：${BROWSER_DEPS_CMD}"
  fi

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
    log "已升级到 v${ver}。"
  else
    cat <<EOF
==> 已安装 v$ver 到 ${INSTALL_DIR}（只监听 127.0.0.1:${PORT}）。
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
