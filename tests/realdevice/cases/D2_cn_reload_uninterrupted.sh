#!/usr/bin/env bash
# D2 sync 拉到新 cn.txt → reload-cn-ipset 轻量重载 → 全程不中断、不重启
set -u
export CASE_ID="D2"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap 'restore_cn_txt; restore_to_running' EXIT

modify_cn_txt || skip "var/cn.txt 不存在"
old="$(singbox_pid)"
note "改 cn.txt 后后台 POST /apply，全程探针监测是否中断"
tmp="$(mktemp)"
( apply_via_api > "$tmp" 2>&1 ) &
apid=$!
ms="$(measure_blackhole_ms 20 1)"
wait "$apid" || true
code="$(cat "$tmp")"; rm -f "$tmp"
note "apply HTTP code=$code"
case "$code" in 501*) skip "apply 未接线（HTTP 501）" ;; esac

new="$(singbox_pid)"
[ "$new" = "$old" ] || fail "sing-box pid 变了 ($old→$new) —— cn.txt 变更不应重启 sing-box"
[ "$ms" -eq 0 ] || fail "cn.txt reload 期间出现 ${ms}ms BLACKHOLE —— 必须全程不中断"
st="$(probe)"
[ "$st" = PROXY ] || fail "cn reload 后 probe=$st（预期 PROXY）"
pass "cn.txt 轻量重载：未重启、无中断、probe PROXY"
