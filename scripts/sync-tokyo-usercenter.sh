#!/bin/sh
# 把 Cartoon 的会员中心页面模板原样同步进 Tokyo。
#
# 为什么要复制：会员中心页面用相对路径 include "../Common/Header.html"，
# 只有页面文件位于 Tokyo 目录下，才会套上 Tokyo 自己的 Common/Header.html（顶栏 + 样式）。
# 页面本身的结构和 JS 不改，所以 acg-faka 升级后重新跑一遍本脚本即可跟上官方改动。
#
# 不会覆盖 Tokyo 自己写的文件：Common/Header.html、Authentication/*、Index/*。
#
# 用法：scripts/sync-tokyo-usercenter.sh [Cartoon 目录] [Tokyo 目录]
set -eu
ROOT=$(cd "$(dirname "$0")/.." && pwd)
SRC=${1:-"$ROOT/app/View/User/Theme/Cartoon"}
DST=${2:-"$ROOT/app/View/User/Theme/Tokyo"}

for dir in User Dashboard Agent; do
    mkdir -p "$DST/$dir"
    cp "$SRC/$dir/"*.html "$DST/$dir/"
done
mkdir -p "$DST/Common"
cp "$SRC/Common/Footer.html" "$SRC/Common/SecurityNav.html" "$DST/Common/"

echo "synced from $SRC -> $DST"
