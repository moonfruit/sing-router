#!/usr/bin/env bash
# S5 sing-box 子进程被 kill -9、daemon 健在 → supervisor 退避重启 → 回到代理通
set -u
export CASE_ID="S5"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

spid="$(singbox_pid)"
[ -n "$spid" ] || skip "缺少 sing-box pid"
note "kill -9 sing-box 子进程 ($spid)，预期 supervisor 退避重启"
rsh "kill -9 $spid 2>/dev/null || true"

if wait_singbox_restart "$spid" 90; then
    st="$(probe)"
    [ "$st" = PROXY ] || fail "已重启但 probe=${st}（预期 PROXY）"
    pass "单次崩溃已恢复；新 pid=$(singbox_pid)"
fi
fail "90s 内 supervisor 未把 sing-box 重启到 running"
