#!/usr/bin/env bash
# S2 sing-box 反复崩溃 → StateFatal → iptables 已拆净 → 直连通
set -u
export CASE_ID="S2"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

note "反复 kill -9 sing-box 直到 StateFatal（最多 300s）"
crashloop_to_fatal 300 || skip "300s 内未达 StateFatal（退避 ladder 较长，可重跑或延长超时）"
sleep 3

if assert_rules_absent; then
    st="$(probe)"
    [ "$st" = DIRECT ] || fail "StateFatal 且规则已拆，但 probe=$st（预期 DIRECT）"
    pass "StateFatal 时规则已拆净；直连可用"
fi
fail "StateFatal 但规则未拆净 —— 黑洞风险"
