#!/usr/bin/env bash
# Docker 镜像冒烟测试：用 docker-compose.yml 里同样的安全选项启动容器，依次检查
#   1. /health 正常；
#   2. /app/data 可写；
#   3. 以 cdk-chrome 身份、带沙箱用 Chrome 打开 about:blank 能拿到 HTML。
# 用法：scripts/docker-smoke.sh <镜像名>
# 容器选项与 docker-compose.yml 一致（只有 seccomp）。注意：绝不使用 --no-sandbox。Ubuntu 24.04 的 CI 机器需先执行
#   sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
set -euo pipefail

IMAGE="${1:?用法：$0 <镜像名>}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SUFFIX="$$-$(date +%s)"
NAME="cdk-smoke-$SUFFIX"
VOLUME="cdk-smoke-data-$SUFFIX"

cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker volume rm -f "$VOLUME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() {
  printf '失败：%s\n' "$*" >&2
  printf '--- 容器日志（末尾 40 行）---\n' >&2
  docker logs --tail 40 "$NAME" >&2 2>&1 || true
  exit 1
}

echo "==> 启动容器（镜像 ${IMAGE}）"
docker run -d --name "$NAME" \
  --security-opt "seccomp=$ROOT/deploy/chrome-seccomp.json" \
  -v "$VOLUME:/app/data" \
  -e JWT_SECRET="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')" \
  -e APP_MASTER_KEY="$(head -c 32 /dev/urandom | base64)" \
  "$IMAGE" >/dev/null

echo "==> 1/3 等待 /health"
ok=0
for _ in $(seq 1 60); do
  if [[ "$(docker inspect -f '{{.State.Running}}' "$NAME")" != true ]]; then
    fail "容器已退出"
  fi
  if docker exec "$NAME" curl -fsS -o /dev/null http://127.0.0.1:8080/health 2>/dev/null; then
    ok=1
    break
  fi
  sleep 1
done
[[ $ok -eq 1 ]] || fail "60 秒内 /health 未就绪"
echo "    /health 正常"

echo "==> 2/3 /app/data 可写"
docker exec "$NAME" sh -c 'touch /app/data/.smoke && rm -f /app/data/.smoke' || fail "/app/data 不可写"
echo "    可写"

echo "==> 3/3 Chrome 以 cdk-chrome 身份带沙箱启动（与程序实际启动方式一致）"
out="$(docker exec "$NAME" sh -c '
  set -e
  home="$(mktemp -d /tmp/smoke.XXXXXX)"
  chown cdk-chrome:cdk-chrome "$home"
  cd /
  HOME="$home" timeout 60 setpriv --no-new-privs --reuid=cdk-chrome --regid=cdk-chrome --clear-groups \
    chromium --headless=new --disable-gpu --no-first-run --user-data-dir="$home/profile" --dump-dom about:blank 2>&1
' 2>&1)" || true
if [[ "$out" != *"<html"* ]]; then
  printf '%s\n' "$out" | grep -v -- 'no-sandbox' | head -n 15 >&2
  fail "Chrome 没能带沙箱启动（输出里没有 <html）"
fi
echo "    Chrome 沙箱启动成功"

echo "冒烟测试全部通过"
