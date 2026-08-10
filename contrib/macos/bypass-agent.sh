#!/usr/bin/env bash
# ============================================================
# sing-router LAN client bypass agent (macOS)
#
# 每次运行做一件事：如果本机 sing-box 健康且我们正连在目标 LAN 上，就把当前
# 出口 IP 续约到路由器的 bypass 白名单；否则撤销。由 launchd 按 StartInterval
# 反复拉起——脚本本身跑完即退，不常驻。
#
# 为什么是续约而不是"启动时注册一次"：本机 sing-box 是常驻的，从插 Dock 到
# 拔 Dock 全程不重启，以进程生命周期为触发点的话钩子一次都不会触发。真正在
# 变的是网络位置。
# ============================================================

set -eu

CONF="${BYPASS_AGENT_CONF:-/etc/sing-router/bypass-agent.conf}"
if [ ! -r "$CONF" ]; then
    echo "bypass-agent: cannot read $CONF" >&2
    exit 1
fi
# shellcheck source=/dev/null
. "$CONF"

: "${ROUTER_URL:?ROUTER_URL not set}"
: "${TOKEN:?TOKEN not set}"
: "${GATEWAY:?GATEWAY not set}"
LOCAL_CLASH_API="${LOCAL_CLASH_API:-http://127.0.0.1:9999}"
TTL="${TTL:-240}"
STATE_FILE="${STATE_FILE:-/var/run/sing-bypass-agent.ip}"

# 只在状态变化时说话：每 60s 一次、一天 1440 次，稳态输出会把日志刷爆。
log() { echo "$(date '+%Y-%m-%d %H:%M:%S') $*"; }

revoke() {
    _ip="$1"
    # 失败静默：路由器不可达时这个请求本来也发不出去，TTL 会兜住。
    curl -sf --max-time 3 -X DELETE \
        -H "Authorization: Bearer $TOKEN" \
        -H "Content-Type: application/json" \
        -d "{\"ips\":[\"$_ip\"]}" \
        "$ROUTER_URL/api/v1/bypass" >/dev/null 2>&1 || true
    rm -f "$STATE_FILE"
}

# 本机代理不健康时【必须】撤销：此刻我们确知自己还在 LAN 内（能跑到这里说明
# 前面的检查还没失败），注销请求发得出去，路由器立刻接管，本机不至于既没
# 自己的代理、又被路由器放行而裸奔。
give_up_unhealthy() {
    _reason="$1"
    if [ -f "$STATE_FILE" ]; then
        _prev=$(cat "$STATE_FILE" 2>/dev/null || true)
        if [ -n "$_prev" ]; then
            log "revoking $_prev ($_reason)"
            revoke "$_prev"
        fi
    fi
    exit 0
}

# 网络位置判断失败时【绝不能】撤销、也不能碰 STATE_FILE：这条路径不代表
# "确认已离开 LAN"，只代表这一次没能判断出结果。Wi-Fi 漫游瞬断、DHCP 续租
# 都会让 `route -n get` 或 `ifconfig` 短暂拿不到东西，而此时 Mac 可能仍在
# LAN 内、sing-box 完全健康——如果这里 revoke，会撤掉一个本不该撤的合法
# 租约，接下来最长一个 StartInterval 窗口内 Mac 既被路由器代理、自己又在
# 代理，正是"路由器不可达时不撤销"这条语义想避免的双重代理。真的离开 LAN
# 时路由器本来就不可达，请求发不出去，交给 TTL 自然过期即可；所以这条路径
# 干脆不发任何网络请求，只写一行本地日志，静默退出。
give_up_unknown_location() {
    _reason="$1"
    log "$_reason"
    exit 0
}

# 1) 本机 sing-box 是否 ready。用 clash api /version，与 sing-router 自己的
#    ready check 采用同一个信号。
if ! curl -sf --max-time 2 "$LOCAL_CLASH_API/version" >/dev/null 2>&1; then
    give_up_unhealthy "local sing-box not ready"
fi

# 2) 找出通往网关的接口与源地址。
#    不用 default 路由——本机 TUN 装了 auto_route，default 会被它接管；网关是
#    同网段私有地址，走的是直连路由，拿到的必定是物理口。
IFACE=$(route -n get "$GATEWAY" 2>/dev/null | awk '/interface:/{print $2}')
if [ -z "$IFACE" ]; then
    give_up_unknown_location "no route to $GATEWAY (not on the target LAN, or a transient blip)"
fi
IP=$(ifconfig "$IFACE" inet 2>/dev/null | awk '/inet /{print $2; exit}')
if [ -z "$IP" ]; then
    give_up_unknown_location "interface $IFACE has no IPv4 address (not on the target LAN, or a transient blip)"
fi

# 3) IP 变了先注销旧的，把切换窗口从整个 TTL 压到接近 0。
PREV=""
if [ -f "$STATE_FILE" ]; then
    PREV=$(cat "$STATE_FILE" 2>/dev/null || true)
fi
if [ -n "$PREV" ] && [ "$PREV" != "$IP" ]; then
    log "address changed $PREV -> $IP, revoking old lease"
    revoke "$PREV"
fi

# 4) 续约。-exist 语义在服务端：条目还在就刷新 TTL，已过期就重建。
#    不用 -f：需要区分"连不上路由器"(000) 与"路由器主动拒绝"(401/403/400)，
#    -f 会把响应体和状态码一起吞掉，日志里就只剩一句含糊的 "unreachable?"，
#    用户只能跑去路由器侧交叉印证。curl 本身连接失败时 %{http_code} 取到
#    的是 "000"，与真实 HTTP 状态码天然不会混淆。
HTTP_CODE=$(curl -s -o /dev/null --max-time 5 -w '%{http_code}' -X POST \
        -H "Authorization: Bearer $TOKEN" \
        -H "Content-Type: application/json" \
        -d "{\"ips\":[\"$IP\"],\"ttl_sec\":$TTL}" \
        "$ROUTER_URL/api/v1/bypass" 2>/dev/null || true)

if [ "$HTTP_CODE" = "200" ] || [ "$HTTP_CODE" = "204" ]; then
    if [ "$PREV" != "$IP" ]; then
        log "registered $IP (iface $IFACE, ttl ${TTL}s)"
    fi
    mkdir -p "$(dirname "$STATE_FILE")"
    echo "$IP" > "$STATE_FILE"
else
    case "$HTTP_CODE" in
        000) WHY="router unreachable (http_code=000)" ;;
        401) WHY="401 unauthorized (TOKEN 与路由器 [http].token 不一致?)" ;;
        403) WHY="403 forbidden ([bypass].enabled 是否为 false?)" ;;
        400) WHY="400 bad request (IP 格式或数量是否超出服务端限制?)" ;;
        *) WHY="unexpected http_code=${HTTP_CODE:-empty}" ;;
    esac
    # 不管哪种失败都【不】撤销：连不上时请求本来就发不出去，路由器主动拒绝
    # 时撤销只会让"裸奔"提前发生——两种情况都靠 TTL 自然过期即可。
    log "renew failed for $IP: $WHY; leaving lease to expire"
    exit 0
fi
