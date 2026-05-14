#!/usr/bin/env bash
# S1 正常 stop → teardown 全清 → 直连通
set -u
export CASE_ID="S1"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

note "stop daemon，预期 teardown 拆净所有规则 → DIRECT"
rsh "$SINGROUTER stop" >/dev/null 2>&1 || true
wait_state offline 30 || note "daemon 未到 offline（可能已退出）；继续检查规则"
sleep 2

if assert_rules_absent; then
    st="$(probe)"
    [ "$st" = DIRECT ] || fail "规则已拆净但 probe=$st（预期 DIRECT）"
    pass "teardown 干净；直连可用"
fi
fail "stop 后规则未完全拆净 —— 见上方 ✗"
