#!/usr/bin/env bash
# D3 无资源变化时 POST /apply → sha256 闸门 no-op → 不触发任何 restart 窗口
set -u
export CASE_ID="D3"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

old="$(singbox_pid)"
note "不改任何资源直接 POST /apply —— 预期 sha256 闸门判定 no-op，不重启"
code="$(apply_via_api)"
note "apply HTTP code=$code"
case "$code" in
    200*) : ;;
    501*) skip "apply 未接线（HTTP 501）" ;;
    *)    fail "apply 返回 $code" ;;
esac
sleep 3

new="$(singbox_pid)"
[ "$new" = "$old" ] || fail "no-op apply 却重启了 sing-box ($old→$new) —— sha256 闸门失效"
st="$(probe)"
[ "$st" = PROXY ] || fail "probe=$st（预期 PROXY）"
pass "no-op apply：sha256 闸门生效，未重启，probe PROXY"
