#!/usr/bin/env bash
# R3 koolshare nat-start 钩子链路：sing-box 规则丢失 → 跑 N99 钩子 → 代理恢复
# 真实 WAN 重拨（拔网线）为手动变体；本用例自动化模拟钩子链路本身。
#
# 不能 `iptables -t nat -F`：那会把固件 LAN→WAN MASQUERADE 一并冲掉，而真实 WAN
# 重拨是固件 start_nat「flush + 重建」—— 重建那半要靠固件自己（且会触发已安装的
# nat-start 钩子）。本用例只复现「sing-box 自有 nat 链丢失」这一对钩子有意义的子
# 状态：flush sing-box / sing-box-dns 两条链，固件 NAT 保持不动。
#
# 测的是「当前 sing-router 二进制内嵌的」N99 钩子（经 `script koolshare/N99` 取出
# 直接喂给 busybox sh），而非路由器上可能由旧 install 写下的过时副本 —— 这样既
# 跟随当前代码，又不依赖重跑 install、不写 /koolshare/init.d。
set -u
export CASE_ID="R3"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
# 用例只 flush sing-box 自有链、不停 daemon。若内嵌钩子没把链装回，daemon 仍
# running → restore_to_running 是 no-op → 残留半套规则。trap 里先 reapply-rules
# 兜底重装，保证无论从哪步退出都不留残规。
trap 'rsh "$SINGROUTER reapply-rules" >/dev/null 2>&1 || true; restore_to_running' EXIT

rsh "$SINGROUTER script koolshare/N99 >/dev/null 2>&1" \
    || skip "$SINGROUTER script koolshare/N99 不可用 —— 无法取出内嵌钩子"

note "flush sing-box 自有 nat 链模拟规则丢失，再跑内嵌 koolshare nat-start 钩子"
rsh "iptables -t nat -F sing-box && iptables -t nat -F sing-box-dns" || skip "无法 flush sing-box nat 链"
sleep 1
mid="$(probe)"
note "sing-box nat 链 flush 后：probe=$mid"
[ "$mid" = BLACKHOLE ] && fail "flush 后即 BLACKHOLE（预期 DIRECT）—— 需排查"

# 内嵌钩子直接喂路由器的 busybox sh 执行（$1=start_nat），全程在路由器侧。
rsh "$SINGROUTER script koolshare/N99 | sh -s -- start_nat" >/dev/null 2>&1 || true
sleep 3
hooklog="$(rsh "tail -3 /tmp/sing-router-nat-start.log 2>/dev/null")"
note "nat-start.log 末尾：$hooklog"
echo "$hooklog" | grep -q "reapply-rules ok" || fail "钩子未记录 'reapply-rules ok'"

st="$(probe)"
[ "$st" = PROXY ] || fail "钩子执行后 probe=${st}（预期 PROXY）"
pass "koolshare 钩子重装规则成功；probe PROXY（真实 WAN 重拨为手动变体）"
