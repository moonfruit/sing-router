#!/usr/bin/env bash
# R1 sing-box 健在时 `sing-router restart`：完整 Shutdown+Startup → 代理恢复
# 新方案：restart = Shutdown（拆 iptables + 停 sing-box）+ Startup（启 + 重装 iptables），
# 中间会有短暂 DIRECT 窗口；最终必须回到 PROXY。
set -u
export CASE_ID="R1"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

# 只 flush sing-box 自己的 nat 链模拟规则丢失 —— 不能 `iptables -t nat -F`：
# 那会连固件的 LAN→WAN MASQUERADE 一并冲掉，跑完整屋 LAN 客户端 IPv4 出网全断。
note "flush sing-box nat 链模拟规则丢失，再 restart"
rsh "iptables -t nat -F sing-box && iptables -t nat -F sing-box-dns" || skip "无法 flush sing-box nat 链"
sleep 1
mid="$(probe)"
note "sing-box nat 链 flush 后：probe=$mid"

rsh "$SINGROUTER restart" >/dev/null 2>&1 || fail "restart 退出非 0"
# 容忍 DIRECT 窗口：Shutdown 期间 iptables 被拆，Startup ready check 默认 60s
wait_state running 90 || fail "restart 后 60s 内未回 running"
st="$(probe)"
[ "$st" = PROXY ] || fail "restart 后 probe=${st}（预期 PROXY）"
pass "restart 恢复代理（中间态为 ${mid}，含 DIRECT 窗口）"
