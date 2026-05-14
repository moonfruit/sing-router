#!/bin/sh
# tests/realdevice/lib/probe.sh
# 双目标连通性探针。busybox-ash 兼容；在 LAN 客户端上执行（流量经路由器转发）。
# 输出恰好一行两个 token：<direct> <proxy>，各为 OK 或 FAIL。
#   - direct（默认 https://www.baidu.com）：任意时刻都应可达 —— FAIL 即「黑洞/断网」信号
#   - proxy （默认 https://www.google.com）：仅当 sing-box 代理正常工作时可达
# 用法：sh probe.sh [curl_timeout_seconds]   默认 5
# URL 可经环境变量 PROBE_DIRECT_URL / PROBE_PROXY_URL 覆盖（主要供单测用）。
set -u
TIMEOUT="${1:-5}"
DIRECT_URL="${PROBE_DIRECT_URL:-https://www.baidu.com}"
PROXY_URL="${PROBE_PROXY_URL:-https://www.google.com}"

_check() {  # <url> -> OK|FAIL（拿到任意 HTTP 响应即 OK，连接失败/超时即 FAIL）
    if command -v curl >/dev/null 2>&1; then
        if curl -sS -m "$TIMEOUT" -o /dev/null "$1" 2>/dev/null; then echo OK; else echo FAIL; fi
    else
        # busybox wget 兜底（需支持 https）
        if wget -q -T "$TIMEOUT" -O /dev/null "$1" 2>/dev/null; then echo OK; else echo FAIL; fi
    fi
}

echo "$(_check "$DIRECT_URL") $(_check "$PROXY_URL")"
