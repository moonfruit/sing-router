#!/usr/bin/env bash
# shellcheck shell=busybox
# ============================================================
# sing-router startup script (managed; embedded into the Go binary).
# 全部参数从环境变量读取（由 sing-router daemon 注入），不在脚本内硬编码。
# 与 spec 第 3.4 / §10 节描述一致。
# ============================================================

set -eu

: "${DNS_PORT:?DNS_PORT not set}"
: "${REDIRECT_PORT:?REDIRECT_PORT not set}"
: "${ROUTE_MARK:?ROUTE_MARK not set}"
: "${BYPASS_MARK:?BYPASS_MARK not set}"
: "${TUN:?TUN not set}"
: "${ROUTE_TABLE:?ROUTE_TABLE not set}"
: "${PROXY_PORTS:?PROXY_PORTS not set}"
: "${FAKEIP:?FAKEIP not set}"
: "${LAN:?LAN not set}"

CN_IP_CIDR="${CN_IP_CIDR:-}"

# IPv4 保留地址 + 私有网段（reserve_ipv4）
BYPASS="0.0.0.0/8 10.0.0.0/8 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 224.0.0.0/4 240.0.0.0/4 255.255.255.255/32"

# ===================== ipset：CN IP 集合 =====================
if [ -n "$CN_IP_CIDR" ] && [ -f "$CN_IP_CIDR" ]; then
    ipset destroy cn 2>/dev/null || true
    {
        echo "create cn hash:net family inet hashsize 10240 maxelem 10240"
        awk '!/^$/ && !/^#/ {print "add cn", $0}' "$CN_IP_CIDR"
    } | ipset -! restore
fi

# ===================== 1. 路由表 =====================
ip route replace default dev "$TUN" table "$ROUTE_TABLE"
# ip rule 没有 replace 动词：先把累积的重复全删掉再 add 一条，保证幂等。
# （ip rule add 不幂等，否则每次 boot / restart / HUP 都会多堆一条重复规则。）
while ip rule del fwmark "$ROUTE_MARK" table "$ROUTE_TABLE" 2>/dev/null; do :; done
ip rule add fwmark "$ROUTE_MARK" table "$ROUTE_TABLE" 2>/dev/null || true

# ===================== 2.1 TCP 透明代理 =====================
iptables -t nat -N sing-box 2>/dev/null || iptables -t nat -F sing-box
iptables -t nat -A sing-box -p tcp --dport 53 -j RETURN
iptables -t nat -A sing-box -p udp --dport 53 -j RETURN
iptables -t nat -A sing-box -m mark --mark "$BYPASS_MARK" -j RETURN
for ip in $BYPASS; do
    iptables -t nat -A sing-box -d "$ip" -j RETURN
done
if [ -n "$CN_IP_CIDR" ] && [ -f "$CN_IP_CIDR" ]; then
    iptables -t nat -A sing-box -m set --match-set cn dst -j RETURN
fi
iptables -t nat -A sing-box -p tcp -s "$LAN" -j REDIRECT --to-ports "$REDIRECT_PORT"
iptables -t nat -C PREROUTING -p tcp -m multiport --dports "$PROXY_PORTS" -j sing-box 2>/dev/null \
    || iptables -t nat -I PREROUTING -p tcp -m multiport --dports "$PROXY_PORTS" -j sing-box
iptables -t nat -C PREROUTING -p tcp -d "$FAKEIP" -j sing-box 2>/dev/null \
    || iptables -t nat -I PREROUTING -p tcp -d "$FAKEIP" -j sing-box

# ===================== 2.2 UDP 透明代理 =====================
iptables -C FORWARD -o "$TUN" -j ACCEPT 2>/dev/null \
    || iptables -I FORWARD -o "$TUN" -j ACCEPT
iptables -t mangle -N sing-box-mark 2>/dev/null || iptables -t mangle -F sing-box-mark
iptables -t mangle -A sing-box-mark -p tcp --dport 53 -j RETURN
iptables -t mangle -A sing-box-mark -p udp --dport 53 -j RETURN
iptables -t mangle -A sing-box-mark -m mark --mark "$BYPASS_MARK" -j RETURN
for ip in $BYPASS; do
    iptables -t mangle -A sing-box-mark -d "$ip" -j RETURN
done
if [ -n "$CN_IP_CIDR" ] && [ -f "$CN_IP_CIDR" ]; then
    iptables -t mangle -A sing-box-mark -m set --match-set cn dst -j RETURN
fi
iptables -t mangle -A sing-box-mark -p udp -s "$LAN" -j MARK --set-mark "$ROUTE_MARK"
iptables -t mangle -C PREROUTING -p udp -m multiport --dports "$PROXY_PORTS" -j sing-box-mark 2>/dev/null \
    || iptables -t mangle -I PREROUTING -p udp -m multiport --dports "$PROXY_PORTS" -j sing-box-mark
iptables -t mangle -C PREROUTING -p udp -d "$FAKEIP" -j sing-box-mark 2>/dev/null \
    || iptables -t mangle -I PREROUTING -p udp -d "$FAKEIP" -j sing-box-mark

# ===================== 2.3 DNS 劫持 =====================
iptables -t nat -N sing-box-dns 2>/dev/null || iptables -t nat -F sing-box-dns
iptables -t nat -A sing-box-dns -m mark --mark "$BYPASS_MARK" -j RETURN
iptables -t nat -A sing-box-dns -p tcp -s "$LAN" -j REDIRECT --to-ports "$DNS_PORT"
iptables -t nat -A sing-box-dns -p udp -s "$LAN" -j REDIRECT --to-ports "$DNS_PORT"
iptables -t nat -C PREROUTING -p tcp --dport 53 -j sing-box-dns 2>/dev/null \
    || iptables -t nat -I PREROUTING -p tcp --dport 53 -j sing-box-dns
iptables -t nat -C PREROUTING -p udp --dport 53 -j sing-box-dns 2>/dev/null \
    || iptables -t nat -I PREROUTING -p udp --dport 53 -j sing-box-dns

# ===================== 2.4 IPv6 DNS 兜底 =====================
ip6tables -C INPUT -p tcp --dport 53 -j REJECT 2>/dev/null \
    || ip6tables -I INPUT -p tcp --dport 53 -j REJECT
ip6tables -C INPUT -p udp --dport 53 -j REJECT 2>/dev/null \
    || ip6tables -I INPUT -p udp --dport 53 -j REJECT

echo "sing-router startup: rules installed"
