#!/usr/bin/env bash
# scripts/fetch-asset.sh URL TARGET
#
# 通用 etag 增量下载：
#   - 比较旁路 ${TARGET}.etag → 命中返回 304 时跳过更新
#   - 写入 ${TARGET}.new；非空才 mv 到 TARGET，确保原子
#   - stdout 简短报告：updated / "already up to date"
#
# 用于 Makefile 的 update-cn / update-rule-sets / update-all 等 target。
set -euo pipefail

if [ "$#" -ne 2 ]; then
    echo "usage: $0 URL TARGET" >&2
    exit 2
fi

url=$1
target=$2
etag="${target}.etag"
tmp="${target}.new"

mkdir -p "$(dirname "$target")"
rm -f "$tmp"

curl -fsSL --retry 3 \
    --etag-compare "$etag" \
    --etag-save    "$etag" \
    -o "$tmp" "$url"

if [ -s "$tmp" ]; then
    mv "$tmp" "$target"
    if [ -t 1 ] || true; then
        size=$(wc -c < "$target")
        echo "${target} updated (${size} bytes)"
    fi
else
    rm -f "$tmp"
    echo "${target} already up to date (HTTP 304)"
fi
