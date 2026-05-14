#!/usr/bin/env bash
# W1 崩溃退避 <10s 保留 iptables 的窗口 —— 量化黑洞时长
set -u
export CASE_ID="W1"
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/../config.sh"
. "$HERE/../lib/harness.sh"

require_running
trap restore_to_running EXIT

spid="$(singbox_pid)"
[ -n "$spid" ] || skip "缺少 sing-box pid"
note "kill -9 sing-box ($spid)，测量「保留 iptables」退避窗口的黑洞时长"
rsh "kill -9 $spid 2>/dev/null || true"

ms="$(measure_blackhole_ms 30 1)"
note "连续 BLACKHOLE 窗口 ≈ ${ms}ms（采样粒度 ≈1s，偏粗）"

wait_singbox_restart "$spid" 60 || fail "60s 内未恢复到 running"
st="$(probe)"
[ "$st" = PROXY ] || fail "最终 probe=$st（预期 PROXY）"
pass "窗口≈${ms}ms；已恢复 PROXY —— 据此评估「赌快速回归」是否可接受（阈值 IptablesKeepBackoffLtMs=10s）"
