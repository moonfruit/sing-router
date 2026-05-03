#!/usr/bin/env bash
# ============================================================
# sing-router teardown script. 撤销 startup.sh 安装的所有规则。
# 幂等：每条规则用 -C 检测后再 -D；不存在不报错。
# ============================================================

set -u

: "${DNS_PORT:?DNS_PORT not set}"
: "${REDIRECT_PORT:?REDIRECT_PORT not set}"
: "${ROUTE_MARK:?ROUTE_MARK not set}"
: "${TUN:?TUN not set}"
: "${ROUTE_TABLE:?ROUTE_TABLE not set}"
: "${PROXY_PORTS:?PROXY_PORTS not set}"
: "${FAKEIP:?FAKEIP not set}"

# ---- DNS 劫持入口 ----
iptables -t nat -D PREROUTING -p tcp --dport 53 -j sing-box-dns 2>/dev/null || true
iptables -t nat -D PREROUTING -p udp --dport 53 -j sing-box-dns 2>/dev/null || true
iptables -t nat -F sing-box-dns 2>/dev/null || true
iptables -t nat -X sing-box-dns 2>/dev/null || true

# ---- TCP 入口 ----
iptables -t nat -D PREROUTING -p tcp -m multiport --dports "$PROXY_PORTS" -j sing-box 2>/dev/null || true
iptables -t nat -D PREROUTING -p tcp -d "$FAKEIP" -j sing-box 2>/dev/null || true
iptables -t nat -F sing-box 2>/dev/null || true
iptables -t nat -X sing-box 2>/dev/null || true

# ---- UDP 入口 ----
iptables -t mangle -D PREROUTING -p udp -m multiport --dports "$PROXY_PORTS" -j sing-box-mark 2>/dev/null || true
iptables -t mangle -D PREROUTING -p udp -d "$FAKEIP" -j sing-box-mark 2>/dev/null || true
iptables -t mangle -F sing-box-mark 2>/dev/null || true
iptables -t mangle -X sing-box-mark 2>/dev/null || true

# ---- TUN forward ----
iptables -D FORWARD -o "$TUN" -j ACCEPT 2>/dev/null || true

# ---- IPv6 DNS 兜底 ----
ip6tables -D INPUT -p tcp --dport 53 -j REJECT 2>/dev/null || true
ip6tables -D INPUT -p udp --dport 53 -j REJECT 2>/dev/null || true

# ---- 路由表 + rule ----
ip rule del fwmark "$ROUTE_MARK" table "$ROUTE_TABLE" 2>/dev/null || true
ip route flush table "$ROUTE_TABLE" 2>/dev/null || true

# ---- ipset ----
ipset destroy cn 2>/dev/null || true

echo "sing-router teardown: rules removed"
