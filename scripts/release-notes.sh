#!/usr/bin/env bash
# 从 CHANGELOG.md 取出指定版本的更新说明，供 CI 作为 GitHub Release 的说明。
#   用法：scripts/release-notes.sh v1.5.0 [CHANGELOG 路径] > notes.md
# 找不到「## v1.5.0」这一节、或这一节没有内容时退出码为 1（CI 据此拒绝发布）。
set -euo pipefail

TAG="${1:?用法: release-notes.sh <vX.Y.Z> [CHANGELOG.md]}"
VER="${TAG#v}"
FILE="${2:-$(cd "$(dirname "$0")/.." && pwd)/CHANGELOG.md}"
[[ -f "$FILE" ]] || { echo "找不到 $FILE" >&2; exit 1; }

# 取「## vX.Y.Z」（标题后可跟日期）到下一个「## 」之间的内容，去掉首尾空行
notes="$(awk -v ver="$VER" '
  /^## / {
    if (inside) exit
    h = $2; sub(/^v/, "", h)
    if (h == ver) { inside = 1; next }
  }
  inside { lines[++n] = $0 }
  END {
    first = 1; last = n
    while (first <= n && lines[first] ~ /^[[:space:]]*$/) first++
    while (last >= first && lines[last] ~ /^[[:space:]]*$/) last--
    for (i = first; i <= last; i++) print lines[i]
  }
' "$FILE")"

if [[ -z "${notes//[[:space:]]/}" ]]; then
  echo "CHANGELOG.md 里没有 v$VER 的更新说明（需要一节「## v${VER}」），拒绝发布" >&2
  exit 1
fi
printf '%s\n' "$notes"
