#!/usr/bin/env bash
# R1 sing-box 健在时 reapply-rules：规则装回、代理恢复
set -u
export CASE_ID="R1"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

# 只 flush sing-box 自己的 nat 链模拟规则丢失 —— 不能 `iptables -t nat -F`：
# 那会连固件的 LAN→WAN MASQUERADE 一并冲掉，跑完整屋 LAN 客户端 IPv4 出网全断。
note "flush sing-box nat 链模拟规则丢失，再 reapply-rules"
rsh "iptables -t nat -F sing-box && iptables -t nat -F sing-box-dns" || skip "无法 flush sing-box nat 链"
sleep 1
mid="$(probe)"
note "sing-box nat 链 flush 后：probe=$mid"

rsh "$SINGROUTER reapply-rules" >/dev/null 2>&1 || fail "reapply-rules 退出非 0"
sleep 2
st="$(probe)"
[ "$st" = PROXY ] || fail "reapply-rules 后 probe=${st}（预期 PROXY）"
pass "reapply-rules 恢复代理（中间态为 ${mid}）"
