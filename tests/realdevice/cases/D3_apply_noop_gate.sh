#!/usr/bin/env bash
# D3 无资源变化时 POST /apply → sha256 闸门 no-op → 不触发任何 restart 窗口
set -u
export CASE_ID="D3"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

# Pre-warm：先 apply 一次把 apply-state.json 与当前磁盘对齐。前面用例（D1/D2）
# 的 trap 只 restore 磁盘上的资源文件，没还原 var/apply-state.json（那是 daemon
# 自己维护的私有状态，外部不应触碰）—— 残留的 hash 会让本用例首次 apply 误判
# 为 "cn/zoo 真变化"。这次 prewarm 可能真发生一次 restart 以让状态对齐。
note "pre-warm: apply 一次让 apply-state 吸收前面用例的状态漂移"
_="$(apply_via_api)"
sleep 5

baseline="$(singbox_pid)"
note "不改任何资源直接 POST /apply —— 预期 sha256 闸门判定 no-op，不重启"
code="$(apply_via_api)"
note "apply HTTP code=$code"
case "$code" in
    200*) : ;;
    501*) skip "apply 未接线（HTTP 501）" ;;
    *)    fail "apply 返回 $code" ;;
esac
sleep 3

new="$(singbox_pid)"
[ "$new" = "$baseline" ] || fail "no-op apply 却重启了 sing-box (${baseline}→$new) —— sha256 闸门失效"
st="$(probe)"
[ "$st" = PROXY ] || fail "probe=${st}（预期 PROXY）"
pass "no-op apply：sha256 闸门生效，未重启，probe PROXY"
