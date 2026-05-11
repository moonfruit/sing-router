#!/usr/bin/env bash
# shellcheck shell=busybox
# ============================================================
# sing-router cn ipset reloader (managed; embedded into the Go binary).
# 仅重建 cn ipset(从 $CN_IP_CIDR 指向的 cn.txt),不动 iptables 规则。
# 用于 cn.txt 变更后的轻量 reload,避免完整 reapply-rules / sing-box restart。
# ============================================================

set -eu

CN_IP_CIDR="${CN_IP_CIDR:-}"

if [ -z "$CN_IP_CIDR" ] || [ ! -f "$CN_IP_CIDR" ]; then
    echo "sing-router reload-cn-ipset: CN_IP_CIDR ($CN_IP_CIDR) missing or empty; skip"
    exit 0
fi

ipset destroy cn 2>/dev/null || true
{
    echo "create cn hash:net family inet hashsize 10240 maxelem 10240"
    awk '!/^$/ && !/^#/ {print "add cn", $0}' "$CN_IP_CIDR"
} | ipset -! restore

echo "sing-router reload-cn-ipset: cn ipset reloaded"
