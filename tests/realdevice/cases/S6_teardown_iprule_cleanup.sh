#!/usr/bin/env bash
# S6 ip rule 幂等 + teardown 清净 —— 调查 + 回归
#
# 背景：ip rule 没有 replace 动词，原本 startup.sh / reapply-routes.sh 直接
# `ip rule add`（不幂等），每次 boot / restart / HUP 都多堆一条
# `fwmark $ROUTE_MARK lookup $ROUTE_TABLE`；teardown.sh 又只 del 一条 → 残留。
# 修复后：startup.sh / reapply-routes.sh 改「先 while-del 再 add」（幂等 + 自愈），
# teardown.sh 改「while-del 删到失败」（一次清净全部累积）。
#
# 本用例验证两侧：
#   part 1  连做 restart，ip rule 不再累积（脚本幂等）
#   part 2  手工种入重复规则，模拟历史残留 / 外部污染
#   part 3  stop → teardown 必须把全部（含手工种入的）清掉
set -u
export CASE_ID="S6"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

before="$(iprule_table_count)"
note "起始：指向 table $ROUTE_TABLE 的 ip rule = ${before}"

# ---- part 1: restart 不应累积（startup.sh / reapply-routes.sh 幂等）----
note "连做 3 次 sing-router restart，验证 ip rule 不累积"
for _ in 1 2 3; do
    rsh "$SINGROUTER restart" >/dev/null 2>&1 || true
    wait_state running 60 || note "  restart 后未及时回 running，继续"
done
after_restart="$(iprule_table_count)"
note "3 次 restart 后：ip rule = ${after_restart}"
[ "$after_restart" -le "$before" ] \
    || fail "restart 累积了 ip rule（${before}→${after_restart}）—— ip rule add 未幂等"

# ---- part 2: 手工种入重复规则，模拟历史残留 / 外部污染 ----
note "手工种入 3 条重复 fwmark ${ROUTE_MARK} 规则，模拟历史累积"
rsh "for n in 1 2 3; do ip rule add fwmark $ROUTE_MARK table $ROUTE_TABLE; done"
planted="$(iprule_table_count)"
note "种入后：ip rule = ${planted}"
[ "$planted" -gt "$after_restart" ] \
    || fail "手工种入未生效（${after_restart}→${planted}）"

# ---- part 3: stop → teardown 必须把全部（含手工种入的）清掉 ----
note "stop daemon → teardown 循环删除"
rsh "$SINGROUTER stop" >/dev/null 2>&1 || true
wait_state offline 30 || note "daemon 未到 offline（可能已退出）；继续"
sleep 2
final="$(iprule_table_count)"
note "teardown 后：ip rule = ${final}"
[ "$final" -eq 0 ] \
    || fail "teardown 后仍残留 ${final} 条指向 table $ROUTE_TABLE 的 ip rule —— 循环删除未清净"
pass "restart 不累积（${before}→${after_restart}）；手工种入至 ${planted}；teardown 全清至 0"
