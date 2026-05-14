#!/usr/bin/env bash
# R1 sing-box 健在时 reapply-rules：规则装回、代理恢复
set -u
export CASE_ID="R1"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

note "flush nat 表模拟规则丢失，再 reapply-rules"
rsh "iptables -t nat -F" || skip "无法 flush nat 表"
sleep 1
mid="$(probe)"
note "nat flush 后（类 WAN 重拨）：probe=$mid"

rsh "$SINGROUTER reapply-rules" >/dev/null 2>&1 || fail "reapply-rules 退出非 0"
sleep 2
st="$(probe)"
[ "$st" = PROXY ] || fail "reapply-rules 后 probe=${st}（预期 PROXY）"
pass "reapply-rules 恢复代理（中间态为 ${mid}）"
