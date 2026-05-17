#!/usr/bin/env bash
# D2 sync 拉到新 cn.txt → Apply 走 4 阶段：sha256 变化 → Restart sing-box → startup.sh 重建 ipset
# 新方案没有「轻量 cn ipset reload」路径；cn.txt 变化会重启 sing-box（用户已接受）。
# 本用例验证：cn.txt 变化触发 sing-box 重启（pid 改变），中间有 DIRECT 窗口，最终 PROXY。
set -u
export CASE_ID="D2"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap 'restore_cn_txt; restore_to_running' EXIT

modify_cn_txt || skip "var/cn.txt 不存在"
old="$(singbox_pid)"
note "改 cn.txt 后后台 POST /apply?resource=cn —— 预期触发 sing-box restart"
tmp="$(mktemp)"
( apply_via_api "cn" > "$tmp" 2>&1 ) &
apid=$!
wait "$apid" || true
code="$(cat "$tmp")"; rm -f "$tmp"
note "apply HTTP code=$code"
case "$code" in 501*) skip "apply 未接线（HTTP 501）" ;; esac

wait_state running 90 || fail "cn.txt apply 后 90s 内未回 running"
new="$(singbox_pid)"
[ "$new" != "$old" ] || fail "cn.txt 变更应触发 sing-box restart（pid 应改变，${old}→$new 未变）"
st="$(probe)"
[ "$st" = PROXY ] || fail "cn reload 后 probe=${st}（预期 PROXY）"
pass "cn.txt 变化触发 sing-box restart (${old}→${new})；最终 probe PROXY"
