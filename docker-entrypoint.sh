#!/bin/sh
set -e

# HEADFUL=1 时启动 Xvfb 虚拟显示，让 Chromium 以有头模式运行。
# 有头模式能显著降低 Cloudflare / OpenAI 对 headless 浏览器的 403 拦截。
if [ "${HEADFUL:-0}" = "1" ]; then
    if ! command -v Xvfb >/dev/null 2>&1; then
        echo "[entrypoint] HEADFUL=1 但镜像内没有 Xvfb，退出" >&2
        exit 1
    fi
    Xvfb :99 -screen 0 "${XVFB_SCREEN:-1920x1080x24}" -nolisten tcp >/tmp/xvfb.log 2>&1 &
    XVFB_PID=$!
    export DISPLAY=:99
    # 等 X socket 就绪
    for i in 1 2 3 4 5 6 7 8 9 10; do
        [ -S /tmp/.X11-unix/X99 ] && break
        sleep 0.3
    done
    if [ ! -S /tmp/.X11-unix/X99 ]; then
        echo "[entrypoint] Xvfb 启动失败，日志：" >&2
        cat /tmp/xvfb.log >&2 || true
        exit 1
    fi
    echo "[entrypoint] Xvfb 已启动 :99 (${XVFB_SCREEN:-1920x1080x24})，DISPLAY=$DISPLAY"
    trap 'kill "$XVFB_PID" 2>/dev/null || true' TERM INT
else
    echo "[entrypoint] HEADFUL=0，使用 headless Chromium"
fi

exec node ota/launcher.js
