#!/usr/bin/env bash
# W2 资源更新触发 applier restart 的窗口（不拆 iptables，ready-check 最长 60s）—— 最大风险点
set -u
CASE_ID="W2"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap 'restore_zoo; restore_to_running' EXIT

stage_modified_zoo || skip "var/zoo.raw.json 不存在 —— 无法触发 applier restart"
old="$(singbox_pid)"
note "后台 POST /api/v1/apply，同时测量 restart 窗口"
tmp="$(mktemp)"
( apply_via_api > "$tmp" 2>&1 ) &
apid=$!
ms="$(measure_blackhole_ms 75 1)"
wait "$apid" || true
code="$(cat "$tmp")"; rm -f "$tmp"

case "$code" in
    501*) skip "apply 未接线（HTTP 501）：需 ApplyPending（auto_apply + gitee token）" ;;
    200*) : ;;
    *)    note "apply HTTP code=$code（非 200，继续按 pid 变化判断）" ;;
esac

new="$(singbox_pid)"
[ -n "$new" ] && [ "$new" != "$old" ] || skip "未观察到重启（apply 可能 no-op）；窗口测量不适用"
note "applier restart 期间连续 BLACKHOLE 窗口 ≈ ${ms}ms"
wait_state running 30 || fail "apply 后 daemon 未回到 running"
st="$(probe)"
[ "$st" = PROXY ] || fail "最终 probe=$st（预期 PROXY）"
pass "applier restart 窗口≈${ms}ms；已恢复 PROXY（受 ready-check 约束，可达 30s+）"
