#!/usr/bin/env bash
# R2 sing-box 不健康时 reapply-rules → 必须失败且不留半套规则（黑洞）
set -u
export CASE_ID="R2"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

note "stop daemon，再尝试 reapply-rules —— 必须失败、且不装出半套规则"
rsh "$SINGROUTER stop" >/dev/null 2>&1 || true
wait_state offline 30 || note "daemon 未到 offline；继续"
sleep 1
before="$(probe)"
note "daemon 已停：probe=$before"

out="$(rsh "$SINGROUTER reapply-rules" 2>&1)"; rc=$?
note "reapply-rules rc=$rc out=[$out]"
[ "$rc" -ne 0 ] || fail "daemon 停止时 reapply-rules 返回 0（必须失败）"
sleep 1

if rules_present; then
    fail "reapply-rules 在无 daemon 时留下了 'sing-box' 链 —— 半套规则黑洞风险"
fi
after="$(probe)"
case "$after" in
    DIRECT) pass "reapply-rules 干净失败；未留半套规则；probe DIRECT" ;;
    PROXY)  fail "daemon 停止时 probe=PROXY —— 状态不一致" ;;
    *)      fail "失败的 reapply-rules 之后 probe=$after" ;;
esac
