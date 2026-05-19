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
: "${LAN_IFACE:?LAN_IFACE not set}"

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
# INPUT 拦"以路由器本身为 DNS"的 v6 查询；FORWARD 拦 LAN 客户端绕过路由器
# 直接发往公网 IPv6 DNS（这条流量不经 INPUT），两条缺一不可。
# FORWARD 用 `-i "$LAN_IFACE"` 收窄到 LAN 网桥入向流量，避免误拦 VPN、
# 访客网或 WAN 侧端口转发到内网 DNS 的合法转发路径。INPUT 不需要 -i：
# 命中即"目的为路由器本身"。
#
# REJECT 的回包语义模拟"端口无人监听"——
#   - TCP 显式 `--reject-with tcp-reset`：发 RST，与真实关闭端口行为一致；
#     默认 REJECT 对 TCP 也回 ICMP unreachable，那个对客户端来说像"网络
#     错误"，部分 stub resolver 会延迟重试。
#   - UDP 走默认 REJECT 即可：默认 reject-with 本来就是 `icmp-port-unreachable`
#     (v6 是 `icmp6-port-unreachable`)，正是内核对未绑定 UDP 端口的标准
#     回应；显式写出来纯属冗余。
# 这两个组合让客户端 stub resolver 立即放弃这个 nameserver 转 IPv4，避免
# happy-eyeballs / 超时拖慢解析。
#
# 已知副作用：旧版默认 REJECT（TCP 也回 ICMP unreachable）时 PS Portal
# (PSP) 的 NAT 类型检测会判失败。切到 TCP tcp-reset 后 TCP 行为像普通
# 关闭端口，理论上 PSP 检测应改善，但未实测。如仍受影响再切 DROP。
ip6tables -C INPUT -p tcp --dport 53 -j REJECT --reject-with tcp-reset 2>/dev/null \
    || ip6tables -I INPUT -p tcp --dport 53 -j REJECT --reject-with tcp-reset
ip6tables -C INPUT -p udp --dport 53 -j REJECT 2>/dev/null \
    || ip6tables -I INPUT -p udp --dport 53 -j REJECT
ip6tables -C FORWARD -i "$LAN_IFACE" -p tcp --dport 53 -j REJECT --reject-with tcp-reset 2>/dev/null \
    || ip6tables -I FORWARD -i "$LAN_IFACE" -p tcp --dport 53 -j REJECT --reject-with tcp-reset
ip6tables -C FORWARD -i "$LAN_IFACE" -p udp --dport 53 -j REJECT 2>/dev/null \
    || ip6tables -I FORWARD -i "$LAN_IFACE" -p udp --dport 53 -j REJECT

# ===================== 2.5 DoT / DoQ 阻断 (853) =====================
# 防止 LAN 客户端用 DoT (tcp/853) 或 DoQ-DTLS (udp/853) 绕过 53 端口劫持，
# 逼其回退到 plain DNS 让 sing-box 接管。同 2.4 节 REJECT 语义：TCP 显式
# tcp-reset、UDP 走默认（默认即 icmp[6]-port-unreachable，无需显式）。
# 注意：DoH (tcp/443) 无法在端口层封堵，否则连带拆掉 HTTPS；那条要靠
# DNS / SNI 层黑名单解决。
# 只挂 FORWARD：路由器本身不对外提供 DoT/DoQ 服务，无 INPUT 风险面。
# 用 `-i "$LAN_IFACE"` 收窄到 LAN 入向，避免误伤 VPN / 访客网 / WAN 端口
# 转发的合法 853 流量。v4 + v6 双栈同处理。
for cmd in iptables ip6tables; do
    $cmd -C FORWARD -i "$LAN_IFACE" -p tcp --dport 853 -j REJECT --reject-with tcp-reset 2>/dev/null \
        || $cmd -I FORWARD -i "$LAN_IFACE" -p tcp --dport 853 -j REJECT --reject-with tcp-reset
    $cmd -C FORWARD -i "$LAN_IFACE" -p udp --dport 853 -j REJECT 2>/dev/null \
        || $cmd -I FORWARD -i "$LAN_IFACE" -p udp --dport 853 -j REJECT
done

echo "sing-router startup: rules installed"
