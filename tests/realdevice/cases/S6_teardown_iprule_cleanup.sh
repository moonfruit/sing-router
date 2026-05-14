#!/usr/bin/env bash
# S6 teardown 必须清净累积的 ip rule —— 调查 + 回归
#
# 背景：ip rule add 不幂等。startup.sh / reapply-routes.sh 每次（冷启 / restart /
# HUP / 崩溃恢复）都新增一条 `fwmark $ROUTE_MARK lookup $ROUTE_TABLE`，多次之后
# 会累积多条重复。teardown.sh 若只 `ip rule del` 一条，就会留下 N-1 条残留 ——
# stop / uninstall 之后策略路由里还挂着指向（已空）table 的规则。
#
# 本用例：先连做几次 restart 把 ip rule 堆起来（证明非幂等累积），再 stop 触发
# teardown，断言指向 $ROUTE_TABLE 的 ip rule 被全部清掉（循环删除生效）。
set -u
export CASE_ID="S6"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

before="$(iprule_table_count)"
note "起始：指向 table $ROUTE_TABLE 的 ip rule 条数 = $before"

note "连做 3 次 sing-router restart（每次 reapply-routes.sh 跑一次 ip rule add）"
for _ in 1 2 3; do
    rsh "$SINGROUTER restart" >/dev/null 2>&1 || true
    wait_state running 60 || note "  restart 后未及时回 running，继续"
done
mid="$(iprule_table_count)"
note "3 次 restart 后：ip rule 条数 = $mid"
[ "$mid" -gt "$before" ] \
    || skip "ip rule 条数未增长（$before→$mid）—— 该平台 ip rule add 可能幂等，本用例不适用"

note "stop daemon → teardown 跑 ip rule 删除"
rsh "$SINGROUTER stop" >/dev/null 2>&1 || true
wait_state offline 30 || note "daemon 未到 offline（可能已退出）；继续"
sleep 2

after="$(iprule_table_count)"
note "teardown 后：ip rule 条数 = $after"
[ "$after" -eq 0 ] \
    || fail "teardown 后仍残留 $after 条指向 table $ROUTE_TABLE 的 ip rule —— 循环删除未生效"
pass "ip rule 累积 $before→$mid，teardown 循环删除清净至 0"
