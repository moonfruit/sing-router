#!/usr/bin/env bash
# W3 kill -HUP sing-box → TUN/路由重建，WatchRoutes(默认30s) 补回 —— 量化路由缺失窗口
set -u
CASE_ID="W3"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

spid="$(singbox_pid)"
[ -n "$spid" ] || skip "缺少 sing-box pid"
route_table_has_default || skip "起始时路由表 $ROUTE_TABLE 无 default 路由"

note "kill -HUP sing-box ($spid)，测量 device-bound 路由缺失窗口"
rsh "kill -HUP $spid 2>/dev/null || true"
gone_ms="$(measure_route_gone_ms 75 1)"
note "路由缺失窗口 ≈ ${gone_ms}ms（WatchRoutes 巡检默认 30s）"

ok=0
for _ in $(seq 1 40); do
    route_table_has_default && { ok=1; break; }
    sleep 2
done
[ "$ok" -eq 1 ] || fail "80s 内 default 路由未被 WatchRoutes 补回"

now_pid="$(singbox_pid)"
[ "$now_pid" = "$spid" ] || note "WARN sing-box pid 变了 ($spid → $now_pid)；HUP 不应重启进程"
st="$(probe)"
[ "$st" = PROXY ] || fail "路由已补回但 probe=$st（预期 PROXY）"
pass "路由缺失≈${gone_ms}ms；WatchRoutes 已补回；probe PROXY"
