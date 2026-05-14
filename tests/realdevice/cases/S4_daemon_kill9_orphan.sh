#!/usr/bin/env bash
# S4 daemon 被 kill -9（最坏情况）：拷问孤儿 sing-box 链 —— 孤儿后续崩溃是否黑洞。
# 本用例是「特征刻画」型：完成刻画即 PASS，黑洞结论以 note 显著标出（见 spec §8）。
set -u
CASE_ID="S4"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

dpid="$(daemon_pid)"; spid="$(singbox_pid)"
[ -n "$dpid" ] && [ -n "$spid" ] || skip "缺少 daemon/sing-box pid"

note "kill -9 daemon ($dpid)；sing-box 子进程 ($spid) 因 setpgid 隔离，预期成孤儿"
rsh "kill -9 $dpid 2>/dev/null || true"
sleep 3

if rsh "kill -0 $spid 2>/dev/null"; then
    p1="$(probe)"
    note "phase1：孤儿 sing-box 存活，probe=$p1"
    [ "$p1" = PROXY ] || note "  WARN phase1 probe=$p1（孤儿服务期预期 PROXY）"
else
    note "phase1：sing-box 子进程未在 daemon kill -9 后存活"
fi

note "phase2：kill -9 孤儿 sing-box ($spid) —— 已无 supervisor 拆规则"
rsh "kill -9 $spid 2>/dev/null || true"
sleep 3
p2="$(probe)"
note "phase2：孤儿被杀后 probe=$p2"
case "$p2" in
    BLACKHOLE) pass "已刻画：phase2=BLACKHOLE —— 孤儿崩溃使 iptables 滞留，证实 spec §8 看门狗缺口" ;;
    DIRECT)    pass "已刻画：phase2=DIRECT —— 规则不知何故被清" ;;
    PROXY)     pass "已刻画：phase2=PROXY —— sing-box 被重新拉起（存在 init.d/cron 看门狗？）" ;;
    *)         fail "phase2 不确定：probe=$p2" ;;
esac
