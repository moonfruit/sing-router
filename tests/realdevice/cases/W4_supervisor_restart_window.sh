#!/usr/bin/env bash
# W4 supervisor.Restart 快速重启路径窗口（sing-router restart）—— 与 W2 对比
set -u
export CASE_ID="W4"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

old="$(singbox_pid)"
note "后台 sing-router restart，同时测量快速重启窗口"
tmp="$(mktemp)"
( rsh "$SINGROUTER restart" > "$tmp" 2>&1 ) &
rpid=$!
ms="$(measure_blackhole_ms 75 1)"
wait "$rpid" || true
rm -f "$tmp"

new="$(singbox_pid)"
[ -n "$new" ] && [ "$new" != "$old" ] || fail "restart 未产生新的 sing-box pid"
wait_state running 30 || fail "restart 后 daemon 未回到 running"
st="$(probe)"
[ "$st" = PROXY ] || fail "最终 probe=${st}（预期 PROXY）"
pass "supervisor.Restart 窗口≈${ms}ms；已恢复 PROXY（与 W2 对比快慢）"
