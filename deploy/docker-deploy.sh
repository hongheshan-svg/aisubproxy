#!/usr/bin/env bash
# CDK Recharge System — Docker 一键部署
# 用法:
#   curl -fsSL <raw-url>/deploy/docker-deploy.sh | bash -s -- --domain gpt66.cc
#   或本地: ./deploy/docker-deploy.sh --domain gpt66.cc
set -euo pipefail

DOMAIN=""
REPO_URL="https://github.com/zovocard/zovo_card_cdk_auto.git"
INSTALL_DIR="/opt/cdk-recharge"

usage() {
  echo "用法: $0 [选项]"
  echo "  --domain DOMAIN    绑定域名（如 gpt66.cc），不传则用 :80 纯 IP 访问"
  echo "  --dir DIR          安装目录（默认 /opt/cdk-recharge）"
  echo "  --skip-build       跳过 Docker 构建（已有镜像时）"
  exit 1
}

SKIP_BUILD=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain) DOMAIN="$2"; shift 2 ;;
    --dir)    INSTALL_DIR="$2"; shift 2 ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    -h|--help) usage ;;
    *) echo "未知参数: $1"; usage ;;
  esac
done

echo "=========================================="
echo "  CDK Recharge System — Docker 部署"
echo "=========================================="
echo "  域名: ${DOMAIN:-'(无，HTTP 模式)'}"
echo "  目录: ${INSTALL_DIR}"
echo

# 1) 检查 Docker
if ! command -v docker &>/dev/null; then
  echo "[1/6] 安装 Docker..."
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
else
  echo "[1/6] Docker 已安装 ✓"
fi

# docker compose (v2 plugin)
if docker compose version &>/dev/null; then
  DC="docker compose"
elif command -v docker-compose &>/dev/null; then
  DC="docker-compose"
else
  echo "[1/6] 安装 docker-compose-plugin..."
  apt-get update -qq && apt-get install -y -qq docker-compose-plugin 2>/dev/null \
    || yum install -y docker-compose-plugin 2>/dev/null \
    || { echo "请手动安装 docker compose plugin"; exit 1; }
  DC="docker compose"
fi
echo "  compose: $DC ✓"

# 2) 拉取/更新代码
if [[ -d "$INSTALL_DIR/.git" ]]; then
  echo "[2/6] 更新代码..."
  cd "$INSTALL_DIR"
  git pull --ff-only origin master 2>/dev/null || git pull origin master
else
  echo "[2/6] 克隆代码..."
  git clone "$REPO_URL" "$INSTALL_DIR"
  cd "$INSTALL_DIR"
fi

# 3) 生成 JWT Secret（首次）
JWT_FILE="$INSTALL_DIR/.jwt_secret"
if [[ ! -f "$JWT_FILE" ]]; then
  JWT_SECRET=$(openssl rand -hex 32)
  echo "$JWT_SECRET" > "$JWT_FILE"
  chmod 600 "$JWT_FILE"
  echo "[3/6] 生成 JWT Secret ✓"
else
  JWT_SECRET=$(cat "$JWT_FILE")
  echo "[3/6] JWT Secret 已存在 ✓"
fi

# 3.5) APP_MASTER_KEY（用于加密 MarsWallex 等密钥；丢失或更换后已保存的密钥无法解密）
# 容器实际使用 .env 中的值：.env 已有非空 APP_MASTER_KEY 时一律沿用，绝不另生成一个不同的。
MASTER_KEY_FILE="$INSTALL_DIR/.master_key"
ENV_FILE="$INSTALL_DIR/.env"
ENV_MASTER_KEY=""
if [[ -f "$ENV_FILE" ]]; then
  # 取最后一行 APP_MASTER_KEY= 的值，去掉首尾空白（含 \r）与成对引号
  ENV_MASTER_KEY=$(grep -E '^APP_MASTER_KEY=' "$ENV_FILE" | tail -n 1 | cut -d= -f2- \
    | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' || true)
  re_dq='^"(.*)"$'
  re_sq="^'(.*)'$"
  if [[ "$ENV_MASTER_KEY" =~ $re_dq || "$ENV_MASTER_KEY" =~ $re_sq ]]; then
    ENV_MASTER_KEY="${BASH_REMATCH[1]}"
  fi
fi
write_master_key_file() {
  # umask 077：文件从创建起就只有属主可读，没有全员可读的窗口
  (umask 077 && printf '%s\n' "$1" > "$MASTER_KEY_FILE")
  chmod 600 "$MASTER_KEY_FILE"
}
if [[ -n "$ENV_MASTER_KEY" ]]; then
  APP_MASTER_KEY="$ENV_MASTER_KEY"
  if [[ ! -f "$MASTER_KEY_FILE" ]]; then
    write_master_key_file "$APP_MASTER_KEY"
    echo "[3/6] 沿用 .env 中的 APP_MASTER_KEY，已备份到 .master_key ✓"
  else
    if [[ "$(cat "$MASTER_KEY_FILE")" != "$APP_MASTER_KEY" ]]; then
      echo "  警告: .master_key 与 .env 中的 APP_MASTER_KEY 不一致；容器使用 .env 中的值，请人工核对"
    fi
    echo "[3/6] APP_MASTER_KEY 已存在 ✓"
  fi
elif [[ -f "$MASTER_KEY_FILE" ]]; then
  APP_MASTER_KEY=$(cat "$MASTER_KEY_FILE")
  echo "[3/6] APP_MASTER_KEY 已存在 ✓"
else
  APP_MASTER_KEY=$(openssl rand -base64 32)
  write_master_key_file "$APP_MASTER_KEY"
  echo "[3/6] 生成 APP_MASTER_KEY ✓"
fi

# 4) 写入 .env（docker compose 自动读取）
if [[ ! -f "$ENV_FILE" ]]; then
  (
    umask 077
    cat > "$ENV_FILE" <<EOF
JWT_SECRET=${JWT_SECRET}
APP_MASTER_KEY=${APP_MASTER_KEY}
INSTALL_MODE=wizard
EOF
  )
  chmod 600 "$ENV_FILE"
  echo "[4/6] 创建 .env ✓"
else
  # 追加前确保文件以换行结尾，避免新行与最后一行粘连
  if [[ -s "$ENV_FILE" && -n "$(tail -c 1 "$ENV_FILE")" ]]; then
    echo >> "$ENV_FILE"
  fi
  # 确保 JWT_SECRET 存在
  if ! grep -q "^JWT_SECRET=" "$ENV_FILE"; then
    echo "JWT_SECRET=${JWT_SECRET}" >> "$ENV_FILE"
  fi
  # 确保 APP_MASTER_KEY 存在且非空：空的 APP_MASTER_KEY= 行就地填入，没有该行则追加
  if [[ -z "$ENV_MASTER_KEY" ]]; then
    if grep -q "^APP_MASTER_KEY=" "$ENV_FILE"; then
      ENV_TMP=$(mktemp "$ENV_FILE.XXXXXX")
      # 用环境变量传值，密钥不出现在进程参数里
      K="$APP_MASTER_KEY" awk '
        /^APP_MASTER_KEY=/ {
          v = substr($0, 16)
          gsub(/^[ \t\r]+|[ \t\r]+$/, "", v)
          if (v == "" || v == "\"\"" || v == "\047\047") { print "APP_MASTER_KEY=" ENVIRON["K"]; next }
        }
        { print }
      ' "$ENV_FILE" > "$ENV_TMP"
      chmod 600 "$ENV_TMP"
      mv "$ENV_TMP" "$ENV_FILE"
      echo "  .env 中的 APP_MASTER_KEY 为空，已填入 ✓"
    else
      echo "APP_MASTER_KEY=${APP_MASTER_KEY}" >> "$ENV_FILE"
    fi
  fi
  echo "[4/6] .env 已存在 ✓"
fi

# 5) 配置 Caddyfile
echo "[5/6] 配置 Caddy..."
if [[ -n "$DOMAIN" ]]; then
  cat > "$INSTALL_DIR/Caddyfile" <<CADDY
${DOMAIN} {
    encode zstd gzip

    @blocked path /.env* /app.env* /.git/* /data/*
    handle @blocked {
        respond 404
    }

    reverse_proxy cdk:8080 {
        header_up X-Real-IP {client_ip}
        header_up X-Forwarded-Proto {scheme}
        header_up X-Forwarded-Host {host}
    }
}
CADDY
  echo "  域名: ${DOMAIN} (自动 HTTPS) ✓"
else
  cat > "$INSTALL_DIR/Caddyfile" <<CADDY
:80 {
    encode zstd gzip

    @blocked path /.env* /app.env* /.git/* /data/*
    handle @blocked {
        respond 404
    }

    reverse_proxy cdk:8080 {
        header_up X-Real-IP {client_ip}
        header_up X-Forwarded-Proto {scheme}
        header_up X-Forwarded-Host {host}
    }
}
CADDY
  echo "  HTTP 模式 (:80) ✓"
fi

# docker-compose.yml 注入 JWT_SECRET
# docker compose 会自动从 .env 读取变量，但我们需要把它传给容器
# 修改 docker-compose.yml 中的 environment 部分添加 JWT_SECRET
# 通过 .env 文件 + docker-compose env_file 实现
if ! grep -q "env_file" "$INSTALL_DIR/docker-compose.yml" 2>/dev/null; then
  sed -i '/environment:/i\    env_file:\n      - .env' "$INSTALL_DIR/docker-compose.yml" 2>/dev/null || true
fi

# 6) 构建 & 启动
echo "[6/6] 构建并启动容器..."
cd "$INSTALL_DIR"
if [[ "$SKIP_BUILD" -eq 0 ]]; then
  $DC build --no-cache cdk
fi
$DC up -d

echo
echo "=========================================="
echo "  部署完成！"
echo "=========================================="
if [[ -n "$DOMAIN" ]]; then
  echo "  访问: https://${DOMAIN}"
  echo "  请确保域名 DNS 已指向本机 IP"
else
  echo "  访问: http://$(curl -s ifconfig.me 2>/dev/null || echo '<本机IP>'):80"
fi
echo "  首次打开进入安装向导，设置管理员账号和卡台 API Key"
echo
echo "  常用命令:"
echo "    cd ${INSTALL_DIR}"
echo "    $DC logs -f          # 查看日志"
echo "    $DC restart           # 重启"
echo "    $DC down              # 停止"
echo "    $DC up -d --build     # 更新后重建"
echo
