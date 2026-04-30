#!/usr/bin/env bash
# ============================================================
# 条件：iptables + sing-box + 仅透明代理局域网 IPv4 常用端口 +
#       混合模式(TCP→REDIRECT, UDP→TUN) + Mix DNS(fake-ip+CN直解) + Bypass CN IP
# 对应 ShellCrash 变量近似值：
#   firewall_mod=iptables   firewall_area=1   lan_proxy=true
#   redir_mod=混合模式      dns_mod=mix       cn_ip_route=ON
#   common_ports=ON         ipv6_redir=OFF
# 说明：dns_mod=mix 下，CN 域名走 dns_direct 拿真实 IP，非 CN 域名走 dns_fakeip
#       拿 28.0.0.0/8 假地址。所以防火墙层同时需要：
#         (a) cn set RETURN —— 短路真实 CN IP 流量直连
#         (b) -d $FAKEIP 入口 —— 兜住非常用端口的 fake-ip 流量
# ============================================================

# ===================== 变量 =====================
DNS_PORT=1053                   # sing-box dns-in 监听端口（DNS 劫持目标）
REDIRECT_PORT=7892              # sing-box redirect-in 监听端口（TCP REDIRECT 目标）
ROUTE_MARK=0x7892               # UDP 走 TUN 用的策略路由 mark
BYPASS_MARK=0x7890              # sing-box 自身出站打的 mark（防回环）
TUN=utun                        # sing-box TUN 接口名
ROUTE_TABLE=111                 # 策略路由专用表号
PROXY_PORTS=22,80,443,8080,8443 # common_ports=ON 时的常用端口列表
FAKEIP=28.0.0.0/8               # sing-box fake-ip IPv4 段（与 dns.json inet4_range 一致）
LAN=192.168.50.0/24             # 局域网 IPv4 网段（host_ipv4）
CN_IP_CIDR=cn.txt               # CN-IP CIDR 列表路径（每行一条 CIDR，# 与空行视作注释）

# IPv4 保留地址 + 私有网段（reserve_ipv4）：本机/链路本地/组播/广播 + 全部 RFC1918 私网，
# 都不应进入代理。注意 192.168.0.0/16 已覆盖 $LAN，所以下面 TCP/UDP 链里去掉了 -d $LAN -j RETURN。
BYPASS="0.0.0.0/8 10.0.0.0/8 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.168.0.0/16 224.0.0.0/4 240.0.0.0/4 255.255.255.255/32"

# ===================== ipset：CN IP 集合 =====================
# mix 模式下 CN 域名拿到的是真实 IP，防火墙用此 set 做 RETURN 短路 CN 流量直连，绕过 sing-box
# 仅当 $CN_IP_CIDR 存在时启用 cn 集合及对应的 RETURN 规则；否则 CN bypass 完全交给 sing-box 出站
if [ -f "$CN_IP_CIDR" ]; then
    ipset destroy cn 2>/dev/null # 清掉旧的 set，避免 create 报 already exists
    {
        echo "create cn hash:net family inet hashsize 10240 maxelem 10240" # 声明 set：IPv4 网段集合，预分配 10240 桶/上限
        awk '!/^$/ && !/^#/ {print "add cn", $0}' "$CN_IP_CIDR"            # 过滤空行/注释，逐行转 add 命令
    } | ipset -! restore                                                   # -! 等价 --exist，重复元素不报错；通过管道一次性灌入，避免临时文件
fi

# ===================== 1. 路由表（fw_start.sh:18-32） =====================
ip route add default dev $TUN table $ROUTE_TABLE  # 专用路由表的默认出口指向 TUN，UDP 打 mark 后走这里进 sing-box
ip rule add fwmark $ROUTE_MARK table $ROUTE_TABLE # 命中 fwmark 的包查上面这张表，从而被导入 utun

# ===================== 2. iptables 防火墙 =====================

# ---- 2.1 TCP 透明代理：nat/PREROUTING 自定义链 sing-box ----
iptables -t nat -N sing-box                                       # 新建 TCP 代理跳转链
iptables -t nat -A sing-box -p tcp --dport 53 -j RETURN           # 53/TCP 让位给后面的 sing-box-dns 链劫持，避免被 REDIRECT 到代理端口
iptables -t nat -A sing-box -p udp --dport 53 -j RETURN           # 同上，链中通用过滤；TCP 链放着也无害
iptables -t nat -A sing-box -m mark --mark $BYPASS_MARK -j RETURN # sing-box 自己发出的包带 BYPASS_MARK，直接放行防回环
for ip in $BYPASS; do
    iptables -t nat -A sing-box -d "$ip" -j RETURN # 跳过 IPv4 保留地址 + RFC1918 私网（含本机网段）
done
# mix 模式下 ShellCrash 加入 cn set 的 RETURN（fw_iptables.sh:43，dns_mod != fake-ip 满足）
# 因为 mix 下 CN 域名经 dns_direct 解析得到的是真实 IP，命中 cn set 直接放行直连，无需进 sing-box
# 仅当 cn.txt 存在且 cn 集合已灌入时才下发；否则跳过这条规则，CN bypass 完全交给 sing-box 出站
[ -f "$CN_IP_CIDR" ] &&
    iptables -t nat -A sing-box -m set --match-set cn dst -j RETURN                 # 目的 IP 属 CN → RETURN 直连，绕过 sing-box
iptables -t nat -A sing-box -p tcp -s $LAN -j REDIRECT --to-ports $REDIRECT_PORT    # 仅来自局域网的 TCP 重定向到 sing-box redirect-in
iptables -t nat -I PREROUTING -p tcp -m multiport --dports $PROXY_PORTS -j sing-box # 入口①：常用端口的 TCP 进入代理链（common_ports=ON）
iptables -t nat -I PREROUTING -p tcp -d $FAKEIP -j sing-box                         # 入口②：非 CN 域名经 dns_fakeip 拿到 28.x，靠这条把这些流量兜进代理链

# ---- 2.2 UDP 透明代理：mangle/PREROUTING 自定义链 sing-box-mark（混合模式下 protocol=udp） ----
iptables -I FORWARD -o $TUN -j ACCEPT                                     # 放行转发到 TUN 的包，否则被 FORWARD 默认策略丢弃
iptables -t mangle -N sing-box-mark                                       # 新建 UDP 打 mark 链
iptables -t mangle -A sing-box-mark -p tcp --dport 53 -j RETURN           # 53 留给 nat 表的 DNS 劫持链处理
iptables -t mangle -A sing-box-mark -p udp --dport 53 -j RETURN           # 同上
iptables -t mangle -A sing-box-mark -m mark --mark $BYPASS_MARK -j RETURN # sing-box 自身流量防回环
for ip in $BYPASS; do
    iptables -t mangle -A sing-box-mark -d "$ip" -j RETURN # 跳过 IPv4 保留地址 + RFC1918 私网
done
[ -f "$CN_IP_CIDR" ] &&
    iptables -t mangle -A sing-box-mark -m set --match-set cn dst -j RETURN                 # 目的 IP 属 CN → RETURN 直连，CN UDP 不进 TUN（仅当 cn.txt 存在）
iptables -t mangle -A sing-box-mark -p udp -s $LAN -j MARK --set-mark $ROUTE_MARK           # 仅来自局域网的 UDP 打上 ROUTE_MARK，由策略路由送进 utun
iptables -t mangle -I PREROUTING -p udp -m multiport --dports $PROXY_PORTS -j sing-box-mark # 入口①：常用端口 UDP 进 mark 链
iptables -t mangle -I PREROUTING -p udp -d $FAKEIP -j sing-box-mark                         # 入口②：fake-ip 段 UDP 全量进 mark 链

# ---- 2.3 DNS 劫持：nat/PREROUTING 自定义链 sing-box-dns ----
# mix 下 DNS 仍必须由 sing-box 统一应答：CN 域名走 dns_direct 拿真实 IP，
# 非 CN 域名走 dns_fakeip 拿 28.x.x.x 假地址，分流逻辑在 sing-box 内部完成
iptables -t nat -N sing-box-dns                                                 # 新建 DNS 劫持链
iptables -t nat -A sing-box-dns -m mark --mark $BYPASS_MARK -j RETURN           # sing-box 自己的 DNS 查询不再被劫持，防回环
iptables -t nat -A sing-box-dns -p tcp -s $LAN -j REDIRECT --to-ports $DNS_PORT # 局域网 TCP/53 → sing-box dns-in
iptables -t nat -A sing-box-dns -p udp -s $LAN -j REDIRECT --to-ports $DNS_PORT # 局域网 UDP/53 → sing-box dns-in
iptables -t nat -I PREROUTING -p tcp --dport 53 -j sing-box-dns                 # 入口：所有 53/TCP 进劫持链
iptables -t nat -I PREROUTING -p udp --dport 53 -j sing-box-dns                 # 入口：所有 53/UDP 进劫持链

# ---- 2.4 IPv6 DNS 兜底（ipv6_redir=OFF，不建 v6 代理/DNS 链，但禁掉外发 53 防绕过） ----
ip6tables -I INPUT -p tcp --dport 53 -j REJECT # 阻断到本机的 IPv6 53/TCP，避免设备走 v6 DNS 绕过劫持
ip6tables -I INPUT -p udp --dport 53 -j REJECT # 同上 UDP

# ---- cspell words ----
# cspell:words dport
# cspell:words dports
# cspell:words fwmark
# cspell:words redir
