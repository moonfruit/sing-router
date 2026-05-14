#!/usr/bin/env bash
# D5 拉到的资源通不过 sing-box check → applier revert → 不重启 → 旧配置全程不中断
set -u
export CASE_ID="D5"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap 'restore_zoo; restore_to_running' EXIT

stage_bad_check_zoo || skip "var/zoo.raw.json 不存在"
old="$(singbox_pid)"
cfg_before="$(rsh "sha256sum $RUNDIR/$CONFIG_DIR/zoo.json 2>/dev/null | cut -d' ' -f1")"
note "POST /apply 一个 check 必失败的 zoo —— 预期 revert、不重启、代理零扰动"
tmp="$(mktemp)"
( apply_via_api > "$tmp" 2>&1 ) &
apid=$!
ms="$(measure_blackhole_ms 20 1)"
wait "$apid" || true
code="$(cat "$tmp")"; rm -f "$tmp"
note "apply HTTP code=$code（CheckConfig 失败路径返回 200；daemon 日志记 apply.check.failed）"
case "$code" in 501*) skip "apply 未接线（HTTP 501）" ;; esac

new="$(singbox_pid)"
[ "$new" = "$old" ] || fail "sing-box 被重启 ($old→$new) —— 坏配置不应触发重启"
[ "$ms" -eq 0 ] || fail "坏配置 apply 期间出现 ${ms}ms BLACKHOLE —— 必须完全透明"
cfg_after="$(rsh "sha256sum $RUNDIR/$CONFIG_DIR/zoo.json 2>/dev/null | cut -d' ' -f1")"
[ "$cfg_before" = "$cfg_after" ] || fail "config.d/zoo.json 未被 revert（$cfg_before → $cfg_after）"
st="$(probe)"
[ "$st" = PROXY ] || fail "probe=$st（预期 PROXY）"
pass "check 失败资源已 revert；未重启；config.d/zoo.json 完好；probe PROXY"
