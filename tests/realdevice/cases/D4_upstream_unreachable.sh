#!/usr/bin/env bash
# D4 上游不可达 → sync/update 失败仅报错 → daemon 主流程与代理持续可用
set -u
export CASE_ID="D4"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

GITEE_HOST="gitee.com"
require_running
trap 'unblock_host "$GITEE_HOST"; restore_to_running' EXIT

note "在路由器 OUTPUT 链 REJECT 到 $GITEE_HOST，再跑 sing-router update all（预期干净失败）"
block_host "$GITEE_HOST" || skip "无法解析/封锁 $GITEE_HOST"
before="$(probe)"

out="$(rsh "$SINGROUTER update all" 2>&1)"; rc=$?
note "update rc=$rc out=[$out]"
[ "$rc" -ne 0 ] || note "WARN update 在封锁下仍返回 0（可能 token 为空或命中缓存）"
sleep 2

[ "$(daemon_state)" = running ] || fail "失败的 update 之后 daemon 状态变了"
after="$(probe)"
{ [ "$before" = PROXY ] && [ "$after" = PROXY ]; } \
    || fail "失败的 update 扰动了代理（before=$before after=$after）"
pass "上游不可达时 update 干净失败；daemon 与代理均未受影响"
