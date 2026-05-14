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

# ============================================================
# 2. SSH / 状态 / 探针 / 断言 I/O 助手
#    依赖 config.sh 提供：ROUTER_SSH LAN_CLIENT_SSH SINGROUTER INITD
#                         RUNDIR CONFIG_DIR ROUTE_TABLE
# ============================================================

# 共用 ssh 选项：BatchMode 禁交互、ControlMaster 多路复用降低每次往返延迟。
_RD_SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=8
    -o ControlMaster=auto -o ControlPath="/tmp/.rd_ssh_%r@%h:%p" -o ControlPersist=60)

# rsh <cmd...> : 在路由器上执行命令
rsh() { ssh "${_RD_SSH_OPTS[@]}" "$ROUTER_SSH" "$@"; }

# probe_conn [timeout_s] → "<direct> <proxy>" : 在 LAN 客户端上跑 probe.sh（双目标）
probe_conn() {
    local to="${1:-5}"
    ssh "${_RD_SSH_OPTS[@]}" "$LAN_CLIENT_SSH" sh -s -- "$to" < "$HARNESS_DIR/probe.sh"
}

# rules_present : rc 0 若 nat 表 'sing-box' 链存在（iptables 真值）
rules_present() { rsh "iptables -t nat -nL sing-box >/dev/null 2>&1"; }

# probe [timeout_s] → PROXY|DIRECT|BLACKHOLE|WANDOWN|INCONCL : 完整三态判定
probe() {
    local to="${1:-5}" line direct proxy rc
    line="$(probe_conn "$to")"
    [ -n "$line" ] || { echo INCONCL; return; }   # ssh 到 LAN 客户端失败
    direct="${line%% *}"
    proxy="${line##* }"
    if rules_present; then rc=0; else rc=1; fi
    classify "$direct" "$proxy" "$rc"
}

# status_json → daemon status JSON（daemon 不在时为空）
status_json() { rsh "$SINGROUTER status --json" 2>/dev/null; }

# daemon_state → running|degraded|fatal|stopping|booting|reloading|offline
daemon_state() { status_json | jq -r '.daemon.state // "offline"' 2>/dev/null || echo offline; }

# singbox_pid / daemon_pid → 纯数字 pid 或空串
singbox_pid() { status_json | jq -r '(.sing_box.pid // empty)|tostring' 2>/dev/null | grep -E '^[0-9]+$' || true; }
daemon_pid()  { status_json | jq -r '(.daemon.pid  // empty)|tostring' 2>/dev/null | grep -E '^[0-9]+$' || true; }

# wait_state <state> <timeout_s> : 轮询直到 daemon_state 命中，rc 0 成功
wait_state() {
    local want="$1" timeout="$2" start now
    start="$(date +%s)"
    while :; do
        [ "$(daemon_state)" = "$want" ] && return 0
        now="$(date +%s)"; [ $((now - start)) -ge "$timeout" ] && return 1
        sleep 2
    done
}

# wait_singbox_restart <old_pid> <timeout_s> : 轮询直到出现新 pid 且 state=running
wait_singbox_restart() {
    local old="$1" timeout="$2" start now np
    start="$(date +%s)"
    while :; do
        np="$(singbox_pid)"
        if [ -n "$np" ] && [ "$np" != "$old" ] && [ "$(daemon_state)" = running ]; then
            return 0
        fi
        now="$(date +%s)"; [ $((now - start)) -ge "$timeout" ] && return 1
        sleep 2
    done
}

# route_table_has_default : rc 0 若策略路由表里有 default 路由
route_table_has_default() { rsh "ip route show table $ROUTE_TABLE 2>/dev/null | grep -q '^default'"; }

# ipset_cn_exists : rc 0 若 'cn' ipset 存在
ipset_cn_exists() { rsh "ipset list cn >/dev/null 2>&1"; }

# assert_rules_absent : rc 0 当 iptables 链 / 策略路由 / ipset 全部已拆净
assert_rules_absent() {
    local bad=0
    if rules_present;            then note "✗ nat 'sing-box' 链仍存在"; bad=1; fi
    if route_table_has_default;  then note "✗ 路由表 $ROUTE_TABLE 仍有 default"; bad=1; fi
    if ipset_cn_exists;          then note "✗ 'cn' ipset 仍存在"; bad=1; fi
    return $bad
}

# assert_rules_present : rc 0 当 nat 'sing-box' 链存在
assert_rules_present() {
    rules_present || { note "✗ nat 'sing-box' 链缺失"; return 1; }
    return 0
}

# measure_blackhole_ms <timeout_s> [probe_timeout_s] → 估算最长连续 BLACKHOLE 毫秒数
# 在 timeout_s 内尽快连续 probe（每次 probe 自带 probe_timeout_s 网络等待），
# 用「最长游程 × 总耗时 / 样本数」估算窗口时长。采样粒度 ≈ probe_timeout + ssh 往返。
measure_blackhole_ms() {
    local timeout="$1" pto="${2:-1}" start now samples="" total runs elapsed
    start="$(date +%s)"
    while :; do
        now="$(date +%s)"; [ $((now - start)) -ge "$timeout" ] && break
        samples="$samples $(probe "$pto")"
    done
    elapsed=$(( $(date +%s) - start ))
    total="$(echo "$samples" | wc -w | tr -d ' ')"
    [ "$total" -gt 0 ] || { echo 0; return; }
    runs="$(max_contiguous_blackhole $samples)"
    echo $(( runs * elapsed * 1000 / total ))
}

# measure_route_gone_ms <timeout_s> [interval_s] → 估算最长连续「default 路由缺失」毫秒数
measure_route_gone_ms() {
    local timeout="$1" iv="${2:-1}" start now samples="" total runs elapsed tok
    start="$(date +%s)"
    while :; do
        now="$(date +%s)"; [ $((now - start)) -ge "$timeout" ] && break
        if route_table_has_default; then tok=PRESENT; else tok=GONE; fi
        samples="$samples $tok"
        sleep "$iv"
    done
    elapsed=$(( $(date +%s) - start ))
    total="$(echo "$samples" | wc -w | tr -d ' ')"
    [ "$total" -gt 0 ] || { echo 0; return; }
    runs="$(max_contiguous GONE $samples)"
    echo $(( runs * elapsed * 1000 / total ))
}

# ---- 用例结果助手（每个都 exit）----
CASE_ID="${CASE_ID:-?}"
note() { echo "  $*"; }
pass() { echo "RESULT $CASE_ID PASS ${1:-}"; exit 0; }
fail() { echo "RESULT $CASE_ID FAIL ${1:-}"; exit 1; }
skip() { echo "RESULT $CASE_ID SKIP ${1:-}"; exit 2; }

# require_running : 用例前置闸门 —— daemon 必须 running 且探针 PROXY，否则 SKIP
require_running() {
    local st p
    st="$(daemon_state)"
    [ "$st" = running ] || skip "前置不满足：daemon state=$st（需 running）"
    p="$(probe)"
    [ "$p" = PROXY ] || skip "前置不满足：probe=$p（需 PROXY）"
}

# restore_to_running : 用例结束 best-effort 把服务恢复到 running（trap EXIT 用）
restore_to_running() {
    local st
    st="$(daemon_state)"
    case "$st" in
        running) return 0 ;;
        offline) rsh "$INITD restart >/dev/null 2>&1 || $INITD start >/dev/null 2>&1 || true" ;;
        *)       rsh "$SINGROUTER restart >/dev/null 2>&1 || $INITD restart >/dev/null 2>&1 || true" ;;
    esac
    wait_state running 120 || note "restore_to_running: 120s 后仍非 running —— 需人工检查"
}

# ============================================================
# 3. 故障注入与资源操作助手
# ============================================================

# crashloop_to_fatal <timeout_s> : 反复 kill -9 sing-box 子进程直到 StateFatal。rc 0 成功
crashloop_to_fatal() {
    local timeout="$1" start now pid
    start="$(date +%s)"
    while :; do
        [ "$(daemon_state)" = fatal ] && return 0
        now="$(date +%s)"; [ $((now - start)) -ge "$timeout" ] && return 1
        pid="$(singbox_pid)"
        [ -n "$pid" ] && rsh "kill -9 $pid 2>/dev/null || true"
        sleep 3
    done
}

# stage_modified_zoo : 备份 var/zoo.raw.json，追加一个无害的 direct outbound
#   → 触发 applier 真正重启（合法、能过 check、易回滚）。rc 1 若 zoo.raw.json 不存在。
#   jq 在开发机侧执行，路由器只需 cat。
stage_modified_zoo() {
    local raw="$RUNDIR/var/zoo.raw.json" nonce
    rsh "test -f $raw" || return 1
    rsh "cp $raw $raw.testbak"
    nonce="$(date +%s)"
    rsh "cat $raw.testbak" \
        | jq ".outbounds += [{\"type\":\"direct\",\"tag\":\"_rdtest_${nonce}\"}]" \
        | rsh "cat > $raw"
}

# stage_bad_check_zoo : 备份并写入一个 preprocess 能过、sing-box check 必失败的 zoo
#   （selector 引用不存在的 outbound tag）。rc 1 若 zoo.raw.json 不存在。
stage_bad_check_zoo() {
    local raw="$RUNDIR/var/zoo.raw.json"
    rsh "test -f $raw" || return 1
    rsh "cp $raw $raw.testbak"
    rsh "cat $raw.testbak" \
        | jq '.outbounds += [{"type":"selector","tag":"_rdtest_bad","outbounds":["does-not-exist-tag"]}]' \
        | rsh "cat > $raw"
}

# restore_zoo : 从 .testbak 还原 var/zoo.raw.json（trap EXIT 用）
restore_zoo() {
    rsh "test -f $RUNDIR/var/zoo.raw.json.testbak && mv -f $RUNDIR/var/zoo.raw.json.testbak $RUNDIR/var/zoo.raw.json || true"
}

# stage_checkok_runfail_box : 在 bin/sing-box.new 放一个假 sing-box
#   —— `check`/`version` 退出 0，`run` 立即退出 1。用于 D6 的 restart 失败 → revert+recover。
stage_checkok_runfail_box() {
    local stg="$RUNDIR/bin/sing-box.new"
    rsh "cat > $stg" <<'FAKE'
#!/bin/sh
# real-device test fake sing-box: check/version succeed, run fails immediately.
case "$1" in
    check|version) exit 0 ;;
    run) echo "fake sing-box: simulated run failure" >&2; exit 1 ;;
    *) exit 0 ;;
esac
FAKE
    rsh "chmod +x $stg"
}

# restore_box : 清掉残留的 bin/sing-box.new（trap EXIT 用）
restore_box() { rsh "rm -f $RUNDIR/bin/sing-box.new"; }

# modify_cn_txt : 备份 var/cn.txt 并追加一条无害 TEST-NET CIDR。rc 1 若不存在。
modify_cn_txt() {
    local cn="$RUNDIR/var/cn.txt"
    rsh "test -f $cn" || return 1
    rsh "cp $cn $cn.testbak && echo '198.51.100.0/24' >> $cn"
}

# restore_cn_txt : 从 .testbak 还原 var/cn.txt（trap EXIT 用）
restore_cn_txt() {
    rsh "test -f $RUNDIR/var/cn.txt.testbak && mv -f $RUNDIR/var/cn.txt.testbak $RUNDIR/var/cn.txt || true"
}

# apply_via_api → 打印 POST /api/v1/apply 的 HTTP code（200/500/501...）
apply_via_api() {
    rsh "curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:9998/api/v1/apply"
}

# block_host <hostname> : 解析 host 的 IP 并在路由器 OUTPUT 链 REJECT（仅影响路由器自身出站）
#   解析结果记到 /tmp/.rd_blocked_<host> 供 unblock 用。rc 1 若解析不到。
block_host() {
    local host="$1" ips ip
    ips="$(rsh "nslookup $host 2>/dev/null | awk '/^Name:/{f=1;next} f&&/Address/{print \$NF}'")"
    [ -n "$ips" ] || return 1
    for ip in $ips; do
        rsh "iptables -I OUTPUT -d $ip -j REJECT 2>/dev/null || true"
    done
    printf '%s\n' "$ips" | rsh "cat > /tmp/.rd_blocked_${host}"
}

# unblock_host <hostname> : 撤销 block_host 装的 REJECT 规则（trap EXIT 用）
unblock_host() {
    local host="$1" ips ip
    ips="$(rsh "cat /tmp/.rd_blocked_${host} 2>/dev/null")"
    for ip in $ips; do
        rsh "iptables -D OUTPUT -d $ip -j REJECT 2>/dev/null || true"
    done
    rsh "rm -f /tmp/.rd_blocked_${host}"
}
