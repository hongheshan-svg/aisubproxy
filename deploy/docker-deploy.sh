#!/usr/bin/env bash
# CDK Recharge System — Docker 一键部署 / 升级
#
# 不再克隆源码、不在服务器上构建：按版本从 GitHub 下载该标签下的 docker-compose.yml、
# deploy/chrome-seccomp.json、Caddyfile 模板，然后 docker compose pull && up -d。
# 容器内程序以 root 运行，启动 Chrome 时降权到 cdk-chrome 用户并保留沙箱。
#
# 用法:
#   curl -fsSL https://raw.githubusercontent.com/hongheshan-svg/aisubproxy/master/deploy/docker-deploy.sh | bash -s -- --domain gpt66.cc
#   或本地: ./deploy/docker-deploy.sh --domain gpt66.cc
#
# 权限：安装 Docker 需要 root；Docker 已装好时，docker 组里的用户也可运行本脚本
# （安装目录需对该用户可写，默认 /opt/cdk-recharge：sudo install -d -o "$USER" /opt/cdk-recharge）。
#
# 环境变量:
#   CDK_VERSION   要部署的版本（如 v1.6.0 或 1.6.0）；不设则取最新 Release
#   CDK_GITHUB_REPO  发布仓库（默认 hongheshan-svg/aisubproxy）
set -euo pipefail

REPO="${CDK_GITHUB_REPO:-hongheshan-svg/aisubproxy}"
IMAGE_REPO="ghcr.io/hongheshan-svg/aisubproxy"
DOMAIN=""
INSTALL_DIR="/opt/cdk-recharge"
WANT_VERSION="${CDK_VERSION:-}"

usage() {
  echo "用法: $0 [选项]"
  echo "  --domain DOMAIN    绑定域名（如 gpt66.cc）；不传则保持现有 Caddyfile，没有时用 :80 纯 IP 访问"
  echo "  --dir DIR          安装目录（默认 /opt/cdk-recharge）"
  echo "  --version VER      部署指定版本（如 v1.6.0），默认最新 Release；也可用环境变量 CDK_VERSION"
  echo "  --skip-build       已无意义（不再在服务器上构建），忽略"
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) DOMAIN="${2:?--domain 需要参数}"; shift 2 ;;
    --dir)    INSTALL_DIR="${2:?--dir 需要参数}"; shift 2 ;;
    --version) WANT_VERSION="${2:?--version 需要参数}"; shift 2 ;;
    --skip-build) echo "提示: --skip-build 已无意义（现在直接拉取发布好的镜像，不在服务器上构建），已忽略"; shift ;;
    -h|--help) usage ;;
    *) echo "未知参数: $1"; usage ;;
  esac
done

log() { printf '%s\n' "$*"; }
die() { printf '错误：%s\n' "$*" >&2; exit 1; }

# env_value <文件> <键>：取最后一行 键=值 的值，去掉首尾空白（含 \r）与成对引号
env_value() {
  local v re_dq='^"(.*)"$' re_sq="^'(.*)'$"
  [[ -f "$1" ]] || return 0
  v="$(grep -E "^$2=" "$1" | tail -n 1 | cut -d= -f2- | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' || true)"
  if [[ "$v" =~ $re_dq || "$v" =~ $re_sq ]]; then v="${BASH_REMATCH[1]}"; fi
  printf '%s' "$v"
}

# set_env_value <文件> <键> <值>：已有该键就原地替换，否则追加；权限保持 600，密钥不出现在进程参数里
set_env_value() {
  local f="$1" k="$2" tmp
  tmp="$(mktemp "$f.XXXXXX")"
  if grep -q "^$k=" "$f"; then
    K="$k" V="$3" awk '
      index($0, ENVIRON["K"] "=") == 1 { print ENVIRON["K"] "=" ENVIRON["V"]; next }
      { print }
    ' "$f" > "$tmp"
  else
    cat "$f" > "$tmp"
    if [[ -s "$tmp" && -n "$(tail -c 1 "$tmp")" ]]; then echo >> "$tmp"; fi
    printf '%s=%s\n' "$k" "$3" >> "$tmp"
  fi
  chmod 600 "$tmp"
  mv "$tmp" "$f"
}

echo "=========================================="
echo "  CDK Recharge System — Docker 部署"
echo "=========================================="
echo "  域名: ${DOMAIN:-'(未指定)'}"
echo "  目录: ${INSTALL_DIR}"
echo

# 1) Docker（只有安装 Docker 需要 root）
if ! command -v docker &>/dev/null; then
  [[ $EUID -eq 0 ]] || die "这台机器没有安装 Docker，安装 Docker 需要 root：请先用 root 或 sudo 运行一次本脚本"
  echo "[1/6] 安装 Docker..."
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
  if [[ -n "${SUDO_USER:-}" && "$SUDO_USER" != root ]]; then
    usermod -aG docker "$SUDO_USER" && echo "  已把 ${SUDO_USER} 加入 docker 组（重新登录后生效，之后无需 sudo）"
  fi
else
  echo "[1/6] Docker 已安装 ✓"
fi

if docker compose version &>/dev/null; then
  DC="docker compose"
elif command -v docker-compose &>/dev/null; then
  DC="docker-compose"
else
  [[ $EUID -eq 0 ]] || die "缺少 docker compose 插件，安装它需要 root"
  echo "[1/6] 安装 docker-compose-plugin..."
  { apt-get update -qq && apt-get install -y -qq docker-compose-plugin; } 2>/dev/null \
    || yum install -y docker-compose-plugin 2>/dev/null \
    || die "请手动安装 docker compose plugin"
  DC="docker compose"
fi
echo "  compose: $DC ✓"
docker info >/dev/null 2>&1 || die "当前用户无权访问 Docker（需要 root 或 docker 组成员）"

# 2) 确定版本
if [[ -z "$WANT_VERSION" || "$WANT_VERSION" == latest ]]; then
  echo "[2/6] 查询最新 Release..."
  TAG="$(curl -fsSL -H 'Accept: application/vnd.github+json' "https://api.github.com/repos/$REPO/releases/latest" \
    | grep -o '"tag_name": *"[^"]*"' | head -n1 | sed 's/.*"\([^"]*\)"$/\1/' || true)"
  [[ -n "$TAG" ]] || die "查询最新 Release 失败；可用 --version vX.Y.Z 指定版本"
else
  TAG="v${WANT_VERSION#v}"
fi
IMAGE_TAG="${TAG#v}"   # 镜像标签不带 v
IMAGE="${IMAGE_REPO}:${IMAGE_TAG}"
RAW="https://raw.githubusercontent.com/$REPO/$TAG"
echo "[2/6] 版本 ${TAG}（镜像 ${IMAGE}）"

# 3) 下载该标签下的部署文件
if [[ ! -d "$INSTALL_DIR" ]]; then
  mkdir -p "$INSTALL_DIR" 2>/dev/null \
    || die "无法创建 ${INSTALL_DIR}；请先执行：sudo install -d -o \"\$USER\" ${INSTALL_DIR}"
fi
[[ -w "$INSTALL_DIR" ]] || die "${INSTALL_DIR} 对当前用户不可写；请执行：sudo chown \"\$USER\" ${INSTALL_DIR}"
mkdir -p "$INSTALL_DIR/deploy"
cd "$INSTALL_DIR"

fetch() { # fetch <相对路径> <目标>
  local tmp
  tmp="$(mktemp "$2.XXXXXX")"
  if ! curl -fsSL -o "$tmp" "$RAW/$1"; then
    rm -f "$tmp"
    die "下载 $RAW/$1 失败"
  fi
  mv "$tmp" "$2"
}

if [[ -f docker-compose.yml ]]; then
  # 老安装目录是 git 克隆、compose 里是 build: .；保留一份备份，方便找回自己的改动
  cp -f docker-compose.yml docker-compose.yml.bak
fi
fetch docker-compose.yml docker-compose.yml
fetch deploy/chrome-seccomp.json deploy/chrome-seccomp.json
CADDY_TMPL="$(mktemp)"
trap 'rm -f "$CADDY_TMPL"' EXIT
curl -fsSL -o "$CADDY_TMPL" "$RAW/deploy/Caddyfile.docker.tmpl" || die "下载 Caddyfile 模板失败"
echo "[3/6] 已下载 docker-compose.yml、chrome-seccomp.json、Caddyfile 模板 ✓"

# 4) .env：已有的不覆盖，只补缺失的密钥
ENV_FILE="$INSTALL_DIR/.env"
if [[ ! -f "$ENV_FILE" ]]; then
  (umask 077 && : > "$ENV_FILE")
  chmod 600 "$ENV_FILE"
  echo "INSTALL_MODE=wizard" >> "$ENV_FILE"
  created=1
else
  created=0
fi
if [[ -z "$(env_value "$ENV_FILE" JWT_SECRET)" ]]; then
  # 兼容老安装留下的 .jwt_secret
  old_jwt=""
  [[ -f "$INSTALL_DIR/.jwt_secret" ]] && old_jwt="$(tr -d ' \r\n' < "$INSTALL_DIR/.jwt_secret")"
  set_env_value "$ENV_FILE" JWT_SECRET "${old_jwt:-$(openssl rand -hex 32)}"
  echo "  .env 补上 JWT_SECRET ✓"
fi
if [[ -z "$(env_value "$ENV_FILE" APP_MASTER_KEY)" ]]; then
  # 丢失或更换 APP_MASTER_KEY 后，已保存的 MarsWallex 密钥无法解密：老安装的 .master_key 优先沿用
  old_key=""
  [[ -f "$INSTALL_DIR/.master_key" ]] && old_key="$(tr -d '\r\n' < "$INSTALL_DIR/.master_key")"
  set_env_value "$ENV_FILE" APP_MASTER_KEY "${old_key:-$(openssl rand -base64 32)}"
  echo "  .env 补上 APP_MASTER_KEY ✓（请备份 ${ENV_FILE}）"
fi
set_env_value "$ENV_FILE" CDK_VERSION "$IMAGE_TAG"
chmod 600 "$ENV_FILE"
if [[ $created -eq 1 ]]; then echo "[4/6] 已创建 .env（权限 600）✓"; else echo "[4/6] .env 已存在，未覆盖 ✓"; fi

# 5) Caddyfile：给了域名就按域名重新生成；没给域名时保留已有的
if [[ -n "$DOMAIN" ]]; then
  sed "s|__SITE_ADDRESS__|${DOMAIN}|" "$CADDY_TMPL" > Caddyfile
  echo "[5/6] Caddy: ${DOMAIN}（自动 HTTPS）✓"
elif [[ -f Caddyfile ]]; then
  echo "[5/6] Caddy: 沿用现有 Caddyfile ✓"
else
  sed "s|__SITE_ADDRESS__|:80|" "$CADDY_TMPL" > Caddyfile
  echo "[5/6] Caddy: HTTP 模式 (:80) ✓"
fi

# 6) 拉取镜像并启动
echo "[6/6] 拉取镜像并启动..."
$DC pull

$DC up -d

echo "  等待健康检查..."
healthy=0
for _ in $(seq 1 60); do
  st="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' cdk-recharge 2>/dev/null || echo missing)"
  if [[ "$st" == healthy ]]; then healthy=1; break; fi
  sleep 2
done
if [[ $healthy -ne 1 ]]; then
  $DC logs --tail 40 cdk || true
  die "2 分钟内容器未通过健康检查（状态：${st:-?}）。查看日志：cd ${INSTALL_DIR} && ${DC} logs -f cdk"
fi

echo
echo "=========================================="
echo "  部署完成！版本 ${TAG}"
echo "=========================================="
if [[ -n "$DOMAIN" ]]; then
  echo "  访问: https://${DOMAIN}"
  echo "  请确保域名 DNS 已指向本机 IP"
else
  echo "  访问: http://$(curl -s ifconfig.me 2>/dev/null || echo '<本机IP>'):80"
fi
echo "  首次打开进入安装向导，设置管理员账号和卡台 API Key"
echo "  请备份 ${ENV_FILE}（其中 APP_MASTER_KEY 丢失后，已保存的 MarsWallex 密钥无法解密）"
echo
echo "  常用命令:"
echo "    cd ${INSTALL_DIR}"
echo "    $DC logs -f                       # 查看日志"
echo "    $DC restart                       # 重启"
echo "    $DC down                          # 停止"
echo "    升级: 重跑本脚本，或 CDK_VERSION=X.Y.Z $DC pull && CDK_VERSION=X.Y.Z $DC up -d"
echo "    docker exec cdk-recharge /app/cdk-recharge rollback && docker restart cdk-recharge   # 回滚在线升级"
echo
