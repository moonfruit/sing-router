#!/usr/bin/env bash
# S3 daemon 收 SIGTERM → 子进程被收掉 + 规则全清 → 直连通
set -u
CASE_ID="S3"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

dpid="$(daemon_pid)"
[ -n "$dpid" ] || skip "status 未给出 daemon pid"
note "kill -TERM daemon ($dpid)"
rsh "kill -TERM $dpid 2>/dev/null || true"
sleep 5

rsh "kill -0 $dpid 2>/dev/null" && fail "SIGTERM 后 daemon ($dpid) 仍存活"
if rsh "ps w 2>/dev/null | grep -v grep | grep -q 'sing-box run'"; then
    fail "daemon SIGTERM 后 sing-box 子进程仍存活（未被收掉）"
fi

if assert_rules_absent; then
    st="$(probe)"
    [ "$st" = DIRECT ] || fail "probe=$st（预期 DIRECT）"
    pass "优雅 SIGTERM：子进程被收、规则拆净"
fi
fail "SIGTERM 后规则未拆净"
