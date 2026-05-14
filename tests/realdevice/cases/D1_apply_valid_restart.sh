#!/usr/bin/env bash
# D1 sync 拉到健康的新 zoo → applier check 通过 → restart → 代理通
set -u
export CASE_ID="D1"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap 'restore_zoo; restore_to_running' EXIT

stage_modified_zoo || skip "var/zoo.raw.json 不存在"
old="$(singbox_pid)"
code="$(apply_via_api)"
note "apply HTTP code=$code"
case "$code" in
    200*) : ;;
    501*) skip "apply 未接线（HTTP 501）" ;;
    *)    fail "apply 返回 $code" ;;
esac

wait_singbox_restart "$old" 90 || fail "合法资源 apply 后未发生重启"
st="$(probe)"
[ "$st" = PROXY ] || fail "apply 后 probe=$st（预期 PROXY）"
pass "合法 zoo 变更已应用；sing-box 已重启；probe PROXY"
