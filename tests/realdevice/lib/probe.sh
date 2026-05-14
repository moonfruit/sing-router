#!/bin/sh
# tests/realdevice/lib/probe.sh
# 双目标连通性探针。busybox-ash 兼容；在 LAN 客户端上执行（流量经路由器转发）。
# 输出恰好一行两个 token：<direct> <proxy>，各为 OK 或 FAIL。
#   - direct（默认 https://www.baidu.com）：任意时刻都应可达 —— FAIL 即「黑洞/断网」信号
#   - proxy （默认 https://www.google.com）：仅当 sing-box 代理正常工作时可达
# 用法：sh probe.sh [curl_timeout_seconds]   默认 5
# URL 可经环境变量 PROBE_DIRECT_URL / PROBE_PROXY_URL 覆盖（主要供单测用）。
#
# 强制 IPv4（-4）：sing-router 的透明代理与 LAN→WAN NAT 全是 IPv4-only（startup.sh
# 用 iptables/REDIRECT，ip6tables 只做 DNS REJECT）。若不锁 IPv4，像 baidu 这样的
# 双栈站点会经 IPv6 直连命中 —— IPv6 是纯路由转发、不经 NAT —— 从而把「固件
# MASQUERADE 被冲掉」「IPv4 透明代理链丢失」这类故障整个掩盖掉，探针报 OK 实则断网。
set -u
TIMEOUT="${1:-5}"
DIRECT_URL="${PROBE_DIRECT_URL:-https://www.baidu.com}"
PROXY_URL="${PROBE_PROXY_URL:-https://www.google.com}"

_check() {  # <url> -> OK|FAIL（拿到任意 HTTP 响应即 OK，连接失败/超时即 FAIL）
    if command -v curl >/dev/null 2>&1; then
        if curl -4 -sS -m "$TIMEOUT" -o /dev/null "$1" 2>/dev/null; then echo OK; else echo FAIL; fi
    else
        # wget 兜底（需支持 https 与 -4；GNU wget / 较新 busybox 均可）
        if wget -4 -q -T "$TIMEOUT" -O /dev/null "$1" 2>/dev/null; then echo OK; else echo FAIL; fi
    fi
}

echo "$(_check "$DIRECT_URL") $(_check "$PROXY_URL")"
