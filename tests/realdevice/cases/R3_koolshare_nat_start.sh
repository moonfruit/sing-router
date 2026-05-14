#!/usr/bin/env bash
# R3 koolshare nat-start 钩子链路：模拟 WAN 重拨（flush nat + 调 N99 钩子）→ 代理恢复
# 真实 WAN 重拨（拔网线）为手动变体；本用例自动化模拟钩子链路本身。
set -u
export CASE_ID="R3"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

HOOK="/koolshare/init.d/N99sing-router.sh"
rsh "test -f $HOOK" || skip "koolshare 钩子 $HOOK 不存在（merlin 固件？请手动跑 R3）"

note "模拟 WAN 重拨：flush nat 表，再调用 koolshare nat-start 钩子"
rsh "iptables -t nat -F"
sleep 1
mid="$(probe)"
note "nat flush 后（类 WAN 重拨）：probe=$mid"
[ "$mid" = BLACKHOLE ] && fail "flush 后即 BLACKHOLE（预期 DIRECT）—— 需排查"

rsh "sh $HOOK start_nat" >/dev/null 2>&1 || true
sleep 3
hooklog="$(rsh "tail -3 /tmp/sing-router-nat-start.log 2>/dev/null")"
note "nat-start.log 末尾：$hooklog"
echo "$hooklog" | grep -q "reapply-rules ok" || fail "钩子未记录 'reapply-rules ok'"

st="$(probe)"
[ "$st" = PROXY ] || fail "钩子执行后 probe=$st（预期 PROXY）"
pass "koolshare 钩子重装规则成功；probe PROXY（真实 WAN 重拨为手动变体）"
