#!/usr/bin/env bash
# tests/realdevice/lib/harness.sh
# 实机测试套件的驱动层函数库。在开发机上 source（由 cases/*.sh 与 run.sh）。
# 调用方负责先 source config.sh、并自行 `set -u`。本文件 source 时无副作用。
#
# 分三段：
#   1. 纯逻辑（classify / max_contiguous / case_matches）—— 有单测
#   2. SSH / 状态 / 探针 / 断言 I/O 助手
#   3. 故障注入与资源操作助手

HARNESS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ============================================================
# 1. 纯逻辑（harness_test.sh 覆盖）
# ============================================================

# classify <direct:OK|FAIL> <proxy:OK|FAIL> <rules_rc:0|1> → PROXY|DIRECT|BLACKHOLE|WANDOWN
# 三态判定核心：
#   direct   = 能否访问 https://www.baidu.com（任意时刻都应可达）
#   proxy    = 能否访问 https://www.google.com（仅 sing-box 代理正常时可达）
#   rules_rc = 0 表示 iptables 'sing-box' 链存在
# direct 通 → 看 proxy 区分 PROXY / DIRECT；
# direct 不通 → 看 rules 区分 BLACKHOLE（规则仍在=黑洞）/ WANDOWN（无规则=WAN 本身断）。
classify() {
    if [ "$1" = OK ]; then
        if [ "$2" = OK ]; then echo PROXY; else echo DIRECT; fi
    else
        if [ "$3" -eq 0 ]; then echo BLACKHOLE; else echo WANDOWN; fi
    fi
}

# max_contiguous <token> <sample>... → 该 token 的最长连续游程长度
max_contiguous() {
    local tok="$1"; shift
    local max=0 cur=0 s
    for s in "$@"; do
        if [ "$s" = "$tok" ]; then
            cur=$((cur + 1))
            [ "$cur" -gt "$max" ] && max=$cur
        else
            cur=0
        fi
    done
    echo "$max"
}

# max_contiguous_blackhole <sample>... → BLACKHOLE 最长游程
max_contiguous_blackhole() { max_contiguous BLACKHOLE "$@"; }

# case_matches <case_id> <selector>... → rc 0 若匹配（无 selector 时全匹配）
# selector 可为完整 ID（S2）或组前缀（S）。
case_matches() {
    local id="$1"; shift
    [ "$#" -eq 0 ] && return 0
    local s
    for s in "$@"; do
        [ "$id" = "$s" ] && return 0
        case "$id" in "$s"*) return 0 ;; esac
    done
    return 1
}
