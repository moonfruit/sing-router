#!/usr/bin/env bash
# D6 资源过 check 但 sing-box run 失败 → applier revert + RecoverFromFailedApply 用旧配置拉回
set -u
CASE_ID="D6"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap 'restore_box; restore_to_running' EXIT

stage_checkok_runfail_box || fail "无法在 bin/sing-box.new 放置假 sing-box"
old="$(singbox_pid)"
note "POST /apply 一个 check 过 / run 失败的 sing-box —— 预期 revert + recover 回旧配置"
tmp="$(mktemp)"
( apply_via_api > "$tmp" 2>&1 ) &
apid=$!
ms="$(measure_blackhole_ms 90 1)"
wait "$apid" || true
code="$(cat "$tmp")"; rm -f "$tmp"
note "apply HTTP code=$code（restart 失败路径预期 500）"
case "$code" in 501*) skip "apply 未接线（HTTP 501）" ;; esac

wait_state running 120 || fail "revert+recover 后 daemon 未回到 running"
st="$(probe)"
[ "$st" = PROXY ] || fail "revert+recover 后 probe=$st（预期 PROXY）"
note "restart 失败 → revert → recover 窗口 ≈ ${ms}ms"
rsh "$RUNDIR/bin/sing-box version >/dev/null 2>&1" \
    || fail "revert 后 bin/sing-box 不是可用二进制（假 box 未被换回）"
pass "check 过/run 失败资源：已 revert + 用旧配置 recover；probe PROXY（窗口≈${ms}ms）"
