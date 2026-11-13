#!/usr/bin/env bash
# CI 里跑 go test：Chrome 偶尔会在启动时卡死（进程起来了却一直不报出调试地址，
# chromedp 等满 20 秒报 "websocket url timeout reached"），和被测的代码无关。
# 碰到这种失败时，把失败的用例重跑一次；重跑还失败，或者失败里没有这句报错，照常算失败。
# 只在 CI 里用，不改变任何产品行为。
#
# 用法：scripts/ci-go-test.sh <go test 的参数...>
set -uo pipefail

marker='websocket url timeout reached'
log="$(mktemp)"
trap 'rm -f "$log"' EXIT

go test "$@" 2>&1 | tee "$log"
rc="${PIPESTATUS[0]}"
if [ "$rc" -eq 0 ]; then
  exit 0
fi
if ! grep -qF "$marker" "$log"; then
  exit "$rc"
fi

# 顶格的 "--- FAIL: 名字" 是顶层用例（子用例有缩进）；"FAIL<TAB>包<TAB>耗时" 是失败的包
tests="$(grep -E '^--- FAIL: ' "$log" | awk '{print $3}' | sort -u | paste -sd'|' -)"
pkgs="$(grep -E '^FAIL[[:space:]]+[^[:space:]]+[[:space:]]+[0-9.]+s$' "$log" | awk '{print $2}' | sort -u)"
if [ -z "$tests" ] || [ -z "$pkgs" ]; then
  exit "$rc"
fi

echo "::warning::Chrome 启动卡死（${marker}），重跑失败的用例一次：${tests}"
# shellcheck disable=SC2086 # 包名一行一个，要拆开传
go test -race -count=1 -timeout 25m -run "^(${tests})\$" $pkgs
